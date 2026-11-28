package rustrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// wiringSQLiteGoldenRoot holds the wiring's files of a crate whose Deps are
// on its database's SQLite file (#725), a directory per fixture.
const wiringSQLiteGoldenRoot = "testdata/golden-sqlite"

// writeWiringGolden writes output's crate and pins files, by their path in
// the crate, under root/name: the files that differ between the wiring on
// Postgres and on a SQLite file. It checks that rustfmt lays out
// src/wiring.rs as written, when rustfmt is there, and returns the files.
func writeWiringGolden(t *testing.T, root, name string, output *APIOutput, files ...string) map[string]string {
	t.Helper()
	paths := naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust", ScalarRust: "/repo/third_party/superscalar/crates/core"}
	if err := SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+name); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	generated := map[string]string{}
	for _, file := range files {
		got, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatalf("read generated %s: %v", file, err)
		}
		generated[file] = string(got)
		golden := filepath.Join(root, name, filepath.FromSlash(file))
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("read golden %s: %v", file, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s/%s differs from golden (run with -update to accept)", name, file)
		}
	}
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		source := filepath.Join(outDir, "src", WiringFile)
		if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
			t.Errorf("rustfmt lays out %s's src/wiring.rs otherwise: %v\n%s", name, err, diff)
		}
	}
	return generated
}

// TestWriteRustAPIGoldenWiringOnSQLite pins fixture-wired-api's crate over
// fixture-agent-runs-db listing sqlite first (#725): src/wiring.rs builds
// the dispatcher, the notifier and the inbound path over the file's
// Tables, as on Postgres, wraps the implementation's authenticator in
// access::sqlite's GrantAuthenticator, serves the search route and its MCP
// tool from search_tables::sqlite, and runs the index worker's queue as a
// PartQueue on the file; WiringDeps holds the file; the four workers stay
// the runtime's. Regenerate with:
//
//	go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenWiringOnSQLite -update
func TestWriteRustAPIGoldenWiringOnSQLite(t *testing.T) {
	output := wiredOutput(t, true)
	w := output.Wiring()
	if w == nil || !w.SQLite || !w.Notifier() || !w.Agents() || !w.Inbound || !w.Grants || len(w.Search) != 1 {
		t.Fatalf("wiring on SQLite = %+v, want every part of fixture-agent-runs-db", w)
	}
	var runtimes []string
	for _, worker := range output.RuntimeWorkers {
		runtimes = append(runtimes, worker.Name+" "+worker.Runtime)
	}
	if want := []string{"Dispatch agents", "InboundRuns runs", "Index search", "Notify notifier"}; !slices.Equal(runtimes, want) || len(output.Workers) != 0 {
		t.Errorf("runtime workers %q and workers %+v, want %q and none", runtimes, output.Workers, want)
	}
	generated := writeWiringGolden(t, wiringSQLiteGoldenRoot, wiredAPI, output, "Cargo.toml", "src/deps.rs", "src/lib.rs", "src/wiring.rs")
	for file, want := range map[string][]string{
		"src/wiring.rs": {
			"pub db: Arc<Database>,",
			"database_types::orm::sqlite::tables(deps.db.clone())",
			"use crate::runtime::access::sqlite::GrantAuthenticator;",
			"use crate::runtime::orm::search_tables::sqlite::{",
			"let queue = PartQueue::<SearchIndexing>::of(&Store::from(self.tables.clone()), definition)?;",
			"index_search(connection, definition, &message, &search)",
			"run_worker(self.notifier.worker_queue(), handle, options, shutdown).await",
			"run_worker(self.inbound.run_queue(), handle, options, shutdown).await",
		},
		"src/deps.rs": {"pub notifier: Notifier,", "pub agents: Agents,", "pub inbound: Inbound,", "pub db: Arc<Database>,"},
		"Cargo.toml":  {`features = ["sqlite"]`},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q:\n%s", file, text, generated[file])
			}
		}
	}
	for _, postgres := range []string{"postgres::Pool", "orm::postgres::Tables", "PostgresQueue", ".pool()"} {
		if strings.Contains(generated["src/wiring.rs"], postgres) {
			t.Errorf("src/wiring.rs on SQLite holds %s:\n%s", postgres, generated["src/wiring.rs"])
		}
	}
}

