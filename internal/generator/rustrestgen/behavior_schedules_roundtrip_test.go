package rustrestgen

// schedulesRoundtripHead is the head of tests/behavior_schedules.rs: its
// uses, its authenticator, its Workers, whose event handler is the project's
// code the cases hold to, its schema of fixture-schedules-db's tables, and
// what the cases read and write with SQL and through the adapter. The only
// real waits are the worker's: a deadline or an event a case needs due is
// made due with SQL, an operator's escape hatch, and the worker's run
// polled for every 100 ms for at most ten seconds; a deadline runs at the
// later of the clock and the time it was armed at, so a wake-up made due
// fires at its nextAt.
const schedulesRoundtripHead = `//! Schedules on a table end to end (D104): fixture-schedules-api's router
//! over a schema of its own in the Postgres at
//! SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, which holds fixture-schedules-db's
//! tables, its problems' behavior operations called through the generated
//! Rust SDK, their creates run through the adapter, and its behavior worker
//! ProblemWakeUps' three queues and handlers run in process, the event
//! queue's handing each run event to the Workers of the test's own, by
//! subjects that are UUIDs in their base62 form.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use API_CRATE::behaviors::{
    problem_wake_ups_changes, problem_wake_ups_deadlines, problem_wake_ups_events, run_problem_wake_ups_change,
    run_problem_wake_ups_deadline, run_problem_wake_ups_event, EventRun, RULES_LAYOUT,
};
use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::queue::QueueError;
use API_CRATE::runtime::worker::{run_worker, Options};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, Shutdown};
use API_CRATE::{build_router, types, BoxError, Handlers, Implementations, Workers};
use SDK_CRATE::{ClientConfig, FixtureSchedulesApiSdk, SDKError};
use async_trait::async_trait;
use axum::http::request::Parts;
use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::{json, Map, Value};
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

/// An event the handler got: its row's key in its wire form, its time, and
/// its payload.
#[derive(Clone, Debug)]
struct Heard {
    key: String,
    at: i64,
    payload: Value,
}

impl Heard {
    /// The event's schedule, run and attempt.
    fn run(&self) -> (String, i64, i64) {
        (
            self.payload["name"].as_str().unwrap_or_default().to_string(),
            self.payload["run"].as_i64().unwrap_or_default(),
            self.payload["attempt"].as_i64().unwrap_or_default(),
        )
    }
}

/// The project's handler of Problem's run events, as the test writes it:
/// it records each event it gets; while the test has it fail, it fails
/// each with the error the agent was not told; otherwise it reports the
/// run through the event's transaction as the worker, the outcome the test
/// set for the schedule's name, else told, and a problem gone completes
/// the event.
#[derive(Default)]
struct Agent {
    heard: Mutex<Vec<Heard>>,
    failing: Mutex<usize>,
    outcomes: Mutex<HashMap<String, String>>,
}

impl Agent {
    /// The events of a problem it got, in the order it got them.
    fn heard_of(&self, problem: &Row) -> Vec<Heard> {
        self.heard.lock().unwrap().iter().filter(|heard| heard.key == problem.wire).cloned().collect()
    }

    /// Sets the outcome it reports of a schedule's runs.
    fn report(&self, name: &str, outcome: &str) {
        self.outcomes.lock().unwrap().insert(name.to_string(), outcome.to_string());
    }

    /// Has it fail the next n events.
    fn fail(&self, n: usize) {
        *self.failing.lock().unwrap() = n;
    }

    /// Whether it fails this event, counting it off those it was told to.
    fn fails(&self) -> bool {
        let mut failing = self.failing.lock().unwrap();
        if *failing == 0 {
            return false;
        }
        *failing -= 1;
        true
    }
}

#[async_trait]
impl Workers for Agent {
    async fn problem_wake_ups_schedules_run(&self, event: types::ProblemSchedulesRunEvent, run: &EventRun<'_>) -> Result<(), BoxError> {
        let event = serde_json::to_value(&event)?;
        let key = event["key"].as_str().unwrap_or_default().to_string();
        let payload = event["payload"].clone();
        self.heard.lock().unwrap().push(Heard { key: key.clone(), at: event["at"].as_i64().unwrap_or_default(), payload: payload.clone() });
        if self.fails() {
            return Err("the agent was not told".into());
        }
        let name = payload["name"].as_str().unwrap_or_default().to_string();
        let outcome = self.outcomes.lock().unwrap().get(&name).cloned().unwrap_or_else(|| "told".to_string());
        let params = json!({"name": name, "run": payload["run"], "attempt": payload["attempt"], "outcome": outcome});
        match run.operation::<_, _, Value>("Problem", &key, "Schedules", "reportRun", &params).await {
            Ok(_) => Ok(()),
            Err(err) if err.is_not_found() => Ok(()),
            Err(err) => Err(err.into()),
        }
    }
}

/// A schema of its own holding fixture-schedules-db's tables, a pool whose
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
    let schema = format!("behavior_schedules_rust_{nanos}");
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
/// principal's subject, and the permissions it holds, comma separated as
/// its token lists them.
struct Subject {
    wire: String,
    permissions: &'static str,
}

/// A row: its key as the SDK takes it, its wire form, as the core names
/// it, and its column's text.
struct Row {
    id: types::IdentityUUID,
    wire: String,
    text: String,
}

/// A live message of the deadline queue: its use, its deadline's name and
/// the time it was armed at, in epoch milliseconds.
#[derive(Debug, PartialEq)]
struct Armed {
    use_id: String,
    name: String,
    at: i64,
}

/// A message of the event queue as a failure leaves it: its id, state and
/// attempts, how many seconds from the database's now it falls due, and
/// its last error.
#[derive(Debug)]
struct Queued {
    id: String,
    state: String,
    attempts: i64,
    due_in: f64,
    last_error: Option<String>,
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

    /// Creates a problem through the core, as the repository's create does,
    /// with a key of its own: its Workflow enters open.
    async fn problem(&self) -> Row {
        let (id, wire, text) = self.uuid().await;
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let options = CreateOptions { id: Some(wire.clone()), ..CreateOptions::default() };
        let own = json!({"title": "Flaky test"}).as_object().cloned().unwrap();
        let key = postgres::run_create(&host, &*tx, "Problem", own, Caller::new("tester", Vec::<String>::new()), &options).await.unwrap();
        tx.commit().await.unwrap();
        assert_eq!(key, wire);
        Row { id, wire, text }
    }

    /// The problem's _version.
    async fn version(&self, problem: &Row) -> i64 {
        self.client.query_one("SELECT _version::bigint FROM problem WHERE id::text = $1", &[&problem.text]).await.unwrap().get(0)
    }

    /// The live messages of the problem's deadline queue, by name.
    async fn armed(&self, problem: &Row) -> Vec<Armed> {
        self.client
            .query(
                "SELECT use_id, name, at FROM problem_deadline_queue WHERE row_key::text = $1 AND state IN ('ready', 'claimed') ORDER BY name",
                &[&problem.text],
            )
            .await
            .unwrap()
            .iter()
            .map(|row| Armed { use_id: row.get(0), name: row.get(1), at: row.get(2) })
            .collect()
    }

    /// The due times of the problem's live deadline messages, by name, in
    /// epoch milliseconds.
    async fn due(&self, problem: &Row) -> Vec<(String, i64)> {
        self.client
            .query(
                "SELECT name, (extract(epoch FROM due_at) * 1000)::bigint FROM problem_deadline_queue WHERE row_key::text = $1 AND state IN ('ready', 'claimed') ORDER BY name",
                &[&problem.text],
            )
            .await
            .unwrap()
            .iter()
            .map(|row| (row.get(0), row.get(1)))
            .collect()
    }

    /// Makes a deadline of the problem due now, as an operator's repair
    /// would: the worker claims it, and the core runs it at the time it was
    /// armed to fall due at.
    async fn make_due(&self, problem: &Row, name: &str) {
        let changed = self
            .client
            .execute(
                "UPDATE problem_deadline_queue SET due_at = now() WHERE row_key::text = $1 AND name = $2 AND state IN ('ready', 'claimed')",
                &[&problem.text, &name],
            )
            .await
            .unwrap();
        assert_eq!(changed, 1, "one live message of {name} on problem_deadline_queue");
    }

    /// The problem's messages of the event queue.
    async fn events(&self, problem: &Row) -> Vec<Queued> {
        self.client
            .query(
                "SELECT id::text, state, attempts::bigint, extract(epoch FROM due_at - now())::float8, last_error FROM problem_event_queue WHERE row_key::text = $1 ORDER BY created_at",
                &[&problem.text],
            )
            .await
            .unwrap()
            .iter()
            .map(|row| Queued { id: row.get(0), state: row.get(1), attempts: row.get(2), due_in: row.get(3), last_error: row.get(4) })
            .collect()
    }

    /// Runs a statement of the test's own on the problem's rows, its
    /// parameters text, and answers how many rows it changed.
    async fn exec(&self, text: &str, params: &[&str]) -> u64 {
        let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> = params.iter().map(|p| p as &(dyn tokio_postgres::types::ToSql + Sync)).collect();
        self.client.execute(text, &params).await.unwrap()
    }
}

/// ProblemWakeUps' three queues, each in a claim loop of its own, as its
/// main runs them, until shutdown: the event queue's handler hands each
/// event to agent.
fn start_worker(pool: &Pool, agent: &Arc<Agent>, shutdown: &Shutdown) -> Vec<tokio::task::JoinHandle<Result<(), QueueError>>> {
    let options = Options { concurrency: 2, idle: Duration::from_millis(100), ..Options::default() };
    let deadlines = problem_wake_ups_deadlines(pool).unwrap();
    let on_deadlines = pool.clone();
    let changes = problem_wake_ups_changes(pool).unwrap();
    let on_changes = pool.clone();
    let events = problem_wake_ups_events(pool).unwrap();
    let on_events = pool.clone();
    let agent = Arc::clone(agent);
    vec![
        tokio::spawn(run_worker(
            deadlines,
            move |claimed| {
                let pool = on_deadlines.clone();
                async move { run_problem_wake_ups_deadline(&pool, claimed).await }
            },
            options,
            shutdown.clone(),
        )),
        tokio::spawn(run_worker(
            changes,
            move |claimed| {
                let pool = on_changes.clone();
                async move { run_problem_wake_ups_change(&pool, claimed).await }
            },
            options,
            shutdown.clone(),
        )),
        tokio::spawn(run_worker(
            events,
            move |claimed| {
                let pool = on_events.clone();
                let agent = Arc::clone(&agent);
                async move { run_problem_wake_ups_event(&pool, &*agent, claimed).await }
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

/// The SDK as a subject holding its permissions.
fn sdk(base_url: &str, subject: &Subject) -> FixtureSchedulesApiSdk {
    let token = format!("{}:{}", subject.wire, subject.permissions);
    FixtureSchedulesApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), Some(token), None)).unwrap()
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

/// An object's members without those named.
fn without(value: &Value, names: &[&str]) -> Value {
    let mut members: Map<String, Value> = value.as_object().cloned().unwrap_or_default();
    for name in names {
        members.remove(*name);
    }
    Value::Object(members)
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

/// A number of a view, by its member.
fn ms(view: &Value, member: &str) -> i64 {
    view[member].as_i64().unwrap_or_else(|| panic!("{view} has no {member}"))
}

fn now_ms() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as i64
}

fn case(name: &str) {
    println!("case: {name}");
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn schedules_run_through_the_core_the_worker_and_the_projects_handler() {
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

// schedulesRoundtripCases are the cases of tests/behavior_schedules.rs, in
// schedulesCases' order and words, most making a problem of their own.
const schedulesRoundtripCases = `
async fn cases(db: Arc<Database>) {
    let base_url = serve(db.pool.clone()).await;
    let agent = Arc::new(Agent::default());
    let shutdown = Shutdown::new();
    let workers = start_worker(&db.pool, &agent, &shutdown);
    let caller = db.subject("problems.schedule").await;
    let guest = db.subject("").await;
    let (as_agent, as_guest) = (sdk(&base_url, &caller), sdk(&base_url, &guest));
    let db = &*db;

    case("setSchedule arms a wake-up after a delay, at an interval and on a cron line in the config's time zone, each a deadline message due at its nextAt");
    let p = db.problem().await;
    let before = now_ms();
    let nudge = value(as_agent.problem.schedules.set_schedule(p.id, of(json!({"name": "nudge", "after": 300000})), None).await.unwrap());
    let hourly = value(as_agent.problem.schedules.set_schedule(p.id, of(json!({"name": "hourly", "every": 3600000})), None).await.unwrap());
    let morning = value(as_agent.problem.schedules.set_schedule(p.id, of(json!({"name": "morning", "cron": "0 9 * * MON-FRI"})), None).await.unwrap());
    let after = now_ms();
    for (view, kind, spec) in [(&nudge, "after", "300000"), (&hourly, "every", "3600000"), (&morning, "cron", "0 9 * * MON-FRI")] {
        assert_eq!(
            without(view, &["setAt", "nextAt"]),
            if kind == "cron" {
                json!({"name": "morning", "kind": kind, "spec": spec, "timeZone": "America/New_York", "state": "active", "runs": 0})
            } else {
                json!({"name": view["name"], "kind": kind, "spec": spec, "state": "active", "runs": 0})
            },
            "{view}"
        );
        assert!((before..=after).contains(&ms(view, "setAt")), "{view}");
    }
    assert_eq!(ms(&nudge, "nextAt"), ms(&nudge, "setAt") + 300_000);
    assert_eq!(ms(&hourly, "nextAt"), ms(&hourly, "setAt") + 3_600_000);
    let morning_at = ms(&morning, "nextAt");
    assert!(morning_at > ms(&morning, "setAt") && morning_at <= ms(&morning, "setAt") + 4 * 86_400_000, "{morning}");
    // 09:00 in New York is 13:00 or 14:00 UTC, on a weekday.
    assert!(matches!(morning_at.rem_euclid(86_400_000), 46_800_000 | 50_400_000), "{morning}");
    assert_eq!(
        db.armed(&p).await,
        [
            Armed { use_id: "Schedules".into(), name: "hourly".into(), at: ms(&hourly, "nextAt") },
            Armed { use_id: "Schedules".into(), name: "morning".into(), at: morning_at },
            Armed { use_id: "Schedules".into(), name: "nudge".into(), at: ms(&nudge, "nextAt") },
        ]
    );
    assert_eq!(
        db.due(&p).await,
        [("hourly".to_string(), ms(&hourly, "nextAt")), ("morning".to_string(), morning_at), ("nudge".to_string(), ms(&nudge, "nextAt"))]
    );

    case("a repeated setSchedule of a live name with the same spec answers unchanged and leaves the row's _version where it was");
    let held = db.version(&p).await;
    let again = value(as_agent.problem.schedules.set_schedule(p.id, of(json!({"name": "hourly", "every": 3600000})), None).await.unwrap());
    let mut unchanged = hourly.clone();
    unchanged["unchanged"] = json!(true);
    assert_eq!(again, unchanged);
    assert_eq!(db.version(&p).await, held, "a repeat writes nothing");

    case("setSchedule without problems.schedule is 403 forbidden, a delay under minMs is 400 invalid_argument, and an eleventh live wake-up is 409 schedule_limit, each leaving the row's _version where it was");
    let held = db.version(&p).await;
    let (status, code, message, _) = refusal(as_guest.problem.schedules.set_schedule(p.id, of(json!({"name": "x", "after": 300000})), None).await.unwrap_err());
    assert_eq!((status, code.as_str()), (403, "forbidden"), "{message}");
    assert!(message.contains(&format!("{} may not set a schedule of Problem {}: it needs permission problems.schedule", guest.wire, p.wire)), "{message}");
    assert_eq!(db.version(&p).await, held);
    let (status, code, message, _) = refusal(as_agent.problem.schedules.set_schedule(p.id, of(json!({"name": "x", "after": 1000})), None).await.unwrap_err());
    assert_eq!((status, code.as_str()), (400, "invalid_argument"), "{message}");
    assert!(message.contains("/after: 1000 is outside minMs..maxMs (60000..2592000000): a delay lies between them"), "{message}");
    assert_eq!(db.version(&p).await, held);
    let mut live = vec!["hourly".to_string(), "morning".to_string(), "nudge".to_string()];
    for n in 1..=7 {
        let name = format!("w{n}");
        as_agent.problem.schedules.set_schedule(p.id, of(json!({"name": name, "after": 600000})), None).await.unwrap();
        live.push(name);
    }
    live.sort();
    let held = db.version(&p).await;
    let (status, code, vetoed) = veto(as_agent.problem.schedules.set_schedule(p.id, of(json!({"name": "w8", "after": 600000})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("schedule_limit"), json!({"max": 10, "live": live})));
    assert_eq!(db.version(&p).await, held);

    case("a due wake-up fires on the worker as run 1 and hands the project's handler one run event with its name, run, attempt, kind, spec and times, which reports told");
    let p2 = db.problem().await;
    let set = value(as_agent.problem.schedules.set_schedule(p2.id, of(json!({"name": "nudge", "after": 300000})), None).await.unwrap());
    let due = ms(&set, "nextAt");
    db.make_due(&p2, "nudge").await;
    let told = json!({"items": [{"name": "nudge", "kind": "after", "spec": "300000", "state": "finished", "setAt": ms(&set, "setAt"), "runs": 1, "attempt": 1, "lastAt": due, "lastOutcome": "told", "finishedAt": due, "endedBy": "run"}]});
    wait_until("the handler reported nudge's run told", || async {
        value(as_agent.problem.schedules.list_schedules(p2.id, of(json!({"name": "nudge"})), None).await.unwrap()) == told
    })
    .await;
    let heard = agent.heard_of(&p2);
    assert_eq!(heard.len(), 1, "{heard:?}");
    assert_eq!(heard[0].at, due);
    assert_eq!(heard[0].payload, json!({"name": "nudge", "run": 1, "attempt": 1, "kind": "after", "spec": "300000", "setAt": ms(&set, "setAt"), "dueAt": due, "firedAt": due}));
    assert!(db.armed(&p2).await.is_empty(), "an after arms none once it ran");

    case("the handler runs once per event: a completed event's message is gone and is not handed to it again");
    wait_until("nudge's event completed", || async { db.events(&p2).await.is_empty() }).await;
    tokio::time::sleep(Duration::from_millis(1500)).await;
    assert_eq!(agent.heard_of(&p2).len(), 1, "the event was handed to the handler once");
    assert!(db.events(&p2).await.is_empty());

    case("a handler that fails fails its message, which is due again after the queue's backoff, and the next claim hands it to the handler again");
    let p3 = db.problem().await;
    agent.fail(1);
    let set = value(as_agent.problem.schedules.set_schedule(p3.id, of(json!({"name": "ping", "after": 300000})), None).await.unwrap());
    db.make_due(&p3, "ping").await;
    wait_until("ping's event failed once", || async {
        db.events(&p3).await.first().is_some_and(|event| event.state == "ready" && event.attempts == 1)
    })
    .await;
    let failed = db.events(&p3).await.remove(0);
    assert!(failed.due_in > 20.0, "due again after the backoff: {failed:?}");
    assert!(failed.last_error.as_deref().is_some_and(|error| error.contains("the agent was not told")), "{failed:?}");
    assert_eq!(agent.heard_of(&p3).len(), 1);
    let listed = value(as_agent.problem.schedules.list_schedules(p3.id, of(json!({"name": "ping"})), None).await.unwrap());
    assert!(listed["items"][0].get("lastOutcome").is_none(), "a failed handler reported nothing: {listed}");
    assert_eq!(db.exec("UPDATE problem_event_queue SET due_at = now() WHERE id::text = $1", &[&failed.id]).await, 1);
    wait_until("ping's event ran again and completed", || async { db.events(&p3).await.is_empty() }).await;
    let heard = agent.heard_of(&p3);
    assert_eq!(heard.len(), 2, "{heard:?}");
    assert_eq!(heard[0].payload, heard[1].payload, "the same event, run 1 attempt 1, twice");
    assert_eq!(heard[1].run(), ("ping".to_string(), 1, 1));
    let listed = value(as_agent.problem.schedules.list_schedules(p3.id, of(json!({"name": "ping"})), None).await.unwrap());
    assert_eq!(listed["items"][0]["lastOutcome"], json!("told"), "{listed}");
    assert_eq!(ms(&listed["items"][0], "lastAt"), ms(&set, "nextAt"));

    case("an interval overdue by many runs fires once, and its next run is the first of its grid after the fire");
    let p4 = db.problem().await;
    let set = value(as_agent.problem.schedules.set_schedule(p4.id, of(json!({"name": "tick", "every": 60000})), None).await.unwrap());
    let set_at = ms(&set, "setAt") - 600_000;
    assert_eq!(
        db.exec("UPDATE problem_schedules_schedules SET set_at = set_at - interval '10 minutes', next_at = next_at - interval '10 minutes' WHERE problem_id::text = $1 AND name = $2", &[&p4.text, "tick"]).await,
        1
    );
    assert_eq!(
        db.exec("UPDATE problem_deadline_queue SET at = at - 600000, due_at = now() WHERE row_key::text = $1 AND name = $2 AND state = 'ready'", &[&p4.text, "tick"]).await,
        1
    );
    wait_until("tick fired", || async { !agent.heard_of(&p4).is_empty() }).await;
    tokio::time::sleep(Duration::from_millis(1500)).await;
    let heard = agent.heard_of(&p4);
    assert_eq!(heard.len(), 1, "an overdue interval fires once: {heard:?}");
    let (fired, next) = (ms(&heard[0].payload, "firedAt"), ms(&heard[0].payload, "nextAt"));
    assert_eq!(heard[0].run(), ("tick".to_string(), 1, 1));
    assert_eq!(ms(&heard[0].payload, "dueAt"), set_at + 60_000);
    assert!(next > fired && next - fired <= 60_000, "{:?}", heard[0]);
    assert_eq!((next - set_at) % 60_000, 0, "the next run lies on the grid of setAt: {:?}", heard[0]);
    assert_eq!(db.armed(&p4).await, [Armed { use_id: "Schedules".into(), name: "tick".into(), at: next }]);
    as_agent.problem.schedules.cancel_schedule(p4.id, of(json!({"name": "tick"})), None).await.unwrap();

    case("a missed run is tried again five minutes on, as attempt 2 of the same run");
    let p5 = db.problem().await;
    agent.report("checkin", "missed");
    let set = value(as_agent.problem.schedules.set_schedule(p5.id, of(json!({"name": "checkin", "every": 3600000})), None).await.unwrap());
    let first = ms(&set, "nextAt");
    db.make_due(&p5, "checkin").await;
    wait_until("checkin's run was reported missed", || async {
        let listed = value(as_agent.problem.schedules.list_schedules(p5.id, of(json!({"name": "checkin"})), None).await.unwrap());
        listed["items"][0]["lastOutcome"] == json!("missed") && listed["items"][0].get("retryAt").is_some()
    })
    .await;
    let listed = value(as_agent.problem.schedules.list_schedules(p5.id, of(json!({"name": "checkin"})), None).await.unwrap());
    let retry_at = ms(&listed["items"][0], "retryAt");
    assert!(retry_at - 300_000 <= now_ms() && retry_at - 300_000 >= set["setAt"].as_i64().unwrap(), "{listed}");
    assert_eq!(db.armed(&p5).await, [Armed { use_id: "Schedules".into(), name: "checkin".into(), at: retry_at }]);
    agent.report("checkin", "told");
    db.make_due(&p5, "checkin").await;
    wait_until("checkin's retry was reported told", || async { agent.heard_of(&p5).len() == 2 && db.events(&p5).await.is_empty() }).await;
    let heard = agent.heard_of(&p5);
    assert_eq!(heard[0].run(), ("checkin".to_string(), 1, 1));
    assert_eq!(ms(&heard[0].payload, "dueAt"), first);
    assert_eq!(heard[1].run(), ("checkin".to_string(), 1, 2));
    assert_eq!(ms(&heard[1].payload, "dueAt"), retry_at);
    assert_eq!(ms(&heard[1].payload, "nextAt"), first + 3_600_000);
    wait_until("the retry's run was reported told", || async {
        let listed = value(as_agent.problem.schedules.list_schedules(p5.id, of(json!({"name": "checkin"})), None).await.unwrap());
        listed["items"][0]["lastOutcome"] == json!("told") && listed["items"][0]["attempt"] == json!(2)
    })
    .await;
    assert_eq!(db.armed(&p5).await, [Armed { use_id: "Schedules".into(), name: "checkin".into(), at: first + 3_600_000 }]);

    case("a report of an older run answers unchanged, and one of a run not had is 409 no_run");
    db.make_due(&p5, "checkin").await;
    wait_until("checkin's second run was reported told", || async {
        let listed = value(as_agent.problem.schedules.list_schedules(p5.id, of(json!({"name": "checkin"})), None).await.unwrap());
        listed["items"][0]["runs"] == json!(2) && listed["items"][0]["lastOutcome"] == json!("told")
    })
    .await;
    let held = db.version(&p5).await;
    let older = value(as_agent.problem.schedules.report_run(p5.id, of(json!({"name": "checkin", "run": 1, "outcome": "held"})), None).await.unwrap());
    assert_eq!(older, json!({"name": "checkin", "run": 1, "outcome": "held", "unchanged": true}));
    assert_eq!(db.version(&p5).await, held, "a report of an older run writes nothing");
    let (status, code, vetoed) = veto(as_agent.problem.schedules.report_run(p5.id, of(json!({"name": "checkin", "run": 3, "outcome": "told"})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("no_run"), json!({"name": "checkin", "run": 3, "runs": 2})));
    assert_eq!(db.version(&p5).await, held);

    case("pauseSchedule keeps nextAt and deletes its deadline message, and resumeSchedule past nextAt fires at once, once");
    let p6 = db.problem().await;
    let set = value(as_agent.problem.schedules.set_schedule(p6.id, of(json!({"name": "hourly", "every": 3600000})), None).await.unwrap());
    let paused = value(as_agent.problem.schedules.pause_schedule(p6.id, of(json!({"name": "hourly"})), None).await.unwrap());
    assert_eq!((&paused["state"], ms(&paused, "nextAt")), (&json!("paused"), ms(&set, "nextAt")), "{paused}");
    assert!(db.armed(&p6).await.is_empty(), "a paused schedule arms no deadline");
    let past = now_ms() - 3_600_000;
    let moved = db
        .client
        .execute("UPDATE problem_schedules_schedules SET next_at = to_timestamp($2::bigint / 1000.0) WHERE problem_id::text = $1 AND name = 'hourly'", &[&p6.text, &past])
        .await
        .unwrap();
    assert_eq!(moved, 1);
    let resumed = value(as_agent.problem.schedules.resume_schedule(p6.id, of(json!({"name": "hourly"})), None).await.unwrap());
    assert_eq!((&resumed["state"], ms(&resumed, "nextAt")), (&json!("active"), past), "{resumed}");
    wait_until("the resumed schedule fired", || async { !agent.heard_of(&p6).is_empty() }).await;
    tokio::time::sleep(Duration::from_millis(1500)).await;
    let heard = agent.heard_of(&p6);
    assert_eq!(heard.len(), 1, "a resumed schedule past its nextAt fires once: {heard:?}");
    assert_eq!(heard[0].run(), ("hourly".to_string(), 1, 1));
    assert_eq!(ms(&heard[0].payload, "dueAt"), past);
    assert_eq!(ms(&heard[0].payload, "nextAt"), ms(&set, "setAt") + 3_600_000);
    assert_eq!(db.armed(&p6).await, [Armed { use_id: "Schedules".into(), name: "hourly".into(), at: ms(&set, "setAt") + 3_600_000 }]);

    case("cancelSchedule finishes a wake-up and frees its place under max");
    let cancelled = value(as_agent.problem.schedules.cancel_schedule(p.id, of(json!({"name": "w1"})), None).await.unwrap());
    assert_eq!((&cancelled["state"], &cancelled["endedBy"]), (&json!("finished"), &json!("cancel")), "{cancelled}");
    assert!(cancelled.get("finishedAt").is_some() && cancelled.get("nextAt").is_none(), "{cancelled}");
    assert!(db.armed(&p).await.iter().all(|armed| armed.name != "w1"), "a cancel deletes its deadline message");
    let set = value(as_agent.problem.schedules.set_schedule(p.id, of(json!({"name": "w8", "after": 600000})), None).await.unwrap());
    assert_eq!(set["state"], json!("active"), "{set}");

    case("a problem whose status enters done finishes its wake-ups, and a new one is 409 schedules_ended");
    let p7 = db.problem().await;
    as_agent.problem.schedules.set_schedule(p7.id, of(json!({"name": "hourly", "every": 3600000})), None).await.unwrap();
    as_agent.problem.schedules.set_schedule(p7.id, of(json!({"name": "nudge", "after": 600000})), None).await.unwrap();
    as_agent.problem.status.transition(p7.id, of(json!({"to": "working"})), None).await.unwrap();
    let before = now_ms();
    as_agent.problem.status.transition(p7.id, of(json!({"to": "done"})), None).await.unwrap();
    let after = now_ms();
    let listed = value(as_agent.problem.schedules.list_schedules(p7.id, of(json!({})), None).await.unwrap());
    let items = listed["items"].as_array().expect("items").clone();
    assert_eq!(items.len(), 2, "{listed}");
    for item in &items {
        assert_eq!((&item["state"], &item["endedBy"]), (&json!("finished"), &json!("endOn")), "{listed}");
        assert!((before..=after).contains(&ms(item, "finishedAt")), "{listed}");
    }
    assert!(db.armed(&p7).await.is_empty(), "endOn clears every deadline");
    let held = db.version(&p7).await;
    let (status, code, vetoed) = veto(as_agent.problem.schedules.set_schedule(p7.id, of(json!({"name": "again", "after": 300000})), None).await.unwrap_err());
    assert_eq!((status, code, vetoed), (409, json!("schedules_ended"), json!({"status": "done"})));
    assert_eq!(db.version(&p7).await, held);

    case("a raw SQL write of a problem's status to dropped is heard by the worker and finishes its wake-ups");
    let p8 = db.problem().await;
    as_agent.problem.schedules.set_schedule(p8.id, of(json!({"name": "hourly", "every": 3600000})), None).await.unwrap();
    assert_eq!(db.exec("UPDATE problem SET status = 'dropped' WHERE id::text = $1", &[&p8.text]).await, 1);
    wait_until("the worker heard the write and finished hourly", || async {
        let listed = value(as_agent.problem.schedules.list_schedules(p8.id, of(json!({"name": "hourly"})), None).await.unwrap());
        listed["items"][0]["state"] == json!("finished")
    })
    .await;
    let listed = value(as_agent.problem.schedules.list_schedules(p8.id, of(json!({"name": "hourly"})), None).await.unwrap());
    assert_eq!(listed["items"][0]["endedBy"], json!("endOn"), "{listed}");
    assert!(db.armed(&p8).await.is_empty(), "the heard write clears the deadline");

    case("listSchedules reads a problem's wake-ups in name order");
    let p9 = db.problem().await;
    for params in [json!({"name": "zeta", "after": 600000}), json!({"name": "alpha", "every": 600000}), json!({"name": "mid", "cron": "0 12 * * *", "timeZone": "Europe/London"})] {
        as_agent.problem.schedules.set_schedule(p9.id, of(params), None).await.unwrap();
    }
    let listed = value(as_agent.problem.schedules.list_schedules(p9.id, of(json!({})), None).await.unwrap());
    let names: Vec<&str> = listed["items"].as_array().expect("items").iter().map(|item| item["name"].as_str().unwrap_or_default()).collect();
    assert_eq!(names, ["alpha", "mid", "zeta"], "{listed}");
    let one = value(as_agent.problem.schedules.list_schedules(p9.id, of(json!({"name": "mid"})), None).await.unwrap());
    let items = one["items"].as_array().expect("items").clone();
    assert_eq!(items.len(), 1, "{one}");
    assert_eq!((&items[0]["kind"], &items[0]["spec"], &items[0]["timeZone"]), (&json!("cron"), &json!("0 12 * * *"), &json!("Europe/London")));
    let none = value(as_agent.problem.schedules.list_schedules(p9.id, of(json!({"name": "ghost"})), None).await.unwrap());
    assert_eq!(none, json!({"items": []}));

    shutdown.cancel();
    for worker in workers {
        worker.await.unwrap().unwrap();
    }
}
`
