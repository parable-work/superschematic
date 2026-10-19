//! The C ABI's implementation, which [`crate::export_engine_c_abi!`]
//! exports: the open engines by handle, each with the event streams its
//! responses hold open, the documents each export reads and writes, and
//! the binding's own calls (`deploy`, `health`, `abi`).
//!
//! A handle is a number in a table, not a pointer, so a call on a handle
//! the host already closed is `ENG_CLOSED` rather than a use after free.
//! A call in flight holds its engine, so a close waits for nothing: the
//! engine shuts down at once, and its storage closes when the last call
//! returns.

use std::collections::HashMap;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, OnceLock, PoisonError, RwLock};
use std::time::Duration;

use serde_json::{Map, Value};

use superschematic_engine::api::{
    self, ApiOptions, Body, Caller, EventStream, McpOptions, Pull, Refusal, Request, ServiceCaller,
    StreamOptions,
};
use superschematic_engine::calls::{self, js_string};
use superschematic_engine::storage::Dialect;
use superschematic_engine::{json, BehaviorSet, Engine, EngineOptions, Principal};

use crate::config::{self, member, safe_integer};
use crate::failure::Failure;

/// The version of the contract between the archive and its bindings: the
/// exports, their documents and their codes. The Go binding refuses an
/// archive of another, which a stale `lib/` would be.
pub const ABI_VERSION: u32 = 1;

/// The output is the result.
pub const ENG_OK: i32 = 0;
/// The output is an error document.
pub const ENG_ERROR: i32 = 1;
/// An out pointer was null, or an input pointer was null with a non-zero
/// length; nothing was written.
pub const ENG_BAD_ARGUMENTS: i32 = 2;
/// The engine is closed or was never opened, or the stream is over;
/// nothing was written.
pub const ENG_CLOSED: i32 = 3;

/// What a project does to an engine's options before it opens: register
/// its behaviors, and anything else its own config (the config's `project`
/// member, `None` when absent) asks for, such as a Rust access policy. A
/// refusal is the open's error, a `TypeError`.
pub type Configure = fn(&mut EngineOptions, Option<&Value>) -> Result<(), String>;

/// The project an archive serves: its name, which `eng_open` and `abi`
/// report (the crate's, for an archive [`crate::export_engine_c_abi!`]
/// emits), and how it configures an engine.
pub struct Project {
    pub name: &'static str,
    pub configure: Configure,
}

/// The configure of a project that only registers behaviors: the ones
/// `behaviors` returns, and no `project` member.
pub fn with_behaviors(
    options: &mut EngineOptions,
    project: Option<&Value>,
    behaviors: fn() -> BehaviorSet,
) -> Result<(), String> {
    if project.is_some() {
        return Err(
            "this engine's archive takes no project config: its project registers behaviors alone"
                .to_owned(),
        );
    }
    options.behaviors = behaviors();
    Ok(())
}

// Handle is an open engine and the event streams its responses hold open.
struct Handle {
    engine: Arc<Engine>,
    project: &'static str,
    streams: Mutex<HashMap<u64, Box<EventStream>>>,
    next_stream: AtomicU64,
}

impl Handle {
    fn streams(&self) -> MutexGuard<'_, HashMap<u64, Box<EventStream>>> {
        self.streams.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn hold(&self, stream: Box<EventStream>) -> u64 {
        let id = self.next_stream.fetch_add(1, Ordering::Relaxed) + 1;
        self.streams().insert(id, stream);
        id
    }
}

static NEXT_ENGINE: AtomicU64 = AtomicU64::new(0);

fn engines() -> &'static RwLock<HashMap<u64, Arc<Handle>>> {
    static ENGINES: OnceLock<RwLock<HashMap<u64, Arc<Handle>>>> = OnceLock::new();
    ENGINES.get_or_init(|| RwLock::new(HashMap::new()))
}

fn handle(engine: u64) -> Option<Arc<Handle>> {
    engines()
        .read()
        .unwrap_or_else(PoisonError::into_inner)
        .get(&engine)
        .cloned()
}

// What an export's work came to.
enum Outcome {
    Done(Vec<u8>),
    Failed(Failure),
    Closed,
}

