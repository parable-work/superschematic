package projectengine_test

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite the goldens in testdata/golden")

// acme declares acme.Rating, as the acme-schematic example does, and
// acme.Flag, from one crate; its invocation policy and tool hook are the
// example's. acme.Pin names no crate.
type acme struct{}

func (acme) Name() string { return "acme" }

func (acme) Register(r *registry.Registry) error {
	crate := func(register string) registry.BehaviorRust {
		return registry.BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: register}
	}
	return errors.Join(
		r.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Rust: crate("acme_behaviors::Rating"), Declaration: json.RawMessage(`{"name":"acme.Rating"}`)}),
		r.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Rust: crate("acme_behaviors::flag::Flag::new()"), Declaration: json.RawMessage(`{"name":"acme.Flag"}`)}),
		r.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: json.RawMessage(`{"name":"acme.Pin"}`)}),
		r.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{Extension: "acme", Key: "confirm", Values: []string{"never", "always"}, Default: "never"}),
		r.RegisterToolHook(registry.ToolHook{Name: "keys", Extension: "acme", Edit: func(_ *ir.Schema, set *apigen.ToolSet) error {
			set.Keys.Scalar = "x-acme-scalar"
			set.Keys.Guidance = "acme/operation-guidance"
			set.Keys.Parameters = append(set.Keys.Parameters, apigen.ToolKeyValue{Key: "x-acme-arguments", Value: 1})
			return nil
		}}),
	)
}

// desk declares desk.Triage, from a crate of its own.
type desk struct{}

func (desk) Name() string { return "desk" }

func (desk) Register(r *registry.Registry) error {
	return r.RegisterBehavior(registry.BehaviorSpec{
		Extension:   "desk",
		Rust:        registry.BehaviorRust{Crate: "desk-triage", Path: "vendor/desk", Register: "desk_triage::Triage"},
		Declaration: json.RawMessage(`{"name":"desk.Triage"}`),
	})
}

func extensions(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := reg.Use(acme{}, desk{}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// schema is a schema whose Ticket and Note compose the behaviors given and
// whose Point composes none.
func schema(kind ir.SchemaKind, ticket, note []string) *ir.Schema {
	refs := func(names []string) []ir.BehaviorRef {
		var out []ir.BehaviorRef
		for _, name := range names {
			out = append(out, ir.BehaviorRef{Name: name})
		}
		return out
	}
	return &ir.Schema{Name: "shop", Kind: kind, Types: map[string]*ir.TypeDef{
		"Ticket": {Name: "Ticket", Behaviors: refs(ticket)},
		"Note":   {Name: "Note", Behaviors: refs(note)},
		"Point":  {Name: "Point"},
	}}
}

// project is a repository with the extensions' crates and a checkout of
// the engine's, and the options that build into its output root.
func project(t *testing.T, reg *registry.Registry, withPaths bool) (string, projectengine.Options) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"crates/behaviors", "vendor/desk"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "Cargo.toml"), []byte("[package]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := projectengine.Options{
		SchemaName:     "shop",
		Registry:       reg,
		Naming:         naming.Default(),
		RepositoryRoot: root,
		Dir:            filepath.Join(root, "schemas", "dist", "engine", "rust", "shop"),
		RustVersion:    "1.99.0",
	}
	if withPaths {
		opts.Paths = naming.LocalPaths{
			EngineRust:          filepath.Join(root, "runtime", "engine", "rust", "engine"),
			EngineBehaviorsRust: filepath.Join(root, "runtime", "engine", "rust", "behaviors"),
			EngineWorkQueueRust: filepath.Join(root, "runtime", "engine-workqueue", "rust"),
			EngineABIRust:       filepath.Join(root, "runtime", "engine", "rust", "abi"),
			EngineNapiRust:      filepath.Join(root, "runtime", "engine", "rust", "napi"),
		}
	}
	return root, opts
}

