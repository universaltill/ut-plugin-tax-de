// Package datev builds a DATEV EXTF "Buchungsstapel" (booking batch) export
// from a range of completed sales, per ut-docs#41 ("DATEV/FiBu Buchungsstapel
// accounting-batch export"). Deliberately its own package with NO
// `//go:build wasip1` constraint (unlike src/main.go) so it can be unit
// tested directly with plain `go test ./...` on the host, without a wasm
// build — src/main.go (the WASI guest) is the only caller.
//
// Format grounded in a real, publicly available EXTF_Buchungsstapel.csv
// reference file (github.com/ledermann/datev/blob/master/examples/
// EXTF_Buchungsstapel.csv, fetched and byte-verified 2026-08-01) rather than
// reconstructed from memory: header row 2 (the 125-column booking-row
// header) is byte-identical to that reference, confirmed by direct diff.
// Header row 1 (31 fields: format version 700, category 21, Buchungsstapel
// format version 13, consultant/client numbers, fiscal-year/period dates,
// currency, then 9 trailing reserved fields) matches that same reference's
// field order and count — an independent review caught an earlier draft of
// this file undercounting the trailing reserved fields (27 instead of 31),
// since fixed and now covered by TestHeader1_FieldCount. NEEDS
// RECONFIRMATION against DATEV's current published "Formatbeschreibung:
// Buchungsstapel" before relying on this for a real filing — DATEV revises
// format versions over time and this was not cross-checked against
// developer.datev.de directly (that page 403'd when fetched), and the
// Soll/Haben booking convention (package doc below) is a documented
// assumption, not accountant-verified. Same honesty convention as this
// repo's fiskaly integration: grounded in real public reference material,
// not verified end-to-end against DATEV/an accountant.
package datev

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SaleRow mirrors universal-till's internal/data.ExportSaleRow JSON shape
// (the "sales" field of the export.requested.ask payload, ut-docs#221) —
// this plugin has no Go-level dependency on universal-till, so the shape is
// duplicated here deliberately, keyed to the wire contract, not the source
// type.
type SaleRow struct {
	ReceiptNo string    `json:"receipt_no"`
	CreatedAt string    `json:"created_at"`
	Total     int64     `json:"total"`
	TaxLines  []TaxLine `json:"tax_lines"`
	Payments  []Payment `json:"payments"`
}

// TaxLine is one tax band's net/tax totals within a sale (money.Money wire
// values — plain integer minor units).
type TaxLine struct {
	RateBP int64 `json:"rate_bp"`
	Net    int64 `json:"net"`
	Tax    int64 `json:"tax"`
}

// Payment is one payment method's total within a sale. Unused by Build
// today — DATEV booking rows below post the whole sale to one configured
// Kasse/Bank account regardless of payment method. Splitting Konto by
// payment method (e.g. separate Kasse vs. card-clearing accounts, a real
// pattern many shops use) is a natural follow-up, deferred — see the code
// review record for this change, not silently dropped.
type Payment struct {
	Method string `json:"method"`
	Amount int64  `json:"amount"`
}

// Settings is the merchant/accountant-configured chart-of-accounts mapping.
// Deliberately NO default real account numbers anywhere in this package —
// SKR03/SKR04 "standard" account numbers are common knowledge but this
// plugin has no way to know which chart of accounts (or custom numbering) a
// given merchant's accountant actually uses, and shipping a guessed default
// risks silently mis-booking a real business's real accounting records.
// Build fails loudly instead of guessing when a required field is empty.
type Settings struct {
	BeraterNr        string            // Beraternummer (DATEV consultant number)
	MandantNr        string            // Mandantennummer (DATEV client number)
	SachkontenLaenge string            // Sachkontenlänge, digits, e.g. "4" (SKR03/04-standard length — safe structural default, not a real account number)
	WJBeginn         string            // Wirtschaftsjahresbeginn, MMDD, e.g. "0101" — safe generic default (calendar year), overridable
	KontoKasse       string            // Konto debited for every booking row (the till's cash/bank collector account) — no default
	Erloeskonten     map[string]string // tax_rate_bp (as decimal string) -> Gegenkonto (revenue account) — no default, must cover every rate present in the sales being exported
	BuSchluessel     map[string]string // optional: tax_rate_bp -> BU-Schlüssel override; omitted/empty per rate means "Gegenkonto is already an Automatikkonto, no BU-Schlüssel needed" (the common SKR03/04 convention)
}

