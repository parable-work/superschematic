//! The engine's routes as an axum router, which a Rust server mounts (D58,
//! the `axum` feature): the adapter between axum and the transport-neutral
//! [`api`], as `runtime/engine/go`'s `enginehttp` is chi's and the Hono
//! adapter TypeScript's.
//!
//! [`router`] registers the path of each of the engine's routes
//! ([`api::routes`]) under the prefix, axum's `{param}` syntax being the
//! engine's, for every method.
//! A request one of them takes becomes an [`api::Request`]: its method,
//! its path still percent-encoded with the prefix it was mounted under,
//! its query, its headers, its body, its request id and the caller the
//! host established ([`Callers`]). [`api::handle`] serves it on tokio's
//! blocking pool, since every engine call is synchronous and may wait on
//! its database, and its [`api::Response`] becomes axum's: the status, the
//! headers, and the body's bytes or, for the event stream, a body that
//! pulls the stream's frames as the client takes them.
//!
//! The router keeps, of the host's steps: the request id (the
//! `X-Request-ID` the host's middleware settled, the HTTP runtime's
//! `request_ids`, or the client's when it is usable, else a fresh UUID, as
//! the runtime's `request_id_of` reads it), and the body limit of a route
//! that takes a body ([`api::Route::takes_body`]), 413
//! `payload_too_large` past [`Options::body_limit`], which it must read
//! the body under anyway. A request on a route's path with a method none
//! of its routes take is the engine's 404, as its own routing answers it
//! (not axum's 405); a path under the prefix that names no route at all is
//! not the router's, and reaches the host's fallback. Rate limits,
//! timeouts and CORS stay the host's layers (D58): [`route_of_request`]
//! names the engine route a request is for, whose `stream` and `mcp` say
//! whether a layer that bounds a response's body would cut it.
//!
//! An event stream is pulled without a thread of its own: each pull reads
//! the log from the stream's cursor on the blocking pool and returns at
//! once, and while the stream has nothing to send it waits, as a future,
//! for the storage's notifier to move (one thread per router waits on the
//! notifier for every stream it serves and wakes them) or for its
//! heartbeat. When the client goes away, the server drops the body, which
//! closes the stream. [`Engine::shutdown`] ends every stream.
//!
//! The engine is synchronous, so a host calls it off its async threads
//! (`spawn_blocking`), [`Engine::shutdown`] and the engine's last drop
//! included: on Postgres the engine's client blocks on a runtime of its
//! own, which cannot start on an async thread. The router does so for
//! what it holds: a request, a stream's pulls, and the engine when the
//! router drops on the runtime.
//!
//! ```ignore
//! let engine = Engine::open_shared(options)?;
//! let routes = superschematic_engine::axum::router(
//!     Arc::clone(&engine),
//!     axum::Options::new("/engine/shop").callers(|request: &Parts| caller_of(request)),
//! )?;
//! let app = Router::new().merge(routes).fallback_service(api_router);
//! ```

use std::convert::Infallible;
use std::fmt;
use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Weak};
use std::time::{Duration, Instant};

use ::axum::body::{Body as AxumBody, Bytes};
use ::axum::extract::{OriginalUri, Request as AxumRequest};
use ::axum::http::request::Parts;
use ::axum::http::{HeaderMap, HeaderName, HeaderValue, StatusCode};
use ::axum::response::Response as AxumResponse;
use ::axum::routing::any;
use ::axum::Router;
use futures_util::{Stream, StreamExt};
use tokio::sync::watch;

use crate::api::{self, ApiOptions, Body, Caller, EventStream, Problem, Pull, Route};
use crate::storage::Notifier;
use crate::Engine;

/// The header a request's id travels in.
const REQUEST_ID: &str = "x-request-id";

/// The longest request id the router keeps, as the HTTP runtimes keep it.
const MAX_REQUEST_ID_LENGTH: usize = 128;

/// How long the thread that wakes a router's streams waits on the notifier
/// before it looks whether any stream is left to wake.
const WATCH_TICK: Duration = Duration::from_secs(1);

