//! Idempotency keys (D140) held to `runtime/http/testdata/idempotency_parity.json`,
//! as the Go runtime's `idempotency` package and the TypeScript runtime's
//! `idempotency.ts` are: `is_key`, `lookup` and `request_hash` answer each
//! vector, and each store each scenario: the memory store always, the
//! SQLite store with the `sqlite` feature, and the Postgres store with the
//! `postgres` feature on the database `SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL`
//! names, which it skips without.

use std::path::Path;
use std::sync::Arc;

use serde_json::Value;
use superschematic_http_runtime::idempotency::{
    is_key, lookup, request_hash, Attempt, Begin, Columns, MemoryStore, Store, Table,
};

const VECTORS: &str = "../testdata/idempotency_parity.json";

/// The reserved table as a DB schema's naming writes it.
#[cfg_attr(not(any(feature = "sqlite", feature = "postgres")), allow(dead_code))]
const NAMES: Table = Table {
    table: "idempotency_key",
    columns: Columns {
        lookup: "lookup",
        operation: "operation",
        principal: "principal",
        key: "key",
        request_hash: "request_hash",
        state: "state",
        status: "status",
        body: "body",
        started_at: "started_at",
        expires_at: "expires_at",
    },
};

fn vectors() -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&text).unwrap()
}

/// The reserved table as the SQL generator writes it for a dialect: the
/// times and the status are numbers.
#[cfg_attr(not(any(feature = "sqlite", feature = "postgres")), allow(dead_code))]
fn ddl(table: &str, number: &str) -> String {
    format!(
        r#"CREATE TABLE "{table}" (
  "lookup" TEXT NOT NULL UNIQUE, "operation" TEXT NOT NULL, "principal" TEXT NOT NULL, "key" TEXT NOT NULL,
  "request_hash" TEXT NOT NULL, "state" TEXT NOT NULL, "status" {number}, "body" TEXT,
  "started_at" {number} NOT NULL, "expires_at" {number} NOT NULL)"#
    )
}

fn text(value: &Value, name: &str) -> String {
    value[name].as_str().unwrap_or_default().to_string()
}

fn int(value: &Value, name: &str) -> i64 {
    value[name].as_i64().unwrap_or_default()
}

/// Runs each scenario on a store `open` makes empty for it.
async fn scenarios<F, Fut>(dialect: &str, open: F)
where
    F: Fn() -> Fut,
    Fut: std::future::Future<Output = Arc<dyn Store>>,
{
    for scenario in vectors()["scenarios"].as_array().unwrap() {
        let name = format!("{dialect}/{}", text(scenario, "name"));
        let store = open().await;
        for (i, step) in scenario["steps"].as_array().unwrap().iter().enumerate() {
            let want = &step["want"];
            match step["call"].as_str().unwrap() {
                "begin" => {
                    let at = int(step, "at");
                    let got = store
                        .begin(&Attempt {
                            lookup: text(step, "lookup"),
                            operation: "orders.createOrder".into(),
                            principal: "user-1".into(),
                            key: "k".into(),
                            request_hash: text(step, "requestHash"),
                            now: at,
                            expires_at: at + int(step, "ttlMs"),
                            take_over_before: at - int(step, "inFlightMs"),
                        })
                        .await
                        .unwrap_or_else(|err| panic!("{name}: step {i}: begin: {err}"));
                    let wanted = match want["kind"].as_str().unwrap() {
                        "started" => Begin::Started,
                        "replay" => Begin::Replay {
                            status: u16::try_from(int(want, "status")).unwrap(),
                            body: text(want, "body"),
                        },
                        "mismatch" => Begin::Mismatch,
                        "running" => Begin::Running {
                            started_at: int(want, "startedAt"),
                        },
                        kind => panic!("{name}: step {i}: unknown kind {kind}"),
                    };
                    assert_eq!(got, wanted, "{name}: step {i}");
                }
                "finish" => store
                    .finish(
                        &text(step, "lookup"),
                        int(step, "startedAt"),
                        u16::try_from(int(step, "status")).unwrap(),
                        &text(step, "body"),
                    )
                    .await
                    .unwrap_or_else(|err| panic!("{name}: step {i}: finish: {err}")),
                "abandon" => store
                    .abandon(&text(step, "lookup"), int(step, "startedAt"))
                    .await
                    .unwrap_or_else(|err| panic!("{name}: step {i}: abandon: {err}")),
                "sweep" => {
                    let limit = usize::try_from(int(step, "limit")).unwrap();
                    let swept = store
                        .sweep(int(step, "at"), limit)
                        .await
                        .unwrap_or_else(|err| panic!("{name}: step {i}: sweep: {err}"));
                    assert_eq!(swept, want.as_u64().unwrap(), "{name}: step {i}");
                }
                call => panic!("{name}: step {i}: unknown call {call}"),
            }
        }
    }
}

