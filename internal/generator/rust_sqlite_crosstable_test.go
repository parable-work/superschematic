package generator

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// Recurrence, the calendar and search across tables in the Rust
// repositories and runtime on a SQLite file (D152, D155, D186, #615): the
// build's SQLite DDL applied to a file, and the cases the Postgres tests
// run (TestRustRecurrenceEndToEnd, TestRustAccessEndToEnd's feed,
// TestRustSearchEndToEnd) held to the repositories, orm::calendar::sqlite
// and orm::search_tables::sqlite over it. They need cargo, which they skip
// without; with SUPERSCHEMATIC_RULES_TEST_DATABASE_URL the Recurrence
// cases run on Postgres too and the two answers are held equal.

//go:embed testdata/recurrence_rust_sqlite.rs
var recurrenceRustSQLiteTest string

//go:embed testdata/calendar_rust_sqlite.rs
var calendarRustSQLiteTest string

//go:embed testdata/search_rust_sqlite.rs
var searchRustSQLiteTest string

// recurrenceRustSQLiteCases are the cases testdata/recurrence_rust_sqlite.rs
// runs.
var recurrenceRustSQLiteCases = []string{
	"a calendar rule plans the dates up to the horizon across a change of the clocks, moved after weekends and off the row calendar holidays",
	"a rule change leaves the open occurrence untouched and replans the ones that have not opened",
	"an afterDone recurrence is due at its start, and once completed, its interval after the completion, across a change of the clocks",
	"a relative recurrence is due its offset before each anniversary of its contract's end",
	"without filings.manage setRecurrence is refused and a rule that is no RRULE is invalid",
	"the calendar merges a contract end date and a filing occurrences, each dated as its source says, and its feed text is the same",
	"a recurrence given at create is set in the create, its occurrences planned and its deadlines armed, and held to filings.manage",
	"a holiday calendar whose weekend is Friday and Saturday moves a due date off Saturday to the Sunday",
	"a write of a contract end date writes a change of each filing that names it, which moves nothing of the filing",
}

// calendarRustSQLiteCases are the cases testdata/calendar_rust_sqlite.rs
// runs.
var calendarRustSQLiteCases = []string{
	"each reader's calendar holds exactly the rows its list reads show",
	"a party's grant reads the calendar through its role's paths alone",
	"a feed shows only the rows its token's principal reads, busy or in full, and its token is no credential elsewhere",
	"a rotated, revoked, expired or unknown feed is refused at the next fetch",
}

// searchRustSQLiteCases are the cases testdata/search_rust_sqlite.rs runs.
var searchRustSQLiteCases = []string{
	"two principals with different access search the same text and each gets exactly the rows their list reads would show, ranked across at least five tables",
	"a party with a grant finds only the rows its grant reaches, through the route and the MCP tool too, and a revoked grant finds nothing",
	"a row's conversation and files are indexed with it, and an encrypted field never is",
	"an uploaded file's text the file processor extracts is indexed with its row, and a file the processor fails on ends failed",
	"an index row follows its row: an audience change and a delete at once, a change of text once the queue drains",
	"scope keeps a search to the app's filter",
	"embeddings are refused on SQLite, which ranks by the words",
	searchParityCase,
}

// TestRustRecurrenceOnSQLite builds fixture-recurrence-db's Rust types with
// their sqlite and postgres features and runs testdata/recurrence_rust_sqlite.rs:
// a Filing's Recurrence through the behaviors' core in the SQLite
// repositories' transactions, its three anchors across a change of the
// clocks and two holiday moves, a rule change leaving the open occurrence,
// the refusals, and the calendar merging a date and occurrences, with its
// feed's text. With the Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL
// the same cases run there with the same keys, and their answers are held
// equal to SQLite's.
func TestRustRecurrenceOnSQLite(t *testing.T) {
	runRustCrossTablesOnSQLite(t, "fixture-recurrence-db", "recurrence", recurrenceRustSQLiteTest, recurrenceRustSQLiteCases, nil, true)
}

// TestRustCalendarOnSQLite builds fixture-access-db's Rust types with their
// sqlite feature and runs testdata/calendar_rust_sqlite.rs: the calendar of
// its documents read as three users, an agent run and a party's grant,
// each holding exactly its list reads, and the feed's routes over the file
// (orm::calendar::sqlite::calendar_routes): a busy and a full feed, its
// token refused by the grants' authenticator elsewhere, rotated, revoked,
// expired and unknown.
func TestRustCalendarOnSQLite(t *testing.T) {
	runRustCrossTablesOnSQLite(t, "fixture-access-db", "calendar", calendarRustSQLiteTest, calendarRustSQLiteCases, func(cfg *schemaconfig.SchemaConfig) {
		sql, _ := cfg.Outputs["sql"].(map[string]any)
		if sql == nil {
			sql = map[string]any{}
			cfg.Outputs["sql"] = sql
		}
		sql["encryption"] = map[string]any{"key": "AccessKeys.ACCESS_FIELD_KEYS"}
	}, false)
}