/// How far past its heartbeat a waiting stream is pulled again, so the pull
/// finds the beat due.
const BEAT_SLACK: Duration = Duration::from_millis(2);

/// The future a [`Callers`] answers with.
pub type CallerFuture<'a> = Pin<Box<dyn Future<Output = Caller> + Send + 'a>>;

/// How the router learns who a request's caller is: what the host's
/// authentication established (D50's end user, D37's calling service, or
/// a refusal the engine answers in its place), as [`api::Caller`] holds
/// it. The router asks once a route takes the request and its body is
/// within the limit, as the HTTP runtimes order their steps, and only the
/// request's head is asked about.
///
/// A function of the request's head is one ([`FromExtensions`] reads what
/// a host's middleware put in the request's extensions); a host whose
/// authentication is asynchronous, as the HTTP runtime's authenticators
/// are, implements it on its own type:
///
/// ```ignore
/// impl Callers for EngineCallers {
///     fn caller<'a>(&'a self, request: &'a Parts) -> CallerFuture<'a> {
///         Box::pin(async move {
///             let service = match self.services.authenticate(request).await {
///                 Ok(service) => service,
///                 Err(refusal) => return Caller { refused: Some(refusal_of(refusal)), ..Caller::default() },
///             };
///             Caller { principal: self.users.authenticate(request).await.ok().flatten(), service, refused: None }
///         })
///     }
/// }
/// ```
pub trait Callers: Send + Sync + 'static {
    /// The caller of the request whose head is `request`.
    fn caller<'a>(&'a self, request: &'a Parts) -> CallerFuture<'a>;
}

impl<F> Callers for F
where
    F: Fn(&Parts) -> Caller + Send + Sync + 'static,
{
    fn caller<'a>(&'a self, request: &'a Parts) -> CallerFuture<'a> {
        Box::pin(std::future::ready(self(request)))
    }
}

/// The [`Caller`] a host's middleware put in the request's extensions
/// (`request.extensions_mut().insert(caller)`), or no caller (401 on every
/// route) when it put none: the default.
#[derive(Clone, Copy, Debug, Default)]
pub struct FromExtensions;

impl Callers for FromExtensions {
    fn caller<'a>(&'a self, request: &'a Parts) -> CallerFuture<'a> {
        Box::pin(std::future::ready(
            request
                .extensions
                .get::<Caller>()
                .cloned()
                .unwrap_or_default(),
        ))
    }
}

/// Which of the engine's routes a router serves.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Serves {
    /// The 26 routes of the HTTP API and the MCP endpoint: what a server
    /// mounts.
    #[default]
    All,
    /// The HTTP API's 26 routes alone, as the TypeScript `engineApp`.
    Http,
    /// The MCP endpoint alone, as the TypeScript `engineMcp`.
    Mcp,
}

impl Serves {
    fn includes(self, route: &Route) -> bool {
        match self {
            Serves::All => true,
            Serves::Http => !route.mcp,
            Serves::Mcp => route.mcp,
        }
    }
}

/// How a router mounts the engine.
#[derive(Clone)]
pub struct Options {
    /// The path the routes sit under: `/engine/shop`, or empty for the
    /// root. A trailing slash is dropped. A host that nests the router
    /// itself (`Router::nest`) leaves it empty, and the engine reads the
    /// full prefix from the request's original URI, so `Location` keeps
    /// it either way.
    pub prefix: String,
    /// The engine's own options for its routes: the event stream's page
    /// size and heartbeat, the MCP endpoint's server info, the tool
    /// filter.
    pub api: ApiOptions,
    /// Who each request's caller is ([`FromExtensions`] unless set).
    pub callers: Arc<dyn Callers>,
    /// Which routes the router serves ([`Serves::All`] unless set).
    pub serves: Serves,
    /// The largest body, in bytes, a route that takes one reads
    /// ([`api::DEFAULT_BODY_LIMIT_BYTES`] unless set); a larger one is 413
    /// `payload_too_large` before the caller is asked. The MCP endpoint
    /// reads up to its own cap and refuses past it as JSON-RPC.
    pub body_limit: usize,
}

