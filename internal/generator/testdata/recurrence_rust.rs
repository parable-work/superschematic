//! fixture-recurrence-api's filings end to end in the Rust server (D152,
//! D188), run by TestRustRecurrenceEndToEnd: the generated router over
//! fixture-recurrence-db's Rust repositories on Postgres, in a schema of
//! the test's own at RECURRENCE_TEST_DATABASE_URL with the database's
//! create.sql applied, and the behavior worker FilingDeadlines' generated
//! queues (Filing's deadlines, changes and events) in process under the
//! runtime's run_worker, with a Workers of the test's own that records each
//! event Recurrence emits as it opens, reminds and misses an occurrence
//! (D153). The cases are the TypeScript server's (tsrestgen's
//! behavior_recurrence_runtime.test.ts).
//!
//! A request names its caller in its Authorization header,
//! `<subject>:<permission>,...`. A Filing implements Recurrence in
//! Europe/London: each occurrence opens two weeks before it is due,
//! reminds three days before, and is missed a day after; its due date moves
//! to the next business day of the holiday calendar its holidayCalendar
//! names, whose days are rows of Holiday. A deadline's fire is driven as
//! the TypeScript test drives one: its message made due with SQL, then the
//! worker's queues until the row shows it.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use api::behaviors::{
    filing_deadlines_changes, filing_deadlines_deadlines, filing_deadlines_events, run_filing_deadlines_change,
    run_filing_deadlines_deadline, run_filing_deadlines_event, EventRun,
};
use api::database_types::orm::postgres::Repositories;
use api::runtime::orm::calendar::{read_calendar, CalendarOptions, CalendarRead};
use api::runtime::orm::postgres::{deadpool_postgres, tokio_postgres};
use api::runtime::orm::{Error, Values};
use api::runtime::postgres::Pool;
use api::runtime::worker::{run_worker, Options};
use api::runtime::{bearer_token, ApiError, Authenticator, Principal, Shutdown};
use api::types::{FilingRecurrenceDueEvent, FilingRecurrenceMissedEvent, FilingRecurrenceOpenedEvent, FilingRecurrenceRemindedEvent};
use api::{build_router, BoxError, Handlers, Implementations, Workers};
use async_trait::async_trait;
use axum::body::Body;
use axum::Router;
use http::Request;
use http_body_util::BodyExt;
use serde_json::{json, Value};
use tower::ServiceExt;

const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/// The UUID 00000000-0000-4000-8000-<n> in its wire form, base62.
fn uuid(n: u128) -> String {
    let mut number = (0x4000u128 << 64) | (0x8000u128 << 48) | n;
    let mut digits = Vec::new();
    while number > 0 {
        digits.push(BASE62[(number % 62) as usize]);
        number /= 62;
    }
    digits.reverse();
    String::from_utf8(digits).unwrap()
}

/// A base62 UUID's hyphenated text, as the database writes it.
fn hyphen(base62: &str) -> String {
    if base62.contains('-') {
        return base62.to_owned();
    }
    let mut n = 0u128;
    for c in base62.bytes() {
        n = n * 62 + BASE62.iter().position(|b| *b == c).unwrap() as u128;
    }
    let h = format!("{n:032x}");
    format!("{}-{}-{}-{}-{}", &h[0..8], &h[8..12], &h[12..16], &h[16..20], &h[20..])
}

fn values(value: Value) -> Values {
    value.as_object().cloned().unwrap()
}

fn case(name: &str) {
    println!("case: {name}");
}

const DAY: i64 = 86_400_000;

fn now_millis() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as i64
}

/// The civil date of days since the epoch (Howard Hinnant's
/// `civil_from_days`).
fn civil(days: i64) -> (i64, i64, i64) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    (if month <= 2 { era * 400 + yoe + 1 } else { era * 400 + yoe }, month, day)
}

fn date(millis: i64) -> String {
    let (y, m, d) = civil(millis.div_euclid(DAY));
    format!("{y:04}-{m:02}-{d:02}")
}

fn iso(millis: i64) -> String {
    let (y, m, d) = civil(millis.div_euclid(DAY));
    let of_day = millis.rem_euclid(DAY) / 1000;
    format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}Z", of_day / 3600, of_day % 3600 / 60, of_day % 60)
}

