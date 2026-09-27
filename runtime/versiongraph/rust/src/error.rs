//! The error every operation returns for input it cannot run on.

use serde::Serialize;

/// A refused input. It crosses the boundary as
/// `{"error":{"code":"...","message":"..."}}`; `code` is stable, `message`
/// names the offending part of the input.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Error {
    pub code: &'static str,
    pub message: String,
}

impl Error {
    pub(crate) fn new(code: &'static str, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
        }
    }

    /// The input is not JSON.
    pub(crate) fn json(message: impl Into<String>) -> Self {
        Self::new("invalid_json", message)
    }

    /// The request envelope is missing a member or has one of the wrong type.
    pub(crate) fn request(message: impl Into<String>) -> Self {
        Self::new("invalid_request", message)
    }

    pub(crate) fn descriptor(message: impl Into<String>) -> Self {
        Self::new("invalid_descriptor", message)
    }

    pub(crate) fn row(message: impl Into<String>) -> Self {
        Self::new("invalid_row", message)
    }

    pub(crate) fn resolution(message: impl Into<String>) -> Self {
        Self::new("invalid_resolution", message)
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}: {}", self.code, self.message)
    }
}

impl std::error::Error for Error {}
