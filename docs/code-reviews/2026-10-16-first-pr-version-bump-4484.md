# Review — version-bump guard: a new repo's first PR (ut-docs#4484)

**Date:** 2026-10-16 · **Lane:** lane:cloud-24 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)

## What shipped

`scripts/check-version-bump.sh` gets the first-release block that is now
byte-identical in all 9 `ut-plugin-*` copies of the guard:

- manifest.json absent at the merge-base and present on main's tip → FAIL
  (a stale branch forked before the first manifest; rebase and bump);
- absent at the merge-base and at the head → pass (nothing releases yet);
- absent at the merge-base, present at the head → first release: the
  same-version comparison is skipped, the already-tagged check still runs;
- present at the merge-base but unparseable → still fails closed.

This repo's copy had no first-PR path: a new repo's first PR failed closed with `could not read manifest.json version at merge-base`. New cases: first PR passes, first version already tagged fails, unparseable base still fails, stale branch fails, no manifest at base or head passes.

No shipped file changed, so no manifest version bump.

## Verification

- `bash scripts/check-version-bump.test.sh` passes; `shellcheck` on both scripts: 0 issues.
- TDD: the new cases were run against the old script first and failed (red), then pass on the new one.
- The reviewer mutation-tested the block in scratch copies (drop the stale check, drop the head-absent check, early exit, the pre-fix parse error, drop the tag check): each mutation is caught by at least one case.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | eposnow case 22 asserted only `FAIL` and tagged v0.1.0, so the already-tagged path masked a deleted stale-branch check | Fixed: asserts `forked before manifest.json existed` (the mutation is now caught) |
| 2 | nit | the stale-branch case comment said main's manifest was "tagged"; the fixture deliberately isn't | Fixed: comment reworded |
| 3 | nit | the `first_release -eq 0 &&` gate is redundant (the sentinel can't equal a real version) | Accepted: explicit belt-and-braces |
| 4 | nit (pre-existing) | locally, BASE_SHA falls back to the merge-base, so the stale check is vacuous outside CI | Accepted: CI passes the real base sha; noted on ut-docs#4489 |
| 5 | info (pre-existing) | two concurrent first-release PRs both pass until one merges | Accepted: same race as any two version bumps |

## Verdict

Safe to merge. Deferred: one canonical copy + drift check — ut-docs#4489.
