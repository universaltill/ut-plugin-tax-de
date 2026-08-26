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
	// Gross is the Z-report's own day total (minor units) — per the host's
	// EODReport definition it includes voucher issuance (a 0% liability
	// inside the sale total) but never tips (held out of revenue). Carried
	// so a generated batch can be checked against the Z-report's headline
	// figure: sum(Erloes rows) + VouchersIssued == Gross (pinned by
	// TestBuildFromCloses_ReconcilesToZReportGross).
	Gross int64 `json:"gross"`
	// VouchersIssued is the day's voucher-issuance total (minor units) —
	// a liability (§3 Abs. 13 UStG), not revenue. It carries NO
	// payment-method breakdown (a day total), which is why the Konto the
	// proceeds landed on is a dedicated setting
	// (datev_konto_gutschein_zahlung), never inferred from
	// datev_konten_by_method — see the voucher validation below.
	VouchersIssued int64 `json:"vouchers_issued"`
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
// EODReport.VouchersIssued and the cash skim's destination carry no
// payment-method dimension, so the accounts they post to are named
// DIRECTLY by two dedicated settings — datev_konto_gutschein_zahlung (the
// Konto the voucher-sale proceeds landed on) and datev_konto_geldtransit
// (the skim's transit/safe Gegenkonto) — never inferred from how many
// entries datev_konten_by_method happens to have (an earlier draft did
// exactly that, and refused a whole multi-day batch for an ordinary
// cash+card shop the moment one gift voucher was sold). When a close needs
// one of these rows and its setting is unset, BuildFromCloses refuses with
// a clear error — the same refuse-don't-guess stance as every other
// unconfigured-account case in this package.
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
	if k := strings.TrimSpace(settings.KontoGutscheinZahlung); k != "" && !isDigits(k) {
		return nil, fmt.Errorf("datev export: datev_konto_gutschein_zahlung must be digits only, got %q", settings.KontoGutscheinZahlung)
	}
	if k := strings.TrimSpace(settings.KontoGeldtransit); k != "" && !isDigits(k) {
		return nil, fmt.Errorf("datev export: datev_konto_geldtransit must be digits only, got %q", settings.KontoGeldtransit)
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
			if strings.TrimSpace(settings.KontoGutscheinZahlung) == "" {
				// See the function doc comment: VouchersIssued is a day
				// total with no payment-method dimension, so the Konto the
				// proceeds landed on must be named directly — never
				// inferred from datev_konten_by_method's entries.
				addProblem("%s has vouchers issued but datev_konto_gutschein_zahlung is not configured — set it to the Konto the voucher-sale money landed on (the day total carries no payment-method breakdown, so it cannot be inferred)", closeName)
			}
		}
		if c.Report.CashReconciliation != nil && c.Report.CashReconciliation.Skim != 0 {
			if c.Report.CashReconciliation.Skim > 0 {
				// A skim is stored negative (cash removed from the drawer);
				// the shifts API refuses to record a positive one, so a
				// positive value here means corrupt or hand-edited archive
				// data — refuse like the negative-tip/negative-cell checks,
				// never book a credit row from a value with the wrong sign.
				addProblem("%s: positive cash skim (%d) — a skim is stored negative (cash removed from the drawer); refusing rather than misbooking it", closeName, c.Report.CashReconciliation.Skim)
			}
			if _, ok := kontoFor("cash"); !ok {
				addProblem("%s has a cash skim but datev_konten_by_method has no \"cash\" Konto configured", closeName)
			}
			if strings.TrimSpace(settings.KontoGeldtransit) == "" {
				// Same shape as vouchers, on the credit side: the skim's
				// destination (transit/safe) account is named directly by
				// its own setting, never inferred from which non-cash
				// methods happen to be configured.
				addProblem("%s has a cash skim but datev_konto_geldtransit is not configured — set it to the transit/safe account the skimmed cash moves into", closeName)
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
	renderedRows := 0
	for _, c := range closes {
		day, _ := time.Parse("2006-01-02", c.Report.Day)
		belegdatum := day.Format("0201")
		belegfeld1 := truncate(strconv.FormatInt(c.ZNumber, 10), 36)

		// One row per (payment method x VAT rate) cell, gross, in the
		// close's own cross-tab order. A zero-Gross cell is skipped, not
		// booked: split-tender apportionment can floor a small tender
		// against a small band to exactly 0 (internal/pages,
		// apportionAmount's own doc comment notes the floor "can shift a
		// minor unit between that sale's methods"), and a same-day
		// sale+return can net a cell to exactly 0 too (negative cells are
		// already refused above; exact-zero is not an error, just nothing
		// to book). DATEV requires a positive Umsatz per row, and this
		// also makes the renderedRows==0 refusal below mean what it says.
		for _, cell := range c.Report.MethodTaxBands {
			if cell.Gross == 0 {
				continue
			}
			konto, _ := kontoFor(cell.Method)
			rateKey := strconv.Itoa(cell.RateBP)
			buSchluessel := ""
			if settings.BuSchluessel != nil {
				buSchluessel = settings.BuSchluessel[rateKey]
			}
			sb.WriteString(closeBookingRow(cell.Gross, "S", konto, strings.TrimSpace(settings.Erloeskonten[rateKey]), buSchluessel,
				belegdatum, belegfeld1, fmt.Sprintf("Erloese %s%% %s", rateText(cell.RateBP), strings.ToUpper(cell.Method))))
			sb.WriteString("\r\n")
			renderedRows++
		}
		// Voucher issuance: a 0% liability (Gegenkonto KontoGutschein), not
		// revenue. The Konto the proceeds landed on is the dedicated
		// datev_konto_gutschein_zahlung setting (validated non-empty above).
		// When that account is unambiguously one configured method's Konto,
		// the Buchungstext carries that method's name (the reference batch's
		// "Aufbuchungen 0% CARD"); a purely-labelling nicety — the booking
		// itself never depends on the lookup.
		if c.Report.VouchersIssued != 0 {
			konto := strings.TrimSpace(settings.KontoGutscheinZahlung)
			text := "Aufbuchungen 0%"
			if method, ok := uniqueMethodForKonto(settings.KonteByMethod, konto); ok {
				text += " " + strings.ToUpper(method)
			}
			sb.WriteString(closeBookingRow(c.Report.VouchersIssued, "S", konto, strings.TrimSpace(settings.KontoGutschein), "",
				belegdatum, belegfeld1, text))
			sb.WriteString("\r\n")
			renderedRows++
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
			renderedRows++
		}
		// Cash skim: the one credit (H) row — cash out of the drawer into
		// the transit/safe account named by datev_konto_geldtransit
		// (validated non-empty above), restoring the float. The debit side
		// stays the configured "cash" Konto. Skim is stored negative; the
		// Umsatz column is unsigned, sign rides on S/H.
		if c.Report.CashReconciliation != nil && c.Report.CashReconciliation.Skim != 0 {
			cashKonto, _ := kontoFor("cash")
			sb.WriteString(closeBookingRow(c.Report.CashReconciliation.Skim, "H", cashKonto, strings.TrimSpace(settings.KontoGeldtransit), "",
				belegdatum, belegfeld1, "Abschoepfung Kasse"))
			sb.WriteString("\r\n")
			renderedRows++
		}
	}
	if renderedRows == 0 {
		// Same reasoning as the len(closes)==0 refusal above, reached the
		// other way: every close in range was economically empty (a genuine
		// zero-trading day that still got closed), so the file would be
		// header-only — an apparently-successful export with zero booking
		// rows. Mirror Build's no-sales refusal rather than return it.
		return nil, fmt.Errorf("datev export: no postings for %s to %s — every close in range had zero trading activity", closes[0].Report.Day, closes[len(closes)-1].Report.Day)
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

// uniqueMethodForKonto returns the one payment method whose configured
// Konto equals konto, when exactly one does — used ONLY to label the
// voucher-issuance row's Buchungstext (e.g. "Aufbuchungen 0% CARD" when
// datev_konto_gutschein_zahlung is card's own Geldtransit account). With
// zero or several matching methods it reports false and the label stays
// method-less; the account booked never depends on this lookup.
func uniqueMethodForKonto(konteByMethod map[string]string, konto string) (string, bool) {
	var matches []string
	for method, k := range konteByMethod {
		if strings.TrimSpace(k) == konto {
			matches = append(matches, method)
		}
	}
	if len(matches) != 1 {
		return "", false
	}
	return matches[0], true
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
