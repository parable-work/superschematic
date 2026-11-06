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

// The pool fixture (D104): fixture-pool-api serves AccountActions,
// @behaviorOperations of Account for any signed-in caller, and runs
// AccountRechecks, @behaviorWorker(Account) holding accounts.pool, over
// fixture-pool-db, its one DB dependency. Its Account implements Pool (a
// person's accounts at two vendors and paid keys, ranked by rank, out
// thirty minutes when a provider names no time, rechecked every two
// minutes, markOut and reportRoom behind accounts.pool) and Budget (tokens
// metered by the hour, observe behind accounts.observe) on the same table.
// Pool's pick runs on the table as a whole, at account/pool/pick, a command
// without a row; each recheck is an event of Account's event queue, which
// AccountRechecks hands to the project's Workers method
// account_rechecks_pool_recheck.
var poolFixture = behaviorFixture{api: "fixture-pool-api", db: "fixture-pool-db"}

// poolCases are the cases every language's end-to-end test of the pool
// fixture runs, by the same names, each of which the Rust test prints as it
// starts it; the TypeScript test's MCP case is its own.
var poolCases = []string{
	"a pick answers the open account lowest by rank among an owner's accounts",
	"a null member of match or prefer is absent, so a pick keeps an owner's accounts whatever their rank",
	"prefer tries a vendor's open accounts, then its near ones, then the others by rank",
	"markOut puts an account out for thirty minutes and arms its recheck at outUntil",
	"markOut keeps the later until, and a repeat answers unchanged",
	"a check with room before outUntil leaves the account out",
	"with every account out, pick answers no member and the soonest outUntil",
	"a due recheck hands the handler its event once, whose check with room opens the account",
	"a recheck whose check finds no room puts the account out until resetsAt and arms the recheck there",
	"an account whose Budget meter has no room counts as out until the provider's figure lapses",
	"markOut and reportRoom without accounts.pool are forbidden",
	"reportRoom of a key no row holds is not found",
}

