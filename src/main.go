//go:build wasip1

// Germany fiscal compliance — TSE signing (fiskaly Cloud-TSE / "SIGN DE" API)
// + DSFinV-K export (fiskaly DSFinV-K API). A WASI command (GOOS=wasip1
// GOARCH=wasm) the till runs in-process, per ut-docs ADR-0025 ("Country-
// specific tax rates and fiscal compliance") and ADR-0002's `tax`/`export`
// canonical types.
//
// STATUS — read before relying on this for anything real:
//   - The endpoint paths/request shapes below follow fiskaly's PUBLICLY
//     DOCUMENTED API (developer.fiskaly.com, kassensichv.net,
//     kassensichv.io) as researched 2026-07-28. They have NOT been run
//     against a real fiskaly sandbox/production account — no credentials
//     were available at write time. Every endpoint below is flagged
//     "NEEDS SANDBOX VERIFICATION" in its doc comment, same convention as
//     ut-plugin-payment-sumup's unverified reader-checkout path.
//   - DSFinV-K export format/content compliance has NOT been legally
//     verified against KassenSichV. Cash-point-closing generation (the
//     aggregation step DSFinV-K exports actually depend on) is NOT
//     implemented — see exportDSFinVK below. Do not treat a successful
//     export call as a compliant one.
//   - This is a skeleton, not a certified compliance solution. Real legal/
//     tax-advisor sign-off is required before any merchant relies on this
//     plugin for a live business. See README.md.
//
// OFFLINE-FIRST TENSION (ADR-0025's flagged-but-unresolved open question):
// `sale.completed` is dispatched NON-BLOCKING and fires AFTER the sale has
// already completed (confirmed against universal-till's
// internal/plugins/ipc.go / wasm_runtime.go: non-blocking events are
// enqueued to a drainer goroutine whose result is discarded — a plugin
// error here is logged, never retried, never surfaced to the till operator
// or any UI). Concretely: this plugin has NO architectural way to block or
// reverse a sale if fiskaly is unreachable — by the time it runs, the till
// has already completed the sale per ADR-0003 (offline-first, non-
// negotiable). What it CAN do, and does: never fabricate a signature. A
// failed sign attempt is logged loudly and the sale is recorded as
// "unsigned, pending retry" in plugin storage (queued the same way
// ut-plugin-integration-webhook queues undelivered sales) instead of being
// silently marked as compliant. Whether an unsigned-then-backfilled sale
// satisfies KassenSichV's "irreversible, tamper-proof at time of
// transaction" requirement is exactly the question ADR-0025 flags as
// needing real TSE-vendor/legal confirmation — NOT decided or resolved by
// this code.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unsafe"
)

// --- host functions (module "ut", see ut-docs reference/plugin-host-functions.md) ---
// Buffer ABI: data-returning calls write min(len, dstCap) bytes into dst and
// return the FULL length; a guest seeing len > cap retries with a bigger
// buffer. Negative returns are host errors: -1 not found, -2 denied,
// -3 internal, -4 invalid.

//go:wasmimport ut log_write
func logWrite(ptr, n uint32)

