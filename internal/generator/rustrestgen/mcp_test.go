package rustrestgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/mcptools"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// generateMCPFixture generates fixture-mcp, the tool documents' fixture
// (visible, hidden and unclassified operations, every replay mode, and an
// invocation policy of ask), as a Rust API with outputs.api.mcp as mcp
// says; rewrite edits the loaded schema first when set.
func generateMCPFixture(t *testing.T, mcp bool, rewrite func(*ir.Schema)) (*APIOutput, error) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-mcp"))
	if err != nil {
		t.Fatalf("load fixture-mcp: %v", err)
	}
	if rewrite != nil {
		rewrite(schema)
	}
	outDir := t.TempDir()
	return generateFrom(schema, apiSource{}, Options{
		SchemaName: "fixture-mcp",
		TypesCrate: "schemas-fixture-mcp-types",
		TypesDir:   filepath.Join(outDir, "types", "rust", "fixture-mcp"),
		OutputDir:  filepath.Join(outDir, "api", "fixture-mcp"),
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		MCP:        mcp,
	})
}

func mustGenerateMCPFixture(t *testing.T, mcp bool, rewrite func(*ir.Schema)) *APIOutput {
	t.Helper()
	output, err := generateMCPFixture(t, mcp, rewrite)
	if err != nil {
		t.Fatalf("generate fixture-mcp: %v", err)
	}
	if output == nil {
		t.Fatal("fixture-mcp generated no Rust API")
	}
	return output
}

// eachOperation runs edit on every operation of schema.
func eachOperation(schema *ir.Schema, edit func(set *ir.OperationSet, op *ir.FieldDef)) {
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			edit(set, op)
		}
	}
}

// toolsOf reads the tool table src/router/mcp.rs declares.
func toolsOf(t *testing.T, module string) []mcptools.Tool {
	t.Helper()
	_, table, ok := strings.Cut(module, "pub const TOOLS: &str = r#\"")
	if !ok {
		t.Fatalf("mcp.rs declares no TOOLS:\n%s", module)
	}
	table, _, ok = strings.Cut(table, "\"#;")
	if !ok {
		t.Fatalf("mcp.rs's TOOLS does not end:\n%s", module)
	}
	var tools []mcptools.Tool
	if err := json.Unmarshal([]byte(table), &tools); err != nil {
		t.Fatalf("mcp.rs's TOOLS is not JSON: %v", err)
	}
	return tools
}

// TestMCPRouterGolden pins the crate of an API with outputs.api.mcp on
// (D81, D116): its router, which declares the endpoint's module and merges
// the endpoint into its routes, and src/router/mcp.rs, with the tool table
// and, since update_order asks, each route's check (D98). Regenerate with:
//
//	go test ./internal/generator/rustrestgen -run TestMCPRouterGolden -update
func TestMCPRouterGolden(t *testing.T) {
	generated := writeGoldenAPI(t, "fixture-mcp", mustGenerateMCPFixture(t, true, nil))

	router := generated["src/router.rs"]
	for _, want := range []string{
		"\nmod mcp;\n",
		"    let routes = router\n        .with_state(Arc::clone(&state))\n        .layer(axum::middleware::from_fn(request_ids));\n",
		"    let endpoint = mcp::endpoint(&routes, &state, &options, false);\n    routes\n        .merge(endpoint)\n        .merge(openapi_router(",
		"/// Always, for this API: update_order runs only once the\n/// person confirms the call (D93)",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.rs does not mount the MCP endpoint: missing %q", want)
		}
	}
	module := generated["src/"+MCPFile]
	for _, want := range []string{
		"        requires_auth: false,\n",
		"        checks: Some(checks(state, options.transport, manual_routes)),\n",
		"        authenticator: None,\n        service_authenticator: None,\n",
		// Every route has its check, the unclassified and the hidden
		// operations' too, so a request resolves as the router resolves it.
		`    router = router.route("/api/orders/export", get(check_order_export_orders));`,
		`    router = router.route("/api/orders/{id}", patch(check_order_update_order));`,
		`    router = router.route("/api/orders/{id}", delete(check_order_delete_order));`,
		"    if let Err(err) = decode_order_update_order(&path_params, body) {\n        return error_response(err);\n    }\n    reached()\n",
		"        .fallback(|| async { reached() })\n",
	} {
		if !strings.Contains(module, want) {
			t.Errorf("mcp.rs is not the endpoint's module: missing %q", want)
		}
	}
}

