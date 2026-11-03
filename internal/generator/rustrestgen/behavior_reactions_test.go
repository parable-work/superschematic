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

// The Reactions fixture (D103): fixture-reactions-api serves ProjectActions
// and TaskActions, @behaviorOperations(Project) behind projects.write and
// @behaviorOperations(Task) behind tasks.write, and runs ProjectReactions
// and TaskReactions, @behaviorWorker(Project) and @behaviorWorker(Task, {
// concurrency: 2 }), over fixture-reactions-db, its one DB dependency, whose
// Project and Task each implement Reactions beside their status, a
// Workflow: a task that enters doing moves its project to active, one
// marked urgent starts, and a project is done once its tasks are all done
// with success and failed once one fails, each heard through the tables'
// change queues, project_change_queue and task_change_queue.
var reactionsFixture = behaviorFixture{api: "fixture-reactions-api", db: "fixture-reactions-db"}

// reactionsDeps are fixture-reactions-api's Deps as the dispatch layer sets
// them: a pool of fixture-reactions-db, whose Rust types the crate
// re-exports for its workers' queues, and the layout and the crates its
// behaviors run with.
func reactionsDeps(t *testing.T) (*APIOutput, DepsInfo) {
	t.Helper()
	_, db, reg := reactionsFixture.load(t)
	output := reactionsFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = reactionsFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(reactionsFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + reactionsFixture.db
	return output, deps
}

// TestRustReactionsWorkersRunTheirTablesChangeQueues: each behavior worker
// of fixture-reactions-api runs its table's change queue alone (D103), the
// tables arming no deadlines, so src/behaviors.rs holds, for each, the
// queue opened on a pool as one its handler completes itself and the
// handler, which runs each claimed change through the adapter's run_change
// as the worker, and reads no deadline; the crate re-exports
// fixture-reactions-db's Rust types, whose ProjectChange and TaskChange are
// the queues' messages.
func TestRustReactionsWorkersRunTheirTablesChangeQueues(t *testing.T) {
	output, deps := reactionsDeps(t)
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 2 {
		t.Fatalf("workers %+v and behavior workers %+v, want ProjectReactions and TaskReactions, behavior workers alone", output.Workers, output.BehaviorWorkers)
	}
	for i, want := range []struct {
		name, table, stem string
		heardBy           []string
	}{
		{"ProjectReactions", "Project", "project_reactions", nil},
		{"TaskReactions", "Task", "task_reactions", []string{"Project"}},
	} {
		w := output.BehaviorWorkers[i]
		if w.Name != want.name || w.Behavior.Table != want.table || w.Behavior.DeadlineQueue != "" ||
			!slices.Equal(w.Behavior.Hearing, []string{"Reactions"}) || !slices.Equal(w.Behavior.HeardBy, want.heardBy) {
			t.Errorf("%s is %+v (%+v)", want.name, w, w.Behavior)
		}
		queue := want.table + "Change"
		if q := w.BehaviorQueues; !slices.Equal(q, []BehaviorQueue{{
			Queue:   queue,
			Const:   strings.ToUpper(want.table) + "_CHANGE_QUEUE",
			Kind:    ir.BehaviorQueueChanges,
			Func:    want.stem + "_changes",
			Handler: "run_" + want.stem + "_change",
		}}) {
			t.Errorf("%s's queues %+v", want.name, q)
		}
	}
	if !output.HasChangeQueues() || output.HasDeadlineQueues() {
		t.Error("the workers run change queues alone")
	}
	dir := filepath.Join(t.TempDir(), "api", reactionsFixture.api)
	if err := SetReplacePaths(output, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", "behaviors.rs"))
	if err != nil {
		t.Fatal(err)
	}
	behaviors := string(data)
	for _, want := range []string{
		"use crate::database_types::{ProjectChange, TaskChange, PROJECT_CHANGE_QUEUE, TASK_CHANGE_QUEUE};",
		`Caller::new("fixture-reactions-api/TaskReactions", Vec::<String>::new())`,
		"pub fn project_reactions_changes(\n    pool: &Pool,\n) -> Result<SelfCompleting<PostgresQueue<ProjectChange>>, BoxError> {",
		"pub async fn run_task_reactions_change(",
		"let run = postgres::run_change(",
		"fn change_of<M: Serialize>(message: &M) -> Result<TableChange, BoxError> {",
		"the writes of its rows its behaviors and Project's hear",
	} {
		if !strings.Contains(behaviors, want) {
			t.Errorf("src/behaviors.rs does not contain %q:\n%s", want, behaviors)
		}
	}
	for _, absent := range []string{"deadline_of", "DeadlineRun", "run_deadline"} {
		if strings.Contains(behaviors, absent) {
			t.Errorf("src/behaviors.rs holds %s, and the workers run no deadline", absent)
		}
	}
}

// TestReactionsAPICrateBuilds lints fixture-reactions-api's crate with
// clippy, warnings denied: its routes of Project's and Task's statuses and
// its behavior workers' change queues and handlers against the checkout's
// superschematic-rules-adapter and the HTTP runtime's queue and claim loop.
func TestReactionsAPICrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	crate := reactionsFixture.build(t, false)
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// TestRustReactionsOperationsAndWorkerOnPostgres serves
// fixture-reactions-api's build_router on a local socket, over a schema of
// its own in the Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL that
// holds fixture-reactions-db's tables and their change triggers, creates
// its rows with the adapter's run_create, moves statuses through the
// generated Rust SDK and with raw SQL, and runs both behavior workers'
// queues and handlers in process (D103): the six Reactions cases every
// language's end-to-end test runs, by the same names. Its authenticator
// reads Bearer <subject>:<permission>,... . It skips without the database,
// without cargo, and in -short mode.
func TestRustReactionsOperationsAndWorkerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's reactions and workers against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's reactions and workers against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's reactions and workers")
	}
	crate := reactionsFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(reactionsFixture.api), "-", "_"),
	).Replace(reactionsRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_reactions.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_reactions", "--", "--nocapture")
}

