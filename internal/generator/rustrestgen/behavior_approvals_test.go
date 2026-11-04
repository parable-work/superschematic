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

// The Approvals fixture (D104): fixture-approvals-api serves TaskActions,
// @behaviorOperations(Task) for any signed-in caller, and TaskAdmin, whose
// reassignTask takes a @source(Task) view carrying reviewer and owner, over
// fixture-approvals-db, its one DB dependency, whose Task implements
// Constants (owner kept as its create gave it, urgent and attempts moving
// up alone, both unless the caller holds tasks.admin), StateRules (the plan
// frozen while planned but for its creator and while working but for its
// reviewer, a proof required to enter proving) and Approvals (a gate on
// proving whose sign-offs survive every edit, and a pinned gate on done
// whose sign-offs a change of the proof consumes, kept in
// task_approvals_sign_offs), beside its status, a Workflow.
var approvalsFixture = behaviorFixture{api: "fixture-approvals-api", db: "fixture-approvals-db"}

// approvalsCases are the cases every language's end-to-end test of the
// Approvals fixture runs, by the same names, each of which the Rust test
// prints as it starts it.
var approvalsCases = []string{
	"a oneWay field moved back is refused as one_way, and moved forward is saved",
	"a caller holding tasks.admin moves a oneWay field back",
	"the creator edits the plan while the task is planned",
	"another caller editing the plan while the task is planned is refused as frozen_fields",
	"a move into proving without its sign-offs is refused as approvals_missing",
	"a pinned signOff with no version is refused as invalid_argument, with a stale one as stale, and with the row's version is saved",
	"editing the proof consumes the done gate's sign-offs and keeps the proving gate's",
	"an update of the owner, a constant, is refused as invalid_instance",
}

