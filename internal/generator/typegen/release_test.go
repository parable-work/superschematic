package typegen

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/release"
	ir "github.com/parable-work/superschematic/ir"
)

// TestWriteTypesReleaseGolden: in a project whose naming file has no
// [paths], the types module a release writes requires the scalar library
// and the schema IR at the release's pins and replaces every version of
// each with its pin, so it resolves from the module proxy on its own (D47,
// amended); the types module of the dependency keeps its directory.
// Regenerate with:
//
//	go test ./internal/generator/typegen -run TestWriteTypesReleaseGolden -update
func TestWriteTypesReleaseGolden(t *testing.T) {
	dep, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	output, err := Generate(schema, Options{
		SchemaName:        "fixture-api",
		ModulePath:        "example.com/schemas/types/go/fixture-api",
		Dependencies:      map[string]*ir.Schema{"fixture-db": dep},
		DependencyModules: map[string]string{"fixture-db": "example.com/schemas/types/go/fixture-db"},
		Clock:             codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	rel := release.Release{Version: "1.2.3", ScalarGo: "v0.0.0-20260928143325-10cf493f485e"}
	outDir := filepath.Join(t.TempDir(), "fixture-api")
	if err := SetReplacePaths(output, naming.LocalPaths{}, outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	SetReleasePins(output, output.Naming.ReleasePins(naming.LocalPaths{}, rel))
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", "fixture-api-release", "go.mod")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update)", err)
	}
	if string(got) != string(want) {
		t.Errorf("go.mod differs from %s (run with -update to accept):\n%s", golden, got)
	}
}
