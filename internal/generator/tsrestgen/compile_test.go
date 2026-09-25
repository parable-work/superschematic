package tsrestgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// requireOrSkipTSTooling decides what a missing TypeScript toolchain means:
// a skip locally, a failure in CI (SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1), the
// same rule as sdkgen's compile gate.
func requireOrSkipTSTooling(t *testing.T, reason string) {
	t.Helper()
	testpaths.RequireOrSkipTS(t, reason)
}

// generatedTree is an API package materialized beside its type packages
// and linked to the real runtime and the scalar library, the way a
// consuming service sees it.
type generatedTree struct {
	bun        string
	apiDir     string
	runtimeDir string
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("create link parent %s: %v", name, err)
	}
	_ = os.RemoveAll(name)
	if err := os.Symlink(target, name); err != nil {
		t.Fatalf("symlink %s -> %s: %v", name, target, err)
	}
}

// installRuntime installs the http runtime's dependencies and links the
// in-repository package it imports (its link-deps script). It returns the
// bun binary, the runtime directory and the in-repository runtime paths.
func installRuntime(t *testing.T) (string, string, naming.LocalPaths) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping TypeScript gate in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	if _, err := os.Stat(filepath.Join(paths.ScalarTypeScript, "dist")); err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("superscalar TypeScript binding not built: %v", err))
	}
	runtimeDir := filepath.Join(testpaths.RepoRoot(t), "runtime", "http", "typescript")
	install := exec.Command(bunPath, "install", "--frozen-lockfile")
	install.Dir = runtimeDir
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed for the http runtime (likely offline): %v\n%s", err, out))
	}
	linkDeps := exec.Command(bunPath, "run", "link-deps")
	linkDeps.Dir = runtimeDir
	if out, err := linkDeps.CombinedOutput(); err != nil {
		t.Fatalf("link the http runtime's local dependencies: %v\n%s", err, out)
	}
	return bunPath, runtimeDir, paths
}

// apiFixture is one API schema a compile gate materializes: its type
// packages are generated for the schema and for each dependency.
type apiFixture struct {
	name   string
	schema *ir.Schema
	deps   map[string]*ir.Schema
	// endpoints is apigen's extraction for the schema.
	endpoints *apigen.APIOutput
}

// fixtureAPI is fixture-api with its fixture-db dependency.
func fixtureAPI(t *testing.T) apiFixture {
	t.Helper()
	apiSchema, dbSchema := loadFixtureAPI(t)
	return apiFixture{
		name:      "fixture-api",
		schema:    apiSchema,
		deps:      map[string]*ir.Schema{"fixture-db": dbSchema},
		endpoints: extractEndpoints(t, apiSchema, dbSchema),
	}
}

