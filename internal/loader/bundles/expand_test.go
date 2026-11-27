package bundles

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// table is a DB schema of one table that implements Assignment, with the
// uses given.
func table(uses ...ir.BehaviorRef) *ir.Schema {
	return &ir.Schema{Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{
		"Chore": {
			Name: "Chore", Role: ir.RoleDBTable,
			Fields:    []*ir.FieldDef{{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true}},
			Bundles:   []ir.BundleRef{{Name: "Assignment", Settings: json.RawMessage(`{"take":"chores.take"}`)}},
			Behaviors: uses,
		},
	}}
}

// TestExpandAddsTheDeclarationsParts (D103, step 5): a data form's bundle
// gains its declaration's parts, its settings substituted, after the
// table's own uses; expanding again adds nothing.
func TestExpandAddsTheDeclarationsParts(t *testing.T) {
	reg := registry.New(naming.Default())
	own := ir.BehaviorRef{Name: "Workflow", Field: "status", Config: json.RawMessage(`{"states":["a","b"],"transitions":[{"from":"a","to":"b"}]}`)}
	schema := table(own)
	for range 2 {
		if err := Expand(schema, reg); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := json.Marshal(schema.Types["Chore"].Behaviors)
	want := `[{"name":"Workflow","field":"status","config":{"states":["a","b"],"transitions":[{"from":"a","to":"b"}]}},` +
		`{"name":"Seats","implements":true,"config":{"seats":{"assignee":{"max":1,"onFull":"refuse","take":"chores.take"}}},"bundle":"Assignment"}]`
	if string(got) != want {
		t.Errorf("uses = %s\nwant %s", got, want)
	}
}

// TestExpandHoldsTheTypeScriptPartsToTheDeclaration (D103, step 5): the
// parts the TypeScript reader recorded for a registered bundle, read from
// its alias through the checker, must be the declaration's: the same
// uses, in order, with the same configs, so the two definitions of a core
// bundle cannot drift apart.
func TestExpandHoldsTheTypeScriptPartsToTheDeclaration(t *testing.T) {
	reg := registry.New(naming.Default())
	recorded := ir.BehaviorRef{Name: "Seats", Implements: true, Bundle: "Assignment",
		Config: json.RawMessage(`{"seats":{"assignee":{"max":2,"onFull":"refuse","take":"chores.take"}}}`)}
	err := Expand(table(recorded), reg)
	want := `type Chore: bundle Assignment: part Seats: the TypeScript form's config {"seats":{"assignee":{"max":2,"onFull":"refuse","take":"chores.take"}}} is not the registered declaration's {"seats":{"assignee":{"max":1,"onFull":"refuse","take":"chores.take"}}}`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v\nwant %s", err, want)
	}
	other := ir.BehaviorRef{Name: "StateRules", Implements: true, Bundle: "Assignment", Config: json.RawMessage(`{"states":{"a":{}}}`)}
	err = Expand(table(other), reg)
	if err == nil || !strings.Contains(err.Error(), "the TypeScript form's parts (StateRules) are not the registered declaration's (Seats)") {
		t.Errorf("error = %v", err)
	}
	same := ir.BehaviorRef{Name: "Seats", Implements: true, Bundle: "Assignment",
		Config: json.RawMessage(`{"seats":{"assignee":{"max":1,"onFull":"refuse","take":"chores.take"}}}`)}
	schema := table(same)
	if err := Expand(schema, reg); err != nil {
		t.Fatal(err)
	}
	if n := len(schema.Types["Chore"].Behaviors); n != 1 {
		t.Errorf("uses = %d, want the one recorded", n)
	}
}

// TestExpandRefusesAnUnregisteredBundle: a bundle the registry does not
// declare is refused with the registered names.
func TestExpandRefusesAnUnregisteredBundle(t *testing.T) {
	schema := table()
	schema.Types["Chore"].Bundles = []ir.BundleRef{{Name: "Rental"}}
	err := Expand(schema, registry.New(naming.Default()))
	if err == nil || err.Error() != "type Chore: bundle Rental is not a registered bundle (registered: Assignment, Barrier, Mailbox, Reservation)" {
		t.Errorf("error = %v", err)
	}
}
