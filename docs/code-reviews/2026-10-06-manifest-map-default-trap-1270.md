# Review — normalize string-typed JSON-object manifest defaults (ut-docs#1270)

Date: 2026-10-06 · Lane: lane:cloud-41

## Change

The double-encoding trap ut-docs#1255 fixed for `takeaway_rate_overrides`
(a manifest `default_value` declared as the JSON *string* `"{}"` instead of
a real JSON *object* `{}`, which core's install-time
`json.Marshal(s.DefaultValue)` then double-encodes, permanently breaking
any reader expecting a real map) had three more armed-but-dormant
instances in this repo's `manifest.json`: `datev_konten_by_method`,
`datev_erloeskonten`, `datev_bu_schluessel`. None was exercised in
production yet, unlike `takeaway_rate_overrides`.

- Fixed all three `default_value`s to real JSON objects (`{}`), same shape
  as the already-merged `takeaway_rate_overrides` precedent a few lines
  above them in the same file.
- Added a general check to `scripts/validate.sh`: fails on any
  `settings[]` entry whose `default_value` is a JSON string that itself
  parses to a `dict`/`list` — catches this whole trap class at
  manifest-author time, not just these three keys.
- Bumped `manifest.json`'s version 0.10.2→0.10.3 (required by
  `check-version-bump.sh`, which gates on any `manifest.json` change).
- `CLAUDE.md` updated to document the new `validate.sh` check.

The card's stated dependency, ut-docs#1269 (unify plugin-setting JSON
encoding), was already merged (universal-till#998, 2026-09-09) before this
cycle picked the card up — no open blocker.

## Dev

Opus 5.5. TDD-first: added the `validate.sh` check before touching
`manifest.json`, confirmed it failed naming exactly the three affected
settings, then fixed the manifest and confirmed it passed. Full local CI
step list green: `go vet` (wasip1/wasm cross-compile), `gofmt -l .`,
`go test ./...` (8 packages), `scripts/build.sh`, `scripts/validate.sh`,
`scripts/guard-plugin-i18n.sh`, `scripts/package.sh`,
`scripts/check-version-bump.test.sh`.

## Independent verification (orchestrator, before review)

Re-ran every CI step myself and reproduced the same results. Personally
re-verified the TDD claim: reverted all three defaults back to the string
shape (check left in place), confirmed `validate.sh` failed naming exactly
those three settings; restored and confirmed it passed again.

## Review

Independent Fable review (different model from the Opus author), in an
isolated worktree off this branch's pre-review commit. **Verdict: safe to
merge as-is, no required fixes.**

- Re-verified the TDD claim again independently (same revert/restore
  method) — reproduced the exact red (`FAIL: setting
  datev_konten_by_method's default_value is a JSON-string-wrapped
  object/list...` naming all three) and green (`ok com.universaltill.tax-de
  v0.10.3`) output.
- Walked every other setting against the new heuristic (`""` ×11, `"zip"`,
  `"proportional"`, `"4"`, `"0101"`) and confirmed zero false positives —
  each either raises on `json.loads` or parses to a non-dict/list.
- Confirmed consumer-side correctness by reading `src/main.go`'s
  `datevSettings` (~line 905-923): a double-encoded default arrives as the
  4-byte string `"{}"` (with quotes), is not skipped by the `""`/`"{}"`
  guard, and fails `json.Unmarshal` → DATEV export refuses. A real `{}`
  default is stored and read correctly. The fix's failure mode is real.
- Confirmed the version bump is correct and unreleased
  (`git fetch --tags`: only `v0.10.0`–`v0.10.2` exist; no `v0.10.3` yet)
  and ran the real `check-version-bump.sh` (not just its self-tests)
  against this commit: `ok: manifest.json version bumped 0.10.2 -> 0.10.3`.
- Confirmed no file-write-without-`os.MkdirAll` or cwd-relative-path
  concerns apply (pure manifest/script diff; `validate.sh` already `cd`s
  to the repo root first).
- No secrets, no real client/shop names.
- Flagged one operational question (whether an already-installed till with
  the double-encoded value stored needs a migration on upgrade) — answered
  by the sibling tax-uk review's independent check of `universal-till`
  core: **no**, `ReconcilePluginSettings` preserves an existing stored
  value across a plugin version bump; this fix only prevents the
  double-encoding on a *fresh* install (this repo's three settings have
  never been exercised, so there is no existing bad value to migrate
  either way).
- Optional, applied: rewrote the WIP commit message to a real one;
  documented the new check in `CLAUDE.md`.

## Verdict

Safe to merge. No ADR implicated (established pattern per #1255's
precedent). No UI surface, no new user-facing i18n string, no money type
involved.
