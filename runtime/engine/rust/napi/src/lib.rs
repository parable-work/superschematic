//! The engine's Node-API addon as a library (D58): what
//! `@superschematic/engine` loads to run on the Rust engine, and
//! [`export_engine_napi!`], which a cdylib crate expands once over the
//! behaviors its addon is built with, the twin of the C ABI's
//! `export_engine_c_abi!` (`superschematic-engine-abi`). The library exports
//! nothing itself: `superschematic-engine-addon` (`runtime/engine/rust/addon`)
//! is the core's addon, over the core's behaviors and the work queue's, and
//! a project whose schemas compose an extension's behaviors builds its own
//! from the `napi/` member of the engine crate `projectengine` writes, over
//! that crate's `behaviors()`. Two addons in one Cargo workspace, whose
//! features Cargo unifies, so never register a class twice.
//!
//! An addon holds one engine per `NativeEngine` and makes its calls by name
//! ([`superschematic_engine::calls`]) with JSON in and out, so the
//! TypeScript facade (`src/native/` in the package) keeps the engine's
//! synchronous, principal-first API over it:
//!
//! - `NativeEngine.open(optionsJson, host?)` opens an engine on the
//!   options [`EngineOptions::from_json`] reads, with the behaviors the
//!   addon was built with ([`Project::behaviors`]): the core's of them
//!   ([`superschematic_engine_behaviors::NAMES`]) always, then each one the
//!   options' `behaviors` member names, in its order, as the TypeScript
//!   engine registers a deployment's after the core's. A name is always a
//!   behavior in Rust; a behavior object is TypeScript code, which the
//!   options' `jsBehaviors` describe (below).
//!   The options' `hosted` member, which the addon takes off first, lists
//!   what the facade answers in JavaScript through `host(kind, json)`: a
//!   `policy` written in TypeScript, which gets each question as the
//!   TypeScript engine asks it, and a `permissionMatcher`, which gets
//!   `[held, required]`; each answers `"true"` to allow.
//! - `NativeEngine.openAsync(optionsJson)` opens one as `open` does, off
//!   the JavaScript thread, and returns a promise of it: what a server on
//!   Postgres opens with, since the open connects and migrates while the
//!   thread serves, and the connection may be one the thread itself
//!   carries (a Cloud SQL socket the host serves). Its options are data
//!   alone: none hosts anything on the JavaScript thread.
//! - `callSync(requestJson)` makes a call on the JavaScript thread and
//!   returns its result as JSON text, `""` where the TypeScript method
//!   returns `undefined`. A request is `{ op, principal?, args?, now? }`:
//!   `op` a call's name, `principal` the caller for a call that takes one,
//!   `args` the arguments after it, and `now` the time a manual clock is
//!   set to first. An engine on Postgres refuses it
//!   (`sync_on_async_storage`), since its call waits on the network.
//! - `call(requestJson)` makes the call off the JavaScript thread and
//!   returns a promise: on SQLite an `AsyncTask` on libuv's pool, on
//!   Postgres a future on tokio's blocking pool that owns the engine, its
//!   error built on the JavaScript thread. Neither is `async fn(&self)`,
//!   which Bun's test runner aborts on when one is pending at exit. An
//!   engine with a hosted policy or matcher refuses it, since they answer
//!   on the JavaScript thread alone.
//! - `shutdown()` stops the runner and ends every event stream; `close()`
//!   shuts down and lets the engine go, its storage closing once a call in
//!   flight returns.
//! - `epoch` is how many commits the engine's notifier has counted, which
//!   the facade reads after each call to tell its event watchers what the
//!   call committed; `watch(callback)` calls back on the JavaScript thread
//!   when a commit made elsewhere (the runner's thread, an asynchronous
//!   call, another process on Postgres) moves it, through a weak
//!   threadsafe function, so it never holds the process open.
//! - `abiVersion()` is [`ABI_VERSION`], `engineRoutes()` the routes the
//!   engine serves, and `project()` the name of the crate that built the
//!   addon ([`Project::name`]).
//! - The test addon, built with the `js-behaviors` feature, runs behaviors
//!   written in TypeScript (`bridge`): the options' `jsBehaviors` and
//!   `registerBehavior(descriptor)` register what the facade describes,
//!   and `bridgeCall(generation, method, args)` is a behavior context's
//!   method while its hook runs. An engine with one is `hosted`: it is
//!   called on the JavaScript thread alone, and the facade runs its runner
//!   on the event loop. An addon built without the feature, as every
//!   shipped one is, has no `bridgeCall` and refuses such a behavior.
//!
//! A refusal is thrown as an `Error` whose `name` is the TypeScript class
//! (`EngineError`, `BehaviorVetoError`, `TypeError`, ...), with its `code`
//! and its class's own members as `details` ([`superschematic_engine::Thrown`]),
//! which the facade puts back on the class. A panic is caught at every
//! entry and thrown as an error, so none unwinds across a JavaScript
//! frame; one inside a transaction poisons the engine, which then refuses
//! every call.

use std::cell::Cell;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, PoisonError, RwLock};

use napi::bindgen_prelude::*;
use napi::sys;
use napi::threadsafe_function::ThreadsafeFunctionCallMode;
use napi_derive::napi;
use serde_json::Value;

use superschematic_engine::storage::{Dialect, Lock};
use superschematic_engine::{
    calls, json, AccessPolicy, AccessRequest, CustomIds, Engine, EngineOptions, Error as Failed,
    IdSource, Ids, PermissionMatcher, Policy, Thrown,
};

#[cfg(feature = "js-behaviors")]
mod bridge;
mod macros;
mod sql;
#[cfg(feature = "js-behaviors")]
use bridge::Entry;

/// What [`export_engine_napi!`]'s expansion names: the crates its
/// `#[napi]` items are written in and the library's items they call. Not
/// an API.
#[doc(hidden)]
pub mod __private {
    pub use napi;
    pub use napi_derive;

    #[cfg(feature = "js-behaviors")]
    pub use crate::bridge::bridge_call;
    pub use crate::{
        engine_routes, CallTask, HostFunction, HttpAnswer, Native, OpenTask, StreamPull,
    };
}

pub use superschematic_engine::BehaviorSet;

// Entry, without the bridge, holds nothing.
#[cfg(not(feature = "js-behaviors"))]
struct Entry;

#[cfg(not(feature = "js-behaviors"))]
impl Entry {
    fn enter() -> Self {
        Entry
    }
}

