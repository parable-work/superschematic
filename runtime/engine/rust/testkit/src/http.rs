//! The replayer of the HTTP corpus (`runtime/engine/testdata/http`, whose
//! README is the format), through the engine's transport-neutral routes
//! ([`superschematic_engine::api::handle`], D58) or a server runtime's
//! adapter over them ([`Transport`]): its `app` steps build the mounts a
//! case serves, an exchange sends its request through them with the caller
//! the recording established, and a stream step pulls the frames a stream
//! sent.
//!
//! The replayer is the host: it draws each request's id from the
//! scenario's seeded generator (or takes the client's `X-Request-Id`), and
//! applies the HTTP runtime's own steps before the engine sees a request,
//! as `runtime/http/typescript/src/hono.ts` orders them: the rate limit
//! (`rateLimitPerMinute`, a token bucket per route), the body limit
//! (`bodyLimitBytes`, 1 MiB unless given, on a route that takes a body;
//! an adapter that applies the mount's limit itself takes it over), and a
//! path no mount serves, 404. The caller an exchange records (`principal`,
//! `service`, `serviceRefused`) is what its authentication established,
//! handed to the engine as [`Caller`].

use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use serde_json::{Map, Value};

use superschematic_engine::api::{
    self, ActingForHeader, ApiOptions, Body, Caller, EventStream, McpOptions, Pull, Refusal,
    Request, ServiceCaller, StreamOptions,
};
use superschematic_engine::{seeded_uuid, Engine, Principal, SCENARIO_SEED};

use super::{Outcome, Run};

/// The files of the HTTP corpus.
pub const HTTP_FILES: [&str; 4] = ["http", "operations", "stream", "mcp"];

/// The response headers an exchange compares, as the README lists them.
const RESPONSE_HEADERS: [&str; 9] = [
    "allow",
    "cache-control",
    "content-type",
    "etag",
    "location",
    "mcp-session-id",
    "retry-after",
    "www-authenticate",
    "x-request-id",
];

/// How long a stream step waits for each frame it expects.
const FRAME_WAIT: Duration = Duration::from_secs(10);

/// A mount of the app an exchange is sent to, as a [`Transport`] sees it.
pub struct MountView<'a> {
    /// The path it is mounted at; empty for the root.
    pub prefix: &'a str,
    /// Whether it is the MCP endpoint's (`engineMcp`), else the HTTP API's
    /// (`engineApp`).
    pub mcp: bool,
    /// The engine's options for its routes.
    pub options: &'a ApiOptions,
    /// The body limit of a route that takes a body.
    pub body_limit: usize,
}

/// A stream of server-sent events a transport hands back, pulled as
/// [`EventStream::next`] is.
pub trait Frames {
    /// The stream's next bytes, waiting up to `timeout`.
    fn next(&mut self, timeout: Duration) -> Pull;
}

impl Frames for EventStream {
    fn next(&mut self, timeout: Duration) -> Pull {
        EventStream::next(self, timeout)
    }
}

/// A response's body as a transport hands it back.
pub enum Answered {
    Bytes(Vec<u8>),
    Stream(Box<dyn Frames>),
}

/// What a transport answered an exchange.
pub struct Answer {
    pub status: u16,
    /// Lowercase names.
    pub headers: Vec<(String, String)>,
    pub body: Answered,
}

impl Answer {
    /// A header's value, by its lowercase name.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
    }

    fn bytes(&self) -> &[u8] {
        match &self.body {
            Answered::Bytes(bytes) => bytes,
            Answered::Stream(_) => &[],
        }
    }
}

impl From<api::Response> for Answer {
    fn from(response: api::Response) -> Self {
        Answer {
            status: response.status,
            headers: response.headers,
            body: match response.body {
                Body::Empty => Answered::Bytes(Vec::new()),
                Body::Bytes(bytes) => Answered::Bytes(bytes),
                Body::Stream(stream) => Answered::Stream(stream),
            },
        }
    }
}