// reactionsRoundtripTest is tests/behavior_reactions.rs of
// fixture-reactions-api's API crate, beside fixture-reactions-db's DDL. A
// rule acts once a worker runs the change that sets it off, so each case
// waits by polling every 100 ms for at most ten seconds while both
// workers' claim loops run.
const reactionsRoundtripTest = `//! Reactions end to end (D103): fixture-reactions-api's router over a
//! schema of its own in the Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL,
//! which holds fixture-reactions-db's tables and their change triggers,
//! called through the generated Rust SDK and written with raw SQL, and its
//! behavior workers ProjectReactions and TaskReactions run in process, each
//! its table's change queue.

use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use API_CRATE::behaviors::{
    project_reactions_changes, run_project_reactions_change, run_task_reactions_change, task_reactions_changes, RULES_LAYOUT,
};
use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::queue::QueueError;
use API_CRATE::runtime::worker::{run_worker, Options};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, Shutdown};
use API_CRATE::{build_router, types, Handlers, Implementations};
use SDK_CRATE::{ClientConfig, FixtureReactionsApiSdk};
use async_trait::async_trait;
use axum::http::request::Parts;
use serde::de::DeserializeOwned;
use serde_json::{json, Value};
use superschematic_rules_adapter::{postgres, Caller, CreateOptions, Host, Layout};

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

/// A schema of its own holding fixture-reactions-db's tables, a pool whose
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
    let schema = format!("behavior_reactions_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("behaviors_postgres.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(8).build().unwrap();
    Database { pool, client, admin, schema }
}

impl Database {
    async fn drop_schema(&self) {
        self.admin.batch_execute(&format!("DROP SCHEMA {} CASCADE", self.schema)).await.unwrap();
    }

    /// Creates a row of a type through the core, as a repository's create
    /// does, and answers its key and its column's text.
    async fn create(&self, type_name: &str, own: Value) -> (types::IdentityUUID, String) {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let own = own.as_object().cloned().unwrap();
        let key = postgres::run_create(&host, &*tx, type_name, own, tester(), &CreateOptions::default()).await.unwrap();
        tx.commit().await.unwrap();
        let id: types::IdentityUUID = serde_json::from_value(json!(key)).unwrap();
        let text = hyphenated(&id);
        (id, text)
    }

    /// A planned project.
    async fn project(&self, name: &str) -> (types::IdentityUUID, String) {
        self.create("Project", json!({ "name": name })).await
    }

    /// A todo task of a project.
    async fn task(&self, title: &str, project: &types::IdentityUUID) -> (types::IdentityUUID, String) {
        self.create("Task", json!({ "title": title, "projectId": wire(project) })).await
    }

    async fn status(&self, table: &str, id: &str) -> String {
        self.client
            .query_one(&format!("SELECT status FROM {table} WHERE id::text = $1"), &[&id])
            .await
            .unwrap()
            .get(0)
    }

    /// Waits until a row's status is want.
    async fn wait_status(&self, table: &str, id: &str, want: &str) {
        let started = Instant::now();
        loop {
            let status = self.status(table, id).await;
            if status == want {
                return;
            }
            assert!(started.elapsed() < Duration::from_secs(10), "{table} {id} is {status}, not {want}, after 10 s");
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    }

    /// The messages of a change queue: of one row, or every live one.
    async fn queued(&self, table: &str, row: Option<&str>) -> i64 {
        let row_key = row.unwrap_or_default();
        let text = format!("SELECT count(*) FROM {table} WHERE ($1 = '' AND state IN ('ready', 'claimed')) OR row_key::text = $1");
        self.client.query_one(&text, &[&row_key]).await.unwrap().get(0)
    }

    /// Waits until both change queues hold no live message: every write so
    /// far heard and run.
    async fn drained(&self) {
        let started = Instant::now();
        while self.queued("task_change_queue", None).await + self.queued("project_change_queue", None).await > 0 {
            assert!(started.elapsed() < Duration::from_secs(10), "the change queues did not drain within 10 s");
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    }
}

fn tester() -> Caller {
    Caller::new("tester", Vec::<String>::new())
}

/// The key's wire form, as the core names a row.
fn wire(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_string()
}

/// The key as its column holds it, hyphenated.
fn hyphenated(id: &types::IdentityUUID) -> String {
    const DIGITS: &[u8] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
    let n = wire(id).bytes().fold(0u128, |n, byte| {
        n * 62 + DIGITS.iter().position(|digit| *digit == byte).unwrap() as u128
    });
    let hex = format!("{n:032x}");
    format!("{}-{}-{}-{}-{}", &hex[0..8], &hex[8..12], &hex[12..16], &hex[16..20], &hex[20..32])
}

/// A value of a generated type from its JSON.
fn of<T: DeserializeOwned>(value: Value) -> T {
    serde_json::from_value(value).unwrap()
}

/// A transition's input to a state.
fn to<T: DeserializeOwned>(state: &str) -> T {
    of(json!({ "to": state }))
}

async fn serve(pool: Pool) -> String {
    let implementations = Implementations::new(Handlers { authenticator: Arc::new(Tokens) }, pool);
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, build_router(implementations)).await.unwrap() });
    base_url
}

fn sdk(base_url: &str, token: Option<&str>) -> FixtureReactionsApiSdk {
    FixtureReactionsApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), token.map(str::to_string), None)).unwrap()
}

/// ProjectReactions' and TaskReactions' change queues, each in a claim
/// loop of its own, as their mains run them, until shutdown.
fn start_workers(pool: &Pool, shutdown: &Shutdown) -> Vec<tokio::task::JoinHandle<Result<(), QueueError>>> {
    let options = Options { concurrency: 2, idle: Duration::from_millis(100), ..Options::default() };
    let (on_projects, on_tasks) = (pool.clone(), pool.clone());
    vec![
        tokio::spawn(run_worker(
            project_reactions_changes(pool).unwrap(),
            move |claimed| {
                let pool = on_projects.clone();
                async move { run_project_reactions_change(&pool, claimed).await }
            },
            options,
            shutdown.clone(),
        )),
        tokio::spawn(run_worker(
            task_reactions_changes(pool).unwrap(),
            move |claimed| {
                let pool = on_tasks.clone();
                async move { run_task_reactions_change(&pool, claimed).await }
            },
            options,
            shutdown.clone(),
        )),
    ]
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn reactions_run_through_the_core_and_the_workers() {
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
    let lee = sdk(&base_url, Some("lee:projects.write,tasks.write"));
    let shutdown = Shutdown::new();
    let workers = start_workers(&db.pool, &shutdown);

    println!("a task that starts moves its project to active");
    let (p1, p1_text) = db.project("p1").await;
    let (t1, _) = db.task("t1", &p1).await;
    assert_eq!(db.status("project", &p1_text).await, "planned");
    lee.task.status.transition(t1, to("doing"), None).await.unwrap();
    db.wait_status("project", &p1_text, "active").await;

    println!("a project whose tasks are all done moves to done");
    let (p2, p2_text) = db.project("p2").await;
    lee.project.status.transition(p2, to("active"), None).await.unwrap();
    let (t2a, _) = db.task("t2a", &p2).await;
    let (t2b, _) = db.task("t2b", &p2).await;
    for task in [t2a, t2b] {
        lee.task.status.transition(task, to("doing"), None).await.unwrap();
    }
    lee.task.status.transition(t2a, to("done"), None).await.unwrap();
    db.drained().await;
    assert_eq!(db.status("project", &p2_text).await, "active", "not done before its second task is");
    lee.task.status.transition(t2b, to("done"), None).await.unwrap();
    db.wait_status("project", &p2_text, "done").await;

    println!("a task that fails moves its project to failed");
    let (p3, p3_text) = db.project("p3").await;
    lee.project.status.transition(p3, to("active"), None).await.unwrap();
    let (t3, _) = db.task("t3", &p3).await;
    lee.task.status.transition(t3, to("doing"), None).await.unwrap();
    lee.task.status.transition(t3, to("failed"), None).await.unwrap();
    db.wait_status("project", &p3_text, "failed").await;

    println!("a raw SQL write of a task's status is heard and moves its project");
    let (p4, p4_text) = db.project("p4").await;
    let (t4, _) = db.task("t4", &p4).await;
    let t4_text = hyphenated(&t4);
    let changed = db
        .client
        .execute("UPDATE task SET status = 'doing', status_entered_at = now() WHERE id::text = $1", &[&t4_text])
        .await
        .unwrap();
    assert_eq!(changed, 1);
    db.wait_status("project", &p4_text, "active").await;

    println!("a raw SQL write of a task's priority to urgent starts it, which moves its project");
    let (p6, p6_text) = db.project("p6").await;
    let (t6, _) = db.task("t6", &p6).await;
    let t6_text = hyphenated(&t6);
    db.drained().await;
    let changed = db.client.execute("UPDATE task SET priority = 'urgent' WHERE id::text = $1", &[&t6_text]).await.unwrap();
    assert_eq!(changed, 1);
    db.wait_status("project", &p6_text, "active").await;
    assert_eq!(db.status("task", &t6_text).await, "doing", "its changes rule starts the task");

    println!("a then whose target has no transition to its state leaves the target as it is and completes the message");
    let (p5, _) = db.project("p5").await;
    let (t5, _) = db.task("t5", &p5).await;
    let t5_text = hyphenated(&t5);
    lee.task.status.transition(t5, to("doing"), None).await.unwrap();
    lee.task.status.transition(t5, to("done"), None).await.unwrap();
    db.drained().await;
    let changed = db.client.execute("UPDATE task SET priority = 'urgent' WHERE id::text = $1", &[&t5_text]).await.unwrap();
    assert_eq!(changed, 1);
    let started = Instant::now();
    while db.queued("task_change_queue", Some(&t5_text)).await > 0 {
        assert!(started.elapsed() < Duration::from_secs(10), "t5's change was not completed within 10 s");
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(db.status("task", &t5_text).await, "done");

    shutdown.cancel();
    for worker in workers {
        worker.await.unwrap().unwrap();
    }
}
`