// materializeAPI generates the fixture's type packages (dependencies first)
// under one Bun workspace root and installs them once at that root, then
// generates the API package and links every peer it resolves by name: the
// type packages, the scalar library, the http runtime, and hono (from the
// runtime's own install so both sides see one copy of the framework).
func materializeAPI(t *testing.T, fixture apiFixture) *generatedTree {
	t.Helper()
	bunPath, runtimeDir, paths := installRuntime(t)

	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	names := naming.Default()
	typesRoot := filepath.Join(tempRoot, "types", "typescript")

	type typesCase struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}
	var typesCases []typesCase
	depPackages := map[string]string{}
	for _, name := range sortedKeys(fixture.deps) {
		typesCases = append(typesCases, typesCase{name: name, schema: fixture.deps[name]})
		depPackages[name] = names.NpmTypesPackage(name)
	}
	typesCases = append(typesCases, typesCase{name: fixture.name, schema: fixture.schema, deps: fixture.deps})

	typesDirs := map[string]string{}
	for _, tc := range typesCases {
		tsOutput, err := tsgen.Generate(tc.schema, tsgen.Options{
			SchemaName:         tc.name,
			Dependencies:       tc.deps,
			DependencyPackages: depPackages,
			Clock:              fixedClock,
		})
		if err != nil {
			t.Fatalf("tsgen.Generate %s: %v", tc.name, err)
		}
		typesDir := filepath.Join(typesRoot, tc.name)
		if err := tsgen.SetScalarLibSpec(tsOutput, paths, typesDir); err != nil {
			t.Fatalf("set superscalar spec for %s: %v", tc.name, err)
		}
		if err := tsgen.WriteTypes(tsOutput, typesDir); err != nil {
			t.Fatalf("write types %s: %v", tc.name, err)
		}
		typesDirs[names.NpmTypesPackage(tc.name)] = typesDir
	}
	if err := tsgen.WriteWorkspaceRoot(typesRoot, naming.Naming{}); err != nil {
		t.Fatalf("write workspace root: %v", err)
	}
	install := exec.Command(bunPath, "install")
	install.Dir = typesRoot
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed for the type packages of %s (likely offline): %v\n%s", fixture.name, err, out))
	}

	output, err := Generate(fixture.schema, fixture.endpoints, Options{
		SchemaName:   fixture.name,
		Dependencies: fixture.deps,
		Clock:        fixedClock,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	apiDir := filepath.Join(tempRoot, "api", fixture.name)
	if err := WriteAPI(output, apiDir); err != nil {
		t.Fatalf("WriteAPI: %v", err)
	}

	modules := filepath.Join(apiDir, "node_modules")
	for name, dir := range typesDirs {
		link(t, dir, filepath.Join(modules, filepath.FromSlash(name)))
	}
	link(t, paths.ScalarTypeScript, filepath.Join(modules, names.ScalarNpmPackage))
	link(t, runtimeDir, filepath.Join(modules, filepath.FromSlash(names.HTTPRuntimeNpmPackage)))
	for _, dep := range []string{"hono", "typescript", filepath.Join("@types", "node")} {
		link(t, filepath.Join(runtimeDir, "node_modules", dep), filepath.Join(modules, dep))
	}
	return &generatedTree{bun: bunPath, apiDir: apiDir, runtimeDir: runtimeDir}
}

// typeCheck runs tsc over the generated package.
func (tree *generatedTree) typeCheck(t *testing.T) {
	t.Helper()
	tsc := filepath.Join(tree.runtimeDir, "node_modules", ".bin", "tsc")
	check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
	check.Dir = tree.apiDir
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("generated API package does not type-check: %v\n%s", err, out)
	}
}

// runTest stages testdata/<name> inside the generated package, so its
// imports (hono, the runtime, the generated router) resolve like a
// consumer's, and runs it under bun with API_DIR set to the package.
func (tree *generatedTree) runTest(t *testing.T, name string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	testFile := filepath.Join(tree.apiDir, name)
	if err := os.WriteFile(testFile, source, 0o644); err != nil {
		t.Fatalf("stage %s: %v", name, err)
	}
	cmd := exec.Command(tree.bun, "test", testFile)
	cmd.Dir = tree.apiDir
	cmd.Env = append(os.Environ(), "API_DIR="+tree.apiDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	if !strings.Contains(string(out), " 0 fail") {
		t.Fatalf("unexpected bun test summary for %s:\n%s", name, out)
	}
}

// TestGeneratedAPICompiles type-checks the generated fixture-api package
// against locally generated type packages, the real runtime, and Hono, so a
// template that emits an invalid import or a signature the runtime does not
// offer fails here rather than in a consuming service.
func TestGeneratedAPICompiles(t *testing.T) {
	materializeAPI(t, fixtureAPI(t)).typeCheck(t)
}

// TestGeneratedRouterRuntime boots the generated router with stub
// implementations under bun and drives it over HTTP: envelopes, the strict
// body parser, the auth gate, parameter decoding, and the manual-route hook.
func TestGeneratedRouterRuntime(t *testing.T) {
	materializeAPI(t, fixtureAPI(t)).runTest(t, "router_runtime.test.ts")
}