impl From<Result<Vec<u8>, Failure>> for Outcome {
    fn from(result: Result<Vec<u8>, Failure>) -> Self {
        match result {
            Ok(bytes) => Outcome::Done(bytes),
            Err(failure) => Outcome::Failed(failure),
        }
    }
}

/// # Safety
/// `ptr` must point to `len` readable bytes, or be null with `len` 0.
unsafe fn input<'a>(ptr: *const u8, len: usize) -> Option<&'a [u8]> {
    if len == 0 {
        return Some(&[]);
    }
    if ptr.is_null() {
        return None;
    }
    Some(std::slice::from_raw_parts(ptr, len))
}

/// # Safety
/// `out_ptr` and `out_len` must be valid for writes.
unsafe fn write_out(out_ptr: *mut *mut u8, out_len: *mut usize, bytes: Vec<u8>) {
    if bytes.is_empty() {
        out_ptr.write_unaligned(std::ptr::null_mut());
        out_len.write_unaligned(0);
        return;
    }
    let bytes = bytes.into_boxed_slice();
    let len = bytes.len();
    out_ptr.write_unaligned(Box::into_raw(bytes).cast::<u8>());
    out_len.write_unaligned(len);
}

// finish writes an outcome and returns its code.
unsafe fn finish(out_ptr: *mut *mut u8, out_len: *mut usize, outcome: Outcome) -> i32 {
    match outcome {
        Outcome::Done(bytes) => {
            write_out(out_ptr, out_len, bytes);
            ENG_OK
        }
        Outcome::Failed(failure) => {
            write_out(out_ptr, out_len, failure.document());
            ENG_ERROR
        }
        Outcome::Closed => ENG_CLOSED,
    }
}

// guarded runs an export's work, a panic its error.
fn guarded(work: impl FnOnce() -> Outcome) -> Outcome {
    catch_unwind(AssertUnwindSafe(work))
        .unwrap_or_else(|payload| Outcome::Failed(Failure::panic(payload.as_ref())))
}

fn parse(bytes: &[u8], what: &str) -> Result<Value, Failure> {
    serde_json::from_slice(bytes)
        .map_err(|error| Failure::usage(format!("{what} is JSON: {error}")))
}

fn written(value: &Value) -> Vec<u8> {
    json::stringify(value).into_bytes()
}

fn dialect_name(dialect: Dialect) -> &'static str {
    match dialect {
        Dialect::Postgres => "postgres",
        _ => "sqlite",
    }
}

fn info(engine: &Engine, project: &str) -> Value {
    let mut out = Map::new();
    out.insert("abi".into(), Value::from(ABI_VERSION));
    out.insert("project".into(), Value::from(project));
    out.insert(
        "dialect".into(),
        Value::from(dialect_name(engine.dialect())),
    );
    out.insert(
        "behaviors".into(),
        Value::Array(
            engine
                .behaviors()
                .names()
                .into_iter()
                .map(Value::from)
                .collect(),
        ),
    );
    Value::Object(out)
}

/// `eng_open`.
///
/// # Safety
/// `config` must point to `config_len` readable bytes; `engine`, `out_ptr`
/// and `out_len` must be valid for writes.
pub unsafe fn open(
    project: &Project,
    config: *const u8,
    config_len: usize,
    engine: *mut u64,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    if engine.is_null() || out_ptr.is_null() || out_len.is_null() {
        return ENG_BAD_ARGUMENTS;
    }
    let Some(config) = input(config, config_len) else {
        return ENG_BAD_ARGUMENTS;
    };
    let outcome = guarded(|| {
        let opened = (|| {
            let (mut options, own) = config::read(&parse(config, "an engine's config")?)?;
            (project.configure)(&mut options, own.as_ref()).map_err(Failure::usage)?;
            let opened = Engine::open_shared(options).map_err(|error| Failure::of(&error))?;
            let id = NEXT_ENGINE.fetch_add(1, Ordering::Relaxed) + 1;
            let opened_info = info(&opened, project.name);
            engines()
                .write()
                .unwrap_or_else(PoisonError::into_inner)
                .insert(
                    id,
                    Arc::new(Handle {
                        engine: opened,
                        project: project.name,
                        streams: Mutex::new(HashMap::new()),
                        next_stream: AtomicU64::new(0),
                    }),
                );
            engine.write_unaligned(id);
            Ok(written(&opened_info))
        })();
        opened.into()
    });
    finish(out_ptr, out_len, outcome)
}

