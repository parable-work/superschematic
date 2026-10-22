//! The engine's test kit (D58): the replayer of the scenario corpus that
//! milestone 1 recorded from the TypeScript engine
//! (`runtime/engine/testdata/scenarios`, whose README is the format), which
//! holds the Rust engine to the TypeScript engine's answers on SQLite and
//! Postgres.
//!
//! A file is the recording of one test file of the TypeScript suite; each
//! scenario one test: the calls it made on the engines it opened and what
//! each returned or threw. The replayer resolves the file's shared values,
//! skips a scenario only the TypeScript engine can run (`js`), one that
//! needs a test behavior the kit has not ported ([`behaviors`]), one that
//! writes the file with SQL of its own on Postgres (`dialect: sqlite`), and
//! one that calls a method that changes the engine and JSON cannot carry
//! ([`UNBUILT_WRITES`]). It opens each
//! engine as the recording says, with a policy answering from the recorded
//! answer table, seeded or fixed ids, a wall (strict) or fixed clock, the
//! runner, retention and tool options, the core's behaviors
//! ([`superschematic_engine_behaviors`]) and the behaviors it names: the
//! work queue's ([`superschematic_engine_workqueue`]), or a test's where
//! the scenario lists it among its test behaviors (`testBehaviors`, as the
//! Budget stand-in of Queue's tests), then makes each step's call through
//! [`superschematic_engine::calls`] (`behaviors.register` through the
//! kit's behaviors, the core's and the work queue's) and
//! compares its result or its error (class, code, message and details),
//! binding the symbols (`{{id:N}}`, `{{time:N}}`, `{{dir:N}}`,
//! `{{opaque:N}}`) at their first appearance in an answer. On Postgres an
//! event cursor is opaque too: the recording's are SQLite's, and a write a
//! rollback undid leaves a gap in a Postgres identity column (D58), so each
//! recorded cursor binds to the one read in its place, in the recorded
//! order, and a cursor argument passes the one bound to it.
//!
//! The HTTP corpus (`runtime/engine/testdata/http`) replays through the
//! same runner: its cases are scenarios whose `app`, exchange and stream
//! steps go through the engine's routes ([`HTTP_FILES`]), called directly
//! ([`Direct`]) or through a server runtime's adapter ([`Transport`]; the
//! `axum` feature's [`AxumRouter`]), which [`replay_file_through`] takes.

#[cfg(feature = "axum")]
mod axum;
pub mod behaviors;
pub mod commerce_fixtures;
pub mod core_fixtures;
mod http;
pub mod workqueue_fixtures;

#[cfg(feature = "axum")]
pub use self::axum::AxumRouter;
pub use http::{Answer, Answered, Direct, Frames, MountView, Transport, HTTP_FILES};

use std::collections::{BTreeMap, HashMap};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{Map, Value};

use superschematic_engine::json::js_equal;
use superschematic_engine::storage::{Dialect, Lock, Row, SqlValue};
use superschematic_engine::{
    calls, AccessPolicy, AccessRequest, Clock, Engine, EngineError, EngineOptions, Error,
    IdCounter, IdQueue, Ids, MetaSchema, NamespaceOptions, Policy, Principal, Storage,
    ValueOptions, SCENARIO_SEED,
};

/// Calls that change the engine and are not built: a scenario that makes
/// one is skipped, since the steps after it depend on it. A transaction of
/// the file's own and a watch take functions.
pub const UNBUILT_WRITES: [&str; 2] = ["storage.transaction", "events.watch"];

/// A scenario file, its shared values resolved.
#[derive(Clone, Debug)]
pub struct ScenarioFile {
    pub name: String,
    pub scenarios: Vec<Value>,
}

/// Reads a scenario file and puts each shared value in its places.
pub fn load(path: &Path) -> Result<ScenarioFile, String> {
    let text =
        std::fs::read_to_string(path).map_err(|error| format!("{}: {error}", path.display()))?;
    let mut file: Value =
        serde_json::from_str(&text).map_err(|error| format!("{}: {error}", path.display()))?;
    let shared = file
        .get("shared")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    // A scenario file holds scenarios; an HTTP file holds cases, a case's
    // members a scenario's.
    let mut scenarios = file
        .get_mut("scenarios")
        .and_then(Value::as_array_mut)
        .map(std::mem::take)
        .or_else(|| {
            file.get_mut("cases")
                .and_then(Value::as_array_mut)
                .map(std::mem::take)
        })
        .unwrap_or_default();
    for scenario in &mut scenarios {
        resolve_shared(scenario, &shared)?;
    }
    Ok(ScenarioFile {
        name: file
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        scenarios,
    })
}

fn resolve_shared(value: &mut Value, shared: &Map<String, Value>) -> Result<(), String> {
    match value {
        Value::Object(members) => {
            if members.len() == 1 {
                if let Some(Value::String(key)) = members.get("$shared") {
                    *value = shared
                        .get(key)
                        .cloned()
                        .ok_or_else(|| format!("no shared value {key}"))?;
                    return Ok(());
                }
            }
            for member in members.values_mut() {
                resolve_shared(member, shared)?;
            }
        }
        Value::Array(items) => {
            for item in items {
                resolve_shared(item, shared)?;
            }
        }
        _ => {}
    }
    Ok(())
}

/// The database a replay runs on.
#[derive(Clone, Debug)]
pub enum Database {
    Sqlite,
    Postgres { url: String },
}

impl Database {
    pub fn dialect(&self) -> Dialect {
        match self {
            Database::Sqlite => Dialect::Sqlite,
            Database::Postgres { .. } => Dialect::Postgres,
        }
    }
}

/// What replaying one file did.
#[derive(Clone, Debug, Default)]
pub struct FileReport {
    pub file: String,
    pub scenarios: usize,
    /// Scenarios replayed, whole or with read-only steps skipped.
    pub replayed: usize,
    /// Scenarios skipped, with why.
    pub skipped: Vec<(String, String)>,
    pub steps_passed: usize,
    /// Steps not made: a skipped scenario's, and read-only calls not built.
    pub steps_skipped: usize,
    /// Each step whose answer differed, with the scenario, the step and how.
    pub failures: Vec<String>,
}

impl FileReport {
    /// One line: the file, its scenarios replayed and skipped, its steps.
    pub fn summary(&self) -> String {
        format!(
            "{}: {} of {} scenarios replayed ({} skipped), {} steps passed, {} skipped, {} failed",
            self.file,
            self.replayed,
            self.scenarios,
            self.skipped.len(),
            self.steps_passed,
            self.steps_skipped,
            self.failures.len()
        )
    }
}

/// Replays every scenario of a file on a database. `repository` is the
/// repository's root, which holds the open meta-schema's source. An HTTP
/// case's requests reach the engine's routes directly ([`Direct`]).
pub fn replay_file(file: &ScenarioFile, database: &Database, repository: &Path) -> FileReport {
    replay_file_through(file, database, repository, &Direct)
}