func build(t *testing.T, s *ir.Schema, opts projectengine.Options) *projectengine.Crate {
	t.Helper()
	crate, why, err := projectengine.Build(s, opts)
	if err != nil {
		t.Fatal(err)
	}
	if crate == nil {
		t.Fatalf("Build wrote no crate: %q", why)
	}
	return crate
}

// checkGoldens holds the crate's files to testdata/golden/<name>, or
// rewrites them under -update:
//
//	go test ./internal/generator/projectengine -update
func checkGoldens(t *testing.T, name string, crate *projectengine.Crate) map[string][]byte {
	t.Helper()
	files, err := crate.Files()
	if err != nil {
		t.Fatal(err)
	}
	for file, got := range files {
		path := filepath.Join("testdata", "golden", name, filepath.FromSlash(file)+".golden")
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (rewrite it with -update)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from %s (rewrite it with -update):\n%s", file, path, got)
		}
	}
	return files
}

// A DB schema's engine types compose the core's behaviors, the work
// queue's Lease and three extension behaviors from two crates: the crate
// depends on the engine's three crates at the [paths] checkouts and on
// each extension crate at its path in the project, and registers each
// composed extension behavior by its expression, by name, after the core's
// and the work queue's, under the binary's tool options; its ffi member,
// a member of the crate's workspace, exports the C ABI over behaviors(),
// with the C ABI's library at its [paths] checkout, and its napi member
// the Node-API addon, with the addon's library at its own. Point, which composes
// none, adds nothing; acme.Pin, which no type composes, needs no crate.
func TestBuildAssemblesTheCoreTheWorkQueueAndTheExtensionsCrates(t *testing.T) {
	reg := extensions(t)
	_, opts := project(t, reg, true)
	crate := build(t, schema(ir.SchemaKindDB, []string{"Workflow", "acme.Rating", "Lease", "desk.Triage"}, []string{"Comments", "acme.Flag", "acme.Rating"}), opts)
	if want := []string{"Comments", "Lease", "Workflow", "acme.Flag", "acme.Rating", "desk.Triage"}; !reflect.DeepEqual(crate.Composed, want) {
		t.Errorf("Composed = %v, want %v", crate.Composed, want)
	}
	if crate.Name != "schemas-shop-engine" || crate.Ident != "schemas_shop_engine" {
		t.Errorf("Name, Ident = %q, %q", crate.Name, crate.Ident)
	}
	files := checkGoldens(t, "db", crate)
	for _, want := range []string{
		`superschematic-engine = { path = "../../../../../runtime/engine/rust/engine", default-features = false }`,
		`superschematic-engine-workqueue = { path = "../../../../../runtime/engine-workqueue/rust", default-features = false }`,
		`acme-behaviors = { path = "../../../../../crates/behaviors" }`,
		`desk-triage = { path = "../../../../../vendor/desk" }`,
		"[workspace]\nresolver = \"2\"\nmembers = [\"ffi\", \"napi\"]",
	} {
		if !strings.Contains(string(files[projectengine.ManifestFile]), want+"\n") {
			t.Errorf("Cargo.toml lacks %s", want)
		}
	}
	for _, want := range []string{
		`name = "schemas-shop-engine-ffi"`,
		`crate-type = ["staticlib", "rlib"]`,
		`schemas-shop-engine = { path = "..", default-features = false }`,
		`superschematic-engine-abi = { path = "../../../../../../runtime/engine/rust/abi", default-features = false }`,
	} {
		if !strings.Contains(string(files[projectengine.FFIManifestFile]), want+"\n") {
			t.Errorf("ffi/Cargo.toml lacks %s", want)
		}
	}
	if want := "\nuse schemas_shop_engine as project;\n\nsuperschematic_engine_abi::export_engine_c_abi!(behaviors = project::behaviors);\n"; !strings.Contains(string(files[projectengine.FFILibFile]), want) {
		t.Errorf("ffi/src/lib.rs exports no C ABI over the crate's behaviors:\n%s", files[projectengine.FFILibFile])
	}
	for _, want := range []string{
		`name = "schemas-shop-engine-napi"`,
		`crate-type = ["cdylib"]`,
		`schemas-shop-engine = { path = "..", default-features = false }`,
		`superschematic-engine-napi = { path = "../../../../../../runtime/engine/rust/napi", default-features = false }`,
		`napi-build = "=2.5.0"`,
	} {
		if !strings.Contains(string(files[projectengine.NapiManifestFile]), want+"\n") {
			t.Errorf("napi/Cargo.toml lacks %s", want)
		}
	}
	if want := "\nuse schemas_shop_engine as project;\n\nsuperschematic_engine_napi::export_engine_napi!(behaviors = project::behaviors);\n"; !strings.Contains(string(files[projectengine.NapiLibFile]), want) {
		t.Errorf("napi/src/lib.rs exports no addon over the crate's behaviors:\n%s", files[projectengine.NapiLibFile])
	}
	if !strings.Contains(string(files[projectengine.NapiBuildFile]), "\n    napi_build::setup();\n") {
		t.Errorf("napi/build.rs runs no napi_build::setup():\n%s", files[projectengine.NapiBuildFile])
	}
	for _, file := range []string{projectengine.LibFile, projectengine.FFILibFile, projectengine.NapiLibFile, projectengine.NapiBuildFile} {
		rustfmtCheck(t, files[file])
	}
}

