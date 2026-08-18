package fiskalyparse

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// realFinishedTransactionBody is the ACTUAL response body captured from a
// live fiskaly SIGN DE TEST-environment sandbox on 2026-08-18 (start →
// update → finish a real transaction, retrieved via GET). Contains no
// credentials — a TSS certificate, public key and transaction signature
// from a disposable TEST sandbox account, not secrets. This is what proved
// the original tss_tx_result-wrapper assumption wrong.
const realFinishedTransactionBody = `{"schema":{"standard_v1":{"receipt":{"amounts_per_payment_type":[{"amount":"4.20","currency_code":"EUR","payment_type":"NON_CASH"}],"amounts_per_vat_rate":[{"amount":"4.20","vat_rate":"NORMAL"}],"receipt_type":"RECEIPT"}},"raw":{"process_type":"Kassenbeleg-V1","process_data":"QmVsZWdeNC4yMF8wLjAwXzAuMDBfMC4wMF8wLjAwXjQuMjA6VW5iYXI="}},"state":"FINISHED","tss_id":"ae35a829-7bb2-4261-9490-bcf3c8a3dc5e","tss_serial_number":"2be721cf1a255afd12626533e251fe7b3e3c8af625b9f12803918a9bb1952867","client_id":"101bd1cd-286c-4c3b-9ae5-43d4fad02f08","client_serial_number":"UT-TEST-101bd1cd-286c-4c3b-9ae5-43d4fad02f08","revision":3,"latest_revision":3,"number":1,"time_start":1787078299,"time_end":1787078299,"_id":"f77891c6-d5dd-48c3-b18a-fa0f6752feb8","_type":"TRANSACTION","_env":"TEST","_version":"2.2.2","signature":{"value":"kHCYf4/f/zz+51m5XlzLtuEOFvVyAF3wrzsz+p+iyQcxzICkza8O9m/P45pRDQRWwEvxYzYZQWR3wGqEsSf2iA==","algorithm":"ecdsa-plain-SHA256","counter":27,"public_key":"BAVHkwS3tptbKRK2Z8E6a8u8N/Qkw/6gBr89NivYZciYJVpF5uQPQTpgb64I3STVhUpLVc/ZCZOz7eaBol+qUnY="},"log":{"operation":"Finish","timestamp":1787078299,"timestamp_format":"unixTime"},"qr_code_data":"V0;UT-TEST-101bd1cd-286c-4c3b-9ae5-43d4fad02f08;Kassenbeleg-V1;Beleg^4.20_0.00_0.00_0.00_0.00^4.20:Unbar;1;27;2026-08-18T18:38:19.000Z;2026-08-18T18:38:19.000Z;ecdsa-plain-SHA256;unixTime;kHCYf4/f/zz+51m5XlzLtuEOFvVyAF3wrzsz+p+iyQcxzICkza8O9m/P45pRDQRWwEvxYzYZQWR3wGqEsSf2iA==;BAVHkwS3tptbKRK2Z8E6a8u8N/Qkw/6gBr89NivYZciYJVpF5uQPQTpgb64I3STVhUpLVc/ZCZOz7eaBol+qUnY="}`

func TestParseSignResponse_RealCapturedBody(t *testing.T) {
	sig, logTime := ParseSignResponse([]byte(realFinishedTransactionBody))
	const wantSig = "kHCYf4/f/zz+51m5XlzLtuEOFvVyAF3wrzsz+p+iyQcxzICkza8O9m/P45pRDQRWwEvxYzYZQWR3wGqEsSf2iA=="
	if sig != wantSig {
		t.Fatalf("signature = %q, want %q", sig, wantSig)
	}
	if logTime != "1787078299" {
		t.Fatalf("logTime = %q, want %q", logTime, "1787078299")
	}
}

// Pins the regression: the ORIGINAL (buggy) implementation read the
// signature from resp.tss_tx_result.signature.value, a wrapper that does
// NOT exist in fiskaly's real response — it would have silently discarded
// every real signature fiskaly ever returned. If this test ever starts
// seeing a non-empty signature, someone reintroduced reading from that
// wrong shape.
func TestParseSignResponse_LegacyWrapperShapeDoesNotParse(t *testing.T) {
	legacy := `{"tss_tx_result":{"signature":{"value":"SHOULD-NOT-PARSE"},"log_time":"123"}}`
	sig, logTime := ParseSignResponse([]byte(legacy))
	if sig != "" || logTime != "" {
		t.Fatalf("legacy tss_tx_result-wrapped body must not parse as a signature, got sig=%q logTime=%q", sig, logTime)
	}
}

func TestParseSignResponse_MissingLog(t *testing.T) {
	sig, logTime := ParseSignResponse([]byte(`{"signature":{"value":"SIG=="}}`))
	if sig != "SIG==" {
		t.Fatalf("signature = %q, want SIG==", sig)
	}
	if logTime != "" {
		t.Fatalf("logTime = %q, want empty when log is absent", logTime)
	}
}

func TestParseSignResponse_UnparseableBody(t *testing.T) {
	sig, logTime := ParseSignResponse([]byte(`not json`))
	if sig != "" || logTime != "" {
		t.Fatalf("unparseable body must return empty, got sig=%q logTime=%q", sig, logTime)
	}
}

// Regression test for review finding #2 (2026-08-18): a log.timestamp shape
// this function doesn't specifically expect must NEVER take the signature
// down with it. The first draft of this fix decoded Timestamp as a fixed
// int64 in the SAME struct as Signature, so a non-integer timestamp made
// the whole unmarshal fail and silently discarded a real signature fiskaly
// had already returned.
func TestParseSignResponse_TimestampShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"unixTime as JSON integer (the only shape observed live)", `{"signature":{"value":"SIG=="},"log":{"timestamp":1755500000}}`, "1755500000"},
		{"a string-shaped timestamp (utcTime/generalizedTime per fiskaly's documented enum)", `{"signature":{"value":"SIG=="},"log":{"timestamp":"2026-08-18T18:38:19Z"}}`, "2026-08-18T18:38:19Z"},
		{"a null timestamp", `{"signature":{"value":"SIG=="},"log":{"timestamp":null}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig, logTime := ParseSignResponse([]byte(tc.body))
			if sig != "SIG==" {
				t.Fatalf("signature discarded (got %q) — a log.timestamp shape must never take the signature down with it", sig)
			}
			if logTime != tc.want {
				t.Fatalf("logTime = %q, want %q", logTime, tc.want)
			}
		})
	}
}

// Turns the exact class of bug the manifest.json permission fix addressed
// (a base-URL host with no matching net:<host> permission → every fiskaly
// call denied) into something CI catches forever, not just this one time.
// universal-till's permission check (internal/plugins/permissions.go) is an
// exact string match against "net:"+hostname, or the "net:*" wildcard.
func TestSignDEBaseHostCoveredByManifestPermission(t *testing.T) {
	u, err := url.Parse(SignDEBase)
	if err != nil {
		t.Fatalf("parse SignDEBase: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var m struct {
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest.json: %v", err)
	}
	want := "net:" + u.Hostname()
	for _, p := range m.Permissions {
		if p == want || p == "net:*" {
			return
		}
	}
	t.Fatalf("SignDEBase host %q is not covered by any declared permission in manifest.json (want %q), got %v — a fiskaly call would be silently denied at runtime", u.Hostname(), want, m.Permissions)
}