// Result is a generated export file, ready to base64-encode into the
// till's exportResponse.content_b64.
type Result struct {
	Filename string
	Content  []byte // Windows-1252 encoded, CRLF line endings, per DATEV's EXTF requirement
}

// header2Columns is the exact 125-column booking-row header, byte-verified
// against the real reference file described in the package doc comment —
// not retyped from memory.
const header2Columns = `Umsatz (ohne Soll/Haben-Kz);Soll/Haben-Kennzeichen;WKZ Umsatz;Kurs;Basisumsatz;WKZ Basisumsatz;Konto;Gegenkonto (ohne BU-Schlüssel);BU-Schlüssel;Belegdatum;Belegfeld 1;Belegfeld 2;Skonto;Buchungstext;Postensperre;Diverse Adressnummer;Geschäftspartnerbank;Sachverhalt;Zinssperre;Beleglink;Beleginfo – Art 1;Beleginfo – Inhalt 1;Beleginfo – Art 2;Beleginfo – Inhalt 2;Beleginfo – Art 3;Beleginfo – Inhalt 3;Beleginfo – Art 4;Beleginfo – Inhalt 4;Beleginfo – Art 5;Beleginfo – Inhalt 5;Beleginfo – Art 6;Beleginfo – Inhalt 6;Beleginfo – Art 7;Beleginfo – Inhalt 7;Beleginfo – Art 8;Beleginfo – Inhalt 8;KOST1 – Kostenstelle;KOST2 – Kostenstelle;Kost Menge;EU-Land u. USt-IdNr.;EU-Steuersatz;Abw. Versteuerungsart;Sachverhalt L+L;Funktionsergänzung L+L;BU 49 Hauptfunktionstyp;BU 49 Hauptfunktionsnummer;BU 49 Funktionsergänzung;Zusatzinformation – Art 1;Zusatzinformation – Inhalt 1;Zusatzinformation – Art 2;Zusatzinformation – Inhalt 2;Zusatzinformation – Art 3;Zusatzinformation – Inhalt 3;Zusatzinformation – Art 4;Zusatzinformation – Inhalt 4;Zusatzinformation – Art 5;Zusatzinformation – Inhalt 5;Zusatzinformation – Art 6;Zusatzinformation – Inhalt 6;Zusatzinformation – Art 7;Zusatzinformation – Inhalt 7;Zusatzinformation – Art 8;Zusatzinformation – Inhalt 8;Zusatzinformation – Art 9;Zusatzinformation – Inhalt 9;Zusatzinformation – Art 10;Zusatzinformation – Inhalt 10;Zusatzinformation – Art 11;Zusatzinformation – Inhalt 11;Zusatzinformation – Art 12;Zusatzinformation – Inhalt 12;Zusatzinformation – Art 13;Zusatzinformation – Inhalt 13;Zusatzinformation – Art 14;Zusatzinformation – Inhalt 14;Zusatzinformation – Art 15;Zusatzinformation – Inhalt 15;Zusatzinformation – Art 16;Zusatzinformation – Inhalt 16;Zusatzinformation – Art 17;Zusatzinformation – Inhalt 17;Zusatzinformation – Art 18;Zusatzinformation – Inhalt 18;Zusatzinformation – Art 19;Zusatzinformation – Inhalt 19;Zusatzinformation – Art 20;Zusatzinformation – Inhalt 20;Stück;Gewicht;Zahlweise;Forderungsart;Veranlagungsjahr;Zugeordnete Fälligkeit;Skontotyp;Auftragsnummer;Buchungstyp;USt-Schlüssel (Anzahlungen);EU-Mitgliedstaat (Anzahlungen);Sachverhalt L+L (Anzahlungen);EU-Steuersatz (Anzahlungen);Erlöskonto (Anzahlungen);Herkunft-Kz;Leerfeld;KOST-Datum;SEPA-Mandatsreferenz;Skontosperre;Gesellschaftername;Beteiligtennummer;Identifikationsnummer;Zeichnernummer;Postensperre bis;Bezeichnung;Kennzeichen;Festschreibung;Leistungsdatum;Datum Zuord.;Fälligkeit;Generalumkehr;Steuersatz;Land;Abrechnungsreferent;BVV-Position;EU-Mitgliedstaat u. UStID (Ursprung);EU-Steuersatz (Ursprung);Abw. Skontokonto`

