# Code review: auto-tag-release.yml (release-on-merge automation)

**Date:** 2026-09-07
**Card:** ut-docs#1694
**Author:** scrum-master pipeline (cloud cycle, `lane:cloud-54`), on behalf of Farshid Mirza

## What changed

Added `.github/workflows/auto-tag-release.yml`. On every push to `main` it
reads `manifest.json`'s `version`, and if no `v<version>` tag exists yet, it
creates and pushes one using the workflow's own `GITHUB_TOKEN`, then
`workflow_dispatch`es the existing `release.yml` on that tag. `release.yml`
itself is unchanged — this workflow only gets the tag created and the
release started; the actual build/package/publish is exactly what it
always was.

## Why this exists

Three recorded incidents (ut-docs#1139, #1241, and a third found 2026-09-07
while cutting this same repo's overdue release) of a plugin repo's `main`
moving ahead of its last release tag with nothing noticing — the marketplace
keeps serving a stale build. The structural cause: the lane that merges a
plugin PR is almost always the cloud pipeline, and the cloud pipeline's own
credentials get HTTP 403 on tag-ref pushes, so the manual "cut the release
now" rule (added after #1241) could never actually be executed by the lane
told to follow it. `GITHUB_TOKEN`, scoped to a single workflow run in this
repo, is a different actor and not subject to that 403 (or, if the 403 turns
out to be a repository ruleset rather than a credential restriction, this is
the first real test of whether GitHub Actions is on that ruleset's bypass
list — see "Known risk" below).

## Live drift this repo was actually in — the real end-to-end proof

At the time this PR was opened, `manifest.json`'s `version` was `0.5.3` but
the latest tag on `main` was only `v0.5.1` (`v0.5.2` was never tagged
either) — this repo was live in exactly the state ut-docs#1694 describes.
Merging this workflow is expected to create tag `v0.5.3` and start a real
`release.yml` run on the very first push to `main` that carries it. DevOps
must verify this actually happened (new tag exists, `release.yml` run
succeeded, marketplace serves `0.5.3`) rather than trust the YAML.

## A design gap Dev caught (not in the original card text)

GitHub does not start a new workflow run from an event created with a
workflow's own `GITHUB_TOKEN` — including a tag push — with
`workflow_dispatch`/`repository_dispatch` as the documented exceptions
(recursion guard). The card's original sketch assumed the tag push alone
would trigger `release.yml`'s `on: push: tags: ["v*"]`; it would not have.
The fix: an explicit `gh workflow run release.yml --ref "$TAG" -f
channel=stable -f publish=true` step after the tag lands, plus `actions:
write` permission. Independent review traced `release.yml`'s `if:`
conditions against what `github.ref`/`github.event_name`/`inputs.*` resolve
to for a dispatch targeted at a tag ref and confirmed the code path is
equivalent to a native tag push (see "Independent review" below).

## Verification performed

- YAML: `python3 -c "import yaml; yaml.safe_load(...)"` — parses, no tabs,
  correct `run:` block indentation/quoting.
- `bash -n` on both extracted `run:` script bodies — clean.
- **Behavioral test against a real throwaway git repo** (bare `origin` +
  working clone, not mocked): reproduced the exact tag-detection script
  verbatim and ran it through, with `$GITHUB_OUTPUT` set to a real temp file
  matching the real Actions runtime:
  - version bump with no existing tag → tag created and pushed, `created=true`
  - rerun on the same commit → "already exists", `created=false`, exit 0
  - unrelated commit with no version change → no new tag
  - race: a concurrent clone pushes the same tag first → local push
    rejected, `ls-remote` confirms it exists, `created=false`, no
    double-dispatch
  - **negative control**: a deliberately broken copy of the script with the
    idempotency guard removed was run against an already-tagged version —
    it failed (`git tag -a` on an existing tag errors), confirming the test
    scenarios actually discriminate correct from broken logic rather than
    passing regardless
  - new version-format guard: a manifest with `"version": "not-a-version"`
    is rejected before any git mutation, confirmed with `$GITHUB_OUTPUT`
    correctly left unwritten
- Confirmed `manifest.json` is at repo root (matches the script's
  assumption) and this repo's `release.yml` declares the `channel`/`publish`
  `workflow_dispatch` inputs the new workflow passes.
- Could **not** locally exercise the `gh workflow run` dispatch step itself
  (needs the real GitHub API) — this is a real, accepted gap closed by
  DevOps observing the actual merge, not by further local testing.

## Independent review (Opus, different model from Dev/Fable)

Verdict: **SAFE TO MERGE**, no blocking findings. Full reasoning traced
step-by-step: the recursion-guard premise is correct and documented;
dispatching `release.yml` via `workflow_dispatch` on the tag ref makes
`github.ref`/`GITHUB_REF_NAME` resolve identically to a native tag push,
and every `if:` condition in `release.yml` that branches on `event_name`
has an `|| inputs.publish` fallback that a dispatch satisfies; permissions
(`contents: write` + `actions: write`) are minimal and sufficient;
idempotency and the concurrent-push race are both handled correctly
(verified independently above); `on: push: branches: [main]` only, not
`pull_request_target`, so no fork-PR exposure on this public repo;
`github-actions[bot]` tag-author identity is correct and doesn't interact
with `commit-attribution.yml` (which only inspects commit authors, not tag
authors); all three plugin repos' copies are byte-identical and make no
repo-specific assumption beyond `manifest.json` living at repo root.

**Findings triaged:**
- Fixed (all three repos): tag pushed via explicit refspec
  (`refs/tags/x:refs/tags/x`, not a bare tag name — avoids ambiguity with a
  same-named branch); a silent-failure branch hardened — if a tag push
  fails and the tag isn't found on origin either (genuine failure, not a
  known concurrent-run race), the step now fails loudly rather than
  potentially reporting green with no tag actually landed; a version-format
  guard added before the value is interpolated into `$GITHUB_OUTPUT`
  (defense in depth — `scripts/validate.sh` already enforces this on every
  PR via `ci.yml`, so this closes the gap only for a push directly to
  `main` bypassing that PR gate); `GH_REPO` pinned explicitly on the
  dispatch step rather than relying on git-remote auto-detection.
