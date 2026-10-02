# Code review: refuse a day-close with revenue but no cross-tab in the DATEV export

**Date:** 2026-10-02
**Card:** universaltill/ut-docs#3421
**Branch:** `fix/3421-datev-close-without-cross-tab`
**Complexity:** medium
**Dev:** Opus 5.5, inline (cloud routine lane `:54`)
**Reviewer:** Fable, one independent subagent with a fresh context (read-only; it ran the gate itself)

## What shipped

- `src/datev/closes.go`: `BuildFromCloses` gets one more up-front
  validation. It refuses a close when `Net − VouchersIssued != 0` (the
  revenue the cross-tab must carry) and no cross-tab cell is non-zero. The
  refusal names the close (`close Z<n> (<day>)`). Before this, such a close
  added zero rows. In a multi-day batch, that day's takings were silently
  left out of an export that still reported success.
- `EODReportForExport` now reads `net` from the host's report JSON. The
  host already sends the full `EODReport`.
- Tests: `TestBuildFromCloses_RevenueWithoutCrossTab_RefusesByName` covers
  an empty cross-tab, an all-zero-cell cross-tab and a negative net, each
  next to a normal close. `TestBuildFromCloses_NoCrossTabWithoutRevenue_StillBuilds`
  covers a zero-sales day, a voucher-only day and a same-day sale plus full
  return, all of which must still build.
- `manifest.json` 0.10.0 → 0.10.1. README: a DATEV bullet sentence, plus
  Known gap #9 (a partial cross-tab is not detected).

## Design decision: Net, not Gross

The card proposed "gross != 0". On the host (universal-till), `Gross` sums
sales only (`sale_type='sale'`), and `Net = Gross − RefundTotal`. The
cross-tab sign-flips returns into its cells. The host documents the
identity `sum(cell gross) == Net − vouchers issued` (eod_tax_bands.go,
voucher_repo.go). With `Gross`, a day whose only trade was a sale and its
full return would be refused falsely. Mutation-checked: swapping `Net`
back to `Gross` fails the sale-and-return case.

## Verification

- TDD: both new refusal tests failed before the fix (no error returned).
  Disabling the check makes `RefusesByName` fail again. Using `Gross` in
  place of `Net` fails `StillBuilds` (Z82).
- Gate: `gofmt -l .` clean, `GOOS=wasip1 GOARCH=wasm go vet ./...` clean,
  `go test ./...` (including the wasm `src/wasmrun` suite) ok,
  `scripts/build.sh` ok, `scripts/validate.sh` ok at v0.10.1.
- The reviewer checked the host side: tips sit outside Gross/Net,
  multi-purpose vouchers sit in Net and in no cell, single-purpose vouchers
  sit in a cell and not in `vouchers_issued`, imported vouchers are in
  neither, voided sales are filtered on both sides, zero-tendered sales
  have total 0, and `apportionAmount` cannot floor a whole day to zero.
  Every current `eod` archive path attaches the cross-tab before archiving,
  so a pre-cross-tab archive is the realistic trigger.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Neither the comment nor the README named the residual gap: a present-but-partial cross-tab still exports | Fixed: code comment plus README Known gap #9. Asserting the full identity was rejected because legacy pre-#1008 archives would be refused falsely. |
| 2 | should-fix | The message said "has no cross-tab" for the all-zero-cell case and "shows revenue" for a negative net | Fixed: it now says "a non-zero net … no non-zero … cross-tab cell" |
| 3 | nit | The `NormalCashCardVoucherDay` fixture had no `Net` | Fixed: `Net: 2178` |
| 4 | nit | Ragged README wrap | Fixed when the sentence was rewritten |

Accepted risk: an archive with no `net` field at all would be accepted
silently. `net` is a core Z-report field that predates the cross-tab
(#1004) and the voucher fields (#1008). The reviewer's clone was shallow,
so this could not be proven from history.

## Verdict

Safe to merge. Nothing fiscal is misbooked, and no ordinary close is newly
refused.
