package paragraph146a

import (
	"strings"
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }

func TestBuild_EmptyRowsRefuses(t *testing.T) {
	if _, err := Build(nil, time.Now()); err == nil {
		t.Fatal("expected an error for an empty register, got nil")
	}
}

func TestBuild_SingleLocationSingleEntry(t *testing.T) {
	rows := []Row{{
		RegisterName: "Front Till", LocationName: "Café Hauptstraße",
		LocationStreet: "Hauptstraße 1", LocationPostcode: "10115", LocationCity: "Berlin",
		EasType: "Tablet-/App-Kassen-Systeme", EasSoftware: "Universal Till 1.0", EasSerial: "eas-1",
		TSESerial: "tse-serial-hex", TSECertificationID: "BSI-K-TR-1234", TSEType: "Cloud",
		AcquiredOn: "2026-01-15", CommissionedOn: strPtr("2026-01-16"),
	}}
	now := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	res, err := Build(rows, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out := string(res.Content)

	for _, want := range []string{
		"Betriebsstätte: Café Hauptstraße",
		"Anschrift: Hauptstraße 1, 10115 Berlin",
		"Gesamtanzahl der genutzten eAs: 1",
		"Art des eAs: Tablet-/App-Kassen-Systeme",
		"Seriennummer des eAs: eas-1",
		"Seriennummer der TSE: tse-serial-hex",
		"BSI-Zertifizierungs-ID: BSI-K-TR-1234",
		"Art/Bauform der TSE: Cloud",
		"Anschaffungs-Datum des eAs: 2026-01-15",
		"Inbetriebnahme-Datum: 2026-01-16",
		"Außerbetriebnahme-Datum: —",
		"Steuernummer: Vom Betreiber zu ermitteln",
		"Erstellt am: 2026-09-19",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
	if res.Filename != "paragraph146a-meldeuebersicht-2026-09-19.txt" {
		t.Fatalf("unexpected filename: %s", res.Filename)
	}
}

// The register's own AO gap semantics: a field the till genuinely doesn't
// know (blank string) or a till never commissioned (nil pointer) render as
// clear placeholders, never blank lines or a zero value.
func TestBuild_MissingFieldsRenderAsPlaceholders(t *testing.T) {
	rows := []Row{{
		RegisterName: "Front Till", LocationName: "Café Hauptstraße",
		AcquiredOn: "2026-01-15", // eas_type/eas_software/eas_serial/tse_* left blank
	}}
	out := string(mustBuild(t, rows).Content)
	for _, want := range []string{
		"Art des eAs: Vom Betreiber zu ermitteln",
		"Seriennummer der TSE: Vom Betreiber zu ermitteln",
		"Inbetriebnahme-Datum: noch nicht in Betrieb genommen",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected placeholder %q, got:\n%s", want, out)
		}
	}
}

// Gross method: several tills at ONE location produce ONE block, not one
// per till — the exact criterion the incumbent vendor's own template
// couldn't fill (ut-docs#665 research finding 2).
func TestBuild_GrossMethodOneBlockPerLocation(t *testing.T) {
	rows := []Row{
		{RegisterName: "Front Till", LocationID: "loc-1", LocationName: "Café Hauptstraße", AcquiredOn: "2026-01-10", CommissionedOn: strPtr("2026-01-11")},
		{RegisterName: "Back Till", LocationID: "loc-1", LocationName: "Café Hauptstraße", AcquiredOn: "2026-02-01", CommissionedOn: strPtr("2026-02-02")},
	}
	out := string(mustBuild(t, rows).Content)
	if n := strings.Count(out, "Betriebsstätte: Café Hauptstraße"); n != 1 {
		t.Fatalf("expected exactly one location block, got %d in:\n%s", n, out)
	}
	if n := strings.Count(out, "eAs "); n != 2 {
		t.Fatalf("expected two eAs entries under the one block, got %d in:\n%s", n, out)
	}
	if !strings.Contains(out, "Gesamtanzahl der genutzten eAs: 2") {
		t.Fatalf("expected count of 2, got:\n%s", out)
	}
}

// A decommissioned till still appears (the AO record must stay visible,
// same rule the core register itself follows) but is excluded from the
// "genutzten" (in-use) count.
func TestBuild_DecommissionedEntryStillListedNotCountedInUse(t *testing.T) {
	rows := []Row{
		{RegisterName: "Front Till", LocationID: "loc-1", LocationName: "Café Hauptstraße",
			AcquiredOn: "2026-01-10", EasSerial: "eas-active", CommissionedOn: strPtr("2026-01-11")},
		{RegisterName: "Old Till", LocationID: "loc-1", LocationName: "Café Hauptstraße",
			AcquiredOn: "2025-06-01", EasSerial: "eas-retired", CommissionedOn: strPtr("2025-06-02"), DecommissionedOn: strPtr("2026-03-01")},
	}
	out := string(mustBuild(t, rows).Content)
	if !strings.Contains(out, "Gesamtanzahl der genutzten eAs: 1") {
		t.Fatalf("expected in-use count of 1 (decommissioned till excluded), got:\n%s", out)
	}
	if !strings.Contains(out, "eas-retired") {
		t.Fatalf("expected decommissioned till to still be listed, got:\n%s", out)
	}
	if !strings.Contains(out, "Außerbetriebnahme-Datum: 2026-03-01") {
		t.Fatalf("expected decommissioned date rendered, got:\n%s", out)
	}
}

// Independent-review finding S3: a till not yet commissioned isn't
// "genutzt" (in use) either — the in-use count must exclude it just as it
// excludes a decommissioned one, even though the till is fully listed with
// its own "noch nicht in Betrieb genommen" placeholder.
func TestBuild_UncommissionedEntryStillListedNotCountedInUse(t *testing.T) {
	rows := []Row{
		{RegisterName: "Front Till", LocationID: "loc-1", LocationName: "Café Hauptstraße",
			AcquiredOn: "2026-01-10", EasSerial: "eas-active", CommissionedOn: strPtr("2026-01-11")},
		{RegisterName: "New Till", LocationID: "loc-1", LocationName: "Café Hauptstraße",
			AcquiredOn: "2026-09-01", EasSerial: "eas-pending"}, // no CommissionedOn yet
	}
	out := string(mustBuild(t, rows).Content)
	if !strings.Contains(out, "Gesamtanzahl der genutzten eAs: 1") {
		t.Fatalf("expected in-use count of 1 (uncommissioned till excluded), got:\n%s", out)
	}
	if !strings.Contains(out, "eas-pending") {
		t.Fatalf("expected uncommissioned till to still be listed, got:\n%s", out)
	}
	if !strings.Contains(out, "Inbetriebnahme-Datum: noch nicht in Betrieb genommen") {
		t.Fatalf("expected the uncommissioned placeholder, got:\n%s", out)
	}
}

// Independent-review finding S5: registers with no assigned location all
// merge into one group (there's no location identity to split them on), so
// more than one row in that group gets an explicit warning rather than
// silently reading as one real Betriebsstätte.
func TestBuild_MultipleNoLocationEntriesGetSplitWarning(t *testing.T) {
	rows := []Row{
		{RegisterName: "Kiosk A", AcquiredOn: "2026-01-01"},
		{RegisterName: "Kiosk B", AcquiredOn: "2026-01-01"},
	}
	out := string(mustBuild(t, rows).Content)
	if !strings.Contains(out, "Hinweis: Diese Kassen sind KEINER Betriebsstätte zugeordnet") {
		t.Fatalf("expected a split warning for multiple no-location entries, got:\n%s", out)
	}
}

// A single no-location entry needs no split warning — there's nothing to
// disambiguate.
func TestBuild_SingleNoLocationEntryGetsNoSplitWarning(t *testing.T) {
	rows := []Row{{RegisterName: "Kiosk A", AcquiredOn: "2026-01-01"}}
	out := string(mustBuild(t, rows).Content)
	if strings.Contains(out, "Hinweis: Diese Kassen sind KEINER Betriebsstätte zugeordnet") {
		t.Fatalf("expected no split warning for a single no-location entry, got:\n%s", out)
	}
}

// Multiple locations, and an entry with no location assigned at all, each
// get their own block — the no-location group sorts last.
func TestBuild_MultipleLocationsAndNoLocationGroup(t *testing.T) {
	rows := []Row{
		{RegisterName: "Kiosk Till", LocationID: "", LocationName: "", AcquiredOn: "2026-01-01"},
		{RegisterName: "West Till", LocationID: "loc-west", LocationName: "West Store", AcquiredOn: "2026-01-01"},
		{RegisterName: "East Till", LocationID: "loc-east", LocationName: "East Store", AcquiredOn: "2026-01-01"},
	}
	out := mustBuild(t, rows).Content
	east := strings.Index(string(out), "East Store")
	west := strings.Index(string(out), "West Store")
	none := strings.Index(string(out), "(keiner Betriebsstätte zugeordnet)")
	if east == -1 || west == -1 || none == -1 {
		t.Fatalf("expected all three groups present, got:\n%s", out)
	}
	if east >= west || west >= none {
		t.Fatalf("expected alphabetical location order with no-location group last, got East=%d West=%d None=%d\n%s", east, west, none, out)
	}
}

// The output must never contain anything resembling a TSE PIN/PUK field —
// the incumbent vendor's own template was found to leak both in plain text
// (ut-docs#665 research comment); this pins the negative as a regression
// guard, not just relying on Row having no such field.
func TestBuild_NeverEmitsPINOrPUK(t *testing.T) {
	rows := []Row{{RegisterName: "Front Till", LocationName: "Café Hauptstraße", AcquiredOn: "2026-01-01"}}
	out := strings.ToLower(string(mustBuild(t, rows).Content))
	for _, forbidden := range []string{"pin", "puk"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("output must never mention %q, got:\n%s", forbidden, out)
		}
	}
}

func mustBuild(t *testing.T, rows []Row) *Result {
	t.Helper()
	res, err := Build(rows, time.Now())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return res
}
