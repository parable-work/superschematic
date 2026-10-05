//! What a generated router answers: the success envelope `{data, meta:
//! {requestId}}` and, for a refusal, an RFC 9457 problem
//! (`application/problem+json`), the shapes the Go and TypeScript servers
//! write and every SDK reads.

use crate::ApiError;
use axum::http::header::{CACHE_CONTROL, CONTENT_TYPE};
use axum::http::{HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde_json::{json, Map, Value};

/// The request header that carries a request id, and the response header
/// the router echoes it in.
pub(crate) const REQUEST_ID_HEADER: &str = "x-request-id";

/// The media type of a problem body.
pub const PROBLEM_CONTENT_TYPE: &str = "application/problem+json";

pub fn json_response(status: StatusCode, body: Value) -> (StatusCode, Json<Value>) {
    (status, Json(body))
}

/// The status's reason phrase, as Go's `http.StatusText` and the
/// TypeScript runtime spell it: the problem's `title`.
pub fn status_title(status: StatusCode) -> &'static str {
    match status.as_u16() {
        400 => "Bad Request",
        401 => "Unauthorized",
        403 => "Forbidden",
        404 => "Not Found",
        405 => "Method Not Allowed",
        409 => "Conflict",
        413 => "Request Entity Too Large",
        415 => "Unsupported Media Type",
        422 => "Unprocessable Entity",
        429 => "Too Many Requests",
        500 => "Internal Server Error",
        501 => "Not Implemented",
        502 => "Bad Gateway",
        503 => "Service Unavailable",
        504 => "Gateway Timeout",
        _ => status.canonical_reason().unwrap_or("Error"),
    }
}

/// The `code` of a refusal the router makes without an `ApiError` of its
/// own, such as axum's 405 for a method a path does not serve.
pub fn status_code_name(status: StatusCode) -> &'static str {
    match status.as_u16() {
        400 => "bad_request",
        401 => "unauthorized",
        403 => "forbidden",
        404 => "not_found",
        405 => "method_not_allowed",
        409 => "conflict",
        413 => "payload_too_large",
        415 => "unsupported_media_type",
        422 => "unprocessable_entity",
        429 => "too_many_requests",
        501 => "not_implemented",
        503 => "service_unavailable",
        504 => "gateway_timeout",
        _ if status.is_server_error() => "internal_error",
        _ => "bad_request",
    }
}

/// The problem body of err: `type`, `title`, `status`, `detail`, `code`,
/// `requestId` when known, and `details` and `errors` when err has them.
/// Built by hand, so its shape does not change when serde_json's
/// `arbitrary_precision` or `preserve_order` feature is on.
pub fn problem_body(err: &ApiError, request_id: Option<&str>) -> Value {
    let mut problem = Map::new();
    problem.insert("type".to_owned(), Value::from("about:blank"));
    problem.insert("title".to_owned(), Value::from(status_title(err.status)));
    problem.insert("status".to_owned(), Value::from(err.status.as_u16()));
    problem.insert("detail".to_owned(), Value::from(err.message.clone()));
    problem.insert("code".to_owned(), Value::from(err.code.clone()));
    if let Some(request_id) = request_id {
        problem.insert("requestId".to_owned(), Value::from(request_id));
    }
    if let Some(details) = &err.details {
        problem.insert("details".to_owned(), Value::clone(details));
    }
    if let Some(errors) = &err.errors {
        problem.insert("errors".to_owned(), Value::clone(errors));
    }
    Value::Object(problem)
}

/// The response of a refusal: err's problem, `application/problem+json`,
/// not to be cached. The router's request-id middleware ([`crate::request_ids`])
/// adds the request's id to the body and the `x-request-id` header.
pub fn error_response(err: ApiError) -> Response {
    let mut response = (err.status, problem_body(&err, None).to_string()).into_response();
    let headers = response.headers_mut();
    headers.insert(CONTENT_TYPE, HeaderValue::from_static(PROBLEM_CONTENT_TYPE));
    headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
    response
}

/// Wraps a success payload in the success envelope `{data, meta:
/// {requestId}}`. The Go and Rust SDKs reject responses that don't carry
/// this shape, so every generated success path runs through this helper.
///
/// `request_id` is the request's id ([`crate::request_id_of`]); without one
/// a fresh UUID is generated so traces still correlate.
pub fn wrap_envelope(data: Value, request_id: Option<&str>) -> Value {
    let request_id = match request_id {
        Some(id) if !id.trim().is_empty() => id.to_string(),
        _ => uuid::Uuid::new_v4().to_string(),
    };
    json!({
        "data": data,
        "meta": {
            "requestId": request_id,
        },
    })
}

/// Extract the request id from a request's header map (case-insensitive).
/// Convenience for router templates that already hold the raw header map.
pub fn request_id_from_headers(headers: &axum::http::HeaderMap) -> Option<String> {
    headers
        .get(REQUEST_ID_HEADER)
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_problem_carries_the_errors_members() {
        let err = ApiError::bad_request("Request body does not match the declared input")
            .with_details(json!({"location": "body"}))
            .with_errors(json!({"name": [{"validator": "required", "message": "required field"}]}));
        assert_eq!(
            problem_body(&err, Some("req-1")),
            json!({
                "type": "about:blank",
                "title": "Bad Request",
                "status": 400,
                "detail": "Request body does not match the declared input",
                "code": "bad_request",
                "requestId": "req-1",
                "details": {"location": "body"},
                "errors": {"name": [{"validator": "required", "message": "required field"}]}
            })
        );
        let plain = problem_body(&ApiError::payload_too_large("too big"), None);
        assert_eq!(plain["title"], "Request Entity Too Large");
        assert!(plain.get("requestId").is_none() && plain.get("errors").is_none());
    }

    #[test]
    fn a_refusal_is_problem_json_and_not_cached() {
        let response = error_response(ApiError::forbidden("Insufficient permissions"));
        assert_eq!(response.status(), StatusCode::FORBIDDEN);
        assert_eq!(response.headers()[CONTENT_TYPE], PROBLEM_CONTENT_TYPE);
        assert_eq!(response.headers()[CACHE_CONTROL], "no-store");
    }
}