impl Default for Options {
    fn default() -> Self {
        Options {
            prefix: String::new(),
            api: ApiOptions::default(),
            callers: Arc::new(FromExtensions),
            serves: Serves::All,
            body_limit: api::DEFAULT_BODY_LIMIT_BYTES,
        }
    }
}

impl fmt::Debug for Options {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Options")
            .field("prefix", &self.prefix)
            .field("api", &self.api)
            .field("serves", &self.serves)
            .field("body_limit", &self.body_limit)
            .finish_non_exhaustive()
    }
}

impl Options {
    /// The defaults, under `prefix`.
    pub fn new(prefix: impl Into<String>) -> Self {
        Options {
            prefix: prefix.into(),
            ..Options::default()
        }
    }

    /// The engine's own options for its routes.
    pub fn api(mut self, api: ApiOptions) -> Self {
        self.api = api;
        self
    }

    /// Who each request's caller is.
    pub fn callers(mut self, callers: impl Callers) -> Self {
        self.callers = Arc::new(callers);
        self
    }

    /// Which routes the router serves.
    pub fn serves(mut self, serves: Serves) -> Self {
        self.serves = serves;
        self
    }

    /// The body limit of a route that takes a body.
    pub fn body_limit(mut self, bytes: usize) -> Self {
        self.body_limit = bytes;
        self
    }
}

/// Options [`router`] refuses: a prefix axum cannot route, stream options
/// out of range, or an engine that is not shared.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InvalidOptions(pub String);

impl fmt::Display for InvalidOptions {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for InvalidOptions {}

/// The engine's routes as an axum router, under `options.prefix`: what a
/// server merges into its own (`Router::merge`), beside its API's routes.
/// The engine must be shared ([`Engine::shared`], [`Engine::open_shared`]),
/// since an event stream outlives the request that opened it.
pub fn router(engine: Arc<Engine>, options: Options) -> Result<Router, InvalidOptions> {
    // Held first, so a refusal lets the engine go off the async runtime.
    let engine = Held(Some(engine));
    let prefix = prefix_of(&options.prefix)?;
    let (_, heartbeat) = options.api.stream.check().map_err(InvalidOptions)?;
    if engine.handle().is_none() {
        return Err(InvalidOptions(
            "the engine must be shared (Engine::shared or Engine::open_shared): an event stream outlives the request that opened it".to_owned(),
        ));
    }
    let mount = Arc::new(Mount {
        wakes: Arc::new(Wakes::new(&engine)),
        engine,
        api: options.api,
        prefix,
        callers: options.callers,
        serves: options.serves,
        body_limit: options.body_limit,
        heartbeat,
    });
    // Each path the routes it serves have, with every method: the engine
    // routes the method itself, answering one no route on the path takes
    // 404, as its own routing does, where a method router would answer 405
    // with an Allow header.
    let mut paths: Vec<&'static str> = Vec::new();
    for route in api::ROUTES
        .iter()
        .filter(|route| mount.serves.includes(route))
    {
        if !paths.contains(&route.path) {
            paths.push(route.path);
        }
    }
    let mut router = Router::new();
    for path in paths {
        router = router.route(&format!("{}{path}", mount.prefix), any(handler(&mount)));
    }
    Ok(router)
}

/// The engine route a request to a router under `prefix` is for, by its
/// method and its path: what a host's own layers read. A layer that
/// bounds the time to a response's head (tower-http's `TimeoutLayer`, the
/// HTTP runtime's `@timeout`) leaves the event stream open, since its head
/// goes out at once; one that bounds a response's body, or its idle time,
/// must leave out a route whose `stream` or `mcp` is set.
pub fn route_of_request<B>(prefix: &str, request: &::axum::http::Request<B>) -> Option<Route> {
    let prefix = prefix.trim_end_matches('/');
    let rest = request.uri().path().strip_prefix(prefix)?;
    api::route_of(request.method().as_str(), rest)
}

// prefix_of is a prefix axum can route: empty, or a path that starts with
// `/`, its trailing slash dropped, with nothing axum reads as a parameter.
fn prefix_of(prefix: &str) -> Result<String, InvalidOptions> {
    let trimmed = prefix.trim_end_matches('/');
    if trimmed.is_empty() {
        return Ok(String::new());
    }
    if !trimmed.starts_with('/')
        || trimmed.contains(['{', '}', '*', '?', '#'])
        || trimmed.contains("//")
    {
        return Err(InvalidOptions(format!(
            "the prefix must be empty or a path that starts with / and holds no {{, }}, *, ?, # or //, got {prefix:?}"
        )));
    }
    Ok(trimmed.to_owned())
}

/// What a router's handlers share.
struct Mount {
    engine: Held,
    api: ApiOptions,
    prefix: String,
    callers: Arc<dyn Callers>,
    serves: Serves,
    body_limit: usize,
    heartbeat: Duration,
    wakes: Arc<Wakes>,
}

/// The engine a router holds, let go of off the async runtime when the
/// router drops on it: the Postgres client the engine closes as it drops
/// blocks on a runtime of its own, which cannot start inside another.
struct Held(Option<Arc<Engine>>);

impl std::ops::Deref for Held {
    type Target = Arc<Engine>;