/// Replays every scenario of a file on a database, an HTTP case's requests
/// reaching the engine through `transport`, once the replayer's host steps
/// let them through.
pub fn replay_file_through(
    file: &ScenarioFile,
    database: &Database,
    repository: &Path,
    transport: &dyn Transport,
) -> FileReport {
    let mut report = FileReport {
        file: file.name.clone(),
        scenarios: file.scenarios.len(),
        ..FileReport::default()
    };
    for scenario in &file.scenarios {
        let name = scenario
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or("?")
            .to_owned();
        let steps = scenario
            .get("steps")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        if let Some(reason) = skip_reason(&file.name, scenario, &steps, database) {
            report.steps_skipped += steps.len();
            report.skipped.push((name, reason));
            continue;
        }
        report.replayed += 1;
        let mut run = Run::new(database, repository, &file.name, scenario, transport);
        for (index, step) in steps.iter().enumerate() {
            run.http.tail = steps[index + 1..]
                .iter()
                .all(|later| later.get("stream").is_some() && later.get("end").is_some());
            match run.step(step) {
                Ok(Outcome::Passed) => report.steps_passed += 1,
                Ok(Outcome::Skipped) => report.steps_skipped += 1,
                // A step that differs is noted and the replay goes on, so
                // one wrong message does not hide the steps after it.
                Err(message) => {
                    let call = step.get("call").and_then(Value::as_str).unwrap_or("?");
                    report
                        .failures
                        .push(format!("{name} > step {} ({call}): {message}", index + 1));
                }
            }
        }
        run.finish();
    }
    report
}

// skip_reason says why a scenario is not replayed, or None.
fn skip_reason(
    file: &str,
    scenario: &Value,
    steps: &[Value],
    database: &Database,
) -> Option<String> {
    let names = |key: &str| -> Vec<String> {
        scenario
            .get(key)
            .and_then(Value::as_array)
            .map(|list| {
                list.iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            })
            .unwrap_or_default()
    };
    let js = names("js");
    if !js.is_empty() {
        return Some(format!("js: {}", js.join(", ")));
    }
    let missing: Vec<String> = names("testBehaviors")
        .into_iter()
        .filter(|name| {
            behaviors::stand_in(name).is_none() && behaviors::test_behavior(file, name).is_none()
        })
        .collect();
    if !missing.is_empty() {
        return Some(format!("test behaviors not ported: {}", missing.join(", ")));
    }
    if scenario.get("dialect").and_then(Value::as_str) == Some("sqlite")
        && database.dialect() == Dialect::Postgres
    {
        return Some("dialect: sqlite".to_owned());
    }
    for step in steps {
        let call = step.get("call").and_then(Value::as_str).unwrap_or_default();
        if UNBUILT_WRITES.contains(&call) {
            return Some(format!("needs {call}"));
        }
    }
    None
}

enum Outcome {
    Passed,
    Skipped,
}

// A place an engine opens on: a SQLite file in a directory of its own, or
// a schema of the Postgres database.
enum Place {
    Sqlite { dir: PathBuf },
    Postgres { schema: String },
}

static NEXT_PLACE: AtomicU64 = AtomicU64::new(0);

fn unique(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| since.as_nanos());
    format!(
        "{prefix}_{nanos}_{}_{}",
        std::process::id(),
        NEXT_PLACE.fetch_add(1, Ordering::Relaxed)
    )
}

// An engine of a scenario, with how its clock and ids are driven. It is
// shared, as a host keeps it, so an event stream and the runner's driver
// can hold it.
struct Opened {
    engine: Option<Arc<Engine>>,
    fixed_clock: bool,
    ids: Option<IdQueue>,
}

// is_cursor is whether a member of an answer holds an event cursor: an
// event's, a subscription's, its failure's or its last skip's `cursor`; a
// page of events' `next`; the runner status's `head`; a cause's `event`;
// retention's `floor` and `heldAt`.
fn is_cursor(object: &Map<String, Value>, key: &str) -> bool {
    match key {
        "cursor" => true,
        "next" => object.contains_key("events"),
        "head" => object.contains_key("running"),
        "event" => object.contains_key("depth"),
        "floor" | "heldAt" => object.contains_key("pruned"),
        _ => false,
    }
}

/// A symbol's bound value: text, or an instant in epoch milliseconds.
#[derive(Clone, Debug, PartialEq)]
enum Bound {
    Text(String),
    Time(i64),
}

// One scenario's replay: its engines, places, symbols and principals.
struct Run<'d> {
    database: &'d Database,
    repository: &'d Path,
    file: &'d str,
    /// The test behaviors the scenario lists (`testBehaviors`).
    test_behaviors: Vec<String>,
    principals: Map<String, Value>,
    engines: Vec<(String, Opened)>,
    places: Vec<(String, Place)>,
    bindings: HashMap<String, Bound>,
    /// On Postgres, each recorded event cursor and the one this replay
    /// read in its place ([`Run::match_cursor`]).
    cursors: BTreeMap<i64, i64>,
    counter: IdCounter,
    unknown_questions: Arc<Mutex<Vec<String>>>,
    /// The apps a case serves, and its open streams (`http`).
    http: http::Served,
    /// How an HTTP case's requests reach the engine.
    transport: &'d dyn Transport,
}

impl<'d> Run<'d> {
    fn new(
        database: &'d Database,
        repository: &'d Path,
        file: &'d str,
        scenario: &Value,
        transport: &'d dyn Transport,
    ) -> Self {
        Run {
            database,
            repository,
            file,
            test_behaviors: scenario
                .get("testBehaviors")
                .and_then(Value::as_array)
                .map(|names| {
                    names
                        .iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                })
                .unwrap_or_default(),
            principals: scenario
                .get("principals")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default(),
            engines: Vec::new(),
            places: Vec::new(),
            bindings: HashMap::new(),
            cursors: BTreeMap::new(),
            counter: IdCounter::new(),
            unknown_questions: Arc::new(Mutex::new(Vec::new())),
            http: http::Served::default(),
            transport,
        }
    }

    // behavior_set is a behavior a recording names: the stand-in a test
    // registers under a work-queue behavior's name, which the scenario
    // lists among its test behaviors; else the file's test behavior, the
    // core's or the work queue's.
    fn behavior_set(&self, name: &str) -> Option<superschematic_engine::BehaviorSet> {
        self.test_behaviors
            .iter()
            .any(|known| known == name)
            .then(|| behaviors::stand_in(name))
            .flatten()
            .or_else(|| behaviors::test_behavior(self.file, name))
            .or_else(|| behaviors::core_behavior(name))
    }

