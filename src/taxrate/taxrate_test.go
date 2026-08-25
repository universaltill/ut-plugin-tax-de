package taxrate

import (
	"encoding/json"
	"testing"
)

// constFn wraps a fixed string as the func() string Resolve's overridesJSON
// parameter expects, for tests that don't care about lazy evaluation.
func constFn(s string) func() string {
	return func() string { return s }
}

// TestResolve_ProductClassMatrix reproduces the reference matrix from
// ut-docs#1013, one full day of real German trading data: VAT is a
// (product tax class x consumption mode) matrix, not a plain takeaway
// switch. overrides mirrors what a real shop would configure: a genuine
// reduction for milk-based drinks, an EXPLICIT no-op entry for pure
// coffee (proving a merchant -- or an export/import round-trip -- who
// writes one anyway doesn't churn or get silently dropped), and no entry
// at all for food (already 7% in both modes since 2026-01-01, so there is
// nothing to configure) or the multi-purpose voucher (0% in both modes,
// no tax code to switch).
func TestResolve_ProductClassMatrix(t *testing.T) {
	const overrides = `{"tax-milk-drink":700,"tax-coffee":1900}`

	cases := []struct {
		name string
		// taxCodeID + ownRateBP describe the line as core already has it
		// (ownRateBP is what core falls back to when Resolve says ok=false
		// -- the pre-tax-plugin default, unaffected by this plugin).
		taxCodeID string
		ownRateBP int
		orderType string
		// wantBP/wantOK are Resolve's own return values.
		wantBP int
		wantOK bool
		// wantEffectiveBP is the rate actually charged once core's
		// fallback is folded in -- the number the reference matrix states.
		wantEffectiveBP int
	}{
		{"food, Im Haus", "tax-food", 700, "", 0, false, 700},
		{"food, Ausser Haus (no override needed -- already 7%)", "tax-food", 700, OrderTypeTakeaway, 0, false, 700},

		{"milk-based drink, Im Haus", "tax-milk-drink", 1900, "", 0, false, 1900},
		{"milk-based drink, Ausser Haus (real switch, 19% -> 7%)", "tax-milk-drink", 1900, OrderTypeTakeaway, 700, true, 700},

		{"pure coffee, Im Haus", "tax-coffee", 1900, "", 0, false, 1900},
		{"pure coffee, Ausser Haus (explicit no-op override, 19% -> 19%)", "tax-coffee", 1900, OrderTypeTakeaway, 1900, true, 1900},

		{"multi-purpose voucher, Im Haus", "tax-voucher", 0, "", 0, false, 0},
		{"multi-purpose voucher, Ausser Haus", "tax-voucher", 0, OrderTypeTakeaway, 0, false, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bp, ok, err := Resolve(c.orderType, c.taxCodeID, constFn(overrides))
			if err != nil {
				t.Fatalf("Resolve error: %v", err)
			}
			if bp != c.wantBP || ok != c.wantOK {
				t.Fatalf("Resolve(%q, %q) = (%d, %v), want (%d, %v)", c.orderType, c.taxCodeID, bp, ok, c.wantBP, c.wantOK)
			}
			effective := c.ownRateBP
			if ok {
				effective = bp
			}
			if effective != c.wantEffectiveBP {
				t.Fatalf("effective rate for %s = %d, want %d", c.name, effective, c.wantEffectiveBP)
			}
		})
	}
}

