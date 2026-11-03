package tsrestgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// mcpFixture is fixture-mcp, the tool documents' fixture: visible, hidden
// and unclassified operations, every replay mode, an invocation policy of
// ask, a nested object, a typed map and a list query parameter; rewrite
// edits the loaded schema first when set.
func mcpFixture(t *testing.T, rewrite func(*ir.Schema)) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-mcp"))
	if err != nil {
		t.Fatalf("load fixture-mcp: %v", err)
	}
	if rewrite != nil {
		rewrite(schema)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: "fixture-mcp", Clock: fixedClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: "fixture-mcp", schema: schema, endpoints: endpoints, mcp: true}
}

func generateMCPFixture(t *testing.T, fixture apiFixture) *APIOutput {
	t.Helper()
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Clock: fixedClock, MCP: fixture.mcp})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return output
}

// TestMCPRouterGolden pins the router and mcp.ts of an API with
// outputs.api.mcp on (D81). Regenerate with:
//
//	go test ./internal/generator/tsrestgen -run TestMCPRouterGolden -update
func TestMCPRouterGolden(t *testing.T) {
	outDir := t.TempDir()
	if err := WriteAPI(generateMCPFixture(t, mcpFixture(t, nil)), outDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"router.ts", MCPFile, "README.md"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join("testdata", "golden", "fixture-mcp", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%s has no golden (run with -update): %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

// mcpToolsOf reads the tool table mcp.ts declares.
func mcpToolsOf(t *testing.T, dir string) []MCPTool {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(dir, MCPFile))
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(source), "export const mcpTools: readonly McpRouteTool[] = ")
	if !ok {
		t.Fatalf("mcp.ts declares no mcpTools:\n%s", source)
	}
	var tools []MCPTool
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSpace(table), ";")), &tools); err != nil {
		t.Fatalf("mcp.ts's mcpTools is not JSON: %v", err)
	}
	return tools
}