/// The version of the contract between the addon and the facade: the
/// request, host and error shapes above. The facade refuses an addon of
/// another, which a checkout's stale `native/` would be.
pub const ABI_VERSION: u32 = 3;

/// The addon an [`export_engine_napi!`] expansion builds: the crate's
/// name, which its `project()` reports, and every behavior it is built
/// with, the core's among them.
pub struct Project {
    pub name: &'static str,
    pub behaviors: fn() -> BehaviorSet,
}

/// The routes the engine serves (`api::routes`), as JSON: what the
/// facade's HTTP adapters mount, for any engine.
pub fn engine_routes() -> String {
    serde_json::to_string(&superschematic_engine::api::routes()).unwrap_or_else(|_| "[]".to_owned())
}

/// What the addon throws: an `Error` named for the TypeScript class, with
/// its code and its class's members.
#[derive(Debug)]
pub struct Failure(Box<Thrown>, Option<u64>);

impl Failure {
    fn new(class: &'static str, code: Option<&str>, message: impl Into<String>) -> Self {
        Failure(
            Box::new(Thrown {
                class,
                code: code.map(Value::from),
                message: message.into(),
                members: None,
            }),
            None,
        )
    }

    /// The failure, naming the engine error the bridge parked for it,
    /// which a behavior that rethrows it hands back
    /// (`src/native/bridge.ts`).
    #[cfg_attr(not(feature = "js-behaviors"), allow(dead_code))]
    fn with_rust_token(mut self, token: u64) -> Self {
        self.1 = Some(token);
        self
    }

    // usage is an argument the addon cannot take, which the TypeScript
    // engine would throw as a TypeError.
    fn usage(message: impl Into<String>) -> Self {
        Failure::new("TypeError", None, message)
    }

    fn panic(payload: &(dyn std::any::Any + Send)) -> Self {
        let message = payload
            .downcast_ref::<&str>()
            .map(|text| (*text).to_owned())
            .or_else(|| payload.downcast_ref::<String>().cloned())
            .unwrap_or_else(|| "a panic".to_owned());
        Failure::new(
            "Error",
            Some("internal"),
            format!("the engine panicked: {message}"),
        )
    }

    /// An engine's error as the TypeScript engine throws it.
    fn of(error: &Failed) -> Self {
        Failure(Box::new(error.thrown()), None)
    }

    /// The error to throw, built on the JavaScript thread.
    fn into_napi(self, env: &Env) -> napi::Error {
        self.build(env).unwrap_or_else(|error| error)
    }

    fn build(&self, env: &Env) -> Result<napi::Error> {
        let thrown = &self.0;
        let mut error = env.create_error(napi::Error::new(
            Status::GenericFailure,
            thrown.message.clone(),
        ))?;
        error.set_named_property("name", thrown.class)?;
        match &thrown.code {
            Some(code) => error.set_named_property("code", parse(env, code)?)?,
            None => {
                error.delete_named_property("code")?;
            }
        }
        if let Some(members) = &thrown.members {
            error.set_named_property("details", parse(env, &Value::Object(members.clone()))?)?;
        }
        if let Some(token) = self.1 {
            #[allow(clippy::cast_precision_loss)] // tokens stay below 2^53
            error.set_named_property("rustToken", token as f64)?;
        }
        Ok(napi::Error::from(error.into_unknown(env)?))
    }
}

// parse is a JSON value made a JavaScript one by JSON.parse, so a number
// keeps the text the engine wrote it with (arbitrary_precision).
fn parse<'env>(env: &'env Env, value: &Value) -> Result<Unknown<'env>> {
    let global = env.get_global()?;
    let json_object: Object = global.get_named_property("JSON")?;
    let parse: Function<String, Unknown> = json_object.get_named_property("parse")?;
    parse.call(json::stringify(value))
}

/// The facade's function that answers what the options leave to
/// JavaScript: `host(kind, json) -> json`.
pub type HostFunction = FunctionRef<FnArgs<(String, String)>, String>;

thread_local! {
    // The environment of the synchronous call this thread is making, set
    // while it makes it: only the JavaScript thread makes one, so a host
    // callback finds it there and nowhere else.
    static SYNC_ENV: Cell<sys::napi_env> = const { Cell::new(std::ptr::null_mut()) };
    // Whether a host callback is running on this thread: a call it makes
    // back into the engine is refused, since it would meet the engine's
    // own transaction.
    static IN_HOST: Cell<bool> = const { Cell::new(false) };
}

// OnJsThread marks a synchronous call on the JavaScript thread for as long
// as it lives.
struct OnJsThread {
    previous: sys::napi_env,
}

impl OnJsThread {
    fn enter(env: &Env) -> Self {
        OnJsThread {
            previous: SYNC_ENV.replace(env.raw()),
        }
    }
}

impl Drop for OnJsThread {
    fn drop(&mut self) {
        SYNC_ENV.set(self.previous);
    }
}

// Host is the facade's host function, which the engine asks on the
// JavaScript thread, inside a synchronous call. The facade's function
// never throws: it keeps what the TypeScript code threw and rethrows it
// once the call returns.
#[derive(Clone)]
struct Host(Arc<HostFunction>);

impl Host {
    // ask is the facade's answer, or None off the JavaScript thread (an
    // asynchronous call or the runner's thread), where it cannot be asked.
    fn ask(&self, kind: &str, payload: String) -> Option<String> {
        let raw = SYNC_ENV.get();
        if raw.is_null() {
            return None;
        }
        let env = Env::from_raw(raw);
        let was = IN_HOST.replace(true);
        let answer = self
            .0
            .borrow_back(&env)
            .and_then(|function| function.call(FnArgs::from((kind.to_owned(), payload))));
        IN_HOST.set(was);
        answer.ok()
    }
}

// HostPolicy is an access policy written in TypeScript: each question as
// the TypeScript engine passes it to a policy.
struct HostPolicy(Host);

impl AccessPolicy for HostPolicy {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        let principal = serde_json::to_value(request.principal()).unwrap_or(Value::Null);
        self.0
            .ask("policy", json::stringify(&request.to_json(principal)))
            .is_some_and(|answer| answer == "true")
    }
}

// HostMatcher is a permission matcher written in TypeScript.
struct HostMatcher(Host);

impl PermissionMatcher for HostMatcher {
    fn matches(&self, held: &[String], required: &[String]) -> bool {
        let pair = Value::Array(vec![
            held.iter().map(|each| Value::from(each.as_str())).collect(),
            required
                .iter()
                .map(|each| Value::from(each.as_str()))
                .collect(),
        ]);
        self.0
            .ask("permissionMatcher", json::stringify(&pair))
            .is_some_and(|answer| answer == "true")
    }
}

