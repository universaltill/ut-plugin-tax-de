# Code review — `tse_result:*` storage bounded to a fixed ring (ut-docs#1299)

**Date:** 2026-09-05
**Card:** universaltill/ut-docs#1299 (`bug`, `compliance`, `pilot:germany`,
`complexity:medium`)
**Branch:** `pipeline/1299-tse-result-storage-ring`
**Dev:** inline (Sonnet, this pipeline cycle — medium tier)
**Reviewer:** independent fresh-context subagent, model `opus` (medium-tier
review, per model routing — never saw the dev reasoning)

## What shipped

`recordResult` (`src/main.go`) persisted a signing attempt's outcome keyed
by `"tse_result:"+sale_id` — one NEW plugin-storage key per sale, forever,
against core's 1024-key-per-plugin `StorageMaxKeys` cap
(`internal/data/plugin_repo.go`). A steadily-trading shop exhausted that
cap within roughly a thousand sales; the failure was confirmed silent to
the operator by reading the actual host-function wiring, not assumed:
`storagePut`'s only error handling is a `log_write` call, and
`internal/plugins/wasm_hostfns.go`'s `hostLogWrite` unconditionally maps
that to `logging.L().Infof` on the host side — never `Warnf` — so it can
never reach the operator-visible Problems ring
(`internal/logging/logging.go` only surfaces `Warn`+).

- New package `src/auditkey` (no `wasip1` tag, host-testable — same
  pattern as `src/fiscalsign`/`src/taxrate`): `TSEResultKey(saleID)`
  hashes the sale id (FNV-1a) into one of `RingSize` (256, a power of two)
  fixed buckets, returning `"tse_result:%03d"`. Total plugin-storage usage
  from this source is now capped at 256 keys for the life of the till,
  regardless of sale volume — the failure mode this card exists to fix
  cannot recur.
- `recordResult` now calls `storagePut(auditkey.TSEResultKey(res.SaleID), ...)`.
- Safe because `tse_result:*` has zero readers anywhere in this codebase
  (confirmed by grep across this repo, `universal-till`, `ut-cloud`,
  `ut-docs`) and is not the system of record either way: core
  independently durably records every outcome via a different mechanism
  per branch — approved sales land in `fiscal_tse_signatures` (ADR-0044),
  not-signed sales get a permanent audit-journal marker plus a receipt
  notice and operator alert (`declareUnsignedFiscalSale`).
- New end-to-end regression test `TestRecordResult_UsesBoundedRingKey`
  (`src/wasmrun`) drives the REAL compiled `.wasm` through 5 distinct
  sales and asserts every resulting storage key matches the bounded ring
  format and is never the old unbounded per-sale-id literal. Complemented
  by `src/auditkey`'s own unit tests (determinism, format, a 50,000-sale
  hard-bound proof, distribution sanity, a fixed non-colliding-pair pin).
- `manifest.json` bumped `0.5.2` → `0.5.3` (behavior change;
  `release.yml` gates a release on `tag == manifest.version`).
