package behaviorlayout_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// numberingFixture is the DB service whose Invoice is numbered per customer
// and per year on its number field, and whose Ticket carries two Numbering
// fields (D103's step 4).
const numberingFixture = "../../loader/tsreader/testdata/services/fixture-numbering-db"

// The layout document of tables whose fields carry Numbering (D103's step
// 4): Invoice's one use is its number field's, the label on the field's own
// column, its value, and the seq beside it, assignNumber its operation, and
// no sub-table: its counts are a shared table of the use, kept for the
// table as a whole, keyed by the scope alone with no column of a row's key,
// its last and counting columns, and the version each write is held to;
// Invoice's audit names createdAt, which assignNumber reads its year from,
// and its spec lists it after its own fields; Numbering keeps no queue.
// Ticket's two uses each keep a shared table of their own.
// testdata/fixture-numbering-db.layout.json is the document the adapters of
// every language are written against.
func TestLayoutOfNumbering(t *testing.T) {
	layout := layoutOfFixture(t, numberingFixture, "fixture-numbering-db")
	td := layout.Types["Invoice"]
	if td == nil {
		t.Fatalf("the layout has no Invoice: %+v", layout.Types)
	}
	if len(td.Uses) != 1 || td.Uses[0].ID != "number" || len(td.Uses[0].Tables) != 0 || len(td.Uses[0].Shared) != 1 {
		t.Fatalf("Invoice's uses = %+v, want number's", td.Uses)
	}
	number, err := json.Marshal(td.Uses[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"number","behavior":"Numbering","anchor":"number","member":"number","fields":[` +
		`{"name":"seq","field":"numberSeq","column":"number_seq","type":"Generic.Int64"},` +
		`{"name":"label","field":"number","column":"number","type":"string","value":true}],` +
		`"operations":[{"name":"assignNumber","writes":true}],` +
		`"shared":[{"name":"counts","table":"invoice_number_counts",` +
		`"key":{"name":"scope","field":"scope","column":"scope","type":"string"},` +
		`"fields":[{"name":"last","field":"last","column":"last","type":"Generic.Int64"},{"name":"counting","field":"counting","column":"counting","type":"string"}],` +
		`"version":"_version"}]}`
	if string(number) != want {
		t.Errorf("Invoice's number =\n%s\nwant\n%s", number, want)
	}
	audit, err := json.Marshal(td.Audit)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"field":"createdAt","column":"created_at","type":"Temporal.DateTime","convert":"epochMillis"}]`; string(audit) != want {
		t.Errorf("Invoice's audit = %s, want %s", audit, want)
	}
	var fields []string
	for _, field := range td.Spec.Fields {
		fields = append(fields, field.Name)
	}
	if want := "customerId, title, createdAt"; strings.Join(fields, ", ") != want {
		t.Errorf("Invoice's spec fields = %s, want %s", strings.Join(fields, ", "), want)
	}
	if td.Deadlines != nil || td.Changes != nil || td.Events != nil {
		t.Errorf("Invoice's queues = %+v %+v %+v, want none", td.Deadlines, td.Changes, td.Events)
	}
	ticket := layout.Types["Ticket"]
	if ticket == nil || len(ticket.Uses) != 2 || ticket.Uses[0].Shared[0].Table != "ticket_number_counts" || ticket.Uses[1].Shared[0].Table != "ticket_reference_counts" || len(ticket.Audit) != 0 {
		t.Errorf("Ticket = %+v", ticket)
	}
}
