//! The routes over HTTP as an axum router (the `server` feature, D117): the
//! handler's transport-free router ([`crate::Dev::http`]) behind one
//! fallback route, each call of the store on tokio's blocking pool, the
//! event stream as a body that pulls the event queue, and the MCP endpoint
//! through the HTTP runtime's stateless transport
//! (`superschematic_http_runtime::mcp`), over the caller's tools.
//!
//! The host keeps authentication: [`Options::authenticate`] reads the
//! caller from a request's headers, and a request it reads none from is
//! 401. [`dev_callers`] is development's own: a subject and permissions
//! from two headers, or a default caller, for a server only its developer
//! reaches.
//!
//! A stream reads the queue from its cursor each time it is pulled: what it
//! holds after the cursor, the `ready` event once it has caught up, then a
//! read each [`Options::poll`] and a heartbeat comment each
//! [`Options::heartbeat`] while nothing comes. A refusal of a read (the
//! policy changed) ends it.

use std::sync::{Arc, Mutex, PoisonError};
use std::time::{Duration, Instant};

use async_trait::async_trait;
use axum::body::Body;
use axum::extract::{Request as AxumRequest, State};
use axum::response::Response;
use axum::Router;
use bytes::Bytes;
use serde_json::{Map, Value};
use superschematic_http_runtime::mcp::{
    self, CallOutcome, Options as McpOptions, ServerInfo, Tool, ToolError, ToolSet,
};
use superschematic_rules::Caller;
use superschematic_rules_handler::events::{self, EventsQuery};
use superschematic_rules_handler::http::{refusal_problem, HttpRequest, HttpResponse};
use superschematic_rules_handler::Store;

use crate::{Dev, Served};

/// The host shared by every request: one store, one writer at a time.
pub type Shared<S> = Arc<Mutex<Dev<S>>>;

/// Reads the caller a request's headers establish; `None` for none (401).
pub type Authenticate = Arc<dyn Fn(&http::HeaderMap) -> Option<Caller> + Send + Sync>;

/// The largest body a route other than MCP's reads, the HTTP runtimes'
/// default: 1 MiB.
pub const BODY_LIMIT: usize = 1024 * 1024;

/// The header development's callers name a subject by.
pub const SUBJECT_HEADER: &str = "x-superschematic-subject";
/// The header development's callers name their permissions by, separated
/// by commas.
pub const PERMISSIONS_HEADER: &str = "x-superschematic-permissions";

/// How the router serves.
#[derive(Clone)]
pub struct Options {
    pub authenticate: Authenticate,
    /// How long a waiting stream goes between reads of the queue.
    pub poll: Duration,
    /// How long a waiting stream goes between heartbeats.
    pub heartbeat: Duration,
    /// The name and version the MCP endpoint reports.
    pub server_info: ServerInfo,
}

impl Options {
    /// The options with a caller from `authenticate`, a read every 200
    /// milliseconds and a heartbeat every 5 seconds, the engine's.
    pub fn new(authenticate: Authenticate) -> Self {
        Options {
            authenticate,
            poll: Duration::from_millis(200),
            heartbeat: Duration::from_millis(5_000),
            server_info: ServerInfo::new("superschematic-dev", env!("CARGO_PKG_VERSION")),
        }
    }
}

/// Development's callers: the subject [`SUBJECT_HEADER`] names, with the
/// permissions [`PERMISSIONS_HEADER`] lists, or `default` when a request
/// names none. Anyone who reaches the server is whoever they say, so it is
/// for a server only its developer reaches.
pub fn dev_callers(default: Option<Caller>) -> Authenticate {
    Arc::new(move |headers: &http::HeaderMap| {
        let header = |name: &str| {
            headers
                .get(name)
                .and_then(|value| value.to_str().ok())
                .map(str::trim)
                .filter(|value| !value.is_empty())
        };
        match header(SUBJECT_HEADER) {
            Some(subject) => {
                let permissions: Vec<String> = header(PERMISSIONS_HEADER)
                    .unwrap_or_default()
                    .split(',')
                    .map(str::trim)
                    .filter(|permission| !permission.is_empty())
                    .map(str::to_owned)
                    .collect();
                Some(Caller::new(subject, permissions))
            }
            None => default.clone(),
        }
    })
}

