package rustrestgen

// agentsRoundtripHead is the head of tests/behavior_agents.rs: its uses,
// its authenticator, its schema of fixture-agents-db's tables and what the
// cases read and write with SQL and through the adapter. The only real
// waits are the workers': a deadline a case needs due is made due with
// SQL, an operator's escape hatch, and the worker's run polled for every
// 100 ms for at most ten seconds; it runs at the later of the clock and the
// deadline's time, so the core sees the lease lapsed, the backoff ended and
// the session missed.
const agentsRoundtripHead = `//! Lease, Retries, Presence and Budget on tables end to end (D104):
//! fixture-agents-api's router over a schema of its own in the Postgres at
//! SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, which holds fixture-agents-db's
//! tables, its jobs', sessions' and accounts' behavior operations called
//! through the generated Rust SDK (a job's fencing token in an input's
//! preconditions member), their creates and updates run through the
//! adapter, the caller's preconditions in its ExecuteOptions, and its
//! behavior workers JobDeadlines' and SessionPresence's queues and handlers
//! run in process, by subjects that are UUIDs in their base62 form.

use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use API_CRATE::behaviors::{
    job_deadlines_deadlines, run_job_deadlines_deadline, run_session_presence_deadline, session_presence_deadlines, RULES_LAYOUT,
};
use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::queue::QueueError;
use API_CRATE::runtime::worker::{run_worker, Options};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, Shutdown};
use API_CRATE::{build_router, types, Handlers, Implementations};
use SDK_CRATE::{ClientConfig, FixtureAgentsApiSdk, SDKError};
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

/// A schema of its own holding fixture-agents-db's tables, a pool whose
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
    let schema = format!("behavior_agents_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("behaviors_postgres.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(8).build().unwrap();
    Database { pool, client, admin, schema }
}

/// A subject: a UUID in its base62 form, as the identity runtime gives a
/// principal's subject and the core holds a UUID field and a lease's
/// holder.
struct Subject {
    wire: String,
}

/// A row: its key as the SDK takes it, its wire form, as the core names
/// it, and its column's text.
struct Row {
    id: types::IdentityUUID,
    wire: String,
    text: String,
}

/// What a case reads of a job's row with SQL, each instant in epoch
/// milliseconds.
#[derive(Debug)]
struct Job {
    title: String,
    status: String,
    holder: Option<String>,
    token: Option<i64>,
    expiries: Option<i64>,
    ended: Option<Value>,
    retry_at: Option<i64>,
    exhausted: Option<bool>,
    total: Option<i64>,
    class_attempts: Option<Value>,
    keyed_by: Option<String>,
    version: i64,
}

/// What a case reads of a session's row with SQL.
#[derive(Debug)]
struct Session {
    status: String,
    entered_at: i64,
    deadline: Option<i64>,
    last_beat_at: Option<i64>,
    missed: Option<bool>,
    released: Option<Value>,
    version: i64,
}

/// A live message of a deadline queue: its deadline's name and the time it
/// was armed at, in epoch milliseconds.
#[derive(Debug, PartialEq)]
struct Message {
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

    /// A fresh subject.
    async fn subject(&self) -> Subject {
        let (_, wire, _) = self.uuid().await;
        Subject { wire }
    }

    /// Creates a row of a type through the core, as the repository's
    /// create does, with a key of its own, its uses' initializers run
    /// (Presence's arms its miss).
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

    /// A job, at revision 1.
    async fn job(&self, title: &str) -> Row {
        self.create("Job", json!({"title": title, "revision": 1})).await
    }

    /// An update of a row's own fields through the core as the caller, the
    /// preconditions it presents in its options, committed when it applies.
    async fn update(&self, type_name: &str, row: &Row, patch: Value, caller: &Subject, preconditions: Value) -> Result<(), Error> {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let options = ExecuteOptions { preconditions: object(preconditions), ..ExecuteOptions::default() };
        let caller = Caller::new(caller.wire.clone(), Vec::<String>::new());
        let updated = postgres::run_update(&host, &*tx, type_name, &row.wire, object(patch), caller, &options).await;
        if updated.is_ok() {
            tx.commit().await.unwrap();
        }
        updated
    }

    async fn job_row(&self, job: &Row) -> Job {
        let row = self
            .client
            .query_one(
                "SELECT title, status, lease_holder, lease_token::bigint, lease_expiries::bigint, lease_ended::text, (extract(epoch FROM retries_retry_at) * 1000)::bigint, retries_exhausted, retries_total::bigint, retries_class_attempts::text, retries_keyed_by, _version::bigint FROM job WHERE id::text = $1",
                &[&job.text],
            )
            .await
            .unwrap();
        Job {
            title: row.get(0),
            status: row.get(1),
            holder: row.get(2),
            token: row.get(3),
            expiries: row.get(4),
            ended: json_of(row.get(5)),
            retry_at: row.get(6),
            exhausted: row.get(7),
            total: row.get(8),
            class_attempts: json_of(row.get(9)),
            keyed_by: row.get(10),
            version: row.get(11),
        }
    }

    async fn session_row(&self, session: &Row) -> Session {
        let row = self
            .client
            .query_one(
                "SELECT status, (extract(epoch FROM status_entered_at) * 1000)::bigint, (extract(epoch FROM presence_deadline) * 1000)::bigint, (extract(epoch FROM presence_last_beat_at) * 1000)::bigint, presence_missed, presence_released::text, _version::bigint FROM session WHERE id::text = $1",
                &[&session.text],
            )
            .await
            .unwrap();
        Session {
            status: row.get(0),
            entered_at: row.get(1),
            deadline: row.get(2),
            last_beat_at: row.get(3),
            missed: row.get(4),
            released: json_of(row.get(5)),
            version: row.get(6),
        }
    }

    /// An account's version and status.
    async fn account_row(&self, account: &Row) -> (i64, String) {
        let row = self.client.query_one("SELECT _version::bigint, status FROM account WHERE id::text = $1", &[&account.text]).await.unwrap();
        (row.get(0), row.get(1))
    }

    /// A meter's row of a Budget table's meters sub-table: what it used and
    /// when its period started, in epoch milliseconds.
    async fn meter(&self, table: &str, row: &Row, meter: &str) -> Option<(i64, Option<i64>)> {
        let text = format!("SELECT used::bigint, (extract(epoch FROM period_start) * 1000)::bigint FROM {table}_budget_meters WHERE {table}_id::text = $1 AND meter = $2");
        self.client.query_opt(&text, &[&row.text, &meter]).await.unwrap().map(|row| (row.get(0), row.get(1)))
    }

    /// The buckets of an account's rolling meter: each one's start, in
    /// epoch milliseconds, and what it used.
    async fn buckets(&self, account: &Row, meter: &str) -> Vec<(i64, i64)> {
        self.client
            .query(
                "SELECT (extract(epoch FROM starts_at) * 1000)::bigint, used::bigint FROM account_budget_buckets WHERE account_id::text = $1 AND meter = $2 ORDER BY starts_at",
                &[&account.text, &meter],
            )
            .await
            .unwrap()
            .iter()
            .map(|row| (row.get(0), row.get(1)))
            .collect()
    }

    /// The live messages of a table's deadline queue for a row, by name.
    async fn live(&self, queue: &str, row: &Row) -> Vec<Message> {
        let text = format!("SELECT name, at FROM {queue} WHERE row_key::text = $1 AND state IN ('ready', 'claimed') ORDER BY name");
        self.client
            .query(&text, &[&row.text])
            .await
            .unwrap()
            .iter()
            .map(|row| Message { name: row.get(0), at: row.get(1) })
            .collect()
    }

    /// Makes a row's deadline due now, as an operator's repair would: the
    /// worker claims it, and the core runs it at the time it was armed to
    /// fall due at.
    async fn make_due(&self, queue: &str, row: &Row, name: &str) {
        let text = format!("UPDATE {queue} SET due_at = now() WHERE row_key::text = $1 AND name = $2 AND state = 'ready'");
        let changed = self.client.execute(&text, &[&row.text, &name]).await.unwrap();
        assert_eq!(changed, 1, "one ready message of {name} on {queue}");
    }
}

fn tester() -> Caller {
    Caller::new("tester", Vec::<String>::new())
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().unwrap_or_default()
}

/// JobDeadlines' and SessionPresence's queues, each in a claim loop of its
/// own, as their mains run them, until shutdown.
fn start_workers(pool: &Pool, shutdown: &Shutdown) -> Vec<tokio::task::JoinHandle<Result<(), QueueError>>> {
    let options = Options { concurrency: 2, idle: Duration::from_millis(100), ..Options::default() };
    let jobs = job_deadlines_deadlines(pool).unwrap();
    let sessions = session_presence_deadlines(pool).unwrap();
    let (on_jobs, on_sessions) = (pool.clone(), pool.clone());
    vec![
        tokio::spawn(run_worker(
            jobs,
            move |claimed| {
                let pool = on_jobs.clone();
                async move { run_job_deadlines_deadline(&pool, claimed).await }
            },
            options,
            shutdown.clone(),
        )),
        tokio::spawn(run_worker(
            sessions,
            move |claimed| {
                let pool = on_sessions.clone();
                async move { run_session_presence_deadline(&pool, claimed).await }
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

/// The SDK as a subject holding permissions.
fn sdk(base_url: &str, subject: &Subject, permissions: &str) -> FixtureAgentsApiSdk {
    let token = format!("{}:{permissions}", subject.wire);
    FixtureAgentsApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), Some(token), None)).unwrap()
}

/// The key's wire form, as the core names a row.
fn wire_of(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_string()
}

/// A value of a generated type from its JSON.
fn of<T: DeserializeOwned>(value: Value) -> T {
    serde_json::from_value(value).unwrap()
}

/// A generated type's JSON.
fn value<T: Serialize>(result: T) -> Value {
    serde_json::to_value(result).unwrap()
}

/// The input that presents a lease's token as Lease's precondition, beside
/// what else the input holds.
fn fenced(token: i64, input: Value) -> Value {
    let mut input = object(input);
    input.insert("preconditions".into(), json!({"Lease": {"token": token}}));
    Value::Object(input)
}

/// The status, code, message and details of a refused call.
fn refusal(err: SDKError) -> (u16, String, String, Value) {
    let details = err.problem().and_then(|problem| problem.details.clone()).unwrap_or(Value::Null);
    match err {
        SDKError::Api { status_code, message, code, .. } => (status_code, code.unwrap_or_default(), message, details),
        other => panic!("not a refusal of the API: {other}"),
    }
}

/// A refused call's veto code and details.
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

const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;

fn case(name: &str) {
    println!("case: {name}");
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn lease_retries_presence_and_budget_run_through_the_core_and_the_workers() {
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

// agentsRoundtripCases are the cases of tests/behavior_agents.rs, in
// agentsCases' order and words: Lease's and Retries' first, then
// Presence's and Budget's, each making rows of its own.
const agentsRoundtripCases = `
async fn cases(db: Arc<Database>) {
    let base_url = serve(db.pool.clone()).await;
    let shutdown = Shutdown::new();
    let workers = start_workers(&db.pool, &shutdown);
    let (worker, other) = (db.subject().await, db.subject().await);
    let as_worker = sdk(&base_url, &worker, "");
    let as_other = sdk(&base_url, &other, "");
    lease_cases(&db, &worker, &as_worker, &as_other).await;
    retries_cases(&db, &worker, &as_worker, &as_other).await;
    presence_cases(&db, &base_url).await;
    budget_cases(&db, &base_url).await;
    shutdown.cancel();
    for worker in workers {
        worker.await.unwrap().unwrap();
    }
}