// TestMCPToolsAreTheTypeScriptRoutersTools: a Rust API's tool table is the
// one the TypeScript router's mcp.ts holds for the same API (tsrestgen's
// golden for fixture-mcp), which tools/schema.json's source writes for
// both (D116): each tool's name, title, description, input schema,
// annotations and _meta, where each argument goes, and whether it asks.
// It names its operation as the crate's operation table does,
// <namespace>.<name>, where the TypeScript table names the router's key.
func TestMCPToolsAreTheTypeScriptRoutersTools(t *testing.T) {
	output := mustGenerateMCPFixture(t, true, nil)
	if output.MCP == nil || output.MCP.RequiresAuth {
		t.Fatalf("fixture-mcp's routes need no caller, so its endpoint needs none: %+v", output.MCP)
	}
	outDir := t.TempDir()
	if err := SetReplacePaths(output, testPaths, "/repo/schemas/dist/api/fixture-mcp"); err != nil {
		t.Fatal(err)
	}
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(outDir, "src", filepath.FromSlash(MCPFile)))
	if err != nil {
		t.Fatal(err)
	}
	tools := toolsOf(t, string(module))

	source, err := os.ReadFile(filepath.Join("..", "tsrestgen", "testdata", "golden", "fixture-mcp", "mcp.ts"))
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(source), "export const mcpTools: readonly McpRouteTool[] = ")
	if !ok {
		t.Fatalf("tsrestgen's golden mcp.ts declares no mcpTools")
	}
	var typescript []mcptools.Tool
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSpace(table), ";")), &typescript); err != nil {
		t.Fatalf("tsrestgen's golden mcpTools is not JSON: %v", err)
	}
	if len(tools) != len(typescript) || len(tools) == 0 {
		t.Fatalf("mcp.rs has %d tools, the TypeScript router's mcp.ts %d", len(tools), len(typescript))
	}
	operations := map[string]bool{}
	for _, endpoint := range output.AllEndpoints() {
		operations[endpoint.OperationKey()] = true
	}
	for i, tool := range tools {
		want := typescript[i]
		if got := "order." + want.Operation; tool.Operation != got || !operations[tool.Operation] {
			t.Errorf("%s runs through %q, want %q, an operation of the crate's table", tool.Name, tool.Operation, got)
		}
		want.Operation = tool.Operation
		gotJSON, _ := json.Marshal(tool)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("tool %d is not the TypeScript router's:\n got %s\nwant %s", i, gotJSON, wantJSON)
		}
	}
	if !slices.Equal(output.MCP.Confirming, []string{"update_order"}) {
		t.Errorf("Confirming = %v, want update_order", output.MCP.Confirming)
	}

	// An API without mcp writes no module, and drops one an earlier build
	// wrote; its router declares none.
	without := mustGenerateMCPFixture(t, false, nil)
	if err := SetReplacePaths(without, testPaths, "/repo/schemas/dist/api/fixture-mcp"); err != nil {
		t.Fatal(err)
	}
	if err := WriteAPI(without, outDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "src", filepath.FromSlash(MCPFile))); !os.IsNotExist(err) {
		t.Errorf("%s survived a build without mcp: %v", MCPFile, err)
	}
	router, err := os.ReadFile(filepath.Join(outDir, "src", "router.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mention := range []string{"mod mcp;", "mcp::", "MCP"} {
		if strings.Contains(string(router), mention) {
			t.Errorf("the router of an API without mcp has %s", mention)
		}
	}
}

// testPaths are where a crate written to a temporary directory finds the
// runtime, as the goldens' paths name it.
var testPaths = naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}

