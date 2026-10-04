use thiserror::Error;

#[derive(Debug, Error)]
pub enum SDKError {
    #[error("invalid sdk configuration: {0}")]
    Config(String),
    #[error("request build failed: {0}")]
    Request(String),
    #[error("network request failed: {0}")]
    Network(#[from] reqwest::Error),
    #[error("json serialization failed: {0}")]
    Json(#[from] serde_json::Error),
    #[error("api request failed (status {status_code}): {message}")]
    Api {
        status_code: u16,
        message: String,
        code: Option<String>,
        body: String,
    },
    #[error("sdk contract error: {0}")]
    Contract(String),
}

impl SDKError {
    pub fn config(message: impl Into<String>) -> Self {
        Self::Config(message.into())
    }

    pub fn request(message: impl Into<String>) -> Self {
        Self::Request(message.into())
    }

    pub fn api(status_code: u16, body: &str) -> Self {
        let (message, code) = extract_problem_details(body);
        Self::Api {
            status_code,
            message,
            code,
            body: body.to_string(),
        }
    }

    pub fn contract(message: impl Into<String>) -> Self {
        Self::Contract(message.into())
    }

    /// HTTP status code when the server responded with an error status;
    /// `None` for failures that never produced a response (config, network,
    /// serialization, contract).
    pub fn status_code(&self) -> Option<u16> {
        match self {
            Self::Api { status_code, .. } => Some(*status_code),
            _ => None,
        }
    }

    /// RFC 7807 error code returned by the API, when present.
    pub fn error_code(&self) -> Option<&str> {
        match self {
            Self::Api { code, .. } => code.as_deref(),
            _ => None,
        }
    }

    /// The server rejected the caller's credentials or permissions
    /// (401 / 403): evidence the token is bad, not that the service is
    /// down. Callers use this to distinguish auth failure from outage.
    pub fn is_auth_denied(&self) -> bool {
        matches!(self, Self::Api { status_code, .. } if *status_code == 401 || *status_code == 403)
    }

    /// The failure does not prove the request can never succeed: network
    /// errors and timeouts, plus 408 / 429 / 5xx responses. Callers should
    /// treat these as retryable rather than tearing down sessions.
    pub fn is_transient(&self) -> bool {
        match self {
            Self::Network(_) => true,
            Self::Api { status_code, .. } => {
                *status_code == 408 || *status_code == 429 || *status_code >= 500
            }
            _ => false,
        }
    }
}

fn extract_problem_details(body: &str) -> (String, Option<String>) {
    if body.trim().is_empty() {
        return ("api request failed".to_string(), None);
    }

    let parsed = serde_json::from_str::<serde_json::Value>(body);
    if let Ok(value) = parsed {
        let code = value
            .get("code")
            .and_then(|entry| entry.as_str())
            .map(str::to_string);
        for key in ["error", "message", "detail"] {
            if let Some(message) = value.get(key).and_then(|entry| entry.as_str()) {
                if !message.trim().is_empty() {
                    return (message.to_string(), code);
                }
            }
        }
        return ("api request failed".to_string(), code);
    }

    ("api request failed".to_string(), None)
}
