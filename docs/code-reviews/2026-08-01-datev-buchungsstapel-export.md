# Code review: DATEV Buchungsstapel export (second `export` entry)

**Date:** 2026-08-01
**Branch:** `feat/datev-buchungsstapel-export`
**Closes:** universaltill/ut-docs#41 (DATEV/FiBu Buchungsstapel accounting-batch export)
**Scope:** `src/datev/` (new), `src/main.go`, `manifest.json`, `README.md`, `CLAUDE.md`, `.github/workflows/ci.yml`.
**Reviewer:** independent subagent, model override `opus` (different from the implementing session's model).

## What shipped

A second `export`-type manifest entry, `datev-buchungsstapel-export-de`,
alongside the existing `dsfinvk-export-de`. Per ADR-0025's own guidance
("split `tax`+`export` entries in one repo"), this landed as a third entry
in `ut-plugin-tax-de` rather than a new `ut-plugin-*` repo — same
jurisdiction, same manifest, same `export.requested.ask` dispatch mechanism
already proven by DSFinV-K.

Unlike DSFinV-K (an async fiskaly API trigger), this is pure local data
transformation: the till's `export.requested.ask` payload already carries
`sales[]` (ut-docs#221 — receipt number, timestamp, per-tax-band net/tax,
per-payment-method amounts); `src/datev/datev.go` turns that into a DATEV
EXTF "Buchungsstapel" CSV file, returned inline via `content_b64` — no
fiskaly account, no network call.

- **`src/datev/`** (new package, deliberately no `//go:build wasip1` tag so
  it's testable with plain `go test ./...` on the host — this repo's only
  automated tests): `Build(from, to, sales, settings, now)` renders the
  file; `Settings` holds the merchant/accountant-configured chart-of-accounts
  mapping with **no default real account numbers anywhere** — `Build`
  refuses (naming the gap) rather than guess when a tax rate has no
  configured Gegenkonto, or when `datev_konto_kasse`/`datev_berater_nr`/
  `datev_mandant_nr` are unset.
- **`src/main.go`**: `export.requested.ask` now dispatches on `entry_key`
  between `handleDSFinVKExport` (extracted, behaviorally unchanged) and the
  new `handleDATEVExport`; unknown keys still decline.
- **`manifest.json`**: new entry + 7 new `datev_*` settings, all
  empty-string/`{}` defaults. Version 0.2.0 → 0.3.0.
- **Format grounding**: the 125-column booking-row header and the 31-field
  header-row-1 layout were extracted byte-verified from a real public
  reference file (github.com/ledermann/datev's `EXTF_Buchungsstapel.csv`,
  decoded from its native Windows-1252 encoding), not reconstructed from
  memory — see `src/datev/datev.go`'s package doc comment.

## Independent review — findings and fixes

Spawned a `general-purpose` subagent (`model: opus`), briefed with the full
diff and told to run the gate itself, fetch and byte-diff against the same
reference file independently, and actively try to break the "never guess an
account number" invariant rather than take the implementation's word for it.
It did all three, plus its own mutation testing (7/8 mutations caught by the
existing suite). Findings, all fixed before merge:

**Should-fix (all real, all fixed):**

1. **Header row 1 was 4 fields short (27 vs. the real 31).** An earlier
   draft's trailing-reserved-fields comment ("5 trailing reserved fields")
   undercounted against the reference file's actual 9 — and the test
   asserted the wrong number (27), so it couldn't have caught itself. Fixed
   to 9 trailing fields (31 total); added `TestHeader1_FieldCount` pinned to
   the reference count, independent of the implementation's own constant.
2. **A configured-but-empty Gegenkonto (`{"700": ""}`) passed the
   missing-account guard**, which checked map-key presence, not
   non-emptiness — would have silently emitted a blank Gegenkonto column
   instead of refusing. Fixed to `strings.TrimSpace(...) == ""`; added
   `TestBuild_EmptyStringErloeskonto_TreatedAsMissing`.
3. **No reconciliation between a sale's `tax_lines` sum and its `Total`.**
   `universal-till`'s `SalesForExport` builds `tax_lines` from `sale_lines`
   independently of `sales.total`, and a sale-level discount isn't currently
   pushed down into `sale_lines` — so a discounted sale's tax-line sum can
   legitimately exceed its total, which would have booked a Kasse debit
   larger than what the till actually took. `Build` now refuses the whole
   export and names the mismatched sale(s) rather than book a number that
   doesn't reconcile; documented as Known gap #7 (a real limitation — any
   period with a discounted sale can't be DATEV-exported until discounts
   are reflected per-line upstream, not just a check that never fires).
   Added `TestBuild_TaxLinesDontReconcileWithTotal_Refuses`.

**Nice-to-have (fixed anyway — all cheap, all real):**

4. Unquoted `Konto`/`Gegenkonto` values and unescaped quotes in
   `Belegfeld 1`/`Buchungstext` could shift CSV columns (e.g. a receipt
   number containing `;` or `"`). Fixed: `Konto`/`Gegenkonto` values are now
   validated digits-only (`isDigits`, rejecting with a clear error
   otherwise); text fields use standard CSV double-quote escaping (`q`).
   Added `TestBuild_ReceiptNoWithQuote_EscapedNotBroken`,
   `TestBuild_KontoKasseNotDigits`, `TestBuild_ErloeskontoNotDigits`.
