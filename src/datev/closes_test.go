package datev

import (
	"strings"
	"testing"
	"time"
)

// closesSettings is validSettings' BuildFromCloses counterpart: the
// method->Konto map replaces the single KontoKasse, plus the two liability
// Gegenkonten (voucher issuance, tips). Account numbers are the SKR03 ones
// from ut-docs#1005's reference table — test fixture data, not code
// defaults (the package still ships no default account numbers).
func closesSettings() Settings {
	return Settings{
		BeraterNr:        "1001",
		MandantNr:        "456",
		SachkontenLaenge: "4",
		WJBeginn:         "0101",
		KonteByMethod: map[string]string{
			"cash": "1000",
			"card": "1360",
		},
		KontoGutschein: "1796",
		KontoTrinkgeld: "1363",
		Erloeskonten: map[string]string{
			"1900": "8400",
			"700":  "8300",
		},
	}
}

// referenceDayClose is ut-docs#1005's reference day, MINUS its voucher row:
// EODReport.VouchersIssued carries no payment-method breakdown, and with
// more than one configured method BuildFromCloses refuses (rather than
// guesses) which Konto to debit for it — so the full 7-row reference day
// cannot be produced in one batch with both cash and card configured. The
// voucher row is reproduced exactly in
// TestBuildFromCloses_GoldenVoucherRow_SingleConfiguredMethod, where the
// single-configured-method rule resolves it unambiguously. See
// TestBuildFromCloses_VouchersAmbiguousWithMultipleMethods_Refuses for the
// refusal itself.
func referenceDayClose() EODCloseExport {
	return EODCloseExport{
		ZNumber: 17,
		Report: EODReportForExport{
			Day: "2026-08-21",
			MethodTaxBands: []MethodTaxBand{
				{Method: "card", RateBP: 700, Net: 65794, Tax: 4606, Gross: 70400},
				{Method: "cash", RateBP: 700, Net: 30542, Tax: 2138, Gross: 32680},
				{Method: "card", RateBP: 1900, Net: 8008, Tax: 1522, Gross: 9530},
				{Method: "cash", RateBP: 1900, Net: 7084, Tax: 1346, Gross: 8430},
			},
			Tips:               []EODTip{{Method: "card", Count: 1, Amount: 320}},
			CashReconciliation: &CashReconciliation{Skim: -41110},
		},
	}
}

// bookingTuples extracts each data row's meaningful columns — Betrag, S/H,
// Konto, Gegenkonto, Belegdatum, Belegfeld1, Buchungstext, Festschreibung —
// as one compact tuple per row, for golden comparison.
func bookingTuples(t *testing.T, res *Result) [][8]string {
	t.Helper()
	text := decodeCP1252(res.Content)
	lines := strings.Split(text, "\r\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 2 {
		t.Fatalf("expected at least the two header rows, got %d lines", len(lines))
	}
	var out [][8]string
	for _, line := range lines[2:] {
		cols := strings.Split(line, ";")
		if len(cols) != dataColumnCount {
			t.Fatalf("data row has %d columns, want %d: %q", len(cols), dataColumnCount, line)
		}
		out = append(out, [8]string{cols[0], cols[1], cols[6], cols[7], cols[9], cols[10], cols[13], cols[113]})
	}
	return out
}

