package servergen_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The TypeScript graphs fixture (D138): fixture-graph-api, whose
// @graphOperations set serves the operations of fixture-version-graph-db's
// Recipe graph, built as a TypeScript API with its MCP endpoint, and
// graph-ts-stack, which serves it from its default server on the local
// target.
const (
	tsGraphsStackRoot  = "testdata/graphs/typescript/services"
	tsGraphsStack      = "graph-ts-stack"
	tsGraphsServer     = "fixture-graph-api"
	tsGraphsDB         = "fixture-version-graph-db"
	tsGraphsGoldenRoot = "testdata/golden/graphs/typescript"
)

// tsGraphsOrder is the order a build-all builds the fixture in.
var tsGraphsOrder = []string{tsGraphsDB, tsGraphsServer, tsGraphsStack}

// loadTypeScriptGraphsFixture loads the fixture with the core registry and
// stacktest's fake target. The API and its database are the loader's
// fixtures, whose configs it narrows to what the TypeScript server reads:
// the database's TypeScript types and DDL, with the Go types a DB service
// always generates, and the API's TypeScript types and server, with its
// MCP endpoint.
func loadTypeScriptGraphsFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tsGraphsOrder {
		root := tsBehaviorsServicesRoot
		if name == tsGraphsStack {
			root = tsGraphsStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[tsGraphsDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "typescript": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}
	f.configs[tsGraphsServer].Outputs = map[string]any{
		"types": map[string]any{"typescript": enabled},
		"api":   map[string]any{"enabled": true, "language": "TYPESCRIPT", "mcp": true},
	}
	return f
}

// tsGraphsFakePaths are tsFakePaths with the version-graph runtime's npm
// package at its place in a checkout at repoRoot, as this repository's
// naming file has it.
func tsGraphsFakePaths(repoRoot string) naming.LocalPaths {
	paths := tsFakePaths(repoRoot)
	paths.VersionGraphTypeScript = filepath.Join(repoRoot, "runtime", "versiongraph", "typescript")
	return paths
}

// TestTypeScriptGraphOperationsServerGolden: the TypeScript server of
// fixture-graph-api hands its router the repositories Deps holds and the
// Recipe graph's facade, built over the database's pool with the
// version-graph package's Postgres client (D138), and its package depends
// on that package. Its image builds the version-graph core's WebAssembly
// module in a stage of its own, from the core's crate beside the package,
// and emits the package's dist/ with it, which the workspace's root
// overrides the package with. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptGraphOperationsServerGolden -update
func TestTypeScriptGraphOperationsServerGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptGraphsFixture(t)
	f.build(t, repoRoot, tsGraphsFakePaths(repoRoot), tsGraphsOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, tsGraphsStack, tsGraphsServer)
	files := map[string]string{}
	for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
		files[filepath.Join("server", tsGraphsStack, tsGraphsServer, file)] = filepath.Join(dir, file)
	}
	compareGoldens(t, tsGraphsGoldenRoot, files)

	read := func(file string) string {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	main := read(servergen.TypeScriptMainFile)
	for _, want := range []string{
		"import { RecipeGraph } from '@schemas/fixture-version-graph-db-types/versiongraph';",
		"import { pgPool as versionGraphPool } from '@superschematic/versiongraph/postgres';",
		"orm: fixtureGraphApiDeps.orm",
		"graphs: { recipe: new RecipeGraph(versionGraphPool(fixtureVersionGraphDbPool)) }",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.ts lacks %q:\n%s", want, main)
		}
	}
	if pkg := read(servergen.TypeScriptPackageFile); !strings.Contains(pkg, `"@superschematic/versiongraph": "*"`) || !strings.Contains(pkg, `"@schemas/fixture-version-graph-db-types": "workspace:*"`) {
		t.Errorf("the package does not depend on the version-graph package and the database's types:\n%s", pkg)
	}
	dockerfile := read(servergen.DockerFile)
	for _, want := range []string{
		"FROM rust:${RUST_VERSION}-trixie AS versiongraph",
		"COPY runtime/versiongraph/rust runtime/versiongraph/rust",
		"cargo build --locked --release --target wasm32-unknown-unknown --manifest-path runtime/versiongraph/rust/Cargo.toml",
		"cp runtime/versiongraph/rust/target/wasm32-unknown-unknown/release/superschematic_versiongraph.wasm /wasm/",
		"COPY --from=versiongraph /wasm/superschematic_versiongraph.wasm runtime/versiongraph/typescript/dist/",
		"cd runtime/versiongraph/typescript \\\n    && bun install --frozen-lockfile \\\n    && bunx tsc -p tsconfig.json",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("the Dockerfile lacks %q:\n%s", want, dockerfile)
		}
	}
	// The package's dist/ is emitted before the workspace's install, which
	// copies the package with it.
	if emit, install := strings.Index(dockerfile, "COPY --from=versiongraph"), strings.Index(dockerfile, "bun install --production"); emit < 0 || install < emit {
		t.Errorf("the build stage installs the workspace before it emits the version-graph package's dist/:\n%s", dockerfile)
	}
	ignore := read(servergen.DockerIgnoreFile)
	for _, want := range []string{"\n!runtime/versiongraph/rust\n", "\n!runtime/versiongraph/typescript\n", "\nruntime/versiongraph/typescript/dist\n"} {
		if !strings.Contains(ignore, want) {
			t.Errorf("the ignore file lacks %q:\n%s", strings.TrimSpace(want), ignore)
		}
	}
	if testing.Short() {
		return
	}

	// The server type-checks over the real runtime, the version-graph
	// package and the generated packages, as bun installs them in the
	// output root: the facade it builds is the one its router takes.
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	for _, file := range []string{"facade.d.ts", "postgres.d.ts", "superschematic_versiongraph.wasm"} {
		if _, err := os.Stat(filepath.Join(paths.VersionGraphTypeScript, "dist", file)); err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("build %s (bun install && bun run build) for the version-graph facade: %v", paths.VersionGraphTypeScript, err))
		}
	}
	realRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f = loadTypeScriptGraphsFixture(t)
	f.build(t, realRoot, paths, tsGraphsOrder...)
	realOut := filepath.Join(realRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = realOut
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, checked := range []string{servergen.ServerDir(realOut, tsGraphsStack, tsGraphsServer), naming.Default().TypeScriptImplementationDir(realRoot, tsGraphsServer)} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = checked
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", checked, err, output)
		}
	}
}
