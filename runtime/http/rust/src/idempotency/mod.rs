//! Idempotency keys (D140), the twin of the Go runtime's `idempotency`
//! package and of the TypeScript runtime's `idempotency.ts`. An operation
//! declared `@idempotent` answers a repeated call what the first one
//! answered: a caller sends `Idempotency-Key`, the route keeps a row per
//! operation, principal and key in the reserved table of the API's
//! database, and a second request under the key gets the stored answer
//! with `Idempotent-Replay: true`, never a second run.
//!
//! Before the handler the route step ([`crate::RouteControls::idempotent`])
//! inserts a `running` row. If it inserted, the handler runs: an answer
//! under 500 is stored as `done`, and one of 500 or more deletes the row,
//! so a retry runs again. A `done` row with the same request hash is
//! replayed; another hash is 422 `idempotency_key_reused`; a `running` row
//! younger than the route's time limit is 409 `idempotency_in_flight` with
//! `Retry-After`, and an older one is taken over. The row is not in the
//! handler's transaction: a crash after the handler commits and before the
//! answer is stored leaves `running`, and a repeat after the time limit
//! runs the handler again. It is once per key but for that window.
//!
//! On tokio the handler runs as a task of its own, so a caller that goes
//! away does not stop it between its commit and the stored answer, as a Go
//! handler and a TypeScript one run on; a handler that panics deletes its
//! row and answers 500. Under a host that drives a request with no runtime
//! (D116) it runs where it stands.
//!
//! The store is a [`Store`]: [`MemoryStore`] in the process, and over the
//! API's database [`PostgresStore`] (the `postgres` feature) or
//! [`SqliteStore`] (the `sqlite` feature), which a generated crate's
//! `Implementations::new` builds over `Deps.db` on the table its
//! `IDEMPOTENCY_TABLE` names. Each answers
//! `runtime/http/testdata/idempotency_parity.json` as Go's and
//! TypeScript's do.

mod sql;

use std::collections::HashMap;
use std::fmt::Write as _;
use std::sync::{Arc, Mutex, MutexGuard, OnceLock, PoisonError};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use axum::body::Body;
use axum::extract::Request;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use http::header::{CACHE_CONTROL, CONTENT_TYPE, RETRY_AFTER};
use http::{HeaderValue, StatusCode};
use ring::digest;
use serde_json::json;

#[cfg(feature = "postgres")]
pub use sql::PostgresStore;
#[cfg(feature = "sqlite")]
pub use sql::SqliteStore;

use crate::{error_response, ApiError, BoxError, Principal, ServiceCaller};

/// The request header that carries the key.
pub const KEY_HEADER: &str = "Idempotency-Key";

/// The response header of a stored answer.
pub const REPLAY_HEADER: &str = "Idempotent-Replay";

/// How long a key's answer is kept when `@idempotent` names no ttl.
pub const DEFAULT_TTL: Duration = Duration::from_secs(24 * 60 * 60);

/// How long a running row holds its key on a route with no `@timeout`.
pub const DEFAULT_IN_FLIGHT: Duration = Duration::from_secs(60);

/// How often a server sweeps a store's expired rows, at most.
pub const SWEEP_INTERVAL: Duration = Duration::from_secs(60);

/// How many expired rows a sweep deletes, at most.
pub const SWEEP_LIMIT: usize = 100;

/// 400: the operation requires a key and the request sent none.
pub const CODE_REQUIRED: &str = "idempotency_key_required";
/// 400: the key is not 1 to 255 printable ASCII characters.
pub const CODE_INVALID: &str = "idempotency_key_invalid";
/// 422: the key was used for another request.
pub const CODE_REUSED: &str = "idempotency_key_reused";
/// 409: the first request under the key is still running.
pub const CODE_IN_FLIGHT: &str = "idempotency_in_flight";

/// Whether `key` is one a caller may send: 1 to 255 printable ASCII
/// characters, space among them.
pub fn is_key(key: &str) -> bool {
    (1..=255).contains(&key.len()) && key.bytes().all(|byte| (0x20..=0x7e).contains(&byte))
}

fn sha256_hex(parts: &[&[u8]]) -> String {
    let mut context = digest::Context::new(&digest::SHA256);
    for part in parts {
        context.update(part);
    }
    let mut out = String::with_capacity(64);
    for byte in context.finish().as_ref() {
        let _ = write!(out, "{byte:02x}");
    }
    out
}