/// `eng_close`.
pub fn close(engine: u64) -> i32 {
    let closed = catch_unwind(|| {
        let taken = engines()
            .write()
            .unwrap_or_else(PoisonError::into_inner)
            .remove(&engine);
        match taken {
            None => ENG_CLOSED,
            Some(handle) => {
                handle.engine.shutdown();
                handle.streams().clear();
                ENG_OK
            }
        }
    });
    closed.unwrap_or(ENG_ERROR)
}

/// `eng_call`.
///
/// # Safety
/// `request` must point to `request_len` readable bytes; `out_ptr` and
/// `out_len` must be valid for writes.
pub unsafe fn call(
    engine: u64,
    request: *const u8,
    request_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    if out_ptr.is_null() || out_len.is_null() {
        return ENG_BAD_ARGUMENTS;
    }
    let Some(request) = input(request, request_len) else {
        return ENG_BAD_ARGUMENTS;
    };
    let Some(handle) = handle(engine) else {
        return ENG_CLOSED;
    };
    let outcome = guarded(|| called(&handle, request).into());
    finish(out_ptr, out_len, outcome)
}

fn called(handle: &Handle, request: &[u8]) -> Result<Vec<u8>, Failure> {
    let request = parse(request, "an engine call")?;
    let Value::Object(members) = &request else {
        return Err(Failure::usage(
            "an engine call is { op, principal?, args?, now? }",
        ));
    };
    let op = members
        .get("op")
        .and_then(Value::as_str)
        .ok_or_else(|| Failure::usage("an engine call names its call: op"))?;
    let args: &[Value] = match members.get("args") {
        None | Some(Value::Null) => &[],
        Some(Value::Array(args)) => args,
        Some(_) => return Err(Failure::usage("an engine call's args are a list")),
    };
    let engine = &handle.engine;
    if let Some(now) = member(members, "now") {
        let now = safe_integer(now)
            .ok_or_else(|| Failure::usage("an engine call's now is epoch milliseconds"))?;
        engine
            .clock()
            .set(now)
            .map_err(|error| Failure::of(&error))?;
    }
    match op {
        "deploy" => return deploy(engine, args).map(|result| written(&result)),
        "health" => return Ok(written(&health(engine))),
        "abi" => return Ok(written(&info(engine, handle.project))),
        _ => {}
    }
    let principal = match member(members, "principal") {
        None => None,
        Some(given) => Some(
            calls::principal_from_json(given)
                .map_err(|refusal| Failure::of(&superschematic_engine::Error::Engine(refusal)))?,
        ),
    };
    match calls::call(engine, op, principal.as_ref(), args) {
        Ok(None) => Ok(Vec::new()),
        Ok(Some(result)) => Ok(written(&result)),
        Err(error) => Err(Failure::of(&error)),
    }
}

// deploy defines and publishes a server's schema documents as the engine's
// own principal, each one whose live version has its hash left as it is
// (Engine::deploy).
fn deploy(engine: &Engine, args: &[Value]) -> Result<Value, Failure> {
    let Some(Value::Array(documents)) = args.first() else {
        return Err(Failure::usage(
            "deploy takes [documents, { namespace? }]: a list of schema documents",
        ));
    };
    let namespace = args
        .get(1)
        .and_then(Value::as_object)
        .and_then(|options| member(options, "namespace"))
        .map(js_string);
    let deployed = engine
        .deploy(documents, namespace.as_deref())
        .map_err(|error| Failure::of(&error))?;
    Ok(Value::Array(
        deployed
            .iter()
            .map(superschematic_engine::Deployed::to_json)
            .collect(),
    ))
}

