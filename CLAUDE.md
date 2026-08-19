# ut-plugin-tax-de — notes

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing Germany's KassenSichV
fiscal requirements: TSE signing via fiskaly's Cloud-TSE ("SIGN DE") API
(`canonical_type: tax`, hooks `sale.completed`) + DSFinV-K export via
fiskaly's DSFinV-K API (`canonical_type: tax`, second `entries[]` item typed
`export`, per ADR-0025's guidance to map both onto ADR-0002's existing
taxonomy in one manifest rather than invent a new type). First fiscal
compliance plugin built, per `ut-docs` ADR-0025 ("Country-specific tax rates
and fiscal compliance") and its "Real-world precedent" section: SumUp itself
delegates TSE signing to fiskaly rather than building it in-house, so this
plugin follows the same shape rather than inventing one.

## Status: SIGN DE's API contract confirmed 2026-08-18; DSFinV-K still a skeleton

SIGN DE (TSE signing) was verified 2026-08-18 against a real fiskaly TEST
sandbox — see `src/main.go`'s package doc comment and README.md's status
table for exactly what that proved and what it didn't. Two real bugs were
found and fixed by that test: `signDEBase` pointed at a dead host
(`kassensichv.io`, now 404s everywhere — corrected to
`kassensichv-middleware.fiskaly.com/api/v2`), and `parseSignResponse` read
the signature from a response shape (`tss_tx_result.signature.value`) that
doesn't match fiskaly's real payload (`signature.value`, top-level) — that
second bug alone would have made every real sign attempt silently fail even
once the host was fixed.

**This IS now the till's real TSE signer (2026-08-19, ut-docs#818).**
Superseding the previous note here, which said the opposite: `manifest.json`
now declares **`fiscal.sign.ask`** — core's actual TSE-signing extension
point (ADR-0041/0044/0048; blocking, exclusive between signer plugins,
persists evidence, renders it on the receipt, gates the ADR-0048
system-of-record check — see `ut-docs/reference/contracts/fiscal-sign-ask.md`
and `universal-till/internal/pages/fiscal_sign_hook.go`). Core therefore
sees a fiscal signer installed; previously it saw **zero**, so every sale
took the `fiscalSignNoSigner` path no matter how well the fiskaly call
worked.

**ADR-0055 decided signing stays in THIS plugin.** ADR-0044 Decision 3 had
called for splitting signing into a separate `ut-plugin-tax-fiskaly` repo;
that is withdrawn. If a second backend is ever needed it becomes a
**config-selected provider inside this plugin**, not a new repo — which is
why the pure mapping lives in `src/fiscalsign` (the provider seam) rather
than inline in `main.go`. One real limit is recorded in that ADR: a
*hardware* TSE (Swissbit) still cannot live here, because it needs
`runtime:"go"` for raw USB and this plugin is `wasm`; that is the moment to
split, and it is blocked on #605 regardless.

`sale.completed` is **kept** alongside it, deliberately — it still drives
this plugin's own `unsigned_queue` retry bookkeeping and its `tse_result:*`
audit records. Both hooks now share one signing implementation through
`signInput`/`signTransaction`, so they can never bucket money differently.

DSFinV-K export is unchanged and still fully unverified — every endpoint
in `src/main.go` for it is grounded in fiskaly's **public** documentation
(developer.fiskaly.com, kassensichv.net) but flagged
`NEEDS SANDBOX VERIFICATION` in its doc comment. It still points at the now-
confirmed-dead `kassensichv.io`, and per `fiskalyparse.DsfinvkBase`'s doc
comment may need a genuinely different host from SIGN DE's — do not assume the
2026-08-18 fix carries over. README.md has the full
honest-status table ("researched, not tested" vs. "confirmed") — read it
before touching this code, and keep both README.md and this file in sync
with reality as verification happens (don't let a `NEEDS SANDBOX
VERIFICATION` comment get resolved in code without also updating the README
row it corresponds to).

## The offline-first tension (do not "fix" this without a real decision)

`sale.completed` is non-blocking and fires **after** the sale has already
completed (confirmed against `universal-till/internal/plugins/ipc.go` /
`wasm_runtime.go`: non-blocking dispatch discards the handler's return
value — errors are logged, never retried, never surfaced to the operator).
This plugin therefore has **no architectural way to block or reverse a sale
if fiskaly is unreachable**, which is exactly the open question ADR-0025
flags and explicitly does NOT resolve ("needs real legal/TSE-vendor
confirmation, not assumed here"). The one hard rule this code follows
instead: **never fabricate a signature**. A failed sign attempt is logged
loudly and queued for retry (`unsigned_queue` in plugin storage, same
bounded-queue pattern as `ut-plugin-integration-webhook`'s delivery queue) —
if you touch `signTransaction` or `handleSaleCompleted`, preserve that
invariant. Don't add a code path that marks a sale "signed" without a real
2xx response containing a signature.

## `export` canonical type has no host dispatcher yet

Confirmed against `ut-docs/reference/plugin-manifest.md` and
`universal-till/internal/plugins/types.go`: the plugin engine dispatches
`page`/`button`/`theme` natively; `report`/`export` are registered/listed on
the plugin info card only. `exportDSFinVK` in `src/main.go` is real code
(triggered by a placeholder event, `tax.de.dsfinvk.export.requested`, that
nothing in the host currently publishes), not a stub — but nothing in the
till UI can invoke it yet. Also incomplete on fiskaly's side: a real
DSFinV-K export needs `cashPointClosing` aggregates built from every signed
transaction first, which this plugin does not build (see README "Known
gaps"). Don't remove the doc comments explaining this — they're load-bearing
context for whoever wires the dispatcher up.

## `export` canonical type now has TWO entries (ut-docs#41)

`manifest.json`'s `entries[]` has a second `type: export` item,
`datev-buchungsstapel-export-de`, alongside `dsfinvk-export-de`. Both answer
`export.requested.ask`, dispatched in `src/main.go` by `payload.EntryKey` —
if you add a third export entry, extend that switch, don't just check
against one constant again. Unlike DSFinV-K, the DATEV path needs no
fiskaly account: it's pure local data transformation (`src/datev`) of the
`sales[]` the host now sends in the payload (ut-docs#221) into a DATEV EXTF
CSV, returned inline via `content_b64` — see README's "DATEV Buchungsstapel
export" bullet and its Known gaps #5/#6.

## Code layout

- `src/main.go` — single WASI command, dispatches on the event JSON's
  `type` field (**`fiscal.sign.ask` → `handleFiscalSignAsk`, the real TSE
  signing point**; `sale.completed` → `handleSaleCompleted`; `tax.rate.ask` →
  `handleTaxRateAsk`, the dine-in/takeaway VAT switch, verified against a
  real wazero run — see README's status table; `export.requested.ask` →
  `handleDSFinVKExport` or `handleDATEVExport` depending on `entry_key`).
- `src/fiscalsign/` — the pure, host-independent half of the
  `fiscal.sign.ask` answerer: contract wire types, and the VAT/payment
  bucket mapping onto fiskaly's `standard_v1` schema. Its own package with
  no wasip1 tag, so `go test ./src/fiscalsign/...` runs on the host —
  `main.go` itself cannot be unit-tested at all (`//go:wasmimport`). Also
  the provider seam ADR-0055 requires: this is the fiskaly-shaped part a
  future config-selected second provider would replace. **Two invariants
  are pinned by tests and must not regress**: the amount signed per VAT
  bucket is GROSS (`net + tax`, not net — sending net under-declares every
  sale), and a payment's tip is included in its signed total.
- `src/wasmrun/` — test-only. Runs the REAL compiled `plugin.wasm` through
  a real wazero runtime with stubbed host functions, asserting the
  `fiscal.sign.ask` request bodies and the exact stdout JSON for the
  approved and failure paths. Note it explicitly `os.ReadFile`s the
  guest sources: the wasm is built by a subprocess, so without that the
  go command's result cache does not treat `main.go` as an input, and the
  suite will happily cache a PASS across a change that deletes the hook
  (observed for real, 2026-08-19). Don't remove those reads.
- `src/datev/` — DATEV EXTF Buchungsstapel file-building logic, deliberately
  its OWN package with no `//go:build wasip1` tag (unlike `src/main.go`) so
  `go test ./src/datev/...` runs on the host with no wasm build — the only
  Go tests this repo has. Never hardcode a real chart-of-accounts number in
  here; `Build` must keep refusing (not guessing) when a tax rate has no
  configured Gegenkonto.
- Host functions imported from module `ut` (see docs repo
  `reference/plugin-host-functions.md`): `log_write`, `settings_get`,
  `storage_get`, `storage_set`, `http_request`. Buffer ABI: data calls
  return the FULL length, retry with a bigger buffer if it exceeds cap;
  negatives are host errors (-1 not found, -2 denied, -3 internal,
  -4 invalid) — same convention as every sibling plugin.
- `net:kassensichv-middleware.fiskaly.com` permission gates the
  `http_request` host function (see
  `universal-till/internal/plugins/wasm_hostfns.go`'s `hostHTTPRequest`) —
  changed 2026-08-18 from `net:kassensichv.io`, which is confirmed dead (see
  Status above); it covers SIGN DE only, NOT DSFinV-K (still on the dead
  host, still unverified — whoever confirms its real host must add its own
  permission here, not assume this one covers it). This is the first-party
  host function ADR-0025 flagged as an open follow-up ("whether TSE signing
  needs a first-party host function... a real question for whoever builds
  ut-plugin-tax-de"); it already existed by the time this repo was built
  (the SumUp plugin uses
  the same mechanism with `net:api.sumup.com`), so no new host-side work was
  needed.
- `countries: ["DE"]` in `manifest.json` — the new `PluginListing` field
  ADR-0025 decision 3 introduces. **Not yet enforced by the marketplace
  schema** (`ut-cloud/pkg/manifest` has no `Countries` field as of this
  writing) — set here as forward-looking metadata per the task that created
  this repo; do not attempt to modify the marketplace repo from here, that's
  tracked separately.

## Before committing

- `go test ./...` — now covers `src/datev`, `src/fiskalyparse`,
  `src/fiscalsign` and `src/wasmrun` (the last actually compiles and runs
  the plugin as wasm, so it is slower than the rest; `src/main.go` remains
  untestable directly, wasip1-only).
- `bash scripts/build.sh` (the real build check — cross-compiles
  `GOOS=wasip1 GOARCH=wasm`).
- `bash scripts/validate.sh` (manifest shape: `canonical_type: tax`,
  `countries: ["DE"]`, one `tax`-type and one `export`-type entry).
- Keep README.md's status table and this file's caveats current — this repo
  exists specifically to be honest about what's real vs. placeholder; don't
  let code changes silently upgrade a claim without re-verifying it.
- Standards & decisions live in the docs repo (`adr/`, ADR-0007
  document-first). Behaviour changes update the README in the same session.