/// A wall date-time at 09:00 of the first weekday at least some days from
/// now, as setRecurrence takes it: no weekend moves it.
fn london_day(days_from_now: i64) -> String {
    let mut at = now_millis() + days_from_now * DAY;
    // 1970-01-01 was a Thursday: 0 is Sunday, 6 Saturday.
    while matches!((at.div_euclid(DAY) + 4).rem_euclid(7), 0 | 6) {
        at += DAY;
    }
    format!("{}T09:00", date(at))
}

/// The caller the credential names, `<subject>:<permissions>`.
struct Bearer;

#[async_trait]
impl Authenticator for Bearer {
    async fn authenticate(&self, request: &http::request::Parts) -> Result<Option<Principal>, ApiError> {
        let Some(token) = bearer_token(&request.headers) else {
            return Ok(None);
        };
        let (who, permissions) = token.split_once(':').unwrap_or((token, ""));
        Ok(Some(Principal::new(who, permissions.split(',').filter(|p| !p.is_empty()))))
    }
}

/// What Recurrence emitted, as the project's Workers methods heard it
/// (D153): the kind, the occurrence's key and the row's.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Heard {
    kind: &'static str,
    key: String,
    row: String,
}

#[derive(Default)]
struct Hearing(Mutex<Vec<Heard>>);

impl Hearing {
    fn hear(&self, kind: &'static str, row: Value, key: &str) {
        self.0.lock().unwrap().push(Heard { kind, key: key.to_owned(), row: hyphen(row.as_str().unwrap()) });
    }

    fn of(&self, row: &str) -> Vec<(String, String)> {
        let mut out: Vec<(String, String)> = self
            .0
            .lock()
            .unwrap()
            .iter()
            .filter(|e| e.row == hyphen(row))
            .map(|e| (e.kind.to_owned(), e.key.clone()))
            .collect();
        out.sort();
        out
    }
}

struct Heard3(Arc<Hearing>);

#[async_trait]
impl Workers for Heard3 {
    async fn filing_deadlines_recurrence_opened(&self, event: FilingRecurrenceOpenedEvent, _: &EventRun<'_>) -> Result<(), BoxError> {
        self.0.hear("opened", serde_json::to_value(event.key)?, &event.payload.key);
        Ok(())
    }

    async fn filing_deadlines_recurrence_reminded(&self, event: FilingRecurrenceRemindedEvent, _: &EventRun<'_>) -> Result<(), BoxError> {
        self.0.hear("reminded", serde_json::to_value(event.key)?, &event.payload.key);
        Ok(())
    }

    async fn filing_deadlines_recurrence_due(&self, event: FilingRecurrenceDueEvent, _: &EventRun<'_>) -> Result<(), BoxError> {
        self.0.hear("due", serde_json::to_value(event.key)?, &event.payload.key);
        Ok(())
    }

    async fn filing_deadlines_recurrence_missed(&self, event: FilingRecurrenceMissedEvent, _: &EventRun<'_>) -> Result<(), BoxError> {
        self.0.hear("missed", serde_json::to_value(event.key)?, &event.payload.key);
        Ok(())
    }
}

/// One occurrence of a filing, its times in epoch milliseconds.
#[derive(Clone, Debug, PartialEq)]
struct Occurrence {
    key: String,
    due: Option<String>,
    due_at: i64,
    opens_at: Option<i64>,
    remind_at: Option<i64>,
    overdue_at: Option<i64>,
    state: String,
    revision: i64,
    shifted: Option<String>,
    reminded_at: Option<i64>,
    completed_by_table: Option<String>,
    completed_by_key: Option<String>,
    completed_by_principal: Option<String>,
}

struct App {
    router: Router,
    pool: Pool,
    admin: tokio_postgres::Client,
    schema: String,
    hearing: Arc<Hearing>,
    filings: Mutex<u32>,
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

impl App {
    async fn new() -> App {
        let url = std::env::var("RECURRENCE_TEST_DATABASE_URL").expect("RECURRENCE_TEST_DATABASE_URL names the database");
        let config: tokio_postgres::Config = url.parse().unwrap();
        let admin = connect(&config).await;
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        let schema = format!("recurrence_rust_{nanos}");
        admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
        let mut scoped = config;
        scoped.options(format!("-c search_path={schema},public"));
        let client = connect(&scoped).await;
        client.batch_execute(include_str!("../create.sql")).await.unwrap();
        let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
        let pool = Pool::builder(manager).max_size(8).build().unwrap();
        let handlers = Handlers { authenticator: Arc::new(Bearer) };
        let router = build_router(Implementations::new(handlers, pool.clone()));
        App { router, pool, admin, schema, hearing: Arc::new(Hearing::default()), filings: Mutex::new(0) }
    }

