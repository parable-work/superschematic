//! A request's id, as the TypeScript runtime assigns and echoes it: the
//! caller's `X-Request-ID` when it is a usable id, else a fresh UUID.

use crate::response::{status_code_name, status_title, PROBLEM_CONTENT_TYPE, REQUEST_ID_HEADER};
use crate::{error_response, ApiError};
use axum::body::Body;
use axum::body::HttpBody as _;
use axum::extract::Request;
use axum::http::header::{CACHE_CONTROL, CONTENT_TYPE};
use axum::http::{HeaderMap, HeaderValue};
use axum::middleware::Next;
use axum::response::Response;
use serde_json::Value;

/// The longest caller-sent request id the router keeps.
const MAX_REQUEST_ID_LENGTH: usize = 128;

/// The largest problem body the middleware reads to add its request id.
const MAX_PROBLEM_BYTES: usize = 64 * 1024;

/// The request's id: the `X-Request-ID` header when it is non-empty, at most
/// 128 characters long and free of control characters, else a fresh UUID v4.
pub fn request_id_of(headers: &HeaderMap) -> String {
    headers
        .get(REQUEST_ID_HEADER)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .filter(|id| {
            !id.is_empty()
                && id.chars().count() <= MAX_REQUEST_ID_LENGTH
                && !id.chars().any(char::is_control)
        })
        .map_or_else(|| uuid::Uuid::new_v4().to_string(), str::to_owned)
}

/// axum middleware the generated router runs around every route, before a
/// webhook verifier and the route's controls. It settles the request's id
/// ([`request_id_of`]) and writes it to the request's `X-Request-ID`, so the
/// handler and every refusal read the same id. On the way out it echoes the
/// id in the response's `x-request-id`, marks the response `no-store`, adds
/// the id to a problem body as `requestId`, and turns an error response
/// without a body (axum's 405 for a method the path does not serve) into a
/// problem. A service mounting a `@manualRouteRegistration` route adds it to
/// that route with `route_layer(axum::middleware::from_fn(request_ids))`.
pub async fn request_ids(mut request: Request, next: Next) -> Response {
    let id = request_id_of(request.headers());
    let header = HeaderValue::from_str(&id).ok();
    if let Some(header) = &header {
        request
            .headers_mut()
            .insert(REQUEST_ID_HEADER, header.clone());
    }
    let mut response = next.run(request).await;
    if response.status().is_client_error() || response.status().is_server_error() {
        response = into_problem(response, &id).await;
    }
    let headers = response.headers_mut();
    if let Some(header) = header {
        headers.insert(REQUEST_ID_HEADER, header);
    }
    headers
        .entry(CACHE_CONTROL)
        .or_insert(HeaderValue::from_static("no-store"));
    response
}

/// An error response as a problem that names the request: a problem body
/// gains `requestId`, and a response without a body becomes the problem of
/// its status. Any other response is returned as it is.
async fn into_problem(response: Response, id: &str) -> Response {
    let is_problem = response
        .headers()
        .get(CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.starts_with(PROBLEM_CONTENT_TYPE));
    if !is_problem {
        if response.body().size_hint().exact() != Some(0) {
            return response;
        }
        let status = response.status();
        let mut problem = error_response(ApiError::new(
            status,
            status_code_name(status),
            status_title(status),
        ));
        for (name, value) in response.headers() {
            if name != CONTENT_TYPE && name != axum::http::header::CONTENT_LENGTH {
                problem.headers_mut().insert(name.clone(), value.clone());
            }
        }
        return add_request_id(problem, id).await;
    }
    add_request_id(response, id).await
}