// TestBuildFromCloses_GoldenReferenceDay reproduces ut-docs#1005's reference
// table row for row (except the voucher row — see referenceDayClose's doc
// comment): one row per (payment method x VAT rate) at gross, Konto by
// method, Gegenkonto by rate, tip liability row, and the cash-skim credit
// (H) row — all keyed to the close's own Z-number in Belegfeld 1, with
// Festschreibung=1 (the archived close is immutable, per the ticket's rule).
func TestBuildFromCloses_GoldenReferenceDay(t *testing.T) {
	now := time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC)
	res, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, closesSettings(), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Filename != "EXTF_Buchungsstapel_2026-08-21_2026-08-21.csv" {
		t.Fatalf("unexpected filename: %s", res.Filename)
	}

	want := [][8]string{
		{"704,00", `"S"`, "1360", "8300", "2108", `"17"`, `"Erloese 7% CARD"`, "1"},
		{"326,80", `"S"`, "1000", "8300", "2108", `"17"`, `"Erloese 7% CASH"`, "1"},
		{"95,30", `"S"`, "1360", "8400", "2108", `"17"`, `"Erloese 19% CARD"`, "1"},
		{"84,30", `"S"`, "1000", "8400", "2108", `"17"`, `"Erloese 19% CASH"`, "1"},
		{"3,20", `"S"`, "1360", "1363", "2108", `"17"`, `"Trinkgeld CARD"`, "1"},
		{"411,10", `"H"`, "1000", "1360", "2108", `"17"`, `"Abschoepfung Kasse"`, "1"},
	}
	got := bookingTuples(t, res)
	if len(got) != len(want) {
		t.Fatalf("expected %d booking rows, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d:\n got  %v\n want %v", i, got[i], want[i])
		}
	}
}

