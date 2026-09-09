use axum::http::StatusCode;
use serde::{Deserialize, Serialize};

/// ApiError is the canonical error type for generated handlers.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ApiError {
    pub code: String,
    pub message: String,
    #[serde(skip)]
    pub status: StatusCode,
}

impl ApiError {
    pub fn new(status: StatusCode, code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
            status,
        }
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

    pub fn not_found(message: impl Into<String>) -> Self {
        Self::new(StatusCode::NOT_FOUND, "not_found", message)
    }

    pub fn not_implemented(message: impl Into<String>) -> Self {
        Self::new(StatusCode::NOT_IMPLEMENTED, "not_implemented", message)
    }

    pub fn internal(message: impl Into<String>) -> Self {
        Self::new(StatusCode::INTERNAL_SERVER_ERROR, "internal_error", message)
    }
}
