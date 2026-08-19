// Package fiscalsign holds the pure, host-independent half of this
// plugin's `fiscal.sign.ask` answerer: the contract's request/response
// wire types, and the mapping from a till sale onto fiskaly SIGN DE's
// standard_v1 receipt schema.
//
// It exists as its own package for two reasons.
//
//  1. **Testability.** `src/main.go` is compiled only for `GOOS=wasip1`
//     (it declares `//go:wasmimport` host functions), so it cannot be
//     unit-tested on the host at all. Everything in here is ordinary Go
//     with no host dependency, so it runs under a normal `go test`.
//
//  2. **The provider seam ADR-0055 asks for.** ADR-0055 kept TSE signing
//     inside this plugin instead of splitting it into its own repo
//     (amending ADR-0044 Decision 3), on the condition that the fiskaly
//     calls sit behind "a small provider-shaped seam from the start" —
//     that is what makes both a future config-selected second provider
//     and any eventual hardware-backend extraction cheap. The wire
//     contract (Request/Response) is provider-neutral; only the
//     VAT/payment bucket mapping below is fiskaly-shaped, and it is the
//     part a second provider would replace.
//
// Contract: ut-docs/reference/contracts/fiscal-sign-ask.md v1.1.0.
package fiscalsign

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Response status values. These are matched by core as exact strings — an
// unknown status is treated as a failure (proceed-and-declare), so a typo
// here silently degrades every sale to "unsigned" rather than erroring.
const (
	StatusApproved        = "approved"
	StatusUnreachable     = "unreachable"
	StatusNotThisTerminal = "not-this-terminal"
)

// Payment is one payment on the sale. Amount is NET of change given (what
// was actually collected under this method); TipAmount is separate and
// additive, exactly as on the receipt.
type Payment struct {
	Method    string `json:"method"`
	Amount    int64  `json:"amount"`
	TipAmount int64  `json:"tip_amount"`
}

// VATLine is one rate bucket of the sale, already aggregated by core.
// Net is after line-level discounts but before sale-level
// discount/service charge.
type VATLine struct {
	RateBP int   `json:"rate_bp"`
	Net    int64 `json:"net"`
	Tax    int64 `json:"tax"`
}

// Request is the `fiscal.sign.ask` payload (contract v1.1.0). Money is in
// integer minor units throughout.
type Request struct {
	SaleID       string    `json:"sale_id"`
	Currency     string    `json:"currency"`
	Total        int64     `json:"total"`
	TenderedAt   time.Time `json:"tendered_at"`
	Payments     []Payment `json:"payments"`
	VATBreakdown []VATLine `json:"vat_breakdown"`
	// Retry marks a background re-attempt for a sale that already
	// completed unsigned. The sale is committed; sign it as-is.
	Retry bool `json:"retry"`
}

// ParseRequest unwraps the event envelope core delivers on stdin
// (`{"type":…,"payload":{…}}`, the same shape tax.rate.ask and
// export.requested.ask already use) and validates the one field the whole
// point correlates on.
//
// It fails closed. A compliance-bearing point must never sign a guess: if
// the request cannot be read, the caller answers `unreachable` (declared,
// retried) rather than `approved`.
func ParseRequest(raw []byte) (Request, error) {
	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return Request{}, fmt.Errorf("unparseable event envelope: %w", err)
	}
	if len(wrapper.Payload) == 0 {
		return Request{}, errors.New("event envelope has no payload")
	}
	var req Request
	if err := json.Unmarshal(wrapper.Payload, &req); err != nil {
		return Request{}, fmt.Errorf("unparseable payload: %w", err)
	}
	if strings.TrimSpace(req.SaleID) == "" {
		return Request{}, errors.New("payload has no sale_id")
	}
	return req, nil
}

// VATRateBucket maps a basis-point VAT rate to fiskaly's SIGN DE
// standard_v1 vat_rate enum. Germany's rates today: 19% (NORMAL), 7%
// (REDUCED_1). Anything else falls back to SPECIAL_RATE_1 rather than
// guessing further.
//
// CONFIRMED 2026-08-18 against a live sandbox: NORMAL only. REDUCED_1 /
// NULL / SPECIAL_RATE_1 are not independently proven — no reason to expect
// they are wrong, but they have not been exercised end to end.
func VATRateBucket(bp int) string {
	switch bp {
	case 1900:
		return "NORMAL"
	case 700:
		return "REDUCED_1"
	case 0:
		return "NULL"
	default:
		return "SPECIAL_RATE_1"
	}
}

