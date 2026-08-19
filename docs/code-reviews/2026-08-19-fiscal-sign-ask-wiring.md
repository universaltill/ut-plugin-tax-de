# Code review — wire this plugin to core's real `fiscal.sign.ask` point

**Date:** 2026-08-19
**Card:** universaltill/ut-docs#818
**Branch:** `feat/818-fiscal-sign-ask`
**Complexity:** hard
**Dev:** Opus, inline (local interactive session)
**Reviewer:** Opus, two independent worktree-isolated subagents (a second
round earned by the first finding a money/tax blocker) — fresh instances
that never saw the implementation reasoning. Per "Model routing by
complexity", a hard card reviews at Opus; here author and reviewer are the
same tier, so isolation and a cold context are what make it independent.

## What shipped

The plugin now declares and answers **`fiscal.sign.ask`**, core's real
TSE-signing extension point (ADR-0044 Decision 1, contract
`fiscal-sign-ask.md` v1.1.0). Before this it declared only
`sale.completed`, so `bus.HasSubscribers(fiscal.sign.ask)` was false and
core treated the till as having **zero fiscal signers** — every sale took
the `fiscalSignNoSigner` path no matter how well the fiskaly call itself
worked. That was the entire gap between "this plugin's fiskaly integration
works" (proven 2026-08-18) and "the till's receipts carry real TSE
evidence".

- **`src/fiscalsign/`** (new) — the pure, host-independent half: contract
  wire types, the VAT/payment bucket mapping onto fiskaly's `standard_v1`,
  and the balance invariant. Its own package because `src/main.go` is
  `wasip1`-only and cannot be unit-tested at all, and because ADR-0055
  requires a provider seam.
- **`src/fiskalyparse.ParseSignEvidence`** (new) — full §6 KassenSichV
  evidence with RFC3339 timestamps.
- **`src/wasmrun/`** (new, test-only) — runs the REAL compiled
  `plugin.wasm` in a real wazero runtime with stubbed host functions.
- **`sale.completed` removed** entirely, with its handler, event types and
  private retry queue (see B2).
- Manifest `0.3.2` → `0.4.0`.

## Architectural decision taken first

ADR-0044 Decision 3 had required splitting signing into a separate
`ut-plugin-tax-fiskaly` repo. A prior cloud cycle died on exactly that
(403 creating the repo) and correctly refused to land half the split, since
removing fiskaly from this plugin without the new one existing would leave
the ecosystem with zero signers. The product owner decided fiskaly suffices
and a second provider would be a config switch inside this plugin, so
**ADR-0055 was written and merged before any code here** (ADR-0007 is
binding: supersede in writing, never contradict in code).

## Independent review — findings and disposition

