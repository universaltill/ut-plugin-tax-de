# Review — tax.rate.ask order_type "none" (ut-docs#3902)

Date: 2026-10-08 · Lane: lane:cloud-24 · Built by: Sonnet (Dev subagent) ·
Reviewed by: Opus 5.5, fresh context, in isolated worktrees.

## What shipped

universal-till sends `tax.rate.ask` with `order_type` `""` (dine-in/eat-in),
`"takeaway"`, or — since ut-docs#3632 — `"none"` when the shop has the
dine-in/takeaway choice switched off.

- **ut-plugin-tax-uk 1.2.2** — `handleTaxRateAsk` now consults
  `eatin_standard_rate_by_tax_code` only for `order_type == ""`. Takeaway,
  `"none"` and any unknown value answer no opinion without reading the
  setting (fail-safe: an unknown value never applies the eat-in uplift).
  Table-driven wasmrun test over takeaway/none/delivery/Takeaway/" ", with
  `stubHost` recording setting reads; new eat-in-unconfigured-code test;
  merchant manual (`content/index.html`) and README updated; version pinned
  in manifest, `hmrcvat.VendorVersion` and the MTD test.
- **ut-plugin-tax-de 0.10.4** — `Resolve` already declined anything but
  `"takeaway"`; `TestResolve_NoneNeverConsultsOverrides` pins it (none,
  delivery, Takeaway). Doc comment + README name `"none"`.
- **ut-docs** — `architecture/wasm-runtime.md` documents the
  `tax.rate.ask` payload and the three `order_type` values.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | UK merchant manual (`content/index.html`) didn't say the eat-in switch never fires when the dine-in/takeaway choice is off | fixed — third bullet |
| 2 | minor | UK README claimed wasmrun covers "eat-in with no override configured declines"; no such test existed (pre-existing) | fixed — added `TestTaxRateAsk_EatInUnconfiguredCodeAnswersNothing`; fails when the not-configured guard is removed |
| 3 | nit | over-long lines in both READMEs and the DE doc comment | fixed |
| 4 | nit | DE test pins existing behaviour (not red-first) | accepted — the behaviour was already correct |

Reviewer checked the key correctness risk: core can only put `""` or
`"takeaway"` on a line (`NormalizeLineOrderType`, `SetLineOrderType`,
held-sale resume, sale completion all normalise; `"mixed"` is header-only;
no `dine_in`/`eat_in` value exists), and rewrites `""` to `"none"` only when
`OrderTypeOff()`. So the fail-safe can't silently drop a real eat-in uplift.
The ask cache keys on the sent order type, so toggling Off can't serve a
stale answer.

## Verified beyond the automated tests

- UK TDD re-verified by the reviewer: old `main.go` → none/delivery/
  Takeaway/" " subtests fail with stdout `{"rate_bp":2000}`; new → pass.
  Moving the order-type check after the settings read fails all 5 subtests
  ("must not consult it"), so the no-read assertion is real.
- Gates green in both repos: gofmt, wasip1 vet, `go test ./...`, build,
  validate, i18n guard, package; DE also `check-version-bump.test.sh` and
  `check-version-bump.sh` (0.10.3 → 0.10.4).
- No UI surface in core touched; the UK manual page is static HTML (one
  bullet), not visually re-checked.

## Verdict

Safe to merge. Merging bumps the version, so `auto-tag-release.yml` cuts the
signed release for each plugin.
