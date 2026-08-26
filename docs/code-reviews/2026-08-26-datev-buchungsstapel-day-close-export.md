# Code review: DATEV Buchungsstapel — day-close-grained export via `eod_closes`

- **Card:** universaltill/ut-docs#1005 (dependency spine #1003 → #1004 →
  #1005)
- **Repo:** `ut-plugin-tax-de` (companion review:
  `universal-till/docs/code-reviews/2026-08-26-datev-eod-closes-payload-1005.md`
  for the host-side `eod_closes` payload this consumes)
- **Dev:** independent Fable subagent (complexity:hard build tier), two
  rounds (initial build + fix round)
- **Reviewer:** independent Opus subagent, fresh context each round
  (complexity:hard review tier); round 2 scoped to the round-1 fixes,
  earned by round 1's blocker-class findings

## What shipped

`src/datev/closes.go` (new): `BuildFromCloses(closes, settings, now)` — a
day-close-grained EXTF Buchungsstapel, one posting set per archived
Z-report (`ZNumber` as `Belegfeld 1`, the document key), built entirely
from already-archived, immutable close records — never recomputed from
live sales — so the batch reconciles to the merchant's own Z-reports by
construction. Preferred over the pre-existing per-sale-per-tax-line
`Build` whenever the host sends `eod_closes`; `Build` stays as the fallback
for a pre-#1005 host. New settings: `datev_konten_by_method`,
`datev_konto_gutschein`, `datev_konto_trinkgeld`, and (added in the fix
round) `datev_konto_gutschein_zahlung`, `datev_konto_geldtransit`.

## Independent review, round 1 — findings (5 blockers)

- **B1 (BLOCKER) — `manifest.json` dropped `datev_konto_kasse` from
  `settings[]`.** The host's `ReconcilePluginSettings` deletes every
  `plugin_settings` row whose key the manifest no longer declares on
  every install/upgrade — so an install that had it configured would lose
  the value entirely on upgrading to v0.5.0, and `src/main.go`'s comment
  claiming the legacy fallback "keeps working" was factually wrong (a
  Dev self-report claim later proven incorrect, not merely overstated).
- **B2 (BLOCKER, plugin-side half) — see the companion host-side review**
  for the `omitempty`/routing-signal half; the plugin-side fix is routing
  on `payload.EODCloses != nil` (presence), not `len(...) > 0`.
- **B3 (BLOCKER) — voucher/skim account selection by payment-method
  cardinality refused an entire batch for an ordinary shop.**
  `VouchersIssued`/the skim's transit destination carry no
  payment-method dimension on the Z-report, so the original design posted
  to "the single configured method's Konto" and refused outright — the
  *whole multi-day batch*, not just the affected row — the moment a
  normal cash+card pilot shop configured both methods and sold any
  voucher (>1 configured method) or had any skim with more than one
  non-cash method configured. This made the ambiguity rule fire on the
  ordinary case, not the edge case.
- **B4 (BLOCKER) — a header-only file was reachable when a close had zero
  economic activity.** A close range whose only archived days had empty
  cross-tabs, no tips, no vouchers, no skim, still produced an
  apparently-successful 2-line (header-only) file.
- **B5 — see companion host-side review** (the unrelated 50,000-sale cap).

**Fixed:**
- B1: `datev_konto_kasse` re-declared in `manifest.json`'s `settings[]`
  (default `""`), documented as legacy in `src/main.go`/README/CLAUDE.md.
- B3: added `KontoGutscheinZahlung`/`KontoGeldtransit` as dedicated
  settings naming the voucher-proceeds and skim-destination accounts
  directly; removed the `configuredMethods`/`nonCashMethods`
  cardinality machinery and both `len(...) != 1` ambiguity refusals
  entirely. The full 7-row ut-docs#1005 golden reference day now builds
  in **one** batch (`TestBuildFromCloses_GoldenReferenceDay`) instead of
  needing a second test to cover the voucher row separately.
- B4: `BuildFromCloses` counts rendered rows; zero across the whole batch
  refuses with `"datev export: no postings for %s to %s — every close in
  range had zero trading activity"`
  (`TestBuildFromCloses_AllClosesEmpty_Refuses`).

## Independent review, round 2 (scoped to the round-1 fixes) — findings (1 new blocker)

- **BLOCKER — a zero-`Gross` cross-tab cell rendered a `0,00` booking row,
  reopening B4's hole from a different angle.** The cell loop had no
  zero-amount guard (unlike tips/vouchers/skim, all skipped on `== 0`).
  Two realistic causes: split-tender apportionment flooring a small tender
  against a small band to exactly 0 (the host's own `apportionAmount` doc
  comment notes the floor "can shift a minor unit between that sale's
  methods"), and a same-day sale+return netting a cell to exactly 0
  (negative cells are refused; exact-zero was not). Consequences: DATEV
  requires a positive Umsatz per row (likely rejected on import), and a
  close whose *only* cell was zero silently passed the B4 zero-row
  refusal it was meant to trigger.

  **Fixed:** skip a zero-`Gross` cell (`if cell.Gross == 0 { continue }`),
  so it neither renders nor counts toward `renderedRows` — restoring what
  B4's refusal was supposed to guarantee. Added
  `TestBuildFromCloses_ZeroGrossCell_SkippedNotBooked`: proves a mixed
  close (one zero cell + one real cell) renders only the real row, and a
  close whose *only* cell is zero still hits the "no postings" refusal.

Round 2 nits, both addressed:
- `TestBuildFromCloses_ReconcilesToZReportGross`'s identity
  (`sum(Erloes rows) + VouchersIssued == Gross`) only holds on a
  refund-free day (host `Gross` excludes `RefundTotal`, while cross-tab
  cells are sign-flipped for returns) — the test fixture is in fact
  refund-free, so the assertion is correct as written; noted for anyone
  extending it to a returns fixture later.
- No plugin-side `wasmrun` test pins the `payload.EODCloses != nil`
  routing directly (the host half is covered by mutation-tested host
  tests; the plugin's own `main.go` routing line has no dedicated test).
  Left as a follow-up, not blocking — the behavior is exercised
  end-to-end by the host's dispatch tests via the same wire contract.

Round 2 also re-verified via targeted mutation (each reverted): swapping
the voucher/skim Konto between the two new dedicated settings fails
`TestBuildFromCloses_NormalCashCardVoucherDay` and
`..._VoucherRowLabel_NoUniqueMethodMatch` (the fixture deliberately uses
distinct account numbers so this can't pass by accident); flipping the
`renderedRows == 0` check fails `..._AllClosesEmpty_Refuses`.

## Independently re-verified this round (orchestrator, not just the Dev/Reviewer subagents' claims)

- `gofmt -l .` clean, `go build ./...`, full `go test ./...` (including the
  real-wazero `wasmrun` suite) — all green.
- `scripts/build.sh` (`bin/plugin.wasm` built) and `scripts/validate.sh`
  (`ok com.universaltill.tax-de v0.5.0`) both clean.
- Confirmed `uniqueMethodForKonto` (the voucher row's Buchungstext label
  lookup) is genuinely label-only by reading its one call site — the
  booked Konto always comes from `settings.KontoGutscheinZahlung`
  directly, never from the lookup's result.
- Confirmed no dead code/unused imports were left by B3's removal of the
  cardinality machinery (`sort` is still used elsewhere in the file).

## Not changed

- The legacy per-sale `Build` function and its byte-verified EXTF layout
  are untouched — `BuildFromCloses` is a new, parallel code path.
- SKR04 preset support was explicitly declined (an unverified guess) —
  only SKR03 is documented, as a README section, never hardcoded account
  numbers in code.
