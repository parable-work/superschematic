package local_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/stack/local"
)

// engineCrate writes a stand-in of the project's engine crate of db under
// the fixture's output root, its manifest depending on the engine by spec,
// and a built member file of each binding beside it, and returns the
// crate's directory and the cargo messages each member's build answers.
func engineCrate(t *testing.T, f *fixture, db, spec string) (string, map[string]string) {
	t.Helper()
	crate := filepath.Join(f.req.OutputRoot, "engine", "rust", db)
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(crate, "Cargo.toml"), "[package]\nname = \"shop-db-engine\"\n\n[dependencies]\nsuperschematic-engine = "+spec+"\n")
	messages := map[string]string{}
	for member, built := range map[string]struct{ kind, file, content string }{
		projectengine.FFIDir:  {"staticlib", "libshop_db_engine_ffi.a", "the archive"},
		projectengine.NapiDir: {"cdylib", "libshop_db_engine_napi.so", "the addon"},
	} {
		manifest := filepath.Join(crate, member, "Cargo.toml")
		write(manifest, "[package]\nname = \"shop-db-engine-"+member+"\"\n")
		file := filepath.Join(f.req.Dir, local.RustTargetDir, "release", built.file)
		write(file, built.content)
		data, err := json.Marshal(map[string]any{
			"reason":        "compiler-artifact",
			"manifest_path": manifest,
			"target":        map[string]any{"kind": []string{built.kind}},
			"filenames":     []string{file},
		})
		if err != nil {
			t.Fatal(err)
		}
		messages[member] = `{"reason":"build-script-executed"}` + "\n" + string(data) + "\n" + `{"reason":"build-finished","success":true}` + "\n"
	}
	return crate, messages
}

// link writes the projectengine.LinkFile of the deployable name's module,
// which takes member of crate.
func link(t *testing.T, f *fixture, name, crate, member, lockfile string) {
	t.Helper()
	module := filepath.Join(f.req.OutputRoot, local.ModulePath(f.env.Stack, name))
	l, err := projectengine.LinkOf(module, "shop-db", crate, member, lockfile)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectengine.WriteLink(module, l); err != nil {
		t.Fatal(err)
	}
}

