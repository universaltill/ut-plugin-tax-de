# Germany Fiscal Compliance (TSE + DSFinV-K) — Universal Till plugin

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing Germany's KassenSichV
fiscal requirements via **fiskaly**'s Cloud-TSE ("SIGN DE") API for
per-transaction TSE signing, and fiskaly's DSFinV-K API for tax-audit
exports. Built per [`ut-docs` ADR-0025](https://github.com/universaltill/ut-docs/blob/main/adr/0025-country-tax-and-fiscal-compliance.md)
("Country-specific tax rates and fiscal compliance"), which chose fiskaly as
the first integration target because SumUp itself delegates TSE signing to
fiskaly rather than building it in-house — real-world precedent that this is
the standard integration shape for German fiscal compliance.

## Status: starting skeleton, not a certified compliance solution

Read this section before installing on anything that isn't a test till.
Labeled the way `germany-pos-parity-backlog.md` labels claims — "confirmed"
vs. "researched, not tested":

| Claim | Status |
|---|---|
| Endpoint paths/request shapes match fiskaly's real, public API | **Researched, not tested.** Grounded in fiskaly's public docs (developer.fiskaly.com, kassensichv.net, kassensichv.io) as of 2026-07-28 — not invented from nothing. Every endpoint is flagged `NEEDS SANDBOX VERIFICATION` in code comments. |
| Tested against a real fiskaly sandbox/production account | **Not done.** No fiskaly credentials were available at write time. Nothing below has been run against a live TSS. |
| DSFinV-K export format/content is legally compliant | **Not verified, at all.** No DSFinV-K output from this plugin has been checked against the DSFinV-K spec or a real tax audit. |
| KassenSichV compliance overall | **Not certified.** This is a starting skeleton. A merchant must get real legal/tax-advisor sign-off before relying on this for a live business — this plugin existing does not make a till KassenSichV-compliant. |
| `go build ./...` / `scripts/build.sh` | **Confirmed** — builds clean as of this commit (see CI). |
| End-to-end behavior against the till's real event bus | **Not verified.** Unlike `ut-plugin-payment-sumup` (which was run against the real wazero host runtime before shipping), this plugin has NOT been exercised against `universal-till`'s actual `WasmRuntime.HandleEvent` — say so plainly rather than claim it works. |

## What this plugin does

Two canonical-type entries in one manifest, per ADR-0025 (no new plugin type
needed, ADR-0002's `tax`/`export` types already exist):

- **`tax` — TSE signing.** Hooks `sale.completed`. For each completed sale,
  attempts a real two-call TSE-sign round-trip against fiskaly's SIGN DE API:
  start the transaction (`state: ACTIVE`), then finish it (`state:
  FINISHED`) with a receipt schema built from the sale's real line items
  (VAT-rate buckets from `tax_rate_bp`) and payments (cash vs. non-cash).
- **`export` — DSFinV-K export.** Calls fiskaly's DSFinV-K API to trigger an
  export for a date range. **Not wired to any till UI** — see "Known gaps"
  below.

## Known gaps (read before assuming this "just works")

1. **Cloud-TSE vs. offline-first — the tension ADR-0025 explicitly flags as
   unresolved, not solved here.** `sale.completed` is dispatched
   **non-blocking** and fires **after** the sale has already completed
   (confirmed against `universal-till/internal/plugins/ipc.go` and
   `wasm_runtime.go`: non-blocking events are enqueued to a drainer
   goroutine whose result is discarded — a plugin error is logged, never
   retried, never surfaced to the operator or any UI). Concretely: **this
   plugin has no architectural way to block or reverse a sale if fiskaly is
   unreachable.** By the time it runs, the till has already completed the
   sale, per ADR-0003's non-negotiable offline-first requirement. What this
   code does instead: it **never fabricates a signature**. A failed sign
   attempt is logged loudly (`log_write`) and the sale is recorded as
   "unsigned, pending retry" in plugin storage (same bounded-queue pattern
   `ut-plugin-integration-webhook` uses for undelivered sales), rather than
   silently marked compliant. Whether an unsigned-then-backfilled sale
   satisfies KassenSichV's "irreversible, tamper-proof at time of
   transaction" requirement is exactly the open question ADR-0025 flags as
   needing real TSE-vendor/legal confirmation — **not decided or resolved
   by this plugin.** A local/hardware TSE path (dongle or TSE-integrated
   printer) may end up being the right primary target specifically because
   of this tension; that's a real decision for whoever takes this further,
   not something this skeleton commits to.

2. **DSFinV-K export is not actually reachable from the till today.**
   Confirmed against `ut-docs/reference/plugin-manifest.md` and
   `universal-till/internal/plugins/types.go`: the plugin engine currently
   dispatches `page`, `button`, and `theme` canonical types natively;
   `report`/`export` types are registered and listed on the plugin info
   card but have **no dispatcher yet**. `exportDSFinVK` in `src/main.go` is
   real, callable code (triggered here by a placeholder event type,
   `tax.de.dsfinvk.export.requested`, that nothing in the host currently
   publishes) — not a stub, but also not something a merchant can click a
   button to run yet.

3. **DSFinV-K export would be incomplete even once wired up.** A real
   DSFinV-K export depends on `cashPointClosing` records — periodic
   aggregates of every signed transaction — existing in the fiskaly account
   before `/export` is triggered. This plugin does **not** build those from
   the `tse_result:*` records it saves after each sign attempt. Calling
   `exportDSFinVK` today triggers an export against whatever (likely empty)
   closings already exist, not a real export of this till's sales. That
   aggregation step is real, non-trivial work, left undone here.

4. **VAT-rate and payment-type bucketing is best-effort.** `vatRateBucket`
   maps 19%/7% basis-point rates to fiskaly's `NORMAL`/`REDUCED_1` schema
   enum; anything else falls back to `SPECIAL_RATE_1` rather than guessing
   further (fiskaly's enum also has `REDUCED_2`/`NULL`/`SPECIAL_RATE_2-5`).
   `paymentTypeBucket` only distinguishes `CASH` vs. `NON_CASH` (case-
   insensitive match on `"cash"`) — good enough for the schema's
   granularity, but not exhaustively tested against every payment method
   string the till can produce.

## What's real vs. placeholder

**Real (actual HTTP calls to fiskaly's documented API shape, not stubs):**
- `fiskalyAuth` — `POST /auth` with `api_key`/`api_secret`, caches the
  returned `access_token` in plugin storage, re-authenticates when the
  cache is stale or missing.
- `signTransaction` — the two-call SIGN DE transaction lifecycle (`PUT
  /tss/{tss_id}/tx/{tx_id}?tx_revision=1` then `?tx_revision=2`), building
  a `standard_v1` receipt schema from the sale's real line items/payments.
- `exportDSFinVK` — `POST {dsfinvk_base}/export` with a business-date range
  and format.
- Failure handling — never returns a fabricated signature; logs loudly and
  queues for retry (see `unsigned_queue` in plugin storage).

**Placeholder / unconfirmed:**
- The exact fiskaly API host/version (`signDEBase`, `dsfinvkBase` constants
  in `src/main.go`) — fiskaly has had multiple entry points over time
  (`kassensichv.io` legacy, `kassensichv.fiskaly.com` v2,
  `workspace.fiskaly.com` newer portal). Confirm the current one against
  the merchant's actual fiskaly dashboard before going live.
- The `standard_v1` schema field names (`amounts_per_vat_rate`,
  `amounts_per_payment_type`, the `vat_rate`/`payment_type` enum values) —
  reconstructed from fiskaly's public docs/support articles, not confirmed
  against a live sandbox response.
- `tx_id` generation (`txID` in `src/main.go`) — fiskaly's docs examples
  all use UUIDs; this derives a deterministic pseudo-UUID from the sale id
  since the till's `sale_id` isn't guaranteed to already be one. Whether
  fiskaly strictly requires a v4 UUID or accepts any unique string is
  unconfirmed.
- The `FINISHED`-transaction response envelope (`parseSignResponse`) —
  best-effort field names (`tss_tx_result.signature.value`,
  `tss_tx_result.log_time`), not confirmed.
- The DSFinV-K `/export` trigger request/response shape — reconstructed
  from fiskaly support docs ("ByBusinessDate"/"ByCreationDate" selection,
  TAR/ZIP format), not confirmed.

Every one of the above is also flagged `NEEDS SANDBOX VERIFICATION` at its
definition in `src/main.go` — the code comments are the source of truth if
this README drifts.

## Configure (plugin settings)

- `fiskaly_api_key` / `fiskaly_api_secret` — the merchant's fiskaly API
  credentials (fiskaly dashboard → API keys).
- `fiskaly_tss_id` — the TSS (Technical Security System) id created in the
  merchant's fiskaly account. **Not created by this plugin** — TSS creation
  is a one-time setup step, done via the fiskaly dashboard/API before this
  plugin is installed.
- `fiskaly_client_id` (register-scoped) — the fiskaly "client" id
  representing this specific till/register (fiskaly's model: one client per
  point of sale).
- `fiskaly_cash_register_id` — the DSFinV-K API's cash register id (a
  separate concept from the SIGN DE `client_id`, per fiskaly's docs — some
  fiskaly accounts share one id across both APIs, some don't; verify
  against the merchant's account).
- `dsfinvk_export_format` — `zip` (default) or `tar`.

## What a human still needs to do before this could ever go live

1. **Get a real fiskaly account** (sandbox first) and run every endpoint in
   `src/main.go` against it — resolve every `NEEDS SANDBOX VERIFICATION`
   comment, fix whatever doesn't match reality.
2. **Legal/tax-advisor review** of the whole approach, not just the export
   format — KassenSichV compliance is a legal question this plugin cannot
   answer by existing.
3. **Resolve the offline-first tension** (Known gaps #1) — decide, with
   real TSE-vendor/legal input, whether cloud-TSE-with-retry is acceptable
   or whether a local/hardware TSE path is required, per ADR-0025's
   explicitly-unresolved open question.
4. **Build the cash-point-closing aggregation** (Known gaps #3) so a
   DSFinV-K export is actually complete, and wire canonical_type `export`
   into an actual dispatcher/UI trigger in `universal-till` (Known gaps #2)
   so a merchant can run one.
5. **Test against the real wazero host runtime**, not just `go build` — the
   way `ut-plugin-payment-sumup` was verified before its reader-checkout
   path shipped.

## Build

```sh
bash scripts/build.sh   # -> bin/plugin.wasm (GOOS=wasip1 GOARCH=wasm)
```

`go build ./...` from the repo root matches every sibling plugin repo's
behavior: it prints `go: warning: "./..." matched no packages` and exits 0,
because `src/main.go` is gated `//go:build wasip1` — the real build check is
`scripts/build.sh`, which cross-compiles for the actual target.