/// What names a row: the SHA-256 of the operation, the principal and the
/// key, each as its length in UTF-8 bytes, a colon and its bytes, so no two
/// triples share a name. A key is one caller's, on one operation: another
/// caller's same key is another row.
pub fn lookup(operation: &str, principal: &str, key: &str) -> String {
    let mut whole = Vec::new();
    for text in [operation, principal, key] {
        whole.extend_from_slice(format!("{}:", text.len()).as_bytes());
        whole.extend_from_slice(text.as_bytes());
    }
    sha256_hex(&[&whole])
}

/// What a request is, for a key to be reused only with the same one: the
/// SHA-256 of its method in upper case, a line feed, its path with its
/// query, a line feed, and its body's bytes.
pub fn request_hash(method: &str, path_and_query: &str, body: &[u8]) -> String {
    let head = format!("{}\n{path_and_query}\n", method.to_ascii_uppercase());
    sha256_hex(&[head.as_bytes(), body])
}

/// One request under a key, as [`Store::begin`] takes it. Times are
/// milliseconds since the epoch.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Attempt {
    pub lookup: String,
    pub operation: String,
    pub principal: String,
    pub key: String,
    pub request_hash: String,
    pub now: i64,
    /// When the key's row expires.
    pub expires_at: i64,
    /// A running row that started before it is taken over.
    pub take_over_before: i64,
}

/// What [`Store::begin`] decided.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Begin {
    /// The row is this request's, and the handler runs.
    Started,
    /// A done row with the same hash: its stored answer.
    Replay { status: u16, body: String },
    /// A row with another hash.
    Mismatch,
    /// A running row with the same hash, too young to take over: when it
    /// started.
    Running { started_at: i64 },
}

/// Where the keys are kept. `finish` and `abandon` name the attempt by when
/// it started, so an attempt whose row another took over changes nothing.
#[async_trait]
pub trait Store: Send + Sync {
    /// Decides `attempt`: inserts its running row, or reads the row that
    /// holds its lookup.
    async fn begin(&self, attempt: &Attempt) -> Result<Begin, BoxError>;

    /// Stores the answer of the attempt that started at `started_at`, as
    /// done.
    async fn finish(
        &self,
        lookup: &str,
        started_at: i64,
        status: u16,
        body: &str,
    ) -> Result<(), BoxError>;

    /// Deletes the running row of the attempt that started at
    /// `started_at`, so a retry runs again.
    async fn abandon(&self, lookup: &str, started_at: i64) -> Result<(), BoxError>;

    /// Deletes up to `limit` rows that expired at or before `now`, and
    /// answers how many.
    async fn sweep(&self, now: i64, limit: usize) -> Result<u64, BoxError>;
}

/// The reserved table's name and its columns' (D140), which the build reads
/// from the DB schema's `implements IdempotencyKeys` class and a generated
/// crate states as its `IDEMPOTENCY_TABLE`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Table {
    pub table: &'static str,
    pub columns: Columns,
}

/// The reserved table's columns.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Columns {
    pub lookup: &'static str,
    pub operation: &'static str,
    pub principal: &'static str,
    pub key: &'static str,
    pub request_hash: &'static str,
    pub state: &'static str,
    pub status: &'static str,
    pub body: &'static str,
    pub started_at: &'static str,
    pub expires_at: &'static str,
}

/// How a row a store found and a request under its lookup decide: an
/// answer, or a take-over of the row.
enum Decision {
    Answer(Begin),
    TakeOver,
}

/// A row a store found, as its columns read: the one rule every store
/// applies to it is [`Found::decide`].
struct Found<'a> {
    request_hash: &'a str,
    state: &'a str,
    status: Option<f64>,
    body: Option<String>,
    started_at: f64,
    expires_at: f64,
}

/// A time the table holds as a number, in whole milliseconds.
#[allow(clippy::cast_possible_truncation)]
fn millis(value: f64) -> i64 {
    value.round() as i64
}

/// A time in whole milliseconds as the table's number.
#[allow(clippy::cast_precision_loss)]
fn number(value: i64) -> f64 {
    value as f64
}