// TestBuildFromCloses_GoldenVoucherRow_SingleConfiguredMethod reproduces the
// one reference row TestBuildFromCloses_GoldenReferenceDay can't: with
// exactly ONE configured payment method the voucher-issuance liability row
// resolves unambiguously (Konto = that method's account, Gegenkonto =
// KontoGutschein) and matches the ticket's "15,00 S 1360 1796 Aufbuchungen
// 0% CARD" exactly.
func TestBuildFromCloses_GoldenVoucherRow_SingleConfiguredMethod(t *testing.T) {
	s := closesSettings()
	s.KonteByMethod = map[string]string{"card": "1360"}
	close := EODCloseExport{
		ZNumber: 18,
		Report: EODReportForExport{
			Day: "2026-08-22",
			MethodTaxBands: []MethodTaxBand{
				{Method: "card", RateBP: 1900, Net: 8008, Tax: 1522, Gross: 9530},
			},
			VouchersIssuedCount: 1,
			VouchersIssued:      1500,
		},
	}
	res, err := BuildFromCloses([]EODCloseExport{close}, s, time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := bookingTuples(t, res)
	want := [][8]string{
		{"95,30", `"S"`, "1360", "8400", "2208", `"18"`, `"Erloese 19% CARD"`, "1"},
		{"15,00", `"S"`, "1360", "1796", "2208", `"18"`, `"Aufbuchungen 0% CARD"`, "1"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d rows, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d:\n got  %v\n want %v", i, got[i], want[i])
		}
	}
}

// TestBuildFromCloses_ReconcilesToZReport is the ut-docs#1005 AC "row total
// equals the day-close gross" — the same identity spirit as ut-docs#1004's
// assertEODMethodTaxBandIdentities, asserted on the generated batch itself:
// the signed sum of the Erloes cell rows reconstructs the close's own
// MethodTaxBands total gross, and every other figure (tips, skim) is
// reproduced exactly, so the whole batch reconstructs the Z-report by
// construction.
func TestBuildFromCloses_ReconcilesToZReport(t *testing.T) {
	close := referenceDayClose()
	res, err := BuildFromCloses([]EODCloseExport{close}, closesSettings(), time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parseAmount := func(s string) int64 {
		parts := strings.SplitN(s, ",", 2)
		var euros, cents int64
		for _, r := range parts[0] {
			euros = euros*10 + int64(r-'0')
		}
		for _, r := range parts[1] {
			cents = cents*10 + int64(r-'0')
		}
		return euros*100 + cents
	}

	var erloesSum, tipSum, skimSum, signedTotal int64
	for _, row := range bookingTuples(t, res) {
		amount := parseAmount(row[0])
		signed := amount
		if row[1] == `"H"` {
			signed = -amount
		}
		signedTotal += signed
		switch {
		case strings.HasPrefix(row[6], `"Erloese `):
			erloesSum += signed
		case strings.HasPrefix(row[6], `"Trinkgeld `):
			tipSum += signed
		case row[6] == `"Abschoepfung Kasse"`:
			skimSum += signed
		}
	}

	var wantGross int64
	for _, cell := range close.Report.MethodTaxBands {
		wantGross += cell.Gross
	}
	if erloesSum != wantGross {
		t.Errorf("Erloes rows sum to %d, want the close's MethodTaxBands gross total %d", erloesSum, wantGross)
	}
	var wantTips int64
	for _, tip := range close.Report.Tips {
		wantTips += tip.Amount
	}
	if tipSum != wantTips {
		t.Errorf("tip rows sum to %d, want %d", tipSum, wantTips)
	}
	if skimSum != close.Report.CashReconciliation.Skim {
		t.Errorf("skim row signed sum is %d, want %d (the recorded negative skim)", skimSum, close.Report.CashReconciliation.Skim)
	}
	if want := wantGross + wantTips + close.Report.CashReconciliation.Skim; signedTotal != want {
		t.Errorf("whole batch signed total is %d, want %d (gross + tips + skim)", signedTotal, want)
	}
}

// TestBuildFromCloses_MultipleCloses_OneSetPerZNumber: a multi-day export
// produces one posting set per archived close, each tagged with THAT close's
// own Z-number and Belegdatum — never merged into one set.
func TestBuildFromCloses_MultipleCloses_OneSetPerZNumber(t *testing.T) {
	day1 := EODCloseExport{
		ZNumber: 21,
		Report: EODReportForExport{
			Day:            "2026-08-21",
			MethodTaxBands: []MethodTaxBand{{Method: "cash", RateBP: 1900, Net: 100, Tax: 19, Gross: 119}},
		},
	}
	day2 := EODCloseExport{
		ZNumber: 22,
		Report: EODReportForExport{
			Day:            "2026-08-22",
			MethodTaxBands: []MethodTaxBand{{Method: "cash", RateBP: 1900, Net: 200, Tax: 38, Gross: 238}},
		},
	}
	res, err := BuildFromCloses([]EODCloseExport{day1, day2}, closesSettings(), time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Filename != "EXTF_Buchungsstapel_2026-08-21_2026-08-22.csv" {
		t.Fatalf("unexpected filename: %s", res.Filename)
	}
	got := bookingTuples(t, res)
	if len(got) != 2 {
		t.Fatalf("expected 2 rows (one per close), got %d: %v", len(got), got)
	}
	if got[0][5] != `"21"` || got[0][4] != "2108" {
		t.Errorf("row 0 should carry close 21's Z-number/date, got %v", got[0])
	}
	if got[1][5] != `"22"` || got[1][4] != "2208" {
		t.Errorf("row 1 should carry close 22's Z-number/date, got %v", got[1])
	}
}

func TestBuildFromCloses_NoCloses_Refuses(t *testing.T) {
	_, err := BuildFromCloses(nil, closesSettings(), time.Now())
	if err == nil {
		t.Fatal("expected error for zero closes, got nil (a header-only file would mask a wrong range)")
	}
}

func TestBuildFromCloses_MethodWithoutKonto_Refuses(t *testing.T) {
	s := closesSettings()
	delete(s.KonteByMethod, "card")
	_, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now())
	if err == nil {
		t.Fatal("expected error for a method with no configured Konto, got nil")
	}
	if !strings.Contains(err.Error(), "card") {
		t.Fatalf("error should name the unconfigured method (card), got: %v", err)
	}
}

// Blank-string konto must refuse like an absent one — the same {"700": ""}
// trap Build's Erloeskonten check already guards.
func TestBuildFromCloses_BlankMethodKonto_TreatedAsMissing(t *testing.T) {
	s := closesSettings()
	s.KonteByMethod["card"] = ""
	_, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now())
	if err == nil {
		t.Fatal("expected error for a blank method Konto, got nil")
	}
	if !strings.Contains(err.Error(), "card") {
		t.Fatalf("error should name the blank method (card), got: %v", err)
	}
}

func TestBuildFromCloses_MissingErloeskontoForRate_Refuses(t *testing.T) {
	s := closesSettings()
	delete(s.Erloeskonten, "700")
	_, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now())
	if err == nil {
		t.Fatal("expected error for an unconfigured tax rate, got nil")
	}
	if !strings.Contains(err.Error(), "700") {
		t.Fatalf("error should name the missing rate (700), got: %v", err)
	}
}

