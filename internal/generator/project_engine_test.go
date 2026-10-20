package generator

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// acmeRating registers acme.Rating as the acme-schematic example does: its
// declaration, the Rust crate at crates/behaviors in the project, and the
// confirm policy its rate operation names a value of. repo is the
// checkout, which holds the example.
type acmeRating struct{ repo string }

func (acmeRating) Name() string { return "acme" }

func (a acmeRating) Register(r *registry.Registry) error {
	declaration, err := os.ReadFile(filepath.Join(a.repo, "examples", "acme-schematic", "ext", "rating.behavior.json"))
	if err != nil {
		return err
	}
	return errors.Join(
		r.RegisterBehavior(registry.BehaviorSpec{
			Extension:   "acme",
			Rust:        registry.BehaviorRust{Crate: "acme-behaviors", Path: "crates/behaviors", Register: "acme_behaviors::Rating"},
			Declaration: json.RawMessage(declaration),
		}),
		r.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{Extension: "acme", Key: "confirm", Values: []string{"never", "always"}, Default: "never"}),
	)
}

func acmeRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.Use(acmeRating{repo: testpaths.RepoRoot(t)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	return reg
}

// ratings is shop-ratings: a General schema whose Product composes the
// behaviors given.
func ratings(behaviors ...string) (*ir.Schema, *schemaconfig.SchemaConfig) {
	product := &ir.TypeDef{Name: "Product", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "sku", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	for _, name := range behaviors {
		product.Behaviors = append(product.Behaviors, ir.BehaviorRef{Name: name})
	}
	schema := &ir.Schema{Name: "shop-ratings", Kind: ir.SchemaKindGeneral, Types: map[string]*ir.TypeDef{"Product": product}}
	return schema, &schemaconfig.SchemaConfig{Name: "shop-ratings", Kind: ir.SchemaKindGeneral, Outputs: map[string]any{}}
}

// The DB and General pipelines run projectengine: a schema whose types
// compose an extension's behavior gets engine/rust/<schema>, depending on
// the extension's crate at its path in the project and on the engine's at
// the [paths] checkouts, with its ffi member, the archive a Go server
// links, and its napi member, the addon a TypeScript server loads; a later
// build without it removes the crate and
// the directories it leaves empty; a schema whose types compose the core's
// alone, the engine fixture, gets none.
func TestProjectEngineCrateIsWrittenAndRemoved(t *testing.T) {
	reg := acmeRegistry(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "crates", "behaviors"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "crates", "behaviors", "Cargo.toml"), []byte("[package]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(repo, "schemas", "dist")
	paths := naming.LocalPaths{EngineRust: filepath.Join(repo, "engine"), EngineBehaviorsRust: filepath.Join(repo, "behaviors"), EngineABIRust: filepath.Join(repo, "abi"), EngineNapiRust: filepath.Join(repo, "napi")}
	opts := Options{OutputRoot: out, Registry: reg, RepositoryRoot: repo, Paths: paths}

	schema, cfg := ratings("acme.Rating", "Comments")
	result, err := Run(schema, cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	dir := EngineDir(out, LangRust, "shop-ratings")
	if result.Outputs["engine-rust"] != dir {
		t.Fatalf("Outputs[engine-rust] = %q, want %q (outputs %v)", result.Outputs["engine-rust"], dir, result.Outputs)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, projectengine.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nname = \"schemas-shop-ratings-engine\"\n",
		"\nsuperschematic-engine = { path = \"../../../../../engine\", default-features = false }\n",
		"\nacme-behaviors = { path = \"../../../../../crates/behaviors\" }\n",
	} {
		if !strings.Contains(string(manifest), want) {
			t.Errorf("Cargo.toml lacks %q:\n%s", want, manifest)
		}
	}
	lib, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(projectengine.LibFile)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lib), "\n        .with(acme_behaviors::Rating)\n") || !strings.Contains(string(lib), `"key": "confirm"`) {
		t.Errorf("lib.rs registers no acme.Rating under acme's policy:\n%s", lib)
	}
	ffi, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(projectengine.FFILibFile)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ffi), "export_engine_c_abi!(behaviors = project::behaviors);") {
		t.Errorf("ffi/src/lib.rs exports no C ABI over the crate's behaviors:\n%s", ffi)
	}
	napi, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(projectengine.NapiLibFile)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(napi), "export_engine_napi!(behaviors = project::behaviors);") {
		t.Errorf("napi/src/lib.rs exports no addon over the crate's behaviors:\n%s", napi)
	}

	schema, cfg = ratings("Comments")
	result, err = Run(schema, cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Outputs["engine-rust"]; ok {
		t.Errorf("a schema of the core's behaviors wrote %v", result.Outputs)
	}
	if _, err := os.Stat(filepath.Join(out, "engine")); !os.IsNotExist(err) {
		t.Errorf("the stale crate is still there: %v", err)
	}

	root, _, result := buildEngineFixture(t)
	if _, ok := result.Outputs["engine-rust"]; ok {
		t.Errorf("the engine fixture wrote %v", result.Outputs)
	}
	if _, err := os.Stat(EngineDir(root, LangRust, engineFixture)); !os.IsNotExist(err) {
		t.Errorf("the engine fixture has a crate: %v", err)
	}
}

