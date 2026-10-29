package tsutil

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestMissingDependencies (D100): a dependency a package.json names in
// any of its tables is there; the rest are missing, in the order asked. A
// directory with no package.json misses them all, and one whose
// package.json does not read as a manifest is an error naming it.
// DependencyLines writes the missing as a package.json holds them.
func TestMissingDependencies(t *testing.T) {
	dir := t.TempDir()
	deps := []Dependency{
		{Name: "@acme/shop-api-sdk", Spec: WorkspaceSpec},
		{Name: "@acme/shop-db-types", Spec: WorkspaceSpec},
		{Name: "superscalar", Spec: "*"},
		{Name: "@acme/cache-types", Spec: WorkspaceSpec},
		{Name: "typescript", Spec: "5.9.3"},
	}
	missing, err := MissingDependencies(dir, deps)
	if err != nil || !reflect.DeepEqual(missing, deps) {
		t.Fatalf("with no package.json = %v, %v; want every one", missing, err)
	}

	write := func(manifest string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{
  "name": "@acme/shop-web-site",
  "dependencies": { "@acme/shop-db-types": "workspace:*" },
  "devDependencies": { "typescript": "5.9.3" },
  "peerDependencies": { "superscalar": "*" },
  "optionalDependencies": {}
}`)
	missing, err = MissingDependencies(dir, deps)
	want := []Dependency{deps[0], deps[3]}
	if err != nil || !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v, %v; want %v", missing, err, want)
	}
	if got, want := DependencyLines(missing), `    "@acme/shop-api-sdk": "workspace:*",
    "@acme/cache-types": "workspace:*"`; got != want {
		t.Errorf("DependencyLines = %q, want %q", got, want)
	}
	if missing, err := MissingDependencies(dir, nil); err != nil || missing != nil {
		t.Errorf("with nothing asked = %v, %v", missing, err)
	}

	for _, manifest := range []string{`{"dependencies": `, `{"dependencies": ["superscalar"]}`} {
		write(manifest)
		if _, err := MissingDependencies(dir, deps); err == nil || !strings.Contains(err.Error(), filepath.Join(dir, ManifestFile)) {
			t.Errorf("MissingDependencies over %s = %v, want an error naming the file", manifest, err)
		}
	}
}