Verdict was **"do not merge as-is"**: five blocking, three
compliance-bearing. Every one is fixed; none was dismissed by asserting the
reviewer wrong.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| B1 | blocking, compliance | **The signed receipt did not balance.** fiskaly renders `standard_v1` into DSFinV-K's `Beleg^<gross per VAT rate>^<per payment type>`, whose halves must be equal — the reviewer confirmed this from fiskaly's own collection, whose "Retrieve Transaction" test asserts the TSE signed `Beleg^21.42_0.00_0.00_0.00_0.00^21.42:Unbar`. A tip rides the payment side in no VAT bucket, and a sale-level discount/service charge moves `total` without moving the per-line `vat_breakdown` (the contract says so itself), so real sales differ by `saleDiscount − serviceCharge − tips`. The branch's own happy-path test asserted `8.33 + 5.35` against `12.90` and called it "the compliance-bearing assertion". | **Fixed — safety half only, deliberately.** How tips and whole-bill discounts *should* be represented is a German tax question, not an engineering one, and is now asked of a real accountant in **ut-docs#833**. What shipped is `fiscalsign.BalanceDelta` plus a refusal: if the halves cannot be reconciled the plugin answers `unreachable` and core declares/retries. **The first version of this fix was itself wrong — see R1 below**; it computed gross as `net + tax` unconditionally, which would have refused every ordinary German sale. A TSE signature is irreversible, so a declared gap beats a permanently false record — the same principle as never fabricating a signature. **Consequence, stated not hidden: tipped and whole-bill-discounted sales do not sign today.** |
| B2 | blocking, data integrity | **Keeping `sale.completed` corrupted every *successful* sale.** Core fires the ask at tender and `sale.completed` after; `txID` is deterministic per sale, so the second hook re-`PUT`s an already-`FINISHED` transaction. Proven with a wazero test modelling fiskaly's immutability: per sale, a false "fiskaly was NOT reached" operator log, `tse_result:<sale_id>` overwritten from signed to failed, and one permanent queue entry — queue grows to `maxQueue` 200, then costs 200 failing round-trips on every subsequent sale (observed climbing 1, 2, 3…). Directly falsified the commit's own CLAUDE.md claim that keeping it was "deliberate". | **Fixed by removal**, the reviewer's recommended clean answer and what ADR-0055's own closing line left open. `sale.completed`, `handleSaleCompleted`, the sale-event types and `unsigned_queue` are all gone. Core owns the failure surface properly now (journal marker, receipt notice, operator alert, background re-ask with `retry: true`), so a private retry loop was redundant as well as harmful. |
| B3 | blocking | **`tse.log_time` sent as a raw Unix epoch** against a contract declaring RFC3339. Core prints it verbatim (`print_api.go`: `"TSE transaction end: "+sig.LogTime`), so a real German receipt and its QR payload would have carried `TSE transaction end: 1755253862`. The one test that could have caught it only asserted non-emptiness. | **Fixed** — `timestampRFC3339` at the parse boundary, with an explicit format assertion in both the unit and wasm suites. `ParseSignResponse` is left untouched so `tse_result:*` records already written stay comparable. |
| B4 | blocking, honesty | **`src/main.go`'s package doc and README:217 still asserted the plugin was NOT the real signer**, contradicting the same commit's CLAUDE.md, README status table and manifest. A repeat of this repo's own 2026-08-18 review finding #1. | **Fixed, but only on the second attempt — the first fix was itself incomplete and this record wrongly claimed otherwise.** The first pass added correct new text above the old contradicting text and left the old text in place, so `src/main.go`'s package doc asserted both positions at once, `README.md`'s hook line still said `Hooks sale.completed`, and README's Known gap #1 still described the removed architecture as current. Caught by the second-round review, which correctly noted the record was asserting a fix that had not happened. Now genuinely done in all three places. |
| B5 | blocking | **No recovery from an already-started transaction.** If core's 3000 ms budget expires mid-finish, fiskaly may still complete the transaction; the retry then re-`PUT`s `tx_revision=1`, gets 400, and answers `unreachable` forever. `Request.Retry` was parsed and tested but never read. | **Root cause removed with B2** (the double-signing path is gone). The residual budget-expiry case is **not fully closed** and is recorded below under Deferred rather than claimed as fixed. |
| N3 | non-blocking, honesty | **The stated reason for omitting five §6 evidence fields was factually wrong.** It claimed their paths were "documented on the TSS and client resources, not the transaction finish response". They are on the transaction: `number`, `tss_serial_number`, `time_start`, `signature.counter`, `signature.algorithm` — and all of them were already present in this repo's own committed real-sandbox fixture. | **Fixed by populating them**, not by rewording. New `ParseSignEvidence` reads every path from that captured real response. `serial_number` comes from `tss_serial_number` on the transaction, so nothing is guessed. |
| N1, N2, N4 | non-blocking | Sale-completed request bodies not byte-identical after refactor (key/array order only — every amount verified identical, and the new ordering is deterministic where the old was not); doc comments detached from their functions, including the load-bearing "CONFIRMED 2026-08-18" caveats; stale symbol names in README. | N1 **accepted as correct** (moot now that `sale.completed` is gone). N2 **fixed**. N4 **initially only partly fixed** — symbol names were corrected but three other stale references were missed; completed after the second round. |

## Second review round — earned, and it paid for itself