// behaviors_of is the behaviors an engine registers: the core's of the
// ones the addon is built with, in their order, then each one the options'
// behaviors name, in theirs. A name is always a behavior in Rust: one the
// addon is built with beside the core's.
fn behaviors_of(project: &Project, named: &[Value]) -> std::result::Result<BehaviorSet, Failure> {
    let built = (project.behaviors)();
    let mut behaviors = built.select(is_core);
    for name in named {
        let picked = name
            .as_str()
            .filter(|name| !is_core(name))
            .map(|name| built.select(|each| each == name))
            .filter(|picked| !picked.is_empty())
            .ok_or_else(|| not_built(project, &built, name))?;
        behaviors = behaviors.extend(picked);
    }
    Ok(behaviors)
}

fn is_core(name: &str) -> bool {
    superschematic_engine_behaviors::NAMES.contains(&name)
}

// not_built refuses a name the addon is not built with, naming the ones it
// is beside the core's.
fn not_built(project: &Project, built: &BehaviorSet, name: &Value) -> Failure {
    let beside: Vec<String> = built
        .names()
        .into_iter()
        .filter(|each| !is_core(each))
        .collect();
    let beside = if beside.is_empty() {
        "none".to_owned()
    } else {
        beside.join(", ")
    };
    Failure::usage(format!(
        "behaviors names the behaviors {} is built with beside the core's ({beside}); a behavior written in TypeScript needs the bridge, not {}",
        project.name,
        json::stringify(name)
    ))
}

// What the options open: the engine's, whether the facade answers
// something on the JavaScript thread alone (a policy, a matcher, a
// behavior), and the facade's host function, kept for a behavior
// registered later.
struct Opening {
    options: EngineOptions,
    hosted: bool,
    host: Option<Host>,
    #[cfg(feature = "js-behaviors")]
    released: bridge::Released,
}

// HostToolFilter is a mount's tool filter written in TypeScript (the
// HTTP and MCP adapters' tools option, a ToolTarget's filter): each tool as
// [principal, tool, namespace].
struct HostToolFilter(Host);

impl superschematic_engine::ToolFilter for HostToolFilter {
    fn keeps(
        &self,
        principal: &superschematic_engine::Principal,
        tool: &superschematic_engine::ToolSummary,
        namespace: &str,
    ) -> bool {
        let mut summary = serde_json::Map::new();
        summary.insert("handle".into(), Value::from(tool.handle.as_str()));
        summary.insert("name".into(), Value::from(tool.name.as_str()));
        if let Some(schema) = &tool.schema {
            summary.insert("schema".into(), Value::from(schema.as_str()));
        }
        summary.insert("operation".into(), Value::from(tool.operation.as_str()));
        if let Some(behavior) = &tool.behavior {
            summary.insert("behavior".into(), Value::from(behavior.as_str()));
        }
        summary.insert("writes".into(), Value::Bool(tool.writes));
        let question = Value::Array(vec![
            serde_json::to_value(principal).unwrap_or(Value::Null),
            Value::Object(summary),
            Value::from(namespace),
        ]);
        self.0
            .ask("toolFilter", json::stringify(&question))
            .is_some_and(|answer| answer == "true")
    }
}

// HostIds is an id source written in TypeScript (the engine's ids
// option), asked on the JavaScript thread alone. What it threw the facade
// keeps and rethrows once the call returns.
struct HostIds(Host);

impl IdSource for HostIds {
    fn next_id(&self) -> std::result::Result<String, Failed> {
        let answer = self.0.ask("ids", String::new()).ok_or_else(|| {
            Failed::Usage(
                "an id source written in TypeScript answers on the JavaScript thread alone, in a synchronous call".to_owned(),
            )
        })?;
        let answer: Value = serde_json::from_str(&answer).unwrap_or(Value::Null);
        answer
            .get("id")
            .and_then(Value::as_str)
            .map(str::to_owned)
            .ok_or_else(|| Failed::Internal("the id source written in TypeScript threw".to_owned()))
    }

    // A behavior's own UUIDs come from the host's crypto.randomUUID, where
    // the TypeScript engine's version graph draws them: a recorder that
    // seeds it sees the ids it gave. Off the JavaScript thread, random ones.
    fn next_uuid(&self) -> Option<[u8; 16]> {
        let answer = self.0.ask("uuid", String::new())?;
        let hex: Vec<u8> = answer.bytes().filter(|byte| *byte != b'-').collect();
        if hex.len() != 32 {
            return None;
        }
        let mut bytes = [0u8; 16];
        for (at, pair) in hex.chunks(2).enumerate() {
            let text = std::str::from_utf8(pair).ok()?;
            bytes[at] = u8::from_str_radix(text, 16).ok()?;
        }
        Some(bytes)
    }
}

// options_of reads the options the facade passes: the engine's, the work
// queue's behaviors they name, the behaviors written in TypeScript they
// describe, and what it hosts.
fn options_of(
    project: &Project,
    text: &str,
    host: Option<HostFunction>,
) -> std::result::Result<Opening, Failure> {
    let mut given: Value = serde_json::from_str(text)
        .map_err(|error| Failure::usage(format!("engine options are JSON: {error}")))?;
    let members = given.as_object_mut();
    let (hosted, behaviors) = match members {
        Some(members) => (members.remove("hosted"), members.remove("jsBehaviors")),
        None => (None, None),
    };
    let hosted = match hosted {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(names)) => names,
        Some(_) => {
            return Err(Failure::usage(
                "hosted lists what the facade answers: policy, permissionMatcher, ids",
            ))
        }
    };
    let named = match given
        .as_object_mut()
        .and_then(|members| members.remove("behaviors"))
    {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(names)) => names,
        Some(_) => {
            return Err(Failure::usage(
                "behaviors lists the names of behaviors the addon is built with; a behavior written in TypeScript needs the bridge",
            ))
        }
    };
    let behaviors = match behaviors {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(descriptors)) => descriptors,
        Some(_) => return Err(Failure::usage("jsBehaviors lists behavior descriptors")),
    };
    let mut options = EngineOptions::from_json(&given).map_err(|error| Failure::of(&error))?;
    // The core's, then the ones the options name, in their order, then the
    // ones written in TypeScript (the bridge): a behavior named by a string
    // is always the Rust one the addon is built with, and an object always
    // the bridge's, whatever its declaration names.
    options.behaviors = behaviors_of(project, &named)?;
    let host = host.map(|function| Host(Arc::new(function)));
    let needs_host = || {
        host.clone().ok_or_else(|| {
            Failure::usage(
                "options that host a policy, a matcher or a behavior come with the host function",
            )
        })
    };
    for name in &hosted {
        match name.as_str() {
            Some("policy") => options.policy = Some(Policy::custom(HostPolicy(needs_host()?))),
            Some("permissionMatcher") => {
                options.permission_matcher = Some(Arc::new(HostMatcher(needs_host()?)));
            }
            Some("ids") => {
                options.ids = Ids::Custom(CustomIds(Arc::new(HostIds(needs_host()?))));
            }
            _ => {
                return Err(Failure::usage(format!(
                    "the facade hosts a policy, a permissionMatcher or ids, not {}",
                    json::stringify(name)
                )))
            }
        }
    }
    #[cfg(feature = "js-behaviors")]
    let released = bridge::Released::default();
    #[cfg(feature = "js-behaviors")]
    for descriptor in &behaviors {
        let behavior = bridge::JsBehavior::of(descriptor, &needs_host()?, &released)?;
        options.behaviors.push_dynamic(behavior);
    }
    #[cfg(not(feature = "js-behaviors"))]
    if !behaviors.is_empty() {
        return Err(no_bridge());
    }
    Ok(Opening {
        options,
        hosted: !hosted.is_empty() || !behaviors.is_empty(),
        host,
        #[cfg(feature = "js-behaviors")]
        released,
    })
}

