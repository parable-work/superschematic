// Package testpaths locates the runtime modules in this repository for tests
// that compile generated code against them.
package testpaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// RepoRoot returns the repository root, found from this file's location.
func RepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("testpaths: cannot locate source file")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatalf("testpaths: resolve repo root: %v", err)
	}
	return root
}

// Local returns the in-repository runtime paths generated manifests point
// at: the superscalar checkout scripts/superscalar-dep.sh stands up under
// third_party/, the ir module, and the schema and http runtimes. It skips
// the test when superscalar has not been checked out.
func Local(t *testing.T) naming.LocalPaths {
	t.Helper()
	root := RepoRoot(t)
	superscalar := filepath.Join(root, "third_party", "superscalar")
	if _, err := os.Stat(filepath.Join(superscalar, "go", "go.mod")); err != nil {
		t.Skipf("superscalar checkout not available (run scripts/superscalar-dep.sh): %v", err)
	}
	return naming.LocalPaths{
		ScalarGo:         filepath.Join(superscalar, "go"),
		ScalarTypeScript: filepath.Join(superscalar, "bindings", "typescript"),
		ScalarRust:       filepath.Join(superscalar, "crates", "core"),
		SchemaIR:         filepath.Join(root, "ir"),
		SchemaRuntimeGo:  filepath.Join(root, "runtime", "schema", "go"),
		HTTPRuntimeGo:    filepath.Join(root, "runtime", "http", "go"),
		HTTPRuntimeRust:  filepath.Join(root, "runtime", "http", "rust"),
	}
}