async fn lease_cases(db: &Database, worker: &Subject, as_worker: &FixtureAgentsApiSdk, as_other: &FixtureAgentsApiSdk) {
    case("a worker acquires a job's lease and gets its fencing token, and a heartbeat that presents it renews the lease");
    let j = db.job("j1").await;
    let before = now_ms();
    let acquired = value(as_worker.job.lease.acquire(j.id, of(json!({})), None).await.unwrap());
    let after = now_ms();
    assert_eq!((&acquired["token"], &acquired["heartbeatMs"]), (&json!(1), &json!(20000)), "{acquired}");
    let expires = acquired["expiresAt"].as_i64().expect("expiresAt");
    assert!((before + MINUTE..=after + MINUTE).contains(&expires), "{acquired}");
    let beat = value(as_worker.job.lease.heartbeat(j.id, of(fenced(1, json!({}))), None).await.unwrap());
    assert_eq!(beat["directives"], json!([]), "{beat}");
    assert!(beat["expiresAt"].as_i64().expect("expiresAt") >= expires, "{beat}");
    let stored = db.job_row(&j).await;
    assert_eq!((stored.holder.as_deref(), stored.token), (Some(worker.wire.as_str()), Some(1)));

    case("under requireToken a write that presents no token is refused as token_required, and the holder's write that presents it is saved");
    let j = db.job("j2").await;
    as_worker.job.lease.acquire(j.id, of(json!({})), None).await.unwrap();
    let held = db.job_row(&j).await.version;
    let (status, code, _) = veto(as_worker.job.status.transition(j.id, of(json!({"to": "running"})), None).await.unwrap_err());
    assert_eq!((status, code), (409, json!("token_required")));
    assert_eq!(db.job_row(&j).await.version, held, "a refused write writes nothing");
    let moved = value(as_worker.job.status.transition(j.id, of(fenced(1, json!({"to": "running"}))), None).await.unwrap());
    assert_eq!(moved, json!({"from": "queued", "to": "running"}));
    let moved = db.job_row(&j).await.version;
    let refusal_of = refused(db.update("Job", &j, json!({"title": "Mine"}), worker, json!({})).await);
    assert_eq!((refusal_of.status, refusal_of.code.as_str()), (409, "vetoed"));
    assert_eq!(details(&refusal_of)["vetoCode"], json!("token_required"));
    assert_eq!(db.job_row(&j).await.version, moved, "a refused update writes nothing");
    db.update("Job", &j, json!({"title": "Mine"}), worker, json!({"Lease": {"token": 1}})).await.unwrap();
    let stored = db.job_row(&j).await;
    assert_eq!((stored.title.as_str(), stored.status.as_str()), ("Mine", "running"));

    case("a token a later lease replaced is refused as token_stale");
    let j = db.job("j3").await;
    as_worker.job.lease.acquire(j.id, of(json!({})), None).await.unwrap();
    as_worker.job.lease.release(j.id, of(fenced(1, json!({}))), None).await.unwrap();
    let taken = value(as_other.job.lease.acquire(j.id, of(json!({})), None).await.unwrap());
    assert_eq!(taken["token"], json!(3), "{taken}");
    let held = db.job_row(&j).await.version;
    let (status, code, vetoed) = veto(as_worker.job.lease.heartbeat(j.id, of(fenced(1, json!({}))), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("token_stale"), json!({"token": 1, "current": 3})));
    let refusal_of = refused(db.update("Job", &j, json!({"title": "Late"}), worker, json!({"Lease": {"token": 1}})).await);
    assert_eq!(details(&refusal_of)["vetoCode"], json!("token_stale"));
    assert_eq!(db.job_row(&j).await.version, held, "a stale token writes nothing");

    case("another caller's acquire of a job whose lease is active is refused as held_by_another");
    let j = db.job("j4").await;
    let taken = value(as_other.job.lease.acquire(j.id, of(json!({})), None).await.unwrap());
    let held = db.job_row(&j).await.version;
    let (status, code, vetoed) = veto(as_worker.job.lease.acquire(j.id, of(json!({})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("held_by_another"), json!({"expiresAt": taken["expiresAt"]})));
    assert_eq!(db.job_row(&j).await.version, held);
}

async fn retries_cases(db: &Database, worker: &Subject, as_worker: &FixtureAgentsApiSdk, as_other: &FixtureAgentsApiSdk) {
    case("a failure in a class with a backoff sets retryAt, and until it passes the move into running and the lease's acquire are refused as backing_off");
    let j = db.job("j5").await;
    as_worker.job.lease.acquire(j.id, of(json!({})), None).await.unwrap();
    as_worker.job.status.transition(j.id, of(fenced(1, json!({"to": "running"}))), None).await.unwrap();
    let before = now_ms();
    let attempt = value(as_worker.job.retries.record_attempt(j.id, of(fenced(1, json!({"failure": "timeout"}))), None).await.unwrap());
    let after = now_ms();
    let retry_at = attempt["retryAt"].as_i64().expect("retryAt");
    assert!((before + MINUTE..=after + MINUTE).contains(&retry_at), "{attempt}");
    assert_eq!(attempt["exhausted"], json!(false), "{attempt}");
    as_worker.job.lease.release(j.id, of(fenced(1, json!({}))), None).await.unwrap();
    assert_eq!(db.job_row(&j).await.status, "queued", "onExpiry moved the released job back to queued");
    let held = db.job_row(&j).await.version;
    let (status, code, vetoed) = veto(as_other.job.status.transition(j.id, of(json!({"to": "running"})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("backing_off"), json!({"retryAt": retry_at})));
    let (status, code, vetoed) = veto(as_other.job.lease.acquire(j.id, of(json!({})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("backing_off"), json!({"retryAt": retry_at})));
    assert_eq!(db.job_row(&j).await.version, held);

    case("when the backoff ends its then, run by the worker, moves the waiting job back to queued");
    let j = db.job("j6").await;
    as_worker.job.lease.acquire(j.id, of(json!({})), None).await.unwrap();
    as_worker.job.status.transition(j.id, of(fenced(1, json!({"to": "running"}))), None).await.unwrap();
    let attempt = value(as_worker.job.retries.record_attempt(j.id, of(fenced(1, json!({"failure": "timeout"}))), None).await.unwrap());
    let retry_at = attempt["retryAt"].as_i64().expect("retryAt");
    as_worker.job.status.transition(j.id, of(fenced(1, json!({"to": "waiting"}))), None).await.unwrap();
    as_worker.job.lease.release(j.id, of(fenced(1, json!({}))), None).await.unwrap();
    assert_eq!(db.live("job_deadline_queue", &j).await, [Message { name: "retry".into(), at: retry_at }]);
    db.make_due("job_deadline_queue", &j, "retry").await;
    wait_until("the worker moved j6 back to queued", || async { db.job_row(&j).await.status == "queued" }).await;
    assert_eq!(db.job_row(&j).await.retry_at, Some(retry_at), "the move keeps retryAt");
    assert!(db.live("job_deadline_queue", &j).await.is_empty());

    case("a lease that lapses expires when its deadline falls due on the worker: onExpiry moves the job back to queued and the token advances");
    let j = db.job("j7").await;
    let acquired = value(as_worker.job.lease.acquire(j.id, of(json!({})), None).await.unwrap());
    let expires = acquired["expiresAt"].as_i64().expect("expiresAt");
    as_worker.job.status.transition(j.id, of(fenced(1, json!({"to": "running"}))), None).await.unwrap();
    assert_eq!(db.live("job_deadline_queue", &j).await, [Message { name: "expire".into(), at: expires }]);
    db.make_due("job_deadline_queue", &j, "expire").await;
    wait_until("the worker expired j7's lease", || async { db.job_row(&j).await.status == "queued" }).await;
    let stored = db.job_row(&j).await;
    assert_eq!((stored.holder, stored.token, stored.expiries), (None, Some(2), Some(1)));
    assert_eq!(stored.ended, Some(json!({"reason": "ttl", "at": expires})));

    case("a second build failure of one revision exhausts the job, and a new revision starts its counts again");
    let j = db.job("j8").await;
    as_worker.job.lease.acquire(j.id, of(json!({})), None).await.unwrap();
    as_worker.job.status.transition(j.id, of(fenced(1, json!({"to": "running"}))), None).await.unwrap();
    let attempt = value(as_worker.job.retries.record_attempt(j.id, of(fenced(1, json!({"failure": "build"}))), None).await.unwrap());
    assert_eq!((&attempt["exhausted"], &attempt["classAttempts"]["build"]), (&json!(false), &json!(1)), "{attempt}");
    let attempt = value(as_worker.job.retries.record_attempt(j.id, of(fenced(1, json!({"failure": "build"}))), None).await.unwrap());
    assert_eq!(attempt["exhausted"], json!(true), "{attempt}");
    assert_eq!(db.job_row(&j).await.status, "failed");
    as_worker.job.lease.release(j.id, of(fenced(1, json!({}))), None).await.unwrap();
    let held = db.job_row(&j).await.version;
    let (status, code, _) = veto(as_other.job.status.transition(j.id, of(json!({"to": "queued"})), None).await.unwrap_err());
    assert_eq!((status, code), (409, json!("exhausted")));
    assert_eq!(db.job_row(&j).await.version, held);
    db.update("Job", &j, json!({"revision": 2}), worker, json!({})).await.unwrap();
    let stored = db.job_row(&j).await;
    assert_eq!((stored.exhausted, stored.total, stored.keyed_by.as_deref()), (Some(false), Some(0), Some("2")));
    assert_eq!(stored.class_attempts, Some(json!({"timeout": 0, "build": 0, "rejected": 0})));
    let moved = value(as_other.job.status.transition(j.id, of(json!({"to": "queued"})), None).await.unwrap());
    assert_eq!(moved, json!({"from": "failed", "to": "queued"}));

    case("a precondition for a use whose behavior takes none is refused as invalid_argument");
    let j = db.job("j9").await;
    let held = db.job_row(&j).await.version;
    let refusal_of = refused(db.update("Job", &j, json!({"title": "Other"}), worker, json!({"Retries": {"token": 1}})).await);
    assert_eq!(
        (refusal_of.status, refusal_of.code.as_str(), refusal_of.class.as_str(), refusal_of.message.as_str()),
        (400, "invalid_argument", "PreconditionsError", "preconditions of Job: /Retries: behavior Retries takes no precondition")
    );
    let err = as_worker.job.lease.heartbeat(j.id, of(fenced(-1, json!({}))), None).await.unwrap_err();
    assert_eq!(refusal(err).0, 400);
    assert_eq!(db.job_row(&j).await.version, held);
}

async fn presence_cases(db: &Database, base_url: &str) {
    let (device, other) = (db.subject().await, db.subject().await);
    let as_device = sdk(base_url, &device, "");
    let as_other = sdk(base_url, &other, "");
    let session = || db.create("Session", json!({"device": device.wire, "agent": "planner"}));

    case("a session's create arms its miss ttlMs on: its deadline and its queue message");
    let before = now_ms();
    let s = session().await;
    let after = now_ms();
    let stored = db.session_row(&s).await;
    let deadline = stored.deadline.expect("presence_deadline");
    assert!((before + 3 * MINUTE..=after + 3 * MINUTE).contains(&deadline), "{stored:?}");
    assert_eq!(stored.missed, Some(false), "{stored:?}");
    assert_eq!(db.live("session_deadline_queue", &s).await, [Message { name: "miss".into(), at: deadline }]);

    case("only the device's principal beats a session: another caller is 409 not_principal, and the device's beat moves the deadline ttlMs on");
    let s = session().await;
    let held = db.session_row(&s).await.version;
    let (status, code, _) = veto(as_other.session.presence.beat(s.id, None).await.unwrap_err());
    assert_eq!((status, code), (409, json!("not_principal")));
    assert_eq!(db.session_row(&s).await.version, held);
    let before = now_ms();
    let beat = value(as_device.session.presence.beat(s.id, None).await.unwrap());
    let after = now_ms();
    let deadline = beat["deadline"].as_i64().expect("deadline");
    assert!((before + 3 * MINUTE..=after + 3 * MINUTE).contains(&deadline), "{beat}");
    let stored = db.session_row(&s).await;
    assert_eq!(stored.deadline, Some(deadline));
    let beat_at = stored.last_beat_at.expect("presence_last_beat_at");
    assert!((before..=after).contains(&beat_at), "{stored:?}");
    assert_eq!(db.live("session_deadline_queue", &s).await, [Message { name: "miss".into(), at: deadline }]);

    case("a session's transition into turn arms its miss fifteen minutes from the entry (ttlByState)");
    let s = session().await;
    let moved = value(as_device.session.status.transition(s.id, of(json!({"to": "turn"})), None).await.unwrap());
    assert_eq!(moved, json!({"from": "idle", "to": "turn"}));
    let stored = db.session_row(&s).await;
    assert_eq!(stored.deadline, Some(stored.entered_at + 15 * MINUTE), "{stored:?}");
    assert_eq!(db.live("session_deadline_queue", &s).await, [Message { name: "miss".into(), at: stored.entered_at + 15 * MINUTE }]);

    case("a due miss the worker runs moves the session to missing and expires the leases its device holds on jobs (releaseLeases)");
    // A device of its own, since another session of the device that is
    // present stands for it, and a miss then expires none of its leases.
    let lone = db.subject().await;
    let as_lone = sdk(base_url, &lone, "");
    let s = db.create("Session", json!({"device": lone.wire, "agent": "planner"})).await;
    let j = db.job("held by the device").await;
    as_lone.job.lease.acquire(j.id, of(json!({})), None).await.unwrap();
    as_lone.session.presence.beat(s.id, None).await.unwrap();
    db.make_due("session_deadline_queue", &s, "miss").await;
    wait_until("the worker missed the session", || async { db.session_row(&s).await.status == "missing" }).await;
    let stored = db.session_row(&s).await;
    assert_eq!(stored.missed, Some(true), "{stored:?}");
    assert_eq!(stored.released, Some(json!({"Job": [j.wire]})), "{stored:?}");
    let job = db.job_row(&j).await;
    assert_eq!((job.holder, job.token), (None, Some(2)), "the device's lease expired");

    case("a beat recovers a missed session: missed clears and onBeat moves it back to idle");
    as_lone.session.presence.beat(s.id, None).await.unwrap();
    let stored = db.session_row(&s).await;
    assert_eq!((stored.missed, stored.status.as_str()), (Some(false), "idle"), "{stored:?}");

    case("an update that changes a session's device is 409 principal_fixed");
    let s = session().await;
    let held = db.session_row(&s).await.version;
    let refusal_of = refused(db.update("Session", &s, json!({"device": other.wire}), &device, json!({})).await);
    assert_eq!((refusal_of.status, refusal_of.code.as_str()), (409, "vetoed"));
    assert_eq!(details(&refusal_of)["vetoCode"], json!("principal_fixed"));
    assert_eq!(db.session_row(&s).await.version, held);
}

async fn budget_cases(db: &Database, base_url: &str) {
    let (agent, observer) = (db.subject().await, db.subject().await);
    let as_agent = sdk(base_url, &agent, "");
    let as_observer = sdk(base_url, &observer, "accounts.observe");
    let account = || async move {
        let w = db.create("Workspace", json!({"name": "Research"})).await;
        let a = db.create("Account", json!({"name": "Planner", "workspaceId": w.wire})).await;
        (w, a)
    };

    case("recordUsage charges the account's hourly meter and its workspace's through the to-one relation");
    let (w, a) = account().await;
    let before = now_ms() / HOUR * HOUR;
    let used = value(as_agent.account.budget.record_usage(a.id, of(json!({"meter": "questions", "amount": 1})), None).await.unwrap());
    let after = now_ms() / HOUR * HOUR;
    assert_eq!(used, json!({"meter": "questions", "used": 1, "released": 0, "overruns": [], "directed": false}));
    // The current UTC hour, the one the call ran in should it cross one.
    let hour = db.meter("account", &a, "questions").await.and_then(|(_, start)| start).expect("period_start");
    assert!(hour == before || hour == after, "the period starts at {hour}, not the hour of the call");
    assert_eq!(db.meter("account", &a, "questions").await, Some((1, Some(hour))));
    assert_eq!(db.meter("workspace", &w, "questions").await, Some((1, Some(hour))));

    case("a reservation past the account's limit is 409 over_limit, and leaves the rows as they were");
    let held = db.account_row(&a).await.0;
    let (status, code, vetoed) = veto(as_agent.account.budget.reserve(a.id, of(json!({"meter": "questions", "amount": 40})), None).await.unwrap_err());
    assert_eq!((status, code), (409, json!("over_limit")));
    assert_eq!(vetoed, json!({"meter": "questions", "amount": 40, "remaining": 39, "limit": 40, "scope": {"schema": "Account", "id": a.wire}}));
    assert_eq!(db.account_row(&a).await.0, held);
    assert_eq!(db.meter("account", &a, "questions").await, Some((1, Some(hour))));
    assert_eq!(db.meter("workspace", &w, "questions").await, Some((1, Some(hour))));

    case("an hourly meter starts again at the next hour: its next write counts from zero");
    db.client
        .execute("UPDATE account_budget_meters SET period_start = period_start - interval '1 hour' WHERE account_id::text = $1 AND meter = 'questions'", &[&a.text])
        .await
        .unwrap();
    let before = now_ms() / HOUR * HOUR;
    let used = value(as_agent.account.budget.record_usage(a.id, of(json!({"meter": "questions", "amount": 1})), None).await.unwrap());
    let after = now_ms() / HOUR * HOUR;
    assert_eq!(used["used"], json!(1), "{used}");
    let restarted = db.meter("account", &a, "questions").await;
    assert!(restarted == Some((1, Some(before))) || restarted == Some((1, Some(after))), "{restarted:?}");

    case("a rolling meter counts the last day's buckets: usage that left the window no longer counts");
    let (_, a) = account().await;
    as_agent.account.budget.record_usage(a.id, of(json!({"meter": "tokens", "amount": 900000})), None).await.unwrap();
    let buckets = db.buckets(&a, "tokens").await;
    assert_eq!(buckets.len(), 1, "{buckets:?}");
    assert_eq!((buckets[0].0 % (24 * MINUTE), buckets[0].1), (0, 900000), "{buckets:?}");
    let reserve = json!({"meter": "tokens", "amount": 200000});
    let (status, code, vetoed) = veto(as_agent.account.budget.reserve(a.id, of(reserve.clone()), None).await.unwrap_err());
    assert_eq!((status, code, &vetoed["remaining"]), (409, json!("over_limit"), &json!(100000)), "{vetoed}");
    db.client
        .execute("UPDATE account_budget_buckets SET starts_at = starts_at - interval '2 days' WHERE account_id::text = $1", &[&a.text])
        .await
        .unwrap();
    let reserved = value(as_agent.account.budget.reserve(a.id, of(reserve), None).await.unwrap());
    assert_eq!(reserved, json!({"reserved": {"tokens": 200000}}));

    case("observe without observePermission is 403 forbidden, and with it the provider's figure replaces what the meter counts until resetsAt");
    let (_, a) = account().await;
    let resets_at = now_ms() + HOUR;
    let observed = json!({"meter": "tokens", "used": 990000, "limit": 1000000, "resetsAt": resets_at});
    let err = as_agent.account.budget.observe(a.id, of(observed.clone()), None).await.unwrap_err();
    let (status, code, _, _) = refusal(err);
    assert_eq!((status, code.as_str()), (403, "forbidden"));
    let answer = value(as_observer.account.budget.observe(a.id, of(observed), None).await.unwrap());
    assert_eq!(answer, json!({"meter": "tokens", "used": 990000, "limit": 1000000, "resetsAt": resets_at}));
    let (status, code, vetoed) = veto(as_agent.account.budget.reserve(a.id, of(json!({"meter": "tokens", "amount": 20000})), None).await.unwrap_err());
    assert_eq!((status, code, &vetoed["remaining"]), (409, json!("over_limit"), &json!(10000)), "{vetoed}");
    let used = value(as_agent.account.budget.record_usage(a.id, of(json!({"meter": "tokens", "amount": 5000})), None).await.unwrap());
    assert_eq!(used["used"], json!(995000), "{used}");

    case("usage that takes the account over its limit moves it out (escalate), and counts all the same");
    let (_, a) = account().await;
    let used = value(as_agent.account.budget.record_usage(a.id, of(json!({"meter": "questions", "amount": 41})), None).await.unwrap());
    assert_eq!(used["overruns"], json!([{"schema": "Account", "id": a.wire, "used": 41, "limit": 40, "escalated": true}]), "{used}");
    assert_eq!(db.account_row(&a).await.1, "out");
}
`