// no_bridge refuses a behavior written in TypeScript in an addon built
// without the bridge, as every shipped addon is.
#[cfg(not(feature = "js-behaviors"))]
fn no_bridge() -> Failure {
    Failure::usage(
        "this addon runs the behaviors compiled into it alone: a behavior written in TypeScript runs only on the test addon, built with the js-behaviors feature",
    )
}

// run makes one call: the request read, the clock set, the call made and
// its result written as JavaScript writes it, a panic caught. An engine
// whose policy or matcher the facade hosts does not start the runner's
// thread, where they could not be asked.
fn run(
    engine: &Engine,
    request: &str,
    hosted: bool,
    host: Option<&Host>,
) -> std::result::Result<String, Failure> {
    match catch_unwind(AssertUnwindSafe(|| call(engine, request, hosted, host))) {
        Ok(outcome) => outcome,
        Err(payload) => Err(Failure::panic(payload.as_ref())),
    }
}

// set_now sets a manual clock to the time a request carries, a call's or
// an HTTP request's: the facade's clock function, read once per request
// as the TypeScript engine reads it.
fn set_now(engine: &Engine, now: Option<&Value>) -> std::result::Result<(), Failure> {
    let Some(now) = now.filter(|now| !now.is_null()) else {
        return Ok(());
    };
    let now = now
        .as_i64()
        .ok_or_else(|| Failure::usage("an engine request's now is epoch milliseconds"))?;
    engine.clock().set(now).map_err(|error| Failure::of(&error))
}

fn call(
    engine: &Engine,
    request: &str,
    hosted: bool,
    host: Option<&Host>,
) -> std::result::Result<String, Failure> {
    let request: Value = serde_json::from_str(request)
        .map_err(|error| Failure::usage(format!("an engine request is JSON: {error}")))?;
    let Value::Object(members) = &request else {
        return Err(Failure::usage(
            "an engine request is { op, principal?, args?, now? }",
        ));
    };
    let op = members
        .get("op")
        .and_then(Value::as_str)
        .ok_or_else(|| Failure::usage("an engine request names its call: op"))?;
    if hosted && op == "runner.start" {
        return Err(Failure::usage(
            "the runner's thread cannot ask a policy or permission matcher written in TypeScript, which answer on the JavaScript thread: run the runner with runDue",
        ));
    }
    let args: &[Value] = match members.get("args") {
        None | Some(Value::Null) => &[],
        Some(Value::Array(args)) => args,
        Some(_) => return Err(Failure::usage("an engine request's args are a list")),
    };
    set_now(engine, members.get("now"))?;
    let principal = match members.get("principal") {
        None | Some(Value::Null) => None,
        Some(given) => Some(
            calls::principal_from_json(given)
                .map_err(|refusal| Failure::of(&Failed::Engine(refusal)))?,
        ),
    };
    // A tools call through a mount's filter written in TypeScript, which
    // the facade answers on this thread.
    let filter = match (members.get("toolFilter"), host) {
        (Some(Value::Bool(true)), Some(host)) => Some(HostToolFilter(host.clone())),
        (Some(Value::Bool(true)), None) => {
            return Err(Failure::usage(
                "a tool filter written in TypeScript comes with the host function",
            ))
        }
        _ => None,
    };
    let filter = filter
        .as_ref()
        .map(|filter| filter as &dyn superschematic_engine::ToolFilter);
    match calls::call_with(engine, op, principal.as_ref(), args, filter) {
        Ok(None) => Ok(String::new()),
        Ok(Some(result)) => Ok(json::stringify(&result)),
        Err(error) => Err(Failure::of(&error)),
    }
}

/// An engine, opened from JavaScript: what an addon's `NativeEngine` holds
/// and calls ([`export_engine_napi!`]), each of its methods one of the
/// class's.
pub struct Native {
    // None once closed.
    engine: RwLock<Option<Arc<Engine>>>,
    dialect: Dialect,
    // Whether the facade answers its policy, matcher or a behavior, on the
    // JavaScript thread alone.
    hosted: AtomicBool,
    // The facade's host function: what answers a tool filter written in
    // TypeScript, and a behavior registered later.
    host: Option<Host>,
    // What stops the watch's thread.
    watching: Mutex<Option<Arc<AtomicBool>>>,
    #[cfg(feature = "js-behaviors")]
    released: bridge::Released,
}

impl Native {
    #[cfg(feature = "js-behaviors")]
    fn register(&self, engine: &Engine, descriptor: &str) -> std::result::Result<(), Failure> {
        let descriptor: Value = serde_json::from_str(descriptor)
            .map_err(|error| Failure::usage(format!("a behavior descriptor is JSON: {error}")))?;
        let host = self.host.as_ref().ok_or_else(|| {
            Failure::usage("an engine registers a behavior written in TypeScript only when it was opened with the host function")
        })?;
        let behavior = bridge::JsBehavior::of(&descriptor, host, &self.released)?;
        engine
            .behaviors()
            .register_dynamic(behavior)
            .map_err(|error| Failure::of(&error))?;
        self.hosted.store(true, Ordering::SeqCst);
        Ok(())
    }