// TestMCPToolsAreTheToolDocuments: the endpoint serves the visible @mcp
// tools of the tool documents the SDK generators write (sdkgen's golden
// tools/schema.json for fixture-mcp), as the engine lists its tools: the
// handle as the name, the @docs title and description, the documents'
// parameters as the input schema, a read-only hint from the replay mode,
// and _meta with the guidance and the invocation policy. A hidden or
// unclassified operation is no tool. Each argument goes where the route
// reads it.
func TestMCPToolsAreTheToolDocuments(t *testing.T) {
	outDir := t.TempDir()
	output := generateMCPFixture(t, mcpFixture(t, nil))
	if output.MCP == nil || output.MCP.RequiresAuth {
		t.Fatalf("fixture-mcp's routes need no caller, so its endpoint needs none: %+v", output.MCP)
	}
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	tools := mcpToolsOf(t, outDir)

	data, err := os.ReadFile(filepath.Join("..", "sdkgen", "testdata", "golden", "fixture-mcp", "tools", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest ir.ToolManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	var visible []ir.ToolManifestTool
	for _, tool := range manifest.Tools {
		if tool.MCP != nil && !tool.MCP.Hidden {
			visible = append(visible, tool)
		}
	}
	if len(tools) != len(visible) {
		t.Fatalf("mcp.ts has %d tools, tools/schema.json %d visible ones", len(tools), len(visible))
	}
	for i, tool := range tools {
		want := visible[i]
		if tool.Name != want.MCP.Handle || tool.Title != want.MCP.Name || tool.Description != want.MCP.Description {
			t.Errorf("tool %d = %s %q %q, want %s %q %q", i, tool.Name, tool.Title, tool.Description, want.MCP.Handle, want.MCP.Name, want.MCP.Description)
		}
		if got, want := compactJSON(t, tool.InputSchema), compactJSON(t, mustJSON(t, want.Parameters)); got != want {
			t.Errorf("%s's input schema is not tools/schema.json's parameters:\n got %s\nwant %s", tool.Name, got, want)
		}
		if readOnly := want.Replay != nil && want.Replay.Mode == "read_only"; tool.Annotations.ReadOnlyHint != readOnly {
			t.Errorf("%s readOnlyHint = %v, want %v", tool.Name, tool.Annotations.ReadOnlyHint, readOnly)
		}
		if tool.Meta["invocationPolicy"] != want.MCP.Invocation.Value || tool.Meta[apigen.DefaultToolGuidanceKey] == nil {
			t.Errorf("%s _meta = %v, want the guidance and policy %q", tool.Name, tool.Meta, want.MCP.Invocation.Value)
		}
	}

	arguments := map[string][]MCPToolArgument{}
	operations := map[string]string{}
	for _, tool := range tools {
		arguments[tool.Name] = tool.Arguments
		operations[tool.Name] = tool.Operation
	}
	for name, want := range map[string][]MCPToolArgument{
		"list_orders":  {{Key: "statuses", In: "query", Name: "statuses"}, {Key: "limit", In: "query", Name: "limit"}},
		"get_order":    {{Key: "id", In: "path", Name: "id"}},
		"update_order": {{Key: "id", In: "path", Name: "id"}, {Key: "revision", In: "body", Name: "revision"}, {Key: "note", In: "body", Name: "note"}},
		"open_return": {
			{Key: "requestId", In: "body", Name: "requestId"}, {Key: "orderId", In: "body", Name: "orderId"}, {Key: "reason", In: "body", Name: "reason"},
			{Key: "pickup", In: "body", Name: "pickup"}, {Key: "labels", In: "body", Name: "labels"},
		},
	} {
		if got := mustJSON(t, arguments[name]); got != mustJSON(t, want) {
			t.Errorf("%s's arguments = %s, want %s", name, got, mustJSON(t, want))
		}
	}
	if operations["update_order"] != "updateOrder" {
		t.Errorf("update_order runs through %q, want updateOrder", operations["update_order"])
	}
	// update_order's policy is ask, which the core's policy confirms (D93).
	for _, tool := range tools {
		if tool.Confirm != (tool.Name == "update_order") {
			t.Errorf("%s confirm = %v", tool.Name, tool.Confirm)
		}
	}
	if !slices.Equal(output.MCP.Confirming, []string{"update_order"}) {
		t.Errorf("Confirming = %v, want update_order", output.MCP.Confirming)
	}
	for _, absent := range []string{"delete_order", "exportOrders"} {
		if _, ok := arguments[absent]; ok {
			t.Errorf("%s, which is no visible tool, is served", absent)
		}
	}

	// An API without mcp writes no mcp.ts, and drops one an earlier build
	// wrote; its router imports none.
	fixture := mcpFixture(t, nil)
	fixture.mcp = false
	if err := WriteAPI(generateMCPFixture(t, fixture), outDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outDir, MCPFile)); !os.IsNotExist(err) {
		t.Errorf("%s survived a build without mcp: %v", MCPFile, err)
	}
	router, err := os.ReadFile(filepath.Join(outDir, "router.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mention := range []string{"mountMcp", "./mcp'", "/mcp'"} {
		if strings.Contains(string(router), mention) {
			t.Errorf("the router of an API without mcp has %s:\n%s", mention, router)
		}
	}
}

// TestOpenAPIModuleIsTheDocument: openapi.ts embeds openapi.json, which the
// router serves without reading a file (D81).
func TestOpenAPIModuleIsTheDocument(t *testing.T) {
	outDir := t.TempDir()
	output := generateFixtureAPI(t)
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, OpenAPIFile))
	if err != nil {
		t.Fatal(err)
	}
	_, literal, ok := strings.Cut(string(source), "export const openAPIJSON: string = ")
	if !ok {
		t.Fatalf("openapi.ts declares no openAPIJSON:\n%s", source)
	}
	var embedded string
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSpace(literal), ";")), &embedded); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(outDir, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if embedded != string(written) {
		t.Error("openapi.ts does not embed openapi.json")
	}
}

