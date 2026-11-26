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

//go:embed testdata/orm/note_rules.rs
var noteRulesTest string

// TestRepositoriesWriteABehaviorTableThroughTheCore generates the Rust
// types of fixture-workflow-db, whose tables' fields are Workflows, with
// their repositories on both dialects (D103, D116), and runs the
// repositories over the SQLite DDL the build writes and, on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, over its Postgres DDL: a create
// starts each Workflow in its initial state, stamped, at version 1; an
// update writes the row's own fields, moves the version and stamps nothing
// a behavior owns, and one that names an owned field is the core's refusal;
// an update of many rows runs the core's update of each; an upsert reads
// whether its key is held, then runs the core's create or update, and one
// whose update sets no field writes nothing; a Workflow's transition runs
// as an operation of its member, vetoed or forbidden as the core refuses
// it; a delete and a hard delete remove the row; a key no row holds is not
// found; and a client outside a transaction is refused by name on
// Postgres. The types crate binds the core through
// superschematic-rules-adapter, by path.
//
// It needs cargo, which it skips without, and runs outside -short. Without
// the database the Postgres half is skipped, and the test says so.
func TestRepositoriesWriteABehaviorTableThroughTheCore(t *testing.T) {
	runBehaviorRepositories(t, "fixture-workflow-db", "workflow_rules", workflowRulesTest)
}

// TestRepositoriesUpsertABehaviorTableThroughTheCore runs the Rust
// repositories of fixture-approvals-db's Note, whose Constants and
// StateRules guard its updates (D104), on both dialects, the cases
// TestNoteUpsertsOnPostgres holds the Go ORM's upserts to: an upsert of a
// new slug is the core's create; one of a held slug is the core's update,
// so a change of a constant, a move of a one-way field back and a change of
// a draft's frozen body by a caller its rule does not name are refused and
// write nothing, and a caller holding notes.admin may change a constant;
// only if changed, an update of the values a note holds leaves it as it
// was; and an upsert of a deleted note's slug is not found. An update of
// many notes is held to the same guards, a note at a time, and a hard
// delete removes a live note through the core and a deleted one by the
// repository's own statement.
//
// It needs cargo, which it skips without, and runs outside -short. Without
// the database the Postgres half is skipped, and the test says so.
func TestRepositoriesUpsertABehaviorTableThroughTheCore(t *testing.T) {
	runBehaviorRepositories(t, "fixture-approvals-db", "note_rules", noteRulesTest)
}

//go:embed testdata/orm/pool_rules.rs
var poolRulesTest string

// TestRepositoriesTypeEachBehaviorOperation runs fixture-pool-db's
// Account, which implements Pool and Budget, through the typed methods
// src/orm.rs writes per behavior operation (D103, D116), on both dialects:
// the repositories open a struct of each use's operations
// (account_pool(), account_budget()), Pool's pick runs on the table as a
// whole, a command without a row (D104), with its parameters and its
// result typed, and Budget's reserve on a row, refused past its limit.
// TestRepositoriesWriteABehaviorTableThroughTheCore runs a Workflow's
// typed transition.
//
// It needs cargo, which it skips without, and runs outside -short. Without
// the database the Postgres half is skipped, and the test says so.
func TestRepositoriesTypeEachBehaviorOperation(t *testing.T) {
	runBehaviorRepositories(t, "fixture-pool-db", "pool_rules", poolRulesTest)
}

//go:embed testdata/orm/aliased_rules.rs
var aliasedRulesTest string

// TestRepositoriesMergeAnAliasedTable runs fixture-aliased-db's Company,
// whose name is an Aliased field and which implements Mergeable (D147), on
// both dialects: a name another company holds, as a name or an alias, is
// refused with that company's key; a rename keeps the old name as an
// alias, which resolve finds; a removed alias and a deleted company's
// names are freed; and merge_into, on SQLite and on Postgres, moves the
// aliases of the company merged away, its name among them, its names and
// its contacts to the one that stays, deletes it through the core, and
// refuses a merge of a company into itself and one of a company gone.
//
// It needs cargo, which it skips without, and runs outside -short. Without
// the database the Postgres half is skipped, and the test says so.
func TestRepositoriesMergeAnAliasedTable(t *testing.T) {
	runBehaviorRepositories(t, "fixture-aliased-db", "aliased_rules", aliasedRulesTest)
}

//go:embed testdata/orm/merge_rules.rs
var mergeRulesTest string

// TestRepositoriesMergeOnSQLiteAndPostgres runs merge_into of
// fixture-merge-sqlite-db's Label and Customer (D147) on both dialects, the
// tables of fixture-merge-db but LabelSlot, whose index makes nulls
// collide, which SQLite cannot hold: a row a
// unique index would refuse once repointed is deleted, softly where the
// index holds live rows alone, one an index over an expression would
// refuse among them, the rest repointed with who changed them; the row
// merged away is deleted softly, or through the core for a Customer, whose
// status is a Workflow; and a merge into itself, into a row under it and
// of a row gone is refused and writes nothing, and a table that does not
// implement Mergeable has none.
//
// It needs cargo, which it skips without, and runs outside -short. Without
// the database the Postgres half is skipped, and the test says so.
func TestRepositoriesMergeOnSQLiteAndPostgres(t *testing.T) {
	runBehaviorRepositories(t, "fixture-merge-sqlite-db", "merge_rules", mergeRulesTest)
}

// runBehaviorRepositories generates fixture's Rust types with both
// dialects' repositories and the core bound, holds the types crate to
// cargo clippy, and runs source, the test crate's tests/<name>.rs, over the
// SQLite DDL the build writes, at create.sql, and the Postgres DDL, at
// postgres/create.sql.
func runBehaviorRepositories(t *testing.T, fixture, name, source string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the behavior tables' cargo test in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the behavior tables' repositories")
	}
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
		"Cargo.toml":            fmt.Sprintf(ormBehaviorsManifest, output.CrateName, filepath.ToSlash(typesDir), filepath.ToSlash(paths.HTTPRuntimeRust)),
		"Cargo.lock":            string(lock),
		"src/lib.rs":            "//! The behavior tables' repositories: see tests/.\n",
		"create.sql":            sqliteDDL,
		"tests/" + name + ".rs": source,
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
// crate's name, its directory and the HTTP runtime's, whose access caller
// (D156) a case opens the repositories for.
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
superschematic-http-runtime = { path = %q }

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
