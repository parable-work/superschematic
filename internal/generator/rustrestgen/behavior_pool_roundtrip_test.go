package rustrestgen

// poolRoundtripHead is the head of tests/behavior_pool.rs: its uses, its
// authenticator, its Workers, whose event handler is the project's code the
// cases hold to, with a fake provider, its schema of fixture-pool-db's
// tables, and what the cases read and write with SQL and through the
// adapter. The only real waits are the worker's and the clock's: a recheck
// a case needs falls due at its outUntil, two seconds on, and the worker's
// run is polled for every 100 ms for at most ten seconds; a provider's
// figure a case needs lapsed lapses three seconds on.
const poolRoundtripHead = `//! Pool on a table end to end (D104): fixture-pool-api's router over a
//! schema of its own in the Postgres at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL,
//! which holds fixture-pool-db's tables, its accounts' behavior operations
//! called through the generated Rust SDK (pick a method of the table with no
//! key, a command without a row), their creates run through the adapter,
//! and its behavior worker AccountRechecks' two queues and handlers run in
//! process, the event queue's handing each recheck event to the Workers of
//! the test's own, which asks a fake provider and reports with reportRoom,
//! by subjects that are UUIDs in their base62 form.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use API_CRATE::behaviors::{
    account_rechecks_deadlines, account_rechecks_events, run_account_rechecks_deadline, run_account_rechecks_event, EventRun,
    RULES_LAYOUT,
};
use API_CRATE::runtime::postgres::{deadpool_postgres, tokio_postgres, Pool};
use API_CRATE::runtime::queue::QueueError;
use API_CRATE::runtime::worker::{run_worker, Options};
use API_CRATE::runtime::{bearer_token, ApiError, Authenticator, Principal, Shutdown};
use API_CRATE::{build_router, types, BoxError, Handlers, Implementations, Workers};
use SDK_CRATE::{ClientConfig, FixturePoolApiSdk, SDKError};
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

/// An event the handler got: its row's key in its wire form, and its
/// payload.
#[derive(Clone, Debug)]
struct Heard {
    key: String,
    payload: Value,
}

impl Heard {
    /// When the recheck was due, in epoch milliseconds.
    fn due_at(&self) -> i64 {
        self.payload["dueAt"].as_i64().unwrap_or_default()
    }
}

/// The project's handler of Account's recheck events, as the test writes
/// it, with a fake provider: it counts each delivery; a delivery of a row's
/// key and dueAt it reported already reports nothing, so it is idempotent;
/// otherwise it asks the provider for the account's room, the answer the
/// test set for it or room, reports it with reportRoom through the event's
/// transaction as the worker, and records the event and the report.
#[derive(Default)]
struct Provider {
    calls: Mutex<Vec<String>>,
    heard: Mutex<Vec<Heard>>,
    answers: Mutex<HashMap<String, Value>>,
    reports: Mutex<Vec<(String, Value)>>,
}

impl Provider {
    /// Sets what the provider answers of an account's room.
    fn answer(&self, account: &Row, answer: Value) {
        self.answers.lock().unwrap().insert(account.wire.clone(), answer);
    }

    /// How many times an account's events were handed to the handler.
    fn calls_of(&self, account: &Row) -> usize {
        self.calls.lock().unwrap().iter().filter(|key| **key == account.wire).count()
    }

    /// The events of an account it reported, in the order it got them.
    fn heard_of(&self, account: &Row) -> Vec<Heard> {
        self.heard.lock().unwrap().iter().filter(|heard| heard.key == account.wire).cloned().collect()
    }

    /// What reportRoom answered of each check it reported of an account.
    fn reports_of(&self, account: &Row) -> Vec<Value> {
        self.reports.lock().unwrap().iter().filter(|(key, _)| *key == account.wire).map(|(_, report)| report.clone()).collect()
    }
}

#[async_trait]
impl Workers for Provider {
    async fn account_rechecks_pool_recheck(&self, event: types::AccountPoolRecheckEvent, run: &EventRun<'_>) -> Result<(), BoxError> {
        let event = serde_json::to_value(&event)?;
        let key = event["key"].as_str().unwrap_or_default().to_string();
        let heard = Heard { key: key.clone(), payload: event["payload"].clone() };
        self.calls.lock().unwrap().push(key.clone());
        let reported = self.heard.lock().unwrap().iter().any(|known| known.key == key && known.due_at() == heard.due_at());
        if reported {
            return Ok(());
        }
        let answer = self.answers.lock().unwrap().get(&key).cloned().unwrap_or_else(|| json!({"room": true}));
        let report: Value = run.operation("Account", &key, "Pool", "reportRoom", &answer).await?;
        self.heard.lock().unwrap().push(heard);
        self.reports.lock().unwrap().push((key, report));
        Ok(())
    }
}

/// A schema of its own holding fixture-pool-db's tables, a pool whose
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
    let schema = format!("behavior_pool_rust_{nanos}");
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

/// What a case reads of an account's Pool with SQL, each instant in epoch
/// milliseconds, and its version.
#[derive(Debug, PartialEq)]
struct Member {
    availability: Option<String>,
    out_until: Option<i64>,
    out_reason: Option<String>,
    checked_at: Option<i64>,
    version: i64,
}

/// A live message of the deadline queue: its use, its deadline's name, the
/// time it was armed at and the time it falls due, in epoch milliseconds.
#[derive(Debug, PartialEq)]
struct Armed {
    use_id: String,
    name: String,
    at: i64,
    due: i64,
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

    /// A fresh owner, in its wire form: the accounts of a case are its
    /// own, which pick's match keeps.
    async fn owner(&self) -> String {
        self.uuid().await.1
    }

    /// Creates an account of an owner through the core, as the
    /// repository's create does, with a key of its own: Pool's initializer
    /// opens it.
    async fn account(&self, owner: &str, name: &str, vendor: &str, kind: &str, rank: i64) -> Row {
        let key = self.uuid().await;
        self.account_keyed(key, owner, name, vendor, kind, rank).await
    }

    /// Creates an account as account does, with the key given in its three
    /// forms.
    async fn account_keyed(&self, (id, wire, text): (types::IdentityUUID, String, String), owner: &str, name: &str, vendor: &str, kind: &str, rank: i64) -> Row {
        let core = superschematic_rules_behaviors::core();
        let layout = Layout::from_json(RULES_LAYOUT).unwrap();
        let host = Host::new(&core, &layout);
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let options = CreateOptions { id: Some(wire.clone()), ..CreateOptions::default() };
        let own = json!({"owner": owner, "name": name, "vendor": vendor, "kind": kind, "rank": rank});
        let own: Map<String, Value> = own.as_object().cloned().unwrap();
        let key = postgres::run_create(&host, &*tx, "Account", own, Caller::new("tester", Vec::<String>::new()), &options).await.unwrap();
        tx.commit().await.unwrap();
        assert_eq!(key, wire);
        Row { id, wire, text }
    }

    /// The account's Pool columns and its version.
    async fn member(&self, account: &Row) -> Member {
        let row = self
            .client
            .query_one(
                "SELECT pool_availability, (extract(epoch FROM pool_out_until) * 1000)::bigint, pool_out_reason, (extract(epoch FROM pool_checked_at) * 1000)::bigint, _version::bigint FROM account WHERE id::text = $1",
                &[&account.text],
            )
            .await
            .unwrap();
        Member { availability: row.get(0), out_until: row.get(1), out_reason: row.get(2), checked_at: row.get(3), version: row.get(4) }
    }

    /// The account's _version.
    async fn version(&self, account: &Row) -> i64 {
        self.member(account).await.version
    }

    /// The live messages of the account's deadline queue, by name.
    async fn armed(&self, account: &Row) -> Vec<Armed> {
        self.client
            .query(
                "SELECT use_id, name, at, (extract(epoch FROM due_at) * 1000)::bigint FROM account_deadline_queue WHERE row_key::text = $1 AND state IN ('ready', 'claimed') ORDER BY name",
                &[&account.text],
            )
            .await
            .unwrap()
            .iter()
            .map(|row| Armed { use_id: row.get(0), name: row.get(1), at: row.get(2), due: row.get(3) })
            .collect()
    }

    /// How many messages of the event queue the account has.
    async fn events(&self, account: &Row) -> i64 {
        self.client
            .query_one("SELECT count(*)::bigint FROM account_event_queue WHERE row_key::text = $1", &[&account.text])
            .await
            .unwrap()
            .get(0)
    }
}

/// AccountRechecks' two queues, each in a claim loop of its own, as its main
/// runs them, until shutdown: the event queue's handler hands each event to
/// provider.
fn start_worker(pool: &Pool, provider: &Arc<Provider>, shutdown: &Shutdown) -> Vec<tokio::task::JoinHandle<Result<(), QueueError>>> {
    let options = Options { concurrency: 2, idle: Duration::from_millis(100), ..Options::default() };
    let deadlines = account_rechecks_deadlines(pool).unwrap();
    let on_deadlines = pool.clone();
    let events = account_rechecks_events(pool).unwrap();
    let on_events = pool.clone();
    let provider = Arc::clone(provider);
    vec![
        tokio::spawn(run_worker(
            deadlines,
            move |claimed| {
                let pool = on_deadlines.clone();
                async move { run_account_rechecks_deadline(&pool, claimed).await }
            },
            options,
            shutdown.clone(),
        )),
        tokio::spawn(run_worker(
            events,
            move |claimed| {
                let pool = on_events.clone();
                let provider = Arc::clone(&provider);
                async move { run_account_rechecks_event(&pool, &*provider, claimed).await }
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

/// Sleeps until the clock is past an instant, in epoch milliseconds.
async fn sleep_past(at: i64) {
    let left = at - now_ms();
    if left >= 0 {
        tokio::time::sleep(Duration::from_millis(left as u64 + 100)).await;
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
fn sdk(base_url: &str, subject: &Subject) -> FixturePoolApiSdk {
    let token = format!("{}:{}", subject.wire, subject.permissions);
    FixturePoolApiSdk::new(ClientConfig::with_base_url(base_url.to_string(), Some(token), None)).unwrap()
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

/// Posts a JSON body as it is, as any HTTP client may send it, as a
/// subject: the answer's status and its envelope's data.
async fn post_as_is(base_url: &str, path: &str, subject: &Subject, body: &str) -> (u16, Value) {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let host = base_url.trim_start_matches("http://");
    let mut stream = tokio::net::TcpStream::connect(host).await.unwrap();
    let request = format!(
        "POST {path} HTTP/1.1\r\nHost: {host}\r\nAuthorization: Bearer {}:{}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        subject.wire,
        subject.permissions,
        body.len()
    );
    stream.write_all(request.as_bytes()).await.unwrap();
    let mut response = String::new();
    stream.read_to_string(&mut response).await.unwrap();
    let (head, payload) = response.split_once("\r\n\r\n").unwrap_or_else(|| panic!("no body: {response}"));
    let status: u16 = head.split(' ').nth(1).and_then(|code| code.parse().ok()).unwrap_or_else(|| panic!("no status: {head}"));
    let answer: Value = serde_json::from_str(payload).unwrap_or_else(|err| panic!("{err}: {payload}"));
    (status, answer["data"].clone())
}

/// The status, code, message and details of a refused call.
fn refusal(err: SDKError) -> (u16, String, String, Value) {
    let details = err.problem().and_then(|problem| problem.details.clone()).unwrap_or(Value::Null);
    match err {
        SDKError::Api { status_code, message, code, .. } => (status_code, code.unwrap_or_default(), message, details),
        other => panic!("not a refusal of the API: {other}"),
    }
}

/// A number of a view, by its member.
fn ms(view: &Value, member: &str) -> i64 {
    view[member].as_i64().unwrap_or_else(|| panic!("{view} has no {member}"))
}

/// pick's member of an account: its key, availability, order and whether
/// prefer kept it.
fn picked(account: &Row, availability: &str, order: i64, preferred: bool) -> Value {
    json!({"member": {"key": account.wire, "availability": availability, "order": order, "preferred": preferred}})
}

fn now_ms() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as i64
}

const MINUTE: i64 = 60_000;

fn case(name: &str) {
    println!("case: {name}");
}

/// The cases run in a task of their own, so the schema is dropped whether
/// they pass or fail.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn pool_runs_through_the_core_the_worker_and_the_projects_handler() {
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

// poolRoundtripCases are the cases of tests/behavior_pool.rs, in poolCases'
// order and words, each making an owner's accounts of its own.
const poolRoundtripCases = `
async fn cases(db: Arc<Database>) {
    let base_url = serve(db.pool.clone()).await;
    let provider = Arc::new(Provider::default());
    let shutdown = Shutdown::new();
    let workers = start_worker(&db.pool, &provider, &shutdown);
    let ada = db.subject("accounts.pool,accounts.observe").await;
    let guest = db.subject("").await;
    let (as_ada, as_guest) = (sdk(&base_url, &ada), sdk(&base_url, &guest));
    let db = &*db;

    case("a pick answers the open account lowest by rank among an owner's accounts");
    let owner = db.owner().await;
    // a1 takes the greatest of three keys and k1 the least, so a pick that
    // went by key rather than by rank would answer k1.
    let mut keys = vec![db.uuid().await, db.uuid().await, db.uuid().await];
    keys.sort_by_key(|key| key.2.clone());
    let (least, middle, greatest) = (keys.remove(0), keys.remove(0), keys.remove(0));
    let a1 = db.account_keyed(greatest, &owner, "a1", "anthropic", "account", 1).await;
    db.account_keyed(middle, &owner, "a2", "anthropic", "account", 2).await;
    db.account_keyed(least, &owner, "k1", "anthropic", "key", 9).await;
    let held = db.version(&a1).await;
    let pick = value(as_ada.account.pool.pick(of(json!({"match": {"owner": owner}})), None).await.unwrap());
    assert_eq!(pick, picked(&a1, "open", 1, true));
    assert_eq!(db.version(&a1).await, held, "a pick writes nothing");
    let other = db.owner().await;
    let none = value(as_ada.account.pool.pick(of(json!({"match": {"owner": other}})), None).await.unwrap());
    assert_eq!(none, json!({}), "another owner's pool has no member");

    case("a null member of match or prefer is absent, so a pick keeps an owner's accounts whatever their rank");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    db.account(&owner, "o1", "openai", "account", 2).await;
    // The body as any HTTP client may send it: the SDK's types take no null.
    let body = json!({"match": {"owner": owner, "rank": null}, "prefer": {"vendor": null}}).to_string();
    let (status, pick) = post_as_is(&base_url, "/api/account/pool/pick", &ada, &body).await;
    assert_eq!((status, pick), (200, picked(&a1, "open", 1, true)));

    case("prefer tries a vendor's open accounts, then its near ones, then the others by rank");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let a2 = db.account(&owner, "a2", "anthropic", "account", 2).await;
    let o1 = db.account(&owner, "o1", "openai", "account", 3).await;
    let near = value(as_ada.account.pool.report_room(a2.id, of(json!({"room": true, "near": true})), None).await.unwrap());
    assert_eq!(without(&near, &["checkedAt"]), json!({"availability": "near"}), "{near}");
    let prefer = json!({"vendor": "anthropic", "kind": "account"});
    let pick = value(as_ada.account.pool.pick(of(json!({"match": {"owner": owner}, "prefer": prefer, "exclude": [a1.wire]})), None).await.unwrap());
    assert_eq!(pick, picked(&a2, "near", 2, true));
    let pick = value(as_ada.account.pool.pick(of(json!({"match": {"owner": owner}, "prefer": prefer, "exclude": [a1.wire, a2.wire]})), None).await.unwrap());
    assert_eq!(pick, picked(&o1, "open", 3, false));

    case("markOut puts an account out for thirty minutes and arms its recheck at outUntil");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let before = now_ms();
    let out = value(as_ada.account.pool.mark_out(a1.id, of(json!({"reason": "rate limited"})), None).await.unwrap());
    let after = now_ms();
    let until = ms(&out, "outUntil");
    assert_eq!(without(&out, &["outUntil"]), json!({"availability": "out", "outReason": "rate limited"}));
    assert!((before + 30 * MINUTE..=after + 30 * MINUTE).contains(&until), "{out}");
    assert_eq!(db.armed(&a1).await, [Armed { use_id: "Pool".into(), name: "recheck".into(), at: until, due: until }]);
    let member = db.member(&a1).await;
    assert_eq!((member.availability.as_deref(), member.out_until, member.out_reason.as_deref()), (Some("out"), Some(until), Some("rate limited")));

    case("markOut keeps the later until, and a repeat answers unchanged");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let t = now_ms();
    let out = value(as_ada.account.pool.mark_out(a1.id, of(json!({"until": t + 60 * MINUTE, "reason": "quota"})), None).await.unwrap());
    assert_eq!(out, json!({"availability": "out", "outUntil": t + 60 * MINUTE, "outReason": "quota"}));
    let held = db.version(&a1).await;
    let again = value(as_ada.account.pool.mark_out(a1.id, of(json!({"until": t + 10 * MINUTE, "reason": "quota"})), None).await.unwrap());
    assert_eq!(again, json!({"availability": "out", "outUntil": t + 60 * MINUTE, "outReason": "quota", "unchanged": true}));
    assert_eq!(db.version(&a1).await, held, "a repeat writes nothing");
    assert_eq!(db.armed(&a1).await, [Armed { use_id: "Pool".into(), name: "recheck".into(), at: t + 60 * MINUTE, due: t + 60 * MINUTE }]);

    case("a check with room before outUntil leaves the account out");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let t = now_ms();
    as_ada.account.pool.mark_out(a1.id, of(json!({"until": t + 60 * MINUTE, "reason": "quota"})), None).await.unwrap();
    let before = now_ms();
    let report = value(as_ada.account.pool.report_room(a1.id, of(json!({"room": true})), None).await.unwrap());
    let after = now_ms();
    assert_eq!(without(&report, &["checkedAt"]), json!({"availability": "out", "outUntil": t + 60 * MINUTE, "outReason": "quota"}), "{report}");
    let checked = ms(&report, "checkedAt");
    assert!((before..=after).contains(&checked), "{report}");
    let member = db.member(&a1).await;
    assert_eq!((member.availability.as_deref(), member.out_until, member.checked_at), (Some("out"), Some(t + 60 * MINUTE), Some(checked)));

    case("with every account out, pick answers no member and the soonest outUntil");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let a2 = db.account(&owner, "a2", "openai", "account", 2).await;
    let t = now_ms();
    as_ada.account.pool.mark_out(a1.id, of(json!({"until": t + 10 * MINUTE, "reason": "busy"})), None).await.unwrap();
    as_ada.account.pool.mark_out(a2.id, of(json!({"until": t + 20 * MINUTE, "reason": "busy"})), None).await.unwrap();
    let pick = value(as_ada.account.pool.pick(of(json!({"match": {"owner": owner}})), None).await.unwrap());
    assert_eq!(pick, json!({"until": t + 10 * MINUTE}), "no member, and the soonest outUntil");

    case("a due recheck hands the handler its event once, whose check with room opens the account");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let until = now_ms() + 2_000;
    let out = value(as_ada.account.pool.mark_out(a1.id, of(json!({"until": until, "reason": "rate limited"})), None).await.unwrap());
    assert_eq!(ms(&out, "outUntil"), until);
    wait_until("the handler reported a1's recheck", || async { provider.reports_of(&a1).len() == 1 }).await;
    let heard = provider.heard_of(&a1);
    assert_eq!(heard.len(), 1, "{heard:?}");
    assert_eq!(heard[0].due_at(), until, "{heard:?}");
    assert_eq!(without(&heard[0].payload, &["firedAt"]), json!({"outUntil": until, "outReason": "rate limited", "dueAt": until}));
    assert!(ms(&heard[0].payload, "firedAt") >= until, "{heard:?}");
    let report = provider.reports_of(&a1).remove(0);
    assert_eq!((&report["availability"], &report["opened"]), (&json!("open"), &json!(true)), "{report}");
    wait_until("a1's event completed", || async { db.events(&a1).await == 0 }).await;
    let member = db.member(&a1).await;
    assert_eq!((member.availability.as_deref(), member.out_until, member.out_reason.as_deref()), (Some("open"), None, None), "{member:?}");
    assert!(db.armed(&a1).await.is_empty(), "a check that opens clears the recheck");
    tokio::time::sleep(Duration::from_millis(1500)).await;
    assert_eq!(provider.calls_of(&a1), 1, "the event was handed to the handler once");
    let pick = value(as_ada.account.pool.pick(of(json!({"match": {"owner": owner}})), None).await.unwrap());
    assert_eq!(pick, picked(&a1, "open", 1, true));

    case("a recheck whose check finds no room puts the account out until resetsAt and arms the recheck there");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let resets_at = now_ms() + 60 * MINUTE;
    provider.answer(&a1, json!({"room": false, "resetsAt": resets_at}));
    let until = now_ms() + 2_000;
    as_ada.account.pool.mark_out(a1.id, of(json!({"until": until, "reason": "rate limited"})), None).await.unwrap();
    wait_until("the handler reported a1's recheck", || async { provider.reports_of(&a1).len() == 1 }).await;
    let report = provider.reports_of(&a1).remove(0);
    assert_eq!(without(&report, &["checkedAt"]), json!({"availability": "out", "outUntil": resets_at, "outReason": "rate limited"}), "{report}");
    wait_until("a1's event completed", || async { db.events(&a1).await == 0 }).await;
    let member = db.member(&a1).await;
    assert_eq!((member.availability.as_deref(), member.out_until, member.checked_at), (Some("out"), Some(resets_at), Some(ms(&report, "checkedAt"))), "{member:?}");
    assert_eq!(db.armed(&a1).await, [Armed { use_id: "Pool".into(), name: "recheck".into(), at: resets_at, due: resets_at }]);

    case("an account whose Budget meter has no room counts as out until the provider's figure lapses");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let a2 = db.account(&owner, "a2", "anthropic", "account", 2).await;
    let resets_at = now_ms() + 3_000;
    let observed = value(as_ada.account.budget.observe(a1.id, of(json!({"meter": "tokens", "used": 1000, "resetsAt": resets_at})), None).await.unwrap());
    assert_eq!(observed, json!({"meter": "tokens", "used": 1000, "limit": 1000, "resetsAt": resets_at}));
    let pick = value(as_ada.account.pool.pick(of(json!({"match": {"owner": owner}})), None).await.unwrap());
    assert_eq!(pick, picked(&a2, "open", 2, true), "a1 has no room");
    as_ada.account.pool.mark_out(a2.id, of(json!({"until": now_ms() + 10 * MINUTE, "reason": "quota"})), None).await.unwrap();
    let pick = value(as_ada.account.pool.pick(of(json!({"match": {"owner": owner}})), None).await.unwrap());
    assert_eq!(pick, json!({"until": resets_at}), "the figure lapses before a2 is back");
    assert_eq!(db.member(&a1).await.availability.as_deref(), Some("open"), "Capacity counts it out at a pick alone");
    sleep_past(resets_at).await;
    let pick = value(as_ada.account.pool.pick(of(json!({"match": {"owner": owner}})), None).await.unwrap());
    assert_eq!(pick, picked(&a1, "open", 1, true));

    case("markOut and reportRoom without accounts.pool are forbidden");
    let owner = db.owner().await;
    let a1 = db.account(&owner, "a1", "anthropic", "account", 1).await;
    let held = db.version(&a1).await;
    let refused = refusal(as_guest.account.pool.mark_out(a1.id, of(json!({"reason": "x"})), None).await.unwrap_err());
    let want = format!("{} may not mark Account {} out: it needs permission accounts.pool", guest.wire, a1.wire);
    assert_eq!(refused, (403, "forbidden".to_string(), want, Value::Null), "the refusal whole");
    let refused = refusal(as_guest.account.pool.report_room(a1.id, of(json!({"room": true})), None).await.unwrap_err());
    let want = format!("{} may not report a check of Account {}: it needs permission accounts.pool", guest.wire, a1.wire);
    assert_eq!(refused, (403, "forbidden".to_string(), want, Value::Null), "the refusal whole");
    assert_eq!(db.version(&a1).await, held, "a refusal writes nothing");
    let pick = value(as_guest.account.pool.pick(of(json!({"match": {"owner": owner}})), None).await.unwrap());
    assert_eq!(pick, picked(&a1, "open", 1, true), "pick needs no permission");

    case("reportRoom of a key no row holds is not found");
    let (ghost, wire, _) = db.uuid().await;
    let refused = refusal(as_ada.account.pool.report_room(ghost, of(json!({"room": true})), None).await.unwrap_err());
    assert_eq!(refused, (404, "not_found".to_string(), format!("Account {wire} does not exist"), Value::Null), "the refusal whole");

    shutdown.cancel();
    for worker in workers {
        worker.await.unwrap().unwrap();
    }
}
`