// TestMCPEndpointOfAGuardedAPI: the endpoint of an API whose routes need a
// caller needs one too, and its checks hold each route's controls but the
// rate limit, which counts the runs alone (D98): the permission check, the
// body limit, the service step and the timeout. A tool of a
// @manualRouteRegistration operation is checked where its route is
// mounted.
func TestMCPEndpointOfAGuardedAPI(t *testing.T) {
	limit, megabytes, seconds := 5, 2, 30
	output := mustGenerateMCPFixture(t, true, func(schema *ir.Schema) {
		eachOperation(schema, func(_ *ir.OperationSet, op *ir.FieldDef) {
			switch op.Name {
			case "updateOrder":
				op.Permissions = []string{"orders.write"}
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &limit, BodyLimit: &megabytes, Timeout: &seconds}
			case "getOrder":
				op.Auth = true
			case "openReturn":
				op.ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{"storefront"}}
			case "listOrders":
				op.ManualRouteRegistration = true
			}
		})
	})
	if !output.MCP.RequiresAuth {
		t.Fatal("an API with a route that needs a caller has an endpoint that needs none")
	}
	outDir := t.TempDir()
	if err := SetReplacePaths(output, testPaths, "/repo/schemas/dist/api/fixture-mcp"); err != nil {
		t.Fatal(err)
	}
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	read := func(file string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(outDir, "src", filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	router, module := read("router.rs"), read(MCPFile)
	for _, want := range []string{
		"    let manual_mounted = manual_routes.is_some();\n",
		"    let endpoint = mcp::endpoint(&routes, &state, &options, manual_mounted);\n",
		// The route keeps its rate limit; its check has none.
		".rate_limiter(limits.of(&crate::operations::ORDER_UPDATE_ORDER))",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.rs: missing %q", want)
		}
	}
	for _, want := range []string{
		"        requires_auth: true,\n",
		"        authenticator: Some(authenticator),\n",
		"        service_authenticator: Some(Arc::clone(&state.implementations.service_authenticator)),\n",
		"    let route = RouteControls::under(transport)\n        .body_limit_megabytes(2)\n        .identify_service(Arc::clone(&state.implementations.service_authenticator))\n        .authorize(Arc::clone(&authenticator), &[\"orders.write\"])\n        .timeout_seconds(30)\n        .apply(patch(check_order_update_order));\n",
		"        .require_service(Arc::clone(&state.implementations.service_authenticator), &[\"storefront\"])\n        .apply(post(check_order_open_return));\n",
		"    if manual_routes {\n        let route = RouteControls::under(transport)\n            .identify_service(Arc::clone(&state.implementations.service_authenticator))\n            .apply(get(check_request));\n        router = router.route(\"/api/orders\", route);\n    }\n",
		"async fn check_request(uri: Uri) -> Response {",
	} {
		if !strings.Contains(module, want) {
			t.Errorf("mcp.rs: missing %q", want)
		}
	}
	if strings.Contains(module, "rate_limit") {
		t.Errorf("a route's check takes its rate limit, which counts the runs alone (D98):\n%s", module)
	}
	// list_orders is a tool still: its route is the service's to mount.
	names := []string{}
	for _, tool := range toolsOf(t, module) {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "list_orders") {
		t.Errorf("the tools are %v, without the manual operation's list_orders", names)
	}
}

// TestMCPEndpointWithNoToolThatAsksHoldsNoChecks: only a call of a tool
// that asks is checked before it runs (D98), so an API none of whose tools
// asks writes no check, and its router needs no confirmation key.
func TestMCPEndpointWithNoToolThatAsksHoldsNoChecks(t *testing.T) {
	output := mustGenerateMCPFixture(t, true, func(schema *ir.Schema) {
		eachOperation(schema, func(_ *ir.OperationSet, op *ir.FieldDef) {
			if op.MCP != nil && !op.MCP.Hidden {
				op.MCP.Invocation = ir.MCPInvocation{Key: op.MCP.Invocation.Key, Value: "auto"}
			}
		})
	})
	if len(output.MCP.Confirming) != 0 {
		t.Fatalf("Confirming = %v, want none", output.MCP.Confirming)
	}
	outDir := t.TempDir()
	if err := SetReplacePaths(output, testPaths, "/repo/schemas/dist/api/fixture-mcp"); err != nil {
		t.Fatal(err)
	}
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(outDir, "src", filepath.FromSlash(MCPFile)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(module), "        checks: None,\n") || strings.Contains(string(module), "fn check") {
		t.Errorf("an endpoint with no tool that asks holds checks:\n%s", module)
	}
	router, err := os.ReadFile(filepath.Join(outDir, "src", "router.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(router), "confirmation_key") {
		t.Errorf("the router of an endpoint with no tool that asks names a confirmation key")
	}
}

// TestMCPEndpointRefusals: with mcp on, the build refuses an operation
// routed at the endpoint's path, which the endpoint would shadow (D81), and
// a tool that asks and takes an argument named confirmationToken, which
// carries its confirmation (D93), as the TypeScript API's build does.
func TestMCPEndpointRefusals(t *testing.T) {
	_, err := generateMCPFixture(t, true, func(schema *ir.Schema) {
		eachOperation(schema, func(_ *ir.OperationSet, op *ir.FieldDef) {
			if op.Name == "openReturn" {
				op.RestPath = "mcp"
			}
		})
	})
	if want := "rustrestgen: operation order.openReturn is routed at POST /api/mcp, where outputs.api.mcp serves the MCP endpoint (D81); give it another @rest path, or turn mcp off"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Generate = %v, want it refused saying %q", err, want)
	}
	_, err = generateMCPFixture(t, true, func(schema *ir.Schema) {
		eachOperation(schema, func(_ *ir.OperationSet, op *ir.FieldDef) {
			if op.Name == "updateOrder" {
				for _, arg := range op.Arguments {
					if arg.Name == "id" {
						arg.Name = mcptools.ConfirmationArgument
					}
				}
				op.RestPath = "orders/{" + mcptools.ConfirmationArgument + "}"
			}
		})
	})
	if want := `rustrestgen: MCP tool update_order of fixture-mcp asks the person before it runs (invocationPolicy "ask"), and takes an argument confirmationToken, which carries the call's confirmation token (D93); rename the argument`; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Generate = %v, want it refused saying %q", err, want)
	}
}

