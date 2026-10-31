package loader

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/behaviorops"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// workflowAPIFixture is the TypeScript-authored API whose
// @behaviorOperations sets name fixture-workflow-db's tables, its one DB
// dependency and its sibling (D103).
const workflowAPIFixture = "tsreader/testdata/services/fixture-workflow-api"

// TestBehaviorOperationsExpand: the API loads with its @behaviorOperations
// sets filled from its database, which the loader reads once, operation by
// operation as the contract of D103's step 2 lists them: the route, the
// arguments, the result, the clause, the docs, the tool and the marker,
// and the types and enums they name, each the loader's.
func TestBehaviorOperationsExpand(t *testing.T) {
	db, err := LoadService(filepath.FromSlash("tsreader/testdata/services/fixture-workflow-db"))
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	schema, err := LoadService(filepath.FromSlash(workflowAPIFixture), WithDependencyLoader(func(name string) (*ir.Schema, error) {
		asked = append(asked, name)
		return db, nil
	}))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if !reflect.DeepEqual(asked, []string{"fixture-workflow-db"}) {
		t.Errorf("the dependency loader was asked for %v, want fixture-workflow-db once", asked)
	}

	sets := map[string]*ir.OperationSet{}
	for _, set := range schema.OperationSets {
		sets[set.Name] = set
	}
	type want struct {
		set, name, path, input, result, handle, title, capability, use string
		auth                                                           bool
		permissions, configPermissions                                 []string
	}
	for _, w := range []want{
		{"OrderActions", "statusTransition", "order/{id}/status/transition", "OrderStatusTransitionInput", "OrderStatusTransitionResult",
			"order_status_transition", "Order status: transition", "order.status.transition", "status", false, []string{"orders.write"}, []string{"orders.ship"}},
		{"ShipmentActions", "statusTransition", "shipment/{id}/status/transition", "ShipmentStatusTransitionInput", "ShipmentStatusTransitionResult",
			"shipment_status_transition", "Shipment status: transition", "shipment.status.transition", "status", true, nil, nil},
		{"ShipmentActions", "paymentStatusTransition", "shipment/{id}/payment-status/transition", "ShipmentPaymentStatusTransitionInput", "ShipmentPaymentStatusTransitionResult",
			"shipment_payment_status_transition", "Shipment payment status: transition", "shipment.payment-status.transition", "paymentStatus", true, nil, []string{"payments.refund"}},
	} {
		set := sets[w.set]
		var op *ir.FieldDef
		for _, each := range set.Operations {
			if each.Name == w.name {
				op = each
			}
		}
		if op == nil {
			t.Errorf("%s has no operation %s: %+v", w.set, w.name, set.Operations)
			continue
		}
		where := w.set + "." + w.name
		if op.HTTPMethod != "POST" || op.RestPath != w.path || op.Origin != ir.OriginBehavior || !op.Required {
			t.Errorf("%s: %s %s, origin %q, required %v", where, op.HTTPMethod, op.RestPath, op.Origin, op.Required)
		}
		wantArgs := []*ir.ArgumentDef{
			{Name: "id", Comment: "The key of the " + strings.TrimSuffix(w.set, "Actions") + " row the operation runs on.", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: ir.BehaviorInputArgument, TypeRef: ir.TypeRef{Name: w.input}, Required: true},
		}
		if !reflect.DeepEqual(op.Arguments, wantArgs) {
			t.Errorf("%s: arguments = %s", where, marshal(t, op.Arguments))
		}
		if op.TypeRef != (ir.TypeRef{Name: w.result}) {
			t.Errorf("%s: result = %+v, want %s", where, op.TypeRef, w.result)
		}
		if op.Auth != w.auth || !reflect.DeepEqual(op.Permissions, w.permissions) || op.Public || op.ServiceCallers != nil || op.Middleware != nil {
			t.Errorf("%s: clause = auth %v, permissions %v, public %v, service %v, middleware %v", where, op.Auth, op.Permissions, op.Public, op.ServiceCallers, op.Middleware)
		}
		const description = "Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it."
		wantDocs := &ir.OperationDocs{
			Title: w.title, Description: description, Capability: w.capability,
			Lifecycle: ir.DocsLifecycleActive, Visibility: ir.DocsVisibilityPublic, MappingStatus: ir.DocsMappingStatusMapped,
		}
		if !reflect.DeepEqual(op.Docs, wantDocs) || op.Comment != description {
			t.Errorf("%s: docs = %s, comment %q", where, marshal(t, op.Docs), op.Comment)
		}
		if op.MCP == nil || op.MCP.Handle != w.handle || op.MCP.Hidden || op.MCP.Invocation != (ir.MCPInvocation{Key: "invocationPolicy", Value: "auto"}) {
			t.Errorf("%s: mcp = %+v", where, op.MCP)
		}
		wantMarker := &ir.BehaviorOperation{
			Database: "fixture-workflow-db", Type: strings.TrimSuffix(w.set, "Actions"), Key: "id", Use: w.use,
			Behavior: "Workflow", Operation: "transition", Writes: true, Permissions: w.configPermissions,
		}
		if !reflect.DeepEqual(op.BehaviorOperation, wantMarker) {
			t.Errorf("%s: marker = %+v, want %+v", where, op.BehaviorOperation, wantMarker)
		}
	}
	if set := sets["OrderActions"]; len(set.Operations) != 1 || !reflect.DeepEqual(set.Permissions, []string{"orders.write"}) {
		t.Errorf("OrderActions = %s", marshal(t, set))
	}
	if set := sets["ShipmentActions"]; len(set.Operations) != 2 || set.Operations[0].Name != "statusTransition" || !set.Auth {
		t.Errorf("ShipmentActions = %s", marshal(t, set))
	}
	if set := sets["OrderQueries"]; len(set.Operations) != 1 || set.Operations[0].BehaviorOperation != nil || set.Operations[0].Origin != "" {
		t.Errorf("OrderQueries = %s", marshal(t, set))
	}

	for _, tc := range []struct{ typeName, state string }{
		{"OrderStatus", "OrderStatusState"},
		{"ShipmentStatus", "ShipmentStatusState"},
		{"ShipmentPaymentStatus", "ShipmentPaymentStatusState"},
	} {
		input, result := schema.Types[tc.typeName+"TransitionInput"], schema.Types[tc.typeName+"TransitionResult"]
		if input == nil || result == nil {
			t.Errorf("%s's input or result was not added", tc.typeName)
			continue
		}
		if input.Role != ir.RoleAPIInput || input.Origin != ir.OriginBehavior || input.Owner != "fixture-workflow-api" || len(input.Fields) != 1 ||
			input.Fields[0].Name != "to" || input.Fields[0].TypeRef.Name != tc.state || !input.Fields[0].Required || input.Fields[0].Origin != ir.OriginBehavior ||
			input.Fields[0].Comment != "The state to move to." {
			t.Errorf("%s = %s", input.Name, marshal(t, input))
		}
		if result.Role != ir.RoleEmbeddedStruct || result.Origin != ir.OriginBehavior || len(result.Fields) != 2 ||
			result.Fields[0].Name != "from" || result.Fields[1].Name != "to" || result.Fields[0].TypeRef.Name != tc.state || !result.Fields[1].Required {
			t.Errorf("%s = %s", result.Name, marshal(t, result))
		}
	}
	if got := input(schema, "OrderStatusTransitionInput").Comment; got != "The parameters of Order's status transition." {
		t.Errorf("OrderStatusTransitionInput's comment = %q", got)
	}
	if got := schema.Types["ShipmentPaymentStatusTransitionResult"].Comment; got != "What Shipment's paymentStatus transition returns." {
		t.Errorf("ShipmentPaymentStatusTransitionResult's comment = %q", got)
	}
	for name, values := range map[string][]string{
		"OrderStatusState":           {"draft", "paid", "shipped"},
		"ShipmentStatusState":        {"pending", "packed", "sent"},
		"ShipmentPaymentStatusState": {"unpaid", "paid", "refunded"},
	} {
		def := schema.Enums[name]
		if def == nil || def.Origin != ir.OriginBehavior || def.Owner != "fixture-workflow-api" {
			t.Errorf("enum %s = %+v", name, def)
			continue
		}
		var got []string
		for _, v := range def.Values {
			got = append(got, v.SerializedAs)
		}
		if !reflect.DeepEqual(got, values) || def.Values[0].Name != strings.ToUpper(values[0][:1])+values[0][1:] {
			t.Errorf("enum %s = %+v, want %v", name, def.Values, values)
		}
	}
	if got := schema.Enums["OrderStatusState"].Comment; got != "A state of Order's status Workflow." {
		t.Errorf("OrderStatusState's comment = %q", got)
	}
	if def := schema.Scalars["Identity.UUID"]; def == nil || def.TypeMappings["go"] == "" {
		t.Errorf("Identity.UUID was not hydrated: %+v", def)
	}
	if db.OperationSets != nil || db.Types["OrderStatusTransitionInput"] != nil {
		t.Error("the database's schema was changed")
	}

	// The expansion runs again on its own output and gives the same IR.
	before := marshal(t, schema)
	if _, err := behaviorops.Expand(schema, db, registry.New(naming.Default())); err != nil {
		t.Fatal(err)
	}
	if after := marshal(t, schema); after != before {
		t.Error("expanding the expanded IR again changed it")
	}

	// An API without such a set reads no database.
	asked = nil
	if _, err := LoadService(filepath.FromSlash("tsreader/testdata/services/fixture-queue-api"), WithDependencyLoader(func(name string) (*ir.Schema, error) {
		asked = append(asked, name)
		return LoadService(filepath.Join("tsreader", "testdata", "services", name))
	})); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 {
		t.Errorf("an API without a @behaviorOperations set read %v", asked)
	}

	failing := WithDependencyLoader(func(string) (*ir.Schema, error) { return nil, errors.New("no such service") })
	_, err = LoadService(filepath.FromSlash(workflowAPIFixture), failing)
	const wantErr = "fixture-workflow-api: reading the database fixture-workflow-db, whose tables its @behaviorOperations sets name: no such service"
	if err == nil || err.Error() != wantErr {
		t.Errorf("LoadService = %v, want %q", err, wantErr)
	}
}

