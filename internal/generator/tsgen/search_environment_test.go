package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// One search index per environment (#740): a source database whose index
// rows its worker copies into the environment's index, and the search
// database that holds that index.
const (
	searchEnvironmentSource = "fixture-search-env-db"
	searchEnvironmentIndex  = "fixture-search-index-db"
)

// searchEnvironmentCases are the cases search_environment.test.ts runs.
var searchEnvironmentCases = []string{
	"one search ranks the rows of both databases, and two principals each get exactly their rows, with right totals",
	"a party's grant in one database reaches its rows there alone, and once revoked they stop showing after that database's worker drains",
	"a delete, a soft delete and an audience change leave the index once the worker drains, and a late write never overwrites a newer one",
	"the parity file's searches answer the same titles over the environment's index as over a database's (#740)",
}

// TestTheSearchDatabaseExportsItsIndex: the search database's orm.ts
// exports environmentSearch, its index's table and words in its
// configuration, and a source's does not.
func TestTheSearchDatabaseExportsItsIndex(t *testing.T) {
	for name, want := range map[string]string{
		searchEnvironmentIndex:  `export const environmentSearch = {"index":"environment_search_index","document":"(setweight(to_tsvector('search_unaccent'::regconfig`,
		searchEnvironmentSource: "",
	} {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		out, err := Generate(schema, Options{SchemaName: name})
		if err != nil {
			t.Fatalf("generate %s: %v", name, err)
		}
		dir := t.TempDir()
		if err := WriteTypes(out, dir); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		text, err := os.ReadFile(filepath.Join(dir, "orm.ts"))
		if err != nil {
			t.Fatalf("read %s's orm.ts: %v", name, err)
		}
		orm := string(text)
		switch {
		case want == "" && strings.Contains(orm, "environmentSearch"):
			t.Errorf("%s's orm.ts exports environmentSearch, and it holds no environment index", name)
		case want != "" && !strings.Contains(orm, want):
			t.Errorf("%s's orm.ts does not export its environment index as %s", name, want)
		}
		if name == searchEnvironmentSource && !schema.SearchesEnvironment() {
			t.Errorf("%s does not search through its environment's index", name)
		}
		if name == searchEnvironmentIndex && schema.EnvironmentSearchIndex() == nil {
			t.Errorf("%s holds no %s", name, ir.EnvironmentSearchIndexTypeName)
		}
	}
}

// TestEnvironmentSearchEndToEnd generates the source's and the search
// database's TypeScript packages, applies each create.sql in schemas of
// their own at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, two of the
// source's, and runs testdata/orm/search_environment.test.ts with bun:
// every case of searchEnvironmentCases must pass. It skips without the
// database.
func TestEnvironmentSearchEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the environment's search end to end in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the environment's search against Postgres")
	}
	var cases []tsPackageCase
	sql := map[string]string{}
	for _, name := range []string{searchEnvironmentSource, searchEnvironmentIndex} {
		reg := registry.New(naming.Default())
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		sql[name] = createSQLOf(t, schema, name)
		cases = append(cases, tsPackageCase{name: name, schema: schema, registry: reg})
	}
	typesRoot, bunPath := buildTSPackages(t, cases)
	for name, text := range sql {
		if err := os.WriteFile(filepath.Join(typesRoot, name, "create.sql"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(typesRoot, searchEnvironmentSource)
	files := searchParityFiles(t)
	source, err := ormTests.ReadFile("testdata/orm/search_environment.test.ts")
	if err != nil {
		t.Fatal(err)
	}
	files["search_environment.test.ts"] = string(source)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "search_environment.junit.xml")
	run := exec.Command(bunPath, "test", "./search_environment.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the end-to-end test failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	ran, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range searchEnvironmentCases {
		tc, ok := ran[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(ran) != len(searchEnvironmentCases) {
		t.Errorf("bun ran %d cases, and searchEnvironmentCases names %d:\n%s", len(ran), len(searchEnvironmentCases), junit)
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
