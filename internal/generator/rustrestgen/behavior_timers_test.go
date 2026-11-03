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

// The Timers fixture (D103): fixture-timers-api serves TicketActions,
// @behaviorOperations(Ticket) behind tickets.write, and runs TicketTimers,
// @behaviorWorker(Ticket, { concurrency: 2 }), over fixture-timers-db, its
// one DB dependency, whose Ticket implements Timers beside its status, a
// Workflow, and keeps its deadlines in ticket_timers_deadlines, armed as
// messages of ticket_deadline_queue.
var timersFixture = behaviorFixture{api: "fixture-timers-api", db: "fixture-timers-db"}

// timersDeps are fixture-timers-api's Deps as the dispatch layer sets them:
// a pool of fixture-timers-db, whose Rust types the crate re-exports for
// its worker's queue, and the layout and the crates its behaviors run with.
func timersDeps(t *testing.T) (*APIOutput, DepsInfo) {
	t.Helper()
	_, db, reg := timersFixture.load(t)
	output := timersFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = timersFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(timersFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + timersFixture.db
	return output, deps
}

// TestWriteRustAPIGoldenTimers pins the crate of fixture-timers-api
// (D103): the router mounts each operation of Ticket's Timers, which the
// table implements, under timers (fire-timer, reset-timer, snooze-timer)
// beside its status's transition, each run through the behaviors' core;
// the behavior worker TicketTimers has no method of the Workers trait, and
// deps.rs declares none, while src/behaviors.rs holds its two queues,
// deadlines first, each opened on a pool as one its handler completes
// itself, and their handlers, which run each claimed deadline through the
// adapter's run_deadline and each claimed change through its run_change
// as the worker; and the crate re-exports fixture-timers-db's Rust types,
// whose TicketDeadline and TicketChange are the queues' messages.
// Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenTimers -update
func TestWriteRustAPIGoldenTimers(t *testing.T) {
	output, deps := timersDeps(t)
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil {
			behaviors = append(behaviors, endpoint.Namespace+"."+endpoint.Name+" "+strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		}
	}
	if want := []string{
		"ticket.statusTransition POST /api/ticket/{id}/status/transition Ticket.status.transition",
		"ticket.timersFireTimer POST /api/ticket/{id}/timers/fire-timer Ticket.Timers.fireTimer",
		"ticket.timersResetTimer POST /api/ticket/{id}/timers/reset-timer Ticket.Timers.resetTimer",
		"ticket.timersSnoozeTimer POST /api/ticket/{id}/timers/snooze-timer Ticket.Timers.snoozeTimer",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations %q, want %q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 1 {
		t.Fatalf("workers %+v and behavior workers %+v, want TicketTimers a behavior worker alone", output.Workers, output.BehaviorWorkers)
	}
	w := output.BehaviorWorkers[0]
	if w.Name != "TicketTimers" || w.Method != "" || w.Concurrency != 2 || w.Behavior.Table != "Ticket" ||
		w.Behavior.Subject != "fixture-timers-api/TicketTimers" || !slices.Equal(w.Behavior.Queues, []string{"TicketDeadline", "TicketChange"}) {
		t.Errorf("TicketTimers is %+v (%+v)", w, w.Behavior)
	}
	if q := w.BehaviorQueues; !slices.Equal(q, []BehaviorQueue{
		{Queue: "TicketDeadline", Const: "TICKET_DEADLINE_QUEUE", Kind: ir.BehaviorQueueDeadlines, Func: "ticket_timers_deadlines", Handler: "run_ticket_timers_deadline"},
		{Queue: "TicketChange", Const: "TICKET_CHANGE_QUEUE", Kind: ir.BehaviorQueueChanges, Func: "ticket_timers_changes", Handler: "run_ticket_timers_change"},
	}) {
		t.Errorf("TicketTimers' queues %+v", q)
	}
	if output.HasWorkers() || output.HasBackgroundWork() {
		t.Error("the crate declares Workers for a behavior worker, whose handler the generator writes")
	}

	generated := writeGoldenAPI(t, timersFixture.api, output)
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-rules-adapter = { path = "../../../../runtime/engine/rust/rules-adapter", features = ["postgres"] }`,
			`schemas-fixture-timers-db-types = { path = "../../types/rust/fixture-timers-db" }`,
		},
		"src/lib.rs": {"pub mod behaviors;", "pub use schemas_fixture_timers_db_types as database_types;"},
		"src/router.rs": {
			"\"Ticket\",\n        &args.id,\n        \"Timers\",\n        \"fireTimer\",",
		},
		"src/behaviors.rs": {
			"use crate::database_types::{\n    TicketChange, TicketDeadline, TICKET_CHANGE_QUEUE, TICKET_DEADLINE_QUEUE,\n};",
			`Caller::new("fixture-timers-api/TicketTimers", Vec::<String>::new())`,
			"pub fn ticket_timers_deadlines(\n    pool: &Pool,\n) -> Result<SelfCompleting<PostgresQueue<TicketDeadline>>, BoxError> {",
			"pub async fn run_ticket_timers_deadline(",
			"let run = postgres::run_deadline(",
			"Some(&claim),",
			"pub fn ticket_timers_changes(\n    pool: &Pool,\n) -> Result<SelfCompleting<PostgresQueue<TicketChange>>, BoxError> {",
			"pub async fn run_ticket_timers_change(",
			"let run = postgres::run_change(",
			"fn change_of<M: Serialize>(message: &M) -> Result<TableChange, BoxError> {",
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	for file, absent := range map[string]string{
		"src/deps.rs":      "pub trait Workers",
		"src/lib.rs":       "Workers",
		"src/behaviors.rs": "fn handle_",
	} {
		if strings.Contains(generated[file], absent) {
			t.Errorf("%s holds %s, which a behavior worker has none of", file, absent)
		}
	}
	if !strings.Contains(output.PermissionCatalogJSON, `"tickets.timers"`) {
		t.Error("the crate's permissions.json does not list tickets.timers, Timers' config permission")
	}
}

// TestABehaviorWorkerAloneNeedsNoCreateWorkers: an API whose only worker is
// a behavior worker (D103) declares no Workers, so neither its
// implementation's scaffold nor its routes' scaffolds hold a method for it
// or a create_workers.
func TestABehaviorWorkerAloneNeedsNoCreateWorkers(t *testing.T) {
	output, deps := timersDeps(t)
	if err := output.SetDeps(deps); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "impl")
	paths := naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}
	if _, err := WriteImplementationScaffold(output, dir, "/repo/schemas/dist/api/"+timersFixture.api, paths, naming.Default()); err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile(filepath.Join(dir, ImplementationLib))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{WorkersFunc, "Workers", "ticket_timers", "TicketDeadline", "TicketChange"} {
		if strings.Contains(string(lib), absent) {
			t.Errorf("the implementation's scaffold holds %s, which the behavior worker's generated handler serves:\n%s", absent, lib)
		}
	}
	// A behavior worker's Deps name the layout and the crates it runs
	// with, as behavior operations' do.
	deps.Behaviors = nil
	output, _ = timersDeps(t)
	if err := output.SetDeps(deps); err == nil || !strings.Contains(err.Error(), "and its Deps names neither") {
		t.Errorf("SetDeps without the behaviors' layout: %v", err)
	}
}

// TestTimersAPICrateBuilds lints fixture-timers-api's crate with clippy,
// warnings denied: its routes of Ticket's Timers and its behavior worker's
// queue and handler against the checkout's superschematic-rules-adapter
// and the HTTP runtime's queue and claim loop.
func TestTimersAPICrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	crate := timersFixture.build(t, false)
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// TestRustTimersOperationsAndWorkerOnPostgres serves fixture-timers-api's
// build_router on a local socket, over a schema of its own in the Postgres
// at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL that holds fixture-timers-db's
// tables and their change triggers, creates its rows with the adapter's
// run_create, calls Ticket's status and timers operations through the
// generated Rust SDK, and runs its behavior worker's queues and handlers in
// process (D103): the cases every language's end-to-end test runs, by the
// same names, among them a raw SQL write of a field start, a raw SQL
// delete and a bulk update, each heard through Ticket's change queue. Its
// authenticator reads Bearer <subject>:<permission>,... . It skips without
// the database, without cargo, and in -short mode.
func TestRustTimersOperationsAndWorkerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's timers and worker against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's timers and worker against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's timers and worker")
	}
	crate := timersFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(timersFixture.api), "-", "_"),
	).Replace(timersRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_timers.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_timers", "--", "--nocapture")
}

// timersRoundtripTest is tests/behavior_timers.rs of fixture-timers-api's
// API crate, beside fixture-timers-db's DDL. The only real wait is
// respond's 1.5 seconds; every other deadline is an hour or more out, and
// the one a case needs due is made due with SQL, an operator's escape
// hatch. A write the change queue hears is waited for by running the
// worker's queues and polling every 100 ms for at most ten seconds.
const timersRoundtripTest = `//! Ticket's Timers end to end (D103): fixture-timers-api's router over a
//! schema of its own in the Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL,
//! which holds fixture-timers-db's tables, called through the generated Rust
//! SDK, and its behavior worker TicketTimers' queues and handlers run in
//! process: its deadlines and the writes its change queue hears, a raw SQL
//! write's among them.

use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use API_CRATE::behaviors::{
    run_ticket_timers_change, run_ticket_timers_deadline, ticket_timers_changes, ticket_timers_deadlines, RULES_LAYOUT,
};
use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::queue::QueueError;
use API_CRATE::runtime::worker::{run_worker, Options, WorkerQueue};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, Shutdown};
use API_CRATE::{build_router, types, Handlers, Implementations};
use SDK_CRATE::{ClientConfig, FixtureTimersApiSdk, SDKError};
use async_trait::async_trait;
use axum::http::request::Parts;
use serde::de::DeserializeOwned;
use serde_json::{json, Value};
use superschematic_rules_adapter::{postgres, Caller, CreateOptions, ExecuteOptions, Host, Layout};

/// Reads Bearer <subject>:<permission>,... : that subject, holding those
/// permissions; no header, no caller.
struct Tokens;

#[async_trait]
impl Authenticator for Tokens {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(bearer_token(&request.headers).map(|token| {
            let (subject, permissions) = token.split_once(':').unwrap_or((token, ""));
            Principal::new(subject, permissions.split(',').filter(|p| !p.is_empty()))
        }))
    }
}

/// A schema of its own holding fixture-timers-db's tables, a pool whose
/// connections search it, and the connection that drops it.
struct Database {
    pool: Pool,
    client: tokio_postgres::Client,
    admin: tokio_postgres::Client,
    schema: String,
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

async fn database() -> Database {
    let url = std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL").expect("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL names the database");
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(&config).await;
    if let Err(err) = admin.batch_execute("CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public").await {
        // Two tests creating the extension at once: the other won.
        assert_eq!(err.code().map(|code| code.code()), Some("23505"), "create pgcrypto: {err}");
    }
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let schema = format!("behavior_timers_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("behaviors_postgres.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(8).build().unwrap();
    Database { pool, client, admin, schema }
}

/// A deadline's sub-table row as the table holds it: when it falls due and
/// when it starts from, in epoch milliseconds, whether it fired, and what
/// its then met.
#[derive(Debug, PartialEq)]
struct SubRow {
    due_at: Option<i64>,
    base_at: Option<i64>,
    fired: Option<bool>,
    outcome: Option<String>,
}

/// A live message of the deadline queue: its deadline's name, the time it
/// was armed at, its due time in epoch milliseconds, and its state.
#[derive(Debug, PartialEq)]
struct Message {
    name: String,
    at: i64,
    due_at: i64,
    state: String,
}

impl Database {
    async fn drop_schema(&self) {
        self.admin.batch_execute(&format!("DROP SCHEMA {} CASCADE", self.schema)).await.unwrap();
    }

    /// Creates a ticket through the core, as the repository's create does,
    /// and answers its key and its column's text.
    async fn create(&self, title: &str) -> (types::IdentityUUID, String) {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let own = json!({ "title": title }).as_object().cloned().unwrap();
        let key = postgres::run_create(&host, &*tx, "Ticket", own, tester(), &CreateOptions::default()).await.unwrap();
        tx.commit().await.unwrap();
        let text: String = self.client.query_one("SELECT id::text FROM ticket WHERE title = $1", &[&title]).await.unwrap().get(0);
        (serde_json::from_value(json!(key)).unwrap(), text)
    }

    /// Creates a ticket with a visit at visit_at, in epoch milliseconds,
    /// through the core, and answers its key and its column's text.
    async fn create_visit(&self, title: &str, visit_at: i64) -> (types::IdentityUUID, String) {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let own = json!({ "title": title, "visitAt": visit_at }).as_object().cloned().unwrap();
        let key = postgres::run_create(&host, &*tx, "Ticket", own, tester(), &CreateOptions::default()).await.unwrap();
        tx.commit().await.unwrap();
        let text: String = self.client.query_one("SELECT id::text FROM ticket WHERE title = $1", &[&title]).await.unwrap().get(0);
        (serde_json::from_value(json!(key)).unwrap(), text)
    }

    /// Deletes a ticket through the core, as the repository's delete does.
    async fn delete(&self, id: &types::IdentityUUID) {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        postgres::run_delete(&host, &*tx, "Ticket", &wire(id), tester(), &ExecuteOptions::default()).await.unwrap();
        tx.commit().await.unwrap();
    }

    async fn status(&self, ticket: &str) -> (String, i64) {
        let row = self
            .client
            .query_one("SELECT status, (extract(epoch FROM status_entered_at) * 1000)::bigint FROM ticket WHERE id::text = $1", &[&ticket])
            .await
            .unwrap();
        (row.get(0), row.get(1))
    }

    async fn sub_row(&self, ticket: &str, name: &str) -> Option<SubRow> {
        self.client
            .query_opt(
                "SELECT (extract(epoch FROM due_at) * 1000)::bigint, (extract(epoch FROM base_at) * 1000)::bigint, fired, outcome FROM ticket_timers_deadlines WHERE ticket_id::text = $1 AND name = $2",
                &[&ticket, &name],
            )
            .await
            .unwrap()
            .map(|row| SubRow { due_at: row.get(0), base_at: row.get(1), fired: row.get(2), outcome: row.get(3) })
    }

    async fn live(&self, ticket: &str) -> Vec<Message> {
        self.client
            .query(
                "SELECT name, at, (extract(epoch FROM due_at) * 1000)::bigint, state FROM ticket_deadline_queue WHERE row_key::text = $1 AND state IN ('ready', 'claimed') ORDER BY name",
                &[&ticket],
            )
            .await
            .unwrap()
            .iter()
            .map(|row| Message { name: row.get(0), at: row.get(1), due_at: row.get(2), state: row.get(3) })
            .collect()
    }

    async fn messages(&self, ticket: &str) -> i64 {
        self.client.query_one("SELECT count(*) FROM ticket_deadline_queue WHERE row_key::text = $1", &[&ticket]).await.unwrap().get(0)
    }
}

fn tester() -> Caller {
    Caller::new("tester", Vec::<String>::new())
}

/// TicketTimers' two queues, its deadlines and its changes, each in a claim
/// loop of its own, as its main runs them, until shutdown.
fn start_worker(pool: &Pool, shutdown: &Shutdown) -> Vec<tokio::task::JoinHandle<Result<(), QueueError>>> {
    let options = Options { concurrency: 2, idle: Duration::from_millis(100), ..Options::default() };
    let deadlines = ticket_timers_deadlines(pool).unwrap();
    let changes = ticket_timers_changes(pool).unwrap();
    let (on_deadlines, on_changes) = (pool.clone(), pool.clone());
    vec![
        tokio::spawn(run_worker(
            deadlines,
            move |claimed| {
                let pool = on_deadlines.clone();
                async move { run_ticket_timers_deadline(&pool, claimed).await }
            },
            options,
            shutdown.clone(),
        )),
        tokio::spawn(run_worker(
            changes,
            move |claimed| {
                let pool = on_changes.clone();
                async move { run_ticket_timers_change(&pool, claimed).await }
            },
            options,
            shutdown.clone(),
        )),
    ]
}

/// Polls holds every 100 ms until it holds, failing after ten seconds.
async fn wait_until<F, Fut>(what: &str, mut holds: F)
where
    F: FnMut() -> Fut,
    Fut: std::future::Future<Output = bool>,
{
    let started = Instant::now();
    while !holds().await {
        assert!(started.elapsed() < Duration::from_secs(10), "{what} within 10 s");
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
}

async fn serve(pool: Pool) -> String {
    let implementations = Implementations::new(Handlers { authenticator: Arc::new(Tokens) }, pool);
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, build_router(implementations)).await.unwrap() });
    base_url
}

fn sdk(base_url: &str, token: Option<&str>) -> FixtureTimersApiSdk {
    FixtureTimersApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), token.map(str::to_string), None)).unwrap()
}

