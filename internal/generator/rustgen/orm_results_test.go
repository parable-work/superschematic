package rustgen

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/orm/ledger_results.rs
var ledgerResultsTest string

// TestRepositoryResultsOnSQLiteAndPostgres holds the Rust repositories'
// results on a SQLite file to their results on Postgres, call for call
// (D116): it generates the Rust types of fixture-ledger-db with both
// dialects' repositories, builds the database from the SQLite DDL and from
// the Postgres DDL the build writes, and runs one script of calls through
// each, every method of a repository and each kind of refusal among them.
// Postgres's side is the one ormgen's table parity tests hold to the Go
// ORM and the TypeScript repositories statement for statement, so a call
// that answers alike here answers on SQLite as theirs answer.
//
// A JSON column's eq is left out of the script: SQLite compares its text,
// where Postgres compares the value (D116's status).
//
// It needs cargo and the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, which it skips without, and
// runs outside -short.
func TestRepositoryResultsOnSQLiteAndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the repositories' result comparison in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to hold the repositories' results on SQLite to Postgres's")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the repositories' result comparison")
	}
	const fixture = "fixture-ledger-db"
	paths := testpaths.Local(t)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	root := testpaths.TempDir(t)
	typesDir := filepath.Join(root, "types", "rust", fixture)
	output, err := Generate(schema, Options{SchemaName: fixture, SQLite: true})
	if err != nil {
		t.Fatalf("generate rust types: %v", err)
	}
	if err := SetLocalPaths(output, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, typesDir); err != nil {
		t.Fatal(err)
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
	crateDir := filepath.Join(root, "server", "orm-results")
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
		"Cargo.toml":              fmt.Sprintf(ormResultsManifest, output.CrateName, filepath.ToSlash(typesDir)),
		"Cargo.lock":              string(lock),
		"src/lib.rs":              "//! The repositories' results on SQLite and on Postgres: see tests/.\n",
		"create.sql":              sqliteDDL,
		"tests/ledger_results.rs": ledgerResultsTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(root, "target")
	}
	test := exec.Command(cargo, "test", "--quiet", "--", "--nocapture")
	test.Dir = crateDir
	test.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	out, err := test.CombinedOutput()
	if err != nil {
		t.Fatalf("cargo test in %s: %v\n%s", crateDir, err, out)
	}
	alike := regexp.MustCompile(`SQLite and Postgres answered \d+ calls alike, \d+ of them refusals`).Find(out)
	if alike == nil {
		t.Fatalf("the comparison did not run:\n%s", out)
	}
	t.Logf("%s", alike)
}

// ormResultsManifest is the test crate's Cargo.toml, given the types
// crate's name and its directory.
const ormResultsManifest = `# The repositories' results on SQLite and on Postgres (D116), over a DB's
# generated Rust types.
[package]
name = "rustgen-orm-results"
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