// health is what a server's readiness reads: not ready once the engine
// closed or panicked inside a transaction, or while its database does not
// answer a read.
fn health(engine: &Engine) -> Value {
    let closed = engine.is_closed();
    let poisoned = engine.poisoned();
    let error = if closed || poisoned {
        None
    } else {
        engine.events().head().err().map(|error| error.to_string())
    };
    let mut out = Map::new();
    out.insert(
        "ready".into(),
        Value::Bool(!closed && !poisoned && error.is_none()),
    );
    out.insert("closed".into(), Value::Bool(closed));
    out.insert("poisoned".into(), Value::Bool(poisoned));
    out.insert(
        "dialect".into(),
        Value::from(dialect_name(engine.dialect())),
    );
    out.insert(
        "listening".into(),
        Value::Bool(engine.db().notifier().listening()),
    );
    if let Some(error) = error {
        out.insert("error".into(), Value::from(error));
    }
    Value::Object(out)
}

/// `eng_http`.
///
/// # Safety
/// `head` and `body` must point to `head_len` and `body_len` readable
/// bytes; the four out pointers must be valid for writes.
#[allow(clippy::too_many_arguments)]
pub unsafe fn http(
    engine: u64,
    head: *const u8,
    head_len: usize,
    body: *const u8,
    body_len: usize,
    head_out: *mut *mut u8,
    head_out_len: *mut usize,
    body_out: *mut *mut u8,
    body_out_len: *mut usize,
) -> i32 {
    if head_out.is_null() || head_out_len.is_null() || body_out.is_null() || body_out_len.is_null()
    {
        return ENG_BAD_ARGUMENTS;
    }
    let (Some(head), Some(body)) = (input(head, head_len), input(body, body_len)) else {
        return ENG_BAD_ARGUMENTS;
    };
    let Some(handle) = handle(engine) else {
        return ENG_CLOSED;
    };
    let mut response_body = Vec::new();
    let outcome = guarded(|| served(&handle, head, body, &mut response_body).into());
    let code = finish(head_out, head_out_len, outcome);
    if code == ENG_OK {
        write_out(body_out, body_out_len, response_body);
    } else {
        write_out(body_out, body_out_len, Vec::new());
    }
    code
}

fn served(
    handle: &Handle,
    head: &[u8],
    body: &[u8],
    response_body: &mut Vec<u8>,
) -> Result<Vec<u8>, Failure> {
    let (mut request, options) = head_of(&parse(head, "a request's head")?)?;
    request.body = (!body.is_empty()).then(|| body.to_vec());
    let response = api::handle(&handle.engine, &options, &request);
    let headers: Vec<Value> = response
        .headers
        .iter()
        .map(|(name, value)| {
            Value::Array(vec![
                Value::from(name.as_str()),
                Value::from(value.as_str()),
            ])
        })
        .collect();
    let stream = match response.body {
        Body::Empty => Value::Null,
        Body::Bytes(bytes) => {
            *response_body = bytes;
            Value::Null
        }
        Body::Stream(stream) => Value::from(handle.hold(stream)),
    };
    let mut out = Map::new();
    out.insert("status".into(), Value::from(response.status));
    out.insert("headers".into(), Value::Array(headers));
    out.insert("stream".into(), stream);
    Ok(written(&Value::Object(out)))
}