// poolDeps are fixture-pool-api's Deps as the dispatch layer sets them: a
// pool of fixture-pool-db, whose Rust types the crate re-exports for its
// worker's queues, and the layout and the crates its behaviors run with.
func poolDeps(t *testing.T) (*APIOutput, DepsInfo) {
	t.Helper()
	_, db, reg := poolFixture.load(t)
	output := poolFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = poolFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(poolFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + poolFixture.db
	return output, deps
}

// TestWriteRustAPIGoldenPool pins the crate of fixture-pool-api (D104): the
// router mounts each operation of Account's Pool and Budget, which the table
// implements, each run through the behaviors' core; Pool's markOut and
// reportRoom under account/{id}/pool with run_operation, and its pick at
// account/pool/pick, a route with no key whose handler calls
// run_table_operation, a command without a row; the behavior worker
// AccountRechecks runs Account's deadline queue and its event queue, the
// second of which hands each recheck event to the Workers method
// account_rechecks_pool_recheck with an EventRun of the transaction that
// spends its message, as the worker holding accounts.pool; Deps declares
// Workers with that method, which the scaffold stubs. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenPool -update
func TestWriteRustAPIGoldenPool(t *testing.T) {
	output, deps := poolDeps(t)
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors, tables []string
	for _, endpoint := range output.Endpoints {
		b := endpoint.Behavior
		if b == nil {
			continue
		}
		behaviors = append(behaviors, endpoint.Namespace+"."+endpoint.Name+" "+strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		if b.Table {
			tables = append(tables, b.Type+"."+b.Use+"."+b.Operation+" "+b.KeyField)
		}
		if b.Preconditions {
			t.Errorf("%s.%s takes the caller's preconditions, and no behavior of Account takes one", b.Use, b.Operation)
		}
	}
	slices.Sort(behaviors)
	if want := []string{
		"account.budgetCheckReserve POST /api/account/{id}/budget/check-reserve Account.Budget.checkReserve",
		"account.budgetObserve POST /api/account/{id}/budget/observe Account.Budget.observe",
		"account.budgetRecordUsage POST /api/account/{id}/budget/record-usage Account.Budget.recordUsage",
		"account.budgetRecordUsageFor POST /api/account/{id}/budget/record-usage-for Account.Budget.recordUsageFor",
		"account.budgetReserve POST /api/account/{id}/budget/reserve Account.Budget.reserve",
		"account.budgetReserveFor POST /api/account/{id}/budget/reserve-for Account.Budget.reserveFor",
		"account.budgetSetLimit POST /api/account/{id}/budget/set-limit Account.Budget.setLimit",
		"account.budgetSettle POST /api/account/{id}/budget/settle Account.Budget.settle",
		"account.budgetSettleFor POST /api/account/{id}/budget/settle-for Account.Budget.settleFor",
		"account.poolMarkOut POST /api/account/{id}/pool/mark-out Account.Pool.markOut",
		"account.poolPick POST /api/account/pool/pick Account.Pool.pick",
		"account.poolReportRoom POST /api/account/{id}/pool/report-room Account.Pool.reportRoom",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if want := []string{"Account.Pool.pick "}; !slices.Equal(tables, want) {
		t.Errorf("operations of a table as a whole %q, want %q, which takes no key", tables, want)
	}
	if len(output.ImplementedEndpoints()) != 0 {
		t.Errorf("implemented endpoints %d, want none: every operation is a behavior's", len(output.ImplementedEndpoints()))
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 1 {
		t.Fatalf("workers %+v and behavior workers %+v, want the AccountRechecks behavior worker alone", output.Workers, output.BehaviorWorkers)
	}
	w := output.BehaviorWorkers[0]
	if w.Name != "AccountRechecks" || w.Behavior.Table != "Account" || w.Behavior.Subject != "fixture-pool-api/AccountRechecks" || !slices.Equal(w.Behavior.Permissions, []string{"accounts.pool"}) {
		t.Errorf("behavior worker %+v (%+v), want AccountRechecks on Account as fixture-pool-api/AccountRechecks holding accounts.pool", w, w.Behavior)
	}
	wantQueues := []BehaviorQueue{
		{Queue: "AccountDeadline", Const: "ACCOUNT_DEADLINE_QUEUE", Kind: ir.BehaviorQueueDeadlines, Func: "account_rechecks_deadlines", Handler: "run_account_rechecks_deadline"},
		{Queue: "AccountEvent", Const: "ACCOUNT_EVENT_QUEUE", Kind: ir.BehaviorQueueEvents, Func: "account_rechecks_events", Handler: "run_account_rechecks_event"},
	}
	if !slices.Equal(w.BehaviorQueues, wantQueues) {
		t.Errorf("AccountRechecks' queues\n%+v, want\n%+v", w.BehaviorQueues, wantQueues)
	}
	handlers := output.EventHandlers()
	if len(handlers) != 1 || handlers[0].Fn != "account_rechecks_pool_recheck" || handlers[0].Event != "AccountPoolRecheckEvent" || handlers[0].Use != "Pool" || handlers[0].Kind != "recheck" || handlers[0].Queue != "AccountEvent" {
		t.Errorf("event handlers %+v, want account_rechecks_pool_recheck of Pool's recheck on AccountEvent", handlers)
	}
	if !output.HasWorkers() || !output.HasTableBehaviorOperations() {
		t.Error("the crate declares no Workers, whose method takes Account's recheck events, or no operation of a table as a whole, which pick is")
	}

	generated := writeGoldenAPI(t, poolFixture.api, output)
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-rules-adapter = { path = "../../../../runtime/engine/rust/rules-adapter", features = ["postgres"] }`,
			`schemas-fixture-pool-db-types = { path = "../../types/rust/fixture-pool-db" }`,
		},
		"src/lib.rs": {"pub mod behaviors;", "pub use deps::{BoxError, Deps, Handlers, Workers};"},
		"src/deps.rs": {
			"pub trait Workers: Send + Sync {",
			"    async fn account_rechecks_pool_recheck(\n        &self,\n        event: crate::types::AccountPoolRecheckEvent,\n        run: &crate::behaviors::EventRun<'_>,\n    ) -> Result<(), BoxError>;",
		},
		"src/router.rs": {
			"behaviors::run_table_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Account\",\n        \"Pool\",\n        \"pick\",\n        &args.input,\n    )",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Account\",\n        &args.id,\n        \"Pool\",\n        \"markOut\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Account\",\n        &args.id,\n        \"Pool\",\n        \"reportRoom\",",
			`router = router.route("/api/account/pool/pick", route);`,
			"// Account's Pool pick, a Pool (D104): the behaviors' core\n    // runs it on the table as a whole, a command without a row, on a",
		},
		"src/operations.rs": {
			"/// account.poolPick (`POST /api/account/pool/pick`), which the behaviors'\n/// core runs: Account's Pool pick, a Pool, on the table as a\n/// whole (D104).",
		},
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			"pub(crate) async fn run_table_operation<P, R>(",
			"let ran = postgres::run_table_operation(",
			"Caller::new(\"fixture-pool-api/AccountRechecks\", [\"accounts.pool\"])",
			"pub fn account_rechecks_deadlines(",
			"pub fn account_rechecks_events(",
			"pub async fn run_account_rechecks_event(",
			"pub struct EventRun<'a> {",
			"let spent = postgres::spend_event(&host, &*tx, \"Account\", Some(&claim)).await?;",
			"(\"Pool\", \"recheck\") => {",
			"workers.account_rechecks_pool_recheck(typed, run).await",
			"behavior worker AccountRechecks: no Workers method takes event {kind} of {use_id} on Account {} (D104)",
			`"account_event_queue"`,
			`"account_budget_meters"`,
			`"pool_availability"`,
			`"table": true`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	if strings.Contains(generated["src/behaviors.rs"], "run_operation_with_preconditions") {
		t.Error("src/behaviors.rs has run_operation_with_preconditions, and no operation of Account takes the caller's preconditions")
	}
	for _, permission := range []string{"accounts.pool", "accounts.observe"} {
		if !strings.Contains(output.PermissionCatalogJSON, `"`+permission+`"`) {
			t.Errorf("the crate's permissions.json does not list %s", permission)
		}
	}
}

// TestPoolImplementationScaffoldStubsTheEventHandler: the scaffold of
// fixture-pool-api's implementation (D104) has create_workers, whose Workers
// stub the recheck event's method, failing every event until it is written.
func TestPoolImplementationScaffoldStubsTheEventHandler(t *testing.T) {
	output, deps := poolDeps(t)
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "impl")
	wrote, err := WriteImplementationScaffold(output, dir, "/repo/schemas/dist/api/"+poolFixture.api, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, naming.Default())
	if err != nil || !wrote {
		t.Fatalf("write the scaffold: %v, %v", wrote, err)
	}
	lib, err := os.ReadFile(filepath.Join(dir, ImplementationLib))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"use schemas_fixture_pool_api_api::behaviors::EventRun;",
		"pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {",
		"    async fn account_rechecks_pool_recheck(\n        &self,\n        _event: types::AccountPoolRecheckEvent,\n        _run: &EventRun<'_>,\n    ) -> Result<(), BoxError> {",
		`Err("event handler AccountRechecksPoolRecheck is not implemented".into())`,
	} {
		if !strings.Contains(string(lib), want) {
			t.Errorf("the scaffold's src/lib.rs does not contain %q:\n%s", want, lib)
		}
	}
}

// TestPoolAPICrateBuilds lints fixture-pool-api's crate with clippy,
// warnings denied: its routes of Account's Pool and Budget, pick's of the
// table as a whole among them, and its behavior worker's two queues and
// handlers, the event queue's EventRun and dispatcher among them, against
// the checkout's superschematic-rules-adapter and
// superschematic-rules-behaviors.
func TestPoolAPICrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	crate := poolFixture.build(t, false)
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// TestRustPoolOnPostgres serves fixture-pool-api's build_router on a local
// socket, over a schema of its own in the Postgres at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL that holds fixture-pool-db's tables
// (D104): it creates its accounts with the adapter's run_create, each case
// for an owner of its own, a fresh UUID, which pick's match keeps; calls
// Account's behavior operations through the generated Rust SDK, pick a
// method of the table with no key, as subjects that are fresh UUIDs in
// their base62 form; runs AccountRechecks' two queues and handlers in
// process, with a Workers of its own whose event handler is the project's
// code: it records each event, asks a fake provider for the account's room
// and reports it with reportRoom through the EventRun's operation, as the
// worker, once per account and dueAt; lets a recheck fall due at its
// outUntil; and reads the columns and the deadline queue with SQL: the
// cases every language's end-to-end test runs, by the same names, each held
// to have run. Its authenticator reads Bearer <subject>:<permission>,... .
// It skips without the database, without cargo, and in -short mode.
func TestRustPoolOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's Pool against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's Pool against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's Pool")
	}
	crate := poolFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(poolFixture.api), "-", "_"),
	).Replace(poolRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_pool.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	out := cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_pool", "--", "--nocapture")
	for _, name := range poolCases {
		if !strings.Contains(out, "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// poolRoundtripTest is tests/behavior_pool.rs of fixture-pool-api's API
// crate, beside fixture-pool-db's DDL; poolRoundtripCases holds its cases.
const poolRoundtripTest = poolRoundtripHead + poolRoundtripCases
