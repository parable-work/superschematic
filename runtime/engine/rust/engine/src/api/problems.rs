//! How a refusal crosses HTTP (D15, D58), the port of
//! `src/http/problems.ts` and the HTTP runtime's problem and envelope
//! writers (`runtime/http/typescript/src/problem.ts`, `envelope.ts`): each
//! engine refusal code answers one status, and the RFC 9457 problem
//! document carries the code as `code` and what the refusal carries as
//! `details` (a refused document's or instance's issues, a refused
//! version's changes, a unique conflict's fields, an expired cursor's
//! floor and head, an oversized value's pointer and lengths, a veto's
//! behavior, action, reason, code and details). `type` stays
//! `about:blank`, as the runtime writes it. A behavior's defect, or any
//! failure that is no refusal, is a 500 whose message never reaches the
//! wire. A success is the envelope `{data, meta: {requestId}}`.

use serde_json::{Map, Value};

use crate::errors::{EngineErrorKind, Error};
use crate::json;

use super::{Body, Response};

/// An HTTP refusal: its status, its detail, the runtime's or the engine's
/// code, what it carries, and headers it adds.
#[derive(Clone, Debug, PartialEq)]
pub struct Problem {
    pub status: u16,
    pub detail: String,
    pub code: Option<String>,
    pub details: Option<Value>,
    pub headers: Vec<(String, String)>,
}

impl Problem {
    pub fn new(status: u16, detail: impl Into<String>, code: &str) -> Self {
        Problem {
            status,
            detail: detail.into(),
            code: (!code.is_empty()).then(|| code.to_owned()),
            details: None,
            headers: Vec::new(),
        }
    }

    pub fn with_details(mut self, details: Value) -> Self {
        self.details = Some(details);
        self
    }

    pub fn with_header(mut self, name: &str, value: &str) -> Self {
        self.headers.push((name.to_owned(), value.to_owned()));
        self
    }

    /// 400 `bad_request`.
    pub fn bad_request(detail: impl Into<String>) -> Self {
        Problem::new(400, detail, "bad_request")
    }

    /// 401 `unauthorized`.
    pub fn unauthorized() -> Self {
        Problem::new(401, "Authentication required", "unauthorized")
    }

    /// 404 `not_found`.
    pub fn not_found(detail: impl Into<String>) -> Self {
        Problem::new(404, detail, "not_found")
    }

    /// 500 `internal_error`, the failure itself kept off the wire.
    pub fn internal() -> Self {
        Problem::new(500, "An unexpected error occurred", "internal_error")
    }

    /// The problem an engine error answers with: a refusal by its code's
    /// status, with its details; anything else 500.
    pub fn of(error: &Error) -> Self {
        let Error::Engine(refusal) = error else {
            return Problem::internal();
        };
        let mut problem =
            Problem::new(refusal.status(), refusal.message(), refusal.code().as_str());
        let details = match refusal.kind() {
            EngineErrorKind::BehaviorVeto { .. }
            | EngineErrorKind::SchemaDocument { .. }
            | EngineErrorKind::InstanceValidation { .. }
            | EngineErrorKind::OperationParams { .. }
            | EngineErrorKind::CreateParams { .. }
            | EngineErrorKind::Preconditions { .. }
            | EngineErrorKind::IncompatibleChange { .. }
            | EngineErrorKind::UniqueConflict { .. }
            | EngineErrorKind::CursorExpired { .. }
            | EngineErrorKind::ValueTooLarge { .. } => refusal.details(),
            EngineErrorKind::Plain | EngineErrorKind::UnknownTool { .. } => None,
        };
        problem.details = details;
        problem
    }

    /// The problem document's body: `type`, `title`, `status`, `detail`,
    /// `code`, `requestId` and `details`, as the runtime writes them.
    pub fn body(&self, request_id: &str) -> Value {
        let mut out = Map::new();
        out.insert("type".into(), Value::from("about:blank"));
        out.insert("title".into(), Value::from(status_text(self.status)));
        out.insert("status".into(), Value::from(self.status));
        out.insert("detail".into(), Value::from(self.detail.as_str()));
        if let Some(code) = &self.code {
            out.insert("code".into(), Value::from(code.as_str()));
        }
        out.insert("requestId".into(), Value::from(request_id));
        if let Some(details) = &self.details {
            out.insert("details".into(), details.clone());
        }
        Value::Object(out)
    }

    /// The problem as a response.
    pub fn response(&self, request_id: &str) -> Response {
        let mut headers = self.headers.clone();
        headers.push((
            "content-type".to_owned(),
            "application/problem+json".to_owned(),
        ));
        headers.push(("cache-control".to_owned(), "no-store".to_owned()));
        headers.push(("x-request-id".to_owned(), request_id.to_owned()));
        Response {
            status: self.status,
            headers,
            body: Body::Bytes(json::stringify(&self.body(request_id)).into_bytes()),
        }
    }
}

/// The success envelope `{data, meta: {requestId}}` as a response, with a
/// status and headers of its own (an ETag, a Location).
pub fn envelope(
    data: Value,
    request_id: &str,
    status: u16,
    extra: Vec<(String, String)>,
) -> Response {
    let mut body = Map::new();
    body.insert("data".into(), data);
    let mut meta = Map::new();
    meta.insert("requestId".into(), Value::from(request_id));
    body.insert("meta".into(), Value::Object(meta));
    let mut headers = vec![
        ("content-type".to_owned(), "application/json".to_owned()),
        ("cache-control".to_owned(), "no-store".to_owned()),
    ];
    headers.extend(extra);
    headers.push(("x-request-id".to_owned(), request_id.to_owned()));
    Response {
        status,
        headers,
        body: Body::Bytes(json::stringify(&Value::Object(body)).into_bytes()),
    }
}

/// The reason phrase Go's `http.StatusText` gives a status, or `Error`.
pub fn status_text(status: u16) -> &'static str {
    match status {
        400 => "Bad Request",
        401 => "Unauthorized",
        402 => "Payment Required",
        403 => "Forbidden",
        404 => "Not Found",
        405 => "Method Not Allowed",
        406 => "Not Acceptable",
        408 => "Request Timeout",
        409 => "Conflict",
        410 => "Gone",
        412 => "Precondition Failed",
        413 => "Request Entity Too Large",
        415 => "Unsupported Media Type",
        422 => "Unprocessable Entity",
        423 => "Locked",
        428 => "Precondition Required",
        429 => "Too Many Requests",
        500 => "Internal Server Error",
        501 => "Not Implemented",
        502 => "Bad Gateway",
        503 => "Service Unavailable",
        504 => "Gateway Timeout",
        _ => "Error",
    }
}

/// The detail the runtime gives a refusal the host makes by its code, when
/// the host gives none: what a request refused before the engine saw it
/// answers.
pub fn default_detail(code: &str) -> &'static str {
    match code {
        "service_unauthorized" => "Invalid service credential",
        "service_forbidden" => "Service not permitted",
        "unauthorized" => "Authentication required",
        "forbidden" => "Insufficient permissions",
        "payload_too_large" => "Request body exceeds the accepted size",
        "too_many_requests" => "Too Many Requests",
        "gateway_timeout" => "Gateway Timeout",
        "not_found" => "Not Found",
        _ => "Request failed",
    }
}