func input(schema *ir.Schema, name string) *ir.TypeDef {
	if td := schema.Types[name]; td != nil {
		return td
	}
	return &ir.TypeDef{}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestBehaviorOperationsInTheDataForms: the JSON form reads
// behaviorOperations and the set's clause, and the loader fills the sets
// from the database beside the API as it fills the TypeScript form's.
func TestBehaviorOperationsInTheDataForms(t *testing.T) {
	ts, err := LoadService(filepath.FromSlash(workflowAPIFixture))
	if err != nil {
		t.Fatal(err)
	}
	sets := func(schema *ir.Schema) string {
		var out []*ir.OperationSet
		for _, set := range schema.OperationSets {
			if set.IsBehaviorOperations() {
				out = append(out, set)
			}
		}
		return marshal(t, out)
	}
	dir, load := workflowAPIDataService(t, `{
  "imports": [{"package": "@schemas/fixture-workflow-db", "types": ["Order", "Shipment"]}],
  "operationSets": [
    {"name": "OrderActions", "comment": "An order's status operations: each transition its Workflow lists, run\nthrough the behaviors' core (D103). The route needs orders.write; a move\nto shipped needs orders.ship too, which the core checks.",
     "operations": [], "behaviorOperations": {"type": "Order"}, "permissions": ["orders.write"]},
    {"name": "ShipmentActions", "comment": "A shipment's two Workflows, status and paymentStatus, for any signed-in\ncaller; a refund needs payments.refund, which the core checks.",
     "operations": [], "behaviorOperations": {"type": "Shipment"}, "auth": true}
  ]
}`)
	schema, err := LoadService(dir, load)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if got, want := sets(schema), sets(ts); got != want {
		t.Errorf("the sets differ from the TypeScript form's\n got %s\nwant %s", got, want)
	}
}

// workflowAPIDataService writes an API of fixture-workflow-db's tables in
// the JSON form, body being its src/orders.schema.json, and returns its
// directory with the option that reads the database, the fixture's, by
// name, as a build's dependency loader does.
func workflowAPIDataService(t *testing.T, body string) (string, Option) {
	t.Helper()
	db, err := LoadService(filepath.FromSlash("tsreader/testdata/services/fixture-workflow-db"))
	if err != nil {
		t.Fatal(err)
	}
	load := WithDependencyLoader(func(name string) (*ir.Schema, error) {
		if name != db.Name {
			return nil, errors.New("no such service")
		}
		return db, nil
	})
	api := filepath.Join(t.TempDir(), "fixture-workflow-api")
	for rel, contents := range map[string]string{
		"schema.config.json":     `{"name": "fixture-workflow-api", "kind": "API", "dependencies": [{"name": "fixture-workflow-db", "kind": "DB"}], "outputs": {}}`,
		"src/orders.schema.json": body,
	} {
		path := filepath.Join(api, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return api, load
}

// behaviorOperationsRegistry is the core registry with two behaviors of
// the test's own: Gauge, a field behavior whose operation runs on the
// schema as a whole, and Tally, one whose operation runs on a namespace.
func behaviorOperationsRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	op := func(name, scope string) string {
		return `{"name":"` + name + `","scope":"` + scope + `","paramsSchema":{"type":"object","additionalProperties":false},"resultSchema":{"type":"object"}}`
	}
	for _, decl := range []string{
		`{"name":"Gauge","fields":[{"name":"level","type":"number"}],"value":"level","operations":[` + op("census", "schema") + `]}`,
		`{"name":"Tally","fields":[{"name":"count","type":"number"}],"value":"count","operations":[` + op("sum", "namespace") + `]}`,
	} {
		if err := reg.RegisterBehavior(registry.BehaviorSpec{Declaration: json.RawMessage(decl)}); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// workflowOn is a Workflow on field, of the states given, from the first
// to each other.
func workflowOn(field string, states ...string) ir.BehaviorRef {
	var transitions []map[string]string
	for _, to := range states[1:] {
		transitions = append(transitions, map[string]string{"from": states[0], "to": to})
	}
	config, _ := json.Marshal(map[string]any{"states": states, "transitions": transitions})
	return ir.BehaviorRef{Name: "Workflow", Field: field, Config: config}
}

// ordersDB is a database whose Order table's status is a Workflow.
func ordersDB() *ir.Schema {
	db := ir.NewSchema("orders-db", ir.SchemaKindDB)
	db.Types["Order"] = &ir.TypeDef{Name: "Order", Role: ir.RoleDBTable, Behaviors: []ir.BehaviorRef{workflowOn("status", "draft", "paid")}, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
		{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	return db
}

// ordersAPI is an API of orders-db, its one DB dependency, importing
// Order from it, with the sets given.
func ordersAPI(sets ...*ir.OperationSet) *ir.Schema {
	api := ir.NewSchema("orders-api", ir.SchemaKindAPI)
	api.Dependencies = []ir.ServiceRef{{Name: "orders-db", Kind: ir.SchemaKindDB}}
	api.Imports = []ir.Import{{Package: "@schemas/orders-db", Types: []string{"Order"}}}
	api.OperationSets = sets
	return api
}

func actionsOf(name, table string) *ir.OperationSet {
	return &ir.OperationSet{Name: name, Operations: []*ir.FieldDef{}, BehaviorOperations: &ir.BehaviorOperationsConfig{Type: table}}
}

// TestBehaviorOperationsRefusals pins each refusal of the checks of a
// @behaviorOperations set (D103): its form, the set's clause on another
// set, the API's database, the table and its behaviors, a second set of a
// table, and the names and handles the expansion adds. An API the checks
// refuse is not expanded, and the pass reports the refusal among its
// findings.
func TestBehaviorOperationsRefusals(t *testing.T) {
	reg := behaviorOperationsRegistry(t)
	plainSet := func(mutate func(*ir.OperationSet)) *ir.OperationSet {
		set := &ir.OperationSet{Name: "Orders", Operations: []*ir.FieldDef{{Name: "list", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true, HTTPMethod: "GET"}}}
		mutate(set)
		return set
	}
	cases := []struct {
		name string
		api  func() *ir.Schema
		db   func() *ir.Schema
		want string
	}{
		{"an authored operation", func() *ir.Schema {
			set := actionsOf("OrderActions", "Order")
			set.Operations = []*ir.FieldDef{{Name: "ship", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true, HTTPMethod: "POST"}}
			return ordersAPI(set)
		}, ordersDB, "OrderActions: a @behaviorOperations class has no members: the loader adds one operation for each operation of each behavior on Order's fields, and the generated server runs it"},
		{"encrypted", func() *ir.Schema {
			set := actionsOf("OrderActions", "Order")
			set.Encrypted = true
			return ordersAPI(set)
		}, ordersDB, "OrderActions: a @behaviorOperations class is not Encrypted: its routes' bodies are its behaviors' parameters, which the generated handlers read as plain JSON"},
		{"not an API", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"))
			api.Kind = ir.SchemaKindGeneral
			return api
		}, ordersDB, "OrderActions: @behaviorOperations is only allowed in an API schema (this schema has kind General)"},
		{"@auth on another set", func() *ir.Schema {
			return ordersAPI(plainSet(func(s *ir.OperationSet) { s.Auth = true }))
		}, ordersDB, "Orders: @auth on a class applies only to a @behaviorOperations set; extend Authenticated, or put @auth on each method of Orders"},
		{"@requirePermission on another set", func() *ir.Schema {
			return ordersAPI(plainSet(func(s *ir.OperationSet) { s.Permissions = []string{"orders.read"} }))
		}, ordersDB, "Orders: @requirePermission on a class applies only to a @behaviorOperations set; put it on each method of Orders"},
		{"@publicRoute on another set", func() *ir.Schema {
			return ordersAPI(plainSet(func(s *ir.OperationSet) { s.Public = true }))
		}, ordersDB, "Orders: @publicRoute on a class applies only to a @behaviorOperations set; put it on each method of Orders"},
		{"@publicRoute with @auth", func() *ir.Schema {
			set := actionsOf("OrderActions", "Order")
			set.Public, set.Auth = true, true
			return ordersAPI(set)
		}, ordersDB, "OrderActions: @publicRoute contradicts @auth on the class: a public route needs no caller"},
		{"@publicRoute with @requirePermission", func() *ir.Schema {
			set := actionsOf("OrderActions", "Order")
			set.Public, set.Permissions = true, []string{"orders.write"}
			return ordersAPI(set)
		}, ordersDB, "OrderActions: @publicRoute contradicts @requirePermission on the class: a public route needs no caller"},
		{"@publicRoute with @requireService", func() *ir.Schema {
			set := actionsOf("OrderActions", "Order")
			set.Public, set.ServiceCallers = true, &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
			return ordersAPI(set)
		}, ordersDB, "OrderActions: @publicRoute contradicts @requireService on the class: a public route needs no caller"},
		{"@publicRoute with @allowService", func() *ir.Schema {
			set := actionsOf("OrderActions", "Order")
			set.Public, set.ServiceCallers = true, &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: []string{"billing"}}
			return ordersAPI(set)
		}, ordersDB, "OrderActions: @publicRoute contradicts @allowService on the class: a public route needs no caller"},
		{"no database", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"))
			api.Dependencies = nil
			return api
		}, ordersDB, "OrderActions: @behaviorOperations(Order) runs Order's behaviors on the API's database, and orders-api connects to none: set its authDb, or give it one DB dependency, the DB service that declares Order"},
		{"several databases and no authDb", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"))
			api.Dependencies = append(api.Dependencies, ir.ServiceRef{Name: "billing-db", Kind: ir.SchemaKindDB})
			return api
		}, ordersDB, "OrderActions: @behaviorOperations(Order) runs Order's behaviors on the API's database, and orders-api names 2 DB dependencies and no authDb: set its authDb to the one that declares Order"},
		{"not imported", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"))
			api.Imports = nil
			return api
		}, ordersDB, "OrderActions: @behaviorOperations names Order, which orders-api does not import; import it from orders-db, its database"},
		{"imported from another service", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"))
			api.Imports = []ir.Import{{Package: "@schemas/billing-db", Types: []string{"Order"}}}
			return api
		}, ordersDB, "OrderActions: @behaviorOperations names Order, which orders-api imports from billing-db, not from orders-db, its database (its authDb, or its one DB dependency)"},
		{"not declared by the database", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"))
		}, func() *ir.Schema { return ir.NewSchema("orders-db", ir.SchemaKindDB) },
			"OrderActions: @behaviorOperations names Order, which orders-db does not declare"},
		{"not a table", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"))
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Role = ir.RoleEmbeddedStruct
			return db
		}, "OrderActions: @behaviorOperations names Order, which is not a table of orders-db: a set serves a table one of whose fields is a behavior (D103)"},
		{"an engine type", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"))
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Behaviors = []ir.BehaviorRef{{Name: "Workflow"}}
			return db
		}, "OrderActions: @behaviorOperations names Order, an engine type of orders-db (D58), whose engine serves its operations; a set serves a table one of whose fields is a behavior (D103)"},
		{"no field behavior", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"))
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Behaviors = nil
			return db
		}, "OrderActions: @behaviorOperations names Order, a table of orders-db none of whose fields is a behavior; a set serves a table one of whose fields is, such as status: Workflow<{ ... }> (D103)"},
		{"no key", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"))
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Fields[0].Key = false
			return db
		}, "OrderActions: @behaviorOperations names Order, which has no @key field: its operations' routes address a row by its key"},
		{"a key of two fields", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"))
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Fields[1].Key = true
			return db
		}, "OrderActions: @behaviorOperations names Order, whose key is 2 fields (id, title): its operations' routes address a row by one"},
		{"an operation on the schema", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"))
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Behaviors = append(db.Types["Order"].Behaviors, ir.BehaviorRef{Name: "Gauge", Field: "fill"})
			return db
		}, "OrderActions: Order's fill is a Gauge, whose operation census runs on the schema as a whole, not on a row: @behaviorOperations serves the operations a behavior runs on a row (D103)"},
		{"an operation on a namespace", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"))
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Behaviors = append(db.Types["Order"].Behaviors, ir.BehaviorRef{Name: "Tally", Field: "items"})
			return db
		}, "OrderActions: Order's items is a Tally, whose operation sum runs on the namespace as a whole, not on a row: @behaviorOperations serves the operations a behavior runs on a row (D103)"},
		{"a second set of the table", func() *ir.Schema {
			return ordersAPI(actionsOf("OrderActions", "Order"), actionsOf("Fulfilment", "Order"))
		}, ordersDB, "Fulfilment: @behaviorOperations names Order, which OrderActions names too: an API serves a table's behavior operations from one set"},
		{"a name the API declares", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"))
			api.Enums["OrderStatusState"] = &ir.EnumDef{Name: "OrderStatusState", Values: []ir.EnumValueDef{{Name: "Open"}}}
			return api
		}, ordersDB, "OrderActions: @behaviorOperations(Order) adds the enum OrderStatusState, for Order's status transition, and orders-api already declares or imports one of that name; rename it"},
		{"a name the API imports", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"))
			api.Imports = append(api.Imports, ir.Import{Package: "@schemas/shared", Types: []string{"OrderStatusTransitionInput"}})
			return api
		}, ordersDB, "OrderActions: @behaviorOperations(Order) adds the type OrderStatusTransitionInput, for Order's status transition, and orders-api already declares or imports one of that name; rename it"},
		{"a name two sets add", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"), actionsOf("OrderStatusActions", "OrderStatus"))
			api.Imports[0].Types = append(api.Imports[0].Types, "OrderStatus")
			return api
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Behaviors = []ir.BehaviorRef{workflowOn("statusTransition", "open", "closed")}
			db.Types["OrderStatus"] = &ir.TypeDef{Name: "OrderStatus", Role: ir.RoleDBTable, Behaviors: []ir.BehaviorRef{workflowOn("transition", "open", "closed")}, Fields: []*ir.FieldDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			}}
			return db
		}, "OrderStatusActions: @behaviorOperations(OrderStatus) adds the enum OrderStatusTransitionState, for OrderStatus's transition transition, and orders-api already declares or imports one of that name; rename it"},
		{"a handle longer than 48 characters", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"))
			return api
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Order"].Behaviors = []ir.BehaviorRef{workflowOn("fulfilmentOfTheWholeConsignment", "open", "closed")}
			return db
		}, `OrderActions: the MCP handle of Order's fulfilmentOfTheWholeConsignment transition, "order_fulfilment_of_the_whole_consignment_transition", is longer than 48 characters (D11)`},
		{"an operation name its namespace takes", func() *ir.Schema {
			queries := plainSet(func(s *ir.OperationSet) {
				s.Name = "OrderQueries"
				s.Operations[0].Name = "statusTransition"
			})
			return ordersAPI(actionsOf("OrderActions", "Order"), queries)
		}, ordersDB, "OrderActions: @behaviorOperations(Order) adds the operation statusTransition, for Order's status transition, to the namespace order, where OrderQueries.statusTransition takes that name; rename that operation, or name the set for another namespace"},
		{"an operation name another set adds to its namespace", func() *ir.Schema {
			api := ordersAPI(actionsOf("OrderActions", "Order"), actionsOf("OrderOperations", "Invoice"))
			api.Imports[0].Types = append(api.Imports[0].Types, "Invoice")
			return api
		}, func() *ir.Schema {
			db := ordersDB()
			db.Types["Invoice"] = &ir.TypeDef{Name: "Invoice", Role: ir.RoleDBTable, Behaviors: []ir.BehaviorRef{workflowOn("status", "open", "settled")}, Fields: []*ir.FieldDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			}}
			return db
		}, "OrderOperations: @behaviorOperations(Invoice) adds the operation statusTransition, for Invoice's status transition, to the namespace order, where OrderActions.statusTransition takes that name; rename that operation, or name the set for another namespace"},
		{"an MCP handle another tool takes", func() *ir.Schema {
			orders := plainSet(func(s *ir.OperationSet) {
				s.Operations[0].MCP = &ir.OperationMCP{Handle: "order_status_transition"}
			})
			return ordersAPI(actionsOf("OrderActions", "Order"), orders)
		}, ordersDB, `OrderActions: @behaviorOperations(Order) adds the MCP tool "order_status_transition", for Order's status transition, and Orders.list's @mcp takes that handle; rename that handle (D11)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, db := tc.api(), tc.db()
			in := verify.Input{Registry: reg, Database: db}
			res := verify.CheckBehaviorOperations(api, in)
			var got []string
			for _, d := range res.Errors {
				got = append(got, d.Msg)
			}
			found := false
			for _, msg := range got {
				found = found || msg == tc.want
			}
			if !found {
				t.Errorf("CheckBehaviorOperations = %q, want %q among them", got, tc.want)
			}
			// The pass reports the same refusal among its findings.
			full := verify.Run(api, in)
			found = false
			for _, d := range full.Errors {
				found = found || d.Msg == tc.want
			}
			if !found {
				t.Errorf("verify.Run does not report %q", tc.want)
			}
		})
	}

	// Without the database the table is not checked, and nothing else is
	// refused.
	if res := verify.CheckBehaviorOperations(ordersAPI(actionsOf("OrderActions", "Order")), verify.Input{Registry: reg}); res.Err() != nil {
		t.Errorf("CheckBehaviorOperations without a database = %v", res.Err())
	}
	// A clean API passes, and so does its expansion when checked again.
	api, db := ordersAPI(actionsOf("OrderActions", "Order")), ordersDB()
	in := verify.Input{Registry: reg, Database: db}
	if err := verify.CheckBehaviorOperations(api, in).Err(); err != nil {
		t.Fatalf("CheckBehaviorOperations = %v", err)
	}
	if _, err := behaviorops.Expand(api, db, reg); err != nil {
		t.Fatal(err)
	}
	if err := verify.CheckBehaviorOperations(api, in).Err(); err != nil {
		t.Errorf("CheckBehaviorOperations after the expansion = %v", err)
	}
}

// TestBehaviorOperationsOfASetOnlyServicesCall: a @behaviorOperations set
// whose @requireService admits only calling services loads, each operation
// taking the set's clause and a hidden tool, whose reason says why, since
// no end user's agent can call it (D37); the other set's tools stay
// visible. @publicRoute beside the clause is refused, since a public route
// takes none from its set.
func TestBehaviorOperationsOfASetOnlyServicesCall(t *testing.T) {
	db, err := LoadService(filepath.FromSlash("tsreader/testdata/services/fixture-workflow-db"))
	if err != nil {
		t.Fatal(err)
	}
	load := WithDependencyLoader(func(string) (*ir.Schema, error) { return db, nil })
	source, err := os.ReadFile(filepath.FromSlash(workflowAPIFixture + "/src/orders.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	withClause := func(t *testing.T, clause string) string {
		t.Helper()
		const imports, set = "import { HttpMethod, auth, behaviorOperations, requirePermission, rest }", "@behaviorOperations(Order)\n@requirePermission([\"orders.write\"])\n"
		if !strings.Contains(string(source), imports) || !strings.Contains(string(source), set) {
			t.Fatalf("the fixture's orders.schema.ts no longer reads %q and %q", imports, set)
		}
		edited := strings.Replace(string(source), imports, "import { HttpMethod, auth, behaviorOperations, publicRoute, requirePermission, requireService, rest }", 1)
		edited = strings.Replace(edited, set, "@behaviorOperations(Order)\n"+clause, 1)
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(filepath.FromSlash(workflowAPIFixture))); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "orders.schema.ts"), []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(viewTSConfig(t)), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	schema, err := LoadService(withClause(t, "@requireService()\n"), load)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	for _, set := range schema.OperationSets {
		switch set.Name {
		case "OrderActions":
			if set.ServiceCallers == nil || set.ServiceCallers.Mode != ir.ServiceCallersRequire || len(set.Operations) != 1 {
				t.Fatalf("OrderActions = %s", marshal(t, set))
			}
			op := set.Operations[0]
			want := &ir.OperationMCP{Hidden: true, HiddenReason: "Only the services that @requireService on OrderActions admits call it (D37)."}
			if !reflect.DeepEqual(op.MCP, want) || ir.EffectiveServiceCallers(set, op) != set.ServiceCallers {
				t.Errorf("OrderActions.%s: mcp %+v, service clause %+v", op.Name, op.MCP, ir.EffectiveServiceCallers(set, op))
			}
			if err := ir.ValidateOperationMCP(op.MCP); err != nil {
				t.Errorf("OrderActions.%s: its @mcp does not validate: %v", op.Name, err)
			}
		case "ShipmentActions":
			for _, op := range set.Operations {
				if op.MCP == nil || op.MCP.Hidden || op.MCP.Handle == "" {
					t.Errorf("ShipmentActions.%s: mcp %+v, want a visible tool", op.Name, op.MCP)
				}
			}
		}
	}

	_, err = LoadService(withClause(t, "@publicRoute\n@requireService()\n"), load)
	if want := "OrderActions: @publicRoute contradicts @requireService on the class: a public route needs no caller"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("LoadService = %v, want an error containing %q", err, want)
	}
}

// TestBehaviorOperationsRefusedInTheDataForms: a data-form API is refused
// through the load, with the pass's message, and its set stays unfilled.
func TestBehaviorOperationsRefusedInTheDataForms(t *testing.T) {
	dir, load := workflowAPIDataService(t, `{
  "operationSets": [{"name": "OrderActions", "operations": [], "behaviorOperations": {"type": "Order"}}]
}`)
	_, err := LoadService(dir, load)
	if want := "OrderActions: @behaviorOperations names Order, which fixture-workflow-api does not import; import it from fixture-workflow-db, its database"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("LoadService = %v, want an error containing %q", err, want)
	}
}

// TestASourceViewProjectsTheFieldsABehaviorOwns: a @source view of another
// service's table whose fields are behaviors may project the fields the
// behaviors own (D103), which its database's load adds and the TypeScript
// reader's flattened source type lacks; the pass checks them as the
// table's own, so a view field the table has no field for is still
// refused.
func TestASourceViewProjectsTheFieldsABehaviorOwns(t *testing.T) {
	schema, err := LoadService(filepath.FromSlash("tsreader/testdata/services/fixture-workflow-view-api"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	view := schema.Types["OrderView"]
	if view == nil || view.Source == nil || view.Source.Target != "fixture-workflow-db.Order" {
		t.Fatalf("OrderView = %+v", view)
	}
	var names []string
	for _, fd := range view.Fields {
		names = append(names, fd.Name)
	}
	if want := []string{"id", "title", "status", "statusEnteredAt"}; !reflect.DeepEqual(names, want) {
		t.Errorf("OrderView's fields = %v, want %v", names, want)
	}
	if !reflect.DeepEqual(view.Source.OmittedFromSource, []string{"placedAt"}) {
		t.Errorf("OrderView omits %v from its source, want [placedAt]", view.Source.OmittedFromSource)
	}

	dir := t.TempDir()
	source, err := os.ReadFile(filepath.FromSlash("tsreader/testdata/services/fixture-workflow-view-api/src/orders.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, os.DirFS(filepath.FromSlash("tsreader/testdata/services/fixture-workflow-view-api"))); err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(source), "  statusEnteredAt?: Temporal.DateTime;\n", "  statusEnteredAt?: Temporal.DateTime;\n  statusLeftAt?: Temporal.DateTime;\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "src", "orders.schema.ts"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(viewTSConfig(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadService(dir)
	if want := "OrderView.statusLeftAt is not in source type fixture-workflow-db.Order and is not marked @virtual"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("LoadService = %v, want an error containing %q", err, want)
	}
}

// viewTSConfig is the fixtures' tsconfig.base.json with its paths made
// absolute, for a copy of a fixture outside the fixtures' directory.
func viewTSConfig(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs(filepath.FromSlash("tsreader/testdata"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(base, "tsconfig.base.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	options := config["compilerOptions"].(map[string]any)
	paths := options["paths"].(map[string]any)
	for key, value := range paths {
		var abs []any
		for _, each := range value.([]any) {
			abs = append(abs, filepath.ToSlash(filepath.Join(base, each.(string))))
		}
		paths[key] = abs
	}
	options["baseUrl"] = filepath.ToSlash(base)
	config["include"] = []string{"schema.config.ts", "src/**/*.ts"}
	out, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
