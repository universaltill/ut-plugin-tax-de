// Package wasmrun_test runs the REAL compiled plugin (bin/plugin.wasm)
// through a real wazero runtime — the same engine `universal-till` uses
// (internal/plugins/wasm_runtime.go) — with a minimal stand-in for the
// host functions the till provides.
//
// Why this exists (ut-docs#818): `src/main.go` is `GOOS=wasip1`-only, so
// nothing in it can be unit-tested on the host. Before this, the only
// evidence that this plugin answers an event correctly was a `go build`
// plus an *ad-hoc, uncommitted* wazero run of `tax.rate.ask` (README's
// status table records that run; the harness itself was never kept).
// README's own "next steps" #5 asks for exactly this. So this is the first
// committed proof that the compiled artefact — not the source — dispatches
// `fiscal.sign.ask` and emits the JSON the contract requires.
//
// `tax.rate.ask` itself only got its OWN committed case here later
// (ut-docs#1013 review finding): the ad-hoc run mentioned above was never
// turned into a kept test at the time #818 landed, so for a while this
// package proved `fiscal.sign.ask` alone. `TestTaxRateAsk_*` below closes
// that gap the same way.
//
// WHAT THIS PROVES: the wasm module dispatches on event type, reads its
// settings through the host ABI, issues the fiskaly HTTP calls in the right
// order with the right bodies, parses a real-shaped response, and writes
// exactly the stdout JSON `fiscal-sign-ask.md` v1.1.0 specifies — including
// the failure paths.
//
// WHAT THIS DOES NOT PROVE: the HTTP layer here is a stub, not fiskaly.
// The live fiskaly contract was verified separately on 2026-08-18 (see
// README's status table); deliberately not re-hit here, because a test that
// needs live credentials and a network is a flaky test, and the canned
// bodies below are taken from that verified real response shape. It is also
// not `universal-till`'s real host-function implementations nor a real
// installed-plugin flow through the till UI.
package wasmrun_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Host ABI (docs repo reference/plugin-host-functions.md, mirrored by every
// sibling plugin): data calls return the FULL length of the value — the
// guest retries with a bigger buffer if that exceeds its cap. Negative
// returns are host errors.
const (
	hostErrNotFound = -1
	hostErrDenied   = -2
)

// httpCall is one request the guest made, captured for assertions.
type httpCall struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
}

// Body decodes the request body. The host ABI carries bodies base64-encoded
// in BOTH directions (`body_b64`), so a stub that used a plain `body` field
// would silently hand the guest an empty body and every assertion below
// would be meaningless.
func (c httpCall) Body() string {
	b, err := base64.StdEncoding.DecodeString(c.BodyB64)
	if err != nil {
		return ""
	}
	return string(b)
}

// stubHost is the fake till: settings, storage, and a scripted HTTP layer.
type stubHost struct {
	mu       sync.Mutex
	settings map[string]string
	storage  map[string][]byte
	logs     []string
	calls    []httpCall
	// respond returns (status, body) for a given request. Nil means a
	// transport failure (the host function returns an error code).
	respond func(c httpCall) (int, string, bool)
}

func writeOut(mem api.Memory, dstPtr, dstCap uint32, val []byte) int32 {
	if uint32(len(val)) <= dstCap {
		mem.Write(dstPtr, val)
	}
	return int32(len(val)) // full length either way, per the ABI
}

func readStr(mem api.Memory, ptr, n uint32) string {
	b, _ := mem.Read(ptr, n)
	return string(b)
}

func (h *stubHost) register(ctx context.Context, r wazero.Runtime) error {
	_, err := r.NewHostModuleBuilder("ut").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n uint32) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.logs = append(h.logs, readStr(m.Memory(), ptr, n))
	}).Export("log_write").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kPtr, kLen, dstPtr, dstCap uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		v, ok := h.settings[readStr(m.Memory(), kPtr, kLen)]
		if !ok {
			return hostErrNotFound
		}
		return writeOut(m.Memory(), dstPtr, dstCap, []byte(v))
	}).Export("settings_get").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kPtr, kLen, dstPtr, dstCap uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		v, ok := h.storage[readStr(m.Memory(), kPtr, kLen)]
		if !ok {
			return hostErrNotFound
		}
		return writeOut(m.Memory(), dstPtr, dstCap, v)
	}).Export("storage_get").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kPtr, kLen, vPtr, vLen uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		k := readStr(m.Memory(), kPtr, kLen)
		b, _ := m.Memory().Read(vPtr, vLen)
		cp := make([]byte, len(b))
		copy(cp, b)
		h.storage[k] = cp
		return 0
	}).Export("storage_set").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, rPtr, rLen, dstPtr, dstCap uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		var req httpCall
		if err := json.Unmarshal([]byte(readStr(m.Memory(), rPtr, rLen)), &req); err != nil {
			return -4 // invalid
		}
		h.calls = append(h.calls, req)
		status, body, ok := h.respond(req)
		if !ok {
			return hostErrDenied
		}
		// Response envelope the guest expects: status + body.
		out, _ := json.Marshal(map[string]any{
			"status":   status,
			"body_b64": base64.StdEncoding.EncodeToString([]byte(body)),
		})
		return writeOut(m.Memory(), dstPtr, dstCap, out)
	}).Export("http_request").
		Instantiate(ctx)
	return err
}

