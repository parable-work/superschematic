package tsgen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// The generated types packages install as one Bun workspace so their sibling
// dependencies resolve from any package.
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

// TestSiblingTypesDependencyIsWorkspaceSpec: a types package that imports
// from another schema names that package with workspace:*, the spec Bun
// keeps stable in the workspace lockfile. A file:../<schema> spec made every
// install after the first fail (see TestWorkspaceInstallsFromAnyPackage).
func TestSiblingTypesDependencyIsWorkspaceSpec(t *testing.T) {
	cases := loadNestedArraysEdges(t)
	edges := cases[1]
	output, err := Generate(edges.schema, Options{SchemaName: edges.name, Dependencies: edges.deps})
	if err != nil {
		t.Fatalf("generate %s: %v", edges.name, err)
	}
	want := []PackageDependency{{Name: naming.Default().NpmTypesPackage(nestedArraysEdgesEnumsService), Spec: "workspace:*"}}
	if fmt.Sprint(output.PackageDependencies) != fmt.Sprint(want) {
		t.Fatalf("PackageDependencies = %v, want %v", output.PackageDependencies, want)
	}

	dir := filepath.Join(t.TempDir(), edges.name)
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("package.json is not JSON: %v\n%s", err, raw)
	}
	if got := manifest.Dependencies[want[0].Name]; got != "workspace:*" {
		t.Fatalf("package.json names %s with %q, want workspace:*", want[0].Name, got)
	}
}

// TestWorkspaceInstallsFromAnyPackage runs `bun install` in the generated
// types workspace the way the docs allow: in members and at the root, again
// after a build adds a package that imports from a sibling. Every install
// must succeed and share the root lockfile, and that package must
// type-check against its installed sibling.
//
// With sibling file:../<schema> specs this failed under Bun 1.4.0 at the
// second install, and under Bun 1.4.2 at the first install after a package
// with such a spec joined an existing lockfile: Bun resolved the superscalar
// file: path, which the lockfile stores relative to the root, from the
// member directory.
func TestWorkspaceInstallsFromAnyPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bun install check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	typesRoot := filepath.Join(tempRoot, "types", "typescript")
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	write := func(tc tsPackageCase) {
		t.Helper()
		output, err := Generate(tc.schema, Options{SchemaName: tc.name, Dependencies: tc.deps, Clock: clock})
		if err != nil {
			t.Fatalf("generate %s: %v", tc.name, err)
		}
		dir := filepath.Join(typesRoot, tc.name)
		if err := SetScalarLibSpec(output, paths, dir); err != nil {
			t.Fatalf("set superscalar spec for %s: %v", tc.name, err)
		}
		if err := WriteTypes(output, dir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
		if err := WriteWorkspaceRoot(typesRoot, naming.Naming{}); err != nil {
			t.Fatalf("write workspace root: %v", err)
		}
	}
	installs := 0
	install := func(rel string) {
		t.Helper()
		cmd := exec.Command(bunPath, "install")
		cmd.Dir = filepath.Join(typesRoot, rel)
		out, err := cmd.CombinedOutput()
		installs++
		if err == nil {
			return
		}
		// Only the first install fetches from the registry; a later failure
		// is the workspace bug, not the network.
		if installs == 1 {
			requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed in %s (likely offline): %v\n%s", rel, err, out))
		}
		t.Fatalf("bun install #%d, in %s, failed: %v\n%s", installs, rel, err, out)
	}

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	edges := loadNestedArraysEdges(t)
	enums, consumer := edges[0], edges[1]

	write(tsPackageCase{name: "fixture-db", schema: dbSchema})
	write(enums)
	install("fixture-db")
	install(enums.name)
	install(".")
	// The next build adds a package that imports from a sibling.
	write(consumer)
	install(consumer.name)
	install(".")
	install("fixture-db")
	install(consumer.name)

	if _, err := os.Stat(filepath.Join(typesRoot, "bun.lock")); err != nil {
		t.Fatalf("the workspace lockfile is not at the root: %v", err)
	}
	for _, name := range []string{enums.name, consumer.name, "fixture-db"} {
		if _, err := os.Stat(filepath.Join(typesRoot, name, "bun.lock")); err == nil {
			t.Fatalf("%s has its own bun.lock; members must share the root lockfile", name)
		}
	}
	check := exec.Command(bunPath, "x", "tsc", "--noEmit")
	check.Dir = filepath.Join(typesRoot, consumer.name)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("%s does not type-check against its installed sibling: %v\n%s", consumer.name, err, out)
	}
}
