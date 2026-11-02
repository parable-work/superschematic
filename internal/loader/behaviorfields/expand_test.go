package behaviorfields

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

func orders(refs ...ir.BehaviorRef) *ir.Schema {
	schema := ir.NewSchema("orders", ir.SchemaKindDB)
	schema.Types["Order"] = &ir.TypeDef{
		Name:      "Order",
		Role:      ir.RoleDBTable,
		Behaviors: refs,
		Fields:    []*ir.FieldDef{{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
	}
	return schema
}

func workflowOn(field string) ir.BehaviorRef {
	return ir.BehaviorRef{Name: "Workflow", Field: field, Config: json.RawMessage(`{"states":["draft","paid"],"transitions":[{"from":"draft","to":"paid"}]}`)}
}

// Each field-anchored use adds its declared fields after the table's own,
// named and columned by the layout rule, typed by the declaration, the
// value field required and the rest optional, each marked with the use
// that owns it; the table takes @optimistic's _version; and the scalars
// the fields name come back for the loader to hydrate (D103).
func TestExpand(t *testing.T) {
	reg := registry.New(naming.Default())
	schema := orders(workflowOn("status"), workflowOn("shipStatus"))
	scalars, err := Expand(schema, reg)
	if err != nil {
		t.Fatal(err)
	}
	order := schema.Types["Order"]
	var got []string
	for _, fd := range order.Fields {
		entry := fd.Name + ":" + fd.TypeRef.Name
		if fd.Required {
			entry += "!"
		}
		if fd.SQLName != "" {
			entry += "@" + fd.SQLName
		}
		if fd.Behavior != nil {
			entry += "<" + fd.Behavior.Use + "." + fd.Behavior.Field + ">"
		}
		got = append(got, entry)
	}
	want := "title:string! status:string!<status.status> statusEnteredAt:Temporal.DateTime<status.enteredAt> " +
		"shipStatus:string!<shipStatus.status> shipStatusEnteredAt:Temporal.DateTime<shipStatus.enteredAt>"
	if strings.Join(got, " ") != want {
		t.Errorf("fields = %s\nwant %s", strings.Join(got, " "), want)
	}
	if !order.Optimistic {
		t.Error("the table is not @optimistic")
	}
	if len(scalars) != 1 || scalars[0] != "Temporal.DateTime" || schema.Scalars["Temporal.DateTime"] == nil {
		t.Errorf("scalars = %v, schema.Scalars = %v", scalars, schema.Scalars)
	}
	if !order.Fields[1].Behavior.Value || order.Fields[2].Behavior.Value {
		t.Error("only the value field is the use's value")
	}

	// An IR read back holds the fields already, and a second pass keeps
	// them as they are.
	if _, err := Expand(schema, reg); err != nil {
		t.Fatal(err)
	}
	if len(order.Fields) != 5 {
		t.Errorf("a second pass left %d fields, want 5", len(order.Fields))
	}
}

// A @versioned table keeps its own versioning, which implies _version; a
// schema of another kind, and a table with no field use, are left alone;
// and a field of the table's own where the pass would add one is a defect
// the verification pass rules out.
func TestExpandLeavesAlone(t *testing.T) {
	reg := registry.New(naming.Default())
	versioned := orders(workflowOn("status"))
	versioned.Types["Order"].Versioned = true
	if _, err := Expand(versioned, reg); err != nil {
		t.Fatal(err)
	}
	if versioned.Types["Order"].Optimistic {
		t.Error("a @versioned table was made @optimistic")
	}

	general := orders(workflowOn("status"))
	general.Kind = ir.SchemaKindGeneral
	if _, err := Expand(general, reg); err != nil || len(general.Types["Order"].Fields) != 1 {
		t.Errorf("a General schema's type was expanded: %v", err)
	}
	onType := orders(ir.BehaviorRef{Name: "Comments"})
	if _, err := Expand(onType, reg); err != nil || len(onType.Types["Order"].Fields) != 1 || onType.Types["Order"].Optimistic {
		t.Errorf("a type-anchored use was expanded: %v", err)
	}

	taken := orders(workflowOn("status"))
	taken.Types["Order"].Fields = append(taken.Types["Order"].Fields, &ir.FieldDef{Name: "statusEnteredAt", TypeRef: ir.TypeRef{Name: "string"}})
	if _, err := Expand(taken, reg); err == nil || !strings.Contains(err.Error(), "behavior Workflow adds field statusEnteredAt, which the type already declares") {
		t.Errorf("Expand = %v", err)
	}
}

// A table named after a field its sub-table declares keeps both: the
// sub-table names its row Outcome by outcomeRow beside Timers' outcome,
// and a table named Name by nameRow beside the key name (D103).
func TestASubTableNamesItsRowApartFromItsFields(t *testing.T) {
	reg := registry.New(naming.Default())
	for _, c := range []struct{ table, row string }{{"Ticket", "ticket"}, {"Outcome", "outcomeRow"}, {"Name", "nameRow"}, {"DueAt", "dueAtRow"}} {
		schema := ir.NewSchema("support", ir.SchemaKindDB)
		schema.Types[c.table] = &ir.TypeDef{
			Name: c.table,
			Role: ir.RoleDBTable,
			Behaviors: []ir.BehaviorRef{{Name: "Timers", Implements: true,
				Config: json.RawMessage(`{"deadlines":{"remind":{"afterMs":1000,"start":{"field":"dueOn"}}}}`)}},
			Fields: []*ir.FieldDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true, Required: true},
				{Name: "dueOn", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
			},
		}
		if _, err := Expand(schema, reg); err != nil {
			t.Fatalf("%s: %v", c.table, err)
		}
		sub := schema.Types[c.table+"TimersDeadlines"]
		if sub == nil {
			t.Fatalf("%s: no sub-table among %v", c.table, schema.Types)
		}
		var names []string
		for _, fd := range sub.Fields {
			names = append(names, fd.Name)
		}
		want := c.row + " name baseAt armedFrom dueAt fired firedAt outcome"
		if strings.Join(names, " ") != want || sub.BehaviorTable.RowField != c.row || sub.Fields[0].Relation == nil {
			t.Errorf("%s: the sub-table's fields = %s, its row field %s, want %s", c.table, strings.Join(names, " "), sub.BehaviorTable.RowField, want)
		}
	}
}

// projects is a DB schema whose Project's Reactions hears its Tasks through
// task.project (D103).
func projects() *ir.Schema {
	schema := ir.NewSchema("projects", ir.SchemaKindDB)
	schema.Types["Project"] = &ir.TypeDef{
		Name: "Project",
		Role: ir.RoleDBTable,
		Behaviors: []ir.BehaviorRef{
			{Name: "Reactions", Implements: true, Config: json.RawMessage(`{"rules":[{"then":{"transition":"paid"},"when":{"allTerminal":{"link":"project","schema":"Task"}}}]}`)},
			workflowOn("status"),
		},
		Fields: []*ir.FieldDef{{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true, Required: true}},
	}
	schema.Types["Task"] = &ir.TypeDef{
		Name:      "Task",
		Role:      ir.RoleDBTable,
		Behaviors: []ir.BehaviorRef{workflowOn("status")},
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true, Required: true},
			{Name: "project", TypeRef: ir.TypeRef{Name: "Project"}, Required: true, Relation: &ir.RelationDef{Type: "Project"}},
		},
	}
	return schema
}

