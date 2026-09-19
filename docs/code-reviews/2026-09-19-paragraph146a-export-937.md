# Code review: §146a Abs. 4 AO notification export (ut-docs#937)

**Date:** 2026-09-19
**Card:** ut-docs#937 — plugin side of the export half of #665's core register
**Companion PR:** `universaltill/universal-till` (core payload gating), full
review record: `universal-till/docs/code-reviews/2026-09-19-paragraph146a-export-937.md`

## What shipped here

A third `export`-type manifest entry, `paragraph146a-de` (v0.6.0), and a new
host-independent package `src/paragraph146a` that renders a human-readable,
German-labelled text summary of the §146a Abs. 4 AO till/TSE register the
host now sends it — grouped by business location (gross method), mirroring
the pilot's incumbent vendor's own field list/order/labels. No ELSTER XML,
no filing on the shop's behalf.

## Independent review (Opus, worktree-isolated, complexity:medium)

Full findings and disposition are in the companion PR's review record
(the review covered both repos' diffs in one pass, plus the `ut-docs`
contract-doc update). What changed on **this repo's side** as a result:

- **B1 (blocker, core-side fix, this plugin's manifest is why it triggered)**:
  `ut-plugin-tax-de` declares `sales:read` (for TSE signing) in addition to
  `fiscal_register_de:read`, so the host's per-sale gather/cap ran
  unconditionally for `paragraph146a-de` before the core-side fix — this
  plugin's own manifest is exactly why the bug was live, even though the
  fix itself lives on the host side.
- **S2**: `handleParagraph146aExport` (`src/main.go`) now distinguishes a
  `nil` payload (not declared/granted — a config problem) from a genuinely
  empty register (`[]`, add tills), with a distinct error for each, instead
  of collapsing both into "add tills".
- **S3**: `paragraph146a.Build`'s "Gesamtanzahl der genutzten eAs" count now
  excludes an uncommissioned till, not just a decommissioned one — both
  still appear individually listed with their own placeholder.
- **S5**: `paragraph146a.Build` now warns when more than one register with
  no assigned location merges into the single "(keiner Betriebsstätte
  zugeordnet)" block, since that merge can silently understate the real
  Betriebsstätte count.
- Nits: the package's error now carries the `paragraph146a export: ` prefix
  `src/datev` uses; `manifest.json`'s description mentions this entry.

New/updated tests in `src/paragraph146a/paragraph146a_test.go`:
`TestBuild_UncommissionedEntryStillListedNotCountedInUse`,
`TestBuild_MultipleNoLocationEntriesGetSplitWarning`,
`TestBuild_SingleNoLocationEntryGetsNoSplitWarning`; existing fixtures
(`TestBuild_GrossMethodOneBlockPerLocation`,
`TestBuild_DecommissionedEntryStillListedNotCountedInUse`) updated to set
`CommissionedOn` where the S3 fix now makes it load-bearing.

**Confirmed correct as originally written**: no TSE-PIN/PUK anywhere (no
such field on `Row` at all — structural, not a redaction); `Row`'s JSON
tags match core's `data.FiscalRegisterDE` field-for-field (manually
cross-checked by the reviewer; the only difference is the two timestamp
fields this plugin doesn't need).

**Accepted gap, not newly introduced**: no `src/wasmrun` coverage exists
for any of the three export entries' dispatch (DSFinV-K, DATEV, or this
one) — `main.go` is wasip1-only and untestable directly outside that
harness, and none of the three has ever had it.

## Verified beyond automated tests

- `go test ./...` (incl. `src/wasmrun`, real compiled `plugin.wasm` through
  a real wazero runtime) — green.
- `gofmt -l .`, `go vet ./...`, `GOOS=wasip1 GOARCH=wasm go vet ./...` —
  clean.
- `scripts/build.sh`, `scripts/validate.sh` (v0.6.0),
  `scripts/guard-plugin-i18n.sh`, `scripts/package.sh` — all green.

## Safe-to-merge verdict

Yes.
