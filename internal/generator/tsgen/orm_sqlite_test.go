package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// resultsCases are the cases results.test.ts runs, by name.
var resultsCases = []string{
	"every call of the script answers on bun:sqlite, node:sqlite and D1 as on Postgres",
	"a transaction keeps its writes, rolls them back when it throws, and nests a savepoint, on bun:sqlite",
	"two transactions on one file take turns, and a write outside one waits for it, on bun:sqlite",
	"upsertMany is not built on SQLite, and says so, on bun:sqlite",
	"a transaction keeps its writes, rolls them back when it throws, and nests a savepoint, on node:sqlite",
	"two transactions on one file take turns, and a write outside one waits for it, on node:sqlite",
	"upsertMany is not built on SQLite, and says so, on node:sqlite",
	"on D1 a transaction is refused, and createMany and an upsert each go as one batch, which rolls back whole",
}

// TestTypeScriptRepositoryResultsOnSQLiteAndPostgres holds the TypeScript
// repositories on SQLite and D1 to their results on Postgres, call for
// call (D116), as TestRepositoryResultsOnSQLiteAndPostgres holds the Rust
// repositories: it generates fixture-ledger-db's TypeScript types with
// both dialects' statements, type-checks them (buildTSPackages), applies
// the SQLite DDL the build writes (sqlmigrate) to a file of each target
// and the Postgres DDL to a schema of its own, and runs
// testdata/orm/results.test.ts with bun: one script of calls, every method
// of a repository and each kind of refusal among them, through
// createSqliteOrm over bun:sqlite and node:sqlite, createD1Orm over the D1
// stand-in, and createOrm on Postgres. Without
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL the three SQLite runs are held
// to one another, and the test says so. It also runs what is SQLite's and
// D1's own: a file's transactions, which take turns on its one
// connection, and on D1 none, with a write of several statements as one
// batch. It skips without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS
// says the gate must run.
func TestTypeScriptRepositoryResultsOnSQLiteAndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the TypeScript repositories' result comparison in -short mode")
	}
	const fixture = "fixture-ledger-db"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: fixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("the SQLite model of %s: %v", fixture, err)
	}
	sqliteDDL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, fixture),
		"create.sqlite.sql": sqliteDDL,
	}
	for _, name := range []string{"harness.ts", "results.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{name: fixture, schema: schema, sqlite: true}})
	dir := filepath.Join(typesRoot, fixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "results.junit.xml")
	run := exec.Command(bunPath, "test", "./results.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the results test failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range resultsCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Fatalf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Fatalf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(resultsCases) {
		t.Fatalf("bun ran %d cases of results.test.ts, and the test names %d:\n%s", len(cases), len(resultsCases), junit)
	}
	alike := regexp.MustCompile(`(SQLite, D1 and Postgres|bun:sqlite, node:sqlite and D1 \(no Postgres\)) answered \d+ calls alike, \d+ of them refusals`).Find(out)
	if alike == nil {
		t.Fatalf("the comparison did not run:\n%s", out)
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is unset: bun:sqlite, node:sqlite and D1 were held to one another alone")
	}
	t.Logf("%s", alike)
}

// TestOrmOnSQLiteCarriesEachTablesStatements: a DB service that lists the
// sqlite dialect gives each table of orm.ts its statements on SQLite,
// sqlutil's, beside Postgres's, and the clauses and JSON conditions there
// (D116); a table SQLite holds no repository of carries why, which orm.ts's
// comment names; and one that lists no sqlite carries none of them.
func TestOrmOnSQLiteCarriesEachTablesStatements(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-ledger-db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-ledger-db", SQLite: true})
	if err != nil {
		t.Fatal(err)
	}
	orm := output.ORM
	if orm == nil || !orm.SQLite || !orm.Postgres {
		t.Fatalf("orm = %+v, want both dialects", orm)
	}
	tables, err := sqlutil.TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range orm.Tables {
		if table.SQLite == nil || table.SQLite.Unsupported != "" {
			t.Fatalf("%s on SQLite = %+v, want its statements", table.Type, table.SQLite)
		}
		var model *sqlutil.TableModel
		for i := range tables {
			if tables[i].Type == table.Type {
				model = &tables[i]
			}
		}
		want := map[string]string{}
		for _, st := range sqlutil.SQLiteTableStatementsOf(model).AllStatements() {
			want[st.Name] = st.SQL
		}
		for _, st := range table.SQLite.Statements {
			if st.SQL != want[st.Name] {
				t.Errorf("%s's %s on SQLite = %q, want sqlutil's %q", table.Type, st.Name, st.SQL, want[st.Name])
			}
		}
		if len(table.SQLite.UniqueKeys) != len(table.UniqueKeys) {
			t.Errorf("%s has %d unique keys on SQLite and %d on Postgres", table.Type, len(table.SQLite.UniqueKeys), len(table.UniqueKeys))
		}
		for _, k := range table.SQLite.UniqueKeys {
			if k.Rows != "" && !strings.Contains(k.Rows, "IN (VALUES %s)") {
				t.Errorf("%s's key %s reads its rows on SQLite with %q, want a VALUES list", table.Type, k.Name, k.Rows)
			}
		}
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, ORMFile))
	if err != nil {
		t.Fatal(err)
	}
	text := string(written)
	for _, want := range []string{"export function createSqliteOrm(db: SqlDatabase", "export function createD1Orm(db: SqlDatabase", "jsonClauses: {", "from '@superschematic/http-runtime/orm-sqlite'"} {
		if !strings.Contains(text, want) {
			t.Errorf("orm.ts holds no %q", want)
		}
	}

	// A DB service that lists no sqlite carries none of it.
	output, err = Generate(schema, Options{SchemaName: "fixture-ledger-db"})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range output.ORM.Tables {
		if table.SQLite != nil {
			t.Errorf("%s carries statements on SQLite for a DB service that lists no sqlite", table.Type)
		}
	}
}
