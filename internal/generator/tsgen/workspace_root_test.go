package tsgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The generated types packages install as one Bun workspace so their sibling
// file: dependencies resolve from any package.
func TestWriteWorkspaceRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "types", "typescript")
	names := naming.Naming{NpmScope: "@acme"}

	if err := WriteWorkspaceRoot(root, names); err != nil {
		t.Fatalf("WriteWorkspaceRoot: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Name       string   `json:"name"`
		Private    bool     `json:"private"`
		Workspaces []string `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, raw)
	}
	if manifest.Name != "@acme/types-workspace" {
		t.Fatalf("name = %q, want the naming file's npm scope", manifest.Name)
	}
	if !manifest.Private {
		t.Fatal("workspace root must be private so it is never published")
	}
	if len(manifest.Workspaces) != 1 || manifest.Workspaces[0] != "*" {
		t.Fatalf("workspaces = %v, want every sibling package", manifest.Workspaces)
	}

	// Parallel schema builds each write the manifest for their own package;
	// the result must be the same file, not a torn or missing one.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := WriteWorkspaceRoot(root, names); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent WriteWorkspaceRoot: %v", err)
	}
	again, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatalf("re-read manifest: %v", err)
	}
	if string(again) != WorkspaceRootManifest(names) {
		t.Fatalf("manifest drifted after concurrent writes:\n%s", again)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "package.json" {
			t.Fatalf("unexpected leftover %q in types root", entry.Name())
		}
	}
}

func TestWorkspaceRootManifestDefaultsScope(t *testing.T) {
	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(WorkspaceRootManifest(naming.Naming{})), &manifest); err != nil {
		t.Fatal(err)
	}
	if want := naming.Default().NpmScope + "/types-workspace"; manifest.Name != want {
		t.Fatalf("name = %q, want %q", manifest.Name, want)
	}
}
