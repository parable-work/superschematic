//! The replayer (D103): it holds the core and the ported behaviors to the
//! recorded scenarios (`runtime/engine/testdata/scenarios`, whose README is
//! the format), through the reference host. A recording is the engine's
//! calls and answers; the replayer maps each call onto the host and
//! compares what the core and its behaviors decide, and only that:
//!
//! - `open` opens a fresh host on the step's clock: a fixed clock reads the
//!   steps' `now` (or `clock`), the wall clock a strict clock of the
//!   replay's own, whose instants the recording writes as symbols. An
//!   engine whose permission matcher is a test's own runs the matcher
//!   [`fixtures`] ports from it; one whose matcher no fixture ports, or
//!   whose access policy is a test's own, is not mapped, and neither are
//!   its steps.
//! - `schemas.define` reads the schema document as a type, each behavior a
//!   use on it, and runs the core's define check and, when the type has a
//!   live version, its config-change check; `schemas.publish` runs the
//!   change check again and publishes.
//! - `instances.create`, `update`, `delete` and `invoke` run the core's
//!   commands; `get`, `list` and `events.read` read the host. A record is
//!   compared by its id, its own fields (`data`) and its `behaviors`, the
//!   fields each behavior owns; an operation by its result; an event by
//!   its kind and its change: a create's fields, an operation's behavior,
//!   name and parameters, and the own and owned fields a write set.
//! - `schemas.list`, `schemas.live` and `schemas.behaviors` are compared
//!   by the types defined, the live version's behavior configs, and each
//!   behavior's declared fields, operations and veto codes.
//! - A refusal is compared whole, as the engine's error: its class, code,
//!   status, message and details. Where the engine's names what the core
//!   does not know (a namespace, a version, a path into the schema
//!   document or the create's request) the host writes it from the
//!   refusal's details ([`shape::engine_error`]), as a host does.
//!
//! What belongs to the engine's store is not compared: sequence numbers,
//! versions and timestamps, the schema document define echoes back,
//! describe and tools documents. A write the store refuses for its own
//! fields is not run: own-field validation is the table's. A test's raw
//! SQL write of behaviors' columns in the one form [`shape::raw_owned_write`]
//! reads is applied to the row past the adapter, and the step, whose result
//! is SQLite's, is skipped; any other SQL of the test's own stops the
//! scenario there, since the steps after it read what it wrote. A step that
//! cannot be mapped (a call outside this surface, a behavior written in
//! TypeScript that no fixture ports, a fixture's operation that tests the
//! store) is skipped with its reason.
//!
//! [`crate::transcript`] writes a recording out as a transcript through this
//! same mapping: the replayer records each command, check and raw write it
//! made on the host, with what the host answered, as it makes it.

use std::collections::BTreeMap;
use std::fmt;
use std::path::{Path, PathBuf};

use serde_json::{json, Map, Value};

use superschematic_engine_testkit::{load, ScenarioFile};
use superschematic_rules::{Action, Caller, Code, Command, Core, Declaration, Refusal, TypeSpec};

use crate::fixtures;
use crate::host::{Applied, MemoryHost};
use crate::shape::{self, FieldDef};
use crate::symbols::Symbols;
use crate::transcript::{Recorded, TranscriptClock};

/// Where the wall clock of a replay starts: an instant of the recording's
/// span (thirteen digits), far from any fixed clock's.
const WALL_START: i64 = 1_767_225_600_000;

/// What replaying one file did.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Report {
    pub file: String,
    pub scenarios: Vec<ScenarioReport>,
}

/// What replaying one scenario did: every step is checked or skipped, and
/// a checked step may have mismatches.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ScenarioReport {
    pub name: String,
    pub steps: usize,
    /// Steps mapped onto the host and compared.
    pub checked: usize,
    pub skipped: Vec<Skipped>,
    pub mismatches: Vec<Mismatch>,
}

/// A step not mapped, and why.
#[derive(Clone, Debug, PartialEq)]
pub struct Skipped {
    /// The step's place in the scenario, from 1.
    pub step: usize,
    pub call: String,
    pub reason: String,
}

/// A difference between what the recording holds and what the host
/// answered.
#[derive(Clone, Debug, PartialEq)]
pub struct Mismatch {
    /// The step's place in the scenario, from 1.
    pub step: usize,
    pub call: String,
    /// What differs: `outcome`, `error.details`, `behaviors`, ...
    pub what: String,
    pub expected: Value,
    pub actual: Value,
}

impl fmt::Display for Mismatch {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "step {} ({}): {} expected {} got {}",
            self.step,
            self.call,
            self.what,
            serde_json::to_string(&self.expected).unwrap_or_default(),
            serde_json::to_string(&self.actual).unwrap_or_default()
        )
    }
}

impl Report {
    /// Steps checked across the file.
    pub fn checked(&self) -> usize {
        self.scenarios.iter().map(|scenario| scenario.checked).sum()
    }

    /// Steps skipped across the file.
    pub fn skipped(&self) -> usize {
        self.scenarios
            .iter()
            .map(|scenario| scenario.skipped.len())
            .sum()
    }

    /// Every mismatch, with its scenario's name.
    pub fn mismatches(&self) -> Vec<(&str, &Mismatch)> {
        self.scenarios
            .iter()
            .flat_map(|scenario| {
                scenario
                    .mismatches
                    .iter()
                    .map(|mismatch| (scenario.name.as_str(), mismatch))
            })
            .collect()
    }

    /// One line: the file, its scenarios and its steps checked, skipped
    /// and mismatched.
    pub fn summary(&self) -> String {
        format!(
            "{}: {} scenarios, {} steps checked, {} skipped, {} mismatches",
            self.file,
            self.scenarios.len(),
            self.checked(),
            self.skipped(),
            self.mismatches().len()
        )
    }

    /// The summary, then each scenario's skipped steps and mismatches.
    pub fn details(&self) -> String {
        let mut out = self.summary();
        for scenario in &self.scenarios {
            if scenario.skipped.is_empty() && scenario.mismatches.is_empty() {
                continue;
            }
            out.push_str(&format!(
                "\n{} ({} of {} steps checked)",
                scenario.name, scenario.checked, scenario.steps
            ));
            for skipped in &scenario.skipped {
                out.push_str(&format!(
                    "\n  skipped step {} ({}): {}",
                    skipped.step, skipped.call, skipped.reason
                ));
            }
            for mismatch in &scenario.mismatches {
                out.push_str(&format!("\n  mismatch {mismatch}"));
            }
        }
        out
    }
}

