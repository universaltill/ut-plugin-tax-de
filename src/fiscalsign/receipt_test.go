package fiscalsign

import (
	"errors"
	"strings"
	"testing"
)

// ut-docs#833: the treatments BuildReceipt signs. Every case asserts both
// halves of the Beleg, because the whole point is that they agree.

func boolPtr(b bool) *bool { return &b }

func vatMap(r Receipt) map[string]string {
	out := map[string]string{}
	for _, v := range r.VAT {
		out[v.VATRate] = v.Amount
	}
	return out
}

func payMap(r Receipt) map[string]string {
	out := map[string]string{}
	for _, p := range r.Payments {
		out[p.PaymentType] = p.Amount
	}
	return out
}

func mustBuild(t *testing.T, req Request, opt Options) Receipt {
	t.Helper()
	r, err := BuildReceipt(req, opt)
	if err != nil {
		t.Fatalf("BuildReceipt: unexpected refusal: %v", err)
	}
	return r
}

func TestBuildReceipt_PlainInclusiveSale(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        1368,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "cash", Amount: 1368}},
		VATBreakdown: []VATLine{{RateBP: 700, Net: 535, Tax: 35}, {RateBP: 1900, Net: 833, Tax: 133}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "8.33" || v["REDUCED_1"] != "5.35" || len(v) != 2 {
		t.Fatalf("VAT = %v", v)
	}
	if p := payMap(r); p["CASH"] != "13.68" || len(p) != 1 {
		t.Fatalf("payments = %v", p)
	}
}

// The explicit flag wins over inference: an exclusive sale is gross net+tax.
func TestBuildReceipt_ExplicitExclusiveFlag(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        1190,
		TaxInclusive: boolPtr(false),
		Payments:     []Payment{{Method: "card", Amount: 1190}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1000, Tax: 190}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "11.90" {
		t.Fatalf("VAT = %v", v)
	}
}

// A flag that contradicts the numbers is refused, never signed.
func TestBuildReceipt_FlagContradictingTotalIsRefused(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:        1190,
		TaxInclusive: boolPtr(false),
		Payments:     []Payment{{Method: "card", Amount: 1190}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1190, Tax: 190}},
	}, Options{})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("err = %v, want ErrCannotSign", err)
	}
}

// Whole-bill discount (DSFinV-K Rabatt, UStAE 10.3): split across the rates
// in proportion to each rate's gross; floors, remainder on the highest rate.
// €20.00 = 12.00 @19% + 8.00 @7%, €2.00 off → 1.20 / 0.80.
func TestBuildReceipt_SaleDiscountApportionedByGross(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        1800,
		TaxInclusive: boolPtr(true),
		SaleDiscount: 200,
		Payments:     []Payment{{Method: "card", Amount: 1800}},
		VATBreakdown: []VATLine{{RateBP: 700, Net: 800, Tax: 52}, {RateBP: 1900, Net: 1200, Tax: 192}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "10.80" || v["REDUCED_1"] != "7.20" {
		t.Fatalf("VAT = %v, want NORMAL 10.80 / REDUCED_1 7.20", v)
	}
}

// The remainder of the floor split lands on the highest rate, so the
// buckets sum to the total to the cent.
func TestBuildReceipt_SaleDiscountRemainderOnHighestRate(t *testing.T) {
	// 1.00 @7% + 2.00 @19%, 1.00 off: shares 33.33 / 66.66 → floor 33, rest 67.
	r := mustBuild(t, Request{
		Total:        200,
		TaxInclusive: boolPtr(true),
		SaleDiscount: 100,
		Payments:     []Payment{{Method: "cash", Amount: 200}},
		VATBreakdown: []VATLine{{RateBP: 700, Net: 100, Tax: 6}, {RateBP: 1900, Net: 200, Tax: 31}},
	}, Options{})
	if v := vatMap(r); v["REDUCED_1"] != "0.67" || v["NORMAL"] != "1.33" {
		t.Fatalf("VAT = %v, want REDUCED_1 0.67 / NORMAL 1.33", v)
	}
}

// Exclusive pricing: core leaves tax on the undiscounted net, so the
// discount comes off the gross (net+tax) and the total still reconciles.
func TestBuildReceipt_SaleDiscountExclusive(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        1090, // 1000 + 190 − 100
		TaxInclusive: boolPtr(false),
		SaleDiscount: 100,
		Payments:     []Payment{{Method: "cash", Amount: 1090}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1000, Tax: 190}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "10.90" {
		t.Fatalf("VAT = %v", v)
	}
}

// A pre-1.2.0 core sends no tax_inclusive flag; the inference still works
// once the discount is taken into account.
func TestBuildReceipt_InfersPricingModeWithDiscount(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        990,
		SaleDiscount: 200,
		Payments:     []Payment{{Method: "cash", Amount: 990}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1190, Tax: 190}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "9.90" {
		t.Fatalf("VAT = %v", v)
	}
}

// Employee tip (DSFinV-K TrinkgeldAN, USt-Schlüssel 5): not the business's
// turnover, so it sits in the 0%/NULL bucket and the Beleg balances. This
// is the body's own example: €12.90 bill, €14.00 on the card.
func TestBuildReceipt_EmployeeTipInNullBucket(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        1290,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1400, TipAmount: 110, TipRecipient: "employee"}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1290, Tax: 206}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "12.90" || v["NULL"] != "1.10" {
		t.Fatalf("VAT = %v, want NORMAL 12.90 / NULL 1.10", v)
	}
	if p := payMap(r); p["NON_CASH"] != "14.00" {
		t.Fatalf("payments = %v, want NON_CASH 14.00 (tip counted once)", p)
	}
}

