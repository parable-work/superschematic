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

//go:embed testdata/search_rust.rs
var searchRustTest string

// searchRustCases are the cases testdata/search_rust.rs runs: D155's "done
// when" in the Rust repositories and runtime (D188), the TypeScript
// repositories' cases (tsgen's searchCases).
var searchRustCases = []string{
	"two principals with different access search the same text and each gets exactly the rows their list reads would show, ranked across at least five tables",
	"a party with a grant finds only the rows its grant reaches, through the route and the MCP tool too, and a revoked grant finds nothing",
	"a row's conversation and files are indexed with it, and an encrypted field never is",
	"an uploaded file's text the file processor extracts is indexed with its row, and a file the processor fails on ends failed",
	"an index row follows its row: an audience change and a delete at once, a change of text once the queue drains",
	"scope keeps a search to the app's filter",
	"embeddings re-rank the first hits, are kept while the text holds, and can be switched off",
	searchParityCase,
}

// searchParityCase is the case the Rust search tests run of the parity
// file's searches (#740), which the TypeScript and Go runtimes' search
// tests run too.
const searchParityCase = "the parity file's searches answer the same titles in every runtime (#740)"

// searchParityJSON is the parity file, which a test crate's tests read
// beside its manifest.
func searchParityJSON(t *testing.T) string {
	t.Helper()
	parity, err := os.ReadFile(filepath.Join("..", "..", "runtime", "http", "testdata", "search_tables_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(parity)
}

// TestRustSearchEndToEnd builds fixture-search-db's Rust types (D155,
// D188) and runs testdata/search_rust.rs over its generated repositories
// and the runtime's orm::search_tables on Postgres, in a schema of its own
// at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's create.sql
// applied: the triggers keep the index's access, the test's drain hands
// each message of the indexing queue to index_search as its worker would,
// and every case of searchRustCases must run. The contract's terms are
// sealed with the parity key.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustSearchEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust search across tables in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Rust search across tables against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust search across tables")
	}
	const db = "fixture-search-db"
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
	// The contract's terms are sealed under a key an API's config would
	// hold; the test hands the repositories the parity key itself.
	sql, _ := cfg.Outputs["sql"].(map[string]any)
	if sql == nil {
		sql = map[string]any{"dialects": []any{"postgres"}}
		cfg.Outputs["sql"] = sql
	}
	sql["encryption"] = map[string]any{"key": "SearchKeys.SEARCH_FIELD_KEYS"}
	if _, err := Run(schema, cfg, Options{
		OutputRoot:       outputRoot,
		Paths:            paths,
		Naming:           naming.Default(),
		Clock:            codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		DependencyConfig: func(string) (*schemaconfig.SchemaConfig, bool) { return nil, false },
	}); err != nil {
		t.Fatalf("run %s: %v", db, err)
	}

	typesDir := TypesDir(outputRoot, "rust", db)
	crateDir := filepath.Join(outputRoot, "search-rust")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(crateDir, "create.sql"))
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for name, content := range map[string]string{
		"Cargo.toml":                fmt.Sprintf(searchRustManifest, naming.Default().RustTypesCrate(db), filepath.ToSlash(typesDir), filepath.ToSlash(paths.HTTPRuntimeRust)),
		"Cargo.lock":                string(lock),
		"src/lib.rs":                "//! fixture-search-db's Rust repositories and search: see tests/.\n",
		"tests/search.rs":           searchRustTest,
		"search_tables_parity.json": searchParityJSON(t),
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
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
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "SEARCH_TEST_DATABASE_URL="+dsn)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/search.rs:\n%s", out)
	for _, name := range searchRustCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// searchRustManifest is the test crate's Cargo.toml, given the types
// crate's name and directory and the HTTP runtime's directory.
const searchRustManifest = `# fixture-search-db's Rust repositories, search across its tables end
# to end (D155, D188).
[package]
name = "search-rust"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
http = "1"
http-body-util = "0.1.3"
ring = "0.17.14"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
tower = { version = "0.5", features = ["util"] }
types = { package = %q, path = %q, features = ["postgres"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["postgres"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