    fn deref(&self) -> &Arc<Engine> {
        self.0
            .as_ref()
            .expect("the engine is held until the router drops")
    }
}

impl Drop for Held {
    fn drop(&mut self) {
        let Some(engine) = self.0.take() else {
            return;
        };
        if tokio::runtime::Handle::try_current().is_ok() {
            // If the thread cannot start, the engine drops here with it.
            let _ = std::thread::Builder::new()
                .name("engine-release".to_owned())
                .spawn(move || drop(engine));
        }
    }
}

fn handler(
    mount: &Arc<Mount>,
) -> impl Fn(AxumRequest) -> Pin<Box<dyn Future<Output = AxumResponse> + Send>>
       + Clone
       + Send
       + Sync
       + 'static {
    let mount = Arc::clone(mount);
    move |request: AxumRequest| Box::pin(serve(Arc::clone(&mount), request))
}

// serve takes one request through the host's steps the router keeps, then
// the engine's routes, on the blocking pool.
async fn serve(mount: Arc<Mount>, request: AxumRequest) -> AxumResponse {
    let (parts, body) = request.into_parts();
    let request_id = request_id_of(&parts.headers);
    let (prefix, path) = mount.paths(&parts);
    let rest = path.strip_prefix(prefix.as_str()).unwrap_or(&path);
    let route =
        api::route_of(parts.method.as_str(), rest).filter(|route| mount.serves.includes(route));
    let Some(route) = route else {
        // A method no route on the path takes: the engine's own 404,
        // before the caller is asked.
        return answer(
            Problem::not_found("Not Found").response(&request_id),
            &mount,
        );
    };
    let body = if route.takes_body() {
        if declared_length(&parts.headers).is_some_and(|length| length > mount.body_limit as u64) {
            return answer(too_large(&request_id), &mount);
        }
        match read(body, mount.body_limit).await {
            Ok(Read::Whole(bytes)) => Some(bytes),
            Ok(Read::Over(_)) => return answer(too_large(&request_id), &mount),
            Err(_) => return answer(unreadable(&request_id), &mount),
        }
    } else if route.mcp && route.method == "POST" {
        // MCP refuses a body past its cap itself, after its media-type
        // checks, so the router hands it what it read past the cap.
        match read(body, api::mcp::MAX_BODY_BYTES).await {
            Ok(Read::Whole(bytes) | Read::Over(bytes)) => Some(bytes),
            Err(_) => return answer(unreadable(&request_id), &mount),
        }
    } else {
        None
    };
    let caller = mount.callers.caller(&parts).await;
    let request = api::Request {
        method: parts.method.as_str().to_owned(),
        prefix,
        path,
        query: parts.uri.query().unwrap_or_default().to_owned(),
        headers: headers_of(&parts.headers),
        body: body.filter(|bytes| !bytes.is_empty()),
        caller,
        request_id: request_id.clone(),
    };
    let worker = Arc::clone(&mount);
    let served =
        tokio::task::spawn_blocking(move || api::handle(&worker.engine, &worker.api, &request))
            .await;
    match served {
        Ok(response) => answer(response, &mount),
        // A panic the engine's own boundary did not catch.
        Err(_) => answer(Problem::internal().response(&request_id), &mount),
    }
}