// Contract 1.11.0 (ut-docs#2571, #2976): every payment's amount includes its
// own tip, so a sale paid exactly has Σamount == total + Σtip. A tipped
// payment whose amount equals the total (the pre-1.11.0 reader-reported
// shape, tip left outside amount) no longer matches the contract: it is
// refused, never signed by guessing the tip sits on top.
func TestBuildReceipt_TipOutsideAmountRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		payments []Payment
	}{
		{"single tipped leg", []Payment{{Method: "card", Amount: 1290, TipAmount: 110, TipRecipient: "employee"}}},
		{"business tip", []Payment{{Method: "card", Amount: 1290, TipAmount: 110, TipRecipient: "business"}}},
		{"split tender, tip on card", []Payment{{Method: "cash", Amount: 290}, {Method: "card", Amount: 1000, TipAmount: 110}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildReceipt(Request{
				Total:        1290,
				TaxInclusive: boolPtr(true),
				Payments:     tc.payments,
				VATBreakdown: []VATLine{{RateBP: 1900, Net: 1290, Tax: 206}},
			}, Options{})
			if !errors.Is(err, ErrCannotSign) {
				t.Fatalf("err = %v, want ErrCannotSign (Σamount must be total + Σtip)", err)
			}
			// Refused by the explicit 1.11.0 check, not only by the later
			// halves-balance check, so the log names the real cause.
			if !strings.Contains(err.Error(), "contract 1.11.0") {
				t.Fatalf("err = %v, want the contract 1.11.0 tip-convention refusal", err)
			}
		})
	}
}

// An over-tender by exactly the tip total used to be indistinguishable from
// "tip inside amount" while both conventions were accepted. With only the
// 1.11.0 convention left, the payment side must equal total + Σtip to the
// cent: one cent either way is refused.
func TestBuildReceipt_PaymentsOffByOneCentRefused(t *testing.T) {
	for _, amount := range []int64{1399, 1401} {
		_, err := BuildReceipt(Request{
			Total:        1290,
			TaxInclusive: boolPtr(true),
			Payments:     []Payment{{Method: "card", Amount: amount, TipAmount: 110}},
			VATBreakdown: []VATLine{{RateBP: 1900, Net: 1290, Tax: 206}},
		}, Options{})
		if !errors.Is(err, ErrCannotSign) {
			t.Errorf("amount %d: err = %v, want ErrCannotSign", amount, err)
		}
	}
}

// No recipient on the wire (a pre-1.9.0 core) means employee — the default
// core persists and the one every researched system uses.
func TestBuildReceipt_MissingRecipientIsEmployee(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        1190,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1290, TipAmount: 100}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1190, Tax: 190}},
	}, Options{})
	if v := vatMap(r); v["NULL"] != "1.00" || v["NORMAL"] != "11.90" {
		t.Fatalf("VAT = %v", v)
	}
}

