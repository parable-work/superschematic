package rustrestgen

// seatsRoundtripHead is the head of tests/behavior_seats.rs: its uses, its
// authenticator, its schema of fixture-seats-db's tables and what the
// cases read and write with SQL and through the adapter. The only real
// wait is the worker's: the stale deadline a case needs due is made due
// with SQL, an operator's escape hatch, and the worker's run polled for
// every 100 ms for at most ten seconds; it runs at the later of the clock
// and the deadline's time, so the core sees the sandbox gone stale.
const seatsRoundtripHead = `//! Seats and Reconcile on tables end to end (D104): fixture-seats-api's
//! router over a schema of its own in the Postgres at
//! SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, which holds fixture-seats-db's
//! tables, its problems' and sandboxes' behavior operations called through
//! the generated Rust SDK, their creates and a problem's update run through
//! the adapter as the caller, and its behavior worker SandboxDeadlines'
//! queue and handler run in process, by subjects that are UUIDs in their
//! base62 form.

use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use API_CRATE::behaviors::{run_sandbox_deadlines_deadline, sandbox_deadlines_deadlines, RULES_LAYOUT};
use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::queue::QueueError;
use API_CRATE::runtime::worker::{run_worker, Options};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, Shutdown};
use API_CRATE::{build_router, types, Handlers, Implementations};
use SDK_CRATE::client::HttpClient;
use SDK_CRATE::{ClientConfig, FixtureSeatsApiSdk, SDKError};
use async_trait::async_trait;
use axum::http::request::Parts;
use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::{json, Map, Value};
use superschematic_rules_adapter::{postgres, Caller, CreateOptions, Error, ExecuteOptions, Host, Layout, Refusal};

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

/// A schema of its own holding fixture-seats-db's tables, a pool whose
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
    let schema = format!("behavior_seats_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("behaviors_postgres.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(8).build().unwrap();
    Database { pool, client, admin, schema }
}

/// A caller: a UUID in its base62 form, as the identity runtime gives a
/// principal's subject and the core keeps a seat's subject, and the
/// permissions it holds, comma separated as its token lists them.
struct Subject {
    wire: String,
    permissions: &'static str,
}

impl Subject {
    /// The caller as the adapter takes it, holding its permissions.
    fn caller(&self) -> Caller {
        Caller::new(self.wire.clone(), self.permissions.split(',').filter(|p| !p.is_empty()))
    }
}

/// A row: its key as the SDK takes it, its wire form, as the core names
/// it, and its column's text.
struct Row {
    id: types::IdentityUUID,
    wire: String,
    text: String,
}

/// What a case reads of a problem's row with SQL.
#[derive(Debug)]
struct Problem {
    title: String,
    status: String,
    version: i64,
}

/// An occupant of a problem's seats, as its sub-table holds it: the seat,
/// the subject, who seated it and when, in epoch milliseconds.
#[derive(Debug, PartialEq)]
struct Occupant {
    seat: String,
    subject: String,
    seated_by: String,
    seated_at: i64,
}

/// What a case reads of a sandbox's row with SQL, each instant in epoch
/// milliseconds.
#[derive(Debug)]
struct Sandbox {
    status: String,
    observed: Option<String>,
    observed_at: Option<i64>,
    drift: Option<Value>,
    reconciled_at: Option<i64>,
    version: i64,
}

/// A live message of a deadline queue: its use, its deadline's name and
/// the time it was armed at, in epoch milliseconds.
#[derive(Debug, PartialEq)]
struct Message {
    use_id: String,
    name: String,
    at: i64,
}

/// A JSON column's text as its value.
fn json_of(text: Option<String>) -> Option<Value> {
    text.map(|text| serde_json::from_str(&text).unwrap())
}

impl Database {
    async fn drop_schema(&self) {
        self.admin.batch_execute(&format!("DROP SCHEMA {} CASCADE", self.schema)).await.unwrap();
    }

    /// A fresh UUID in its three forms.
    async fn uuid(&self) -> (types::IdentityUUID, String, String) {
        let text: String = self.client.query_one("SELECT gen_random_uuid()::text", &[]).await.unwrap().get(0);
        let id: types::IdentityUUID = text.parse().unwrap();
        let wire = wire_of(&id);
        (id, wire, text)
    }

    /// A fresh subject holding permissions.
    async fn subject(&self, permissions: &'static str) -> Subject {
        let (_, wire, _) = self.uuid().await;
        Subject { wire, permissions }
    }

    /// Creates a row of a type through the core, as the repository's
    /// create does, with a key of its own, its uses' initializers run
    /// (Workflow's enters its first state).
    async fn create(&self, type_name: &str, own: Value) -> Row {
        let (id, wire, text) = self.uuid().await;
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let options = CreateOptions { id: Some(wire.clone()), ..CreateOptions::default() };
        let key = postgres::run_create(&host, &*tx, type_name, object(own), tester(), &options).await.unwrap();
        tx.commit().await.unwrap();
        assert_eq!(key, wire);
        Row { id, wire, text }
    }

    /// A problem whose agent is the agent's subject, open.
    async fn problem(&self, agent: &Subject) -> Row {
        self.create("Problem", json!({"title": "Flaky test", "agent": agent.wire})).await
    }

    /// A sandbox, pending.
    async fn sandbox(&self) -> Row {
        self.create("Sandbox", json!({"name": "box"})).await
    }

    /// An update of a row's own fields through the core as the caller,
    /// committed when it applies.
    async fn update(&self, type_name: &str, row: &Row, patch: Value, caller: &Subject) -> Result<(), Error> {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let updated = postgres::run_update(&host, &*tx, type_name, &row.wire, object(patch), caller.caller(), &ExecuteOptions::default()).await;
        if updated.is_ok() {
            tx.commit().await.unwrap();
        }
        updated
    }

    /// A behavior operation of a row through the core as the caller, its
    /// params as JSON, committed when it applies: what a route runs past
    /// its input's types.
    async fn operate(&self, type_name: &str, row: &Row, use_id: &str, operation: &str, params: Value, caller: &Subject) -> Result<Value, Error> {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let ran = postgres::run_operation(&host, &*tx, type_name, &row.wire, use_id, operation, params, caller.caller()).await;
        if ran.is_ok() {
            tx.commit().await.unwrap();
        }
        ran
    }

    async fn problem_row(&self, problem: &Row) -> Problem {
        let row = self
            .client
            .query_one("SELECT title, status, _version::bigint FROM problem WHERE id::text = $1", &[&problem.text])
            .await
            .unwrap();
        Problem { title: row.get(0), status: row.get(1), version: row.get(2) }
    }

    /// A problem's occupants, in the order they were seated.
    async fn occupants(&self, problem: &Row) -> Vec<Occupant> {
        self.client
            .query(
                "SELECT seat, subject, seated_by, (extract(epoch FROM seated_at) * 1000)::bigint FROM problem_seats_occupants WHERE problem_id::text = $1 ORDER BY number",
                &[&problem.text],
            )
            .await
            .unwrap()
            .iter()
            .map(|row| Occupant { seat: row.get(0), subject: row.get(1), seated_by: row.get(2), seated_at: row.get(3) })
            .collect()
    }

    async fn sandbox_row(&self, sandbox: &Row) -> Sandbox {
        let row = self
            .client
            .query_one(
                "SELECT status, observed, (extract(epoch FROM observed_observed_at) * 1000)::bigint, observed_drift::text, (extract(epoch FROM observed_reconciled_at) * 1000)::bigint, _version::bigint FROM sandbox WHERE id::text = $1",
                &[&sandbox.text],
            )
            .await
            .unwrap();
        Sandbox {
            status: row.get(0),
            observed: row.get(1),
            observed_at: row.get(2),
            drift: json_of(row.get(3)),
            reconciled_at: row.get(4),
            version: row.get(5),
        }
    }

    /// The live messages of the sandbox's deadline queue, by name.
    async fn live(&self, sandbox: &Row) -> Vec<Message> {
        self.client
            .query(
                "SELECT use_id, name, at FROM sandbox_deadline_queue WHERE row_key::text = $1 AND state IN ('ready', 'claimed') ORDER BY name",
                &[&sandbox.text],
            )
            .await
            .unwrap()
            .iter()
            .map(|row| Message { use_id: row.get(0), name: row.get(1), at: row.get(2) })
            .collect()
    }

    /// Makes the sandbox's deadline due now, as an operator's repair would:
    /// the worker claims it, and the core runs it at the time it was armed
    /// to fall due at.
    async fn make_due(&self, sandbox: &Row, name: &str) {
        let changed = self
            .client
            .execute("UPDATE sandbox_deadline_queue SET due_at = now() WHERE row_key::text = $1 AND name = $2 AND state = 'ready'", &[&sandbox.text, &name])
            .await
            .unwrap();
        assert_eq!(changed, 1, "one ready message of {name} on sandbox_deadline_queue");
    }
}

fn tester() -> Caller {
    Caller::new("tester", Vec::<String>::new())
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().unwrap_or_default()
}

/// SandboxDeadlines' queue in a claim loop of its own, as its main runs
/// it, until shutdown.
fn start_worker(pool: &Pool, shutdown: &Shutdown) -> tokio::task::JoinHandle<Result<(), QueueError>> {
    let options = Options { concurrency: 2, idle: Duration::from_millis(100), ..Options::default() };
    let queue = sandbox_deadlines_deadlines(pool).unwrap();
    let on_queue = pool.clone();
    tokio::spawn(run_worker(
        queue,
        move |claimed| {
            let pool = on_queue.clone();
            async move { run_sandbox_deadlines_deadline(&pool, claimed).await }
        },
        options,
        shutdown.clone(),
    ))
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

/// The SDK as a subject holding its permissions.
fn sdk(base_url: &str, subject: &Subject) -> FixtureSeatsApiSdk {
    let token = format!("{}:{}", subject.wire, subject.permissions);
    FixtureSeatsApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), Some(token), None)).unwrap()
}

/// A POST of a body the SDK's types do not let a caller build (a state no
/// Workflow names), as a client in another language may send it, through
/// the SDK's client as the subject: the refusal the route answers.
async fn post_untyped(base_url: &str, subject: &Subject, path: &str, body: Value) -> SDKError {
    let token = format!("{}:{}", subject.wire, subject.permissions);
    let client = HttpClient::new(ClientConfig::with_base_url(base_url.to_string(), Some(token), None)).unwrap();
    match client.request_json::<Value>("POST", path, &[], Some(body), None).await {
        Ok(answer) => panic!("POST {path} answered {answer}, not a refusal"),
        Err(err) => err,
    }
}

/// The key's wire form, as the core names a row.
fn wire_of(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_string()
}

/// A value of a generated type from its JSON.
fn of<T: DeserializeOwned>(value: Value) -> T {
    serde_json::from_value(value).unwrap()
}

/// A generated type's JSON, its null members left out, as an absent
/// optional member and a null are one to the cases.
fn value<T: Serialize>(result: T) -> Value {
    bare(serde_json::to_value(result).unwrap())
}

/// A JSON value without the null members of its objects.
fn bare(value: Value) -> Value {
    match value {
        Value::Object(members) => Value::Object(members.into_iter().filter(|(_, v)| !v.is_null()).map(|(k, v)| (k, bare(v))).collect()),
        Value::Array(items) => Value::Array(items.into_iter().map(bare).collect()),
        other => other,
    }
}

/// The status, code, message and details of a refused call.
fn refusal(err: SDKError) -> (u16, String, String, Value) {
    let details = err.problem().and_then(|problem| problem.details.clone()).unwrap_or(Value::Null);
    match err {
        SDKError::Api { status_code, message, code, .. } => (status_code, code.unwrap_or_default(), message, details),
        other => panic!("not a refusal of the API: {other}"),
    }
}

/// A refused call's status, veto code and veto details.
fn veto(err: SDKError) -> (u16, Value, Value) {
    let (status, code, message, details) = refusal(err);
    assert_eq!(code, "vetoed", "{message}");
    (status, details["vetoCode"].clone(), details["vetoDetails"].clone())
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

fn now_ms() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as i64
}

/// Sandbox's staleAfterMs: five minutes.
const STALE_AFTER_MS: i64 = 300_000;

fn case(name: &str) {
    println!("case: {name}");
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn seats_and_reconcile_run_through_the_core_and_the_worker() {
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
`