// TestMCPEndpointPathIsTheEndpoints: an operation routed at /api/mcp would
// be shadowed by the endpoint, so the build refuses it with mcp on.
func TestMCPEndpointPathIsTheEndpoints(t *testing.T) {
	fixture := mcpFixture(t, func(schema *ir.Schema) {
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				if op.Name == "openReturn" {
					op.RestPath = "mcp"
				}
			}
		}
	})
	_, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Clock: fixedClock, MCP: true})
	if want := "is routed at POST /api/mcp, where outputs.api.mcp serves the MCP endpoint (D81)"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Generate = %v, want it refused saying %q", err, want)
	}
}

// TestMCPToolsConfirmByTheBuildsPolicy: which tools the endpoint confirms
// is the build's invocation policy's to say (D93): under an extension's
// policy that confirms always, update_order (always) confirms and the
// others (never) do not; a tool that confirms and takes an argument named
// confirmationToken, which carries its confirmation, is refused.
func TestMCPToolsConfirmByTheBuildsPolicy(t *testing.T) {
	policy := apigen.ToolInvocationPolicy{Extension: "acme", Key: "confirm", Values: []string{"never", "always"}, Default: "never", Confirm: []string{"always"}}
	rewrite := func(schema *ir.Schema) {
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				if op.MCP != nil && !op.MCP.Hidden {
					value := "never"
					if op.MCP.Invocation.Value == apigen.ToolInvocationAsk {
						value = "always"
					}
					op.MCP.Invocation = ir.MCPInvocation{Key: "confirm", Value: value}
				}
			}
		}
	}
	generate := func(rewrites ...func(*ir.Schema)) (*APIOutput, error) {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-mcp"))
		if err != nil {
			t.Fatalf("load fixture-mcp: %v", err)
		}
		for _, rewrite := range rewrites {
			rewrite(schema)
		}
		endpoints, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: "fixture-mcp", Clock: fixedClock, ToolInvocation: policy})
		if err != nil {
			t.Fatalf("apigen.Generate: %v", err)
		}
		return Generate(schema, endpoints, Options{SchemaName: "fixture-mcp", Clock: fixedClock, MCP: true})
	}
	output, err := generate(rewrite)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(output.MCP.Confirming, []string{"update_order"}) {
		t.Errorf("under confirm: always, Confirming = %v, want update_order", output.MCP.Confirming)
	}

	_, err = generate(rewrite, func(schema *ir.Schema) {
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				if op.Name == "updateOrder" {
					for _, arg := range op.Arguments {
						if arg.Name == "id" {
							arg.Name = MCPConfirmationArgument
						}
					}
					op.RestPath = "orders/{" + MCPConfirmationArgument + "}"
				}
			}
		}
	})
	if want := `MCP tool update_order of fixture-mcp asks the person before it runs (confirm "always"), and takes an argument confirmationToken, which carries the call's confirmation token (D93); rename the argument`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Generate = %v, want it refused saying %q", err, want)
	}
}

// TestGeneratedMCPRuntime boots fixture-mcp's router, built with mcp on,
// under bun: it type-checks, serves its OpenAPI document and page, and
// serves its MCP tools, each call through its route.
func TestGeneratedMCPRuntime(t *testing.T) {
	tree := materializeAPI(t, mcpFixture(t, nil))
	tree.typeCheck(t)
	tree.runTest(t, "mcp_runtime.test.ts")
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// compactJSON re-encodes a JSON document with its keys sorted.
func compactJSON(t *testing.T, data any) string {
	t.Helper()
	var raw []byte
	switch value := data.(type) {
	case json.RawMessage:
		raw = value
	case string:
		raw = []byte(value)
	default:
		t.Fatalf("compactJSON of %T", data)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return mustJSON(t, decoded)
}
