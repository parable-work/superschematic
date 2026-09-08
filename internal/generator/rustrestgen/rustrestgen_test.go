package rustrestgen

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

func generateFixtureAPIRust(t *testing.T) *APIOutput {
	t.Helper()

	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}

	outDir := t.TempDir()
	typesDir := filepath.Join(outDir, "types", "rust", "fixture-api")

	output, err := Generate(apiSchema, Options{
		AuthProvider:   sessionauth.Provider{},
		SchemaName:     "fixture-api",
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     dbSchema,
		TypesCrate:     "parable-fixture-api-types",
		TypesDir:       typesDir,
		OutputDir:      filepath.Join(outDir, "api", "fixture-api"),
		Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected Rust API output for fixture-api")
	}
	return output
}

func TestWriteRustAPIGolden(t *testing.T) {
	output := generateFixtureAPIRust(t)

	// Compute the Cargo.toml runtime path against a fixed fake output
	// location so the golden stays machine-independent; SetReplacePaths only
	// computes strings, so the directories need not exist.
	if err := SetReplacePaths(output, "/repo/utils/parable-scalars", "/repo/platform-schemas/dist/api/fixture-api"); err != nil {
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
	}

	goldenDir := filepath.Join("testdata", "golden", "fixture-api")
	for _, name := range files {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}

		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}

		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

func TestGenerateFixtureAPIShape(t *testing.T) {
	output := generateFixtureAPIRust(t)

	if output.CrateName != "parable-fixture-api-api" {
		t.Errorf("CrateName = %q, want parable-fixture-api-api", output.CrateName)
	}
	if len(output.Endpoints) != 6 {
		t.Fatalf("expected 6 endpoints, got %d", len(output.Endpoints))
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
}
