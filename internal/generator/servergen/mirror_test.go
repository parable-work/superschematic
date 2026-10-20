package servergen_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustapigen"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// mirrorManifest is the Cargo.toml of the implementation crate the mirror
// tests copy: its API crate and the API's types crate under the output
// root it was scaffolded for, schemas/dist, one under its dev-dependencies
// by another name; the HTTP runtime's checkout; a crate beside it, one a
// target's, one by an absolute path; its own workspace, and a package
// exclude.
const mirrorManifest = `[package]
name = "schemas-svc-implementation"
version = "0.1.0"
edition = "2021"
build = "build.rs"
exclude = ["fixtures/big"]
workspace = "../.."

[lib]
name = "svc_impl"
path = "src/lib.rs"

[dependencies]
async-trait = "0.1.89"
pinned = { path = "/opt/crates/pinned" }
schemas-svc-api = { path = "../../schemas/dist/api/svc" }
schemas-svc-types = { path = "../../schemas/dist/types/rust/svc" }
shared = { version = "0.1", path = "../shared" }
superschematic-http-runtime = { path = "../../runtime/http/rust", features = ["postgres"] }

[dev-dependencies]
api = { package = "schemas-svc-api", path = "../../schemas/dist/api/svc" }

[target.'cfg(unix)'.dependencies]
unix-only = { path = "../unix-only" }

[profile.release]
opt-level = 3

# Its own workspace.
[workspace]
`

