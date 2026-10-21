//! The queue client on SQLite (D53, amended), over a file of the test's
//! own holding the tables of `testdata/queue/create_sqlite.sql`, the SQLite
//! model's DDL of the fixture-queue-db schema, with the descriptors of
//! `order_placed.json` and `ping.json`, which carry the queues' statements
//! on SQLite as a Rust types crate's `queues.rs` holds them. Each test opens
//! the file twice, as a worker and the server that enqueues would: two
//! connections, each waiting for the other's write lock. The worker loop
//! runs over the same queue at the end.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use rusqlite::Connection;
use serde_json::{json, Value};
use superschematic_http_runtime::queue::{
    enqueue_sqlite, QueueDefinition, QueueError, SqliteQueue,
};
use superschematic_http_runtime::worker::{run_worker, Options};
use superschematic_http_runtime::{BoxError, Shutdown};

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

/// A SQLite file of the test's own, holding the fixture's tables, removed
/// when dropped.
struct Db {
    dir: PathBuf,
    path: PathBuf,
}

impl Db {
    fn open(name: &str) -> Db {
        let dir = std::env::temp_dir().join(format!(
            "queue-sqlite-{name}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("queue.sqlite");
        let connection = Connection::open(&path).unwrap();
        connection
            .execute_batch(&testdata("create_sqlite.sql"))
            .unwrap();
        Db { dir, path }
    }

    fn connection(&self) -> Connection {
        let connection = Connection::open(&self.path).unwrap();
        connection.busy_timeout(Duration::from_secs(5)).unwrap();
        connection
    }

    fn queue(&self, definition: QueueDefinition) -> SqliteQueue<Value> {
        SqliteQueue::open(&self.path, definition).unwrap()
    }

    fn enqueue(&self, definition: &QueueDefinition, message: &Value) {
        enqueue_sqlite(&self.connection(), definition, message).unwrap();
    }

    /// A message's state, its attempts, its last error, and the seconds
    /// until it is due.
    fn row(&self, id: &str) -> (String, i64, Option<String>, f64) {
        self.connection()
            .query_row(
                "SELECT state, attempts, last_error, (julianday(due_at) - julianday('now')) * 86400 FROM order_placed_queue WHERE id = ?1",
                [id],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
            )
            .unwrap()
    }

    fn exec(&self, sql: &str) {
        self.connection().execute_batch(sql).unwrap();
    }

    fn count(&self, table: &str, condition: &str) -> i64 {
        self.connection()
            .query_row(
                &format!("SELECT COUNT(*) FROM {table} WHERE {condition}"),
                [],
                |row| row.get(0),
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

/// An order in Rust's wire form: its UUID in base62 and its timestamp at an
/// offset.
fn an_order(n: u32) -> Value {
    json!({
        "orderId": "3Nf1lmBDcdDKfsC9ccR4DG",
        "placedAt": "2026-10-11T09:15:00.123456+02:00",
        "quantity": n,
        "shipTo": {"line1": "1 Main St", "city": "Springfield"},
        "tags": ["a", "b \"c\"", "x,y"],
    })
}

#[tokio::test]
async fn a_message_commits_with_its_transaction_or_not_at_all() {
    let db = Db::open("tx");
    let def = order_placed();
    let mut connection = db.connection();
    let tx = connection.transaction().unwrap();
    enqueue_sqlite(&tx, &def, &an_order(1)).unwrap();
    tx.rollback().unwrap();
    assert_eq!(db.count("order_placed_queue", "1"), 0);
    let tx = connection.transaction().unwrap();
    enqueue_sqlite(&tx, &def, &an_order(1)).unwrap();
    tx.commit().unwrap();
    let row: (String, String, String, i64, Option<String>, String, String) = db
        .connection()
        .query_row(
            "SELECT order_id, placed_at, priority, quantity, note, ship_to, tags FROM order_placed_queue",
            [],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?, r.get(5)?, r.get(6)?)),
        )
        .unwrap();
    assert_eq!(
        row,
        (
            "6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e".to_string(),
            "2026-10-11T07:15:00.123456Z".to_string(),
            "low".to_string(),
            1,
            None,
            r#"{"city":"Springfield","line1":"1 Main St"}"#.to_string(),
            r#"["a","b \"c\"","x,y"]"#.to_string(),
        )
    );
}

#[tokio::test]
async fn two_connections_claim_different_messages_each_read_as_written() {
    let db = Db::open("claim");
    let def = order_placed();
    db.enqueue(&def, &an_order(1));
    db.enqueue(&def, &an_order(2));
    let (one, two) = (db.queue(def.clone()), db.queue(def));
    let first = one.claim(1, Duration::from_secs(60)).await.unwrap();
    let second = two.claim(5, Duration::from_secs(60)).await.unwrap();
    assert_eq!((first.len(), second.len()), (1, 1));
    assert_ne!(first[0].id, second[0].id);
    assert_eq!(first[0].attempt, 1);
    let mut message = first[0].message.clone();
    message["quantity"] = json!(0);
    assert_eq!(
        message,
        json!({
            "orderId": "6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e",
            "placedAt": "2026-10-11T07:15:00.123456Z",
            "priority": "low",
            "quantity": 0,
            "shipTo": {"line1": "1 Main St", "city": "Springfield"},
            "tags": ["a", "b \"c\"", "x,y"],
        })
    );
    assert!(one
        .claim(5, Duration::from_secs(60))
        .await
        .unwrap()
        .is_empty());
}

#[tokio::test]
async fn a_failure_waits_longer_each_time_until_the_message_is_dead() {
    let db = Db::open("backoff");
    let def = order_placed();
    db.enqueue(&def, &an_order(1));
    let queue = db.queue(def);
    for (attempt, wait) in [(1, 60.0), (2, 120.0), (3, 200.0)] {
        let claims = queue.claim(1, Duration::from_secs(60)).await.unwrap();
        assert_eq!(claims[0].attempt, attempt);
        let id = &claims[0].id;
        assert!(!queue.fail(id, &claims[0].token, "no stock").await.unwrap());
        let (state, attempts, error, due_in) = db.row(id);
        assert_eq!(
            (state.as_str(), attempts, error.as_deref()),
            ("ready", i64::from(attempt), Some("no stock"))
        );
        assert!(
            due_in > wait - 5.0 && due_in <= wait + 0.01,
            "due in {due_in}, want about {wait}"
        );
        assert!(queue
            .claim(1, Duration::from_secs(60))
            .await
            .unwrap()
            .is_empty());
        let lost = queue.complete(id, &claims[0].token).await.unwrap_err();
        assert!(lost.is_claim_lost(), "{lost}");
        db.exec("UPDATE order_placed_queue SET due_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 seconds')");
    }
    let last = queue.claim(1, Duration::from_secs(60)).await.unwrap();
    assert!(queue
        .fail(&last[0].id, &last[0].token, "gone")
        .await
        .unwrap());
    assert_eq!(db.row(&last[0].id).0, "dead");
}

#[tokio::test]
async fn extend_release_and_an_expired_claim() {
    let db = Db::open("extend");
    let def = order_placed();
    db.enqueue(&def, &an_order(1));
    let queue = db.queue(def);
    let claim = queue
        .claim(1, Duration::from_secs(10))
        .await
        .unwrap()
        .remove(0);
    queue
        .extend(&claim.id, &claim.token, Duration::from_secs(600))
        .await
        .unwrap();
    let expires: f64 = db
        .connection()
        .query_row(
            "SELECT (julianday(claim_expires_at) - julianday('now')) * 86400 FROM order_placed_queue",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert!(expires > 590.0, "{expires}");
    queue.release(&claim.id, &claim.token).await.unwrap();
    assert_eq!(db.row(&claim.id).1, 0);
    assert!(matches!(
        queue.release(&claim.id, &claim.token).await,
        Err(QueueError::ClaimLost(_))
    ));
    let again = queue
        .claim(1, Duration::from_secs(60))
        .await
        .unwrap()
        .remove(0);
    db.exec("UPDATE order_placed_queue SET claim_expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 seconds')");
    let taken = queue
        .claim(1, Duration::from_secs(60))
        .await
        .unwrap()
        .remove(0);
    assert_eq!((taken.id.as_str(), taken.attempt), (again.id.as_str(), 2));
    assert_ne!(taken.token, again.token);
    queue.complete(&taken.id, &taken.token).await.unwrap();
    assert_eq!(db.row(&taken.id).0, "done");
}

#[tokio::test]
async fn a_prune_deletes_done_messages_past_the_retention_in_batches() {
    let db = Db::open("prune");
    let def = order_placed();
    let queue = db.queue(def.clone());
    for n in 0..7 {
        db.enqueue(&def, &an_order(n));
    }
    let claims = queue.claim(7, Duration::from_secs(60)).await.unwrap();
    for c in &claims {
        queue.complete(&c.id, &c.token).await.unwrap();
    }
    db.exec("UPDATE order_placed_queue SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-2 hours')");
    db.exec(&format!(
        "UPDATE order_placed_queue SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 minutes') WHERE id = '{}'",
        claims[0].id
    ));
    db.exec(&format!(
        "UPDATE order_placed_queue SET state = 'dead', updated_at = '2000-01-01T00:00:00.000Z' WHERE id = '{}'",
        claims[1].id
    ));
    for want in [3, 2, 0] {
        assert_eq!(queue.prune(3).await.unwrap(), want);
    }
    assert_eq!(db.count("order_placed_queue", "1"), 2);
    assert_eq!(db.count("order_placed_queue", "state = 'dead'"), 1);
}

#[tokio::test]
async fn a_queue_that_keeps_none_deletes_a_message_as_it_completes_it() {
    let db = Db::open("ping");
    let def = ping();
    db.enqueue(&def, &json!({}));
    let queue = db.queue(def);
    let claim = queue
        .claim(1, Duration::from_secs(60))
        .await
        .unwrap()
        .remove(0);
    assert_eq!(claim.message, json!({}));
    queue.complete(&claim.id, &claim.token).await.unwrap();
    assert_eq!(db.count("ping_queue", "1"), 0);
}

#[tokio::test]
async fn a_descriptor_without_statements_on_sqlite_is_refused() {
    let db = Db::open("refused");
    let mut def = order_placed();
    def.sqlite_statements = None;
    let refused = SqliteQueue::<Value>::open(db.path(), def.clone()).unwrap_err();
    assert!(
        refused
            .to_string()
            .contains("carries no statements on SQLite"),
        "{refused}"
    );
    let refused = enqueue_sqlite(&db.connection(), &def, &an_order(1)).unwrap_err();
    assert!(
        refused
            .to_string()
            .contains("carries no statements on SQLite"),
        "{refused}"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn a_worker_handles_every_message_on_sqlite() {
    let db = Db::open("worker");
    let def = order_placed();
    for n in 0..6 {
        db.enqueue(&def, &an_order(n));
    }
    let handled = Arc::new(AtomicUsize::new(0));
    let counting = Arc::clone(&handled);
    let shutdown = Shutdown::new();
    let run = tokio::spawn(run_worker(
        db.queue(def),
        move |message: Value| {
            let counting = Arc::clone(&counting);
            async move {
                if message["quantity"] == json!(3) {
                    return Err::<(), BoxError>("three is out of stock".into());
                }
                counting.fetch_add(1, Ordering::SeqCst);
                Ok(())
            }
        },
        Options {
            concurrency: 2,
            grace: Duration::from_secs(1),
            idle: Duration::from_millis(50),
            ..Options::default()
        },
        shutdown.clone(),
    ));
    for _ in 0..200 {
        if db.count("order_placed_queue", "state = 'done'") == 5
            && db.count("order_placed_queue", "state = 'ready' AND attempts = 1") == 1
        {
            break;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    shutdown.cancel();
    run.await.unwrap().unwrap();
    assert_eq!(handled.load(Ordering::SeqCst), 5);
    assert_eq!(db.count("order_placed_queue", "state = 'done'"), 5);
    assert_eq!(
        db.count(
            "order_placed_queue",
            "state = 'ready' AND last_error = 'three is out of stock'"
        ),
        1
    );
}
