package rustrestgen

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixture of the generated wiring (#620, #622, #617, #610):
// fixture-wired-api, a Rust API over fixture-agent-runs-db, whose
// database has the notifier, the agent orchestration, the inbound path,
// two @searchable tables on one queue and grants, and whose workers are
// the parts' queues.
const (
	wiredAPI = "fixture-wired-api"
	wiredDB  = "fixture-agent-runs-db"
)

// wiredOutput is fixture-wired-api's crate with its Deps set, as a build over
// Postgres sets them, or over SQLite with sqlite.
func wiredOutput(t *testing.T, sqlite bool) *APIOutput {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, wiredDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", wiredDB, err)
	}
	api, err := loader.LoadService(filepath.Join(fixturesDir, wiredAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", wiredAPI, err)
	}
	outDir := t.TempDir()
	output, err := generateFrom(api, apiSource{upstream: wiredDB, upstreamIR: db}, Options{
		SchemaName:   wiredAPI,
		Dependencies: map[string]*ir.Schema{wiredDB: db},
		TypesCrate:   "schemas-" + wiredAPI + "-types",
		TypesDir:     filepath.Join(outDir, "types", "rust", wiredAPI),
		OutputDir:    filepath.Join(outDir, "api", wiredAPI),
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		MCP:          true,
	})
	if err != nil || output == nil {
		t.Fatalf("generate %s: %v, %v", wiredAPI, output, err)
	}
	output.HasEnvConfig = true
	// The provider crates' paths, from a build's output root, as this
	// checkout's are: the loader found them on this machine.
	for i := range output.Providers.Crates {
		output.Providers.Crates[i].Path = "../../../../examples/runtime-providers/rust/" + output.Providers.Crates[i].Crate
	}
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps = DepsInfo{
		Behaviors:          deps.Behaviors,
		Database:           wiredDB,
		SQLite:             sqlite,
		DatabaseTypesCrate: naming.Default().RustTypesCrate(wiredDB),
		DatabaseTypesPath:  "../../types/rust/" + wiredDB,
	}
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	return output
}

// TestWriteRustAPIGoldenWiring pins fixture-wired-api's crate: src/wiring.rs
// builds the dispatcher, then the notifier with its approve and deny and
// the requests queue, then the inbound path with the dispatcher's send
// approval, hands each to the implementation's Wire, wraps the
// implementation's authenticator in the grants', serves the parts' routes
// and runs each worker of a part's queue; Deps holds the notifier, the
// dispatcher and the inbound path; and Workers has no method for any of the
// four workers, which are the parts'. Regenerate with:
//
//	go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenWiring -update
func TestWriteRustAPIGoldenWiring(t *testing.T) {
	output := wiredOutput(t, false)
	w := output.Wiring()
	if w == nil {
		t.Fatal("fixture-wired-api's Deps has no wiring")
	}
	if got := (DepsWiring{
		Dispatch: "NoticeWork", DispatchConst: "NOTICE_WORK_QUEUE",
		Requests: "AgentRunRequest", RequestsConst: "AGENT_RUN_REQUEST_QUEUE",
		RunRequests: "RunRequest", RunRequestsConst: "RUN_REQUEST_QUEUE",
		Inbound: true,
		Search:  []SearchQueue{{Queue: "SearchIndexing", Const: "SEARCH_INDEXING_QUEUE"}},
		Grants:  true,
	}); !wiringEqual(*w, got) {
		t.Errorf("wiring = %+v, want %+v", *w, got)
	}
	var runtimes []string
	for _, worker := range output.RuntimeWorkers {
		runtimes = append(runtimes, worker.Name+" "+worker.Runtime+" "+worker.RunFunc())
	}
	if want := []string{"Dispatch agents run_agent_run_request", "InboundRuns runs run_run_request", "Index search run_search_indexing", "Notify notifier run_notice_work"}; !slices.Equal(runtimes, want) {
		t.Errorf("runtime workers %q, want %q", runtimes, want)
	}
	if len(output.Workers) != 0 {
		t.Errorf("workers %+v, want none the implementation handles", output.Workers)
	}
	generated := writeGoldenAPI(t, wiredAPI, output)
	for file, want := range map[string][]string{
		"src/wiring.rs": {
			"pub trait Wire: Send + Sync {",
			"let agents = wire.agents(agents)?;",
			`notifier = notifier.with_reply("approval.requested", verb, action);`,
			".with_approval(agents.send_approval());",
			"agents.set_notifier(notifier.clone());",
			"agents.set_inbound(inbound.clone());",
			"GrantAuthenticator::new(self.tables.clone())?",
			"pub async fn run_notice_work(",
			"run_worker(self.inbound.run_queue(), handle, options, shutdown).await",
			"index_search(&mut client, definition, &message, &search).await?;",
		},
		"src/deps.rs": {"pub notifier: Notifier,", "pub agents: Agents,", "pub inbound: Inbound,"},
		"src/lib.rs":  {"pub mod wiring;", "pub use wiring::{Wire, Wired, WiringDeps};"},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q:\n%s", file, text, generated[file])
			}
		}
	}
	// Workers has the tasks' schedules' run event alone (D104).
	if strings.Contains(generated["src/deps.rs"], "async fn notify(") || !strings.Contains(generated["src/deps.rs"], "pub trait Workers") {
		t.Errorf("deps.rs declares a method of a part's worker, or no Workers for the run event:\n%s", generated["src/deps.rs"])
	}
}