/// The path of a recorded scenario file by its name (`core-workflow`).
pub fn scenario_path(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/scenarios")
        .join(format!("{name}.json"))
}

/// Reads a scenario file (its shared values resolved) and replays it on
/// cores `make_core` builds, one for each engine a scenario opens.
pub fn replay_path(path: &Path, make_core: &dyn Fn() -> Core) -> Result<Report, String> {
    Ok(replay(&load(path)?, make_core))
}

/// Replays every scenario of a file on cores `make_core` builds, one for
/// each engine a scenario opens, with the test behaviors a scenario names
/// added from [`fixtures`].
pub fn replay(file: &ScenarioFile, make_core: &dyn Fn() -> Core) -> Report {
    replay_recording(file, make_core, false).0
}

/// Replays a file as [`replay`] does and, when `record` is set, records
/// for each scenario, in order, what a transcript holds of it: the
/// commands, checks and raw writes the replay made on the host and what
/// the host answered ([`Recorded`]).
pub(crate) fn replay_recording(
    file: &ScenarioFile,
    make_core: &dyn Fn() -> Core,
    record: bool,
) -> (Report, Vec<Recorded>) {
    let runs = make_core().names();
    let mut report = Report {
        file: file.name.clone(),
        scenarios: Vec::new(),
    };
    let mut recorded = Vec::new();
    for scenario in &file.scenarios {
        let steps = scenario
            .get("steps")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        let mut run = Run::new(&file.name, scenario, make_core, steps.len());
        run.recorded = record.then(Recorded::default);
        match skip_reason(&file.name, scenario, &runs) {
            Some(reason) => {
                for (index, step) in steps.iter().enumerate() {
                    run.skip(index, step, reason.clone());
                }
            }
            None => {
                for (index, step) in steps.iter().enumerate() {
                    run.step(index, step);
                }
            }
        }
        recorded.extend(run.recorded.take());
        report.scenarios.push(run.report);
    }
    (report, recorded)
}

// names reads a scenario's list of names under a key.
fn names(scenario: &Value, key: &str) -> Vec<String> {
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
}

// skip_reason says why no step of a scenario is mapped, or None: only the
// recording's own host runs it (a permission matcher of the test's own
// skips only its engine's steps), it needs a test behavior no fixture
// ports, or it composes a behavior the core does not run.
fn skip_reason(file: &str, scenario: &Value, runs: &[String]) -> Option<String> {
    let js: Vec<String> = names(scenario, "js")
        .into_iter()
        .filter(|reason| reason != "permissionMatcher")
        .collect();
    if !js.is_empty() {
        return Some(format!(
            "only the recording's own host runs it (js: {})",
            js.join(", ")
        ));
    }
    let tests = names(scenario, "testBehaviors");
    let unported: Vec<&String> = tests
        .iter()
        .filter(|name| !fixtures::is_ported(file, name))
        .collect();
    if !unported.is_empty() {
        return Some(format!(
            "it needs test behaviors written in TypeScript that no fixture ports: {}",
            listed(&unported)
        ));
    }
    let missing: Vec<String> = names(scenario, "behaviors")
        .into_iter()
        .filter(|name| !tests.contains(name) && !runs.contains(name))
        .collect();
    if !missing.is_empty() {
        return Some(format!(
            "it composes behaviors the core does not run: {}",
            missing.join(", ")
        ));
    }
    None
}

fn listed(names: &[&String]) -> String {
    names
        .iter()
        .map(|name| name.as_str())
        .collect::<Vec<_>>()
        .join(", ")
}

// is_read_only says whether a call the replayer does not map changes
// nothing a later step reads, so skipping it leaves the scenario whole.
fn is_read_only(call: &str) -> bool {
    call.starts_with("tools.")
        || matches!(
            call,
            "schemas.get"
                | "schemas.describe"
                | "schemas.versions"
                | "schemas.config"
                | "schemas.validator"
                | "behaviors.names"
                | "behaviors.has"
                | "behaviors.declaration"
                | "events.head"
                | "instances.count"
                | "instances.history"
                | "namespaces.list"
                | "namespaces.get"
                | "runner.status"
                | "search"
                | "storage.get"
                | "storage.all"
                | "values.get"
        )
}

// The clock an engine reads.
enum Clock {
    /// A test's own: the steps' `now` and `clock`.
    Fixed(i64),
    /// The wall clock, the replay's strict clock.
    Wall,
}

// One engine a scenario opened, on the host.
struct Engine {
    name: String,
    path: String,
    host: MemoryHost,
    clock: Clock,
    /// Why its steps are not mapped.
    off: Option<String>,
    /// The own fields of each type's version defined and not published.
    drafts: BTreeMap<String, Vec<FieldDef>>,
    /// The own fields of each type's live version.
    fields: BTreeMap<String, Vec<FieldDef>>,
}

// What a step expects.
enum Expected<'s> {
    /// A returned value.
    Value(&'s Value),
    /// Nothing returned.
    Nothing,
    /// A thrown error.
    Error(&'s Value),
}

impl Expected<'_> {
    fn of(step: &Value) -> Expected<'_> {
        if let Some(error) = step.get("error") {
            return Expected::Error(error);
        }
        match step.get("expect") {
            Some(value) => Expected::Value(value),
            None => Expected::Nothing,
        }
    }

    // json is what the step expects, for a mismatch.
    fn json(&self) -> Value {
        match self {
            Expected::Value(value) => (*value).clone(),
            Expected::Nothing => json!({ "returned": "nothing" }),
            Expected::Error(error) => (*error).clone(),
        }
    }
}

// What one step came to.
enum Done {
    Checked(Vec<Found>),
    Skipped(String),
    /// Skipped, and so is every step after it.
    Halted(String),
}

// A difference within a step.
struct Found {
    what: String,
    expected: Value,
    actual: Value,
}

// Checks compares within one step, binding symbols as it goes.
struct Checks<'s> {
    symbols: &'s mut Symbols,
    found: Vec<Found>,
}

