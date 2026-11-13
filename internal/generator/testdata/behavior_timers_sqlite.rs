//! fixture-timers-api over a SQLite file (D116): its behavior worker
//! TicketTimers as a worker its host drains, with no async runtime under
//! it, and its behavior operations on the file's writing connection,
//! through the generated router with no socket under it.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use api::behaviors::{ticket_timers_worker, RULES_LAYOUT};
use api::runtime::orm;
use api::runtime::orm::sqlite::{rusqlite, Database};
use api::runtime::worker::drain;
use api::runtime::{block_on, handle, ApiError, Authenticator, Principal};
use api::{build_router, Handlers, Implementations};
use async_trait::async_trait;
use axum::Router;
use bytes::Bytes;
use http::{Request, StatusCode};
use serde_json::{json, Value};
use superschematic_rules_adapter::{sqlite, Caller, CreateOptions, Host, Layout};

/// The scenario's authenticator: Authorization: Bearer
/// <subject>[:<permission>[,...]] is that subject, holding those
/// permissions; no header, no caller.
struct Bearer;

#[async_trait]
impl Authenticator for Bearer {
    async fn authenticate(&self, request: &http::request::Parts) -> Result<Option<Principal>, ApiError> {
        let token = request
            .headers
            .get(http::header::AUTHORIZATION)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "))
            .filter(|token| !token.is_empty());
        Ok(token.map(|token| {
            let (subject, held) = token.split_once(':').unwrap_or((token, ""));
            let permissions: Vec<&str> = held.split(',').filter(|p| !p.is_empty()).collect();
            Principal::new(subject, permissions)
        }))
    }
}

/// A SQLite file of the test's own, holding the tables of the build's
/// SQLite DDL, with the server's handle on it and a connection that stands
/// for another process.
struct File {
    dir: PathBuf,
    path: PathBuf,
    db: Arc<Database>,
    other: rusqlite::Connection,
}

