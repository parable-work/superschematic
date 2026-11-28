package ormgen

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/sqlite/recurrence_results_test.go
var sqliteRecurrenceResultsTest string

//go:embed testdata/sqlite/calendar_results_test.go
var sqliteCalendarResultsTest string

//go:embed testdata/sqlite/search_results_test.go
var sqliteSearchResultsTest string

// sqliteCrossTablesCase is a generated module's test of the reads across
// tables on SQLite: its fixture, the script and the test it runs, and the
// cases that must pass.
type sqliteCrossTablesCase struct {
	fixture, test, source string
	behaviors             bool
	cases                 []string
}

var sqliteCrossTablesCases = []sqliteCrossTablesCase{
	{
		fixture: "fixture-recurrence-db", test: "TestRecurrenceOnSQLite", source: sqliteRecurrenceResultsTest, behaviors: true,
		cases: []string{
			"a calendar rule plans the core's dates up to the horizon across a change of the clocks, moved after weekends and off the row calendar holidays",
			"a rule change leaves the open occurrence untouched and replans the ones that have not opened",
			"an afterDone recurrence is due at its start, and once completed, its interval after the completion, across a change of the clocks",
			"a relative recurrence is due its offset before each anniversary of its contract's end, as the core expands it",
			"without filings.manage setRecurrence is refused and a rule that is no RRULE is invalid",
			"the calendar merges a contract end date and a filing occurrences, each dated as its source says",
		},
	},
	{
		fixture: "fixture-access-db", test: "TestCalendarOnSQLite", source: sqliteCalendarResultsTest,
		cases: []string{
			"each reader's calendar holds the documents their list reads show, by their audience",
			"a party's calendar holds the documents its grants reach and a scope of mine its own",
			"a feed's token reads its holder's calendar as a busy or a full iCalendar document",
			"a revoked, a rotated and an expired feed is refused, and its token reads nothing else",
		},
	},
	{
		fixture: "fixture-search-db", test: "TestSearchOnSQLite", source: sqliteSearchResultsTest, behaviors: true,
		cases: []string{
			"two principals with different access each find exactly the rows their list reads show, ranked across five tables",
			"a party finds the rows its grant reaches, and no table a grant reaches no row of",
			"a hit's match, its snippet and its place, the words every one required, a page and its total",
			"a row's write is indexed by the queue's worker, and its delete and soft delete leave the index",
			"a conversation's messages and a file's extracted text are indexed with their row",
			"a scope keeps to its rows, and an embedding is refused on SQLite",
			"reindexSearch indexes every row of a table that became searchable",
		},
	},
}

// TestTheGoReadsAcrossTablesOnSQLite holds the Go ORM of a database that
// lists sqlite to Recurrence, the calendar and search across tables on a
// SQLite file (D152, D155, D186, #615): the behaviors' core runs a
// Recurrence's operations in the SQLite repositories' transactions, and
// the SQLite database's CrossTables is a crosstable.DialectHost whose
// statements crosstable writes in SQLite's words, its reads held to the
// caller by the repositories' conditions on SQLite (sqliteAccessConditions)
// and its search's words the FTS5 table the index's triggers keep. Outside
// -short mode each fixture's module runs its script over a SQLite file,
// and with the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL the same
// cases on Postgres, whose answers each script holds equal to SQLite's.
func TestTheGoReadsAcrossTablesOnSQLite(t *testing.T) {
	paths := testpaths.Local(t)
	for _, c := range sqliteCrossTablesCases {
		t.Run(c.fixture, func(t *testing.T) {
			reg := registry.New(naming.Default())
			schema, err := loader.LoadService(filepath.Join(fixturesDir, c.fixture), loader.WithRegistry(reg))
			if err != nil {
				t.Fatal(err)
			}
			output, err := Generate(schema, Options{SchemaName: c.fixture, ModulePath: "example.com/schemas/orm/" + c.fixture, TypesModule: "example.com/schemas/types/go/" + c.fixture, Registry: reg, SQLite: true})
			if err != nil {
				t.Fatal(err)
			}
			if output.CrossTables == nil {
				t.Fatalf("the ORM of %s holds no reads across tables", c.fixture)
			}
			if (output.CrossTables.Searchable != "") != (output.CrossTables.SQLiteSearchable != "") {
				t.Errorf("the search definition of %s on SQLite: %q", c.fixture, output.CrossTables.SQLiteSearchable)
			}
			if testing.Short() {
				return
			}
			var ormDir string
			if c.behaviors {
				ormDir = generateBehaviorORMModule(t, paths, c.fixture, func(o *Options) { o.SQLite = true })
			} else {
				ormDir = generateORMModuleAt(t, filepath.Join(fixturesDir, c.fixture), c.fixture, func(o *Options) { o.SQLite = true })
			}
			text, err := os.ReadFile(filepath.Join(ormDir, "crosstable.go"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"func (db *SQLiteDatabase) CrossTables() crosstable.Host",
				"func (sqliteCrossTablesHost) Dialect() string { return crosstable.DialectSQLite }",
			} {
				if !strings.Contains(string(text), want) {
					t.Errorf("crosstable.go of %s on SQLite holds no %q", c.fixture, want)
				}
			}
			out := runSQLiteTestIn(t, ormDir, schema, c.fixture, c.source, c.test)
			for _, name := range c.cases {
				if want := "--- PASS: " + c.test + "/sqlite/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(out, want) {
					t.Errorf("the generated module's test did not pass %q on SQLite:\n%s", name, out)
				}
			}
			if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" {
				alike := regexp.MustCompile(`SQLite and Postgres answered \d+ cases alike`).FindString(out)
				if alike == "" || !strings.Contains(out, "--- PASS: "+c.test+"/postgres ") {
					t.Fatalf("the cases did not pass alike on Postgres:\n%s", out)
				}
				t.Log(alike)
			}
		})
	}
}