    fn step(&mut self, step: &Value) -> Result<Outcome, String> {
        // A step only the TypeScript engine can make, or one during which a
        // test behavior read a hook the test had set (the README's `js`).
        if step.get("js").is_some_and(|js| js == &Value::Bool(true)) {
            return Ok(Outcome::Skipped);
        }
        if step.get("app").is_some() && step.get("request").is_none() {
            return http::app(self, step);
        }
        if step.get("request").is_some() {
            return http::exchange(self, step);
        }
        if step.get("stream").is_some() {
            return http::stream(self, step);
        }
        let call = step.get("call").and_then(Value::as_str).unwrap_or_default();
        let mut args = match step.get("args") {
            Some(Value::Array(args)) => args
                .iter()
                .map(|arg| self.substitute(arg))
                .collect::<Result<Vec<_>, _>>()?,
            _ => Vec::new(),
        };
        if call.starts_with("events.") && self.database.dialect() == Dialect::Postgres {
            for arg in &mut args {
                if let Some(after) = arg.get("after").and_then(Value::as_i64) {
                    arg["after"] = Value::from(self.cursor_argument(after));
                }
            }
        }
        let compare = !(step.get("dialect").and_then(Value::as_str) == Some("sqlite")
            && self.database.dialect() == Dialect::Postgres);
        let result = match call {
            "open" => self.open(step, &args).map(|()| None),
            "close" => {
                let opened = self.engine_of(step)?;
                if let Some(engine) = opened.engine.take() {
                    engine.shutdown();
                }
                Ok(None)
            }
            "behaviors.register" => {
                let name = args
                    .first()
                    .and_then(|given| given.get("behavior"))
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned();
                let set = self
                    .behavior_set(&name)
                    .ok_or_else(|| format!("the test kit has no behavior {name}"))?;
                let engine = self
                    .engine_of(step)?
                    .engine
                    .as_ref()
                    .ok_or("the engine is closed")?;
                engine
                    .behaviors()
                    .register_set(set)
                    .map(|()| None)
                    .map_err(|error| error_json(&error))
            }
            "storage.get" | "storage.all" | "storage.run" | "storage.exec" => {
                if self.database.dialect() == Dialect::Postgres {
                    return Ok(Outcome::Skipped);
                }
                let engine = self
                    .engine_of(step)?
                    .engine
                    .as_ref()
                    .ok_or("the engine is closed")?;
                storage_call(engine, call, &args)
            }
            _ => {
                let principal: Option<Result<Principal, EngineError>> = match step.get("principal")
                {
                    None => None,
                    Some(Value::String(name)) => {
                        let given = self
                            .principals
                            .get(name)
                            .ok_or_else(|| format!("no principal {name}"))?;
                        Some(calls::principal_from_json(given))
                    }
                    Some(other) => {
                        return Err(format!("a principal the replayer cannot pass: {other}"))
                    }
                };
                let drawn: Vec<String> = match step.get("ids") {
                    Some(Value::Array(drawn)) => drawn
                        .iter()
                        .map(|id| {
                            self.substitute(id)
                                .map(|id| id.as_str().unwrap_or_default().to_owned())
                        })
                        .collect::<Result<_, _>>()?,
                    _ => Vec::new(),
                };
                let opened = self.engine_of(step)?;
                let fixed_clock = opened.fixed_clock;
                let ids = opened.ids.clone();
                let engine = opened.engine.as_ref().ok_or("the engine is closed")?;
                if fixed_clock {
                    if let Some(now) = step.get("now").and_then(Value::as_i64) {
                        engine.clock().set(now).map_err(|error| error.to_string())?;
                    }
                    if let Some(Value::Array(times)) = step.get("clock") {
                        engine
                            .clock()
                            .script(times.iter().filter_map(Value::as_i64))
                            .map_err(|error| error.to_string())?;
                    }
                }
                if let Some(queue) = &ids {
                    queue.clear();
                    for id in drawn {
                        queue.push(id);
                    }
                }
                match principal {
                    Some(Err(refused)) => Err(error_json(&Error::Engine(refused))),
                    Some(Ok(principal)) => calls::call(engine, call, Some(&principal), &args)
                        .map_err(|error| error_json(&error)),
                    None => {
                        calls::call(engine, call, None, &args).map_err(|error| error_json(&error))
                    }
                }
            }
        };
        let asked: Vec<String> = std::mem::take(
            &mut *self
                .unknown_questions
                .lock()
                .map_err(|_| "a poisoned lock")?,
        );
        if !asked.is_empty() {
            return Err(format!(
                "the engine asked the policy questions the recording never asked: {}",
                asked.join("; ")
            ));
        }
        if !compare {
            return Ok(Outcome::Passed);
        }
        match (step.get("expect"), step.get("error"), result) {
            (Some(expected), _, Ok(Some(actual))) => self
                .matches(expected, &actual, "")
                .map(|()| Outcome::Passed),
            (Some(expected), _, Ok(None)) => Err(format!("expected {expected}, got nothing")),
            (Some(expected), _, Err(error)) => {
                Err(format!("expected {expected}, got the error {error}"))
            }
            (None, Some(expected), Err(error)) => {
                self.matches(expected, &error, "").map(|()| Outcome::Passed)
            }
            (None, Some(expected), Ok(actual)) => Err(format!(
                "expected the error {expected}, got {}",
                actual.unwrap_or(Value::Null)
            )),
            (None, None, Ok(None)) => Ok(Outcome::Passed),
            (None, None, Ok(Some(actual))) => Err(format!("expected nothing, got {actual}")),
            (None, None, Err(error)) => Err(format!("expected nothing, got the error {error}")),
        }
    }

    fn engine_of(&mut self, step: &Value) -> Result<&mut Opened, String> {
        let name = step.get("engine").and_then(Value::as_str);
        let found = match name {
            Some(name) => self.engines.iter_mut().find(|(each, _)| each == name),
            None => self.engines.last_mut(),
        };
        found
            .map(|(_, opened)| opened)
            .ok_or_else(|| "no engine is open".to_owned())
    }