// head_of reads a request's head: what the engine's Request holds but the
// body, and the mount's options.
fn head_of(given: &Value) -> Result<(Request, ApiOptions), Failure> {
    let refuse = |what: &str| Failure::usage(format!("a request's head: {what}"));
    let Value::Object(members) = given else {
        return Err(refuse(
            "it is { method, prefix, path, query, headers, caller, requestId, options }",
        ));
    };
    let string = |key: &str| {
        member(members, key)
            .and_then(Value::as_str)
            .map(str::to_owned)
    };
    let headers = match member(members, "headers") {
        None => Vec::new(),
        Some(Value::Array(pairs)) => pairs
            .iter()
            .map(|pair| match pair.as_array().map(Vec::as_slice) {
                Some([Value::String(name), Value::String(value)]) => {
                    Ok((name.clone(), value.clone()))
                }
                _ => Err(refuse("headers are [name, value] pairs of strings")),
            })
            .collect::<Result<Vec<_>, _>>()?,
        Some(Value::Object(headers)) => headers
            .iter()
            .map(|(name, value)| (name.clone(), js_string(value)))
            .collect(),
        Some(_) => return Err(refuse("headers are [name, value] pairs of strings")),
    };
    let empty = Map::new();
    let caller = member(members, "caller")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    let principal = match member(caller, "principal") {
        None => None,
        Some(given) => Some(
            serde_json::from_value::<Principal>(given.clone())
                .map_err(|_| refuse("caller.principal is { subject, permissions, claims? }"))?,
        ),
    };
    let service = match member(caller, "service") {
        None => None,
        Some(Value::Object(service)) => {
            let text = |key: &str| service.get(key).and_then(Value::as_str).map(str::to_owned);
            Some(ServiceCaller {
                deployable: text("deployable")
                    .ok_or_else(|| refuse("caller.service is { deployable, serves, subject }"))?,
                serves: service
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
                subject: text("subject").unwrap_or_default(),
            })
        }
        Some(_) => return Err(refuse("caller.service is { deployable, serves, subject }")),
    };
    let refused = match member(caller, "refused") {
        None => None,
        Some(Value::Object(refusal)) => Some(Refusal {
            status: refusal
                .get("status")
                .and_then(Value::as_u64)
                .and_then(|status| u16::try_from(status).ok())
                .ok_or_else(|| refuse("caller.refused is { status, code, detail? }"))?,
            code: refusal
                .get("code")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            detail: refusal
                .get("detail")
                .and_then(Value::as_str)
                .map(str::to_owned),
        }),
        Some(_) => return Err(refuse("caller.refused is { status, code, detail? }")),
    };
    let request = Request {
        method: string("method").ok_or_else(|| refuse("method is a string"))?,
        prefix: string("prefix").unwrap_or_default(),
        path: string("path").ok_or_else(|| refuse("path is a string"))?,
        query: string("query").unwrap_or_default(),
        headers,
        body: None,
        caller: Caller {
            principal,
            service,
            refused,
        },
        request_id: string("requestId").unwrap_or_default(),
    };
    let options = member(members, "options").and_then(Value::as_object);
    Ok((request, api_options_of(options)?))
}

// api_options_of is how a mount serves: `stream` ({pageSize, heartbeatMs})
// and `mcp` ({serverInfo: {name, version}, instructions}).
fn api_options_of(options: Option<&Map<String, Value>>) -> Result<ApiOptions, Failure> {
    let object = |within: Option<&Map<String, Value>>, key: &str| {
        within
            .and_then(|within| member(within, key))
            .and_then(Value::as_object)
            .cloned()
    };
    let text = |within: Option<&Map<String, Value>>, key: &str| {
        within
            .and_then(|within| member(within, key))
            .and_then(Value::as_str)
            .map(str::to_owned)
    };
    let stream = object(options, "stream");
    let mcp = object(options, "mcp");
    let server = object(mcp.as_ref(), "serverInfo");
    let integer = |key: &str| -> Result<Option<i64>, Failure> {
        match stream.as_ref().and_then(|stream| member(stream, key)) {
            None => Ok(None),
            Some(given) => safe_integer(given).map(Some).ok_or_else(|| {
                Failure::usage(format!(
                    "stream.{key} must be an integer, got {}",
                    js_string(given)
                ))
            }),
        }
    };
    let stream = StreamOptions {
        page_size: integer("pageSize")?,
        heartbeat_ms: integer("heartbeatMs")?
            .map(|ms| {
                u64::try_from(ms).map_err(|_| {
                    Failure::usage(format!(
                        "stream.heartbeatMs must be a positive integer, got {ms}"
                    ))
                })
            })
            .transpose()?,
    };
    stream.check().map_err(Failure::usage)?;
    Ok(ApiOptions {
        stream,
        mcp: McpOptions {
            server_name: text(server.as_ref(), "name"),
            server_version: text(server.as_ref(), "version"),
            instructions: text(mcp.as_ref(), "instructions"),
        },
        tools: None,
    })
}