impl Found<'_> {
    fn decide(self, attempt: &Attempt) -> Decision {
        let started = millis(self.started_at);
        let expired = millis(self.expires_at) <= attempt.now;
        let stale = self.state == "running" && started < attempt.take_over_before;
        if !expired && self.request_hash != attempt.request_hash {
            return Decision::Answer(Begin::Mismatch);
        }
        if !expired && self.state == "done" {
            #[allow(clippy::cast_possible_truncation, clippy::cast_sign_loss)]
            let status = self.status.unwrap_or(0.0).round().clamp(0.0, 999.0) as u16;
            return Decision::Answer(Begin::Replay {
                status,
                body: self.body.unwrap_or_default(),
            });
        }
        if !expired && !stale {
            return Decision::Answer(Begin::Running {
                started_at: started,
            });
        }
        // An expired row, or a running one past the time limit, becomes
        // this request's, unless another request changed it first.
        Decision::TakeOver
    }
}

#[derive(Clone, Debug)]
struct Row {
    request_hash: String,
    state: &'static str,
    status: Option<u16>,
    body: Option<String>,
    started_at: i64,
    expires_at: i64,
}

/// A store in the process's memory: for tests, and for an API one process
/// serves alone. It decides as the SQL stores do.
#[derive(Debug, Default)]
pub struct MemoryStore {
    rows: Mutex<HashMap<String, Row>>,
}

impl MemoryStore {
    pub fn new() -> Self {
        Self::default()
    }

    fn rows(&self) -> MutexGuard<'_, HashMap<String, Row>> {
        self.rows.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

#[async_trait]
impl Store for MemoryStore {
    async fn begin(&self, attempt: &Attempt) -> Result<Begin, BoxError> {
        let mut rows = self.rows();
        let started = Row {
            request_hash: attempt.request_hash.clone(),
            state: "running",
            status: None,
            body: None,
            started_at: attempt.now,
            expires_at: attempt.expires_at,
        };
        let Some(row) = rows.get(&attempt.lookup) else {
            rows.insert(attempt.lookup.clone(), started);
            return Ok(Begin::Started);
        };
        let found = Found {
            request_hash: &row.request_hash,
            state: row.state,
            status: row.status.map(f64::from),
            body: row.body.clone(),
            started_at: number(row.started_at),
            expires_at: number(row.expires_at),
        };
        match found.decide(attempt) {
            Decision::Answer(begin) => Ok(begin),
            Decision::TakeOver => {
                rows.insert(attempt.lookup.clone(), started);
                Ok(Begin::Started)
            }
        }
    }

    async fn finish(
        &self,
        lookup: &str,
        started_at: i64,
        status: u16,
        body: &str,
    ) -> Result<(), BoxError> {
        if let Some(row) = self.rows().get_mut(lookup) {
            if row.started_at == started_at && row.state == "running" {
                row.state = "done";
                row.status = Some(status);
                row.body = Some(body.to_string());
            }
        }
        Ok(())
    }

    async fn abandon(&self, lookup: &str, started_at: i64) -> Result<(), BoxError> {
        let mut rows = self.rows();
        if rows
            .get(lookup)
            .is_some_and(|row| row.started_at == started_at && row.state == "running")
        {
            rows.remove(lookup);
        }
        Ok(())
    }

    async fn sweep(&self, now: i64, limit: usize) -> Result<u64, BoxError> {
        let mut rows = self.rows();
        let mut expired: Vec<(i64, String)> = rows
            .iter()
            .filter(|(_, row)| row.expires_at <= now)
            .map(|(lookup, row)| (row.expires_at, lookup.clone()))
            .collect();
        expired.sort();
        expired.truncate(limit);
        for (_, lookup) in &expired {
            rows.remove(lookup);
        }
        Ok(expired.len() as u64)
    }
}

/// One route's `@idempotent`, as [`crate::RouteControls::idempotent`] takes
/// it.
#[derive(Clone)]
pub(crate) struct Idempotent {
    pub(crate) store: Arc<dyn Store>,
    /// The operation, `<namespace>.<name>`.
    pub(crate) operation: Arc<str>,
    /// How long a key's answer is kept.
    pub(crate) ttl: Duration,
    /// Whether a request without the header is refused.
    pub(crate) required: bool,
}

/// Milliseconds since the epoch.
fn now_millis() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| {
            i64::try_from(since.as_millis()).unwrap_or(i64::MAX)
        })
}