// seatsRoundtripCases are the cases of tests/behavior_seats.rs, in
// seatsCases' order and words: Seats' and the seats in by's first, then
// Reconcile's, each making rows of its own.
const seatsRoundtripCases = `
async fn cases(db: Arc<Database>) {
    let base_url = serve(db.pool.clone()).await;
    let shutdown = Shutdown::new();
    let worker = start_worker(&db.pool, &shutdown);
    seats_cases(&db, &base_url).await;
    reconcile_cases(&db, &base_url).await;
    shutdown.cancel();
    worker.await.unwrap().unwrap();
}

async fn seats_cases(db: &Database, base_url: &str) {
    let person = db.subject("problems.own,problems.assign").await;
    let person2 = db.subject("problems.own").await;
    let agent = db.subject("problems.work").await;
    let agent2 = db.subject("problems.work").await;
    let nobody = db.subject("").await;
    let releaser = db.subject("problems.release").await;
    let moderator = db.subject("problems.override").await;
    let (as_person, as_person2, as_agent, as_agent2) = (sdk(base_url, &person), sdk(base_url, &person2), sdk(base_url, &agent), sdk(base_url, &agent2));
    let (as_nobody, as_releaser, as_moderator) = (sdk(base_url, &nobody), sdk(base_url, &releaser), sdk(base_url, &moderator));

    case("a caller holding take seats itself as accountable, and the next to take the seat displaces it (onFull displace)");
    let p = db.problem(&agent).await;
    let taken = value(as_person.problem.seats.take_seat(p.id, of(json!({"seat": "accountable"})), None).await.unwrap());
    assert_eq!(taken, json!({"seat": "accountable", "subject": person.wire, "displaced": []}));
    let before = now_ms();
    let taken = value(as_person2.problem.seats.take_seat(p.id, of(json!({"seat": "accountable"})), None).await.unwrap());
    let after = now_ms();
    assert_eq!(taken, json!({"seat": "accountable", "subject": person2.wire, "displaced": [person.wire]}));
    let listed = value(as_person.problem.seats.list_seats(p.id, of(json!({"seat": "accountable"})), None).await.unwrap());
    let items = listed["items"].as_array().expect("items").clone();
    assert_eq!(items.len(), 1, "{listed}");
    assert_eq!(
        (&items[0]["seat"], &items[0]["subject"], &items[0]["seatedBy"], &items[0]["fromField"]),
        (&json!("accountable"), &json!(person2.wire), &json!(person2.wire), &json!(false)),
        "{listed}"
    );
    let seated_at = items[0]["seatedAt"].as_i64().expect("seatedAt");
    assert!((before..=after).contains(&seated_at), "{listed}");
    assert_eq!(
        db.occupants(&p).await,
        [Occupant { seat: "accountable".into(), subject: person2.wire.clone(), seated_by: person2.wire.clone(), seated_at }]
    );

    case("a take of a seat the caller holds already answers unchanged and leaves the row's _version where it was");
    let held = db.problem_row(&p).await.version;
    let again = value(as_person2.problem.seats.take_seat(p.id, of(json!({"seat": "accountable"})), None).await.unwrap());
    assert_eq!(again, json!({"seat": "accountable", "subject": person2.wire, "displaced": [], "unchanged": true}));
    assert_eq!(db.problem_row(&p).await.version, held, "a repeat writes nothing");
    assert_eq!(db.occupants(&p).await.len(), 1);

    case("a take by a caller without the seat's take permission is 403 forbidden");
    let held = db.problem_row(&p).await.version;
    let (status, code, message, _) = refusal(as_agent.problem.seats.take_seat(p.id, of(json!({"seat": "accountable"})), None).await.unwrap_err());
    assert_eq!((status, code.as_str()), (403, "forbidden"), "{message}");
    assert!(
        message.contains(&format!("{} may not take a seat in Problem {}'s accountable: it needs permission problems.own", agent.wire, p.wire)),
        "{message}"
    );
    assert_eq!(db.problem_row(&p).await.version, held, "a refused take writes nothing");

    case("the row's agent is seated as responsible through its field: listSeats lists it first, and its leaveSeat is 409 held_by_field");
    let p = db.problem(&agent).await;
    as_agent2.problem.seats.take_seat(p.id, of(json!({"seat": "responsible"})), None).await.unwrap();
    let listed = value(as_nobody.problem.seats.list_seats(p.id, of(json!({"seat": "responsible"})), None).await.unwrap());
    let items = listed["items"].as_array().expect("items").clone();
    assert_eq!(items.len(), 2, "{listed}");
    assert_eq!(items[0], json!({"seat": "responsible", "subject": agent.wire, "fromField": true}), "{listed}");
    assert!(items[0]["seatedAt"].is_null() && items[0]["seatedBy"].is_null(), "{listed}");
    assert_eq!(
        (&items[1]["subject"], &items[1]["seatedBy"], &items[1]["fromField"]),
        (&json!(agent2.wire), &json!(agent2.wire), &json!(false)),
        "{listed}"
    );
    let held = db.problem_row(&p).await.version;
    let (status, code, vetoed) = veto(as_agent.problem.seats.leave_seat(p.id, of(json!({"seat": "responsible"})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("held_by_field"), json!({"seat": "responsible", "subject": agent.wire, "field": "agent"})));
    assert_eq!(db.problem_row(&p).await.version, held);

    case("a sixth handle is 409 seat_full");
    let p = db.problem(&agent).await;
    let mut seated = Vec::new();
    for _ in 0..5 {
        let claimer = db.subject("").await;
        let taken = value(sdk(base_url, &claimer).problem.seats.take_seat(p.id, of(json!({"seat": "handles"})), None).await.unwrap());
        assert_eq!(taken, json!({"seat": "handles", "subject": claimer.wire, "displaced": []}));
        seated.push(claimer.wire);
    }
    let held = db.problem_row(&p).await.version;
    let sixth = db.subject("").await;
    let (status, code, vetoed) = veto(sdk(base_url, &sixth).problem.seats.take_seat(p.id, of(json!({"seat": "handles"})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("seat_full"), json!({"seat": "handles", "max": 5, "seated": seated})));
    assert_eq!(db.problem_row(&p).await.version, held);
    assert_eq!(db.occupants(&p).await.len(), 5);

    case("a handle is released by the agent that claimed it or by a caller holding assign, and another caller's release is 403 forbidden");
    let p = db.problem(&agent).await;
    as_agent.problem.seats.take_seat(p.id, of(json!({"seat": "handles"})), None).await.unwrap();
    let held = db.problem_row(&p).await.version;
    let (status, code, message, _) =
        refusal(as_nobody.problem.seats.leave_seat(p.id, of(json!({"seat": "handles", "subject": agent.wire})), None).await.unwrap_err());
    assert_eq!((status, code.as_str()), (403, "forbidden"), "{message}");
    assert!(
        message.contains(&format!("{} may not unseat {} from Problem {}'s handles: unseating another needs permission problems.release", nobody.wire, agent.wire, p.wire)),
        "{message}"
    );
    assert_eq!(db.problem_row(&p).await.version, held);
    let left = value(as_agent.problem.seats.leave_seat(p.id, of(json!({"seat": "handles"})), None).await.unwrap());
    assert_eq!(left, json!({"seat": "handles", "subject": agent.wire}));
    as_agent2.problem.seats.take_seat(p.id, of(json!({"seat": "handles"})), None).await.unwrap();
    let released = value(as_releaser.problem.seats.leave_seat(p.id, of(json!({"seat": "handles", "subject": agent2.wire})), None).await.unwrap());
    assert_eq!(released, json!({"seat": "handles", "subject": agent2.wire}));
    assert!(db.occupants(&p).await.is_empty());

    case("a leave of a seat the caller does not hold answers unchanged");
    let held = db.problem_row(&p).await.version;
    let left = value(as_agent.problem.seats.leave_seat(p.id, of(json!({"seat": "handles"})), None).await.unwrap());
    assert_eq!(left, json!({"seat": "handles", "subject": agent.wire, "unchanged": true}));
    assert_eq!(db.problem_row(&p).await.version, held, "a leave of a seat not held writes nothing");

    case("a transition whose by names seat:responsible is made by a subject seated there, and another caller is 409 not_principal naming the seat");
    let p = db.problem(&agent).await;
    let held = db.problem_row(&p).await.version;
    let (status, code, vetoed) = veto(as_nobody.problem.status.transition(p.id, of(json!({"to": "working"})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("not_principal"), json!({"from": "open", "to": "working", "by": ["seat:responsible"]})));
    assert_eq!(db.problem_row(&p).await.version, held);
    let moved = value(as_agent.problem.status.transition(p.id, of(json!({"to": "working"})), None).await.unwrap());
    assert_eq!(moved, json!({"from": "open", "to": "working"}));
    assert_eq!(db.problem_row(&p).await.status, "working");

    case("a caller holding overridePermission moves past by");
    let p = db.problem(&agent).await;
    let moved = value(as_moderator.problem.status.transition(p.id, of(json!({"to": "working"})), None).await.unwrap());
    assert_eq!(moved, json!({"from": "open", "to": "working"}));
    assert_eq!(db.problem_row(&p).await.status, "working");

    case("in review the accountable subject edits the title StateRules freezes, and another caller's edit is 409 frozen_fields naming seat:accountable");
    let p = db.problem(&agent).await;
    as_agent.problem.status.transition(p.id, of(json!({"to": "working"})), None).await.unwrap();
    as_agent.problem.status.transition(p.id, of(json!({"to": "review"})), None).await.unwrap();
    as_person.problem.seats.take_seat(p.id, of(json!({"seat": "accountable"})), None).await.unwrap();
    db.update("Problem", &p, json!({"title": "Reviewed"}), &person).await.unwrap();
    let held = db.problem_row(&p).await;
    assert_eq!((held.title.as_str(), held.status.as_str()), ("Reviewed", "review"));
    let refusal_of = refused(db.update("Problem", &p, json!({"title": "Mine"}), &agent2).await);
    assert_eq!((refusal_of.status, refusal_of.code.as_str()), (409, "vetoed"), "{}", refusal_of.message);
    assert_eq!(details(&refusal_of)["vetoCode"], json!("frozen_fields"));
    assert_eq!(details(&refusal_of)["vetoDetails"], json!({"state": "review", "fields": ["title"], "by": ["seat:accountable"]}));
    let stored = db.problem_row(&p).await;
    assert_eq!((stored.title.as_str(), stored.version), ("Reviewed", held.version), "a refused update writes nothing");
}

async fn reconcile_cases(db: &Database, base_url: &str) {
    let poller = db.subject("sandboxes.observe").await;
    let operator = db.subject("sandboxes.observe,sandboxes.stop").await;
    let nobody = db.subject("").await;
    let (as_poller, as_operator, as_nobody) = (sdk(base_url, &poller), sdk(base_url, &operator), sdk(base_url, &nobody));

    case("observe moves the sandbox along the fewest transitions to the observed state and records observed, observedAt and reconciledAt");
    let s = db.sandbox().await;
    let at = now_ms() - 1000;
    let before = now_ms();
    let observed = value(as_poller.sandbox.observed.observe(s.id, of(json!({"state": "running", "at": at})), None).await.unwrap());
    let after = now_ms();
    assert_eq!(
        observed,
        json!({"observed": "running", "observedAt": at, "status": "running", "moves": [{"from": "pending", "to": "starting"}, {"from": "starting", "to": "running"}]})
    );
    let stored = db.sandbox_row(&s).await;
    assert_eq!((stored.status.as_str(), stored.observed.as_deref(), stored.observed_at, &stored.drift), ("running", Some("running"), Some(at), &None), "{stored:?}");
    let reconciled = stored.reconciled_at.expect("observed_reconciled_at");
    assert!((before..=after).contains(&reconciled), "{stored:?}");
    assert_eq!(db.live(&s).await, [Message { use_id: "observed".into(), name: "stale".into(), at: at + STALE_AFTER_MS }]);

    case("an observation no newer than observedAt answers unchanged and leaves the row's _version where it was");
    let held = db.sandbox_row(&s).await.version;
    let unchanged = json!({"observed": "running", "observedAt": at, "status": "running", "moves": [], "unchanged": true});
    let again = value(as_poller.sandbox.observed.observe(s.id, of(json!({"state": "running", "at": at})), None).await.unwrap());
    assert_eq!(again, unchanged);
    let older = value(as_poller.sandbox.observed.observe(s.id, of(json!({"state": "pending", "at": at - 500})), None).await.unwrap());
    assert_eq!(older, unchanged);
    let stored = db.sandbox_row(&s).await;
    assert_eq!((stored.version, stored.observed_at, stored.status.as_str()), (held, Some(at), "running"), "an observation no newer writes nothing");

    case("a step the observer may not make stops the path and records drift as forbidden, and an observer who may completes it and clears drift");
    let s = db.sandbox().await;
    let t = now_ms();
    as_poller.sandbox.observed.observe(s.id, of(json!({"state": "running", "at": t - 3000})), None).await.unwrap();
    let drifted = value(as_poller.sandbox.observed.observe(s.id, of(json!({"state": "stopped", "at": t - 2000})), None).await.unwrap());
    let drift = json!({"status": "running", "observed": "stopped", "code": "forbidden"});
    assert_eq!(drifted, json!({"observed": "stopped", "observedAt": t - 2000, "status": "running", "moves": [], "drift": drift}));
    let stored = db.sandbox_row(&s).await;
    assert_eq!((stored.status.as_str(), stored.observed.as_deref(), stored.drift.as_ref()), ("running", Some("stopped"), Some(&drift)), "{stored:?}");
    let completed = value(as_operator.sandbox.observed.observe(s.id, of(json!({"state": "stopped", "at": t - 1000})), None).await.unwrap());
    assert_eq!(
        completed,
        json!({"observed": "stopped", "observedAt": t - 1000, "status": "stopped", "moves": [{"from": "running", "to": "stopping"}, {"from": "stopping", "to": "stopped"}]})
    );
    let stored = db.sandbox_row(&s).await;
    assert_eq!((stored.status.as_str(), stored.drift), ("stopped", None), "the drift column is NULL again");

    case("observe without observePermission is 403 forbidden, and a state the Workflow lacks is 400 invalid_argument");
    let s = db.sandbox().await;
    let held = db.sandbox_row(&s).await.version;
    let (status, code, message, _) = refusal(as_nobody.sandbox.observed.observe(s.id, of(json!({"state": "running", "at": now_ms() - 1000})), None).await.unwrap_err());
    assert_eq!((status, code.as_str()), (403, "forbidden"), "{message}");
    assert!(
        message.contains(&format!("{} may not observe Sandbox {}'s observed: observe needs permission sandboxes.observe", nobody.wire, s.wire)),
        "{message}"
    );
    // The SDK's input types the state as the Workflow's states, and the
    // route checks a body by them before the core runs, so a state no
    // Workflow names is the route's 400; the adapter hands it to the core,
    // which refuses it in Reconcile's words.
    let path = format!("/api/sandbox/{}/observed/observe", s.wire);
    let (status, code, message, _) = refusal(post_untyped(base_url, &poller, &path, json!({"state": "hibernating", "at": now_ms() - 1000})).await);
    assert_eq!(status, 400, "{code}: {message}");
    let params = json!({"state": "hibernating", "at": now_ms() - 1000});
    let refusal_of = refused(db.operate("Sandbox", &s, "observed", "observe", params, &poller).await.map(|_| ()));
    assert_eq!(
        (refusal_of.status, refusal_of.code.as_str(), refusal_of.class.as_str(), refusal_of.message.as_str()),
        (
            400,
            "invalid_argument",
            "OperationParamsError",
            "operation observe of behavior Reconcile on field observed: /state: \"hibernating\" is not a state of Sandbox.status (pending, starting, running, stopping, stopped, failed, unreachable)"
        )
    );
    let stored = db.sandbox_row(&s).await;
    assert_eq!((stored.version, stored.status.as_str(), stored.observed), (held, "pending", None), "a refused observation writes nothing");

    case("the stale deadline falls due on the worker five minutes after the last observation and moves the sandbox to unreachable");
    let s = db.sandbox().await;
    let at = now_ms() - 1000;
    as_poller.sandbox.observed.observe(s.id, of(json!({"state": "running", "at": at})), None).await.unwrap();
    assert_eq!(db.live(&s).await, [Message { use_id: "observed".into(), name: "stale".into(), at: at + STALE_AFTER_MS }]);
    db.make_due(&s, "stale").await;
    wait_until("the worker moved the sandbox to unreachable", || async { db.sandbox_row(&s).await.status == "unreachable" }).await;
    let stored = db.sandbox_row(&s).await;
    assert_eq!((stored.observed.as_deref(), stored.observed_at, stored.drift.as_ref()), (Some("unreachable"), Some(at), None), "{stored:?}");
    assert!(db.live(&s).await.is_empty(), "the fire spent its message and arms none at unreachable");

    case("a newer observation moves the sandbox back from unreachable");
    let at = now_ms();
    let back = value(as_poller.sandbox.observed.observe(s.id, of(json!({"state": "running", "at": at})), None).await.unwrap());
    assert_eq!(back, json!({"observed": "running", "observedAt": at, "status": "running", "moves": [{"from": "unreachable", "to": "running"}]}));
    let stored = db.sandbox_row(&s).await;
    assert_eq!((stored.status.as_str(), stored.observed.as_deref(), stored.observed_at, stored.drift), ("running", Some("running"), Some(at), None));
    assert_eq!(db.live(&s).await, [Message { use_id: "observed".into(), name: "stale".into(), at: at + STALE_AFTER_MS }]);
}
`