impl Checks<'_> {
    fn eq(&mut self, what: &str, expected: &Value, actual: &Value) {
        if !self.symbols.matches(expected, actual) {
            self.found.push(Found {
                what: what.to_owned(),
                expected: expected.clone(),
                actual: actual.clone(),
            });
        }
    }
}

// One scenario's replay.
struct Run<'r> {
    file: &'r str,
    make_core: &'r dyn Fn() -> Core,
    test_behaviors: Vec<String>,
    principals: Map<String, Value>,
    engines: Vec<Engine>,
    symbols: Symbols,
    wall: i64,
    halted: Option<(usize, String)>,
    report: ScenarioReport,
    /// The step being made, from 1, and its call, for the recording.
    current: (usize, String),
    /// What a transcript holds of the scenario, when one is written.
    recorded: Option<Recorded>,
}

impl<'r> Run<'r> {
    fn new(file: &'r str, scenario: &Value, make_core: &'r dyn Fn() -> Core, steps: usize) -> Self {
        Run {
            file,
            make_core,
            test_behaviors: names(scenario, "testBehaviors"),
            principals: scenario
                .get("principals")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default(),
            engines: Vec::new(),
            symbols: Symbols::default(),
            wall: WALL_START,
            halted: None,
            current: (0, String::new()),
            recorded: None,
            report: ScenarioReport {
                name: scenario
                    .get("name")
                    .and_then(Value::as_str)
                    .unwrap_or("?")
                    .to_owned(),
                steps,
                ..ScenarioReport::default()
            },
        }
    }

    fn skip(&mut self, index: usize, step: &Value, reason: String) {
        self.report.skipped.push(Skipped {
            step: index + 1,
            call: call_of(step).to_owned(),
            reason,
        });
    }

    fn step(&mut self, index: usize, step: &Value) {
        if let Some((at, reason)) = &self.halted {
            let reason = format!("the scenario stopped at step {at}: {reason}");
            self.skip(index, step, reason);
            return;
        }
        self.current = (index + 1, call_of(step).to_owned());
        let done = if step.get("js").and_then(Value::as_bool) == Some(true) {
            Done::Halted("only the recording's own host runs it (js)".to_owned())
        } else {
            self.make(step)
        };
        match done {
            Done::Checked(found) => {
                self.report.checked += 1;
                let call = call_of(step).to_owned();
                self.report
                    .mismatches
                    .extend(found.into_iter().map(|found| Mismatch {
                        step: index + 1,
                        call: call.clone(),
                        what: found.what,
                        expected: found.expected,
                        actual: found.actual,
                    }));
            }
            Done::Skipped(reason) => self.skip(index, step, reason),
            Done::Halted(reason) => {
                self.skip(index, step, reason.clone());
                self.halted = Some((index + 1, reason));
            }
        }
    }

    // make maps one step onto the host and compares its answer.
    fn make(&mut self, step: &Value) -> Done {
        let call = call_of(step);
        if call == "open" {
            return self.open(step);
        }
        let at = match step.get("engine").and_then(Value::as_str) {
            Some(name) => self.engines.iter().position(|engine| engine.name == name),
            None => self.engines.len().checked_sub(1),
        };
        let Some(at) = at else {
            return Done::Skipped("no engine is open".to_owned());
        };
        if let Some(reason) = &self.engines[at].off {
            return Done::Skipped(reason.clone());
        }
        if call == "close" {
            return Done::Checked(Vec::new());
        }
        let caller = match self.caller(step) {
            Ok(caller) => caller,
            Err(reason) => return Done::Skipped(reason),
        };
        let args = self.symbols.substitute(
            &step
                .get("args")
                .cloned()
                .unwrap_or_else(|| Value::Array(Vec::new())),
        );
        let args = args.as_array().cloned().unwrap_or_default();
        if args.iter().any(holds_unjsonable) {
            return Done::Skipped("an argument JSON cannot hold".to_owned());
        }
        let now = self.now(at, step);
        if let Some(ids) = step.get("ids").and_then(Value::as_array) {
            let ids: Vec<String> = ids
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect();
            self.engines[at].host.queue_ids(ids);
        }
        let expected = Expected::of(step);
        let cx = Cx {
            at,
            caller: &caller,
            now,
            args: &args,
            expected: &expected,
        };
        match call {
            "schemas.define" => self.define(&cx),
            "schemas.publish" => self.publish(&cx),
            "schemas.list" => self.list_schemas(&cx),
            "schemas.live" => self.live(&cx),
            "schemas.behaviors" => self.behaviors(&cx),
            "instances.create" => self.create(&cx),
            "instances.get" => self.get(&cx),
            "instances.list" => self.list(&cx),
            "instances.update" => self.update(&cx),
            "instances.delete" => self.delete(&cx),
            "instances.invoke" => self.invoke(&cx),
            "events.read" => self.events(&cx),
            "storage.run" => self.raw_write(&cx),
            "storage.exec" | "storage.transaction" => Done::Halted(format!(
                "it writes with SQL of its own ({call}), which the reference host has no notion of, and the steps after it read what it wrote"
            )),
            _ if is_read_only(call) => Done::Skipped(format!(
                "{call} is outside the reference host's surface; it changes nothing"
            )),
            _ => Done::Halted(format!(
                "{call} is outside the reference host's surface, and the steps after it may read what it changed"
            )),
        }
    }

    // ---- open, callers and clocks ----

