package datev

import (
	"strings"
	"testing"
	"time"
)

func validSettings() Settings {
	return Settings{
		BeraterNr:        "1001",
		MandantNr:        "456",
		SachkontenLaenge: "4",
		WJBeginn:         "0101",
		KontoKasse:       "1000",
		Erloeskonten: map[string]string{
			"1900": "8400",
			"700":  "8300",
		},
	}
}

func twoSales() []SaleRow {
	return []SaleRow{
		{
			ReceiptNo: "R-0001",
			CreatedAt: "2026-02-21T10:15:00Z",
			Total:     2495,
			TaxLines: []TaxLine{
				{RateBP: 1900, Net: 2097, Tax: 398},
			},
		},
		{
			ReceiptNo: "R-0002",
			CreatedAt: "2026-02-22T09:00:00Z",
			Total:     1000,
			TaxLines: []TaxLine{
				{RateBP: 700, Net: 935, Tax: 65},
			},
		},
	}
}

// oneSale19 is a single-sale, single-19%-band fixture for tests that don't
// care about the two-sale/two-rate shape — Total is deliberately kept
// reconciled with TaxLines (Net+Tax), since Build now refuses mismatches.
func oneSale19(receiptNo, createdAt string) []SaleRow {
	return []SaleRow{{
		ReceiptNo: receiptNo,
		CreatedAt: createdAt,
		Total:     119,
		TaxLines:  []TaxLine{{RateBP: 1900, Net: 100, Tax: 19}},
	}}
}

func TestBuild_MissingBeraterNr(t *testing.T) {
	s := validSettings()
	s.BeraterNr = ""
	if _, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now()); err == nil {
		t.Fatal("expected error for missing datev_berater_nr, got nil")
	}
}

func TestBuild_MissingMandantNr(t *testing.T) {
	s := validSettings()
	s.MandantNr = ""
	if _, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now()); err == nil {
		t.Fatal("expected error for missing datev_mandant_nr, got nil")
	}
}

func TestBuild_MissingKontoKasse(t *testing.T) {
	s := validSettings()
	s.KontoKasse = ""
	if _, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now()); err == nil {
		t.Fatal("expected error for missing datev_konto_kasse, got nil")
	}
}

func TestBuild_KontoKasseNotDigits(t *testing.T) {
	s := validSettings()
	s.KontoKasse = "1000;DROP"
	if _, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now()); err == nil {
		t.Fatal("expected error for non-digit datev_konto_kasse, got nil")
	}
}

func TestBuild_ErloeskontoNotDigits(t *testing.T) {
	s := validSettings()
	s.Erloeskonten["1900"] = `8400"`
	if _, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now()); err == nil {
		t.Fatal("expected error for non-digit Gegenkonto, got nil")
	}
}

func TestBuild_MissingErloeskontoForRate_RefusesRatherThanGuess(t *testing.T) {
	s := validSettings()
	delete(s.Erloeskonten, "700") // sale 2 uses 700bp
	_, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now())
	if err == nil {
		t.Fatal("expected error for unconfigured tax rate, got nil")
	}
	if !strings.Contains(err.Error(), "700") {
		t.Fatalf("error should name the missing rate (700), got: %v", err)
	}
}

// TestBuild_EmptyStringErloeskonto_TreatedAsMissing guards against a
// configured-but-blank Gegenkonto silently producing a blank account column
// instead of refusing (independent review caught this: presence-only
// checking let {"700": ""} through).
func TestBuild_EmptyStringErloeskonto_TreatedAsMissing(t *testing.T) {
	s := validSettings()
	s.Erloeskonten["700"] = ""
	_, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now())
	if err == nil {
		t.Fatal("expected error for blank Gegenkonto, got nil")
	}
	if !strings.Contains(err.Error(), "700") {
		t.Fatalf("error should name the blank rate (700), got: %v", err)
	}
}

