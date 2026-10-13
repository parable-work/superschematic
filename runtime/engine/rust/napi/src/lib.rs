//! The engine's Node-API addon (D58): what `@superschematic/engine` loads
//! to run on the Rust engine. It holds one engine per `NativeEngine` and
//! makes its calls by name ([`superschematic_engine::calls`]) with JSON in
//! and out, so the TypeScript facade (`src/native/` in the package) keeps
//! the engine's synchronous, principal-first API over it.
//!
//! - `NativeEngine.open(optionsJson)` opens an engine on the options
//!   [`EngineOptions::from_json`] reads, with the core's behaviors this
//!   addon was built with ([`superschematic_engine_behaviors::core`]).
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
//!   which Bun's test runner aborts on when one is pending at exit.
//! - `shutdown()` stops the runner and ends every event stream; `close()`
//!   shuts down and lets the engine go, its storage closing once a call in
//!   flight returns.
//!
//! A refusal is thrown as an `Error` whose `name` is the TypeScript class
//! (`EngineError`, `BehaviorVetoError`, `TypeError`, ...), with its `code`
//! and its class's own members as `details`, which the facade puts back
//! on the class. A panic is caught at every entry and thrown as an error,
//! so none unwinds across a JavaScript frame; one inside a transaction
//! poisons the engine, which then refuses every call.

use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::{Arc, PoisonError, RwLock};

use napi::bindgen_prelude::*;
use napi_derive::napi;
use serde_json::{Map, Value};

use superschematic_engine::storage::Dialect;
use superschematic_engine::{
    calls, json, Engine, EngineError, EngineErrorKind, EngineOptions, Error as Failed,
};

/// The version of the contract between the addon and the facade: the
/// request and error shapes above. The facade refuses an addon of another,
/// which a checkout's stale `native/` would be.
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

#[derive(Debug)]
struct Thrown {
    name: String,
    code: Option<Value>,
    message: String,
    details: Option<Value>,
}

impl Failure {
    fn new(name: &str, code: Option<&str>, message: impl Into<String>) -> Self {
        Failure::of_parts(name, code.map(Value::from), message.into(), None)
    }

    fn of_parts(name: &str, code: Option<Value>, message: String, details: Option<Value>) -> Self {
        Failure(Box::new(Thrown {
            name: name.to_owned(),
            code,
            message,
            details,
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
        match error {
            Failed::Engine(refusal) => Failure::of_parts(
                refusal.name(),
                Some(Value::from(refusal.code().as_str())),
                refusal.message().to_owned(),
                members(refusal),
            ),
            Failed::Behavior(defect) => {
                let mut details = Map::new();
                details.insert("behavior".into(), Value::from(defect.behavior.as_str()));
                Failure::of_parts(
                    "BehaviorError",
                    None,
                    defect.to_string(),
                    Some(Value::Object(details)),
                )
            }
            Failed::Usage(message) => Failure::usage(message.as_str()),
            Failed::Config(message) => Failure::new("BehaviorConfigError", None, message.as_str()),
            Failed::Storage(failure) => match failure.sqlite_code() {
                Some(code) => Failure::of_parts(
                    "SqliteError",
                    Some(Value::from(code)),
                    failure.to_string(),
                    None,
                ),
                None => Failure::new("Error", None, failure.to_string()),
            },
            other => Failure::new("Error", None, other.to_string()),
        }
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
        error.set_named_property("name", thrown.name.as_str())?;
        match &thrown.code {
            Some(code) => error.set_named_property("code", parse(env, code)?)?,
            None => {
                error.delete_named_property("code")?;
            }
        }
        if let Some(details) = &thrown.details {
            error.set_named_property("details", parse(env, details)?)?;
        }
        Ok(napi::Error::from(error.into_unknown(env)?))
    }
}

// members are a refusal's members beyond its code and message, by the
// TypeScript class's own names: a veto's behavior, action, reason,
// vetoCode and vetoDetails, an unknown tool's name, an expired cursor's
// after, floor and head, the issues, changes and fields of the others.
fn members(refusal: &EngineError) -> Option<Value> {
    match refusal.kind() {
        EngineErrorKind::BehaviorVeto {
            behavior,
            action,
            reason,
            veto_code,
            veto_details,
        } => {
            let mut details = Map::new();
            details.insert("behavior".into(), Value::from(behavior.as_str()));
            details.insert("action".into(), Value::from(action.as_str()));
            details.insert("reason".into(), Value::from(reason.as_str()));
            if let Some(code) = veto_code {
                details.insert("vetoCode".into(), Value::from(code.as_str()));
            }
            if let Some(inner) = veto_details {
                details.insert("vetoDetails".into(), Value::Object(inner.clone()));
            }
            Some(Value::Object(details))
        }
        EngineErrorKind::UnknownTool { tool } => {
            let mut details = Map::new();
            details.insert("tool".into(), Value::from(tool.as_str()));
            Some(Value::Object(details))
        }
        _ => refusal.details(),
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

// run makes one call: the request read, the clock set, the call made and
// its result written as JavaScript writes it, a panic caught.
fn run(engine: &Engine, request: &str) -> std::result::Result<String, Failure> {
    match catch_unwind(AssertUnwindSafe(|| call(engine, request))) {
        Ok(outcome) => outcome,
        Err(payload) => Err(Failure::panic(payload.as_ref())),
    }
}

fn call(engine: &Engine, request: &str) -> std::result::Result<String, Failure> {
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
        Ok(run(&self.engine, &self.request))
    }

    fn resolve(&mut self, env: Env, output: Self::Output) -> Result<String> {
        output.map_err(|failure| failure.into_napi(&env))
    }
}

#[napi]
impl NativeEngine {
    /// Opens an engine on its options' JSON: its storage created if absent
    /// and migrated, the core's behaviors registered.
    #[napi(factory)]
    pub fn open(env: Env, options: String) -> Result<NativeEngine> {
        let opened = catch_unwind(AssertUnwindSafe(|| -> std::result::Result<_, Failure> {
            let given: Value = serde_json::from_str(&options)
                .map_err(|error| Failure::usage(format!("engine options are JSON: {error}")))?;
            let mut options =
                EngineOptions::from_json(&given).map_err(|error| Failure::of(&error))?;
            options.behaviors = superschematic_engine_behaviors::core();
            Engine::open_shared(options).map_err(|error| Failure::of(&error))
        }));
        let engine = match opened {
            Ok(Ok(engine)) => engine,
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
        let engine = self.engine().map_err(|failure| failure.into_napi(&env))?;
        if self.dialect == Dialect::Postgres {
            return Err(Failure::new(
                "Error",
                Some("sync_on_async_storage"),
                "an engine on Postgres is called through call(), which returns a promise: a synchronous call would hold the JavaScript thread while it waits on the database",
            )
            .into_napi(&env));
        }
        run(&engine, &request).map_err(|failure| failure.into_napi(&env))
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
        if self.dialect != Dialect::Postgres {
            return Ok(Either::A(AsyncTask::new(CallTask { engine, request })));
        }
        let promise = env.spawn_future_with_callback(
            async move {
                napi::bindgen_prelude::spawn_blocking(move || run(&engine, &request))
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