// Business tip (TrinkgeldAG) is taxable turnover. Default treatment:
// proportional to the sale's own rates (extra consideration for the same
// supply, like an Aufschlag). €10 @19% + €10 @7%, €2 tip → 1.00 / 1.00.
func TestBuildReceipt_BusinessTipProportionalByDefault(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        2000,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 2200, TipAmount: 200, TipRecipient: "business"}},
		VATBreakdown: []VATLine{{RateBP: 700, Net: 1000, Tax: 65}, {RateBP: 1900, Net: 1000, Tax: 160}},
	}, Options{})
	v := vatMap(r)
	if v["NORMAL"] != "11.00" || v["REDUCED_1"] != "11.00" {
		t.Fatalf("VAT = %v, want NORMAL 11.00 / REDUCED_1 11.00", v)
	}
	if _, ok := v["NULL"]; ok {
		t.Fatalf("a business tip is turnover, never in NULL: %v", v)
	}
}

func TestBuildReceipt_BusinessTipStandardRate(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        2000,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 2200, TipAmount: 200, TipRecipient: "business"}},
		VATBreakdown: []VATLine{{RateBP: 700, Net: 1000, Tax: 65}, {RateBP: 1900, Net: 1000, Tax: 160}},
	}, Options{BusinessTip: BusinessTipStandardRate})
	if v := vatMap(r); v["NORMAL"] != "12.00" || v["REDUCED_1"] != "10.00" {
		t.Fatalf("VAT = %v, want NORMAL 12.00 / REDUCED_1 10.00", v)
	}
}

// ADR-0136 Decision 7 (ut-docs#3309): "refuse" is no longer a treatment. A
// shop that still has the legacy value stored reads it as proportional —
// no migration — and the sale signs with exactly the default's math
// (€10 @19% + €10 @7%, €2 business tip → 11.00 / 11.00), never refused.
func TestBuildReceipt_LegacyRefuseSettingSignsProportionally(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        2000,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 2200, TipAmount: 200, TipRecipient: "business"}},
		VATBreakdown: []VATLine{{RateBP: 700, Net: 1000, Tax: 65}, {RateBP: 1900, Net: 1000, Tax: 160}},
	}, Options{BusinessTip: ParseBusinessTipTreatment("refuse")})
	v := vatMap(r)
	if v["NORMAL"] != "11.00" || v["REDUCED_1"] != "11.00" {
		t.Fatalf("VAT = %v, want NORMAL 11.00 / REDUCED_1 11.00 (proportional)", v)
	}
	if _, ok := v["NULL"]; ok {
		t.Fatalf("a business tip is turnover, never in NULL: %v", v)
	}
}

// Only an exact "standard_rate" selects the 19% treatment; everything else
// — empty, "proportional", the legacy "refuse", or anything unrecognised —
// is proportional (ADR-0136 Decision 7). Nothing can select refusal.
func TestParseBusinessTipTreatment(t *testing.T) {
	cases := map[string]BusinessTipTreatment{
		"":                BusinessTipProportional,
		"proportional":    BusinessTipProportional,
		" Standard_Rate ": BusinessTipStandardRate,
		"refuse":          BusinessTipProportional,
		"19%":             BusinessTipProportional,
	}
	for in, want := range cases {
		if got := ParseBusinessTipTreatment(in); got != want {
			t.Errorf("ParseBusinessTipTreatment(%q) = %q, want %q", in, got, want)
		}
	}
}

// Payments that do not reconcile with total + Σtip (e.g. an under-reported
// payment) are refused, never signed.
func TestBuildReceipt_UnreconcilablePaymentsRefused(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:        1190,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1000, TipAmount: 100}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1190, Tax: 190}},
	}, Options{})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("err = %v, want ErrCannotSign", err)
	}
}