// TestWriteRustAPIGoldenApprovals pins the crate of fixture-approvals-api
// (D104): the router mounts each operation of Task's Approvals, which the
// table implements, under approvals (sign-off, withdraw-sign-off,
// list-sign-offs) beside its status's transition, each run through the
// behaviors' core; Constants and StateRules, which have no operations, add
// no route; reassignTask is the one operation an implementation serves;
// and the crate's permissions list Approvals' gates' permission and the
// route's. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenApprovals -update
func TestWriteRustAPIGoldenApprovals(t *testing.T) {
	_, db, reg := approvalsFixture.load(t)
	output := approvalsFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = approvalsFixture.db
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
		"task.approvalsListSignOffs POST /api/task/{id}/approvals/list-sign-offs Task.Approvals.listSignOffs",
		"task.approvalsSignOff POST /api/task/{id}/approvals/sign-off Task.Approvals.signOff",
		"task.approvalsWithdrawSignOff POST /api/task/{id}/approvals/withdraw-sign-off Task.Approvals.withdrawSignOff",
		"task.statusTransition POST /api/task/{id}/status/transition Task.status.transition",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations %q, want %q", behaviors, want)
	}
	var implemented []string
	for _, endpoint := range output.ImplementedEndpoints() {
		implemented = append(implemented, endpoint.Namespace+"."+endpoint.Name)
	}
	if !slices.Equal(implemented, []string{"task-admin.reassignTask"}) {
		t.Errorf("implemented endpoints %q, want reassignTask alone", implemented)
	}

	generated := writeGoldenAPI(t, approvalsFixture.api, output)
	for file, want := range map[string][]string{
		"Cargo.toml": {
			`superschematic-rules-adapter = { path = "../../../../runtime/engine/rust/rules-adapter", features = ["postgres"] }`,
			`superschematic-rules-behaviors = { path = "../../../../runtime/engine/rust/rules-behaviors" }`,
		},
		"src/lib.rs":        {"pub mod behaviors;"},
		"src/interfaces.rs": {"pub trait TaskAdminImplementation", "async fn reassign_task(", "pub struct TaskApprovalsSignOffArgs"},
		"src/router.rs": {
			"\"Task\",\n        &args.id,\n        \"Approvals\",\n        \"signOff\",",
			"\"Task\",\n        &args.id,\n        \"Approvals\",\n        \"withdrawSignOff\",",
			"\"Task\",\n        &args.id,\n        \"Approvals\",\n        \"listSignOffs\",",
			"\"Task\",\n        &args.id,\n        \"status\",\n        \"transition\",",
		},
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"task_approvals_sign_offs"`,
			"postgres::run_operation(",
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
	for _, absent := range []string{"constants", "staterules", "Constants", "StateRules"} {
		if strings.Contains(generated["src/router.rs"], absent) {
			t.Errorf("src/router.rs names %s, which has no operation", absent)
		}
	}
	for _, permission := range []string{"tasks.review", "tasks.admin"} {
		if !strings.Contains(output.PermissionCatalogJSON, `"`+permission+`"`) {
			t.Errorf("the crate's permissions.json does not list %s", permission)
		}
	}
}

// TestApprovalsAPICrateBuilds lints fixture-approvals-api's crate with
// clippy, warnings denied: its routes of Task's Approvals and status, and
// reassignTask's trait, against the checkout's
// superschematic-rules-adapter and superschematic-rules-behaviors.
func TestApprovalsAPICrateBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	crate := approvalsFixture.build(t, false)
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
}

// TestRustApprovalsOnPostgres serves fixture-approvals-api's build_router on
// a local socket, over a schema of its own in the Postgres at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL that holds fixture-approvals-db's
// tables (D104): it inserts its tasks with SQL, created_by stamped as a
// repository stamps it, updates them through the adapter's run_update with
// a caller holding the permissions the case names (a Rust server has no
// repository), and moves and signs them off through the generated Rust
// SDK as subjects that are fresh UUIDs in their base62 form: the eight
// cases every language's end-to-end test runs, by the same names, each held
// to have run. Its authenticator reads Bearer <subject>:<permission>,... .
// It skips without the database, without cargo, and in -short mode.
func TestRustApprovalsOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's Approvals against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's Constants, StateRules and Approvals against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's Approvals")
	}
	crate := approvalsFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(approvalsFixture.api), "-", "_"),
	).Replace(approvalsRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_approvals.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	out := cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_approvals", "--", "--nocapture")
	for _, name := range approvalsCases {
		if !strings.Contains(out, "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// approvalsRoundtripTest is tests/behavior_approvals.rs of
// fixture-approvals-api's API crate, beside fixture-approvals-db's DDL. Its
// cases are the ones every server's end-to-end test of the fixture runs, by
// the same names, in the core's words.
const approvalsRoundtripTest = `//! Constants, StateRules and Approvals on a table end to end (D104):
//! fixture-approvals-api's router over a schema of its own in the Postgres
//! at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, which holds
//! fixture-approvals-db's tables, its tasks' transitions and sign-offs
//! called through the generated Rust SDK and their updates run through the
//! adapter, by subjects that are UUIDs in their base62 form.

use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use API_CRATE::behaviors::RULES_LAYOUT;
use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, RequestContext};
use API_CRATE::{build_router, types, Handlers, Implementations, TaskAdminImplementation, TaskAdminReassignTaskArgs};
use SDK_CRATE::{ClientConfig, FixtureApprovalsApiSdk, SDKError};
use async_trait::async_trait;
use axum::http::request::Parts;
use serde::de::DeserializeOwned;
use serde_json::{json, Value};
use superschematic_rules_adapter::{postgres, Caller, Error, ExecuteOptions, Host, Layout, Refusal};

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

/// reassignTask, which no case calls: the build warns that a caller who may
/// call it can name itself in reviewer and then edit the plan while the
/// task is working.
struct Admin;

#[async_trait]
impl TaskAdminImplementation for Admin {
    async fn reassign_task(&self, _ctx: RequestContext, _args: TaskAdminReassignTaskArgs) -> Result<types::Task, ApiError> {
        Err(ApiError::not_implemented("task_admin.reassign_task is not implemented"))
    }
}

/// A schema of its own holding fixture-approvals-db's tables, a pool whose
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
    let schema = format!("behavior_approvals_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("behaviors_postgres.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(4).build().unwrap();
    Database { pool, client, admin, schema }
}

/// A subject: a UUID in its base62 form, as the identity runtime gives a
/// principal's subject and the core holds a UUID field, and hyphenated, as
/// a column holds it.
struct Subject {
    wire: String,
    column: String,
}

/// A task: its key as the SDK takes it, its wire form, as the core names
/// its row, and its column's text.
struct Task {
    id: types::IdentityUUID,
    wire: String,
    text: String,
}

/// What a case reads of a task's row with SQL.
#[derive(Debug, PartialEq)]
struct Stored {
    plan: Option<String>,
    urgent: bool,
    attempts: String,
    status: String,
    version: i64,
}

impl Database {
    async fn drop_schema(&self) {
        self.admin.batch_execute(&format!("DROP SCHEMA {} CASCADE", self.schema)).await.unwrap();
    }

