package fiscalsign

import (
	"errors"
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
	for _, tc := range []struct {
		name   string
		amount int64
	}{
		{"amount includes the tip (core's stored convention)", 1400},
		{"amount excludes the tip (reader-reported path)", 1290},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := mustBuild(t, Request{
				Total:        1290,
				TaxInclusive: boolPtr(true),
				Payments:     []Payment{{Method: "card", Amount: tc.amount, TipAmount: 110, TipRecipient: "employee"}},
				VATBreakdown: []VATLine{{RateBP: 1900, Net: 1290, Tax: 206}},
			}, Options{})
			if v := vatMap(r); v["NORMAL"] != "12.90" || v["NULL"] != "1.10" {
				t.Fatalf("VAT = %v, want NORMAL 12.90 / NULL 1.10", v)
			}
			if p := payMap(r); p["NON_CASH"] != "14.00" {
				t.Fatalf("payments = %v, want NON_CASH 14.00 (tip counted once)", p)
			}
		})
	}
}

// No recipient on the wire (a pre-1.9.0 core) means employee — the default
// core persists and the one every researched system uses.
func TestBuildReceipt_MissingRecipientIsEmployee(t *testing.T) {
	r := mustBuild(t, Request{
		Total:        1190,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1190, TipAmount: 100}},
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

func TestBuildReceipt_BusinessTipRefuseSetting(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:        1190,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "card", Amount: 1290, TipAmount: 100, TipRecipient: "business"}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1190, Tax: 190}},
	}, Options{BusinessTip: BusinessTipRefuse})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("err = %v, want ErrCannotSign", err)
	}
}

// An unknown setting value must never be guessed at: refuse.
func TestParseBusinessTipTreatment(t *testing.T) {
	cases := map[string]BusinessTipTreatment{
		"":                BusinessTipProportional,
		"proportional":    BusinessTipProportional,
		" Standard_Rate ": BusinessTipStandardRate,
		"refuse":          BusinessTipRefuse,
		"19%":             BusinessTipRefuse,
	}
	for in, want := range cases {
		if got := ParseBusinessTipTreatment(in); got != want {
			t.Errorf("ParseBusinessTipTreatment(%q) = %q, want %q", in, got, want)
		}
	}
}

// Payments that reconcile with the total neither with nor without the tips
// (e.g. an under-reported payment) are refused, never signed.
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
