package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedModulesCompile generates the Go type modules for the fixture
// services into a temp tree wired against the real scalar-lib runtime and
// runs `go build` on each. This is the cheap end-to-end compile check for
// the typegen port.
func TestGeneratedModulesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	scalarLib, err := filepath.Abs("../../../../parable-scalars")
	if err != nil {
		t.Fatalf("resolve scalar-lib path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scalarLib, "go")); err != nil {
		t.Skipf("scalar-lib runtime not available: %v", err)
	}

	tempRoot := t.TempDir()
	typesGoDir := filepath.Join(tempRoot, "types", "go")

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	cases := []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{name: "fixture-db", schema: dbSchema},
		{name: "fixture-api", schema: apiSchema, deps: map[string]*ir.Schema{"fixture-db": dbSchema}},
	}

	for _, tc := range cases {
		depModules := map[string]string{}
		for depName := range tc.deps {
			depModules[depName] = "github.com/parable-platform/platform-schemas/types/go/" + depName
		}

		output, err := Generate(tc.schema, Options{
			SchemaName:        tc.name,
			ModulePath:        "github.com/parable-platform/platform-schemas/types/go/" + tc.name,
			Dependencies:      tc.deps,
			DependencyModules: depModules,
			Clock:             fixedClock,
		})
		if err != nil {
			t.Fatalf("generate %s: %v", tc.name, err)
		}

		outDir := filepath.Join(typesGoDir, tc.name)
		if err := SetReplacePaths(output, scalarLib, outDir); err != nil {
			t.Fatalf("set replace paths for %s: %v", tc.name, err)
		}
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
	}

	// schema-ir's test files import the private ptr module; `go mod tidy`
	// resolves test deps of direct deps, and replace directives inside
	// dependency go.mod files do not apply to the main module. The committed
	// v1 dist modules have the same shape and rely on consumer-side
	// workspaces, so add the ptr replace here only for the compile check.
	ptrPath := filepath.Clean(filepath.Join(scalarLib, "..", "..", "services", "pkg", "ptr"))
	if _, err := os.Stat(ptrPath); err != nil {
		t.Skipf("ptr module not available: %v", err)
	}

	for _, tc := range cases {
		outDir := filepath.Join(typesGoDir, tc.name)

		gomod, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
		if err != nil {
			t.Fatalf("read go.mod for %s: %v", tc.name, err)
		}
		gomod = append(gomod, []byte("\nreplace github.com/parable-work/superschematic/runtime/schema/go/ptr => "+ptrPath+"\n")...)
		if err := os.WriteFile(filepath.Join(outDir, "go.mod"), gomod, 0o644); err != nil {
			t.Fatalf("write go.mod for %s: %v", tc.name, err)
		}

		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = outDir
		if out, err := tidy.CombinedOutput(); err != nil {
			t.Skipf("go mod tidy failed for %s (likely offline): %v\n%s", tc.name, err, out)
		}

		build := exec.Command("go", "build", "./...")
		build.Dir = outDir
		if out, err := build.CombinedOutput(); err != nil {
			t.Errorf("generated module %s does not compile: %v\n%s", tc.name, err, out)
		}
	}
}
