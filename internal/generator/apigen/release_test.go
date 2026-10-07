package apigen_test

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/release"
)

// TestWriteAPIReleaseGolden: in a project whose naming file has no
// [paths], the API module a release writes requires the HTTP runtime and
// the scalar library at the release's pins and replaces every version of
// them, of the schema runtime the HTTP runtime builds on and of the schema
// IR with its pin, so it resolves from the module proxy on its own (D47,
// amended); the generated modules it requires keep their directories.
// Regenerate with:
//
//	go test ./internal/generator/apigen -run TestWriteAPIReleaseGolden -update
func TestWriteAPIReleaseGolden(t *testing.T) {
	output := generateFixtureAPI(t)
	rel := release.Release{Version: "1.2.3", ScalarGo: "v0.0.0-20260928143325-10cf493f485e"}
	outDir := t.TempDir()
	if err := apigen.SetReplacePaths(output, naming.LocalPaths{}, outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	apigen.SetReleasePins(output, output.Naming.ReleasePins(naming.LocalPaths{}, rel))
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", "fixture-api-release"), []string{"go.mod"})
}
