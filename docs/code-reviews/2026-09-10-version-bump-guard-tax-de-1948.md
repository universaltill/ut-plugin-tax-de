# Code review: manifest.json version-bump guard, compiled-plugin variant (ut-docs#1948)

**Date:** 2026-09-10
**Branch:** `feat/1948-version-bump-guard-tax-de` (commit `6aa1c94` + review fixes)
**Author:** implemented inline (Sonnet)
**Reviewer:** independent Opus subagent (fresh context, no shared context with the author)

## What shipped

Rollout of the ut-docs#1940 version-bump guard into this repo, tracked by
ut-docs#1948. Four files, CI-tooling only:

- `scripts/check-version-bump.sh` (new) — fails a PR that changes a shipped
  file without moving `manifest.json`'s `version`.
- `scripts/check-version-bump.test.sh` (new) — 19 cases (15 as authored,
  4 added in review), each against a throwaway git repo with real commits.
- `.github/workflows/ci.yml` — new PR-only `version-bump` job.
- `CLAUDE.md` — the "Before committing" rule.

`git diff main --name-only` confirms **nothing under `src/`** is touched, so
no fiscal-signing path (`fiscalsign`, `taxrate`, `datev`, `auditkey`,
`fiskalyparse`, `wasmrun`, `main.go`) is in scope. No scope creep found.

## Why this repo is the hard case

Every prior repo in this rollout (`ut-plugin-language-{de,es}`,
`ut-plugin-theme-*`, `ut-plugin-integration-ai`) is asset- or config-only:
`package.sh`'s `entries=(...)` names files that git actually tracks, so the
guard can key `SHIPPED_PATTERNS` off the literal bundled path.

Here `entries=(manifest.json README.md bin)` bundles `bin/`, which is
`scripts/build.sh`'s **output** (`go build -o bin/plugin.wasm ./src`) and is
gitignored. `git diff --name-only` can therefore never see the shipped
artifact change. The author's design — key off what *produces* the binary
instead — is the right call, and the "entries mirror" self-test's
`git check-ignore` branch to distinguish tracked from built entries is a
genuinely good generalization rather than a fixture-shaped hack (proved by
mutation, below).

## Findings

### Fixed in review

**F1 (blocker-adjacent — silent guard erosion). `check-version-bump.test.sh`
"entries mirror" case: `patterns` was scraped from the whole script.**
The check read `grep -oE "'[^']*'" "$REAL_SCRIPT"`, i.e. every
single-quoted token anywhere in a deliberately comment-heavy file — not the
`SHIPPED_PATTERNS` array. Verified by mutation: replacing the `'go.sum'`
array line with a *comment* containing `'go.sum'` left the mirror case
green. Because `LICENSE` and `go.sum` had no behavioural case of their own,
that was a **total** false pass — the entire suite went green while the
guard silently stopped covering those shipped files. Extraction is now
anchored to the `SHIPPED_PATTERNS=(...)` body with `#` comments stripped
first (`check-version-bump.test.sh:266`), so neither a comment outside the
array nor one inside it can masquerade as an entry.

**F2 (should-fix — coverage asymmetry). `LICENSE` and `go.sum` had no
behavioural test case.** Every other pattern (`manifest.json`, `README.md`,
`locales/*`, `src/*`, nested `src/`, `go.mod`) had one; these two were
mirror-test-only, which is what made F1 total rather than backstopped.
Added as cases 16 and 17 (`check-version-bump.test.sh:466`, `:476`).

**F3 (should-fix — real false negative). The build recipe was not a shipped
input.** `SHIPPED_PATTERNS` covered `src/*` + `go.mod`/`go.sum` but not
`scripts/build.sh`, which *is* an input to `bin/plugin.wasm`: adding
`-trimpath`/`-ldflags`/`-tags`, or changing `GOOS`/`GOARCH` or the `-o`
path, produces a different shipped binary with every file under `src/`
byte-identical. Reproduced against a real scratch repo — appending a flag
line to `build.sh` gave `ok: no shipped file changed`, i.e. exactly the
silent-never-ships failure the guard exists to prevent. Added
`'scripts/build.sh'` to `SHIPPED_PATTERNS`
(`check-version-bump.sh:62`), extended the mirror case's gitignored-entry
branch to require it (alongside `go.mod`/`go.sum`), added behavioural case
18, and corrected the script header and `CLAUDE.md`.

This is a false positive only for a comment-only edit to a six-line script,
and the header already records the same accepted trade for `*_test.go`
under `src/*`: a harmless extra bump beats a real change shipping unbumped.
It is also this-repo-specific — the asset-only repos have no build step —
so it introduces no divergence in the rollout.

**F4 (should-fix — real false negative). Rename detection hid renames *out
of* a shipped location.** `git diff --name-only` has rename detection on by
default (git ≥ 2.9) and prints only the destination, so
`git mv src/fiscalsign/f.go docs/f.go` — which really does remove a file
from the compiled package — reported as a lone `docs/f.go` and the guard
answered `ok: no shipped file changed`. Reproduced against a real scratch
repo. Fixed with `--no-renames` (`check-version-bump.sh:111`), which makes
the same change report as delete + add. Renames *into* or *within* a
shipped location were already caught (the destination is the shipped path),
so this only ever adds coverage. Behavioural case 19 pins it.

*This one is inherited by the whole rollout* — an asset-only repo has the
same hole for `git mv locales/de.json docs/de.json`. Worth folding the
one-flag fix back into ut-docs#1940's template and the already-merged
sibling repos; flagged here for ut-docs#1948 rather than fixed cross-repo
from this branch.

### Deferred as findings (not fixed here)