func TestBuildFromCloses_TipsWithoutKontoTrinkgeld_Refuses(t *testing.T) {
	s := closesSettings()
	s.KontoTrinkgeld = ""
	_, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now())
	if err == nil {
		t.Fatal("expected error: the close has tips but datev_konto_trinkgeld is unset")
	}
	if !strings.Contains(err.Error(), "trinkgeld") {
		t.Fatalf("error should name the missing setting, got: %v", err)
	}
}

// Unset-but-unneeded liability Gegenkonten are fine: a close with no tips,
// no vouchers and no skim must build with only KonteByMethod/Erloeskonten
// configured (empty+unused is fine; empty+needed is the hard refuse).
func TestBuildFromCloses_UnusedLiabilityKontenMayStayEmpty(t *testing.T) {
	s := closesSettings()
	s.KontoTrinkgeld = ""
	s.KontoGutschein = ""
	close := EODCloseExport{
		ZNumber: 30,
		Report: EODReportForExport{
			Day:            "2026-08-23",
			MethodTaxBands: []MethodTaxBand{{Method: "cash", RateBP: 1900, Net: 100, Tax: 19, Gross: 119}},
		},
	}
	if _, err := BuildFromCloses([]EODCloseExport{close}, s, time.Now()); err != nil {
		t.Fatalf("unexpected error for unused empty liability accounts: %v", err)
	}
}

func TestBuildFromCloses_VouchersWithoutKontoGutschein_Refuses(t *testing.T) {
	s := closesSettings()
	s.KonteByMethod = map[string]string{"card": "1360"} // single method, so only the missing Gegenkonto can refuse
	s.KontoGutschein = ""
	close := EODCloseExport{
		ZNumber: 31,
		Report: EODReportForExport{
			Day:            "2026-08-23",
			MethodTaxBands: []MethodTaxBand{{Method: "card", RateBP: 1900, Net: 100, Tax: 19, Gross: 119}},
			VouchersIssued: 1500, VouchersIssuedCount: 1,
		},
	}
	_, err := BuildFromCloses([]EODCloseExport{close}, s, time.Now())
	if err == nil {
		t.Fatal("expected error: the close issued vouchers but datev_konto_gutschein is unset")
	}
	if !strings.Contains(err.Error(), "gutschein") {
		t.Fatalf("error should name the missing setting, got: %v", err)
	}
}

// TestBuildFromCloses_VouchersAmbiguousWithMultipleMethods_Refuses pins the
// documented limitation: EODReport.VouchersIssued is a day total with no
// payment-method dimension, so with more than one configured method the
// Konto to debit is genuinely ambiguous — refuse, never guess a method.
func TestBuildFromCloses_VouchersAmbiguousWithMultipleMethods_Refuses(t *testing.T) {
	close := referenceDayClose()
	close.Report.VouchersIssued = 1500
	close.Report.VouchersIssuedCount = 1
	_, err := BuildFromCloses([]EODCloseExport{close}, closesSettings(), time.Now())
	if err == nil {
		t.Fatal("expected error: vouchers issued with >1 configured payment method is ambiguous")
	}
	if !strings.Contains(err.Error(), "voucher") {
		t.Fatalf("error should explain the voucher ambiguity, got: %v", err)
	}
}