// TestBuild_TaxLinesDontReconcileWithTotal_Refuses guards the case a
// sale-level discount isn't reflected in its tax_lines (universal-till's
// SalesForExport builds tax_lines from sale_lines independently of
// sales.total) — Build must refuse rather than book a mismatched Kasse
// debit (independent review finding).
func TestBuild_TaxLinesDontReconcileWithTotal_Refuses(t *testing.T) {
	sales := []SaleRow{{
		ReceiptNo: "R-3000",
		CreatedAt: "2026-02-10T10:00:00Z",
		Total:     900, // a discount brought the sale to 9.00, but tax_lines below still sum to 11.90
		TaxLines:  []TaxLine{{RateBP: 1900, Net: 1000, Tax: 190}},
	}}
	_, err := Build("2026-02-01", "2026-02-28", sales, validSettings(), time.Now())
	if err == nil {
		t.Fatal("expected error for mismatched tax-line/total sum, got nil")
	}
	if !strings.Contains(err.Error(), "R-3000") {
		t.Fatalf("error should name the sale (R-3000), got: %v", err)
	}
}

func TestBuild_NoSalesInPeriod_Refuses(t *testing.T) {
	_, err := Build("2026-02-01", "2026-02-28", nil, validSettings(), time.Now())
	if err == nil {
		t.Fatal("expected error/message for an empty period, got nil (a silent header-only file would look like a successful export)")
	}
}