    fn open(&mut self, step: &Value, args: &[Value]) -> Result<(), Value> {
        let given = args
            .first()
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        let name = step
            .get("as")
            .and_then(Value::as_str)
            .unwrap_or("e1")
            .to_owned();
        let path = given
            .get("path")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let storage = self
            .storage(&path, given.get("busyTimeoutMs").and_then(Value::as_u64))
            .map_err(|message| error_json(&Error::Usage(message)))?;
        let mut options = EngineOptions::new(storage);
        options.policy = match given.get("policy") {
            None => None,
            Some(Value::String(shorthand)) => Some(
                Policy::from_json(&Value::from(shorthand.as_str()))
                    .map_err(|error| error_json(&error))?,
            ),
            Some(Value::Object(table)) => Some(Policy::custom(AnswerPolicy {
                answers: table
                    .get("answers")
                    .and_then(Value::as_array)
                    .map(|answers| {
                        answers
                            .iter()
                            .filter_map(|pair| Some((pair.get(0)?.clone(), pair.get(1)?.clone())))
                            .collect()
                    })
                    .unwrap_or_default(),
                principals: self.principals.clone(),
                unknown: Arc::clone(&self.unknown_questions),
            })),
            Some(other) => {
                return Err(error_json(&Error::Usage(format!(
                    "a policy the replayer cannot pass: {other}"
                ))))
            }
        };
        // What a binding passes as it is, read as the engine reads a
        // binding's options (EngineOptions::from_json): the namespaces and,
        // below, the runner, retention, tools and value store.
        if let Some(namespaces) = given.get("namespaces") {
            options.namespaces =
                NamespaceOptions::from_json(namespaces).map_err(|error| error_json(&error))?;
        }
        let mut queue = None;
        options.ids = match given.get("ids").and_then(Value::as_str) {
            Some("fixed") => {
                let ids = IdQueue::new();
                queue = Some(ids.clone());
                Ids::Queue(ids)
            }
            _ => Ids::Seeded {
                prefix: SCENARIO_SEED.to_owned(),
                drawn: self.counter.clone(),
            },
        };
        let fixed_clock = given.get("clock").and_then(Value::as_str) == Some("fixed");
        options.clock = if fixed_clock {
            Clock::Manual(step.get("now").and_then(Value::as_i64).unwrap_or(0))
        } else {
            Clock::Strict
        };
        options.meta_schema = match given.get("metaSchema") {
            None => None,
            Some(Value::String(open)) if open == "open" => Some(MetaSchema::Document(
                open_meta_schema(self.repository)
                    .map_err(|message| error_json(&Error::Usage(message)))?,
            )),
            Some(document) => Some(MetaSchema::Document(document.clone())),
        };
        // The core's behaviors first, as the TypeScript engine registers
        // them, then the ones the recording names.
        options.behaviors = superschematic_engine_behaviors::core();
        if let Some(Value::Array(names)) = given.get("behaviors") {
            for name in names.iter().filter_map(Value::as_str) {
                let set = self.behavior_set(name).ok_or_else(|| {
                    error_json(&Error::Usage(format!(
                        "the test kit has no behavior {name}"
                    )))
                })?;
                options.behaviors = std::mem::take(&mut options.behaviors).extend(set);
            }
        }
        // In the order the TypeScript engine checks them.
        if let Some(tools) = given.get("tools") {
            options.tools = superschematic_engine::ToolOptions::from_json(tools)
                .map_err(|error| error_json(&error))?;
        }
        if let Some(runner) = given.get("runner") {
            options.runner = Some(
                superschematic_engine::RunnerOptions::from_json(runner)
                    .map_err(|error| error_json(&error))?,
            );
        }
        if let Some(values) = given.get("values") {
            options.values = ValueOptions::from_json(values).map_err(|error| error_json(&error))?;
        }
        if let Some(retention) = given.get("retention") {
            options.retention = Some(
                superschematic_engine::RetentionOptions::from_json(retention)
                    .map_err(|error| error_json(&error))?,
            );
        }
        let engine = Engine::open_shared(options).map_err(|error| error_json(&error))?;
        self.engines.push((
            name,
            Opened {
                engine: Some(engine),
                fixed_clock,
                ids: queue,
            },
        ));
        Ok(())
    }

    // storage is where a path opens: the place of its directory, made at
    // the first open of the path.
    fn storage(&mut self, path: &str, busy_timeout: Option<u64>) -> Result<Storage, String> {
        if !self.places.iter().any(|(known, _)| known == path) {
            let place = match self.database {
                Database::Sqlite => {
                    let dir = std::env::temp_dir().join(unique("replay"));
                    std::fs::create_dir_all(&dir).map_err(|error| error.to_string())?;
                    Place::Sqlite { dir }
                }
                Database::Postgres { .. } => Place::Postgres {
                    schema: unique("replay"),
                },
            };
            self.places.push((path.to_owned(), place));
        }
        let place = &self
            .places
            .iter()
            .find(|(known, _)| known == path)
            .ok_or("no place")?
            .1;
        let timeout = std::time::Duration::from_millis(busy_timeout.unwrap_or(5000));
        match (place, self.database) {
            #[cfg(feature = "sqlite")]
            (Place::Sqlite { dir }, _) => Ok(Storage::Sqlite {
                path: dir.join("engine.db"),
                options: superschematic_engine::storage::SqliteOptions {
                    busy_timeout: timeout,
                },
            }),
            #[cfg(feature = "postgres")]
            (Place::Postgres { schema }, Database::Postgres { url }) => Ok(Storage::Postgres {
                url: url.clone(),
                options: superschematic_engine::storage::PostgresOptions {
                    schema: schema.clone(),
                    busy_timeout: timeout,
                    listen: false,
                    ..Default::default()
                },
            }),
            _ => {
                let _ = timeout;
                Err("this build does not open the database the replay names".to_owned())
            }
        }
    }

    // finish closes the engines and removes their places.
    fn finish(mut self) {
        self.http = http::Served::default();
        for (_, opened) in self.engines.drain(..) {
            if let Some(engine) = opened.engine {
                engine.shutdown();
            }
        }
        for (_, place) in self.places.drain(..) {
            match place {
                Place::Sqlite { dir } => {
                    let _ = std::fs::remove_dir_all(dir);
                }
                Place::Postgres { schema } => {
                    #[cfg(feature = "postgres")]
                    if let Database::Postgres { url } = self.database {
                        let db = superschematic_engine::storage::Db::open_postgres(
                            url,
                            superschematic_engine::storage::PostgresOptions {
                                schema: schema.clone(),
                                listen: false,
                                ..Default::default()
                            },
                        );
                        if let Ok(db) = db {
                            let _ = db.write(Lock::Catalog, |tx| {
                                tx.exec(format!("DROP SCHEMA {schema} CASCADE"))
                            });
                        }
                    }
                    let _ = schema;
                }
            }
        }
    }

    // substitute writes a step's argument with its symbols' bound values;
    // a directory the step names first is made.
    fn substitute(&mut self, value: &Value) -> Result<Value, String> {
        match value {
            Value::String(text) => {
                let segments = segments(text);
                if segments.len() == 1 {
                    if let Segment::Symbol(symbol) = &segments[0] {
                        return self.bound_argument(symbol, true);
                    }
                }
                if !segments
                    .iter()
                    .any(|segment| matches!(segment, Segment::Symbol(_)))
                {
                    return Ok(value.clone());
                }
                let mut out = String::new();
                for segment in &segments {
                    match segment {
                        Segment::Text(text) => out.push_str(text),
                        Segment::Symbol(symbol) => match self.bound_argument(symbol, false)? {
                            Value::String(text) => out.push_str(&text),
                            other => out.push_str(&other.to_string()),
                        },
                    }
                }
                Ok(Value::String(out))
            }
            Value::Array(items) => items
                .iter()
                .map(|item| self.substitute(item))
                .collect::<Result<Vec<_>, _>>()
                .map(Value::Array),
            Value::Object(members) => {
                let mut out = Map::new();
                for (key, member) in members {
                    out.insert(key.clone(), self.substitute(member)?);
                }
                Ok(Value::Object(out))
            }
            other => Ok(other.clone()),
        }
    }

