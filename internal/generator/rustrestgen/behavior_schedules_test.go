package rustrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// The schedules fixture (D104): fixture-schedules-api serves ProblemActions,
// @behaviorOperations of Problem for any signed-in caller, and runs
// ProblemWakeUps, @behaviorWorker(Problem) holding problems.schedule, over
// fixture-schedules-db, its one DB dependency. Its Problem implements
// Schedules (wake-ups after a delay, at an interval or on a cron line in
// America/New_York, a minute to thirty days apart, at most ten live a
// problem; outcomes told, resumed, held and missed, a missed run tried
// again five minutes on; every wake-up finished when its status, a
// Workflow, enters done or dropped). Each run is an event of Problem's
// event queue, which ProblemWakeUps hands to the project's Workers method
// problem_wake_ups_schedules_run.
var schedulesFixture = behaviorFixture{api: "fixture-schedules-api", db: "fixture-schedules-db"}

// schedulesCases are the cases every language's end-to-end test of the
// schedules fixture runs, by the same names, each of which the Rust test
// prints as it starts it.
var schedulesCases = []string{
	"setSchedule arms a wake-up after a delay, at an interval and on a cron line in the config's time zone, each a deadline message due at its nextAt",
	"a repeated setSchedule of a live name with the same spec answers unchanged and leaves the row's _version where it was",
	"setSchedule without problems.schedule is 403 forbidden, a delay under minMs is 400 invalid_argument, and an eleventh live wake-up is 409 schedule_limit, each leaving the row's _version where it was",
	"a due wake-up fires on the worker as run 1 and hands the project's handler one run event with its name, run, attempt, kind, spec and times, which reports told",
	"the handler runs once per event: a completed event's message is gone and is not handed to it again",
	"a handler that fails fails its message, which is due again after the queue's backoff, and the next claim hands it to the handler again",
	"an interval overdue by many runs fires once, and its next run is the first of its grid after the fire",
	"a missed run is tried again five minutes on, as attempt 2 of the same run",
	"a report of an older run answers unchanged, and one of a run not had is 409 no_run",
	"pauseSchedule keeps nextAt and deletes its deadline message, and resumeSchedule past nextAt fires at once, once",
	"cancelSchedule finishes a wake-up and frees its place under max",
	"a problem whose status enters done finishes its wake-ups, and a new one is 409 schedules_ended",
	"a raw SQL write of a problem's status to dropped is heard by the worker and finishes its wake-ups",
	"listSchedules reads a problem's wake-ups in name order",
}

