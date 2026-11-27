//! The route step of an `@idempotent` operation (D140), through
//! `RouteControls::idempotent` over the memory store: a repeat under a key
//! is answered what the first request was and runs nothing, the key with
//! another request is 422, a repeat while the first runs 409 with
//! Retry-After, a 5xx or a panic keeps nothing, a key is the caller's, and
//! a request without one runs each time unless the operation requires one.

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use axum::body::Body;
use axum::extract::Extension;
use axum::http::request::Parts;
use axum::http::{Request, StatusCode};
use axum::routing::post;
use axum::Router;
use serde_json::{json, Value};
use superschematic_http_runtime::idempotency::{
    MemoryStore, Store, CODE_INVALID, CODE_IN_FLIGHT, CODE_REQUIRED, CODE_REUSED, KEY_HEADER,
    REPLAY_HEADER,
};
use superschematic_http_runtime::{
    bearer_token, request_ids, ApiError, Authenticator, Principal, RouteControls,
};
use tower::ServiceExt;

/// Accepts `Bearer <subject>`.
struct Users;

#[async_trait]
impl Authenticator for Users {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(bearer_token(&request.headers)
            .map(|subject| Principal::new(subject, Vec::<String>::new())))
    }
}

/// A route that counts its runs: it answers 201 with the run's number and
/// the body it read, 500 for a body `fail`, panics for `panic`, and takes
/// half a second for `slow`.
fn app(store: Arc<dyn Store>, runs: &Arc<AtomicUsize>, required: bool) -> Router {
    let counted = Arc::clone(runs);
    let handler = move |principal: Option<Extension<Principal>>, body: String| async move {
        let run = counted.fetch_add(1, Ordering::SeqCst) + 1;
        match body.as_str() {
            "fail" => return (StatusCode::INTERNAL_SERVER_ERROR, "{}".to_string()),
            "panic" => panic!("the handler broke"),
            "slow" => tokio::time::sleep(Duration::from_millis(500)).await,
            _ => {}
        }
        let caller = principal.map(|Extension(p)| p.subject).unwrap_or_default();
        (
            StatusCode::CREATED,
            json!({ "data": { "run": run, "body": body, "caller": caller } }).to_string(),
        )
    };
    let route = RouteControls::new()
        .authorize(Arc::new(Users), &[])
        .idempotent(store, "orders.placeOrder", 3600, required)
        .timeout_seconds(30)
        .apply(post(handler));
    Router::new()
        .route("/api/orders", route)
        .layer(axum::middleware::from_fn(request_ids))
}

struct Answer {
    status: StatusCode,
    replay: bool,
    retry_after: Option<String>,
    body: Value,
}

async fn send(app: &Router, user: &str, key: Option<&str>, uri: &str, body: &str) -> Answer {
    let mut request = Request::post(uri).header("authorization", format!("Bearer {user}"));
    if let Some(key) = key {
        request = request.header(KEY_HEADER, key);
    }
    let response = app
        .clone()
        .oneshot(request.body(Body::from(body.to_string())).unwrap())
        .await
        .unwrap();
    let status = response.status();
    let replay = response
        .headers()
        .get(REPLAY_HEADER)
        .is_some_and(|value| value == "true");
    let retry_after = response
        .headers()
        .get("retry-after")
        .map(|value| value.to_str().unwrap().to_string());
    let bytes = axum::body::to_bytes(response.into_body(), usize::MAX)
        .await
        .unwrap();
    Answer {
        status,
        replay,
        retry_after,
        body: serde_json::from_slice(&bytes).unwrap_or(Value::Null),
    }
}

#[tokio::test]
async fn a_repeat_under_a_key_is_answered_the_first_answer_and_runs_nothing() {
    let runs = Arc::new(AtomicUsize::new(0));
    let app = app(Arc::new(MemoryStore::new()), &runs, false);
    let first = send(&app, "ana", Some("order-7"), "/api/orders", "tea").await;
    assert_eq!((first.status, first.replay), (StatusCode::CREATED, false));
    assert_eq!(first.body["data"]["run"], 1);
    let again = send(&app, "ana", Some("order-7"), "/api/orders", "tea").await;
    assert_eq!((again.status, again.replay), (StatusCode::CREATED, true));
    assert_eq!(again.body, first.body);
    assert_eq!(runs.load(Ordering::SeqCst), 1);

    // The key with another body, or another query, is another request.
    for (uri, body) in [
        ("/api/orders", "coffee"),
        ("/api/orders?dryRun=true", "tea"),
    ] {
        let reused = send(&app, "ana", Some("order-7"), uri, body).await;
        assert_eq!(
            reused.status,
            StatusCode::UNPROCESSABLE_ENTITY,
            "{uri} {body}"
        );
        assert_eq!(reused.body["code"], CODE_REUSED);
    }
    // Another caller's same key is another row.
    let ben = send(&app, "ben", Some("order-7"), "/api/orders", "tea").await;
    assert_eq!((ben.status, ben.replay), (StatusCode::CREATED, false));
    assert_eq!(ben.body["data"]["caller"], "ben");
    // Without a key the route runs each time.
    for _ in 0..2 {
        assert!(!send(&app, "ana", None, "/api/orders", "tea").await.replay);
    }
    assert_eq!(runs.load(Ordering::SeqCst), 4);
}