**N1 (should-fix, ecosystem-wide). A version *downgrade* passes the guard.**
`check-version-bump.sh:189` tests `base_version = head_version`, i.e.
"different", while the failure message tells the author to pick "a version
**higher** than X". A shipped change plus `0.5.4 → 0.5.3` exits 0 and even
prints `ok: manifest.json version bumped 0.5.4 -> 0.5.3`. In practice the
already-tagged check catches the realistic case (the lower version is
normally already released), so this needs a *skipped* or typo'd lower
version (`0.5.4 → 0.4.9`, `0.5.4 → 0.54`) to bite — but then
`auto-tag-release.yml` happily cuts a tag that sorts below the live
release. A numeric comparison when both versions are plain integer triples
(falling back to the current equality test for pre-release strings, which
`:181`'s guarded arithmetic already models) would close it. Left alone
because it changes semantics shared by every repo in the rollout; belongs
on ut-docs#1948 as one cross-repo change, not a per-repo divergence.

**N2 (nit). `if: always()` on the self-test step is a no-op.** It is the
first step after `checkout` in `version-bump`, so the only prior step that
could fail is the checkout itself, in which case running the self-test is
pointless anyway. Harmless, and it matches the reference implementations'
shape, so left as-is — noted only so nobody reads it as load-bearing.

**N3 (nit). The `version-bump` job is not on branch protection.** Out of
scope for this PR and a repo-settings change, but the guard only actually
blocks a merge once it is required. Worth confirming on ut-docs#1948
alongside the other rolled-out repos.

## What was verified beyond automated tests

Everything below was run by the reviewer, not taken from the diff or the
commit message.

**The suite genuinely passes.** `bash scripts/check-version-bump.test.sh`
run directly: 15/15 as authored, 19/19 after the review fixes, exit 0.

**Mutation testing of the guard-of-the-guard (17 mutants).** The concern
with a self-test that re-parses `package.sh`/`build.sh` is that it checks
its own fixture rather than the real scripts. It does not: deleting any of
`src/*`, `go.mod`, `go.sum`, `locales/*`, `LICENSE`, `README.md` from
`SHIPPED_PATTERNS`; pointing `build.sh` at `./cmd` instead of `./src`;
adding a tracked `assets` entry to `package.sh`'s bundle; deleting
`build.sh`'s `go build` line; and deleting `package.sh`'s `entries=(...)`
line were each caught with an accurate message naming the real cause. Only
the comment-scraping mutant survived (F1). After the fixes, all 17 mutants
die — most now to two independent cases (the mirror check *and* a
behavioural case), including new mutants for `--no-renames` reverted,
`'scripts/build.sh'` removed, `BUILD_SH_REL` drifted to `tools/build.sh`,
and the array emptied wholesale.

**Real scratch-repo behaviour probes (7), separate from the fixture.** Built
throwaway repos mirroring this repo's actual layout and ran the real script
over real commits: deleting a shipped locale → fails; a non-ASCII shipped
path (`locales/de-Ü.json`) → fails and prints the path as real UTF-8, not
octal escapes (the `core.quotePath=false` claim holds); `docs/` +
`.github/` only → passes; touching `scripts/check-version-bump.sh` itself →
passes. Renaming out of `src/` (F4) and editing `build.sh` (F3) wrongly
passed before the fixes and correctly fail after.

**SHIPPED_PATTERNS is complete against the real `package.sh`.** The bundle
is `manifest.json README.md bin` plus conditional `LICENSE` and `locales`
— all covered, with `bin` covered transitively through `src/*` +
`go.mod`/`go.sum` + (now) `scripts/build.sh`. `src/*` inside `[[ ]]` does
match `/`, so nested packages like `src/fiscalsign/fiscalsign.go` are
covered; confirmed by case 15 and by the real `src/` tree. Note `go.sum`
here pins only `wazero`, a test-only dependency, so it can never actually
change the shipped binary — kept as a deliberate over-approximation,
consistent with the header's stated bias.

**The shellcheck directive is the fixed form, read directly.**
`check-version-bump.sh:103` is `# shellcheck disable=SC2053` on its own
line with the reason prose on the preceding line — i.e. the
`ut-plugin-theme-buttons-left` form, not the original
`ut-plugin-language-de` `disable=SC2053 -- <prose>` form that trips
SC1072/SC1073. shellcheck v0.10.0 over `scripts/*.sh` (all eight, not just
the two new ones): **0 issues**, before and after the review fixes.

**The guard on its own PR.** `BASE_SHA=main HEAD_SHA=HEAD` against this
branch → `ok: no shipped file changed`, correct: nothing here ships.

**CLAUDE.md accuracy.** Checked line by line against the code. As authored
it was accurate; updated for F3 (`scripts/build.sh` added to the shipped
list with the reason), and the exemption sentence tightened — "only
`docs/`/`.github/`" was not quite right now that one path under `scripts/`
is shipped.

**Full existing suite, on the branch with review fixes applied.**
`gofmt -l .` clean · `GOOS=wasip1 GOARCH=wasm go vet ./...` clean ·
`go build ./...` clean · `go test ./...` all six packages pass (incl. the
real wazero run in `src/wasmrun`, 32s) · `bash scripts/build.sh` →
`built bin/plugin.wasm (3612270 bytes)` · `bash scripts/validate.sh` →
`ok com.universaltill.tax-de v0.5.4` · `bash scripts/guard-plugin-i18n.sh`
→ ok · `bash scripts/package.sh` → packaged.

## Verdict

**SAFE TO MERGE** with the four review fixes applied (F1–F4, all in this
branch). The design decision this card was blocked on — how the guard
handles a gitignored, compiled bundle entry — is sound and, after F3, the
"source that produces the binary" set is actually complete.

N1 and F4's cross-repo half should be folded back into ut-docs#1940's
template and the already-merged sibling repos.
