package servergen_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The TypeScript behaviors fixture (D103): fixture-workflow-api, whose
// @behaviorOperations sets serve the operations of fixture-workflow-db's
// Workflows, built as a TypeScript API with its MCP endpoint, and
// wf-ts-stack, which serves it from its default server on the local target.
const (
	// tsBehaviorsServicesRoot holds the loader's fixtures, the API and
	// its database among them.
	tsBehaviorsServicesRoot = "../../loader/tsreader/testdata/services"
	// tsBehaviorsStackRoot holds wf-ts-stack.
	tsBehaviorsStackRoot = "testdata/behaviors/typescript/services"
	tsBehaviorsStack     = "wf-ts-stack"
	tsBehaviorsServer    = "fixture-workflow-api"
	tsBehaviorsDB        = "fixture-workflow-db"
	// tsBehaviorsGoldenRoot holds the server's files as the output root
	// lays them out.
	tsBehaviorsGoldenRoot = "testdata/golden/behaviors/typescript"
)

// tsBehaviorsOrder is the order a build-all builds the fixture in.
var tsBehaviorsOrder = []string{tsBehaviorsDB, tsBehaviorsServer, tsBehaviorsStack}

// loadTypeScriptBehaviorsFixture loads the fixture with the core registry
// and stacktest's fake target. The API and its database are the loader's
// fixtures, whose configs it narrows to what the TypeScript server reads:
// the database's TypeScript types and DDL, with the Go types a DB service
// always generates, and the API's TypeScript types and server, with its
// MCP endpoint.
func loadTypeScriptBehaviorsFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tsBehaviorsOrder {
		root := tsBehaviorsServicesRoot
		if name == tsBehaviorsStack {
			root = tsBehaviorsStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[tsBehaviorsDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "typescript": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres", "sqlite"}},
	}
	f.configs[tsBehaviorsServer].Outputs = map[string]any{
		"types": map[string]any{"typescript": enabled},
		"api":   map[string]any{"enabled": true, "language": "TYPESCRIPT", "mcp": true},
	}
	return f
}

// tsBehaviorsFakePaths are tsFakePaths with the behaviors' core's npm
// package at its place in a checkout at repoRoot, as this repository's
// naming file has it.
func tsBehaviorsFakePaths(repoRoot string) naming.LocalPaths {
	paths := tsFakePaths(repoRoot)
	paths.RulesTypeScript = filepath.Join(repoRoot, "runtime", "rules", "typescript")
	return paths
}

// TestTypeScriptBehaviorOperationsServerGolden: the TypeScript server of
// fixture-workflow-api hands its router the repositories Deps holds, which
// run the behavior operations (D103); its package depends on the
// behaviors' core, which orm.ts imports; and its Dockerfile builds the
// core's WebAssembly module from the engine's Cargo workspace in a stage of
// its own and emits the core's package with it, the context holding the
// package, the workspace and the crates it reaches. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptBehaviorOperationsServerGolden -update
func TestTypeScriptBehaviorOperationsServerGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptBehaviorsFixture(t)
	f.build(t, repoRoot, tsBehaviorsFakePaths(repoRoot), tsBehaviorsOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, tsBehaviorsStack, tsBehaviorsServer)
	files := map[string]string{}
	for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
		files[filepath.Join("server", tsBehaviorsStack, tsBehaviorsServer, file)] = filepath.Join(dir, file)
	}
	compareGoldens(t, tsBehaviorsGoldenRoot, files)

	read := func(file string) string {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if main := read(servergen.TypeScriptMainFile); !strings.Contains(main, "orm: fixtureWorkflowApiDeps.orm") {
		t.Errorf("main.ts hands the router no repositories:\n%s", main)
	}
	if pkg := read(servergen.TypeScriptPackageFile); !strings.Contains(pkg, `"@superschematic/rules": "*"`) {
		t.Errorf("the package does not depend on the behaviors' core:\n%s", pkg)
	}
	dockerfile := read(servergen.DockerFile)
	for _, want := range []string{
		"FROM rust:${RUST_VERSION}-trixie AS rules",
		"cargo build --locked --profile dist --target wasm32-unknown-unknown --manifest-path runtime/engine/rust/Cargo.toml -p superschematic-rules-wasm",
		"COPY --from=rules /wasm/superschematic_rules_wasm.wasm runtime/rules/typescript/dist/",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("the Dockerfile has no %s:\n%s", want, dockerfile)
		}
	}
	ignore := strings.Split(read(servergen.DockerIgnoreFile), "\n")
	for _, want := range []string{"!runtime/rules/typescript", "!runtime/engine/rust", "!third_party/superscalar/crates", "runtime/rules/typescript/dist"} {
		if !slices.Contains(ignore, want) {
			t.Errorf("the build context has no %s:\n%s", want, strings.Join(ignore, "\n"))
		}
	}
}

// TestTypeScriptBehaviorOperationsServerWithoutTheCoresCheckout: with the
// naming file's [paths] rules_typescript unset, the image has no checkout
// of the behaviors' core to build its module from, so the server gets no
// Dockerfile, and says why; its entrypoint is written still.
func TestTypeScriptBehaviorOperationsServerWithoutTheCoresCheckout(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptBehaviorsFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), tsBehaviorsOrder...)
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), tsBehaviorsStack, tsBehaviorsServer)
	if _, err := os.Stat(filepath.Join(dir, servergen.DockerFile)); !os.IsNotExist(err) {
		t.Errorf("a Dockerfile was written without the core's checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, servergen.TypeScriptMainFile)); err != nil {
		t.Errorf("no entrypoint: %v", err)
	}
}
