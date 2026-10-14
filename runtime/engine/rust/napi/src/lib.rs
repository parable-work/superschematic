//! The engine's Node-API addon (D58): what `@superschematic/engine` loads
//! to run on the Rust engine. It holds one engine per `NativeEngine` and
//! makes its calls by name ([`superschematic_engine::calls`]) with JSON in
//! and out, so the TypeScript facade (`src/native/` in the package) keeps
//! the engine's synchronous, principal-first API over it.
//!
//! - `NativeEngine.open(optionsJson, host?)` opens an engine on the
//!   options [`EngineOptions::from_json`] reads, with the core's behaviors
//!   this addon was built with ([`superschematic_engine_behaviors::core`]).
//!   The options' `hosted` member, which the addon takes off first, lists
//!   what the facade answers in JavaScript through `host(kind, json)`: a
//!   `policy` written in TypeScript, which gets each question as the
//!   TypeScript engine asks it, and a `permissionMatcher`, which gets
//!   `[held, required]`; each answers `"true"` to allow.
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
use std::sync::{Arc, PoisonError, RwLock};

use napi::bindgen_prelude::*;
use napi::sys;
use napi_derive::napi;
use serde_json::Value;

use superschematic_engine::storage::Dialect;
use superschematic_engine::{
    calls, json, AccessPolicy, AccessRequest, Engine, EngineOptions, Error as Failed,
    PermissionMatcher, Policy, Thrown,
};

/// The version of the contract between the addon and the facade: the
/// request, host and error shapes above. The facade refuses an addon of
/// another, which a checkout's stale `native/` would be.
pub const ABI_VERSION: u32 = 1;

/// The contract's version ([`ABI_VERSION`]).
#[napi]
pub fn abi_version() -> u32 {
    ABI_VERSION
}

/// What the addon throws: an `Error` named for the TypeScript class, with
/// its code and its class's members.
#[derive(Debug)]
pub struct Failure(Box<Thrown>);

impl Failure {
    fn new(class: &'static str, code: Option<&str>, message: impl Into<String>) -> Self {
        Failure(Box::new(Thrown {
            class,
            code: code.map(Value::from),
            message: message.into(),
            members: None,
        }))
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
        Failure(Box::new(error.thrown()))
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

// The facade's function that answers what the options leave to
// JavaScript: host(kind, json) -> json.
type HostFunction = FunctionRef<FnArgs<(String, String)>, String>;

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

// options_of reads the options the facade passes: the engine's, and what
// it hosts.
fn options_of(
    text: &str,
    host: Option<HostFunction>,
) -> std::result::Result<(EngineOptions, bool), Failure> {
    let mut given: Value = serde_json::from_str(text)
        .map_err(|error| Failure::usage(format!("engine options are JSON: {error}")))?;
    let hosted = match given
        .as_object_mut()
        .and_then(|members| members.remove("hosted"))
    {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(names)) => names,
        Some(_) => {
            return Err(Failure::usage(
                "hosted lists what the facade answers: policy, permissionMatcher",
            ))
        }
    };
    let mut options = EngineOptions::from_json(&given).map_err(|error| Failure::of(&error))?;
    options.behaviors = superschematic_engine_behaviors::core();
    if hosted.is_empty() {
        return Ok((options, false));
    }
    let host = Host(Arc::new(host.ok_or_else(|| {
        Failure::usage("options that host a policy or a matcher come with the host function")
    })?));
    for name in hosted {
        match name.as_str() {
            Some("policy") => options.policy = Some(Policy::custom(HostPolicy(host.clone()))),
            Some("permissionMatcher") => {
                options.permission_matcher = Some(Arc::new(HostMatcher(host.clone())));
            }
            _ => {
                return Err(Failure::usage(format!(
                    "the facade hosts a policy or a permissionMatcher, not {}",
                    json::stringify(&name)
                )))
            }
        }
    }
    Ok((options, true))
}

// run makes one call: the request read, the clock set, the call made and
// its result written as JavaScript writes it, a panic caught. An engine
// whose policy or matcher the facade hosts does not start the runner's
// thread, where they could not be asked.
fn run(engine: &Engine, request: &str, hosted: bool) -> std::result::Result<String, Failure> {
    match catch_unwind(AssertUnwindSafe(|| call(engine, request, hosted))) {
        Ok(outcome) => outcome,
        Err(payload) => Err(Failure::panic(payload.as_ref())),
    }
}

fn call(engine: &Engine, request: &str, hosted: bool) -> std::result::Result<String, Failure> {
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
    if let Some(now) = members.get("now").filter(|now| !now.is_null()) {
        let now = now
            .as_i64()
            .ok_or_else(|| Failure::usage("an engine request's now is epoch milliseconds"))?;
        engine
            .clock()
            .set(now)
            .map_err(|error| Failure::of(&error))?;
    }
    let principal = match members.get("principal") {
        None | Some(Value::Null) => None,
        Some(given) => Some(
            calls::principal_from_json(given)
                .map_err(|refusal| Failure::of(&Failed::Engine(refusal)))?,
        ),
    };
    match calls::call(engine, op, principal.as_ref(), args) {
        Ok(None) => Ok(String::new()),
        Ok(Some(result)) => Ok(json::stringify(&result)),
        Err(error) => Err(Failure::of(&error)),
    }
}

/// An engine, opened from JavaScript.
#[napi]
pub struct NativeEngine {
    // None once closed.
    engine: RwLock<Option<Arc<Engine>>>,
    dialect: Dialect,
    // Whether the facade answers its policy or matcher, on the JavaScript
    // thread alone.
    hosted: bool,
}

impl NativeEngine {
    fn engine(&self) -> std::result::Result<Arc<Engine>, Failure> {
        self.engine
            .read()
            .unwrap_or_else(PoisonError::into_inner)
            .clone()
            .ok_or_else(|| Failure::new("Error", None, "the engine is closed"))
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
        Ok(run(&self.engine, &self.request, false))
    }

