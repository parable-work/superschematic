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