// TestTheProjectsEngineBuildsForItsEntrypoints applies Dev of the shop with
// its TypeScript storefront, shop-api's module and the storefront's naming
// the project's engine crate of shop-db (D58): cargo builds the crate's ffi
// member for shop-api, whose go build searches the staged archive's
// directory before the request's CGO_LDFLAGS, and its napi member for the
// storefront, whose process names the staged addon with
// SUPERSCHEMATIC_ENGINE_ADDON; the engine workspace's lock file seeds the
// crate's. Every other build and process is as before.
func TestTheProjectsEngineBuildsForItsEntrypoints(t *testing.T) {
	t.Setenv("RUSTUP_TOOLCHAIN", "")
	f := newFixtureOf(t, storefront(), "Dev")
	crate, messages := engineCrate(t, f, "shop-db", `{ path = "../../../../runtime/engine/rust/engine", default-features = false }`)
	lockfile := filepath.Join(t.TempDir(), "Cargo.lock")
	if err := os.WriteFile(lockfile, []byte("# the engine's lock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link(t, f, "shop-api", crate, projectengine.FFIDir, lockfile)
	link(t, f, "shop-storefront", crate, projectengine.NapiDir, lockfile)
	f.runner.rules = []rule{
		{prefix: inspectPrefix, stderr: "Error response from daemon: No such container: " + f.pgName},
		{prefix: migrateStatus, stderr: "superschematic-migrate: service shop-db has no applied model"},
		{prefix: "/bin/cargo build --release --quiet --manifest-path " + filepath.Join(crate, "ffi", "Cargo.toml"), out: messages[projectengine.FFIDir]},
		{prefix: "/bin/cargo build --release --quiet --manifest-path " + filepath.Join(crate, "napi", "Cargo.toml"), out: messages[projectengine.NapiDir]},
	}
	f.req.BuildEnv = map[string]string{"CGO_LDFLAGS": "-L/cache/archives/lib"}
	if err := f.applyAll(t); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}

	if data, err := os.ReadFile(filepath.Join(crate, "Cargo.lock")); err != nil || string(data) != "# the engine's lock\n" {
		t.Errorf("the crate's lock file is %q (%v), want the engine's", data, err)
	}
	cargos := 0
	for _, cmd := range f.runner.commands {
		if cmd.Path != "/bin/cargo" {
			continue
		}
		cargos++
		if !strings.Contains(strings.Join(cmd.Args, " "), "--target-dir "+filepath.Join(f.req.Dir, local.RustTargetDir)+" ") {
			t.Errorf("cargo %s builds outside the program's target directory", strings.Join(cmd.Args, " "))
		}
		if got := envValue(cmd.Env, "RUSTUP_TOOLCHAIN"); got != "1.99.0" {
			t.Errorf("cargo builds the engine crate with RUSTUP_TOOLCHAIN=%q, want tools.env's release", got)
		}
	}
	if cargos != 2 {
		t.Errorf("cargo ran %d times, want once for shop-api's archive and once for the storefront's addon", cargos)
	}

	archive := filepath.Join(f.req.Dir, local.EngineDir, "shop-db", "ffi")
	for _, cmd := range f.runner.commands {
		if !strings.HasSuffix(cmd.Path, "/go") {
			continue
		}
		got := envValue(cmd.Env, "CGO_LDFLAGS")
		if filepath.Base(cmd.Dir) != "shop-api" {
			if got != "-L/cache/archives/lib" {
				t.Errorf("go build in %s with CGO_LDFLAGS=%q, want the request's alone", cmd.Dir, got)
			}
			continue
		}
		dir, rest, _ := strings.Cut(strings.TrimPrefix(got, "-L"), " ")
		if filepath.Dir(dir) != archive || rest != "-L/cache/archives/lib" {
			t.Fatalf("shop-api builds with CGO_LDFLAGS=%q, want the staged archive's directory under %s, then the request's", got, archive)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "libsuperschematic_engine.a")); err != nil || string(data) != "the archive" {
			t.Errorf("the staged archive is %q (%v), want the ffi member's", data, err)
		}
	}

	for _, p := range f.runner.started {
		addon := envValue(p.cmd.Env, projectengine.AddonEnv)
		if filepath.Base(p.cmd.Dir) != "shop-storefront" {
			if addon != "" {
				t.Errorf("%s runs with %s=%q", p.cmd.Dir, projectengine.AddonEnv, addon)
			}
			continue
		}
		if filepath.Dir(filepath.Dir(addon)) != filepath.Join(f.req.Dir, local.EngineDir, "shop-db", "napi") || filepath.Base(addon) != "shop-db-napi.node" {
			t.Fatalf("the storefront runs with %s=%q, want the staged addon", projectengine.AddonEnv, addon)
		}
		if data, err := os.ReadFile(addon); err != nil || string(data) != "the addon" {
			t.Errorf("the staged addon is %q (%v), want the napi member's", data, err)
		}
	}
	if !strings.Contains(f.out.String(), "build shop-api: cargo build --release of the engine crate of shop-db, its ffi member") {
		t.Errorf("output does not say it builds the engine crate:\n%s", f.out)
	}
}

// TestAnEngineCrateByVersionIsRefused: a crate that depends on the engine
// by version, as a build with no [paths] engine keys writes it, is refused
// before cargo runs, since no registry serves the engine's crates.
func TestAnEngineCrateByVersionIsRefused(t *testing.T) {
	f := newFixture(t, "Dev")
	crate, _ := engineCrate(t, f, "shop-db", `{ version = "0.0.0", default-features = false }`)
	link(t, f, "shop-api", crate, projectengine.FFIDir, "")
	f.runner.rules = []rule{
		{prefix: inspectPrefix, stderr: "Error response from daemon: No such container: " + f.pgName},
		{prefix: migrateStatus, stderr: "superschematic-migrate: service shop-db has no applied model"},
	}
	err := f.applyAll(t)
	if err == nil || !strings.Contains(err.Error(), "local: server shop-api: the engine crate of shop-db depends on superschematic-engine by version") {
		t.Fatalf("apply = %v, want the crate refused", err)
	}
	for _, cmd := range f.runner.commands {
		if cmd.Path == "/bin/cargo" {
			t.Errorf("cargo ran: %s", cmd)
		}
	}
}
