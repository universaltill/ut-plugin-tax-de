# 2026-10-14 — sale discount apportioned over goods only (ut-docs#4240)

**Change:** `ut-plugin-tax-de` v0.12.1, branch `fix/4240-discount-over-goods`.
Plus `ut-docs` `reference/contracts/fiscal-sign-ask.md` known-consumer note.

## What shipped

- `fiscalsign.discountedGrossByRate` weights `sale_discount` by each band's
  **goods** gross (new `goodsGrossByRate`: band gross minus the gross of the
  single-purpose `voucher_issues` at that rate — inclusive `amount`,
  exclusive `amount + round_half_up(amount × rate_bp / 10000)`), per
  contract 1.14.1 § `sale_discount` steps 2–4. Remainder lands on the
  highest band with goods > 0 (`apportion` skips zero weights). A sale with
  no goods falls back to full gross, as `pos.VATBandsForSale` does.
- Refusals (`cannot-sign`): a single-purpose voucher larger than its band or
  at a rate the breakdown lacks; a rate outside 0–10000 bp; a discount that
  takes any band below zero.
- Version 0.12.0 → 0.12.1; README, CLAUDE.md, request-type doc comment.

Before: a discounted sale that also sold a specific-item voucher signed a
different per-rate split from the till's receipt / invoice / Z report
(e.g. goods 100.00 @19% + SP voucher 50.00 @7%, 10.00 off → signed
93.33 / 46.67 instead of 90.00 / 50.00).

## Review (independent, Fable — author was Opus 5.5)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | A discount larger than a band's goods next to an SP voucher passed the sum check (the voucher props up `total`) and signed a negative band (−1.00 @7%). Reachable via an uncapped tender discount + `issue_vouchers`. | **Fixed**: negative band → `cannot-sign`; `TestBuildReceipt_RefusesDiscountTakingGoodsBandNegative`. |
| 2 | nit (core, pre-existing) | Core's `VATBandsForSale` can put the floor remainder on a highest-rate band whose goods are 0 and that holds no voucher (−0.01); the plugin puts it on the highest positive band. | Out of scope here — filed as ut-docs#4275. |
| 3 | nit | Overflow guard only valid for `rate_bp ≤ 10000`. | **Fixed**: rate bounded explicitly. |
| 4 | nit | Stale wording in CLAUDE.md and `fiscalsign.go` ("tracking v1.14.0"). | **Fixed.** |

The reviewer confirmed by hand and scratch runs that the plugin's per-rate
gross equals `pos.VATBandsForSale`'s for: voucher in its own band, voucher
sharing a band, several vouchers in one band (per-voucher half-up rounding
= core's `ComputeTaxBasisPoints`), goods-0-next-to-voucher, remainder
placement, and the no-goods fallback; both new voucher refusals cannot fire
on a core-built payload. All test numbers recomputed by hand.

## Verified

- TDD: the six new tests were written first and failed on v0.12.0 (e.g.
  `VAT = NORMAL 93.33 / REDUCED_1 46.67`); the negative-band test fails with
  its guard disabled and passes with it.
- `go vet ./...`, `go test ./...` (incl. `src/wasmrun`, the real compiled
  wasm under wazero), `scripts/build.sh`, `scripts/validate.sh`,
  `scripts/check-version-bump.test.sh` — all pass.
- Backend-only, no UI surface; no live fiskaly call (pure mapping change).

## Verdict

Safe to merge after finding 1's fix (done). Release: `auto-tag-release`
cuts v0.12.1 on merge.
