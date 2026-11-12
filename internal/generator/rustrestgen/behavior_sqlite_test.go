package rustrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// The behaviors of a Rust API on a SQLite file (D116): the Timers and the
// Schedules fixtures with Deps.db their database's SQLite file, as the
// dispatch layer sets it for a database whose DB service lists sqlite
// first.
const sqliteBehaviorsGoldenRoot = "testdata/golden-sqlite"

// writeSQLiteBehaviorsGolden writes the fixture's crate with its Deps on a
// SQLite file and pins the files that differ from the crate on Postgres:
// Cargo.toml, src/deps.rs and src/behaviors.rs. It returns them, and checks
// that rustfmt lays out behaviors.rs as written, when rustfmt is there.
func writeSQLiteBehaviorsGolden(t *testing.T, name string, output *APIOutput, deps DepsInfo) map[string]string {
	t.Helper()
	deps.SQLite = true
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	paths := naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust", ScalarRust: "/repo/third_party/superscalar/crates/core"}
	if err := SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+name); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	generated := map[string]string{}
	for _, file := range []string{"Cargo.toml", "src/deps.rs", "src/behaviors.rs"} {
		got, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatalf("read generated %s: %v", file, err)
		}
		generated[file] = string(got)
		golden := filepath.Join(sqliteBehaviorsGoldenRoot, name, filepath.FromSlash(file))
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
		source := filepath.Join(outDir, "src", "behaviors.rs")
		if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
			t.Errorf("rustfmt lays out %s's src/behaviors.rs otherwise: %v\n%s", name, err, diff)
		}
	}
	return generated
}