5. `datev_wj_beginn` was length-checked (4 chars) but not digit- or
   range-checked — `"abcd"` silently produced `WJ-Beginn 20260000`. Fixed:
   digit + month(01-12)/day(01-31) validation. Added
   `TestBuild_WJBeginnNotDigits`, `TestBuild_WJBeginnMonthOutOfRange`.
6. The fiscal-year-boundary test only exercised the "well before" case, not
   the boundary itself — a `Before` → `!After` mutation left the suite
   green. Added `TestBuild_FiscalYearBoundary_ExactStartDate` (from == the
   WJ start date, asserting the fiscal year is the current one, not prior).
7. `bookingDate` only accepted RFC3339 or `YYYY-MM-DD`, not
   `universal-till`'s SQLite `created_at` column default format
   (`"2006-01-02 15:04:05"`, no `T`/zone) — production `CompleteSale` writes
   RFC3339 so normal rows were unaffected, but any row from the column
   default (seed/imported/legacy) would have aborted the whole export.
   Added the third layout; `TestBookingDate_AlternateLayouts`.
8. `Buchungstext`/`Belegfeld 1` truncation lengths were swapped (36 applied
   to Buchungstext, no truncation on Belegfeld 1 / receipt number — DATEV's
   documented limits are the reverse: 60 / 36). Fixed; added
   `TestBuild_ReceiptNoTruncatedTo36`.
9. An empty period silently produced a "successful" header-only download,
   which could as easily mask a wrong date range as confirm a genuinely
   sale-free period. `Build` now refuses with a clear "no sales in period"
   message. A malformed `export.requested.ask` payload was silently
   swallowed (`_ = json.Unmarshal`) and fell through to the DSFinV-K
   fallback path — misleading for a DATEV request with a bad payload. Both
   now fail closed with a clear error. Added `TestBuild_NoSalesInPeriod_Refuses`.

**Verified correct** (reviewer actively tried to break these): header row 2
is byte-identical to the reference file (programmatic diff, not eyeballed);
output is genuinely not valid UTF-8 and decodes correctly as CP1252
(byte-set `{0x96, 0xe4, 0xf6, 0xfc}`, matching the reference); `bookingDate`'s
`ddMM` (not `MMdd`) formatting is correct; `formatAmount` has no
sign/rounding bugs; the wire contract matches `universal-till`'s
`ExportSaleRow`/`exportResponse` field-for-field; the `src/main.go` dispatch
refactor is behaviorally identical for DSFinV-K/empty/unknown `entry_key`;
adding a second export entry doesn't break the host's existing single-entry
UI path (`len(entries) > 1` already renders a picker); no real
chart-of-accounts numbers anywhere in non-test source; no secrets, no SQL,
no client/shop names.

## Verification (post-fix, re-run personally)

- `go test ./...` — 24 tests, all pass (`src/datev` — the only host-testable
  package).
- `go vet ./...` (host) and `GOOS=wasip1 GOARCH=wasm go vet ./src` — clean.
- `gofmt -l .` — clean.
- `bash scripts/build.sh` — builds `bin/plugin.wasm` clean.
- `bash scripts/validate.sh` — `ok com.universaltill.tax-de v0.3.0`.
- `bash scripts/package.sh` — packages clean (dry run).
- Re-ran the reviewer's own mutation set (missing-Gegenkonto guard removed,
  S/H-Kennzeichen flipped, Belegdatum format swapped) personally — all three
  produce genuine test failures, confirming the suite is load-bearing, not
  a false-pass.
- CI (`.github/workflows/ci.yml`) now runs `go test ./...` as its first
  step — this repo had zero testable Go code before this change (the only
  file was `//go:build wasip1`-gated), so this is a new, permanent gate for
  the package going forward, not a one-off local check.

## Deferred (real, tracked in README, not silently dropped)

- Splitting `Konto` by payment method instead of one Kasse/Bank account for
  the whole sale (Known gap #5).
- Confirming DATEV's current format-version numbers and the Soll/Haben
  booking convention against DATEV's own current published spec / a live
  accountant import test — `developer.datev.de` 403'd when fetched during
  this session (Known gap #6).
- Pushing sale-level discounts down into `sale_lines` so tax-line sums
  reconcile with `sales.total` for every sale, not just undiscounted ones
  (Known gap #7 — a `universal-till`/`internal/data` change, out of scope
  for this plugin repo).
- Accountant confirmation of the actual account numbers a merchant enters
  into `datev_konto_kasse`/`datev_erloeskonten` — this plugin can refuse an
  unconfigured or malformed value, it cannot know if a configured one is
  the *right* one for a given business.

## Verdict

**Safe to merge**, post-fix. All three should-fix findings and all six
nice-to-have findings from the independent review are resolved and each has
a regression test that fails without the fix (verified personally for the
three most safety-relevant: missing-Gegenkonto, S/H convention, Belegdatum
format). The remaining gaps are genuine, documented limitations (a
pre-existing upstream data gap, and format details that need a real
accountant/DATEV import to fully confirm), not defects in this diff.