    /// A fresh subject.
    async fn subject(&self) -> Subject {
        let text: String = self.client.query_one("SELECT gen_random_uuid()::text", &[]).await.unwrap().get(0);
        let id: types::IdentityUUID = text.parse().unwrap();
        Subject { wire: wire(&id), column: text }
    }

    /// A task alice created, owned by bob and reviewed by carol, planned as
    /// the core's create leaves it, its created_by stamped as a repository
    /// stamps it with its caller. A Rust server has no repository, so the
    /// insert is the application's.
    async fn create(&self, alice: &Subject, bob: &Subject, carol: &Subject) -> Task {
        let row = self
            .client
            .query_one(
                "INSERT INTO task (title, owner, reviewer, plan, urgent, attempts, created_by, status, status_entered_at) VALUES ('Launch', $1::text::uuid, $2::text::uuid, 'First plan', false, 0, $3::text::uuid, 'planned', now()) RETURNING id::text",
                &[&bob.column, &carol.column, &alice.column],
            )
            .await
            .unwrap();
        let text: String = row.get(0);
        let id: types::IdentityUUID = text.parse().unwrap();
        Task { wire: wire(&id), id, text }
    }

    /// An update of the task's own fields through the core, as the caller
    /// holding permissions, committed when it applies.
    async fn update(&self, task: &Task, patch: Value, caller: &Subject, permissions: &[&str]) -> Result<(), Error> {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let patch = patch.as_object().cloned().unwrap();
        let caller = Caller::new(caller.wire.clone(), permissions.iter().copied());
        let updated = postgres::run_update(&host, &*tx, "Task", &task.wire, patch, caller, &ExecuteOptions::default()).await;
        if updated.is_ok() {
            tx.commit().await.unwrap();
        }
        updated
    }

    /// The task as its row holds it.
    async fn stored(&self, task: &Task) -> Stored {
        let row = self
            .client
            .query_one("SELECT plan, urgent, attempts::text, status, _version::bigint FROM task WHERE id::text = $1", &[&task.text])
            .await
            .unwrap();
        Stored { plan: row.get(0), urgent: row.get(1), attempts: row.get(2), status: row.get(3), version: row.get(4) }
    }
}

async fn serve(pool: Pool) -> String {
    let implementations = Implementations::new(
        Handlers {
            task_admin: Arc::new(Admin),
            authenticator: Arc::new(Tokens),
        },
        pool,
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, build_router(implementations)).await.unwrap() });
    base_url
}

/// The SDK as a subject holding permissions.
fn sdk(base_url: &str, subject: &Subject, permissions: &str) -> FixtureApprovalsApiSdk {
    let token = format!("{}:{permissions}", subject.wire);
    FixtureApprovalsApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), Some(token), None)).unwrap()
}

/// The key's wire form, as the core names a row.
fn wire(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_string()
}

/// A value of a generated type from its JSON.
fn of<T: DeserializeOwned>(value: Value) -> T {
    serde_json::from_value(value).unwrap()
}

/// The status, code, message and details of a refused call.
fn refusal(err: SDKError) -> (u16, String, String, Value) {
    let details = err.problem().and_then(|problem| problem.details.clone()).unwrap_or(Value::Null);
    match err {
        SDKError::Api { status_code, message, code, .. } => (status_code, code.unwrap_or_default(), message, details),
        other => panic!("not a refusal of the API: {other}"),
    }
}

/// The core's refusal of an update through the adapter.
fn refused(updated: Result<(), Error>) -> Refusal {
    match updated {
        Err(Error::Refused(refusal)) => *refusal,
        other => panic!("not refused: {other:?}"),
    }
}

/// A refusal's details, null for none.
fn details(refusal: &Refusal) -> Value {
    refusal.details.as_ref().map_or(Value::Null, |details| Value::Object((**details).clone()))
}