// schedulesDeps are fixture-schedules-api's Deps as the dispatch layer sets
// them: a pool of fixture-schedules-db, whose Rust types the crate
// re-exports for its worker's queues, and the layout and the crates its
// behaviors run with.
func schedulesDeps(t *testing.T) (*APIOutput, DepsInfo) {
	t.Helper()
	_, db, reg := schedulesFixture.load(t)
	output := schedulesFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = schedulesFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(schedulesFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + schedulesFixture.db
	return output, deps
}

// TestWriteRustAPIGoldenSchedules pins the crate of fixture-schedules-api
// (D104): the router mounts each operation of Problem's Schedules, which the
// table implements, under schedules (set-schedule, pause-schedule,
// resume-schedule, cancel-schedule, report-run, list-schedules) beside its
// status's transition, each run through the behaviors' core with
// run_operation; the behavior worker ProblemWakeUps runs Problem's deadline
// queue, its change queue and its event queue, the last of which hands each
// run event to the Workers method problem_wake_ups_schedules_run with an
// EventRun of the transaction that spends its message, as the worker
// holding problems.schedule; Deps declares Workers with that method, which
// the scaffold stubs. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenSchedules -update
func TestWriteRustAPIGoldenSchedules(t *testing.T) {
	output, deps := schedulesDeps(t)
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil {
			behaviors = append(behaviors, endpoint.Namespace+"."+endpoint.Name+" "+strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		}
	}
	slices.Sort(behaviors)
	if want := []string{
		"problem.schedulesCancelSchedule POST /api/problem/{id}/schedules/cancel-schedule Problem.Schedules.cancelSchedule",
		"problem.schedulesListSchedules POST /api/problem/{id}/schedules/list-schedules Problem.Schedules.listSchedules",
		"problem.schedulesPauseSchedule POST /api/problem/{id}/schedules/pause-schedule Problem.Schedules.pauseSchedule",
		"problem.schedulesReportRun POST /api/problem/{id}/schedules/report-run Problem.Schedules.reportRun",
		"problem.schedulesResumeSchedule POST /api/problem/{id}/schedules/resume-schedule Problem.Schedules.resumeSchedule",
		"problem.schedulesSetSchedule POST /api/problem/{id}/schedules/set-schedule Problem.Schedules.setSchedule",
		"problem.statusTransition POST /api/problem/{id}/status/transition Problem.status.transition",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 1 {
		t.Fatalf("workers %+v and behavior workers %+v, want the ProblemWakeUps behavior worker alone", output.Workers, output.BehaviorWorkers)
	}
	w := output.BehaviorWorkers[0]
	if w.Name != "ProblemWakeUps" || w.Behavior.Table != "Problem" || w.Behavior.Subject != "fixture-schedules-api/ProblemWakeUps" || !slices.Equal(w.Behavior.Permissions, []string{"problems.schedule"}) {
		t.Errorf("behavior worker %+v (%+v), want ProblemWakeUps on Problem as fixture-schedules-api/ProblemWakeUps holding problems.schedule", w, w.Behavior)
	}
	wantQueues := []BehaviorQueue{
		{Queue: "ProblemDeadline", Const: "PROBLEM_DEADLINE_QUEUE", Kind: ir.BehaviorQueueDeadlines, Func: "problem_wake_ups_deadlines", Handler: "run_problem_wake_ups_deadline"},
		{Queue: "ProblemChange", Const: "PROBLEM_CHANGE_QUEUE", Kind: ir.BehaviorQueueChanges, Func: "problem_wake_ups_changes", Handler: "run_problem_wake_ups_change"},
		{Queue: "ProblemEvent", Const: "PROBLEM_EVENT_QUEUE", Kind: ir.BehaviorQueueEvents, Func: "problem_wake_ups_events", Handler: "run_problem_wake_ups_event"},
	}
	if !slices.Equal(w.BehaviorQueues, wantQueues) {
		t.Errorf("ProblemWakeUps' queues\n%+v, want\n%+v", w.BehaviorQueues, wantQueues)
	}
	handlers := output.EventHandlers()
	if len(handlers) != 1 || handlers[0].Fn != "problem_wake_ups_schedules_run" || handlers[0].Event != "ProblemSchedulesRunEvent" || handlers[0].Use != "Schedules" || handlers[0].Kind != "run" || handlers[0].Queue != "ProblemEvent" {
		t.Errorf("event handlers %+v, want problem_wake_ups_schedules_run of Schedules' run on ProblemEvent", handlers)
	}
	if !output.HasWorkers() {
		t.Error("the crate declares no Workers, whose method takes Problem's run events")
	}

	generated := writeGoldenAPI(t, schedulesFixture.api, output)
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-rules-adapter = { path = "../../../../runtime/engine/rust/rules-adapter", features = ["postgres"] }`,
			`schemas-fixture-schedules-db-types = { path = "../../types/rust/fixture-schedules-db" }`,
		},
		"src/lib.rs": {"pub mod behaviors;", "pub use deps::{BoxError, Deps, Handlers, Workers};"},
		"src/deps.rs": {
			"pub trait Workers: Send + Sync {",
			"    async fn problem_wake_ups_schedules_run(\n        &self,\n        event: crate::types::ProblemSchedulesRunEvent,\n        run: &crate::behaviors::EventRun<'_>,\n    ) -> Result<(), BoxError>;",
		},
		"src/router.rs": {
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Problem\",\n        &args.id,\n        \"Schedules\",\n        \"setSchedule\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Problem\",\n        &args.id,\n        \"Schedules\",\n        \"listSchedules\",",
		},
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			"Caller::new(\n        \"fixture-schedules-api/ProblemWakeUps\",\n        [\"problems.schedule\"],\n    )",
			"pub fn problem_wake_ups_deadlines(",
			"pub fn problem_wake_ups_changes(",
			"pub fn problem_wake_ups_events(",
			"pub async fn run_problem_wake_ups_event(",
			"pub struct EventRun<'a> {",
			"let spent = postgres::spend_event(&host, &*tx, \"Problem\", Some(&claim)).await?;",
			"(\"Schedules\", \"run\") => {",
			"workers.problem_wake_ups_schedules_run(typed, run).await",
			"behavior worker ProblemWakeUps: no Workers method takes event {kind} of {use_id} on Problem {} (D104)",
			`"problem_event_queue"`,
			`"problem_schedules_schedules"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	if !strings.Contains(output.PermissionCatalogJSON, `"problems.schedule"`) {
		t.Error("the crate's permissions.json does not list problems.schedule")
	}
}

// TestSchedulesImplementationScaffoldStubsTheEventHandler: the scaffold of
// fixture-schedules-api's implementation (D104) has create_workers, whose
// Workers stub the run event's method, failing every event until it is
// written; the API declares no @worker, so it names no message of its
// database's types.
func TestSchedulesImplementationScaffoldStubsTheEventHandler(t *testing.T) {
	output, deps := schedulesDeps(t)
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "impl")
	wrote, err := WriteImplementationScaffold(output, dir, "/repo/schemas/dist/api/"+schedulesFixture.api, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, naming.Default())
	if err != nil || !wrote {
		t.Fatalf("write the scaffold: %v, %v", wrote, err)
	}
	lib, err := os.ReadFile(filepath.Join(dir, ImplementationLib))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"//! Each event handler fails every event until you implement it.",
		"use schemas_fixture_schedules_api_api::behaviors::EventRun;",
		"use schemas_fixture_schedules_api_api::types;",
		"pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {",
		"    async fn problem_wake_ups_schedules_run(\n        &self,\n        _event: types::ProblemSchedulesRunEvent,\n        _run: &EventRun<'_>,\n    ) -> Result<(), BoxError> {",
		`Err("event handler ProblemWakeUpsSchedulesRun is not implemented".into())`,
	} {
		if !strings.Contains(string(lib), want) {
			t.Errorf("the scaffold's src/lib.rs does not contain %q:\n%s", want, lib)
		}
	}
	for _, absent := range []string{"database_types", "Each worker fails every message"} {
		if strings.Contains(string(lib), absent) {
			t.Errorf("the scaffold's src/lib.rs holds %q, of a @worker the API declares none of:\n%s", absent, lib)
		}
	}
}

