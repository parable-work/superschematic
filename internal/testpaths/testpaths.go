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
		ScalarGo:               filepath.Join(superscalar, "go"),
		ScalarTypeScript:       filepath.Join(superscalar, "bindings", "typescript"),
		ScalarRust:             filepath.Join(superscalar, "crates", "core"),
		SchemaIR:               filepath.Join(root, "ir"),
		SchemaRuntimeGo:        filepath.Join(root, "runtime", "schema", "go"),
		SchemaRuntimeRust:      filepath.Join(root, "runtime", "schema", "rust"),
		VersionGraphGo:         filepath.Join(root, "runtime", "versiongraph", "go"),
		VersionGraphTypeScript: filepath.Join(root, "runtime", "versiongraph", "typescript"),
		VersionGraphRust:       filepath.Join(root, "runtime", "versiongraph", "rust-engine"),
		VersionGraphPython:     filepath.Join(root, "runtime", "versiongraph", "python"),
		HTTPRuntimeGo:          filepath.Join(root, "runtime", "http", "go"),
		HTTPRuntimeRust:        filepath.Join(root, "runtime", "http", "rust"),
	}
}

// RustPatch is a Cargo [patch.crates-io] table that resolves the runtime
// crates a generated Rust crate names by version, which are not on
// crates.io yet, from paths: the scalar crate and the schema runtime the
// generated validators call. A test that writes a generated crate without
// rustgen.SetLocalPaths appends it to the crate's Cargo.toml.
func RustPatch(paths naming.LocalPaths, n naming.Naming) string {
	n = n.OrDefault()
	table := "[patch.crates-io]\n"
	for _, entry := range []struct{ crate, dir string }{
		{n.ScalarRustCrate, paths.ScalarRust},
		{n.SchemaRuntimeRustCrate, paths.SchemaRuntimeRust},
	} {
		if entry.dir != "" {
			table += entry.crate + ` = { path = "` + filepath.ToSlash(entry.dir) + "\" }\n"
		}
	}
	return table
}

// TempDir is t.TempDir with symlinks resolved (macOS's /var is
// /private/var), so a relative path a generator computes from it resolves
// where the build tool looks.
func TempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("testpaths: resolve temp dir: %v", err)
	}
	return dir
}
