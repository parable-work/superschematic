use reqwest::header::HeaderMap;
use serde_json::Value;
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
        /// The problem's `detail`, or an older body's `error` or `message`.
        message: String,
        code: Option<String>,
        body: String,
        /// The rest of what the response said, boxed so the error stays
        /// small.
        problem: Box<ApiProblem>,
    },
    #[error("sdk contract error: {0}")]
    Contract(String),
}

/// What an error response says beyond its status, message and code.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ApiProblem {
    /// The response's `X-Request-ID` header, else the problem's `requestId`.
    pub request_id: Option<String>,
    /// The seconds the response's `Retry-After` header asks the caller to
    /// wait, when it holds a number of seconds.
    pub retry_after: Option<u64>,
    /// The problem's `errors` member: a refused input's field errors, keyed
    /// by path.
    pub validation_errors: Option<Value>,
    /// The problem's `details` member.
    pub details: Option<Value>,
}

impl SDKError {
    pub fn config(message: impl Into<String>) -> Self {
        Self::Config(message.into())
    }

    pub fn request(message: impl Into<String>) -> Self {
        Self::Request(message.into())
    }

    /// The error of a response outside 2xx, read from its headers and its
    /// RFC 9457 problem body.
    pub fn api(status_code: u16, headers: &HeaderMap, body: &str) -> Self {
        let problem = ProblemBody::parse(body);
        let header = |name: &str| {
            headers
                .get(name)
                .and_then(|value| value.to_str().ok())
                .map(str::trim)
                .filter(|value| !value.is_empty())
        };
        Self::Api {
            status_code,
            message: problem.message,
            code: problem.code,
            body: body.to_string(),
            problem: Box::new(ApiProblem {
                request_id: header("x-request-id").map(str::to_string).or(problem.request_id),
                retry_after: header("retry-after").and_then(|value| value.parse().ok()),
                validation_errors: problem.validation_errors,
                details: problem.details,
            }),
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

    /// What the error response said beyond its status, message and code.
    pub fn problem(&self) -> Option<&ApiProblem> {
        match self {
            Self::Api { problem, .. } => Some(problem),
            _ => None,
        }
    }

    /// The request id the server answered with, to quote in a report.
    pub fn request_id(&self) -> Option<&str> {
        self.problem()?.request_id.as_deref()
    }

    /// The seconds a 429 (or a 503) asks the caller to wait before trying
    /// again, from its `Retry-After` header.
    pub fn retry_after(&self) -> Option<u64> {
        self.problem()?.retry_after
    }

    /// The field errors of a refused input, keyed by path, when the server
    /// sent them.
    pub fn validation_errors(&self) -> Option<&Value> {
        self.problem()?.validation_errors.as_ref()
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

/// What an error response's body says, as the Go and TypeScript SDKs read
/// it: an RFC 9457 problem's `detail`, `code`, `requestId`, `errors` and
/// `details`. An older body's string `error` or `message` is the message
/// when there is no `detail`, and so is the `message` of an `error` object,
/// which the Rust server answered with before it wrote problems.
struct ProblemBody {
    message: String,
    code: Option<String>,
    request_id: Option<String>,
    validation_errors: Option<Value>,
    details: Option<Value>,
}

impl ProblemBody {
    fn parse(body: &str) -> Self {
        let value = serde_json::from_str::<Value>(body).unwrap_or(Value::Null);
        let text = |value: Option<&Value>| {
            value
                .and_then(Value::as_str)
                .map(str::trim)
                .filter(|text| !text.is_empty())
                .map(str::to_string)
        };
        let nested = value.get("error").filter(|error| error.is_object());
        let message = text(value.get("detail"))
            .or_else(|| text(value.get("error")))
            .or_else(|| text(value.get("message")))
            .or_else(|| text(nested.and_then(|error| error.get("message"))))
            .unwrap_or_else(|| "api request failed".to_string());
        Self {
            message,
            code: text(value.get("code")).or_else(|| text(nested.and_then(|error| error.get("code")))),
            request_id: text(value.get("requestId")),
            validation_errors: value.get("errors").filter(|errors| errors.is_object()).cloned(),
            details: value.get("details").filter(|details| !details.is_null()).cloned(),
        }
    }
}
