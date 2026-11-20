package behaviorlayout_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// derivedProvenanceFixture is the DB service whose Ticket a model triages
// under review and whose Article a model summarizes (D73, D103's step 4).
const derivedProvenanceFixture = "../../loader/tsreader/testdata/services/fixture-derived-provenance-db"

// The layout document of tables that implement Derived and Provenance (D73
// on D103's step 4): Ticket's uses are Derived's, with its sub-table
// derivations keyed by the field and its operations of the table as a
// whole, Provenance's, with its owned inReview, a boolean column, and its
// sub-table records, and the status Workflow's; the spec gives each field
// Derived derives its JSON Schema, an enum's values among it, the output a
// deriver asks for, and no other field one; neither keeps a queue.
// testdata/fixture-derived-provenance-db.layout.json is the document the
// adapters of every language are written against.
func TestLayoutOfDerivedAndProvenance(t *testing.T) {
	layout := layoutOfFixture(t, derivedProvenanceFixture, "fixture-derived-provenance-db")
	td := layout.Types["Ticket"]
	if td == nil || len(td.Uses) != 3 {
		t.Fatalf("Ticket = %+v", td)
	}
	derived, provenance := td.Uses[0], td.Uses[1]
	if derived.ID != "Derived" || len(derived.Fields) != 0 || len(derived.Tables) != 1 || derived.Tables[0].Table != "ticket_derived_derivations" {
		t.Errorf("Ticket's Derived = %+v", derived)
	}
	var operations []string
	for _, op := range derived.Operations {
		operations = append(operations, op.Name)
		if op.Table != (op.Name != "applyDerived" && op.Name != "rederive") {
			t.Errorf("Derived's %s is of the table as a whole: %v", op.Name, op.Table)
		}
	}
	if want := "staleDerivations, settleDerivations, failedDerivations, applyDerived, rederive"; strings.Join(operations, ", ") != want {
		t.Errorf("Derived's operations = %s, want %s", strings.Join(operations, ", "), want)
	}
	owned, err := json.Marshal(provenance.Fields)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"name":"inReview","field":"provenanceInReview","column":"provenance_in_review","type":"boolean"}]`; string(owned) != want {
		t.Errorf("Provenance's fields = %s, want %s", owned, want)
	}
	if len(provenance.Tables) != 1 || provenance.Tables[0].Table != "ticket_provenance_records" {
		t.Errorf("Provenance's tables = %+v", provenance.Tables)
	}
	schemas := map[string]string{}
	for _, field := range td.Spec.Fields {
		schemas[field.Name] = string(field.Schema)
	}
	if want := `{"description":"A Category value","enum":["billing","bug","other",null],"type":["string","null"]}`; schemas["category"] != want {
		t.Errorf("category's schema = %s, want %s", schemas["category"], want)
	}
	if want := `{"description":"A string value","type":["string","null"]}`; schemas["priority"] != want {
		t.Errorf("priority's schema = %s, want %s", schemas["priority"], want)
	}
	if schemas["subject"] != "" || schemas["body"] != "" {
		t.Errorf("subject's and body's schemas = %q %q, want none", schemas["subject"], schemas["body"])
	}
	if td.Deadlines != nil || td.Changes != nil || td.Events != nil {
		t.Errorf("Ticket's queues = %+v %+v %+v, want none", td.Deadlines, td.Changes, td.Events)
	}
}