    fn bound_argument(&mut self, symbol: &Symbol, whole: bool) -> Result<Value, String> {
        if symbol.kind == "dir" && !self.bindings.contains_key(&symbol.key()) {
            let dir = format!("/replay/{}", unique("dir"));
            self.bindings.insert(symbol.key(), Bound::Text(dir));
        }
        let bound = self.bindings.get(&symbol.key()).ok_or_else(|| {
            format!(
                "an argument names {{{{{}}}}} before an answer bound it",
                symbol.text
            )
        })?;
        Ok(render(symbol, bound, whole))
    }

    // matches compares an answer with the recorded one, binding each symbol
    // at its first appearance.
    fn matches(&mut self, expected: &Value, actual: &Value, at: &str) -> Result<(), String> {
        match (expected, actual) {
            (Value::String(text), _) if text.contains("{{") => {
                let segments = segments(text);
                if let [Segment::Symbol(symbol)] = segments.as_slice() {
                    return self.match_whole(symbol, actual, at);
                }
                let Value::String(actual_text) = actual else {
                    return Err(format!("{at}: expected {expected}, got {actual}"));
                };
                let mut found = Vec::new();
                if !self.match_segments(&segments, actual_text, &mut found) {
                    return Err(format!("{at}: expected {expected}, got {actual}"));
                }
                for (symbol, bound) in found {
                    self.bind(&symbol, bound, at)?;
                }
                Ok(())
            }
            (Value::Object(want), Value::Object(got)) => {
                for key in want.keys() {
                    if !got.contains_key(key) {
                        return Err(format!("{at}/{key}: expected {}, got nothing", want[key]));
                    }
                }
                for key in got.keys() {
                    if !want.contains_key(key) {
                        return Err(format!("{at}/{key}: expected nothing, got {}", got[key]));
                    }
                }
                for (key, member) in want {
                    let at = format!("{at}/{key}");
                    match (member.as_i64(), got[key].as_i64()) {
                        (Some(recorded), Some(read))
                            if self.database.dialect() == Dialect::Postgres
                                && is_cursor(want, key) =>
                        {
                            self.match_cursor(recorded, read, &at)?;
                        }
                        _ => self.matches(member, &got[key], &at)?,
                    }
                }
                Ok(())
            }
            (Value::Array(want), Value::Array(got)) => {
                if want.len() != got.len() {
                    return Err(format!(
                        "{at}: expected {} items, got {}: {actual}",
                        want.len(),
                        got.len()
                    ));
                }
                for (index, (item, other)) in want.iter().zip(got).enumerate() {
                    self.matches(item, other, &format!("{at}/{index}"))?;
                }
                Ok(())
            }
            _ if js_equal(expected, actual) => Ok(()),
            _ => Err(format!("{at}: expected {expected}, got {actual}")),
        }
    }

    // match_cursor holds an event cursor read on Postgres to the recorded
    // one. The recording's cursors are SQLite's, where a write a rollback
    // undid gives its cursor back to the next event; a Postgres identity
    // column, which no rollback takes back, leaves a gap there (D58), so
    // every later cursor reads higher. A cursor is opaque, as the README's
    // symbols are (`## Symbols`): each recorded cursor binds, at its first
    // appearance, to the one read in its place, and needs only to keep the
    // recorded order; at a later appearance it must read the same.
    fn match_cursor(&mut self, recorded: i64, read: i64, at: &str) -> Result<(), String> {
        if let Some(&bound) = self.cursors.get(&recorded) {
            return if bound == read {
                Ok(())
            } else {
                Err(format!(
                    "{at}: expected cursor {recorded}, bound to {bound}, got {read}"
                ))
            };
        }
        let below = self.cursors.range(..recorded).next_back();
        let above = self.cursors.range(recorded + 1..).next();
        if below.is_some_and(|(_, &bound)| bound >= read)
            || above.is_some_and(|(_, &bound)| bound <= read)
        {
            return Err(format!(
                "{at}: expected cursor {recorded}, got {read}, out of the recorded order (cursors bound so far: {:?})",
                self.cursors
            ));
        }
        self.cursors.insert(recorded, read);
        Ok(())
    }

    // cursor_argument is the cursor this replay passes on Postgres where
    // the recording passed `recorded`: the one bound to it, else as far
    // past the nearest one bound below it as the recording's.
    fn cursor_argument(&self, recorded: i64) -> i64 {
        match self.cursors.range(..=recorded).next_back() {
            Some((&below, &bound)) => bound + (recorded - below),
            None => recorded,
        }
    }

    fn match_whole(&mut self, symbol: &Symbol, actual: &Value, at: &str) -> Result<(), String> {
        let bound = match (symbol.kind.as_str(), actual) {
            ("time", Value::Number(number)) => {
                let read = superschematic_engine::json::number_value(number) as i64;
                Bound::Time(if symbol.micros { read / 1000 } else { read })
            }
            ("time", Value::String(text)) => {
                Bound::Time(ms_of_iso(text).ok_or_else(|| format!("{at}: {text} is no date-time"))?)
            }
            (_, Value::String(text)) => Bound::Text(text.clone()),
            _ => {
                return Err(format!(
                    "{at}: expected {{{{{}}}}}, got {actual}",
                    symbol.text
                ))
            }
        };
        match self.bindings.get(&symbol.key()) {
            // An instant bound from a number stands in a string as a
            // date-time: Branches' times are RFC 3339 (D32), the
            // instances' epoch milliseconds.
            Some(known @ Bound::Time(_)) if actual.is_string() => {
                if *known == bound {
                    Ok(())
                } else {
                    Err(format!(
                        "{at}: expected {{{{{}}}}}, bound to {}, got {actual}",
                        symbol.text,
                        render(symbol, known, false)
                    ))
                }
            }
            Some(known) => {
                let rendered = render(symbol, known, true);
                if js_equal(&rendered, actual) {
                    Ok(())
                } else {
                    Err(format!(
                        "{at}: expected {{{{{}}}}}, bound to {rendered}, got {actual}",
                        symbol.text
                    ))
                }
            }
            None => self.bind(symbol, bound, at),
        }
    }

    fn bind(&mut self, symbol: &Symbol, bound: Bound, at: &str) -> Result<(), String> {
        if let Some(known) = self.bindings.get(&symbol.key()) {
            return if *known == bound {
                Ok(())
            } else {
                Err(format!(
                    "{at}: {{{{{}}}}} is bound to {known:?}, got {bound:?}",
                    symbol.text
                ))
            };
        }
        let prefix = format!("{}:", symbol.kind);
        if let Some((other, _)) = self
            .bindings
            .iter()
            .find(|(key, value)| key.starts_with(&prefix) && **value == bound)
        {
            return Err(format!(
                "{at}: {{{{{}}}}} and {{{{{other}}}}} stand for one value, {bound:?}",
                symbol.text
            ));
        }
        self.bindings.insert(symbol.key(), bound);
        Ok(())
    }

