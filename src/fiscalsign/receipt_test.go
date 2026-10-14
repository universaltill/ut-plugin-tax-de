package fiscalsign

import (
	"errors"
	"math"
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

// ut-docs#4232 (contract 1.14.0): a multi-purpose voucher (Mehrzweck-
// gutschein) sold in the sale is in `total` but not in `vat_breakdown`; it
// is not taxable at issue (DSFinV-K MehrzweckgutscheinKauf, USt-Schlüssel
// 5), so it is signed in the NULL bucket and never touched by the sale
// discount.

func TestBuildReceipt_VoucherOnlySaleSignsInNULL(t *testing.T) {
	r := mustBuild(t, Request{
		Total:         2500,
		TaxInclusive:  boolPtr(true),
		Payments:      []Payment{{Method: "card", Amount: 2500}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeMulti, Amount: 2500}},
	}, Options{})
	if v := vatMap(r); v["NULL"] != "25.00" || len(v) != 1 {
		t.Fatalf("VAT = %v", v)
	}
	if p := payMap(r); p["NON_CASH"] != "25.00" || len(p) != 1 {
		t.Fatalf("payments = %v", p)
	}
}

// Goods + voucher with a sale discount: the discount is split across the
// goods' rates only, the voucher keeps its full face value — and the
// inclusive/exclusive inference (no flag) still finds the right reading.
func TestBuildReceipt_GoodsPlusVoucherWithDiscount(t *testing.T) {
	for _, flag := range []*bool{boolPtr(true), nil} {
		r := mustBuild(t, Request{
			Total:         5990,
			TaxInclusive:  flag,
			SaleDiscount:  200,
			Payments:      []Payment{{Method: "cash", Amount: 5990}},
			VATBreakdown:  []VATLine{{RateBP: 1900, Net: 1190, Tax: 190}},
			VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeMulti, Amount: 5000}},
		}, Options{})
		if v := vatMap(r); v["NORMAL"] != "9.90" || v["NULL"] != "50.00" || len(v) != 2 {
			t.Fatalf("flag=%v: VAT = %v", flag, v)
		}
	}
}

// A business tip is turnover: a voucher in NULL must not attract a share
// of it (proportional treatment weights taxable rates only).
func TestBuildReceipt_VoucherDoesNotWeightBusinessTip(t *testing.T) {
	r := mustBuild(t, Request{
		Total:         2700,
		TaxInclusive:  boolPtr(true),
		Payments:      []Payment{{Method: "card", Amount: 2800, TipAmount: 100, TipRecipient: TipRecipientBusiness}},
		VATBreakdown:  []VATLine{{RateBP: 700, Net: 700, Tax: 46}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeMulti, Amount: 2000}},
	}, Options{})
	if v := vatMap(r); v["REDUCED_1"] != "8.00" || v["NULL"] != "20.00" || len(v) != 2 {
		t.Fatalf("VAT = %v", v)
	}
}

// A single-purpose voucher is already a taxed band in vat_breakdown; its
// voucher_issues entry is informational and must not be counted again.
func TestBuildReceipt_SinglePurposeVoucherAlreadyInBreakdown(t *testing.T) {
	r := mustBuild(t, Request{
		Total:         1000,
		TaxInclusive:  boolPtr(true),
		Payments:      []Payment{{Method: "cash", Amount: 1000}},
		VATBreakdown:  []VATLine{{RateBP: 1900, Net: 1000, Tax: 160}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 1000, RateBP: 1900}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "10.00" || len(v) != 1 {
		t.Fatalf("VAT = %v", v)
	}
}

// A single-purpose entry counted again (as if multi-purpose) would push
// the goods part below total; reading it as informational must sign.
func TestBuildReceipt_SinglePurposeVoucherNotCountedTwice(t *testing.T) {
	r := mustBuild(t, Request{
		Total:         1290,
		TaxInclusive:  boolPtr(true),
		Payments:      []Payment{{Method: "cash", Amount: 1290}},
		VATBreakdown:  []VATLine{{RateBP: 700, Net: 290, Tax: 19}, {RateBP: 1900, Net: 1000, Tax: 160}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 1000, RateBP: 1900}},
	}, Options{})
	if v := vatMap(r); v["NULL"] != "" || v["NORMAL"] != "10.00" || v["REDUCED_1"] != "2.90" {
		t.Fatalf("VAT = %v", v)
	}
}

func TestBuildReceipt_RefusesOverflowingVoucherTotal(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:        1000,
		TaxInclusive: boolPtr(true),
		Payments:     []Payment{{Method: "cash", Amount: 1000}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1000, Tax: 160}},
		VoucherIssues: []VoucherIssue{
			{Purpose: VoucherPurposeMulti, Amount: math.MaxInt64},
			{Purpose: VoucherPurposeMulti, Amount: math.MaxInt64},
			{Purpose: VoucherPurposeMulti, Amount: 2},
		},
	}, Options{})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("want ErrCannotSign, got %v", err)
	}
}

