// Day-close-grained DATEV Buchungsstapel building (ut-docs#1005): one
// posting set per ARCHIVED day-close, one row per (payment method x VAT
// rate) cell of the close's own cross-tab, plus the liability/transfer rows
// (voucher issuance, tips, cash skim) — keyed to the close's Z-number in
// Belegfeld 1. This is the grain a German Steuerberater actually books, as
// opposed to Build's one-row-per-(sale, tax-line) ledger grain (kept as-is
// in datev.go — see main.go's routing comment for how the two coexist).
//
// The input is the ALREADY-ARCHIVED, immutable Z-report (universal-till's
// report_archive content_json, json-unmarshaled host-side), never a fresh
// recomputation — so a generated batch can never disagree with the Z-report
// the merchant already has in hand, which is what makes the ticket's
// "generated from the day-close cross-tab, not re-queried independently"
// acceptance criterion literally true.
package datev

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EODCloseExport mirrors universal-till's internal/data.EODCloseExport JSON
// shape (the "eod_closes" field of the export.requested.ask payload,
// ut-docs#1005) — same deliberate wire-shape duplication as SaleRow (no
// Go-level dependency on universal-till; keyed to the wire contract, not
// the source type).
type EODCloseExport struct {
	ZNumber int64              `json:"z_number"`
	Report  EODReportForExport `json:"report"`
}

// EODReportForExport carries only the EODReport fields this package reads —
// not the full report (SalesCount, Departments, ... stay host-side only as
// far as this package is concerned; unmarshaling simply drops them).
type EODReportForExport struct {
	// Day is the close's business day ("YYYY-MM-DD") — each row's Belegdatum.
	Day string `json:"day"`
	// MethodTaxBands is the payment-method x VAT-rate cross-tab
	// (universal-till ut-docs#1004) the Erloes rows are generated from.
	MethodTaxBands []MethodTaxBand `json:"method_tax_bands"`
	// Tips by payment method, held out of revenue (ut-docs#1007) — posted
	// to the tip liability account, never a revenue account.
	Tips []EODTip `json:"tips"`
	// VouchersIssued is the day's voucher-issuance total (minor units) —
	// a liability (§3 Abs. 13 UStG), not revenue. NOTE: it carries NO
	// payment-method breakdown (a day total), which is why BuildFromCloses
	// refuses when more than one payment method is configured — see the
	// voucher validation below.
	VouchersIssuedCount int   `json:"vouchers_issued_count"`
	VouchersIssued      int64 `json:"vouchers_issued"`
	// CashReconciliation carries the day's cash skim (stored negative —
	// cash removed from the drawer). nil when no shift was closed that day.
	CashReconciliation *CashReconciliation `json:"cash_reconciliation"`
}

// MethodTaxBand mirrors universal-till's data.MethodTaxBand wire shape.
type MethodTaxBand struct {
	Method string `json:"method"`
	RateBP int    `json:"rate_bp"`
	Net    int64  `json:"net"`
	Tax    int64  `json:"tax"`
	Gross  int64  `json:"gross"`
}

// EODTip mirrors universal-till's data.EODTip wire shape.
type EODTip struct {
	Method string `json:"method"`
	Count  int    `json:"count"`
	Amount int64  `json:"amount"`
}

// CashReconciliation mirrors only the field this package reads of
// universal-till's data.CashReconciliation wire shape.
type CashReconciliation struct {
	Skim int64 `json:"skim"` // negative = cash removed from the drawer
}