// PaymentTypeBucket maps the till's free-text payment method onto
// fiskaly's CASH/NON_CASH split — the granularity the TSE schema actually
// cares about (everything that isn't physical cash is NON_CASH).
func PaymentTypeBucket(method string) string {
	if strings.EqualFold(method, "cash") {
		return "CASH"
	}
	return "NON_CASH"
}

// MinorToDecimalString renders integer minor units as the decimal string
// fiskaly's schema expects ("12.90"). Never float arithmetic.
func MinorToDecimalString(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// VATAmount is one entry of fiskaly's amounts_per_vat_rate.
type VATAmount struct {
	VATRate string `json:"vat_rate"`
	Amount  string `json:"amount"`
}

// PaymentAmount is one entry of fiskaly's amounts_per_payment_type.
type PaymentAmount struct {
	PaymentType string `json:"payment_type"`
	Amount      string `json:"amount"`
}

// VATAmounts builds amounts_per_vat_rate from the request's VAT breakdown.
//
// The amount per bucket is GROSS — net + tax. The contract sends the two
// separately; sending net alone would under-declare the turnover of every
// signed sale.
//
// Output order is sorted by bucket name so the same sale always produces
// byte-identical request bodies (Go randomises map iteration, and a
// background retry re-signs the same sale).
func VATAmounts(req Request) []VATAmount {
	buckets := map[string]int64{}
	for _, l := range req.VATBreakdown {
		buckets[VATRateBucket(l.RateBP)] += l.Net + l.Tax
	}
	out := make([]VATAmount, 0, len(buckets))
	for rate, cents := range buckets {
		out = append(out, VATAmount{VATRate: rate, Amount: MinorToDecimalString(cents)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VATRate < out[j].VATRate })
	return out
}

// PaymentAmounts builds amounts_per_payment_type from the request's
// payments.
//
// The tip is included in its payment's amount: it is real money collected
// under that method, so leaving it out would under-report every tipped
// sale and fail to reconcile against the printed receipt.
//
// Sorted, for the same determinism reason as VATAmounts.
func PaymentAmounts(req Request) []PaymentAmount {
	buckets := map[string]int64{}
	for _, p := range req.Payments {
		buckets[PaymentTypeBucket(p.Method)] += p.Amount + p.TipAmount
	}
	out := make([]PaymentAmount, 0, len(buckets))
	for typ, cents := range buckets {
		out = append(out, PaymentAmount{PaymentType: typ, Amount: MinorToDecimalString(cents)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PaymentType < out[j].PaymentType })
	return out
}

// TSEEvidence is the optional §6 KassenSichV receipt evidence an
// `approved` response may carry (contract 1.1.0). Every field is
// individually optional; core renders only what is present and never
// substitutes placeholder text.
type TSEEvidence struct {
	TransactionNumber  int64  `json:"transaction_number,omitempty"`
	SignatureCounter   int64  `json:"signature_counter,omitempty"`
	SerialNumber       string `json:"serial_number,omitempty"`
	StartTime          string `json:"start_time,omitempty"`
	LogTime            string `json:"log_time,omitempty"`
	Signature          string `json:"signature,omitempty"`
	SignatureAlgorithm string `json:"signature_algorithm,omitempty"`
}

// Response is what this plugin writes to stdout.
type Response struct {
	Status string       `json:"status"`
	TSE    *TSEEvidence `json:"tse,omitempty"`
}

// JSON renders the response for stdout. Marshalling cannot fail for this
// shape, so errors are impossible by construction rather than swallowed.
func (r Response) JSON() []byte {
	b, err := json.Marshal(r)
	if err != nil { // unreachable: only strings/ints/omitempty pointers
		return []byte(`{"status":"unreachable"}`)
	}
	return b
}

// Approved builds the approved response, attaching evidence only when it
// carries an actual signature.
//
// Contract 1.1.0: core treats evidence as present only when
// `tse.signature` is non-empty — "an evidence object without the signature
// itself proves nothing worth persisting". Enforcing that here rather than
// relying on core to ignore it keeps the wire output honest.
func Approved(ev TSEEvidence) Response {
	if strings.TrimSpace(ev.Signature) == "" {
		return Response{Status: StatusApproved}
	}
	e := ev
	return Response{Status: StatusApproved, TSE: &e}
}

// Unreachable declares the signing backend unreachable. Core treats this
// as a failure and applies proceed-and-declare: the sale still completes,
// but journaled unsigned, with a receipt notice, an operator alert and a
// background retry. It is never a refusal of the sale.
func Unreachable() Response { return Response{Status: StatusUnreachable} }

// NotThisTerminal declares the sale is not this plugin's responsibility.
// Core treats it exactly like no answer — the sale proceeds with no
// marker. Explicitly NOT a failure.
func NotThisTerminal() Response { return Response{Status: StatusNotThisTerminal} }