const dataColumnCount = 125 // len(strings.Split(header2Columns, ";"))

// herkunftKz is the 2-character "origin" code identifying the exporting
// application (DATEV requires exactly 2 characters here) — an application
// identifier, not a business decision.
const herkunftKz = "UT"

// Build validates settings against the sales actually being exported (every
// distinct tax_rate_bp must have a configured Gegenkonto — Build refuses to
// guess one) and renders the EXTF file. from/to are "YYYY-MM-DD". now is the
// export's wall-clock time (injected, not time.Now(), so callers/tests are
// deterministic).
func Build(from, to string, sales []SaleRow, settings Settings, now time.Time) (*Result, error) {
	if strings.TrimSpace(settings.BeraterNr) == "" {
		return nil, fmt.Errorf("datev export: datev_berater_nr is not configured")
	}
	if strings.TrimSpace(settings.MandantNr) == "" {
		return nil, fmt.Errorf("datev export: datev_mandant_nr is not configured")
	}
	if strings.TrimSpace(settings.KontoKasse) == "" {
		return nil, fmt.Errorf("datev export: datev_konto_kasse is not configured")
	}
	if !isDigits(settings.KontoKasse) {
		return nil, fmt.Errorf("datev export: datev_konto_kasse must be digits only, got %q", settings.KontoKasse)
	}
	for rate, konto := range settings.Erloeskonten {
		if strings.TrimSpace(konto) != "" && !isDigits(konto) {
			return nil, fmt.Errorf("datev export: datev_erloeskonten[%q] must be digits only, got %q", rate, konto)
		}
	}
	sachkontenLaenge := strings.TrimSpace(settings.SachkontenLaenge)
	if sachkontenLaenge == "" {
		sachkontenLaenge = "4"
	}
	wjBeginn := strings.TrimSpace(settings.WJBeginn)
	if wjBeginn == "" {
		wjBeginn = "0101"
	}
	if len(wjBeginn) != 4 || !isDigits(wjBeginn) {
		return nil, fmt.Errorf("datev export: datev_wj_beginn must be MMDD (4 digits), got %q", wjBeginn)
	}
	if wjMonth, _ := strconv.Atoi(wjBeginn[:2]); wjMonth < 1 || wjMonth > 12 {
		return nil, fmt.Errorf("datev export: datev_wj_beginn month must be 01-12, got %q", wjBeginn)
	}
	if wjDay, _ := strconv.Atoi(wjBeginn[2:]); wjDay < 1 || wjDay > 31 {
		return nil, fmt.Errorf("datev export: datev_wj_beginn day must be 01-31, got %q", wjBeginn)
	}

	fromDate, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, fmt.Errorf("datev export: from must be YYYY-MM-DD: %w", err)
	}
	toDate, err := time.Parse("2006-01-02", to)
	if err != nil {
		return nil, fmt.Errorf("datev export: to must be YYYY-MM-DD: %w", err)
	}
	if len(sales) == 0 {
		// A header-only file would download as an apparently-successful
		// export with zero booking rows, silently masking a wrong date
		// range as easily as it would confirm a genuinely sale-free period
		// (reviewer-caught). Surface it as a message instead of a file.
		return nil, fmt.Errorf("datev export: no sales in %s to %s", from, to)
	}

	// Missing-Gegenkonto check up front, across ALL sales, before rendering
	// anything — a partial file that silently drops a tax band's revenue
	// would be worse than refusing outright.
	missing := map[string]bool{}
	for _, s := range sales {
		for _, tl := range s.TaxLines {
			key := strconv.FormatInt(tl.RateBP, 10)
			// TrimSpace, not just presence: {"700": ""} must be treated the
			// same as an absent key, or Build would silently emit a blank
			// Gegenkonto column instead of refusing (reviewer-caught).
			if strings.TrimSpace(settings.Erloeskonten[key]) == "" {
				missing[key] = true
			}
		}
	}
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for k := range missing {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("datev export: datev_erloeskonten has no Gegenkonto configured for tax rate(s) (basis points) %s — configure every rate that appears in this period before exporting", strings.Join(keys, ", "))
	}

	// A sale's tax_lines are built from sale_lines, independently of
	// sales.total (universal-till's SalesForExport, internal/data/
	// export_repo.go) — a sale-level discount is not currently pushed down
	// into sale_lines, so sum(tax_lines) can legitimately disagree with
	// Total for a discounted sale (reviewer-caught). Booking sum(tax_lines)
	// as Kasse would then debit more than the till actually took, an
	// unreconcilable batch. Refuse rather than book a number that doesn't
	// match the sale — same "refuse, don't guess" stance as the
	// missing-Gegenkonto check above.
	var unreconciled []string
	for _, s := range sales {
		var grossSum int64
		for _, tl := range s.TaxLines {
			grossSum += tl.Net + tl.Tax
		}
		if grossSum != s.Total {
			unreconciled = append(unreconciled, fmt.Sprintf("%s (tax lines sum %s, total %s)", s.ReceiptNo, formatAmount(grossSum), formatAmount(s.Total)))
		}
	}
	if len(unreconciled) > 0 {
		return nil, fmt.Errorf("datev export: %d sale(s) have a tax-line total that doesn't match the sale total (likely a sale-level discount not reflected per-line) — refusing rather than booking a mismatched amount: %s", len(unreconciled), strings.Join(unreconciled, "; "))
	}

	var sb strings.Builder
	sb.WriteString(buildHeaderRow1(settings, sachkontenLaenge, wjBeginn, fromDate, toDate, now))
	sb.WriteString("\r\n")
	sb.WriteString(header2Columns)
	sb.WriteString("\r\n")
	for _, s := range sales {
		belegdatum, err := bookingDate(s.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("datev export: sale %s: %w", s.ReceiptNo, err)
		}
		for _, tl := range s.TaxLines {
			sb.WriteString(buildDataRow(s, tl, belegdatum, settings))
			sb.WriteString("\r\n")
		}
	}

	return &Result{
		Filename: fmt.Sprintf("EXTF_Buchungsstapel_%s_%s.csv", from, to),
		Content:  encodeCP1252(sb.String()),
	}, nil
}