var _ = binary.LittleEndian // keep the import honest if the ABI grows

// buildWasm compiles the CURRENT source to wasm, so this test can never
// pass against a stale committed artefact.
func buildWasm(t *testing.T) []byte {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	// Register the guest sources as inputs of THIS test, so `go test`'s
	// result cache invalidates when they change.
	//
	// This is load-bearing, not defensive: the wasm is built by a
	// subprocess, and the go command's cache only tracks files the test
	// process itself opens. Without these reads the harness cached a PASS
	// across an edit that deleted the fiscal.sign.ask dispatch entirely —
	// caught by deliberately mutating main.go and watching this suite
	// wrongly stay green (2026-08-19). A test that cannot notice the code
	// changing is not a test.
	// Walk, don't ReadDir: the first version of this only covered
	// src/*.go, so a mutation to src/fiscalsign or src/fiskalyparse — where
	// the balance check and the evidence parsing actually live — was still
	// cached as a PASS. Caught by the second-round review.
	srcDir := filepath.Join(root, "src")
	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		_, readErr := os.ReadFile(path)
		return readErr
	})
	if err != nil {
		t.Fatalf("registering guest sources as cache inputs: %v", err)
	}

	out := filepath.Join(t.TempDir(), "plugin.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./src")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building wasm: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read wasm: %v", err)
	}
	return b
}

// run executes the compiled plugin with event JSON on stdin and returns
// stdout plus the host stub (for asserting what it did).
func run(t *testing.T, wasm []byte, h *stubHost, event string) (string, *stubHost) {
	t.Helper()
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		t.Fatalf("wasi: %v", err)
	}
	if err := h.register(ctx, r); err != nil {
		t.Fatalf("host module: %v", err)
	}

	var stdout, stderr strings.Builder
	cfg := wazero.NewModuleConfig().
		WithStdin(strings.NewReader(event)).
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithStartFunctions("_start")

	// The guest calls os.Exit(0); wazero surfaces that as a sys.ExitError
	// with code 0, which is a normal, successful finish for this plugin.
	if _, err := r.InstantiateWithConfig(ctx, wasm, cfg); err != nil {
		if !strings.Contains(err.Error(), "exit_code(0)") {
			t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
		}
	}
	return stdout.String(), h
}

func configuredHost(respond func(httpCall) (int, string, bool)) *stubHost {
	return &stubHost{
		settings: map[string]string{
			"fiskaly_api_key":    "test-key",
			"fiskaly_api_secret": "test-secret",
			"fiskaly_tss_id":     "tss-123",
			"fiskaly_client_id":  "client-456",
		},
		storage: map[string][]byte{},
		respond: respond,
	}
}

// A real-shaped fiskaly FINISHED response. Field paths match what the
// 2026-08-18 live-sandbox verification actually found (top-level
// `signature.value`, `log.timestamp`) — the shape an earlier version of
// this plugin got WRONG, which is precisely why asserting it here matters.
const finishedResponse = `{
  "number": 4711,
  "state": "FINISHED",
  "tss_serial_number": "TSS-SERIAL-9d5e",
  "time_start": 1755253860,
  "signature": {"value": "MEQCIFakeSignature==", "counter": 12345, "algorithm": "ecdsa-plain-SHA256"},
  "log": {"timestamp": 1755253862, "timestamp_format": "unixTime"}
}`

func scriptedFiskaly(t *testing.T) func(httpCall) (int, string, bool) {
	t.Helper()
	return func(c httpCall) (int, string, bool) {
		switch {
		case strings.Contains(c.URL, "/auth"):
			return 200, `{"access_token":"tok-abc"}`, true
		case strings.Contains(c.URL, "tx_revision=1"):
			return 200, `{"state":"ACTIVE"}`, true
		case strings.Contains(c.URL, "tx_revision=2"):
			return 200, finishedResponse, true
		}
		t.Errorf("unexpected request: %s %s", c.Method, c.URL)
		return 500, "", true
	}
}