func TestBuildReceipt_RefusesMalformedVoucherIssue(t *testing.T) {
	for name, vi := range map[string]VoucherIssue{
		"unknown purpose": {Purpose: "gift", Amount: 500},
		"zero amount":     {Purpose: VoucherPurposeMulti, Amount: 0},
		"negative amount": {Purpose: VoucherPurposeMulti, Amount: -500},
	} {
		// The goods reconcile on their own, so the only reason to refuse
		// is the malformed voucher entry (a reader ignoring it would sign).
		_, err := BuildReceipt(Request{
			Total:         1000,
			TaxInclusive:  boolPtr(true),
			Payments:      []Payment{{Method: "cash", Amount: 1000}},
			VATBreakdown:  []VATLine{{RateBP: 1900, Net: 1000, Tax: 160}},
			VoucherIssues: []VoucherIssue{vi},
		}, Options{})
		if !errors.Is(err, ErrCannotSign) {
			t.Fatalf("%s: want ErrCannotSign, got %v", name, err)
		}
	}
}

// Contract 1.14.1 (ut-docs#4233, #4240): the sale discount is apportioned
// over each band's GOODS gross only. A single-purpose voucher sold in the
// sale keeps the gross it was issued with, so the signed split matches
// core's own banding (pos.VATBandsForSale: receipt, invoice, Z report).
// The card's case: goods 100.00 @19% + single-purpose voucher 50.00 @7%,
// 10% (10.00) discount.
func TestBuildReceipt_SaleDiscountSkipsSinglePurposeVoucher(t *testing.T) {
	sp := []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 5000, RateBP: 700}}
	t.Run("inclusive", func(t *testing.T) {
		r := mustBuild(t, Request{
			Total:         14000,
			TaxInclusive:  boolPtr(true),
			SaleDiscount:  1000,
			Payments:      []Payment{{Method: "cash", Amount: 14000}},
			VATBreakdown:  []VATLine{{RateBP: 700, Net: 5000, Tax: 327}, {RateBP: 1900, Net: 10000, Tax: 1597}},
			VoucherIssues: sp,
		}, Options{})
		if v := vatMap(r); v["NORMAL"] != "90.00" || v["REDUCED_1"] != "50.00" || len(v) != 2 {
			t.Fatalf("VAT = %v, want NORMAL 90.00 / REDUCED_1 50.00", v)
		}
	})
	t.Run("exclusive", func(t *testing.T) {
		// net 150.00 − 10.00 + tax 22.50
		r := mustBuild(t, Request{
			Total:         16250,
			TaxInclusive:  boolPtr(false),
			SaleDiscount:  1000,
			Payments:      []Payment{{Method: "cash", Amount: 16250}},
			VATBreakdown:  []VATLine{{RateBP: 700, Net: 5000, Tax: 350}, {RateBP: 1900, Net: 10000, Tax: 1900}},
			VoucherIssues: sp,
		}, Options{})
		// 19%: net 90.00 + VAT 19.00; 7%: net 50.00 + VAT 3.50.
		if v := vatMap(r); v["NORMAL"] != "109.00" || v["REDUCED_1"] != "53.50" || len(v) != 2 {
			t.Fatalf("VAT = %v, want NORMAL 109.00 / REDUCED_1 53.50", v)
		}
	})
}

// The remainder goes to the highest band whose goods gross is > 0 — never
// to a band holding only a single-purpose voucher, even the highest rate.
func TestBuildReceipt_DiscountRemainderSkipsVoucherOnlyBand(t *testing.T) {
	// goods 1.00 @7% + 2.00 @10.7%, voucher 10.00 @19%, 1.00 off:
	// shares 33 (floor) / 67 (remainder on 10.7%, not 19%).
	r := mustBuild(t, Request{
		Total:        1200,
		TaxInclusive: boolPtr(true),
		SaleDiscount: 100,
		Payments:     []Payment{{Method: "cash", Amount: 1200}},
		VATBreakdown: []VATLine{
			{RateBP: 700, Net: 100, Tax: 6},
			{RateBP: 1070, Net: 200, Tax: 19},
			{RateBP: 1900, Net: 1000, Tax: 160},
		},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 1000, RateBP: 1900}},
	}, Options{})
	if v := vatMap(r); v["REDUCED_1"] != "0.67" || v["SPECIAL_RATE_1"] != "1.33" || v["NORMAL"] != "10.00" {
		t.Fatalf("VAT = %v, want REDUCED_1 0.67 / SPECIAL_RATE_1 1.33 / NORMAL 10.00", v)
	}
}

