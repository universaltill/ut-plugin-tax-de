// Human-readable §146a Abs. 4 AO notification summary (ut-docs#937, export
// half of #665's core register). Pure local data transformation — same
// no-network, no-fiskaly-account shape as src/datev, and the same
// no-wasip1-tag convention so `go test ./src/paragraph146a/...` runs on the
// host (main.go itself stays untestable directly, wasip1-only).
//
// The field list, order and per-field labels below deliberately mirror the
// pilot's incumbent (certified) vendor's own "Daten per E-Mail verschicken
// (Kassenmeldepflicht)" output, captured in ut-docs#665's 2026-08-14 research
// comment — the till fills every field the incumbent's software could also
// fill (it sees only its own device; this till sees every till/TSE recorded
// under a business location, the gross-method requirement's whole point) and
// is honest ("Vom Betreiber zu ermitteln" — for the operator to work out)
// about the one field it genuinely cannot know: the shop's own Steuernummer.
//
// This is NOT the ELSTER XML upload format (deliberately out of scope until
// the real schema is verified against BMF/ELSTER docs, not guessed — see
// ut-docs#937's own "Explicitly NOT in this card" section) and it does not
// file anything — the shop still submits via Mein ELSTER themselves.
package paragraph146a

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Row mirrors universal-till's internal/data.FiscalRegisterDE JSON shape
// (the "fiscal_register_de" field of the export.requested.ask payload,
// ut-docs#937) — same deliberate wire-shape duplication as datev.SaleRow: no
// Go-level dependency on universal-till, keyed to the wire contract. There is
// deliberately NO TSE-PIN or TSE-PUK field here: §146a Abs. 4 AO does not ask
// for either, the till never stores either, and the incumbent vendor's own
// output was found to leak both in plain text (ut-docs#665 research comment)
// — this type's shape is what keeps that mistake structurally impossible to
// repeat, not a redaction applied at render time.
type Row struct {
	ID                 string  `json:"id"`
	RegisterID         string  `json:"register_id"`
	RegisterName       string  `json:"register_name"`
	LocationID         string  `json:"location_id"`
	LocationName       string  `json:"location_name"`
	LocationStreet     string  `json:"location_street"`
	LocationPostcode   string  `json:"location_postcode"`
	LocationCity       string  `json:"location_city"`
	EasType            string  `json:"eas_type"`
	EasSoftware        string  `json:"eas_software"`
	EasSerial          string  `json:"eas_serial"`
	TSESerial          string  `json:"tse_serial"`
	TSECertificationID string  `json:"tse_certification_id"`
	TSEType            string  `json:"tse_type"`
	AcquiredOn         string  `json:"acquired_on"`
	CommissionedOn     *string `json:"commissioned_on"`
	DecommissionedOn   *string `json:"decommissioned_on"`
}

// Result is Build's output — plain UTF-8 text (not datev.Result's
// Windows-1252/CRLF): this file is read by a person (or pasted into an
// accountant's email), never ingested by accounting software, so there is no
// external encoding contract to match.
type Result struct {
	Filename string
	Content  []byte
}