The first round found a money/tax blocker (B1), which per this pipeline's
own rule earns a second round scoped to the fix. It found three more real
problems, one of which would have been worse in production than the bug it
replaced.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| R1 | **blocking, compliance** | **B1's fix would have refused to sign EVERY ordinary German sale.** `BalanceDelta` and `VATAmounts` both computed gross as `net + tax`, which is correct only for tax-**exclusive** pricing. Germany prices tax-**inclusive**, where core puts the gross in `vat_breakdown[].net` and the *contained* tax in `.tax` (`buildFiscalSignPayload` + `pos.ComputeTaxBasisPoints`), so `net + tax` double-counts the VAT. The reviewer reproduced core's arithmetic: a plain €11.90 inclusive sale gives `delta = 190` (refused), and had the balance check not fired it would have **signed 13.80 gross for an 11.90 sale** — over-declaring turnover on an irreversible record. The first record even enshrined `net + tax` as a "pinned invariant". | **Fixed** by inferring the convention instead of assuming one: `total` is authoritative, so whichever reading reconciles with it is correct (zero-rated lines satisfy both and give the same gross). Found independently by the author while the review ran, and confirmed by it. Root cause filed upstream: **ut-docs#834** (the payload has no `tax_inclusive` flag). Mutation-verified. |
| R2 | blocking | **`unreachable` is the wrong status for a permanently-unsignable sale**, and starves core's retry queue: it is the *abort-the-tick* status, so one such sale blocks every genuinely-signable sale behind it during a real outage, is retried every 2 minutes forever, and prints an *outage* notice where there was no outage. | **Not fixed here — no correct status exists in contract v1.1.0** (`not-this-terminal` leaves no marker, which is worse). Filed as **ut-docs#835**: add a per-entry `cannot-sign` status. Recorded as a real production risk rather than summarised as benign. |
| R3 | blocking | **B4/N4 were only half done** (see the table above). | **Fixed.** |
| R4 | non-blocking | **The wasmrun cache-buster only covered `src/*.go`, not the subpackages** — so a mutation to `fiscalsign` or `fiskalyparse`, where the load-bearing logic lives, was still cached as a PASS. The same hazard the first round had already fixed once, half-covered. | **Fixed** (recursive walk). Re-verified: mutating `BalanceDelta` in a subpackage now fails `wasmrun` without `-count=1`, where before it reported `(cached) ok`. |
| R5 | non-blocking | `serial_number` = `tss_serial_number` is correct for §6 (it is the security-module serial), but fiskaly's own `qr_code_data` uses `client_serial_number` in field 2 — relevant if the QR payload is ever built from this evidence. | Noted; no change. Recorded for whoever builds the QR. |

It also verified as sound what the first round's fixes claimed: the
`sale.completed` removal leaves nothing dangling (checked by symbol across
code, manifest and scripts, with `go doc -all` confirming comments are
attached to the right symbols this time), B3's RFC3339 conversion handles
all branches without turning an absent timestamp into `1970-01-01`,
`ParseSignResponse` is untouched, and N3's field paths all match the real
captured fixture.

## What was verified, beyond "tests pass"

- **Mutation testing, by both author and reviewer.** Independently
  confirmed failing: dropping `tax` from the VAT gross, dropping the tip
  from payment totals, emitting evidence with no signature, deleting the
  `fiscal.sign.ask` dispatch, and answering `approved` on a failed sign.
- **A false-pass in the new suite, caught and fixed.** The first version of
  `src/wasmrun` cached a PASS across a mutation that deleted the hook
  entirely — the wasm is built by a subprocess, so the go command's result
  cache never saw `main.go` as an input. Fixed by reading the guest sources
  in the test; re-running the same mutation then failed four tests. The
  reviewer independently reproduced both halves of this.
- Full gate: `go test ./...`, `go vet ./src/...`, `gofmt -l src/`,
  `scripts/build.sh`, `scripts/validate.sh`, `scripts/package.sh` — all
  clean. `go.mod` stays on `go 1.23` matching CI; wazero pinned to v1.9.0
  because v1.12 forces the directive to 1.25.
- **Not verified:** live fiskaly. The HTTP layer in the wasm suite is a
  stub; the live contract was proven separately on 2026-08-18. The two
  halves are each verified, their combination is not.

## Deferred, with cards

- **ut-docs#833** — accountant ruling on tips and whole-bill discounts.
  Until it lands those sales do not sign. Biggest functional gap.
- **Budget-expiry recovery (B5 residual)** — on `start_failed`, `GET` the
  transaction and adopt an existing `FINISHED` signature. Note the
  constraint for whoever builds it: recovering a real signature from a real
  2xx is fine, but inferring "400 means it was already signed" would
  violate the never-fabricate rule.
- **Remaining VAT buckets** — only `NORMAL` has been round-tripped through
  live fiskaly; `REDUCED_1`/`NULL`/`SPECIAL_RATE_1` are unit-tested for
  mapping only.
- Reviewer's N5 note that two host-stub behaviours differ from the real
  runtime (response caching across the buffer retry; JSON-string setting
  unwrapping). Neither affects current results.

## Known-wrong-and-accepted, stated plainly

- **A cash overpayment where the cashier does not record change** arrives as
  a payment `amount` larger than `total` (`ChangeGiven` is client-supplied),
  so the sale will be refused. Arguably correct — the payload as sent really
  does not reconcile — but it is a legitimate sale refused on a data-quality
  problem, not a fiscal one. Worth watching in the pilot.
- **ut-docs#835's queue-starvation risk is live** the moment a German shop
  takes its first tipped sale.

## Safe-to-merge verdict

Safe to merge. The change is a strict improvement on today's state, where
core sees no signer at all and nothing signs. It is honest about the two
classes of sale that still will not sign and why, and it fails closed in
both directions — never a fabricated signature, and never a signature on a
receipt already known to misstate the sale.
