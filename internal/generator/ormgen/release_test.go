package ormgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/release"
)

// TestWriteORMReleaseGolden: in a project whose naming file has no
// [paths], the ORM module a release writes for a schema with a version
// graph requires the scalar library, the schema IR and the version-graph
// binding at the release's pins and replaces every version of each with
// its pin, so it resolves from the module proxy on its own (D47, amended).
// Regenerate with:
//
//	go test ./internal/generator/ormgen -run TestWriteORMReleaseGolden -update
func TestWriteORMReleaseGolden(t *testing.T) {
	output := generateFixture(t, "fixture-version-graph-db")
	rel := release.Release{Version: "1.2.3", ScalarGo: "v0.0.0-20260928143325-10cf493f485e"}
	outDir := t.TempDir()
	if err := SetReplacePaths(output, naming.LocalPaths{}, outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	SetReleasePins(output, output.Naming.ReleasePins(naming.LocalPaths{}, rel))
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", "fixture-version-graph-db-release", "go.mod")
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
