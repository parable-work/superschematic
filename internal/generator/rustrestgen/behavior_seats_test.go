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

// The seats fixture (D104): fixture-seats-api serves ProblemActions and
// SandboxActions, @behaviorOperations of Problem and Sandbox for any
// signed-in caller, and runs SandboxDeadlines, @behaviorWorker(Sandbox),
// over fixture-seats-db, its one DB dependency. Its Problem implements
// Seats (one accountable person, whom the next to take the seat displaces;
// any number of responsible agents, the row's own agent among them through
// fromField; at most five handles, each released by the agent that claimed
// it or by a caller holding problems.release) and StateRules (in review
// only the accountable person edits the title, by seat:accountable),
// beside its status, a Workflow whose transitions name the seats in by;
// its Sandbox's observed is a Reconcile beside its status, a Workflow
// whose state follows the provider's reports and moves to unreachable when
// none has reached it for five minutes.
var seatsFixture = behaviorFixture{api: "fixture-seats-api", db: "fixture-seats-db"}

// seatsCases are the cases every language's end-to-end test of the seats
// fixture runs, by the same names, each of which the Rust test prints as
// it starts it: Seats' and the seats in by's first, then Reconcile's.
var seatsCases = []string{
	"a caller holding take seats itself as accountable, and the next to take the seat displaces it (onFull displace)",
	"a take of a seat the caller holds already answers unchanged and leaves the row's _version where it was",
	"a take by a caller without the seat's take permission is 403 forbidden",
	"the row's agent is seated as responsible through its field: listSeats lists it first, and its leaveSeat is 409 held_by_field",
	"a sixth handle is 409 seat_full",
	"a handle is released by the agent that claimed it or by a caller holding assign, and another caller's release is 403 forbidden",
	"a leave of a seat the caller does not hold answers unchanged",
	"a transition whose by names seat:responsible is made by a subject seated there, and another caller is 409 not_principal naming the seat",
	"a caller holding overridePermission moves past by",
	"in review the accountable subject edits the title StateRules freezes, and another caller's edit is 409 frozen_fields naming seat:accountable",
	"observe moves the sandbox along the fewest transitions to the observed state and records observed, observedAt and reconciledAt",
	"an observation no newer than observedAt answers unchanged and leaves the row's _version where it was",
	"a step the observer may not make stops the path and records drift as forbidden, and an observer who may completes it and clears drift",
	"observe without observePermission is 403 forbidden, and a state the Workflow lacks is 400 invalid_argument",
	"the stale deadline falls due on the worker five minutes after the last observation and moves the sandbox to unreachable",
	"a newer observation moves the sandbox back from unreachable",
}

