package typegen

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedModulesCompile generates the Go type modules for the fixture
// services into a temp tree wired against the real superscalar module and
// runs `go build` on each. This is the cheap end-to-end compile check for
// the typegen port.
func TestGeneratedModulesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	paths := testpaths.Local(t)

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
			depModules[depName] = "example.com/schemas/types/go/" + depName
		}

		output, err := Generate(tc.schema, Options{
			SchemaName:        tc.name,
			ModulePath:        "example.com/schemas/types/go/" + tc.name,
			Dependencies:      tc.deps,
			DependencyModules: depModules,
			Clock:             fixedClock,
		})
		if err != nil {
			t.Fatalf("generate %s: %v", tc.name, err)
		}

		outDir := filepath.Join(typesGoDir, tc.name)
		if err := SetReplacePaths(output, paths, outDir); err != nil {
			t.Fatalf("set replace paths for %s: %v", tc.name, err)
		}
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
	}

	for _, tc := range cases {
		outDir := filepath.Join(typesGoDir, tc.name)

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
