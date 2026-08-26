//go:build wasip1

// Germany fiscal compliance — TSE signing (fiskaly Cloud-TSE / "SIGN DE" API)
// + DSFinV-K export (fiskaly DSFinV-K API). A WASI command (GOOS=wasip1
// GOARCH=wasm) the till runs in-process, per ut-docs ADR-0025 ("Country-
// specific tax rates and fiscal compliance") and ADR-0002's `tax`/`export`
// canonical types.
//
// STATUS — read before relying on this for anything real:
//   - SIGN DE (TSE signing): the fiskaly API contract this plugin implements
//     — host, auth, TSS/client lifecycle, the ACTIVE/FINISHED transaction
//     flow, and the standard_v1 receipt schema shape — was CONFIRMED live
//     2026-08-18 against a real fiskaly sandbox account (auth → create TSS
//     → initialize → create/register client → start+finish a transaction →
//     retrieve it with a valid ECDSA signature). That test exercised the raw
//     HTTP contract directly, not this compiled plugin end-to-end through
//     universal-till's actual wazero runtime/host functions — see the
//     README status table for the precise line between the two. Endpoints
//     not exercised by that test (DSFinV-K export, REDUCED_1/NULL/
//     SPECIAL_RATE_1 vat buckets, the RECEIPT_0104 return-receipt type, the
//     deterministic-pseudo-UUID tx_id scheme) remain flagged "NEEDS SANDBOX
//     VERIFICATION" below, same convention as ut-plugin-payment-sumup's
//     unverified reader-checkout path.
//   - DSFinV-K export: still entirely unconfirmed — public docs describe it
//     as possibly a genuinely separate API/host from SIGN DE (cash_register/
//     cashPointClosing concepts), not just a different path on the same
//     host. Do not assume fiskalyparse.DsfinvkBase is even structurally right.
//   - IS the till's real TSE signer since 2026-08-19 (ut-docs#818). This
//     plugin now declares fiscal.sign.ask — core's actual TSE-signing
//     extension point (blocking, exclusive between signer plugins, persists
//     evidence, gates ADR-0048; see
//     ut-docs/reference/contracts/fiscal-sign-ask.md) — so core sees a
//     fiscal signer installed. It previously declared only sale.completed,
//     which meant core saw ZERO signers however well the fiskaly call
//     itself worked. sale.completed has been REMOVED: core now owns the
//     whole failure surface (journal marker, receipt notice, operator
//     alert, background re-ask), so this plugin's own private retry queue
//     was both redundant and actively corrupting its audit records.
//   - REFUSES to sign an unbalanced receipt. fiskaly renders standard_v1
//     into DSFinV-K's Beleg^<per VAT rate>^<per payment type>, whose halves
//     must be equal. A tip rides the payment side with no VAT bucket, and a
//     sale-level discount/service charge moves the total without moving the
//     per-line vat_breakdown — so those sales currently do NOT sign, by
//     design, and take core's declared-and-retried path instead. The
//     correct German tax representation is an open accountant question,
//     ut-docs#833. Do not "fix" this by picking a bucket and signing.
//   - DSFinV-K export format/content compliance has NOT been legally
//     verified against KassenSichV. Cash-point-closing generation (the
//     aggregation step DSFinV-K exports actually depend on) is NOT
//     implemented — see exportDSFinVK below. Do not treat a successful
//     export call as a compliant one.
//   - This is a skeleton, not a certified compliance solution. Real legal/
//     tax-advisor sign-off is required before any merchant relies on this
//     plugin for a live business. See README.md.
//
// OFFLINE-FIRST TENSION — RESOLVED 2026-08-19 (ut-docs#818). This block
// previously described `sale.completed`'s dead end: dispatched
// NON-BLOCKING and fired AFTER the sale, so this plugin had no
// architectural way to block, reverse or even declare a sale when fiskaly
// was unreachable, and worked around it with a private "unsigned, pending
// retry" queue. That hook and that queue are GONE.
//
// The plugin now answers `fiscal.sign.ask`: blocking, tender-phase, 3000ms
// budget, `proceed-and-declare`. Core owns the whole failure surface —
// audit-journal marker, receipt outage notice, operator alert, background
// re-ask — so a failure is declared and visible, never silent, and the sale
// still never blocks on connectivity (ADR-0003 intact).
//
// What ADR-0025 flagged and did NOT resolve is still not resolved here:
// whether an unsigned-then-backfilled sale satisfies KassenSichV's
// "irreversible, tamper-proof at time of transaction" requirement is a
// legal/TSE-vendor question, not something this code decides.
//
// Two hard rules survive: never fabricate a signature, and never sign a
// receipt already known to misstate the sale (see fiscalsign.BalanceDelta).
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

	"github.com/universaltill/ut-plugin-tax-de/src/datev"
	"github.com/universaltill/ut-plugin-tax-de/src/fiscalsign"
	"github.com/universaltill/ut-plugin-tax-de/src/fiskalyparse"
	"github.com/universaltill/ut-plugin-tax-de/src/taxrate"
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