    fn open(&mut self, step: &Value) -> Done {
        let options = step
            .get("args")
            .and_then(|args| args.get(0))
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        let name = step
            .get("as")
            .and_then(Value::as_str)
            .map_or_else(|| format!("e{}", self.engines.len() + 1), str::to_owned);
        let path = options
            .get("path")
            .and_then(Value::as_str)
            .unwrap_or(&name)
            .to_owned();
        let mut off = None;
        let mut core = (self.make_core)();
        if options.contains_key("permissionMatcher") {
            // The recording names the test's function and does not carry
            // it; a fixture ports the ones the corpus opens engines with.
            match fixtures::with_matcher(self.file, &self.report.name, core) {
                Some(matched) => core = matched,
                None => {
                    core = (self.make_core)();
                    off = Some("its permission matcher is a function of the test's, which the recording does not carry and no fixture ports (js: permissionMatcher)".to_owned());
                }
            }
        }
        match options.get("policy").and_then(Value::as_str) {
            Some("allowAll") => {}
            _ => {
                off.get_or_insert_with(|| {
                    "its access policy is not allowAll, and the reference host applies none: the API authorizes (D103)".to_owned()
                });
            }
        }
        for behavior in options
            .get("behaviors")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
        {
            let known = if self.test_behaviors.iter().any(|test| test == behavior) {
                fixtures::register(self.file, behavior, &mut core)
            } else {
                core.declaration(behavior).is_some()
            };
            if !known {
                off.get_or_insert_with(|| {
                    format!("it registers {behavior}, which the core does not run")
                });
            }
        }
        let clock = match options.get("clock").and_then(Value::as_str) {
            Some("fixed") => Clock::Fixed(step.get("now").and_then(Value::as_i64).unwrap_or(0)),
            _ => Clock::Wall,
        };
        if let Some(recorded) = &mut self.recorded {
            recorded.opened(match clock {
                Clock::Fixed(reading) => TranscriptClock::Fixed(reading),
                Clock::Wall => TranscriptClock::Wall,
            });
        }
        // Two opens of one path are one file: the second reopens what the
        // first wrote.
        let store = self
            .engines
            .iter_mut()
            .find(|engine| engine.path == path)
            .map(|earlier| {
                earlier.off = Some(format!("{} was reopened as {name}", earlier.name));
                std::mem::replace(&mut earlier.host, MemoryHost::new(Core::new())).into_store()
            });
        let host = match store {
            Some(store) => MemoryHost::with_store(core, store),
            None => MemoryHost::new(core),
        };
        self.engines.push(Engine {
            name,
            path,
            host,
            clock,
            off,
            drafts: BTreeMap::new(),
            fields: BTreeMap::new(),
        });
        Done::Checked(Vec::new())
    }

    // caller is the step's principal as the core's caller.
    fn caller(&self, step: &Value) -> Result<Caller, String> {
        let principal = match step.get("principal") {
            None => return Ok(Caller::new("", Vec::<String>::new())),
            Some(Value::String(name)) => self
                .principals
                .get(name)
                .ok_or_else(|| format!("no principal {name}"))?,
            Some(_) => {
                return Err(
                    "its principal is a value that is not one, a test of the engine's refusal"
                        .to_owned(),
                )
            }
        };
        let subject = principal
            .get("subject")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let stands_in = principal
            .get("service")
            .and_then(|service| service.get("standsIn"))
            .and_then(Value::as_bool)
            == Some(true);
        if stands_in {
            return Ok(Caller::standing_in(subject));
        }
        let permissions: Vec<String> = principal
            .get("permissions")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect();
        Ok(Caller::new(subject, permissions))
    }

    // now is the time a step's command runs at: a fixed clock's reading,
    // or the next instant of the wall clock, which never reads one twice.
    fn now(&mut self, at: usize, step: &Value) -> i64 {
        match &mut self.engines[at].clock {
            Clock::Fixed(reading) => {
                if let Some(readings) = step.get("clock").and_then(Value::as_array) {
                    let times: Vec<i64> = readings.iter().filter_map(Value::as_i64).collect();
                    if let (Some(first), Some(last)) = (times.first(), times.last()) {
                        *reading = *last;
                        return *first;
                    }
                }
                if let Some(now) = step.get("now").and_then(Value::as_i64) {
                    *reading = now;
                }
                *reading
            }
            Clock::Wall => {
                self.wall += 1;
                self.wall
            }
        }
    }

    // ---- judging ----

