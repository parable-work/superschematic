package generator

import (
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/mcp_endpoint_go.go
var mcpEndpointGoTest string

// goMCPFixture loads fixture-mcp, the tool documents' fixture, as a Go API
// with outputs.api.mcp on (#660) whose @envVars class McpKeys holds the
// secret its config names for the confirmations of update_order, which
// asks (D93); guard edits its operations first when set.
func goMCPFixture(t *testing.T, guard func(op *ir.FieldDef)) (*ir.Schema, *schemaconfig.SchemaConfig) {
	t.Helper()
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-mcp"))
	if err != nil {
		t.Fatalf("load fixture-mcp: %v", err)
	}
	cfg.Outputs["types"] = map[string]any{"go": map[string]any{"enabled": true}}
	cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageGo, "mcp": true, "mcpConfirmationKey": "McpKeys.MCP_CONFIRMATION_KEY"}
	schema.Types["McpKeys"] = &ir.TypeDef{Name: "McpKeys", Role: ir.RoleEmbeddedStruct, EnvVars: true, Fields: []*ir.FieldDef{
		{Name: "MCP_CONFIRMATION_KEY", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
	}}
	if guard != nil {
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				guard(op)
			}
		}
	}
	return schema, cfg
}

func buildGoMCPFixture(t *testing.T, guard func(op *ir.FieldDef)) string {
	t.Helper()
	outputRoot := testpaths.TempDir(t)
	schema, cfg := goMCPFixture(t, guard)
	if _, err := Run(schema, cfg, Options{
		OutputRoot: outputRoot,
		Paths:      testpaths.Local(t),
		Naming:     naming.Default(),
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	}); err != nil {
		t.Fatalf("run fixture-mcp: %v", err)
	}
	return outputRoot
}

// goToolTable is the tool table a Go API's mcp.go holds.
func goToolTable(t *testing.T, source string) []map[string]any {
	t.Helper()
	match := regexp.MustCompile("(?s)const mcpTools = `(.*?)`\n").FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("mcp.go holds no tool table:\n%s", source)
	}
	var tools []map[string]any
	if err := json.Unmarshal([]byte(match[1]), &tools); err != nil {
		t.Fatal(err)
	}
	return tools
}

// TestGoAPIServesItsMCPEndpoint builds fixture-mcp as a Go API with
// outputs.api.mcp on and drives the generated package's MCP endpoint
// through RegisterRoutes' router, the implementation behind it the test's
// own (testdata/mcp_endpoint_go.go): the scenario the TypeScript router
// (tsrestgen's mcp_runtime.test.ts) and the Rust one
// (TestRustAPIServesItsMCPEndpoint) are held to. It serves the visible
// tools and no hidden or unclassified operation; a call is a request to
// its route, which reads each argument where the operation takes it and
// refuses what breaks its rules; update_order, which asks, answers a
// confirmation token and runs with it once (D93), and a call its route
// would refuse is refused at once, with no token spent (D98); and
// RegisterRoutes refuses a Config with no confirmation key.
func TestGoAPIServesItsMCPEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go API's MCP endpoint in -short mode")
	}
	outputRoot := buildGoMCPFixture(t, nil)
	apiDir := APIDir(outputRoot, "fixture-mcp")
	if err := os.WriteFile(filepath.Join(apiDir, "mcp_endpoint_test.go"), []byte(mcpEndpointGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	goIn(t, apiDir, env, "mod", "tidy")
	goIn(t, apiDir, env, "vet", ".")
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "TestTheEndpoint", ".")
	cmd.Dir = apiDir
	cmd.Env = append(cmd.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test in %s: %v\n%s", apiDir, err, out)
	}
	t.Logf("the generated API's endpoint:\n%s", out)
}