- README.md (Known gap #8, new; gap #3 corrected) and CLAUDE.md
  (code-layout section) updated in the same session, per repo convention.
- Explicitly NOT fixed here, documented as a separate follow-up instead:
  no WASM plugin can reach the operator-visible Problems ring at all —
  `log_write` has no severity parameter and there's no
  `report_problem`-shaped host function. That's a host-ABI change in
  `universal-till` affecting every plugin, not just this one — same
  blast-radius reasoning as this card's own non-goal on raising
  `StorageMaxKeys`.

## Independent review — findings

**0 blocking.** Four should-fix items, all applied before this commit:

1. `manifest.json` version was un-bumped — bumped to `0.5.3` (mechanically
   required per this repo's own prior review record, `2026-09-03-...-1404.md`
   finding 4: `release.yml` gates on `tag == manifest.version`).
2. `README.md` gap #3 and `exportDSFinVK`'s doc comment (`main.go`)
   pointed a future DSFinV-K-aggregation implementer at `tse_result:*` as
   raw material — after this change it's a lossy 256-entry sample, and
   aggregating from it would silently under-report. Both reworded to
   point at core's sales data / `fiscal_tse_signatures` instead.
3. `TestTSEResultKey_DifferentIDsUsuallyDifferentBuckets` used `t.Skip` on
   its only failure branch, making it structurally unable to ever fail —
   exactly the "test that cannot notice the code changing is not a test"
   trap `src/wasmrun`'s own package doc warns about. Reviewer proved this
   by mutating `TSEResultKey` to ignore its input: the test **skipped**
   instead of failing. Changed to `t.Fatalf` against two fixed,
   confirmed-non-colliding fixture ids — deterministic, not flaky.
4. `recordResult`'s doc comment and README gap #8 overstated "core's
   `fiscal_tse_signatures` table already durably records every outcome" —
   that table only holds *approved* sales; failures are durably recorded
   by a different mechanism (`declareUnsignedFiscalSale`'s audit-journal
   marker). Both reworded to name both paths correctly. Reviewer also
   independently verified this by reading `internal/pages/
   fiscal_sign_hook.go` — `RecordFiscalTSESignature` is called from
   exactly one call site, on the approval branch only.

## What the reviewer verified beyond reading

- **TDD re-derived independently, not trusted**: reverted `recordResult`
  to the pre-fix `storagePut("tse_result:"+res.SaleID, ...)` in a scratch
  copy, rebuilt the real `.wasm`, ran `TestRecordResult_UsesBoundedRingKey`
  — failed on the first sale with the expected non-tautological message
  (`found old-style unbounded key "tse_result:sale-ring-a" in storage`).
- **Bound genuinely holds**: `h.Sum32() & (RingSize-1)` with
  `RingSize=256` always yields `[0,255]`; `%03d` always emits exactly 3
  digits — no path outside `tse_result:NNN`. Confirmed the power-of-two
  invariant is empirically (not just structurally) enforced: setting
  `RingSize=300` in a scratch copy made `TestTSEResultKey_Distributes`
  fail immediately (`only 32 distinct buckets hit ... want >= 225`).
- **No reader breaks**: grepped this repo, `universal-till`, `ut-cloud`,
  `ut-docs` for any code reading `tse_result:` by prefix or exact key —
  none found; core's only prefix reader is unrelated (`fiscal_register:`).
- **Compliance conclusion re-verified independently**, not taken on
  faith: read `internal/data/fiscal_repo.go` and `internal/pages/
  fiscal_sign_hook.go` directly to confirm both the approved-sale and
  failed-sale durability paths exist and are unaffected by this change.
- Additional factual finding surfaced by the reviewer and independently
  re-confirmed by the dev: the pre-fix bug was slightly worse than
  stated — `plugin_storage`'s cap is counted per `plugin_id`, and
  `internal/data/fiscal_repo.go`'s `FiscalRegisterDEKeyPrefix` writes
  (`fiscal_register:*`) share this same plugin's storage row-space
  (`taxDePluginID = "com.universaltill.tax-de"`, `internal/pages/
  import_page.go`), so unbounded `tse_result:*` growth would eventually
  have blocked core's own fiscal-register writes too, not just future
  ones from this plugin. Now noted in README gap #8.
- Full gate run independently by the reviewer: `gofmt -l .`,
  `go build ./...`, `go vet ./...`, `GOOS=wasip1 GOARCH=wasm go vet ./...`
  (matching `.github/workflows/ci.yml`), `go test ./...` (all 6 packages),
  `scripts/build.sh`, `scripts/validate.sh`, `scripts/package.sh` — all
  green.

## Full gate (orchestrator, after applying the should-fix items)

- `go build ./...` clean.
- `bash scripts/build.sh` — `bin/plugin.wasm` built, 3,611,979 bytes.
- `go test ./...` — all 6 packages green (`src/wasmrun` 26.1s, includes
  the new end-to-end test).
- `gofmt -l .` — no output.
- `go vet ./...` and `GOOS=wasip1 GOARCH=wasm go vet ./...` — clean.
- `bash scripts/validate.sh` — `ok com.universaltill.tax-de v0.5.3`.

## Non-blocking notes (not applied — genuinely optional)

- A compile-time power-of-two guard for `RingSize` was suggested
  (`const _ uint = 0 - (RingSize & (RingSize - 1))`); the `Distributes`
  test already catches a violation empirically, so this is
  belt-and-braces, not applied here.
- `auditkey.go` now documents precisely that "ring" retains whichever
  sale most recently hashed into each bucket, not literally the most
  recent N sales — added directly to the doc comment (cheap, done).
- A prior card's code-review record
  (`2026-09-03-fiscal-sign-ask-sale-type-return-receipt-1404.md`) left a
  deferred follow-up to "add `sale_type` to the permanent
  `tse_result:<sale_id>` audit record" — that record is now neither
  permanent nor per-sale-id, so that follow-up needs rescoping (or
  dropping) by whoever picks it up. Noted here rather than chased down as
  a separate action this cycle.

## Safe-to-merge verdict

Yes. Independent review found zero blocking issues; all four should-fix
items applied and re-verified green.