// Build renders the notification summary, grouped by business location
// (gross-method: one block per location covering every till/TSE recorded
// there, per ADR's own criterion) — deterministically re-sorted here by
// location name (a no-location group last) then register name then
// AcquiredOn, so the output does not depend on the order the host happened
// to send rows in.
//
// Refuses (returns an error, not an empty file) when rows is empty — same
// "declare the gap, don't guess" discipline as datev.Build's missing-
// Gegenkonto refusal: an empty register means the shop has not recorded any
// till/TSE yet, which is worth a clear message, not a blank document that
// looks like a successful export of nothing.
func Build(rows []Row, now time.Time) (*Result, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("paragraph146a export: no fiscal register entries recorded — add tills under Fiscal Register before exporting a notification summary")
	}

	type location struct {
		name, street, postcode, city string
		rows                         []Row
	}
	byLocation := map[string]*location{}
	var order []string
	for _, r := range rows {
		key := r.LocationID
		loc, ok := byLocation[key]
		if !ok {
			loc = &location{name: r.LocationName, street: r.LocationStreet, postcode: r.LocationPostcode, city: r.LocationCity}
			byLocation[key] = loc
			order = append(order, key)
		}
		loc.rows = append(loc.rows, r)
	}
	sort.Slice(order, func(i, j int) bool {
		li, lj := byLocation[order[i]], byLocation[order[j]]
		// A no-location group (key "") sorts last, same convention
		// universal-till's own FiscalRegisterDEStore.List uses.
		if (order[i] == "") != (order[j] == "") {
			return order[j] == ""
		}
		if li.name != lj.name {
			return li.name < lj.name
		}
		return order[i] < order[j]
	})
	for _, key := range order {
		loc := byLocation[key]
		sort.SliceStable(loc.rows, func(i, j int) bool {
			a, b := loc.rows[i], loc.rows[j]
			if a.RegisterName != b.RegisterName {
				return a.RegisterName < b.RegisterName
			}
			return a.AcquiredOn < b.AcquiredOn
		})
	}

	var b strings.Builder
	b.WriteString("§146a Abs. 4 AO – Meldeübersicht\n")
	fmt.Fprintf(&b, "Erstellt am: %s\n", now.UTC().Format("2006-01-02"))
	b.WriteString("Steuernummer: Vom Betreiber zu ermitteln\n")
	b.WriteString("\nHinweis: Diese Übersicht ersetzt nicht die Meldung über Mein ELSTER. Die Meldung ist\n")
	b.WriteString("vom Betreiber selbst einzureichen — Frist: innerhalb eines Monats nach Anschaffung bzw.\n")
	b.WriteString("Außerbetriebnahme eines elektronischen Aufzeichnungssystems.\n")

	for _, key := range order {
		loc := byLocation[key]
		b.WriteString("\n----------------------------------------\n")
		if loc.name != "" {
			fmt.Fprintf(&b, "Betriebsstätte: %s\n", loc.name)
		} else {
			b.WriteString("Betriebsstätte: (keiner Betriebsstätte zugeordnet)\n")
			// Independent-review finding S5: every register with no
			// assigned location merges into this ONE group, so more than
			// one row here likely means more than one real Betriebsstaette
			// got folded together, understating the Betriebsstaette count
			// and overstating this group's own "genutzten eAs" figure,
			// exactly the number ELSTER asks for per premises. Flag it
			// rather than silently merge.
			if len(loc.rows) > 1 {
				b.WriteString("Hinweis: Diese Kassen sind KEINER Betriebsstätte zugeordnet und wurden hier\n")
				b.WriteString("zusammengefasst. Falls sie sich an mehreren Betriebsstätten befinden, bitte\n")
				b.WriteString("vor der Meldung im ELSTER-Formular einzeln zuordnen (siehe Fiscal Register).\n")
			}
		}
		addr := addressLine(loc.street, loc.postcode, loc.city)
		if addr != "" {
			fmt.Fprintf(&b, "Anschrift: %s\n", addr)
		}
		// "genutzten" (in use) counts only tills that are BOTH commissioned
		// (Inbetriebnahme-Datum set) AND not decommissioned -- a till not
		// yet put into service isn't "genutzt" any more than a retired one
		// is (independent-review finding S3), so both are excluded from
		// this count even though both still appear listed below (their own
		// dates are separate notification-worthy facts). This is exactly
		// the field the incumbent vendor could never fill (it sees only
		// its own device) — see the package doc comment.
		inUse := 0
		for _, r := range loc.rows {
			if r.CommissionedOn != nil && r.DecommissionedOn == nil {
				inUse++
			}
		}
		fmt.Fprintf(&b, "Gesamtanzahl der genutzten eAs: %d\n", inUse)

		for i, r := range loc.rows {
			fmt.Fprintf(&b, "\n  eAs %d:\n", i+1)
			fmt.Fprintf(&b, "    Art des eAs: %s\n", orPlaceholder(r.EasType))
			fmt.Fprintf(&b, "    Software des eAs: %s\n", orPlaceholder(r.EasSoftware))
			fmt.Fprintf(&b, "    Seriennummer des eAs: %s\n", orPlaceholder(r.EasSerial))
			fmt.Fprintf(&b, "    Seriennummer der TSE: %s\n", orPlaceholder(r.TSESerial))
			fmt.Fprintf(&b, "    BSI-Zertifizierungs-ID: %s\n", orPlaceholder(r.TSECertificationID))
			fmt.Fprintf(&b, "    Art/Bauform der TSE: %s\n", orPlaceholder(r.TSEType))
			fmt.Fprintf(&b, "    Anschaffungs-Datum des eAs: %s\n", orPlaceholder(r.AcquiredOn))
			fmt.Fprintf(&b, "    Inbetriebnahme-Datum: %s\n", orDatePlaceholder(r.CommissionedOn, "noch nicht in Betrieb genommen"))
			fmt.Fprintf(&b, "    Außerbetriebnahme-Datum: %s\n", orDatePlaceholder(r.DecommissionedOn, "—"))
		}
	}

	return &Result{
		Filename: fmt.Sprintf("paragraph146a-meldeuebersicht-%s.txt", now.UTC().Format("2006-01-02")),
		Content:  []byte(b.String()),
	}, nil
}

// addressLine joins street/postcode/city, omitting whatever part is empty
// rather than emitting stray ", " separators for a location with a partial
// address on file.
func addressLine(street, postcode, city string) string {
	cityPart := strings.TrimSpace(postcode + " " + city)
	parts := make([]string, 0, 2)
	if street != "" {
		parts = append(parts, street)
	}
	if cityPart != "" {
		parts = append(parts, cityPart)
	}
	return strings.Join(parts, ", ")
}

func orPlaceholder(s string) string {
	if s == "" {
		return "Vom Betreiber zu ermitteln"
	}
	return s
}

func orDatePlaceholder(s *string, placeholder string) string {
	if s == nil || *s == "" {
		return placeholder
	}
	return *s
}