// mirrorCrate writes the implementation crate the mirror tests copy into
// rust/svc under repoRoot, and returns its directory. Beside its manifest
// and sources it holds what the mirror leaves out: its lockfile, target/,
// .git, a crate of its own, what its .gitignore files ignore and what its
// package excludes.
func mirrorCrate(t *testing.T, repoRoot string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, "rust", "svc")
	for name, content := range map[string]string{
		servergen.RustManifestFile: mirrorManifest,
		servergen.RustLockfileName: "version = 4\n",
		".gitignore":               "/scratch\n*.log\n!keep.log\n",
		".hidden/config":           "kept\n",
		".git/HEAD":                "ref: refs/heads/main\n",
		"build.rs":                 "fn main() {}\n",
		"run.sh":                   "#!/bin/sh\n",
		"src/lib.rs":               "mod nested;\n",
		"src/.gitignore":           "generated.rs\n",
		"src/generated.rs":         "// ignored\n",
		"src/nested/mod.rs":        "// nested\n",
		"scratch/notes.txt":        "ignored\n",
		"debug.log":                "ignored\n",
		"keep.log":                 "taken back\n",
		"target/debug/out":         "build output\n",
		"tests/target/case.rs":     "// a test's directory named target\n",
		"vendor/inner/Cargo.toml":  "[package]\nname = \"inner\"\n",
		"vendor/inner/src/lib.rs":  "// a crate of its own\n",
		"fixtures/big/blob":        "excluded\n",
		"fixtures/small.json":      "{}\n",
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

// mirroredFiles are the files of the mirror of mirrorCrate's crate.
var mirroredFiles = []string{
	".gitignore",
	".hidden/config",
	"Cargo.toml",
	"build.rs",
	"fixtures/small.json",
	"keep.log",
	"run.sh",
	"src/.gitignore",
	"src/lib.rs",
	"src/nested/mod.rs",
	"tests/target/case.rs",
}

// planMirror plans a Rust server of svc in the output root out, with the
// implementation at implDir, as the stack's build plans it, and returns
// the plan of the implementation's mirror.
func planMirror(t *testing.T, repoRoot, out, implDir string) (*servergen.RustServer, *servergen.RustMirror) {
	t.Helper()
	s, err := servergen.PlanRust(mirrorInput(t, repoRoot, out, implDir))
	if err != nil {
		t.Fatalf("PlanRust: %v", err)
	}
	if len(s.Mirrors) != 1 {
		t.Fatalf("PlanRust planned %d mirrors, want 1", len(s.Mirrors))
	}
	return s, s.Mirrors[0]
}

// mirrorInput is a Rust server of svc in the output root out, its
// implementation at implDir.
func mirrorInput(t *testing.T, repoRoot, out, implDir string) servergen.RustInput {
	t.Helper()
	impl, err := servergen.ReadRustImplementation(naming.Default(), "svc", implDir)
	if err != nil {
		t.Fatal(err)
	}
	return servergen.RustInput{
		Stack:      "svc-stack",
		Server:     "svc",
		Dir:        servergen.ServerDir(out, "svc-stack", "svc"),
		OutputRoot: out,
		APIs: []servergen.RustAPIInput{{
			Output:         &rustrestgen.APIOutput{APIOutputBase: rustapigen.APIOutputBase{SchemaName: "svc", CrateName: "schemas-svc-api"}, Deps: &rustrestgen.DepsInfo{}},
			Routes:         &apigen.APIOutput{SchemaName: "svc"},
			Dir:            generator.APIDir(out, "svc"),
			Implementation: impl,
		}},
		Paths: naming.LocalPaths{HTTPRuntimeRust: filepath.Join(repoRoot, "runtime", "http", "rust")},
	}
}

// listFiles lists the files under dir, slash-separated and sorted.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		files = append(files, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

// relTo is target's path from dir, slash-separated.
func relTo(t *testing.T, dir, target string) string {
	t.Helper()
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// TestARustMirrorHoldsTheCrate (D65): a build into the output root the
// crate's scaffold was written for, and one into another, each copy the
// implementation into the root's impl/<service>, and the server's crate
// depends on that copy. The copy holds the crate's files but its lockfile,
// target/, .git, a crate of its own, what its .gitignore files ignore and
// what its package excludes; executable files stay executable. Its
// Cargo.toml names the root's own API crate, under its dev-dependencies
// too, the root's own types crate, which the crate named under the output
// root its API crate is under, and every other path dependency where the
// crate's names it; it drops the crate's [workspace] and
// package.workspace, and keeps the rest.
func TestARustMirrorHoldsTheCrate(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	source := mirrorCrate(t, repoRoot)
	for _, out := range []string{filepath.Join(repoRoot, "schemas", "dist"), filepath.Join(testpaths.TempDir(t), "out")} {
		s, m := planMirror(t, repoRoot, out, source)
		if m.Dir != servergen.MirrorDir(out, "svc") || m.Source != source {
			t.Errorf("the mirror of %s is %s, want %s", m.Source, m.Dir, servergen.MirrorDir(out, "svc"))
		}
		if !slices.Contains(s.Dependencies, servergen.CargoDependency{Name: "schemas-svc-implementation", Spec: `{ path = "../../../impl/svc" }`}) {
			t.Errorf("the server's crate does not depend on the mirror: %+v", s.Dependencies)
		}
		if err := servergen.WriteRustMirror(m); err != nil {
			t.Fatalf("WriteRustMirror into %s: %v", out, err)
		}
		if got := listFiles(t, m.Dir); !slices.Equal(got, mirroredFiles) {
			t.Errorf("the mirror in %s holds\n%s\nwant\n%s", out, strings.Join(got, "\n"), strings.Join(mirroredFiles, "\n"))
		}
		if info, err := os.Stat(filepath.Join(m.Dir, "run.sh")); err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("run.sh in the mirror: %v, %v, want mode 0755", info, err)
		}

		data, err := os.ReadFile(filepath.Join(m.Dir, servergen.RustManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "# Code generated by superschematic. DO NOT EDIT.\n") {
			t.Errorf("the mirror's Cargo.toml has no header:\n%s", data)
		}
		var manifest struct {
			Package         map[string]any            `toml:"package"`
			Lib             map[string]any            `toml:"lib"`
			Dependencies    map[string]any            `toml:"dependencies"`
			DevDependencies map[string]map[string]any `toml:"dev-dependencies"`
			Target          map[string]map[string]map[string]map[string]any
			Profile         map[string]map[string]any `toml:"profile"`
			Workspace       map[string]any            `toml:"workspace"`
		}
		if _, err := toml.Decode(string(data), &manifest); err != nil {
			t.Fatalf("the mirror's Cargo.toml: %v\n%s", err, data)
		}
		path := func(dep any) string {
			spec, _ := dep.(map[string]any)
			p, _ := spec["path"].(string)
			return p
		}
		for name, want := range map[string]string{
			"schemas-svc-api":             "../../api/svc",
			"schemas-svc-types":           "../../types/rust/svc",
			"superschematic-http-runtime": relTo(t, m.Dir, filepath.Join(repoRoot, "runtime", "http", "rust")),
			"shared":                      relTo(t, m.Dir, filepath.Join(repoRoot, "rust", "shared")),
			"pinned":                      "/opt/crates/pinned",
		} {
			if got := path(manifest.Dependencies[name]); got != want {
				t.Errorf("in %s, the mirror's %s is at %q, want %q", out, name, got, want)
			}
		}
		if spec, _ := manifest.Dependencies["shared"].(map[string]any); spec["version"] != "0.1" {
			t.Errorf("the mirror's shared lost its version: %v", spec)
		}
		if spec, _ := manifest.Dependencies["superschematic-http-runtime"].(map[string]any); !slices.Equal(stringsOf(spec["features"]), []string{"postgres"}) {
			t.Errorf("the mirror's runtime lost its features: %v", spec)
		}
		if manifest.Dependencies["async-trait"] != "0.1.89" {
			t.Errorf("the mirror's async-trait is %v", manifest.Dependencies["async-trait"])
		}
		if api := manifest.DevDependencies["api"]; api["path"] != "../../api/svc" || api["package"] != "schemas-svc-api" {
			t.Errorf("the mirror's dev-dependency on the API crate is %v", api)
		}
		if got, want := path(manifest.Target["cfg(unix)"]["dependencies"]["unix-only"]), relTo(t, m.Dir, filepath.Join(repoRoot, "rust", "unix-only")); got != want {
			t.Errorf("the mirror's unix-only is at %q, want %q", got, want)
		}
		if manifest.Workspace != nil || manifest.Package["workspace"] != nil {
			t.Errorf("the mirror kept the crate's workspace: %v, %v", manifest.Workspace, manifest.Package["workspace"])
		}
		if manifest.Package["name"] != "schemas-svc-implementation" || manifest.Package["build"] != "build.rs" || manifest.Lib["name"] != "svc_impl" || manifest.Profile["release"]["opt-level"] != int64(3) {
			t.Errorf("the mirror lost what it keeps:\n%s", data)
		}
	}
}

// stringsOf is a decoded array of strings.
func stringsOf(v any) []string {
	items, _ := v.([]any)
	var out []string
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// TestARustMirrorFollowsTheCrate (D65): a build after the crate changed
// copies what changed into the mirror, and only that, so a file whose
// content is the same keeps its modification time, Cargo's fingerprint;
// a file the crate no longer has leaves the mirror, with a directory it
// leaves empty; a file's mode follows the crate's.
func TestARustMirrorFollowsTheCrate(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	source := mirrorCrate(t, repoRoot)
	_, m := planMirror(t, repoRoot, filepath.Join(testpaths.TempDir(t), "out"), source)
	if err := servergen.WriteRustMirror(m); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, file := range []string{"build.rs", "src/lib.rs", servergen.RustManifestFile} {
		if err := os.Chtimes(filepath.Join(m.Dir, filepath.FromSlash(file)), old, old); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(source, "src", "nested", "mod.rs"), []byte("// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "src", "added.rs"), []byte("// added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(source, "fixtures", "small.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(source, "keep.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "run.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := servergen.WriteRustMirror(m); err != nil {
		t.Fatal(err)
	}

	want := slices.DeleteFunc(slices.Clone(mirroredFiles), func(f string) bool { return f == "fixtures/small.json" || f == "keep.log" })
	want = append(want, "src/added.rs")
	slices.Sort(want)
	if got := listFiles(t, m.Dir); !slices.Equal(got, want) {
		t.Errorf("the mirror holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "fixtures")); !os.IsNotExist(err) {
		t.Errorf("the mirror kept the directory the crate emptied: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(m.Dir, "src", "nested", "mod.rs")); err != nil || string(got) != "// changed\n" {
		t.Errorf("the change did not reach the mirror: %q, %v", got, err)
	}
	for _, file := range []string{"build.rs", "src/lib.rs", servergen.RustManifestFile} {
		if info, err := os.Stat(filepath.Join(m.Dir, filepath.FromSlash(file))); err != nil || !info.ModTime().Equal(old) {
			t.Errorf("the build rewrote %s, which did not change: %v", file, err)
		}
	}
	if info, err := os.Stat(filepath.Join(m.Dir, "run.sh")); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("run.sh's mode did not follow the crate's: %v, %v", info, err)
	}
}

// TestARustMirrorRefuses (D65): the plan refuses an implementation that
// inherits from a workspace, which its mirror belongs to none of, or names
// a target outside the crate, which its mirror does not hold, and one that
// lies in its mirror's directory; the write refuses a mirror's directory
// holding a Cargo.toml no build wrote.
func TestARustMirrorRefuses(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	crate := func(dir, manifest string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, servergen.RustManifestFile), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	const head = "[package]\nname = \"schemas-svc-implementation\"\n"
	for _, c := range []struct{ manifest, want string }{
		{head + "version.workspace = true\n", "inherits package.version from a workspace"},
		{head + "[dependencies]\ntokio = { workspace = true }\n", "inherits dependencies.tokio from a workspace"},
		{head + "[lints]\nworkspace = true\n", "inherits lints from a workspace"},
		{head + "[lib]\npath = \"../common/lib.rs\"\n", `names lib.path "../common/lib.rs", outside the crate`},
		{head + "build = \"/opt/build.rs\"\n", `names package.build "/opt/build.rs", outside the crate`},
	} {
		dir := crate(filepath.Join(repoRoot, "rust", "svc"), c.manifest)
		if _, err := servergen.PlanRust(mirrorInput(t, repoRoot, out, dir)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("PlanRust over\n%s= %v, want %q", c.manifest, err, c.want)
		}
	}
	inside := crate(servergen.MirrorDir(out, "svc"), head)
	if _, err := servergen.PlanRust(mirrorInput(t, repoRoot, out, inside)); err == nil || !strings.Contains(err.Error(), "lies in or around its mirror's directory") {
		t.Errorf("PlanRust of an implementation in its mirror's directory = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(out, servergen.MirrorRoot)); err != nil {
		t.Fatal(err)
	}

	_, m := planMirror(t, repoRoot, out, crate(filepath.Join(repoRoot, "rust", "svc"), head))
	crate(m.Dir, head)
	if err := servergen.WriteRustMirror(m); err == nil || !strings.Contains(err.Error(), "holds a Cargo.toml no build wrote") {
		t.Errorf("WriteRustMirror over a project's crate = %v", err)
	}
}

// TestPruneRustMirrors (D65): a build removes each mirror no server crate
// under the output root depends on, whichever stack's, then impl/ once it
// is empty, and leaves a directory there no build wrote.
func TestPruneRustMirrors(t *testing.T) {
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
	mirror := "# Code generated by superschematic. DO NOT EDIT.\n[package]\nname = \"m\"\n"
	for _, service := range []string{"kept", "gone"} {
		write(filepath.Join(servergen.MirrorDir(out, service), servergen.RustManifestFile), mirror)
	}
	write(filepath.Join(servergen.MirrorDir(out, "foreign"), servergen.RustManifestFile), "[package]\nname = \"foreign\"\n")
	server := filepath.Join(servergen.ServerDir(out, "b-stack", "kept"), servergen.RustManifestFile)
	write(server, "[dependencies]\nm = { path = \"../../../impl/kept\" }\nr = \"1\"\n")
	if err := servergen.PruneRustMirrors(out); err != nil {
		t.Fatal(err)
	}
	for service, want := range map[string]bool{"kept": true, "gone": false, "foreign": true} {
		if _, err := os.Stat(servergen.MirrorDir(out, service)); (err == nil) != want {
			t.Errorf("mirror %s on disk: %v, want %v", service, err == nil, want)
		}
	}
	if err := os.Remove(server); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(servergen.MirrorDir(out, "foreign")); err != nil {
		t.Fatal(err)
	}
	if err := servergen.PruneRustMirrors(out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, servergen.MirrorRoot)); !os.IsNotExist(err) {
		t.Errorf("impl/ outlived the last mirror: %v", err)
	}
}
