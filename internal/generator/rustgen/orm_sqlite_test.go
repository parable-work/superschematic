package rustgen

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/orm/ledger_sqlite.rs
var ledgerSQLiteTest string

//go:embed testdata/orm/pantry_sqlite.rs
var pantrySQLiteTest string

// TestRepositoriesOnSQLite generates the Rust types of fixture-ledger-db
// and fixture-optimistic-db with their repositories on SQLite (D116),
// builds each database from the SQLite DDL the build writes for it, and
// runs the repositories over it: every method, a page across the nulls of
// a column that may be null in both directions, a negated filter that
// keeps a null (D112), the acting user's stamps (D87), a soft delete, and
// a write fenced on _version.
//
// It needs cargo and the crates the HTTP runtime's lockfile names, which
// it skips without, and runs outside -short.
func TestRepositoriesOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the repositories' cargo test in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the repositories' test on SQLite")
	}
	for _, c := range []struct{ fixture, name, source string }{
		{"fixture-ledger-db", "ledger", ledgerSQLiteTest},
		{"fixture-optimistic-db", "pantry", pantrySQLiteTest},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			runRepositoriesOnSQLite(t, cargo, c.fixture, c.name, c.source)
		})
	}
}

// runRepositoriesOnSQLite writes fixture's types crate with its sqlite
// feature, the database's SQLite DDL beside a test crate over both, and
// runs the test source as the crate's integration test.
func runRepositoriesOnSQLite(t *testing.T, cargo, fixture, name, source string) {
	paths := testpaths.Local(t)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	typesDir := filepath.Join(root, "types", "rust", fixture)
	output, err := Generate(schema, Options{SchemaName: fixture, SQLite: true})
	if err != nil {
		t.Fatalf("generate rust types: %v", err)
	}
	if output.ORM == nil || output.ORM.SQLite == "" {
		t.Fatalf("%s's types carry no repositories on SQLite", fixture)
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
	ddl, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	crateDir := filepath.Join(root, "server", "orm-sqlite")
	if err := os.MkdirAll(filepath.Join(crateDir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(crateDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	manifest := fmt.Sprintf(ormSQLiteManifest, output.CrateName, filepath.ToSlash(typesDir))
	for file, content := range map[string]string{
		"Cargo.toml":            manifest,
		"Cargo.lock":            string(lock),
		"src/lib.rs":            "//! The repositories' test on SQLite: see tests/.\n",
		"create.sql":            ddl,
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
	test := exec.Command(cargo, "test", "--quiet")
	test.Dir = crateDir
	test.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	out, err := test.CombinedOutput()
	if err != nil {
		t.Fatalf("cargo test in %s: %v\n%s", crateDir, err, out)
	}
	t.Logf("%s", out)
}

// ormSQLiteManifest is the test crate's Cargo.toml, given the types crate's
// name and its directory.
const ormSQLiteManifest = `# The repositories' test on SQLite (D116), over a DB's generated Rust types.
[package]
name = "rustgen-orm-sqlite"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
serde_json = "1.0.149"
db = { package = %q, path = %q, features = ["sqlite"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