func TestBuild_HappyPath_StructureAndEncoding(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	res, err := Build("2026-02-01", "2026-02-28", twoSales(), validSettings(), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Filename != "EXTF_Buchungsstapel_2026-02-01_2026-02-28.csv" {
		t.Fatalf("unexpected filename: %s", res.Filename)
	}

	// Decode back from Windows-1252 to verify round-trip correctness of the
	// umlaut/en-dash encoding (this is NOT valid UTF-8 by design, so decode
	// manually rather than treating res.Content as a Go string directly).
	text := decodeCP1252(res.Content)
	lines := strings.Split(text, "\r\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) != 4 { // header1 + header2 + 2 data rows
		t.Fatalf("expected 4 lines, got %d: %q", len(lines), lines)
	}

	header1Fields := strings.Split(lines[0], ";")
	if len(header1Fields) != 31 {
		t.Fatalf("expected 31 header-row-1 fields, got %d", len(header1Fields))
	}
	if header1Fields[10] != "1001" {
		t.Fatalf("Beraternummer: expected 1001, got %q", header1Fields[10])
	}
	if header1Fields[11] != "456" {
		t.Fatalf("Mandantennummer: expected 456, got %q", header1Fields[11])
	}
	if header1Fields[12] != "20260101" {
		t.Fatalf("WJ-Beginn: expected 20260101, got %q", header1Fields[12])
	}
	if header1Fields[14] != "20260201" || header1Fields[15] != "20260228" {
		t.Fatalf("booking period: expected 20260201/20260228, got %q/%q", header1Fields[14], header1Fields[15])
	}

	header2Fields := strings.Split(lines[1], ";")
	if len(header2Fields) != dataColumnCount {
		t.Fatalf("expected %d header-row-2 columns, got %d", dataColumnCount, len(header2Fields))
	}
	if header2Fields[7] != "Gegenkonto (ohne BU-Schlüssel)" {
		t.Fatalf("umlaut round-trip failed for column 8: got %q", header2Fields[7])
	}
	if header2Fields[20] != "Beleginfo – Art 1" {
		t.Fatalf("en-dash round-trip failed for column 21: got %q", header2Fields[20])
	}

	row1 := strings.Split(lines[2], ";")
	if len(row1) != dataColumnCount {
		t.Fatalf("expected %d data columns in row 1, got %d", dataColumnCount, len(row1))
	}
	if row1[0] != "24,95" {
		t.Fatalf("Umsatz: expected 24,95, got %q", row1[0])
	}
	if row1[1] != `"S"` {
		t.Fatalf("Soll/Haben-Kennzeichen: expected \"S\", got %q", row1[1])
	}
	if row1[6] != "1000" {
		t.Fatalf("Konto: expected 1000, got %q", row1[6])
	}
	if row1[7] != "8400" {
		t.Fatalf("Gegenkonto: expected 8400 (19%% band), got %q", row1[7])
	}
	if row1[9] != "2102" {
		t.Fatalf("Belegdatum: expected 2102 (21 Feb, ddMM), got %q", row1[9])
	}
	if row1[10] != `"R-0001"` {
		t.Fatalf("Belegfeld 1: expected \"R-0001\", got %q", row1[10])
	}

	row2 := strings.Split(lines[3], ";")
	if row2[7] != "8300" {
		t.Fatalf("Gegenkonto: expected 8300 (7%% band), got %q", row2[7])
	}
}

func TestHeader1_FieldCount(t *testing.T) {
	res, err := Build("2026-02-01", "2026-02-28", twoSales(), validSettings(), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	header1 := strings.Split(strings.Split(decodeCP1252(res.Content), "\r\n")[0], ";")
	if len(header1) != 31 {
		t.Fatalf("header row 1 has %d fields, want 31 (byte-verified reference file)", len(header1))
	}
}

func TestBuild_FiscalYearBoundary_StartsPriorCalendarYear(t *testing.T) {
	s := validSettings()
	s.WJBeginn = "0701" // fiscal year starts July 1
	sales := oneSale19("R-1000", "2026-03-15T10:00:00Z")
	res, err := Build("2026-03-01", "2026-03-31", sales, s, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := decodeCP1252(res.Content)
	header1 := strings.Split(strings.Split(text, "\r\n")[0], ";")
	// from=March 2026 is before this calendar year's July 1, so the fiscal
	// year containing it started July 1 2025, not 2026.
	if header1[12] != "20250701" {
		t.Fatalf("expected fiscal year to have started 2025-07-01, got %q", header1[12])
	}
}

// TestBuild_FiscalYearBoundary_ExactStartDate pins the boundary condition
// itself (from == the WJ start date), not just the "well before" case above
// — independent review found the original boundary test didn't actually
// exercise the off-by-one edge (a `Before` vs `!After` mutation left the
// suite green).
func TestBuild_FiscalYearBoundary_ExactStartDate(t *testing.T) {
	s := validSettings()
	s.WJBeginn = "0701"
	sales := oneSale19("R-1001", "2026-07-01T00:00:00Z")
	res, err := Build("2026-07-01", "2026-07-31", sales, s, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	header1 := strings.Split(strings.Split(decodeCP1252(res.Content), "\r\n")[0], ";")
	// from == the WJ start date itself: the fiscal year beginning on that
	// exact date is THIS year (2026), not the prior one.
	if header1[12] != "20260701" {
		t.Fatalf("expected fiscal year to start exactly on 2026-07-01, got %q", header1[12])
	}
}

func TestBuild_WJBeginnNotDigits(t *testing.T) {
	s := validSettings()
	s.WJBeginn = "abcd"
	if _, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now()); err == nil {
		t.Fatal("expected error for non-digit datev_wj_beginn, got nil")
	}
}

func TestBuild_WJBeginnMonthOutOfRange(t *testing.T) {
	s := validSettings()
	s.WJBeginn = "1301" // month 13
	if _, err := Build("2026-02-01", "2026-02-28", twoSales(), s, time.Now()); err == nil {
		t.Fatal("expected error for out-of-range month in datev_wj_beginn, got nil")
	}
}

func TestBuild_BuSchluesselOmittedWhenNotConfigured(t *testing.T) {
	s := validSettings()
	sales := oneSale19("R-2000", "2026-02-10T10:00:00Z")
	res, err := Build("2026-02-01", "2026-02-28", sales, s, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	row := strings.Split(strings.Split(decodeCP1252(res.Content), "\r\n")[2], ";")
	if row[8] != "" {
		t.Fatalf("expected empty BU-Schlüssel when unconfigured, got %q", row[8])
	}
}

func TestBuild_BuSchluesselUsedWhenConfigured(t *testing.T) {
	s := validSettings()
	s.BuSchluessel = map[string]string{"1900": "9"}
	sales := oneSale19("R-2001", "2026-02-10T10:00:00Z")
	res, err := Build("2026-02-01", "2026-02-28", sales, s, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	row := strings.Split(strings.Split(decodeCP1252(res.Content), "\r\n")[2], ";")
	if row[8] != `"9"` {
		t.Fatalf("expected BU-Schlüssel \"9\", got %q", row[8])
	}
}

// TestBuild_ReceiptNoWithQuote_EscapedNotBroken guards against a receipt
// number containing a literal `"` breaking the quoted field for a
// conformant reader (independent review finding).
func TestBuild_ReceiptNoWithQuote_EscapedNotBroken(t *testing.T) {
	sales := oneSale19(`R-"2002`, "2026-02-10T10:00:00Z")
	res, err := Build("2026-02-01", "2026-02-28", sales, validSettings(), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	row := strings.Split(strings.Split(decodeCP1252(res.Content), "\r\n")[2], ";")
	if len(row) != dataColumnCount {
		t.Fatalf("embedded quote shifted columns: expected %d, got %d", dataColumnCount, len(row))
	}
	if row[10] != `"R-""2002"` {
		t.Fatalf("expected doubled-quote CSV escaping, got %q", row[10])
	}
}

func TestBuild_ReceiptNoTruncatedTo36(t *testing.T) {
	long := strings.Repeat("R", 50)
	sales := oneSale19(long, "2026-02-10T10:00:00Z")
	res, err := Build("2026-02-01", "2026-02-28", sales, validSettings(), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	row := strings.Split(strings.Split(decodeCP1252(res.Content), "\r\n")[2], ";")
	got := strings.Trim(row[10], `"`)
	if len(got) != 36 {
		t.Fatalf("Belegfeld 1: expected truncation to 36 chars, got %d: %q", len(got), got)
	}
}

func TestBookingDate_AlternateLayouts(t *testing.T) {
	cases := map[string]string{
		"2026-02-21T10:15:00Z": "2102",
		"2026-02-21":           "2102",
		"2026-02-21 10:15:00":  "2102", // SQLite datetime('now') default column format
	}
	for in, want := range cases {
		got, err := bookingDate(in)
		if err != nil {
			t.Fatalf("bookingDate(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Errorf("bookingDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBookingDate_Unparseable(t *testing.T) {
	if _, err := bookingDate("not-a-date"); err == nil {
		t.Fatal("expected error for unparseable created_at, got nil")
	}
}

func TestFormatAmount(t *testing.T) {
	cases := map[int64]string{
		2495: "24,95",
		100:  "1,00",
		5:    "0,05",
		0:    "0,00",
		-250: "2,50", // Umsatz column is always unsigned per DATEV's "ohne Soll/Haben-Kz" convention
	}
	for minor, want := range cases {
		if got := formatAmount(minor); got != want {
			t.Errorf("formatAmount(%d) = %q, want %q", minor, got, want)
		}
	}
}

func TestHeader2Columns_ColumnCount(t *testing.T) {
	if got := len(strings.Split(header2Columns, ";")); got != dataColumnCount {
		t.Fatalf("header2Columns has %d columns, want %d", got, dataColumnCount)
	}
}

func TestIsDigits(t *testing.T) {
	cases := map[string]bool{
		"1000":      true,
		"":          false,
		"1000;DROP": false,
		`1000"`:     false,
		"10a0":      false,
	}
	for in, want := range cases {
		if got := isDigits(in); got != want {
			t.Errorf("isDigits(%q) = %v, want %v", in, got, want)
		}
	}
}

// decodeCP1252 is the test-only inverse of encodeCP1252, verifying the
// encoder round-trips (Go source files, and this test's string literals,
// are UTF-8 — so a correct decode must reproduce them exactly).
func decodeCP1252(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c == 0x96:
			sb.WriteRune(0x2013)
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}