fn duration_millis(duration: Duration) -> i64 {
    i64::try_from(duration.as_millis()).unwrap_or(i64::MAX)
}

/// The caller a route keeps a key for: the principal its permission check
/// admitted; for a service that calls with no end user, `service:` and its
/// deployable; empty for a request with neither, a public route's. The Go
/// and TypeScript routers name a caller the same way, so servers over one
/// database share their keys.
pub fn caller_principal(extensions: &http::Extensions) -> String {
    if let Some(principal) = extensions.get::<Principal>() {
        return principal.subject.clone();
    }
    if let Some(caller) = extensions.get::<ServiceCaller>() {
        return format!("service:{}", caller.deployable);
    }
    String::new()
}

/// When each store was last swept, by its address.
fn swept() -> &'static Mutex<HashMap<usize, i64>> {
    static SWEPT: OnceLock<Mutex<HashMap<usize, i64>>> = OnceLock::new();
    SWEPT.get_or_init(Mutex::default)
}

/// Whether `store` is due a sweep at `now`, marking it swept when it is.
fn sweep_due(store: &Arc<dyn Store>, now: i64) -> bool {
    let address = Arc::as_ptr(store).cast::<()>() as usize;
    let mut swept = swept().lock().unwrap_or_else(PoisonError::into_inner);
    let last = swept.get(&address).copied().unwrap_or(i64::MIN);
    if now.saturating_sub(last) < duration_millis(SWEEP_INTERVAL) {
        return false;
    }
    swept.insert(address, now);
    true
}

fn problem(error: ApiError) -> Response {
    error_response(error).into_response()
}

fn internal_error() -> Response {
    problem(ApiError::internal("An unexpected error occurred"))
}

/// What a request sends of a key.
enum Sent {
    None,
    Invalid,
    Key(String),
}

/// The request's key: the header's values joined by a comma and a space, as
/// a Go server reads a repeated header.
fn key_of(request: &Request) -> Sent {
    let mut values = request.headers().get_all(KEY_HEADER).iter().peekable();
    if values.peek().is_none() {
        return Sent::None;
    }
    values
        .map(|value| value.to_str().map(str::to_string))
        .collect::<Result<Vec<_>, _>>()
        .ok()
        .map(|values| values.join(", "))
        .filter(|key| is_key(key))
        .map_or(Sent::Invalid, Sent::Key)
}

/// The 409 of a key whose first request still runs, which started at
/// `started_at`: Retry-After is the seconds until it has run for as long as
/// the route lets it, at least 1.
fn in_flight(started_at: i64, in_flight_ms: i64, now: i64) -> Response {
    let left = started_at.saturating_add(in_flight_ms).saturating_sub(now);
    let retry_after = ((left + 999) / 1000).max(1);
    problem(
        ApiError::new(
            StatusCode::CONFLICT,
            CODE_IN_FLIGHT,
            format!("The first request under this {KEY_HEADER} is still running"),
        )
        .with_details(json!({ "retryAfterSeconds": retry_after }))
        .with_header(RETRY_AFTER, HeaderValue::from(retry_after)),
    )
}