    #[cfg(not(feature = "js-behaviors"))]
    fn register(&self, _engine: &Engine, _descriptor: &str) -> std::result::Result<(), Failure> {
        Err(no_bridge())
    }

    // holding is the Native of an engine opened off the JavaScript thread
    // (openAsync), which hosts nothing.
    fn holding(env: &Env, engine: Arc<Engine>) -> Result<Native> {
        shut_down_with(env, &engine)?;
        Ok(Native {
            dialect: engine.dialect(),
            engine: RwLock::new(Some(engine)),
            hosted: AtomicBool::new(false),
            host: None,
            watching: Mutex::new(None),
            #[cfg(feature = "js-behaviors")]
            released: bridge::Released::default(),
        })
    }

    fn engine(&self) -> std::result::Result<Arc<Engine>, Failure> {
        self.engine
            .read()
            .unwrap_or_else(PoisonError::into_inner)
            .clone()
            .ok_or_else(|| Failure::new("Error", None, "the engine is closed"))
    }
}

/// One HTTP answer: its status, its headers as [name, value] pairs
/// (lowercase names), and its body's bytes or the id of the event stream
/// the engine holds open.
#[napi(object)]
pub struct HttpAnswer {
    pub status: u32,
    pub headers: Vec<Vec<String>>,
    pub body: Option<Buffer>,
    pub stream: Option<f64>,
}

/// One pull of an event stream: its bytes, or its end, or neither.
#[napi(object)]
pub struct StreamPull {
    pub data: Option<Buffer>,
    pub end: bool,
}

// serve_http serves one request whose head is JSON, on the JavaScript
// thread.
fn serve_http(
    engine: &Engine,
    head: &str,
    body: Option<Vec<u8>>,
    host: Option<Host>,
) -> std::result::Result<HttpAnswer, Failure> {
    let head: Value = serde_json::from_str(head)
        .map_err(|error| Failure::usage(format!("an HTTP request's head is JSON: {error}")))?;
    let filter: Option<Arc<dyn superschematic_engine::ToolFilter>> =
        match (head.get("toolFilter"), host) {
            (Some(Value::Bool(true)), Some(host)) => Some(Arc::new(HostToolFilter(host))),
            (Some(Value::Bool(true)), None) => {
                return Err(Failure::usage(
                    "a tool filter written in TypeScript comes with the host function",
                ))
            }
            _ => None,
        };
    set_now(engine, head.get("now"))?;
    let reply = calls::http(engine, &head, body, filter).map_err(|error| Failure::of(&error))?;
    Ok(answer_of(reply))
}

// serve_http_owned serves one request off the JavaScript thread, where no
// filter written in TypeScript can be asked.
fn serve_http_owned(
    engine: &Engine,
    head: &str,
    body: Option<Vec<u8>>,
) -> std::result::Result<calls::HttpReply, Failure> {
    let head: Value = serde_json::from_str(head)
        .map_err(|error| Failure::usage(format!("an HTTP request's head is JSON: {error}")))?;
    if head.get("toolFilter") == Some(&Value::Bool(true)) {
        return Err(Failure::usage(
            "a tool filter written in TypeScript answers on the JavaScript thread: an engine served asynchronously takes none",
        ));
    }
    set_now(engine, head.get("now"))?;
    calls::http(engine, &head, body, None).map_err(|error| Failure::of(&error))
}

fn answer_of(reply: calls::HttpReply) -> HttpAnswer {
    #[allow(clippy::cast_precision_loss)] // a stream id stays below 2^53
    HttpAnswer {
        status: u32::from(reply.status),
        headers: reply
            .headers
            .into_iter()
            .map(|(name, value)| vec![name, value])
            .collect(),
        body: reply.body.map(Buffer::from),
        stream: reply.stream.map(|id| id as f64),
    }
}

fn pull_of(pulled: superschematic_engine::api::Pull) -> StreamPull {
    match pulled {
        superschematic_engine::api::Pull::Data(bytes) => StreamPull {
            data: Some(bytes.into()),
            end: false,
        },
        superschematic_engine::api::Pull::Pending => StreamPull {
            data: None,
            end: false,
        },
        superschematic_engine::api::Pull::End => StreamPull {
            data: None,
            end: true,
        },
    }
}

/// An open on libuv's pool (`openAsync`): the engine opens there, and the
/// class that holds it, `T`, the addon's `NativeEngine`
/// ([`export_engine_napi!`]), is made on the JavaScript thread by `wrap`.
pub struct OpenTask<T> {
    project: &'static Project,
    options: String,
    wrap: fn(Native) -> T,
}

impl<T: ToNapiValue + TypeName> Task for OpenTask<T> {
    type Output = std::result::Result<Arc<Engine>, Failure>;
    type JsValue = T;

    fn compute(&mut self) -> Result<Self::Output> {
        let opened = catch_unwind(AssertUnwindSafe(|| {
            let Opening { options, .. } = options_of(self.project, &self.options, None)?;
            Engine::open_shared(options).map_err(|error| Failure::of(&error))
        }));
        Ok(opened.unwrap_or_else(|payload| Err(Failure::panic(payload.as_ref()))))
    }

    fn resolve(&mut self, env: Env, output: Self::Output) -> Result<T> {
        let engine = output.map_err(|failure| failure.into_napi(&env))?;
        Native::holding(&env, engine).map(self.wrap)
    }
}

/// A call on libuv's pool.
pub struct CallTask {
    engine: Arc<Engine>,
    request: String,
}

impl Task for CallTask {
    type Output = std::result::Result<String, Failure>;
    type JsValue = String;

    fn compute(&mut self) -> Result<Self::Output> {
        Ok(run(&self.engine, &self.request, false, None))
    }

    fn resolve(&mut self, env: Env, output: Self::Output) -> Result<String> {
        output.map_err(|failure| failure.into_napi(&env))
    }
}

