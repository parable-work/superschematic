package codegen

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// stateMachineSchema is a schema with two types that declare machines:
// Order, on an enum of its own whose members' values are not their names
// and on a nullable field typed by an enum a DB dependency owns, and
// Account, on an enum a General dependency owns.
func stateMachineSchema() (*ir.Schema, map[string]*ir.Schema) {
	schema := ir.NewSchema("shop-db", ir.SchemaKindDB)
	schema.Imports = []ir.Import{
		{Package: "@schemas/ledger-db", Types: []string{"Settlement"}},
		{Package: "@schemas/shop-common"},
	}
	schema.Enums["OrderStatus"] = &ir.EnumDef{Name: "OrderStatus", Values: []ir.EnumValueDef{
		{Name: "Open", SerializedAs: "open"},
		{Name: "InTransit", SerializedAs: "in_transit"},
		{Name: "Done", SerializedAs: "done", Comment: "Delivered."},
	}}
	schema.Types["Order"] = &ir.TypeDef{
		Name: "Order",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "status", TypeRef: ir.TypeRef{Name: "OrderStatus"}, Required: true},
			{Name: "settlement", TypeRef: ir.TypeRef{Name: "Settlement"}},
		},
		StateMachines: []*ir.StateMachine{
			{
				Field: "status",
				Transitions: []*ir.StateTransition{
					{From: "open", To: []string{"in_transit", "done"}},
					{From: "in_transit", To: []string{"done", "open"}},
				},
				Terminal: []string{"done"},
			},
			{
				Field:       "settlement",
				Transitions: []*ir.StateTransition{{From: "PENDING", To: []string{"SETTLED"}, ToNull: true}},
				Terminal:    []string{"SETTLED"},
				FromNull:    []string{"PENDING"},
			},
		},
	}
	schema.Types["Account"] = &ir.TypeDef{
		Name:   "Account",
		Role:   ir.RoleDBTable,
		Fields: []*ir.FieldDef{{Name: "standing", TypeRef: ir.TypeRef{Name: "Standing"}, Required: true}},
		StateMachines: []*ir.StateMachine{{
			Field:       "standing",
			Transitions: []*ir.StateTransition{{From: "good", To: []string{"closed"}}},
			Terminal:    []string{"closed"},
		}},
	}

	ledger := ir.NewSchema("ledger-db", ir.SchemaKindDB)
	ledger.Enums["Settlement"] = &ir.EnumDef{Name: "Settlement", Values: []ir.EnumValueDef{{Name: "PENDING"}, {Name: "SETTLED"}}}
	// An enum of a DB dependency the import does not name is not the
	// schema's to read.
	ledger.Enums["Standing"] = &ir.EnumDef{Name: "Standing", Values: []ir.EnumValueDef{{Name: "Wrong", SerializedAs: "good"}}}
	common := ir.NewSchema("shop-common", ir.SchemaKindGeneral)
	common.Enums["Standing"] = &ir.EnumDef{Name: "Standing", Values: []ir.EnumValueDef{
		{Name: "Good", SerializedAs: "good"},
		{Name: "Closed", SerializedAs: "closed"},
	}}
	return schema, map[string]*ir.Schema{"ledger-db": ledger, "shop-common": common}
}

