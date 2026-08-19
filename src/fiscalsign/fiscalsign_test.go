package fiscalsign

import (
	"encoding/json"
	"testing"
)

// The event envelope core delivers on stdin for an `.ask` event: a typed
// wrapper with the real payload nested under "payload" (same shape
// tax.rate.ask and export.requested.ask already use in this plugin).
const realEnvelope = `{
  "type": "fiscal.sign.ask",
  "payload": {
    "sale_id": "3f2a1b",
    "currency": "EUR",
    "total": 1290,
    "tendered_at": "2026-08-15T10:31:02Z",
    "payments": [{"method": "card", "amount": 1190, "tip_amount": 100}],
    "vat_breakdown": [
      {"rate_bp": 700,  "net": 500, "tax": 35},
      {"rate_bp": 1900, "net": 700, "tax": 133}
    ]
  }
}`

func TestParseRequest_RealEnvelope(t *testing.T) {
	req, err := ParseRequest([]byte(realEnvelope))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if req.SaleID != "3f2a1b" {
		t.Errorf("SaleID = %q, want 3f2a1b", req.SaleID)
	}
	if req.Total != 1290 {
		t.Errorf("Total = %d, want 1290", req.Total)
	}
	if req.Retry {
		t.Error("Retry should default false when the field is absent")
	}
	if len(req.Payments) != 1 || req.Payments[0].Amount != 1190 || req.Payments[0].TipAmount != 100 {
		t.Errorf("Payments = %+v", req.Payments)
	}
	if len(req.VATBreakdown) != 2 {
		t.Fatalf("VATBreakdown len = %d, want 2", len(req.VATBreakdown))
	}
}