struct Routes<S> {
    dev: Shared<S>,
    options: Options,
}

impl<S> Clone for Routes<S> {
    fn clone(&self) -> Self {
        Routes {
            dev: Arc::clone(&self.dev),
            options: self.options.clone(),
        }
    }
}

/// The router: every path to the handler's routes, a path no route serves
/// the handler's 404.
pub fn router<S: Store + Send + 'static>(dev: Shared<S>, options: Options) -> Router {
    Router::new()
        .fallback(serve::<S>)
        .with_state(Routes { dev, options })
}

/// Runs a call of the host on tokio's blocking pool, the store's one writer
/// held for it.
async fn blocking<S, T>(dev: &Shared<S>, call: impl FnOnce(&mut Dev<S>) -> T + Send + 'static) -> T
where
    S: Store + Send + 'static,
    T: Send + 'static,
{
    let dev = Arc::clone(dev);
    tokio::task::spawn_blocking(move || {
        let mut guard = dev.lock().unwrap_or_else(PoisonError::into_inner);
        call(&mut guard)
    })
    .await
    .unwrap_or_else(|error| std::panic::resume_unwind(error.into_panic()))
}

fn response_of(answer: HttpResponse) -> Response {
    let mut builder = Response::builder().status(answer.status);
    for (name, value) in &answer.headers {
        builder = builder.header(name, value);
    }
    builder
        .body(Body::from(answer.body))
        .unwrap_or_else(|_| Response::new(Body::empty()))
}

async fn serve<S: Store + Send + 'static>(
    State(routes): State<Routes<S>>,
    request: AxumRequest,
) -> Response {
    let (parts, body) = request.into_parts();
    let request_id = parts
        .headers
        .get("x-request-id")
        .and_then(|value| value.to_str().ok())
        .filter(|value| !value.is_empty())
        .map_or_else(|| uuid::Uuid::new_v4().to_string(), str::to_owned);
    let mcp_path = parts.uri.path().ends_with("/mcp");
    let limit = if mcp_path {
        mcp::MCP_MAX_BODY_BYTES
    } else {
        BODY_LIMIT
    };
    let Ok(bytes) = axum::body::to_bytes(body, limit).await else {
        return response_of(superschematic_rules_handler::http::problem(
            413,
            "Request body exceeds the accepted size",
            "payload_too_large",
            None,
            &request_id,
        ));
    };
    let caller = (routes.options.authenticate)(&parts.headers);
    let http_request = HttpRequest {
        method: parts.method.as_str().to_owned(),
        path: parts.uri.path().to_owned(),
        query: parts.uri.query().unwrap_or_default().to_owned(),
        headers: parts
            .headers
            .iter()
            .filter_map(|(name, value)| {
                value
                    .to_str()
                    .ok()
                    .map(|value| (name.as_str().to_owned(), value.to_owned()))
            })
            .collect(),
        body: (!bytes.is_empty()).then(|| bytes.to_vec()),
    };
    let id = request_id.clone();
    let served = blocking(&routes.dev, move |dev| {
        dev.http(caller.as_ref(), &http_request, &id)
    })
    .await;
    match served {
        Served::Response(answer) => response_of(answer),
        Served::Stream { query, caller } => stream(routes, query, caller).await,
        Served::Mcp { caller } => {
            let mut request = http::Request::builder()
                .method(parts.method.clone())
                .uri(parts.uri.clone());
            for (name, value) in &parts.headers {
                request = request.header(name, value);
            }
            let Ok(request) = request.body(bytes) else {
                return response_of(superschematic_rules_handler::http::problem(
                    400,
                    "The request is not one MCP reads",
                    "bad_request",
                    None,
                    &request_id,
                ));
            };
            let tools = DevTools {
                dev: Arc::clone(&routes.dev),
                caller: caller.clone(),
                request_id: request_id.clone(),
            };
            let options = McpOptions::new(routes.options.server_info.clone())
                .with_caller(caller.subject.clone())
                .with_request_id(request_id);
            let answer = mcp::serve(&request, &tools, &options).await;
            let (parts, body) = answer.into_parts();
            Response::from_parts(parts, Body::from(body))
        }
    }
}

