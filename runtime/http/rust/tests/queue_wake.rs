//! The queue client's wake (D145) on a real Postgres, the one
//! `SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL` names (the test skips
//! without it): an enqueue tells the process's waker, which reads
//! `QUEUE_WAKES` once, and sends the queue's wake at most once in five
//! seconds. It is a test binary of its own, since the process's waker is
//! read once per process.

use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::routing::post;
use axum::Router;
use serde_json::json;
use superschematic_http_runtime::postgres::connect;
use superschematic_http_runtime::queue::{enqueue, enqueue_in, QueueDefinition};
use superschematic_http_runtime::stackconfig::Database;
use superschematic_http_runtime::wake;

const URL_ENV: &str = "SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL";

fn testdata(name: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/testdata/queue")
        .join(name);
    std::fs::read_to_string(&path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()))
}

#[tokio::test]
async fn an_enqueue_sends_its_queues_wake_at_most_once_in_five_seconds() {
    let Some(url) = std::env::var(URL_ENV).ok().filter(|url| !url.is_empty()) else {
        eprintln!("skipped: {URL_ENV} is unset");
        return;
    };
    let woken = Arc::new(Mutex::new(Vec::new()));
    let seen = Arc::clone(&woken);
    let app = Router::new().route(
        "/jobs/{job}",
        post(
            move |axum::extract::Path(job): axum::extract::Path<String>| {
                let seen = Arc::clone(&seen);
                async move {
                    seen.lock().unwrap().push(job);
                    "{}"
                }
            },
        ),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    // Set before the process's first enqueue, which reads it.
    std::env::set_var(
        wake::VARIABLE,
        json!({ "Ping": [{ "url": format!("http://{address}/jobs/ping:run") }] }).to_string(),
    );

    let schema = format!("queue_wake_test_{}", uuid::Uuid::new_v4().simple());
    let admin = connect(&Database::Url(url.clone())).unwrap();
    let client = admin.get().await.unwrap();
    client
        .batch_execute(&format!("CREATE SCHEMA {schema}"))
        .await
        .unwrap();
    let ddl: String = testdata("create.sql")
        .lines()
        .filter(|line| !line.starts_with("CREATE EXTENSION"))
        .map(|line| format!("{line}\n"))
        .collect();
    client
        .batch_execute(&format!("SET search_path TO {schema}, public;\n{ddl}"))
        .await
        .unwrap();
    let ping = QueueDefinition::parse(&testdata("ping.json")).unwrap();
    let message = json!({ "sentAt": "2026-11-26T12:00:00Z" });
    enqueue(&client, &ping, &message).await.unwrap();
    enqueue(&client, &ping, &message).await.unwrap();
    enqueue_in(&client, &ping, &message, Duration::from_secs(30))
        .await
        .unwrap();
    wake::settled().await;
    assert_eq!(
        *woken.lock().unwrap(),
        ["ping:run"],
        "three enqueues within five seconds send one wake"
    );
    let held: i64 = client
        .query_one("SELECT count(*) FROM ping_queue", &[])
        .await
        .unwrap()
        .get(0);
    assert_eq!(held, 3, "every enqueue wrote its message");
    client
        .batch_execute(&format!("DROP SCHEMA {schema} CASCADE"))
        .await
        .unwrap();
}