    // match_segments matches a string of text and symbols against an
    // answer's text, collecting what each unbound symbol stands for.
    fn match_segments(
        &self,
        segments: &[Segment],
        actual: &str,
        found: &mut Vec<(Symbol, Bound)>,
    ) -> bool {
        let Some((first, rest)) = segments.split_first() else {
            return actual.is_empty();
        };
        match first {
            Segment::Text(text) => actual
                .strip_prefix(text.as_str())
                .is_some_and(|after| self.match_segments(rest, after, found)),
            Segment::Symbol(symbol) => {
                let known = self.bindings.get(&symbol.key()).cloned().or_else(|| {
                    found
                        .iter()
                        .find(|(each, _)| each.key() == symbol.key())
                        .map(|(_, bound)| bound.clone())
                });
                if let Some(known) = known {
                    let mut forms = vec![match render(symbol, &known, false) {
                        Value::String(text) => text,
                        other => other.to_string(),
                    }];
                    // An instant in text is a date-time, the version
                    // graph's canonical one too, or, in JSON a string
                    // carries, its epoch milliseconds.
                    if let (Bound::Time(ms), false) = (&known, symbol.micros) {
                        forms.push(canonical_iso_of(*ms));
                        forms.push(ms.to_string());
                    }
                    return forms.iter().any(|text| {
                        actual
                            .strip_prefix(text.as_str())
                            .is_some_and(|after| self.match_segments(rest, after, found))
                    });
                }
                // An unbound symbol takes the longest run its kind allows
                // that lets the rest match.
                let mut length = actual.len();
                loop {
                    if actual.is_char_boundary(length) {
                        let candidate = &actual[..length];
                        if let Some(bound) = symbol.reads(candidate) {
                            found.push((symbol.clone(), bound));
                            if self.match_segments(rest, &actual[length..], found) {
                                return true;
                            }
                            found.pop();
                        }
                    }
                    if length == 0 {
                        return false;
                    }
                    length -= 1;
                }
            }
        }
    }
}

/// A symbol of the corpus: its kind (`id`, `time`, `dir`, `opaque`), its
/// number, and its form (`:62`, `:us`).
#[derive(Clone, Debug, PartialEq)]
struct Symbol {
    text: String,
    kind: String,
    number: String,
    micros: bool,
    base62: bool,
}

impl Symbol {
    fn key(&self) -> String {
        format!("{}:{}", self.kind, self.number)
    }

    // reads is what a run of an answer's text stands for, when it can be
    // this symbol's kind.
    fn reads(&self, text: &str) -> Option<Bound> {
        match self.kind.as_str() {
            "id" if !self.base62 => (text.len() == 36
                && text.bytes().all(|b| b.is_ascii_hexdigit() || b == b'-'))
            .then(|| Bound::Text(text.to_owned())),
            "time" if self.micros => text
                .parse::<i64>()
                .ok()
                .map(|micros| Bound::Time(micros / 1000)),
            // A date-time, or 13 digits of epoch milliseconds, as the
            // recorder finds them in text.
            "time" if text.len() == 13 && text.bytes().all(|b| b.is_ascii_digit()) => {
                text.parse::<i64>().ok().map(Bound::Time)
            }
            "time" => ms_of_iso(text).map(Bound::Time),
            "opaque" => (!text.is_empty()
                && text
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_'))
            .then(|| Bound::Text(text.to_owned())),
            "dir" => (!text.is_empty()).then(|| Bound::Text(text.to_owned())),
            _ => None,
        }
    }
}

enum Segment {
    Text(String),
    Symbol(Symbol),
}

// segments splits a string into its text and its symbols.
fn segments(text: &str) -> Vec<Segment> {
    let mut out = Vec::new();
    let mut rest = text;
    while let Some(open) = rest.find("{{") {
        let Some(close) = rest[open..].find("}}") else {
            break;
        };
        let inner = &rest[open + 2..open + close];
        let parts: Vec<&str> = inner.split(':').collect();
        let symbol = match parts.as_slice() {
            [kind, number] | [kind, number, _]
                if ["id", "time", "dir", "opaque"].contains(kind)
                    && number.bytes().all(|b| b.is_ascii_digit()) =>
            {
                Some(Symbol {
                    text: inner.to_owned(),
                    kind: (*kind).to_owned(),
                    number: (*number).to_owned(),
                    micros: parts.get(2) == Some(&"us"),
                    base62: parts.get(2) == Some(&"62"),
                })
            }
            _ => None,
        };
        match symbol {
            Some(symbol) => {
                if open > 0 {
                    out.push(Segment::Text(rest[..open].to_owned()));
                }
                out.push(Segment::Symbol(symbol));
            }
            None => out.push(Segment::Text(rest[..open + close + 2].to_owned())),
        }
        rest = &rest[open + close + 2..];
    }
    if !rest.is_empty() {
        out.push(Segment::Text(rest.to_owned()));
    }
    out
}

// render is a symbol's bound value where it stands: a time as a number in
// place of a whole value, as an RFC 3339 date-time inside a string.
fn render(symbol: &Symbol, bound: &Bound, whole: bool) -> Value {
    match bound {
        Bound::Text(text) => Value::String(text.clone()),
        Bound::Time(ms) if symbol.micros => {
            let micros = ms * 1000;
            if whole {
                Value::from(micros)
            } else {
                Value::String(micros.to_string())
            }
        }
        Bound::Time(ms) if whole => Value::from(*ms),
        Bound::Time(ms) => Value::String(iso_of(*ms)),
    }
}

/// An instant in epoch milliseconds as JavaScript's `toISOString` writes
/// it: `YYYY-MM-DDTHH:MM:SS.mmmZ`.
pub fn iso_of(ms: i64) -> String {
    let days = ms.div_euclid(86_400_000);
    let of_day = ms.rem_euclid(86_400_000);
    // Howard Hinnant's civil_from_days.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3_600_000,
        of_day / 60_000 % 60,
        of_day / 1000 % 60,
        of_day % 1000
    )
}

/// An instant in epoch milliseconds as the version graph's canonical
/// date-time writes it (D32): [`iso_of`]'s, its fraction of a second
/// without trailing zeros and left out when zero.
pub fn canonical_iso_of(ms: i64) -> String {
    let iso = iso_of(ms);
    let (whole, fraction) = iso.split_at(19);
    let digits = fraction.trim_start_matches('.').trim_end_matches('Z');
    let digits = digits.trim_end_matches('0');
    if digits.is_empty() {
        format!("{whole}Z")
    } else {
        format!("{whole}.{digits}Z")
    }
}