/// How the replayer's host hands an exchange's request to the engine once
/// its own steps let it through: the engine's routes called directly
/// ([`Direct`]), or a server runtime's adapter over them, which the same
/// recorded answers hold to.
pub trait Transport: Sync {
    /// Serves `request` (its path the app's, prefix and all) on the app
    /// whose mounts are `mounts`: through `mounts[served]`, the mount the
    /// replayer's dispatch found under whose prefix the request falls and
    /// whose kind serves its route, or, for `None`, no mount (the app's
    /// 404). A transport that routes for itself may send it either way.
    fn serve(
        &self,
        engine: &Arc<Engine>,
        mounts: &[MountView<'_>],
        served: Option<usize>,
        request: Request,
    ) -> Answer;

    /// Whether it applies a mount's body limit itself, in place of the
    /// replayer's host step.
    fn limits_bodies(&self) -> bool {
        false
    }
}

/// The engine's transport-neutral routes, called directly
/// ([`api::handle`]).
pub struct Direct;

impl Transport for Direct {
    fn serve(
        &self,
        engine: &Arc<Engine>,
        mounts: &[MountView<'_>],
        served: Option<usize>,
        request: Request,
    ) -> Answer {
        match served {
            // No mount serves it: the app's notFound answers.
            None => api::Problem::not_found("Not Found")
                .response(&request.request_id)
                .into(),
            Some(index) => api::handle(engine, mounts[index].options, &request).into(),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Kind {
    Engine,
    Mcp,
}

struct Mount {
    prefix: String,
    kind: Kind,
    options: ApiOptions,
    rate_limit: Option<f64>,
    body_limit: usize,
}

struct App {
    engine: Option<String>,
    mounts: Vec<Mount>,
}

struct OpenStream {
    /// The stream it pulls; `None` for an event stream the engine handed
    /// back whole (MCP's), whose frames are all in `frames`.
    stream: Option<Box<dyn Frames>>,
    buffer: String,
    frames: Vec<Value>,
    ended: bool,
}

/// What a case serves: its apps, its open streams, and the rate limit's
/// buckets.
#[derive(Default)]
pub(crate) struct Served {
    apps: Vec<(String, App)>,
    streams: Vec<(String, OpenStream)>,
    buckets: HashMap<String, (f64, i64)>,
    /// Whether the steps after this one are streams' ends alone: the case
    /// is over, and its harness closes the engines, which ends them.
    pub(crate) tail: bool,
}

fn mount_of(prefix: &str, kind: &str, options: &Value) -> Mount {
    let member = |key: &str| options.get(key);
    let stream = member("stream");
    let server = member("serverInfo");
    Mount {
        prefix: prefix.to_owned(),
        kind: if kind == "mcp" {
            Kind::Mcp
        } else {
            Kind::Engine
        },
        options: ApiOptions {
            stream: StreamOptions {
                page_size: stream
                    .and_then(|stream| stream.get("pageSize"))
                    .and_then(Value::as_i64),
                heartbeat_ms: stream
                    .and_then(|stream| stream.get("heartbeatMs"))
                    .and_then(Value::as_u64),
            },
            mcp: McpOptions {
                server_name: server
                    .and_then(|server| server.get("name"))
                    .and_then(Value::as_str)
                    .map(str::to_owned),
                server_version: server
                    .and_then(|server| server.get("version"))
                    .and_then(Value::as_str)
                    .map(str::to_owned),
                instructions: member("instructions")
                    .and_then(Value::as_str)
                    .map(str::to_owned),
            },
            tools: None,
            acting_for: if member("actingFor") == Some(&Value::Bool(true)) {
                ActingForHeader::Honor
            } else {
                ActingForHeader::Ignore
            },
        },
        rate_limit: member("rateLimitPerMinute")
            .and_then(Value::as_f64)
            .filter(|limit| *limit > 0.0),
        body_limit: member("bodyLimitBytes")
            .and_then(Value::as_u64)
            .map_or(api::DEFAULT_BODY_LIMIT_BYTES, |limit| limit as usize),
    }
}

impl Mount {
    fn view(&self) -> MountView<'_> {
        MountView {
            prefix: &self.prefix,
            mcp: self.kind == Kind::Mcp,
            options: &self.options,
            body_limit: self.body_limit,
        }
    }
}

/// An `app` step: the mounts a case serves.
pub(crate) fn app(run: &mut Run<'_>, step: &Value) -> Result<Outcome, String> {
    let name = step
        .get("app")
        .and_then(Value::as_str)
        .unwrap_or("a1")
        .to_owned();
    let kind = step.get("kind").and_then(Value::as_str).unwrap_or("engine");
    let empty = Value::Object(Map::new());
    let mounts = if kind == "mount" {
        step.get("mounts")
            .and_then(Value::as_array)
            .map(|mounts| {
                mounts
                    .iter()
                    .map(|mount| {
                        mount_of(
                            mount.get("prefix").and_then(Value::as_str).unwrap_or(""),
                            mount
                                .get("kind")
                                .and_then(Value::as_str)
                                .unwrap_or("engine"),
                            mount.get("options").unwrap_or(&empty),
                        )
                    })
                    .collect()
            })
            .unwrap_or_default()
    } else {
        vec![mount_of("", kind, step.get("options").unwrap_or(&empty))]
    };
    run.http.apps.push((
        name,
        App {
            engine: step
                .get("engine")
                .and_then(Value::as_str)
                .map(str::to_owned),
            mounts,
        },
    ));
    Ok(Outcome::Passed)
}

// request_id is the host's: the client's X-Request-Id when it is sane,
// else the next id of the scenario's seeded generator.
fn request_id(run: &Run<'_>, headers: &[(String, String)]) -> String {
    let supplied = headers
        .iter()
        .find(|(name, _)| name == "x-request-id")
        .map(|(_, value)| value.trim().to_owned())
        .filter(|value| !value.is_empty() && value.len() <= 128 && !value.contains(['\r', '\n']));
    supplied.unwrap_or_else(|| seeded_uuid(SCENARIO_SEED, run.counter.next()))
}

// body_of is a recorded body's bytes: a JSON body as JSON, any other as
// its text.
fn body_of(body: &Value, content_type: Option<&str>) -> Vec<u8> {
    let json = content_type.is_some_and(|kind| kind.contains("json"));
    if let Value::Object(members) = body {
        if members.len() == 1 {
            if let Some(Value::String(text)) = members.get("text") {
                if !json || serde_json::from_str::<Value>(text).is_err() {
                    return text.as_bytes().to_vec();
                }
            }
        }
    }
    superschematic_engine::json::stringify(body).into_bytes()
}

fn caller_of(recorded: Option<&Value>) -> Result<Caller, String> {
    let Some(recorded) = recorded else {
        return Ok(Caller::default());
    };
    let principal =
        match recorded.get("principal") {
            None | Some(Value::Null) => None,
            Some(given) => Some(serde_json::from_value::<Principal>(given.clone()).map_err(
                |error| format!("a caller's principal the replayer cannot read: {error}"),
            )?),
        };
    let service = match recorded.get("service") {
        None | Some(Value::Null) => None,
        Some(given) => Some(ServiceCaller {
            deployable: given
                .get("deployable")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            serves: given
                .get("serves")
                .and_then(Value::as_array)
                .map(|serves| {
                    serves
                        .iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                })
                .unwrap_or_default(),
            subject: given
                .get("subject")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
        }),
    };
    let refused = recorded.get("serviceRefused").map(|refusal| Refusal {
        status: refusal
            .get("status")
            .and_then(Value::as_u64)
            .map_or(401, |status| status as u16),
        code: refusal
            .get("code")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        detail: None,
    });
    Ok(Caller {
        principal,
        service,
        refused,
    })
}

fn wall_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| {
            i64::try_from(since.as_millis()).unwrap_or(i64::MAX)
        })
}

// admit takes one token from a route's bucket (MemoryRateLimitStore), or
// says how many seconds until one is there.
fn admit(served: &mut Served, route: &str, limit: f64) -> Result<(), u64> {
    let now = wall_ms();
    let per_ms = limit / 60_000.0;
    let bucket = served
        .buckets
        .entry(format!("{route}:unknown"))
        .or_insert((limit, now));
    if now > bucket.1 {
        bucket.0 = limit.min(bucket.0 + (now - bucket.1) as f64 * per_ms);
        bucket.1 = now;
    }
    if bucket.0 >= 1.0 {
        bucket.0 -= 1.0;
        return Ok(());
    }
    Err(((1.0 - bucket.0) / per_ms / 1000.0).ceil().max(1.0) as u64)
}

fn problem_bytes(
    status: u16,
    detail: &str,
    code: &str,
    request_id: &str,
    details: Option<Value>,
) -> Answer {
    let mut problem = api::Problem::new(status, detail, code);
    problem.details = details;
    problem.response(request_id).into()
}

/// An exchange: the request through the host's steps and the engine's
/// routes, its response compared.
pub(crate) fn exchange(run: &mut Run<'_>, step: &Value) -> Result<Outcome, String> {
    let recorded = step
        .get("request")
        .ok_or("an exchange without its request")?;
    let recorded = run.substitute(recorded)?;
    let method = recorded
        .get("method")
        .and_then(Value::as_str)
        .unwrap_or("GET")
        .to_owned();
    let path = recorded
        .get("path")
        .and_then(Value::as_str)
        .unwrap_or("/")
        .to_owned();
    let headers: Vec<(String, String)> = recorded
        .get("headers")
        .and_then(Value::as_object)
        .map(|headers| {
            headers
                .iter()
                .map(|(name, value)| (name.clone(), value.as_str().unwrap_or_default().to_owned()))
                .collect()
        })
        .unwrap_or_default();
    let content_type = headers
        .iter()
        .find(|(name, _)| name == "content-type")
        .map(|(_, value)| value.as_str());
    let body = recorded.get("body").map(|body| body_of(body, content_type));
    let caller = caller_of(recorded.get("caller"))?;
    let app_name = step.get("app").and_then(Value::as_str).map(str::to_owned);
    let app_index = match &app_name {
        Some(name) => run
            .http
            .apps
            .iter()
            .position(|(each, _)| each == name)
            .ok_or_else(|| format!("no app {name}"))?,
        None => run
            .http
            .apps
            .len()
            .checked_sub(1)
            .ok_or("no app is served")?,
    };
    let engine_name = run.http.apps[app_index].1.engine.clone();
    let engine = {
        let opened = match &engine_name {
            Some(name) => run
                .engines
                .iter_mut()
                .find(|(each, _)| each == name)
                .map(|(_, opened)| opened),
            None => run.engines.last_mut().map(|(_, opened)| opened),
        }
        .ok_or("no engine is open")?;
        let engine = opened.engine.clone().ok_or("the engine is closed")?;
        if opened.fixed_clock {
            if let Some(now) = step.get("now").and_then(Value::as_i64) {
                engine.clock().set(now).map_err(|error| error.to_string())?;
            }
        }
        engine
    };
    let id = request_id(run, &headers);
    // The mount that serves the request: one under whose prefix it falls
    // and whose kind serves its route.
    let served = run.http.apps[app_index]
        .1
        .mounts
        .iter()
        .enumerate()
        .find_map(|(index, mount)| {
            let rest = path.strip_prefix(mount.prefix.trim_end_matches('/'))?;
            let route = api::route_of(&method, rest)?;
            let kind = if route.mcp { Kind::Mcp } else { Kind::Engine };
            (kind == mount.kind).then_some((index, route))
        });
    // The host's steps on the mount that serves it: the rate limit, then
    // the body limit, unless the transport applies the mount's.
    let refused = match &served {
        None => None,
        Some((index, route)) => {
            let mount = &run.http.apps[app_index].1.mounts[*index];
            let (rate_limit, body_limit) = (mount.rate_limit, mount.body_limit);
            let limited = match rate_limit {
                Some(limit) => admit(&mut run.http, route.name, limit).err(),
                None => None,
            };
            if let Some(retry) = limited {
                let mut response = problem_bytes(
                    429,
                    "Too Many Requests",
                    "too_many_requests",
                    &id,
                    Some(serde_json::json!({"retryAfterSeconds": retry})),
                );
                response
                    .headers
                    .insert(0, ("retry-after".to_owned(), retry.to_string()));
                Some(response)
            } else if !run.transport.limits_bodies()
                && route.takes_body()
                && body.as_ref().is_some_and(|bytes| bytes.len() > body_limit)
            {
                Some(problem_bytes(
                    413,
                    "Request body exceeds the accepted size",
                    "payload_too_large",
                    &id,
                    None,
                ))
            } else {
                None
            }
        }
    };
    let response = match refused {
        Some(response) => response,
        None => {
            let mounts = &run.http.apps[app_index].1.mounts;
            let views: Vec<MountView<'_>> = mounts.iter().map(Mount::view).collect();
            let index = served.as_ref().map(|(index, _)| *index);
            run.transport.serve(
                &engine,
                &views,
                index,
                Request {
                    method: method.clone(),
                    prefix: index.map_or_else(String::new, |index| mounts[index].prefix.clone()),
                    path: path.clone(),
                    query: recorded
                        .get("query")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned(),
                    headers: headers.clone(),
                    body: body.clone(),
                    caller: caller.clone(),
                    request_id: id.clone(),
                },
            )
        }
    };
    let expected = step.get("response").cloned().unwrap_or(Value::Null);
    let status = expected.get("status").and_then(Value::as_u64).unwrap_or(0);
    if u64::from(response.status) != status {
        return Err(format!(
            "{method} {path}: expected status {status}, got {}: {}",
            response.status,
            String::from_utf8_lossy(response.bytes())
        ));
    }
    let empty = Map::new();
    let expected_headers = expected
        .get("headers")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    for name in RESPONSE_HEADERS {
        match (expected_headers.get(name), response.header(name)) {
            (Some(want), Some(got)) => {
                run.matches(want, &Value::from(got), &format!("/headers/{name}"))?
            }
            (Some(want), None) => {
                return Err(format!(
                    "{method} {path}: expected header {name}: {want}, got none"
                ))
            }
            (None, Some(got)) => {
                return Err(format!(
                    "{method} {path}: expected no header {name}, got {got}"
                ))
            }
            (None, None) => {}
        }
    }
    let content_type = response
        .header("content-type")
        .unwrap_or_default()
        .to_owned();
    match response.body {
        Answered::Stream(stream) => {
            let label = step
                .get("stream")
                .and_then(Value::as_str)
                .ok_or("a stream the recording does not name")?
                .to_owned();
            run.http.streams.push((
                label,
                OpenStream {
                    stream: Some(stream),
                    buffer: String::new(),
                    frames: Vec::new(),
                    ended: false,
                },
            ));
            if let Some(body) = expected.get("body") {
                return Err(format!(
                    "{method} {path}: expected the body {body}, got a stream"
                ));
            }
            Ok(Outcome::Passed)
        }
        // An event stream the engine wrote whole (MCP's answer on the 2025
        // revisions): the recording reads it as a stream that ends with
        // its last frame.
        Answered::Bytes(bytes) if content_type.starts_with("text/event-stream") => {
            let label = step
                .get("stream")
                .and_then(Value::as_str)
                .ok_or("a stream the recording does not name")?
                .to_owned();
            let mut open = OpenStream {
                stream: None,
                buffer: String::from_utf8_lossy(&bytes).into_owned(),
                frames: Vec::new(),
                ended: true,
            };
            frames_of(&mut open);
            run.http.streams.push((label, open));
            if let Some(body) = expected.get("body") {
                return Err(format!(
                    "{method} {path}: expected the body {body}, got a stream"
                ));
            }
            Ok(Outcome::Passed)
        }
        Answered::Bytes(bytes) if !bytes.is_empty() => {
            let text = String::from_utf8_lossy(&bytes).into_owned();
            let actual = if content_type.contains("json") {
                serde_json::from_str::<Value>(&text)
                    .unwrap_or_else(|_| serde_json::json!({ "text": text }))
            } else {
                serde_json::json!({ "text": text })
            };
            match expected.get("body") {
                Some(want) => run
                    .matches(want, &actual, "/body")
                    .map(|()| Outcome::Passed),
                None => Err(format!("{method} {path}: expected no body, got {text}")),
            }
        }
        _ => match expected.get("body") {
            Some(want) => Err(format!(
                "{method} {path}: expected the body {want}, got none"
            )),
            None => Ok(Outcome::Passed),
        },
    }
}

// frame_of reads one server-sent event as the recorder does.
fn frame_of(block: &str) -> Value {
    let mut frame = Map::new();
    for line in block.split('\n') {
        if let Some(comment) = line.strip_prefix(':') {
            frame.insert("comment".into(), Value::from(comment.trim()));
        } else if let Some(event) = line.strip_prefix("event: ") {
            frame.insert("event".into(), Value::from(event));
        } else if let Some(id) = line.strip_prefix("id: ") {
            frame.insert("id".into(), Value::from(id));
        } else if let Some(data) = line.strip_prefix("data: ") {
            match serde_json::from_str::<Value>(data) {
                Ok(value) => {
                    frame.insert("data".into(), value);
                }
                Err(_) => {
                    frame.insert("text".into(), Value::from(data));
                }
            }
        }
    }
    Value::Object(frame)
}

// frames_of moves the whole frames in a stream's buffer to its frames,
// leaving out the heartbeat's comments.
fn frames_of(open: &mut OpenStream) {
    while let Some(end) = open.buffer.find("\n\n") {
        let block = open.buffer[..end].to_owned();
        open.buffer.drain(..end + 2);
        let frame = frame_of(&block);
        let keepalive = frame.as_object().is_some_and(|members| {
            members.len() == 1 && members.get("comment") == Some(&Value::from("keepalive"))
        });
        if !keepalive {
            open.frames.push(frame);
        }
    }
}

// take pulls from a stream until it holds `count` frames or ends.
fn take(open: &mut OpenStream, count: usize) {
    let deadline = Instant::now() + FRAME_WAIT;
    while open.frames.len() < count && !open.ended && Instant::now() < deadline {
        let Some(stream) = open.stream.as_mut() else {
            open.ended = true;
            break;
        };
        match stream.next(Duration::from_millis(200)) {
            Pull::Data(bytes) => {
                open.buffer.push_str(&String::from_utf8_lossy(&bytes));
                frames_of(open);
            }
            Pull::Pending => {}
            Pull::End => open.ended = true,
        }
    }
}

/// A stream step: the frames a stream sent, or its end.
pub(crate) fn stream(run: &mut Run<'_>, step: &Value) -> Result<Outcome, String> {
    let label = step
        .get("stream")
        .and_then(Value::as_str)
        .ok_or("a stream step without its stream")?
        .to_owned();
    let index = run
        .http
        .streams
        .iter()
        .position(|(each, _)| *each == label)
        .ok_or_else(|| format!("no stream {label} is open"))?;
    if step.get("end") == Some(&Value::Bool(true)) {
        if run.http.tail {
            // The test was over: its cleanup closed the engines, which ends
            // every stream.
            for (_, opened) in &run.engines {
                if let Some(engine) = &opened.engine {
                    engine.shutdown();
                }
            }
        }
        let open = &mut run.http.streams[index].1;
        take(open, usize::MAX);
        if !open.frames.is_empty() {
            return Err(format!(
                "stream {label}: expected its end, got frames {}",
                Value::Array(open.frames.clone())
            ));
        }
        if !open.ended {
            return Err(format!("stream {label}: expected its end, and it went on"));
        }
        return Ok(Outcome::Passed);
    }
    let expected = step
        .get("frames")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    let got: Vec<Value> = {
        let open = &mut run.http.streams[index].1;
        take(open, expected.len());
        let count = expected.len().min(open.frames.len());
        open.frames.drain(..count).collect()
    };
    if got.len() < expected.len() {
        return Err(format!(
            "stream {label}: expected {} frames, got {}: {}",
            expected.len(),
            got.len(),
            Value::Array(got)
        ));
    }
    for (index, (want, frame)) in expected.iter().zip(&got).enumerate() {
        run.matches(want, frame, &format!("/frames/{index}"))?;
    }
    Ok(Outcome::Passed)
}