// Goods and a single-purpose voucher in the SAME band: only the goods part
// carries that band's share; the voucher part is added back unchanged.
func TestBuildReceipt_DiscountSharedBandWeightsGoodsOnly(t *testing.T) {
	// 7%: goods 1.00; 19%: goods 2.00 + voucher 10.00. 1.00 off →
	// weights 100 / 200 (not 1200): 33 / 67.
	r := mustBuild(t, Request{
		Total:         1200,
		TaxInclusive:  boolPtr(true),
		SaleDiscount:  100,
		Payments:      []Payment{{Method: "cash", Amount: 1200}},
		VATBreakdown:  []VATLine{{RateBP: 700, Net: 100, Tax: 6}, {RateBP: 1900, Net: 1200, Tax: 191}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 1000, RateBP: 1900}},
	}, Options{})
	if v := vatMap(r); v["REDUCED_1"] != "0.67" || v["NORMAL"] != "11.33" {
		t.Fatalf("VAT = %v, want REDUCED_1 0.67 / NORMAL 11.33", v)
	}
}

// Exclusive pricing: a single-purpose voucher's gross is its amount (the
// net) plus its own tax, rounded half-up — so a 0.50 voucher @19% weighs
// 0.60 (0.095 → 0.10), leaving the 7% goods all of the discount.
func TestBuildReceipt_ExclusiveVoucherGrossIncludesRoundedTax(t *testing.T) {
	// 7%: goods net 1.00 tax 0.07; 19%: voucher net 0.50 tax 0.10.
	// total = 150 − 50 + 17 = 117.
	r := mustBuild(t, Request{
		Total:         117,
		TaxInclusive:  boolPtr(false),
		SaleDiscount:  50,
		Payments:      []Payment{{Method: "cash", Amount: 117}},
		VATBreakdown:  []VATLine{{RateBP: 700, Net: 100, Tax: 7}, {RateBP: 1900, Net: 50, Tax: 10}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 50, RateBP: 1900}},
	}, Options{})
	if v := vatMap(r); v["REDUCED_1"] != "0.57" || v["NORMAL"] != "0.60" {
		t.Fatalf("VAT = %v, want REDUCED_1 0.57 / NORMAL 0.60", v)
	}
}

// No goods at all but a discount (the basket caps a discount at the goods,
// but another CompleteSale caller can still send one): core falls back to
// every band's full gross, so the signer does too.
func TestBuildReceipt_DiscountWithNoGoodsFallsBackToFullGross(t *testing.T) {
	r := mustBuild(t, Request{
		Total:         900,
		TaxInclusive:  boolPtr(true),
		SaleDiscount:  100,
		Payments:      []Payment{{Method: "cash", Amount: 900}},
		VATBreakdown:  []VATLine{{RateBP: 1900, Net: 1000, Tax: 160}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 1000, RateBP: 1900}},
	}, Options{})
	if v := vatMap(r); v["NORMAL"] != "9.00" || len(v) != 1 {
		t.Fatalf("VAT = %v", v)
	}
}

// A single-purpose voucher worth more than its band (or at a rate the
// breakdown doesn't have) means the payload contradicts itself: refuse.
func TestBuildReceipt_RefusesVoucherLargerThanItsBand(t *testing.T) {
	for name, vi := range map[string]VoucherIssue{
		"larger than band": {Purpose: VoucherPurposeSingle, Amount: 1500, RateBP: 1900},
		"rate not in band": {Purpose: VoucherPurposeSingle, Amount: 500, RateBP: 700},
	} {
		_, err := BuildReceipt(Request{
			Total:         900,
			TaxInclusive:  boolPtr(true),
			SaleDiscount:  100,
			Payments:      []Payment{{Method: "cash", Amount: 900}},
			VATBreakdown:  []VATLine{{RateBP: 1900, Net: 1000, Tax: 160}},
			VoucherIssues: []VoucherIssue{vi},
		}, Options{})
		if !errors.Is(err, ErrCannotSign) {
			t.Fatalf("%s: want ErrCannotSign, got %v", name, err)
		}
	}
}

