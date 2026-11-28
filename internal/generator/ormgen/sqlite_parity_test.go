package ormgen

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/sqlite/ledger_results_test.go
var sqliteLedgerResultsTest string

// sqliteDriverVersion is the modernc.org/sqlite a generated module's test
// opens its file with, the HTTP runtime's: the ORM imports no driver.
const sqliteDriverVersion = "v1.60.1"

// TestRepositoryResultsOfGoOnSQLiteAndPostgres holds the Go repositories'
// results on a SQLite file to their results on Postgres, call for call
// (D116), as TestRepositoryResultsOnSQLiteAndPostgres holds the Rust
// repositories': it generates the ORM of fixture-ledger-db with the
// repositories on SQLite, builds a database from the Postgres DDL and one
// from the SQLite DDL the build writes, and runs one script of calls
// through each one's DatabaseInterface, every method of a repository and
// each kind of refusal among them: reads, field selections and the
// relations they hydrate, every kind of filter and its negations, a JSON
// column's comparisons, pages after a key across a column's nulls,
// writes, upserts that insert, update and leave a row as it was, soft and
// hard deletes, transactions and a savepoint. Postgres's side is the one
// the table parity tests hold to the TypeScript and Rust repositories
// statement for statement.
//
// It needs the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, which
// it skips without, and runs outside -short.
func TestRepositoryResultsOfGoOnSQLiteAndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the repositories' result comparison in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to hold the Go repositories on SQLite to Postgres")
	}
	out := runSQLiteModuleTest(t, tableParityFixture, sqliteLedgerResultsTest, "TestRepositoryResultsOnSQLiteAndPostgres")
	alike := regexp.MustCompile(`SQLite and Postgres answered \d+ calls alike, \d+ of them refusals`).FindString(out)
	if alike == "" {
		t.Fatalf("the comparison did not run:\n%s", out)
	}
	t.Log(alike)
}

//go:embed testdata/sqlite/workflow_results_test.go
var sqliteWorkflowResultsTest string

// TestWorkflowTablesOfGoOnSQLiteAndPostgres holds the Go repositories of
// fixture-workflow-db, whose tables compose a Workflow on their status
// fields, on a SQLite file to Postgres, call for call (D103, D116): each
// create, update and delete and each use's operation is a command of the
// behaviors' core, run on SQLite through the rules module's store over the
// repository's transaction, and answers what it answers on Postgres,
// refusals among them: a key a row holds, a transition the Workflow does
// not list, one without the permission it names, a stale one, and a row
// no key names. Reads, field selections, filters, an UpdateMany and
// transactions over the same tables are in the script.
//
// It needs the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL and the
// rules archive (make rules-archive), and runs outside -short.
func TestWorkflowTablesOfGoOnSQLiteAndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the workflow tables' result comparison in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to hold the workflow tables on SQLite to Postgres")
	}
	paths := testpaths.Local(t)
	ormDir := generateBehaviorORMModule(t, paths, workflowFixture, func(o *Options) { o.SQLite = true })
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, workflowFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", workflowFixture, err)
	}
	out := runSQLiteTestIn(t, ormDir, schema, workflowFixture, sqliteWorkflowResultsTest, "TestWorkflowTablesOnSQLiteAndPostgres")
	alike := regexp.MustCompile(`SQLite and Postgres answered \d+ calls alike, \d+ of them refusals`).FindString(out)
	if alike == "" {
		t.Fatalf("the comparison did not run:\n%s", out)
	}
	t.Log(alike)
}

// runSQLiteModuleTest generates fixture's ORM with its repositories on
// SQLite beside its Postgres DDL, writes the SQLite DDL the build writes
// at testdata/sqlite.sql and source as a test of the module, which opens
// its file with modernc.org/sqlite, and runs the test named test. It
// answers the test's output, and fails when the test does.
func runSQLiteModuleTest(t *testing.T, fixture, source, test string) string {
	t.Helper()
	ormDir := generateORMModuleAt(t, filepath.Join(fixturesDir, fixture), fixture, func(o *Options) { o.SQLite = true })
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	return runSQLiteTestIn(t, ormDir, schema, fixture, source, test)
}

// runSQLiteTestIn is runSQLiteModuleTest in the ORM module at ormDir, of
// schema.
func runSQLiteTestIn(t *testing.T, ormDir string, schema *ir.Schema, fixture, source, test string) string {
	t.Helper()
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: fixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("the SQLite model of %s: %v", fixture, err)
	}
	ddl, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "testdata", "sqlite.sql"), []byte(ddl), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "sqlite_results_test.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	// The searches every runtime runs (#740), which search's script reads.
	parity, err := os.ReadFile(filepath.Join("..", "..", "..", "runtime", "http", "testdata", "search_tables_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "testdata", "search_tables_parity.json"), parity, 0o644); err != nil {
		t.Fatal(err)
	}
	require := exec.Command("go", "mod", "edit", "-require=modernc.org/sqlite@"+sqliteDriverVersion)
	require.Dir = ormDir
	if out, err := require.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit: %v\n%s", err, out)
	}
	tidyGeneratedModule(t, ormDir)
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "^"+test+"$", ".")
	cmd.Dir = ormDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test in the generated ORM module: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: "+test+" ") {
		t.Fatalf("%s did not pass:\n%s", test, out)
	}
	return string(out)
}
