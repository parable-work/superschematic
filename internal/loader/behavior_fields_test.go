package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/behaviorfields"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// A JSON schema's table whose fields are behaviors (D103) loads with the
// fields the behaviors own added after its own, the anchor field among
// them as the value field, and @optimistic's _version: the loader adds
// them before the verification pass's field checks, whatever form the
// schema is written in. The IR it gives verifies again, round-trips through its JSON, and the
// pass run again on that IR adds nothing.
func TestLoadAddsBehaviorFields(t *testing.T) {
	reg := registry.New(naming.Default())
	schema, err := LoadService(filepath.Join("tsreader", "testdata", "services", "fixture-workflow-fields-db"), WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	shipment := schema.Types["Shipment"]
	var names []string
	for _, fd := range shipment.Fields {
		names = append(names, fd.Name)
	}
	if got, want := names, []string{"id", "reference", "status", "statusEnteredAt", "paymentStatus", "paymentStatusEnteredAt"}; !slices.Equal(got, want) {
		t.Fatalf("Shipment's fields = %v, want %v", got, want)
	}
	if !shipment.Optimistic || shipment.IsEngineType() {
		t.Errorf("Shipment: optimistic %v, engine type %v; want a table with _version", shipment.Optimistic, shipment.IsEngineType())
	}
	if fd := shipment.Fields[4]; fd.Behavior == nil || fd.Behavior.Use != "paymentStatus" || !fd.Behavior.Value || fd.SQLName != "" {
		t.Errorf("paymentStatus = %+v, behavior %+v", fd, fd.Behavior)
	}
	if schema.Scalars["Temporal.DateTime"] == nil || schema.Scalars["Temporal.DateTime"].Primitive == "" {
		t.Error("Temporal.DateTime is not hydrated")
	}

	// The verification pass takes the loaded IR as it took the schema.
	if res := verify.Run(schema, verify.Input{Registry: reg}); res.Err() != nil {
		t.Fatalf("verify the loaded IR: %v", res.Err())
	}

	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var back ir.Schema
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if _, err := behaviorfields.Expand(&back, reg); err != nil {
		t.Fatal(err)
	}
	if got := len(back.Types["Shipment"].Fields); got != len(shipment.Fields) {
		t.Errorf("the pass on the IR read back left %d fields, want %d", got, len(shipment.Fields))
	}
	if fd := back.Types["Shipment"].Fields[3]; fd.Behavior == nil || fd.Behavior.Field != "enteredAt" {
		t.Errorf("statusEnteredAt read back = %+v", fd)
	}
}

// The fields a behavior owns join their table before the verification pass
// (D103), so every check that resolves a field by its name takes one as it
// takes the table's own: an index and a uniqueness over Order's status and
// statusEnteredAt, a display's summary fields, a projection's columns read
// from them, and an @enqueueOnChange's fields, conditions and message. A
// name no field holds is still refused.
func TestOwnedFieldsAreTheTablesToEveryCheck(t *testing.T) {
	fixture := filepath.Join("tsreader", "testdata", "services", "fixture-workflow-fields-db")
	write := func(t *testing.T, edit func(types map[string]any)) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(fixture, "src", "orders.schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		edit(doc["types"].(map[string]any))
		schema, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		config, err := os.ReadFile(filepath.Join(fixture, "schema.config.json"))
		if err != nil {
			t.Fatal(err)
		}
		return writeService(t, map[string]string{"schema.config.json": string(config), "src/orders.schema.json": string(schema)})
	}
	features := func(types map[string]any) {
		order := types["Order"].(map[string]any)
		order["indexes"] = []any{
			map[string]any{"keys": []any{"status"}},
			map[string]any{"keys": []any{"title", "statusEnteredAt"}, "unique": true, "name": "entered"},
		}
		order["display"] = map[string]any{"titleField": "title", "summaryFields": []any{"status", "statusEnteredAt"}}
		order["enqueueOnChange"] = []any{map[string]any{
			"queue":   "OrderMoved",
			"on":      []any{"update"},
			"fields":  []any{"status"},
			"when":    []any{map[string]any{"field": "status", "equals": "paid"}},
			"message": map[string]any{"orderId": "id", "status": "status"},
		}}
		types["OrderMoved"] = map[string]any{
			"name":  "OrderMoved",
			"role":  "EmbeddedStruct",
			"queue": map[string]any{},
			"fields": []any{
				map[string]any{"name": "orderId", "typeRef": map[string]any{"name": "Identity.UUID"}, "required": true},
				map[string]any{"name": "status", "typeRef": map[string]any{"name": "string"}, "required": true},
			},
		}
		types["OrderStatus"] = map[string]any{
			"name": "OrderStatus",
			"role": "Projection",
			"fields": []any{
				map[string]any{"name": "id", "typeRef": map[string]any{"name": "Identity.UUID"}, "required": true},
				map[string]any{"name": "status", "typeRef": map[string]any{"name": "string"}, "required": true, "projectedFrom": "base.status"},
				map[string]any{"name": "enteredAt", "typeRef": map[string]any{"name": "Temporal.DateTime"}, "projectedFrom": "base.statusEnteredAt"},
			},
			"projection": map[string]any{"name": "order_status", "migration": "20261030000000", "source": "Order"},
		}
	}

	schema, err := LoadService(write(t, features), WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatalf("a schema whose features name owned fields: %v", err)
	}
	order := schema.Types["Order"]
	if got := order.Indexes[1].Keys; !slices.Equal(got, []string{"title", "statusEnteredAt"}) {
		t.Errorf("Order's unique index keys = %v", got)
	}
	if got := order.EnqueueOnChange[0].Fields; !slices.Equal(got, []string{"status"}) {
		t.Errorf("Order's @enqueueOnChange fields = %v", got)
	}
	if got := schema.Types["OrderStatus"].Fields[1].ProjectedFrom; got != "base.status" {
		t.Errorf("OrderStatus.status reads %q", got)
	}

	for _, tc := range []struct {
		name, want string
		edit       func(types map[string]any)
	}{
		{"an index", `key "statusAt" names no field of Order`, func(types map[string]any) {
			types["Order"].(map[string]any)["indexes"] = []any{map[string]any{"keys": []any{"statusAt"}}}
		}},
		{"a projection's column", `Order has no field "statusAt"`, func(types map[string]any) {
			features(types)
			column := types["OrderStatus"].(map[string]any)["fields"].([]any)[1].(map[string]any)
			column["projectedFrom"] = "base.statusAt"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadService(write(t, tc.edit), WithRegistry(registry.New(naming.Default())))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
