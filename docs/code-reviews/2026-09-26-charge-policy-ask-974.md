# Code review — answer `charge.policy.ask` with Germany's service-charge/tip policy

**Date:** 2026-09-26
**Card:** universaltill/ut-docs#974
**Branch:** `feat/974-charge-policy-ask`
**Complexity:** medium
**Dev:** Opus 5.5, inline (cloud routine lane `:54`)
**Reviewer:** Fable, one independent subagent with a fresh context (read-only; ran the full gate itself)

## What shipped

- `src/chargepolicy/` (new, host-testable): `DE()` returns ut-docs#961's
  German row: `service_charge_permitted: true`, `service_charge_default_rate_bp: 0`
  (the merchant opts in), `service_charge_tax_basis_bp: 0` (apportion at the
  lines' own §12 UStG rates), `tip_default_recipient: "employee"`,
  `fiscal_business_case: "TrinkgeldAN"` (the DSFinV-K GV_TYP `fiscalsign`
  already signs employee tips under). No `omitempty`, no `charges`.
- `src/main.go`: dispatches `charge.policy.ask` to that constant. No HTTP,
  no settings, no storage.
- `manifest.json`: new `hooks[]` entry, version `0.8.0` → `0.9.0`.
- Tests: exact-JSON and explicit-`permitted` unit tests; a wasmrun test
  that decodes the compiled plugin's stdout against a mirror of core's
  `chargePolicyAskResponse` with `DisallowUnknownFields`; a test that the
  manifest declares the hook.
- README (feature bullet + status row) and CLAUDE.md (code layout).

## Design decisions

- **TrinkgeldAG (a tip the business keeps).** Nothing beyond the default:
  it is a per-payment choice core already carries (`PaymentInput.TipRecipient`),
  and signing already handles it via `tip_business_vat_treatment`.
- **No behaviour change to sales.** Core's `BuildCharges` gives this answer
  the same charge list as the no-plugin path, and the tip default only flips
  on `business`. The answer's value is that core now holds DE's policy as an
  answer, plus the DSFinV-K business case.

## Verification beyond unit tests

- Mutation: renaming the `main.go` case made `TestChargePolicyAsk_AnswersGermanPolicy`
  fail (empty stdout), and removing the manifest hook made
  `TestManifestSubscribesChargePolicyAsk` fail. Both pass once restored.
- Cross-repo, one-off and not committed: the plugin's literal stdout went
  through universal-till's `validateChargePolicy`, then `pos.BuildCharges`
  compared with the unanswered path for merchant rates 0/10%/12.5% and
  bases 0–123456 minor units. The lists were identical.
- The reviewer confirmed dispatch in core: `manifest.go` stores every
  `hooks[]` row, `wasm_runtime.go` subscribes `.ask` events as blocking,
  and `ipc.go` gates `Ask` on `events:receive` only (already declared).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | README row presented an uncommitted one-off check as verification | **Fixed:** reworded to "by inspection + one-off run", with an explicit note that no committed test pins the equivalence |
| 2 | minor | Several country plugins → first answer wins in plugin-id order, and nothing logs the collision | **Fixed (docs):** one sentence in the README bullet. It is core's ADR-0061 design, not this PR's defect |
| 3 | nit | "Lawful but rare": PAngV requires a mandatory charge to be inside displayed prices | Accepted: the answer is still "permitted", matching ADR-0061/#961 |
| 4 | nit | CLAUDE.md `go test` list omitted `src/chargepolicy` | **Fixed** |
| 5 | nit | `chargepolicy` imports `fiscalsign` for one constant | Accepted: both packages are host-independent, and sharing the constant keeps them in agreement |
| 6 | process | Review record missing | This file |

## Not verified

- The card's second criterion (a real DSFinV-K export showing the mapping)
  can't be met yet: this plugin's DSFinV-K export is still the unverified
  skeleton, and core doesn't persist `fiscal_business_case`. The README row
  says so.
- No real installed-plugin run through a till UI. No UI surfaces the
  default rate today.

## Verdict

Safe to merge. Gate: gofmt, wasip1 vet, `go test ./...`, build, validate,
i18n guard, version-bump self-test (all green).