    /// Calls an operation of a filing's Recurrence as a caller: its status
    /// and its answer, the body's data (or the refusal whole).
    async fn call(&self, who: &str, id: &str, operation: &str, body: Value) -> (u16, Value) {
        let request = Request::post(format!("/api/filing/{id}/recurrence/{operation}"))
            .header(http::header::AUTHORIZATION, format!("Bearer {who}"))
            .header(http::header::CONTENT_TYPE, "application/json")
            .body(Body::from(body.to_string()))
            .unwrap();
        let response = self.router.clone().oneshot(request).await.unwrap();
        let status = response.status().as_u16();
        let bytes = response.into_body().collect().await.unwrap().to_bytes();
        let body: Value = if bytes.is_empty() { Value::Null } else { serde_json::from_slice(&bytes).unwrap() };
        let data = body.get("data").cloned().unwrap_or(body);
        (status, data)
    }

    /// Creates a row through the repositories, as no one, in a transaction
    /// of its own, and answers its key in its wire form.
    async fn create(&self, table: &str, row: Value) -> String {
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let id = {
            let repos = Repositories::new(&tx).unwrap();
            let row = repos.untyped().table(table).unwrap().create_one(&values(row)).await.unwrap();
            row["id"].as_str().unwrap().to_owned()
        };
        tx.commit().await.unwrap();
        id
    }

    async fn new_filing(&self, own: Value) -> String {
        let n = {
            let mut filings = self.filings.lock().unwrap();
            *filings += 1;
            *filings
        };
        let mut row = values(json!({ "title": format!("Filing {n}") }));
        row.extend(values(own));
        self.create("Filing", Value::Object(row)).await
    }

    /// A filing's occurrences by SQL, in due order.
    async fn occurrences(&self, id: &str) -> Vec<Occurrence> {
        let ms = |column: &str| format!("(extract(epoch from {column}) * 1000)::bigint");
        let sql = format!(
            "SELECT key, due, {}, {}, {}, {}, state, revision, shifted, {}, completed_by_table, completed_by_key, completed_by_principal FROM filing_recurrence_occurrences WHERE filing_id = $1::text::uuid ORDER BY due_at, key",
            ms("due_at"),
            ms("opens_at"),
            ms("remind_at"),
            ms("overdue_at"),
            ms("reminded_at"),
        );
        let client = self.pool.get().await.unwrap();
        let rows = client.query(&sql, &[&hyphen(id)]).await.unwrap();
        rows.iter()
            .map(|row| Occurrence {
                key: row.get(0),
                due: row.get(1),
                due_at: row.get(2),
                opens_at: row.get(3),
                remind_at: row.get(4),
                overdue_at: row.get(5),
                state: row.get(6),
                revision: row.get(7),
                shifted: row.get(8),
                reminded_at: row.get(9),
                completed_by_table: row.get(10),
                completed_by_key: row.get(11),
                completed_by_principal: row.get(12),
            })
            .collect()
    }

    /// A filing's live deadline messages, by name.
    async fn deadlines(&self, id: &str) -> BTreeMap<String, i64> {
        let client = self.pool.get().await.unwrap();
        let rows = client
            .query("SELECT name, at FROM filing_deadline_queue WHERE row_key = $1::text::uuid AND state IN ('ready', 'claimed')", &[&hyphen(id)])
            .await
            .unwrap();
        rows.iter().map(|row| (row.get(0), row.get(1))).collect()
    }

    async fn make_due(&self, id: &str, name: &str) {
        let client = self.pool.get().await.unwrap();
        let n = client
            .execute(
                "UPDATE filing_deadline_queue SET due_at = now() WHERE row_key = $1::text::uuid AND name = $2 AND state IN ('ready', 'claimed')",
                &[&hyphen(id), &name],
            )
            .await
            .unwrap();
        assert_eq!(n, 1);
    }

