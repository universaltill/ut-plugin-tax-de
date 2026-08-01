# Code review: wire export.requested.ask, answer with a real response

**Date:** 2026-08-01
**Scope:** `src/main.go`, `manifest.json`, `README.md`.
**Trigger:** companion to `universal-till`'s export/report plugin dispatcher
(ut-docs#189). Full review record and independent-review detail:
`universal-till/docs/code-reviews/2026-08-01-export-report-plugin-dispatcher.md`.

## What shipped

- Renamed the placeholder event this plugin listened for
  (`tax.de.dsfinvk.export.requested`, which nothing in the host ever
  published) to the generic `export.requested.ask` the host now
  dispatches.
- Declared `export.requested.ask` in `manifest.json`'s `hooks[]` — it was
  **not declared at all before**, so `EventBus.subscribe` (which requires
  an active declared hook) would have rejected this plugin's subscription
  even once the host-side dispatcher existed. This alone was a real,
  independently-confirmed gap, not a hypothetical.
- Changed the handler to write a real `{"ok":bool,"message"/"error":...}`
  JSON response to stdout (the answer channel for `.ask`-suffixed events,
  same convention `handleTaxRateAsk` already uses) instead of silently
  `os.Exit(0)`-ing with nothing on stdout.
- Version bumped to `0.2.0`; README's "Known gaps" / "What a human still
  needs to do" sections updated to describe the trigger-only reality
  (fiskaly's DSFinV-K export is async, up to ~1h — this plugin has no file
  bytes to return inline).

## Independent review finding (fixed)

The independent review of the paired `universal-till` change found this
plugin answered `export.requested.ask` **unconditionally**, ignoring
`entry_key` — which, combined with the host originally broadcasting to
any subscriber (also fixed, see the main review record), meant a till
running this plugin alongside any other export plugin could get a
DSFinV-K trigger when the merchant asked for a different export entirely.

**Fix:** the host-side fix (`EventBus.AskPlugin`, routes to the specific
plugin owning the resolved entry) is what actually closes this. This
plugin additionally now declines (doesn't answer) when a non-empty
`entry_key` doesn't match its own `dsfinvk-export-de` entry — defense in
depth for a future second export entry in this same plugin, not the
primary fix.

## Verification

- `bash scripts/build.sh` (`GOOS=wasip1 GOARCH=wasm go build`): clean,
  builds `bin/plugin.wasm`.
- `bash scripts/validate.sh` (manifest schema/taxonomy validation): green
  — `ok com.universaltill.tax-de v0.2.0`.
- Cross-repo contract checked field-for-field against `universal-till`'s
  `exportResponse` struct (`ok`/`filename`/`content_b64`/`message`/`error`,
  all snake_case): no drift. This plugin never emits `filename`/
  `content_b64`, consistent with its trigger-only nature.
- Exercised end-to-end through `universal-till`'s real-wazero regression
  test infrastructure pattern (a generic test guest, not this plugin's own
  compiled module — this plugin itself still has no automated tests, a
  pre-existing gap noted in its own README's status table, not introduced
  or resolved by this change).

## Verdict

**Safe to merge.** No fiskaly-account-dependent behavior changed; this is
event-plumbing and response-shape only, matching the paired
`universal-till` change's expectations exactly.
