// Package fiskalyparse holds fiskaly SIGN DE endpoint constants and response
// parsing that need to be host-testable — deliberately NOT wasip1-tagged,
// same reason src/datev isn't: src/main.go's //go:build wasip1 tag excludes
// it from ordinary `go test ./...` on the host, so any logic that stays
// there is provably untested by CI, not just untested today. Extracted
// 2026-08-18 during code review of the signDEBase/parseSignResponse fix,
// after the review found a second live bug (see ParseSignResponse's doc
// comment) that a table test here would have caught immediately.
package fiskalyparse

import (
	"encoding/json"
	"strconv"
	"time"
)

// SignDEBase is fiskaly's Cloud-TSE ("SIGN DE") API, the KassenSichV
// TSE-signing product.
//
// CONFIRMED 2026-08-18 against a real fiskaly TEST-environment sandbox: this
// exact host + path prefix returned a valid bearer token from POST
// {SignDEBase}/auth, and the full TSS/client/transaction lifecycle worked
// end-to-end, producing a real ECDSA-signed transaction. Cross-checked
// against fiskaly's official docs (workspace.fiskaly.com/api/sign-de/),
// which name kassensichv-middleware.fiskaly.com as the primary base URL.
// The previous value here, kassensichv.io, is DEAD — it 404s (plain nginx,
// not even fiskaly's app) on every path including root; it is not fiskaly's
// API anymore.
const SignDEBase = "https://kassensichv-middleware.fiskaly.com/api/v2"

// DsfinvkBase is fiskaly's DSFinV-K export API. Documented at
// developer.fiskaly.com/dsfinvk.
//
// NEEDS SANDBOX VERIFICATION, unchanged by the 2026-08-18 SIGN DE fix: this
// still points at kassensichv.io, now confirmed DEAD (see SignDEBase's
// comment) — so this is almost certainly wrong too, but NOT changed here,
// because fiskaly's docs describe DSFinV-K as possibly a genuinely separate
// API (cash_register/cashPointClosing concepts) that may live on a
// different host entirely from SIGN DE's kassensichv-middleware.fiskaly.com
// — fiskaly's own SIGN DE reference page documents no separate DSFinV-K
// host at all, which is a hint but not proof either way. Guessing wrong
// here would be worse than leaving the existing, already-flagged-wrong
// value; whoever verifies the real host next must also add its permission
// to manifest.json (removed for kassensichv.io by the 2026-08-18 fix, since
// that host is dead regardless of which API needs a new one).
const DsfinvkBase = "https://kassensichv.io/api/v1/dsfinvk"

// ParseSignResponse pulls the signature and log timestamp out of a
// FINISHED transaction response.
//
// CONFIRMED 2026-08-18 against a real fiskaly sandbox response: the
// signature is a TOP-LEVEL `signature.value` field, NOT nested under a
// `tss_tx_result` wrapper as an earlier version of this function
// (incorrectly) assumed — that wrapper does not exist in fiskaly's real
// response and would have silently discarded every real signature even
// once SignDEBase pointed at a working host. See parse_test.go's
// TestParseSignResponse_LegacyWrapperShapeDoesNotParse, which pins this as
// a permanent regression.
//
// Signature and log-timestamp extraction are deliberately two INDEPENDENT
// json.Unmarshal calls against the same body, not one combined struct.
// Code review (2026-08-18) found the first version of this fix decoded
// `log.timestamp` as a fixed int64 in the SAME struct as the signature — so
// any timestamp shape that isn't a bare JSON integer (fiskaly's own
// log.timestamp_format field documents unixTime as only one of several
// possible values; the others are very plausibly strings) made the WHOLE
// unmarshal fail and threw away a real signature fiskaly had already
// returned, over a field this function doesn't even need for correctness.
// Decoding them independently means a timestamp shape this function
// doesn't recognize can never take the signature down with it — worst case
// logTime comes back as the raw JSON literal instead of a cleanly
// formatted string. See parse_test.go's TestParseSignResponse_TimestampShapes.
func ParseSignResponse(body []byte) (signature, logTime string) {
	var sigResp struct {
		Signature struct {
			Value string `json:"value"`
		} `json:"signature"`
	}
	if err := json.Unmarshal(body, &sigResp); err != nil {
		return "", ""
	}
	signature = sigResp.Signature.Value

	var logResp struct {
		Log struct {
			Timestamp json.RawMessage `json:"timestamp"`
		} `json:"log"`
	}
	if err := json.Unmarshal(body, &logResp); err == nil && hasTimestampValue(logResp.Log.Timestamp) {
		logTime = formatTimestamp(logResp.Log.Timestamp)
	}
	return signature, logTime
}