// TestWiringOnEitherDialect: a database that lists sqlite first has the
// generated wiring as one on Postgres does (#725), over its SQLite file,
// and a worker of a part's queue is the runtime's on both.
func TestWiringOnEitherDialect(t *testing.T) {
	output := &APIOutput{databases: map[string]*ir.Schema{}}
	output.SchemaName = wiredAPI
	output.Workers = []WorkerInfo{{Name: "Notify", Queue: "NoticeWork"}}
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, wiredDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	output.databases[wiredDB] = db
	deps := DepsInfo{Database: wiredDB, SQLite: true, DatabaseTypesCrate: "types"}
	if err := output.setWiring(&deps); err != nil {
		t.Fatal(err)
	}
	if deps.Wiring == nil || !deps.Wiring.SQLite || len(output.RuntimeWorkers) != 1 || len(output.Workers) != 0 {
		t.Errorf("on SQLite: wiring %+v, runtime workers %+v, workers %+v", deps.Wiring, output.RuntimeWorkers, output.Workers)
	}
	deps = DepsInfo{Database: wiredDB, DatabaseTypesCrate: "types"}
	if err := output.setWiring(&deps); err != nil {
		t.Fatal(err)
	}
	if deps.Wiring == nil || len(output.RuntimeWorkers) != 1 || len(output.Workers) != 0 {
		t.Errorf("on Postgres: wiring %+v, runtime workers %+v, workers %+v", deps.Wiring, output.RuntimeWorkers, output.Workers)
	}
	deps = DepsInfo{Database: wiredDB}
	if err := output.setWiring(&deps); err == nil || !strings.Contains(err.Error(), "#620") {
		t.Errorf("a wiring with no Rust types: %v", err)
	}
}

// TestWiredImplementationScaffold: the scaffold of fixture-wired-api's
// implementation declares create_wiring, whose Wire keeps every part as
// built, and no method for a worker of a part's queue.
func TestWiredImplementationScaffold(t *testing.T) {
	output := wiredOutput(t, false)
	dir := filepath.Join(t.TempDir(), "impl")
	wrote, err := WriteImplementationScaffold(output, dir, "/repo/schemas/dist/api/"+wiredAPI, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, naming.Default())
	if err != nil || !wrote {
		t.Fatalf("write the scaffold: %v, %v", wrote, err)
	}
	lib, err := os.ReadFile(filepath.Join(dir, ImplementationLib))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub async fn create_wiring(_deps: &WiringDeps) -> Result<Arc<dyn Wire>, BoxError> {",
		"impl Wire for FixtureWiredApiWiring {}",
	} {
		if !strings.Contains(string(lib), want) {
			t.Errorf("the scaffold's src/lib.rs does not contain %q:\n%s", want, lib)
		}
	}
	if strings.Contains(string(lib), "async fn notify(") || strings.Contains(string(lib), "Each worker fails every message") {
		t.Errorf("the scaffold implements a part's worker:\n%s", lib)
	}
}

func wiringEqual(a, b DepsWiring) bool {
	return a.Dispatch == b.Dispatch && a.DispatchConst == b.DispatchConst && a.Requests == b.Requests && a.RequestsConst == b.RequestsConst &&
		a.RunRequests == b.RunRequests && a.RunRequestsConst == b.RunRequestsConst && a.Inbound == b.Inbound &&
		slices.Equal(a.Search, b.Search) && a.Calendar == b.Calendar && a.Grants == b.Grants && a.AccessLog == b.AccessLog
}

// TestWriteRustAPIGoldenAccessWiring pins the wiring of
// fixture-access-api, over fixture-access-db, whose database has a
// calendar source, grants and an access log and none of the notifier's,
// the dispatcher's or the inbound path's tables: src/wiring.rs wraps the
// implementation's authenticator in the grants' and the access log, and
// serves the calendar's routes and its MCP tool, and Deps holds no part.
// Regenerate with:
//
//	go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenAccessWiring -update
func TestWriteRustAPIGoldenAccessWiring(t *testing.T) {
	const api, db = "fixture-access-api", "fixture-access-db"
	f := behaviorFixture{api: api, db: db}
	_, schema, _ := f.load(t)
	output := f.generate(t, schema)
	if err := output.SetDeps(DepsInfo{Database: db, DatabaseTypesCrate: naming.Default().RustTypesCrate(db), DatabaseTypesPath: "../../types/rust/" + db}); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	w := output.Wiring()
	if w == nil || !w.Calendar || !w.Grants || !w.AccessLog || w.Notifier() || w.Agents() || w.Inbound {
		t.Fatalf("wiring = %+v, want the calendar, grants and the access log alone", w)
	}
	if err := SetReplacePaths(output, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust", ScalarRust: "/repo/third_party/superscalar/crates/core"}, "/repo/schemas/dist/api/"+api); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", WiringFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", api, "src", WiringFile)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if want, err := os.ReadFile(golden); err != nil || string(got) != string(want) {
		t.Errorf("%s differs from its golden (run with -update to accept): %v", golden, err)
	}
	deps, err := os.ReadFile(filepath.Join(dir, "src", "deps.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(deps), "pub notifier:") || strings.Contains(string(deps), "pub agents:") {
		t.Errorf("Deps holds a part the database has none of:\n%s", deps)
	}
}
