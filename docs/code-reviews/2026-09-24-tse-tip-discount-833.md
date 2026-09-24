# Review — TSE receipt: tips and whole-bill discounts (ut-docs#833)

**Date:** 2026-09-24 · **Author:** pipeline (Opus 5.5) · **Reviewer:** independent Fable subagent
**Repos:** `ut-plugin-tax-de` (v0.7.0), `universal-till` (`tip_recipient` on the sign payload), `ut-docs` (contract `fiscal-sign-ask` 1.9.0)

## What shipped

`fiscalsign.BuildReceipt` replaces `BalanceDelta`'s blanket refusal of every tipped or
whole-bill-discounted sale. It builds both halves of fiskaly's `standard_v1` receipt
(DSFinV-K `Beleg^<gross per rate>^<per payment type>`) so they agree:

| Situation | Treatment | Basis (sources on ut-docs#833) |
|---|---|---|
| Pricing mode | `tax_inclusive` (contract 1.2.0), inferred only when absent | contract |
| Whole-bill discount | split by gross per rate, floors, remainder on highest rate | DSFinV-K Anhang C `Rabatt`, UStAE 10.3; same as core `VATBandsForSale` |
| Service charge | already inside `vat_breakdown` (contract 1.5.0), never re-applied | ADR-0061 |
| Employee tip | 0% `NULL` bucket | DSFinV-K `TrinkgeldAN`, USt-Schlüssel 5 |
| Business tip | setting `tip_business_vat_treatment`: `proportional` (default, taxable rates only) / `standard_rate` / `refuse`; unknown or unreadable → refuse | DSFinV-K `TrinkgeldAG` fixes no rate → several defensible methods → setting |
| Tip on the payment side | counted once; `total` decides whether core already put it in `amount` | core has both conventions (ut-docs#2571) |
| Rate outside 19/7/10.7/5.5/0% | refused (was silently `SPECIAL_RATE_1`) | review finding 1 |
| Anything unreconcilable | `cannot-sign` (contract 1.3.0; was `unreachable`) | contract |

Core now sends `tip_recipient` per tipped payment (already resolved from `charge.policy.ask`
before dispatch). Cash rounding: core has no rounding feature, so there is nothing to sign; if
one is added it must come back through this mapping.

## Findings (independent review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Unknown VAT rates were signed as `SPECIAL_RATE_1` (10.7%) | **Fixed**: `VATRateBucket` returns ok=false outside 19/7/10.7/5.5/0%; `BuildReceipt` refuses; tests added |
| 2 | major | Tip-in-amount inference ambiguous when a reader-reported tip meets an over-tender of exactly Σtip | **Accepted, documented** (code, README, contract); core fix filed as **ut-docs#2571** |
| 3 | minor | Proportional business tip could land in `NULL` via 0% lines | **Fixed**: weights use taxable rates only; fully zero-rated sale refuses; tests added |
| 4 | minor | Host error reading the setting fell back to the default | **Fixed**: only "not set" (-1) uses the default; other errors → refuse |
| 5 | minor | README said the payload has no `tax_inclusive`; package doc said contract 1.6.0 | **Fixed** |
| 6 | minor | Contract said an unset recipient is sent as employee without noting the policy default is applied first | **Fixed** |
| 7 | nit | 1-cent divergence from core when the highest band has zero gross | Accepted (plugin behaviour is the saner one) |
| 8 | nit | Missing tests (unknown recipient, inclusive flag on exclusive numbers, 0% line with business tip) | **Fixed** |

## Verified beyond unit tests

- Mutation run in a scratch worktree: employee tip moved to 19%, tip dropped on the payment
  side, remainder on the lowest rate, inclusive read as exclusive, wrong standard rate,
  unknown setting not refused: **every mutation fails the suite** (1–13 failing tests each).
- `src/wasmrun` drives the real compiled `plugin.wasm` in wazero: the card's own example
  (€12.90 + €1.10 employee tip → `NORMAL 12.90 / NULL 1.10 ^ NON_CASH 14.00`) signs; a mixed-rate
  €2 discount signs as 10.80 / 7.20; `refuse` and unbalanced payloads answer `cannot-sign` with
  zero fiskaly calls.
- Gates: `go vet ./...`, `go test ./...`, `scripts/build.sh`, `scripts/validate.sh`,
  `check-version-bump.test.sh` clean; core `go build ./...` + full `go test ./...` green.

**Not verified:** the `NULL`/`REDUCED_1`/`SPECIAL_RATE_*` buckets have never been round-tripped
through a live fiskaly sandbox (README gap 7); the research leaned on GitHub copies of DSFinV-K
Anhang C and search snippets, because bzst.de and the vendor sites were unreachable from this
sandbox. The pilot's Steuerberater confirms the business-tip setting.

**Verdict:** safe to merge.
