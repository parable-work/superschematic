package rustrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// audienceBehaviorsFixture is fixture-audience-behaviors-api, whose
// TaskActions are @behaviorOperations(Task) for any signed-in caller, over
// fixture-audience-behaviors-db, whose Task is @audience({ owner }), its
// status a Workflow, and implements Comments (D156, D103).
var audienceBehaviorsFixture = behaviorFixture{api: "fixture-audience-behaviors-api", db: "fixture-audience-behaviors-db"}

// behaviorReachCases are the cases the Rust server's test of the fixture
// runs, by name, each of which it prints as it starts it: the cases
// ormgen's TestBehaviorOperationsKeepToTheCallerOnPostgres and rustgen's
// TestRepositoriesKeepBehaviorOperationsToTheCaller hold the repositories
// to, through the routes.
var behaviorReachCases = []string{
	"a private row's and a named row's behaviors are not found for a user outside their audience, and write nothing",
	"a private row's behaviors answer its owner",
	"a named row's behaviors answer the principals it names",
	"a party whose grant reaches the row runs its behaviors, and one whose grant reaches another row finds it not there",
	"an agent run reads a read-only row's conversation, and its moves and posts are not found",
}

// TestTheBehaviorOperationsOfAHeldDatabaseCheckTheCallersReach: the crate
// of an API whose behavior operations run on a database whose rows keep to
// a caller (D156, D181) checks each row's reach through the database's
// Rust types before the core runs, so it depends on their Postgres
// repositories and re-exports them; one over a database with no audience
// and no grants checks nothing, and depends on no repositories for it.
func TestTheBehaviorOperationsOfAHeldDatabaseCheckTheCallersReach(t *testing.T) {
	_, db, reg := audienceBehaviorsFixture.load(t)
	held := audienceBehaviorsFixture.generate(t, db)
	if !held.BehaviorsHeld() {
		t.Fatal("the behavior operations of fixture-audience-behaviors-db's Task are not held to their caller's reach")
	}
	_, plainDB, _ := workflowFixture.load(t)
	if workflowFixture.generate(t, plainDB).BehaviorsHeld() {
		t.Error("the behavior operations of fixture-workflow-db, which has no audience and no grants, are held to their caller's reach")
	}
	// Without the database's Rust types, the reach would go unchecked.
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = audienceBehaviorsFixture.db
	if err := held.checkBehaviorDeps(&deps); err == nil || !strings.Contains(err.Error(), "names no Rust types of fixture-audience-behaviors-db") {
		t.Errorf("a crate whose Deps names no Rust types of the held database: %v", err)
	}
	deps.DatabaseTypesCrate = "schemas-fixture-audience-behaviors-db-types"
	if err := held.checkBehaviorDeps(&deps); err != nil {
		t.Errorf("a crate whose Deps names the held database's Rust types: %v", err)
	}
}