// TestWriteRustAPIGoldenAccessWiringOnSQLite pins the wiring of
// fixture-access-api over fixture-access-db on its SQLite file (#725): the
// grants' authenticator and the access log are access::sqlite's, and the
// calendar's routes and MCP tool calendar::sqlite's. Regenerate with:
//
//	go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenAccessWiringOnSQLite -update
func TestWriteRustAPIGoldenAccessWiringOnSQLite(t *testing.T) {
	const api, db = "fixture-access-api", "fixture-access-db"
	f := behaviorFixture{api: api, db: db}
	_, schema, _ := f.load(t)
	output := f.generate(t, schema)
	if err := output.SetDeps(DepsInfo{Database: db, SQLite: true, DatabaseTypesCrate: naming.Default().RustTypesCrate(db), DatabaseTypesPath: "../../types/rust/" + db}); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	if w := output.Wiring(); w == nil || !w.SQLite || !w.Calendar || !w.Grants || !w.AccessLog {
		t.Fatalf("wiring = %+v, want the calendar, grants and the access log on SQLite", w)
	}
	generated := writeWiringGolden(t, wiringSQLiteGoldenRoot, api, output, "src/wiring.rs")
	for _, want := range []string{
		"use crate::runtime::access::sqlite::{access_log_of, GrantAuthenticator};",
		"use crate::runtime::access::Logged;",
		"use crate::runtime::orm::calendar::sqlite::{calendar_routes, CalendarRoutes, CalendarTool};",
		"use crate::runtime::orm::sqlite::{Database, Tables};",
	} {
		if !strings.Contains(generated["src/wiring.rs"], want) {
			t.Errorf("src/wiring.rs does not contain %q:\n%s", want, generated["src/wiring.rs"])
		}
	}
}

// TestWriteRustAPIGoldenAudienceWiring pins the wiring of
// fixture-audience-queue-api over fixture-audience-queue-db, whose pages'
// @audience names a queue (D156, #734): the API's @worker of the queue is
// the runtime's, src/wiring.rs runs it through orm::resolve_audience on
// the pool, and Workers and the scaffold have no method for it.
// Regenerate with:
//
//	go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenAudienceWiring -update
func TestWriteRustAPIGoldenAudienceWiring(t *testing.T) {
	const api, db = "fixture-audience-queue-api", "fixture-audience-queue-db"
	f := behaviorFixture{api: api, db: db}
	_, schema, _ := f.load(t)
	output := f.generate(t, schema)
	if err := output.SetDeps(DepsInfo{Database: db, DatabaseTypesCrate: naming.Default().RustTypesCrate(db), DatabaseTypesPath: "../../types/rust/" + db}); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	w := output.Wiring()
	if w == nil || len(w.Audience) != 1 || w.Audience[0].Queue != "AudienceResolving" || w.SQLite {
		t.Fatalf("wiring = %+v, want the audience queue on Postgres", w)
	}
	if len(output.RuntimeWorkers) != 1 || output.RuntimeWorkers[0].Runtime != RuntimeAudience || output.RuntimeWorkers[0].RunFunc() != "run_audience_resolving" || len(output.Workers) != 0 {
		t.Fatalf("runtime workers %+v, workers %+v, want ResolveAudience the runtime's", output.RuntimeWorkers, output.Workers)
	}
	if !RuntimeQueues(schema)["AudienceResolving"] {
		t.Errorf("RuntimeQueues leaves the audience queue to the implementation")
	}
	generated := writeWiringGolden(t, "testdata/golden", api, output, "src/wiring.rs", "src/deps.rs")
	for _, want := range []string{
		"use crate::runtime::orm::{resolve_audience, AudienceResolving};",
		"pub async fn run_audience_resolving(",
		"PostgresQueue::<AudienceResolving>::new(self.tables.pool().clone(), definition)",
		"resolve_audience(&client, definition, &message).await?;",
	} {
		if !strings.Contains(generated["src/wiring.rs"], want) {
			t.Errorf("src/wiring.rs does not contain %q:\n%s", want, generated["src/wiring.rs"])
		}
	}
	if strings.Contains(generated["src/deps.rs"], "async fn resolve_audience(") {
		t.Errorf("Workers has a method for the audience queue's worker:\n%s", generated["src/deps.rs"])
	}
}
