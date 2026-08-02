# Code review: grant sales:read (companion to universal-till's export permission gating)

**Date:** 2026-08-02
**Scope:** `manifest.json` only.
**Trigger:** companion to `universal-till`'s ut-docs#228 (the
`export.requested.ask` dispatcher now gates its `sales`/`stock` payload
fields on `sales:read`/`inventory:read` respectively, rather than the
generic `events:receive` alone). Full review record and independent-review
detail: `universal-till/docs/code-reviews/2026-08-02-export-permission-gating.md`.

## What shipped

- Added `sales:read` to `manifest.json`'s `permissions[]`. Without it, the
  host would send `sales: null` in every `export.requested.ask` payload to
  this plugin once the paired `universal-till` change lands — and
  `datev.Build` refuses with `"datev export: no sales in %s to %s"` on an
  empty sales slice (`TestBuild_NoSalesInPeriod_Refuses`), so the
  `datev-buchungsstapel-export-de` entry would go from working to always
  erroring, a real functional regression, not a hypothetical.
- Deliberately did **not** add `inventory:read` — confirmed via `src/main.go`
  that the `export.requested.ask` payload struct this plugin decodes only
  has a `Sales` field, never `Stock`; neither the DSFinV-K nor DATEV path
  reads inventory data. Requesting a permission this plugin never uses
  would be over-granting.
- Version bumped `0.3.0` → `0.3.1` (real content change to the declared
  permission set). Not enforced by `release.yml` at merge time (it only
  checks version == tag at release time), but correct practice regardless.

## Verification

- `go test ./...`: green (the `datev` package's own unit tests, unaffected
  by a manifest-only change).
- `bash scripts/build.sh`: clean, builds `bin/plugin.wasm`.
- `bash scripts/validate.sh`: green — `ok com.universaltill.tax-de v0.3.1`.
- Beyond the above: the paired `universal-till` review built this exact
  compiled WASM module and drove it through the real wazero runtime with
  this new manifest (real accounting settings seeded), and it produced a
  genuine DATEV export file from live sales data — the strongest available
  proof this permission grant actually closes the regression it exists to
  prevent, not just that the manifest schema validates.

## Verdict

**Safe to merge.** One-line permission addition plus a version bump,
already exercised end-to-end by the paired `universal-till` review's real
compiled-module verification.