    fn resolve(&mut self, env: Env, output: Self::Output) -> Result<String> {
        output.map_err(|failure| failure.into_napi(&env))
    }
}

#[napi]
impl NativeEngine {
    /// Opens an engine on its options' JSON: its storage created if absent
    /// and migrated, the core's behaviors registered. `host` answers what
    /// the options' `hosted` lists.
    #[napi(factory)]
    pub fn open(env: Env, options: String, host: Option<HostFunction>) -> Result<NativeEngine> {
        if IN_HOST.get() {
            return Err(reentry().into_napi(&env));
        }
        let _on_js_thread = OnJsThread::enter(&env);
        let opened = catch_unwind(AssertUnwindSafe(|| {
            let (options, hosted) = options_of(&options, host)?;
            let engine = Engine::open_shared(options).map_err(|error| Failure::of(&error))?;
            Ok::<_, Failure>((engine, hosted))
        }));
        let (engine, hosted) = match opened {
            Ok(Ok(opened)) => opened,
            Ok(Err(failure)) => return Err(failure.into_napi(&env)),
            Err(payload) => return Err(Failure::panic(payload.as_ref()).into_napi(&env)),
        };
        // A runtime that tears its environment down (a worker that ends,
        // a process that exits) stops the engine's runner and streams.
        env.add_env_cleanup_hook(Arc::downgrade(&engine), |engine| {
            if let Some(engine) = engine.upgrade() {
                engine.shutdown();
            }
        })?;
        Ok(NativeEngine {
            dialect: engine.dialect(),
            engine: RwLock::new(Some(engine)),
            hosted,
        })
    }

    /// `"sqlite"` or `"postgres"`.
    #[napi(getter)]
    pub fn dialect(&self) -> &'static str {
        match self.dialect {
            Dialect::Postgres => "postgres",
            _ => "sqlite",
        }
    }

    /// Makes a call on the JavaScript thread; SQLite only.
    #[napi]
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
        run(&engine, &request, self.hosted).map_err(|failure| failure.into_napi(&env))
    }

    /// Makes a call off the JavaScript thread, returning a promise of its
    /// result.
    #[napi(ts_return_type = "Promise<string>")]
    pub fn call<'env>(
        &self,
        env: &'env Env,
        request: String,
    ) -> Result<Either<AsyncTask<CallTask>, PromiseRaw<'env, String>>> {
        let engine = self.engine().map_err(|failure| failure.into_napi(env))?;
        if self.hosted {
            return Err(Failure::usage(
                "an engine whose policy or permission matcher is written in TypeScript is called synchronously: they answer on the JavaScript thread",
            )
            .into_napi(env));
        }
        if self.dialect != Dialect::Postgres {
            return Ok(Either::A(AsyncTask::new(CallTask { engine, request })));
        }
        let promise = env.spawn_future_with_callback(
            async move {
                napi::bindgen_prelude::spawn_blocking(move || run(&engine, &request, false))
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

    /// Stops the runner and ends every event stream; every later call is
    /// refused.
    #[napi]
    pub fn shutdown(&self) {
        if let Ok(engine) = self.engine() {
            engine.shutdown();
        }
    }

    /// Shuts the engine down and lets it go: its storage closes once a
    /// call in flight returns.
    #[napi]
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

// reentry is the refusal of a call a hosted policy or matcher makes back
// into an engine while it answers.
fn reentry() -> Failure {
    Failure::usage(
        "an access policy or permission matcher written in TypeScript cannot call the engine: it answers inside the engine's transaction",
    )
}