func buildHeaderRow1(settings Settings, sachkontenLaenge, wjBeginn string, fromDate, toDate, now time.Time) string {
	wjYear := fromDate.Year()
	// The fiscal year containing `from`: if from is before this year's
	// WJ-start month/day, the fiscal year actually began the PRIOR calendar
	// year (e.g. WJ-Beginn July 1, from = March -> fiscal year started last
	// July).
	wjMonth, _ := strconv.Atoi(wjBeginn[:2])
	wjDay, _ := strconv.Atoi(wjBeginn[2:])
	wjStart := time.Date(wjYear, time.Month(wjMonth), wjDay, 0, 0, 0, 0, time.UTC)
	if fromDate.Before(wjStart) {
		wjYear--
	}
	wjBeginnDate := fmt.Sprintf("%04d%02d%02d", wjYear, wjMonth, wjDay)

	fields := []string{
		q("EXTF"),
		"700", // format version — see package doc comment: matches the real reference file, NEEDS RECONFIRMATION against DATEV's current spec
		"21",  // Kategorie: Buchungsstapel
		q("Buchungsstapel"),
		"13", // Buchungsstapel format version — same caveat as field 2
		now.Format("20060102150405") + "000",
		"", // imported at
		q(herkunftKz),
		q("Universal Till"),
		"", // imported by
		settings.BeraterNr,
		settings.MandantNr,
		wjBeginnDate,
		sachkontenLaenge,
		fromDate.Format("20060102"),
		toDate.Format("20060102"),
		q(fmt.Sprintf("Kassenumsaetze %s - %s", fromDate.Format("2006-01-02"), toDate.Format("2006-01-02"))),
		"",  // Diktatkürzel
		"1", // Buchungstyp: 1 = Finanzbuchführung
		"",  // Rechnungslegungszweck
		"0", // Festschreibung: 0 = nein
		q("EUR"),
		"", "", "", "", "", "", "", "", "", // 9 trailing reserved fields — header row 1 is 31 fields total (reviewer-caught: this was 5, undercounting by 4 against the byte-verified reference)
	}
	return strings.Join(fields, ";")
}