// A table whose writes are heard gets its change queue: by a behavior of
// its own that hears changes (Project's Reactions), or by another table's
// through a relation of it (Task, heard by Project), whose marker names
// who hears it; a table nothing hears gets none, and a second pass keeps
// the queue (D103).
func TestExpandChangeQueues(t *testing.T) {
	reg := registry.New(naming.Default())
	schema := projects()
	if heard := HeardBy(schema); len(heard) != 1 || strings.Join(heard["Task"], ",") != "Project" {
		t.Errorf("HeardBy = %v, want Task heard by Project", heard)
	}
	if hearing := Hearing(schema.Types["Project"], reg); strings.Join(hearing, ",") != "Reactions" {
		t.Errorf("Hearing(Project) = %v", hearing)
	}
	if _, err := Expand(schema, reg); err != nil {
		t.Fatal(err)
	}
	for _, want := range []ir.BehaviorQueue{
		{Row: "Project", Kind: ir.BehaviorQueueChanges, Behaviors: []string{"Reactions"}},
		{Row: "Task", Kind: ir.BehaviorQueueChanges, HeardBy: []string{"Project"}},
	} {
		queue := schema.Types[want.Row+"Change"]
		if queue == nil || !queue.BehaviorQueue.Same(&want) || queue.Queue == nil || queue.Queue.Key != "rowKey" || !queue.Queue.Serialize {
			t.Errorf("%s's change queue = %+v", want.Row, queue)
		}
	}
	if _, err := Expand(schema, reg); err != nil || len(schema.Types) != 4 {
		t.Errorf("a second pass = %v, types %d, want the 4 kept", err, len(schema.Types))
	}

	quiet := orders(workflowOn("status"))
	if _, err := Expand(quiet, reg); err != nil || quiet.Types["OrderChange"] != nil {
		t.Errorf("a table nothing hears got a change queue: %v", err)
	}

	taken := projects()
	taken.Types["TaskChange"] = &ir.TypeDef{Name: "TaskChange", Queue: &ir.QueueDef{}}
	if _, err := Expand(taken, reg); err == nil || err.Error() != "type Task: its writes are heard (Project's Reactions), whose queue the build adds as TaskChange, which the schema also declares" {
		t.Errorf("Expand of a taken queue name = %v", err)
	}
	keys := projects()
	keys.Types["Task"].Fields = append(keys.Types["Task"].Fields, &ir.FieldDef{Name: "code", TypeRef: ir.TypeRef{Name: "string"}, Key: true})
	keys.Types["Task"].Fields[0].Key = true
	if _, err := Expand(keys, reg); err == nil || err.Error() != "type Task: its writes are heard (Project's Reactions), and it has no single key field" {
		t.Errorf("Expand of a table with two key fields = %v", err)
	}
}
