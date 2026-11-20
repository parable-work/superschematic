//! The axum binding (the `server` feature, D117): the routes in process
//! through the router, a caller from development's headers or none (401),
//! the event stream's frames from a cursor, its `ready` event and its
//! heartbeat, and the MCP endpoint's `initialize`, `tools/list` and
//! `tools/call` through the HTTP runtime's transport.

#![cfg(feature = "server")]

use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::body::Body;
use futures_util::StreamExt;
use serde_json::{json, Value};
use superschematic_rules::Caller;
use superschematic_rules_dev::server::{dev_callers, router, Options, SUBJECT_HEADER};
use superschematic_rules_dev::Dev;
use superschematic_rules_handler::{Store, StoreWrite, Unit};
use tower::ServiceExt;

fn app() -> (axum::Router, Arc<Mutex<Dev>>) {
    let dev = Arc::new(Mutex::new(
        Dev::memory()
            .expect("SQLite opens")
            .with_clock(|| 1_767_225_600_000),
    ));
    let mut options = Options::new(dev_callers(Some(Caller::new("developer", ["Task"]))));
    options.poll = Duration::from_millis(10);
    options.heartbeat = Duration::from_millis(50);
    (router(Arc::clone(&dev), options), dev)
}

async fn send(
    app: &axum::Router,
    method: &str,
    path: &str,
    headers: &[(&str, &str)],
    body: Option<Value>,
) -> (u16, Value, http::HeaderMap) {
    let mut request = http::Request::builder().method(method).uri(path);
    for (name, value) in headers {
        request = request.header(*name, *value);
    }
    let body = match body {
        Some(value) => {
            request = request.header("content-type", "application/json");
            Body::from(serde_json::to_vec(&value).expect("JSON"))
        }
        None => Body::empty(),
    };
    let response = app
        .clone()
        .oneshot(request.body(body).expect("a request"))
        .await
        .expect("an answer");
    let status = response.status().as_u16();
    let headers = response.headers().clone();
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .expect("the body");
    let value = serde_json::from_slice(&bytes)
        .unwrap_or_else(|_| Value::String(String::from_utf8_lossy(&bytes).into_owned()));
    (status, value, headers)
}

fn task() -> Value {
    json!({"name": "Task",
        "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}],
        "behaviors": [{"name": "Workflow", "field": "status", "config": {
            "states": ["todo", "done"], "transitions": [{"from": "todo", "to": "done"}]}}]})
}

async fn published(app: &axum::Router) {
    let (status, body, _) = send(
        app,
        "POST",
        "/namespaces/default/schemas",
        &[],
        Some(task()),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let (status, body, _) = send(
        app,
        "POST",
        "/namespaces/default/schemas/Task/publish",
        &[],
        None,
    )
    .await;
    assert_eq!(status, 200, "{body}");
}

#[tokio::test(flavor = "multi_thread")]
async fn the_routes_serve_in_process_as_the_callers_headers_say() {
    let (app, _) = app();
    published(&app).await;
    let (status, created, headers) = send(
        &app,
        "POST",
        "/namespaces/default/schemas/Task/instances",
        &[(SUBJECT_HEADER, "alice"), ("x-request-id", "r-7")],
        Some(json!({"id": "t1", "data": {"title": "Desk"}})),
    )
    .await;
    assert_eq!(status, 201, "{created}");
    assert_eq!(created["meta"]["requestId"], "r-7");
    assert_eq!(
        headers.get("etag").and_then(|v| v.to_str().ok()),
        Some("\"1\"")
    );
    let (status, got, _) = send(
        &app,
        "GET",
        "/namespaces/default/schemas/Task/instances/t1",
        &[],
        None,
    )
    .await;
    assert_eq!(status, 200);
    assert_eq!(got["data"]["status"], "todo");
    let (status, missing, _) = send(&app, "GET", "/nowhere", &[], None).await;
    assert_eq!((status, missing["code"].as_str()), (404, Some("not_found")));

    // With no default caller, a request that names none is 401.
    let dev = Arc::new(Mutex::new(Dev::memory().expect("SQLite opens")));
    let closed = router(dev, Options::new(dev_callers(None)));
    let (status, refused, _) = send(&closed, "GET", "/namespaces/default/schemas", &[], None).await;
    assert_eq!(
        (status, refused["code"].as_str()),
        (401, Some("unauthorized"))
    );
}

fn event(dev: &Arc<Mutex<Dev>>, id: &str) {
    let mut dev = dev.lock().expect("the host");
    let store = dev.store();
    store.begin().expect("begun");
    store
        .apply(&Unit {
            writes: vec![StoreWrite::Event {
                type_name: "Task".into(),
                id: id.into(),
                use_id: "Schedules".into(),
                kind: "run".into(),
                at: 1_767_225_600_000,
                payload: json!({}),
            }],
        })
        .expect("the event applies");
    store.commit().expect("committed");
}

async fn next(frames: &mut axum::body::BodyDataStream) -> String {
    let frame = tokio::time::timeout(Duration::from_secs(5), frames.next())
        .await
        .expect("a frame in time")
        .expect("a frame")
        .expect("its bytes");
    String::from_utf8(frame.to_vec()).expect("text")
}

#[tokio::test(flavor = "multi_thread")]
async fn the_event_stream_sends_from_its_cursor_then_ready_then_what_comes() {
    let (app, dev) = app();
    published(&app).await;
    event(&dev, "t1");
    event(&dev, "t2");
    let request = http::Request::builder()
        .uri("/namespaces/default/events?after=1")
        .header("accept", "text/event-stream")
        .body(Body::empty())
        .expect("a request");
    let response = app.clone().oneshot(request).await.expect("an answer");
    assert_eq!(response.status(), 200);
    assert_eq!(
        response
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok()),
        Some("text/event-stream")
    );
    let mut frames = response.into_body().into_data_stream();
    let first = next(&mut frames).await;
    assert!(first.starts_with("id: 2\ndata: {"), "{first}");
    assert!(first.contains("\"instanceId\":\"t2\""), "{first}");
    assert_eq!(
        next(&mut frames).await,
        "id: 2\nevent: ready\ndata: {\"cursor\":2}\n\n"
    );
    // What is written while it waits reaches it, and while nothing comes
    // it beats.
    event(&dev, "t3");
    let third = next(&mut frames).await;
    assert!(third.starts_with("id: 3\n"), "{third}");
    assert_eq!(next(&mut frames).await, ": heartbeat\n\n");
}