/// The key's wire form, as the core names a row.
fn wire(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_string()
}

/// A value of a generated type from its JSON.
fn of<T: DeserializeOwned>(value: Value) -> T {
    serde_json::from_value(value).unwrap()
}

/// The status, code and message of a refused call.
fn refusal(err: SDKError) -> (u16, String, String) {
    match err {
        SDKError::Api { status_code, message, code, .. } => (status_code, code.unwrap_or_default(), message),
        other => panic!("not a refusal of the API: {other}"),
    }
}

fn now_ms() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as i64
}

const HOUR: i64 = 3_600_000;
const DAY: i64 = 24 * HOUR;

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn timers_run_through_the_core_and_the_worker() {
    let db = Arc::new(database().await);
    let outcome = tokio::spawn(cases(Arc::clone(&db))).await;
    db.drop_schema().await;
    if let Err(err) = outcome {
        match err.try_into_panic() {
            Ok(panic) => std::panic::resume_unwind(panic),
            Err(err) => panic!("the cases did not finish: {err}"),
        }
    }
}

async fn cases(db: Arc<Database>) {
    let base_url = serve(db.pool.clone()).await;
    let lee = sdk(&base_url, Some("lee:tickets.write"));
    let kim = sdk(&base_url, Some("kim:tickets.write,tickets.timers"));

    println!("a create arms the deadline: its sub-table row and its queue message with its due time");
    let (_, t1) = db.create("t1").await;
    let respond = db.sub_row(&t1, "respond").await.expect("respond's sub-table row");
    let base = respond.base_at.expect("respond starts from the create");
    assert_eq!(respond, SubRow { due_at: Some(base + 1500), base_at: Some(base), fired: Some(false), outcome: None });
    assert_eq!(db.sub_row(&t1, "idle").await, None);
    assert_eq!(db.live(&t1).await, [Message { name: "respond".into(), at: base + 1500, due_at: base + 1500, state: "ready".into() }]);

    println!("a due deadline the worker runs moves the status and records the outcome");
    let shutdown = Shutdown::new();
    let pool = db.pool.clone();
    let queue = ticket_timers_deadlines(&db.pool).unwrap();
    let running = tokio::spawn(run_worker(
        queue,
        move |claimed| {
            let pool = pool.clone();
            async move { run_ticket_timers_deadline(&pool, claimed).await }
        },
        Options { concurrency: 2, idle: Duration::from_millis(100), ..Options::default() },
        shutdown.clone(),
    ));
    let started = Instant::now();
    while db.status(&t1).await.0 != "escalated" {
        assert!(started.elapsed() < Duration::from_secs(10), "the worker did not run respond within 10 s");
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    shutdown.cancel();
    running.await.unwrap().unwrap();
    let fired = db.sub_row(&t1, "respond").await.expect("respond's sub-table row");
    assert_eq!((fired.fired, fired.outcome.as_deref()), (Some(true), Some("moved")));
    assert!(db.live(&t1).await.is_empty(), "{:?}", db.live(&t1).await);

    println!("a status that leaves the state cancels the deadline by until");
    let (id3, t3) = db.create("t3").await;
    lee.ticket.status.transition(id3, of(json!({"to": "waiting"})), None).await.unwrap();
    let (status, entered) = db.status(&t3).await;
    assert_eq!(status, "waiting");
    let respond = db.sub_row(&t3, "respond").await.expect("respond's sub-table row");
    assert_eq!(respond.due_at, None);
    let idle = db.sub_row(&t3, "idle").await.expect("idle's sub-table row");
    assert_eq!(idle.due_at, Some(entered + HOUR));
    assert_eq!(db.live(&t3).await, [Message { name: "idle".into(), at: entered + HOUR, due_at: entered + HOUR, state: "ready".into() }]);

    println!("a re-armed deadline does not fire on its old arming");
    db.client
        .execute("UPDATE ticket_deadline_queue SET due_at = now() WHERE row_key::text = $1 AND name = 'idle'", &[&t3])
        .await
        .unwrap();
    let claims = ticket_timers_deadlines(&db.pool).unwrap().claim(4, Duration::from_secs(60)).await.unwrap();
    assert_eq!(claims.len(), 1, "idle alone is due");
    let claim = claims.into_iter().next().unwrap();
    assert_eq!(serde_json::to_value(&claim.message.message).unwrap()["name"], json!("idle"));
    let snoozed = now_ms() + 2 * HOUR;
    let moved = kim.ticket.timers.snooze_timer(id3, of(json!({"deadline": "idle", "dueAt": snoozed})), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"dueAt": snoozed}));
    run_ticket_timers_deadline(&db.pool, claim.message).await.unwrap();
    assert_eq!(db.status(&t3).await.0, "waiting");
    assert_eq!(db.sub_row(&t3, "idle").await.expect("idle's sub-table row").due_at, Some(snoozed));
    assert_eq!(db.live(&t3).await, [Message { name: "idle".into(), at: snoozed, due_at: snoozed, state: "ready".into() }]);

    println!("resetTimer and snoozeTimer are refused without the config's permission and run with it");
    let err = lee.ticket.timers.reset_timer(id3, of(json!({"deadline": "idle"})), None).await.unwrap_err();
    let (status, code, message) = refusal(err);
    assert_eq!((status, code.as_str()), (403, "forbidden"));
    assert_eq!(message, format!("lee may not reset deadline idle of Ticket {}: it needs permission tickets.timers", wire(&id3)));
    let err = lee.ticket.timers.snooze_timer(id3, of(json!({"deadline": "idle", "dueAt": snoozed + HOUR})), None).await.unwrap_err();
    assert_eq!(refusal(err).0, 403);
    assert_eq!(db.sub_row(&t3, "idle").await.expect("idle's sub-table row").due_at, Some(snoozed));
    let reset = kim.ticket.timers.reset_timer(id3, of(json!({"deadline": "idle"})), None).await.unwrap();
    let due: i64 = serde_json::to_value(&reset).unwrap()["dueAt"].as_i64().expect("dueAt");
    assert!(due > now_ms() + HOUR - 60_000 && due <= now_ms() + HOUR, "reset to {due}");
    assert_eq!(db.sub_row(&t3, "idle").await.expect("idle's sub-table row").due_at, Some(due));
    assert_eq!(db.live(&t3).await, [Message { name: "idle".into(), at: due, due_at: due, state: "ready".into() }]);
    let later = due + HOUR;
    kim.ticket.timers.snooze_timer(id3, of(json!({"deadline": "idle", "dueAt": later})), None).await.unwrap();
    assert_eq!(db.sub_row(&t3, "idle").await.expect("idle's sub-table row").due_at, Some(later));
    assert_eq!(db.live(&t3).await.iter().map(|m| m.at).collect::<Vec<_>>(), [later]);

    println!("fireTimer through the SDK fires a due deadline and spends its message");
    let (id6, t6) = db.create("t6").await;
    tokio::time::sleep(Duration::from_millis(1600)).await;
    let fired = lee.ticket.timers.fire_timer(id6, of(json!({"deadline": "respond"})), None).await.unwrap();
    assert_eq!(serde_json::to_value(&fired).unwrap(), json!({"fired": true, "outcome": "moved"}));
    assert_eq!(db.status(&t6).await.0, "escalated");
    assert!(db.live(&t6).await.is_empty(), "{:?}", db.live(&t6).await);

    println!("deleting the row removes its deadlines");
    db.delete(&id3).await;
    assert_eq!(db.sub_row(&t3, "idle").await, None);
    assert_eq!(db.sub_row(&t3, "respond").await, None);
    assert_eq!(db.messages(&t3).await, 0);

    // The worker runs both its queues from here on: the change queue hears
    // every write of a ticket, the core's and a raw one alike.
    let shutdown = Shutdown::new();
    let workers = start_worker(&db.pool, &shutdown);
    let base = now_ms() / 1000 * 1000;

    println!("a raw SQL update of a Timers field start re-arms its deadline");
    let (_, t7) = db.create_visit("t7", base + 2 * DAY).await;
    let remind = db.sub_row(&t7, "remind").await.expect("remind's sub-table row");
    assert_eq!(remind.due_at, Some(base + DAY));
    let moved = base + 3 * DAY;
    db.client.execute("UPDATE ticket SET visit_at = to_timestamp($2::bigint / 1000.0) WHERE id::text = $1", &[&t7, &moved]).await.unwrap();
    wait_until("t7's remind re-armed", || async {
        db.sub_row(&t7, "remind").await.and_then(|row| row.due_at) == Some(moved - DAY)
            && db.live(&t7).await.iter().any(|m| m.name == "remind" && m.at == moved - DAY)
    })
    .await;
    let reminds: Vec<i64> = db.live(&t7).await.iter().filter(|m| m.name == "remind").map(|m| m.at).collect();
    assert_eq!(reminds, [moved - DAY], "one live message of remind, at its new time");

    println!("a raw SQL delete of a ticket clears its deadline messages");
    let (_, t8) = db.create_visit("t8", base + 2 * DAY).await;
    assert!(!db.live(&t8).await.is_empty(), "t8 has live deadline messages");
    db.client.execute("DELETE FROM ticket WHERE id::text = $1", &[&t8]).await.unwrap();
    wait_until("t8's deadline messages cleared", || async { db.messages(&t8).await == 0 }).await;

    println!("a repository's bulk update of a field start re-arms its deadline");
    let (_, t9) = db.create_visit("t9", base + 2 * DAY).await;
    let (_, t10) = db.create_visit("t10", base + 2 * DAY).await;
    let bulk = base + 4 * DAY;
    let ids = vec![t9.clone(), t10.clone()];
    let changed = db.client.execute("UPDATE ticket SET visit_at = to_timestamp($1::bigint / 1000.0) WHERE id::text = ANY($2)", &[&bulk, &ids]).await.unwrap();
    assert_eq!(changed, 2);
    for ticket in [&t9, &t10] {
        wait_until("a bulk-updated ticket's remind re-armed", || async {
            db.sub_row(ticket, "remind").await.and_then(|row| row.due_at) == Some(bulk - DAY)
                && db.live(ticket).await.iter().any(|m| m.name == "remind" && m.at == bulk - DAY)
        })
        .await;
    }

    shutdown.cancel();
    for worker in workers {
        worker.await.unwrap().unwrap();
    }
}
`