// asks makes the operation named name a visible tool whose policy is ask,
// documented as a visible tool must be.
func asks(schema *ir.Schema, name, handle string) {
	eachOperation(schema, func(_ *ir.OperationSet, op *ir.FieldDef) {
		if op.Name != name {
			return
		}
		op.Docs = &ir.OperationDocs{
			Title:         "The " + handle + " tool",
			Description:   "Runs " + name + ".",
			Capability:    "fixtures." + strings.ReplaceAll(handle, "_", "-"),
			Lifecycle:     "active",
			Visibility:    "public",
			MappingStatus: "mapped",
			ReplayMode:    "read_only",
		}
		op.MCP = &ir.OperationMCP{Handle: handle, Invocation: ir.MCPInvocation{Key: "invocationPolicy", Value: "ask"}}
	})
}

// TestMCPEndpointCratesBuild runs clippy, with warnings denied, and cargo
// test on the Rust API crates of fixtures built with outputs.api.mcp on
// and a tool that asks, so each holds its routes' checks (D98, D116):
// fixture-user-routes-api, which authenticates with the identity runtime
// and whose user model's routes the runtime serves; fixture-webhooks-api,
// whose @hmacVerified routes run their provider's verifier first, one of
// them a manual route; fixture-service-auth-api, whose routes run the
// service step; and fixture-api, with a manual route and permissions.
func TestMCPEndpointCratesBuild(t *testing.T) {
	for _, tc := range []struct {
		service, operation string
		edit               func(*ir.Schema)
		wants              []string
	}{
		{"fixture-user-routes-api", "greet", nil, []string{
			"    let authenticator: Arc<dyn Authenticator> = state.implementations.authenticator.clone();\n",
			// The user model's routes are the identity runtime's: a route's
			// check runs its controls, and a public route's none.
			"    router = router.route(\"/api/auth/login\", post(check_request));\n",
			"        .authorize(Arc::clone(&authenticator), &[])\n        .apply(get(check_request));\n    router = router.route(\"/api/auth/me\", route);\n",
		}},
		{"fixture-webhooks-api", "getEvent", nil, []string{
			"        webhook_verified(\n            post(check_webhook_receive_stripe_event),\n            Arc::clone(&state.implementations.webhook_verifiers[\"stripe\"]),\n        ),",
			"    if manual_routes {\n",
		}},
		{"fixture-service-auth-api", "getReservation", nil, []string{
			"        service_authenticator: Some(Arc::clone(&state.implementations.service_authenticator)),\n",
			".require_service(Arc::clone(&state.implementations.service_authenticator), &[\"fixture-service-caller-api\"])",
		}},
		{"fixture-api", "getTenant", withoutEncryption, []string{
			"    if manual_routes {\n",
			".authorize(Arc::clone(&authenticator), &[\"tenants.read\"])",
		}},
	} {
		t.Run(tc.service, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.service))
			if err != nil {
				t.Fatalf("load %s: %v", tc.service, err)
			}
			if tc.edit != nil {
				tc.edit(schema)
			}
			asks(schema, tc.operation, "ask_first")
			test := strings.ReplaceAll(mcpCrateTest, "OPERATION", tc.operation)
			cargoTestAPICrateWith(t, tc.service, schema, "mcp", test, cargoOptions{mcp: true}, func(apiDir string, output *APIOutput) error {
				if output.MCP == nil || !slices.Equal(output.MCP.Confirming, []string{"ask_first"}) {
					t.Errorf("%s's endpoint confirms %+v, want ask_first", tc.service, output.MCP)
				}
				module, err := os.ReadFile(filepath.Join(apiDir, "src", filepath.FromSlash(MCPFile)))
				if err != nil {
					return err
				}
				for _, want := range tc.wants {
					if !strings.Contains(string(module), want) {
						t.Errorf("%s's mcp.rs: missing %q", tc.service, want)
					}
				}
				return nil
			})
		})
	}
}

// mcpCrateTest is tests/mcp.rs of a generated API crate built with
// outputs.api.mcp on, with API_CRATE replaced by its module name and
// OPERATION by the operation its tool that asks runs through: the tool's
// operation is one of the crate's operation table, by which the endpoint
// finds its route.
const mcpCrateTest = `#[test]
fn the_tool_that_asks_runs_through_an_operation_of_the_crates_table() {
    assert!(API_CRATE::operations::ALL.iter().any(|operation| operation.name == "OPERATION"));
}
`