#[tokio::test(flavor = "multi_thread")]
async fn the_mcp_endpoint_lists_and_calls_the_callers_tools() {
    let (app, _) = app();
    published(&app).await;
    let rpc = |id: i64, method: &str, params: Value| json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params});
    let accept = [("accept", "application/json, text/event-stream")];
    let (status, initialized, _) = send(
        &app,
        "POST",
        "/namespaces/default/mcp",
        &accept,
        Some(rpc(
            1,
            "initialize",
            json!({"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test", "version": "1"}}),
        )),
    )
    .await;
    assert_eq!(status, 200);
    let text = initialized.as_str().unwrap_or_default().to_owned();
    assert!(text.contains("superschematic-dev"), "{text}");
    let message = |text: &str| -> Value {
        let data = text
            .lines()
            .find_map(|line| line.strip_prefix("data: "))
            .expect("a message");
        serde_json::from_str(data).expect("JSON-RPC")
    };
    let (_, listed, _) = send(
        &app,
        "POST",
        "/namespaces/default/mcp",
        &accept,
        Some(rpc(2, "tools/list", json!({}))),
    )
    .await;
    let listed = message(listed.as_str().unwrap_or_default());
    let names: Vec<&str> = listed["result"]["tools"]
        .as_array()
        .expect("tools")
        .iter()
        .filter_map(|tool| tool["name"].as_str())
        .collect();
    assert!(
        names.contains(&"task_create") && names.contains(&"task_transition"),
        "{names:?}"
    );
    let (_, called, _) = send(
        &app,
        "POST",
        "/namespaces/default/mcp",
        &accept,
        Some(rpc(
            3,
            "tools/call",
            json!({"name": "task_create", "arguments": {"data": {"id": "t1", "title": "Desk"}}}),
        )),
    )
    .await;
    let called = message(called.as_str().unwrap_or_default());
    assert_eq!(
        called["result"]["structuredContent"]["status"], "todo",
        "{called}"
    );
    let (_, refused, _) = send(
        &app,
        "POST",
        "/namespaces/default/mcp",
        &accept,
        Some(rpc(
            4,
            "tools/call",
            json!({"name": "task_create", "arguments": {"data": {"id": "t1", "title": "Again"}}}),
        )),
    )
    .await;
    let refused = message(refused.as_str().unwrap_or_default());
    assert_eq!(refused["result"]["isError"], true, "{refused}");
    assert_eq!(refused["result"]["structuredContent"]["code"], "conflict");
}
