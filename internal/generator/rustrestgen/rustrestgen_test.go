package rustrestgen

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

// generateFixtureAPIRust generates fixture-api without its encryption. The
// Rust server refuses an encrypted operation that is not
// @manualRouteRegistration (TestFixtureAPIIsRefusedForItsEncryptedMutations),
// so the golden tests serve fixture-api with its TenantMutations set and
// updateSecret argument unencrypted, which changes no generated file.
func generateFixtureAPIRust(t *testing.T) *APIOutput {
	t.Helper()

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	return generateRustAPI(t, "fixture-api", true, "fixture-db", dbSchema, withoutEncryption)
}

// withoutEncryption clears every encryption flag of schema's operation sets.
func withoutEncryption(schema *ir.Schema) {
	for _, set := range schema.OperationSets {
		set.Encrypted = false
		for _, op := range set.Operations {
			op.Encrypted = false
			for _, arg := range op.Arguments {
				arg.Encrypted = false
			}
		}
	}
}

// generateMultiwordAPIRust generates the fixture whose operation sets
// (PoolSearchQueries, PoolSearchMutations) share the two-word namespace
// `pool-search`.
func generateMultiwordAPIRust(t *testing.T) *APIOutput {
	t.Helper()
	return generateRustAPI(t, "fixture-multiword-api", false, "", nil)
}

func generateRustAPI(t *testing.T, name string, public bool, upstream string, upstreamIR *ir.Schema, edits ...func(*ir.Schema)) *APIOutput {
	t.Helper()

	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, name))
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	for _, edit := range edits {
		edit(apiSchema)
	}

	outDir := t.TempDir()
	typesDir := filepath.Join(outDir, "types", "rust", name)

	output, err := generateFrom(apiSchema, apiSource{public: public, upstream: upstream, upstreamIR: upstreamIR, authDB: authDBOf(t, apiSchema)}, Options{
		SchemaName: name,
		TypesCrate: "schemas-" + name + "-types",
		TypesDir:   typesDir,
		OutputDir:  filepath.Join(outDir, "api", name),
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate %s: %v", name, err)
	}
	if output == nil {
		t.Fatalf("expected Rust API output for %s", name)
	}
	return output
}

// writeGoldenAPI writes the generated crate and compares every file with
// testdata/golden/<name>. It returns the generated files by slash path.
func writeGoldenAPI(t *testing.T, name string, output *APIOutput) map[string]string {
	t.Helper()

	// Compute the Cargo.toml runtime and scalar crate paths against a fixed
	// fake output location so the golden stays machine-independent;
	// SetReplacePaths only computes strings, so the directories need not
	// exist.
	paths := naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust", ScalarRust: "/repo/third_party/superscalar/crates/core"}
	if err := SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+name); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	files := []string{
		"Cargo.toml",
		filepath.Join("src", "lib.rs"),
		filepath.Join("src", "interfaces.rs"),
		filepath.Join("src", "router.rs"),
		filepath.Join("src", "openapi.rs"),
		filepath.Join("src", "operations.rs"),
		"openapi.json",
	}
	if output.Identity != nil {
		files = append(files, filepath.Join("src", "identity.rs"))
	}

	generated := make(map[string]string, len(files))
	goldenDir := filepath.Join("testdata", "golden", name)
	for _, file := range files {
		got, err := os.ReadFile(filepath.Join(outDir, file))
		if err != nil {
			t.Fatalf("read generated %s: %v", file, err)
		}
		generated[filepath.ToSlash(file)] = string(got)

		goldenPath := filepath.Join(goldenDir, file)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", file, err)
			}
			continue
		}

		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", file, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s/%s differs from golden (run with -update to accept)", name, file)
		}
	}
	return generated
}

// TestWriteRustAPIGolden pins the crate for fixture-api. Its
// tenant.customHandler is @manualRouteRegistration, so the router does not
// mount it and TenantImplementation has no method for it, as the Go server
// leaves it out of RegisterRoutes and the TypeScript server out of its
// implementation interfaces. build_router's doc names its method and path
// for the service, which adds the route to the router it returns.
func TestWriteRustAPIGolden(t *testing.T) {
	generated := writeGoldenAPI(t, "fixture-api", generateFixtureAPIRust(t))

	router := generated["src/router.rs"]
	for _, mounted := range []string{"handle_tenant_custom_handler", `"/api/tenant/custom-handler"`} {
		if strings.Contains(router, mounted) {
			t.Errorf("router.rs mounts the manual operation tenant.customHandler: it contains %q", mounted)
		}
	}
	if want := "/// - `POST /api/tenant/custom-handler` (tenant.customHandler)"; !strings.Contains(router, want) {
		t.Errorf("router.rs does not name the manual operation for the service: missing %q", want)
	}
	if interfaces := generated["src/interfaces.rs"]; strings.Contains(interfaces, "fn custom_handler") {
		t.Error("interfaces.rs declares a method for the manual operation tenant.customHandler")
	}
}