// A discount larger than the goods next to a single-purpose voucher (a
// tender discount the basket didn't cap): the voucher props total up, so
// the sum still reconciles — but a negative band must never be signed.
func TestBuildReceipt_RefusesDiscountTakingGoodsBandNegative(t *testing.T) {
	_, err := BuildReceipt(Request{
		Total:         900,
		TaxInclusive:  boolPtr(true),
		SaleDiscount:  600,
		Payments:      []Payment{{Method: "cash", Amount: 900}},
		VATBreakdown:  []VATLine{{RateBP: 700, Net: 500, Tax: 33}, {RateBP: 1900, Net: 1000, Tax: 160}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 1000, RateBP: 1900}},
	}, Options{})
	if !errors.Is(err, ErrCannotSign) {
		t.Fatalf("want ErrCannotSign, got %v", err)
	}
}

// Contract 1.15.0 (ut-docs#4277, #4282): each band's share is capped at the
// goods it comes off and the leftover minor units fill the bands from the
// highest rate down, each up to its cap — core's pos.splitSaleDiscount.
// Before, the whole remainder went on the highest band carrying goods and
// a tiny-goods top band ended at −0.01 (the sum still matched total).
func TestBuildReceipt_DiscountShareCappedAtGoodsInclusive(t *testing.T) {
	// goods 1.00 @7%, 2.00 @10.7%, 0.01 @19%, 3.00 off:
	// floors 99 / 199 / 0, leftover 2 → 19% +1 (cap 1), 10.7% +1 (cap 200).
	r := mustBuild(t, Request{
		Total:        1,
		TaxInclusive: boolPtr(true),
		SaleDiscount: 300,
		Payments:     []Payment{{Method: "cash", Amount: 1}},
		VATBreakdown: []VATLine{
			{RateBP: 700, Net: 100, Tax: 7},
			{RateBP: 1070, Net: 200, Tax: 19},
			{RateBP: 1900, Net: 1, Tax: 0},
		},
	}, Options{})
	// A band left at exactly 0 carries no bucket.
	if v := vatMap(r); v["REDUCED_1"] != "0.01" || len(v) != 1 {
		t.Fatalf("VAT = %v, want only REDUCED_1 0.01", v)
	}
}

// Exclusive pricing caps a share at the band's goods NET (the discount
// comes off the net base; tax stays as computed).
func TestBuildReceipt_DiscountShareCappedAtGoodsExclusive(t *testing.T) {
	// nets 100 / 200 / 1, taxes 7 / 21 / 0 → gross weights 107 / 221 / 1.
	// floors 97 / 201→cap 200 / 0, leftover 3 → 19% +1, 10.7% full, 7% +2:
	// shares 99 / 200 / 1 → gross 8 / 21 / 0. total = 301 − 300 + 28 = 29.
	r := mustBuild(t, Request{
		Total:        29,
		TaxInclusive: boolPtr(false),
		SaleDiscount: 300,
		Payments:     []Payment{{Method: "cash", Amount: 29}},
		VATBreakdown: []VATLine{
			{RateBP: 700, Net: 100, Tax: 7},
			{RateBP: 1070, Net: 200, Tax: 21},
			{RateBP: 1900, Net: 1, Tax: 0},
		},
	}, Options{})
	if v := vatMap(r); v["REDUCED_1"] != "0.08" || v["SPECIAL_RATE_1"] != "0.21" || len(v) != 2 {
		t.Fatalf("VAT = %v, want REDUCED_1 0.08 / SPECIAL_RATE_1 0.21 only", v)
	}
}

// Exclusive pricing with a single-purpose voucher in the same band: the cap
// is the band's net less the voucher's amount. Old code put 12 on 19% —
// more than its goods net of 10 — and signed 0.09 / 1.19 against core's
// 0.07 / 1.21 (no band went negative: the voucher propped it up).
func TestBuildReceipt_DiscountCapExclusiveExcludesVoucherNet(t *testing.T) {
	// 7%: goods net 100 tax 7. 19%: goods net 10 tax 2 + voucher net 100
	// tax 19. weights 107 / (131−119=12), d = 110: floors 98 / 11→cap 10,
	// leftover 2 → 19% full, 7% +2: shares 100 / 10 → gross 7 / 121.
	// total = 210 − 110 + 28 = 128.
	r := mustBuild(t, Request{
		Total:         128,
		TaxInclusive:  boolPtr(false),
		SaleDiscount:  110,
		Payments:      []Payment{{Method: "cash", Amount: 128}},
		VATBreakdown:  []VATLine{{RateBP: 700, Net: 100, Tax: 7}, {RateBP: 1900, Net: 110, Tax: 21}},
		VoucherIssues: []VoucherIssue{{Purpose: VoucherPurposeSingle, Amount: 100, RateBP: 1900}},
	}, Options{})
	if v := vatMap(r); v["REDUCED_1"] != "0.07" || v["NORMAL"] != "1.21" {
		t.Fatalf("VAT = %v, want REDUCED_1 0.07 / NORMAL 1.21", v)
	}
}