/// The instant an RFC 3339 date-time names, as [`iso_of`] or
/// [`canonical_iso_of`] writes it.
pub fn ms_of_iso(text: &str) -> Option<i64> {
    let bytes = text.as_bytes();
    if bytes.len() < 20
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || bytes[10] != b'T'
        || bytes[13] != b':'
        || bytes[16] != b':'
        || bytes[bytes.len() - 1] != b'Z'
    {
        return None;
    }
    let number = |range: std::ops::Range<usize>| text.get(range)?.parse::<i64>().ok();
    let (year, month, day) = (number(0..4)?, number(5..7)?, number(8..10)?);
    let fraction = text.get(19..text.len() - 1)?;
    let milli = match fraction.strip_prefix('.') {
        None if fraction.is_empty() => 0,
        Some(digits)
            if (1..=3).contains(&digits.len()) && digits.bytes().all(|b| b.is_ascii_digit()) =>
        {
            format!("{digits:0<3}").parse::<i64>().ok()?
        }
        _ => return None,
    };
    let (hour, minute, second) = (number(11..13)?, number(14..16)?, number(17..19)?);
    // Howard Hinnant's days_from_civil.
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y.rem_euclid(400);
    let mp = if month > 2 { month - 3 } else { month + 9 };
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    let ms = days * 86_400_000 + hour * 3_600_000 + minute * 60_000 + second * 1000 + milli;
    (iso_of(ms) == text || canonical_iso_of(ms) == text).then_some(ms)
}

/// The core meta-schema with behaviors let through, as the TypeScript
/// tests' `openMetaSchema()` makes it and the corpus names `"open"`.
pub fn open_meta_schema(repository: &Path) -> Result<Value, String> {
    let path = repository.join("ir/typescript/schema-file.json");
    let text =
        std::fs::read_to_string(&path).map_err(|error| format!("{}: {error}", path.display()))?;
    let mut document: Value = serde_json::from_str(&text).map_err(|error| error.to_string())?;
    if let Some(behaviors) = document
        .pointer_mut("/$defs/TypeDef/properties/behaviors")
        .and_then(Value::as_object_mut)
    {
        behaviors.shift_remove("maxItems");
    }
    if let Some(name) = document
        .pointer_mut("/$defs/BehaviorRef/properties/name")
        .and_then(Value::as_object_mut)
    {
        name.shift_remove("enum");
    }
    if let Some(reference) = document
        .pointer_mut("/$defs/BehaviorRef")
        .and_then(Value::as_object_mut)
    {
        reference.shift_remove("allOf");
    }
    Ok(document)
}

/// An error as the corpus records it: its class, its code, its message,
/// and its other members as `details`, as the TypeScript engine throws it
/// ([`Error::thrown`], which the Node-API addon throws too).
pub fn error_json(error: &Error) -> Value {
    let thrown = error.thrown();
    let mut out = Map::new();
    out.insert("class".into(), Value::from(thrown.class));
    if let Some(code) = thrown.code {
        out.insert("code".into(), code);
    }
    out.insert("message".into(), Value::from(thrown.message));
    if let Some(members) = thrown.members {
        out.insert("details".into(), Value::Object(members));
    }
    Value::Object(out)
}

// storage_call runs a step's SQL on the engine's SQLite file, as the
// TypeScript engine's storage.get, all, run and exec return.
fn storage_call(engine: &Engine, call: &str, args: &[Value]) -> Result<Option<Value>, Value> {
    let sql = args
        .first()
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    let params: Vec<SqlValue> = args
        .get(1)
        .and_then(Value::as_array)
        .map(|params| params.iter().map(sql_value).collect())
        .unwrap_or_default();
    let db = engine.db();
    let failed = |error: superschematic_engine::storage::Error| error_json(&Error::Storage(error));
    match call {
        "storage.get" => db
            .read(|tx| tx.get(sql.as_str(), &params))
            .map(|row| row.map(|row| row_json(&row)))
            .map_err(failed),
        "storage.all" => db
            .read(|tx| tx.all(sql.as_str(), &params))
            .map(|rows| Some(Value::Array(rows.iter().map(row_json).collect())))
            .map_err(failed),
        "storage.run" => db
            .write(Lock::Catalog, |tx| tx.run(sql.as_str(), &params))
            .map(|result| {
                let mut out = Map::new();
                out.insert("changes".into(), Value::from(result.changes));
                out.insert(
                    "lastInsertRowid".into(),
                    Value::from(result.last_insert_rowid.unwrap_or(0)),
                );
                Some(Value::Object(out))
            })
            .map_err(failed),
        _ => db
            .write(Lock::Catalog, |tx| tx.exec(sql.as_str()))
            .map(|()| None)
            .map_err(failed),
    }
}

fn sql_value(value: &Value) -> SqlValue {
    match value {
        Value::Null => SqlValue::Null,
        Value::Bool(flag) => SqlValue::Integer(i64::from(*flag)),
        Value::Number(number) => {
            let read = superschematic_engine::json::number_value(number);
            if read.fract() == 0.0 && read.abs() < 9.2e18 {
                SqlValue::Integer(read as i64)
            } else {
                SqlValue::Real(read)
            }
        }
        Value::String(text) => SqlValue::Text(text.clone()),
        Value::Object(members) => match members.get("$bytes").and_then(Value::as_str) {
            Some(hex) => SqlValue::Blob(
                (0..hex.len() / 2)
                    .filter_map(|at| u8::from_str_radix(hex.get(at * 2..at * 2 + 2)?, 16).ok())
                    .collect(),
            ),
            None => SqlValue::Text(value.to_string()),
        },
        Value::Array(_) => SqlValue::Text(value.to_string()),
    }
}

fn row_json(row: &Row) -> Value {
    let mut out = Map::new();
    for (column, value) in row.iter() {
        let json = match value {
            SqlValue::Null => Value::Null,
            SqlValue::Integer(number) => Value::from(*number),
            SqlValue::Real(number) => {
                serde_json::from_str(&superschematic_engine::json::js_number(*number))
                    .unwrap_or(Value::Null)
            }
            SqlValue::Text(text) => Value::from(text.as_str()),
            SqlValue::Blob(bytes) => {
                let hex: String = bytes.iter().map(|byte| format!("{byte:02x}")).collect();
                let mut blob = Map::new();
                blob.insert("$bytes".into(), Value::from(hex));
                Value::Object(blob)
            }
        };
        out.insert(column.to_owned(), json);
    }
    Value::Object(out)
}

/// A policy that answers from a recording's answer table: each question the
/// engine asked, once, with its answer, a question by its principal's name
/// in the scenario. Only a literal true allows, as in the TypeScript
/// engine. A question the table does not hold is answered false and noted,
/// which fails the step: the Rust engine asked what the TypeScript engine
/// did not.
pub struct AnswerPolicy {
    answers: Vec<(Value, Value)>,
    principals: Map<String, Value>,
    unknown: Arc<Mutex<Vec<String>>>,
}

