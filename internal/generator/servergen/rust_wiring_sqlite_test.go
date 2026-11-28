package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
)

// wiredSQLiteGoldenRoot holds the wiring's files of fixture-wired-api's
// build over fixture-agent-runs-db listing sqlite first (#725).
const wiredSQLiteGoldenRoot = "testdata/golden/rust-wiring-sqlite"

// TestRustWiringGoldenOnSQLite is TestRustWiringGolden over
// fixture-agent-runs-db listing sqlite first (D116, #725): the server and
// each worker of a part's queue open the database's SQLite file, build
// WiringDeps over it and Wired before Deps, as on Postgres; the server
// wraps the implementation's authenticator, merges the parts' routes and
// passes the call gate and the search tool to its MCP endpoint; each
// worker runs its queue through Wired in the claim loop, with no
// create_workers and no pool. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustWiringGoldenOnSQLite -update
func TestRustWiringGoldenOnSQLite(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadWiredFixtureOn(t, "sqlite", "postgres")
	f.build(t, repoRoot, tablesDeriverPaths(repoRoot), wiredOrder...)
	files := wiredFiles(repoRoot)
	compareGoldens(t, wiredSQLiteGoldenRoot, files)

	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if wiring := read("api/fixture-wired-api/src/wiring.rs"); !strings.Contains(wiring, "pub db: Arc<Database>,") || strings.Contains(wiring, "postgres::Pool") {
		t.Errorf("the API crate's wiring is not over the SQLite file:\n%s", wiring)
	}
	server := read(filepath.Join("server", wiredStack, wiredAPI, "src", "main.rs"))
	for _, want := range []string{
		"let fixture_agent_runs_db_pool = open_file(",
		"fixture_wired_api_impl::create_wiring(&fixture_wired_api_wiring_deps)",
		"db: fixture_agent_runs_db_pool.clone(),",
		".handlers(fixture_wired_api_handlers)",
		"fixture_wired_api_wired.routes(&fixture_wired_api_implementations)",
		".with_call_gate(fixture_wired_api_wired.mcp_gate())",
	} {
		if !strings.Contains(server, want) {
			t.Errorf("the server's main.rs has no %s:\n%s", want, server)
		}
	}
	for worker, run := range map[string]string{
		"fixture-wired-api-notify":       ".run_notice_work(options, shutdown)",
		"fixture-wired-api-dispatch":     ".run_agent_run_request(options, shutdown)",
		"fixture-wired-api-inbound-runs": ".run_run_request(options, shutdown)",
		"fixture-wired-api-index":        ".run_search_indexing(options, shutdown)",
	} {
		main := read(filepath.Join("server", wiredStack, worker, "src", "main.rs"))
		for _, absent := range []string{"create_workers", "PostgresQueue", "postgres::", "run_drain"} {
			if strings.Contains(main, absent) {
				t.Errorf("%s's main.rs holds %s:\n%s", worker, absent, main)
			}
		}
		if !strings.Contains(main, run) || !strings.Contains(main, "open_file(") {
			t.Errorf("%s's main.rs does not run its queue through Wired on the file:\n%s", worker, main)
		}
	}
	if manifest := read(filepath.Join("server", wiredStack, wiredAPI, "Cargo.toml")); strings.Contains(manifest, `"postgres"`) {
		t.Errorf("the server's Cargo.toml turns on the runtime's postgres feature:\n%s", manifest)
	}
	scaffold := read("rust/fixture-wired-api/" + rustrestgen.ImplementationLib)
	if !strings.Contains(scaffold, "pub async fn create_wiring(") {
		t.Errorf("the scaffold declares no create_wiring:\n%s", scaffold)
	}
}