// signDEBase and dsfinvkBase moved to src/fiskalyparse (2026-08-18 code
// review) so their values and ParseSignResponse are host-testable —
// src/main.go is wasip1-only, excluded from ordinary `go test ./...`. See
// fiskalyparse.SignDEBase / fiskalyparse.DsfinvkBase for the full status
// (which is CONFIRMED, which is still NEEDS SANDBOX VERIFICATION, and why)
// and parse_test.go for the regression coverage.

// dsfinvkExportEntryKey must match manifest.json's entries[].key for the
// "export" entry. The host resolves export.requested.ask by plugin id, so
// this plugin (declaring exactly one export entry) is already only ever
// asked on its own behalf — this check is defense-in-depth for the day
// this plugin ships a second export entry with a different key, not a fix
// for cross-plugin routing (that's the host's job, see universal-till's
// EventBus.AskPlugin).
const dsfinvkExportEntryKey = "dsfinvk-export-de"

// datevExportEntryKey must match manifest.json's second "export" entries[]
// item — see dsfinvkExportEntryKey's doc comment for why this check exists.
const datevExportEntryKey = "datev-buchungsstapel-export-de"

const (
	tokenStorageKey = "fiskaly_token" // cached {access_token, obtained_at}
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
// function (permission-gated by `net:kassensichv-middleware.fiskaly.com` in
// manifest.json — see universal-till/internal/plugins/wasm_hostfns.go
// hostHTTPRequest). ok is
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
// necessary. /auth as the path (relative to fiskalyparse.SignDEBase) is CONFIRMED
// 2026-08-18 against a live sandbox — decoding the returned JWT showed a
// 24h expiry (iat/exp claims), well past this function's conservative
// 5-minute reuse window, so no change needed there. NEEDS SANDBOX
// VERIFICATION still: behavior when a cached token is presented past its
// real expiry (does a call 401, and does this function's caller retry
// correctly? — not exercised by the 2026-08-18 test, which always
// authenticated fresh).
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
	body, status, ok := httpCall("POST", fiskalyparse.SignDEBase+"/auth", map[string]string{"Content-Type": "application/json"}, reqBody)
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

// --- TSE signing (SIGN DE API) ---

// The VAT/payment bucket mapping and minor-unit formatting moved to
// src/fiscalsign (2026-08-19, ut-docs#818) so they are unit-testable on the
// host -- this file is wasip1-only and cannot be tested directly. They are
// also the fiskaly-shaped part of the seam ADR-0055 requires, i.e. what a
// future config-selected second provider would replace.

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

	// Evidence is the full §6 receipt evidence from the FINISHED
	// transaction. Not persisted in the audit record (the flat fields
	// above already cover that); carried so the fiscal.sign.ask answer can
	// hand it to core for the receipt.
	Evidence fiskalyparse.SignEvidence `json:"-"`
}

// signInput is the provider-neutral view of a sale to be signed: already
// bucketed, already deterministically ordered, with nothing fiskaly-shaped
// left to decide. Keeping signTransaction off the raw event struct is the
// provider seam ADR-0055 requires — a second backend would replace the
// mapping in src/fiscalsign that produces this, not this function.
type signInput struct {
	SaleID   string
	SaleType string
	VAT      []fiscalsign.VATAmount
	Payments []fiscalsign.PaymentAmount
}