impl Mount {
    // paths are the prefix the request was mounted under and its path, the
    // prefix included: the router's own, or, when the host nested the
    // router, the original URI's, so Location keeps the host's prefix too.
    fn paths(&self, parts: &Parts) -> (String, String) {
        let inner = parts.uri.path();
        let rest = inner.strip_prefix(self.prefix.as_str()).unwrap_or(inner);
        if let Some(original) = parts.extensions.get::<OriginalUri>() {
            let full = original.path();
            if let Some(outer) = full.strip_suffix(rest) {
                return (outer.to_owned(), full.to_owned());
            }
        }
        (self.prefix.clone(), inner.to_owned())
    }
}

/// A request's id: its `X-Request-ID` when it is non-empty, at most 128
/// characters long and free of control characters (what the host's
/// middleware settled, or the client's), else a fresh UUID v4, as the HTTP
/// runtimes' `request_id_of` reads it.
fn request_id_of(headers: &HeaderMap) -> String {
    headers
        .get(REQUEST_ID)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .filter(|id| {
            !id.is_empty()
                && id.chars().count() <= MAX_REQUEST_ID_LENGTH
                && !id.chars().any(char::is_control)
        })
        .map_or_else(|| uuid::Uuid::new_v4().to_string(), str::to_owned)
}

// headers_of is a request's headers, a name sent more than once its values
// joined with ", ", as the Fetch API's Headers reads them.
fn headers_of(map: &HeaderMap) -> Vec<(String, String)> {
    map.keys()
        .map(|name| {
            let values: Vec<String> = map
                .get_all(name)
                .iter()
                .map(|value| String::from_utf8_lossy(value.as_bytes()).into_owned())
                .collect();
            (name.as_str().to_owned(), values.join(", "))
        })
        .collect()
}

fn declared_length(headers: &HeaderMap) -> Option<u64> {
    headers
        .get(::axum::http::header::CONTENT_LENGTH)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.trim().parse().ok())
}

// What reading a body under a limit gave: all of it, or the bytes read
// once they passed the limit.
enum Read {
    Whole(Vec<u8>),
    Over(Vec<u8>),
}

async fn read(body: AxumBody, limit: usize) -> Result<Read, ::axum::Error> {
    let mut bytes = Vec::new();
    let mut chunks = body.into_data_stream();
    while let Some(chunk) = chunks.next().await {
        bytes.extend_from_slice(&chunk?);
        if bytes.len() > limit {
            return Ok(Read::Over(bytes));
        }
    }
    Ok(Read::Whole(bytes))
}

// too_large is the HTTP runtimes' body-limit refusal.
fn too_large(request_id: &str) -> api::Response {
    Problem::new(
        413,
        "Request body exceeds the accepted size",
        "payload_too_large",
    )
    .response(request_id)
}

fn unreadable(request_id: &str) -> api::Response {
    Problem::bad_request("The request body could not be read").response(request_id)
}

