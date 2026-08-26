package datev

import (
	"strings"
	"testing"
	"time"
)

// closesSettings is validSettings' BuildFromCloses counterpart: the
// method->Konto map replaces the single KontoKasse, plus the liability
// Gegenkonten (voucher issuance, tips) and the two dedicated debit/transit
// accounts (voucher proceeds, cash-skim destination). Account numbers are
// the SKR03 ones from ut-docs#1005's reference table — test fixture data,
// not code defaults (the package still ships no default account numbers).
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
		KontoGutschein:        "1796",
		KontoGutscheinZahlung: "1360", // the reference day's voucher was card-paid: proceeds landed in Geldtransit
		KontoGeldtransit:      "1360", // skim destination: Geldtransit, per the reference's "Abschoepfung Kasse" row
		KontoTrinkgeld:        "1363",
		Erloeskonten: map[string]string{
			"1900": "8400",
			"700":  "8300",
		},
	}
}

// referenceDayClose is ut-docs#1005's FULL reference day — all seven
// reference rows, voucher issuance included: with the dedicated
// datev_konto_gutschein_zahlung / datev_konto_geldtransit settings naming
// the voucher-proceeds and skim-destination accounts directly, an ordinary
// cash+card day that also sold a voucher builds in one batch (an earlier
// draft inferred both accounts from datev_konten_by_method's cardinality
// and could only manage 6 of the 7 rows).
//
// Gross is the Z-report's own headline figure: the cross-tab cells' gross
// total (121040) plus VouchersIssued (1500) — vouchers are inside the sale
// total but outside every per-rate band, per the host's EODReport doc.
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
			Gross:              122540,
			VouchersIssued:     1500,
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

// TestBuildFromCloses_GoldenReferenceDay reproduces ALL SEVEN of
// ut-docs#1005's reference rows in one batch (the ticket's own "Golden-file
// test reproducing all seven reference rows" acceptance criterion): one row
// per (payment method x VAT rate) at gross, Konto by method, Gegenkonto by
// rate, the voucher-issuance liability row (debit
// datev_konto_gutschein_zahlung, credit datev_konto_gutschein, labelled
// "CARD" because 1360 is uniquely card's Konto), the tip liability row, and
// the cash-skim credit (H) row into datev_konto_geldtransit — all keyed to
// the close's own Z-number in Belegfeld 1, with Festschreibung=1 (the
// archived close is immutable, per the ticket's rule). Row order is this
// package's rendering order (cells, voucher, tips, skim), not the ticket
// table's — the seven rows themselves match the reference exactly.
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
		{"15,00", `"S"`, "1360", "1796", "2108", `"17"`, `"Aufbuchungen 0% CARD"`, "1"},
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

