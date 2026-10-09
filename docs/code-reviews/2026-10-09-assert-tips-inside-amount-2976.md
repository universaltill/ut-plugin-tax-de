# Review — assert Σamount = total + Σtip (ut-docs#2976)

Date: 2026-10-09 · Lane: lane:cloud-54 · Built by: Opus 5.5 (inline) ·
Reviewed by: Fable, fresh context. Card sized medium at pick time (fiscal
signing, money/tax) from its `complexity:easy` label.

## What shipped

- **ut-plugin-tax-de 0.11.0** — `fiscalsign.BuildReceipt` no longer infers
  whether a payment's `amount` includes its tip (`paid == total` → on top,
  `paid == total + tips` → inside). Since contract 1.11.0 (ut-docs#2571,
  universal-till v0.27.1) core always puts the tip inside `amount`, so the
  plugin now requires `Σamount == total + Σtip` to the cent and answers
  `cannot-sign` otherwise (core refuses the tender, ADR-0136). The payment
  side is `Σamount` with no tip added.
- Why unconditional and not "only for ≥ 1.11.0 hosts" (the card's
  alternative): the payload carries no contract version, and
  `min_pos_version` gates nothing (core's installer compares against a
  hardcoded `"2.5.0"` — ut-docs#3288), so the plugin cannot tell an old
  core apart. A pre-v0.27.1 core loses only reader-tipped sales, refused at
  the till, never signed wrong; no German shop signs in production yet.
- Tests: `TestBuildReceipt_TipOutsideAmountRefused` (single leg, business
  tip, split tender; also asserts the refusal names contract 1.11.0),
  `TestBuildReceipt_PaymentsOffByOneCentRefused`, and the compiled-wasm
  `TestFiscalSignAsk_TipOutsideAmountIsCannotSign` (cannot-sign, zero
  fiskaly calls). Existing tests moved to the 1.11.0 shape.
- README (tip bullet; "residual risk" paragraph removed), CLAUDE.md (fourth
  pinned invariant + why inference must not return), contract-tracking
  comments → 1.11.0.
- ut-docs `reference/contracts/fiscal-sign-ask.md` known-consumers entry
  and the 1.11.0 note updated (separate ut-docs PR).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | CLAUDE.md didn't record the new invariant or why inference must not come back | fixed |
| 2 | minor | `fiscalsign.go` contract-tracking comments still said 1.10.0 / 1.9.0 | fixed |
| 3 | nit | Removing the explicit check leaves tests green — the halves-balance check refuses the same inputs | fixed — unit test now asserts the "contract 1.11.0" refusal text |
| 4 | nit | `min_pos_version` still `1.0.0` | accepted — gates nothing; ut-docs#3288 owns the real gate |
| 5 | nit | int64 wrap on summing payments | accepted — pre-existing; core refuses overflow before dispatch |
| 6 | nit | ut-docs contract rows stale | fixed in the ut-docs PR |

Reviewer verified in universal-till (read-only) that no core path sends an
exactly-paid sale with `Σamount ≠ total + Σtip`: `buildFiscalSignPayload`,
reader-reported tip (`applyPluginReportedTip` adds to both fields),
request-carried tip (400 unless inside amount), refunds/returns (one leg,
no tip), vouchers, and sync replay (never dispatches the ask).

## Verified beyond unit tests

- TDD: the new unit tests failed before the fix; the wasm test, run against
  the old `receipt.go`, failed with "contacted fiskaly 3 times" (the old code
  signed the tip-on-top receipt) and passes with the fix. The reviewer
  independently re-ran the mutation (re-instated inference → test fails).
- Gate: `gofmt -l .`, `go vet ./...`, `GOOS=wasip1 GOARCH=wasm go vet ./...`,
  `go test ./...` (wasmrun included), `scripts/build.sh`,
  `scripts/validate.sh`, `scripts/guard-plugin-i18n.sh`,
  `scripts/package.sh` — all pass.
- No UI surface touched.

**Verdict:** safe to merge.