impl Idempotent {
    /// Runs the route under the request's key: `next` runs once per key,
    /// and a repeat is answered what the first request was. `in_flight` is
    /// the route's time limit, and `body_limit` the most of the body the
    /// step reads.
    pub(crate) async fn run(
        self,
        in_flight_limit: Duration,
        body_limit: usize,
        request: Request,
        next: Next,
    ) -> Response {
        let operation = self.operation.as_ref();
        let key = match key_of(&request) {
            Sent::Key(key) => key,
            Sent::None if self.required => {
                return problem(ApiError::new(
                    StatusCode::BAD_REQUEST,
                    CODE_REQUIRED,
                    format!("{operation} needs an {KEY_HEADER} header"),
                ));
            }
            Sent::None => return next.run(request).await,
            Sent::Invalid => {
                return problem(ApiError::new(
                    StatusCode::BAD_REQUEST,
                    CODE_INVALID,
                    format!("{KEY_HEADER} is 1 to 255 printable ASCII characters"),
                ));
            }
        };
        let (parts, body) = request.into_parts();
        let body = match axum::body::to_bytes(body, body_limit).await {
            Ok(body) => body,
            Err(err) if crate::controls::is_length_limit(&err) => {
                return crate::controls::too_large();
            }
            Err(_) => {
                return problem(ApiError::bad_request("The request body could not be read"));
            }
        };
        let path_and_query = parts
            .uri
            .path_and_query()
            .map_or_else(|| parts.uri.path().to_string(), ToString::to_string);
        let principal = caller_principal(&parts.extensions);
        let lookup = lookup(operation, &principal, &key);
        let started_at = now_millis();
        let in_flight_ms = duration_millis(in_flight_limit);
        // A row expires no sooner than its in-flight limit, so a ttl shorter
        // than the route's time limit never lets a repeat take over a first
        // request that may still be running, nor a sweep delete its row.
        let keep = duration_millis(self.ttl.max(in_flight_limit));
        let attempt = Attempt {
            lookup: lookup.clone(),
            operation: operation.to_string(),
            principal,
            key,
            request_hash: request_hash(parts.method.as_str(), &path_and_query, &body),
            now: started_at,
            expires_at: started_at.saturating_add(keep),
            take_over_before: started_at.saturating_sub(in_flight_ms),
        };
        match self.store.begin(&attempt).await {
            Ok(Begin::Started) => {}
            Ok(Begin::Replay { status, body }) => return replay(status, body),
            Ok(Begin::Mismatch) => {
                return problem(ApiError::new(
                    StatusCode::UNPROCESSABLE_ENTITY,
                    CODE_REUSED,
                    format!("This {KEY_HEADER} was used for another request"),
                ));
            }
            Ok(Begin::Running {
                started_at: running,
            }) => return in_flight(running, in_flight_ms, started_at),
            Err(err) => {
                tracing::error!(error = %err, operation, "idempotency: begin");
                return internal_error();
            }
        }
        let store = Arc::clone(&self.store);
        if sweep_due(&store, started_at) {
            let sweeping = Arc::clone(&store);
            spawn_or_run(async move {
                let _ = sweeping.sweep(started_at, SWEEP_LIMIT).await;
            })
            .await;
        }
        // The request's id, which the router's request_ids wrote: a kept
        // problem names the first request, as a kept envelope does.
        let request_id = parts
            .headers
            .get(crate::response::REQUEST_ID_HEADER)
            .and_then(|value| value.to_str().ok())
            .map(str::to_string);
        let request = Request::from_parts(parts, Body::from(body));
        let held = lookup.clone();
        let attempt = async move {
            let response = next.run(request).await;
            let kept = Kept {
                store: store.as_ref(),
                lookup: &held,
                started_at,
                request_id: request_id.as_deref(),
            };
            kept.settle(response).await
        };
        let Ok(runtime) = tokio::runtime::Handle::try_current() else {
            return attempt.await;
        };
        // The handler runs as a task of its own, which finishes and stores
        // its answer when the caller goes away.
        use tracing::Instrument as _;
        match runtime.spawn(attempt.in_current_span()).await {
            Ok(response) => response,
            Err(_) => {
                // A handler that panics stored nothing: the row goes, so a
                // retry runs again.
                abandon(self.store.as_ref(), &lookup, started_at).await;
                internal_error()
            }
        }
    }
}

/// The attempt that started a key's row, which settles the handler's
/// answer.
struct Kept<'a> {
    store: &'a dyn Store,
    lookup: &'a str,
    started_at: i64,
    request_id: Option<&'a str>,
}