    // judge compares a call's outcome with what the step expects: a
    // refusal with the error, a value with `check`.
    fn judge(
        &mut self,
        cx: &Cx<'_>,
        type_name: &str,
        live_version: usize,
        outcome: Result<Value, Refusal>,
        check: impl FnOnce(&mut Checks<'_>, &Value, &Value),
    ) -> Done {
        let mut checks = Checks {
            symbols: &mut self.symbols,
            found: Vec::new(),
        };
        match (cx.expected, outcome) {
            (Expected::Error(error), Err(refusal)) => {
                compare_error(&mut checks, error, &refusal, type_name, live_version);
            }
            (Expected::Error(error), Ok(value)) => checks.found.push(Found {
                what: "outcome".to_owned(),
                expected: (*error).clone(),
                actual: json!({ "returned": value }),
            }),
            (expected, Err(refusal)) => checks.found.push(Found {
                what: "outcome".to_owned(),
                expected: expected.json(),
                actual: shape::engine_error(&refusal, type_name, live_version),
            }),
            (Expected::Value(expected), Ok(value)) => check(&mut checks, expected, &value),
            (Expected::Nothing, Ok(_)) => {}
        }
        Done::Checked(checks.found)
    }

    // raw_write applies a test's raw SQL write of behaviors' columns to the
    // row, past the adapter, as an operator's repair writes one (D103); any
    // other statement stops the scenario.
    fn raw_write(&mut self, cx: &Cx<'_>) -> Done {
        let halted = Done::Halted(
            "it writes with SQL of its own (storage.run), which the reference host has no notion of, and the steps after it read what it wrote"
                .to_owned(),
        );
        let Some(sql) = cx.args.first().and_then(Value::as_str) else {
            return halted;
        };
        let host = &mut self.engines[cx.at].host;
        let store = host.store();
        let live = |type_name: &str| store.live(type_name).cloned();
        let Some(write) = shape::raw_owned_write(sql, host.core(), &live) else {
            return halted;
        };
        if !host
            .store_mut()
            .write_owned(&write.type_name, &write.key, &write.owned)
        {
            return halted;
        }
        if let Some(recorded) = &mut self.recorded {
            let database = self.engines[cx.at].path.clone();
            recorded.raw(&self.current, &database, write);
        }
        Done::Skipped(
            "it writes behaviors' columns with SQL of its own, whose result (changes, lastInsertRowid) is SQLite's; the replayer applied the write to the row past the adapter, as a raw write a host does not refuse (D103)"
                .to_owned(),
        )
    }

    // ---- schemas ----

    fn define(&mut self, cx: &Cx<'_>) -> Done {
        let Some(document) = cx.args.first() else {
            return Done::Skipped("a define without a document".to_owned());
        };
        let defined = match shape::read_document(document) {
            Ok(defined) => defined,
            Err(reason) => {
                return Done::Skipped(format!(
                    "a document the reference host cannot read: {reason}"
                ))
            }
        };
        if let Expected::Error(error) = cx.expected {
            if is_document_error(error) {
                return Done::Skipped(
                    "the schema document's own validation is the store's".to_owned(),
                );
            }
        }
        let type_name = defined.spec.name.clone();
        let engine = &mut self.engines[cx.at];
        let live_version = engine.host.store().live_version(&type_name);
        let check = engine.host.define_command(&defined.spec, cx.caller, cx.now);
        let spec = defined.spec.clone();
        let outcome = engine.host.define(defined.spec, cx.caller, cx.now);
        if let Some(recorded) = &mut self.recorded {
            recorded.check(
                &self.current,
                &engine.path,
                spec,
                check,
                outcome.as_ref().err(),
            );
        }
        if outcome.is_ok() {
            engine.drafts.insert(type_name.clone(), defined.fields);
        }
        self.judge(
            cx,
            &type_name,
            live_version,
            outcome.map(|()| Value::Null),
            |_, _, _| {},
        )
    }

    fn publish(&mut self, cx: &Cx<'_>) -> Done {
        let Some(type_name) = cx.args.first().and_then(Value::as_str) else {
            return Done::Skipped("a publish that names no schema".to_owned());
        };
        let engine = &mut self.engines[cx.at];
        let live_version = engine.host.store().live_version(type_name);
        let pending = engine.host.publish_command(type_name, cx.caller, cx.now);
        let published = engine.host.publish(type_name, cx.caller, cx.now);
        if let (Some(recorded), Some((spec, check))) = (&mut self.recorded, pending) {
            recorded.check(
                &self.current,
                &engine.path,
                spec.clone(),
                check,
                published.as_ref().err(),
            );
            if let (Ok(Some(_)), Some(fields)) = (&published, engine.drafts.get(type_name)) {
                recorded.published(spec, fields);
            }
        }
        let outcome = published.map(|published| match published {
            Some(version) => json!({ "version": version, "published": true }),
            None => json!({ "published": false }),
        });
        if outcome.is_ok() {
            if let Some(fields) = engine.drafts.remove(type_name) {
                engine.fields.insert(type_name.to_owned(), fields);
            }
        }
        self.judge(cx, type_name, live_version, outcome, |_, _, _| {})
    }

    fn list_schemas(&mut self, cx: &Cx<'_>) -> Done {
        let names = json!(self.engines[cx.at].host.store().type_names());
        self.judge(cx, "", 0, Ok(names), |checks, expected, actual| {
            let mut defined: Vec<Value> = expected
                .as_array()
                .into_iter()
                .flatten()
                .filter_map(|schema| schema.get("name").cloned())
                .collect();
            defined.sort_by_key(|name| name.as_str().map(str::to_owned));
            checks.eq("schemas", &Value::Array(defined), actual);
        })
    }

    fn live(&mut self, cx: &Cx<'_>) -> Done {
        let Some(type_name) = cx.args.first().and_then(Value::as_str) else {
            return Done::Skipped("a read that names no schema".to_owned());
        };
        let store = self.engines[cx.at].host.store();
        let live = store
            .live(type_name)
            .map_or(Value::Null, |spec| json!({ "behaviors": uses_of(spec) }));
        let name = type_name.to_owned();
        self.judge(
            cx,
            type_name,
            0,
            Ok(live),
            move |checks, expected, actual| {
                if expected.is_null() || actual.is_null() {
                    checks.eq("live", expected, actual);
                    return;
                }
                let recorded = expected
                    .get("document")
                    .and_then(|document| shape::read_document(document).ok())
                    .filter(|defined| defined.spec.name == name)
                    .map_or(Value::Null, |defined| uses_of(&defined.spec));
                checks.eq("document.behaviors", &recorded, &actual["behaviors"]);
            },
        )
    }

    fn behaviors(&mut self, cx: &Cx<'_>) -> Done {
        let Some(type_name) = cx.args.first().and_then(Value::as_str) else {
            return Done::Skipped("a read that names no schema".to_owned());
        };
        let host = &self.engines[cx.at].host;
        let actual = host
            .store()
            .live(type_name)
            .map_or(Value::Array(Vec::new()), |spec| {
                Value::Array(
                    spec.uses
                        .iter()
                        .map(|entry| {
                            json!({
                                "name": entry.behavior,
                                "config": entry.config_or_empty(),
                                "declaration": host
                                    .core()
                                    .declaration(&entry.behavior)
                                    .map_or(Value::Null, declared),
                            })
                        })
                        .collect(),
                )
            });
        self.judge(cx, type_name, 0, Ok(actual), |checks, expected, actual| {
            let recorded: Vec<Value> = expected
                .as_array()
                .into_iter()
                .flatten()
                .map(|entry| {
                    json!({
                        "name": entry.get("name").cloned().unwrap_or(Value::Null),
                        "config": entry.get("config").cloned().unwrap_or_else(|| json!({})),
                        "declaration": entry
                            .get("declaration")
                            .map_or(Value::Null, recorded_declaration),
                    })
                })
                .collect();
            checks.eq("behaviors", &Value::Array(recorded), actual);
        })
    }

    // ---- instances ----

    fn create(&mut self, cx: &Cx<'_>) -> Done {
        let (Some(type_name), Some(data)) = (
            cx.args.first().and_then(Value::as_str),
            cx.args.get(1).and_then(Value::as_object),
        ) else {
            return Done::Skipped("a create without a schema and an object of fields".to_owned());
        };
        let options = cx
            .args
            .get(2)
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if let Some(reason) = unmapped_options(&options, &["id", "behaviors"]) {
            return Done::Skipped(reason);
        }
        if let Some(done) = self.store_refuses(cx, type_name, data, true) {
            return done;
        }
        let action = Action::Create {
            id: options.get("id").and_then(Value::as_str).map(str::to_owned),
            own: data.clone(),
            params: options
                .get("behaviors")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default(),
        };
        self.write(cx, type_name, action)
    }

    fn update(&mut self, cx: &Cx<'_>) -> Done {
        let (Some(type_name), Some(key), Some(patch)) = (
            cx.args.first().and_then(Value::as_str),
            cx.args.get(1).and_then(Value::as_str),
            cx.args.get(2).and_then(Value::as_object),
        ) else {
            return Done::Skipped("an update without a schema, an id and a patch".to_owned());
        };
        let options = cx
            .args
            .get(3)
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if let Some(reason) = unmapped_options(&options, &[]) {
            return Done::Skipped(reason);
        }
        if let Some(done) = self.store_refuses(cx, type_name, patch, false) {
            return done;
        }
        let action = Action::Update {
            key: key.to_owned(),
            patch: patch.clone(),
        };
        self.write(cx, type_name, action)
    }

    // store_refuses skips a create or an update the store refuses for its
    // own fields, which the recording refused: own-field validation is the
    // store's, not the core's, and the write never reaches the core.
    fn store_refuses(
        &self,
        cx: &Cx<'_>,
        type_name: &str,
        data: &Map<String, Value>,
        create: bool,
    ) -> Option<Done> {
        let Expected::Error(_) = cx.expected else {
            return None;
        };
        let fields = self.engines[cx.at].fields.get(type_name)?;
        let problems = shape::own_field_problems(type_name, fields, data, create);
        (!problems.is_empty()).then(|| {
            Done::Skipped(format!(
                "own-field validation is the store's, and it refuses this write: {}",
                problems.join("; ")
            ))
        })
    }

    // write runs a create or an update and compares the record it leaves.
    fn write(&mut self, cx: &Cx<'_>, type_name: &str, action: Action) -> Done {
        let live_version = self.engines[cx.at].host.store().live_version(type_name);
        let command = Command::new(cx.caller.clone(), cx.now, action);
        let outcome = self.execute(cx.at, type_name, command);
        let host = &self.engines[cx.at].host;
        let outcome = outcome.map(|applied| record_of(host, type_name, &applied.key));
        self.judge(cx, type_name, live_version, outcome, compare_record)
    }

    // execute runs a command on an engine's host, and records it with what
    // the host answered and the row it leaves when a transcript is written.
    fn execute(
        &mut self,
        at: usize,
        type_name: &str,
        command: Command,
    ) -> Result<Applied, Refusal> {
        let engine = &mut self.engines[at];
        let command = engine.host.prepare(command);
        let live = engine.host.store().live(type_name).cloned();
        let outcome = engine.host.execute(type_name, command.clone());
        if let Some(recorded) = &mut self.recorded {
            let row = command
                .action
                .key()
                .and_then(|key| engine.host.store().row(type_name, key));
            recorded.command(
                &self.current,
                &engine.path,
                type_name,
                live.as_ref(),
                command.clone(),
                &outcome,
                row,
            );
        }
        outcome
    }

    fn delete(&mut self, cx: &Cx<'_>) -> Done {
        let (Some(type_name), Some(key)) = (
            cx.args.first().and_then(Value::as_str),
            cx.args.get(1).and_then(Value::as_str),
        ) else {
            return Done::Skipped("a delete without a schema and an id".to_owned());
        };
        let live_version = self.engines[cx.at].host.store().live_version(type_name);
        let command = Command::new(
            cx.caller.clone(),
            cx.now,
            Action::Delete {
                key: key.to_owned(),
            },
        );
        let outcome = match self.execute(cx.at, type_name, command) {
            Ok(_) => Ok(Value::Bool(true)),
            // The engine answers false for a row that is not there.
            Err(refusal) if refusal.code == Code::NotFound => Ok(Value::Bool(false)),
            Err(refusal) => Err(refusal),
        };
        self.judge(cx, type_name, live_version, outcome, |checks, e, a| {
            checks.eq("deleted", e, a);
        })
    }

    fn invoke(&mut self, cx: &Cx<'_>) -> Done {
        let (Some(type_name), Some(key), Some(operation)) = (
            cx.args.first().and_then(Value::as_str),
            cx.args.get(1).and_then(Value::as_str),
            cx.args.get(2).and_then(Value::as_str),
        ) else {
            return Done::Skipped("an invoke without a schema, an id and an operation".to_owned());
        };
        let params = cx.args.get(3).cloned().unwrap_or_else(|| json!({}));
        let options = cx
            .args
            .get(4)
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if let Some(reason) = unmapped_options(&options, &[]) {
            return Done::Skipped(reason);
        }
        let engine = &mut self.engines[cx.at];
        let live_version = engine.host.store().live_version(type_name);
        // The engine's invoke names an operation, not its use: the use is
        // the one whose behavior declares it.
        let uses: Vec<String> =
            engine
                .host
                .store()
                .live(type_name)
                .map(|spec| {
                    spec.uses
                        .iter()
                        .filter(|entry| {
                            engine.host.core().declaration(&entry.behavior).is_some_and(
                                |declaration| declaration.find_operation(operation).is_some(),
                            )
                        })
                        .map(|entry| entry.id().to_owned())
                        .collect()
                })
                .unwrap_or_default();
        // A test behavior's operation whose fixture tests the engine's
        // store is not run.
        let unported = uses.first().and_then(|use_id| {
            let spec = engine.host.store().live(type_name)?;
            let (_, entry) = spec.use_of(use_id)?;
            self.test_behaviors
                .contains(&entry.behavior)
                .then(|| fixtures::unported_operation(self.file, &entry.behavior, operation))
                .flatten()
        });
        if let Some(reason) = unported {
            return Done::Skipped(reason.to_owned());
        }
        let outcome = match uses.as_slice() {
            [] => Err(Refusal::not_found(format!(
                "{type_name} has no operation {operation}"
            ))),
            [use_id] => {
                let command = Command::new(
                    cx.caller.clone(),
                    cx.now,
                    Action::Operation {
                        key: key.to_owned(),
                        use_id: use_id.clone(),
                        operation: operation.to_owned(),
                        params,
                    },
                );
                self.execute(cx.at, type_name, command)
                    .map(|applied| applied.result().clone())
            }
            _ => {
                return Done::Skipped(format!(
                    "more than one use of {type_name} declares {operation}, and the engine's invoke names no use"
                ))
            }
        };
        self.judge(cx, type_name, live_version, outcome, |checks, e, a| {
            checks.eq("result", e, a);
        })
    }

    fn get(&mut self, cx: &Cx<'_>) -> Done {
        let (Some(type_name), Some(key)) = (
            cx.args.first().and_then(Value::as_str),
            cx.args.get(1).and_then(Value::as_str),
        ) else {
            return Done::Skipped("a get without a schema and an id".to_owned());
        };
        let host = &self.engines[cx.at].host;
        let found = record_of(host, type_name, key);
        self.judge(cx, type_name, 0, Ok(found), compare_record)
    }

    fn list(&mut self, cx: &Cx<'_>) -> Done {
        let Some(type_name) = cx.args.first().and_then(Value::as_str) else {
            return Done::Skipped("a list that names no schema".to_owned());
        };
        let options = cx
            .args
            .get(1)
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if let Some(reason) = unmapped_options(&options, &["where"]) {
            return Done::Skipped(reason);
        }
        let filters = options
            .get("where")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if filters
            .values()
            .any(|value| value.is_object() || value.is_array())
        {
            return Done::Skipped(
                "a filter other than equality, which the reference host does not take".to_owned(),
            );
        }
        let host = &self.engines[cx.at].host;
        let store = host.store();
        let spec = store.live(type_name);
        let items: Vec<Value> = store
            .rows(type_name)
            .into_iter()
            .filter(|(_, row)| {
                filters.iter().all(|(name, wanted)| {
                    let owned = name.split_once('.').filter(|(use_id, _)| {
                        spec.is_some_and(|spec| spec.use_of(use_id).is_some())
                    });
                    let value = match owned {
                        Some((use_id, field)) => {
                            row.owned.get(use_id).and_then(|fields| fields.get(field))
                        }
                        None => row.own.get(name),
                    };
                    value.unwrap_or(&Value::Null) == wanted
                })
            })
            .filter_map(|(key, _)| {
                spec.zip(store.row(type_name, key))
                    .map(|(spec, row)| shape::record(host.core(), spec, key, row))
            })
            .collect();
        self.judge(
            cx,
            type_name,
            0,
            Ok(json!({ "items": items })),
            |checks, expected, actual| {
                let recorded = items_of(expected);
                let found = items_of(actual);
                if recorded.len() != found.len() {
                    checks.eq(
                        "items.id",
                        &Value::Array(recorded.iter().map(|item| item["id"].clone()).collect()),
                        &Value::Array(found.iter().map(|item| item["id"].clone()).collect()),
                    );
                    return;
                }
                for (recorded, found) in recorded.iter().zip(&found) {
                    compare_record(checks, recorded, found);
                }
            },
        )
    }

    fn events(&mut self, cx: &Cx<'_>) -> Done {
        let options = cx
            .args
            .first()
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if let Some(reason) = unmapped_options(&options, &["schema", "instanceId"]) {
            return Done::Skipped(reason);
        }
        let schema = options.get("schema").and_then(Value::as_str);
        let instance = options.get("instanceId").and_then(Value::as_str);
        let store = self.engines[cx.at].host.store();
        let events: Vec<Value> = store
            .log()
            .iter()
            .filter(|logged| {
                schema.is_none_or(|schema| logged.type_name == schema)
                    && instance.is_none_or(|key| logged.key == key)
            })
            .map(|logged| shape::event(store.live(&logged.type_name), logged))
            .collect();
        self.judge(
            cx,
            schema.unwrap_or_default(),
            0,
            Ok(json!({ "events": events })),
            compare_events,
        )
    }
}

// The step a call handler maps: the engine, the caller, the time, the
// arguments with their symbols written, and what the step expects.
struct Cx<'c> {
    at: usize,
    caller: &'c Caller,
    now: i64,
    args: &'c [Value],
    expected: &'c Expected<'c>,
}