// TestRustBehaviorOperationsKeepToTheCallerOnPostgres serves
// fixture-audience-behaviors-api's build_router on a local socket, over a
// schema of its own in the Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL
// that holds fixture-audience-behaviors-db's tables, inserts its tasks with
// SQL, the audience triggers resolving each, and calls the status
// transition and Comments' post, edit, delete and list through the
// generated Rust SDK (D156, D181): a private row answers its owner and a
// named row the principals it names; a party is answered on the row its
// grant names, and an agent run with read-only access on its
// conversation's reads; anyone else's call is the core's 404 of a row no
// live row holds, with nothing written. Its authenticator reads Bearer
// <kind>:<subject>[:<target or acting for>]. Every case of
// behaviorReachCases must run. It skips without the database, without
// cargo, and in -short mode.
func TestRustBehaviorOperationsKeepToTheCallerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust server's behavior operations under per-record access against Postgres in -short mode")
	}
	if os.Getenv(rulesTestDatabaseEnv) == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Rust server's behavior operations under per-record access against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust server's behavior operations under per-record access")
	}
	crate := audienceBehaviorsFixture.build(t, true)
	test := strings.NewReplacer(
		"API_CRATE", strings.ReplaceAll(crate.output.CrateName, "-", "_"),
		"SDK_CRATE", strings.ReplaceAll(naming.Default().RustSDKCrate(audienceBehaviorsFixture.api), "-", "_"),
	).Replace(behaviorReachRoundtripTest)
	if err := os.WriteFile(filepath.Join(crate.dir, "tests", "behavior_reach.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	out := cargoInBehaviorCrate(t, cargo, crate, "test", "--quiet", "--test", "behavior_reach", "--", "--nocapture")
	for _, name := range behaviorReachCases {
		if !strings.Contains(out, "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// behaviorReachRoundtripTest is tests/behavior_reach.rs of
// fixture-audience-behaviors-api's API crate, beside
// fixture-audience-behaviors-db's DDL.
const behaviorReachRoundtripTest = `//! The behavior operations of fixture-audience-behaviors-api under per-record
//! access end to end (D156, D181): the crate's router over a schema of its
//! own in the Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, which holds
//! fixture-audience-behaviors-db's tables, called through the generated Rust
//! SDK by users, a party and an agent run.

use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::{bearer_token, ActingFor, ApiError, Authenticator, GrantReach, Principal, PrincipalKind};
use API_CRATE::{build_router, types, Handlers, Implementations};
use SDK_CRATE::{ClientConfig, FixtureAudienceBehaviorsApiSdk, SDKError};
use async_trait::async_trait;
use axum::http::request::Parts;
use serde::de::DeserializeOwned;
use serde_json::{json, Value};

const ALICE: &str = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const BOB: &str = "6a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9e02";
const CAROL: &str = "7b2e5c74-ac70-4e3d-9f30-4b8b3d9eaf13";
const PARTY: &str = "8c3f6d85-bd81-4f4e-a041-5c9c4eafb024";
const AGENT: &str = "9d407e96-ce92-4a5f-b152-6dad5fb0c135";

/// Reads Bearer user:<subject>, party:<subject>:<the Task its grant names>
/// and agent:<subject>:<whom it acts for>; no header, no caller.
struct Tokens;

#[async_trait]
impl Authenticator for Tokens {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        let Some(token) = bearer_token(&request.headers) else {
            return Ok(None);
        };
        let parts: Vec<&str> = token.split(':').collect();
        let principal = Principal::new(parts[1], Vec::<String>::new());
        Ok(Some(match parts[0] {
            "party" => principal.of_kind(PrincipalKind::Party).with_grants(vec![GrantReach {
                id: "g1".to_owned(),
                target_type: "Task".to_owned(),
                target_id: parts[2].to_owned(),
                reaches: Default::default(),
            }]),
            "agent" => principal.of_kind(PrincipalKind::Agent).acting_for(ActingFor {
                kind: None,
                subject: parts[2].to_owned(),
            }),
            _ => principal,
        }))
    }
}

/// A schema of its own holding fixture-audience-behaviors-db's tables, a
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
    let schema = format!("behavior_reach_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("behaviors_postgres.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(4).build().unwrap();
    Database { pool, client, admin, schema }
}

impl Database {
    async fn drop_schema(&self) {
        self.admin.batch_execute(&format!("DROP SCHEMA {} CASCADE", self.schema)).await.unwrap();
    }

    /// Inserts a task of Alice's, open, with the audience and the agent
    /// access given, and the ids a named one names, which the audience
    /// trigger resolves; answers its key, and its key's text.
    async fn task(&self, audience: &str, agents: &str, named: &[&str]) -> (types::IdentityUUID, String) {
        let named: Vec<String> = named.iter().map(|id| (*id).to_owned()).collect();
        let row = self
            .client
            .query_one(
                "INSERT INTO task (title, owner, status, status_entered_at, audience, audience_named, agent_access) VALUES ('t', $1::text::uuid, 'open', now(), $2, (SELECT array_agg(id::uuid) FROM unnest($3::text[]) id), $4) RETURNING id::text",
                &[&ALICE, &audience, &named, &agents],
            )
            .await
            .unwrap();
        let text: String = row.get(0);
        (text.parse().unwrap(), text)
    }

    /// The status of the task whose key's text is id, and the count of its
    /// messages.
    async fn state(&self, id: &str) -> (String, i64) {
        let row = self
            .client
            .query_one(
                "SELECT t.status, (SELECT count(*) FROM task_comments_messages m WHERE m.task_id = t.id) FROM task t WHERE t.id::text = $1",
                &[&id],
            )
            .await
            .unwrap();
        (row.get(0), row.get(1))
    }
}

async fn serve(pool: Pool) -> String {
    let implementations = Implementations::new(Handlers { authenticator: Arc::new(Tokens) }, pool);
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, build_router(implementations)).await.unwrap() });
    base_url
}

fn sdk(base_url: &str, token: &str) -> FixtureAudienceBehaviorsApiSdk {
    FixtureAudienceBehaviorsApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), Some(token.to_string()), None)).unwrap()
}

/// An input, from its JSON.
fn input<T: DeserializeOwned>(value: Value) -> T {
    serde_json::from_value(value).unwrap()
}

/// The key's wire form, as the core names a row.
fn wire(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_string()
}

/// The status, code and message of a refused call.
fn refusal(err: SDKError) -> (u16, String, String) {
    match err {
        SDKError::Api { status_code, message, code, .. } => (status_code, code.unwrap_or_default(), message),
        other => panic!("not a refusal of the API: {other}"),
    }
}

/// Every behavior call of the task as the SDK's caller, a list of its
/// conversation among them, is the core's 404 of a row no live row holds.
async fn refused(sdk: &FixtureAudienceBehaviorsApiSdk, task: types::IdentityUUID) {
    let gone = (404, "not_found".to_owned(), format!("Task {} does not exist", wire(&task)));
    let err = sdk.task.status.transition(task, input(json!({"to": "done"})), None).await.unwrap_err();
    assert_eq!(refusal(err), gone, "transition");
    let err = sdk.task.comments.post(task, input(json!({"body": "Can I see this?"})), None).await.unwrap_err();
    assert_eq!(refusal(err), gone, "post");
    let err = sdk.task.comments.edit(task, input(json!({"message": 1, "body": "Mine now"})), None).await.unwrap_err();
    assert_eq!(refusal(err), gone, "edit");
    let err = sdk.task.comments.delete_message(task, input(json!({"message": 1})), None).await.unwrap_err();
    assert_eq!(refusal(err), gone, "delete a message");
    let err = sdk.task.comments.list_messages(task, input(json!({})), None).await.unwrap_err();
    assert_eq!(refusal(err), gone, "list the messages");
}

/// A transition, a post, an edit and a delete of the message posted, and a
/// list, of the task as the SDK's caller, which the row answers.
async fn answers(sdk: &FixtureAudienceBehaviorsApiSdk, task: types::IdentityUUID) {
    let moved = sdk.task.status.transition(task, input(json!({"to": "done"})), None).await.unwrap();
    assert_eq!(serde_json::to_value(&moved).unwrap(), json!({"from": "open", "to": "done"}));
    let posted = sdk.task.comments.post(task, input(json!({"body": "Done on Tuesday."})), None).await.unwrap();
    let message = serde_json::to_value(&posted).unwrap()["id"].clone();
    sdk.task.comments.edit(task, input(json!({"message": message, "body": "Done on Wednesday."})), None).await.unwrap();
    let page = serde_json::to_value(sdk.task.comments.list_messages(task, input(json!({})), None).await.unwrap()).unwrap();
    let items = page["items"].as_array().unwrap();
    assert_eq!(items.len(), 1, "{page}");
    assert_eq!((&items[0]["id"], &items[0]["body"]), (&message, &json!("Done on Wednesday.")));
    sdk.task.comments.delete_message(task, input(json!({"message": message})), None).await.unwrap();
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test]
async fn behavior_operations_keep_to_the_callers_reach() {
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
    println!("case: a private row's and a named row's behaviors are not found for a user outside their audience, and write nothing");
    let (mine, mine_text) = db.task("private", "followRole", &[]).await;
    let (bobs, bobs_text) = db.task("named", "followRole", &[BOB]).await;
    let carol = sdk(&base_url, &format!("user:{CAROL}"));
    for (task, text) in [(mine, &mine_text), (bobs, &bobs_text)] {
        refused(&carol, task).await;
        assert_eq!(db.state(text).await, ("open".to_owned(), 0), "a refused call wrote");
    }

    println!("case: a private row's behaviors answer its owner");
    answers(&sdk(&base_url, &format!("user:{ALICE}")), mine).await;
    assert_eq!(db.state(&mine_text).await.0, "done");

    println!("case: a named row's behaviors answer the principals it names");
    answers(&sdk(&base_url, &format!("user:{BOB}")), bobs).await;
    assert_eq!(db.state(&bobs_text).await.0, "done");

    println!("case: a party whose grant reaches the row runs its behaviors, and one whose grant reaches another row finds it not there");
    let (reached, _) = db.task("everyone", "followRole", &[]).await;
    let (other, other_text) = db.task("everyone", "followRole", &[]).await;
    let party = sdk(&base_url, &format!("party:{PARTY}:{}", wire(&reached)));
    answers(&party, reached).await;
    refused(&party, other).await;
    assert_eq!(db.state(&other_text).await, ("open".to_owned(), 0), "a refused call wrote");

    println!("case: an agent run reads a read-only row's conversation, and its moves and posts are not found");
    let (watered, watered_text) = db.task("everyone", "readOnly", &[]).await;
    sdk(&base_url, &format!("user:{ALICE}")).task.comments.post(watered, input(json!({"body": "First."})), None).await.unwrap();
    let agent = sdk(&base_url, &format!("agent:{AGENT}:{ALICE}"));
    let page = serde_json::to_value(agent.task.comments.list_messages(watered, input(json!({})), None).await.unwrap()).unwrap();
    assert_eq!(page["items"].as_array().map(Vec::len), Some(1), "{page}");
    let gone = (404, "not_found".to_owned(), format!("Task {} does not exist", wire(&watered)));
    let err = agent.task.status.transition(watered, input(json!({"to": "done"})), None).await.unwrap_err();
    assert_eq!(refusal(err), gone, "transition");
    let err = agent.task.comments.post(watered, input(json!({"body": "On it."})), None).await.unwrap_err();
    assert_eq!(refusal(err), gone, "post");
    assert_eq!(db.state(&watered_text).await, ("open".to_owned(), 1), "a refused call wrote");
}
`
