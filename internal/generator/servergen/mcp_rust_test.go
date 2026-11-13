package servergen_test

import (
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// rustMCPConfirmationKey is the environment's value of the secret
// rs-catalog's outputs.api.mcpConfirmationKey names (D93).
const rustMCPConfirmationKey = "MCP_CONFIRMATION_KEY=a random key of at least thirty-two bytes"

// loadRustMCPFixture is the Rust fixture with outputs.api.mcp on in
// rs-catalog, which rs-merged-stack's Warehouse serves beside rs-pricing
// (D116): getEntry is the visible tool get_entry, whose policy is ask, and
// the API's @envVars class McpKeys holds the secret its config names for
// the tool's confirmations (D93).
func loadRustMCPFixture(t *testing.T) fixture {
	t.Helper()
	f := loadRustFixture(t)
	catalog := f.schemas["rs-catalog"]
	for _, set := range catalog.OperationSets {
		for _, op := range set.Operations {
			if op.Name != "getEntry" {
				continue
			}
			op.Docs = &ir.OperationDocs{
				Title:         "Get an entry",
				Description:   "Returns one entry of the catalog, named by its quote.",
				Capability:    "catalog.entries.get",
				Lifecycle:     "active",
				Visibility:    "public",
				MappingStatus: "mapped",
				ReplayMode:    "read_only",
			}
			op.MCP = &ir.OperationMCP{Handle: "get_entry", Invocation: ir.MCPInvocation{Key: "invocationPolicy", Value: "ask"}}
		}
	}
	catalog.Types["McpKeys"] = &ir.TypeDef{Name: "McpKeys", EnvVars: true, Fields: []*ir.FieldDef{
		{Name: "MCP_CONFIRMATION_KEY", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
	}}
	f.configs["rs-catalog"] = withRustMCP(f, "rs-catalog", "McpKeys.MCP_CONFIRMATION_KEY")
	return f
}

// withRustMCP is the fixture's config of the Rust API service with
// outputs.api.mcp on, and key, when set, the secret its confirmations are
// signed with.
func withRustMCP(f fixture, service, key string) *schemaconfig.SchemaConfig {
	cfg := *f.configs[service]
	api := map[string]any{"enabled": true, "language": "RUST", "mcp": true}
	if key != "" {
		api["mcpConfirmationKey"] = key
	}
	cfg.Outputs = maps.Clone(cfg.Outputs)
	cfg.Outputs["api"] = api
	return &cfg
}

// TestRustEntrypointBuildsItsRouterWithTheMCPConfirmationKey: a Rust
// server's main.rs builds the router of an API whose MCP endpoint serves a
// tool that asks with the secret outputs.api.mcpConfirmationKey names, read
// from the API's config, as a TypeScript server's main.ts does (D93, D116),
// laid out as rustfmt lays it out. The APIs of one server share its paths,
// so the stack's build refuses a second API with mcp on (D81).
func TestRustEntrypointBuildsItsRouterWithTheMCPConfirmationKey(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadRustMCPFixture(t)
	f.build(t, repoRoot, rustFakePaths(repoRoot), rustOrder...)
	path := filepath.Join(servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), rustMergedStack, rustMergedServer), filepath.FromSlash(servergen.RustMainFile))
	main, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"use superschematic_http_runtime::mcp::EndpointOptions;\n",
		"    // Its MCP endpoint signs the confirmations of its tools that ask with\n    // the secret outputs.api.mcpConfirmationKey names (D93, D116).\n" +
			"    let rs_catalog_router_options = rs_catalog_api::RouterOptions {\n" +
			"        mcp: EndpointOptions::default()\n" +
			"            .with_confirmation_key(rs_catalog_config.mcp_confirmation_key.as_str()),\n" +
			"        ..rs_catalog_api::RouterOptions::default()\n" +
			"    };\n",
		"rs_catalog_api::build_router_with(rs_catalog_implementations, rs_catalog_router_options)",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("Warehouse's main.rs does not build rs-catalog's router with the confirmation key: missing %q\n%s", want, main)
		}
	}
	if strings.Contains(string(main), "rs_pricing_router_options") {
		t.Errorf("rs-pricing, whose mcp is off, has router options:\n%s", main)
	}
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", path).CombinedOutput(); err != nil {
			t.Errorf("rustfmt lays out Warehouse's main.rs otherwise: %v\n%s", err, diff)
		}
	}

	f.configs["rs-pricing"] = withRustMCP(f, "rs-pricing", "")
	repoRoot = t.TempDir()
	options := f.options(repoRoot, rustFakePaths(repoRoot))
	for _, name := range rustOrder {
		_, err := generator.Run(f.schemas[name], f.configs[name], options)
		if name != rustMergedStack {
			if err != nil {
				t.Fatalf("build %s: %v", name, err)
			}
			continue
		}
		want := "stack rs-merged-stack: server Warehouse serves rs-catalog and rs-pricing, which both turn on outputs.api.mcp, and one server answers POST /api/mcp for one API; serve them from two servers, or turn mcp off in one (D81)"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build %s = %v, want it refused saying %q", name, err, want)
		}
	}
}