// TestBuildFromCloses_VoucherRowLabel_NoUniqueMethodMatch pins the label
// fallback: when datev_konto_gutschein_zahlung matches no configured
// method's Konto (a dedicated voucher-proceeds account), the Buchungstext
// is plain "Aufbuchungen 0%" — the booking itself is identical either way.
func TestBuildFromCloses_VoucherRowLabel_NoUniqueMethodMatch(t *testing.T) {
	s := closesSettings()
	s.KontoGutscheinZahlung = "1361" // matches neither cash (1000) nor card (1360)
	close := EODCloseExport{
		ZNumber: 18,
		Report: EODReportForExport{
			Day: "2026-08-22",
			MethodTaxBands: []MethodTaxBand{
				{Method: "card", RateBP: 1900, Net: 8008, Tax: 1522, Gross: 9530},
			},
			VouchersIssued: 1500,
		},
	}
	res, err := BuildFromCloses([]EODCloseExport{close}, s, time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := bookingTuples(t, res)
	want := [][8]string{
		{"95,30", `"S"`, "1360", "8400", "2208", `"18"`, `"Erloese 19% CARD"`, "1"},
		{"15,00", `"S"`, "1361", "1796", "2208", `"18"`, `"Aufbuchungen 0%"`, "1"},
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

// TestBuildFromCloses_ReconcilesToZReportGross is the ut-docs#1005 AC "row
// total equals the day-close gross", asserted as a genuine identity against
// the Z-report's OWN headline Gross figure (not just against a re-sum of
// the same cells the rows were rendered from): per the host's EODReport
// definition, Gross includes voucher issuance (a 0% liability inside the
// sale total) but never tips, so
//
//	sum(Erloes rows) + voucher-issuance row == Report.Gross
//
// while tips and skim are each reproduced exactly and the whole batch's
// signed total is Gross + tips + skim.
func TestBuildFromCloses_ReconcilesToZReportGross(t *testing.T) {
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

	var erloesSum, voucherSum, tipSum, skimSum, signedTotal int64
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
		case strings.HasPrefix(row[6], `"Aufbuchungen `):
			voucherSum += signed
		case strings.HasPrefix(row[6], `"Trinkgeld `):
			tipSum += signed
		case row[6] == `"Abschoepfung Kasse"`:
			skimSum += signed
		}
	}

	if got, want := erloesSum+voucherSum, close.Report.Gross; got != want {
		t.Errorf("Erloes rows (%d) + voucher row (%d) sum to %d, want the Z-report's own Gross %d", erloesSum, voucherSum, got, want)
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
	if want := close.Report.Gross + wantTips + close.Report.CashReconciliation.Skim; signedTotal != want {
		t.Errorf("whole batch signed total is %d, want %d (Gross + tips + skim)", signedTotal, want)
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

// Unset-but-unneeded liability/transit Konten are fine: a close with no
// tips, no vouchers and no skim must build with only KonteByMethod/
// Erloeskonten configured (empty+unused is fine; empty+needed is the hard
// refuse).
func TestBuildFromCloses_UnusedLiabilityKontenMayStayEmpty(t *testing.T) {
	s := closesSettings()
	s.KontoTrinkgeld = ""
	s.KontoGutschein = ""
	s.KontoGutscheinZahlung = ""
	s.KontoGeldtransit = ""
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
	s.KontoGutschein = ""
	_, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now())
	if err == nil {
		t.Fatal("expected error: the close issued vouchers but datev_konto_gutschein is unset")
	}
	if !strings.Contains(err.Error(), "datev_konto_gutschein is not configured") {
		t.Fatalf("error should name the missing setting, got: %v", err)
	}
}

// TestBuildFromCloses_VouchersWithoutKontoGutscheinZahlung_Refuses:
// EODReport.VouchersIssued is a day total with no payment-method dimension,
// so the Konto the proceeds landed on must be named directly by
// datev_konto_gutschein_zahlung — with it unset the export refuses, never
// inferring an account from datev_konten_by_method (whatever its size).
func TestBuildFromCloses_VouchersWithoutKontoGutscheinZahlung_Refuses(t *testing.T) {
	s := closesSettings()
	s.KontoGutscheinZahlung = ""
	_, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now())
	if err == nil {
		t.Fatal("expected error: vouchers issued but datev_konto_gutschein_zahlung is unset")
	}
	if !strings.Contains(err.Error(), "datev_konto_gutschein_zahlung") {
		t.Fatalf("error should name the missing setting, got: %v", err)
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

// Same rule as vouchers, on the skim's Gegenkonto: the destination
// (transit/safe) account is named directly by datev_konto_geldtransit —
// with it unset and a nonzero skim in range the export refuses, never
// inferring the account from which non-cash methods happen to be
// configured.
func TestBuildFromCloses_SkimWithoutKontoGeldtransit_Refuses(t *testing.T) {
	s := closesSettings()
	s.KontoGeldtransit = ""
	_, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now())
	if err == nil {
		t.Fatal("expected error: nonzero skim but datev_konto_geldtransit is unset")
	}
	if !strings.Contains(err.Error(), "datev_konto_geldtransit") {
		t.Fatalf("error should name the missing setting, got: %v", err)
	}
}

// TestBuildFromCloses_PositiveSkim_Refuses: a skim is stored negative (cash
// removed from the drawer) and the shifts API refuses to record a positive
// one — a positive value here means corrupt or hand-edited archive data, so
// it refuses like the negative-tip/negative-cell checks (defense in depth).
func TestBuildFromCloses_PositiveSkim_Refuses(t *testing.T) {
	close := referenceDayClose()
	close.Report.CashReconciliation.Skim = 5000
	_, err := BuildFromCloses([]EODCloseExport{close}, closesSettings(), time.Now())
	if err == nil {
		t.Fatal("expected error for a positive skim, got nil")
	}
	if !strings.Contains(err.Error(), "positive cash skim") {
		t.Fatalf("error should name the positive skim, got: %v", err)
	}
}

// TestBuildFromCloses_NormalCashCardVoucherDay is B3's regression test
// (independent review of ut-docs#1005): an ordinary pilot-shop
// configuration — cash + card + a third tender ("giro", chosen to sort
// AFTER "cash" so no revived sorted-slice-[0] pattern could pass by
// accident) — that sells a gift voucher AND skims the drawer on the same
// day must build, because the voucher-proceeds and skim-destination
// accounts come from their own dedicated settings, not from
// datev_konten_by_method's cardinality. An earlier draft refused this
// entire batch.
func TestBuildFromCloses_NormalCashCardVoucherDay(t *testing.T) {
	s := closesSettings()
	s.KonteByMethod = map[string]string{
		"cash": "1000",
		"card": "1360",
		"giro": "1362",
	}
	s.KontoGutscheinZahlung = "1360"
	s.KontoGeldtransit = "1370"
	close := EODCloseExport{
		ZNumber: 40,
		Report: EODReportForExport{
			Day: "2026-08-24",
			MethodTaxBands: []MethodTaxBand{
				{Method: "cash", RateBP: 1900, Net: 100, Tax: 19, Gross: 119},
				{Method: "card", RateBP: 1900, Net: 200, Tax: 38, Gross: 238},
				{Method: "giro", RateBP: 700, Net: 300, Tax: 21, Gross: 321},
			},
			Gross:              2178, // 678 cells + 1500 vouchers
			VouchersIssued:     1500,
			CashReconciliation: &CashReconciliation{Skim: -100},
		},
	}
	res, err := BuildFromCloses([]EODCloseExport{close}, s, time.Now().UTC())
	if err != nil {
		t.Fatalf("a normal cash+card(+giro) voucher day must build with the dedicated settings, got: %v", err)
	}
	got := bookingTuples(t, res)
	want := [][8]string{
		{"1,19", `"S"`, "1000", "8400", "2408", `"40"`, `"Erloese 19% CASH"`, "1"},
		{"2,38", `"S"`, "1360", "8400", "2408", `"40"`, `"Erloese 19% CARD"`, "1"},
		{"3,21", `"S"`, "1362", "8300", "2408", `"40"`, `"Erloese 7% GIRO"`, "1"},
		{"15,00", `"S"`, "1360", "1796", "2408", `"40"`, `"Aufbuchungen 0% CARD"`, "1"},
		{"1,00", `"H"`, "1000", "1370", "2408", `"40"`, `"Abschoepfung Kasse"`, "1"},
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

// TestBuildFromCloses_ZeroGrossCell_SkippedNotBooked pins the fix for a
// zero-Gross cross-tab cell: split-tender apportionment can floor a small
// tender against a small band to exactly 0, and a same-day sale+return can
// net a cell to exactly 0 too (neither is an error — negative cells are
// refused separately). A zero cell must be skipped, not booked as a 0,00
// Umsatz row (DATEV requires a positive amount per row), and must not count
// toward renderedRows -- a close whose only cell is zero, with no other
// activity, must still hit the AllClosesEmpty refusal, not silently pass it.
func TestBuildFromCloses_ZeroGrossCell_SkippedNotBooked(t *testing.T) {
	s := closesSettings()
	close := EODCloseExport{
		ZNumber: 60,
		Report: EODReportForExport{
			Day: "2026-08-24",
			MethodTaxBands: []MethodTaxBand{
				{Method: "cash", RateBP: 1900, Net: 0, Tax: 0, Gross: 0},
				{Method: "card", RateBP: 1900, Net: 200, Tax: 38, Gross: 238},
			},
		},
	}
	res, err := BuildFromCloses([]EODCloseExport{close}, s, time.Now().UTC())
	if err != nil {
		t.Fatalf("a close with one zero cell and one real cell must build, got: %v", err)
	}
	got := bookingTuples(t, res)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 row (the zero cell skipped), got %d: %v", len(got), got)
	}
	if got[0][6] != `"Erloese 19% CARD"` {
		t.Fatalf("expected the surviving row to be the card cell, got: %v", got[0])
	}

	// A close whose ONLY cell is zero, with nothing else, must still count
	// as zero rendered rows -- the AllClosesEmpty refusal, not a silent
	// header-only pass.
	onlyZero := EODCloseExport{
		ZNumber: 61,
		Report: EODReportForExport{
			Day:            "2026-08-25",
			MethodTaxBands: []MethodTaxBand{{Method: "cash", RateBP: 1900, Net: 0, Tax: 0, Gross: 0}},
		},
	}
	_, err = BuildFromCloses([]EODCloseExport{onlyZero}, s, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "no postings") {
		t.Fatalf("a close whose only cell is zero-Gross must refuse as no-postings, got: %v", err)
	}
}

// TestBuildFromCloses_AllClosesEmpty_Refuses closes the header-only-file
// hole the len(closes)==0 check alone leaves open: a range containing only
// genuinely zero-trading closed days (empty cross-tab, no tips, no
// vouchers, no skim) renders zero booking rows and must refuse, not return
// an apparently-successful 2-line file.
func TestBuildFromCloses_AllClosesEmpty_Refuses(t *testing.T) {
	empty := func(z int64, day string) EODCloseExport {
		return EODCloseExport{ZNumber: z, Report: EODReportForExport{Day: day}}
	}
	_, err := BuildFromCloses([]EODCloseExport{empty(50, "2026-08-25"), empty(51, "2026-08-26")}, closesSettings(), time.Now())
	if err == nil {
		t.Fatal("expected error for a range of zero-activity closes, got nil (a header-only file would look like a successful export)")
	}
	if !strings.Contains(err.Error(), "no postings") || !strings.Contains(err.Error(), "2026-08-25") || !strings.Contains(err.Error(), "2026-08-26") {
		t.Fatalf("error should say no postings and name the range, got: %v", err)
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

func TestBuildFromCloses_DedicatedKontenNotDigits_Refuse(t *testing.T) {
	s := closesSettings()
	s.KontoGutscheinZahlung = "1360;DROP"
	if _, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now()); err == nil {
		t.Fatal("expected error for a non-digit datev_konto_gutschein_zahlung, got nil")
	}
	s = closesSettings()
	s.KontoGeldtransit = "1370;DROP"
	if _, err := BuildFromCloses([]EODCloseExport{referenceDayClose()}, s, time.Now()); err == nil {
		t.Fatal("expected error for a non-digit datev_konto_geldtransit, got nil")
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