func buildDataRow(s SaleRow, tl TaxLine, belegdatum string, settings Settings) string {
	gross := tl.Net + tl.Tax
	if gross < 0 {
		gross = -gross
	}
	rateKey := strconv.FormatInt(tl.RateBP, 10)
	buSchluessel := ""
	if settings.BuSchluessel != nil {
		buSchluessel = settings.BuSchluessel[rateKey]
	}
	// DATEV's documented max lengths: Buchungstext 60, Belegfeld 1 36 — the
	// two were swapped in an earlier draft (reviewer-caught): Buchungstext
	// was truncated to 36 while Belegfeld 1 (ReceiptNo) wasn't truncated at
	// all, the actual overflow risk.
	buchungstext := truncate(fmt.Sprintf("Kassenverkauf %s", s.ReceiptNo), 60)
	belegfeld1 := truncate(s.ReceiptNo, 36)

	fields := make([]string, dataColumnCount)
	fields[0] = formatAmount(gross)
	fields[1] = q("S") // Soll/Haben-Kennzeichen: Konto (Kasse) is debited — see package doc comment, NEEDS ACCOUNTANT VERIFICATION
	fields[6] = settings.KontoKasse
	fields[7] = settings.Erloeskonten[rateKey]
	if buSchluessel != "" {
		fields[8] = q(buSchluessel)
	}
	fields[9] = belegdatum
	fields[10] = q(belegfeld1)
	fields[13] = q(buchungstext)
	// every other column (2-6, 11-125 minus the above) stays "" — an empty
	// field in a semicolon-joined row is exactly right for EXTF's optional
	// columns, matching the reference file's own trailing empties.
	return strings.Join(fields, ";")
}

// bookingDate renders a sale's Belegdatum: DATEV's EXTF format uses ddMM
// (day+month only, no year — the year comes from the header's period), per
// the real reference file ("2102" = 21 Feb, "2202" = 22 Feb).
func bookingDate(createdAt string) (string, error) {
	// Production CompleteSale writes RFC3339, but universal-till's
	// `created_at` column defaults to SQLite's `datetime('now')` format
	// ("2006-01-02 15:04:05", no "T"/zone) for any row not written through
	// that path (seed/imported/legacy) — a third layout so those rows don't
	// abort the whole export (reviewer-caught).
	layouts := []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, createdAt); err == nil {
			return t.Format("0201"), nil
		}
	}
	return "", fmt.Errorf("unparseable created_at %q", createdAt)
}

// formatAmount renders minor units (cents) as DATEV's comma-decimal amount
// format (e.g. 2495 -> "24,95"), always unsigned (column 1 is "Umsatz ohne
// Soll/Haben-Kz" — sign is carried by the Soll/Haben-Kennzeichen column
// instead).
func formatAmount(minor int64) string {
	if minor < 0 {
		minor = -minor
	}
	return fmt.Sprintf("%d,%02d", minor/100, minor%100)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// q quotes a text field, doubling any embedded double-quote (standard CSV
// escaping) so a receipt number or booking text containing `"` can't break
// out of the quoted field for a conformant reader (reviewer-caught: the
// prior version interpolated raw, and a `;` or `"` inside receipt_no shifted
// every subsequent column for a naive/non-quote-aware reader).
func q(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// isDigits reports whether s is non-empty and every rune is an ASCII digit
// — used to validate account numbers (Konto/Gegenkonto), which are always
// numeric in a real chart of accounts and are emitted UNQUOTED, so a
// non-digit value (e.g. containing `;`) would otherwise shift columns with
// no quoting to protect against it.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// encodeCP1252 transcodes UTF-8 to Windows-1252, the encoding DATEV's EXTF
// format requires (never UTF-8 — confirmed by the reference file, which is
// NOT valid UTF-8). No external dependency: every rune this package ever
// emits is either ASCII, in the 0xA0-0xFF range (byte-identical between
// Windows-1252 and Unicode code points), or the one special case below.
func encodeCP1252(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r == 0x2013: // EN DASH ("–"), used in header2Columns
			out = append(out, 0x96)
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		default:
			out = append(out, '?') // not expected for any content this package generates
		}
	}
	return out
}
