# Code review: fix SIGN DE base URL and FINISHED-transaction response parsing

**Date:** 2026-08-18
**Scope:** `src/main.go`, `manifest.json`, `README.md`, `CLAUDE.md`, new
`src/fiskalyparse/` package.
**Trigger:** while preparing for a fiskaly sales call, a real SIGN DE
sandbox account was created and the full TSE-signing lifecycle was run
against it live (auth → create TSS → initialize → create/register client →
start+finish a transaction → retrieve it). That test surfaced two real bugs
in this plugin's code, both fixed here.

## What shipped

- **`signDEBase` (now `fiskalyparse.SignDEBase`) fixed**: was
  `https://kassensichv.io/api/v2`, confirmed **dead** — plain nginx 404 on
  every path including root, not fiskaly's server anymore. Corrected to
  `https://kassensichv-middleware.fiskaly.com/api/v2`, verified by running
  the full lifecycle above against it live, and cross-checked against
  fiskaly's official docs (workspace.fiskaly.com/api/sign-de/), which name
  this host as the primary base URL.
- **`manifest.json`'s `net:kassensichv.io` permission changed** to
  `net:kassensichv-middleware.fiskaly.com` — confirmed required (not
  cosmetic): `universal-till/internal/plugins/permissions.go`'s permission
  check is an exact string match against `net:<hostname>`, so without this
  change the `http_request` host function would deny every fiskaly call
  even with a correct URL constant.
- **`parseSignResponse` (now `fiskalyparse.ParseSignResponse`) fixed**: was
  reading the signature from `resp.tss_tx_result.signature.value` — a
  wrapper that does **not exist** in fiskaly's real response. The real
  response has `signature.value` and `log.timestamp` as **top-level**
  fields. This bug meant the base-URL fix alone would not have been
  sufficient: even hitting the right host, every real sign attempt would
  still have logged `finish_response_missing_signature` and discarded a
  signature fiskaly had actually returned.
- **Extracted `src/fiskalyparse`** (no `wasip1` build tag, same shape as the
  existing `src/datev` precedent) so both base-URL constants and
  `ParseSignResponse` are host-testable — `src/main.go` is `wasip1`-only and
  excluded from ordinary `go test ./...`, so this logic was previously
  provably untested by CI, not just untested today.
- Version bumped `0.3.1` → `0.3.2`. `dsfinvkBase`/DSFinV-K deliberately
  **not** touched — still points at the now-confirmed-dead `kassensichv.io`,
  flagged `NEEDS SANDBOX VERIFICATION`, because fiskaly's docs suggest
  DSFinV-K may be a genuinely different API/host, not just a different path
  on SIGN DE's host. Guessing would be worse than leaving it flagged wrong.

## Independent review (Opus, fresh context, different model from Dev/Tester)

Findings, and what was done about each:

1. **BLOCKING, fixed.** README's older "Placeholder / unconfirmed" section
   (below the status table) still described the deleted `tss_tx_result`
   wrapper as the current best-effort guess, and still listed `signDEBase`'s
   host as unconfirmed — directly contradicting the status table 140 lines
   above, and this repo's own `CLAUDE.md` forbids exactly this drift.
   Rewritten into "Confirmed 2026-08-18" / "Still placeholder" sections that
   match the code as shipped.
2. **SHOULD-FIX, fixed — a second, real bug found by the review itself.**
   The first draft of `ParseSignResponse` decoded `log.timestamp` as a fixed
   `int64` in the *same* struct as `signature.value`. Since fiskaly's own
   `log.timestamp_format` field documents more than one possible value
   (`unixTime` was the only one observed live; `utcTime`/others would decode
   as a JSON string, not a number), any non-integer timestamp would fail the
   whole `json.Unmarshal` call and silently discard a real signature —
   exactly the bug class this commit exists to eliminate, just narrower.
   Fixed by decoding the signature and the log timestamp as two
   **independent** `json.Unmarshal` calls against the same body, so a
   timestamp shape this function doesn't recognize can never take the
   signature down with it. A follow-up self-check during the fix caught a
   further edge case (`null` unmarshals into `int64` as `0` with no error,
   which would have rendered as `logTime = "0"` instead of empty) — caught
   by the new test suite itself (`TestParseSignResponse_TimestampShapes`
   failed on first run), fixed, verified green.
3. **Regression-test gap — reviewer's judgment: warranted, not scope creep;
   done.** Both bugs are exactly the class a table test pins for free, the
   `src/datev` extraction is direct precedent for solving this same
   "wasip1-only, so untestable" problem, and finding #2 above is live proof
   the extraction pays for itself immediately. `src/fiskalyparse/parse_test.go`
   now has: a test against the **real captured response body** from the live
   sandbox run; a regression test pinning that the old `tss_tx_result`
   wrapper shape must never parse again; missing-log and unparseable-body
   cases; the timestamp-shape table (int/string/null); and a test that reads
   `manifest.json` and asserts `SignDEBase`'s hostname is covered by a
   declared `net:<host>` permission — turning the exact bug class behind
   finding needing the manifest change into something CI catches forever.