#[test]
fn keys_lookups_and_hashes_answer_each_vector() {
    let vectors = vectors();
    for key in vectors["keys"]["valid"].as_array().unwrap() {
        assert!(is_key(key.as_str().unwrap()), "{key}");
    }
    for key in vectors["keys"]["invalid"].as_array().unwrap() {
        assert!(!is_key(key.as_str().unwrap()), "{key}");
    }
    for vector in vectors["lookups"].as_array().unwrap() {
        assert_eq!(
            lookup(
                &text(vector, "operation"),
                &text(vector, "principal"),
                &text(vector, "key")
            ),
            text(vector, "lookup"),
            "{}",
            text(vector, "name")
        );
    }
    for vector in vectors["hashes"].as_array().unwrap() {
        assert_eq!(
            request_hash(
                &text(vector, "method"),
                &text(vector, "path"),
                text(vector, "body").as_bytes()
            ),
            text(vector, "hash"),
            "{}",
            text(vector, "name")
        );
    }
}

#[tokio::test]
async fn the_memory_store_answers_each_scenario() {
    scenarios("memory", || async {
        Arc::new(MemoryStore::new()) as Arc<dyn Store>
    })
    .await;
}

#[cfg(feature = "sqlite")]
#[tokio::test(flavor = "multi_thread")]
async fn the_sqlite_store_answers_each_scenario() {
    use std::sync::atomic::{AtomicUsize, Ordering};
    use superschematic_http_runtime::idempotency::SqliteStore;
    use superschematic_http_runtime::orm::sqlite::{rusqlite, Database};

    let dir = std::env::temp_dir().join(format!("idempotency-{}", uuid::Uuid::new_v4().simple()));
    std::fs::create_dir_all(&dir).unwrap();
    let files = AtomicUsize::new(0);
    scenarios("sqlite", || {
        let path = dir.join(format!("keys-{}.db", files.fetch_add(1, Ordering::SeqCst)));
        async move {
            rusqlite::Connection::open(&path)
                .and_then(|connection| connection.execute_batch(&ddl(NAMES.table, "REAL")))
                .expect("create the file");
            let db = Arc::new(Database::open(&path).unwrap());
            Arc::new(SqliteStore::new(db, &NAMES)) as Arc<dyn Store>
        }
    })
    .await;
    let _ = std::fs::remove_dir_all(&dir);
}

#[cfg(feature = "postgres")]
#[tokio::test(flavor = "multi_thread")]
async fn the_postgres_store_answers_each_scenario() {
    use std::sync::atomic::{AtomicUsize, Ordering};
    use superschematic_http_runtime::idempotency::PostgresStore;
    use superschematic_http_runtime::postgres::connect;
    use superschematic_http_runtime::stackconfig::Database;

    const URL_ENV: &str = "SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL";
    let Some(url) = std::env::var(URL_ENV).ok().filter(|url| !url.is_empty()) else {
        eprintln!("skipped: {URL_ENV} is unset");
        return;
    };
    let pool = connect(&Database::Url(url)).unwrap();
    let prefix = format!("idempotency_key_rust_{}", uuid::Uuid::new_v4().simple());
    let tables = AtomicUsize::new(0);
    let made = std::sync::Mutex::new(Vec::new());
    scenarios("postgres", || {
        let table = format!("{prefix}_{}", tables.fetch_add(1, Ordering::SeqCst));
        made.lock().unwrap().push(table.clone());
        let pool = pool.clone();
        async move {
            pool.get()
                .await
                .unwrap()
                .batch_execute(&ddl(&table, "DOUBLE PRECISION"))
                .await
                .unwrap();
            let names = Table {
                table: Box::leak(table.into_boxed_str()),
                ..NAMES
            };
            Arc::new(PostgresStore::new(pool, &names)) as Arc<dyn Store>
        }
    })
    .await;
    let client = pool.get().await.unwrap();
    for table in made.into_inner().unwrap() {
        client
            .batch_execute(&format!("DROP TABLE \"{table}\""))
            .await
            .unwrap();
    }
}
