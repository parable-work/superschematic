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

// The agents fixture (D104): fixture-agents-api serves JobActions,
// SessionActions and AccountActions, @behaviorOperations of Job, Session
// and Account for any signed-in caller, and runs JobDeadlines,
// @behaviorWorker(Job), and SessionPresence, @behaviorWorker(Session)
// holding jobs.override, over fixture-agents-db, its one DB dependency.
// Its Job implements Lease (a fencing token every write under the lease
// presents as Lease's precondition, an expiry that moves the job back to
// queued) and Retries (a timeout's backoff, a build failure once per
// revision through keyField, giveUpAfterMs and a window), beside its
// status, a Workflow; its Session implements Presence (a device's beat,
// ttlByState for a turn, a miss that expires the device's leases on jobs);
// and its Account and Workspace implement Budget (hourly and rolling daily
// meters, a provider's observed usage, a scope through the to-one relation
// workspace), the account beside its status.
var agentsFixture = behaviorFixture{api: "fixture-agents-api", db: "fixture-agents-db"}

// agentsCases are the cases every language's end-to-end test of the agents
// fixture runs, by the same names, each of which the Rust test prints as it
// starts it: Lease's and Retries' (D104's contract A), then Presence's and
// Budget's (contract B).
var agentsCases = []string{
	"a worker acquires a job's lease and gets its fencing token, and a heartbeat that presents it renews the lease",
	"under requireToken a write that presents no token is refused as token_required, and the holder's write that presents it is saved",
	"a token a later lease replaced is refused as token_stale",
	"another caller's acquire of a job whose lease is active is refused as held_by_another",
	"expireHolder expires every lease a gone holder holds, for a caller holding jobs.override, and is forbidden without it",
	"a failure in a class with a backoff sets retryAt, and until it passes the move into running and the lease's acquire are refused as backing_off",
	"when the backoff ends its then, run by the worker, moves the waiting job back to queued",
	"a lease that lapses expires when its deadline falls due on the worker: onExpiry moves the job back to queued and the token advances",
	"a second build failure of one revision exhausts the job, and a new revision starts its counts again",
	"a precondition for a use whose behavior takes none is refused as invalid_argument",
	"a session's create arms its miss ttlMs on: its deadline and its queue message",
	"only the device's principal beats a session: another caller is 409 not_principal, and the device's beat moves the deadline ttlMs on",
	"a session's transition into turn arms its miss fifteen minutes from the entry (ttlByState)",
	"a due miss the worker runs moves the session to missing and expires the leases its device holds on jobs (releaseLeases)",
	"a beat recovers a missed session: missed clears and onBeat moves it back to idle",
	"an update that changes a session's device is 409 principal_fixed",
	"recordUsage charges the account's hourly meter and its workspace's through the to-one relation",
	"a reservation past the account's limit is 409 over_limit, and leaves the rows as they were",
	"an hourly meter starts again at the next hour: its next write counts from zero",
	"a rolling meter counts the last day's buckets: usage that left the window no longer counts",
	"observe without observePermission is 403 forbidden, and with it the provider's figure replaces what the meter counts until resetsAt",
	"usage that takes the account over its limit moves it out (escalate), and counts all the same",
}