// BuildFromCloses validates settings against every close being exported —
// refusing, never guessing, exactly like Build — and renders one DATEV EXTF
// posting set per archived close, in the order given (the host's
// ArchivedReportsInRange already orders by period). now is injected for
// deterministic tests, same as Build.
//
// Known limitation, deliberate: EODReport.VouchersIssued and the cash
// skim's destination carry no payment-method dimension, so when more than
// one candidate account is configured (any second method for vouchers; a
// second non-cash method for the skim's Gegenkonto) the account to post is
// genuinely ambiguous and BuildFromCloses refuses with a clear error rather
// than silently picking one — the same refuse-don't-guess stance as every
// other unconfigured-account case in this package. Lifting it needs a
// method-dimensioned voucher/skim breakdown on the Z-report itself
// (host-side), not a guess here.
func BuildFromCloses(closes []EODCloseExport, settings Settings, now time.Time) (*Result, error) {
	sachkontenLaenge, wjBeginn, err := commonSettingsChecks(settings)
	if err != nil {
		return nil, err
	}
	for method, konto := range settings.KonteByMethod {
		if strings.TrimSpace(konto) != "" && !isDigits(konto) {
			return nil, fmt.Errorf("datev export: datev_konten_by_method[%q] must be digits only, got %q", method, konto)
		}
	}
	if k := strings.TrimSpace(settings.KontoGutschein); k != "" && !isDigits(k) {
		return nil, fmt.Errorf("datev export: datev_konto_gutschein must be digits only, got %q", settings.KontoGutschein)
	}
	if k := strings.TrimSpace(settings.KontoTrinkgeld); k != "" && !isDigits(k) {
		return nil, fmt.Errorf("datev export: datev_konto_trinkgeld must be digits only, got %q", settings.KontoTrinkgeld)
	}
	if len(closes) == 0 {
		// Same reasoning as Build's no-sales refusal: a header-only file
		// would look like a successful export. The likely causes differ
		// though — here it usually means no day was CLOSED in the range
		// (the batch is generated from archived Z-reports only).
		return nil, fmt.Errorf("datev export: no archived day-closes in the requested period — the DATEV batch is generated from archived day-close (Z-report) records, so close the day first")
	}

	kontoFor := func(method string) (string, bool) {
		k := strings.TrimSpace(settings.KonteByMethod[method])
		return k, k != ""
	}
	// Configured methods (non-blank Konto), for the voucher/skim ambiguity
	// rules — sorted so error messages and the single-method pick are
	// deterministic regardless of map iteration order.
	var configuredMethods []string
	for method, konto := range settings.KonteByMethod {
		if strings.TrimSpace(konto) != "" {
			configuredMethods = append(configuredMethods, method)
		}
	}
	sort.Strings(configuredMethods)
	var nonCashMethods []string
	for _, m := range configuredMethods {
		if m != "cash" {
			nonCashMethods = append(nonCashMethods, m)
		}
	}

	// Validate EVERY close up front, before rendering anything — a partial
	// file silently missing a close (or a row within one) would be worse
	// than refusing outright; same all-up-front discipline Build uses for
	// its missing-Gegenkonto check.
	missingMethods := map[string]bool{}
	missingRates := map[string]bool{}
	var problems []string
	addProblem := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	for _, c := range closes {
		closeName := fmt.Sprintf("close Z%d (%s)", c.ZNumber, c.Report.Day)
		if c.ZNumber < 1 {
			// Belegfeld 1 is the document key tying the batch to the
			// day-close in the accountant's system; a pre-migration legacy
			// archive row has no real Z-number (0), and filing under a fake
			// key would mis-key the batch — refuse, don't invent one.
			addProblem("close for %s has no Z-number (pre-numbering legacy archive row) — cannot key its Belegfeld 1; narrow the export range to numbered closes", c.Report.Day)
		}
		if _, derr := time.Parse("2006-01-02", c.Report.Day); derr != nil {
			addProblem("%s: unparseable day %q", closeName, c.Report.Day)
		}
		for _, cell := range c.Report.MethodTaxBands {
			if _, ok := kontoFor(cell.Method); !ok {
				missingMethods[cell.Method] = true
			}
			if strings.TrimSpace(settings.Erloeskonten[strconv.Itoa(cell.RateBP)]) == "" {
				missingRates[strconv.Itoa(cell.RateBP)] = true
			}
			if cell.Gross < 0 {
				// A negative cell (a refund-dominated method/rate for the
				// day) has no accountant-verified representation here yet
				// (Generalumkehr vs. flipped S/H is exactly the kind of
				// convention this package refuses to guess) — refuse rather
				// than book an inflated "S" row from its absolute value.
				addProblem("%s: cross-tab cell %s/%sbp has a negative gross (%d) — no verified representation for a negative day cell; refusing rather than misbooking it", closeName, cell.Method, strconv.Itoa(cell.RateBP), cell.Gross)
			}
		}
		for _, tip := range c.Report.Tips {
			if tip.Amount == 0 {
				continue
			}
			if tip.Amount < 0 {
				addProblem("%s: negative tip total for %q (%d) — refusing rather than misbooking it", closeName, tip.Method, tip.Amount)
			}
			if _, ok := kontoFor(tip.Method); !ok {
				missingMethods[tip.Method] = true
			}
			if strings.TrimSpace(settings.KontoTrinkgeld) == "" {
				addProblem("%s has tips to post but datev_konto_trinkgeld is not configured", closeName)
			}
		}
		if c.Report.VouchersIssued != 0 {
			if c.Report.VouchersIssued < 0 {
				addProblem("%s: negative vouchers-issued total (%d) — refusing rather than misbooking it", closeName, c.Report.VouchersIssued)
			}
			if strings.TrimSpace(settings.KontoGutschein) == "" {
				addProblem("%s has vouchers issued but datev_konto_gutschein is not configured", closeName)
			}
			if len(configuredMethods) != 1 {
				// See the function doc comment: VouchersIssued is a day
				// total with no payment-method dimension, so with more than
				// one configured method the Konto to debit is ambiguous.
				addProblem("%s has vouchers issued, but the day total carries no payment-method breakdown and %d payment methods are configured (%s) — which Konto to debit is ambiguous; refusing rather than guessing (configure exactly one method, or export once the Z-report breaks vouchers down by method)", closeName, len(configuredMethods), strings.Join(configuredMethods, ", "))
			}
		}
		if c.Report.CashReconciliation != nil && c.Report.CashReconciliation.Skim != 0 {
			if _, ok := kontoFor("cash"); !ok {
				addProblem("%s has a cash skim but datev_konten_by_method has no \"cash\" Konto configured", closeName)
			}
			if len(nonCashMethods) != 1 {
				// Same ambiguity shape as vouchers, on the credit side: the
				// skim's destination (transit) account is the single
				// configured non-cash method's Konto; with zero or several
				// there is nothing unambiguous to post.
				addProblem("%s has a cash skim, but its destination account is ambiguous: expected exactly one configured non-cash method as the transit account, found %d (%s) — refusing rather than guessing", closeName, len(nonCashMethods), strings.Join(nonCashMethods, ", "))
			}
		}
	}
	if len(missingMethods) > 0 {
		keys := make([]string, 0, len(missingMethods))
		for k := range missingMethods {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("datev export: datev_konten_by_method has no Konto configured for payment method(s) %s — configure every method that appears in this period's closes before exporting", strings.Join(keys, ", "))
	}
	if len(missingRates) > 0 {
		keys := make([]string, 0, len(missingRates))
		for k := range missingRates {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("datev export: datev_erloeskonten has no Gegenkonto configured for tax rate(s) (basis points) %s — configure every rate that appears in this period's closes before exporting", strings.Join(keys, ", "))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("datev export: %s", strings.Join(problems, "; "))
	}

	// The batch's header period spans the closes' own business days —
	// closes arrive ordered by period (ArchivedReportsInRange), so first
	// and last bound the range.
	fromDate, _ := time.Parse("2006-01-02", closes[0].Report.Day)
	toDate, _ := time.Parse("2006-01-02", closes[len(closes)-1].Report.Day)

	var sb strings.Builder
	sb.WriteString(buildHeaderRow1(settings, sachkontenLaenge, wjBeginn, "1", fromDate, toDate, now))
	sb.WriteString("\r\n")
	sb.WriteString(header2Columns)
	sb.WriteString("\r\n")
	for _, c := range closes {
		day, _ := time.Parse("2006-01-02", c.Report.Day)
		belegdatum := day.Format("0201")
		belegfeld1 := truncate(strconv.FormatInt(c.ZNumber, 10), 36)

		// One row per (payment method x VAT rate) cell, gross, in the
		// close's own cross-tab order.
		for _, cell := range c.Report.MethodTaxBands {
			konto, _ := kontoFor(cell.Method)
			rateKey := strconv.Itoa(cell.RateBP)
			buSchluessel := ""
			if settings.BuSchluessel != nil {
				buSchluessel = settings.BuSchluessel[rateKey]
			}
			sb.WriteString(closeBookingRow(cell.Gross, "S", konto, settings.Erloeskonten[rateKey], buSchluessel,
				belegdatum, belegfeld1, fmt.Sprintf("Erloese %s%% %s", rateText(cell.RateBP), strings.ToUpper(cell.Method))))
			sb.WriteString("\r\n")
		}
		// Voucher issuance: a 0% liability (Gegenkonto KontoGutschein), not
		// revenue. The single-configured-method rule above already resolved
		// which Konto the money landed on.
		if c.Report.VouchersIssued != 0 {
			konto, _ := kontoFor(configuredMethods[0])
			sb.WriteString(closeBookingRow(c.Report.VouchersIssued, "S", konto, strings.TrimSpace(settings.KontoGutschein), "",
				belegdatum, belegfeld1, fmt.Sprintf("Aufbuchungen 0%% %s", strings.ToUpper(configuredMethods[0]))))
			sb.WriteString("\r\n")
		}
		// Tips: liability (Gegenkonto KontoTrinkgeld), never revenue.
		for _, tip := range c.Report.Tips {
			if tip.Amount == 0 {
				continue
			}
			konto, _ := kontoFor(tip.Method)
			sb.WriteString(closeBookingRow(tip.Amount, "S", konto, strings.TrimSpace(settings.KontoTrinkgeld), "",
				belegdatum, belegfeld1, fmt.Sprintf("Trinkgeld %s", strings.ToUpper(tip.Method))))
			sb.WriteString("\r\n")
		}
		// Cash skim: the one credit (H) row — cash out of the drawer into
		// the (single) transit account, restoring the float. Skim is stored
		// negative; the Umsatz column is unsigned, sign rides on S/H.
		if c.Report.CashReconciliation != nil && c.Report.CashReconciliation.Skim != 0 {
			cashKonto, _ := kontoFor("cash")
			transitKonto, _ := kontoFor(nonCashMethods[0])
			sb.WriteString(closeBookingRow(c.Report.CashReconciliation.Skim, "H", cashKonto, transitKonto, "",
				belegdatum, belegfeld1, "Abschoepfung Kasse"))
			sb.WriteString("\r\n")
		}
	}

	return &Result{
		Filename: fmt.Sprintf("EXTF_Buchungsstapel_%s_%s.csv", closes[0].Report.Day, closes[len(closes)-1].Report.Day),
		Content:  encodeCP1252(sb.String()),
	}, nil
}

// closeBookingRow renders one day-close booking row. Same 125-column layout
// and quoting rules as Build's buildDataRow, with two deliberate
// differences: Belegfeld 1 is the close's Z-number (the document key tying
// the batch to the day-close) rather than a receipt number, and column 114
// (Festschreibung) is "1" — the batch is generated from an already-archived,
// immutable close (ut-docs#1005's "Festschreibung=1 on every row" rule).
func closeBookingRow(amountMinor int64, sollHaben, konto, gegenkonto, buSchluessel, belegdatum, belegfeld1, buchungstext string) string {
	fields := make([]string, dataColumnCount)
	fields[0] = formatAmount(amountMinor) // always unsigned; sign is carried by S/H
	fields[1] = q(sollHaben)
	fields[6] = konto
	fields[7] = gegenkonto
	if buSchluessel != "" {
		fields[8] = q(buSchluessel)
	}
	fields[9] = belegdatum
	fields[10] = q(belegfeld1)
	fields[13] = q(truncate(buchungstext, 60))
	fields[113] = "1" // Festschreibung
	return strings.Join(fields, ";")
}

// rateText renders a basis-point VAT rate as the reference table's percent
// text: whole percents plain ("7", "19"), fractional ones comma-decimal
// with no trailing zero ("19,5"). Integer math only — no float rounding.
func rateText(bp int) string {
	whole, frac := bp/100, bp%100
	switch {
	case frac == 0:
		return strconv.Itoa(whole)
	case frac%10 == 0:
		return fmt.Sprintf("%d,%d", whole, frac/10)
	default:
		return fmt.Sprintf("%d,%02d", whole, frac)
	}
}