fn call_of(step: &Value) -> &str {
    step.get("call").and_then(Value::as_str).unwrap_or("?")
}

// holds_unjsonable says whether an argument holds a value JSON cannot
// hold, which the recording writes as an object of one `$` member.
fn holds_unjsonable(value: &Value) -> bool {
    match value {
        Value::Object(members) => {
            (members.len() == 1 && members.keys().all(|key| key.starts_with('$')))
                || members.values().any(holds_unjsonable)
        }
        Value::Array(items) => items.iter().any(holds_unjsonable),
        _ => false,
    }
}

// unmapped_options names an option of a call the host does not take; the
// namespace is taken when it is the corpus's own.
fn unmapped_options(options: &Map<String, Value>, taken: &[&str]) -> Option<String> {
    let unmapped: Vec<&str> = options
        .iter()
        .filter(|(key, value)| {
            !taken.contains(&key.as_str())
                && !(key.as_str() == "namespace" && value.as_str() == Some(shape::NAMESPACE))
        })
        .map(|(key, _)| key.as_str())
        .collect();
    (!unmapped.is_empty()).then(|| {
        format!(
            "options the reference host does not take: {}",
            unmapped.join(", ")
        )
    })
}

// is_document_error says whether a define's refusal is the schema
// document's own validation, which names no behavior.
fn is_document_error(error: &Value) -> bool {
    error.get("class").and_then(Value::as_str) == Some("SchemaDocumentError")
        && !issues_of(error).iter().any(|issue| {
            issue
                .get("path")
                .and_then(Value::as_str)
                .is_some_and(|path| path.contains("/behaviors"))
        })
}

