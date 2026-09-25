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

// RequireTSChecksEnv names the variable that turns a TypeScript gate's skip
// into a failure. CI sets it to 1 in every job that runs these gates.
const RequireTSChecksEnv = "SUPERSCHEMATIC_REQUIRE_TS_CHECKS"

// RequireOrSkipTS handles a TypeScript gate that cannot run: bun is
// missing, an install failed, or a dependency tree is not installed.
// Locally it skips, so `go test ./...` runs without bun. With
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1 it fails, because in CI a skip hides
// a broken gate.
func RequireOrSkipTS(t testing.TB, reason string) {
	t.Helper()
	if os.Getenv(RequireTSChecksEnv) == "1" {
		t.Fatalf("%s=1 requires this TypeScript gate to run: %s", RequireTSChecksEnv, reason)
		return
	}
	t.Skipf("skipping TypeScript gate: %s", reason)
}

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
