package tsgen

import (
	"fmt"
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
	"github.com/parable-work/superschematic/internal/testpaths"
)

// tableDefinitionBlock is the block of a template that writes a table's
// definition: from its doc comment to the line that closes the object.
func tableDefinitionBlock(t *testing.T, name string) string {
	t.Helper()
	source, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(source), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "/** The {{.Table}} table") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s writes no table definition", name)
	}
	for end := start; end < len(lines); end++ {
		if lines[end] == "};" {
			return strings.Join(lines[start:end+1], "\n")
		}
	}
	t.Fatalf("%s's table definition does not end", name)
	return ""
}

// TestTheSQLiteDefinitionOfATableIsPostgres: orm-sqlite.ts writes a table's
// definition as orm.ts does, the same members from the same fields, but for
// its doc comment and its audience's condition in SQLite's words (D182), so
// a member a later change adds to one is added to the other.
func TestTheSQLiteDefinitionOfATableIsPostgres(t *testing.T) {
	postgres := tableDefinitionBlock(t, "orm.tmpl")
	sqlite := tableDefinitionBlock(t, "orm_sqlite.tmpl")
	sqlite = strings.Replace(sqlite, "/** The {{.Table}} table on SQLite, as", "/** The {{.Table}} table, as", 1)
	sqlite = strings.Replace(sqlite, ", contains: {{jsonString .AudienceContains}}, placeholder: {{jsonString .AudiencePlaceholder}} }", " }", 1)
	if postgres != sqlite {
		t.Errorf("orm_sqlite.tmpl's table definition is not orm.tmpl's:\n%s\n----\n%s", sqlite, postgres)
	}
}

// resultsCases are the cases results.test.ts runs, by name.
var resultsCases = []string{
	"every call of the script answers on bun:sqlite, node:sqlite and D1 as on Postgres",
	"a transaction keeps its writes, rolls them back when it throws, and nests a savepoint, on bun:sqlite",
	"two transactions on one file take turns, and a write outside one waits for it, on bun:sqlite",
	"an upsertMany of more rows than a statement binds runs as one unit, which rolls back whole, on bun:sqlite",
	"a transaction keeps its writes, rolls them back when it throws, and nests a savepoint, on node:sqlite",
	"two transactions on one file take turns, and a write outside one waits for it, on node:sqlite",
	"an upsertMany of more rows than a statement binds runs as one unit, which rolls back whole, on node:sqlite",
	"on D1 a transaction's statements each commit as they run, and createMany, an upsert and an upsertMany each go as one batch, which rolls back whole",
}

// TestTypeScriptRepositoryResultsOnSQLiteAndPostgres holds the TypeScript
// repositories on SQLite and D1 to their results on Postgres, call for
// call (D116, D182), as TestRepositoryResultsOnSQLiteAndPostgres holds the
// Rust repositories and TestRepositoryResultsOfGoOnSQLiteAndPostgres the Go
// ORM's: it generates fixture-ledger-db's TypeScript types with both
// dialects' statements, type-checks them (buildTSPackages), applies the
// SQLite DDL the build writes (sqlmigrate) to a file of each target and the
// Postgres DDL to a schema of its own, and runs testdata/orm/results.test.ts
// with bun: one script of calls, every method of a repository, its upserts
// of many rows (D89) and each kind of refusal among them, through createOrm
// of the ./orm/sqlite entry over bun:sqlite, node:sqlite and the D1
// stand-in, and createOrm of ./orm on Postgres. Without
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL the three SQLite runs are held to
// one another, and the test says so. It also runs what is SQLite's and D1's
// own: a file's transactions, which take turns on its one connection, and
// on D1 none, with a write of several statements as one batch. It skips
// without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must
// run.
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

// TestTheRuntimesParameterLimitsAreSqlutils: the TypeScript repositories on
// SQLite batch an upsert of many rows by the parameters a statement binds
// on a file and on D1 (D89), which the runtime's orm-sqlite.ts names as
// sqlutil does, so a batch there is the Go and Rust repositories' batch.
func TestTheRuntimesParameterLimitsAreSqlutils(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), "runtime", "http", "typescript", "src", "orm-sqlite.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for name, limit := range map[string]int{"SQLITE_MAX_PARAMETERS": sqlutil.SQLiteMaxParameters, "D1_MAX_PARAMETERS": sqlutil.D1MaxParameters} {
		if want := fmt.Sprintf("export const %s = %d;", name, limit); !strings.Contains(string(source), want) {
			t.Errorf("orm-sqlite.ts holds no %q, sqlutil's limit", want)
		}
	}
}
