package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// searchSQLiteCheck is the file tsc checks the fixture's package against
// on SQLite (D186): a search over the ./orm/sqlite entry's ORM, and the
// indexing its queue's worker runs, from the runtime's ./d1 entry.
const searchSQLiteCheck = `import { indexSearch, searchTables, type SearchResult, type SqlDatabase } from '@superschematic/http-runtime/d1';
import { createOrm } from './orm-sqlite';

export async function find(db: SqlDatabase, q: string): Promise<SearchResult> {
  const orm = createOrm(db);
  await indexSearch(orm, { sourceTable: 'Note', sourceKey: '00000000-0000-4000-8000-000000000001' });
  return searchTables(orm, null, { q, tables: ['Note', 'Unit'], scope: 'p1' });
}
`

// searchSQLiteCases are the cases search_sqlite.test.ts runs: D155's
// "done when" in the generated repositories on SQLite, and the refusal of
// embeddings there. The file processor's case is search.test.ts's alone:
// its pass is the runtime's, the same on every dialect, and its own test
// covers it (file-processing.test.ts).
var searchSQLiteCases = append(searchCasesWithout(searchFileProcessorCase, searchCases[len(searchCases)-1]),
	"embeddings are Postgres's alone: a search or an indexing on SQLite that asks for them is refused, and reindexSearch builds every row again")

// searchCasesWithout returns searchCases less the cases named.
func searchCasesWithout(names ...string) []string {
	out := make([]string, 0, len(searchCases))
	for _, name := range searchCases {
		if !slices.Contains(names, name) {
			out = append(out, name)
		}
	}
	return out
}

// TestSearchTablesEndToEndOnSQLite generates fixture-search-db's
// TypeScript package with its ./orm/sqlite entry, type-checks it against
// searchSQLiteCheck, writes the database's sqlite/create.sql beside it and
// runs testdata/orm/search_sqlite.test.ts with bun over openSqlite's
// connection to a file it makes (D186): every case of searchSQLiteCases
// must pass. It needs no database server.
func TestSearchTablesEndToEndOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check on SQLite in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, searchFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", searchFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: searchFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("build %s's SQLite model: %v", searchFixture, err)
	}
	createSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatalf("write %s's SQLite create.sql: %v", searchFixture, err)
	}
	files := map[string]string{"sqlite/create.sql": createSQL}
	for _, name := range []string{"harness.ts", "search_sqlite.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     searchFixture,
		schema:   schema,
		registry: reg,
		sqlite:   true,
		extra:    map[string]string{"search-sqlite.check.ts": searchSQLiteCheck},
	}})
	dir := filepath.Join(typesRoot, searchFixture)
	for name, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "search-sqlite.junit.xml")
	run := exec.Command(bunPath, "test", "./search_sqlite.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the end-to-end test on SQLite failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range searchSQLiteCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(searchSQLiteCases) {
		t.Errorf("bun ran %d cases, and searchSQLiteCases names %d:\n%s", len(cases), len(searchSQLiteCases), junit)
	}
	t.Logf("bun ran the end-to-end test on SQLite:\n%s", out)
}
