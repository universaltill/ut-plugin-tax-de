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
// Contract: ut-docs/reference/contracts/fiscal-sign-ask.md, tracking through
// v1.10.0 (ut-docs#2880's `receipt` object on approved is the latest addition).
package fiscalsign

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// StatusCannotSign (contract 1.3.0) refuses a sale this signer cannot
	// put on a correct Beleg — not an outage, so core words it differently.
	StatusCannotSign = "cannot-sign"
)

// Payment is one payment on the sale. Amount is NET of change given (what
// was actually collected under this method); TipAmount is separate and
// additive, exactly as on the receipt.
type Payment struct {
	Method    string `json:"method"`
	Amount    int64  `json:"amount"`
	TipAmount int64  `json:"tip_amount"`
	// TipRecipient (contract 1.9.0, ut-docs#833) is "employee" or
	// "business" on a tipped payment; absent from an older core, which
	// BuildReceipt reads as employee — core's own persisted default.
	TipRecipient string `json:"tip_recipient"`
}

// VATLine is one rate bucket of the sale, already aggregated by core.
// Net is after line-level discounts but before sale-level
// discount/service charge.
type VATLine struct {
	RateBP int   `json:"rate_bp"`
	Net    int64 `json:"net"`
	Tax    int64 `json:"tax"`
}

// Request is the `fiscal.sign.ask` payload (contract v1.1.0+, currently
// tracking v1.9.0). Money is in integer minor units throughout.
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
	// SaleType (contract 1.6.0, ut-docs#1203/#1404) is "sale" | "return" —
	// core sends it on every request as of 1.6.0, but this field is read
	// defensively: an absent value (a pre-1.6.0 core, or a genuinely empty
	// answer) is treated as "sale", the pre-1.6.0 behavior, never as an
	// error. A refund/return must be signed/recorded as a Rückgabe, not as
	// positive turnover — see main.go's signTransaction, which branches
	// fiskaly's receipt_type on this field.
	SaleType string `json:"sale_type"`
	// TaxInclusive / SaleDiscount / ServiceCharge (contract 1.2.0,
	// ut-docs#834). TaxInclusive is a pointer so a core that predates it
	// (absent) is told apart from an exclusive-priced sale (false); absent
	// falls back to inference. ServiceCharge is display-only since 1.5.0 —
	// core already folds it into VATBreakdown.
	TaxInclusive  *bool `json:"tax_inclusive"`
	SaleDiscount  int64 `json:"sale_discount"`
	ServiceCharge int64 `json:"service_charge"`
}

// IsReturn reports whether this request is for a refund/return, treating
// anything other than the literal "return" (including the zero value, for
// a pre-1.6.0 core or a malformed/omitted field) as an ordinary sale — the
// back-compat default the contract requires (ut-docs#1404 acceptance
// criteria: "no behavior change for a payload that omits the field").
func (r Request) IsReturn() bool { return r.SaleType == "return" }

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
// standard_v1 vat_rate enum, which mirrors DSFinV-K's USt-Schlüssel:
// 19% NORMAL, 7% REDUCED_1, 10.7% SPECIAL_RATE_1, 5.5% SPECIAL_RATE_2,
// 0% NULL. ok is false for any other rate — the caller must refuse to sign
// rather than put it in a bucket that states a different rate on an
// irreversible record (ut-docs#833 review).
//
// CONFIRMED 2026-08-18 against a live sandbox: NORMAL only. REDUCED_1 /
// NULL / SPECIAL_RATE_* are not independently proven — no reason to expect
// they are wrong, but they have not been exercised end to end.
func VATRateBucket(bp int) (string, bool) {
	switch bp {
	case 1900:
		return "NORMAL", true
	case 700:
		return "REDUCED_1", true
	case 1070:
		return "SPECIAL_RATE_1", true
	case 550:
		return "SPECIAL_RATE_2", true
	case 0:
		return "NULL", true
	default:
		return "", false
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

// ReceiptQRMaxBytes is contract 1.10.0's bound on receipt.qr_payload; core
// drops an object over it, so this plugin never sends one.
const ReceiptQRMaxBytes = 1024

// ReceiptEvidence is the generic `receipt` object an `approved` response may
// carry since contract 1.10.0 (ut-docs#2880): the QR payload core renders on
// the receipt (and on every reprint) verbatim, plus optional extra lines.
// Core no longer builds a QR itself — without this object the receipt shows
// none.
type ReceiptEvidence struct {
	QRPayload string   `json:"qr_payload,omitempty"`
	Lines     []string `json:"lines,omitempty"`
}

// Response is what this plugin writes to stdout.
type Response struct {
	Status  string           `json:"status"`
	TSE     *TSEEvidence     `json:"tse,omitempty"`
	Receipt *ReceiptEvidence `json:"receipt,omitempty"`
}

// WithReceiptQR attaches fiskaly's qr_code_data as receipt.qr_payload,
// verbatim. It is a no-op — never a placeholder — for a non-approved
// response, an empty payload, or one over ReceiptQRMaxBytes.
func (r Response) WithReceiptQR(payload string) Response {
	if r.Status != StatusApproved || strings.TrimSpace(payload) == "" || len(payload) > ReceiptQRMaxBytes {
		return r
	}
	r.Receipt = &ReceiptEvidence{QRPayload: payload}
	return r
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

// CannotSign refuses to sign a sale that would produce a receipt already
// known to be wrong (see BuildReceipt). Core completes the sale unsigned
// and declares it, exactly as for an outage, with its own wording.
func CannotSign() Response { return Response{Status: StatusCannotSign} }

// NotThisTerminal declares the sale is not this plugin's responsibility.
// Core treats it exactly like no answer — the sale proceeds with no
// marker. Explicitly NOT a failure.
func NotThisTerminal() Response { return Response{Status: StatusNotThisTerminal} }