// TestRustServerServesItsMCPEndpoint: a generated Rust server serves its
// API's visible @mcp operations as MCP tools at POST /api/mcp (D81, D116),
// over a socket, as a generated TypeScript server does
// (TestTypeScriptMCPEndpointServes). Warehouse, built with cargo in the
// output root's workspace, reads the confirmation key from the variable
// rs-catalog's config field names and builds rs-catalog's router with it:
// a client initializes, lists get_entry, whose policy is ask, and calls
// it. The call answers a confirmation token and runs nothing; a call its
// route would refuse answers the route's 400 at once, with no token
// (D98); and the call with its token runs through get_entry's route, once
// (D93), whose implementation asks rs-pricing, which Warehouse serves
// too, for its quote. Without the key's variable the server stops at
// once, naming it.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestRustServerServesItsMCPEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadRustMCPFixture(t)
	f.build(t, repoRoot, paths, rustOrder...)
	writeRustImplementations(t, repoRoot)
	f.build(t, repoRoot, paths, rustMergedStack)
	out := filepath.Join(repoRoot, "schemas", "dist")
	target := rustTargetDir(t)
	server := naming.Default().RustServerCrate(rustMergedStack, rustMergedServer)
	cargoCommand(t, cargo, out, target, "clippy", "--locked", "--quiet", "-p", server, "--", "-D", "warnings")
	cargoCommand(t, cargo, out, target, "build", "--locked", "--quiet", "-p", server)
	binary := filepath.Join(target, "debug", rustMergedServer)

	callers := ir.CallersField("rs-pricing") + "_ISSUERS=0"
	missing := exec.Command(binary)
	missing.Env = append(os.Environ(), callers, "RS_PRICING_SERVICE_URL=http://127.0.0.1:9", "PORT="+freePort(t))
	if output, err := missing.CombinedOutput(); err == nil || !strings.Contains(string(output), "MCP_CONFIRMATION_KEY") {
		t.Errorf("Warehouse without its confirmation key: %v, want it to stop naming MCP_CONFIRMATION_KEY\n%s", err, output)
	}

	port := freePort(t)
	warehouse := startOn(t, port, binary, callers, "RS_PRICING_SERVICE_URL=http://127.0.0.1:"+port, rustMCPConfirmationKey)
	warehouse.expect(t, http.MethodGet, "/readyz", http.StatusOK, `"ready"`)

	status, init := warehouse.rpc(t, "", initialize)
	if status != http.StatusOK || init["result"] == nil {
		t.Fatalf("initialize = %d %v", status, init)
	}
	if got := mustMarshal(t, init["result"].(map[string]any)["serverInfo"]); got != `{"name":"rs-catalog","version":"1.0.0"}` {
		t.Errorf("initialize names the server %s", got)
	}
	if status, _ := warehouse.rpc(t, "", `{"jsonrpc": "2.0", "method": "notifications/initialized"}`); status != http.StatusAccepted {
		t.Errorf("notifications/initialized = %d, want 202", status)
	}
	_, listed := warehouse.rpc(t, "", `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`)
	tools, _ := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools/list = %v, want get_entry alone", listed)
	}
	tool := tools[0].(map[string]any)
	schema, _ := tool["inputSchema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	if tool["name"] != "get_entry" || tool["title"] != "Get an entry" || tool["_meta"].(map[string]any)["invocationPolicy"] != "ask" || properties["confirmationToken"] == nil {
		t.Errorf("get_entry is listed as %v", tool)
	}
	if got := slices.Sorted(maps.Keys(properties)); !slices.Equal(got, []string{"confirmationToken", "sku"}) {
		t.Errorf("get_entry takes %v, want sku and its confirmation token", got)
	}

	// get_entry asks: its first call runs nothing and answers a token.
	asked := warehouse.toolCall(t, "", "get_entry", `{"sku": "tea"}`)
	problem, _ := asked["structuredContent"].(map[string]any)
	details, _ := problem["details"].(map[string]any)
	token, _ := details["confirmationToken"].(string)
	if asked["isError"] != true || problem["status"] != float64(428) || problem["code"] != "confirmation_required" || token == "" {
		t.Fatalf("get_entry's first call answered %v, want confirmation_required with a token", asked)
	}
	// A call the route would refuse answers its 400 at once, with no token
	// to confirm, and the token sent with it is not spent (D98).
	refused := warehouse.toolCall(t, "", "get_entry", `{"sku": "tea", "input": {}, "confirmationToken": "`+token+`"}`)
	if problem, _ := refused["structuredContent"].(map[string]any); refused["isError"] != true || problem["status"] != float64(400) || problem["detail"] != `get_entry takes sku, not "input"` {
		t.Errorf("get_entry with an argument it does not take answered %v, want the 400 at once", refused)
	}
	got := warehouse.toolCall(t, "", "get_entry", `{"sku": "tea", "confirmationToken": "`+token+`"}`)
	if mustMarshal(t, got["structuredContent"]) != `{"name":"tea:Warehouse","sku":"tea"}` || got["isError"] != nil {
		t.Errorf("get_entry with its token answered %v", got)
	}
	replayed := warehouse.toolCall(t, "", "get_entry", `{"sku": "tea", "confirmationToken": "`+token+`"}`)
	if problem, _ := replayed["structuredContent"].(map[string]any); problem["code"] != "confirmation_required" || mustMarshal(t, problem["details"].(map[string]any)["refused"]) != `"used"` {
		t.Errorf("get_entry with a used token answered %v, want it asked about again", replayed)
	}
	_, reply := warehouse.rpc(t, "", `{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": {"name": "get_quote", "arguments": {}}}`)
	if mustMarshal(t, reply["error"]) != `{"code":-32602,"message":"rs-catalog has no tool \"get_quote\""}` {
		t.Errorf("tools/call get_quote answered %v, want the invalid-params error of a tool the API does not offer", reply)
	}
	warehouse.expect(t, http.MethodGet, "/api/mcp", http.StatusMethodNotAllowed)
	warehouse.stop(t)
}
