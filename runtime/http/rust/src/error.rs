use axum::http::StatusCode;
use serde::{Deserialize, Serialize};
use serde_json::Value;

/// ApiError is the canonical error type for generated handlers. The router
/// answers it as an RFC 9457 problem (`error_response`): `message` is the
/// problem's `detail`, `code` its `code`, and `details` and `errors` the
/// members of the same names.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ApiError {
    pub code: String,
    pub message: String,
    #[serde(skip)]
    pub status: StatusCode,
    /// Structured context the client may read, such as the parameter a
    /// refusal names. Boxed, as `errors` is, so the error stays small
    /// enough to return when serde_json's `preserve_order` feature makes a
    /// `Value` large.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub details: Option<Box<Value>>,
    /// A refused input's field errors, keyed by path, the member the Go
    /// server and the TypeScript SDK use.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub errors: Option<Box<Value>>,
}

impl ApiError {
    pub fn new(status: StatusCode, code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
            status,
            details: None,
            errors: None,
        }
    }

    /// The error with the problem's `details` member.
    #[must_use]
    pub fn with_details(mut self, details: Value) -> Self {
        self.details = Some(Box::new(details));
        self
    }

    /// The error with the problem's `errors` member: field errors keyed by
    /// path.
    #[must_use]
    pub fn with_errors(mut self, errors: Value) -> Self {
        self.errors = Some(Box::new(errors));
        self
    }

    pub fn bad_request(message: impl Into<String>) -> Self {
        Self::new(StatusCode::BAD_REQUEST, "bad_request", message)
    }

    pub fn unauthorized(message: impl Into<String>) -> Self {
        Self::new(StatusCode::UNAUTHORIZED, "unauthorized", message)
    }

    pub fn forbidden(message: impl Into<String>) -> Self {
        Self::new(StatusCode::FORBIDDEN, "forbidden", message)
    }

    /// A service credential is required and missing, or does not verify (D37).
    pub fn service_unauthorized(message: impl Into<String>) -> Self {
        Self::new(StatusCode::UNAUTHORIZED, "service_unauthorized", message)
    }

    /// A verified service identity that is no caller of this server, or that
    /// the route's `from` does not list (D37).
    pub fn service_forbidden(message: impl Into<String>) -> Self {
        Self::new(StatusCode::FORBIDDEN, "service_forbidden", message)
    }

    pub fn not_found(message: impl Into<String>) -> Self {
        Self::new(StatusCode::NOT_FOUND, "not_found", message)
    }

    pub fn method_not_allowed(message: impl Into<String>) -> Self {
        Self::new(
            StatusCode::METHOD_NOT_ALLOWED,
            "method_not_allowed",
            message,
        )
    }

    pub fn conflict(message: impl Into<String>) -> Self {
        Self::new(StatusCode::CONFLICT, "conflict", message)
    }

    pub fn unprocessable_entity(message: impl Into<String>) -> Self {
        Self::new(
            StatusCode::UNPROCESSABLE_ENTITY,
            "unprocessable_entity",
            message,
        )
    }

    /// 503, also a service credential that could not be checked for a
    /// reason that is not the caller's, such as keys that cannot be fetched
    /// (D37).
    pub fn service_unavailable(message: impl Into<String>) -> Self {
        Self::new(
            StatusCode::SERVICE_UNAVAILABLE,
            "service_unavailable",
            message,
        )
    }

    pub fn payload_too_large(message: impl Into<String>) -> Self {
        Self::new(StatusCode::PAYLOAD_TOO_LARGE, "payload_too_large", message)
    }

    pub fn too_many_requests(message: impl Into<String>) -> Self {
        Self::new(StatusCode::TOO_MANY_REQUESTS, "too_many_requests", message)
    }

    pub fn gateway_timeout(message: impl Into<String>) -> Self {
        Self::new(StatusCode::GATEWAY_TIMEOUT, "gateway_timeout", message)
    }

    pub fn not_implemented(message: impl Into<String>) -> Self {
        Self::new(StatusCode::NOT_IMPLEMENTED, "not_implemented", message)
    }

    pub fn internal(message: impl Into<String>) -> Self {
        Self::new(StatusCode::INTERNAL_SERVER_ERROR, "internal_error", message)
    }
}