// answer is the engine's response as axum's: a stream's body pulls it.
fn answer(response: api::Response, mount: &Mount) -> AxumResponse {
    let body = match response.body {
        Body::Empty => AxumBody::empty(),
        Body::Bytes(bytes) => AxumBody::from(bytes),
        Body::Stream(stream) => AxumBody::from_stream(frames(stream, mount)),
    };
    let mut answered = AxumResponse::new(body);
    *answered.status_mut() =
        StatusCode::from_u16(response.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    let headers = answered.headers_mut();
    for (name, value) in response.headers {
        if let (Ok(name), Ok(value)) = (
            HeaderName::from_bytes(name.as_bytes()),
            HeaderValue::from_str(&value),
        ) {
            headers.append(name, value);
        }
    }
    answered
}

// frames is a stream's body: each item the bytes of one pull that sent
// something, until the stream ends.
fn frames(
    stream: Box<EventStream>,
    mount: &Mount,
) -> impl Stream<Item = Result<Bytes, Infallible>> + Send + 'static {
    let pulling = Pulling {
        receiver: mount.wakes.subscribe(),
        stream: Some(stream),
        heartbeat: mount.heartbeat,
        idle_since: None,
        _wakes: Arc::clone(&mount.wakes),
    };
    futures_util::stream::unfold(pulling, |mut pulling| async move {
        let bytes = pulling.frame().await?;
        Some((Ok(bytes), pulling))
    })
}

/// One stream's body: the stream between its pulls, and how it waits.
struct Pulling {
    /// `None` while a pull runs on the blocking pool, which holds it.
    stream: Option<Box<EventStream>>,
    receiver: watch::Receiver<u64>,
    heartbeat: Duration,
    /// Since when the stream has had nothing to send: its heartbeat is
    /// due one heartbeat after.
    idle_since: Option<Instant>,
    /// The wakes it subscribed to, kept while it waits.
    _wakes: Arc<Wakes>,
}

impl Pulling {
    // frame pulls the stream until it sends something or ends, waiting
    // between pulls for a commit or its heartbeat.
    async fn frame(&mut self) -> Option<Bytes> {
        loop {
            let mut stream = self.stream.take()?;
            self.receiver.borrow_and_update();
            // The stream reaches its engine only on the blocking pool, so
            // the engine never drops on an async thread.
            let pulled = tokio::task::spawn_blocking(move || {
                let notifier = stream
                    .notifier()
                    .map(|notifier| (notifier.epoch(), notifier));
                let pull = stream.next(Duration::ZERO);
                (stream, pull, notifier)
            })
            .await;
            // A pull that panicked ends the stream.
            let Ok((stream, pull, notifier)) = pulled else {
                return None;
            };
            match pull {
                Pull::Data(bytes) => {
                    self.stream = Some(stream);
                    self.idle_since = None;
                    return Some(Bytes::from(bytes));
                }
                Pull::End => return None,
                Pull::Pending => {
                    self.stream = Some(stream);
                    let idle = *self.idle_since.get_or_insert_with(Instant::now);
                    // A commit since the pull read, or an engine closed
                    // since: pull again now.
                    let moved = match &notifier {
                        Some((epoch, notifier)) => notifier.epoch() > *epoch,
                        None => true,
                    };
                    if !moved {
                        let beat =
                            tokio::time::Instant::from_std(idle + self.heartbeat + BEAT_SLACK);
                        let _ = tokio::time::timeout_at(beat, self.receiver.changed()).await;
                    }
                }
            }
        }
    }
}

impl Drop for Pulling {
    // The body dropped: the client went away, or the server let it go.
    fn drop(&mut self) {
        if let Some(stream) = self.stream.as_mut() {
            stream.close();
        }
    }
}

/// The streams a router serves wake through one channel, which a thread
/// of the router's own moves on each time the storage's notifier does:
/// one blocked thread for every stream, however many are open.
struct Wakes {
    engine: Weak<Engine>,
    notifier: Arc<Notifier>,
    sender: watch::Sender<u64>,
    /// Whether the thread runs; it stops when no stream is left, and the
    /// next stream starts it again.
    watching: AtomicBool,
}

impl Wakes {
    fn new(engine: &Arc<Engine>) -> Wakes {
        Wakes {
            engine: Arc::downgrade(engine),
            notifier: Arc::clone(engine.db().notifier()),
            sender: watch::Sender::new(0),
            watching: AtomicBool::new(false),
        }
    }