    /// Runs the behavior worker's three queues until `done` holds, or
    /// fifteen seconds pass, then stops them.
    async fn work_until(&self, done: impl AsyncFn() -> bool) {
        let shutdown = Shutdown::new();
        let options = Options { concurrency: 2, idle: Duration::from_millis(100), grace: Duration::from_secs(2), ..Options::default() };
        let workers: Arc<dyn Workers> = Arc::new(Heard3(self.hearing.clone()));
        let pool = self.pool.clone();
        let deadlines = {
            let pool = pool.clone();
            tokio::spawn(run_worker(
                filing_deadlines_deadlines(&pool).unwrap(),
                move |claimed| {
                    let pool = pool.clone();
                    async move { run_filing_deadlines_deadline(&pool, claimed).await }
                },
                options,
                shutdown.clone(),
            ))
        };
        let changes = {
            let pool = pool.clone();
            tokio::spawn(run_worker(
                filing_deadlines_changes(&pool).unwrap(),
                move |claimed| {
                    let pool = pool.clone();
                    async move { run_filing_deadlines_change(&pool, claimed).await }
                },
                options,
                shutdown.clone(),
            ))
        };
        let events = {
            let pool = pool.clone();
            tokio::spawn(run_worker(
                filing_deadlines_events(&pool).unwrap(),
                move |claimed| {
                    let pool = pool.clone();
                    let workers = workers.clone();
                    async move { run_filing_deadlines_event(&pool, workers.as_ref(), claimed).await }
                },
                options,
                shutdown.clone(),
            ))
        };
        let deadline = Instant::now() + Duration::from_secs(15);
        while !done().await && Instant::now() < deadline {
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
        shutdown.cancel();
        for ran in [deadlines, changes, events] {
            ran.await.unwrap().unwrap();
        }
        if !done().await {
            // What the queues failed with, for the failure's message.
            let client = self.pool.get().await.unwrap();
            let mut failed = Vec::new();
            for queue in ["filing_deadline_queue", "filing_change_queue", "filing_event_queue"] {
                let sql = format!("SELECT state, attempts, COALESCE(last_error, '') FROM {queue} WHERE last_error IS NOT NULL OR state <> 'done'");
                for row in client.query(&sql, &[]).await.unwrap() {
                    failed.push(format!("{queue}: {} after {} attempts: {}", row.get::<_, String>(0), row.get::<_, i32>(1), row.get::<_, String>(2)));
                }
            }
            panic!("the worker did not get there in fifteen seconds: {failed:#?}");
        }
    }

    async fn drop_schema(&self) {
        self.admin.batch_execute(&format!("DROP SCHEMA IF EXISTS {} CASCADE", self.schema)).await.unwrap();
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn recurrence_on_postgres() {
    let app = App::new().await;
    let manager_id = uuid(0x3a);
    let worker_id = uuid(0x3b);
    let manager = format!("{manager_id}:filings.manage,filings.work");
    let worker = format!("{worker_id}:filings.work");

    case("setRecurrence plans the occurrences up to the horizon, moved off the row calendar holidays, and arms the open, remind and overdue deadlines");
    {
        let start = london_day(40);
        // The first due day is a holiday of the east calendar: it moves to
        // the next business day.
        app.create("Holiday", json!({ "calendar": "east", "date": &start[..10], "name": "Closed" })).await;
        let id = app.new_filing(json!({ "holidayCalendar": "east" })).await;
        let (status, set) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=MONTHLY", "start": start })).await;
        assert_eq!(status, 200, "{set}");
        assert_eq!(set["recurrence"]["revision"], 1);
        let rows = app.occurrences(&id).await;
        assert!(rows.len() >= 12, "{} occurrences", rows.len());
        assert_eq!(rows[0].key, start);
        assert_eq!(rows[0].shifted.as_deref(), Some("holiday"));
        // A monthly rule's later occurrences fall on any day: some move off
        // a weekend, and each has its key.
        assert_ne!(rows[0].due.as_deref(), Some(start.as_str()));
        assert!(rows.iter().all(|row| row.state == "upcoming"));
        let armed = app.deadlines(&id).await;
        assert_eq!(armed.get("open").copied(), rows[0].opens_at);
        assert_eq!(armed.get("remind").copied(), rows[0].remind_at);
        assert!(!armed.contains_key("overdue"));
        let (status, listed) = app.call(&worker, &id, "list-occurrences", json!({ "states": ["upcoming"] })).await;
        assert_eq!(status, 200, "{listed}");
        assert_eq!(listed["items"][0]["key"], start);
    }

    case("a rule change leaves the open occurrence untouched and replans the ones that have not opened");
    {
        let id = app.new_filing(json!({})).await;
        // Due in ten days: its two-week window is open at once.
        let (status, _) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=MONTHLY", "start": london_day(10) })).await;
        assert_eq!(status, 200);
        let before = app.occurrences(&id).await;
        assert_eq!(before[0].state, "open");
        let (status, changed) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=WEEKLY", "start": london_day(20) })).await;
        assert_eq!(status, 200, "{changed}");
        assert_eq!(changed["kept"], json!([before[0].key]));
        let after = app.occurrences(&id).await;
        assert_eq!(after[0], before[0]);
        assert!(after[1..].iter().all(|row| row.revision == 2));
        // The rest are the weekly rule's: a week apart, give or take a
        // change of the clocks.
        let gaps: Vec<i64> = after[2..].iter().zip(&after[1..]).map(|(next, prev)| next.due_at - prev.due_at).collect();
        assert!(gaps.len() > 40, "{} gaps", gaps.len());
        assert!(gaps.iter().all(|gap| (gap - 7 * DAY).abs() <= 3_600_000), "{gaps:?}");
    }

    case("the remind deadline fires on the worker, records the reminder and emits the opening and the reminder");
    {
        let id = app.new_filing(json!({})).await;
        let (status, _) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=YEARLY", "start": london_day(30) })).await;
        assert_eq!(status, 200);
        let first = app.occurrences(&id).await.remove(0);
        assert_eq!(first.reminded_at, None);
        app.make_due(&id, "remind").await;
        app.work_until(async || app.occurrences(&id).await[0].reminded_at.is_some()).await;
        let fired = app.occurrences(&id).await.remove(0);
        assert!(fired.reminded_at.unwrap() >= first.remind_at.unwrap());
        // Three days before its due, its two-week window has opened too.
        assert_eq!(fired.state, "open");
        // The fire emitted both moves, which the worker handed to the
        // project's Workers methods (D153).
        app.work_until(async || app.hearing.of(&id).iter().any(|(kind, _)| kind == "reminded")).await;
        assert_eq!(app.hearing.of(&id), [("opened".to_owned(), fired.key.clone()), ("reminded".to_owned(), fired.key.clone())]);
    }

    case("the due deadline fires on the worker at an occurrence due, records it and emits due (#601)");
    {
        let id = app.new_filing(json!({})).await;
        // Due in ten days: its two-week window is open at once.
        let (status, _) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=MONTHLY", "start": london_day(10) })).await;
        assert_eq!(status, 200);
        let first = app.occurrences(&id).await.remove(0);
        assert_eq!(first.state, "open");
        assert_eq!(app.deadlines(&id).await.get("due"), Some(&first.due_at));
        // Its due comes: the occurrence's due is a second ago, and its
        // deadline now.
        let client = app.pool.get().await.unwrap();
        let n = client
            .execute(
                "UPDATE filing_recurrence_occurrences SET due_at = now() - interval '1 second' WHERE filing_id = $1::text::uuid AND key = $2",
                &[&hyphen(&id), &first.key],
            )
            .await
            .unwrap();
        assert_eq!(n, 1);
        app.make_due(&id, "due").await;
        app.work_until(async || app.hearing.of(&id).iter().any(|(kind, _)| kind == "due")).await;
        let row = client
            .query_one("SELECT state, fell_due_at IS NOT NULL FROM filing_recurrence_occurrences WHERE filing_id = $1::text::uuid AND key = $2", &[&hyphen(&id), &first.key])
            .await
            .unwrap();
        assert_eq!((row.get::<_, String>(0).as_str(), row.get::<_, bool>(1)), ("open", true));
        let dues: Vec<String> = app.hearing.of(&id).into_iter().filter(|(kind, _)| kind == "due").map(|(_, key)| key).collect();
        assert_eq!(dues, std::slice::from_ref(&first.key));
    }

    case("complete records what completed an occurrence and who, and an afterDone recurrence dates its next from it");
    {
        let id = app.new_filing(json!({})).await;
        let (status, _) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "afterDone", "every": "P90D", "start": london_day(5) })).await;
        assert_eq!(status, 200);
        let (status, done) = app.call(&worker, &id, "complete", json!({ "completedBy": { "table": "Payment", "key": "p-17" } })).await;
        assert_eq!(status, 200, "{done}");
        assert_eq!(done["state"], "done");
        assert_eq!(done["completedBy"], json!({ "table": "Payment", "key": "p-17" }));
        assert_eq!(done["completedByPrincipal"], worker_id);
        let rows = app.occurrences(&id).await;
        assert_eq!(rows.iter().map(|row| row.state.as_str()).collect::<Vec<_>>(), ["done", "upcoming"]);
        assert_eq!((rows[0].completed_by_table.as_deref(), rows[0].completed_by_key.as_deref()), (Some("Payment"), Some("p-17")));
        assert_eq!(rows[0].completed_by_principal.as_deref(), Some(worker_id.as_str()));
    }

