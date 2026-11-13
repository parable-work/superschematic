package generator

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/mcp_endpoint.rs
var mcpEndpointTest string

// mcpFixture loads fixture-mcp, the tool documents' fixture, as a Rust API
// with outputs.api.mcp on (D116) whose @envVars class McpKeys holds the
// secret its config names for the confirmations of update_order, which
// asks (D93); guard edits its operations first when set.
func mcpFixture(t *testing.T, guard func(op *ir.FieldDef)) (*ir.Schema, *schemaconfig.SchemaConfig) {
	t.Helper()
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-mcp"))
	if err != nil {
		t.Fatalf("load fixture-mcp: %v", err)
	}
	cfg.Outputs["types"] = map[string]any{"rust": map[string]any{"enabled": true}}
	cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust, "mcp": true, "mcpConfirmationKey": "McpKeys.MCP_CONFIRMATION_KEY"}
	schema.Types["McpKeys"] = &ir.TypeDef{Name: "McpKeys", EnvVars: true, Fields: []*ir.FieldDef{
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

// TestRustAPIServesItsMCPEndpoint builds fixture-mcp as a Rust API with
// outputs.api.mcp on and drives the generated crate's MCP endpoint (D81,
// D116), both through build_router's router and through Api::handle with
// no async runtime under it: the scenario the TypeScript router is held to
// (tsrestgen's mcp_runtime.test.ts), case for case. It serves the visible
// tools and no hidden or unclassified operation; a call is a request to
// its route, which reads each argument where the operation takes it and
// refuses what breaks its rules; update_order, which asks, answers a
// confirmation token and runs with it once, or runs once a client that
// elicits accepts (D93), and a call its route would refuse is refused at
// once, with no one asked and no token spent (D98).
//
// The guarded build gives the routes callers: getOrder needs a permission,
// openReturn a caller or a listed calling service in the user's place, and
// updateOrder takes one request a minute. The endpoint then needs a
// caller, lists the tools whose routes admit it, and runs each call as
// that caller, whom the route's authenticator establishes again from the
// endpoint request's credentials. No route of it has a caller, a service
// step, a path argument and a body at once: such a route's generated
// handler takes eight arguments, which clippy refuses under -D warnings.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestRustAPIServesItsMCPEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's MCP endpoint in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's MCP endpoint")
	}
	paths := testpaths.Local(t)
	limit := 1
	for _, variant := range []struct {
		name    string
		feature string
		guard   func(op *ir.FieldDef)
	}{
		{name: "open"},
		{name: "guarded", feature: "guarded", guard: func(op *ir.FieldDef) {
			switch op.Name {
			case "getOrder":
				op.Permissions = []string{"orders.read"}
			case "updateOrder":
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &limit}
			case "openReturn":
				op.Auth = true
				op.ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: []string{"storefront"}}
			}
		}},
	} {
		t.Run(variant.name, func(t *testing.T) {
			outputRoot := testpaths.TempDir(t)
			schema, cfg := mcpFixture(t, variant.guard)
			if _, err := Run(schema, cfg, Options{
				OutputRoot: outputRoot,
				Paths:      paths,
				Naming:     naming.Default(),
				Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			}); err != nil {
				t.Fatalf("run fixture-mcp: %v", err)
			}
			apiDir := APIDir(outputRoot, "fixture-mcp")
			module, err := os.ReadFile(filepath.Join(apiDir, "src", "router", "mcp.rs"))
			if err != nil {
				t.Fatalf("the build wrote no MCP module: %v", err)
			}
			if guarded := strings.Contains(string(module), "requires_auth: true"); guarded != (variant.guard != nil) {
				t.Fatalf("the endpoint needs a caller: %v", guarded)
			}

			crateDir := filepath.Join(outputRoot, "server", "mcp-endpoint")
			for _, dir := range []string{"src", "tests"} {
				if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
			if err != nil {
				t.Fatalf("the HTTP runtime's lockfile: %v", err)
			}
			for name, content := range map[string]string{
				"Cargo.toml":        fmt.Sprintf(mcpEndpointManifest, naming.Default().RustAPICrate("fixture-mcp"), filepath.ToSlash(apiDir)),
				"Cargo.lock":        string(lock),
				"src/lib.rs":        "//! The generated Rust server of fixture-mcp and its MCP endpoint: see tests/.\n",
				"tests/endpoint.rs": mcpEndpointTest,
			} {
				if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			target := os.Getenv("CARGO_TARGET_DIR")
			if target == "" {
				target = filepath.Join(outputRoot, "target")
			}
			test := []string{"test", "--quiet"}
			if variant.feature != "" {
				test = append(test, "--features", variant.feature)
			}
			for _, args := range [][]string{
				{"clippy", "--quiet", "--manifest-path", filepath.Join(apiDir, "Cargo.toml"), "--", "-D", "warnings"},
				test,
			} {
				cmd := exec.Command(cargo, args...)
				cmd.Dir = crateDir
				cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
				}
				t.Logf("cargo %s:\n%s", args[0], out)
			}
		})
	}
}

// mcpEndpointManifest is the test crate's Cargo.toml, given the API
// crate's name and its directory. Its guarded feature picks the cases of
// the build whose routes need callers.
const mcpEndpointManifest = `# The generated Rust server of fixture-mcp and its MCP endpoint (D116).
[package]
name = "mcp-endpoint"
version = "0.0.0"
edition = "2021"
publish = false

[features]
guarded = []

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
http = "1"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread"] }
api = { package = %q, path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`

// TestARustAPIWhoseToolsAskHoldsItsConfirmationKey: a Rust API whose MCP
// endpoint serves a tool that asks names the secret it signs confirmations
// with, as a TypeScript API does (D93, D116): the build refuses it without
// outputs.api.mcpConfirmationKey, and with a key its @envVars lacks.
func TestARustAPIWhoseToolsAskHoldsItsConfirmationKey(t *testing.T) {
	build := func(edit func(*ir.Schema, *schemaconfig.SchemaConfig)) error {
		schema, cfg := mcpFixture(t, nil)
		edit(schema, cfg)
		_, err := Run(schema, cfg, Options{
			OutputRoot: testpaths.TempDir(t),
			Paths:      naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust", SchemaRuntimeRust: "/repo/runtime/schema/rust"},
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		})
		return err
	}
	if err := build(func(*ir.Schema, *schemaconfig.SchemaConfig) {}); err != nil {
		t.Fatalf("a Rust API with mcp on and its key: %v", err)
	}
	err := build(func(_ *ir.Schema, cfg *schemaconfig.SchemaConfig) {
		cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust, "mcp": true}
	})
	if want := `generator: fixture-mcp serves MCP tools that run only once the person confirms the call (update_order), and its endpoint signs their confirmations with an @envVars Secret<string> its config names; set outputs.api.mcpConfirmationKey in fixture-mcp's config`; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("without a key: %v, want it to say %q", err, want)
	}
	err = build(func(schema *ir.Schema, _ *schemaconfig.SchemaConfig) {
		delete(schema.Types, "McpKeys")
	})
	if want := `generator: fixture-mcp's MCP endpoint signs the confirmations of update_order with the secret McpKeys.MCP_CONFIRMATION_KEY (outputs.api.mcpConfirmationKey in fixture-mcp's config), and`; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("without the field: %v, want it to say %q", err, want)
	}
}