// signTransaction attempts one real TSE-sign round-trip against fiskaly's
// SIGN DE API: start the transaction (state ACTIVE), then finish it (state
// FINISHED) with the receipt schema. Returns signed=false — NEVER a
// fabricated signature — on any auth, network, or non-2xx failure. See the
// package doc comment for why a failure here cannot block the sale.
//
// CONFIRMED 2026-08-18: the tx_revision-incrementing PUT lifecycle against a
// live sandbox produces a real signed transaction, and parseSignResponse's
// field paths now match the real FINISHED response envelope (see its doc
// comment — the original guess was wrong). One nuance NOT independently
// re-tested: the live verification used three PUT calls (start ACTIVE →
// update ACTIVE-with-schema → finish FINISHED-with-schema, tx_revision
// 1/2/3), matching fiskaly's own example collection; this function instead
// goes directly from start (tx_revision=1, ACTIVE, no schema) to finish
// (tx_revision=2, FINISHED, with schema) in two calls. Skipping the
// intermediate update revision is consistent with fiskaly's documented
// revision model (each PUT is just the next revision; nothing requires a
// specific count of them before FINISHED) but was not itself run against
// live fiskaly — worth confirming if a real sign attempt ever fails at the
// finish step specifically.
func signTransaction(sale signInput, apiKey, apiSecret, tssID, clientID string) tseSignResult {
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
	base := fmt.Sprintf("%s/tss/%s/tx/%s", fiskalyparse.SignDEBase, tssID, id)

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

	// 2. Finish the transaction with the receipt schema. The vat-rate and
	//    payment-type breakdowns are computed by the caller (src/fiscalsign,
	//    unit-tested) and arrive already bucketed and deterministically
	//    ordered, so the same sale always produces the same request body --
	//    which matters because a background retry re-signs the same sale.
	amountsPerVAT := sale.VAT
	amountsPerPayment := sale.Payments
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

	sig, logTime := fiskalyparse.ParseSignResponse(body)
	if sig == "" {
		result.FailureReason = "finish_response_missing_signature"
		return result
	}
	result.Signed = true
	result.SignatureB64 = sig
	result.LogTime = logTime
	// Full evidence for the wire (RFC3339 timestamps); the flat fields
	// above keep their existing shape so `tse_result:*` records already
	// written stay comparable.
	result.Evidence = fiskalyparse.ParseSignEvidence(body)
	return result
}

// parseSignResponse moved to fiskalyparse.ParseSignResponse (2026-08-18 code
// review) — see that function's doc comment for why signature and
// log-timestamp extraction are deliberately independent.

// taxRateAskPayload mirrors universal-till's internal/pages/tax_hook.go —
// the till's core has NO built-in notion of §12 UStG's dine-in/takeaway
// VAT switch; this plugin is where that rule actually lives.
type taxRateAskPayload struct {
	ItemID    string `json:"item_id"`
	TaxCodeID string `json:"tax_code_id"`
	TaxRateBP int    `json:"tax_rate_bp"`
	OrderType string `json:"order_type"`
}

// NOTE: the old `sale.completed` handler and this plugin's own
// `unsigned_queue` retry loop were REMOVED in ut-docs#818 (2026-08-19).
//
// They existed only because `sale.completed` is non-blocking and fires
// AFTER the sale — the plugin had no way to declare a signing failure, so
// it queued and retried privately. Now that the plugin answers the real
// `fiscal.sign.ask` point, core owns that entire surface properly:
// journal marker, receipt outage notice, operator alert, and background
// re-ask with `retry: true`. A second, private retry loop is redundant.
//
// It was also actively harmful. An independent review (2026-08-19) proved
// that with both hooks live, every SUCCESSFULLY signed sale was then
// re-signed by `sale.completed` against the same deterministic `tx_id` —
// which fiskaly rejects, because that transaction is already FINISHED. The
// result per sale: a false "fiskaly was NOT reached" operator log, the
// `tse_result:<sale_id>` audit record overwritten from signed to FAILED,
// and one permanent entry added to a queue that grows to 200 and then
// costs 200 failing round-trips on every subsequent sale.