    case("without filings.manage setRecurrence is 403 and a rule that is no RRULE is 400");
    {
        let id = app.new_filing(json!({})).await;
        let (status, _) = app.call(&worker, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=YEARLY", "start": london_day(5) })).await;
        assert_eq!(status, 403);
        let (status, _) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=SOMETIMES", "start": london_day(5) })).await;
        assert_eq!(status, 400);
    }

    case("the calendar merges a contract end date and a filing occurrences, each dated as its source says");
    {
        let ends = london_day(30)[..10].to_owned();
        let contract = app.create("Contract", json!({ "title": "Lease", "endsOn": ends })).await;
        let id = app.new_filing(json!({ "title": "Quarterly return" })).await;
        let (status, _) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=MONTHLY", "start": london_day(10) })).await;
        assert_eq!(status, 200);
        let principal = Principal::new(manager_id.clone(), ["filings.manage", "filings.work"]);
        let client = app.pool.get().await.unwrap();
        let repos = Repositories::new(&client).unwrap();
        let now = now_millis();
        let read = CalendarRead { from: iso(now), to: iso(now + 45 * DAY), ..CalendarRead::default() };
        let calendar = read_calendar(repos.untyped(), Some(&principal), &read, &CalendarOptions::default()).await.unwrap();
        let renewal = calendar.entries.iter().find(|e| e.source == "contract" && e.key == contract).expect("the contract's end");
        assert_eq!((renewal.kind.as_str(), renewal.title.as_str(), renewal.start.as_str(), renewal.all_day), ("renewal", "Lease", ends.as_str(), true));
        let deadlines: Vec<_> = calendar.entries.iter().filter(|e| e.source == "filing" && e.key == id).collect();
        assert!(!deadlines.is_empty());
        let first = deadlines[0];
        assert_eq!((first.kind.as_str(), first.color.as_deref(), first.title.as_str()), ("deadline", Some("filing"), "Quarterly return"));
        let occurrence = first.occurrence.as_ref().unwrap();
        assert_eq!((occurrence.key.as_str(), occurrence.state.as_str()), (london_day(10).as_str(), "open"));
        let starts: Vec<&str> = calendar.entries.iter().map(|e| e.start.as_str()).collect();
        let mut sorted = starts.clone();
        sorted.sort_unstable();
        assert_eq!(starts, sorted);
        // The filing source needs filings.work, the schema's permission
        // (#601): a caller without it reads the contract's end alone.
        let unworked = Principal::new(manager_id.clone(), ["filings.manage"]);
        let other = read_calendar(repos.untyped(), Some(&unworked), &read, &CalendarOptions::default()).await.unwrap();
        assert!(!other.entries.iter().any(|e| e.source == "filing"), "{:?}", other.entries);
        assert!(other.entries.iter().any(|e| e.source == "contract" && e.key == contract));
        // A read with no instant is refused.
        let refused = read_calendar(repos.untyped(), Some(&principal), &CalendarRead { from: "soon".to_owned(), to: iso(now), ..CalendarRead::default() }, &CalendarOptions::default()).await;
        assert!(matches!(refused, Err(Error::Invalid(_))), "{refused:?}");
    }

    case("a raw SQL write of a filing holiday calendar is heard by the worker, which plans again");
    {
        let start = london_day(60);
        app.create("Holiday", json!({ "calendar": "west", "date": &start[..10], "name": "Closed" })).await;
        let id = app.new_filing(json!({})).await;
        let (status, _) = app.call(&manager, &id, "set-recurrence", json!({ "anchor": "calendar", "rule": "FREQ=MONTHLY", "start": start })).await;
        assert_eq!(status, 200);
        assert_eq!(app.occurrences(&id).await[0].shifted, None);
        let client = app.pool.get().await.unwrap();
        client.execute("UPDATE filing SET holiday_calendar = 'west' WHERE id = $1::text::uuid", &[&hyphen(&id)]).await.unwrap();
        app.work_until(async || app.occurrences(&id).await[0].shifted.as_deref() == Some("holiday")).await;
    }

    app.drop_schema().await;
}