// agentsDeps are fixture-agents-api's Deps as the dispatch layer sets them:
// a pool of fixture-agents-db, whose Rust types the crate re-exports for
// its workers' queues, and the layout and the crates its behaviors run
// with.
func agentsDeps(t *testing.T) (*APIOutput, DepsInfo) {
	t.Helper()
	_, db, reg := agentsFixture.load(t)
	output := agentsFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = agentsFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(agentsFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + agentsFixture.db
	return output, deps
}

// TestWriteRustAPIGoldenAgents pins the crate of fixture-agents-api (D104):
// the router mounts each operation of Job's Lease and Retries, Session's
// Presence and Account's Budget, which the tables implement, beside each
// table's status transition, each run through the behaviors' core, and
// Lease's expireHolder, which runs on the table as a whole, at
// job/lease/expire-holder, a route with no key whose handler calls
// run_table_operation, a command without a row (D104); every operation of
// a row of Job, whose Lease takes a precondition, takes the caller's
// preconditions in its input's preconditions member and calls
// run_operation_with_preconditions, which hands them to the adapter's
// run_operation_with, while expireHolder's input presents none, a row's,
// and Session's and Account's call run_operation as before; and the
// behavior workers JobDeadlines and SessionPresence each
// run its table's deadline queue, SessionPresence as the worker holding
// jobs.override, which a miss's expiry of a device's leases needs.
// Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenAgents -update
func TestWriteRustAPIGoldenAgents(t *testing.T) {
	output, deps := agentsDeps(t)
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
	want := []string{
		"account.budgetCheckReserve POST /api/account/{id}/budget/check-reserve Account.Budget.checkReserve",
		"account.budgetObserve POST /api/account/{id}/budget/observe Account.Budget.observe",
		"account.budgetRecordUsage POST /api/account/{id}/budget/record-usage Account.Budget.recordUsage",
		"account.budgetRecordUsageFor POST /api/account/{id}/budget/record-usage-for Account.Budget.recordUsageFor",
		"account.budgetReserve POST /api/account/{id}/budget/reserve Account.Budget.reserve",
		"account.budgetReserveFor POST /api/account/{id}/budget/reserve-for Account.Budget.reserveFor",
		"account.budgetSetLimit POST /api/account/{id}/budget/set-limit Account.Budget.setLimit",
		"account.budgetSettle POST /api/account/{id}/budget/settle Account.Budget.settle",
		"account.budgetSettleFor POST /api/account/{id}/budget/settle-for Account.Budget.settleFor",
		"account.statusTransition POST /api/account/{id}/status/transition Account.status.transition",
		"job.leaseAcknowledge POST /api/job/{id}/lease/acknowledge Job.Lease.acknowledge",
		"job.leaseAcquire POST /api/job/{id}/lease/acquire Job.Lease.acquire",
		"job.leaseDirect POST /api/job/{id}/lease/direct Job.Lease.direct",
		"job.leaseExpire POST /api/job/{id}/lease/expire Job.Lease.expire",
		"job.leaseExpireHolder POST /api/job/lease/expire-holder Job.Lease.expireHolder",
		"job.leaseHeartbeat POST /api/job/{id}/lease/heartbeat Job.Lease.heartbeat",
		"job.leaseRelease POST /api/job/{id}/lease/release Job.Lease.release",
		"job.leaseResetExpiries POST /api/job/{id}/lease/reset-expiries Job.Lease.resetExpiries",
		"job.retriesRecordAttempt POST /api/job/{id}/retries/record-attempt Job.Retries.recordAttempt",
		"job.statusTransition POST /api/job/{id}/status/transition Job.status.transition",
		"session.presenceBeat POST /api/session/{id}/presence/beat Session.Presence.beat",
		"session.presenceMiss POST /api/session/{id}/presence/miss Session.Presence.miss",
		"session.statusTransition POST /api/session/{id}/status/transition Session.status.transition",
	}
	if !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	slices.Sort(presenting)
	if wantPresenting := []string{
		"Job.Lease.acknowledge", "Job.Lease.acquire", "Job.Lease.direct", "Job.Lease.expire",
		"Job.Lease.heartbeat", "Job.Lease.release", "Job.Lease.resetExpiries",
		"Job.Retries.recordAttempt", "Job.status.transition",
	}; !slices.Equal(presenting, wantPresenting) {
		t.Errorf("operations that take the caller's preconditions %q, want Job's every one of a row, %q", presenting, wantPresenting)
	}
	var tables []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil && b.Table {
			tables = append(tables, b.Type+"."+b.Use+"."+b.Operation+" "+b.KeyField)
		}
	}
	if want := []string{"Job.Lease.expireHolder "}; !slices.Equal(tables, want) {
		t.Errorf("operations of a table as a whole %q, want %q, which takes no key", tables, want)
	}
	if !output.HasTableBehaviorOperations() {
		t.Error("the crate has no operation of a table as a whole, which Lease's expireHolder is")
	}
	if len(output.ImplementedEndpoints()) != 0 {
		t.Errorf("implemented endpoints %d, want none: every operation is a behavior's", len(output.ImplementedEndpoints()))
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 2 {
		t.Fatalf("workers %+v and behavior workers %+v, want JobDeadlines and SessionPresence behavior workers alone", output.Workers, output.BehaviorWorkers)
	}
	for i, want := range []struct {
		name, table, subject string
		permissions          []string
		queue                BehaviorQueue
	}{
		{"JobDeadlines", "Job", "fixture-agents-api/JobDeadlines", nil, BehaviorQueue{Queue: "JobDeadline", Const: "JOB_DEADLINE_QUEUE", Kind: ir.BehaviorQueueDeadlines, Func: "job_deadlines_deadlines", Handler: "run_job_deadlines_deadline"}},
		{"SessionPresence", "Session", "fixture-agents-api/SessionPresence", []string{"jobs.override"}, BehaviorQueue{Queue: "SessionDeadline", Const: "SESSION_DEADLINE_QUEUE", Kind: ir.BehaviorQueueDeadlines, Func: "session_presence_deadlines", Handler: "run_session_presence_deadline"}},
	} {
		w := output.BehaviorWorkers[i]
		if w.Name != want.name || w.Behavior.Table != want.table || w.Behavior.Subject != want.subject || !slices.Equal(w.Behavior.Permissions, want.permissions) {
			t.Errorf("behavior worker %d is %+v (%+v), want %s on %s as %s holding %v", i, w, w.Behavior, want.name, want.table, want.subject, want.permissions)
		}
		if !slices.Equal(w.BehaviorQueues, []BehaviorQueue{want.queue}) {
			t.Errorf("%s's queues %+v, want %+v", w.Name, w.BehaviorQueues, want.queue)
		}
	}

	generated := writeGoldenAPI(t, agentsFixture.api, output)
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-rules-adapter = { path = "../../../../runtime/engine/rust/rules-adapter", features = ["postgres"] }`,
			`schemas-fixture-agents-db-types = { path = "../../types/rust/fixture-agents-db" }`,
		},
		"src/lib.rs": {"pub mod behaviors;", "pub use schemas_fixture_agents_db_types as database_types;"},
		"src/router.rs": {
			"behaviors::run_operation_with_preconditions(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Job\",\n        &args.id,\n        \"Lease\",\n        \"heartbeat\",\n        &args.input,",
			"behaviors::run_operation_with_preconditions(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Job\",\n        &args.id,\n        \"status\",\n        \"transition\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Session\",\n        &args.id,\n        \"Presence\",\n        \"beat\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Account\",\n        &args.id,\n        \"Budget\",\n        \"observe\",",
			"behaviors::run_table_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Job\",\n        \"Lease\",\n        \"expireHolder\",\n        &args.input,\n    )",
			`router = router.route("/api/job/lease/expire-holder", route);`,
		},
		"src/behaviors.rs": {
			"use serde_json::{Map, Value};",
			"DeadlineRun, Error, ExecuteOptions, Host, Layout,",
			"pub(crate) async fn run_operation_with_preconditions<K, P, R>(",
			"let ran = postgres::run_operation_with(",
			"pub(crate) async fn run_table_operation<P, R>(",
			"let ran = postgres::run_table_operation(",
			"        &ExecuteOptions::default(),",
			"fn presented(input: &mut Value) -> Map<String, Value> {",
			`Caller::new("fixture-agents-api/SessionPresence", ["jobs.override"])`,
			`Caller::new("fixture-agents-api/JobDeadlines", Vec::<String>::new())`,
			"pub async fn run_job_deadlines_deadline(",
			"pub async fn run_session_presence_deadline(",
			`"job_lease_directives"`,
			`"job_retries_attempts"`,
			`"account_budget_buckets"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	for _, permission := range []string{"jobs.override", "accounts.limits", "accounts.observe"} {
		if !strings.Contains(output.PermissionCatalogJSON, `"`+permission+`"`) {
			t.Errorf("the crate's permissions.json does not list %s", permission)
		}
	}
}

// TestAgentsAPICrateBuilds lints fixture-agents-api's crate with clippy,
// warnings denied: its routes of Job's Lease, Retries and status, which
// take the caller's preconditions, Session's and Account's, and its two
// behavior workers' queues and handlers, against the checkout's
// superschematic-rules-adapter and superschematic-rules-behaviors.
func TestAgentsAPICrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	crate := agentsFixture.build(t, false)
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// TestRustAgentsOnPostgres serves fixture-agents-api's build_router on a
// local socket, over a schema of its own in the Postgres at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL that holds fixture-agents-db's
// tables (D104): it creates its jobs, sessions, workspaces and accounts
// with the adapter's run_create, so Presence's create arms its miss, and
// updates them with its run_update, presenting the caller's preconditions
// in ExecuteOptions (a Rust server has no repository); calls the tables'
// behavior operations through the generated Rust SDK, a fencing token in
// an input's preconditions member, and Lease's expireHolder, a method of
// the table with no key (D104), as subjects that are fresh UUIDs in their
// base62 form; runs JobDeadlines' and SessionPresence's queues and
// handlers in process, a deadline made due with SQL as the Timers test
// makes one; and reads the columns and sub-tables with SQL: the cases
// every language's end-to-end test runs, by the same names, each held to
// have run. Its authenticator reads Bearer <subject>:<permission>,... . It
// skips without the database, without cargo, and in -short mode.
func TestRustAgentsOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's Lease, Retries, Presence and Budget against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's Lease, Retries, Presence and Budget against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's Lease, Retries, Presence and Budget")
	}
	crate := agentsFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(agentsFixture.api), "-", "_"),
	).Replace(agentsRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_agents.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	out := cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_agents", "--", "--nocapture")
	for _, name := range agentsCases {
		if !strings.Contains(out, "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// agentsRoundtripTest is tests/behavior_agents.rs of fixture-agents-api's
// API crate, beside fixture-agents-db's DDL; agentsRoundtripCases holds
// its cases.
const agentsRoundtripTest = agentsRoundtripHead + agentsRoundtripCases