// handleFiscalSignAsk answers core's real TSE-signing extension point
// (ut-docs#818; contract fiscal-sign-ask.md v1.1.0, registered by ADR-0044
// Decision 1). This -- not sale.completed -- is what makes core see a
// fiscal signer as installed at all.
//
// Failure policy is core's, not ours: on any failure we answer
// "unreachable" and core applies proceed-and-declare (sale completes,
// journaled unsigned, receipt notice, operator alert, background retry).
// We never block the sale and never fabricate a signature.
func handleFiscalSignAsk(raw []byte) {
	req, err := fiscalsign.ParseRequest(raw)
	if err != nil {
		// Fail closed: an unreadable request means signing is unproven.
		logf("tax-de: fiscal.sign.ask: %v -- answering unreachable", err)
		fmt.Print(string(fiscalsign.Unreachable().JSON()))
		os.Exit(0)
	}

	apiKey := strings.TrimSpace(setting("fiskaly_api_key"))
	apiSecret := strings.TrimSpace(setting("fiskaly_api_secret"))
	tssID := strings.TrimSpace(setting("fiskaly_tss_id"))
	clientID := strings.TrimSpace(setting("fiskaly_client_id"))

	if apiKey == "" || apiSecret == "" || tssID == "" || clientID == "" {
		// Not configured is NOT "not-this-terminal": that status means "no
		// opinion" and would let the sale pass with no marker at all. A
		// German till with an unconfigured signer must still surface the
		// gap, so declare it unreachable and let core declare + retry.
		logf("tax-de: fiscal.sign.ask: fiskaly settings incomplete -- answering unreachable")
		fmt.Print(string(fiscalsign.Unreachable().JSON()))
		os.Exit(0)
	}

	// Refuse to sign a receipt whose own two halves disagree.
	//
	// fiskaly renders standard_v1 into DSFinV-K's `Beleg^<per VAT
	// rate>^<per payment type>`, and those halves must be equal. Core can
	// legitimately hand us an unbalanced request — a tip rides the payment
	// side with no VAT bucket, and a sale-level discount/service charge
	// moves `total` without moving the per-line `vat_breakdown`. How those
	// SHOULD be represented is a German tax question, asked of a real
	// accountant in ut-docs#833, not something to guess here.
	//
	// A TSE signature cannot be corrected afterwards, so signing a receipt
	// we already know misstates the sale is worse than declaring a gap:
	// core's proceed-and-declare path completes the sale, marks it
	// unsigned, prints the outage notice, alerts the operator and retries.
	// Same principle as never fabricating a signature.
	if delta := fiscalsign.BalanceDelta(req); delta != 0 {
		logf("tax-de: fiscal.sign.ask: REFUSING to sign sale %s — VAT side and payment side differ by %d minor units (tip / sale-level discount / service charge; see ut-docs#833). Answering unreachable.", req.SaleID, delta)
		fmt.Print(string(fiscalsign.Unreachable().JSON()))
		os.Exit(0)
	}

	in := signInput{
		SaleID:   req.SaleID,
		VAT:      fiscalsign.VATAmounts(req),
		Payments: fiscalsign.PaymentAmounts(req),
	}
	res := signTransaction(in, apiKey, apiSecret, tssID, clientID)
	recordResult(res)

	if !res.Signed {
		logf("tax-de: fiscal.sign.ask: sale %s NOT signed (%s) -- answering unreachable", req.SaleID, res.FailureReason)
		fmt.Print(string(fiscalsign.Unreachable().JSON()))
		os.Exit(0)
	}

	// Full §6 KassenSichV evidence. Every field path below is read from a
	// REAL fiskaly response captured from the live sandbox on 2026-08-18
	// (fiskalyparse's realFinishedTransactionBody fixture) — not guessed,
	// and not from documentation. An earlier draft of this handler sent
	// only signature+log_time, claiming the other paths were unknown; an
	// independent review found that was simply wrong, and that this repo's
	// own committed fixture already contained all of them.
	ev := res.Evidence
	fmt.Print(string(fiscalsign.Approved(fiscalsign.TSEEvidence{
		TransactionNumber:  ev.TransactionNumber,
		SignatureCounter:   ev.SignatureCounter,
		SerialNumber:       ev.SerialNumber,
		StartTime:          ev.StartTime,
		LogTime:            ev.LogTime,
		Signature:          ev.Signature,
		SignatureAlgorithm: ev.SignatureAlgorithm,
	}).JSON()))
	os.Exit(0)
}

