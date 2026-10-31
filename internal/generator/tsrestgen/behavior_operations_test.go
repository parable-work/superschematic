package tsrestgen

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// behaviorAPI is the API whose @behaviorOperations sets the loader fills
// with the operations of fixture-workflow-db's Workflows (D103): Order's
// status under OrderActions, which needs orders.write, and Shipment's
// status and paymentStatus under ShipmentActions, which needs a signed-in
// caller; and OrderQueries.getOrder, an authored operation that answers
// an Order, its owned fields among them.
const behaviorAPI = "fixture-workflow-api"

// behaviorDB is fixture-workflow-api's database, its one DB dependency.
const behaviorDB = "fixture-workflow-db"

// behaviorFixture is fixture-workflow-api with its database and apigen's
// endpoints for it, read with one registry, which declares the
// behaviors; the router serves its MCP tools.
func behaviorFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, behaviorDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, behaviorAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorAPI, err)
	}
	deps := map[string]*ir.Schema{behaviorDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     behaviorAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: behaviorAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: behaviorDeps}
}

// behaviorDeps is fixture-workflow-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var behaviorDeps = DepsInfo{
	Database:             behaviorDB,
	Dialect:              "postgres",
	ORM:                  "FixtureWorkflowDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-workflow-db-types",
}

// generateBehavior generates the package for fixture, with its MCP
// endpoint, without setting its Deps.
func generateBehavior(t *testing.T, fixture apiFixture) *APIOutput {
	t.Helper()
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock, MCP: fixture.mcp})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return output
}