// TestAGoAPIsToolTableIsTheTypeScriptRouters builds fixture-mcp as a Go
// API with outputs.api.mcp on (#660): mcp.go holds the tool table the
// TypeScript router's mcp.ts holds (tsrestgen's golden), tool for tool and
// member for member, each with its route's method and path beside it, as
// the Rust router's holds it with its route's key; and RegisterRoutes
// mounts the endpoint.
func TestAGoAPIsToolTableIsTheTypeScriptRouters(t *testing.T) {
	outputRoot := buildGoMCPFixture(t, nil)
	apiDir := APIDir(outputRoot, "fixture-mcp")
	source, err := os.ReadFile(filepath.Join(apiDir, apigen.MCPFile))
	if err != nil {
		t.Fatalf("the build wrote no mcp.go: %v", err)
	}
	golden, err := os.ReadFile(filepath.Join("tsrestgen", "testdata", "golden", "fixture-mcp", "mcp.ts"))
	if err != nil {
		t.Fatal(err)
	}
	ts := regexp.MustCompile(`(?s)export const mcpTools: readonly McpRouteTool\[\] = (\[.*?\n\]);`).FindSubmatch(golden)
	if ts == nil {
		t.Fatalf("the TypeScript golden holds no tool table:\n%s", golden)
	}
	var want []map[string]any
	if err := json.Unmarshal(ts[1], &want); err != nil {
		t.Fatal(err)
	}
	got := goToolTable(t, string(source))
	if len(got) != len(want) {
		t.Fatalf("the Go table has %d tools, the TypeScript one %d", len(got), len(want))
	}
	routes := map[string]string{"get_order": "GET /api/orders/{id}", "list_orders": "GET /api/orders", "open_return": "POST /api/returns", "update_order": "PATCH /api/orders/{id}"}
	for i, tool := range got {
		route := tool["method"].(string) + " " + tool["path"].(string)
		if routes[tool["name"].(string)] != route {
			t.Errorf("%s routes to %s", tool["name"], route)
		}
		// Each router names an operation by its own key: Go's, as Rust's,
		// is <namespace>.<name>.
		if tool["operation"] != "order."+want[i]["operation"].(string) {
			t.Errorf("%s names operation %v", tool["name"], tool["operation"])
		}
		for _, member := range []string{"method", "path", "idempotent", "operation"} {
			delete(tool, member)
		}
		delete(want[i], "operation")
		gotJSON, _ := json.Marshal(tool)
		wantJSON, _ := json.Marshal(want[i])
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("tool %d:\n go %s\n ts %s", i, gotJSON, wantJSON)
		}
	}
	routesGo, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MCP MCPOptions", "mcpHandler, err := mcpEndpoint(r, cfg, openAPIVersion)", "registerMCP(r, cfg, mcpHandler)"} {
		if !strings.Contains(string(routesGo), want) {
			t.Errorf("routes.go lacks %s", want)
		}
	}
	if !strings.Contains(string(source), `"update_order": true,`) || strings.Contains(string(source), "Permits: map[string]func(gohttp.Handler) gohttp.Handler{\n\t\t\t\"") {
		t.Errorf("mcp.go checks update_order's route and asks no permission:\n%s", source)
	}
}

func TestGoMCPEmitsInstructionsFromOutputsAPI(t *testing.T) {
	outputRoot := testpaths.TempDir(t)
	schema, cfg := goMCPFixture(t, nil)
	const primer = "Household primer: amounts are cents; negative = outflow."
	cfg.Outputs["api"].(map[string]any)["mcpInstructions"] = primer
	if _, err := Run(schema, cfg, Options{
		OutputRoot: outputRoot,
		Paths:      testpaths.Local(t),
		Naming:     naming.Default(),
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	}); err != nil {
		t.Fatalf("run fixture-mcp with mcpInstructions: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(APIDir(outputRoot, "fixture-mcp"), apigen.MCPFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`const mcpInstructions = "Household primer: amounts are cents; negative = outflow."`,
		`if opts.Instructions == "" {`,
		`opts.Instructions = mcpInstructions`,
		`}.Handler(opts)`,
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("mcp.go missing %q:\n%s", want, source)
		}
	}
}