// handleTaxRateAsk answers the "tax.rate.ask" hook (EventBus.Ask — a
// blocking, value-returning hook; see universal-till's
// internal/plugins/ipc.go doc comment). Writing valid JSON to stdout is the
// answer; writing nothing means "no opinion on this line," and the till
// falls back to the line's own configured rate.
//
// The actual rule — which tax codes switch to which reduced rate on
// takeaway, and the full (product tax class x consumption mode) matrix it
// produces (ut-docs#1013) — lives in the taxrate package, host-testable
// independently of this wasip1-only file. The rule itself is merchant-
// configured via the takeaway_rate_overrides setting (a JSON object,
// tax_code_id → basis points), NOT hardcoded here: a real German café's
// catalog varies (e.g. only some drinks, not food), and this plugin has no
// way to know a shop's own tax-code IDs in advance. There is currently no
// dedicated settings UI for this beyond editing the JSON value directly
// (same pre-existing gap noted in universal-till's
// docs/code-reviews/2026-07-28-order-type-tax-switching.md, now on the
// plugin side instead of core's) — a real follow-up, not built here.
func handleTaxRateAsk(raw []byte) {
	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	_ = json.Unmarshal(raw, &wrapper)
	var ask taxRateAskPayload
	_ = json.Unmarshal(wrapper.Payload, &ask)

	bp, ok, err := taxrate.Resolve(ask.OrderType, ask.TaxCodeID, func() string { return setting("takeaway_rate_overrides") })
	if err != nil {
		logf("tax-de: takeaway_rate_overrides setting is not valid JSON: %v", err)
		os.Exit(0)
	}
	if !ok {
		os.Exit(0) // no opinion on this line — stays pinned to its own rate
	}

	fmt.Print(string(mustJSON(map[string]int{"rate_bp": bp})))
	os.Exit(0)
}

// recordResult persists the sign attempt's outcome (signed or not) keyed by
// sale id, so a future report/reconciliation surface can enumerate unsigned
// sales. A permanent audit record. (It used to be contrasted with this
// plugin's own transient retry queue; that queue was removed in v0.4.0 —
// core owns retry now.)
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
	body, status, ok := httpCall("POST", fiskalyparse.DsfinvkBase+"/export", authHeader(token), reqBody)
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
	logf("tax-de: dsfinvk export triggered id=%s state=%s (poll GET %s/export/%s for completion — not implemented here, generating can take up to an hour per fiskaly docs)", exportResp.ID, exportResp.State, fiskalyparse.DsfinvkBase, exportResp.ID)
	return true, exportResp.ID
}

// handleDSFinVKExport answers export.requested.ask for the dsfinvk-export-de
// entry — extracted unchanged from the pre-ut-docs#41 inline handler (this
// plugin's only export entry until the DATEV entry below was added).
func handleDSFinVKExport(from, to string) {
	ok, info := exportDSFinVK(from, to)
	logf("tax-de: dsfinvk export requested from=%s to=%s ok=%v info=%s", from, to, ok, info)
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
}

// --- DATEV Buchungsstapel export (ut-docs#41) ---
//
// Unlike DSFinV-K, this is pure local data transformation (internal/data.
// ExportSaleRow -> a DATEV EXTF CSV, see src/datev) — no fiskaly account, no
// network call, no async job. The file is built and returned inline via
// content_b64, same request/response cycle as the till's own reset-
// transactions/cleanup-catalog actions.
func handleDATEVExport(from, to string, sales []datev.SaleRow) {
	settings, err := datevSettings()
	if err != nil {
		logf("tax-de: datev export settings invalid: %v", err)
		fmt.Print(string(mustJSON(map[string]any{"ok": false, "error": err.Error()})))
		os.Exit(0)
	}
	result, err := datev.Build(from, to, sales, settings, time.Now().UTC())
	if err != nil {
		logf("tax-de: datev export failed: %v", err)
		fmt.Print(string(mustJSON(map[string]any{"ok": false, "error": err.Error()})))
		os.Exit(0)
	}
	logf("tax-de: datev export built from=%s to=%s sales=%d bytes=%d", from, to, len(sales), len(result.Content))
	fmt.Print(string(mustJSON(map[string]any{
		"ok":          true,
		"filename":    result.Filename,
		"content_b64": base64.StdEncoding.EncodeToString(result.Content),
	})))
	os.Exit(0)
}

