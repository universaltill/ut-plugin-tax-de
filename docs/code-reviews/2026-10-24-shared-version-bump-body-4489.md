# Review: shared check-version-bump body (ut-docs#4489)

**Date:** 2026-10-24. Built by Sonnet, reviewed by Opus 5.5.

## What changed

- `scripts/check-version-bump.sh` is now a thin wrapper. It keeps this repo's
  `SHIPPED_PATTERNS` (unchanged) and sources `scripts/check-version-bump.body.sh`.
- That body is byte-identical in every ut-plugin-* repo. Its canonical copy is
  ut-docs `scripts/templates/check-version-bump.body.sh`, and it fails CI if
  edited here.
- New behaviour from the shared body:
  - `--no-renames`, so a rename out of a shipped path counts.
  - A local run compares against `origin/main`'s tip instead of the merge-base.
- The test now copies the body into each fixture and has one new case: a
  hand-edited body fails.

## Verification

- `scripts/check-version-bump.test.sh` passes.
- shellcheck is clean.
- A local run of the guard prints `ok`.
- No shipped file changed, so no version bump is needed.

Full review record: ut-docs `code-reviews/2026-10-24-shared-version-bump-body-4489.md`.
Findings: none blocking. Verdict: safe to merge.