// Service charge is already folded into vat_breakdown by core (contract
// 1.5.0); the flat field is display-only and must not be applied again.
func TestBuildReceipt_ServiceChargeNotAppliedTwice(t *testing.T) {
	r := mustBuild(t, Request{
		Total:         1100,
		TaxInclusive:  boolPtr(true),
		ServiceCharge: 100,
		Payments:      []Payment{{Method: "card", Amount: 1100}},
		VATBreakdown:  []VATLine{{RateBP: 1900, Net: 1100, Tax: 175}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "11.00" {
		t.Fatalf("VAT = %v", v)
	}
}

// Split tender with a tip on only the card, cash change already netted.
func TestBuildReceipt_SplitTenderTipOnCard(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        2000,
		TaxInclusive: boolPtr(true),
		Payments: []Payment{
			{Method: "cash", Amount: 1000},
			{Method: "card", Amount: 1150, TipAmount: 150, TipRecipient: "employee"},
		},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 2000, Tax: 319}},
	}, Options{})
	if p := payMap(r); p["CASH"] != "10.00" || p["NON_CASH"] != "11.50" {
		t.Fatalf("payments = %v", p)
	}
	if v := vatMap(r); v["NORMAL"] != "20.00" || v["NULL"] != "1.50" {
		t.Fatalf("VAT = %v", v)
	}
}

// The same sale always produces byte-identical request bodies (map order is
// random in Go), so the fiskaly call is deterministic.
func TestBuildReceipt_DeterministicOrder(t *testing.T) {
	req := Request{
		Total:        2000,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1100, TipAmount: 100}, {Method: "cash", Amount: 1000}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1000, Tax: 160}, {RateBP: 700, Net: 1000, Tax: 65}},
	}
	first := mustBuild(t, req, Options{})
	for i := 0; i < 50; i++ {
		got := mustBuild(t, req, Options{})
		for j := range first.VAT {
			if got.VAT[j] != first.VAT[j] {
				t.Fatalf("VAT order changed: %v vs %v", got.VAT, first.VAT)
			}
		}
		for j := range first.Payments {
			if got.Payments[j] != first.Payments[j] {
				t.Fatalf("payment order changed: %v vs %v", got.Payments, first.Payments)
			}
		}
	}
}

func TestCannotSignResponse(t *testing.T) {
	if got := string(CannotSign().JSON()); got != `{"status":"cannot-sign"}` {
		t.Fatalf("CannotSign().JSON() = %s", got)
	}
}

// Review finding 1: a rate with no DSFinV-K bucket (a mis-set 16% tax code)
// must be refused, never signed as if it were 10.7%.
func TestBuildReceipt_UnknownRateRefused(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:        1160,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1160}},
		VATBreakdown: []VATLine{{RateBP: 1600, Net: 1160, Tax: 160}},
	}, Options{})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("err = %v, want ErrCannotSign", err)
	}
}

// Review finding 3: a business tip is turnover — no share of it may land in
// NULL because the sale also has a zero-rated line.
func TestBuildReceipt_BusinessTipSkipsZeroRatedLines(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        2000,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 2100, TipAmount: 100, TipRecipient: "business"}},
		VATBreakdown: []VATLine{{RateBP: 0, Net: 1000, Tax: 0}, {RateBP: 1900, Net: 1000, Tax: 160}},
	}, Options{})
	if v := vatMap(r); v["NULL"] != "10.00" || v["NORMAL"] != "11.00" {
		t.Fatalf("VAT = %v, want NULL 10.00 / NORMAL 11.00", v)
	}
}

func TestBuildReceipt_BusinessTipOnZeroRatedSaleRefused(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:        1000,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1100, TipAmount: 100, TipRecipient: "business"}},
		VATBreakdown: []VATLine{{RateBP: 0, Net: 1000, Tax: 0}},
	}, Options{})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("err = %v, want ErrCannotSign", err)
	}
}

func TestBuildReceipt_UnknownTipRecipientRefused(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:        1190,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1290, TipAmount: 100, TipRecipient: "pool"}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1190, Tax: 190}},
	}, Options{})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("err = %v, want ErrCannotSign", err)
	}
}

// tax_inclusive:true on exclusive-shaped numbers does not reconcile: refuse.
func TestBuildReceipt_InclusiveFlagOnExclusiveNumbersRefused(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:        1190,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1190}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1000, Tax: 190}},
	}, Options{})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("err = %v, want ErrCannotSign", err)
	}
}
