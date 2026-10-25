package verify

import (
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestQueueKeysRefusedInIR: a queue's key names a required field of its
// message that holds one value an index compares (D80).
func TestQueueKeysRefusedInIR(t *testing.T) {
	schema := ir.NewSchema("orders-db", ir.SchemaKindDB)
	queue := func(name, key string, field *ir.FieldDef) {
		schema.Types[name] = &ir.TypeDef{Name: name, Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{Key: key, OnDuplicate: "replace"}, Fields: []*ir.FieldDef{field}}
	}
	queue("Missing", "orderId", &ir.FieldDef{Name: "sku", TypeRef: ir.TypeRef{Name: "string"}, Required: true})
	queue("Optional", "orderId", &ir.FieldDef{Name: "orderId", TypeRef: ir.TypeRef{Name: "string"}})
	queue("Listed", "tags", &ir.FieldDef{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true})
	queue("Good", "orderId", &ir.FieldDef{Name: "orderId", TypeRef: ir.TypeRef{Name: "string"}, Required: true})
	r := &Result{}
	checkQueues(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		`queue Listed's key tags is a list, a map or a JSON value; a key is one scalar or enum value`,
		`queue Missing's key "orderId" names no field of the message; name one of its fields, as key: "sku"`,
		`queue Optional's key orderId is optional; a key is a required field, so every message has one`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}
}

// TestEnqueueOnChangeRefusedInIR: an @enqueueOnChange is on a table, names
// a queue of the schema, compares and tests fields with a column, and its
// message lines up with the queue's: the same types, a required field from
// a value the row always has, and every required field it leaves out with
// a default (D80).
func TestEnqueueOnChangeRefusedInIR(t *testing.T) {
	str := ir.TypeRef{Name: "string"}
	num := ir.TypeRef{Name: "number"}
	schema := ir.NewSchema("orders-db", ir.SchemaKindDB)
	schema.Enums["Status"] = &ir.EnumDef{Name: "Status", Values: []ir.EnumValueDef{{Name: "Open", SerializedAs: "open"}, {Name: "Shut", SerializedAs: "shut"}}}
	schema.Types["Product"] = &ir.TypeDef{Name: "Product", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
	}}
	schema.Types["Changed"] = &ir.TypeDef{Name: "Changed", Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{Key: "lineId", OnDuplicate: "replace"}, Fields: []*ir.FieldDef{
		{Name: "lineId", TypeRef: str, Required: true},
		{Name: "productId", TypeRef: str, Required: true},
		{Name: "count", TypeRef: num, Required: true},
		{Name: "note", TypeRef: str},
	}}
	on := func(def *ir.EnqueueOnChange) *ir.EnqueueOnChange {
		if def.Queue == "" {
			def.Queue = "Changed"
		}
		if def.On == nil {
			def.On = []string{"insert", "update"}
		}
		return def
	}
	schema.Types["Line"] = &ir.TypeDef{Name: "Line", Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: str, Required: true, Key: true},
			{Name: "product", TypeRef: ir.TypeRef{Name: "Product"}},
			{Name: "quantity", TypeRef: num, Required: true},
			{Name: "status", TypeRef: ir.TypeRef{Name: "Status"}, Required: true},
			{Name: "memo", TypeRef: str},
			{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true},
		},
		EnqueueOnChange: []*ir.EnqueueOnChange{
			// Lines up: a nullable relation guarded by notNull.
			on(&ir.EnqueueOnChange{
				Fields:  []string{"product", "quantity"},
				When:    []*ir.EnqueueCondition{{Field: "product", NotNull: true}, {Field: "status", Equals: "open"}},
				Message: map[string]string{"lineId": "id", "productId": "product", "count": "quantity", "note": "memo"},
			}),
			on(&ir.EnqueueOnChange{Queue: "Nowhere", Message: map[string]string{"lineId": "id"}}),
			on(&ir.EnqueueOnChange{
				Fields:  []string{"nothing"},
				When:    []*ir.EnqueueCondition{{Field: "quantity", IsNull: true}, {Field: "status", Equals: "lost"}, {Field: "labels", Equals: "x"}, {Field: "quantity", Equals: "one"}},
				Message: map[string]string{"lineId": "id", "productId": "product", "count": "memo", "ghost": "id"},
			}),
		},
	}
	schema.Types["Pane"] = &ir.TypeDef{Name: "Pane", Role: ir.RoleEmbeddedStruct, EnqueueOnChange: []*ir.EnqueueOnChange{on(&ir.EnqueueOnChange{Message: map[string]string{"lineId": "id"}})}}
	r := &Result{}
	checkEnqueueOnChange(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	// The first declaration lines up, a nullable relation guarded by
	// notNull, so no error names it.
	want := []string{
		`@enqueueOnChange(Nowhere) on Line: Nowhere is no @queue class of the schema; name the queue whose message the trigger enqueues`,
		`@enqueueOnChange(Changed) on Line: fields names "nothing", which is no field of Line with a column`,
		`@enqueueOnChange(Changed) on Line: when tests whether quantity is null, and it is required, so it never is; leave the condition out`,
		`@enqueueOnChange(Changed) on Line: when status equals lost: "lost" is no member of Status`,
		`@enqueueOnChange(Changed) on Line: when labels equals x: equals compares an enum, text, number or boolean field`,
		`@enqueueOnChange(Changed) on Line: when quantity equals one: quantity is a DOUBLE PRECISION column, which equals compares with a number`,
		`@enqueueOnChange(Changed) on Line: message field count takes memo: the message's column is DOUBLE PRECISION and the table's TEXT`,
		`@enqueueOnChange(Changed) on Line: message maps ghost, which is no field of Changed`,
		`@enqueueOnChange(Changed) on Line: message field productId is required, and product may be null; add { field: "product", notNull: true } to when, or make productId optional`,
		`@enqueueOnChange on Pane: Pane is not a table of the database; put it on a table, whose rows' writes enqueue`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	missing := ir.NewSchema("orders-db", ir.SchemaKindDB)
	for name, td := range schema.Types {
		missing.Types[name] = td
	}
	missing.Enums = schema.Enums
	missing.Types["Line"] = &ir.TypeDef{Name: "Line", Role: ir.RoleDBTable, Fields: schema.Types["Line"].Fields,
		EnqueueOnChange: []*ir.EnqueueOnChange{on(&ir.EnqueueOnChange{Message: map[string]string{"lineId": "id"}})}}
	delete(missing.Types, "Pane")
	r = &Result{}
	checkEnqueueOnChange(missing, r)
	got = nil
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	wantMissing := []string{
		"@enqueueOnChange(Changed) on Line: Changed's field productId is required and its column has no default, so the message maps it to a field of Line",
		"@enqueueOnChange(Changed) on Line: Changed's field count is required and its column has no default, so the message maps it to a field of Line",
	}
	if !slices.Equal(got, wantMissing) {
		t.Errorf("errors =\n%q\nwant\n%q", got, wantMissing)
	}
}