impl Kept<'_> {
    /// The handler's answer, settled for its key: one of 500 or more
    /// deletes the row, so a retry runs again; any other is stored as done,
    /// a problem with the request's id, and passed on as it was.
    async fn settle(&self, response: Response) -> Response {
        let status = response.status();
        if status.is_server_error() {
            abandon(self.store, self.lookup, self.started_at).await;
            return response;
        }
        let (mut parts, body) = response.into_parts();
        let Ok(mut bytes) = axum::body::to_bytes(body, usize::MAX).await else {
            // The answer broke off while it was read: nothing was answered.
            abandon(self.store, self.lookup, self.started_at).await;
            return internal_error();
        };
        if let Some(named) = self.named_problem(&parts.headers, &bytes) {
            bytes = named.into();
            parts.headers.remove(http::header::CONTENT_LENGTH);
        }
        // A failure to store the answer fails no call that committed: the
        // row stays running, and a repeat after the time limit runs again.
        let body = String::from_utf8_lossy(&bytes);
        let finished = self
            .store
            .finish(self.lookup, self.started_at, status.as_u16(), &body)
            .await;
        if let Err(err) = finished {
            tracing::error!(error = %err, "idempotency: finish");
        }
        Response::from_parts(parts, Body::from(bytes))
    }

    /// A problem body with the request's id as its `requestId`, which the
    /// router's request_ids adds on the way out, so a replay names the
    /// first request as the Go router's does; `None` for any other body,
    /// and one that names its request already.
    fn named_problem(&self, headers: &http::HeaderMap, bytes: &[u8]) -> Option<String> {
        let id = self.request_id?;
        let problem = headers
            .get(CONTENT_TYPE)
            .and_then(|value| value.to_str().ok())
            .is_some_and(|value| value.starts_with(crate::PROBLEM_CONTENT_TYPE));
        if !problem {
            return None;
        }
        let serde_json::Value::Object(mut body) = serde_json::from_slice(bytes).ok()? else {
            return None;
        };
        if body.contains_key("requestId") {
            return None;
        }
        body.insert("requestId".to_owned(), serde_json::Value::from(id));
        Some(serde_json::Value::Object(body).to_string())
    }
}

/// Deletes the attempt's running row, logging a failure, which leaves the
/// row to the time limit.
async fn abandon(store: &dyn Store, lookup: &str, started_at: i64) {
    if let Err(err) = store.abandon(lookup, started_at).await {
        tracing::error!(error = %err, "idempotency: abandon");
    }
}

/// A stored answer, as a repeat is given it: its status and body, JSON or a
/// problem by its status, marked with `Idempotent-Replay: true`.
fn replay(status: u16, body: String) -> Response {
    let status = StatusCode::from_u16(status).unwrap_or(StatusCode::OK);
    let content_type = if status.is_client_error() || status.is_server_error() {
        crate::PROBLEM_CONTENT_TYPE
    } else {
        "application/json"
    };
    let body = if status == StatusCode::NO_CONTENT || status == StatusCode::NOT_MODIFIED {
        Body::empty()
    } else {
        Body::from(body)
    };
    let mut response = Response::new(body);
    *response.status_mut() = status;
    let headers = response.headers_mut();
    headers.insert(CONTENT_TYPE, HeaderValue::from_static(content_type));
    headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
    headers.insert(REPLAY_HEADER, HeaderValue::from_static("true"));
    response
}

/// Runs `work` as a task of its own on tokio, and to its end where it
/// stands on no runtime.
async fn spawn_or_run(work: impl std::future::Future<Output = ()> + Send + 'static) {
    match tokio::runtime::Handle::try_current() {
        Ok(runtime) => {
            runtime.spawn(work);
        }
        Err(_) => work.await,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_key_is_printable_ascii_of_1_to_255() {
        assert!(is_key("a"));
        assert!(is_key(&"k".repeat(255)));
        assert!(is_key("two words"));
        for key in [
            "",
            &"k".repeat(256),
            "line\nfeed",
            "tab\tbed",
            "café",
            "\u{7f}",
        ] {
            assert!(!is_key(key), "{key:?}");
        }
    }

    #[test]
    fn a_running_row_holds_its_key_until_its_time_limit() {
        let response = in_flight(1_000, 60_000, 31_000);
        assert_eq!(response.status(), StatusCode::CONFLICT);
        assert_eq!(response.headers()[RETRY_AFTER], "30");
        assert_eq!(in_flight(0, 60_000, 90_000).headers()[RETRY_AFTER], "1");
    }

    #[test]
    fn a_replay_is_marked_and_typed_by_its_status() {
        let ok = replay(201, "{\"data\":1}".into());
        assert_eq!(ok.status(), StatusCode::CREATED);
        assert_eq!(ok.headers()[CONTENT_TYPE], "application/json");
        assert_eq!(ok.headers()[REPLAY_HEADER], "true");
        assert_eq!(ok.headers()[CACHE_CONTROL], "no-store");
        let refused = replay(404, "{}".into());
        assert_eq!(refused.headers()[CONTENT_TYPE], crate::PROBLEM_CONTENT_TYPE);
    }
}