    // subscribe is a stream's receiver, the thread started if it is not
    // running, from the epoch now, which is before the stream's next pull.
    fn subscribe(self: &Arc<Self>) -> watch::Receiver<u64> {
        let receiver = self.sender.subscribe();
        if !self.watching.swap(true, Ordering::SeqCst) {
            let notifier = Arc::clone(&self.notifier);
            let epoch = notifier.epoch();
            let wakes = Arc::downgrade(self);
            let started = std::thread::Builder::new()
                .name("engine-stream-wakes".to_owned())
                .spawn(move || watch_notifier(&wakes, &notifier, epoch));
            if started.is_err() {
                // No thread: the streams wake on their heartbeats alone.
                self.watching.store(false, Ordering::SeqCst);
            }
        }
        receiver
    }
}

// watch_notifier moves the wakes on each time the notifier does, until the
// engine closes, the router and its streams are gone, or no stream is left.
fn watch_notifier(wakes: &Weak<Wakes>, notifier: &Notifier, mut last: u64) {
    loop {
        let now = notifier.wait(last, WATCH_TICK);
        // Not held across the wait, so the router and its streams can go.
        let Some(wakes) = wakes.upgrade() else {
            return;
        };
        if now > last {
            last = now;
            wakes.sender.send_replace(now);
        }
        let closed = wakes
            .engine
            .upgrade()
            .is_none_or(|engine| engine.is_closed());
        if closed {
            // Shutdown bumped the notifier, which woke every stream to its
            // end; one more wake for any that subscribed since.
            wakes.sender.send_replace(last.wrapping_add(1));
            wakes.watching.store(false, Ordering::SeqCst);
            return;
        }
        if wakes.sender.receiver_count() == 0 {
            wakes.watching.store(false, Ordering::SeqCst);
            // A stream that subscribed after the count was read either
            // started a thread of its own or finds this one running.
            if wakes.sender.receiver_count() == 0 || wakes.watching.swap(true, Ordering::SeqCst) {
                return;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_prefix_is_a_path_axum_routes() {
        assert_eq!(prefix_of("").unwrap(), "");
        assert_eq!(prefix_of("/").unwrap(), "");
        assert_eq!(prefix_of("/engine/shop/").unwrap(), "/engine/shop");
        assert!(prefix_of("engine").is_err());
        assert!(prefix_of("/engine/{db}").is_err());
        assert!(prefix_of("/engine//shop").is_err());
    }

    #[test]
    fn a_request_id_is_the_header_when_usable_else_a_fresh_one() {
        let mut headers = HeaderMap::new();
        headers.insert(REQUEST_ID, HeaderValue::from_static(" req-7 "));
        assert_eq!(request_id_of(&headers), "req-7");
        headers.insert(REQUEST_ID, HeaderValue::from_str(&"x".repeat(129)).unwrap());
        assert_eq!(request_id_of(&headers).len(), 36);
        assert_eq!(request_id_of(&HeaderMap::new()).len(), 36);
    }

    #[test]
    fn a_header_sent_twice_is_one_value() {
        let mut headers = HeaderMap::new();
        headers.append("accept", HeaderValue::from_static("text/event-stream"));
        headers.append("accept", HeaderValue::from_static("application/json"));
        headers.append("if-match", HeaderValue::from_static("\"1\""));
        assert_eq!(
            headers_of(&headers),
            vec![
                (
                    "accept".to_owned(),
                    "text/event-stream, application/json".to_owned()
                ),
                ("if-match".to_owned(), "\"1\"".to_owned()),
            ]
        );
    }

    #[test]
    fn a_route_names_the_engine_route_under_the_prefix() {
        let request = ::axum::http::Request::get("/engine/shop/namespaces/default/events")
            .body(())
            .unwrap();
        let route = route_of_request("/engine/shop/", &request).unwrap();
        assert_eq!(route.name, "readEvents");
        assert!(route.stream);
        let request = ::axum::http::Request::post("/engine/shop/namespaces/default/mcp")
            .body(())
            .unwrap();
        assert!(route_of_request("/engine/shop", &request).unwrap().mcp);
        let request = ::axum::http::Request::get("/other/namespaces")
            .body(())
            .unwrap();
        assert_eq!(route_of_request("/engine/shop", &request), None);
    }
}
