package rulescore

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// counterDeclaration is test.Counter's, the behavior a project's class
// declares and testdata/counter implements.
const counterDeclaration = `{
  "name": "test.Counter",
  "description": "A count on the row that bump raises.",
  "fields": [{ "name": "count", "description": "How many bumps.", "type": "number" }],
  "operations": [{
    "name": "bump", "description": "Raises the count by one.",
    "paramsSchema": { "type": "object", "additionalProperties": false },
    "resultSchema": { "type": "object", "properties": { "count": { "type": "integer" } } },
    "writes": true
  }]
}`

// counterRegistry is the core's registry with test.Counter, whose crate is
// testdata/counter.
func counterRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := reg.RegisterBehavior(registry.BehaviorSpec{
		Extension:   "test",
		Rules:       registry.BehaviorRust{Crate: "test-counter-rules", Path: "counter", Register: "test_counter_rules::Counter"},
		Declaration: json.RawMessage(counterDeclaration),
	}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// counters is a DB schema whose Tally implements test.Counter beside a
// Workflow, which the core's crate ships.
func counters(behaviors ...ir.BehaviorRef) *ir.Schema {
	return &ir.Schema{Name: "counters-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{
		"Tally": {Name: "Tally", Role: ir.RoleDBTable, Behaviors: behaviors},
	}}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// options builds the crate in dir over this checkout's core.
func options(t *testing.T, reg *registry.Registry, dir string) Options {
	t.Helper()
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		SchemaName:     "counters-db",
		Registry:       reg,
		Naming:         naming.Default(),
		RepositoryRoot: testdata,
		Paths:          naming.LocalPaths{RulesBehaviorsRust: filepath.Join(repoRoot(t), "runtime", "engine", "rust", "rules-behaviors")},
		Dir:            dir,
		RustVersion:    "1.99.0",
	}
}

// A schema whose tables compose a behavior a class declares with its
// crate gets a rules crate over the core's crates and that crate, which
// registers it beside the core's and declares it through the ABI; one
// whose tables compose the core's alone gets none, the stock core running
// them; and a crate the project does not hold leaves none, saying why.
func TestBuildAssemblesTheCoreAndTheComposedBehaviorsCrates(t *testing.T) {
	reg := counterRegistry(t)
	dir := filepath.Join(t.TempDir(), "rules", "rust", "counters-db")
	crate, why, err := Build(counters(ir.BehaviorRef{Name: "test.Counter", Implements: true}, ir.BehaviorRef{Name: "Workflow", Field: "status"}), options(t, reg, dir))
	if err != nil || crate == nil {
		t.Fatalf("Build = %v, %q, %v", crate, why, err)
	}
	if crate.Name != "schemas-counters-db-rules" || len(crate.Composed) != 1 || crate.Composed[0].Name != "test.Counter" {
		t.Fatalf("crate %s composes %+v", crate.Name, crate.Composed)
	}
	files := crate.Files()
	manifest := string(files[ManifestFile])
	for _, want := range []string{`name = "schemas-counters-db-rules"`, `superschematic-rules-wasm = { path = `, `default-features = false }`, `test-counter-rules = { path = `, "[workspace]"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("Cargo.toml lacks %q:\n%s", want, manifest)
		}
	}
	lib := string(files[LibFile])
	for _, want := range []string{`pub const COMPOSED: [&str; 1] = ["test.Counter"];`, ".with(test_counter_rules::Counter)", `include_str!("../declarations/test.Counter.behavior.json")`, "export_rules_abi!(core = crate::core, declarations = crate::declarations);"} {
		if !strings.Contains(lib, want) {
			t.Errorf("src/lib.rs lacks %q:\n%s", want, lib)
		}
	}
	if declaration := files["declarations/test.Counter.behavior.json"]; !strings.Contains(string(declaration), `"name": "test.Counter"`) {
		t.Errorf("the declaration = %s", declaration)
	}

	if crate, why, err := Build(counters(ir.BehaviorRef{Name: "Workflow", Field: "status"}), options(t, reg, dir)); crate != nil || why != "" || err != nil {
		t.Errorf("a schema of the core's behaviors alone gets %v, %q, %v", crate, why, err)
	}

	missing := options(t, reg, dir)
	missing.RepositoryRoot = t.TempDir()
	if crate, why, err := Build(counters(ir.BehaviorRef{Name: "test.Counter", Implements: true}), missing); crate != nil || err != nil || !strings.Contains(why, "test.Counter's crate test-counter-rules is not at counter in the project") {
		t.Errorf("a crate the project lacks gives %v, %q, %v", crate, why, err)
	}
}

// The crate Build writes builds and passes its tests over this checkout's
// core and testdata/counter: its core registers test.Counter beside the
// core's behaviors and its ABI declares it, and the staticlib is the
// archive scripts/rules-archive.sh --crate stages for the Go binding. It
// needs cargo, and builds the core: CARGO_TARGET_DIR shares a build.
func TestTheRulesCrateBuildsAndRegistersTheComposedBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	reg := counterRegistry(t)
	dir := filepath.Join(t.TempDir(), "rules", "rust", "counters-db")
	crate, why, err := Build(counters(ir.BehaviorRef{Name: "test.Counter", Implements: true}), options(t, reg, dir))
	if err != nil || crate == nil {
		t.Fatalf("Build = %v, %q, %v", crate, why, err)
	}
	if err := crate.Write(dir); err != nil {
		t.Fatal(err)
	}
	// The core's workspace resolved the core's dependencies; the crate
	// takes the same versions.
	lock, err := os.ReadFile(filepath.Join(repoRoot(t), "runtime", "engine", "rust", "Cargo.lock"))
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
	cmd := exec.Command(cargo, "test", "--quiet")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test in %s: %v\n%s", dir, err, output)
	}

	// The Go binding links the crate's archive in the stock one's place:
	// staged by scripts/rules-archive.sh --crate, first in CGO_LDFLAGS,
	// its core declares test.Counter (runtime/rules/go's
	// TestAProjectArchiveDeclaresItsComposedBehaviors).
	archive := filepath.Join(t.TempDir(), "lib")
	stage := exec.Command(filepath.Join(repoRoot(t), "scripts", "rules-archive.sh"), "--crate", dir, "--out", archive)
	stage.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	if output, err := stage.CombinedOutput(); err != nil {
		t.Fatalf("rules-archive.sh --crate %s: %v\n%s", dir, err, output)
	}
	gotest := exec.Command("go", "test", "-count=1", "-run", "TestAProjectArchiveDeclaresItsComposedBehaviors", ".")
	gotest.Dir = filepath.Join(repoRoot(t), "runtime", "rules", "go")
	gotest.Env = append(os.Environ(), "CGO_LDFLAGS=-L"+archive+" "+os.Getenv("CGO_LDFLAGS"), "RULES_COMPOSED=test.Counter")
	if output, err := gotest.CombinedOutput(); err != nil {
		t.Fatalf("go test over the project's archive: %v\n%s", err, output)
	}
}
