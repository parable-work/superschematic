package rustgen

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/orm/workflow_rules.rs
var workflowRulesTest string

// TestRepositoriesWriteABehaviorTableThroughTheCore generates the Rust
// types of fixture-workflow-db, whose tables' fields are Workflows, with
// their repositories on both dialects (D103, D116), and runs the
// repositories over the SQLite DDL the build writes and, on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, over its Postgres DDL: a create
// starts each Workflow in its initial state, stamped, at version 1; an
// update writes the row's own fields, moves the version and stamps nothing
// a behavior owns, and one that names an owned field is the core's refusal;
// a Workflow's transition runs as an operation of its member, vetoed or
// forbidden as the core refuses it; a delete removes the row; a key no row
// holds is not found; and a client outside a transaction is refused by
// name on Postgres. The types crate
// binds the core through superschematic-rules-adapter, by path.
//
// It needs cargo, which it skips without, and runs outside -short. Without
// the database the Postgres half is skipped, and the test says so.
func TestRepositoriesWriteABehaviorTableThroughTheCore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the behavior tables' cargo test in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the behavior tables' repositories")
	}
	const fixture = "fixture-workflow-db"
	paths := testpaths.Local(t)
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	root := testpaths.TempDir(t)
	typesDir := filepath.Join(root, "types", "rust", fixture)
	output, err := Generate(schema, Options{SchemaName: fixture, SQLite: true, Registry: reg})
	if err != nil {
		t.Fatalf("generate rust types: %v", err)
	}
	if output.ORM == nil || !output.ORM.Rules {
		t.Fatalf("%s's repositories write no table through the core", fixture)
	}
	if err := SetLocalPaths(output, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if !output.BindsRules() {
		t.Fatal("the types crate binds no core, with both of the behaviors' crates' paths set")
	}
	if err := WriteTypes(output, typesDir); err != nil {
		t.Fatal(err)
	}
	orm, err := os.ReadFile(filepath.Join(typesDir, "src", ORMFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub const RULES_LAYOUT: &str = ",
		"impl orm::sqlite::Rules for Rules {",
		"impl<C: CoreClient> orm::postgres::Rules<C> for Rules {",
	} {
		if !strings.Contains(string(orm), want) {
			t.Errorf("src/%s lacks %q", ORMFile, want)
		}
	}

	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: fixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("the SQLite model of %s: %v", fixture, err)
	}
	sqliteDDL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: fixture})
	if err != nil {
		t.Fatalf("generate the Postgres DDL: %v", err)
	}
	crateDir := filepath.Join(root, "server", "orm-behaviors")
	for _, dir := range []string{"src", "tests", "postgres"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := sqlgen.WriteDDL(ddl, filepath.Join(crateDir, "postgres")); err != nil {
		t.Fatalf("write the Postgres DDL: %v", err)
	}
	postgresDDL := filepath.Join(crateDir, "postgres", "create.sql")
	pgtest.WriteCreateSQL(t, postgresDDL, postgresDDL)
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for file, content := range map[string]string{
		"Cargo.toml":              fmt.Sprintf(ormBehaviorsManifest, output.CrateName, filepath.ToSlash(typesDir)),
		"Cargo.lock":              string(lock),
		"src/lib.rs":              "//! The behavior tables' repositories: see tests/.\n",
		"create.sql":              sqliteDDL,
		"tests/workflow_rules.rs": workflowRulesTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(root, "target")
	}
	// The types crate's own lints first, with the core bound on both
	// dialects, then the cases.
	clippy := exec.Command(cargo, "clippy", "--quiet", "--manifest-path", filepath.Join(typesDir, "Cargo.toml"), "--features", "postgres,sqlite", "--", "-D", "warnings")
	clippy.Dir = crateDir
	clippy.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	if out, err := clippy.CombinedOutput(); err != nil {
		t.Fatalf("cargo clippy of %s: %v\n%s", typesDir, err, out)
	}
	test := exec.Command(cargo, "test", "--quiet", "--", "--nocapture")
	test.Dir = crateDir
	test.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	out, err := test.CombinedOutput()
	if err != nil {
		t.Fatalf("cargo test in %s: %v\n%s", crateDir, err, out)
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Log("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is unset: the Postgres half did not run")
	} else if !strings.Contains(string(out), "the repositories ran on Postgres") {
		t.Errorf("the Postgres half did not run:\n%s", out)
	}
	t.Logf("%s", out)
}

// ormBehaviorsManifest is the test crate's Cargo.toml, given the types
// crate's name and its directory.
const ormBehaviorsManifest = `# The repositories of a database whose tables compose behaviors (D103,
# D116), over its generated Rust types, on both dialects.
[package]
name = "rustgen-orm-behaviors"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
serde = "1.0.228"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt"] }
db = { package = %q, path = %q, features = ["postgres", "sqlite"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`

// TestRepositoriesBindNoCoreWithoutTheBehaviorsCrates: a build whose
// naming file names neither of the unpublished crates the binding depends
// on writes a types crate that depends on neither and binds no core, and
// its src/orm.rs says which tables' writes are then refused, and why.
func TestRepositoriesBindNoCoreWithoutTheBehaviorsCrates(t *testing.T) {
	const fixture = "fixture-workflow-db"
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	output, err := Generate(schema, Options{SchemaName: fixture, SQLite: true, Registry: reg})
	if err != nil {
		t.Fatalf("generate rust types: %v", err)
	}
	dir := t.TempDir()
	if err := SetLocalPaths(output, naming.LocalPaths{}, dir); err != nil {
		t.Fatal(err)
	}
	if output.BindsRules() {
		t.Fatal("the types crate binds a core with neither of the behaviors' crates' paths set")
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	read := func(file string) string {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	manifest, orm := read("Cargo.toml"), read(filepath.Join("src", ORMFile))
	for _, crate := range []string{RulesAdapterCrate, RulesBehaviorsCrate} {
		if strings.Contains(manifest, crate) {
			t.Errorf("Cargo.toml depends on %s, which no path names", crate)
		}
	}
	for _, want := range []string{
		"//! Order and Shipment compose behaviors (D103), whose writes go",
		"`[paths] rules_adapter_rust` and `rules_behaviors_rust`",
	} {
		if !strings.Contains(orm, want) {
			t.Errorf("src/%s lacks %q", ORMFile, want)
		}
	}
	for _, unwanted := range []string{"RULES_LAYOUT", "pub struct Rules;"} {
		if strings.Contains(orm, unwanted) {
			t.Errorf("src/%s holds %q, and binds no core", ORMFile, unwanted)
		}
	}
}