// TestWriteRustAPIGoldenTimersOnSQLite pins what the crate of
// fixture-timers-api holds over a SQLite file (D116): Deps.db is the file;
// the crate takes the adapter's sqlite feature; its behavior operations
// run through the adapter's sqlite module in a transaction of the file's
// writing connection; and its behavior worker TicketTimers is a worker a
// host drains, which ticket_timers_worker builds from Ticket's deadline
// queue and then its change queue, each with the handler that runs a
// claimed message in the unit its drain runs it in, at the time the drain
// was given. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenTimersOnSQLite -update
func TestWriteRustAPIGoldenTimersOnSQLite(t *testing.T) {
	output, deps := timersDeps(t)
	generated := writeSQLiteBehaviorsGolden(t, timersFixture.api, output, deps)
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-http-runtime = { path = "../../../../runtime/http/rust", features = ["sqlite"] }`,
			`superschematic-rules-adapter = { path = "../../../../runtime/engine/rust/rules-adapter", features = ["sqlite"] }`,
		},
		"src/deps.rs": {"pub db: Arc<Database>,", "behaviors: crate::behaviors::Behaviors::new(db),"},
		"src/behaviors.rs": {
			"use superschematic_http_runtime::worker::{Claimed, SqliteWorker, Unit};",
			"use crate::database_types::{\n    TicketChange, TicketDeadline, TICKET_CHANGE_QUEUE, TICKET_DEADLINE_QUEUE,\n};",
			"pub struct Behaviors {\n    db: Arc<Database>,\n}",
			".write_async(move |connection| {",
			"sqlite::run_operation(",
			"Err(Failure::File(err @ orm::Error::Busy(_))) => Err(err.into()),",
			`Caller::new("fixture-timers-api/TicketTimers", Vec::<String>::new())`,
			"pub fn ticket_timers_worker() -> Result<SqliteWorker, BoxError> {",
			"let deadlines = QueueDefinition::parse(TICKET_DEADLINE_QUEUE)?;",
			"let worker = worker.queue_in_unit(deadlines, run_ticket_timers_deadline)?;",
			"let worker = worker.queue_in_unit(changes, run_ticket_timers_change)?;",
			"pub fn run_ticket_timers_deadline(\n    unit: &Unit<'_>,\n    claimed: Claimed<TicketDeadline>,\n    now: SystemTime,\n) -> Result<(), BoxError> {",
			"let host = host_at(now);",
			"let run = unit.with(|connection| {\n        sqlite::run_deadline(",
			"sqlite::run_change(",
			"Some(&claim),",
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	for _, absent := range []string{"postgres", "Pool", "SelfCompleting", ".await?"} {
		if strings.Contains(generated["src/behaviors.rs"], absent) {
			t.Errorf("src/behaviors.rs holds %s, which a crate on a SQLite file has none of", absent)
		}
	}
}

// TestWriteRustAPIGoldenSchedulesOnSQLite pins what the crate of
// fixture-schedules-api holds over a SQLite file (D104, D116): its behavior
// worker takes the API's Workers, and its event queue's handler spends
// each claimed event in the unit its drain runs it in and hands it to the
// Workers method of its use and kind, whose future it drives where the
// drain runs, with an EventRun that lends the unit's connection and runs
// a behavior operation in it. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenSchedulesOnSQLite -update
func TestWriteRustAPIGoldenSchedulesOnSQLite(t *testing.T) {
	output, deps := schedulesDeps(t)
	generated := writeSQLiteBehaviorsGolden(t, schedulesFixture.api, output, deps)
	for _, text := range []string{
		"use superschematic_http_runtime::orm::sqlite::rusqlite::Connection;",
		"pub struct EventRun<'a> {\n    unit: Unit<'a>,\n    caller: Caller,\n    now: SystemTime,\n}",
		"pub fn with<T>(&self, work: impl FnOnce(&Connection) -> T) -> T {",
		"pub fn problem_wake_ups_worker(workers: Arc<dyn crate::Workers>) -> Result<SqliteWorker, BoxError> {",
		"let handle = move |unit: &Unit<'_>, claimed: Claimed<ProblemEvent>, now: SystemTime| {\n        run_problem_wake_ups_event(unit, workers.as_ref(), claimed, now)\n    };",
		"let worker = worker.queue_in_unit(events, handle)?;",
		"let spent =\n        unit.with(|connection| sqlite::spend_event(&host, connection, \"Problem\", Some(&claim)))?;",
		"block_on(problem_wake_ups_event(workers, &run, &claimed.message))",
		"workers.problem_wake_ups_schedules_run(typed, run).await",
	} {
		if !strings.Contains(generated["src/behaviors.rs"], text) {
			t.Errorf("src/behaviors.rs does not contain %q", text)
		}
	}
}

// TestBehaviorsOnSQLiteAreLaidOutAsRustfmtLaysThem writes src/behaviors.rs
// of each behavior fixture with a worker over a SQLite file, an operation
// of a table as a whole and the caller's preconditions among them, and
// holds each to rustfmt's layout. It skips without rustfmt.
func TestBehaviorsOnSQLiteAreLaidOutAsRustfmtLaysThem(t *testing.T) {
	rustfmt, err := exec.LookPath("rustfmt")
	if err != nil {
		t.Skip("rustfmt not available")
	}
	seen := map[string]bool{}
	for name, fixture := range map[string]func(*testing.T) (*APIOutput, DepsInfo){
		agentsFixture.api:    agentsDeps,
		poolFixture.api:      poolDeps,
		reactionsFixture.api: reactionsDeps,
		seatsFixture.api:     seatsDeps,
	} {
		t.Run(name, func(t *testing.T) {
			output, deps := fixture(t)
			deps.SQLite = true
			if err := output.SetDeps(deps); err != nil {
				t.Fatalf("set deps: %v", err)
			}
			seen["a table's operation"] = seen["a table's operation"] || output.HasTableBehaviorOperations()
			seen["preconditions"] = seen["preconditions"] || output.HasBehaviorPreconditions()
			paths := naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust", ScalarRust: "/repo/third_party/superscalar/crates/core"}
			if err := SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+name); err != nil {
				t.Fatalf("set replace paths: %v", err)
			}
			outDir := t.TempDir()
			if err := WriteAPI(output, outDir); err != nil {
				t.Fatalf("write api: %v", err)
			}
			source := filepath.Join(outDir, "src", "behaviors.rs")
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s's src/behaviors.rs otherwise: %v\n%s", name, err, diff)
			}
		})
	}
	for _, what := range []string{"a table's operation", "preconditions"} {
		if !seen[what] {
			t.Errorf("no fixture's behaviors.rs holds %s", what)
		}
	}
}

// buildOnSQLite writes the fixture's API crate as build writes it, with
// its Deps on a SQLite file.
func (f behaviorFixture) buildOnSQLite(t *testing.T) behaviorCrate {
	t.Helper()
	crate := f.build(t, false)
	deps := *crate.output.Deps
	deps.SQLite = true
	if err := crate.output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	if err := WriteAPI(crate.output, crate.dir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	manifest := filepath.Join(crate.dir, "Cargo.toml")
	cargoToml, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// The generated manifest ends in its [dependencies] table.
	cargoToml = append(cargoToml, []byte("\n"+testpaths.RustPatch(testpaths.Local(t), naming.Default()))...)
	if err := os.WriteFile(manifest, cargoToml, 0o644); err != nil {
		t.Fatal(err)
	}
	return crate
}

// TestBehaviorsOnSQLiteAPICratesBuild lints the crates of
// fixture-timers-api, fixture-schedules-api and fixture-agents-api over a
// SQLite file with clippy, warnings denied: their behavior operations on
// the file's writing connection, an operation of a table as a whole and
// the caller's preconditions among them, and their behavior workers as
// workers a host drains, an event's among them, against the checkout's
// superschematic-rules-adapter, with its sqlite feature, and the HTTP
// runtime's drain (D116).
func TestBehaviorsOnSQLiteAPICratesBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	for _, f := range []behaviorFixture{timersFixture, schedulesFixture, agentsFixture} {
		t.Run(f.api, func(t *testing.T) {
			crate := f.buildOnSQLite(t)
			cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
		})
	}
}

// TestWhatARustAPIOnASQLiteFileDoesNotRunIsRefused: over a SQLite file a
// Rust API's behaviors run, its workers claim and its identity store is
// built (D116), and what the runtime builds over a Postgres pool alone
// stays refused, by name: an engine.
func TestWhatARustAPIOnASQLiteFileDoesNotRunIsRefused(t *testing.T) {
	output, deps := timersDeps(t)
	deps.SQLite = true
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("the behaviors and the behavior worker of %s on a SQLite file: %v", timersFixture.api, err)
	}
	output, deps = timersDeps(t)
	deps.SQLite = true
	output.Identity = &IdentityInfo{AuthDB: timersFixture.db}
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("an API over the user model on a SQLite file: %v", err)
	}
	if got := output.RuntimeFeatures(); !slices.Equal(got, []string{"identity", "sqlite", "identity-sqlite"}) {
		t.Errorf("the runtime's features = %v, want its identity store on SQLite", got)
	}
	output, deps = timersDeps(t)
	deps.SQLite = true
	deps.Engine = &DepsEngine{Database: timersFixture.db}
	want := "fixture-timers-api is a Rust API on fixture-timers-db, whose DB service lists sqlite first, and the Rust server opens an engine on Postgres alone so far (D116); list postgres first in fixture-timers-db's outputs.sql.dialects, or serve the API in TypeScript"
	if err := output.SetDeps(deps); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("SetDeps = %v, want %q", err, want)
	}
}
