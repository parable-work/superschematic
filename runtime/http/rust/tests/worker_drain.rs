//! A worker on a SQLite file as a function its host calls (D116), over a
//! file of the test's own holding the tables of
//! `testdata/queue/create_sqlite.sql` and the descriptors of
//! `order_placed.json` and `ping.json`: what a drain counts and when it says
//! the next message falls due, a message spent in the unit that applies its
//! writes, the prune's cadence, a file another connection holds, a host
//! with no async runtime that a commit of its own process wakes, and the
//! loop on tokio.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use rusqlite::Connection;
use serde_json::{json, Value};
use superschematic_http_runtime::orm;
use superschematic_http_runtime::queue::{enqueue_sqlite, QueueDefinition, QueueError};
use superschematic_http_runtime::worker::{
    drain, run_drain, Claimed, DrainOptions, Drained, SqliteWorker, Unit, Wake,
};
use superschematic_http_runtime::{block_on, BoxError, Shutdown};

fn testdata(name: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/testdata/queue")
        .join(name);
    std::fs::read_to_string(&path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()))
}

fn order_placed() -> QueueDefinition {
    QueueDefinition::parse(&testdata("order_placed.json")).unwrap()
}

fn ping() -> QueueDefinition {
    QueueDefinition::parse(&testdata("ping.json")).unwrap()
}

/// A SQLite file of the test's own, holding the fixture's tables and
/// `applied`, a table a handler writes, removed when dropped.
struct Db {
    dir: PathBuf,
    path: PathBuf,
}