// hasTimestampValue reports whether raw is an absent or JSON-null timestamp
// — both must produce an empty logTime, not "0" (json.Unmarshal into an
// int64 target treats `null` as a no-op, leaving the zero value with no
// error — caught by TestParseSignResponse_TimestampShapes/a_null_timestamp).
func hasTimestampValue(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// formatTimestamp renders a raw JSON `log.timestamp` value as a string,
// without assuming its shape. unixTime is the only value
// log.timestamp_format has been observed to carry live (2026-08-18); the
// enum documented by fiskaly also allows other formats, which would decode
// as a JSON string rather than a number. Try both rather than guess one.
func formatTimestamp(raw json.RawMessage) string {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return strconv.FormatInt(n, 10)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Neither a plain integer nor a string — return the raw JSON literal
	// rather than silently dropping it. Better a slightly unformatted value
	// than an empty one for an audit-trail field.
	return string(raw)
}

// SignEvidence is the full §6 KassenSichV receipt evidence carried on a
// FINISHED transaction response.
//
// Separate from ParseSignResponse deliberately. That function predates this
// one, returns the log timestamp as fiskaly's raw unixTime, and feeds this
// plugin's internal `tse_result:*` audit records — changing its output would
// change records already written. This one is the WIRE view, used to answer
// core's fiscal.sign.ask, where the contract requires RFC3339
// (ut-docs/reference/contracts/fiscal-sign-ask.md v1.1.0).
type SignEvidence struct {
	TransactionNumber  int64
	SignatureCounter   int64
	SerialNumber       string
	StartTime          string // RFC3339
	LogTime            string // RFC3339
	Signature          string
	SignatureAlgorithm string
}

// ParseSignEvidence extracts every evidence field a §6 receipt needs from a
// FINISHED transaction body.
//
// Field paths are taken from a REAL sandbox response captured 2026-08-18
// (see parse_test.go's realFinishedTransactionBody), not from documentation
// and not guessed:
//
//	number                → TransactionNumber
//	tss_serial_number     → SerialNumber   (on the transaction, not only the TSS)
//	time_start            → StartTime
//	log.timestamp         → LogTime
//	signature.value       → Signature
//	signature.counter     → SignatureCounter
//	signature.algorithm   → SignatureAlgorithm
//
// Every field is independently optional: an absent one stays zero rather
// than failing the whole parse, matching the contract's "every tse field is
// individually optional" rule. A partially-populated evidence object is
// valid; a fabricated one is not.
func ParseSignEvidence(body []byte) SignEvidence {
	var r struct {
		Number          int64           `json:"number"`
		TSSSerialNumber string          `json:"tss_serial_number"`
		TimeStart       json.RawMessage `json:"time_start"`
		Signature       struct {
			Value     string `json:"value"`
			Counter   int64  `json:"counter"`
			Algorithm string `json:"algorithm"`
		} `json:"signature"`
		Log struct {
			Timestamp json.RawMessage `json:"timestamp"`
		} `json:"log"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return SignEvidence{}
	}
	ev := SignEvidence{
		TransactionNumber:  r.Number,
		SignatureCounter:   r.Signature.Counter,
		SerialNumber:       r.TSSSerialNumber,
		Signature:          r.Signature.Value,
		SignatureAlgorithm: r.Signature.Algorithm,
	}
	if hasTimestampValue(r.Log.Timestamp) {
		ev.LogTime = timestampRFC3339(r.Log.Timestamp)
	}
	if hasTimestampValue(r.TimeStart) {
		ev.StartTime = timestampRFC3339(r.TimeStart)
	}
	return ev
}

// timestampRFC3339 renders fiskaly's unixTime (or an already-formatted
// string) as RFC3339 UTC.
//
// This matters on a receipt, not just in a struct: core prints the value
// verbatim ("TSE transaction end: <value>"), so a raw epoch would reach a
// German customer's receipt and a tax audit as an unreadable integer.
// fiskaly renders this same value the same way in its own qr_code_data.
func timestampRFC3339(raw json.RawMessage) string {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return time.Unix(n, 0).UTC().Format(time.RFC3339)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s // already a formatted timestamp
	}
	return ""
}