impl Native {
    /// Opens an engine on its options' JSON: its storage created if absent
    /// and migrated, the core's behaviors registered. `host` answers what
    /// the options' `hosted` lists.
    pub fn open(
        env: Env,
        project: &Project,
        options: String,
        host: Option<HostFunction>,
    ) -> Result<Native> {
        if IN_HOST.get() {
            return Err(reentry().into_napi(&env));
        }
        let _on_js_thread = OnJsThread::enter(&env);
        let _entry = Entry::enter();
        let opened = catch_unwind(AssertUnwindSafe(|| {
            let Opening {
                options,
                hosted,
                host,
                #[cfg(feature = "js-behaviors")]
                released,
            } = options_of(project, &options, host)?;
            let engine = Engine::open_shared(options).map_err(|error| Failure::of(&error))?;
            Ok::<_, Failure>(Native {
                dialect: engine.dialect(),
                engine: RwLock::new(Some(engine)),
                hosted: AtomicBool::new(hosted),
                host,
                watching: Mutex::new(None),
                #[cfg(feature = "js-behaviors")]
                released,
            })
        }));
        let opened = match opened {
            Ok(Ok(opened)) => opened,
            Ok(Err(failure)) => return Err(failure.into_napi(&env)),
            Err(payload) => return Err(Failure::panic(payload.as_ref()).into_napi(&env)),
        };
        let engine = opened.engine().map_err(|failure| failure.into_napi(&env))?;
        shut_down_with(&env, &engine)?;
        Ok(opened)
    }

    /// Opens an engine as `open` does, off the JavaScript thread, and
    /// returns a promise of it, which `wrap` makes the addon's class of:
    /// its storage created if absent and migrated, the core's behaviors
    /// registered and the ones its `behaviors` name. Its options are data
    /// alone: options that host something on the JavaScript thread are
    /// refused, as they are without `open`'s host function.
    pub fn open_async<T: ToNapiValue + TypeName>(
        env: &Env,
        project: &'static Project,
        options: String,
        wrap: fn(Native) -> T,
    ) -> Result<AsyncTask<OpenTask<T>>> {
        if IN_HOST.get() {
            return Err(reentry().into_napi(env));
        }
        Ok(AsyncTask::new(OpenTask {
            project,
            options,
            wrap,
        }))
    }

    /// Whether the facade answers something of this engine on the
    /// JavaScript thread alone (a policy, a matcher, a behavior written in
    /// TypeScript): its asynchronous call and the runner's thread are
    /// refused, and the facade runs the runner on its event loop.
    pub fn hosted(&self) -> bool {
        self.hosted.load(Ordering::SeqCst)
    }

    /// Whether this addon has the bridge to behaviors written in
    /// TypeScript (the test addon, built with the js-behaviors feature).
    pub fn bridged(&self) -> bool {
        cfg!(feature = "js-behaviors")
    }

    /// Registers a behavior written in TypeScript, as the facade describes
    /// it (`src/native/bridge.ts`), on the test addon alone.
    pub fn register_behavior(&self, env: Env, descriptor: String) -> Result<()> {
        if IN_HOST.get() {
            return Err(reentry().into_napi(&env));
        }
        let _on_js_thread = OnJsThread::enter(&env);
        let _entry = Entry::enter();
        let engine = self.engine().map_err(|failure| failure.into_napi(&env))?;
        let registered = catch_unwind(AssertUnwindSafe(|| self.register(&engine, &descriptor)));
        match registered {
            Ok(Ok(())) => Ok(()),
            Ok(Err(failure)) => Err(failure.into_napi(&env)),
            Err(payload) => Err(Failure::panic(payload.as_ref()).into_napi(&env)),
        }
    }