func TestBuildFromCloses_SkimWithoutCashKonto_Refuses(t *testing.T) {
	s := closesSettings()
	delete(s.KonteByMethod, "cash")
	close := EODCloseExport{
		ZNumber: 32,
		Report: EODReportForExport{
			Day:                "2026-08-23",
			MethodTaxBands:     []MethodTaxBand{{Method: "card", RateBP: 1900, Net: 100, Tax: 19, Gross: 119}},
			CashReconciliation: &CashReconciliation{Skim: -5000},
		},
	}
	_, err := BuildFromCloses([]EODCloseExport{close}, s, time.Now())
	if err == nil {
		t.Fatal("expected error: nonzero skim with no configured cash Konto")
	}
	if !strings.Contains(err.Error(), "cash") {
		t.Fatalf("error should name the missing cash Konto, got: %v", err)
	}
}

// Same ambiguity rule as vouchers, on the skim's Gegenkonto: with two
// non-cash methods configured there is no way to know which transit account
// the skimmed cash goes to — refuse, never guess.
func TestBuildFromCloses_SkimAmbiguousWithMultipleNonCashMethods_Refuses(t *testing.T) {
	s := closesSettings()
	s.KonteByMethod["voucher"] = "1361"
	close := referenceDayClose()
	_, err := BuildFromCloses([]EODCloseExport{close}, s, time.Now())
	if err == nil {
		t.Fatal("expected error: nonzero skim with >1 configured non-cash method is ambiguous")
	}
	if !strings.Contains(err.Error(), "skim") && !strings.Contains(err.Error(), "Abschoepfung") {
		t.Fatalf("error should explain the skim ambiguity, got: %v", err)
	}
}

// TestBuildFromCloses_LegacyCloseWithoutZNumber_Refuses: Belegfeld 1 is the
// document key tying each posting set to its day-close; a pre-migration
// archive row without a real Z-number (ZNumber 0) would file the batch
// under a fake key — refuse and name the close instead.
func TestBuildFromCloses_LegacyCloseWithoutZNumber_Refuses(t *testing.T) {
	close := referenceDayClose()
	close.ZNumber = 0
	_, err := BuildFromCloses([]EODCloseExport{close}, closesSettings(), time.Now())
	if err == nil {
		t.Fatal("expected error for a close with no Z-number, got nil")
	}
	if !strings.Contains(err.Error(), "Z-number") {
		t.Fatalf("error should explain the missing Z-number, got: %v", err)
	}
}

func TestBuildFromCloses_NegativeCellGross_Refuses(t *testing.T) {
	close := referenceDayClose()
	close.Report.MethodTaxBands[0].Gross = -100
	_, err := BuildFromCloses([]EODCloseExport{close}, closesSettings(), time.Now())
	if err == nil {
		t.Fatal("expected error for a negative cross-tab cell, got nil")
	}
}

func TestBuildFromCloses_MethodKontoNotDigits_Refuses(t *testing.T) {
	s := closesSettings()
	s.KonteByMethod["card"] = "1360;DROP"
	if _, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now()); err == nil {
		t.Fatal("expected error for a non-digit method Konto, got nil")
	}
}

func TestBuildFromCloses_HeaderPeriodFromCloses(t *testing.T) {
	res, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, closesSettings(), time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	header1 := strings.Split(strings.Split(decodeCP1252(res.Content), "\r\n")[0], ";")
	if len(header1) != 31 {
		t.Fatalf("header row 1 has %d fields, want 31", len(header1))
	}
	if header1[14] != "20260821" || header1[15] != "20260821" {
		t.Fatalf("booking period should span the closes' own days, got %q/%q", header1[14], header1[15])
	}
	// Festschreibung header flag is 1 for a closes batch (the archived
	// close is immutable — ut-docs#1005's Festschreibung=1 rule).
	if header1[20] != "1" {
		t.Fatalf("header Festschreibung: want 1, got %q", header1[20])
	}
}

func TestRateText(t *testing.T) {
	cases := map[int]string{
		0:    "0",
		700:  "7",
		1900: "19",
		1950: "19,5",
		1234: "12,34",
	}
	for bp, want := range cases {
		if got := rateText(bp); got != want {
			t.Errorf("rateText(%d) = %q, want %q", bp, got, want)
		}
	}
}
