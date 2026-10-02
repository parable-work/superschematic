package verify

import (
	"encoding/json"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// behaviorRegistry registers test behaviors under core names, as the
// registry allows for the core: Stock takes a required config, adds a field
// and an operation; Audit requires Stock; Clearance conflicts with Stock;
// Shelved adds Stock's field; Recount adds Stock's operation.
func behaviorRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Naming{})
	op := func(name string) string {
		return `{"name":"` + name + `","paramsSchema":{"type":"object","additionalProperties":false},"resultSchema":{"type":"object"}}`
	}
	for _, decl := range []string{
		`{"name":"Stock","configSchema":{"type":"object","required":["aisles"],"properties":{"aisles":{"type":"integer","minimum":1}}},` +
			`"fields":[{"name":"onHand"}],"operations":[` + op("restock") + `]}`,
		`{"name":"Audit","requires":["Stock"],"fields":[{"name":"auditedAt"}],"operations":[` + op("audit") + `]}`,
		`{"name":"Clearance","conflicts":["Stock"],"fields":[{"name":"markdown"}]}`,
		`{"name":"Shelved","fields":[{"name":"onHand"},{"name":"shelf"}]}`,
		`{"name":"Recount","operations":[` + op("restock") + `]}`,
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
	return ir.BehaviorRef{Name: "Stock", Config: json.RawMessage(config)}
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
			[]string{`src/item.schema.json: type Item: behavior "Ghost" is not a registered behavior (registered: Assignment, Audit, Blueprint, Clearance, Comments, Dependencies, Lease, Links, Presence, Queue, Reactions, Recount, Revisions, Rollups, Search, Shelved, Stock, Workflow)`}},
		{"config rejected", []ir.BehaviorRef{stock(`{"aisles":0}`)},
			[]string{"src/item.schema.json: type Item: behavior Stock config: "}},
		{"config missing", []ir.BehaviorRef{{Name: "Stock"}},
			[]string{"src/item.schema.json: type Item: behavior Stock config: "}},
		{"config on a behavior without one", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Audit", Config: json.RawMessage(`{"often":true}`)}},
			[]string{"src/item.schema.json: type Item: behavior Audit takes no config"}},
		{"listed twice", []ir.BehaviorRef{stock(`{"aisles":2}`), stock(`{"aisles":3}`)},
			[]string{"src/item.schema.json: type Item lists behavior Stock twice"}},
		{"missing requirement", []ir.BehaviorRef{{Name: "Audit"}},
			[]string{"src/item.schema.json: type Item: behavior Audit requires behavior Stock, which the type does not list"}},
		{"conflict", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Clearance"}},
			[]string{"src/item.schema.json: type Item: behavior Clearance conflicts with behavior Stock, which the type also lists"}},
		{"field collides with the type's own", []ir.BehaviorRef{{Name: "Shelved"}},
			[]string{"src/item.schema.json: type Item: behavior Shelved adds field shelf, which the type declares"}},
		{"field collides with another behavior's", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Shelved"}},
			[]string{"src/item.schema.json: type Item: behaviors Stock and Shelved both add field onHand"}},
		{"operation collides", []ir.BehaviorRef{stock(`{"aisles":2}`), {Name: "Recount"}},
			[]string{"src/item.schema.json: type Item: behaviors Stock and Recount both add operation restock"}},
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

// A behavior field may not take the JSON key of one of the type's own
// fields any more than its name, as the engine refuses it: an instance's
// JSON holds the behavior's fields beside the type's own. The wording is
// the engine's for both.
func TestBehaviorsVerifyFieldJSONKey(t *testing.T) {
	reg := behaviorRegistry(t)
	for _, field := range []*ir.FieldDef{
		{Name: "onHand", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "unitsOnHand", JSONTag: "onHand", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "onHand", JSONTag: "units_on_hand", TypeRef: ir.TypeRef{Name: "string"}},
	} {
		schema := behaviorSchema(stock(`{"aisles":2}`))
		schema.Types["Item"].Fields = append(schema.Types["Item"].Fields, field)
		r := Run(schema, Input{Registry: reg})
		want := "src/item.schema.json: type Item: behavior Stock adds field onHand, which the type declares"
		if got := errorStrings(r); len(got) != 1 || got[0] != want {
			t.Errorf("field %s (jsonTag %q): errors = %v, want [%q]", field.Name, field.JSONTag, got, want)
		}
	}

	// A JSON key that is no behavior field's collides with nothing.
	schema := behaviorSchema(stock(`{"aisles":2}`))
	schema.Types["Item"].Fields = append(schema.Types["Item"].Fields, &ir.FieldDef{Name: "count", JSONTag: "item_count", TypeRef: ir.TypeRef{Name: "string"}})
	if got := errorStrings(Run(schema, Input{Registry: reg})); len(got) != 0 {
		t.Fatalf("errors = %v, want none", got)
	}
}

// The core registry accepts the core's behaviors with no extension linked,
// and holds them to the same rules: a behavior it does not register fails
// the load, and so does a core behavior's field the type already declares.
func TestBehaviorsVerifyWithTheCore(t *testing.T) {
	core := registry.New(naming.Naming{})
	workflow := ir.BehaviorRef{Name: "Workflow", Config: json.RawMessage(`{"states":["open","done"],"transitions":[{"from":"open","to":"done"}]}`)}
	r := Run(behaviorSchema(workflow, ir.BehaviorRef{Name: "Comments"}, ir.BehaviorRef{Name: "Revisions"}), Input{Registry: core})
	if got := errorStrings(r); len(got) != 0 {
		t.Fatalf("errors = %v, want none", got)
	}

	r = Run(behaviorSchema(ir.BehaviorRef{Name: "Stock"}), Input{Registry: core})
	if want := `type Item: behavior "Stock" is not a registered behavior (registered: Assignment, Blueprint, Comments, Dependencies, Lease, Links, Presence, Queue, Reactions, Revisions, Rollups, Search, Workflow)`; !hasError(r, want) {
		t.Fatalf("errors = %v, want %q", errorStrings(r), want)
	}

	schema := behaviorSchema(workflow)
	schema.Types["Item"].Fields = append(schema.Types["Item"].Fields, &ir.FieldDef{Name: "status", TypeRef: ir.TypeRef{Name: "string"}})
	r = Run(schema, Input{Registry: core})
	if want := "type Item: behavior Workflow adds field status, which the type declares"; !hasError(r, want) {
		t.Fatalf("errors = %v, want %q", errorStrings(r), want)
	}
}