const signAskEvent = `{
  "type": "fiscal.sign.ask",
  "payload": {
    "sale_id": "sale-abc",
    "currency": "EUR",
    "total": 1368,
    "tendered_at": "2026-08-19T10:31:02Z",
    "payments": [{"method": "card", "amount": 1368}],
    "vat_breakdown": [{"rate_bp": 1900, "net": 700, "tax": 133}, {"rate_bp": 700, "net": 500, "tax": 35}]
  }
}`

// THE test this card exists for: core sees a fiscal signer only because the
// compiled plugin answers this event.
func TestFiscalSignAsk_ApprovedCarriesEvidence(t *testing.T) {
	wasm := buildWasm(t)
	out, h := run(t, wasm, configuredHost(scriptedFiskaly(t)), signAskEvent)

	var got struct {
		Status string `json:"status"`
		TSE    *struct {
			Signature string `json:"signature"`
			LogTime   string `json:"log_time"`
		} `json:"tse"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout is not the JSON core expects: %v\nstdout: %q\nlogs: %v", err, out, h.logs)
	}
	if got.Status != "approved" {
		t.Fatalf("status = %q, want approved (logs: %v)", got.Status, h.logs)
	}
	if got.TSE == nil || got.TSE.Signature != "MEQCIFakeSignature==" {
		t.Fatalf("evidence signature not carried through: %+v", got.TSE)
	}
	if got.TSE.LogTime == "" {
		t.Error("log_time missing — the §6 receipt needs it and the response shape provides it")
	}

	// The signing lifecycle actually happened, in order.
	if len(h.calls) != 3 {
		t.Fatalf("want auth + start + finish = 3 calls, got %d: %+v", len(h.calls), h.calls)
	}
	if !strings.Contains(h.calls[1].URL, "tx_revision=1") || !strings.Contains(h.calls[2].URL, "tx_revision=2") {
		t.Errorf("revision order wrong: %s then %s", h.calls[1].URL, h.calls[2].URL)
	}

	// The signed body carries the right money. This is the compliance-
	// bearing assertion: gross per VAT bucket (net+tax) and tip included
	// in the payment total.
	body := h.calls[2].Body()
	for _, want := range []string{`"amount":"8.33"`, `"amount":"5.35"`, `"amount":"13.68"`, `"vat_rate":"NORMAL"`, `"vat_rate":"REDUCED_1"`, `"payment_type":"NON_CASH"`} {
		if !strings.Contains(strings.ReplaceAll(body, " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("finish body missing %s\nbody: %s", want, body)
		}
	}
}

// Failure must be declared, never silently approved — core's whole
// proceed-and-declare surface hangs off this answer.
func TestFiscalSignAsk_UnreachableOnFiskalyFailure(t *testing.T) {
	wasm := buildWasm(t)
	h := configuredHost(func(c httpCall) (int, string, bool) {
		if strings.Contains(c.URL, "/auth") {
			return 200, `{"access_token":"tok"}`, true
		}
		return 503, `{"error":"service unavailable"}`, true
	})
	out, h := run(t, wasm, h, signAskEvent)
	if s := statusOf(t, out, h); s != "unreachable" {
		t.Errorf("status = %q, want unreachable", s)
	}
}

func TestFiscalSignAsk_UnreachableWhenNotConfigured(t *testing.T) {
	wasm := buildWasm(t)
	h := &stubHost{
		settings: map[string]string{"fiskaly_api_key": "", "fiskaly_api_secret": ""},
		storage:  map[string][]byte{},
		respond: func(c httpCall) (int, string, bool) {
			t.Errorf("must not call fiskaly when unconfigured: %s", c.URL)
			return 500, "", true
		},
	}
	out, h := run(t, wasm, h, signAskEvent)
	// Deliberately NOT "not-this-terminal": that means "no opinion" and
	// would let the sale through with no marker at all.
	if s := statusOf(t, out, h); s != "unreachable" {
		t.Errorf("status = %q, want unreachable for an unconfigured signer", s)
	}
	if len(h.calls) != 0 {
		t.Errorf("made %d HTTP calls while unconfigured", len(h.calls))
	}
}

// Fail closed on a malformed request: never sign a guess.
func TestFiscalSignAsk_UnreachableOnMalformedPayload(t *testing.T) {
	wasm := buildWasm(t)
	out, h := run(t, wasm, configuredHost(scriptedFiskaly(t)),
		`{"type":"fiscal.sign.ask","payload":{"total":100}}`) // no sale_id
	if s := statusOf(t, out, h); s != "unreachable" {
		t.Errorf("status = %q, want unreachable", s)
	}
	if len(h.calls) != 0 {
		t.Errorf("attempted to sign a request with no sale_id (%d calls)", len(h.calls))
	}
}

// Regression guard for the whole point of ut-docs#818: if this hook stops
// being dispatched, core silently sees zero fiscal signers again.
func TestUnrelatedEventProducesNoSignAnswer(t *testing.T) {
	wasm := buildWasm(t)
	out, _ := run(t, wasm, configuredHost(scriptedFiskaly(t)),
		`{"type":"some.other.event","payload":{}}`)
	if strings.Contains(out, "approved") || strings.Contains(out, "unreachable") {
		t.Errorf("answered a fiscal.sign.ask response to an unrelated event: %q", out)
	}
}

func statusOf(t *testing.T, out string, h *stubHost) string {
	t.Helper()
	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\nstdout: %q\nlogs: %v", err, out, h.logs)
	}
	return got.Status
}

// A tipped sale is exactly the case the independent review caught: the tip
// rides the payment side and lands in no VAT bucket, so the DSFinV-K
// Beleg's two halves disagree. Until an accountant rules on the correct
// representation (ut-docs#833) the plugin must refuse to sign rather than
// write an irreversible TSE record that misstates the sale.
func TestFiscalSignAsk_RefusesToSignAnUnbalancedReceipt(t *testing.T) {
	wasm := buildWasm(t)
	const tipped = `{
	  "type": "fiscal.sign.ask",
	  "payload": {
	    "sale_id": "sale-tip",
	    "currency": "EUR",
	    "total": 1190,
	    "payments": [{"method": "card", "amount": 1190, "tip_amount": 100}],
	    "vat_breakdown": [{"rate_bp": 1900, "net": 1000, "tax": 190}]
	  }
	}`
	out, h := run(t, wasm, configuredHost(scriptedFiskaly(t)), tipped)
	if s := statusOf(t, out, h); s != "unreachable" {
		t.Errorf("status = %q, want unreachable for an unbalanced receipt", s)
	}
	if len(h.calls) != 0 {
		t.Errorf("contacted fiskaly %d times for a receipt it should have refused outright", len(h.calls))
	}
}

// The evidence core renders on a §6 receipt must be complete and correctly
// formatted — a raw Unix epoch would print as "TSE transaction end:
// 1755253862" to a customer and a tax auditor.
func TestFiscalSignAsk_EvidenceIsCompleteAndRFC3339(t *testing.T) {
	wasm := buildWasm(t)
	out, h := run(t, wasm, configuredHost(scriptedFiskaly(t)), signAskEvent)
	var got struct {
		TSE *struct {
			TransactionNumber  int64  `json:"transaction_number"`
			SignatureCounter   int64  `json:"signature_counter"`
			SerialNumber       string `json:"serial_number"`
			StartTime          string `json:"start_time"`
			LogTime            string `json:"log_time"`
			Signature          string `json:"signature"`
			SignatureAlgorithm string `json:"signature_algorithm"`
		} `json:"tse"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout: %v (%q) logs=%v", err, out, h.logs)
	}
	if got.TSE == nil {
		t.Fatal("no evidence on the approved response")
	}
	if got.TSE.TransactionNumber != 4711 || got.TSE.SignatureCounter != 12345 {
		t.Errorf("transaction_number/signature_counter = %d/%d, want 4711/12345", got.TSE.TransactionNumber, got.TSE.SignatureCounter)
	}
	if got.TSE.SerialNumber != "TSS-SERIAL-9d5e" {
		t.Errorf("serial_number = %q", got.TSE.SerialNumber)
	}
	if got.TSE.SignatureAlgorithm != "ecdsa-plain-SHA256" {
		t.Errorf("signature_algorithm = %q", got.TSE.SignatureAlgorithm)
	}
	for name, v := range map[string]string{"log_time": got.TSE.LogTime, "start_time": got.TSE.StartTime} {
		if !strings.Contains(v, "T") || !strings.HasSuffix(v, "Z") {
			t.Errorf("%s = %q — must be RFC3339, never a raw epoch", name, v)
		}
	}
}