fn issues_of(error: &Value) -> Vec<Value> {
    error
        .get("details")
        .and_then(|details| details.get("issues"))
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default()
}

// status_of is the HTTP status of an error the recording holds, from its
// code; a defect (no code, `BehaviorError`) and an error that is not an
// `EngineError` (no code, `Error`) answer 500.
fn status_of(error: &Value) -> Option<u16> {
    let code = match error.get("code").and_then(Value::as_str) {
        Some(code) => code,
        None => {
            return match error.get("class").and_then(Value::as_str) {
                Some("BehaviorError") => Some(Code::BehaviorError.status()),
                Some("Error") => Some(Code::Internal.status()),
                _ => None,
            }
        }
    };
    let known = [
        Code::InvalidSchema,
        Code::IncompatibleChange,
        Code::NotFound,
        Code::InvalidArgument,
        Code::Forbidden,
        Code::InvalidInstance,
        Code::Conflict,
        Code::Vetoed,
        Code::Busy,
        Code::BehaviorError,
        Code::Internal,
    ];
    known
        .iter()
        .find(|known| known.as_str() == code)
        .map(|known| known.status())
}

// compare_error compares a refusal, written as the engine's error, with
// the recorded one, whole: its class, code, status, message and details.
fn compare_error(
    checks: &mut Checks<'_>,
    expected: &Value,
    refusal: &Refusal,
    type_name: &str,
    live_version: usize,
) {
    let actual = shape::engine_error(refusal, type_name, live_version);
    let member = |error: &Value, key: &str| error.get(key).cloned().unwrap_or(Value::Null);
    checks.eq(
        "error.class",
        &member(expected, "class"),
        &member(&actual, "class"),
    );
    checks.eq(
        "error.code",
        &member(expected, "code"),
        &member(&actual, "code"),
    );
    if let Some(status) = status_of(expected) {
        checks.eq(
            "error.status",
            &Value::from(status),
            &Value::from(refusal.status),
        );
    }
    checks.eq(
        "error.details",
        &member(expected, "details"),
        &member(&actual, "details"),
    );
    checks.eq(
        "error.message",
        &member(expected, "message"),
        &member(&actual, "message"),
    );
}

