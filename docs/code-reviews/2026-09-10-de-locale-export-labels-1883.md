# Code review: ship German export-entry labels via the plugin locale overlay (ut-docs#1883)

**Branch:** `feat/1883-de-locale-export-labels`
**Author:** Farshid Mirza (pipeline, `lane:cloud-24`)
**Reviewer:** independent Opus subagent (different model from the Sonnet
build pass, per the pipeline's review-model routing for `complexity:medium`)

## What changed

The two `export`-type manifest entries' `label` fields changed from literal
English text to locale keys (`tax_de.entry_dsfinvk_export_label`,
`tax_de.entry_datev_export_label`), resolved through core's
`plugins.Manager.syncLocales()` overlay mechanism (ADR-0010,
`architecture/plugin-architecture.md` §7) once `universal-till`'s matching
`settings.html` fix (ut-docs#1883's sibling change, same card) lands.
Ships `locales/en.json` (base, identical to the pre-change English text)
and `locales/de.json` (German — the live-pilot market). The `tse-sign-de`
(`tax`-type) entry's label is deliberately left as a plain literal: no
render path in `universal-till` calls `T` on a `tax`-type entry's label
today (verified by the independent reviewer: no `ListTaxEntries` or
equivalent exists at all), so keying it would have been unverifiable,
speculative scope. Version bumped 0.5.3 → 0.5.4 for the auto-tag release
pipeline (`.github/workflows/auto-tag-release.yml`, which tags and releases
automatically the instant this version reaches `main` — no human gate).

## Independent review — one real blocker found and fixed before merge

**F1 (BLOCKER, found and fixed pre-merge):** the initial version of this
change did **not** update `scripts/package.sh`, whose `entries=(...)` array
never included `locales/`. The reviewer verified empirically — ran
`scripts/package.sh` and inspected the resulting `.tar.gz` — that
`locales/en.json`/`locales/de.json` were silently absent from the release
artifact. Core's `plugins.Manager.syncLocales()` reads `locales/*.json` from
the *installed bundle* (`paths.Plugins()/<id>/<version>/locales`), which
comes from exactly what `package.sh` puts in the archive, not from this repo
checkout. Concrete failure scenario the reviewer traced end-to-end: a
merchant installs 0.5.4 from the marketplace → no `locales/` in their
install → `syncLocales()` finds nothing to overlay → the bare key
(`tax_de.entry_dsfinvk_export_label`) renders verbatim in Settings → Data →
Export, on a live-pilot fiscal-compliance plugin, published automatically
by `auto-tag-release.yml` with no human in the loop. **This would have been
strictly worse than before this change.**

Fixed before merge: `scripts/package.sh` now includes
`[ -d locales ] && entries+=(locales)`. Re-ran `scripts/package.sh` and
confirmed via `tar -tzf` that `locales/en.json`/`locales/de.json` are now
present in the archive.

**F2 (high, addressed via a new CI check rather than a core fallback):**
the reviewer noted this change adds no fallback-to-English degrade for an
unresolvable key-shaped label, unlike ADR-0088 Decision G's explicit
requirement for layout-slot labels ("Fallback is the core default, not the
raw key"). Rather than a bigger core change this cycle, extended
`scripts/guard-plugin-i18n.sh` (synced from the canonical
`ut-docs/scripts/templates/guard-plugin-i18n.sh`, updated in the same
change) with a new check: any key-shaped `entries[].label` in
`manifest.json` must resolve in `locales/en.json` — this check runs even
with **no** `locales/` directory at all, which is exactly the shape of the
F1 bug, so a future regression of this kind fails CI immediately. Verified
by deliberately removing `locales/` locally and re-running the guard: it now
fails with a clear message naming both offending entries; restored, and it
passes again.

**F3/F4 (medium, documented, not fixed):** the reviewer found the same
raw-`.Label` gap also applies to plugin `theme`- and `button`-type entries
elsewhere in `universal-till` (unrelated to this repo's own entries, which
are only `tax`/`export`). Not this repo's problem to fix, but recorded in
this README's "Known gaps" list and in `architecture/plugin-architecture.md`
§7 (ut-docs) so it isn't lost.

**F5 (doc, mandatory per CLAUDE.md):** this repo's own `guard-plugin-i18n.sh`
header comment (copied verbatim from the canonical template) previously
stated flatly that "manifest labels aren't localized at all today" — false
in this very repo as of this change. Corrected in the same template update
that added the F2 check, propagated to this repo's copy.

**F6:** N/A to this repo (test-cleanup nit was in `universal-till`'s test
file only).

## Verification

- `go test ./...` — all 6 packages green (`src/auditkey`, `src/datev`,
  `src/fiscalsign`, `src/fiskalyparse`, `src/taxrate`, `src/wasmrun` — the
  last actually compiles+runs the real wasm module).
- `gofmt -l .` — clean.
- `bash scripts/build.sh` — builds `bin/plugin.wasm`.
- `bash scripts/validate.sh` — `ok com.universaltill.tax-de v0.5.4`.
- `bash scripts/guard-plugin-i18n.sh` — `2 manifest entries[].label key(s)
  resolve in locales/en.json`; `ok (2 key(s) across 2 locale file(s))`.
- `bash scripts/package.sh` + `tar -tzf` — confirmed `locales/en.json` and
  `locales/de.json` are present in the packaged archive (the F1 fix,
  verified directly, not just by re-reading the script).
- German translations independently checked by the reviewer as correct,
  idiomatic German (proper Durchkopplung hyphenation for the compound
  proper nouns), not machine-garbled.

## Not in scope

Payment-method display names, plugin settings-field labels, and plugin
`theme`/`button` entry labels — see `README.md`'s "Known gaps this does NOT
close" section and `ut-docs#1883`'s tracking comment for the full reasoning.