/// A problem response whose body holds `requestId`.
async fn add_request_id(response: Response, id: &str) -> Response {
    let (mut parts, body) = response.into_parts();
    let Ok(bytes) = axum::body::to_bytes(body, MAX_PROBLEM_BYTES).await else {
        return Response::from_parts(parts, Body::empty());
    };
    let Ok(Value::Object(mut problem)) = serde_json::from_slice::<Value>(&bytes) else {
        return Response::from_parts(parts, Body::from(bytes));
    };
    problem
        .entry("requestId")
        .or_insert_with(|| Value::from(id));
    parts.headers.remove(axum::http::header::CONTENT_LENGTH);
    Response::from_parts(parts, Body::from(Value::Object(problem).to_string()))
}

#[cfg(test)]
mod tests {
    use axum::http::{Request as HttpRequest, StatusCode};
    use axum::routing::{get, post};
    use axum::Router;
    use serde_json::json;
    use tower::ServiceExt;

    use super::*;

    fn headers(id: &str) -> HeaderMap {
        let mut headers = HeaderMap::new();
        headers.insert(REQUEST_ID_HEADER, HeaderValue::from_str(id).unwrap());
        headers
    }

    #[test]
    fn a_usable_caller_id_is_kept() {
        assert_eq!(request_id_of(&headers("req-42")), "req-42");
        let long = "x".repeat(129);
        assert_ne!(request_id_of(&headers(&long)), long);
        assert_ne!(request_id_of(&headers("")), "");
        assert_eq!(request_id_of(&HeaderMap::new()).len(), 36);
    }

    fn router() -> Router {
        Router::new()
            .route(
                "/ok",
                get(|request: Request| async move { request_id_of(request.headers()) }),
            )
            .route(
                "/refused",
                get(|| async { error_response(ApiError::forbidden("no")) }),
            )
            .route("/only-post", post(|| async { "posted" }))
            .layer(axum::middleware::from_fn(request_ids))
    }

    async fn call(path: &str, id: Option<&str>) -> (StatusCode, HeaderMap, String) {
        let mut request = HttpRequest::get(path);
        if let Some(id) = id {
            request = request.header(REQUEST_ID_HEADER, id);
        }
        let response = router()
            .oneshot(request.body(Body::empty()).unwrap())
            .await
            .unwrap();
        let (parts, body) = response.into_parts();
        let bytes = axum::body::to_bytes(body, usize::MAX).await.unwrap();
        (
            parts.status,
            parts.headers,
            String::from_utf8(bytes.to_vec()).unwrap(),
        )
    }

    #[tokio::test]
    async fn the_handler_and_the_response_share_the_id() {
        let (status, headers, body) = call("/ok", Some("req-7")).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body, "req-7");
        assert_eq!(headers[REQUEST_ID_HEADER], "req-7");
        assert_eq!(headers[CACHE_CONTROL], "no-store");

        let (_, headers, body) = call("/ok", None).await;
        assert_eq!(
            headers[REQUEST_ID_HEADER],
            body.as_str(),
            "a generated id is the handler's too"
        );
    }

    #[tokio::test]
    async fn a_problem_names_the_request() {
        let (status, headers, body) = call("/refused", Some("req-9")).await;
        assert_eq!(status, StatusCode::FORBIDDEN);
        assert_eq!(headers[CONTENT_TYPE], PROBLEM_CONTENT_TYPE);
        let problem: Value = serde_json::from_str(&body).unwrap();
        assert_eq!(problem["requestId"], "req-9");
        assert_eq!(problem["code"], "forbidden");
        assert_eq!(problem["detail"], "no");
    }

    #[tokio::test]
    async fn an_empty_error_becomes_the_problem_of_its_status() {
        let (status, headers, body) = call("/only-post", Some("req-5")).await;
        assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED);
        assert_eq!(headers[CONTENT_TYPE], PROBLEM_CONTENT_TYPE);
        assert!(headers.contains_key("allow"), "{headers:?}");
        let problem: Value = serde_json::from_str(&body).unwrap();
        assert_eq!(
            problem,
            json!({"type": "about:blank", "title": "Method Not Allowed", "status": 405,
                   "detail": "Method Not Allowed", "code": "method_not_allowed", "requestId": "req-5"})
        );
    }
}