// rustFnName matches the name of every generated Rust function, hyphens
// included, so a kebab-case namespace inside a name is caught.
var rustFnName = regexp.MustCompile(`\bfn ([A-Za-z0-9_-]+)`)

// TestWriteRustAPIMultiwordNamespaceGolden pins the crate for a two-word
// namespace. The kebab-case namespace `pool-search` must become the
// snake_case `pool_search` wherever it is part of a Rust identifier;
// `handle_pool-search_...` does not compile.
func TestWriteRustAPIMultiwordNamespaceGolden(t *testing.T) {
	output := generateMultiwordAPIRust(t)

	if len(output.Namespaces) != 1 || output.Namespaces[0] != "pool-search" {
		t.Fatalf("Namespaces = %v, want [pool-search]", output.Namespaces)
	}

	generated := writeGoldenAPI(t, "fixture-multiword-api", output)

	router := generated["src/router.rs"]
	for _, want := range []string{
		"get(handle_pool_search_get_index)",
		"post(handle_pool_search_rebuild_index)",
		"async fn handle_pool_search_get_index(",
		"async fn handle_pool_search_rebuild_index(",
		".pool_search\n",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.rs missing %q", want)
		}
	}

	for file, source := range generated {
		for _, match := range rustFnName.FindAllStringSubmatch(source, -1) {
			if strings.Contains(match[1], "-") {
				t.Errorf("%s declares fn %q, which is not a Rust identifier", file, match[1])
			}
		}
	}

	interfaces := generated["src/interfaces.rs"]
	for _, want := range []string{
		"pub trait PoolSearchImplementation",
		"pub pool_search: Arc<dyn PoolSearchImplementation>,",
	} {
		if !strings.Contains(interfaces, want) {
			t.Errorf("interfaces.rs missing %q", want)
		}
	}
}

func TestGenerateFixtureAPIShape(t *testing.T) {
	output := generateFixtureAPIRust(t)

	if output.CrateName != "schemas-fixture-api-api" {
		t.Errorf("CrateName = %q, want schemas-fixture-api-api", output.CrateName)
	}
	// Five mounted operations; the sixth, tenant.customHandler, is
	// @manualRouteRegistration.
	if len(output.Endpoints) != 5 {
		t.Fatalf("expected 5 endpoints, got %d", len(output.Endpoints))
	}
	if len(output.Namespaces) != 2 || output.Namespaces[0] != "session" || output.Namespaces[1] != "tenant" {
		t.Fatalf("expected [session tenant] namespace, got %v", output.Namespaces)
	}

	for _, ep := range output.Endpoints {
		if ep.Path == "" || ep.Method == "" {
			t.Errorf("endpoint %q missing path or method", ep.Name)
		}
		if !strings.HasPrefix(ep.Path, "/") {
			t.Errorf("endpoint %q path %q should start with /", ep.Name, ep.Path)
		}
	}
}

func TestWriteScaffolds(t *testing.T) {
	output := generateFixtureAPIRust(t)

	scaffoldsDir := t.TempDir()
	result, err := WriteScaffolds(output, scaffoldsDir)
	if err != nil {
		t.Fatalf("write scaffolds: %v", err)
	}
	if len(result.Generated) == 0 {
		t.Fatal("expected scaffold files to be generated")
	}
	if _, err := os.Stat(filepath.Join(scaffoldsDir, "README.md")); err != nil {
		t.Fatalf("expected README.md scaffold: %v", err)
	}
	// The manual tenant.customHandler has no trait method to implement.
	if _, err := os.Stat(filepath.Join(scaffoldsDir, "tenant", "list_tenants.rs")); err != nil {
		t.Fatalf("expected tenant/list_tenants.rs scaffold: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scaffoldsDir, "tenant", "custom_handler.rs")); !os.IsNotExist(err) {
		t.Errorf("custom_handler.rs scaffold for the manual operation: stat err %v, want not exist", err)
	}
}
