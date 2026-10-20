package servergen_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// tsMirrorManifest is the package.json of the implementation package the
// TypeScript mirror tests copy: a comment of its own, a script whose &&
// stays as it is, and dependencies by name, on workspace members, into
// the default output root it was scaffolded for, inside the package, by
// an absolute path, and beside it, with file:, link: and a bare path.
const tsMirrorManifest = `{
  "name": "@schemas/svc-implementation",
  "//": "the package's own comment",
  "version": "0.0.0",
  "private": true,
  "type": "module",
  "main": "index.ts",
  "scripts": {
    "check": "tsc --noEmit -p tsconfig.json && bun test"
  },
  "dependencies": {
    "@schemas/svc-api": "workspace:*",
    "@schemas/svc-types": "file:../../schemas/dist/types/typescript/svc",
    "local": "link:./vendor/local",
    "pinned": "file:/opt/packages/pinned",
    "shared": "file:../shared",
    "sibling": "../sibling",
    "hono": "4.13.8"
  },
  "devDependencies": {
    "@schemas/svc-sdk": "workspace:*",
    "typescript": "5.9.3"
  }
}
`

// tsMirrorPackage writes the implementation package the TypeScript mirror
// tests copy into typescript/svc under repoRoot, and returns its
// directory. Beside its manifest and sources it holds what the mirror
// leaves out: its lockfiles, node_modules at two depths, .git, and what
// its .gitignore files ignore.
func tsMirrorPackage(t *testing.T, repoRoot string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, "typescript", "svc")
	for name, content := range map[string]string{
		servergen.TypeScriptPackageFile:      tsMirrorManifest,
		servergen.TypeScriptConfigFile:       `{ "include": ["**/*.ts"] }` + "\n",
		"bun.lock":                           "{}\n",
		"bun.lockb":                          "binary\n",
		".gitignore":                         "/scratch\n*.log\n!keep.log\n",
		".git/HEAD":                          "ref: refs/heads/main\n",
		"index.ts":                           "export const create = () => ({});\n",
		"index.test.ts":                      "// a test\n",
		"run.sh":                             "#!/bin/sh\n",
		"src/.gitignore":                     "generated.ts\n",
		"src/generated.ts":                   "// ignored\n",
		"src/carts.ts":                       "// carts\n",
		"src/node_modules/dep/index.js":      "// linked by an install\n",
		"node_modules/@schemas/svc-api/x.js": "// linked by an install\n",
		"node_modules/.bin/tsc":              "#!/bin/sh\n",
		"vendor/local/package.json":          `{ "name": "local" }` + "\n",
		"vendor/local/bun.lock":              "{}\n",
		"dist/index.js":                      "// a build no ignore file names\n",
		"scratch/notes.txt":                  "ignored\n",
		"debug.log":                          "ignored\n",
		"keep.log":                           "taken back\n",
	} {
		file := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// tsMirroredFiles are the files of the mirror of tsMirrorPackage's
// package.
var tsMirroredFiles = []string{
	".gitignore",
	"dist/index.js",
	"index.test.ts",
	"index.ts",
	"keep.log",
	"package.json",
	"run.sh",
	"src/.gitignore",
	"src/carts.ts",
	"tsconfig.json",
	"vendor/local/bun.lock",
	"vendor/local/package.json",
}

// tsMirrorInput is a TypeScript server of svc in the output root out, its
// implementation at implDir, in a project whose default output root is
// schemas/dist under repoRoot.
func tsMirrorInput(repoRoot, out, implDir string) servergen.TypeScriptInput {
	return servergen.TypeScriptInput{
		Stack:             "svc-stack",
		Server:            "svc",
		Dir:               servergen.ServerDir(out, "svc-stack", "svc"),
		OutputRoot:        out,
		DefaultOutputRoot: filepath.Join(repoRoot, "schemas", "dist"),
		APIs: []servergen.TypeScriptAPIInput{{
			Output:         &tsrestgen.APIOutput{SchemaName: "svc", PackageName: "@schemas/svc-api"},
			Routes:         &apigen.APIOutput{SchemaName: "svc"},
			Implementation: servergen.TypeScriptImplementation{Dir: implDir, Package: "@schemas/svc-implementation"},
		}},
	}
}

// planTSMirror plans the TypeScript server of tsMirrorInput and returns
// the plan of its implementation's mirror.
func planTSMirror(t *testing.T, repoRoot, out, implDir string) *servergen.TypeScriptMirror {
	t.Helper()
	s, err := servergen.PlanTypeScript(tsMirrorInput(repoRoot, out, implDir))
	if err != nil {
		t.Fatalf("PlanTypeScript: %v", err)
	}
	if len(s.Mirrors) != 1 {
		t.Fatalf("PlanTypeScript planned %d mirrors, want 1", len(s.Mirrors))
	}
	return s.Mirrors[0]
}

// TestATypeScriptMirrorHoldsThePackage (D65, amended): a build into the
// default output root mirrors nothing, and its server builds against the
// package; one into another output root copies the package into the
// root's impl/<service>. The copy holds the package's files but its
// lockfiles, node_modules at any depth, .git and what its .gitignore files
// ignore; executable files stay executable. Its package.json is the
// package's, member by member in their order, with the mirror's notice in
// place of the package's own comment, and each dependency on a path
// re-based: the one into the default output root names the same place
// under this root, the one inside the package and the absolute one stay,
// and the others name the same directories from the mirror's.
func TestATypeScriptMirrorHoldsThePackage(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	source := tsMirrorPackage(t, repoRoot)
	def, err := servergen.PlanTypeScript(tsMirrorInput(repoRoot, filepath.Join(repoRoot, "schemas", "dist"), source))
	if err != nil || len(def.Mirrors) != 0 {
		t.Fatalf("PlanTypeScript in the default output root = %d mirrors, %v; want none", len(def.Mirrors), err)
	}

	out := filepath.Join(testpaths.TempDir(t), "out")
	m := planTSMirror(t, repoRoot, out, source)
	if m.Dir != servergen.MirrorDir(out, "svc") || m.Source != source || m.Package != "@schemas/svc-implementation" {
		t.Errorf("the mirror of %s is %s of %s, want %s", m.Source, m.Dir, m.Package, servergen.MirrorDir(out, "svc"))
	}
	if err := servergen.WriteTypeScriptMirror(m); err != nil {
		t.Fatalf("WriteTypeScriptMirror: %v", err)
	}
	if got := listFiles(t, m.Dir); !slices.Equal(got, tsMirroredFiles) {
		t.Errorf("the mirror holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tsMirroredFiles, "\n"))
	}
	if info, err := os.Stat(filepath.Join(m.Dir, "run.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("run.sh in the mirror: %v, %v, want mode 0755", info, err)
	}
	got, err := os.ReadFile(filepath.Join(m.Dir, servergen.TypeScriptPackageFile))
	if err != nil {
		t.Fatal(err)
	}
	notice := "Code generated by superschematic. DO NOT EDIT. The mirror of the implementation of svc, the package at " + relTo(t, m.Dir, source) +
		" (D65; docs/stack-model.md, section 8.6). Each build of a stack into this output root copies the package's files here" +
		" and writes this file from its package.json, with each dependency on a path re-based for this directory." +
		" This output root's servers, jobs and workers build against the mirror, which its Bun workspace lists in place of the package." +
		" Edit the package, not the mirror."
	want := `{
  "//": "` + notice + `",
  "name": "@schemas/svc-implementation",
  "version": "0.0.0",
  "private": true,
  "type": "module",
  "main": "index.ts",
  "scripts": {
    "check": "tsc --noEmit -p tsconfig.json && bun test"
  },
  "dependencies": {
    "@schemas/svc-api": "workspace:*",
    "@schemas/svc-types": "file:../../types/typescript/svc",
    "local": "link:./vendor/local",
    "pinned": "file:/opt/packages/pinned",
    "shared": "file:` + relTo(t, m.Dir, filepath.Join(repoRoot, "typescript", "shared")) + `",
    "sibling": "` + relTo(t, m.Dir, filepath.Join(repoRoot, "typescript", "sibling")) + `",
    "hono": "4.13.8"
  },
  "devDependencies": {
    "@schemas/svc-sdk": "workspace:*",
    "typescript": "5.9.3"
  }
}
`
	if string(got) != want {
		t.Errorf("the mirror's package.json:\n%s\nwant:\n%s", got, want)
	}
}

// TestATypeScriptMirrorFollowsThePackage (D65, amended): a build after
// the package changed copies what changed into the mirror, and only that,
// so a file whose content is the same keeps its modification time; a file
// the package no longer has leaves the mirror, with a directory it leaves
// empty; a change to package.json reaches the mirror's.
func TestATypeScriptMirrorFollowsThePackage(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	source := tsMirrorPackage(t, repoRoot)
	m := planTSMirror(t, repoRoot, filepath.Join(testpaths.TempDir(t), "out"), source)
	if err := servergen.WriteTypeScriptMirror(m); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, file := range []string{"index.ts", "src/carts.ts", servergen.TypeScriptPackageFile} {
		if err := os.Chtimes(filepath.Join(m.Dir, filepath.FromSlash(file)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "index.test.ts"), []byte("// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "src", "orders.ts"), []byte("// added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(source, "vendor")); err != nil {
		t.Fatal(err)
	}
	if err := servergen.WriteTypeScriptMirror(m); err != nil {
		t.Fatal(err)
	}
	want := slices.DeleteFunc(slices.Clone(tsMirroredFiles), func(f string) bool { return strings.HasPrefix(f, "vendor/") })
	want = append(want, "src/orders.ts")
	slices.Sort(want)
	if got := listFiles(t, m.Dir); !slices.Equal(got, want) {
		t.Errorf("the mirror holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "vendor")); !os.IsNotExist(err) {
		t.Errorf("the mirror kept the directory the package emptied: %v", err)
	}
	for _, file := range []string{"index.ts", "src/carts.ts", servergen.TypeScriptPackageFile} {
		if info, err := os.Stat(filepath.Join(m.Dir, filepath.FromSlash(file))); err != nil || !info.ModTime().Equal(old) {
			t.Errorf("the build rewrote %s, which did not change: %v", file, err)
		}
	}

	manifest := strings.Replace(tsMirrorManifest, `"hono": "4.13.8"`, `"hono": "4.13.9"`, 1)
	if err := os.WriteFile(filepath.Join(source, servergen.TypeScriptPackageFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := servergen.WriteTypeScriptMirror(m); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(m.Dir, servergen.TypeScriptPackageFile)); err != nil || !strings.Contains(string(got), `"hono": "4.13.9"`) {
		t.Errorf("the change to package.json did not reach the mirror's: %v\n%s", err, got)
	}
}

// TestATypeScriptMirrorRefuses (D65, amended): the plan refuses an
// implementation whose package.json does not parse, and one that lies in
// its mirror's directory; the write refuses a mirror's directory holding a
// package.json no build wrote.
func TestATypeScriptMirrorRefuses(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(testpaths.TempDir(t), "out")
	pkg := func(dir, manifest string) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, servergen.TypeScriptPackageFile), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.ts"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for _, c := range []struct{ manifest, want string }{
		{`{ "name": `, "has a package.json that does not parse"},
		{`["not", "an", "object"]`, "has a package.json that does not parse"},
		{`{ "name": "a" } { "name": "b" }`, "has a package.json that does not parse"},
		{`{ "name": "a", "dependencies": ["b"] }`, "has a package.json whose dependencies is not an object"},
	} {
		dir := pkg(filepath.Join(repoRoot, "typescript", "svc"), c.manifest)
		if _, err := servergen.PlanTypeScript(tsMirrorInput(repoRoot, out, dir)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("PlanTypeScript over %s = %v, want %q", c.manifest, err, c.want)
		}
	}
	inside := pkg(servergen.MirrorDir(out, "svc"), `{ "name": "@schemas/svc-implementation" }`)
	if _, err := servergen.PlanTypeScript(tsMirrorInput(repoRoot, out, inside)); err == nil || !strings.Contains(err.Error(), "lies in or around its mirror's directory") {
		t.Errorf("PlanTypeScript of an implementation in its mirror's directory = %v", err)
	}

	m := planTSMirror(t, repoRoot, out, pkg(filepath.Join(repoRoot, "typescript", "svc"), `{ "name": "@schemas/svc-implementation" }`))
	if err := servergen.WriteTypeScriptMirror(m); err == nil || !strings.Contains(err.Error(), "holds a package.json no build wrote") {
		t.Errorf("WriteTypeScriptMirror over a project's package = %v", err)
	}
}

// TestPruneTypeScriptMirrors (D65, amended): a build removes each
// TypeScript mirror no server, job or worker package under the output
// root depends on, whichever stack's, then impl/ once it is empty; it
// leaves a directory there no build wrote, and a Rust mirror. In the
// default output root, which builds against the project's packages, it
// removes every TypeScript mirror, whose name the project's package holds.
func TestPruneTypeScriptMirrors(t *testing.T) {
	out := testpaths.TempDir(t)
	write := func(file, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mirror := func(name string) string {
		return `{ "//": "Code generated by superschematic. DO NOT EDIT. A mirror.", "name": "` + name + `" }`
	}
	write(filepath.Join(servergen.MirrorDir(out, "kept"), servergen.TypeScriptPackageFile), mirror("@schemas/kept-implementation"))
	write(filepath.Join(servergen.MirrorDir(out, "dev"), servergen.TypeScriptPackageFile), mirror("@schemas/dev-implementation"))
	write(filepath.Join(servergen.MirrorDir(out, "gone"), servergen.TypeScriptPackageFile), mirror("@schemas/gone-implementation"))
	write(filepath.Join(servergen.MirrorDir(out, "foreign"), servergen.TypeScriptPackageFile), `{ "name": "@schemas/gone-implementation" }`)
	write(filepath.Join(servergen.MirrorDir(out, "rust"), servergen.RustManifestFile), "# Code generated by superschematic. DO NOT EDIT.\n[package]\nname = \"r\"\n")
	server := filepath.Join(servergen.ServerDir(out, "b-stack", "kept"), servergen.TypeScriptPackageFile)
	write(server, `{ "dependencies": { "@schemas/kept-implementation": "workspace:*" }, "devDependencies": { "@schemas/dev-implementation": "workspace:*" } }`)
	if err := servergen.PruneTypeScriptMirrors(out, true); err != nil {
		t.Fatal(err)
	}
	for service, want := range map[string]bool{"kept": true, "dev": true, "gone": false, "foreign": true, "rust": true} {
		if _, err := os.Stat(servergen.MirrorDir(out, service)); (err == nil) != want {
			t.Errorf("mirror %s on disk: %v, want %v", service, err == nil, want)
		}
	}
	if err := servergen.PruneTypeScriptMirrors(out, false); err != nil {
		t.Fatal(err)
	}
	for service, want := range map[string]bool{"kept": false, "dev": false, "foreign": true, "rust": true} {
		if _, err := os.Stat(servergen.MirrorDir(out, service)); (err == nil) != want {
			t.Errorf("in the default output root, mirror %s on disk: %v, want %v", service, err == nil, want)
		}
	}
	for _, service := range []string{"foreign", "rust"} {
		if err := os.RemoveAll(servergen.MirrorDir(out, service)); err != nil {
			t.Fatal(err)
		}
	}
	if err := servergen.PruneTypeScriptMirrors(out, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, servergen.MirrorRoot)); !os.IsNotExist(err) {
		t.Errorf("impl/ outlived the last mirror: %v", err)
	}
}