// An ordinary German sale: tax-INCLUSIVE pricing, no tip, no discount. It
// must sign. Core puts the gross in vat_breakdown[].net and the contained
// tax in .tax for this convention, and the payload has no flag saying so —
// an earlier version of the balance check read it as tax-exclusive, which
// would have refused to sign every real sale in a German shop.
func TestFiscalSignAsk_OrdinaryTaxInclusiveGermanSaleSigns(t *testing.T) {
	wasm := buildWasm(t)
	const inclusive = `{
	  "type": "fiscal.sign.ask",
	  "payload": {
	    "sale_id": "sale-de-1",
	    "currency": "EUR",
	    "total": 1368,
	    "payments": [{"method": "cash", "amount": 1368}],
	    "vat_breakdown": [
	      {"rate_bp": 1900, "net": 833, "tax": 133},
	      {"rate_bp": 700,  "net": 535, "tax": 35}
	    ]
	  }
	}`
	out, h := run(t, wasm, configuredHost(scriptedFiskaly(t)), inclusive)
	if s := statusOf(t, out, h); s != "approved" {
		t.Fatalf("status = %q, want approved — this is a plain German sale (logs: %v)", s, h.logs)
	}
	body := strings.ReplaceAll(h.calls[2].Body(), " ", "")
	// Gross per rate is net as-is here, NOT net+tax: 8.33 and 5.35,
	// summing to the 13.68 actually paid.
	for _, want := range []string{`"amount":"8.33"`, `"amount":"5.35"`, `"amount":"13.68"`} {
		if !strings.Contains(body, want) {
			t.Errorf("finish body missing %s\nbody: %s", want, body)
		}
	}
	if strings.Contains(body, `"amount":"9.66"`) || strings.Contains(body, `"amount":"5.70"`) {
		t.Error("tax double-counted — net+tax was used where net is already gross")
	}
}