func TestParseRequest_RetryFlag(t *testing.T) {
	req, err := ParseRequest([]byte(`{"type":"fiscal.sign.ask","payload":{"sale_id":"x","retry":true}}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if !req.Retry {
		t.Error("Retry = false, want true — a background re-attempt must be distinguishable")
	}
}

func TestParseRequest_Malformed(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"not json", `{{{`},
		{"payload not an object", `{"type":"fiscal.sign.ask","payload":"nope"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRequest([]byte(tc.in)); err == nil {
				t.Error("want an error, got nil — a compliance point must fail closed, not sign a guess")
			}
		})
	}
}

// A missing sale_id is unusable: the whole point correlates on it (core
// keys the unsigned marker, the retry queue and the evidence row on it).
func TestParseRequest_MissingSaleIDIsAnError(t *testing.T) {
	if _, err := ParseRequest([]byte(`{"type":"fiscal.sign.ask","payload":{"total":100}}`)); err == nil {
		t.Error("want an error for a payload with no sale_id")
	}
}

func TestVATRateBucket(t *testing.T) {
	for _, tc := range []struct {
		bp   int
		want string
	}{
		{1900, "NORMAL"},
		{700, "REDUCED_1"},
		{0, "NULL"},
		{500, "SPECIAL_RATE_1"},
	} {
		if got := VATRateBucket(tc.bp); got != tc.want {
			t.Errorf("VATRateBucket(%d) = %q, want %q", tc.bp, got, tc.want)
		}
	}
}

func TestPaymentTypeBucket(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"cash", "CASH"},
		{"CASH", "CASH"},
		{"card", "NON_CASH"},
		{"voucher", "NON_CASH"},
		{"", "NON_CASH"},
	} {
		if got := PaymentTypeBucket(tc.in); got != tc.want {
			t.Errorf("PaymentTypeBucket(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMinorToDecimalString(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{1290, "12.90"},
		{5, "0.05"},
		{0, "0.00"},
		{-250, "-2.50"},
		{100000, "1000.00"},
	} {
		if got := MinorToDecimalString(tc.in); got != tc.want {
			t.Errorf("MinorToDecimalString(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// fiskaly's standard_v1 receipt wants the GROSS amount per VAT rate. The
// contract sends net and tax separately, so the mapping must add them --
// sending net alone would under-declare every signed sale's turnover.
func TestVATAmounts_SumsNetPlusTaxPerBucket(t *testing.T) {
	req, err := ParseRequest([]byte(realEnvelope))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	got := map[string]string{}
	for _, a := range VATAmounts(req) {
		got[a.VATRate] = a.Amount
	}
	if got["REDUCED_1"] != "5.35" { // 500 net + 35 tax
		t.Errorf("REDUCED_1 = %q, want 5.35", got["REDUCED_1"])
	}
	if got["NORMAL"] != "8.33" { // 700 net + 133 tax
		t.Errorf("NORMAL = %q, want 8.33", got["NORMAL"])
	}
}

func TestVATAmounts_MergesTwoLinesOfTheSameRate(t *testing.T) {
	req := Request{VATBreakdown: []VATLine{
		{RateBP: 1900, Net: 100, Tax: 19},
		{RateBP: 1900, Net: 200, Tax: 38},
	}}
	amounts := VATAmounts(req)
	if len(amounts) != 1 {
		t.Fatalf("want 1 merged bucket, got %d: %+v", len(amounts), amounts)
	}
	if amounts[0].Amount != "3.57" { // (100+19) + (200+38)
		t.Errorf("Amount = %q, want 3.57", amounts[0].Amount)
	}
}

// Deterministic output ordering: two runs over the same request must
// produce the same slice, or an otherwise-identical sale signs differently
// on a retry purely because Go randomises map iteration.
func TestVATAmounts_DeterministicOrder(t *testing.T) {
	req := Request{VATBreakdown: []VATLine{
		{RateBP: 1900, Net: 100, Tax: 19},
		{RateBP: 700, Net: 100, Tax: 7},
		{RateBP: 0, Net: 50, Tax: 0},
	}}
	first := VATAmounts(req)
	for i := 0; i < 50; i++ {
		got := VATAmounts(req)
		for j := range first {
			if got[j] != first[j] {
				t.Fatalf("ordering not deterministic at %d: %+v vs %+v", j, got, first)
			}
		}
	}
}

// The tip is real money collected under that payment method, so it belongs
// in the signed payment total -- otherwise amounts_per_payment_type
// under-reports every tipped sale and won't reconcile against the receipt.
func TestPaymentAmounts_IncludesTip(t *testing.T) {
	req, err := ParseRequest([]byte(realEnvelope))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	amounts := PaymentAmounts(req)
	if len(amounts) != 1 {
		t.Fatalf("want 1 bucket, got %d", len(amounts))
	}
	if amounts[0].PaymentType != "NON_CASH" {
		t.Errorf("PaymentType = %q, want NON_CASH", amounts[0].PaymentType)
	}
	if amounts[0].Amount != "12.90" { // 1190 + 100 tip
		t.Errorf("Amount = %q, want 12.90 (amount + tip)", amounts[0].Amount)
	}
}

func TestPaymentAmounts_SplitsCashAndNonCash(t *testing.T) {
	req := Request{Payments: []Payment{
		{Method: "cash", Amount: 500},
		{Method: "card", Amount: 700, TipAmount: 100},
		{Method: "cash", Amount: 200},
	}}
	got := map[string]string{}
	for _, a := range PaymentAmounts(req) {
		got[a.PaymentType] = a.Amount
	}
	if got["CASH"] != "7.00" {
		t.Errorf("CASH = %q, want 7.00", got["CASH"])
	}
	if got["NON_CASH"] != "8.00" {
		t.Errorf("NON_CASH = %q, want 8.00", got["NON_CASH"])
	}
}

func TestPaymentAmounts_DeterministicOrder(t *testing.T) {
	req := Request{Payments: []Payment{
		{Method: "card", Amount: 100},
		{Method: "cash", Amount: 100},
	}}
	first := PaymentAmounts(req)
	for i := 0; i < 50; i++ {
		got := PaymentAmounts(req)
		for j := range first {
			if got[j] != first[j] {
				t.Fatalf("ordering not deterministic: %+v vs %+v", got, first)
			}
		}
	}
}

// --- responses (the three contract states) ---

func TestApprovedResponse_NoEvidence(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(Approved(TSEEvidence{}).JSON(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"] != "approved" {
		t.Errorf("status = %v, want approved", got["status"])
	}
	if _, present := got["tse"]; present {
		t.Error("tse must be omitted entirely when there is no evidence")
	}
}

// Contract 1.1.0: core treats evidence as present ONLY when signature is
// non-empty -- an evidence object without the signature proves nothing.
func TestApprovedResponse_EvidenceWithoutSignatureIsOmitted(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(Approved(TSEEvidence{SerialNumber: "9d5e", SignatureCounter: 12345}).JSON(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["tse"]; present {
		t.Error("tse must be omitted when signature is empty, however many other fields are set")
	}
}

func TestApprovedResponse_FullEvidence(t *testing.T) {
	ev := TSEEvidence{
		TransactionNumber:  4711,
		SignatureCounter:   12345,
		SerialNumber:       "9d5e",
		StartTime:          "2026-08-15T10:31:00Z",
		LogTime:            "2026-08-15T10:31:02Z",
		Signature:          "MEQCIF",
		SignatureAlgorithm: "ecdsa-plain-SHA256",
	}
	var got struct {
		Status string `json:"status"`
		TSE    *struct {
			TransactionNumber  int64  `json:"transaction_number"`
			SignatureCounter   int64  `json:"signature_counter"`
			SerialNumber       string `json:"serial_number"`
			StartTime          string `json:"start_time"`
			LogTime            string `json:"log_time"`
			Signature          string `json:"signature"`
			SignatureAlgorithm string `json:"signature_algorithm"`
		} `json:"tse"`
	}
	if err := json.Unmarshal(Approved(ev).JSON(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "approved" {
		t.Fatalf("status = %q", got.Status)
	}
	if got.TSE == nil {
		t.Fatal("tse missing")
	}
	if got.TSE.Signature != "MEQCIF" || got.TSE.SignatureCounter != 12345 ||
		got.TSE.SerialNumber != "9d5e" || got.TSE.TransactionNumber != 4711 ||
		got.TSE.LogTime != "2026-08-15T10:31:02Z" || got.TSE.StartTime != "2026-08-15T10:31:00Z" ||
		got.TSE.SignatureAlgorithm != "ecdsa-plain-SHA256" {
		t.Errorf("evidence round-trip wrong: %+v", *got.TSE)
	}
}

// Partial evidence is explicitly allowed by the contract: every field is
// individually optional so long as signature is present.
func TestApprovedResponse_PartialEvidenceOmitsEmptyFields(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(Approved(TSEEvidence{Signature: "MEQCIF"}).JSON(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tse, ok := got["tse"].(map[string]any)
	if !ok {
		t.Fatal("tse missing despite a signature being present")
	}
	if tse["signature"] != "MEQCIF" {
		t.Errorf("signature = %v", tse["signature"])
	}
	for _, absent := range []string{"serial_number", "start_time", "log_time", "signature_algorithm", "transaction_number", "signature_counter"} {
		if _, present := tse[absent]; present {
			t.Errorf("%s should be omitted when unset, not sent as a zero value", absent)
		}
	}
}

func TestUnreachableResponse(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(Unreachable().JSON(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"] != "unreachable" {
		t.Errorf("status = %v, want unreachable", got["status"])
	}
	if _, present := got["tse"]; present {
		t.Error("unreachable must carry no tse object")
	}
}

// Guards the exact wire strings -- core matches on them, so a typo here is
// silently treated as an unknown status (= failure) by the host.
func TestStatusConstantsMatchTheContract(t *testing.T) {
	if StatusApproved != "approved" || StatusUnreachable != "unreachable" || StatusNotThisTerminal != "not-this-terminal" {
		t.Errorf("status constants drifted from contract v1.1.0: %q %q %q",
			StatusApproved, StatusUnreachable, StatusNotThisTerminal)
	}
}

// --- balance invariant (ut-docs#818 review finding B1) ---
//
// fiskaly's standard_v1 receipt becomes a DSFinV-K Kassenbeleg-V1 string
// whose two halves must be equal: `Beleg^<gross per VAT rate>^<per payment
// type>`. A receipt where they disagree is either rejected by fiskaly or —
// worse — signed into an irreversible record that misstates the sale.

func TestBalanceDelta_PlainSaleBalances(t *testing.T) {
	req := Request{
		Total:        1190,
		Payments:     []Payment{{Method: "card", Amount: 1190}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1000, Tax: 190}},
	}
	if d := BalanceDelta(req); d != 0 {
		t.Errorf("BalanceDelta = %d, want 0 for a plain sale", d)
	}
}

// The exact defect the review found: a tip lands on the payment side and in
// no VAT bucket, so the two halves differ by the tip.
func TestBalanceDelta_TipUnbalances(t *testing.T) {
	req := Request{
		Total:        1190,
		Payments:     []Payment{{Method: "card", Amount: 1190, TipAmount: 100}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1000, Tax: 190}},
	}
	if d := BalanceDelta(req); d != -100 {
		t.Errorf("BalanceDelta = %d, want -100 (payments exceed VAT by the tip)", d)
	}
}

// A sale-level discount reduces `total` (and so the payments) but not the
// per-line VAT breakdown, which the contract states is computed before it.
func TestBalanceDelta_SaleLevelDiscountUnbalances(t *testing.T) {
	req := Request{
		Total:        990,
		Payments:     []Payment{{Method: "cash", Amount: 990}},
		VATBreakdown: []VATLine{{RateBP: 1900, Net: 1000, Tax: 190}},
	}
	if d := BalanceDelta(req); d != 200 {
		t.Errorf("BalanceDelta = %d, want 200 (VAT side exceeds what was paid)", d)
	}
}

func TestBalanceDelta_MixedRatesBalance(t *testing.T) {
	req := Request{
		Payments: []Payment{{Method: "cash", Amount: 500}, {Method: "card", Amount: 868}},
		VATBreakdown: []VATLine{
			{RateBP: 1900, Net: 700, Tax: 133},
			{RateBP: 700, Net: 500, Tax: 35},
		},
	}
	if d := BalanceDelta(req); d != 0 {
		t.Errorf("BalanceDelta = %d, want 0 (833 + 535 == 500 + 868)", d)
	}
}