func TestExtractStateMachines(t *testing.T) {
	schema, deps := stateMachineSchema()
	got, err := ExtractStateMachines(schema, deps)
	if err != nil {
		t.Fatalf("ExtractStateMachines: %v", err)
	}

	open := EnumValueInfo{Name: "Open", Value: "open"}
	inTransit := EnumValueInfo{Name: "InTransit", Value: "in_transit"}
	done := EnumValueInfo{Name: "Done", Value: "done", Comment: "Delivered."}
	pending := EnumValueInfo{Name: "PENDING", Value: "PENDING"}
	settled := EnumValueInfo{Name: "SETTLED", Value: "SETTLED"}
	good := EnumValueInfo{Name: "Good", Value: "good"}
	closed := EnumValueInfo{Name: "Closed", Value: "closed"}
	// The types in name order, a type's machines as it declares them.
	want := []StateMachineInfo{
		{
			TypeName: "Account", FieldName: "standing", EnumName: "Standing", EnumOwner: "shop-common",
			Transitions: []StateTransitionInfo{{From: good, To: []EnumValueInfo{closed}}},
			Terminal:    []EnumValueInfo{closed},
		},
		{
			TypeName: "Order", FieldName: "status", EnumName: "OrderStatus",
			Transitions: []StateTransitionInfo{
				{From: open, To: []EnumValueInfo{inTransit, done}},
				{From: inTransit, To: []EnumValueInfo{done, open}},
			},
			Terminal: []EnumValueInfo{done},
		},
		{
			TypeName: "Order", FieldName: "settlement", EnumName: "Settlement", EnumOwner: "ledger-db", Nullable: true,
			Transitions: []StateTransitionInfo{{From: pending, To: []EnumValueInfo{settled}, ToNull: true}},
			Terminal:    []EnumValueInfo{settled},
			FromNull:    []EnumValueInfo{pending},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractStateMachines:\n got %+v\nwant %+v", got, want)
	}
}

func TestExtractStateMachinesWithoutOne(t *testing.T) {
	schema := ir.NewSchema("plain", ir.SchemaKindGeneral)
	schema.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleEmbeddedStruct}
	got, err := ExtractStateMachines(schema, nil)
	if err != nil || got != nil {
		t.Fatalf("ExtractStateMachines = %+v, %v; want none", got, err)
	}
}

// A machine verification would have refused names what no helper could.
func TestExtractStateMachinesRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(schema *ir.Schema, deps map[string]*ir.Schema)
		want   string
	}{
		{
			name: "a field the type lacks",
			mutate: func(schema *ir.Schema, _ map[string]*ir.Schema) {
				schema.Types["Order"].StateMachines[0].Field = "state"
			},
			want: "@stateMachine on Order.state: Order has no field state",
		},
		{
			name: "a state that is no member",
			mutate: func(schema *ir.Schema, _ map[string]*ir.Schema) {
				schema.Types["Order"].StateMachines[0].Terminal = []string{"Done"}
			},
			want: `@stateMachine on Order.status: "Done" is no member of OrderStatus`,
		},
		{
			name: "a field no enum types",
			mutate: func(schema *ir.Schema, _ map[string]*ir.Schema) {
				schema.Types["Order"].Fields[0].TypeRef = ir.TypeRef{Name: "string"}
			},
			want: "status is typed by string, which is no enum",
		},
		{
			name: "a list of an enum",
			mutate: func(schema *ir.Schema, _ map[string]*ir.Schema) {
				schema.Types["Order"].Fields[0].TypeRef.IsArray = true
			},
			want: "status is typed by OrderStatus, which is no enum",
		},
		{
			name: "an enum of a dependency that is not loaded",
			mutate: func(_ *ir.Schema, deps map[string]*ir.Schema) {
				delete(deps, "ledger-db")
			},
			want: "settlement is typed by Settlement, which is no enum",
		},
		{
			name: "an enum a DB dependency's import does not name",
			mutate: func(schema *ir.Schema, _ map[string]*ir.Schema) {
				schema.Imports[0].Types = nil
			},
			want: "settlement is typed by Settlement, which is no enum",
		},
		{
			name: "a type of the schema that shadows the dependency's enum",
			mutate: func(schema *ir.Schema, _ map[string]*ir.Schema) {
				schema.Types["Standing"] = &ir.TypeDef{Name: "Standing", Role: ir.RoleEmbeddedStruct}
			},
			want: "standing is typed by Standing, which is no enum",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema, deps := stateMachineSchema()
			tc.mutate(schema, deps)
			_, err := ExtractStateMachines(schema, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ExtractStateMachines error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