//go:wasmimport ut settings_get
func settingsGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut http_request
func httpRequest(rPtr, rLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut storage_get
func storageGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut storage_set
func storageSet(kPtr, kLen, vPtr, vLen uint32) int32

// signDEBase is fiskaly's Cloud-TSE ("SIGN DE") API, the KassenSichV
// TSE-signing product. Publicly documented at developer.fiskaly.com /
// kassensichv.net; historically hosted at kassensichv.io/api/v1, with a v2
// also referenced in fiskaly's docs (kassensichv.fiskaly.com/api/v2).
//
// NEEDS SANDBOX VERIFICATION: confirm the exact current production host +
// API version against the merchant's fiskaly dashboard/workspace before
// going live — fiskaly has had multiple entry points (kassensichv.io
// legacy, kassensichv.fiskaly.com v2, workspace.fiskaly.com newer portal)
// and this was not confirmed against a live account.
const signDEBase = "https://kassensichv.io/api/v2"

// dsfinvkBase is fiskaly's DSFinV-K export API. Documented at
// developer.fiskaly.com/dsfinvk — same host family as SIGN DE.
//
// NEEDS SANDBOX VERIFICATION: same caveat as signDEBase.
const dsfinvkBase = "https://kassensichv.io/api/v1/dsfinvk"

// dsfinvkExportEntryKey must match manifest.json's entries[].key for the
// "export" entry. The host resolves export.requested.ask by plugin id, so
// this plugin (declaring exactly one export entry) is already only ever
// asked on its own behalf — this check is defense-in-depth for the day
// this plugin ships a second export entry with a different key, not a fix
// for cross-plugin routing (that's the host's job, see universal-till's
// EventBus.AskPlugin).
const dsfinvkExportEntryKey = "dsfinvk-export-de"

const (
	unsignedQueueKey = "unsigned_queue" // storage key: sales that failed to sign, pending retry
	maxQueue         = 200              // bounded, oldest dropped past this (mirrors ut-plugin-integration-webhook)
	tokenStorageKey  = "fiskaly_token"  // cached {access_token, obtained_at}
)

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func logf(format string, args ...any) {
	msg := []byte(fmt.Sprintf(format, args...))
	p, n := ptrOf(msg)
	logWrite(p, n)
}

// callBuf runs a data-returning host call, honoring the buffer ABI (grow +
// retry once if the first buffer was too small). Same pattern as the
// webhook/sumup plugins.
func callBuf(fn func(dstPtr, dstCap uint32) int32) ([]byte, int32) {
	buf := make([]byte, 8192)
	p, c := ptrOf(buf)
	n := fn(p, c)
	if n < 0 {
		return nil, n
	}
	if int(n) > len(buf) {
		buf = make([]byte, n)
		p, c = ptrOf(buf)
		n = fn(p, c)
		if n < 0 {
			return nil, n
		}
		if int(n) > len(buf) {
			n = int32(len(buf))
		}
	}
	return buf[:n], n
}

func setting(key string) string {
	kb := []byte(key)
	out, code := callBuf(func(dp, dc uint32) int32 {
		kp, kl := ptrOf(kb)
		return settingsGet(kp, kl, dp, dc)
	})
	if code < 0 {
		return ""
	}
	return string(out)
}

func storageRead(key string) ([]byte, bool) {
	kb := []byte(key)
	out, code := callBuf(func(dp, dc uint32) int32 {
		kp, kl := ptrOf(kb)
		return storageGet(kp, kl, dp, dc)
	})
	if code < 0 {
		return nil, false
	}
	return out, true
}

func storagePut(key string, v []byte) {
	kp, kl := ptrOf([]byte(key))
	vp, vl := ptrOf(v)
	if code := storageSet(kp, kl, vp, vl); code != 0 {
		logf("tax-de: storage_set(%s) failed, code=%d", key, code)
	}
}

// httpCall performs one outbound HTTP call through the host's http_request
// function (permission-gated by `net:kassensichv.io` in manifest.json — see
// universal-till/internal/plugins/wasm_hostfns.go hostHTTPRequest). ok is
// false only on a host/transport failure; a non-2xx HTTP status still
// returns ok=true with status/body set.
func httpCall(method, url string, headers map[string]string, jsonBody []byte) (body []byte, status int, ok bool) {
	reqJSON, _ := json.Marshal(map[string]any{
		"method":   method,
		"url":      url,
		"headers":  headers,
		"body_b64": base64.StdEncoding.EncodeToString(jsonBody),
	})
	respBuf, code := callBuf(func(dp, dc uint32) int32 {
		rp, rl := ptrOf(reqJSON)
		return httpRequest(rp, rl, dp, dc)
	})
	if code < 0 {
		return nil, 0, false
	}
	var httpResp struct {
		Status  int    `json:"status"`
		BodyB64 string `json:"body_b64"`
	}
	_ = json.Unmarshal(respBuf, &httpResp)
	b, _ := base64.StdEncoding.DecodeString(httpResp.BodyB64)
	return b, httpResp.Status, true
}

// --- fiskaly auth (shared by SIGN DE and DSFinV-K — same fiskaly account) ---

type cachedToken struct {
	AccessToken string `json:"access_token"`
	ObtainedAt  int64  `json:"obtained_at"` // unix seconds
}

// fiskalyAuth returns a bearer access token, reusing a cached one from
// plugin storage when present. fiskaly's JWTs are short-lived (documented
// as needing periodic refresh); this skeleton re-authenticates from
// api_key/api_secret every time the cache is empty or a call 401s, rather
// than implementing the refresh_token rotation flow fiskaly's SDKs use —
// simpler and correct, just does one extra auth round-trip more often than
// necessary. NEEDS SANDBOX VERIFICATION: token TTL, and whether /auth is
// the exact current path (fiskaly's SDKs abstract this; verified only
// against public docs, not a live account).
func fiskalyAuth(apiKey, apiSecret string) (string, bool) {
	if raw, ok := storageRead(tokenStorageKey); ok {
		var t cachedToken
		if err := json.Unmarshal(raw, &t); err == nil && t.AccessToken != "" {
			if time.Now().Unix()-t.ObtainedAt < 5*60 { // conservative 5-minute reuse window
				return t.AccessToken, true
			}
		}
	}
	reqBody, _ := json.Marshal(map[string]string{
		"api_key":    apiKey,
		"api_secret": apiSecret,
	})
	body, status, ok := httpCall("POST", signDEBase+"/auth", map[string]string{"Content-Type": "application/json"}, reqBody)
	if !ok || status < 200 || status >= 300 {
		logf("tax-de: fiskaly auth failed (status=%d ok=%v)", status, ok)
		return "", false
	}
	var authResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &authResp); err != nil || authResp.AccessToken == "" {
		logf("tax-de: fiskaly auth response unparseable")
		return "", false
	}
	storagePut(tokenStorageKey, mustJSON(cachedToken{AccessToken: authResp.AccessToken, ObtainedAt: time.Now().Unix()}))
	return authResp.AccessToken, true
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func authHeader(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
}

// --- till event contract (mirrors internal/plugins/ipc.go's SaleCompletedEvent) ---

type saleLineItem struct {
	Quantity       float64 `json:"quantity"`
	UnitPriceCents int64   `json:"unit_price_cents"`
	TaxRateBP      int     `json:"tax_rate_bp"`
	TotalCents     int64   `json:"total_cents"`
}

type salePayment struct {
	Method      string `json:"method"`
	AmountCents int64  `json:"amount_cents"`
}

type saleCompletedEvent struct {
	SaleID      string         `json:"sale_id"`
	ReceiptNo   string         `json:"receipt_no"`
	SaleType    string         `json:"sale_type"`
	Currency    string         `json:"currency"`
	TotalCents  int64          `json:"total_cents"`
	Payments    []salePayment  `json:"payments"`
	LineItems   []saleLineItem `json:"line_items"`
	CompletedAt time.Time      `json:"completed_at"`
}

// --- TSE signing (SIGN DE API) ---

// vatRateBucket maps a basis-point VAT rate to fiskaly's SIGN DE
// `standard_v1` schema vat_rate enum. Germany's rates today: 19% (NORMAL),
// 7% (REDUCED_1). Anything else falls back to SPECIAL_RATE_1 rather than
// guessing further — fiskaly's enum also has REDUCED_2/NULL/SPECIAL_RATE_2-5
// for cases (e.g. 0%) this skeleton doesn't attempt to classify.
//
// NEEDS SANDBOX VERIFICATION: the standard_v1 schema shape (amounts_per_
// vat_rate / amounts_per_payment_type field names and this enum) is
// reconstructed from fiskaly's public documentation and support articles,
// not confirmed against a live sandbox response.
func vatRateBucket(bp int) string {
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

// paymentTypeBucket maps the till's free-text payment method to fiskaly's
// CASH/NON_CASH split (that's the granularity the TSE schema actually
// cares about — everything that isn't physical cash is NON_CASH).
func paymentTypeBucket(method string) string {
	if strings.EqualFold(method, "cash") {
		return "CASH"
	}
	return "NON_CASH"
}

func minorToDecimalString(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// txID derives a fiskaly transaction id from the sale id. SIGN DE's
// /tss/{tss_id}/tx/{tx_id} path segment must be a UUID in fiskaly's docs
// examples; the till's sale_id is not guaranteed to be one, so this hashes
// it into one deterministically (stable per sale, no extra state needed).
// NEEDS SANDBOX VERIFICATION: whether tx_id must strictly be a v4 UUID or
// any unique string is accepted — public docs use UUIDs in every example.
func txID(saleID string) string {
	// Deterministic pseudo-UUID-shaped id from saleID — NOT cryptographic,
	// just a stable, path-safe identifier derived from the sale.
	h := fnv1a(saleID)
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", uint32(h), h&0xFFFFFFFFFFFF)
}

func fnv1a(s string) uint64 {
	var h uint64 = 0xcbf29ce484222325
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 0x100000001b3
	}
	return h
}

type tseSignResult struct {
	SaleID        string    `json:"sale_id"`
	Signed        bool      `json:"signed"`
	TxID          string    `json:"tx_id,omitempty"`
	SignatureB64  string    `json:"signature,omitempty"`
	LogTime       string    `json:"log_time,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	AttemptedAt   time.Time `json:"attempted_at"`
}

// signTransaction attempts one real TSE-sign round-trip against fiskaly's
// SIGN DE API: start the transaction (state ACTIVE), then finish it (state
// FINISHED) with the receipt schema. Returns signed=false — NEVER a
// fabricated signature — on any auth, network, or non-2xx failure. See the
// package doc comment for why a failure here cannot block the sale.
//
// NEEDS SANDBOX VERIFICATION: the ACTIVE/FINISHED two-call lifecycle with
// an incrementing tx_revision query param is documented by fiskaly support
// articles; the exact response envelope for a FINISHED transaction
// (signature/log_time/signature_counter field names) was not confirmed
// against a live account — parseSignResponse below is best-effort.
func signTransaction(sale saleCompletedEvent, apiKey, apiSecret, tssID, clientID string) tseSignResult {
	result := tseSignResult{SaleID: sale.SaleID, AttemptedAt: time.Now().UTC()}

	if tssID == "" || clientID == "" {
		result.FailureReason = "not_configured: fiskaly_tss_id/fiskaly_client_id not set"
		return result
	}
	token, ok := fiskalyAuth(apiKey, apiSecret)
	if !ok {
		result.FailureReason = "fiskaly_auth_failed"
		return result
	}

	id := txID(sale.SaleID)
	result.TxID = id
	base := fmt.Sprintf("%s/tss/%s/tx/%s", signDEBase, tssID, id)

	// 1. Start the transaction.
	startBody, _ := json.Marshal(map[string]any{
		"state":     "ACTIVE",
		"client_id": clientID,
	})
	_, status, ok := httpCall("PUT", base+"?tx_revision=1", authHeader(token), startBody)
	if !ok || status < 200 || status >= 300 {
		result.FailureReason = fmt.Sprintf("start_failed status=%d ok=%v", status, ok)
		return result
	}

	// 2. Finish the transaction with the receipt schema (vat-rate and
	//    payment-type breakdown built from the real sale line items/payments).
	vatBuckets := map[string]int64{}
	for _, li := range sale.LineItems {
		vatBuckets[vatRateBucket(li.TaxRateBP)] += li.TotalCents
	}
	payBuckets := map[string]int64{}
	for _, p := range sale.Payments {
		payBuckets[paymentTypeBucket(p.Method)] += p.AmountCents
	}
	amountsPerVAT := make([]map[string]string, 0, len(vatBuckets))
	for rate, cents := range vatBuckets {
		amountsPerVAT = append(amountsPerVAT, map[string]string{"vat_rate": rate, "amount": minorToDecimalString(cents)})
	}
	amountsPerPayment := make([]map[string]string, 0, len(payBuckets))
	for typ, cents := range payBuckets {
		amountsPerPayment = append(amountsPerPayment, map[string]string{"payment_type": typ, "amount": minorToDecimalString(cents)})
	}
	receiptType := "RECEIPT"
	if sale.SaleType == "return" {
		receiptType = "RECEIPT_0104" // best-effort: fiskaly's return/cancellation receipt type code, NOT confirmed
	}
	finishBody, _ := json.Marshal(map[string]any{
		"state":     "FINISHED",
		"client_id": clientID,
		"schema": map[string]any{
			"standard_v1": map[string]any{
				"receipt": map[string]any{
					"receipt_type":             receiptType,
					"amounts_per_vat_rate":     amountsPerVAT,
					"amounts_per_payment_type": amountsPerPayment,
				},
			},
		},
	})
	body, status, ok := httpCall("PUT", base+"?tx_revision=2", authHeader(token), finishBody)
	if !ok || status < 200 || status >= 300 {
		result.FailureReason = fmt.Sprintf("finish_failed status=%d ok=%v", status, ok)
		return result
	}

	sig, logTime := parseSignResponse(body)
	if sig == "" {
		result.FailureReason = "finish_response_missing_signature"
		return result
	}
	result.Signed = true
	result.SignatureB64 = sig
	result.LogTime = logTime
	return result
}

// parseSignResponse pulls the signature out of a FINISHED transaction
// response. NEEDS SANDBOX VERIFICATION — see signTransaction's doc comment.
func parseSignResponse(body []byte) (signature, logTime string) {
	var resp struct {
		TSSTXResult struct {
			Signature struct {
				Value string `json:"value"`
			} `json:"signature"`
			LogTime string `json:"log_time"`
		} `json:"tss_tx_result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", ""
	}
	return resp.TSSTXResult.Signature.Value, resp.TSSTXResult.LogTime
}

// --- unsigned-sale retry queue (mirrors ut-plugin-integration-webhook's delivery queue) ---

func loadUnsignedQueue() []json.RawMessage {
	raw, ok := storageRead(unsignedQueueKey)
	if !ok || len(raw) == 0 {
		return nil
	}
	var q []json.RawMessage
	if err := json.Unmarshal(raw, &q); err != nil {
		return nil
	}
	return q
}

func saveUnsignedQueue(q []json.RawMessage) {
	if len(q) > maxQueue {
		q = q[len(q)-maxQueue:]
	}
	storagePut(unsignedQueueKey, mustJSON(q))
}

// handleSaleCompleted is the `sale.completed` hook body. It NEVER blocks or
// reverses the sale (impossible from this seam, see package doc comment) —
// its only job is: attempt a real sign, and if that fails, fail loudly
// (log + queue) rather than pretend success.
func handleSaleCompleted(raw []byte) {
	apiKey := strings.TrimSpace(setting("fiskaly_api_key"))
	apiSecret := strings.TrimSpace(setting("fiskaly_api_secret"))
	tssID := strings.TrimSpace(setting("fiskaly_tss_id"))
	clientID := strings.TrimSpace(setting("fiskaly_client_id"))

	if apiKey == "" || apiSecret == "" {
		logf("tax-de: not configured (fiskaly_api_key/fiskaly_api_secret empty) — sale NOT signed, skipping")
		os.Exit(0)
	}

	// 1. Retry previously-unsigned sales first (same pattern as the webhook
	//    plugin's queue flush).
	var stillUnsigned []json.RawMessage
	for _, p := range loadUnsignedQueue() {
		var s saleCompletedEvent
		if err := json.Unmarshal(p, &s); err != nil {
			continue
		}
		res := signTransaction(s, apiKey, apiSecret, tssID, clientID)
		recordResult(res)
		if !res.Signed {
			stillUnsigned = append(stillUnsigned, p)
		} else {
			logf("tax-de: retry signed previously-unsigned sale %s", s.SaleID)
		}
	}

	// 2. Handle the current sale.
	var ev struct {
		Payload json.RawMessage `json:"payload"`
	}
	_ = json.Unmarshal(raw, &ev)
	var sale saleCompletedEvent
	if len(ev.Payload) > 0 {
		_ = json.Unmarshal(ev.Payload, &sale)
	}
	if sale.SaleID != "" {
		res := signTransaction(sale, apiKey, apiSecret, tssID, clientID)
		recordResult(res)
		if res.Signed {
			logf("tax-de: SIGNED sale %s tx=%s", sale.SaleID, res.TxID)
		} else {
			logf("tax-de: UNSIGNED sale %s (%s) — queued for retry, fiskaly was NOT reached successfully", sale.SaleID, res.FailureReason)
			stillUnsigned = append(stillUnsigned, ev.Payload)
		}
	}

	saveUnsignedQueue(stillUnsigned)

	// Non-blocking hook contract (same as ut-plugin-integration-webhook):
	// this exit code cannot affect a sale that has already completed. It is
	// NOT a signal of "signed successfully" — that is recordResult's job.
	os.Exit(0)
}

// taxRateAskPayload mirrors universal-till's internal/pages/tax_hook.go —
// the till's core has NO built-in notion of §12 UStG's dine-in/takeaway
// VAT switch; this plugin is where that rule actually lives.
type taxRateAskPayload struct {
	ItemID    string `json:"item_id"`
	TaxCodeID string `json:"tax_code_id"`
	TaxRateBP int    `json:"tax_rate_bp"`
	OrderType string `json:"order_type"`
}

// handleTaxRateAsk answers the "tax.rate.ask" hook (EventBus.Ask — a
// blocking, value-returning hook; see universal-till's
// internal/plugins/ipc.go doc comment). Writing valid JSON to stdout is the
// answer; writing nothing means "no opinion on this line," and the till
// falls back to the line's own configured rate.
//
// The actual rule — which tax codes switch to which reduced rate on
// takeaway — is merchant-configured via the takeaway_rate_overrides
// setting (a JSON object, tax_code_id → basis points), NOT hardcoded here:
// a real German café's catalog varies (e.g. only some drinks, not food),
// and this plugin has no way to know a shop's own tax-code IDs in advance.
// There is currently no dedicated settings UI for this beyond editing the
// JSON value directly (same pre-existing gap noted in universal-till's
// docs/code-reviews/2026-07-28-order-type-tax-switching.md, now on the
// plugin side instead of core's) — a real follow-up, not built here.
func handleTaxRateAsk(raw []byte) {
	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	_ = json.Unmarshal(raw, &wrapper)
	var ask taxRateAskPayload
	_ = json.Unmarshal(wrapper.Payload, &ask)

	if ask.OrderType != "takeaway" {
		os.Exit(0) // dine-in/standard: this plugin has no opinion, use the line's own rate
	}

	overrides := map[string]int{}
	if raw := strings.TrimSpace(setting("takeaway_rate_overrides")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
			logf("tax-de: takeaway_rate_overrides setting is not valid JSON: %v", err)
			os.Exit(0)
		}
	}

	bp, ok := overrides[ask.TaxCodeID]
	if !ok || bp <= 0 {
		os.Exit(0) // no override configured for this tax code — stays pinned to its own rate
	}

	fmt.Print(string(mustJSON(map[string]int{"rate_bp": bp})))
	os.Exit(0)
}

