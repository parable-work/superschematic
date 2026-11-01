package behaviorlayout

import (
	"encoding/json"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// The layout rule names a field anchor's value field after the anchor and
// every other field after the anchor and the declared name, and a type
// anchor's fields after the behavior's storage key (D103); a use's member
// is its field, or the storage key in camel case, which names its
// sub-tables.
func TestTheLayoutRule(t *testing.T) {
	status := ir.BehaviorRef{Name: "Workflow", Field: "status"}
	shipStatus := ir.BehaviorRef{Name: "Workflow", Field: "shipStatus"}
	onType := ir.BehaviorRef{Name: "Workflow"}
	for _, test := range []struct {
		use            ir.BehaviorRef
		declared       string
		value          bool
		field, column  string
		typeAnchorOnly bool
	}{
		{use: status, declared: "status", value: true, field: "status", column: "status"},
		{use: status, declared: "enteredAt", field: "statusEnteredAt", column: "status_entered_at"},
		{use: shipStatus, declared: "status", value: true, field: "shipStatus", column: "ship_status"},
		{use: shipStatus, declared: "enteredAt", field: "shipStatusEnteredAt", column: "ship_status_entered_at"},
		{use: onType, declared: "status", column: "workflow_status", typeAnchorOnly: true},
		{use: onType, declared: "enteredAt", column: "workflow_entered_at", typeAnchorOnly: true},
		{use: ir.BehaviorRef{Name: "acme.Rating"}, declared: "count", column: "acme_rating_count", typeAnchorOnly: true},
	} {
		if got := OwnedColumn(test.use, test.declared, test.value); got != test.column {
			t.Errorf("OwnedColumn(%+v, %s) = %q, want %q", test.use, test.declared, got, test.column)
		}
		if test.typeAnchorOnly {
			continue
		}
		if got := OwnedFieldName(test.use.Field, test.declared, test.value); got != test.field {
			t.Errorf("OwnedFieldName(%s, %s) = %q, want %q", test.use.Field, test.declared, got, test.field)
		}
	}
	if got := StorageKey("acme.Rating"); got != "acme_rating" {
		t.Errorf("StorageKey(acme.Rating) = %q", got)
	}
	for use, want := range map[*ir.BehaviorRef]string{
		&status: "status", &shipStatus: "shipStatus",
		{Name: "Timers", Implements: true}: "timers", {Name: "acme.Rating", Implements: true}: "acmeRating",
	} {
		if got := UseMember(*use); got != want {
			t.Errorf("UseMember(%+v) = %q, want %q", *use, got, want)
		}
	}
	if got := SubTableTypeName("Ticket", ir.BehaviorRef{Name: "Timers", Implements: true}, "deadlines"); got != "TicketTimersDeadlines" {
		t.Errorf("SubTableTypeName = %q", got)
	}
	if got := SubTableTypeName("Shipment", shipStatus, "notes"); got != "ShipmentShipStatusNotes" {
		t.Errorf("SubTableTypeName of a field use = %q", got)
	}
	if got := DeadlineQueueTypeName("Ticket"); got != "TicketDeadline" {
		t.Errorf("DeadlineQueueTypeName = %q", got)
	}
}

// A date-time is epoch milliseconds in the core; any other type is its
// value as it is.
func TestConvertOf(t *testing.T) {
	if got := ConvertOf("Temporal.DateTime"); got != ConvertEpochMillis {
		t.Errorf("ConvertOf(Temporal.DateTime) = %q", got)
	}
	for _, typeName := range []string{"string", "number", "boolean", "Identity.UUID"} {
		if got := ConvertOf(typeName); got != ConvertNone {
			t.Errorf("ConvertOf(%s) = %q", typeName, got)
		}
	}
}

// The document's JSON is the shape the adapter reads (RulesLayout in
// runtime/http/typescript/src/rules.ts), and its spec the core's TypeSpec.
func TestLayoutJSON(t *testing.T) {
	layout := Layout{Types: map[string]*Table{"Order": {
		Type:    "Order",
		Table:   "order",
		Key:     Column{Field: "id", Column: "id", Type: "Identity.UUID"},
		Version: VersionColumn,
		Own:     []Column{{Field: "title", Column: "title", Type: "string"}},
		Uses: []Use{{
			ID:       "status",
			Behavior: "Workflow",
			Anchor:   "status",
			Member:   "status",
			Fields: []OwnedField{
				{Name: "status", Column: Column{Field: "status", Column: "status", Type: "string"}, Value: true},
				{Name: "enteredAt", Column: Column{Field: "statusEnteredAt", Column: "status_entered_at", Type: "Temporal.DateTime", Convert: ConvertEpochMillis}},
			},
			Operations: []Operation{{Name: "transition", Writes: true}},
		}, {
			ID:         "Timers",
			Behavior:   "Timers",
			Member:     "timers",
			Fields:     []OwnedField{},
			Operations: []Operation{{Name: "fireTimer", Writes: true}},
			Tables: []SubTable{{
				Name:   "deadlines",
				Table:  "order_timers_deadlines",
				Row:    "order_id",
				Key:    OwnedField{Name: "name", Column: Column{Field: "name", Column: "name", Type: "string"}},
				Fields: []OwnedField{{Name: "dueAt", Column: Column{Field: "dueAt", Column: "due_at", Type: "Temporal.DateTime", Convert: ConvertEpochMillis}}},
			}},
		}},
		Deadlines: &Deadlines{Queue: "OrderDeadline", Table: "order_deadline_queue", RowKey: "row_key", Use: "use_id", Name: "name", At: "at", Payload: "payload"},
		Spec: TypeSpec{Name: "Order", Uses: []UseSpec{
			{Behavior: "Workflow", Config: json.RawMessage(`{"states":["draft","paid"]}`), Anchor: Anchor{Field: "status"}},
			{Behavior: "Timers", Anchor: Anchor{}},
		}, Fields: []FieldSpec{{Name: "title", Type: "string"}, {Name: "placedAt", Type: "Temporal.DateTime", DateTime: true}}},
	}}}
	data, err := json.Marshal(layout)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"types":{"Order":{"type":"Order","table":"order","key":{"field":"id","column":"id","type":"Identity.UUID"},"version":"_version",` +
		`"own":[{"field":"title","column":"title","type":"string"}],` +
		`"uses":[{"id":"status","behavior":"Workflow","anchor":"status","member":"status","fields":[` +
		`{"name":"status","field":"status","column":"status","type":"string","value":true},` +
		`{"name":"enteredAt","field":"statusEnteredAt","column":"status_entered_at","type":"Temporal.DateTime","convert":"epochMillis"}],` +
		`"operations":[{"name":"transition","writes":true}]},` +
		`{"id":"Timers","behavior":"Timers","member":"timers","fields":[],"operations":[{"name":"fireTimer","writes":true}],` +
		`"tables":[{"name":"deadlines","table":"order_timers_deadlines","row":"order_id","key":{"name":"name","field":"name","column":"name","type":"string"},` +
		`"fields":[{"name":"dueAt","field":"dueAt","column":"due_at","type":"Temporal.DateTime","convert":"epochMillis"}]}]}],` +
		`"deadlines":{"queue":"OrderDeadline","table":"order_deadline_queue","rowKey":"row_key","use":"use_id","name":"name","at":"at","payload":"payload"},` +
		`"spec":{"name":"Order","uses":[{"behavior":"Workflow","config":{"states":["draft","paid"]},"anchor":{"field":"status"}},{"behavior":"Timers","anchor":"type"}],` +
		`"fields":[{"name":"title","type":"string"},{"name":"placedAt","type":"Temporal.DateTime","dateTime":true}]}}}}`
	if string(data) != want {
		t.Fatalf("layout JSON =\n%s\nwant\n%s", data, want)
	}
	var back Layout
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if again, _ := json.Marshal(back); string(again) != want {
		t.Fatalf("round trip changed the JSON:\n%s", again)
	}
	var bad Anchor
	if err := json.Unmarshal([]byte(`"field"`), &bad); err == nil {
		t.Error(`anchor "field" read`)
	}
	if err := json.Unmarshal([]byte(`{"field":""}`), &bad); err == nil {
		t.Error(`anchor {"field":""} read`)
	}
}
