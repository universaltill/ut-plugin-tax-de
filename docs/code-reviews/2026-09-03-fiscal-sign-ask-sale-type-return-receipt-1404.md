# Review: `fiscal.sign.ask` — wire `sale_type` through to a distinct receipt type (ut-docs#1404)

**Date:** 2026-09-03
**Card:** ut-docs#1404
**Author:** Farshid Mirza (pipeline, `lane:cloud-54`)
**Reviewer:** independent Opus subagent, worktree-isolated, different model from the implementer (Sonnet)

## What shipped

`fiscal.sign.ask`'s request payload gained a `sale_type` field
(`"sale"`|`"return"`) in contract 1.6.0 (`universal-till` side, ut-docs#1203,
merged independently of this repo — and, in the same pipeline cycle,
ut-docs#1405/`universal-till` PR #741 wired the actual refund/return
dispatch that now sends it). This plugin is the sole TSE signer
(ADR-0055).

**What this diff found, before writing a line of new logic:**
`src/main.go` already had a `signInput.SaleType` field AND
`signTransaction` already branched fiskaly's `receipt_type` on it
(`sale.SaleType == "return"` → `"RECEIPT_0104"`, else `"RECEIPT"`) —
pre-existing on `main`. But `fiscalsign.Request` (the wire-parsing type)
had no `sale_type` field at all, and `handleFiscalSignAsk`'s construction
of `signInput{}` never populated `SaleType`. So the branch was permanently
dead: `signInput.SaleType` was always `""`, never `"return"`, no matter
what core sent.

This diff:
1. Adds `SaleType string \`json:"sale_type"\`` to `fiscalsign.Request`, plus
   an `IsReturn() bool` method treating anything other than the literal
   `"return"` (absent, malformed, unrecognized) as an ordinary sale.
2. In `handleFiscalSignAsk`, derives `saleType` from `req.IsReturn()` and
   sets it on the constructed `signInput` — the one line that actually
   closes the gap.
3. Adds unit tests (`src/fiscalsign/fiscalsign_test.go`:
   `TestParseRequest_SaleType`, table-driven over return/sale/absent/
   unrecognized) and one `src/wasmrun` end-to-end test
   (`TestFiscalSignAsk_ReturnSignsWithDistinctReceiptType`) that runs the
   REAL compiled `plugin.wasm` twice against otherwise-identical payloads
   differing only in `sale_type`, asserting the two signed HTTP bodies
   differ on `receipt_type` while every amount stays identical.
4. Updates `README.md`'s status table.

**Explicitly not claimed:** whether `RECEIPT_0104` is fiskaly's real
return-receipt code, or whether fiskaly expects a return's amounts as the
same positive magnitudes a sale carries (distinguished purely by
`receipt_type`) versus negative amounts — both were, and remain, flagged
`NEEDS SANDBOX VERIFICATION` in `src/main.go`'s doc comment. This card's
job was only to make the existing (already-written, already-decided)
branch reachable at all.

## Independent review

Spawned a fresh, worktree-isolated **Opus** subagent (this card is
`complexity:medium`, built at Sonnet) with no access to the implementer's
reasoning, told to read `CLAUDE.md` first, confirm the diff's own
description of the pre-existing dead code by reading the surrounding
code itself (not take it on faith), grep the whole repo for anything else
that should have stayed in sync, and independently re-verify the TDD claim
via revert-then-restore plus a mutation test.

### Findings — no blockers; three taken before merge as a cheap batch (reviewer's own recommendation)

1. **The new README row overstated what was proven** — its original wording
   ("picks the right fiskaly `receipt_type`") could read as "verified
   correct against fiskaly," when only the wiring (does core's signal reach
   the branch) was confirmed; the `RECEIPT_0104` value's correctness, and
   whether fiskaly wants signed amounts rather than positive-magnitude
   ones, are both still open. **Fixed:** reworded the row, and added the
   amount-sign question explicitly to both the README row and
   `src/main.go`'s `NEEDS SANDBOX VERIFICATION` list (it wasn't named
   anywhere before this review — a real, not-yet-flagged gap, distinct from
   the `RECEIPT_0104` enum question that was already flagged).
2. **`fiscalsign.go`'s package/type doc comments still said "contract
   v1.1.0"** after this diff added a 1.6.0 field to the exact struct they
   describe. **Fixed:** both now read "v1.1.0+, currently tracking v1.6.0."
