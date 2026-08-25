# Germany Fiscal Compliance (TSE + DSFinV-K + DATEV) — Universal Till plugin

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing Germany's KassenSichV
fiscal requirements via **fiskaly**'s Cloud-TSE ("SIGN DE") API for
per-transaction TSE signing, and fiskaly's DSFinV-K API for tax-audit
exports. Built per [`ut-docs` ADR-0025](https://github.com/universaltill/ut-docs/blob/main/adr/0025-country-tax-and-fiscal-compliance.md)
("Country-specific tax rates and fiscal compliance"), which chose fiskaly as
the first integration target because SumUp itself delegates TSE signing to
fiskaly rather than building it in-house — real-world precedent that this is
the standard integration shape for German fiscal compliance.

Also builds a **DATEV EXTF Buchungsstapel export** (ut-docs#41) — a
locally-generated accounting-batch file for handing sales to an accountant,
unrelated to fiskaly/KassenSichV (see its own section below).

## Status: starting skeleton, not a certified compliance solution

Read this section before installing on anything that isn't a test till.
Labeled the way `germany-pos-parity-backlog.md` labels claims — "confirmed"
vs. "researched, not tested":

| Claim | Status |
|---|---|
| SIGN DE (TSE signing): host, auth, TSS/client/transaction lifecycle, `standard_v1` receipt schema | **Confirmed 2026-08-18** against a real fiskaly TEST-environment sandbox: auth → create TSS → initialize → create/register a client → start+finish a transaction (19% VAT, card) → retrieved with a valid ECDSA signature. Fixed two real bugs this uncovered: `signDEBase` was pointed at `kassensichv.io`, now confirmed **dead** (plain 404 on every path, not fiskaly's server anymore) — corrected to `kassensichv-middleware.fiskaly.com/api/v2`; `parseSignResponse` was reading the signature from a `tss_tx_result` wrapper that doesn't exist in the real response (it's top-level `signature.value`) — would have silently discarded every real signature even with a working host. See `src/main.go` doc comments for exactly what was and wasn't exercised (e.g. only the `NORMAL` VAT bucket, not `REDUCED_1`/`NULL`/`SPECIAL_RATE_1`; this test's own curl script used a 3-call start/update/finish sequence, while this plugin's code uses a 2-call start/finish sequence — architecturally consistent with fiskaly's revision model but not independently re-verified). |
| DSFinV-K export: endpoint paths/request shapes | **Researched, not tested — unchanged by the 2026-08-18 fix.** Still points at `kassensichv.io` (now confirmed dead), and may need a genuinely different host from SIGN DE's, not just a different path — see `dsfinvkBase`'s doc comment in `src/main.go`. Every endpoint here remains flagged `NEEDS SANDBOX VERIFICATION`. |
| Tested against a real fiskaly sandbox/production account | **Partially done, 2026-08-18.** SIGN DE's raw API contract was exercised directly (see row above). The compiled plugin now runs through a real wazero runtime (see the end-to-end row below) but against a *stubbed* HTTP layer, not live fiskaly — the two halves are each verified, their combination is not. DSFinV-K has not been touched at all. |
| DSFinV-K export format/content is legally compliant | **Not verified, at all.** No DSFinV-K output from this plugin has been checked against the DSFinV-K spec or a real tax audit. |
| KassenSichV compliance overall | **Not certified.** This is a starting skeleton. A merchant must get real legal/tax-advisor sign-off before relying on this for a live business — this plugin existing does not make a till KassenSichV-compliant. |
| `go build ./...` / `scripts/build.sh` | **Confirmed** — builds clean as of this commit (see CI). |
| End-to-end behavior against the till's real event bus | **Partially verified, and now covered by a committed test suite** (`src/wasmrun`, 2026-08-19). The `fiscal.sign.ask` path is exercised against the REAL compiled `plugin.wasm` in a real wazero runtime (same engine `universal-till` uses) with stubbed host functions: event dispatch, settings lookup, the auth→start(`tx_revision=1`)→finish(`tx_revision=2`) call order, the exact money in the signed body, the full §6 evidence (RFC3339 timestamps, not raw epochs), and the exact stdout JSON for the approved path plus four failure paths (fiskaly 5xx, unconfigured, malformed payload, unbalanced receipt). Proven to bite: deleting the dispatch, dropping `tax` from the VAT gross, or answering `approved` without a signature each fail it. Still NOT proven: `universal-till`'s actual host-function implementations, a real installed-plugin flow through the till UI, and the HTTP layer here is a stub rather than live fiskaly — the two halves are each verified, their combination is not. |
| DATEV EXTF file structure (31-field header row 1, 125-column header row 2, semicolon-delimited, Windows-1252, CRLF) | **Confirmed against a real reference file** (github.com/ledermann/datev's `EXTF_Buchungsstapel.csv`, byte-verified 2026-08-01), not reconstructed from memory — see `src/datev/datev.go`'s package doc comment. An independent review caught an early draft undercounting header row 1's trailing fields (27 vs. the real 31); fixed and pinned by `TestHeader1_FieldCount`. Unit-tested (`go test ./src/datev/...`), including a Windows-1252 round-trip check on the umlaut/en-dash header text. |
| DATEV format-version numbers (`700`/`21`/`13`) and Soll/Haben booking convention (Kasse debited, Erlöskonto credited via an Automatikkonto, no BU-Schlüssel by default) | **Researched, not confirmed against DATEV's current published spec** (developer.datev.de 403'd when fetched) or a real accountant/DATEV import. Matches the reference file and common SKR03/04 practice, but NEEDS ACCOUNTANT VERIFICATION before a real filing. |
| DATEV chart-of-accounts mapping (which Konto/Gegenkonto number per tax rate) | **Never guessed.** No default real account numbers anywhere in this plugin — `datev_konto_kasse`/`datev_erloeskonten` are merchant/accountant-configured settings; the export refuses (with a clear error) rather than emit an unconfigured or invented account number. |

## What this plugin does

Two canonical-type entries in one manifest, per ADR-0025 (no new plugin type
needed, ADR-0002's `tax`/`export` types already exist):

- **`tax` — TSE signing.** Hooks **`fiscal.sign.ask`** (core's real,
  blocking, tender-phase signing point — `sale.completed` was removed in
  v0.4.0). For each sale,
  attempts a real two-call TSE-sign round-trip against fiskaly's SIGN DE API:
  start the transaction (`state: ACTIVE`), then finish it (`state:
  FINISHED`) with a receipt schema built from the sale's real line items
  (VAT-rate buckets from `tax_rate_bp`) and payments (cash vs. non-cash).
- **`export` — DSFinV-K export.** Calls fiskaly's DSFinV-K API to trigger an
  export for a date range. Reachable from a manager's Data/Export page in
  the till since ut-docs#189 (`export.requested.ask` hook) — see "Known
  gaps" below for what's still incomplete once triggered.
- **`export` — DATEV Buchungsstapel export.** A second `export`-type entry
  (`datev-buchungsstapel-export-de`), also reachable from the Data/Export
  page. Unlike DSFinV-K, this needs no fiskaly account: it's a pure local
  transformation of the sales the till already sends in the
  `export.requested.ask` payload (ut-docs#221) into a DATEV EXTF CSV file,
  returned inline (`content_b64`) for immediate download — see `src/datev/`.
- **Dine-in/takeaway VAT rate switching (§12 UStG).** Subscribes to
  `tax.rate.ask` — a generic, blocking, value-returning hook
  (`EventBus.Ask`) universal-till's core added specifically so this rule
  didn't have to live in core itself (see `universal-till`'s
  `docs/code-reviews/2026-07-28-tax-rate-plugin-hook-refactor.md`). Core
  asks this plugin "what's the rate for this line, given this order type";
  `handleTaxRateAsk` in `src/main.go` answers from the merchant-configured
  `takeaway_rate_overrides` setting (tax_code_id → basis points) when the
  order type is takeaway, or declines (writes nothing) for dine-in or an
  unconfigured tax code — core then falls back to the line's own rate.

## Known gaps (read before assuming this "just works")

1. **Cloud-TSE vs. offline-first — the *architectural* half is resolved
   (v0.4.0); the *legal* half is not.** This gap previously described
   `sale.completed`'s dead end: non-blocking, fired after the sale, so the
   plugin could not block, reverse or even declare a failure, and worked
   around it with a private "unsigned, pending retry" queue. **That hook
   and that queue are gone.** The plugin answers `fiscal.sign.ask` — a
   blocking, tender-phase point with a 3000 ms budget and a
   `proceed-and-declare` policy — so core provides the journal marker,
   receipt outage notice, operator alert and background re-ask. A failure
   is now declared and visible, and the sale still never blocks on
   connectivity (ADR-0003 intact).

   What is **still unresolved**, exactly as ADR-0025 flags it: whether an
   unsigned-then-backfilled sale satisfies KassenSichV's "irreversible,
   tamper-proof at time of transaction" requirement. That needs real
   TSE-vendor/legal confirmation and is **not decided by this plugin**. A
   local/hardware TSE (dongle or TSE-integrated printer) may still be the
   right primary target for exactly that reason — ADR-0044 Decision 2 says
   so, and ADR-0055 records that such a backend cannot live in this wasm
   plugin anyway.

2. **DSFinV-K export is now reachable from the till (ut-docs#189), but only
   as a trigger.** A manager's Data/Export page action publishes the
   generic, blocking `export.requested.ask` event
   (`universal-till/internal/pages/data_api.go`); this plugin declares that
   event in `manifest.json`'s `hooks[]` and answers it in `main()`,
   calling `exportDSFinVK` and reporting back whether the fiskaly trigger
   call succeeded. Because `exportDSFinVK` only ever *starts* an async
   fiskaly job (can take up to an hour per fiskaly's docs — see Known gap
   #3), the response never has file bytes to hand back inline; the till
   shows the trigger result as a status message, not a download. Polling
   fiskaly for the finished export and surfacing a real download is not
   implemented.

3. **DSFinV-K export would be incomplete even once wired up.** A real
   DSFinV-K export depends on `cashPointClosing` records — periodic
   aggregates of every signed transaction — existing in the fiskaly account
   before `/export` is triggered. This plugin does **not** build those from
   the `tse_result:*` records it saves after each sign attempt. Calling
   `exportDSFinVK` today triggers an export against whatever (likely empty)
   closings already exist, not a real export of this till's sales. That
   aggregation step is real, non-trivial work, left undone here.

4. **VAT-rate and payment-type bucketing is best-effort.** `fiscalsign.VATRateBucket`
   maps 19%/7% basis-point rates to fiskaly's `NORMAL`/`REDUCED_1` schema
   enum; anything else falls back to `SPECIAL_RATE_1` rather than guessing
   further (fiskaly's enum also has `REDUCED_2`/`NULL`/`SPECIAL_RATE_2-5`).
   `fiscalsign.PaymentTypeBucket` only distinguishes `CASH` vs. `NON_CASH` (case-
   insensitive match on `"cash"`) — good enough for the schema's
   granularity, but not exhaustively tested against every payment method
   string the till can produce.

5. **DATEV export posts every sale to one configured Kasse/Bank account,
   regardless of payment method.** A real shop's accountant may want
   separate intermediary accounts per payment method (e.g. `1000` Kasse vs.
   a card-clearing account) — the export's payload already carries
   per-payment-method breakdowns (`ExportSaleRow.Payments`) but `src/datev`
   does not use them yet, posting the sale's whole gross total against
   `datev_konto_kasse` instead. A real follow-up, not built here.

6. **DATEV format-version numbers and the Soll/Haben booking convention are
   not confirmed against DATEV's current published spec or a live
   accountant/import test** — see the status table above. Don't treat a
   structurally valid file as an automatically correct one.

7. **A sale-level discount can make DATEV export refuse a sale outright.**
   `internal/data.ExportSaleRow.TaxLines` is built from `sale_lines`
   independently of `sales.total` (universal-till's `SalesForExport`), and a
   sale-level discount isn't currently pushed down into `sale_lines` — so a
   discounted sale's tax-line sum can legitimately disagree with its total.
   Rather than book a Kasse debit that doesn't match what the till actually
   took, `datev.Build` refuses the whole export and names the mismatched
   sale(s). Until discounts are reflected per-line upstream, any period with
   a discounted sale in it can't be DATEV-exported at all — a real
   limitation, not just a defensive check that never fires.

## What's real vs. placeholder

**Real (actual HTTP calls to fiskaly's documented API shape, not stubs):**
- `fiskalyAuth` — `POST /auth` with `api_key`/`api_secret`, caches the
  returned `access_token` in plugin storage, re-authenticates when the
  cache is stale or missing.
- `signTransaction` — the two-call SIGN DE transaction lifecycle (`PUT
  /tss/{tss_id}/tx/{tx_id}?tx_revision=1` then `?tx_revision=2`), building
  a `standard_v1` receipt schema from the sale's real line items/payments.
- `exportDSFinVK` — `POST {dsfinvk_base}/export` with a business-date range
  and format.
- Failure handling — never returns a fabricated signature, and never signs
  a receipt whose VAT side and payment side disagree. On either, it answers
  `unreachable` and core applies proceed-and-declare (journal marker,
  receipt outage notice, operator alert, background re-ask). The plugin's
  own `unsigned_queue` was removed in v0.4.0 — core owns retry now.

**Confirmed 2026-08-18 (SIGN DE only — see the status table above for exactly
what this does and doesn't prove):**
- `signDEBase` (now `src/fiskalyparse.SignDEBase`) — `kassensichv.io`
  (previously assumed current) is confirmed **dead**; the real host is
  `https://kassensichv-middleware.fiskaly.com/api/v2`, verified against a
  live sandbox and cross-checked against fiskaly's own docs
  (workspace.fiskaly.com/api/sign-de/).
- The `standard_v1` schema field names (`amounts_per_vat_rate`,
  `amounts_per_payment_type`) and the `NORMAL`/`NON_CASH` enum values —
  accepted and echoed back correctly by a live sandbox. `REDUCED_1`/`NULL`/
  `SPECIAL_RATE_1` were not individually exercised.
- The `FINISHED`-transaction response envelope
  (`src/fiskalyparse.ParseSignResponse`) — the signature and log timestamp
  are **top-level** fields (`signature.value`, `log.timestamp`), NOT nested
  under a `tss_tx_result` wrapper as an earlier draft of this code
  (incorrectly) assumed; that wrapper does not exist in fiskaly's real
  response and would have silently discarded every real signature even once
  the host was fixed. See `src/fiskalyparse/parse_test.go` — table-tested
  against the real captured response, plus a regression test pinning that
  the old wrapper shape must never parse again.

**Still placeholder / unconfirmed:**
- `dsfinvkBase` (`src/fiskalyparse.DsfinvkBase`) — still points at the
  now-confirmed-dead `kassensichv.io`, deliberately NOT changed by the
  2026-08-18 fix. fiskaly's own SIGN DE reference page documents no separate
  DSFinV-K host, which hints the real one may not be a distinct `dsfinvk.*`
  domain at all (possibly the same middleware host, a different path) — not
  verified either way, don't guess.
- `tx_id` generation (`txID` in `src/main.go`) — fiskaly's docs examples
  all use UUIDs; this derives a deterministic pseudo-UUID from the sale id
  since the till's `sale_id` isn't guaranteed to already be one. Whether
  fiskaly strictly requires a v4 UUID or accepts any unique string is
  unconfirmed. **Known related gap, not yet fixed** (ut-docs#819): because
  `txID` is deterministic, a retry of the *same* sale re-issues
  `tx_revision=1` against a transaction id fiskaly may have already
  partially consumed on the first attempt (e.g. `start` succeeded but
  `finish` failed) — that retry can conflict forever instead of draining.
  The plugin's own retry queue was removed in v0.4.0, but this survives
  unchanged: **core** now re-asks with `retry: true`, against the same
  deterministic `txID`. The fix is to `GET` the transaction on
  `start_failed` and adopt an existing `FINISHED` signature — real 2xx
  evidence, never inferring "400 means it was signed", which would break
  the never-fabricate rule.
- The DSFinV-K `/export` trigger request/response shape — reconstructed
  from fiskaly support docs ("ByBusinessDate"/"ByCreationDate" selection,
  TAR/ZIP format), not confirmed.

Every unconfirmed item above is also flagged `NEEDS SANDBOX VERIFICATION` at
its definition (`src/main.go` or `src/fiskalyparse/parse.go`) — the code
comments are the source of truth if this README drifts.

**Upgrade note (0.3.1 → 0.3.2 or any version that changes `permissions`):**
an already-installed plugin's permission ROWS are inserted once and never
reconciled on upgrade (unlike settings) — a new `net:<host>` permission
added in a later version lands **ungranted** until a manager re-grants it
in the till's plugin UI, even though the manifest now declares it. Every
fiskaly call will fail with a permission denial until that happens. Low
impact today (no real installs of this plugin exist yet), but real for
whoever installs 0.3.2 over an existing 0.3.x.

**This IS the till's real TSE signer, since v0.4.0 (2026-08-19,
ut-docs#818).** The plugin declares **`fiscal.sign.ask`** — `universal-till`
core's actual TSE-signing integration point (blocking, exclusive between
signer plugins, persists evidence, renders it on the receipt, gates
ADR-0048's system-of-record check — see
`ut-docs/reference/contracts/fiscal-sign-ask.md`) — so core now sees a
fiscal signer installed. Before this it declared only `sale.completed`, and
core saw **zero fiscal signers** however well the fiskaly call itself
worked.

`sale.completed` has been **removed**, along with this plugin's own
`unsigned_queue`. Core owns the entire failure surface properly now
(journal marker, receipt outage notice, operator alert, background re-ask
with `retry: true`), so a second private retry loop was redundant — and an
independent review proved it was actively harmful: with both hooks live,
every *successfully signed* sale was immediately re-signed against the same
`tx_id`, which fiskaly rejects because that transaction is already
`FINISHED`. Each such sale then got a false "fiskaly was NOT reached" log,
had its `tse_result:*` audit record overwritten from signed to failed, and
added a permanent queue entry.

**Two sales that will NOT sign today, deliberately.** A sale carrying a
**tip**, or a **sale-level discount / service charge**, produces a receipt
whose two halves cannot be reconciled (fiskaly renders `standard_v1` into
DSFinV-K's `Beleg^<gross per VAT rate>^<per payment type>`, and those must
be equal — a tip has no VAT bucket, and a whole-bill discount moves the
total but not the per-line breakdown, which the payload never breaks out).
The plugin refuses to sign such a receipt and answers `unreachable`, so the
sale completes, is marked unsigned, prints the outage notice and is retried
— rather than writing an **irreversible** TSE record that misstates the
sale. Tip treatment is an open accountant question (**ut-docs#833**); the
missing payload fields are **ut-docs#834**.

**Ordinary sales — including tax-inclusive German pricing — do sign.** The
payload carries no `tax_inclusive` flag even though core fills
`vat_breakdown` differently for each convention (inclusive puts the *gross*
in `net`; exclusive puts the true net there and adds `tax` on top). The
plugin deduces which by testing which reading reconciles with `total`.
Reading inclusive pricing as exclusive would double-count the tax and, with
the balance check above, refuse to sign every real German sale.

**`handleTaxRateAsk` (dine-in/takeaway VAT switching) is real, and is the
one piece of this plugin verified against a real wazero-compiled run** — no
fiskaly dependency, so nothing about it needed a sandbox account. Until
ut-docs#1013, that run was ad-hoc and uncommitted (the harness itself was
never kept, only its result recorded in the status table above) — `go test
./src/wasmrun/...` now COMMITS two cases,
`TestTaxRateAsk_TakeawayOverrideAnswersReducedRate` and
`TestTaxRateAsk_DineInAnswersNothing`, driving the real compiled
`bin/plugin.wasm` through wazero exactly like the `fiscal.sign.ask`
coverage elsewhere in that package. The rule itself — the
takeaway_rate_overrides lookup that produces Germany's (product tax class
x consumption mode) matrix (ut-docs#1013) — is extracted into `src/taxrate`
so it also has direct host-level unit coverage of the full matrix,
independent of the wasm-level check.

## Configure (plugin settings)

- `fiskaly_api_key` / `fiskaly_api_secret` — the merchant's fiskaly API
  credentials (fiskaly dashboard → API keys).
- `fiskaly_tss_id` — the TSS (Technical Security System) id created in the
  merchant's fiskaly account. **Not created by this plugin** — TSS creation
  is a one-time setup step, done via the fiskaly dashboard/API before this
  plugin is installed.
- `fiskaly_client_id` (register-scoped) — the fiskaly "client" id
  representing this specific till/register (fiskaly's model: one client per
  point of sale).
- `fiskaly_cash_register_id` — the DSFinV-K API's cash register id (a
  separate concept from the SIGN DE `client_id`, per fiskaly's docs — some
  fiskaly accounts share one id across both APIs, some don't; verify
  against the merchant's account).
- `dsfinvk_export_format` — `zip` (default) or `tar`.
- `takeaway_rate_overrides` — JSON object, tax_code_id → basis points, e.g.
  `{"tax-drink-de": 700}` for a drinks tax code that should be 7% on
  takeaway. Edited as raw JSON — there is no dedicated form for this yet
  (see "What a human still needs to do" below).
- `datev_berater_nr` / `datev_mandant_nr` — the DATEV Beraternummer/
  Mandantennummer for the merchant's accountant (from the accountant, not
  guessable). Required — export refuses without them.
- `datev_sachkontenlaenge` — G/L account number length, digits (default
  `"4"`, the SKR03/04-standard length).
- `datev_wj_beginn` — fiscal year start, `MMDD` (default `"0101"`,
  calendar-year; override for a non-calendar fiscal year).
- `datev_konto_kasse` — the Konto debited for every booking row (the
  till's cash/bank collector account). Required — no default, export
  refuses without it.
- `datev_erloeskonten` — JSON object, tax rate in basis points → Gegenkonto
  (revenue account), e.g. `{"1900": "8400", "700": "8300"}`. Required to
  cover every tax rate that appears in the exported period — export refuses
  and names the missing rate(s) rather than guessing an account number.
- `datev_bu_schluessel` — optional JSON object, tax rate in basis points →
  BU-Schlüssel override, only needed if `datev_erloeskonten`'s accounts are
  NOT already "Automatikkonten" (accounts DATEV auto-splits VAT for) — most
  SKR03/04 standard revenue accounts (e.g. `8400`/`8300`) already are, so
  this is usually left empty.

## What a human still needs to do before this could ever go live

1. **Get a real fiskaly account** (sandbox first) and run every endpoint in
   `src/main.go` against it — resolve every `NEEDS SANDBOX VERIFICATION`
   comment, fix whatever doesn't match reality.
2. **Legal/tax-advisor review** of the whole approach, not just the export
   format — KassenSichV compliance is a legal question this plugin cannot
   answer by existing.
3. **Resolve the offline-first tension** (Known gaps #1) — decide, with
   real TSE-vendor/legal input, whether cloud-TSE-with-retry is acceptable
   or whether a local/hardware TSE path is required, per ADR-0025's
   explicitly-unresolved open question.
4. **Build the cash-point-closing aggregation** (Known gaps #3) so a
   triggered DSFinV-K export is actually complete, and implement polling +
   a real downloadable result once fiskaly's async export finishes (Known
   gaps #2) — today's `export.requested.ask` response is trigger-only.
5. ~~**Test the TSE/DSFinV-K paths against the real wazero host runtime**~~
   — **done for TSE signing** (2026-08-19, ut-docs#818): `src/wasmrun` is a
   committed wazero suite over the real compiled wasm. **Still open for
   DSFinV-K**, which remains entirely unexercised.
6. **Get an accountant's ruling on tips and whole-bill discounts**
   (**ut-docs#833**) and then represent them on the signed receipt. Until
   that lands, those sales deliberately do not sign — see above. This is
   the single biggest functional gap in TSE signing today.
7. **Verify the remaining VAT buckets against the sandbox.** Only `NORMAL`
   has been exercised live; `REDUCED_1` / `NULL` / `SPECIAL_RATE_1` are
   unit-tested for mapping but never round-tripped through fiskaly.
8. **Build a settings UI for `takeaway_rate_overrides`** — today it's a raw
   JSON text field, no form to pick a tax code from the shop's actual
   catalog and set its takeaway rate. A real merchant can't use this
   feature without either that UI or editing plugin settings by hand.
9. **Get the DATEV export's chart-of-accounts mapping confirmed by the
   merchant's actual accountant** before relying on it for a real filing —
   `datev_konto_kasse`/`datev_erloeskonten` are configuration this plugin
   never guesses, but a wrong number entered by whoever configures it would
   still mis-book real accounting records; a real accountant should
   confirm the values, not just that the fields are filled in.
10. **Confirm the DATEV format-version numbers and Soll/Haben booking
   convention** (`src/datev/datev.go`'s package doc comment) against
   DATEV's current published "Formatbeschreibung: Buchungsstapel" and a
   real DATEV import test — this plugin's file is structurally grounded in
   a real reference export, not verified end-to-end.
11. **Decide whether DATEV export should split Konto by payment method**
   (Known gap #5) instead of posting every sale to one Kasse/Bank account.
12. **Push sale-level discounts down into `sale_lines`** (Known gap #7) so a
    discounted sale's tax-line sum reconciles with its total — until then,
    any period containing a discounted sale can't be DATEV-exported at all.

## Build

```sh
bash scripts/build.sh   # -> bin/plugin.wasm (GOOS=wasip1 GOARCH=wasm)
```

`go build ./...` from the repo root now builds the host-side packages
(`src/datev`, `src/fiskalyparse`, `src/fiscalsign`, `src/taxrate`, and the
`src/wasmrun` test package) and silently skips `src/main.go`, which is gated
`//go:build wasip1`. It therefore does NOT prove the plugin itself compiles
— the real build check is still `scripts/build.sh`, which cross-compiles for
the actual target. (Before 2026-08-19 this repo had only `src/datev` outside
the build tag and the command printed `go: warning: "./..." matched no
packages`; that is no longer what you will see.)