- Accepted, not fixed (out of scope for this PR): adding a `concurrency:`
  group to `release.yml` to fully close a narrow double-dispatch window
  (touches a file this PR deliberately leaves alone); gating the tag on
  `ci.yml` having passed on that commit first (currently `release.yml`
  itself re-runs `validate.sh`/`build.sh` before publishing, so a broken
  tree produces a red release rather than a bad publish — a real
  hardening, but not a fix for a gap this card exists to close).
- **Known risk, not blocking:** this repo's own prior incident record
  (`docs/code-reviews/2026-08-28-...1241.md` referenced from
  `ut-docs/.claude/skills/devops/SKILL.md`) describes the original 403 as
  possibly a repository *ruleset* on tag refs rather than purely a
  credential restriction. If so, `GITHUB_TOKEN` could hit the same
  restriction unless GitHub Actions is on that ruleset's bypass list. This
  is empirically unresolvable from a working tree — but the failure mode is
  loud (`git push` fails, step exits non-zero with a clear message), not
  silent, and the fix if it happens is a one-line repo-ruleset setting. The
  merge to this repo (which will attempt a real tag push, given the live
  drift above) is the actual test. `SKILL.md` now says this explicitly.

## Scope note

This card also touches `ut-plugin-language-de` and `ut-plugin-language-es`
(identical file, each gets its own PR and this same review record content
adapted per repo) and `ut-docs/.claude/skills/devops/SKILL.md` (documents
the automation and preserves the manual fallback for every other
`ut-plugin-*` repo, which does not have this yet — tracked as a follow-up
Backlog card).

## Non-goals confirmed out of scope

- Rolling this out to the ~17 other `ut-plugin-*` repos (follow-up card).
- Changing `release.yml`, `ci.yml`, or `commit-attribution.yml`.
- Adding a `concurrency:` group to `release.yml` (noted above, separate
  change).