// A General schema the engine runs as it is written composes acme.Rating
// alone: no work queue, and with no [paths] checkout of the engine's crates
// the crate and its ffi member name their version.
func TestBuildOfAGeneralSchemaNamesTheEnginesVersionWithoutPaths(t *testing.T) {
	reg := extensions(t)
	_, opts := project(t, reg, false)
	crate := build(t, schema(ir.SchemaKindGeneral, []string{"acme.Rating"}, nil), opts)
	if crate.WorkQueue != nil {
		t.Errorf("WorkQueue = %+v, want none", crate.WorkQueue)
	}
	files := checkGoldens(t, "general", crate)
	if !strings.Contains(string(files[projectengine.ManifestFile]), "\nsuperschematic-engine = { version = \"0.0.0\", default-features = false }\n") {
		t.Errorf("Cargo.toml names no engine version:\n%s", files[projectengine.ManifestFile])
	}
	if !strings.Contains(string(files[projectengine.FFIManifestFile]), "\nsuperschematic-engine-abi = { version = \"0.0.0\", default-features = false }\n") {
		t.Errorf("ffi/Cargo.toml names no C ABI version:\n%s", files[projectengine.FFIManifestFile])
	}
	if !strings.Contains(string(files[projectengine.NapiManifestFile]), "\nsuperschematic-engine-napi = { version = \"0.0.0\", default-features = false }\n") {
		t.Errorf("napi/Cargo.toml names no addon version:\n%s", files[projectengine.NapiManifestFile])
	}
	for _, file := range []string{projectengine.LibFile, projectengine.FFILibFile, projectengine.NapiLibFile, projectengine.NapiBuildFile} {
		rustfmtCheck(t, files[file])
	}
}