// TestResolve_NoOpOverrideStableAcrossReserialization is the regression
// case ut-docs#1013 calls out explicitly: an override whose value equals
// the line's own rate (pure coffee, 19% -> 19%) must answer identically no
// matter how the surrounding JSON was (re)serialized -- e.g. after an
// export/import round-trip that re-marshals the map in a different key
// order or whitespace. This is the same shape as the equal-pair bug fixed
// in ut-docs#536 (a hand-created 19/19 tax code churning to a different
// code on round-trip), generalized from tax codes to this setting.
//
// This does a REAL round-trip -- parse each variant, re-marshal it with
// encoding/json (not just eyeball the input strings), and Resolve against
// the re-marshaled bytes -- so it actually exercises "however the JSON was
// last serialized," not just whitespace/key-order variants of a literal
// that json.Unmarshal was always going to treat identically anyway.
func TestResolve_NoOpOverrideStableAcrossReserialization(t *testing.T) {
	variants := []string{
		`{"tax-coffee":1900}`,
		`{ "tax-coffee" : 1900 }`,
		`{"tax-other":1,"tax-coffee":1900}`,
		"\n\t" + `{"tax-coffee":1900}` + "\n",
	}
	for _, overrides := range variants {
		// Parse -> re-marshal -> re-parse: the actual round-trip, not just
		// a differently-formatted literal.
		parsed, err := ParseOverrides(overrides)
		if err != nil {
			t.Fatalf("ParseOverrides(%q) error: %v", overrides, err)
		}
		remarshaled, err := json.Marshal(parsed)
		if err != nil {
			t.Fatalf("re-marshal %q: %v", overrides, err)
		}

		bp, ok, err := Resolve(OrderTypeTakeaway, "tax-coffee", constFn(string(remarshaled)))
		if err != nil {
			t.Fatalf("Resolve error for %q -> %q: %v", overrides, remarshaled, err)
		}
		if !ok || bp != 1900 {
			t.Fatalf("Resolve(%q -> %q) = (%d, %v), want (1900, true) -- no-op override must not churn or drop across a re-marshal", overrides, remarshaled, bp, ok)
		}
	}
}

// TestResolve_DineInNeverConsultsOverrides proves the dine-in short-circuit
// by making the overridesJSON func PANIC if it is ever called -- not just
// asserting the observable result is as if it hadn't been. This is the
// exact ordering the original inline handler relied on (checking order
// type BEFORE ever touching the settings JSON, so malformed overrides
// never surfaces as an error on the dine-in path) and the exact ordering
// Resolve's signature -- overridesJSON as a func, not a plain string --
// exists to make cheap to guarantee: main.go's real caller wraps a host
// settings_get round-trip in that func, and dine-in is the common case
// (most receipts), so this must never fire on that path.
func TestResolve_DineInNeverConsultsOverrides(t *testing.T) {
	tripwire := func() string {
		t.Fatal("overridesJSON() was called on the dine-in path -- Resolve must check orderType first")
		return ""
	}
	bp, ok, err := Resolve("", "tax-coffee", tripwire)
	if err != nil || ok || bp != 0 {
		t.Fatalf("Resolve(dine-in, ...) = (%d, %v, %v), want (0, false, nil)", bp, ok, err)
	}
}

func TestResolve_OverridePresentButZeroOrNegativeTreatedAsUnset(t *testing.T) {
	for _, overrides := range []string{`{"tax-food":0}`, `{"tax-food":-100}`} {
		bp, ok, err := Resolve(OrderTypeTakeaway, "tax-food", constFn(overrides))
		if err != nil {
			t.Fatalf("Resolve error: %v", err)
		}
		if ok || bp != 0 {
			t.Fatalf("Resolve(%q) = (%d, %v), want (0, false) -- non-positive override must fall back like an absent one", overrides, bp, ok)
		}
	}
}

func TestResolve_InvalidOverridesJSONOnTakeawayReturnsError(t *testing.T) {
	_, _, err := Resolve(OrderTypeTakeaway, "tax-food", constFn(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed overrides JSON on the takeaway path")
	}
}

func TestParseOverrides_Empty(t *testing.T) {
	// "null" is valid JSON that unmarshals a map to nil -- included here,
	// not just alongside the other empty-ish inputs, because ParseOverrides
	// promises callers a non-nil map even in that case (ut-docs#1013
	// review finding): a caller that indexes or ranges the result without
	// a nil check must not panic just because the setting happened to
	// contain the literal null rather than being absent.
	for _, raw := range []string{"", "   ", "\n\t", "null"} {
		m, err := ParseOverrides(raw)
		if err != nil {
			t.Fatalf("ParseOverrides(%q) error: %v", raw, err)
		}
		if m == nil {
			t.Fatalf("ParseOverrides(%q) returned a nil map, want non-nil empty", raw)
		}
		if len(m) != 0 {
			t.Fatalf("ParseOverrides(%q) = %v, want empty map", raw, m)
		}
	}
}

func TestParseOverrides_Invalid(t *testing.T) {
	if _, err := ParseOverrides(`{oops`); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}