    /// `"sqlite"` or `"postgres"`.
    pub fn dialect(&self) -> &'static str {
        match self.dialect {
            Dialect::Postgres => "postgres",
            _ => "sqlite",
        }
    }

    /// Makes a call on the JavaScript thread; SQLite only.
    pub fn call_sync(&self, env: Env, request: String) -> Result<String> {
        if IN_HOST.get() {
            return Err(reentry().into_napi(&env));
        }
        let engine = self.engine().map_err(|failure| failure.into_napi(&env))?;
        if self.dialect == Dialect::Postgres {
            return Err(Failure::new(
                "Error",
                Some("sync_on_async_storage"),
                "an engine on Postgres is called through call(), which returns a promise: a synchronous call would hold the JavaScript thread while it waits on the database",
            )
            .into_napi(&env));
        }
        let _on_js_thread = OnJsThread::enter(&env);
        let _entry = Entry::enter();
        run(&engine, &request, self.hosted(), self.host.as_ref())
            .map_err(|failure| failure.into_napi(&env))
    }

    /// Makes a call off the JavaScript thread, returning a promise of its
    /// result.
    pub fn call<'env>(
        &self,
        env: &'env Env,
        request: String,
    ) -> Result<Either<AsyncTask<CallTask>, PromiseRaw<'env, String>>> {
        let engine = self.engine().map_err(|failure| failure.into_napi(env))?;
        if self.hosted() {
            return Err(Failure::usage(
                "an engine whose policy, permission matcher or a behavior is written in TypeScript is called synchronously: they answer on the JavaScript thread",
            )
            .into_napi(env));
        }
        if self.dialect != Dialect::Postgres {
            return Ok(Either::A(AsyncTask::new(CallTask { engine, request })));
        }
        let promise = env.spawn_future_with_callback(
            async move {
                napi::bindgen_prelude::spawn_blocking(move || run(&engine, &request, false, None))
                    .await
                    .map_err(|error| {
                        napi::Error::from_reason(format!(
                            "the engine's call did not finish: {error}"
                        ))
                    })
            },
            |env, outcome| outcome.map_err(|failure| failure.into_napi(env)),
        )?;
        Ok(Either::B(promise))
    }

    /// Serves one HTTP request off the JavaScript thread, as `handleHttp`
    /// does on it: what an engine on Postgres serves through (on SQLite as
    /// well). Its promise resolves on the JavaScript thread. A tool filter
    /// written in TypeScript, which answers on that thread, is refused.
    pub fn handle_http_async<'env>(
        &self,
        env: &'env Env,
        head: String,
        body: Option<Buffer>,
    ) -> Result<PromiseRaw<'env, HttpAnswer>> {
        let engine = self.engine().map_err(|failure| failure.into_napi(env))?;
        if self.hosted() {
            return Err(Failure::usage(
                "an engine whose policy, permission matcher or a behavior is written in TypeScript serves HTTP synchronously: they answer on the JavaScript thread",
            )
            .into_napi(env));
        }
        let body = body.map(|body| body.to_vec());
        env.spawn_future_with_callback(
            async move {
                napi::bindgen_prelude::spawn_blocking(move || {
                    match catch_unwind(AssertUnwindSafe(|| serve_http_owned(&engine, &head, body)))
                    {
                        Ok(outcome) => outcome,
                        Err(payload) => Err(Failure::panic(payload.as_ref())),
                    }
                })
                .await
                .map_err(|error| {
                    napi::Error::from_reason(format!(
                        "the engine's request did not finish: {error}"
                    ))
                })
            },
            |env, outcome| match outcome {
                Ok(reply) => Ok(answer_of(reply)),
                Err(failure) => Err(failure.into_napi(env)),
            },
        )
    }

    /// Pulls an event stream off the JavaScript thread, waiting up to
    /// `timeoutMs` for its next bytes: an engine on Postgres's stream
    /// waits there for a commit in any process.
    pub fn stream_pull_async<'env>(
        &self,
        env: &'env Env,
        id: f64,
        timeout_ms: u32,
    ) -> Result<PromiseRaw<'env, StreamPull>> {
        let engine = self.engine().map_err(|failure| failure.into_napi(env))?;
        #[allow(clippy::cast_possible_truncation, clippy::cast_sign_loss)]
        let id = id as u64;
        env.spawn_future_with_callback(
            async move {
                napi::bindgen_prelude::spawn_blocking(move || {
                    match catch_unwind(AssertUnwindSafe(|| {
                        calls::stream_pull(
                            &engine,
                            id,
                            std::time::Duration::from_millis(u64::from(timeout_ms)),
                        )
                    })) {
                        Ok(outcome) => outcome.map_err(|error| Failure::of(&error)),
                        Err(payload) => Err(Failure::panic(payload.as_ref())),
                    }
                })
                .await
                .map_err(|error| {
                    napi::Error::from_reason(format!("the stream's pull did not finish: {error}"))
                })
            },
            |env, outcome| match outcome {
                Ok(pulled) => Ok(pull_of(pulled)),
                Err(failure) => Err(failure.into_napi(env)),
            },
        )
    }

    /// Runs raw SQL on the engine's SQLite file, as the TypeScript
    /// engine's `engine.storage` does (`get`, `all` and `run` one statement
    /// with its parameters, `exec` statements with none): in a write
    /// transaction of its own, or in a savepoint of the host's
    /// (`storageTransaction`). Nothing checks what it runs: it is the
    /// deployment's own reach into the file, for what the engine's API does
    /// not do (its tests' looks at the tables, an identity store's DDL),
    /// and a write the engine does not expect is the deployment's to
    /// answer for. An engine on Postgres refuses it.
    pub fn storage_query(
        &self,
        env: Env,
        mode: String,
        sql: String,
        params: String,
    ) -> Result<String> {
        if IN_HOST.get() {
            return Err(reentry().into_napi(&env));
        }
        let engine = self.engine().map_err(|failure| failure.into_napi(&env))?;
        if self.dialect == Dialect::Postgres {
            return Err(Failure::usage(
                "engine.storage is the engine's SQLite file: an engine on Postgres has none",
            )
            .into_napi(&env));
        }
        let _on_js_thread = OnJsThread::enter(&env);
        let outcome = catch_unwind(AssertUnwindSafe(
            || -> std::result::Result<Value, Failed> {
                let params: Value = serde_json::from_str(&params)
                    .map_err(|error| Failed::Usage(format!("SQL parameters are JSON: {error}")))?;
                let params = sql::sql_params(Some(&params))?;
                engine
                    .db()
                    .write(Lock::Catalog, |tx| -> std::result::Result<Value, Failed> {
                        Ok(match mode.as_str() {
                            "get" => tx
                                .get(sql.as_str(), &params)?
                                .map_or(Value::Null, |row| sql::row_json(&row)),
                            "all" => Value::Array(
                                tx.all(sql.as_str(), &params)?
                                    .iter()
                                    .map(sql::row_json)
                                    .collect(),
                            ),
                            "run" => sql::run_json(&tx.run(sql.as_str(), &params)?),
                            "exec" => {
                                tx.exec(sql.as_str())?;
                                Value::Null
                            }
                            other => {
                                return Err(Failed::Usage(format!(
                                    "storage runs get, all, run or exec, not {other}"
                                )))
                            }
                        })
                    })
            },
        ));
        match outcome {
            Ok(Ok(value)) => Ok(json::stringify(&value)),
            Ok(Err(error)) => Err(Failure::of(&error).into_napi(&env)),
            Err(payload) => Err(Failure::panic(payload.as_ref()).into_napi(&env)),
        }
    }

    /// Runs `callback` in a write transaction on the engine's SQLite file
    /// that this thread holds while it runs, as the TypeScript engine's
    /// `storage.transaction` does: the raw SQL and the engine's calls it
    /// makes run in savepoints of it, and commit together when it returns
    /// true or roll back together when it returns false (the facade keeps
    /// what it threw). Inside another, a savepoint of that one.
    pub fn storage_transaction(&self, env: Env, callback: Function<(), bool>) -> Result<bool> {
        if IN_HOST.get() {
            return Err(reentry().into_napi(&env));
        }
        let engine = self.engine().map_err(|failure| failure.into_napi(&env))?;
        if self.dialect == Dialect::Postgres {
            return Err(Failure::usage(
                "engine.storage is the engine's SQLite file: an engine on Postgres has none",
            )
            .into_napi(&env));
        }
        let _on_js_thread = OnJsThread::enter(&env);
        // What the callback's own call left, when it failed in JavaScript.
        let mut called: Option<napi::Error> = None;
        let held = engine
            .db()
            .hold(|_tx| -> std::result::Result<bool, Failed> {
                match callback.call(()) {
                    Ok(true) => Ok(true),
                    Ok(false) => Err(Failed::Usage(
                        "the host's transaction rolled back".to_owned(),
                    )),
                    Err(error) => {
                        called = Some(error);
                        Err(Failed::Usage(
                            "the host's transaction rolled back".to_owned(),
                        ))
                    }
                }
            });
        if let Some(error) = called {
            return Err(error);
        }
        match held {
            Ok(_) => Ok(true),
            Err(Failed::Usage(message)) if message == "the host's transaction rolled back" => {
                Ok(false)
            }
            Err(error) => Err(Failure::of(&error).into_napi(&env)),
        }
    }

    /// Whether this thread holds a transaction on the engine's storage: a
    /// host's (`storageTransaction`), or the engine's own around a hook.
    pub fn storage_in_transaction(&self) -> bool {
        self.engine()
            .is_ok_and(|engine| engine.db().in_transaction())
    }

    /// Serves one HTTP request on the JavaScript thread (SQLite): its head
    /// as JSON (`method`, `prefix`, `path`, `query`, `headers`, `caller`,
    /// `requestId`, the mount's `options`, whether its `toolFilter` is
    /// the facade's, and the `now` a manual clock is set to first, as a
    /// call's), and its body's bytes. The answer's body is bytes,
    /// or the id of an event stream the engine holds open, which
    /// `streamPull` pulls.
    pub fn handle_http(&self, env: Env, head: String, body: Option<Buffer>) -> Result<HttpAnswer> {
        if IN_HOST.get() {
            return Err(reentry().into_napi(&env));
        }
        let engine = self.engine().map_err(|failure| failure.into_napi(&env))?;
        if self.dialect == Dialect::Postgres {
            return Err(Failure::new(
                "Error",
                Some("sync_on_async_storage"),
                "an engine on Postgres serves HTTP through handleHttpAsync, which returns a promise",
            )
            .into_napi(&env));
        }
        let _on_js_thread = OnJsThread::enter(&env);
        let _entry = Entry::enter();
        let host = self.host.clone();
        let served = catch_unwind(AssertUnwindSafe(|| {
            serve_http(&engine, &head, body.map(|body| body.to_vec()), host)
        }));
        match served {
            Ok(Ok(answer)) => Ok(answer),
            Ok(Err(failure)) => Err(failure.into_napi(&env)),
            Err(payload) => Err(Failure::panic(payload.as_ref()).into_napi(&env)),
        }
    }

    /// Pulls an event stream `handleHttp` holds open, on the JavaScript
    /// thread and without waiting: its next bytes, `end` once it is over,
    /// or neither while it has nothing yet (the facade waits for a commit
    /// or the heartbeat, and pulls again).
    pub fn stream_pull(&self, env: Env, id: f64) -> Result<StreamPull> {
        let engine = self.engine().map_err(|failure| failure.into_napi(&env))?;
        let _on_js_thread = OnJsThread::enter(&env);
        let _entry = Entry::enter();
        #[allow(clippy::cast_possible_truncation, clippy::cast_sign_loss)]
        let id = id as u64;
        let pulled = catch_unwind(AssertUnwindSafe(|| {
            calls::stream_pull(&engine, id, std::time::Duration::ZERO)
        }));
        match pulled {
            Ok(Ok(pulled)) => Ok(pull_of(pulled)),
            Ok(Err(error)) => Err(Failure::of(&error).into_napi(&env)),
            Err(payload) => Err(Failure::panic(payload.as_ref()).into_napi(&env)),
        }
    }

    /// Ends an event stream `handleHttp` holds open: the client went away.
    pub fn stream_close(&self, id: f64) {
        if let Ok(engine) = self.engine() {
            #[allow(clippy::cast_possible_truncation, clippy::cast_sign_loss)]
            let _ = calls::call(&engine, "stream.close", None, &[Value::from(id as u64)]);
        }
    }

    /// How many commits that notified the engine's notifier has counted
    /// (D58): the facade reads it after each call to tell its watchers of
    /// what the call committed.
    pub fn epoch(&self) -> f64 {
        #[allow(clippy::cast_precision_loss)] // an epoch stays below 2^53
        self.engine()
            .map_or(0.0, |engine| engine.db().notifier().epoch() as f64)
    }

    /// Calls `callback(epoch)` on the JavaScript thread each time a commit
    /// moves the engine's notifier: one the runner's thread, an
    /// asynchronous call or, on Postgres, another process made. A thread of
    /// its own waits on the notifier and calls through a weak threadsafe
    /// function, so a watch never holds the process open. It ends when the
    /// engine shuts down (a last call) or at `unwatch`; a second watch
    /// replaces the first.
    pub fn watch(&self, env: Env, callback: Function<f64, ()>) -> Result<()> {
        let engine = self.engine().map_err(|failure| failure.into_napi(&env))?;
        let tsfn = callback
            .build_threadsafe_function::<f64>()
            .weak::<true>()
            .build()?;
        let stop = Arc::new(AtomicBool::new(false));
        if let Some(previous) = self
            .watching
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .replace(Arc::clone(&stop))
        {
            previous.store(true, Ordering::SeqCst);
        }
        let notifier = Arc::clone(engine.db().notifier());
        let weak = Arc::downgrade(&engine);
        drop(engine);
        std::thread::Builder::new()
            .name("engine-watch".to_owned())
            .spawn(move || {
                let mut seen = notifier.epoch();
                while !stop.load(Ordering::SeqCst) {
                    let epoch = notifier.wait(seen, std::time::Duration::from_millis(250));
                    let closed = weak.upgrade().is_none_or(|engine| engine.is_closed());
                    if epoch > seen || closed {
                        seen = epoch;
                        #[allow(clippy::cast_precision_loss)]
                        tsfn.call(epoch as f64, ThreadsafeFunctionCallMode::NonBlocking);
                    }
                    if closed {
                        break;
                    }
                }
            })
            .map_err(|error| {
                napi::Error::from_reason(format!("could not start the engine's watch: {error}"))
            })?;
        Ok(())
    }

    /// Ends the watch `watch` started.
    pub fn unwatch(&self) {
        if let Some(stop) = self
            .watching
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .take()
        {
            stop.store(true, Ordering::SeqCst);
        }
    }

    /// Stops the runner and ends every event stream; every later call is
    /// refused.
    pub fn shutdown(&self) {
        if let Ok(engine) = self.engine() {
            engine.shutdown();
        }
    }

    /// Shuts the engine down and lets it go: its storage closes once a
    /// call in flight returns.
    pub fn close(&self) {
        let engine = self
            .engine
            .write()
            .unwrap_or_else(PoisonError::into_inner)
            .take();
        if let Some(engine) = engine {
            engine.shutdown();
        }
    }
}

// shut_down_with stops the engine's runner and streams when the runtime
// tears its environment down (a worker that ends, a process that exits).
fn shut_down_with(env: &Env, engine: &Arc<Engine>) -> Result<()> {
    env.add_env_cleanup_hook(Arc::downgrade(engine), |engine| {
        if let Some(engine) = engine.upgrade() {
            engine.shutdown();
        }
    })?;
    Ok(())
}

// reentry is the refusal of a call that a behavior, a policy or a matcher
// written in TypeScript makes back into the engine itself while it runs,
// rather than through its context: it would open a second transaction
// inside the call's (D58: a behavior reaches the engine through its
// context alone).
fn reentry() -> Failure {
    Failure::usage(
        "a behavior, access policy or permission matcher written in TypeScript cannot call the engine itself while it runs: it runs inside the engine's transaction, so a behavior reaches the engine through its context",
    )
}
