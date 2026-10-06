//! Reading a request as the generated handlers do: the body as JSON, which
//! the Go and TypeScript servers parse whatever its `Content-Type`, and the
//! query as its keys and values. A failure is an [`ApiError`], so the router
//! answers it as a problem rather than with axum's plain-text rejection.

use crate::params::QueryValues;
use crate::response::status_code_name;
use crate::ApiError;
use axum::body::Bytes;
use axum::extract::rejection::{BytesRejection, QueryRejection};
use axum::extract::Query;
use serde_json::Value;

/// The request body as JSON. An empty body is `None`, which an operation
/// without a required input or argument takes; a body that is not JSON is
/// 400, "Request body is not valid JSON" as every generated server says,
/// and one axum could not read (over its size limit, say) is its status.
pub fn json_body(body: Result<Bytes, BytesRejection>) -> Result<Option<Value>, ApiError> {
    let bytes = body.map_err(|rejection| {
        let status = rejection.status();
        ApiError::new(status, status_code_name(status), rejection.body_text())
    })?;
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Ok(None);
    }
    serde_json::from_slice(&bytes)
        .map(Some)
        .map_err(|_| ApiError::bad_request("Request body is not valid JSON"))
}

/// The query string's keys and values, every occurrence in order. A query
/// that does not decode is 400.
pub fn query_values(
    query: Result<Query<Vec<(String, String)>>, QueryRejection>,
) -> Result<QueryValues, ApiError> {
    match query {
        Ok(Query(pairs)) => Ok(QueryValues::new(pairs)),
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
        assert_eq!(json_body(Ok(Bytes::from_static(b""))).unwrap(), None);
        assert_eq!(json_body(Ok(Bytes::from_static(b" \n"))).unwrap(), None);
        assert_eq!(
            json_body(Ok(Bytes::from_static(br#"{"a":1}"#))).unwrap(),
            Some(json!({"a": 1}))
        );
        assert_eq!(
            json_body(Ok(Bytes::from_static(b"null"))).unwrap(),
            Some(Value::Null)
        );
        let err = json_body(Ok(Bytes::from_static(b"{nope"))).unwrap_err();
        assert_eq!(err.status, StatusCode::BAD_REQUEST);
        assert_eq!(err.code, "bad_request");
        assert_eq!(err.message, "Request body is not valid JSON");
    }
}
