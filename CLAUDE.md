# ut-plugin-tax-de — notes

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing Germany's KassenSichV
fiscal requirements: TSE signing via fiskaly's Cloud-TSE ("SIGN DE") API
(`canonical_type: tax`, hooks `sale.completed`) + DSFinV-K export via
fiskaly's DSFinV-K API (`canonical_type: tax`, second `entries[]` item typed
`export`, per ADR-0025's guidance to map both onto ADR-0002's existing
taxonomy in one manifest rather than invent a new type). First fiscal
compliance plugin built, per `ut-docs` ADR-0025 ("Country-specific tax rates
and fiscal compliance") and its "Real-world precedent" section: SumUp itself
delegates TSE signing to fiskaly rather than building it in-house, so this
plugin follows the same shape rather than inventing one.

## Status: skeleton, not verified against a live fiskaly account

No fiskaly credentials were available while building this — every endpoint
in `src/main.go` is grounded in fiskaly's **public** documentation
(developer.fiskaly.com, kassensichv.net, kassensichv.io) but flagged
`NEEDS SANDBOX VERIFICATION` in its doc comment. README.md has the full
honest-status table ("researched, not tested" vs. "confirmed") — read it
before touching this code, and keep both README.md and this file in sync
with reality as verification happens (don't let a `NEEDS SANDBOX
VERIFICATION` comment get resolved in code without also updating the README
row it corresponds to).

## The offline-first tension (do not "fix" this without a real decision)

`sale.completed` is non-blocking and fires **after** the sale has already
completed (confirmed against `universal-till/internal/plugins/ipc.go` /
`wasm_runtime.go`: non-blocking dispatch discards the handler's return
value — errors are logged, never retried, never surfaced to the operator).
This plugin therefore has **no architectural way to block or reverse a sale
if fiskaly is unreachable**, which is exactly the open question ADR-0025
flags and explicitly does NOT resolve ("needs real legal/TSE-vendor
confirmation, not assumed here"). The one hard rule this code follows
instead: **never fabricate a signature**. A failed sign attempt is logged
loudly and queued for retry (`unsigned_queue` in plugin storage, same
bounded-queue pattern as `ut-plugin-integration-webhook`'s delivery queue) —
if you touch `signTransaction` or `handleSaleCompleted`, preserve that
invariant. Don't add a code path that marks a sale "signed" without a real
2xx response containing a signature.

## `export` canonical type has no host dispatcher yet

Confirmed against `ut-docs/reference/plugin-manifest.md` and
`universal-till/internal/plugins/types.go`: the plugin engine dispatches
`page`/`button`/`theme` natively; `report`/`export` are registered/listed on
the plugin info card only. `exportDSFinVK` in `src/main.go` is real code
(triggered by a placeholder event, `tax.de.dsfinvk.export.requested`, that
nothing in the host currently publishes), not a stub — but nothing in the
till UI can invoke it yet. Also incomplete on fiskaly's side: a real
DSFinV-K export needs `cashPointClosing` aggregates built from every signed
transaction first, which this plugin does not build (see README "Known
gaps"). Don't remove the doc comments explaining this — they're load-bearing
context for whoever wires the dispatcher up.

## Code layout

- `src/main.go` — single WASI command, dispatches on the event JSON's
  `type` field (`sale.completed` → `handleSaleCompleted`; `tax.rate.ask` →
  `handleTaxRateAsk`, the dine-in/takeaway VAT switch, verified against a
  real wazero run — see README's status table; the placeholder
  export-request type → `exportDSFinVK`).
- Host functions imported from module `ut` (see docs repo
  `reference/plugin-host-functions.md`): `log_write`, `settings_get`,
  `storage_get`, `storage_set`, `http_request`. Buffer ABI: data calls
  return the FULL length, retry with a bigger buffer if it exceeds cap;
  negatives are host errors (-1 not found, -2 denied, -3 internal,
  -4 invalid) — same convention as every sibling plugin.
- `net:kassensichv.io` permission gates the `http_request` host function
  (see `universal-till/internal/plugins/wasm_hostfns.go`'s
  `hostHTTPRequest`) — this is the first-party host function ADR-0025
  flagged as an open follow-up ("whether TSE signing needs a first-party
  host function... a real question for whoever builds ut-plugin-tax-de");
  it already existed by the time this repo was built (the SumUp plugin uses
  the same mechanism with `net:api.sumup.com`), so no new host-side work was
  needed.
- `countries: ["DE"]` in `manifest.json` — the new `PluginListing` field
  ADR-0025 decision 3 introduces. **Not yet enforced by the marketplace
  schema** (`ut-cloud/pkg/manifest` has no `Countries` field as of this
  writing) — set here as forward-looking metadata per the task that created
  this repo; do not attempt to modify the marketplace repo from here, that's
  tracked separately.

## Before committing

- `bash scripts/build.sh` (the real build check — cross-compiles
  `GOOS=wasip1 GOARCH=wasm`; plain `go build ./...` matches no packages by
  design, same as every sibling plugin, because `src/main.go` is gated
  `//go:build wasip1`).
- `bash scripts/validate.sh` (manifest shape: `canonical_type: tax`,
  `countries: ["DE"]`, one `tax`-type and one `export`-type entry).
- Keep README.md's status table and this file's caveats current — this repo
  exists specifically to be honest about what's real vs. placeholder; don't
  let code changes silently upgrade a claim without re-verifying it.
- Standards & decisions live in the docs repo (`adr/`, ADR-0007
  document-first). Behaviour changes update the README in the same session.