// seatsDeps are fixture-seats-api's Deps as the dispatch layer sets them:
// a pool of fixture-seats-db, whose Rust types the crate re-exports for
// its worker's queue, and the layout and the crates its behaviors run
// with.
func seatsDeps(t *testing.T) (*APIOutput, DepsInfo) {
	t.Helper()
	_, db, reg := seatsFixture.load(t)
	output := seatsFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = seatsFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(seatsFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + seatsFixture.db
	return output, deps
}

// TestWriteRustAPIGoldenSeats pins the crate of fixture-seats-api (D104):
// the router mounts each operation of Problem's Seats, which the table
// implements, under seats (take-seat, leave-seat, list-seats) beside its
// status's transition, and Sandbox's observed observe, a Reconcile on a
// field, beside its status's transition, each run through the behaviors'
// core with run_operation, since no use of either table takes a
// precondition; StateRules, which has no operation, adds no route; and
// the behavior worker SandboxDeadlines runs Sandbox's deadline queue, the
// stale deadline among its messages, as the worker holding no permission,
// since no move to unreachable names one. The crate's permissions list
// Seats' take and assign permissions, Workflow's override and a
// transition's permission, and Reconcile's observePermission. Regenerate
// with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenSeats -update
func TestWriteRustAPIGoldenSeats(t *testing.T) {
	output, deps := seatsDeps(t)
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors, presenting []string
	for _, endpoint := range output.Endpoints {
		b := endpoint.Behavior
		if b == nil {
			continue
		}
		behaviors = append(behaviors, endpoint.Namespace+"."+endpoint.Name+" "+strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		if b.Preconditions {
			presenting = append(presenting, b.Type+"."+b.Use+"."+b.Operation)
		}
	}
	slices.Sort(behaviors)
	if want := []string{
		"problem.seatsLeaveSeat POST /api/problem/{id}/seats/leave-seat Problem.Seats.leaveSeat",
		"problem.seatsListSeats POST /api/problem/{id}/seats/list-seats Problem.Seats.listSeats",
		"problem.seatsTakeSeat POST /api/problem/{id}/seats/take-seat Problem.Seats.takeSeat",
		"problem.statusTransition POST /api/problem/{id}/status/transition Problem.status.transition",
		"sandbox.observedObserve POST /api/sandbox/{id}/observed/observe Sandbox.observed.observe",
		"sandbox.statusTransition POST /api/sandbox/{id}/status/transition Sandbox.status.transition",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(presenting) != 0 {
		t.Errorf("operations that take the caller's preconditions %q, want none: no use of either table takes one", presenting)
	}
	if len(output.ImplementedEndpoints()) != 0 {
		t.Errorf("implemented endpoints %d, want none: every operation is a behavior's", len(output.ImplementedEndpoints()))
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 1 {
		t.Fatalf("workers %+v and behavior workers %+v, want the SandboxDeadlines behavior worker alone", output.Workers, output.BehaviorWorkers)
	}
	w := output.BehaviorWorkers[0]
	if w.Name != "SandboxDeadlines" || w.Behavior.Table != "Sandbox" || w.Behavior.Subject != "fixture-seats-api/SandboxDeadlines" || len(w.Behavior.Permissions) != 0 {
		t.Errorf("behavior worker %+v (%+v), want SandboxDeadlines on Sandbox as fixture-seats-api/SandboxDeadlines holding no permission", w, w.Behavior)
	}
	wantQueue := BehaviorQueue{Queue: "SandboxDeadline", Const: "SANDBOX_DEADLINE_QUEUE", Kind: ir.BehaviorQueueDeadlines, Func: "sandbox_deadlines_deadlines", Handler: "run_sandbox_deadlines_deadline"}
	if !slices.Equal(w.BehaviorQueues, []BehaviorQueue{wantQueue}) {
		t.Errorf("SandboxDeadlines' queues %+v, want %+v", w.BehaviorQueues, wantQueue)
	}

	generated := writeGoldenAPI(t, seatsFixture.api, output)
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-rules-adapter = { path = "../../../../runtime/engine/rust/rules-adapter", features = ["postgres"] }`,
			`superschematic-rules-behaviors = { path = "../../../../runtime/engine/rust/rules-behaviors" }`,
			`schemas-fixture-seats-db-types = { path = "../../types/rust/fixture-seats-db" }`,
		},
		"src/lib.rs": {"pub mod behaviors;", "pub use schemas_fixture_seats_db_types as database_types;"},
		"src/router.rs": {
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Problem\",\n        &args.id,\n        \"Seats\",\n        \"takeSeat\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Problem\",\n        &args.id,\n        \"Seats\",\n        \"leaveSeat\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Problem\",\n        &args.id,\n        \"Seats\",\n        \"listSeats\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Problem\",\n        &args.id,\n        \"status\",\n        \"transition\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Sandbox\",\n        &args.id,\n        \"observed\",\n        \"observe\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Sandbox\",\n        &args.id,\n        \"status\",\n        \"transition\",",
		},
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			"postgres::run_operation(",
			`Caller::new("fixture-seats-api/SandboxDeadlines", Vec::<String>::new())`,
			"pub fn sandbox_deadlines_deadlines(",
			"pub async fn run_sandbox_deadlines_deadline(",
			`"problem_seats_occupants"`,
			`"observed_observed_at"`,
			`"sandbox_deadline_queue"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	for _, absent := range []string{"run_operation_with_preconditions", "stateRules", "StateRules", "state-rules"} {
		if strings.Contains(generated["src/router.rs"], absent) {
			t.Errorf("src/router.rs names %s: no use takes a precondition, and StateRules has no operation", absent)
		}
	}
	for _, permission := range []string{"problems.own", "problems.assign", "problems.work", "problems.release", "problems.override", "sandboxes.observe", "sandboxes.stop"} {
		if !strings.Contains(output.PermissionCatalogJSON, `"`+permission+`"`) {
			t.Errorf("the crate's permissions.json does not list %s", permission)
		}
	}
}

// TestSeatsAPICrateBuilds lints fixture-seats-api's crate with clippy,
// warnings denied: its routes of Problem's Seats and status and Sandbox's
// observed and status, and its behavior worker's queue and handler,
// against the checkout's superschematic-rules-adapter and
// superschematic-rules-behaviors.
func TestSeatsAPICrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	crate := seatsFixture.build(t, false)
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// TestRustSeatsOnPostgres serves fixture-seats-api's build_router on a
// local socket, over a schema of its own in the Postgres at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL that holds fixture-seats-db's
// tables (D104): it creates its problems and sandboxes with the adapter's
// run_create, a problem's agent the agent's subject, updates a problem's
// title with its run_update as the caller (a Rust server has no
// repository), and observes a state no Workflow names with its
// run_operation, which the route's input types refuse first; calls the
// tables' behavior operations through the generated Rust SDK, as subjects
// that are fresh UUIDs in their base62 form; runs
// SandboxDeadlines' queue and handler in process, the stale deadline made
// due with SQL as the Agents test makes one; and reads the columns, the
// occupants sub-table and the deadline queue with SQL: the cases every
// language's end-to-end test runs, by the same names, each held to have
// run. Its authenticator reads Bearer <subject>:<permission>,... . It
// skips without the database, without cargo, and in -short mode.
func TestRustSeatsOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's Seats and Reconcile against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's Seats and Reconcile against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's Seats and Reconcile")
	}
	crate := seatsFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(seatsFixture.api), "-", "_"),
	).Replace(seatsRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_seats.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	out := cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_seats", "--", "--nocapture")
	for _, name := range seatsCases {
		if !strings.Contains(out, "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// seatsRoundtripTest is tests/behavior_seats.rs of fixture-seats-api's API
// crate, beside fixture-seats-db's DDL; seatsRoundtripCases holds its
// cases.
const seatsRoundtripTest = seatsRoundtripHead + seatsRoundtripCases