// TestRustSearchOnSQLite builds fixture-search-db's Rust types with their
// sqlite feature and runs testdata/search_rust_sqlite.rs, the cases of
// TestRustSearchEndToEnd over orm::search_tables::sqlite: six @searchable
// tables in one index whose words are an FTS5 table, two principals each
// finding exactly their list reads, a party's grant through the route and
// the MCP tool, a row's conversation and files, an index row following its
// row through the queue's drain, scope, and embeddings refused.
func TestRustSearchOnSQLite(t *testing.T) {
	runRustCrossTablesOnSQLite(t, "fixture-search-db", "search", searchRustSQLiteTest, searchRustSQLiteCases, func(cfg *schemaconfig.SchemaConfig) {
		sql, _ := cfg.Outputs["sql"].(map[string]any)
		if sql == nil {
			sql = map[string]any{}
			cfg.Outputs["sql"] = sql
		}
		sql["encryption"] = map[string]any{"key": "SearchKeys.SEARCH_FIELD_KEYS"}
	}, false)
}

// runRustCrossTablesOnSQLite builds db's Rust types and runs source, a test
// of a crate of its own over them, on the SQLite DDL the build wrote
// (create.sql); with postgres, the types and the runtime have their
// postgres feature too, the Postgres DDL is create.pg.sql, and the test
// is given RECURRENCE_TEST_DATABASE_URL when the database is set.
func runRustCrossTablesOnSQLite(t *testing.T, db, name, source string, cases []string, configure func(*schemaconfig.SchemaConfig), postgres bool) {
	if testing.Short() {
		t.Skip("skipping the Rust reads across tables on SQLite in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust reads across tables on SQLite")
	}
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, db))
	if err != nil {
		t.Fatalf("load %s: %v", db, err)
	}
	types, _ := cfg.Outputs["types"].(map[string]any)
	if types == nil {
		types = map[string]any{}
		cfg.Outputs["types"] = types
	}
	types["rust"] = map[string]any{"enabled": true}
	// The Go ORM, which a DB service always generates, imports its Go
	// types.
	types["go"] = map[string]any{"enabled": true}
	if configure != nil {
		configure(cfg)
	}
	if _, err := Run(schema, cfg, Options{
		OutputRoot:       outputRoot,
		Paths:            paths,
		Naming:           naming.Default(),
		Clock:            codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		DependencyConfig: func(string) (*schemaconfig.SchemaConfig, bool) { return nil, false },
	}); err != nil {
		t.Fatalf("run %s: %v", db, err)
	}
	ddl, err := os.ReadFile(filepath.Join(SQLDir(outputRoot, db), SQLiteSubdir, "create.sql"))
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL for %s: %v", db, err)
	}
	typesDir := TypesDir(outputRoot, "rust", db)
	crateDir := filepath.Join(outputRoot, name+"-rust-sqlite")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	features := `"sqlite"`
	env := []string{}
	if postgres {
		features = `"sqlite", "postgres"`
		pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(crateDir, "create.pg.sql"))
		if dsn := os.Getenv(behaviorServerDatabaseEnv); dsn != "" {
			env = append(env, "RECURRENCE_TEST_DATABASE_URL="+dsn)
		} else {
			t.Log("set " + behaviorServerDatabaseEnv + " to hold the cases on Postgres too")
		}
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for file, content := range map[string]string{
		"Cargo.toml":                fmt.Sprintf(rustCrossTablesManifest, name, naming.Default().RustTypesCrate(db), filepath.ToSlash(typesDir), features, filepath.ToSlash(paths.HTTPRuntimeRust), features),
		"Cargo.lock":                string(lock),
		"create.sql":                string(ddl),
		"src/lib.rs":                "//! " + db + "'s Rust reads across tables on SQLite: see tests/.\n",
		"tests/" + name + ".rs":     source,
		"search_tables_parity.json": searchParityJSON(t),
	} {
		if err := os.WriteFile(filepath.Join(crateDir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(outputRoot, "target")
	}
	var out []byte
	for _, args := range [][]string{
		{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"},
		{"test", "--quiet", "--", "--nocapture"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(append(os.Environ(), "CARGO_TARGET_DIR="+target), env...)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/%s.rs:\n%s", name, out)
	for _, c := range cases {
		if !strings.Contains(string(out), "case: "+c+"\n") {
			t.Errorf("the case %q did not run:\n%s", c, out)
		}
	}
}

// rustCrossTablesManifest is the test crate's Cargo.toml, given its name,
// the types crate's name, directory and features, and the HTTP runtime's
// directory and features.
const rustCrossTablesManifest = `# A DB's generated Rust reads across tables on a SQLite file (D186, #615).
[package]
name = "%s-rust-sqlite"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
http = "1"
http-body-util = "0.1.3"
serde = { version = "1.0.228", features = ["derive"] }
ring = "0.17.14"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
tower = { version = "0.5", features = ["util"] }
types = { package = %q, path = %q, features = [%s] }
runtime = { package = "superschematic-http-runtime", path = %q, features = [%s] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