struct Pull {
    cursor: i64,
    ready: bool,
    beat: Instant,
    done: bool,
}

async fn stream<S: Store + Send + 'static>(
    routes: Routes<S>,
    query: EventsQuery,
    caller: Caller,
) -> Response {
    let start = {
        let query = query.clone();
        blocking(&routes.dev, move |dev| {
            dev.events_head().map(|head| query.start(head))
        })
        .await
    };
    let start = match start {
        Ok(start) => start,
        Err(refusal) => return response_of(refusal_problem(&refusal, "")),
    };
    let state = Pull {
        cursor: start,
        ready: false,
        beat: Instant::now(),
        done: false,
    };
    let frames = futures_util::stream::unfold(state, move |mut state| {
        let routes = routes.clone();
        let query = query.clone();
        let caller = caller.clone();
        async move {
            if state.done {
                return None;
            }
            loop {
                let (cursor, query_now, caller_now) = (state.cursor, query.clone(), caller.clone());
                let read = blocking(&routes.dev, move |dev| {
                    dev.events_since(&caller_now, &query_now, cursor)
                })
                .await;
                let (events, next) = match read {
                    Ok(read) => read,
                    Err(_) => return None,
                };
                state.cursor = next;
                if !events.is_empty() {
                    let text: String = events.iter().map(events::frame).collect();
                    state.beat = Instant::now();
                    return Some((Ok::<Bytes, std::io::Error>(Bytes::from(text)), state));
                }
                if !state.ready {
                    state.ready = true;
                    state.beat = Instant::now();
                    let frame = events::ready_frame(state.cursor);
                    return Some((Ok(Bytes::from(frame)), state));
                }
                if state.beat.elapsed() >= routes.options.heartbeat {
                    state.beat = Instant::now();
                    return Some((Ok(Bytes::from_static(events::HEARTBEAT.as_bytes())), state));
                }
                tokio::time::sleep(routes.options.poll).await;
            }
        }
    });
    Response::builder()
        .status(200)
        .header("content-type", "text/event-stream")
        .header("cache-control", "no-store")
        .header("x-accel-buffering", "no")
        .body(Body::from_stream(frames))
        .unwrap_or_else(|_| Response::new(Body::empty()))
}

/// The caller's tools, as the MCP transport asks for them.
struct DevTools<S> {
    dev: Shared<S>,
    caller: Caller,
    request_id: String,
}

fn failed(message: String) -> ToolError {
    ToolError::Failed(message.into())
}

#[async_trait]
impl<S: Store + Send + 'static> ToolSet for DevTools<S> {
    async fn list(&self) -> Result<Vec<Tool>, ToolError> {
        let caller = self.caller.clone();
        let tools = blocking(&self.dev, move |dev| dev.tools(&caller))
            .await
            .map_err(|refusal| failed(refusal.message))?;
        tools
            .into_iter()
            .map(|entry| {
                serde_json::from_value::<Tool>(entry["tool"].clone())
                    .map_err(|error| failed(format!("a tool reads as no MCP tool: {error}")))
            })
            .collect()
    }

    async fn call(
        &self,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<CallOutcome, ToolError> {
        let (caller, handle, given) = (self.caller.clone(), name.to_owned(), arguments.clone());
        let called = blocking(&self.dev, move |dev| {
            dev.call_tool(&caller, &handle, &given)
        })
        .await
        .map_err(|refusal| failed(refusal.message))?;
        match called {
            None => Err(ToolError::unknown(format!(
                "development mode has no tool \"{name}\""
            ))),
            Some(Ok(value)) => Ok(CallOutcome::Result(value)),
            Some(Err(refusal)) => Ok(CallOutcome::Problem(
                refusal_problem(&refusal, &self.request_id).json(),
            )),
        }
    }
}
