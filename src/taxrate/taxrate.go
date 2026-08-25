// Package taxrate holds the pure, host-independent half of this plugin's
// "tax.rate.ask" answerer: Germany's §12 UStG dine-in/takeaway VAT switch.
//
// It exists as its own package for the same reason src/fiscalsign does:
// src/main.go is compiled only for GOOS=wasip1 (it declares
// //go:wasmimport host functions), so it cannot be unit-tested on the
// host at all. Everything in here is ordinary Go with no host dependency,
// so it runs under a normal `go test` (ut-docs#1013).
package taxrate

import (
	"encoding/json"
	"strings"
)

// OrderTypeTakeaway mirrors universal-till's internal/pos.OrderTypeTakeaway
// wire value. Duplicated rather than imported: this plugin is a separate
// Go module compiled to wasip1/wasm and has no dependency on core.
const OrderTypeTakeaway = "takeaway"

// Resolve answers the tax.rate.ask hook for one basket line under the
// sale's current order type.
//
// orderType is the sale's order type as sent in the ask payload ("" for
// dine-in/standard, or OrderTypeTakeaway). taxCodeID identifies the
// line's tax code. overridesJSON is called to fetch the merchant-
// configured takeaway_rate_overrides plugin setting verbatim -- a JSON
// object mapping tax_code_id -> takeaway basis points, possibly ""
// meaning nothing configured yet -- and is deliberately a FUNC, not a
// plain string: on the dine-in path this plugin has no opinion regardless
// of what the setting holds, and must never call it at all. In main.go
// that call is a real host round-trip (settings_get -> a SQLite read via
// the plugin engine's host functions on every single ask), so evaluating
// it eagerly on every dine-in line -- the common case, most receipts --
// would be a real, avoidable cost, not just a style choice. This mirrors
// the original inline handler's own order-type-first short-circuit
// exactly (see TestResolve_DineInNeverConsultsOverrides).
//
// ok=false means this plugin has no opinion on the line -- core falls
// back to the line's own configured rate unchanged (exactly the pre-tax-
// plugin default). That is the correct answer for:
//
//   - dine-in (orderType != OrderTypeTakeaway): §12 UStG's switch only
//     ever pulls a rate DOWN for takeaway, it never touches dine-in --
//     and overridesJSON is never even called for this case.
//   - a tax code with no configured override -- e.g. food, which German
//     law already taxes at 7% in both modes since 2026-01-01, so there is
//     nothing to switch. "No entry" and "an entry present but <= 0" mean
//     the same thing here: the setting is a raw JSON blob (core's typed
//     editor, plugin_settings_page.go's buildTaxOverrideRows/
//     parseTaxOverrides, deletes the key on clear and rejects <= 0
//     outright -- but this function has no way to know the JSON came from
//     that editor rather than a hand-edit, an older core version, or a
//     future tool), so both shapes must resolve identically rather than
//     trusting the writer to have normalized one away.
//
// A configured override whose basis points happen to equal the line's
// own dine-in rate (the "no-op override" case -- e.g. pure coffee, taxed
// at 19% in both modes, with an explicit {"tax-coffee":1900} entry) still
// returns ok=true: it is a real, deliberately-configured entry, not an
// absent one, and must answer identically on every call regardless of how
// the JSON was last (re)serialized -- an export/import round-trip must
// never silently drop it or churn its value (ut-docs#536's failure mode,
// generalized from tax codes to this setting).
//
// err is non-nil only when overridesJSON() is non-empty and not valid
// JSON -- the caller decides what to do (main.go logs and answers "no
// opinion", same as if the setting were unset).
func Resolve(orderType, taxCodeID string, overridesJSON func() string) (bp int, ok bool, err error) {
	if orderType != OrderTypeTakeaway {
		return 0, false, nil
	}
	overrides, err := ParseOverrides(overridesJSON())
	if err != nil {
		return 0, false, err
	}
	rate, present := overrides[taxCodeID]
	if !present || rate <= 0 {
		return 0, false, nil
	}
	return rate, true, nil
}

// ParseOverrides parses the takeaway_rate_overrides plugin setting (a JSON
// object, tax_code_id -> takeaway basis points). An empty/whitespace-only
// string is a valid "nothing configured yet" state, not an error, and
// always returns a non-nil map (never JSON `null`'s zero value) so a
// caller can range over or index the result without a nil check.
func ParseOverrides(raw string) (map[string]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]int{}, nil
	}
	overrides := map[string]int{}
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		return nil, err
	}
	if overrides == nil { // valid JSON `null` unmarshals to a nil map
		overrides = map[string]int{}
	}
	return overrides, nil
}
