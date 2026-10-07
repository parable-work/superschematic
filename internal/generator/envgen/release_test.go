package envgen_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/release"
)

// TestWriteConfigModuleReleaseGolden: in a project whose naming file has
// no [paths], the standalone config module a release writes replaces every
// version of the scalar library and the schema IR, which it reaches
// through its types module, with the release's pins, so it resolves from
// the module proxy on its own (D47, amended). Regenerate with:
//
//	go test ./internal/generator/envgen -run TestWriteConfigModuleReleaseGolden -update
func TestWriteConfigModuleReleaseGolden(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}
	output, err := envgen.Generate(schema, "fixture-general")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	rel := release.Release{Version: "1.2.3", ScalarGo: "v0.0.0-20260928143325-10cf493f485e"}
	outDir := t.TempDir()
	if err := envgen.SetReplacePaths(output, naming.LocalPaths{}, outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	envgen.SetReleasePins(output, output.Naming.ReleasePins(naming.LocalPaths{}, rel))
	if err := envgen.WriteConfigModule(output, outDir); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", "fixture-general-release", "go.mod")
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