3. **A new code comment in `handleFiscalSignAsk` was garbled and stated the
   opposite of the truth in its first clause** ("req.SaleType was already
   being read into signInput's own SaleType field... for a long time" —
   nothing was reading it; that's the whole bug). **Fixed:** reworded to
   state plainly that `signTransaction` already branched on the field but
   nothing populated it.

### Findings — taken as an easy fourth (not in the reviewer's "cheap batch" but mechanically required)

4. **`manifest.json`'s version was still `0.5.1`** — a merchant-visible
   plugin behavior change with no version bump, and
   `.github/workflows/release.yml` gates a release on `tag ==
   manifest.version` regardless, so this needs bumping before any release
   whether or not it blocks this PR. **Fixed:** bumped to `0.5.2`.

### Findings — deferred as follow-ups (documented, not fixed here)

- The permanent `tse_result:<sale_id>` audit record (`recordResult`) still
  doesn't carry `sale_type`, so a future reconciliation pass can't tell a
  signed return from a signed sale by that record alone. Arguably in scope
  per the card's own wording ("sign/record... as a Rückgabe"), but a
  separate two-line change — filed as a follow-up rather than widening this
  PR.
- `CLAUDE.md`'s `src/fiscalsign/` bullet only enumerated two test-pinned
  invariants; `sale_type → receipt_type` is now a third. **Fixed in this
  same session** (cheap, and the file's own header says to keep it
  current) — not deferred.
- Minor code nits (hoisting `scriptedFiskaly(t)` out of a per-call closure
  in the new wasmrun test; replacing the `"sale"`/`"return"` string
  literals with exported constants to remove a two-place coupling) — left
  as-is, genuinely cosmetic, no behavior risk.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` (host), `GOOS=wasip1 GOARCH=wasm go vet
  ./...` (what CI actually runs), `gofmt -l` on all changed `.go` files:
  clean.
- `go test ./...` (full repo, including the real-wasm `src/wasmrun` suite):
  green, ~18s.
- `bash scripts/build.sh`, `bash scripts/validate.sh`,
  `bash scripts/package.sh`: all pass, package built as
  `com.universaltill.tax-de_0.5.2_universal.tar.gz`.
- **TDD re-verification (independent, not the implementer's own claim):**
  reviewer removed just the `saleType := ...` derivation and the
  `SaleType: saleType` field from `handleFiscalSignAsk` (back to the
  pre-diff dead-code state), rebuilt, and reran
  `TestFiscalSignAsk_ReturnSignsWithDistinctReceiptType` — it failed with
  the expected error (`finish body missing receipt_type RECEIPT_0104 —
  sale_type was not threaded through`). Restored the file; passed again,
  byte-identical to the pre-revert state.
- Reviewer additionally mutation-tested the unit test (renamed the JSON tag
  to `"saletype"`) — `TestParseRequest_SaleType` failed on 3 of 4
  subcases. Neither test is vacuous.
- Grep across the whole repo for `SaleType`/`IsReturn`/`"return"`/
  `receipt_type` found no other consumer that needed keeping in sync.
- No real client/shop name anywhere in the diff (test fixtures reuse
  `configuredHost`'s existing placeholder credentials and synthetic sale
  ids). No literal credential/secret.
- No UI surface changed — this is a WASM backend plugin with no templates,
  so the UX-guidelines/help-manual review steps don't apply.

## Verdict

**Safe to merge.** The wiring is a correct, minimal fix to a genuinely dead
branch — confirmed by an independent review that re-derived the bug from
the surrounding code rather than trusting the diff's own description, and
by a revert-then-restore TDD check with a clear, specific failure message
when the fix is missing. Three documentation-accuracy findings and one
mechanical version-bump were fixed before merge; the remaining findings
are genuine but separable follow-ups, not blockers.

## Deferred / follow-up items (not filed as separate board cards — small, same-repo, noted here for whoever next touches this file)

- Add `sale_type` to the permanent `tse_result:<sale_id>` audit record
  (`recordResult`/`tseSignResult` in `src/main.go`).
- Replace the `"sale"`/`"return"` string-literal coupling between
  `handleFiscalSignAsk` and `signTransaction` with exported constants.