// handleDATEVClosesExport answers export.requested.ask for the DATEV entry
// when the host sent archived day-closes (ut-docs#1005): pure local
// transformation of the already-archived Z-reports into a DATEV EXTF
// Buchungsstapel — one posting set per close, keyed to that close's own
// Z-number — returned inline via content_b64, same cycle as
// handleDATEVExport.
func handleDATEVClosesExport(closes []datev.EODCloseExport) {
	settings, err := datevSettings()
	if err != nil {
		logf("tax-de: datev closes export settings invalid: %v", err)
		fmt.Print(string(mustJSON(map[string]any{"ok": false, "error": err.Error()})))
		os.Exit(0)
	}
	result, err := datev.BuildFromCloses(closes, settings, time.Now().UTC())
	if err != nil {
		logf("tax-de: datev closes export failed: %v", err)
		fmt.Print(string(mustJSON(map[string]any{"ok": false, "error": err.Error()})))
		os.Exit(0)
	}
	logf("tax-de: datev closes export built closes=%d bytes=%d", len(closes), len(result.Content))
	fmt.Print(string(mustJSON(map[string]any{
		"ok":          true,
		"filename":    result.Filename,
		"content_b64": base64.StdEncoding.EncodeToString(result.Content),
	})))
	os.Exit(0)
}