fn case(name: &str) {
    println!("case: {name}");
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test]
async fn constants_staterules_and_approvals_run_through_the_core() {
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
    let (alice, bob, carol, dave) = (db.subject().await, db.subject().await, db.subject().await, db.subject().await);
    let as_bob = sdk(&base_url, &bob, "");
    let as_carol = sdk(&base_url, &carol, "tasks.review");

    case("a oneWay field moved back is refused as one_way, and moved forward is saved");
    let t = db.create(&alice, &bob, &carol).await;
    db.update(&t, json!({"urgent": true, "attempts": 1}), &bob, &[]).await.unwrap();
    let before = db.stored(&t).await.version;
    let refusal_of = refused(db.update(&t, json!({"urgent": false}), &bob, &[]).await);
    assert_eq!((refusal_of.status, refusal_of.code.as_str()), (409, "vetoed"));
    assert_eq!(
        refusal_of.message,
        format!(
            "behavior Constants vetoes update of Task {t}: Task {t}'s urgent only turns true: an update may not move it from true to false; only a caller with tasks.admin may move it back",
            t = t.wire
        )
    );
    let vetoed = details(&refusal_of);
    assert_eq!(vetoed["vetoCode"], json!("one_way"));
    assert_eq!(vetoed["vetoDetails"], json!({"field": "urgent", "direction": "up", "current": true, "attempted": false}));
    assert_eq!(db.stored(&t).await.version, before, "a refused write writes nothing");
    db.update(&t, json!({"attempts": 3}), &bob, &[]).await.unwrap();
    let stored = db.stored(&t).await;
    assert_eq!((stored.urgent, stored.attempts.as_str()), (true, "3"));

    case("a caller holding tasks.admin moves a oneWay field back");
    let t = db.create(&alice, &bob, &carol).await;
    db.update(&t, json!({"urgent": true, "attempts": 2}), &bob, &[]).await.unwrap();
    let refusal_of = refused(db.update(&t, json!({"attempts": 0}), &bob, &[]).await);
    assert_eq!((refusal_of.status, refusal_of.code.as_str()), (409, "vetoed"));
    assert_eq!(
        refusal_of.message,
        format!(
            "behavior Constants vetoes update of Task {t}: Task {t}'s attempts only grows: an update may not move it from 2 to 0; only a caller with tasks.admin may move it back",
            t = t.wire
        )
    );
    let vetoed = details(&refusal_of);
    assert_eq!(vetoed["vetoCode"], json!("one_way"));
    assert_eq!(vetoed["vetoDetails"], json!({"field": "attempts", "direction": "up", "current": 2, "attempted": 0}));
    db.update(&t, json!({"urgent": false, "attempts": 0}), &dave, &["tasks.admin"]).await.unwrap();
    let stored = db.stored(&t).await;
    assert_eq!((stored.urgent, stored.attempts.as_str()), (false, "0"));

    case("the creator edits the plan while the task is planned");
    let t = db.create(&alice, &bob, &carol).await;
    db.update(&t, json!({"plan": "Second plan"}), &alice, &[]).await.unwrap();
    let stored = db.stored(&t).await;
    assert_eq!((stored.status.as_str(), stored.plan.as_deref()), ("planned", Some("Second plan")));

    case("another caller editing the plan while the task is planned is refused as frozen_fields");
    let t = db.create(&alice, &bob, &carol).await;
    let before = db.stored(&t).await.version;
    let refusal_of = refused(db.update(&t, json!({"plan": "Bob's plan"}), &bob, &[]).await);
    assert_eq!((refusal_of.status, refusal_of.code.as_str()), (409, "vetoed"));
    assert_eq!(
        refusal_of.message,
        format!(
            "behavior StateRules vetoes update of Task {t}: Task {t} is planned, which freezes plan: it does not change while it is planned; only the subject createdBy holds may change it",
            t = t.wire
        )
    );
    let vetoed = details(&refusal_of);
    assert_eq!(vetoed["vetoCode"], json!("frozen_fields"));
    assert_eq!(vetoed["vetoDetails"], json!({"state": "planned", "fields": ["plan"], "by": ["createdBy"]}));
    let stored = db.stored(&t).await;
    assert_eq!((stored.plan.as_deref(), stored.version), (Some("First plan"), before));

    case("a move into proving without its sign-offs is refused as approvals_missing");
    let t = db.create(&alice, &bob, &carol).await;
    let moved = as_bob.task.status.transition(t.id, of(json!({"to": "working"})), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "planned", "to": "working"}));
    db.update(&t, json!({"proof": "Screenshots"}), &bob, &[]).await.unwrap();
    let before = db.stored(&t).await.version;
    let err = as_bob.task.status.transition(t.id, of(json!({"to": "proving"})), None).await.unwrap_err();
    let (status, code, message, problem) = refusal(err);
    assert_eq!((status, code.as_str()), (409, "vetoed"));
    assert_eq!(
        message,
        format!("behavior Approvals vetoes transition of Task {t}: Task {t} cannot move to proving with 0 of the 1 sign-offs it needs", t = t.wire)
    );
    assert_eq!(problem["vetoCode"], json!("approvals_missing"));
    assert_eq!(problem["vetoDetails"], json!({"state": "proving", "required": 1, "count": 0}));
    let stored = db.stored(&t).await;
    assert_eq!((stored.status.as_str(), stored.version), ("working", before));

    case("a pinned signOff with no version is refused as invalid_argument, with a stale one as stale, and with the row's version is saved");
    let t = working(&db, &as_bob, &alice, &bob, &carol).await;
    let version = db.stored(&t).await.version;
    let err = as_carol.task.approvals.sign_off(t.id, of(json!({"state": "done"})), None).await.unwrap_err();
    let (status, code, message, _) = refusal(err);
    assert_eq!((status, code.as_str()), (400, "invalid_argument"));
    assert_eq!(
        message,
        "operation signOff of behavior Approvals: /version: is required: the gate on done is pinned, so a sign-off names the row's version its signer read"
    );
    let err = as_carol.task.approvals.sign_off(t.id, of(json!({"state": "done", "version": version - 1})), None).await.unwrap_err();
    let (status, code, message, problem) = refusal(err);
    assert_eq!((status, code.as_str()), (409, "vetoed"));
    assert_eq!(
        message,
        format!("behavior Approvals vetoes signOff of Task {t}: Task {t} is at version {version}, not {stale}", t = t.wire, stale = version - 1)
    );
    assert_eq!(problem["vetoCode"], json!("stale"));
    assert_eq!(problem["vetoDetails"], json!({"version": version - 1, "current": version}));
    assert_eq!(db.stored(&t).await.version, version, "a refused sign-off writes nothing");
    let signed = as_carol.task.approvals.sign_off(t.id, of(json!({"state": "done", "version": version})), None).await.unwrap();
    let mut signed = serde_json::to_value(&signed).unwrap();
    assert!(signed["at"].is_i64(), "{signed}");
    signed.as_object_mut().unwrap().remove("at");
    assert_eq!(signed, json!({"state": "done", "subject": carol.wire, "signedBy": [carol.wire], "count": 1}));
    assert_eq!(db.stored(&t).await.version, version + 1, "a sign-off moves the row's version");

    case("editing the proof consumes the done gate's sign-offs and keeps the proving gate's");
    let t = working(&db, &as_bob, &alice, &bob, &carol).await;
    as_carol.task.approvals.sign_off(t.id, of(json!({"state": "proving"})), None).await.unwrap();
    let version = db.stored(&t).await.version;
    as_carol.task.approvals.sign_off(t.id, of(json!({"state": "done", "version": version})), None).await.unwrap();
    db.update(&t, json!({"proof": "New screenshots"}), &bob, &[]).await.unwrap();
    let listed = as_carol.task.approvals.list_sign_offs(t.id, of(json!({})), None).await.unwrap();
    let listed = serde_json::to_value(&listed).unwrap();
    let items = listed["items"].as_array().unwrap();
    let states: Vec<(&str, bool)> = items.iter().map(|item| (item["state"].as_str().unwrap(), item.get("consumedAt").is_some_and(|at| !at.is_null()))).collect();
    assert_eq!(states, [("proving", false), ("done", true)], "{listed}");
    let moved = as_bob.task.status.transition(t.id, of(json!({"to": "proving"})), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "working", "to": "proving"}));
    assert_eq!(db.stored(&t).await.status, "proving");

    case("an update of the owner, a constant, is refused as invalid_instance");
    let t = db.create(&alice, &bob, &carol).await;
    let before = db.stored(&t).await.version;
    let refusal_of = refused(db.update(&t, json!({"owner": dave.wire}), &bob, &[]).await);
    assert_eq!((refusal_of.status, refusal_of.code.as_str()), (422, "invalid_instance"));
    assert_eq!(
        details(&refusal_of)["issues"],
        json!([{"path": "owner", "rule": "constant", "message": "owner is a constant of Task: its create sets it and nothing changes it after; only a caller with tasks.admin may change it"}])
    );
    assert_eq!(db.stored(&t).await.version, before, "a refused write writes nothing");
}

/// A task bob moved to working and gave a proof.
async fn working(db: &Database, as_bob: &FixtureApprovalsApiSdk, alice: &Subject, bob: &Subject, carol: &Subject) -> Task {
    let t = db.create(alice, bob, carol).await;
    as_bob.task.status.transition(t.id, of(json!({"to": "working"})), None).await.unwrap();
    db.update(&t, json!({"proof": "Screenshots"}), bob, &[]).await.unwrap();
    t
}
`