impl AnswerPolicy {
    /// A policy over `answers`, `[[question, answer], ...]`, whose questions
    /// name principals by their keys in `principals`; a question the table
    /// lacks is noted in `unknown`.
    pub fn new(
        answers: Vec<(Value, Value)>,
        principals: Map<String, Value>,
        unknown: Arc<Mutex<Vec<String>>>,
    ) -> Self {
        AnswerPolicy {
            answers,
            principals,
            unknown,
        }
    }

    fn name_of(&self, principal: &Principal) -> Value {
        let given = serde_json::to_value(principal).unwrap_or(Value::Null);
        self.principals
            .iter()
            .find(|(_, known)| js_equal(known, &given))
            .map_or_else(
                || Value::from(principal.subject.as_str()),
                |(name, _)| Value::from(name.as_str()),
            )
    }
}

impl AccessPolicy for AnswerPolicy {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        let question = request.to_json(self.name_of(request.principal()));
        match self
            .answers
            .iter()
            .find(|(asked, _)| js_equal(asked, &question))
        {
            Some((_, answer)) => answer == &Value::Bool(true),
            None => {
                if let Ok(mut unknown) = self.unknown.lock() {
                    unknown.push(question.to_string());
                }
                false
            }
        }
    }
}

/// The files of the corpus a replay runs by default: the engine's own
/// test files, whose scenarios need no behavior or few, the core
/// behaviors' files, the work queue's (`workqueue-*`) and the commerce
/// package's (`commerce-*`), whose engines register the package's
/// behaviors by name after the core's ([`behaviors::package_behavior`]).
pub const ENGINE_FILES: [&str; 51] = [
    "access",
    "commerce-holds",
    "commerce-holds-timers",
    "commerce-payments",
    "commerce-payments-holds",
    "commerce-payments-refunds",
    "commerce-stock",
    "compat",
    "core-approvals",
    "core-behaviors",
    "core-branches",
    "core-branches-sweep",
    "core-comments",
    "core-constants",
    "core-dependencies",
    "core-links",
    "core-links-many",
    "core-numbering",
    "core-reactions",
    "core-reactions-changes",
    "core-reactions-holds-revised",
    "core-revisions",
    "core-rollups",
    "core-search",
    "core-search-vectors",
    "core-staterules",
    "core-timers",
    "core-variants",
    "core-workflow",
    "events",
    "instances",
    "list-filters",
    "namespaces",
    "normalize",
    "registry",
    "unique",
    "workqueue-assignment",
    "workqueue-blueprint",
    "workqueue-budget",
    "workqueue-filters",
    "workqueue-lease",
    "workqueue-lease-approvals",
    "workqueue-lease-branches",
    "workqueue-lease-direct-on",
    "workqueue-lease-numbering",
    "workqueue-lease-timers",
    "workqueue-package",
    "workqueue-presence",
    "workqueue-queue",
    "workqueue-queue-exclusion",
    "workqueue-retries",
];

/// Counts of a set of reports, by file.
pub fn summarize(reports: &[FileReport]) -> BTreeMap<String, (usize, usize, usize)> {
    reports
        .iter()
        .map(|report| {
            (
                report.file.clone(),
                (
                    report.steps_passed,
                    report.steps_skipped,
                    report.failures.len(),
                ),
            )
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn instants_read_and_write_as_javascript_writes_them() {
        assert_eq!(iso_of(0), "1970-01-01T00:00:00.000Z");
        assert_eq!(iso_of(1_760_000_000_123), "2025-10-09T08:53:20.123Z");
        assert_eq!(
            ms_of_iso("2025-10-09T08:53:20.123Z"),
            Some(1_760_000_000_123)
        );
        assert_eq!(ms_of_iso("2025-13-09T08:53:20.123Z"), None);
        assert_eq!(iso_of(951_782_400_000), "2000-02-29T00:00:00.000Z");
        // The version graph's canonical date-time trims its fraction.
        assert_eq!(
            canonical_iso_of(1_760_000_000_120),
            "2025-10-09T08:53:20.12Z"
        );
        assert_eq!(canonical_iso_of(1_760_000_000_000), "2025-10-09T08:53:20Z");
        assert_eq!(
            ms_of_iso("2025-10-09T08:53:20.12Z"),
            Some(1_760_000_000_120)
        );
        assert_eq!(ms_of_iso("2025-10-09T08:53:20Z"), Some(1_760_000_000_000));
        assert_eq!(
            ms_of_iso("2025-10-09T08:53:20.120Z"),
            Some(1_760_000_000_120)
        );
        assert_eq!(ms_of_iso("2025-10-09T08:53:20.1200Z"), None);
        assert_eq!(ms_of_iso("2025-10-09T08:53:20.Z"), None);
    }

    #[test]
    fn symbols_are_found_in_their_strings() {
        let parts = segments("behavior Links vetoes create of Task {{id:1}}: no {{x}}");
        assert_eq!(parts.len(), 3);
        assert!(matches!(&parts[1], Segment::Symbol(symbol) if symbol.key() == "id:1"));
        assert!(matches!(&segments("{{time:3:us}}")[0], Segment::Symbol(symbol) if symbol.micros));
    }

    #[test]
    fn a_cursor_on_postgres_binds_to_the_one_read_in_its_place_in_the_recorded_order() {
        let page = |cursors: &[i64], next: i64| {
            json!({
                "events": cursors.iter().map(|cursor| json!({"cursor": cursor, "kind": "create"})).collect::<Vec<_>>(),
                "next": next,
                "more": false
            })
        };
        let postgres = Database::Postgres { url: String::new() };
        let repository = PathBuf::new();
        let mut run = Run::new(&postgres, &repository, "f", &json!({}), &Direct);
        run.matches(&page(&[1, 2], 2), &page(&[1, 2], 2), "")
            .expect("as recorded");
        // A rolled-back write took cursor 3: the recording's 3 and 4 read
        // as 4 and 5, and an argument after 2 passes 2, after 4 passes 5.
        run.matches(&page(&[3, 4], 4), &page(&[4, 5], 5), "")
            .expect("a gap");
        assert_eq!((run.cursor_argument(2), run.cursor_argument(4)), (2, 5));
        assert_eq!(run.cursor_argument(6), 7);
        let refused = run.matches(&page(&[4], 4), &page(&[6], 6), "");
        assert!(refused.is_err_and(|error| error.contains("bound to 5")));
        let refused = run.matches(&page(&[5], 5), &page(&[3], 3), "");
        assert!(refused.is_err_and(|error| error.contains("out of the recorded order")));
        // On SQLite a cursor is compared as it is.
        let sqlite = Database::Sqlite;
        let mut run = Run::new(&sqlite, &repository, "f", &json!({}), &Direct);
        assert!(run.matches(&page(&[3], 3), &page(&[4], 4), "").is_err());
    }
}