// compare_record compares a record by its id, its own fields where the
// recording holds them, and the fields its behaviors own; a missing record
// is null.
fn compare_record(checks: &mut Checks<'_>, expected: &Value, actual: &Value) {
    if expected.is_null() || actual.is_null() {
        checks.eq("record", expected, actual);
        return;
    }
    checks.eq("id", &expected["id"], &actual["id"]);
    if let Some(data) = expected.get("data") {
        checks.eq("data", data, &actual["data"]);
    }
    checks.eq(
        "behaviors",
        expected.get("behaviors").unwrap_or(&json!({})),
        &actual["behaviors"],
    );
}

fn items_of(list: &Value) -> Vec<Value> {
    list.get("items")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default()
}

// compare_events compares a row's events by kind and their changes: a
// create's own and owned fields, an operation's behavior, name and
// parameters, and the own (`data`) and owned (`behaviors`) fields a patch
// set, each where the recording holds the change.
fn compare_events(checks: &mut Checks<'_>, expected: &Value, actual: &Value) {
    let events = |page: &Value| {
        page.get("events")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default()
    };
    let (recorded, found) = (events(expected), events(actual));
    let kinds = |list: &[Value]| Value::Array(list.iter().map(|e| e["kind"].clone()).collect());
    if recorded.len() != found.len() {
        checks.eq("events.kind", &kinds(&recorded), &kinds(&found));
        return;
    }
    let empty = json!({});
    for (index, (recorded, found)) in recorded.iter().zip(&found).enumerate() {
        let at = |part: &str| format!("events[{index}].{part}");
        checks.eq(&at("kind"), &recorded["kind"], &found["kind"]);
        let (change, mine) = (&recorded["change"], &found["change"]);
        if change.is_null() {
            continue;
        }
        let parts: &[&str] = match recorded["kind"].as_str() {
            Some("create") => &["data", "behaviors"],
            Some("operation") => &["behavior", "operation", "params"],
            _ => &[],
        };
        for part in parts {
            checks.eq(
                &at(&format!("change.{part}")),
                change.get(*part).unwrap_or(&empty),
                mine.get(*part).unwrap_or(&empty),
            );
        }
        if matches!(recorded["kind"].as_str(), Some("operation" | "update")) {
            for part in ["data", "behaviors"] {
                checks.eq(
                    &at(&format!("change.patch.{part}")),
                    change["patch"].get(part).unwrap_or(&empty),
                    mine["patch"].get(part).unwrap_or(&empty),
                );
            }
        }
    }
}

// record_of is a row as the engine's record, or null.
fn record_of(host: &MemoryHost, type_name: &str, key: &str) -> Value {
    let store = host.store();
    match (store.live(type_name), store.row(type_name, key)) {
        (Some(spec), Some(row)) => shape::record(host.core(), spec, key, row),
        _ => Value::Null,
    }
}

// uses_of lists a type's uses as the engine's document lists its
// behaviors: each one's name and config.
fn uses_of(spec: &TypeSpec) -> Value {
    Value::Array(
        spec.uses
            .iter()
            .map(|entry| json!({ "name": entry.behavior, "config": entry.config_or_empty() }))
            .collect(),
    )
}

// declared summarizes a core declaration as this replayer compares one:
// its name, its fields, its operations and whether each writes, and its
// veto codes, each list sorted.
fn declared(declaration: &Declaration) -> Value {
    let mut fields = declaration.fields.clone();
    fields.sort();
    let mut operations: Vec<(String, bool)> = declaration
        .operations
        .iter()
        .map(|operation| (operation.name.clone(), operation.writes))
        .collect();
    operations.sort();
    let mut vetoes = declaration.vetoes.clone();
    vetoes.sort();
    summary(&declaration.name, fields, operations, vetoes)
}

// recorded_declaration summarizes the engine's declaration JSON the same
// way.
fn recorded_declaration(declaration: &Value) -> Value {
    let names = |key: &str, member: &str| -> Vec<String> {
        let mut names: Vec<String> = declaration
            .get(key)
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter_map(|entry| entry.get(member).and_then(Value::as_str))
            .map(str::to_owned)
            .collect();
        names.sort();
        names
    };
    let mut operations: Vec<(String, bool)> = declaration
        .get("operations")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|operation| {
            Some((
                operation.get("name")?.as_str()?.to_owned(),
                operation.get("writes").and_then(Value::as_bool) == Some(true),
            ))
        })
        .collect();
    operations.sort();
    summary(
        declaration
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or_default(),
        names("fields", "name"),
        operations,
        names("vetoes", "code"),
    )
}

fn summary(
    name: &str,
    fields: Vec<String>,
    operations: Vec<(String, bool)>,
    vetoes: Vec<String>,
) -> Value {
    json!({
        "name": name,
        "fields": fields,
        "operations": operations
            .into_iter()
            .map(|(name, writes)| json!({ "name": name, "writes": writes }))
            .collect::<Vec<_>>(),
        "vetoes": vetoes,
    })
}