#[tokio::test]
async fn a_key_that_is_no_key_and_a_missing_one_the_operation_requires_are_refused() {
    let runs = Arc::new(AtomicUsize::new(0));
    let app = app(Arc::new(MemoryStore::new()), &runs, true);
    let missing = send(&app, "ana", None, "/api/orders", "tea").await;
    assert_eq!(
        (missing.status, missing.body["code"].as_str()),
        (StatusCode::BAD_REQUEST, Some(CODE_REQUIRED))
    );
    assert_eq!(
        missing.body["detail"],
        "orders.placeOrder needs an Idempotency-Key header"
    );
    let long = "k".repeat(256);
    let invalid = send(&app, "ana", Some(&long), "/api/orders", "tea").await;
    assert_eq!(
        (invalid.status, invalid.body["code"].as_str()),
        (StatusCode::BAD_REQUEST, Some(CODE_INVALID))
    );
    assert_eq!(runs.load(Ordering::SeqCst), 0);
    // A refusal the permission check answers keeps no key.
    let response = app
        .clone()
        .oneshot(
            Request::post("/api/orders")
                .header(KEY_HEADER, "k")
                .body(Body::from("tea"))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    assert!(
        send(&app, "ana", Some("k"), "/api/orders", "tea")
            .await
            .status
            == StatusCode::CREATED
    );
}

#[tokio::test]
async fn a_repeat_while_the_first_runs_is_told_to_retry_after() {
    let runs = Arc::new(AtomicUsize::new(0));
    let app = app(Arc::new(MemoryStore::new()), &runs, false);
    let slow = tokio::spawn({
        let app = app.clone();
        async move { send(&app, "ana", Some("slow-1"), "/api/orders", "slow").await }
    });
    tokio::time::sleep(Duration::from_millis(100)).await;
    let busy = send(&app, "ana", Some("slow-1"), "/api/orders", "slow").await;
    assert_eq!(busy.status, StatusCode::CONFLICT);
    assert_eq!(busy.body["code"], CODE_IN_FLIGHT);
    // The route's timeout is 30 seconds: the first request may run that long.
    assert_eq!(busy.retry_after.as_deref(), Some("30"));
    assert_eq!(busy.body["details"]["retryAfterSeconds"], 30);
    let first = slow.await.unwrap();
    assert_eq!((first.status, first.replay), (StatusCode::CREATED, false));
    let after = send(&app, "ana", Some("slow-1"), "/api/orders", "slow").await;
    assert_eq!((after.replay, &after.body), (true, &first.body));
    assert_eq!(runs.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn a_5xx_or_a_panic_keeps_nothing_so_a_retry_runs_again() {
    let runs = Arc::new(AtomicUsize::new(0));
    let app = app(Arc::new(MemoryStore::new()), &runs, false);
    for body in ["fail", "panic"] {
        let failed = send(&app, "ana", Some(body), "/api/orders", body).await;
        assert_eq!(failed.status, StatusCode::INTERNAL_SERVER_ERROR, "{body}");
        let retried = send(&app, "ana", Some(body), "/api/orders", body).await;
        assert_eq!(retried.status, StatusCode::INTERNAL_SERVER_ERROR, "{body}");
        assert!(!retried.replay, "{body}");
    }
    assert_eq!(runs.load(Ordering::SeqCst), 4);
}

#[tokio::test]
async fn a_refusal_under_500_is_kept_and_replayed_as_a_problem() {
    let runs = Arc::new(AtomicUsize::new(0));
    let counted = Arc::clone(&runs);
    let route = RouteControls::new()
        .idempotent(Arc::new(MemoryStore::new()), "orders.placeOrder", 0, false)
        .apply(post(move || async move {
            counted.fetch_add(1, Ordering::SeqCst);
            superschematic_http_runtime::error_response(ApiError::conflict("sold out"))
        }));
    let app = Router::new()
        .route("/api/orders", route)
        .layer(axum::middleware::from_fn(request_ids));
    let first = send(&app, "", Some("k"), "/api/orders", "").await;
    let again = send(&app, "", Some("k"), "/api/orders", "").await;
    assert_eq!(
        (first.status, again.status),
        (StatusCode::CONFLICT, StatusCode::CONFLICT)
    );
    assert_eq!((first.replay, again.replay), (false, true));
    assert_eq!(again.body["detail"], "sold out");
    // The kept problem names the first request, as a kept envelope does.
    assert!(first.body["requestId"].is_string(), "{}", first.body);
    assert_eq!(again.body["requestId"], first.body["requestId"]);
    assert_eq!(runs.load(Ordering::SeqCst), 1);
}