// recordResult persists the sign attempt's outcome (signed or not) keyed by
// sale id, so a future report/reconciliation surface can enumerate unsigned
// sales. Deliberately separate from the retry queue: this is a permanent
// audit record, the queue is transient retry state.
func recordResult(res tseSignResult) {
	storagePut("tse_result:"+res.SaleID, mustJSON(res))
}

// --- DSFinV-K export (fiskaly DSFinV-K API) ---
//
// Reachable from the till since ut-docs#189: a manager's Data/Export page
// action dispatches the generic, blocking `export.requested.ask` event
// (universal-till internal/pages/data_api.go → internal/plugins EventBus);
// this plugin declares that event in manifest.json's hooks[] and answers it
// below in main(), the same "write JSON to stdout" convention
// handleTaxRateAsk already uses for tax.rate.ask.
//
// ALSO NOT COMPLETE even on the fiskaly side: a real DSFinV-K export
// requires cash_point_closing records (period aggregates of every signed
// transaction) to exist before /export is triggered — this skeleton does
// NOT build those from the tse_result:* records recorded above. Calling
// exportDSFinVK today would trigger an export against whatever (likely
// empty) closings already exist in the fiskaly account, not a real export
// of this till's data. That aggregation step is real, non-trivial work
// left for whoever takes this further — flagged here and in the README so
// it isn't discovered late.
func exportDSFinVK(fromDate, toDate string) (ok bool, downloadInfo string) {
	apiKey := strings.TrimSpace(setting("fiskaly_api_key"))
	apiSecret := strings.TrimSpace(setting("fiskaly_api_secret"))
	cashRegisterID := strings.TrimSpace(setting("fiskaly_cash_register_id"))
	format := strings.ToUpper(strings.TrimSpace(setting("dsfinvk_export_format")))
	if format == "" {
		format = "ZIP"
	}
	if apiKey == "" || apiSecret == "" || cashRegisterID == "" {
		logf("tax-de: dsfinvk export not configured (api key/secret/cash_register_id)")
		return false, ""
	}
	token, ok := fiskalyAuth(apiKey, apiSecret)
	if !ok {
		logf("tax-de: dsfinvk export: fiskaly auth failed")
		return false, ""
	}

	// Trigger the export. NEEDS SANDBOX VERIFICATION: exact field names
	// (`by`, `format`, date field names) reconstructed from fiskaly's public
	// DSFinV-K docs ("ByBusinessDate"/"ByCreationDate" selection, TAR/ZIP
	// format), not confirmed against a live response.
	reqBody, _ := json.Marshal(map[string]any{
		"cash_register_id": cashRegisterID,
		"by":               "BY_BUSINESS_DATE",
		"from":             fromDate,
		"to":               toDate,
		"format":           format,
	})
	body, status, ok := httpCall("POST", dsfinvkBase+"/export", authHeader(token), reqBody)
	if !ok || status < 200 || status >= 300 {
		logf("tax-de: dsfinvk export trigger failed status=%d ok=%v", status, ok)
		return false, ""
	}
	var exportResp struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	_ = json.Unmarshal(body, &exportResp)
	if exportResp.ID == "" {
		logf("tax-de: dsfinvk export response missing id")
		return false, ""
	}
	logf("tax-de: dsfinvk export triggered id=%s state=%s (poll GET %s/export/%s for completion — not implemented here, generating can take up to an hour per fiskaly docs)", exportResp.ID, exportResp.State, dsfinvkBase, exportResp.ID)
	return true, exportResp.ID
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var ev struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &ev)

	switch {
	case ev.Type == "sale.completed":
		handleSaleCompleted(raw)

	case ev.Type == "tax.rate.ask":
		handleTaxRateAsk(raw)

	// The generic export/report dispatch hook (ut-docs#189) — the host
	// (internal/pages/data_api.go) resolves entries[].key to this plugin's
	// id and asks this plugin specifically (EventBus.AskPlugin, not a
	// broadcast Ask), so entry_key here is always already ours. Checked
	// anyway (declining, not answering, on a mismatch) as defense-in-depth
	// against a future second export entry in this same plugin.
	case ev.Type == "export.requested.ask":
		var payload struct {
			From     string `json:"from"`
			To       string `json:"to"`
			EntryKey string `json:"entry_key"`
		}
		var wrapper struct {
			Payload json.RawMessage `json:"payload"`
		}
		_ = json.Unmarshal(raw, &wrapper)
		_ = json.Unmarshal(wrapper.Payload, &payload)
		if payload.EntryKey != "" && payload.EntryKey != dsfinvkExportEntryKey {
			logf("tax-de: export.requested.ask for entry_key=%q, not ours (%q) — declining", payload.EntryKey, dsfinvkExportEntryKey)
			os.Exit(0)
		}
		if payload.From == "" || payload.To == "" {
			now := time.Now().UTC()
			payload.To = now.Format("2006-01-02")
			payload.From = now.AddDate(0, 0, -1).Format("2006-01-02")
		}
		ok, info := exportDSFinVK(payload.From, payload.To)
		logf("tax-de: dsfinvk export requested from=%s to=%s ok=%v info=%s", payload.From, payload.To, ok, info)
		// This plugin only ever triggers fiskaly's async job (see
		// exportDSFinVK's doc comment) — it never has file bytes to return
		// inline, so the response always omits content_b64/filename.
		if !ok {
			fmt.Print(string(mustJSON(map[string]any{
				"ok":    false,
				"error": "DSFinV-K export trigger failed (not configured, or fiskaly auth/request error — see plugin logs)",
			})))
			os.Exit(0)
		}
		fmt.Print(string(mustJSON(map[string]any{
			"ok": true,
			"message": fmt.Sprintf(
				"DSFinV-K export triggered (fiskaly export id=%s). This is async and can take up to an hour per fiskaly's docs — "+
					"polling for completion is not implemented here, check the fiskaly dashboard.", info),
		})))
		os.Exit(0)

	default:
		logf("tax-de: unhandled event type %q", ev.Type)
		os.Exit(0)
	}
}