4. **SHOULD-FIX (honesty) + new board card, done.** Confirmed independently
   (`grep` across the whole repo for `fiscal.sign.ask`/ADR-0041/044/048
   returned zero hits before this review): `universal-till` core's actual
   TSE-signing extension point is a different, newer hook, `fiscal.sign.ask`
   (blocking, exclusive between signer plugins, persists evidence, renders
   it on the receipt, gates ADR-0048's system-of-record check). This plugin
   only declares `sale.completed` and does not subscribe to it — core
   currently sees **zero fiscal signers installed**, so this plugin's
   now-working fiskaly connection is disconnected from the till's real
   receipts/compliance gate. The diff's language was accurate but
   incomplete (never literally claimed this fixes German TSE signing
   end-to-end) — closed the gap with explicit paragraphs in `README.md` and
   `CLAUDE.md` and `src/main.go`'s package doc comment. Filed as
   `ut-docs#818` (high priority — the review's own judgment: this fix makes
   the gap *more* urgent, since the fiskaly side is now demonstrably
   functional, not less).
5. **NICE-TO-HAVE, filed as `ut-docs#819`.** The unsigned-retry queue can
   re-issue `tx_revision=1` against a `tx_id` fiskaly may have already
   partially consumed (deterministic `txID`, so a retry after a
   start-succeeded/finish-failed attempt targets the same transaction id at
   the same starting revision). Not introduced by this fix (before it, no
   call ever reached fiskaly, so the path was unreachable) — now a live,
   reachable risk instead of a theoretical one. Documented in README's
   "Still placeholder" section; not fixed here.
6. **NICE-TO-HAVE, documented.** An in-place plugin upgrade does not
   re-grant a newly-added `net:<host>` permission automatically (permission
   rows are inserted once, unlike settings which reconcile) — a manager must
   manually re-grant it after upgrading from 0.3.1. Added as an explicit
   upgrade note in `README.md`. Low impact today (no real installs of this
   plugin exist).

Not findings, confirmed by the reviewer rather than assumed: no
`os.MkdirAll`/`paths.Data(...)` issue (this plugin has zero filesystem I/O —
`grep` for write/create/MkdirAll/Open/filepath.Join under `src/` returns
nothing); no secret-shaped literal values or real client/shop names in the
diff; the three core code changes are each independently correct, including
every call site and doc comment, not just the constant definitions.

## Verification

- `bash scripts/build.sh` / `bash scripts/validate.sh` / `go test ./...` /
  `go vet ./...` — all clean, re-run independently by Dev, Tester, and the
  review subagent (three separate passes, not one carried claim).
- `go test ./...`: 33 passed across 2 host-testable packages (`src/datev`'s
  existing 24, `src/fiskalyparse`'s new 9).
- **TDD discipline, verified inline, not just claimed**: for both the
  manifest-permission-coverage test and the `parseSignResponse` regression
  tests, the fix was reverted, the specific test re-run and confirmed to
  fail with the expected real error, then the fix restored and the test
  re-confirmed passing — done atomically (no turn boundary between revert
  and restore) rather than via a separate isolated worktree, since this is
  an interactive session with no risk of a concurrent stop-hook commit
  landing mid-revert (that risk is specific to the autonomous
  cloud/cron pipeline, ut-docs#386).
- `ParseSignResponse`'s core claim (signature extraction from the real
  response shape) was additionally checked against the **actual JSON body**
  captured from the live fiskaly sandbox transaction earlier the same
  session — not a hand-written fixture guessing at the shape.
- What was **not** verified, stated plainly rather than left silent: the
  compiled plugin has not been run end-to-end through `universal-till`'s
  real wazero runtime / event bus (see finding #4 — there is currently no
  meaningful way to do that for `sale.completed`-shaped signing given core's
  real signing gate is `fiscal.sign.ask`, which this plugin doesn't
  implement); `REDUCED_1`/`NULL`/`SPECIAL_RATE_1` VAT buckets, the
  `RECEIPT_0104` return-receipt type, and the deterministic-`tx_id` scheme's
  acceptability were not individually exercised against live fiskaly (only
  `NORMAL`/`NON_CASH` was); DSFinV-K remains completely unverified.

## Verdict

**Safe to merge.** Three real, verified bug fixes (dead host, missing
permission, wrong response-parsing shape — the last one caught a second,
independent bug during the fix itself), a permanent regression-test suite
where none existed before, and two new board cards for the real,
larger gaps this work surfaced rather than silently deferring them. No
blocking issues remain open.
