package behaviorlayout

import (
	"encoding/json"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// The layout rule names a field anchor's value field after the anchor and
// every other field after the anchor and the declared name, and a type
// anchor's fields after the behavior's storage key (D103).
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
			Fields: []OwnedField{
				{Name: "status", Column: Column{Field: "status", Column: "status", Type: "string"}, Value: true},
				{Name: "enteredAt", Column: Column{Field: "statusEnteredAt", Column: "status_entered_at", Type: "Temporal.DateTime", Convert: ConvertEpochMillis}},
			},
			Operations: []Operation{{Name: "transition", Writes: true}},
		}},
		Spec: TypeSpec{Name: "Order", Uses: []UseSpec{
			{Behavior: "Workflow", Config: json.RawMessage(`{"states":["draft","paid"]}`), Anchor: Anchor{Field: "status"}},
			{Behavior: "Comments", Anchor: Anchor{}},
		}},
	}}}
	data, err := json.Marshal(layout)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"types":{"Order":{"type":"Order","table":"order","key":{"field":"id","column":"id","type":"Identity.UUID"},"version":"_version",` +
		`"own":[{"field":"title","column":"title","type":"string"}],` +
		`"uses":[{"id":"status","behavior":"Workflow","anchor":"status","fields":[` +
		`{"name":"status","field":"status","column":"status","type":"string","value":true},` +
		`{"name":"enteredAt","field":"statusEnteredAt","column":"status_entered_at","type":"Temporal.DateTime","convert":"epochMillis"}],` +
		`"operations":[{"name":"transition","writes":true}]}],` +
		`"spec":{"name":"Order","uses":[{"behavior":"Workflow","config":{"states":["draft","paid"]},"anchor":{"field":"status"}},{"behavior":"Comments","anchor":"type"}]}}}}`
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