/// `eng_stream_poll`.
///
/// # Safety
/// `out_ptr` and `out_len` must be valid for writes.
pub unsafe fn stream_poll(
    engine: u64,
    stream: u64,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    if out_ptr.is_null() || out_len.is_null() {
        return ENG_BAD_ARGUMENTS;
    }
    let Some(handle) = handle(engine) else {
        return ENG_CLOSED;
    };
    // Out of the table while it reads, so polls of other streams go on.
    let Some(taken) = handle.streams().remove(&stream) else {
        return ENG_CLOSED;
    };
    let polled = catch_unwind(AssertUnwindSafe(move || {
        let mut taken = taken;
        let pulled = taken.next(Duration::ZERO);
        (pulled, taken)
    }));
    let outcome = match polled {
        Ok((Pull::End, _)) => Outcome::Closed,
        Ok((pulled, taken)) => {
            handle.streams().insert(stream, taken);
            match pulled {
                Pull::Data(bytes) => Outcome::Done(bytes),
                _ => Outcome::Done(Vec::new()),
            }
        }
        Err(payload) => Outcome::Failed(Failure::panic(payload.as_ref())),
    };
    finish(out_ptr, out_len, outcome)
}

/// `eng_stream_close`.
pub fn stream_close(engine: u64, stream: u64) -> i32 {
    let Some(handle) = handle(engine) else {
        return ENG_CLOSED;
    };
    let taken = handle.streams().remove(&stream);
    match taken {
        Some(mut stream) => {
            stream.close();
            ENG_OK
        }
        None => ENG_CLOSED,
    }
}

/// `eng_wait`.
///
/// # Safety
/// `epoch` must be valid for writes.
pub unsafe fn wait(engine: u64, after: u64, timeout_ms: u32, epoch: *mut u64) -> i32 {
    if epoch.is_null() {
        return ENG_BAD_ARGUMENTS;
    }
    let Some(handle) = handle(engine) else {
        return ENG_CLOSED;
    };
    let waited = catch_unwind(AssertUnwindSafe(|| {
        if handle.engine.is_closed() {
            return None;
        }
        let notifier = Arc::clone(handle.engine.db().notifier());
        let moved = notifier.wait(after, Duration::from_millis(u64::from(timeout_ms)));
        // A shutdown moves the epoch to wake every waiter.
        (!handle.engine.is_closed()).then_some(moved)
    }));
    match waited {
        Ok(Some(moved)) => {
            epoch.write_unaligned(moved);
            ENG_OK
        }
        Ok(None) => ENG_CLOSED,
        Err(_) => ENG_ERROR,
    }
}

/// `eng_runner`.
///
/// # Safety
/// `request` must point to `request_len` readable bytes; `out_ptr` and
/// `out_len` must be valid for writes.
pub unsafe fn runner(
    engine: u64,
    request: *const u8,
    request_len: usize,
    out_ptr: *mut *mut u8,
    out_len: *mut usize,
) -> i32 {
    if out_ptr.is_null() || out_len.is_null() {
        return ENG_BAD_ARGUMENTS;
    }
    let Some(request) = input(request, request_len) else {
        return ENG_BAD_ARGUMENTS;
    };
    let Some(handle) = handle(engine) else {
        return ENG_CLOSED;
    };
    let outcome = guarded(|| {
        let run = || -> Result<Vec<u8>, Failure> {
            let request = parse(request, "a runner request")?;
            let action = request
                .get("action")
                .and_then(Value::as_str)
                .unwrap_or_default();
            let op = match action {
                "start" => "runner.start",
                "stop" => "runner.stop",
                "run_due" => "runner.runDue",
                "status" => "runner.status",
                other => {
                    return Err(Failure::usage(format!(
                        "a runner request's action is start, stop, run_due or status, got {}",
                        json::quote(other)
                    )))
                }
            };
            match calls::call(&handle.engine, op, None, &[]) {
                Ok(None) => Ok(Vec::new()),
                Ok(Some(result)) => Ok(written(&result)),
                Err(error) => Err(Failure::of(&error)),
            }
        };
        run().into()
    });
    finish(out_ptr, out_len, outcome)
}

/// `eng_free`.
///
/// # Safety
/// `ptr` and `len` must be one output of an export, not yet released, or
/// null.
pub unsafe fn free(ptr: *mut u8, len: usize) {
    if !ptr.is_null() {
        drop(Box::from_raw(std::ptr::slice_from_raw_parts_mut(ptr, len)));
    }
}