// TestSchedulesAPICrateBuilds lints fixture-schedules-api's crate with
// clippy, warnings denied: its routes of Problem's Schedules and status,
// and its behavior worker's three queues and handlers, the event queue's
// EventRun and dispatcher among them, against the checkout's
// superschematic-rules-adapter and superschematic-rules-behaviors.
func TestSchedulesAPICrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	crate := schedulesFixture.build(t, false)
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// TestRustSchedulesOnPostgres serves fixture-schedules-api's build_router on
// a local socket, over a schema of its own in the Postgres at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL that holds fixture-schedules-db's
// tables (D104): it creates its problems with the adapter's run_create;
// calls Problem's behavior operations through the generated Rust SDK, as
// subjects that are fresh UUIDs in their base62 form; runs ProblemWakeUps'
// three queues and handlers in process, with a Workers of its own whose
// event handler records each event and reports its run through the
// EventRun's operation as the worker, or fails the events the case says
// to; makes a deadline or an event due with SQL, an operator's escape
// hatch; and reads the sub-table and the queues with SQL: the cases every
// language's end-to-end test runs, by the same names, each held to have
// run. Its authenticator reads Bearer <subject>:<permission>,... . It skips
// without the database, without cargo, and in -short mode.
func TestRustSchedulesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's Schedules against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's Schedules against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's Schedules")
	}
	crate := schedulesFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(schedulesFixture.api), "-", "_"),
	).Replace(schedulesRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_schedules.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	out := cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_schedules", "--", "--nocapture")
	for _, name := range schedulesCases {
		if !strings.Contains(out, "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// schedulesRoundtripTest is tests/behavior_schedules.rs of
// fixture-schedules-api's API crate, beside fixture-schedules-db's DDL;
// schedulesRoundtripCases holds its cases.
const schedulesRoundtripTest = schedulesRoundtripHead + schedulesRoundtripCases
