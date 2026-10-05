//! Reading a request as the generated handlers do: the body as JSON, which
//! the Go and TypeScript servers parse whatever its `Content-Type`, and the
//! query as a map. A failure is an [`ApiError`], so the router answers it
//! as a problem rather than with axum's plain-text rejection.

use crate::response::status_code_name;
use crate::ApiError;
use axum::body::Bytes;
use axum::extract::rejection::{BytesRejection, QueryRejection};
use axum::extract::Query;
use serde_json::Value;
use std::collections::HashMap;

/// The request body as JSON. An empty body is `null`, which an operation
/// without an input takes; a body that is not JSON is 400, and one axum
/// could not read (over its size limit, say) is its status.
pub fn json_body(body: Result<Bytes, BytesRejection>) -> Result<Value, ApiError> {
    let bytes = body.map_err(|rejection| {
        let status = rejection.status();
        ApiError::new(status, status_code_name(status), rejection.body_text())
    })?;
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Ok(Value::Null);
    }
    serde_json::from_slice(&bytes)
        .map_err(|err| ApiError::bad_request(format!("The request body is not valid JSON: {err}")))
}

/// The query string as a map of each parameter's last value. A query that
/// does not decode is 400.
pub fn query_map(
    query: Result<Query<HashMap<String, String>>, QueryRejection>,
) -> Result<HashMap<String, String>, ApiError> {
    match query {
        Ok(Query(query)) => Ok(query),
        Err(rejection) => Err(ApiError::bad_request(rejection.body_text())),
    }
}

#[cfg(test)]
mod tests {
    use axum::http::StatusCode;
    use serde_json::json;

    use super::*;

    #[test]
    fn a_body_is_json_or_nothing() {
        assert_eq!(json_body(Ok(Bytes::from_static(b""))).unwrap(), Value::Null);
        assert_eq!(
            json_body(Ok(Bytes::from_static(b" \n"))).unwrap(),
            Value::Null
        );
        assert_eq!(
            json_body(Ok(Bytes::from_static(br#"{"a":1}"#))).unwrap(),
            json!({"a": 1})
        );
        let err = json_body(Ok(Bytes::from_static(b"{nope"))).unwrap_err();
        assert_eq!(err.status, StatusCode::BAD_REQUEST);
        assert_eq!(err.code, "bad_request");
        assert!(
            err.message
                .starts_with("The request body is not valid JSON"),
            "{}",
            err.message
        );
    }
}
