package rustrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The principals fixture (D104): fixture-workflow-principals-api serves
// QuestionActions, @behaviorOperations(Question) for any signed-in caller,
// and QuestionAdmin, whose reassignQuestion takes a @source(Question) view
// carrying askedOf, over fixture-workflow-principals-db, its one DB
// dependency, whose Question's status is a Workflow whose transitions name
// who makes them (by on askedOf, by on createdBy, and by beside a
// permission), with an override permission and repeat unchanged, and whose
// review is a Workflow with neither.
var principalsFixture = behaviorFixture{api: "fixture-workflow-principals-api", db: "fixture-workflow-principals-db"}

// workflowPrincipalsCases are the cases every language's end-to-end test of
// the principals fixture runs, by the same names, each of which the Rust
// test prints as it starts it.
var workflowPrincipalsCases = []string{
	"the subject the question is asked of answers it",
	"another caller is refused as not_principal",
	"the question's creator withdraws it, by createdBy",
	"a caller holding the override permission answers it",
	"answering again answers unchanged and leaves the row's version",
	"a repeat on the Workflow without repeat is refused as already_in_state",
	"a from that is not the row's state is refused as stale",
	"a from that is the row's state moves it, and its retry is stale with the state it moved to",
}

// TestRustWorkflowPrincipalsOnPostgres serves
// fixture-workflow-principals-api's build_router on a local socket, over a
// schema of its own in the Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL
// that holds fixture-workflow-principals-db's tables, inserts its questions
// with SQL, created_by stamped as a repository stamps it, and calls the
// status and review transitions through the generated Rust SDK as subjects
// that are fresh UUIDs in their base62 form, as the identity runtime gives
// a principal's (D104): the eight cases every language's end-to-end test
// runs, by the same names, each held to have run. Its authenticator reads
// Bearer <subject>:<permission>,... . It skips without the database,
// without cargo, and in -short mode.
func TestRustWorkflowPrincipalsOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's Workflow principals against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's Workflow principals against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's Workflow principals")
	}
	crate := principalsFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(principalsFixture.api), "-", "_"),
	).Replace(workflowPrincipalsRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "workflow_principals.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	out := cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "workflow_principals", "--", "--nocapture")
	for _, name := range workflowPrincipalsCases {
		if !strings.Contains(out, "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// workflowPrincipalsRoundtripTest is tests/workflow_principals.rs of
// fixture-workflow-principals-api's API crate, beside
// fixture-workflow-principals-db's DDL. Its cases are the ones every
// server's end-to-end test of the fixture runs, by the same names, in the
// core's words.
const workflowPrincipalsRoundtripTest = `//! The principals of a Workflow transition end to end (D104):
//! fixture-workflow-principals-api's router over a schema of its own in the
//! Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, which holds
//! fixture-workflow-principals-db's tables, called through the generated
//! Rust SDK by subjects that are UUIDs in their base62 form.

use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, RequestContext};
use API_CRATE::{
    build_router, types, Handlers, Implementations, QuestionAdminImplementation, QuestionAdminReassignQuestionArgs,
};
use SDK_CRATE::{ClientConfig, FixtureWorkflowPrincipalsApiSdk, SDKError};
use async_trait::async_trait;
use axum::http::request::Parts;
use serde_json::{json, Value};

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

/// reassignQuestion, which no case calls: the build warns that a caller who
/// may call it can name itself in askedOf and then answer the question.
struct Admin;

#[async_trait]
impl QuestionAdminImplementation for Admin {
    async fn reassign_question(
        &self,
        _ctx: RequestContext,
        _args: QuestionAdminReassignQuestionArgs,
    ) -> Result<types::Question, ApiError> {
        Err(ApiError::not_implemented("question_admin.reassign_question is not implemented"))
    }
}

/// A schema of its own holding fixture-workflow-principals-db's tables, a
/// pool whose connections search it, and the connection that drops it.
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
    let schema = format!("workflow_principals_rust_{nanos}");
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
/// principal's subject, and hyphenated, as a column holds it.
struct Subject {
    wire: String,
    column: String,
}

/// A question: its key as the SDK takes it, its wire form, as the core
/// names its row, and its column's text.
struct Asked {
    id: types::IdentityUUID,
    wire: String,
    text: String,
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

    /// A question asked of asked_of by created_by, open and pending, as the
    /// core's create leaves it, its created_by stamped as a repository
    /// stamps it with its caller.
    async fn ask(&self, asked_of: &Subject, created_by: &Subject) -> Asked {
        let row = self
            .client
            .query_one(
                "INSERT INTO question (prompt, asked_of, created_by, status, status_entered_at, review, review_entered_at) VALUES ('Which table?', $1::text::uuid, $2::text::uuid, 'open', now(), 'pending', now()) RETURNING id::text",
                &[&asked_of.column, &created_by.column],
            )
            .await
            .unwrap();
        let text: String = row.get(0);
        let id: types::IdentityUUID = text.parse().unwrap();
        Asked { wire: wire(&id), id, text }
    }

    /// The question's status, its version and when its status was entered,
    /// as text.
    async fn state(&self, asked: &Asked) -> (String, String, String) {
        let row = self
            .client
            .query_one("SELECT status, _version::text, status_entered_at::text FROM question WHERE id::text = $1", &[&asked.text])
            .await
            .unwrap();
        (row.get(0), row.get(1), row.get(2))
    }
}

async fn serve(pool: Pool) -> String {
    let implementations = Implementations::new(
        Handlers {
            question_admin: Arc::new(Admin),
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
fn sdk(base_url: &str, subject: &Subject, permissions: &str) -> FixtureWorkflowPrincipalsApiSdk {
    let token = format!("{}:{permissions}", subject.wire);
    FixtureWorkflowPrincipalsApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), Some(token), None)).unwrap()
}

/// The key's wire form, as the core names a row.
fn wire(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_string()
}

/// The status, code, message and details of a refused call.
fn refusal(err: SDKError) -> (u16, String, String, Value) {
    let details = err.problem().and_then(|problem| problem.details.clone()).unwrap_or(Value::Null);
    match err {
        SDKError::Api { status_code, message, code, .. } => (status_code, code.unwrap_or_default(), message, details),
        other => panic!("not a refusal of the API: {other}"),
    }
}

/// A veto's status, code, message, veto code and veto details.
fn veto(err: SDKError) -> (u16, String, String, Value, Value) {
    let (status, code, message, details) = refusal(err);
    (status, code, message, details["vetoCode"].clone(), details["vetoDetails"].clone())
}

/// A status transition's input: the state to move to and, when given, the
/// state the caller read.
fn status(to: types::QuestionStatusState, from: Option<types::QuestionStatusState>) -> types::QuestionStatusTransitionInput {
    types::QuestionStatusTransitionInput { to, from }
}

fn case(name: &str) {
    println!("case: {name}");
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test]
async fn workflow_principals_run_through_the_core() {
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
    use types::QuestionStatusState::{Answered, Open, Withdrawn};

    let base_url = serve(db.pool.clone()).await;
    let (alice, bob, carol, moderator) = (db.subject().await, db.subject().await, db.subject().await, db.subject().await);
    let as_alice = sdk(&base_url, &alice, "");
    let as_bob = sdk(&base_url, &bob, "");
    let as_carol = sdk(&base_url, &carol, "");
    let as_moderator = sdk(&base_url, &moderator, "questions.moderate");

    case("the subject the question is asked of answers it");
    let q = db.ask(&bob, &alice).await;
    let moved = as_bob.question.status.transition(q.id, status(Answered, None), None).await.unwrap();
    assert_eq!(moved.unchanged, None);
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "open", "to": "answered"}));
    assert_eq!(db.state(&q).await.0, "answered");

    case("another caller is refused as not_principal");
    let q = db.ask(&bob, &alice).await;
    let (_, before, _) = db.state(&q).await;
    let err = as_carol.question.status.transition(q.id, status(Answered, None), None).await.unwrap_err();
    let (status_code, code, message, veto_code, veto_details) = veto(err);
    assert_eq!((status_code, code.as_str()), (409, "vetoed"));
    assert_eq!(
        message,
        format!(
            "behavior Workflow vetoes transition of Question {q}: {carol} may not move Question {q}'s status from open to answered: the transition is made by the subject askedOf holds",
            q = q.wire,
            carol = carol.wire
        )
    );
    assert_eq!(veto_code, json!("not_principal"));
    assert_eq!(veto_details, json!({"from": "open", "to": "answered", "by": ["askedOf"]}));
    let (state, after, _) = db.state(&q).await;
    assert_eq!((state.as_str(), after), ("open", before));

    case("the question's creator withdraws it, by createdBy");
    let q = db.ask(&bob, &alice).await;
    let err = as_bob.question.status.transition(q.id, status(Withdrawn, None), None).await.unwrap_err();
    let (status_code, code, _, veto_code, veto_details) = veto(err);
    assert_eq!((status_code, code.as_str()), (409, "vetoed"));
    assert_eq!(veto_code, json!("not_principal"));
    assert_eq!(veto_details, json!({"from": "open", "to": "withdrawn", "by": ["createdBy"]}));
    let moved = as_alice.question.status.transition(q.id, status(Withdrawn, None), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "open", "to": "withdrawn"}));
    assert_eq!(db.state(&q).await.0, "withdrawn");

    case("a caller holding the override permission answers it");
    let q = db.ask(&bob, &alice).await;
    let moved = as_moderator.question.status.transition(q.id, status(Answered, None), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "open", "to": "answered"}));
    assert_eq!(db.state(&q).await.0, "answered");

    case("answering again answers unchanged and leaves the row's version");
    let q = db.ask(&bob, &alice).await;
    as_bob.question.status.transition(q.id, status(Answered, None), None).await.unwrap();
    let answered = db.state(&q).await;
    let again = as_bob.question.status.transition(q.id, status(Answered, None), None).await.unwrap();
    assert_eq!(again.unchanged, Some(true));
    assert_eq!(serde_json::to_value(&again).unwrap(), json!({"from": "answered", "to": "answered", "unchanged": true}));
    assert_eq!(db.state(&q).await, answered, "the repeat wrote nothing: the version and status_entered_at stay");

    case("a repeat on the Workflow without repeat is refused as already_in_state");
    let q = db.ask(&bob, &alice).await;
    let err = as_bob
        .question
        .review
        .transition(q.id, types::QuestionReviewTransitionInput { to: types::QuestionReviewState::Pending, from: None }, None)
        .await
        .unwrap_err();
    let (status_code, code, message, veto_code, veto_details) = veto(err);
    assert_eq!((status_code, code.as_str()), (409, "vetoed"));
    assert_eq!(
        message,
        format!("behavior Workflow vetoes transition of Question {q}: Question {q}'s review is already pending", q = q.wire)
    );
    assert_eq!(veto_code, json!("already_in_state"));
    assert_eq!(veto_details, json!({"from": "pending", "to": "pending"}));

    case("a from that is not the row's state is refused as stale");
    let q = db.ask(&bob, &alice).await;
    let err = as_bob.question.status.transition(q.id, status(Answered, Some(Answered)), None).await.unwrap_err();
    let (status_code, code, message, veto_code, veto_details) = veto(err);
    assert_eq!((status_code, code.as_str()), (409, "vetoed"));
    assert_eq!(
        message,
        format!("behavior Workflow vetoes transition of Question {q}: Question {q}'s status is open, not answered", q = q.wire)
    );
    assert_eq!(veto_code, json!("stale"));
    assert_eq!(veto_details, json!({"from": "answered", "status": "open"}));
    assert_eq!(db.state(&q).await.0, "open");

    case("a from that is the row's state moves it, and its retry is stale with the state it moved to");
    let q = db.ask(&bob, &alice).await;
    let moved = as_bob.question.status.transition(q.id, status(Answered, Some(Open)), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "open", "to": "answered"}));
    let err = as_bob.question.status.transition(q.id, status(Answered, Some(Open)), None).await.unwrap_err();
    let (status_code, code, _, veto_code, veto_details) = veto(err);
    assert_eq!((status_code, code.as_str()), (409, "vetoed"));
    assert_eq!(veto_code, json!("stale"));
    assert_eq!(veto_details, json!({"from": "open", "status": "answered"}));
    assert_eq!(db.state(&q).await.0, "answered");
}
`