// An extension behavior whose crate the project does not hold leaves the
// schema with no engine that can run it: no crate, and the log says why.
func TestProjectEngineCrateIsNotWrittenWithoutTheExtensionsCrate(t *testing.T) {
	var log strings.Builder
	schema, cfg := ratings("acme.Rating")
	out := t.TempDir()
	result, err := Run(schema, cfg, Options{OutputRoot: out, Registry: acmeRegistry(t), RepositoryRoot: t.TempDir(), Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Outputs["engine-rust"]; ok {
		t.Errorf("wrote %v", result.Outputs)
	}
	if !strings.Contains(log.String(), "  - engine-rust: no engine can run shop-ratings's behaviors, so no engine crate is written and an engine refuses the schema: acme.Rating's crate acme-behaviors is not at crates/behaviors in the project") {
		t.Errorf("the log says nothing of it:\n%s", log.String())
	}
}

// The crate the generator writes for shop-ratings, over acme's own crate
// (examples/acme-schematic/crates/behaviors) and this checkout's engine,
// builds and passes its tests, its ffi member's with them: the engine
// opens on SQLite with behaviors() under tools(), acme's policy, and runs
// acme.Rating, and so does the one the C ABI's archive opens through its
// own eng_open, as a Go server would. Its napi member, the addon, builds
// beside them, with the napi crates a resolve beyond acme's lock file
// picks (acme's smoke loads it from TypeScript). It needs cargo, and
// builds the engine: CARGO_TARGET_DIR shares a build.
func TestProjectEngineCrateBuildsWithAcmeRating(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	acme := filepath.Join(testpaths.RepoRoot(t), "examples", "acme-schematic")
	out := t.TempDir()
	schema, cfg := ratings("acme.Rating", "Workflow", "Lease")
	if _, err := Run(schema, cfg, Options{OutputRoot: out, Registry: acmeRegistry(t), RepositoryRoot: acme, Paths: testpaths.Local(t)}); err != nil {
		t.Fatal(err)
	}
	dir := EngineDir(out, LangRust, "shop-ratings")
	// acme's crate resolved the engine's dependencies; the engine crate
	// takes the same versions.
	lock, err := os.ReadFile(filepath.Join(acme, "crates", "behaviors", "Cargo.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Cargo.lock"), lock, 0o644); err != nil {
		t.Fatal(err)
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(t.TempDir(), "target")
	}
	// cargo test builds no cdylib whose lib has no tests, so the napi
	// member builds first.
	for _, args := range [][]string{{"build", "--workspace", "--quiet"}, {"test", "--workspace", "--quiet"}} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], dir, err, output)
		}
	}
}
