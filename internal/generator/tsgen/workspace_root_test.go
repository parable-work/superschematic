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

// The output root's package.json is the Bun workspace root of every
// generated TypeScript package, the implementations (D51) and the sites,
// at their own template (D55).
func TestWorkspaceRootManifest(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Dir(root)
	names := naming.Naming{NpmScope: "@acme", ImplementationPaths: naming.ImplementationPathsConfig{TypeScript: "services/{service}/ts"}}
	w := WorkspaceRoot{
		OutputRoot:     filepath.Join(repo, "schemas", "dist"),
		RepositoryRoot: repo,
		Naming:         names,
		Paths: naming.LocalPaths{
			ScalarTypeScript:      filepath.Join(repo, "third_party", "superscalar", "bindings", "typescript"),
			HTTPRuntimeTypeScript: filepath.Join(repo, "runtime", "http", "typescript"),
		},
	}
	got, err := w.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "name": "@acme/workspace",
  "private": true,
  "workspaces": [
    "types/typescript/*",
    "sdk/typescript/*",
    "api/*",
    "server/*/*",
    "../../services/*/ts",
    "../../web/*"
  ],
  "dependencies": {
    "superscalar": "file:../../third_party/superscalar/bindings/typescript"
  },
  "overrides": {
    "@superschematic/http-runtime": "file:../../runtime/http/typescript",
    "superscalar": "file:../../third_party/superscalar/bindings/typescript"
  }
}
`
	if got != want {
		t.Fatalf("manifest:\n%s\nwant:\n%s", got, want)
	}

	// Without [paths] or a repository root: the members the output root
	// holds, and superscalar by name.
	bare, err := (WorkspaceRoot{OutputRoot: root}).Manifest()
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Name         string            `json:"name"`
		Private      bool              `json:"private"`
		Workspaces   []string          `json:"workspaces"`
		Dependencies map[string]string `json:"dependencies"`
		Overrides    map[string]string `json:"overrides"`
	}
	if err := json.Unmarshal([]byte(bare), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != naming.Default().NpmScope+"/workspace" || !manifest.Private || len(manifest.Workspaces) != 4 ||
		manifest.Dependencies["superscalar"] != "*" || manifest.Overrides != nil {
		t.Fatalf("manifest without paths: %s", bare)
	}
}

// TestIgnoredLockfile: the workspace's lockfile, which the project commits
// (D51, amended), is ignored under a rule that ignores the output root
// itself, which names the rule, and not under one that ignores its
// contents and takes the lockfile back, nor once git tracks it; outside a
// repository nothing is ignored.
func TestIgnoredLockfile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// No global or system ignore file of this machine's.
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo := t.TempDir()
	git(repo, "init", "--quiet")
	out := filepath.Join(repo, "schemas", "dist")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	ignore := func(rules string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(rules), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ignore("node_modules/\ndist/\n")
	if rule, ignored := IgnoredLockfile(out); !ignored || rule != ".gitignore:2:dist/" {
		t.Errorf("under dist/: %q, %v; want .gitignore:2:dist/", rule, ignored)
	}
	ignore("dist/*\n!dist/bun.lock\n")
	if rule, ignored := IgnoredLockfile(out); ignored {
		t.Errorf("under dist/* and !dist/bun.lock: ignored by %q", rule)
	}
	// A nested ignore file takes the directory back from a broader rule.
	ignore("dist/\n")
	if err := os.WriteFile(filepath.Join(repo, "schemas", ".gitignore"), []byte("!/dist/\n/dist/*\n!/dist/bun.lock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rule, ignored := IgnoredLockfile(out); ignored {
		t.Errorf("under schemas/.gitignore's exception: ignored by %q", rule)
	}
	// A tracked lockfile is not ignored, whatever the rules say.
	if err := os.Remove(filepath.Join(repo, "schemas", ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, LockfileName), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ignored := IgnoredLockfile(out); !ignored {
		t.Error("an untracked lockfile under dist/ is not ignored")
	}
	git(repo, "add", "--force", "schemas/dist/bun.lock")
	if rule, ignored := IgnoredLockfile(out); ignored {
		t.Errorf("a tracked lockfile: ignored by %q", rule)
	}

	if rule, ignored := IgnoredLockfile(t.TempDir()); ignored {
		t.Errorf("outside a repository: ignored by %q", rule)
	}
}

// TestWriteWorkspaceRoot: builds that run in parallel each write the
// root for their own package, and the result is one whole file; the
// types-only root an earlier build wrote is removed, unless edited.
func TestWriteWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	names := naming.Naming{NpmScope: "@acme"}
	w := WorkspaceRoot{OutputRoot: root, Naming: names}
	legacyDir := filepath.Join(root, "types", "typescript")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "package.json"), []byte(legacyTypesWorkspaceManifest(names)), 0o644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Write(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Write: %v", err)
	}
	want, err := w.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("manifest drifted after concurrent writes:\n%s", got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "package.json" && entry.Name() != "types" {
			t.Fatalf("unexpected leftover %q in the output root", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "package.json")); !os.IsNotExist(err) {
		t.Fatalf("the types-only root is still there: %v", err)
	}

	// An edited types root is the engineer's, and stays.
	edited := `{"name": "mine", "private": true, "workspaces": ["*"]}`
	if err := os.WriteFile(filepath.Join(legacyDir, "package.json"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(); err != nil {
		t.Fatal(err)
	}
	if kept, err := os.ReadFile(filepath.Join(legacyDir, "package.json")); err != nil || string(kept) != edited {
		t.Fatalf("an edited types root was not kept: %q, %v", kept, err)
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
// workspace the way the docs allow: in members and at the root, again
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
	workspace := WorkspaceRoot{OutputRoot: tempRoot, Paths: paths}
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
		if err := workspace.Write(); err != nil {
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
	install("../..")
	// The next build adds a package that imports from a sibling.
	write(consumer)
	install(consumer.name)
	install("../..")
	install("fixture-db")
	install(consumer.name)

	if _, err := os.Stat(filepath.Join(tempRoot, "bun.lock")); err != nil {
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