// datevSettings reads and parses the datev_* plugin settings. Erloeskonten/
// BuSchluessel/KonteByMethod are merchant/accountant-configured JSON maps
// (tax_rate_bp or payment-method id -> account number) — see the datev
// package's Settings doc comment for why this plugin never defaults them to
// a guessed real account number.
func datevSettings() (datev.Settings, error) {
	erloeskonten := map[string]string{}
	if raw := strings.TrimSpace(setting("datev_erloeskonten")); raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &erloeskonten); err != nil {
			return datev.Settings{}, fmt.Errorf("datev_erloeskonten is not valid JSON: %w", err)
		}
	}
	buSchluessel := map[string]string{}
	if raw := strings.TrimSpace(setting("datev_bu_schluessel")); raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &buSchluessel); err != nil {
			return datev.Settings{}, fmt.Errorf("datev_bu_schluessel is not valid JSON: %w", err)
		}
	}
	konteByMethod := map[string]string{}
	if raw := strings.TrimSpace(setting("datev_konten_by_method")); raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &konteByMethod); err != nil {
			return datev.Settings{}, fmt.Errorf("datev_konten_by_method is not valid JSON: %w", err)
		}
	}
	return datev.Settings{
		BeraterNr:        strings.TrimSpace(setting("datev_berater_nr")),
		MandantNr:        strings.TrimSpace(setting("datev_mandant_nr")),
		SachkontenLaenge: strings.TrimSpace(setting("datev_sachkontenlaenge")),
		WJBeginn:         strings.TrimSpace(setting("datev_wj_beginn")),
		// datev_konto_kasse is LEGACY (superseded for the day-close batch
		// by datev_konten_by_method in v0.5.0) but stays DECLARED in
		// manifest.json's settings[] deliberately: the host's
		// ReconcilePluginSettings deletes every stored plugin_settings row
		// whose key the manifest no longer declares, so dropping the
		// declaration would silently destroy an upgraded install's stored
		// value on upgrade — and with no declaration there'd be no UI to
		// ever set it again. Kept so the per-sale Build fallback genuinely
		// keeps working (see main()'s routing comment); a fresh install
		// gets "" here and Build keeps refusing with its clear
		// not-configured error, never a guessed account.
		KontoKasse:            strings.TrimSpace(setting("datev_konto_kasse")),
		KonteByMethod:         konteByMethod,
		KontoGutschein:        strings.TrimSpace(setting("datev_konto_gutschein")),
		KontoGutscheinZahlung: strings.TrimSpace(setting("datev_konto_gutschein_zahlung")),
		KontoGeldtransit:      strings.TrimSpace(setting("datev_konto_geldtransit")),
		KontoTrinkgeld:        strings.TrimSpace(setting("datev_konto_trinkgeld")),
		Erloeskonten:          erloeskonten,
		BuSchluessel:          buSchluessel,
	}, nil
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var ev struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &ev)

	switch {
	case ev.Type == "fiscal.sign.ask":
		handleFiscalSignAsk(raw)

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
			From      string                 `json:"from"`
			To        string                 `json:"to"`
			EntryKey  string                 `json:"entry_key"`
			Sales     []datev.SaleRow        `json:"sales"`      // ut-docs#221 — only the DATEV path below consumes this; DSFinV-K stays fiskaly-triggered, no local data needed
			EODCloses []datev.EODCloseExport `json:"eod_closes"` // ut-docs#1005 — archived day-closes (Z-reports); a supporting host always sends the field ("[]" when the range has no archived close), so nil here means a pre-#1005 host — see the routing comment below
		}
		var wrapper struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			logf("tax-de: export.requested.ask: unparseable event envelope: %v", err)
			fmt.Print(string(mustJSON(map[string]any{"ok": false, "error": "malformed export request"})))
			os.Exit(0)
		}
		if err := json.Unmarshal(wrapper.Payload, &payload); err != nil {
			// Reviewer-caught: this used to be a silently-ignored `_ =`,
			// which for a DATEV request with a malformed payload fell
			// through to payload.EntryKey == "" and got silently routed to
			// the DSFinV-K/fiskaly path instead — a confusing "DSFinV-K
			// export trigger failed" error for what was actually a DATEV
			// request with a bad payload. Fail closed instead.
			logf("tax-de: export.requested.ask: unparseable payload: %v", err)
			fmt.Print(string(mustJSON(map[string]any{"ok": false, "error": "malformed export request payload"})))
			os.Exit(0)
		}
		if payload.From == "" || payload.To == "" {
			now := time.Now().UTC()
			payload.To = now.Format("2006-01-02")
			payload.From = now.AddDate(0, 0, -1).Format("2006-01-02")
		}

		switch payload.EntryKey {
		case datevExportEntryKey:
			// ut-docs#1005: the day-close-grained Buchungsstapel (one
			// posting set per archived Z-report) is the export a German
			// accountant actually books, so it's preferred whenever the
			// host sent eod_closes. The routing key is PRESENCE, not
			// length: a host that supports eod_closes always sends the
			// field — "[]" (supported, zero archived closes in range) is
			// wire-distinguishable from absent/null (a pre-#1005 host that
			// doesn't know the concept). Present-but-empty therefore goes
			// to BuildFromCloses, which refuses with its clear
			// no-closes-in-range error — never a silent fall-back to the
			// wrong-grain per-sale export. The per-sale path is kept — NOT
			// removed — solely for a genuinely old host, which sends
			// sales[] unconditionally and no eod_closes field at all, so
			// the pre-#1005 behavior is preserved there instead of turning
			// a working export into a hard failure on rollout.
			if payload.EODCloses != nil {
				handleDATEVClosesExport(payload.EODCloses)
			} else {
				handleDATEVExport(payload.From, payload.To, payload.Sales)
			}
		case dsfinvkExportEntryKey, "":
			// "" (no entry_key) preserves this plugin's pre-ut-docs#41
			// behavior, from when dsfinvk-export-de was its only export
			// entry — the host always sends a real entry_key today
			// (internal/pages/data_api.go resolves it before asking), this
			// is defensive only.
			handleDSFinVKExport(payload.From, payload.To)
		default:
			logf("tax-de: export.requested.ask for entry_key=%q, not ours (%q or %q) — declining", payload.EntryKey, dsfinvkExportEntryKey, datevExportEntryKey)
			os.Exit(0)
		}

	default:
		logf("tax-de: unhandled event type %q", ev.Type)
		os.Exit(0)
	}
}
