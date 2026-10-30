package verify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// behaviorRegistry registers test behaviors under core names, as the
// registry allows for the core: Pallet takes a required config, adds a field
// and an operation; Audit requires Pallet; Clearance conflicts with Pallet;
// Shelved adds Pallet's field; Recount adds Pallet's operation; Tally and
// Census each add a namespace operation named count (D77).
func behaviorRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Naming{})
	op := func(name string) string {
		return `{"name":"` + name + `","paramsSchema":{"type":"object","additionalProperties":false},"resultSchema":{"type":"object"}}`
	}
	for _, decl := range []string{
		`{"name":"Pallet","configSchema":{"type":"object","required":["aisles"],"properties":{"aisles":{"type":"integer","minimum":1}}},` +
			`"fields":[{"name":"onHand"}],"operations":[` + op("restock") + `]}`,
		`{"name":"Audit","requires":["Pallet"],"fields":[{"name":"auditedAt"}],"operations":[` + op("audit") + `]}`,
		`{"name":"Clearance","conflicts":["Pallet"],"fields":[{"name":"markdown"}]}`,
		`{"name":"Shelved","fields":[{"name":"onHand"},{"name":"shelf"}]}`,
		`{"name":"Recount","operations":[` + op("restock") + `]}`,
		`{"name":"Tally","operations":[` + strings.Replace(op("count"), `"name":"count"`, `"name":"count","scope":"namespace"`, 1) + `]}`,
		`{"name":"Census","operations":[` + strings.Replace(op("count"), `"name":"count"`, `"name":"count","scope":"namespace"`, 1) + `]}`,
	} {
		if err := reg.RegisterBehavior(registry.BehaviorSpec{Declaration: json.RawMessage(decl)}); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func behaviorSchema(refs ...ir.BehaviorRef) *ir.Schema {
	schema := ir.NewSchema("inventory", ir.SchemaKindGeneral)
	schema.Types["Item"] = &ir.TypeDef{
		Name:      "Item",
		Owner:     "src/item.schema.json",
		Role:      ir.RoleEmbeddedStruct,
		Behaviors: refs,
		Fields: []*ir.FieldDef{
			{Name: "sku", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "shelf", TypeRef: ir.TypeRef{Name: "string"}},
		},
	}
	return schema
}

func stock(config string) ir.BehaviorRef {
	return ir.BehaviorRef{Name: "Pallet", Config: json.RawMessage(config)}
}

func TestBehaviorsVerify(t *testing.T) {
	reg := behaviorRegistry(t)
	for _, test := range []struct {
		name string
		refs []ir.BehaviorRef
		want []string
	}{
		{"accepted", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Audit"}}, nil},
		{"requirement listed first", []ir.BehaviorRef{{Name: "Audit"}, stock(`{"aisles":2}`)}, nil},
		{"unknown", []ir.BehaviorRef{{Name: "Ghost"}},
			[]string{`src/item.schema.json: type Item: behavior "Ghost" is not a registered behavior (registered: Approvals, Assignment, Audit, Blueprint, Branches, Budget, Calendar, Census, Clearance, Comments, Constants, Dependencies, Derived, Holds, Lease, Links, Numbering, Pallet, Payments, Presence, Provenance, Queue, Reactions, Recount, Retries, Revisions, Rollups, Search, Shelved, StateRules, Stock, Tally, Timers, Variants, Workflow)`}},
		{"config rejected", []ir.BehaviorRef{stock(`{"aisles":0}`)},
			[]string{"src/item.schema.json: type Item: behavior Pallet config: "}},
		{"config missing", []ir.BehaviorRef{{Name: "Pallet"}},
			[]string{"src/item.schema.json: type Item: behavior Pallet config: "}},
		{"config on a behavior without one", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Audit", Config: json.RawMessage(`{"often":true}`)}},
			[]string{"src/item.schema.json: type Item: behavior Audit takes no config"}},
		{"listed twice", []ir.BehaviorRef{stock(`{"aisles":2}`), stock(`{"aisles":3}`)},
			[]string{"src/item.schema.json: type Item lists behavior Pallet twice"}},
		{"missing requirement", []ir.BehaviorRef{{Name: "Audit"}},
			[]string{"src/item.schema.json: type Item: behavior Audit requires behavior Pallet, which the type does not list"}},
		{"conflict", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Clearance"}},
			[]string{"src/item.schema.json: type Item: behavior Clearance conflicts with behavior Pallet, which the type also lists"}},
		{"field named like the type's own", []ir.BehaviorRef{{Name: "Shelved"}}, nil},
		{"field named like another behavior's", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Shelved"}}, nil},
		{"operation collides", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Recount"}},
			[]string{"src/item.schema.json: type Item: behaviors Pallet and Recount both add operation restock"}},
		// A namespace operation is called by its behavior's name, not on
		// the type, so two behaviors' namespace operations of one name
		// share none.
		{"namespace operations share a name", []ir.BehaviorRef{{Name: "Tally"}, {Name: "Census"}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := Run(behaviorSchema(test.refs...), Input{Registry: reg})
			got := errorStrings(r)
			if len(test.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("errors = %v, want none", got)
				}
				return
			}
			for _, want := range test.want {
				if !hasError(r, want) {
					t.Errorf("errors = %v, want one with %q", got, want)
				}
			}
		})
	}
}

// A behavior field never collides with one of the type's own fields, by
// its name or its JSON key: an instance keeps a behavior's fields under the
// behavior's name, apart from the type's own (D16, amended: a behavior's
// fields sit under its name).
func TestBehaviorsVerifyFieldJSONKey(t *testing.T) {
	reg := behaviorRegistry(t)
	for _, field := range []*ir.FieldDef{
		{Name: "onHand", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "unitsOnHand", JSONTag: "onHand", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "onHand", JSONTag: "units_on_hand", TypeRef: ir.TypeRef{Name: "string"}},
	} {
		schema := behaviorSchema(stock(`{"aisles":2}`))
		schema.Types["Item"].Fields = append(schema.Types["Item"].Fields, field)
		if got := errorStrings(Run(schema, Input{Registry: reg})); len(got) != 0 {
			t.Errorf("field %s (jsonTag %q): errors = %v, want none", field.Name, field.JSONTag, got)
		}
	}
}

// The core registry accepts the core's behaviors with no extension linked,
// and holds them to the same rules: a behavior it does not register fails
// the load. A type may declare a field a core behavior also adds, since the
// behavior's sits under its name.
func TestBehaviorsVerifyWithTheCore(t *testing.T) {
	core := registry.New(naming.Naming{})
	workflow := ir.BehaviorRef{Name: "Workflow", Config: json.RawMessage(`{"states":["open","done"],"transitions":[{"from":"open","to":"done"}]}`)}
	r := Run(behaviorSchema(workflow, ir.BehaviorRef{Name: "Comments"}, ir.BehaviorRef{Name: "Revisions"}), Input{Registry: core})
	if got := errorStrings(r); len(got) != 0 {
		t.Fatalf("errors = %v, want none", got)
	}

	r = Run(behaviorSchema(ir.BehaviorRef{Name: "Pallet"}), Input{Registry: core})
	if want := `type Item: behavior "Pallet" is not a registered behavior (registered: Approvals, Assignment, Blueprint, Branches, Budget, Calendar, Comments, Constants, Dependencies, Derived, Holds, Lease, Links, Numbering, Payments, Presence, Provenance, Queue, Reactions, Retries, Revisions, Rollups, Search, StateRules, Stock, Timers, Variants, Workflow)`; !hasError(r, want) {
		t.Fatalf("errors = %v, want %q", errorStrings(r), want)
	}

	// Branches and Lease compose on one type: Branches points the release
	// pointer with releaseCommit and Lease ends a lease with release, so no
	// operation name is added twice.
	branches := ir.BehaviorRef{Name: "Branches", Config: json.RawMessage(`{"kinds":{"step":{"type":"Step"}}}`)}
	r = Run(behaviorSchema(branches, ir.BehaviorRef{Name: "Lease"}), Input{Registry: core})
	if got := errorStrings(r); len(got) != 0 {
		t.Fatalf("Branches and Lease: errors = %v, want none", got)
	}

	schema := behaviorSchema(workflow)
	schema.Types["Item"].Fields = append(schema.Types["Item"].Fields, &ir.FieldDef{Name: "status", TypeRef: ir.TypeRef{Name: "string"}})
	if got := errorStrings(Run(schema, Input{Registry: core})); len(got) != 0 {
		t.Fatalf("an own status beside Workflow's: errors = %v, want none", got)
	}
}

// Derived refuses a secret field as an input, at build (D74): a deriver
// sends its inputs to a model outside the engine.
func TestBehaviorsVerifyDerivedRefusesASecretInput(t *testing.T) {
	core := registry.New(naming.Naming{})
	derived := ir.BehaviorRef{Name: "Derived", Config: json.RawMessage(`{"fields":{"summary":{"from":["body","apiKey"],"instruction":"Summarize it.","model":"simple"}},"permission":"items.derive"}`)}
	schema := behaviorSchema(derived)
	schema.Types["Item"].Fields = append(schema.Types["Item"].Fields,
		&ir.FieldDef{Name: "body", TypeRef: ir.TypeRef{Name: "string"}},
		&ir.FieldDef{Name: "summary", TypeRef: ir.TypeRef{Name: "string"}},
		&ir.FieldDef{Name: "apiKey", TypeRef: ir.TypeRef{Name: "string"}, Secret: true})
	r := Run(schema, Input{Registry: core})
	if want := "type Item: Derived field summary is derived from apiKey, which is secret: a secret never goes to a model"; !hasError(r, want) {
		t.Fatalf("errors = %v, want %q", errorStrings(r), want)
	}
	schema.Types["Item"].Fields[len(schema.Types["Item"].Fields)-1].Secret = false
	if got := errorStrings(Run(schema, Input{Registry: core})); len(got) != 0 {
		t.Fatalf("an input that is not secret: errors = %v, want none", got)
	}
}

// fieldBehaviorRegistry is the core's registry, whose Workflow names its
// value field and types its fields (D103), with Pallet, which names none,
// Gauge, which leaves its reading untyped, and Dial, whose value field is
// named level and whose other field is levelSetAt.
func fieldBehaviorRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Naming{})
	for _, decl := range []string{
		`{"name":"Pallet","fields":[{"name":"onHand","type":"number"}]}`,
		`{"name":"Gauge","fields":[{"name":"level","type":"number"},{"name":"reading"}],"value":"level"}`,
		`{"name":"Dial","fields":[{"name":"level","type":"number"},{"name":"setAt","type":"Temporal.DateTime"}],"value":"level"}`,
	} {
		if err := reg.RegisterBehavior(registry.BehaviorSpec{Declaration: json.RawMessage(decl)}); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// orderTable is a DB schema's Order table with a title, whose fields refs
// anchor behaviors on (D103).
func orderTable(refs ...ir.BehaviorRef) *ir.Schema {
	schema := ir.NewSchema("orders", ir.SchemaKindDB)
	schema.Types["Order"] = &ir.TypeDef{
		Name:      "Order",
		Owner:     "src/order.schema.json",
		Role:      ir.RoleDBTable,
		Behaviors: refs,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	return schema
}

func onField(field, behavior, config string) ir.BehaviorRef {
	ref := ir.BehaviorRef{Name: behavior, Field: field}
	if config != "" {
		ref.Config = json.RawMessage(config)
	}
	return ref
}

const orderStates = `{"states":["draft","paid"],"transitions":[{"from":"draft","to":"paid"}]}`

// A behavior anchored on a field of a table (D103) is held to what the
// loader's pass that adds its fields needs: a DB table, a declaration that
// names its value field and types its fields, one use per field, both
// forms never on one type, and no field or column the pass adds that the
// table or another use already has. Two Workflows on two fields add one
// operation each under their own field, so they share no name.
func TestBehaviorsVerifyFieldAnchors(t *testing.T) {
	reg := fieldBehaviorRegistry(t)
	withField := func(schema *ir.Schema, fd *ir.FieldDef) *ir.Schema {
		schema.Types["Order"].Fields = append(schema.Types["Order"].Fields, fd)
		return schema
	}
	general := orderTable(onField("status", "Workflow", orderStates))
	general.Kind = ir.SchemaKindGeneral
	embedded := orderTable(onField("status", "Workflow", orderStates))
	embedded.Types["Order"].Role = ir.RoleEmbeddedStruct
	for _, test := range []struct {
		name   string
		schema *ir.Schema
		want   []string
	}{
		{"one field", orderTable(onField("status", "Workflow", orderStates)), nil},
		{"two fields", orderTable(onField("status", "Workflow", orderStates), onField("paymentStatus", "Workflow", orderStates)), nil},
		{"both forms", orderTable(ir.BehaviorRef{Name: "Comments"}, onField("status", "Workflow", orderStates)),
			[]string{"src/order.schema.json: type Order composes behavior Comments on the type (@behavior) and behavior Workflow on its field status: a type composes its behaviors on the type or on fields, never both (D103)"}},
		{"one field twice", orderTable(onField("status", "Workflow", orderStates), onField("status", "Workflow", orderStates)),
			[]string{"type Order anchors two behaviors on its field status"}},
		{"not a table", general,
			[]string{"type Order: field status is a Workflow, and a behavior is anchored on a field of a DB table, which Order is not (D103)"}},
		{"not a table role", embedded,
			[]string{"type Order: field status is a Workflow, and a behavior is anchored on a field of a DB table, which Order is not (D103)"}},
		{"config", orderTable(onField("status", "Workflow", `{"states":[]}`)),
			[]string{"type Order: field status: behavior Workflow config: "}},
		{"no value field", orderTable(onField("stock", "Pallet", "")),
			[]string{"type Order: field stock: behavior Pallet names no value field in its declaration, so it is not anchored on a field; compose it on the type (D103)"}},
		{"untyped field", orderTable(onField("gauge", "Gauge", "")),
			[]string{"type Order: field gauge: behavior Gauge declares field reading with no type, so it takes no column of a table; compose it on the type (D103)"}},
		{"anchor declared too", withField(orderTable(onField("status", "Workflow", orderStates)), &ir.FieldDef{Name: "status", TypeRef: ir.TypeRef{Name: "string"}}),
			[]string{"type Order: field status: behavior Workflow is anchored on status, which the type also declares as a field of its own: the anchor field is the behavior's value field, which the behavior adds (D103)"}},
		{"owned field declared too", withField(orderTable(onField("status", "Workflow", orderStates)), &ir.FieldDef{Name: "statusEnteredAt", TypeRef: ir.TypeRef{Name: "string"}}),
			[]string{"type Order: field status: behavior Workflow adds field statusEnteredAt for its enteredAt, which the type also declares; rename one of them (D103)"}},
		{"owned column taken", withField(orderTable(onField("status", "Workflow", orderStates)), &ir.FieldDef{Name: "entered", SQLName: "status_entered_at", TypeRef: ir.TypeRef{Name: "string"}}),
			[]string{"type Order: field status: behavior Workflow adds column status_entered_at for its enteredAt, the column of Order.entered; rename one of them (D103)"}},
		{"two uses add one field", orderTable(onField("level", "Dial", ""), onField("levelSetAt", "Dial", "")),
			[]string{"type Order: field level's Dial setAt and field levelSetAt's Dial level both add field levelSetAt; rename one of the fields (D103)"}},
		{"version column taken", withField(orderTable(onField("status", "Workflow", orderStates)), &ir.FieldDef{Name: "version", SQLName: "_version", TypeRef: ir.TypeRef{Name: "number"}}),
			[]string{"Order.version: its column is _version, the column a table that composes behaviors keeps its version in (D103); give it another name with @column"}},
		{"anchor not a name", orderTable(onField("status-2", "Workflow", orderStates)),
			[]string{`type Order: behavior Workflow is anchored on field "status-2", which is not a field name`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := Run(test.schema, Input{Registry: reg})
			got := errorStrings(r)
			if len(test.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("errors = %v, want none", got)
				}
				return
			}
			for _, want := range test.want {
				if !hasError(r, want) {
					t.Errorf("errors = %v, want one with %q", got, want)
				}
			}
		})
	}
}