impl Db {
    fn open(name: &str) -> Db {
        let dir = std::env::temp_dir().join(format!(
            "worker-drain-{name}-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("queue.sqlite");
        let connection = Connection::open(&path).unwrap();
        connection
            .execute_batch(&testdata("create_sqlite.sql"))
            .unwrap();
        connection
            .execute_batch("CREATE TABLE applied (quantity INTEGER NOT NULL)")
            .unwrap();
        Db { dir, path }
    }

    /// A connection as a worker opens its own.
    fn worker(&self) -> Connection {
        drain::open(&self.path).unwrap()
    }

    /// A connection that waits `busy` for another's write lock.
    fn connection(&self, busy: Duration) -> Connection {
        let connection = Connection::open(&self.path).unwrap();
        connection.busy_timeout(busy).unwrap();
        connection
    }

    fn enqueue(&self, definition: &QueueDefinition, message: &Value) {
        enqueue_sqlite(
            &self.connection(Duration::from_secs(5)),
            definition,
            message,
        )
        .unwrap();
    }

    fn exec(&self, sql: &str) {
        self.connection(Duration::from_secs(5))
            .execute_batch(sql)
            .unwrap();
    }

    fn count(&self, table: &str, condition: &str) -> i64 {
        self.connection(Duration::from_secs(5))
            .query_row(
                &format!("SELECT COUNT(*) FROM {table} WHERE {condition}"),
                [],
                |row| row.get(0),
            )
            .unwrap()
    }

    /// The one message's state, its attempts and its last error.
    fn row(&self) -> (String, i64, Option<String>) {
        self.connection(Duration::from_secs(5))
            .query_row(
                "SELECT state, attempts, last_error FROM order_placed_queue",
                [],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
            )
            .unwrap()
    }

    fn path(&self) -> &Path {
        &self.path
    }
}

impl Drop for Db {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

fn an_order(n: u32) -> Value {
    json!({
        "orderId": "3Nf1lmBDcdDKfsC9ccR4DG",
        "placedAt": "2026-10-11T09:15:00.123456+02:00",
        "quantity": n,
        "shipTo": {"line1": "1 Main St", "city": "Springfield"},
        "tags": ["a"],
    })
}

/// The quantities a worker's handler saw, in the order it saw them.
#[derive(Clone, Default)]
struct Seen(Arc<Mutex<Vec<u64>>>);

impl Seen {
    fn push(&self, message: &Value) {
        self.0
            .lock()
            .unwrap()
            .push(message["quantity"].as_u64().unwrap());
    }

    fn all(&self) -> Vec<u64> {
        self.0.lock().unwrap().clone()
    }
}

/// A worker of the orders' queue whose handler records each message.
fn recording(seen: &Seen) -> SqliteWorker {
    let seen = seen.clone();
    SqliteWorker::new()
        .queue(order_placed(), move |message: Value| {
            seen.push(&message);
            Ok(())
        })
        .unwrap()
}

/// How far `at` lies from `from`, in seconds, negative before it.
fn seconds_from(from: SystemTime, at: SystemTime) -> f64 {
    match at.duration_since(from) {
        Ok(after) => after.as_secs_f64(),
        Err(before) => -before.duration().as_secs_f64(),
    }
}

#[test]
fn a_drain_runs_what_is_due_up_to_its_limit_and_counts_it() {
    let db = Db::open("counts");
    let def = order_placed();
    for n in 1..=3 {
        db.enqueue(&def, &an_order(n));
    }
    db.exec("UPDATE order_placed_queue SET due_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-' || (4 - quantity) || ' seconds')");
    let seen = Seen::default();
    let worker = recording(&seen);
    assert_eq!(worker.name(), "OrderPlaced");
    let mut connection = db.worker();
    let now = SystemTime::now();

    // Two of the three, the oldest first, and more is due: the next is now.
    let drained = worker.drain(&mut connection, now, 2).unwrap();
    assert_eq!(
        drained,
        Drained {
            ran: 2,
            next: Some(now)
        }
    );
    assert_eq!(drained.wait(now, Duration::from_secs(1)), Duration::ZERO);
    assert_eq!(seen.all(), [1, 2]);

    // The third, and then nothing waits: done messages are due never.
    let drained = worker.drain(&mut connection, now, 2).unwrap();
    assert_eq!(drained, Drained { ran: 1, next: None });
    assert_eq!(
        drained.wait(now, Duration::from_secs(1)),
        Duration::from_secs(1)
    );
    assert_eq!(seen.all(), [1, 2, 3]);
    assert_eq!(db.count("order_placed_queue", "state = 'done'"), 3);

    // A drain of nothing runs nothing, and a limit of none claims none.
    assert_eq!(
        worker.drain(&mut connection, now, 2).unwrap(),
        Drained { ran: 0, next: None }
    );
    db.enqueue(&def, &an_order(4));
    let idle = worker.drain(&mut connection, now, 0).unwrap();
    assert_eq!(idle.ran, 0);
    assert_eq!(db.count("order_placed_queue", "state = 'ready'"), 1);
}

#[test]
fn a_drain_answers_when_the_next_message_falls_due() {
    let db = Db::open("next");
    let def = order_placed();
    let seen = Seen::default();
    let worker = recording(&seen);
    let mut connection = db.worker();
    let now = SystemTime::now();

    // A ready message's due time, to the millisecond its row holds.
    db.enqueue(&def, &an_order(1));
    db.exec("UPDATE order_placed_queue SET due_at = '2099-01-01T00:00:00.250Z'");
    let drained = worker.drain(&mut connection, now, 10).unwrap();
    let due = UNIX_EPOCH + Duration::from_millis(4_070_908_800_250);
    assert_eq!(
        drained,
        Drained {
            ran: 0,
            next: Some(due)
        }
    );
    // A host sleeps until then, and its poll at most.
    assert_eq!(
        drained.wait(now, Duration::from_secs(1)),
        Duration::from_secs(1)
    );
    assert_eq!(
        drained.wait(due - Duration::from_millis(300), Duration::from_secs(1)),
        Duration::from_millis(300)
    );

    // The earliest of the worker's queues: a claimed message's expiry.
    db.exec("UPDATE order_placed_queue SET state = 'claimed', claim_token = 'another-worker', claim_expires_at = '2098-06-01T00:00:00.000Z'");
    let drained = worker.drain(&mut connection, now, 10).unwrap();
    let expiry = seconds_from(due, drained.next.unwrap());
    assert!(
        (-18_500_000.0..=-18_400_000.0).contains(&expiry),
        "the next is the claim's expiry, seven months before: {expiry}"
    );

    // A message due by the host's time that the database's clock has not
    // reached is not claimed, and the host is sent to its next poll.
    db.exec("UPDATE order_placed_queue SET state = 'ready', claim_token = NULL, claim_expires_at = NULL, due_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+30 minutes')");
    let ahead = now + Duration::from_secs(3600);
    assert_eq!(
        worker.drain(&mut connection, ahead, 10).unwrap(),
        Drained {
            ran: 0,
            next: Some(ahead + Duration::from_secs(1))
        }
    );
    assert_eq!(seen.all(), [] as [u64; 0]);
}

#[test]
fn a_handler_that_fails_or_panics_fails_its_message_alone() {
    let db = Db::open("fails");
    let def = order_placed();
    let worker = SqliteWorker::new()
        .queue(def.clone(), |message: Value| -> Result<(), BoxError> {
            match message["quantity"].as_u64() {
                Some(1) => Err("the warehouse is closed".into()),
                Some(2) => panic!("no such shelf"),
                _ => Ok(()),
            }
        })
        .unwrap();
    let mut connection = db.worker();
    for n in 1..=3 {
        db.enqueue(&def, &an_order(n));
    }
    let now = SystemTime::now();
    let drained = worker.drain(&mut connection, now, 10).unwrap();
    assert_eq!(drained.ran, 3, "a failed message is run, and counted");
    assert_eq!(db.count("order_placed_queue", "state = 'done'"), 1);
    assert_eq!(
        db.count(
            "order_placed_queue",
            "state = 'ready' AND attempts = 1 AND last_error = 'the warehouse is closed'"
        ),
        1
    );
    assert_eq!(
        db.count(
            "order_placed_queue",
            "state = 'ready' AND attempts = 1 AND last_error = 'panic: no such shelf'"
        ),
        1
    );
    // The failed ones are due again after the queue's backoff, a minute.
    let backoff = seconds_from(now, drained.next.unwrap());
    assert!(
        (55.0..=65.0).contains(&backoff),
        "next is due in {backoff} s"
    );
}

/// Spends a claimed message as the rules adapter spends one: deleted by its
/// id and its claim's token.
fn spend(connection: &Connection, claimed: &Claimed<Value>) -> rusqlite::Result<usize> {
    connection.execute(
        "DELETE FROM order_placed_queue WHERE id = ?1 AND claim_token = ?2 AND state = 'claimed'",
        [&claimed.id, &claimed.token],
    )
}

#[test]
fn a_message_is_spent_in_the_unit_that_applies_its_writes() {
    let db = Db::open("unit");
    let def = order_placed();
    let times = Arc::new(Mutex::new(Vec::new()));
    let given = Arc::clone(&times);
    let worker = SqliteWorker::new()
        .queue_in_unit(
            def.clone(),
            move |unit: &Unit<'_>, claimed: Claimed<Value>, now: SystemTime| {
                given.lock().unwrap().push(now);
                let quantity = claimed.message["quantity"].as_i64().unwrap();
                unit.with(|connection| -> Result<(), BoxError> {
                    // The unit holds the write lock already.
                    assert!(!connection.is_autocommit());
                    connection.execute("INSERT INTO applied (quantity) VALUES (?1)", [quantity])?;
                    if quantity == 2 {
                        return Err("the order is on hold".into());
                    }
                    assert_eq!(spend(connection, &claimed)?, 1);
                    Ok(())
                })
            },
        )
        .unwrap();
    let mut connection = db.worker();

    // The handler's write and its message's spending commit together.
    db.enqueue(&def, &an_order(1));
    let now = SystemTime::now();
    assert_eq!(
        worker.drain(&mut connection, now, 10).unwrap(),
        Drained { ran: 1, next: None }
    );
    assert_eq!(db.count("applied", "quantity = 1"), 1);
    assert_eq!(db.count("order_placed_queue", "1"), 0);
    assert_eq!(*times.lock().unwrap(), [now]);
    assert!(connection.is_autocommit());

    // A handler that fails writes nothing, and its message is failed after
    // the unit rolled back, to come again.
    db.enqueue(&def, &an_order(2));
    assert_eq!(worker.drain(&mut connection, now, 10).unwrap().ran, 1);
    assert_eq!(db.count("applied", "quantity = 2"), 0);
    assert_eq!(
        db.row(),
        ("ready".into(), 1, Some("the order is on hold".into()))
    );
    assert!(connection.is_autocommit());
}

#[test]
fn a_worker_runs_its_queues_in_turn_and_counts_what_a_handler_caused() {
    let db = Db::open("turn");
    let (orders, pings) = (order_placed(), ping());
    let order = Arc::new(Mutex::new(Vec::new()));
    let (first, second) = (Arc::clone(&order), Arc::clone(&order));
    let enqueued = orders.clone();
    let worker = SqliteWorker::new()
        .queue_in_unit(
            pings.clone(),
            move |unit: &Unit<'_>, claimed: Claimed<Value>, _now: SystemTime| {
                first.lock().unwrap().push("ping");
                unit.with(|connection| -> Result<(), BoxError> {
                    // A ping causes an order, which commits with its
                    // spending and is run in the same drain.
                    enqueue_sqlite(connection, &enqueued, &an_order(7))?;
                    connection.execute(
                        "DELETE FROM ping_queue WHERE id = ?1 AND claim_token = ?2",
                        [&claimed.id, &claimed.token],
                    )?;
                    Ok(())
                })
            },
        )
        .unwrap()
        .queue(orders, move |_message: Value| {
            second.lock().unwrap().push("order");
            Ok(())
        })
        .unwrap();
    assert_eq!(worker.name(), "Ping, OrderPlaced");
    db.enqueue(&pings, &json!({}));
    let mut connection = db.worker();
    let now = SystemTime::now();
    assert_eq!(
        worker.drain(&mut connection, now, 10).unwrap(),
        Drained { ran: 2, next: None }
    );
    assert_eq!(*order.lock().unwrap(), ["ping", "order"]);

    // The limit is the worker's, over its queues: the order a second ping
    // causes is due by the host's time, so the host is sent straight back.
    db.enqueue(&pings, &json!({}));
    let later = SystemTime::now() + Duration::from_secs(5);
    assert_eq!(
        worker.drain(&mut connection, later, 1).unwrap(),
        Drained {
            ran: 1,
            next: Some(later)
        }
    );
    assert_eq!(worker.drain(&mut connection, later, 1).unwrap().ran, 1);
}

#[test]
fn a_drain_prunes_as_it_starts_and_every_five_minutes() {
    let db = Db::open("prune");
    let def = order_placed();
    let seen = Seen::default();
    let worker = recording(&seen);
    let mut connection = db.worker();
    let old = "UPDATE order_placed_queue SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-2 hours') WHERE state = 'done'";
    let now = SystemTime::now();

    // Its first drain prunes: of the done messages, those done past the
    // queue's retention, an hour, go, and one done a minute ago stays.
    for n in 1..=3 {
        db.enqueue(&def, &an_order(n));
    }
    assert_eq!(worker.drain(&mut connection, now, 10).unwrap().ran, 3);
    db.exec(old);
    db.exec("UPDATE order_placed_queue SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 minutes') WHERE quantity = 3");
    // The worker pruned as that drain began, before any was done.
    assert_eq!(db.count("order_placed_queue", "1"), 3);
    let fresh = recording(&seen);
    fresh.drain(&mut connection, now, 10).unwrap();
    assert_eq!(db.count("order_placed_queue", "1"), 1);

    // Under five minutes on, by the time it is given, it prunes nothing.
    db.exec(old);
    fresh
        .drain(&mut connection, now + Duration::from_secs(299), 10)
        .unwrap();
    assert_eq!(db.count("order_placed_queue", "1"), 1);
    // Five minutes on it prunes again, and never a dead message.
    db.enqueue(&def, &an_order(4));
    db.exec("UPDATE order_placed_queue SET state = 'dead', updated_at = '2000-01-01T00:00:00.000Z' WHERE quantity = 4");
    fresh
        .drain(&mut connection, now + Duration::from_secs(300), 10)
        .unwrap();
    assert_eq!(db.count("order_placed_queue", "state = 'done'"), 0);
    assert_eq!(db.count("order_placed_queue", "state = 'dead'"), 1);
}

#[test]
fn a_busy_file_fails_the_drain_and_an_idle_drain_only_reads() {
    let db = Db::open("busy");
    let def = order_placed();
    let seen = Seen::default();
    let worker = recording(&seen);
    let mut connection = db.worker();
    connection.busy_timeout(Duration::from_millis(50)).unwrap();
    let now = SystemTime::now();
    // The first drain's prune is done with before the file is held.
    worker.drain(&mut connection, now, 10).unwrap();

    // Another connection holds the write lock: a drain of nothing only
    // reads, beside it.
    let holder = db.connection(Duration::from_millis(50));
    holder.execute_batch("BEGIN IMMEDIATE").unwrap();
    assert_eq!(
        worker.drain(&mut connection, now, 10).unwrap(),
        Drained { ran: 0, next: None }
    );

    // A due message's claim waits out the busy timeout, and the drain
    // fails: the host drains again later.
    enqueue_sqlite(&holder, &def, &an_order(1)).unwrap();
    holder.execute_batch("COMMIT; BEGIN IMMEDIATE").unwrap();
    let err = worker.drain(&mut connection, now, 10).unwrap_err();
    assert!(
        matches!(&err, QueueError::Failed(why) if why.contains("claim on order_placed_queue") && why.contains("locked")),
        "{err}"
    );
    assert_eq!(seen.all(), [] as [u64; 0]);
    holder.execute_batch("COMMIT").unwrap();
    assert_eq!(worker.drain(&mut connection, now, 10).unwrap().ran, 1);
    assert_eq!(seen.all(), [1]);
}

#[test]
fn a_descriptor_without_statements_on_sqlite_is_refused() {
    let mut def = order_placed();
    def.sqlite_statements = None;
    let err = SqliteWorker::new()
        .queue(def.clone(), |_message: Value| Ok(()))
        .unwrap_err();
    assert!(
        matches!(&err, QueueError::Definition(why) if why.contains("queue OrderPlaced carries no statements on SQLite")),
        "{err}"
    );
    assert!(SqliteWorker::new()
        .queue_in_unit(def, |_: &Unit<'_>, _: Claimed<Value>, _: SystemTime| Ok(()))
        .is_err());
}

/// A host with no async runtime: a thread of its own that drains, then
/// sleeps until the next message falls due, a long poll at most, or until a
/// commit of its process to the file wakes it.
#[test]
fn a_host_with_no_runtime_sleeps_until_a_commit_of_its_own_wakes_it() {
    let db = Db::open("host");
    let def = order_placed();
    let file = orm::sqlite::Database::open(db.path()).unwrap();
    let wake = Arc::clone(file.wake());
    let seen = Seen::default();
    let worker = recording(&seen);
    let stop = Arc::new(AtomicBool::new(false));
    let drains = Arc::new(AtomicUsize::new(0));
    let poll = Duration::from_secs(60);

    let host = {
        let (wake, stop, drains) = (Arc::clone(&wake), Arc::clone(&stop), Arc::clone(&drains));
        let mut connection = db.worker();
        std::thread::spawn(move || {
            assert!(tokio::runtime::Handle::try_current().is_err());
            while !stop.load(Ordering::SeqCst) {
                let woken = wake.seen();
                let now = SystemTime::now();
                let drained = worker.drain(&mut connection, now, 10).unwrap();
                drains.fetch_add(1, Ordering::SeqCst);
                wake.wait(woken, drained.wait(now, poll));
            }
        })
    };

    let until = |what: &str, done: &dyn Fn() -> bool| {
        let started = Instant::now();
        while !done() {
            assert!(
                started.elapsed() < Duration::from_secs(10),
                "{what} within ten seconds"
            );
            std::thread::sleep(Duration::from_millis(5));
        }
    };
    until("the host's first drain", &|| {
        drains.load(Ordering::SeqCst) == 1
    });
    // The host sleeps for its poll, a minute: a write the file commits
    // wakes it, and it runs the message at once.
    file.write(|connection| -> Result<(), orm::Error> {
        enqueue_sqlite(connection, &def, &an_order(5))
            .map_err(|err| orm::Error::Database(err.to_string()))
    })
    .unwrap();
    until("the message a commit woke the host for", &|| {
        seen.all() == [5]
    });
    // A write that rolls back wakes no one.
    let woken = wake.seen();
    let rolled: Result<(), orm::Error> =
        file.write(|_connection| Err(orm::Error::Database("no".into())));
    assert!(rolled.is_err());
    assert_eq!(wake.seen(), woken);
    assert!(!wake.wait(woken, Duration::from_millis(20)));

    stop.store(true, Ordering::SeqCst);
    wake.notify();
    host.join().unwrap();
}

/// What a `@worker`'s method is to a drain: an async function, driven where
/// the drain runs.
async fn handle(seen: Seen, message: Value) -> Result<(), BoxError> {
    tokio::time::sleep(Duration::from_millis(5)).await;
    seen.push(&message);
    Ok(())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_loop_on_tokio_drains_until_it_is_stopped() {
    let db = Db::open("loop");
    let def = order_placed();
    let file = Arc::new(orm::sqlite::Database::open(db.path()).unwrap());
    let seen = Seen::default();
    let handled = seen.clone();
    let worker = SqliteWorker::new()
        .queue(def.clone(), move |message: Value| {
            block_on(handle(handled.clone(), message))
        })
        .unwrap();
    for n in 1..=6 {
        db.enqueue(&def, &an_order(n));
    }
    let shutdown = Shutdown::new();
    let options = DrainOptions {
        limit: 2,
        grace: Duration::from_secs(5),
        // A poll far longer than the test: only the due times and the
        // file's commits bring the loop back.
        poll: Duration::from_secs(60),
    };
    let running = tokio::spawn(run_drain(
        Arc::new(worker),
        vec![db.worker(), db.worker()],
        Some(Arc::clone(file.wake())),
        options,
        shutdown.clone(),
    ));

    let until = |what: &'static str, want: usize| {
        let seen = seen.clone();
        async move {
            let started = Instant::now();
            while seen.all().len() < want {
                assert!(
                    started.elapsed() < Duration::from_secs(10),
                    "{what} within ten seconds: {:?}",
                    seen.all()
                );
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
        }
    };
    until("the six messages due as it started", 6).await;
    let mut all = seen.all();
    all.sort_unstable();
    assert_eq!(all, [1, 2, 3, 4, 5, 6], "each message once");

    // It sleeps for its poll now: a commit of the file wakes it.
    let enqueued = def.clone();
    file.write_async(move |connection| -> Result<(), orm::Error> {
        enqueue_sqlite(connection, &enqueued, &an_order(7))
            .map_err(|err| orm::Error::Database(err.to_string()))
    })
    .await
    .unwrap();
    until("the message a commit woke it for", 7).await;

    shutdown.cancel();
    tokio::time::timeout(Duration::from_secs(5), running)
        .await
        .expect("the loop stops when its shutdown is cancelled")
        .unwrap()
        .unwrap();
    assert_eq!(db.count("order_placed_queue", "state = 'done'"), 7);

    // A worker with no connection cannot run.
    let none = run_drain(
        Arc::new(recording(&seen)),
        Vec::new(),
        None,
        DrainOptions::default(),
        Shutdown::new(),
    )
    .await
    .unwrap_err();
    assert!(
        matches!(&none, QueueError::Definition(why) if why.contains("no connection to drain OrderPlaced on")),
        "{none}"
    );
}

#[test]
fn a_wake_counts_commits_and_wakes_its_waiters() {
    let wake = Arc::new(Wake::new());
    let seen = wake.seen();
    assert!(!wake.wait(seen, Duration::from_millis(10)));
    wake.notify();
    // A commit made before the wait is not missed.
    assert!(wake.wait(seen, Duration::from_secs(60)));
    block_on(wake.woken(seen));

    let seen = wake.seen();
    let waiter = {
        let wake = Arc::clone(&wake);
        std::thread::spawn(move || block_on(wake.woken(seen)))
    };
    std::thread::sleep(Duration::from_millis(20));
    wake.notify();
    waiter.join().unwrap();
}
