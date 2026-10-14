# 2026-10-14 — sale-discount share capped at each band's goods (ut-docs#4282)

**Change:** `ut-plugin-tax-de` v0.12.2, branch `fix/4282-discount-band-cap`.
Plus `ut-docs` `reference/contracts/fiscal-sign-ask.md` known-consumer note.

## What shipped

- New `fiscalsign.splitSaleDiscount`, a line-for-line mirror of core's
  `pos.splitSaleDiscount` (contract 1.15.0 § `sale_discount` step 3,
  ut-docs#4277). Each band gets `floor(d × w / Σw)`, capped at its goods.
  The leftover minor units fill the bands from the highest rate down, each
  up to its cap. Anything still left goes on the highest-rate weighted band.
- `discountedGrossByRate` builds the caps:
  - inclusive: goods gross;
  - exclusive: band net less the single-purpose voucher amounts at that rate;
  - no-goods fallback: full gross (inclusive) or full net (exclusive), as in core.

  The negative-band refusal stays as the guard for a discount larger than
  every cap.
- The business-tip path still uses `apportion` (uncapped, unchanged).
- Version 0.12.1 → 0.12.2. README, CLAUDE.md, and the request doc comment
  ("tracking v1.15.0") are updated.

Before: on the card's repros (1.00 @7%, 2.00 @10.7%, 0.01 @19%, 3.00 off),
v0.12.1's uncapped remainder took 19% to −0.01. The plugin then answered
`cannot-sign`, so the till refused that sale. When a single-purpose voucher
in the same band kept that band positive, v0.12.1 signed a different split
from the Z report (exclusive example: 0.09 / 1.19 instead of 0.07 / 1.21).

## Review (independent, Fable — author was Opus 5.5)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Exclusive cap reads the payload net, which includes the folded service charge. Contract step 1 takes the charge's part out first. This is the same class as the known weight drift and is not made worse. | Accepted. The doc comment now says so. Pre-existing, out of scope. |
| 2 | nit | "highest weighted band" reads as "largest weight". | **Fixed**: "highest-rate band with weight > 0". |
| 3 | nit | `d*w` int64 overflow at ~1e10 × 1e9 minor units, identical to old code and core. | Accepted (unreachable amounts). |
| 4 | nit | Review record missing. | **Fixed** (this file). |

The reviewer ran a throwaway differential test: a verbatim mirror of core's
`VATBandsForSale` (minus the service charge) against `BuildReceipt` over 300k
random sales. The sales were inclusive and exclusive, with vouchers in goods
bands and in voucher-only bands, tiny and zero amounts, and discounts up to
above all goods. All 249,071 reconciling sales matched bucket for bucket.
All 50,929 non-reconciling ones were refused, never signed. The reviewer
recomputed every test's arithmetic by hand.

## Verified

- TDD: the three new tests failed on v0.12.1, two with `cannot-sign … 1900 bp
  below zero` and one with `NORMAL 1.19 / REDUCED_1 0.09`. They pass with the
  fix (revert → fail → restore → pass, done inline).
- Author's own differential check: `splitSaleDiscount` against core's slice
  version, 200k random weight/cap/discount inputs, identical.
- `gofmt -l .`, `go vet ./...` (host and `GOOS=wasip1`), `go test ./...` (incl.
  `src/wasmrun`, the real compiled wasm), `scripts/build.sh`,
  `scripts/validate.sh`, `scripts/guard-plugin-i18n.sh`,
  `scripts/check-version-bump.test.sh` all pass.
- Backend-only, no UI surface. No live fiskaly call (pure mapping change).

## Verdict

Safe to merge. Release: `auto-tag-release` cuts v0.12.2 on merge.