impl File {
    fn create(name: &str) -> File {
        let dir = std::env::temp_dir().join(format!("timers-sqlite-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create the directory");
        let path = dir.join("timers.sqlite");
        rusqlite::Connection::open(&path)
            .and_then(|connection| connection.execute_batch(include_str!("../create.sql")))
            .expect("create the tables");
        let db = Arc::new(Database::open(&path).expect("open the file"));
        let other = rusqlite::Connection::open(&path).expect("open the file again");
        other.busy_timeout(Duration::from_secs(5)).expect("a busy timeout");
        File { dir, path, db, other }
    }

    /// Creates a ticket through the core, on the file's writing connection,
    /// and answers its key in its wire form.
    fn create_ticket(&self, title: &str) -> String {
        let own = json!({ "title": title }).as_object().cloned().unwrap();
        self.db
            .write(|connection| {
                let core = superschematic_rules_behaviors::core();
                let layout = Layout::from_json(RULES_LAYOUT).unwrap();
                let host = Host::new(&core, &layout);
                let caller = Caller::new("tester", Vec::<String>::new());
                sqlite::run_create(&host, connection, "Ticket", own, caller, &CreateOptions::default())
                    .map_err(|err| orm::Error::Database(err.to_string()))
            })
            .expect("the ticket is created")
    }

    fn text(&self, sql: &str) -> String {
        self.other.query_row(sql, [], |row| row.get(0)).expect(sql)
    }

    fn count(&self, table: &str, condition: &str) -> i64 {
        self.other
            .query_row(&format!("SELECT COUNT(*) FROM {table} WHERE {condition}"), [], |row| row.get(0))
            .expect("a count")
    }

    fn status(&self) -> String {
        self.text("SELECT status FROM ticket")
    }
}

impl Drop for File {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

/// The router's answer to a behavior operation, driven where it stands:
/// no runtime polls it.
fn call(router: &Router, token: &str, path: &str, body: Value) -> (StatusCode, Value) {
    let request = Request::builder()
        .method("POST")
        .uri(format!("/api{path}"))
        .header(http::header::AUTHORIZATION, format!("Bearer {token}"))
        .header(http::header::CONTENT_TYPE, "application/json")
        .body(Bytes::from(body.to_string()))
        .expect("a request");
    let response = block_on(handle(router, request));
    let status = response.status();
    let body = serde_json::from_slice(response.body()).unwrap_or(Value::Null);
    (status, body)
}

/// How far `at` lies after `from`, in seconds.
fn seconds_after(from: SystemTime, at: Option<SystemTime>) -> f64 {
    let at = at.expect("a message waits");
    match at.duration_since(from) {
        Ok(after) => after.as_secs_f64(),
        Err(before) => -before.duration().as_secs_f64(),
    }
}

#[test]
fn a_host_with_no_runtime_drains_tickets_deadlines_and_changes() {
    assert!(tokio::runtime::Handle::try_current().is_err());
    let file = File::create("drain");
    let worker = ticket_timers_worker().expect("the worker builds from its queues' descriptors");
    assert_eq!(worker.name(), "TicketDeadline, TicketChange");
    let mut connection = drain::open(&file.path).expect("the worker's own connection");

    // A ticket's create arms its first answer's deadline, a second and a
    // half on, and its trigger writes the insert to the change queue.
    file.create_ticket("No dial tone");
    assert_eq!(file.status(), "open");
    assert_eq!(file.count("ticket_deadline_queue", "name = 'respond' AND state = 'ready'"), 1);
    assert_eq!(file.count("ticket_change_queue", "write = 'insert'"), 1);

    // The first drain runs the insert, which Timers hears and finds armed
    // already, its message spent in the unit, and answers when the deadline
    // falls due.
    let now = SystemTime::now();
    let drained = worker.drain(&mut connection, now, 10).expect("a drain");
    assert_eq!(drained.ran, 1);
    assert_eq!(file.count("ticket_change_queue", "1"), 0);
    let due_in = seconds_after(now, drained.next);
    assert!((0.5..=1.6).contains(&due_in), "respond falls due in {due_in} s");

    // A raw write another process makes is heard at the next drain: the
    // visit's reminder is armed a day before it.
    file.other
        .execute_batch("UPDATE ticket SET visit_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+3 days')")
        .expect("a raw write");
    let drained = worker.drain(&mut connection, SystemTime::now(), 10).expect("a drain");
    assert_eq!(drained.ran, 1);
    assert_eq!(file.count("ticket_deadline_queue", "name = 'remind' AND state = 'ready'"), 1);
    assert!((0.0..=1.6).contains(&seconds_after(SystemTime::now(), drained.next)), "respond is still the next");

    // The host sleeps until the deadline falls due, and the drain runs it:
    // the ticket escalates, the deadline's message is spent with its change
    // set, and the escalation's own write is heard in the same drain.
    std::thread::sleep(drained.wait(SystemTime::now(), Duration::from_secs(5)) + Duration::from_millis(50));
    let now = SystemTime::now();
    let drained = worker.drain(&mut connection, now, 10).expect("a drain");
    assert_eq!(drained.ran, 2, "the deadline, and the write it made");
    assert_eq!(file.status(), "escalated");
    assert_eq!(file.count("ticket_deadline_queue", "name = 'respond'"), 0);
    assert_eq!(file.count("ticket_timers_deadlines", "name = 'respond' AND fired = 1"), 1);
    assert_eq!(file.count("ticket_change_queue", "1"), 0);
    // The reminder is the next, two days on.
    let remind_in = seconds_after(now, drained.next);
    assert!((172_000.0..=173_000.0).contains(&remind_in), "remind falls due in {remind_in} s");

    // A drain of nothing runs nothing.
    assert_eq!(worker.drain(&mut connection, SystemTime::now(), 10).expect("a drain").ran, 0);
}

#[test]
fn a_deadline_runs_at_the_time_its_drain_is_given() {
    let file = File::create("clock");
    let worker = ticket_timers_worker().expect("the worker");
    let mut connection = drain::open(&file.path).expect("the worker's own connection");
    file.create_ticket("No dial tone");
    // The deadline is made due by the database's clock, and the drain is
    // given a time of the host's own, an hour on: the core runs the fire at
    // it, so the ticket entered its state then.
    file.other
        .execute_batch("UPDATE ticket_deadline_queue SET due_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 seconds')")
        .expect("a raw write");
    let given = SystemTime::now() + Duration::from_secs(3600);
    let drained = worker.drain(&mut connection, given, 10).expect("a drain");
    assert_eq!(drained.ran, 3, "the insert, the deadline and the write it made");
    assert_eq!(file.status(), "escalated");
    let entered = file.text("SELECT status_entered_at FROM ticket");
    let millis = given.duration_since(UNIX_EPOCH).unwrap().as_millis() as i64;
    let entered_at: i64 = file
        .other
        .query_row(
            "SELECT CAST(ROUND((julianday(?1) - 2440587.5) * 86400000) AS INTEGER)",
            [&entered],
            |row| row.get(0),
        )
        .unwrap();
    assert!((entered_at - millis).abs() <= 1, "the ticket escalated at {entered}, not the time the drain was given");
}

#[test]
fn a_behavior_operation_runs_on_the_files_writer_and_wakes_the_worker() {
    let file = File::create("operation");
    let handlers = Handlers {
        authenticator: Arc::new(Bearer),
    };
    let router = build_router(Implementations::new(handlers, Arc::clone(&file.db)));
    let worker = ticket_timers_worker().expect("the worker");
    let mut connection = drain::open(&file.path).expect("the worker's own connection");
    let key = file.create_ticket("No dial tone");
    worker.drain(&mut connection, SystemTime::now(), 10).expect("a drain");

    // A transition through the route: the core runs it in a transaction of
    // the file's writing connection, whose commit wakes a sleeping worker.
    let seen = file.db.wake().seen();
    let (status, body) = call(
        &router,
        "ada:tickets.write",
        &format!("/ticket/{key}/status/transition"),
        json!({ "to": "waiting" }),
    );
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(file.status(), "waiting");
    assert!(file.db.wake().wait(seen, Duration::ZERO), "the commit wakes the worker");

    // The worker hears the write: the first answer's deadline is cancelled,
    // since the ticket left open, and the idle one is armed an hour on.
    let now = SystemTime::now();
    let drained = worker.drain(&mut connection, now, 10).expect("a drain");
    assert_eq!(drained.ran, 1);
    assert_eq!(file.count("ticket_deadline_queue", "name = 'respond'"), 0);
    assert_eq!(file.count("ticket_deadline_queue", "name = 'idle' AND state = 'ready'"), 1);
    let idle_in = seconds_after(now, drained.next);
    assert!((3590.0..=3601.0).contains(&idle_in), "idle falls due in {idle_in} s");

    // A refusal is the problem of its status, nothing written.
    let seen = file.db.wake().seen();
    let (status, body) = call(
        &router,
        "ada:tickets.write",
        &format!("/ticket/{key}/status/transition"),
        json!({ "to": "escalated" }),
    );
    assert!(status.is_client_error(), "{status}: {body}");
    assert_eq!(file.status(), "waiting");
    assert_eq!(file.db.wake().seen(), seen, "a write that rolls back wakes no one");

    // Without the permission the route asks for, the core is not reached.
    let (status, _) = call(&router, "eve", &format!("/ticket/{key}/status/transition"), json!({ "to": "closed" }));
    assert_eq!(status, StatusCode::FORBIDDEN);
}
