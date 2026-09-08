use crate::ApiError;
use axum::http::StatusCode;
use axum::Json;
use serde_json::{json, Value};

/// success envelope key for response metadata.
const META_REQUEST_ID_HEADER: &str = "x-request-id";

pub fn json_response(status: StatusCode, body: Value) -> (StatusCode, Json<Value>) {
    (status, Json(body))
}

pub fn error_response(err: ApiError) -> (StatusCode, Json<Value>) {
    let body = json!({
        "error": {
            "code": err.code,
            "message": err.message,
        }
    });
    (err.status, Json(body))
}

/// Wraps a success payload in the success envelope `{data, meta:
/// {requestId}}`. The Go and Rust SDKs reject responses that don't carry
/// this shape, so every generated success path runs through this helper.
///
/// `request_id` is the incoming `X-Request-Id` header value when present;
/// otherwise a fresh UUID is generated so traces still correlate.
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
        .get(META_REQUEST_ID_HEADER)
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_string())
}