// TestTaxRateAsk_TakeawayOverrideAnswersReducedRate is this repo's first
// COMMITTED wasm-level proof for "tax.rate.ask" (ut-docs#1013 review
// finding): before this, the only evidence the compiled plugin dispatched
// this event and wired taxRateAskPayload's fields onto taxrate.Resolve in
// the right order was an ad-hoc, uncommitted wazero run (see this file's
// package doc comment, and README's status table). taxrate.Resolve takes
// three same-typed strings (orderType, taxCodeID) plus a func — a mistake
// swapping the first two at the call site in main.go would pass every test
// in both the taxrate and pos packages (neither can see main.go's call
// site) while being wrong for every real till. This test is the one place
// that can catch it: it drives the REAL compiled main.go, not a mock of it.
func TestTaxRateAsk_TakeawayOverrideAnswersReducedRate(t *testing.T) {
	wasm := buildWasm(t)
	h := &stubHost{
		settings: map[string]string{
			"takeaway_rate_overrides": `{"tax-milk-drink":700}`,
		},
		storage: map[string][]byte{},
	}
	const ask = `{
	  "type": "tax.rate.ask",
	  "payload": {"item_id": "item-cappuccino", "tax_code_id": "tax-milk-drink", "tax_rate_bp": 1900, "order_type": "takeaway"}
	}`
	out, _ := run(t, wasm, h, ask)
	var got struct {
		RateBP int `json:"rate_bp"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout not the {\"rate_bp\":N} shape the tax.rate.ask contract requires: %v\nstdout: %q\nlogs: %v", err, out, h.logs)
	}
	if got.RateBP != 700 {
		t.Fatalf("rate_bp = %d, want 700 (the configured takeaway override) — a swapped argument order at the main.go call site would produce a different wrong answer here without failing any other test", got.RateBP)
	}
}

// TestTaxRateAsk_DineInAnswersNothing pins the dine-in short-circuit at the
// compiled-plugin level too (taxrate_test.go's
// TestResolve_DineInNeverConsultsOverrides pins it at the Resolve level;
// this is the same guarantee one layer up, through the real wasip1 binary
// and its real host settings_get call).
func TestTaxRateAsk_DineInAnswersNothing(t *testing.T) {
	wasm := buildWasm(t)
	h := &stubHost{
		settings: map[string]string{
			"takeaway_rate_overrides": `{"tax-milk-drink":700}`,
		},
		storage: map[string][]byte{},
	}
	const ask = `{
	  "type": "tax.rate.ask",
	  "payload": {"item_id": "item-cappuccino", "tax_code_id": "tax-milk-drink", "tax_rate_bp": 1900, "order_type": ""}
	}`
	out, _ := run(t, wasm, h, ask)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("dine-in produced an answer, want no opinion (empty stdout): %q", out)
	}
}