// No crate when the schema composes the core's behaviors alone, which the
// release's engine runs, or none; and none, with the reason, when an
// extension behavior it composes names no crate, or names one the project
// does not hold, or the build names no project.
func TestBuildWritesNoCrateItDoesNotNeedOrCannotWrite(t *testing.T) {
	reg := extensions(t)
	_, opts := project(t, reg, true)
	for _, behaviors := range [][]string{nil, {"Workflow", "Lease"}} {
		crate, why, err := projectengine.Build(schema(ir.SchemaKindDB, behaviors, nil), opts)
		if crate != nil || why != "" || err != nil {
			t.Errorf("%v: Build = %v, %q, %v; want no crate and no reason", behaviors, crate, why, err)
		}
	}

	refused := func(opts projectengine.Options, behaviors ...string) string {
		t.Helper()
		crate, why, err := projectengine.Build(schema(ir.SchemaKindDB, behaviors, nil), opts)
		if crate != nil || err != nil {
			t.Fatalf("%v: Build = %v, %v; want no crate", behaviors, crate, err)
		}
		return why
	}
	if got, want := refused(opts, "acme.Rating", "acme.Pin"), "the Rust engine cannot run shop's behaviors yet, so no engine crate is written: acme.Pin (extension acme) names no Rust crate; the TypeScript engine runs them (D58)"; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
	gone := opts
	gone.RepositoryRoot = t.TempDir()
	if got := refused(gone, "desk.Triage"); !strings.Contains(got, "desk.Triage's crate desk-triage is not at vendor/desk in the project ("+filepath.Join(gone.RepositoryRoot, "vendor", "desk")+" holds no Cargo.toml)") {
		t.Errorf("reason = %q", got)
	}
	none := opts
	none.RepositoryRoot = ""
	if got := refused(none, "acme.Rating"); !strings.Contains(got, "acme.Rating's crate acme-behaviors is at crates/behaviors in the project, and the build names no project") {
		t.Errorf("reason = %q", got)
	}

	if _, _, err := projectengine.Build(schema(ir.SchemaKindDB, []string{"acme.Gone"}, nil), opts); err == nil || !strings.Contains(err.Error(), "shop composes behavior acme.Gone, which the registry does not declare") {
		t.Errorf("an undeclared behavior: err = %v", err)
	}
}

// Write puts the crate and its ffi and napi members in its directory,
// replacing the sources an earlier build wrote; RemoveStale takes away a
// crate a build wrote, its members with it, and the directories
// it leaves empty up to the output root, and leaves a directory whose
// Cargo.toml a build did not write.
func TestWriteAndRemoveStale(t *testing.T) {
	reg := extensions(t)
	root, opts := project(t, reg, true)
	crate := build(t, schema(ir.SchemaKindGeneral, []string{"acme.Rating"}, nil), opts)
	var stale []string
	for _, src := range []string{"src", "ffi/src", "napi/src"} {
		path := filepath.Join(opts.Dir, filepath.FromSlash(src), "stale.rs")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		stale = append(stale, path)
	}
	if err := crate.Write(opts.Dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range stale {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("Write left %s: %v", path, err)
		}
	}
	for _, file := range []string{projectengine.ManifestFile, projectengine.LibFile, projectengine.FFIManifestFile, projectengine.FFILibFile, projectengine.NapiManifestFile, projectengine.NapiLibFile, projectengine.NapiBuildFile} {
		if _, err := os.Stat(filepath.Join(opts.Dir, filepath.FromSlash(file))); err != nil {
			t.Error(err)
		}
	}

	outputRoot := filepath.Join(root, "schemas", "dist")
	if err := projectengine.RemoveStale(opts.Dir, outputRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outputRoot, "engine")); !os.IsNotExist(err) {
		t.Errorf("RemoveStale left engine/: %v", err)
	}
	if _, err := os.Stat(outputRoot); err != nil {
		t.Errorf("RemoveStale removed the output root: %v", err)
	}

	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(opts.Dir, projectengine.ManifestFile)
	if err := os.WriteFile(own, []byte("[package]\nname = \"mine\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := projectengine.RemoveStale(opts.Dir, outputRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Errorf("RemoveStale removed a Cargo.toml a build did not write: %v", err)
	}
}

// rustfmtCheck holds lib.rs to the layout rustfmt gives it, when rustfmt
// is installed.
func rustfmtCheck(t *testing.T, lib []byte) {
	t.Helper()
	rustfmt, err := exec.LookPath("rustfmt")
	if err != nil {
		return
	}
	path := filepath.Join(t.TempDir(), "lib.rs")
	if err := os.WriteFile(path, lib, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(rustfmt, "--edition", "2021", "--check", path).CombinedOutput(); err != nil {
		t.Errorf("rustfmt lays out lib.rs otherwise: %v\n%s", err, out)
	}
}
