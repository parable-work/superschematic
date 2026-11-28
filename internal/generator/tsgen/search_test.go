package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// searchFixture is the DB service whose tables share the search index
// (D155): folders and documents with an audience, contracts with an
// encrypted field, units with a conversation and files, requests and notes
// with a scope, notes soft-deleting, and the grants a party reaches rows
// through.
const searchFixture = "fixture-search-db"

// searchCheck is the file tsc checks the fixture's package against: a
// search over the generated ORM, and the indexing its queue's worker runs.
const searchCheck = `import type { FixtureSearchDbOrm } from './orm';
import { indexSearch, searchTables, type SearchResult } from '@superschematic/http-runtime/postgres';

export async function find(orm: FixtureSearchDbOrm, q: string): Promise<SearchResult> {
  await indexSearch(orm, { sourceTable: 'Note', sourceKey: '00000000-0000-4000-8000-000000000001' });
  return searchTables(orm, null, { q, tables: ['Note', 'Unit'], scope: 'p1' });
}
`

// searchCases are the cases search.test.ts runs: D155's "done when" in
// the generated repositories.
var searchCases = []string{
	"two principals with different access search the same text and each gets exactly the rows their list reads would show, ranked across at least five tables",
	"a party with a grant finds only the rows its grant reaches, through the route and the MCP tool too, and a revoked grant finds nothing",
	"a row's conversation and files are indexed with it, and an encrypted field never is",
	"a file's text the file processor extracts is indexed with its row (D154, D159, #702)",
	"an index row follows its row: an audience change and a delete at once, a change of text once the queue drains",
	"scope keeps a search to the app's filter",
	"embeddings re-rank the first hits, are kept while the text holds, and can be switched off",
}

// TestSearchTablesEndToEnd generates fixture-search-db's TypeScript
// package, type-checks it against searchCheck, and runs
// testdata/orm/search.test.ts in it with bun over the generated create.sql
// in a schema of its own at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL: every
// case of searchCases must pass. It skips without the database.
func TestSearchTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run search across tables against Postgres")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, searchFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", searchFixture, err)
	}
	files := map[string]string{"create.sql": createSQLOf(t, schema, searchFixture)}
	for _, name := range []string{"harness.ts", "search.test.ts"} {
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
		extra:    map[string]string{"search.check.ts": searchCheck},
	}})
	dir := filepath.Join(typesRoot, searchFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "search.junit.xml")
	run := exec.Command(bunPath, "test", "./search.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the end-to-end test failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range searchCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(searchCases) {
		t.Errorf("bun ran %d cases, and searchCases names %d:\n%s", len(cases), len(searchCases), junit)
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