// generateBehaviorFixture generates the package for fixture-workflow-api,
// its Deps set.
func generateBehaviorFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, behaviorFixture(t))
	if err := output.SetDeps(behaviorDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorOperations pins the package for
// fixture-workflow-api: its implementation interface has getOrder alone,
// and its router mounts each behavior operation with a handler that runs
// it through RouterOptions.orm, keyed apart where two namespaces share a
// name. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorOperations -update
func TestWriteAPIGoldenBehaviorOperations(t *testing.T) {
	checkGolden(t, generateBehaviorFixture(t), behaviorAPI)
}

// TestGenerateBehaviorOperations: a behavior operation (D103) is the
// router's, not the implementation's. Its endpoint carries the repository
// method it runs and its key parameter; the implementation interfaces, the
// scaffold and interfaces.ts's imports leave it out; the operation table
// keys statusTransition, which order and shipment share, by namespace; and
// the MCP endpoint serves its tool through its route.
func TestGenerateBehaviorOperations(t *testing.T) {
	output := generateBehaviorFixture(t)
	if !output.FieldBehaviors || !output.HasBehaviors() {
		t.Errorf("FieldBehaviors %t, HasBehaviors %t", output.FieldBehaviors, output.HasBehaviors())
	}
	keys := map[string]EndpointInfo{}
	for _, ep := range output.Endpoints {
		keys[ep.Key] = ep
	}
	for key, want := range map[string]struct {
		namespace, name, method string
		route                   *BehaviorRoute
	}{
		"orderStatusTransition":    {"order", "statusTransition", "order.status.transition", &BehaviorRoute{Set: "OrderActions", Database: behaviorDB, Type: "Order", Use: "status", Behavior: "Workflow", Operation: "transition", Repository: "order"}},
		"shipmentStatusTransition": {"shipment", "statusTransition", "shipment.status.transition", &BehaviorRoute{Set: "ShipmentActions", Database: behaviorDB, Type: "Shipment", Use: "status", Behavior: "Workflow", Operation: "transition", Repository: "shipment"}},
		"paymentStatusTransition":  {"shipment", "paymentStatusTransition", "shipment.paymentStatus.transition", &BehaviorRoute{Set: "ShipmentActions", Database: behaviorDB, Type: "Shipment", Use: "paymentStatus", Behavior: "Workflow", Operation: "transition", Repository: "shipment"}},
		"getOrder":                 {"order", "getOrder", "", nil},
	} {
		ep, ok := keys[key]
		switch {
		case !ok:
			t.Errorf("no endpoint keyed %s among %v", key, slices.Sorted(maps.Keys(keys)))
			continue
		case ep.Namespace != want.namespace || ep.Name != want.name:
			t.Errorf("%s is %s.%s", key, ep.Namespace, ep.Name)
		}
		if want.route == nil {
			if ep.Behavior != nil {
				t.Errorf("%s runs a behavior: %+v", key, ep.Behavior)
			}
			continue
		}
		route := ep.Behavior
		if route == nil {
			t.Errorf("%s runs no behavior", key)
			continue
		}
		if route.Method() != want.method || route.Key.Name != "id" || route.Key.TSType != "IdentityUUID" {
			t.Errorf("%s calls %s with key %+v", key, route.Method(), route.Key)
		}
		got := *route
		got.Key = ParamInfo{}
		if got != *want.route {
			t.Errorf("%s's route = %+v, want %+v", key, got, *want.route)
		}
		if ep.Method != "POST" || !ep.HasInput || ep.Manual() {
			t.Errorf("%s = %+v", key, ep)
		}
	}
	if order := keys["orderStatusTransition"]; strings.Join(order.RequiredPerms, ",") != "orders.write" || order.OutputType != "OrderStatusTransitionResult" {
		t.Errorf("orderStatusTransition: permissions %v, output %s", order.RequiredPerms, order.OutputType)
	}
	if shipment := keys["shipmentStatusTransition"]; !shipment.RequiresAuth || len(shipment.RequiredPerms) != 0 {
		t.Errorf("shipmentStatusTransition: auth %t, permissions %v", shipment.RequiresAuth, shipment.RequiredPerms)
	}

	// The implementation has getOrder alone: shipment, all of whose
	// operations are behaviors', has no implementation at all.
	if len(output.Namespaces) != 1 || output.Namespaces[0].Name != "order" || len(output.Namespaces[0].Endpoints) != 1 || output.Namespaces[0].Endpoints[0].Name != "getOrder" {
		t.Errorf("namespaces = %+v", output.Namespaces)
	}
	imported := func(imports []PackageImport) []string {
		var out []string
		for _, imp := range imports {
			out = append(out, imp.Symbols...)
		}
		return out
	}
	for _, symbol := range imported(output.TypeImports) {
		if strings.HasPrefix(symbol, "OrderStatus") || strings.HasPrefix(symbol, "Shipment") {
			t.Errorf("interfaces.ts imports %s, a behavior operation's", symbol)
		}
	}
	routerTypes := imported(output.RouterTypeImports)
	for _, want := range []string{"OrderStatusTransitionResult", "ShipmentStatusTransitionResult", "ShipmentPaymentStatusTransitionResult"} {
		if !slices.Contains(routerTypes, want) {
			t.Errorf("router.ts does not import %s: %v", want, routerTypes)
		}
	}
	if slices.Contains(routerTypes, "OrderStatusTransitionInput") {
		t.Error("router.ts imports OrderStatusTransitionInput, which no cast names")
	}
	if !slices.Contains(imported(output.ValidatorImports), "parseOrderStatusTransitionInputJson") {
		t.Errorf("router.ts does not import the input's validator: %+v", output.ValidatorImports)
	}

	// The MCP endpoint serves the operations' tools through their routes,
	// by their keys.
	if output.MCP == nil {
		t.Fatal("no MCP endpoint")
	}
	tools := map[string]MCPTool{}
	for _, tool := range output.MCP.Tools {
		tools[tool.Name] = tool
	}
	tool, ok := tools["order_status_transition"]
	if !ok {
		t.Fatalf("no tool order_status_transition among %+v", output.MCP.Tools)
	}
	if tool.Operation != "orderStatusTransition" || tool.Annotations.ReadOnlyHint || tool.Confirm {
		t.Errorf("order_status_transition = %+v", tool)
	}
	if want := []MCPToolArgument{{Key: "id", In: "path", Name: "id"}, {Key: "to", In: "body", Name: "to"}}; !slices.Equal(tool.Arguments, want) {
		t.Errorf("order_status_transition's arguments = %+v, want %+v", tool.Arguments, want)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(schema.Properties["to"].Enum, []string{"draft", "paid", "shipped"}) || !slices.Equal(slices.Sorted(slices.Values(schema.Required)), []string{"id", "to"}) {
		t.Errorf("order_status_transition's input schema = %s", tool.InputSchema)
	}
	if got := tools["shipment_status_transition"].Operation; got != "shipmentStatusTransition" {
		t.Errorf("shipment_status_transition runs through %s", got)
	}
}

// TestBehaviorOperationsNeedTheRepositories: the behavior operations run
// through Deps.orm, so Deps without the repositories is refused, saying
// why there are none, and so is a package written without Deps.
func TestBehaviorOperationsNeedTheRepositories(t *testing.T) {
	const prefix = "tsrestgen: fixture-workflow-api serves the behavior operations of Order's status, a Workflow (@behaviorOperations on OrderActions), which run through Deps.orm, the repositories fixture-workflow-db's TypeScript types export for Postgres; "
	for name, tc := range map[string]struct {
		deps DepsInfo
		why  string
	}{
		"no TypeScript types": {DepsInfo{Database: behaviorDB, Dialect: "postgres"}, "enable outputs.types.typescript in fixture-workflow-db's config"},
		"sqlite first":        {DepsInfo{Database: behaviorDB, Dialect: SQLiteDialect, ORM: "FixtureWorkflowDbOrm", DatabaseTypesPackage: "@schemas/fixture-workflow-db-types"}, "fixture-workflow-db lists sqlite first, and the repositories run on Postgres alone (D103)"},
	} {
		t.Run(name, func(t *testing.T) {
			err := generateBehavior(t, behaviorFixture(t)).SetDeps(tc.deps)
			if err == nil || err.Error() != prefix+tc.why {
				t.Errorf("SetDeps = %v,\nwant %s", err, prefix+tc.why)
			}
		})
	}
	err := WriteAPI(generateBehavior(t, behaviorFixture(t)), t.TempDir())
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("WriteAPI without Deps = %v", err)
	}
}

// TestOperationKeysRefuseAClash: the key a shared name takes is refused
// when another operation is named so.
func TestOperationKeysRefuseAClash(t *testing.T) {
	_, err := operationKeys([]apigen.EndpointInfo{
		{Namespace: "order", Name: "statusTransition"},
		{Namespace: "shipment", Name: "statusTransition"},
		{Namespace: "reports", Name: "orderStatusTransition"},
	})
	want := "tsrestgen: operations order.statusTransition and reports.orderStatusTransition both key the router's operation table as orderStatusTransition, since operations of several namespaces are named statusTransition; rename one"
	if err == nil || err.Error() != want {
		t.Errorf("operationKeys = %v,\nwant %s", err, want)
	}
}

// behaviorCheck is the file tsc checks the package for
// fixture-workflow-api against beside it: buildRouter takes the
// repositories, and refuses options without them; and the implementation
// has no member for shipment, whose operations are all behaviors'.
const behaviorCheck = `import type { FixtureWorkflowDbOrm } from '@schemas/fixture-workflow-db-types/orm';
import { buildRouter, operationSpecs, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureWorkflowDbOrm;

export const router = buildRouter(implementations, { orm, bodyLimits: { orderStatusTransition: 1024, shipmentStatusTransition: 1024 } });
// @ts-expect-error RouterOptions.orm runs the behavior operations.
export const without = buildRouter(implementations, {});
// @ts-expect-error The behavior operations have no implementation method.
export const shipment: Implementations['shipment'] = {};
export const spec: 'statusTransition' = operationSpecs.shipmentStatusTransition.name;
`

// TestGeneratedBehaviorOperationsAPICompiles type-checks the package for
// fixture-workflow-api against its type packages, fixture-workflow-db's
// repositories among them, the real runtime and Hono, with behaviorCheck.
func TestGeneratedBehaviorOperationsAPICompiles(t *testing.T) {
	tree := materializeAPI(t, behaviorFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "behavior.check.ts"), []byte(behaviorCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// behaviorCases are the cases behavior_operations_runtime.test.ts runs,
// by name: D103's end-to-end scenario, which the Go and Rust servers run
// too, and the MCP endpoint's, which only the TypeScript server serves.
var behaviorCases = []string{
	"a transition moves the field and returns the result",
	"a move the config has no transition for is refused with the core's status, code and message",
	"a caller without the route's permission is refused by the API's auth",
	"a caller without the permission the config names is refused by the core, and one who holds it moves the row",
	"a key no row holds is not found",
	"a table's two Workflows move apart",
	"the MCP tool is listed with its schema, and calling it runs the operation",
}

// TestGeneratedBehaviorOperationsEndToEnd serves fixture-workflow-api's
// generated router under bun over fixture-workflow-db's generated
// repositories on Postgres, in a schema of its own at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's create.sql
// applied, and drives it through the API's generated TypeScript SDK, each
// field's operations under it, and its MCP endpoint over HTTP
// (testdata/behavior_operations_runtime.test.ts): every case of
// behaviorCases must pass. It skips without the database, and without bun
// unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedBehaviorOperationsEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's behavior operations against Postgres")
	}
	fixture := behaviorFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         behaviorAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{behaviorDB: names.NpmTypesPackage(behaviorDB)},
		Registry:           fixture.registry,
		Clock:              fixedClock,
	})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	sdkOutput, err := sdkgen.Generate(fixture.endpoints, tsgen.ParseableTypeNames(tsOutput), fixedClock)
	if err != nil {
		t.Fatalf("sdkgen.Generate: %v", err)
	}
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", behaviorAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(behaviorAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[behaviorDB], sqlgen.Options{SchemaName: behaviorDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", behaviorDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", behaviorDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", behaviorDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_operations_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_operations.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_operations_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range behaviorCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(behaviorCases) {
		t.Errorf("bun ran %d cases, and behaviorCases names %d:\n%s", len(cases), len(behaviorCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_operations_runtime.test.ts:\n%s", out)
	}
}

// junitTestcase is one testcase of bun's junit report: its name, and
// whether it failed or was skipped.
type junitTestcase struct {
	Name    string    `xml:"name,attr"`
	Failure *struct{} `xml:"failure"`
	Skipped *struct{} `xml:"skipped"`
}

// junitTestcases reads every testcase of a junit report, at any depth of
// testsuite nesting, keyed by name.
func junitTestcases(report []byte) (map[string]junitTestcase, error) {
	cases := map[string]junitTestcase{}
	decoder := xml.NewDecoder(bytes.NewReader(report))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return cases, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "testcase" {
			continue
		}
		var tc junitTestcase
		if err := decoder.DecodeElement(&tc, &start); err != nil {
			return nil, err
		}
		cases[tc.Name] = tc
	}
}
