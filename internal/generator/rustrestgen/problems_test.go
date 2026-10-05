package rustrestgen

// problemsRouterTest is tests/problems.rs of fixture-nested-arrays-api's
// crate (TestNestedArraysAPICrateBuildsAndRoutes), with API_CRATE and
// RUNTIME_CRATE replaced by the crates' module names. It checks the wire
// contract the router shares with the Go and TypeScript servers: each
// response echoes the request's id in x-request-id and is not cached, a
// success's meta.requestId and a refusal's requestId are that id, a
// refusal is an RFC 9457 problem, the body is JSON whatever its
// Content-Type and an empty one is null, and a POST's query reaches the
// implementation.
const problemsRouterTest = `use std::sync::Arc;

use API_CRATE::{build_router, GridImplementation, Implementations};
use RUNTIME_CRATE::{ApiError, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{HeaderMap, Request, StatusCode};
use axum::Router;
use serde_json::{json, Value};
use tower::ServiceExt;

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

struct Grids;

#[async_trait]
impl GridImplementation for Grids {
    async fn save_grid(&self, ctx: RequestContext, payload: Value) -> Result<Value, ApiError> {
        Ok(json!({"payload": payload, "dryRun": ctx.query_params.get("dryRun")}))
    }
    async fn replace_labels(&self, _ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        Err(ApiError::conflict("The labels changed since you read them").with_details(json!({"version": 3})))
    }
    async fn paint(&self, _ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        Ok(Value::Null)
    }
    async fn get_grid(&self, _ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        Err(ApiError::not_implemented("get_grid is not implemented"))
    }
    async fn grid_labels(&self, _ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        Ok(json!([]))
    }
}

fn router() -> Router {
    build_router(Implementations { grid: Arc::new(Grids) })
}

/// Sends a request with no Content-Type, and answers the status, the
/// headers and the JSON body.
async fn send(method: &str, uri: &str, request_id: Option<&str>, body: &str) -> (StatusCode, HeaderMap, Value) {
    let mut request = Request::builder().method(method).uri(uri);
    if let Some(id) = request_id {
        request = request.header("x-request-id", id);
    }
    let response = router()
        .oneshot(request.body(Body::from(body.to_string())).unwrap())
        .await
        .unwrap();
    let status = response.status();
    let headers = response.headers().clone();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, headers, serde_json::from_slice(&bytes).unwrap_or(Value::Null))
}

fn assert_problem(headers: &HeaderMap, body: &Value, status: u16, title: &str, code: &str) {
    assert_eq!(headers["content-type"], "application/problem+json", "{body}");
    assert_eq!(body["type"], "about:blank");
    assert_eq!(body["status"], status);
    assert_eq!(body["title"], title);
    assert_eq!(body["code"], code, "{body}");
    assert_eq!(body["requestId"], headers["x-request-id"].to_str().unwrap(), "{body}");
}

#[tokio::test]
async fn a_success_echoes_the_request_id() {
    let (status, headers, body) = send("POST", "/api/grids?dryRun=true", Some("req-1"), r#"{"name": "g"}"#).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(headers["x-request-id"], "req-1");
    assert_eq!(headers["cache-control"], "no-store");
    assert_eq!(body["meta"]["requestId"], "req-1");
    assert_eq!(body["data"]["payload"], json!({"name": "g"}), "the body is JSON without a Content-Type");
    assert_eq!(body["data"]["dryRun"], "true", "a POST's query reaches the implementation");
}

#[tokio::test]
async fn a_request_without_an_id_gets_one() {
    let (status, headers, body) = send("POST", "/api/grids", None, "").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    let id = headers["x-request-id"].to_str().unwrap();
    assert_eq!(id.len(), 36, "a UUID: {id}");
    assert_eq!(body["meta"]["requestId"], id);
    assert_eq!(body["data"]["payload"], Value::Null, "an empty body is null");
}

#[tokio::test]
async fn a_body_that_is_not_json_is_a_bad_request_problem() {
    let (status, headers, body) = send("POST", "/api/grids", Some("req-2"), "{nope").await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_problem(&headers, &body, 400, "Bad Request", "bad_request");
    assert!(body["detail"].as_str().unwrap().starts_with("The request body is not valid JSON"), "{body}");
    assert_eq!(body["requestId"], "req-2");
}

#[tokio::test]
async fn an_implementation_error_is_its_problem() {
    let uri = format!("/api/grids/{GRID_ID}/labels");
    let (status, headers, body) = send("PUT", &uri, None, r#"{"labels": []}"#).await;
    assert_eq!(status, StatusCode::CONFLICT);
    assert_problem(&headers, &body, 409, "Conflict", "conflict");
    assert_eq!(body["detail"], "The labels changed since you read them");
    assert_eq!(body["details"], json!({"version": 3}));
}

#[tokio::test]
async fn a_method_the_path_does_not_serve_is_a_problem() {
    let (status, headers, body) = send("DELETE", "/api/grids", None, "").await;
    assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED);
    assert_problem(&headers, &body, 405, "Method Not Allowed", "method_not_allowed");
    assert!(headers.contains_key("allow"), "{headers:?}");
}
`
