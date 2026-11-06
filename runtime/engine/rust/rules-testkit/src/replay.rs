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
//!   change check again and publishes. The engine's define refusals name the
//!   document's type where its other messages name the schema, so a refused
//!   define of a document that names them apart (schema `invoices`, type
//!   `Invoice`) is checked again under its type's name, which keeps
//!   nothing. A recorded type that lists a behavior whose engine read an
//!   instance's creator from its row ([`CREATOR_READERS`]: Approvals'
//!   excludeCreator) and declares no own field createdBy gets one, which a
//!   create sets to its caller's subject unless it gives one, as the engine
//!   kept the creator beside the instance, and which no comparison with the
//!   recording reads; a hand-written file declares it where it wants one.
//! - `instances.create`, `update`, `delete` and `invoke` run the core's
//!   commands, an update's, a delete's and an invoke's `preconditions`
//!   option the command's preconditions (D16, D104); `get`, `list` and
//!   `events.read` read the host. A write's
//!   own date-time fields are stored in their canonical form, as the
//!   engine's parse of the scalar stores them. A record is compared by its
//!   id, its own fields (`data`) and its `behaviors`, the fields each
//!   behavior owns and those it computes on a read
//!   ([`superschematic_rules::Core::read_fields`], Timers' `deadlines`); an
//!   operation by its result; an event by its kind and its change: a
//!   create's fields, an operation's behavior, name and parameters, and the
//!   own and owned fields a write set, the fields a behavior computes on a
//!   read left out of the recorded change, since an outbox carries none.
//! - `runner.runDue` runs the changes the hearing types' triggers queued,
//!   each a change command of its own, then each deadline armed and due at
//!   the step's time, earliest first, then the changes again, as the
//!   engine's runner (the `open` step's `runner.principal`), as the API's
//!   worker runs a table's change queue and deadline queue (D103); the
//!   engine's counters are the store's and are not compared, but a deadline
//!   the core refuses for anything but a veto, which the engine's sweep
//!   passes over, and a change it refuses, are mismatches, unless the
//!   recording's run failed. A runDue whose recording failed stops the
//!   scenario after it: the engine halted the subscription, which heard
//!   nothing after it, where a table's change queue fails the one message
//!   and hears the next. A deadline command's change-log entry is compared
//!   as the engine's sweep event of the operation the deadline's handler
//!   calls on its own use ([`SWEEP_OPERATIONS`]).
//! - The engine's Links is a to-one relation on a table (D103): each link
//!   with one target is an own field `<link>Id` holding the target's key
//!   ([`shape::read_document`]), a create's `behaviors.Links.<link>` sets
//!   it, `link` and `unlink` are updates of it answered as Links answered,
//!   and a record shows it as the engine's `Links.targets`. A link that
//!   holds a list of targets or pins one stops the scenario, and Links' own
//!   validation, a refusal that names Links, and a define refused of a type
//!   that lists Links (its paths count Links among the uses) are skipped.
//! - A config member only the engine's runner read ([`RUNNER_MEMBERS`]:
//!   Lease's sweepMs) is left out of a recording's configs, since a table's
//!   deadlines replace the sweep and its behavior refuses the member; a
//!   behavior a recording composes only incidentally ([`INCIDENTAL`]) is
//!   left out of its types and of what its steps expect, and a step that
//!   calls one of its operations, or whose refusal names it, is skipped. A
//!   row that predates a use a publish added is compared without the use's
//!   owned fields the core holds no value of, where the engine's readers
//!   gave their defaults. A define the engine refused because it was opened
//!   without the package that implements a behavior the core runs is not
//!   compared.
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
//! describe and tools documents, the runner's status. A step that names a
//! namespace other than the corpus's own stops the scenario there: the
//! host keeps one, as a build-time project's tables are its database's. A
//! publish that changes a config in a way the engine's afterConfigChange
//! rewrote rows for, on a type with rows, stops it too
//! ([`PUBLISH_REWRITES`]): the core writes no row at a publish, and a row's
//! state reconciles at its next write (D103). A write the store refuses for its own
//! fields is not run: own-field validation is the table's. A test's raw
//! SQL write of behaviors' columns in the one form [`shape::raw_owned_write`]
//! reads is applied to the row past the adapter, and the step, whose result
//! is SQLite's, is skipped; any other SQL of the test's own stops the
//! scenario there, since the steps after it read what it wrote. A step that
//! cannot be mapped (a call outside this surface, a behavior written in
//! TypeScript that no fixture ports, a fixture's operation that tests the
//! store) is skipped with its reason. So is an engine opened with options
//! no table has a counterpart of: a runner's depth limit, and the value
//! store. A define the recording took and the core refuses as waiting for a
//! later step of D103's order of the work stops the scenario there with the
//! core's reason.
//!
//! Every step the replayer checks is held to the events its commands wrote
//! (D104), the outbox entries the reference host gained while it ran, in
//! order, each whole (the row's schema and id, the use, the kind, the
//! command's time and the payload) against the step's `events`; a step
//! without the member wrote none, and no recording's step emits one.
//!
//! [`crate::transcript`] writes a recording out as a transcript through this
//! same mapping: the replayer records each command, check and raw write it
//! made on the host, with what the host answered, as it makes it.

use std::collections::{BTreeMap, BTreeSet};
use std::fmt;
use std::path::{Path, PathBuf};

use serde_json::{json, Map, Value};

use superschematic_engine_testkit::{load, ScenarioFile};
use superschematic_rules::{
    Action, Caller, Code, Command, Core, Declaration, FieldSpec, Refusal, Row, TypeSpec,
};

use crate::fixtures;
use crate::host::{Applied, Logged, MemoryHost, Store, MAX_CHANGES};
use crate::shape::{self, FieldDef};
use crate::symbols::Symbols;
use crate::transcript::{Recorded, TranscriptClock};

/// The option of an update, a delete and an invoke that carries the
/// caller's preconditions (D16), which the replay maps onto the command
/// (D104).
const PRECONDITIONS: &str = "preconditions";

/// Where the wall clock of a replay starts: an instant of the recording's
/// span (thirteen digits), far from any fixed clock's.
const WALL_START: i64 = 1_767_225_600_000;

/// The parameters a deadline's handler calls its operation with, from the
/// deadline's name.
pub type SweepParams = fn(&str) -> Value;

/// The operation a deadline's handler calls on its own use when it falls
/// due, by its behavior, and the parameters it calls it with from the
/// deadline's name: the engine's sweep invoked it, so its log holds the
/// deadline's change as that operation's event.
pub const SWEEP_OPERATIONS: [(&str, &str, SweepParams); 3] = [
    ("Timers", "fireTimer", |name| json!({ "deadline": name })),
    ("Lease", "expire", |_| json!({})),
    ("Presence", "miss", |_| json!({})),
];

/// The config members of a behavior only the engine's runner read, by
/// behavior: how often it swept. A table's behavior refuses each, since its
/// deadlines replace the runner's sweep (D103, D104), so in the engine's
/// recordings alone the replay leaves each out of a recorded config before
/// it defines the type, and out of every recorded config it compares.
pub const RUNNER_MEMBERS: [(&str, &str); 2] = [("Lease", "sweepMs"), ("Presence", "sweepMs")];

/// The behaviors a recording composes only incidentally, by file, each
/// with why: the replay leaves the behavior's use out of each type it
/// defines, its members out of every record and event it compares (an
/// event of its operation left out whole), and skips each step that calls
/// one of its operations or whose recorded refusal names it.
pub const INCIDENTAL: [(&str, &str, &str); 2] = [
    ("workqueue-lease", "Comments", COMMENTS_INCIDENTAL),
    ("workqueue-budget", "Comments", BUDGET_COMMENTS_INCIDENTAL),
];

const COMMENTS_INCIDENTAL: &str = "Comments, which the core does not run (D103, step 4), is composed beside Lease only as another behavior's writing operation Lease's guard holds: its steps, and a refusal that names it, are not compared; Workflow's transition holds the guard's rule on another behavior's operation";

const BUDGET_COMMENTS_INCIDENTAL: &str = "Comments, which the core does not run (D103, step 4), is composed beside Budget only as another behavior of the scope type: its use and its members are not compared";

/// Why an events.read that holds a write one row's operation or deadline
/// made of another row is not compared.
const EVENTS_OF_CALLS: &str = "a read of the engine's event log that holds a write one row's operation or deadline made of another row through that row's operation, which the engine logged as the operation's own event and a table's change set logs under the command that wrote it (D103)";

/// Why a scenario stops at a setLimit of a meter whose limit an own field
/// holds.
const LIMIT_FIELD_SET_LIMIT: &str = "setLimit of a meter with limitField writes the field through the engine's update(), and the core writes no own field from an operation (D103): on a table the limit is the field's, set by an update (D104)";

/// Why a scenario stops at a define the engine refused for another
/// schema's state.
const OTHER_SCHEMA_DEFINE: &str = "a define check that reads another schema, which the core's define check cannot make: the build holds a table's releaseLeases to its database (D104)";

/// Why a define the engine refused for the behavior's package it was
/// opened without is not compared.
const NO_IMPLEMENTATION: &str =
    "the engine opened without the package that implements the behavior, which the core runs (D104)";

/// Whether a config change, from the published config to the new one,
/// either absent, is one a behavior's engine afterConfigChange rewrote
/// rows for.
pub type Rewrites = fn(Option<&Value>, Option<&Value>) -> bool;

/// The behaviors whose engine's afterConfigChange rewrote every row's
/// state at a publish, each with its [`Rewrites`] and why a scenario stops
/// at a publish it names.
pub const PUBLISH_REWRITES: [(&str, Rewrites, &str); 2] = [
    ("Timers", timers_rewrites, TIMERS_REWRITE),
    ("Approvals", approvals_rewrites, APPROVALS_REWRITE),
];

/// Why a scenario stops at a publish whose Timers change the engine
/// rewrote rows for.
const TIMERS_REWRITE: &str = "the engine's afterConfigChange rewrites every row's deadlines at the publish, and the core writes no row at a publish (a build-time project's config change is a build, and each row's deadlines reconcile at its next write through the core, D103), so the steps after it read state the core does not make";

/// Why a scenario stops at a publish whose Approvals change the engine
/// rewrote rows for.
const APPROVALS_REWRITE: &str = "the engine's afterConfigChange consumed a removed gate's sign-offs at the publish, and the core writes no row at a publish: a row's sign-offs of a gate the config no longer names are consumed at its next write through the core (D104), so the steps after it read state the core does not make";

/// The behaviors whose engine read an instance's creator from its row
/// (`created_by`), which a table holds in the own field `createdBy`
/// (D87): a recorded type that lists one and declares no such field gets
/// one in the type the host runs, which each create sets to its caller's
/// subject unless it gives one, as the engine kept an instance's creator
/// beside it, and which no comparison with the recording reads.
pub const CREATOR_READERS: [&str; 1] = ["Approvals"];

/// The own field a table holds its row's creator in (D87).
const CREATED_BY: &str = "createdBy";

/// The behavior whose moves the engine logged with their cause.
const REACTIONS: &str = "Reactions";

/// The class of a define's refusal of a type's uses and configs, which the
/// engine wrote as its schema document's validation.
const SCHEMA_DOCUMENT_ERROR: &str = "SchemaDocumentError";

/// Why a step whose recorded refusal names Links, or a refused define of a
/// type that lists it, is not compared.
const LINKS_REFUSAL: &str = "the engine's refusal names its Links; a table's link is a relation, which the core names so (D103)";

/// Why a step Links' own validation refuses is not compared.
const LINKS_OWN: &str =
    "Links' own validation, which a relation's foreign key holds on a table (D103)";

/// Why a define the engine's requires check refuses is not compared.
const REQUIRES_REFUSAL: &str = "the engine's check that a type lists each behavior another requires; the core holds each rule that reads a Workflow to the type's (D103)";

/// Why an events.read whose log the core's cannot map is not compared.
const EVENTS_UNMAPPED: &str = "a read of the engine's event log that holds what a table's writes do not: a Links operation, which a table writes as an update of its relation's column, or a move a rule made, which a change command writes as an update of the row's owned fields (D103)";

/// Why an events.read of rows a heard change wrote is not compared.
const HEARD_WRITE: &str = "a read of the change log where a change command wrote a row it heard, a write the engine made at another event if at all: a table's change queue runs a write's rules after the commit, Timers' reconcile under the live version among them, where the engine's ran at the write or at the deadline's fire (D103)";

/// Why an engine opened with a runner's depth limit is not mapped.
const MAX_DEPTH_REASON: &str = "the engine's runner stops a chain at its depth limit; a table's change queue hears each move as any write (D103)";

/// Why an engine opened with a value store is not mapped.
const VALUES_REASON: &str = "its engine keeps large values in a value store, which a table has no counterpart of: a column holds the value (D103)";

/// Why a scenario stops after a runDue whose recording failed.
const HALTED_RUN: &str = "the engine halts a subscription that fails, which hears nothing after it; a table's change queue fails the one message and hears the next (D103)";

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
    replay_recording(file, make_core, None).0
}

/// Replays a file as [`replay`] does and, when `record` is set, records
/// for each scenario, in order, what a transcript holds of it: the
/// commands, checks and raw writes the replay made on the host and what
/// the host answered ([`Recorded`]), each command's row version with them
/// when it is `Some(true)`, as a hand-written file's transcript holds it.
pub(crate) fn replay_recording(
    file: &ScenarioFile,
    make_core: &dyn Fn() -> Core,
    record: Option<bool>,
) -> (Report, Vec<Recorded>) {
    let runs = make_core().names();
    let recording = is_recording(file);
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
        run.recording = recording;
        run.recorded = record.map(Recorded::new);
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

// is_recording says whether a file is one of the engine's recordings
// (`runtime/engine/testdata/scenarios`), by its name, rather than a
// hand-written one (D104), which no engine answered.
fn is_recording(file: &ScenarioFile) -> bool {
    scenario_path(&file.name).is_file()
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
// ports, or it composes a behavior the core does not run, Links aside,
// whose links are relations.
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
    // Links is mapped onto relations, which no behavior runs, and an
    // incidental behavior of the file is left out (INCIDENTAL).
    let missing: Vec<String> = names(scenario, "behaviors")
        .into_iter()
        .filter(|name| {
            !tests.contains(name)
                && !runs.contains(name)
                && name != shape::LINKS
                && incidental(file, name).is_none()
        })
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

// incidental is why a file composes a behavior only incidentally
// (INCIDENTAL), or None.
fn incidental(file: &str, behavior: &str) -> Option<&'static str> {
    INCIDENTAL
        .iter()
        .find(|(known, name, _)| *known == file && *name == behavior)
        .map(|(_, _, reason)| *reason)
}

// incidental_operations is the operations of a file's incidental behaviors,
// each with its behavior's reason, from the engine's declarations, which
// the core does not register.
fn incidental_operations(file: &str) -> Vec<(String, &'static str)> {
    INCIDENTAL
        .iter()
        .filter(|(known, _, _)| *known == file)
        .flat_map(|(_, behavior, reason)| {
            let path = Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../behaviors/declarations")
                .join(format!("{behavior}.behavior.json"));
            let file: Value = std::fs::read_to_string(path)
                .ok()
                .and_then(|text| serde_json::from_str(&text).ok())
                .unwrap_or(Value::Null);
            file.get("operations")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .filter_map(|operation| operation.get("name").and_then(Value::as_str))
                .map(|name| (name.to_owned(), *reason))
                .collect::<Vec<_>>()
        })
        .collect()
}

// without_runner_members leaves the members only the engine's runner read
// (RUNNER_MEMBERS) out of each use's config of a type.
fn without_runner_members(spec: &mut TypeSpec) {
    for entry in &mut spec.uses {
        let Some(Value::Object(config)) = &mut entry.config else {
            continue;
        };
        for (behavior, member) in RUNNER_MEMBERS {
            if entry.behavior == behavior {
                config.remove(member);
            }
        }
    }
}

// without_incidental is a step with a file's incidental behaviors left out
// of what it expects (INCIDENTAL): a record's or a listed item's owned
// fields, an event of an incidental behavior's operation whole and the
// fields it set in any other, and an entry of a type's behaviors.
fn without_incidental(file: &str, step: &Value) -> Value {
    let behaviors: Vec<&str> = INCIDENTAL
        .iter()
        .filter(|(known, _, _)| *known == file)
        .map(|(_, behavior, _)| *behavior)
        .collect();
    let mut step = step.clone();
    if behaviors.is_empty() {
        return step;
    }
    let Some(expect) = step.get_mut("expect") else {
        return step;
    };
    let strip = |holder: &mut Value| {
        if let Some(owned) = holder.get_mut("behaviors").and_then(Value::as_object_mut) {
            for behavior in &behaviors {
                owned.remove(*behavior);
            }
        }
    };
    strip(expect);
    for item in expect
        .get_mut("items")
        .and_then(Value::as_array_mut)
        .into_iter()
        .flatten()
    {
        strip(item);
    }
    if let Some(events) = expect.get_mut("events").and_then(Value::as_array_mut) {
        events.retain(|event| {
            !event["change"]
                .get("behavior")
                .and_then(Value::as_str)
                .is_some_and(|behavior| behaviors.contains(&behavior))
        });
        for event in events {
            if let Some(change) = event.get_mut("change") {
                strip(change);
                if let Some(patch) = change.get_mut("patch") {
                    strip(patch);
                }
            }
        }
    }
    if let Some(listed) = expect.as_array_mut() {
        listed.retain(|entry| {
            !entry
                .get("name")
                .and_then(Value::as_str)
                .is_some_and(|name| behaviors.contains(&name))
        });
    }
    step
}

// without_incidental_uses leaves a file's incidental behaviors' uses out
// of a type (INCIDENTAL).
fn without_incidental_uses(file: &str, spec: &mut TypeSpec) {
    spec.uses
        .retain(|entry| incidental(file, &entry.behavior).is_none());
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
    /// The types the replay gave an own field createdBy, which the
    /// recording's documents do not declare ([`CREATOR_READERS`]).
    creators: BTreeSet<String>,
    /// The principal its runner runs the due deadlines as.
    runner: Caller,
    /// The uses each row predates, by its type and key: those a publish
    /// added to its type while it had rows. Such a row holds none of the
    /// use's columns, where the engine's readers gave their defaults, which
    /// the core reads alike (D103), so its record is compared without the
    /// use's owned fields the core holds no value of.
    predates: BTreeMap<(String, String), Vec<String>>,
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
    /// Checked, and every step after it skipped.
    Last(Vec<Found>, String),
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
    /// Whether the file is one of the engine's recordings, whose types the
    /// replay gives the creator field the engine kept ([`CREATOR_READERS`]);
    /// a hand-written file declares it where it wants one.
    recording: bool,
    /// The operations of the file's incidental behaviors, each with why
    /// its steps are not compared ([`INCIDENTAL`]).
    incidental_ops: Vec<(String, &'static str)>,
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
            recording: false,
            incidental_ops: incidental_operations(file),
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
        let before: Vec<usize> = self
            .engines
            .iter()
            .map(|engine| engine.host.store().outbox().len())
            .collect();
        let done = if step.get("js").and_then(Value::as_bool) == Some(true) {
            Done::Halted("only the recording's own host runs it (js)".to_owned())
        } else {
            self.make(step)
        };
        let done = self.with_events(step, &before, done);
        let last = match &done {
            Done::Last(_, reason) => Some(reason.clone()),
            _ => None,
        };
        match done {
            Done::Checked(found) | Done::Last(found, _) => {
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
                if let Some(reason) = last {
                    self.halted = Some((index + 1, reason));
                }
            }
            Done::Skipped(reason) => self.skip(index, step, reason),
            Done::Halted(reason) => {
                self.skip(index, step, reason.clone());
                self.halted = Some((index + 1, reason));
            }
        }
    }

    // with_events holds a checked step to the events its commands wrote
    // (D104): the entries each engine's outbox gained since `before`, its
    // lengths before the step, in the order of the engines and then of the
    // writes, each whole, against the step's `events`, none when it has
    // none.
    fn with_events(&mut self, step: &Value, before: &[usize], done: Done) -> Done {
        let (found, last) = match done {
            Done::Checked(found) => (found, None),
            Done::Last(found, reason) => (found, Some(reason)),
            other => return other,
        };
        let emitted: Vec<Value> = self
            .engines
            .iter()
            .enumerate()
            .flat_map(|(at, engine)| {
                let outbox = engine.host.store().outbox();
                outbox[before.get(at).copied().unwrap_or(0).min(outbox.len())..].to_vec()
            })
            .map(|written| event_json(&written))
            .collect();
        let expected = step
            .get("events")
            .cloned()
            .unwrap_or_else(|| Value::Array(Vec::new()));
        let mut checks = Checks {
            symbols: &mut self.symbols,
            found,
        };
        checks.eq("events", &expected, &Value::Array(emitted));
        match last {
            Some(reason) => Done::Last(checks.found, reason),
            None => Done::Checked(checks.found),
        }
    }

    // make maps one step onto the host and compares its answer.
    fn make(&mut self, step: &Value) -> Done {
        let call = call_of(step);
        if call == "open" {
            return self.open(step);
        }
        // A file's incidental behaviors are left out of what a step
        // expects, and a step that calls one of their operations, or whose
        // refusal names one, is not compared.
        let adjusted;
        let step = if INCIDENTAL.iter().any(|(file, _, _)| *file == self.file) {
            if let Some(reason) = self.incidental_step(step) {
                return Done::Skipped(reason.to_owned());
            }
            adjusted = without_incidental(self.file, step);
            &adjusted
        } else {
            step
        };
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
        if let Some(namespace) = args.iter().find_map(|arg| {
            arg.get("namespace")
                .and_then(Value::as_str)
                .filter(|namespace| *namespace != shape::NAMESPACE)
        }) {
            return Done::Halted(format!(
                "it names namespace {namespace}, and the reference host keeps the corpus's one ({}): a build-time project's tables are its database's, which has no namespaces (D103), and the steps after it read what the ones before it wrote there",
                shape::NAMESPACE
            ));
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
        if let (true, Expected::Error(error)) = (call.starts_with("instances."), &expected) {
            let linked = args
                .first()
                .and_then(Value::as_str)
                .and_then(|type_name| self.engines[at].host.store().live(type_name))
                .is_some_and(|spec| spec.fields.iter().any(|field| field.relation.is_some()));
            if linked && names_links(error) {
                return Done::Skipped(LINKS_REFUSAL.to_owned());
            }
        }
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
            "runner.runDue" => self.run_due(&cx),
            "storage.run" => self.raw_write(&cx),
            "instances.invokeSchema" => self.invoke_schema(&cx),
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

    // incidental_step says why a step of an incidental behavior's
    // (INCIDENTAL) is not compared: it invokes one of its operations, or
    // its recorded refusal names it.
    fn incidental_step(&self, step: &Value) -> Option<&'static str> {
        let message = step
            .get("error")
            .and_then(|error| error.get("message"))
            .and_then(Value::as_str)
            .unwrap_or_default();
        let named = INCIDENTAL
            .iter()
            .filter(|(file, _, _)| *file == self.file)
            .find(|(_, behavior, _)| message.contains(behavior))
            .map(|(_, _, reason)| *reason);
        if named.is_some() {
            return named;
        }
        if call_of(step) != "instances.invoke" {
            return None;
        }
        let operation = step
            .get("args")
            .and_then(|args| args.get(2))
            .and_then(Value::as_str)?;
        self.incidental_ops
            .iter()
            .find(|(name, _)| name == operation)
            .map(|(_, reason)| *reason)
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
        if options
            .get("runner")
            .and_then(|runner| runner.get("maxDepth"))
            .is_some()
        {
            off.get_or_insert_with(|| MAX_DEPTH_REASON.to_owned());
        }
        if options.contains_key("values") {
            off.get_or_insert_with(|| VALUES_REASON.to_owned());
        }
        match options.get("policy").and_then(Value::as_str) {
            Some("allowAll") => {}
            _ => {
                off.get_or_insert_with(|| {
                    "its access policy is not allowAll, and the reference host applies none: the API authorizes (D103)".to_owned()
                });
            }
        }
        // A test behavior the engine registers is the file's fixture, whether
        // or not the scenario composes it. A behavior of an extension's
        // package the engine registers (a name with no dot: the work queue's)
        // that the core does not run leaves the engine on: a scenario that
        // composes it is skipped for it, and one that does not reads none
        // of it.
        for behavior in options
            .get("behaviors")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
        {
            let known = fixtures::register(self.file, behavior, &mut core)
                || core.declaration(behavior).is_some()
                || !behavior.contains('.');
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
        let runner = options
            .get("runner")
            .and_then(|runner| runner.get("principal"))
            .map_or_else(
                || Caller::new("runner", Vec::<String>::new()),
                |principal| {
                    Caller::new(
                        principal
                            .get("subject")
                            .and_then(Value::as_str)
                            .unwrap_or("runner"),
                        principal
                            .get("permissions")
                            .and_then(Value::as_array)
                            .into_iter()
                            .flatten()
                            .filter_map(Value::as_str),
                    )
                },
            );
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
            creators: BTreeSet::new(),
            runner,
            predates: BTreeMap::new(),
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
            .write_owned(&write.type_name, &write.key, &write.owned, cx.now)
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
        let mut defined = match shape::read_document(document) {
            Ok(defined) => defined,
            Err(reason) => {
                return Done::Skipped(format!(
                    "a document the reference host cannot read: {reason}"
                ))
            }
        };
        if self.recording {
            without_runner_members(&mut defined.spec);
        }
        without_incidental_uses(self.file, &mut defined.spec);
        if self.recording && with_creator(&mut defined) {
            self.engines[cx.at]
                .creators
                .insert(defined.spec.name.clone());
        }
        if let Some(reason) = &defined.unmapped {
            return Done::Halted(reason.clone());
        }
        if let Expected::Error(error) = cx.expected {
            if is_document_error(error) {
                return Done::Skipped(
                    "the schema document's own validation is the store's".to_owned(),
                );
            }
            if defined.links || names_links(error) {
                return Done::Skipped(LINKS_REFUSAL.to_owned());
            }
            let message = error
                .get("message")
                .and_then(Value::as_str)
                .unwrap_or_default();
            if message.contains(" requires behavior ") {
                return Done::Skipped(REQUIRES_REFUSAL.to_owned());
            }
            if message.contains("no implementation registered") {
                return Done::Skipped(NO_IMPLEMENTATION.to_owned());
            }
            if self.recording
                && (message.contains("releaseLeases: schema ")
                    || message.contains("does not compose Lease, so it holds no lease to expire"))
            {
                return Done::Halted(OTHER_SCHEMA_DEFINE.to_owned());
            }
        }
        let type_name = defined.spec.name.clone();
        let engine = &mut self.engines[cx.at];
        let live_version = engine.host.store().live_version(&type_name);
        let mut check = engine.host.define_command(&defined.spec, cx.caller, cx.now);
        let mut spec = defined.spec.clone();
        let mut outcome = engine.host.define(defined.spec, cx.caller, cx.now);
        // The engine's define refusal names the document's type, where its
        // other messages name the schema: a document that names them apart
        // is checked again under its type's name for the refusal, which
        // keeps nothing, as the core names a type by one name.
        let mut refused_as = type_name.clone();
        let documented = matches!(&outcome, Err(refusal) if refusal.class == SCHEMA_DOCUMENT_ERROR);
        if documented && defined.type_name != type_name {
            let rename = |spec: &TypeSpec| TypeSpec {
                name: defined.type_name.clone(),
                ..spec.clone()
            };
            spec = rename(&spec);
            if let Action::Publish { from: Some(from) } = &mut check.action {
                *from = rename(from);
            }
            outcome = engine.host.check(&spec, &check);
            refused_as = defined.type_name.clone();
        }
        // A config the core takes at a later step of D103's order of the
        // work stops the scenario, whose steps after it read what the
        // engine made of it.
        if let (Err(refusal), false) = (&outcome, matches!(cx.expected, Expected::Error(_))) {
            if refusal.message.contains("waits for") {
                return Done::Halted(format!(
                    "the core refuses what the engine took: {}",
                    refusal.message
                ));
            }
        }
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
            &refused_as,
            live_version,
            outcome.map(|()| Value::Null),
            |_, _, _| {},
        )
    }

    fn publish(&mut self, cx: &Cx<'_>) -> Done {
        let Some(type_name) = cx.args.first().and_then(Value::as_str) else {
            return Done::Skipped("a publish that names no schema".to_owned());
        };
        if let Some(reason) = rewrites_rows(self.engines[cx.at].host.store(), type_name) {
            return Done::Halted(reason.to_owned());
        }
        let engine = &mut self.engines[cx.at];
        let live_version = engine.host.store().live_version(type_name);
        let added = added_uses(engine.host.store(), type_name);
        let pending = engine.host.publish_command(type_name, cx.caller, cx.now);
        let published = engine.host.publish(type_name, cx.caller, cx.now);
        if let (Ok(Some(_)), false) = (&published, added.is_empty()) {
            let keys: Vec<String> = engine
                .host
                .store()
                .rows(type_name)
                .into_iter()
                .map(|(key, _)| key.to_owned())
                .collect();
            for key in keys {
                engine
                    .predates
                    .entry((type_name.to_owned(), key))
                    .or_default()
                    .extend(added.iter().cloned());
            }
        }
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
        let (recording, file) = (self.recording, self.file);
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
                    .map_or(Value::Null, |mut defined| {
                        if recording {
                            without_runner_members(&mut defined.spec);
                        }
                        without_incidental_uses(file, &mut defined.spec);
                        uses_of(&defined.spec)
                    });
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
        let recording = self.recording;
        self.judge(cx, type_name, 0, Ok(actual), |checks, expected, actual| {
            // The engine's Links is the type's relations, no use of it.
            let recorded: Vec<Value> = expected
                .as_array()
                .into_iter()
                .flatten()
                .filter(|entry| entry.get("name").and_then(Value::as_str) != Some(shape::LINKS))
                .map(|entry| {
                    let name = entry
                        .get("name")
                        .and_then(Value::as_str)
                        .unwrap_or_default();
                    let mut config = entry.get("config").cloned().unwrap_or_else(|| json!({}));
                    if let (true, Some(members)) = (recording, config.as_object_mut()) {
                        for (behavior, member) in RUNNER_MEMBERS {
                            if behavior == name {
                                members.remove(member);
                            }
                        }
                    }
                    json!({
                        "name": entry.get("name").cloned().unwrap_or(Value::Null),
                        "config": config,
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
        let mut own = self.stored(cx.at, type_name, data);
        // The creator the engine kept beside an instance, in the field the
        // replay gave its type.
        let engine = &self.engines[cx.at];
        let creates_creator = engine.creators.contains(type_name)
            && engine
                .host
                .store()
                .live(type_name)
                .is_some_and(|spec| spec.field(CREATED_BY).is_some());
        if creates_creator && !own.contains_key(CREATED_BY) && !cx.caller.subject.is_empty() {
            own.insert(
                CREATED_BY.to_owned(),
                Value::from(cx.caller.subject.as_str()),
            );
        }
        let mut params = options
            .get("behaviors")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        // A create's links are its relations' keys.
        if let Some(links) = params.remove(shape::LINKS) {
            let live = self.engines[cx.at].host.store().live(type_name);
            for (link, id) in links.as_object().into_iter().flatten() {
                let field = live.and_then(|spec| spec.relation(link));
                let (Some(field), Value::String(_)) = (field, id) else {
                    return Done::Skipped(LINKS_OWN.to_owned());
                };
                own.insert(field.name.clone(), id.clone());
            }
        }
        let action = Action::Create {
            id: options.get("id").and_then(Value::as_str).map(str::to_owned),
            own,
            params,
        };
        self.write(cx, type_name, action, Map::new())
    }

    // link maps Links' link and unlink onto an update of the relation's own
    // field, answered as Links answered: the link's name, its target's
    // schema and id (the one unlink took away). Links' own validation, and
    // its listLinked, which a table answers with a query, are not mapped.
    fn link(
        &mut self,
        cx: &Cx<'_>,
        type_name: &str,
        key: &str,
        operation: &str,
        params: &Value,
    ) -> Done {
        if let Expected::Error(_) = cx.expected {
            return Done::Skipped(LINKS_OWN.to_owned());
        }
        let store = self.engines[cx.at].host.store();
        let name = params
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let Some(field) = store.live(type_name).and_then(|spec| spec.relation(name)) else {
            return Done::Skipped(LINKS_OWN.to_owned());
        };
        let target = field
            .relation
            .as_ref()
            .map(|relation| relation.type_name.clone())
            .unwrap_or_default();
        let field = field.name.clone();
        let (value, id) = match operation {
            "link" => match params.get("id") {
                Some(Value::String(id)) => (Value::from(id.as_str()), Value::from(id.as_str())),
                _ => return Done::Skipped(LINKS_OWN.to_owned()),
            },
            "unlink" => {
                let held = store
                    .row(type_name, key)
                    .and_then(|row| row.own.get(&field))
                    .cloned()
                    .unwrap_or(Value::Null);
                (Value::Null, held)
            }
            _ => {
                return Done::Skipped(format!(
                    "Links' {operation}, a read a table answers with a query of its relation's column (D103)"
                ))
            }
        };
        let answer = if id.is_null() {
            Value::Null
        } else {
            json!({ "name": name, "schema": target, "id": id })
        };
        let mut patch = Map::new();
        patch.insert(field, value);
        let live_version = store.live_version(type_name);
        let command = Command::new(
            cx.caller.clone(),
            cx.now,
            Action::Update {
                key: key.to_owned(),
                patch,
            },
        );
        let outcome = self
            .execute(cx.at, type_name, command)
            .map(|_| answer.clone());
        self.judge(cx, type_name, live_version, outcome, |checks, e, a| {
            checks.eq("result", e, a);
        })
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
        if let Some(reason) = unmapped_options(&options, &[PRECONDITIONS]) {
            return Done::Skipped(reason);
        }
        if let Some(done) = self.store_refuses(cx, type_name, patch, false) {
            return done;
        }
        let action = Action::Update {
            key: key.to_owned(),
            patch: self.stored(cx.at, type_name, patch),
        };
        self.write(cx, type_name, action, preconditions_of(&options))
    }

    // stored is a write's own fields as the store keeps them: its date-time
    // fields' instants in their canonical form, as the engine's parse
    // stores them, before any behavior sees them.
    fn stored(&self, at: usize, type_name: &str, data: &Map<String, Value>) -> Map<String, Value> {
        match self.engines[at].fields.get(type_name) {
            Some(fields) => shape::normalize_own(fields, data),
            None => data.clone(),
        }
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

    // write runs a create or an update, with the caller's preconditions,
    // and compares the record it leaves.
    fn write(
        &mut self,
        cx: &Cx<'_>,
        type_name: &str,
        action: Action,
        preconditions: Map<String, Value>,
    ) -> Done {
        let live_version = self.engines[cx.at].host.store().live_version(type_name);
        let predated = match &action {
            Action::Update { key, .. } => self.predated(cx.at, type_name, key),
            _ => Vec::new(),
        };
        let command =
            Command::new(cx.caller.clone(), cx.now, action).with_preconditions(preconditions);
        let outcome = self.execute(cx.at, type_name, command);
        let engine = &self.engines[cx.at];
        let outcome = outcome.map(|applied| {
            record_of(
                &engine.host,
                &engine.creators,
                type_name,
                &applied.key,
                cx.now,
            )
        });
        self.judge(cx, type_name, live_version, outcome, move |checks, e, a| {
            compare_record(checks, &without_predated(e, a, &predated), a);
        })
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
            recorded.command(
                &self.current,
                &engine.path,
                type_name,
                live.as_ref(),
                command.clone(),
                &outcome,
                engine.host.store(),
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
        let options = cx
            .args
            .get(2)
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if let Some(reason) = unmapped_options(&options, &[PRECONDITIONS]) {
            return Done::Skipped(reason);
        }
        let live_version = self.engines[cx.at].host.store().live_version(type_name);
        let command = Command::new(
            cx.caller.clone(),
            cx.now,
            Action::Delete {
                key: key.to_owned(),
            },
        )
        .with_preconditions(preconditions_of(&options));
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
        if let Some(reason) = unmapped_options(&options, &[PRECONDITIONS]) {
            return Done::Skipped(reason);
        }
        let linked = self.engines[cx.at]
            .host
            .store()
            .live(type_name)
            .is_some_and(|spec| spec.fields.iter().any(|field| field.relation.is_some()));
        if linked && matches!(operation, "link" | "unlink" | "listLinked") {
            return self.link(cx, type_name, key, operation, &params);
        }
        if self.recording
            && operation == "setLimit"
            && sets_limit_field(self.engines[cx.at].host.store(), type_name, &params)
        {
            return Done::Halted(LIMIT_FIELD_SET_LIMIT.to_owned());
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
                )
                .with_preconditions(preconditions_of(&options));
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

    // invoke_schema maps no operation of a type as a whole: a row's core
    // runs a row's operations (D104). One a behavior the core runs
    // declares of its table (Lease's expireHolder) is skipped with that
    // reason, the scenario stopping there when it wrote rows the steps
    // after it read; any other is outside the host's surface.
    fn invoke_schema(&mut self, cx: &Cx<'_>) -> Done {
        let operation = cx.args.get(1).and_then(Value::as_str).unwrap_or_default();
        let declared = cx
            .args
            .first()
            .and_then(Value::as_str)
            .and_then(|type_name| self.engines[cx.at].host.store().live(type_name))
            .is_some_and(|spec| {
                spec.uses
                    .iter()
                    .any(|entry| declares_row_less(&entry.behavior, operation))
            });
        if !declared {
            return Done::Halted(
                "instances.invokeSchema is outside the reference host's surface, and the steps after it may read what it changed"
                    .to_owned(),
            );
        }
        let reason = format!(
            "{operation} runs on the table as a whole, which waits for a command without a row (D104)"
        );
        match cx.expected {
            Expected::Error(_) => Done::Skipped(reason),
            _ => Done::Halted(reason),
        }
    }

    fn get(&mut self, cx: &Cx<'_>) -> Done {
        let (Some(type_name), Some(key)) = (
            cx.args.first().and_then(Value::as_str),
            cx.args.get(1).and_then(Value::as_str),
        ) else {
            return Done::Skipped("a get without a schema and an id".to_owned());
        };
        let engine = &self.engines[cx.at];
        let found = record_of(&engine.host, &engine.creators, type_name, key, cx.now);
        let predated = self.predated(cx.at, type_name, key);
        self.judge(cx, type_name, 0, Ok(found), move |checks, e, a| {
            compare_record(checks, &without_predated(e, a, &predated), a);
        })
    }

    // predated is the uses a row predates ([`Engine::predates`]), each with
    // the owned fields its behavior declares, which no column of the row
    // holds until a write through the core sets them.
    fn predated(&self, at: usize, type_name: &str, key: &str) -> Vec<(String, Vec<String>)> {
        let engine = &self.engines[at];
        let Some(uses) = engine.predates.get(&(type_name.to_owned(), key.to_owned())) else {
            return Vec::new();
        };
        let spec = engine.host.store().live(type_name);
        uses.iter()
            .filter_map(|use_id| {
                let (_, entry) = spec?.use_of(use_id)?;
                let declaration = engine.host.core().declaration(&entry.behavior)?;
                let owned: Vec<String> = declaration
                    .fields
                    .iter()
                    .filter(|field| !declaration.is_computed(field))
                    .cloned()
                    .collect();
                Some((use_id.clone(), owned))
            })
            .collect()
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
        let predated: BTreeMap<String, Vec<(String, Vec<String>)>> = self.engines[cx.at]
            .host
            .store()
            .rows(type_name)
            .into_iter()
            .map(|(key, _)| (key.to_owned(), self.predated(cx.at, type_name, key)))
            .collect();
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
                spec.zip(store.row(type_name, key)).map(|(spec, row)| {
                    without_creator(
                        shape::record(
                            host.core(),
                            spec,
                            key,
                            row,
                            &store.sub_tables(type_name, key),
                            cx.now,
                        ),
                        &self.engines[cx.at].creators,
                        type_name,
                    )
                })
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
                    let uses = found["id"]
                        .as_str()
                        .and_then(|key| predated.get(key))
                        .cloned()
                        .unwrap_or_default();
                    compare_record(checks, &without_predated(recorded, found, &uses), found);
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
        if let Expected::Value(page) = cx.expected {
            let unmapped = page
                .get("events")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
                .any(|event| {
                    event["cause"].get("behavior").and_then(Value::as_str) == Some(REACTIONS)
                        || event["change"].get("behavior").and_then(Value::as_str)
                            == Some(shape::LINKS)
                });
            if unmapped {
                return Done::Skipped(EVENTS_UNMAPPED.to_owned());
            }
        }
        let schema = options.get("schema").and_then(Value::as_str);
        let instance = options.get("instanceId").and_then(Value::as_str);
        let host = &self.engines[cx.at].host;
        let store = host.store();
        let heard = store.log().iter().any(|logged| {
            schema.is_none_or(|schema| logged.type_name == schema)
                && instance.is_none_or(|key| logged.key == key)
                && matches!(logged.command.action, Action::Change { .. })
        });
        if heard {
            return Done::Skipped(HEARD_WRITE.to_owned());
        }
        let called = store.log().iter().any(|logged| {
            schema.is_none_or(|schema| logged.type_name == schema)
                && instance.is_none_or(|key| logged.key == key)
                && written_by_a_call(logged)
        });
        if called {
            return Done::Skipped(EVENTS_OF_CALLS.to_owned());
        }
        let events: Vec<Value> = store
            .log()
            .iter()
            .filter(|logged| {
                schema.is_none_or(|schema| logged.type_name == schema)
                    && instance.is_none_or(|key| logged.key == key)
            })
            .map(|logged| {
                let spec = store.live(&logged.type_name);
                let event = sweep_event(spec, logged, shape::event(spec, logged));
                without_creator(event, &self.engines[cx.at].creators, &logged.type_name)
            })
            .collect();
        // The fields each use computes on a read, by type and use id, which
        // the recorded changes carry and an outbox does not.
        let computed: BTreeMap<String, BTreeMap<String, Vec<String>>> = store
            .type_names()
            .into_iter()
            .filter_map(|name| {
                let spec = store.live(&name)?;
                Some((name, computed_fields(host.core(), spec)))
            })
            .collect();
        self.judge(
            cx,
            schema.unwrap_or_default(),
            0,
            Ok(json!({ "events": events })),
            move |checks, expected, actual| {
                compare_events(checks, &without_computed(expected, &computed), actual);
            },
        )
    }

    // run_due runs the queued changes, then each deadline armed and due at
    // the step's time, earliest first, then the changes again, as the
    // engine's runner: each change a change command at the step's time, each
    // deadline a deadline command at that time (never before the time it
    // was armed for, as a worker whose clock lags runs it, D103). A deadline
    // one run before it cleared or moved is not run. A deadline's refusal
    // other than a veto, which the engine's sweep passes over, and a
    // change's refusal are mismatches unless the recording's run failed,
    // and a run whose recording failed is the scenario's last step.
    fn run_due(&mut self, cx: &Cx<'_>) -> Done {
        let runner = self.engines[cx.at].runner.clone();
        let failed = match cx.expected {
            Expected::Value(counters) => {
                counters.get("failed").and_then(Value::as_u64).unwrap_or(0) > 0
            }
            _ => false,
        };
        let mut found = Vec::new();
        self.run_changes(cx, &runner, failed, &mut found);
        let due = self.engines[cx.at].host.store().due(cx.now);
        for (at, armed) in due {
            if self.engines[cx.at].host.store().armed_at(&at) != Some(armed.at) {
                continue;
            }
            let command = Command::new(
                runner.clone(),
                cx.now.max(armed.at),
                Action::Deadline {
                    key: at.key.clone(),
                    use_id: at.use_id.clone(),
                    name: at.name.clone(),
                    at: armed.at,
                    payload: armed.payload.clone(),
                },
            );
            let outcome = self.execute(cx.at, &at.type_name, command);
            if let Err(refusal) = outcome {
                if refusal.code != Code::Vetoed && !failed {
                    let live = self.engines[cx.at].host.store().live_version(&at.type_name);
                    found.push(Found {
                        what: format!(
                            "deadline {}.{} of {} {}",
                            at.use_id, at.name, at.type_name, at.key
                        ),
                        expected: json!("fired, or vetoed"),
                        actual: shape::engine_error(&refusal, &at.type_name, live),
                    });
                }
            }
        }
        self.run_changes(cx, &runner, failed, &mut found);
        if failed {
            return Done::Last(found, HALTED_RUN.to_owned());
        }
        Done::Checked(found)
    }

    // run_changes runs the changes the hearing types' triggers queued, first
    // in first out, those the commands queue included, at most MAX_CHANGES,
    // each a change command as the runner: at the step's time on a fixed
    // clock, and at the wall clock's next instant on the wall clock, as the
    // engine's runner read its clock for each event. A change the core
    // refuses is taken off, as a message that goes dead, and is a mismatch
    // unless the recording's run failed.
    fn run_changes(&mut self, cx: &Cx<'_>, runner: &Caller, failed: bool, found: &mut Vec<Found>) {
        for _ in 0..MAX_CHANGES {
            let Some(queued) = self.engines[cx.at].host.store_mut().take_change() else {
                return;
            };
            let now = match self.engines[cx.at].clock {
                Clock::Fixed(_) => cx.now,
                Clock::Wall => {
                    self.wall += 1;
                    self.wall
                }
            };
            let command = MemoryHost::change_command(&queued, runner, now);
            if let Err(refusal) = self.execute(cx.at, &queued.type_name, command) {
                if !failed {
                    let live = self.engines[cx.at]
                        .host
                        .store()
                        .live_version(&queued.type_name);
                    found.push(Found {
                        what: format!(
                            "change of {} {} ({})",
                            queued.type_name,
                            queued.key,
                            queued.change.write.as_str()
                        ),
                        expected: json!("heard"),
                        actual: shape::engine_error(&refusal, &queued.type_name, live),
                    });
                }
            }
        }
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

// event_json is an event the outbox holds as a scenario step's `events`
// writes it (D104): the row's schema and id, the use, the kind, the
// command's time and the payload.
fn event_json(written: &crate::host::Outboxed) -> Value {
    json!({
        "schema": written.type_name,
        "id": written.key,
        "use": written.event.use_id,
        "kind": written.event.kind,
        "at": written.at,
        "payload": written.event.payload,
    })
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

// written_by_a_call says whether a logged write is of a row another row's
// operation or deadline reached through an operation of the row (a
// scope's reserveFor, a Lease's expire from a miss): the command runs on
// another key.
fn written_by_a_call(logged: &Logged) -> bool {
    matches!(
        logged.command.action,
        Action::Operation { .. } | Action::Deadline { .. }
    ) && logged.command.action.key() != Some(logged.key.as_str())
}

// sets_limit_field says whether a setLimit names a meter whose live
// Budget config keeps its limit in an own field (limitField), which the
// engine's setLimit wrote through update().
fn sets_limit_field(store: &Store, type_name: &str, params: &Value) -> bool {
    let meter = params
        .get("meter")
        .and_then(Value::as_str)
        .unwrap_or_default();
    store.live(type_name).is_some_and(|spec| {
        spec.uses.iter().any(|entry| {
            entry.behavior == "Budget"
                && entry
                    .config
                    .as_ref()
                    .and_then(|config| config.get("meters"))
                    .and_then(|meters| meters.get(meter))
                    .and_then(|spec| spec.get("limitField"))
                    .is_some()
        })
    })
}

// declares_row_less says whether a behavior the core runs declares an
// operation of its table or namespace as a whole by a name, which its
// declaration file holds and the core's declaration leaves out (D104).
fn declares_row_less(behavior: &str, operation: &str) -> bool {
    superschematic_rules_behaviors::DECLARATIONS
        .iter()
        .any(|text| {
            let file: Value = serde_json::from_str(text).unwrap_or(Value::Null);
            file.get("name").and_then(Value::as_str) == Some(behavior)
                && file
                    .get("operations")
                    .and_then(Value::as_array)
                    .into_iter()
                    .flatten()
                    .any(|declared| {
                        declared.get("name").and_then(Value::as_str) == Some(operation)
                            && matches!(
                                declared.get("scope").and_then(Value::as_str),
                                Some("schema" | "namespace")
                            )
                    })
        })
}

// preconditions_of is the caller's preconditions an update's, a delete's
// or an invoke's options give (D16), each use's entry by its name, which is
// an implements use's id, as the core's command carries them (D104).
fn preconditions_of(options: &Map<String, Value>) -> Map<String, Value> {
    options
        .get(PRECONDITIONS)
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default()
}

// names_links says whether a recorded refusal names the engine's Links.
fn names_links(error: &Value) -> bool {
    serde_json::to_string(error).is_ok_and(|text| text.contains(shape::LINKS))
}

// is_document_error says whether a define's refusal is the schema
// document's own validation, which names no behavior.
fn is_document_error(error: &Value) -> bool {
    error.get("class").and_then(Value::as_str) == Some(SCHEMA_DOCUMENT_ERROR)
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
    let canonical = canonical_changes(expected);
    let expected = &canonical;
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

// canonical_changes is a recorded incompatible change with each config its
// messages print as the core prints them, members sorted: the engine
// printed the new config in the order its document gave it, and the core
// prints a config the same however its host ordered it.
fn canonical_changes(error: &Value) -> Value {
    let mut error = error.clone();
    if error.get("class").and_then(Value::as_str) != Some("IncompatibleChangeError") {
        return error;
    }
    if let Some(Value::String(message)) = error.get_mut("message") {
        *message = canonical_configs(message);
    }
    if let Some(changes) = error
        .get_mut("details")
        .and_then(|details| details.get_mut("changes"))
        .and_then(Value::as_array_mut)
    {
        for change in changes {
            if let Some(Value::String(message)) = change.get_mut("message") {
                *message = canonical_configs(message);
            }
        }
    }
    error
}

// canonical_configs rewrites each `config from {..} to {..}` of a message
// with both configs' members sorted.
fn canonical_configs(message: &str) -> String {
    const FROM: &str = " cannot change its config from ";
    let mut out = String::new();
    let mut rest = message;
    while let Some(at) = rest.find(FROM) {
        out.push_str(&rest[..at + FROM.len()]);
        rest = &rest[at + FROM.len()..];
        for (index, joint) in [" to ", ""].iter().enumerate() {
            let mut values = serde_json::Deserializer::from_str(rest).into_iter::<Value>();
            match values.next() {
                Some(Ok(value)) => {
                    let end = values.byte_offset();
                    out.push_str(&sorted(&value).to_string());
                    rest = &rest[end..];
                }
                _ => return format!("{out}{rest}"),
            }
            if index == 0 {
                match rest.strip_prefix(joint) {
                    Some(after) => {
                        out.push_str(joint);
                        rest = after;
                    }
                    None => return format!("{out}{rest}"),
                }
            }
        }
    }
    out.push_str(rest);
    out
}

// sorted is a value with every object's members sorted by name.
fn sorted(value: &Value) -> Value {
    match value {
        Value::Object(members) => {
            let mut keys: Vec<&String> = members.keys().collect();
            keys.sort();
            Value::Object(
                keys.into_iter()
                    .map(|key| (key.clone(), sorted(&members[key])))
                    .collect(),
            )
        }
        Value::Array(items) => Value::Array(items.iter().map(sorted).collect()),
        other => other.clone(),
    }
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
// create's and an update's own (`data`) and owned (`behaviors`) fields, an
// operation's behavior, name and parameters and the own and owned fields
// its patch set, each where the recording holds the change.
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
            Some("create" | "update") => &["data", "behaviors"],
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
        if recorded["kind"].as_str() == Some("operation") {
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

// record_of is a row as the engine's record at a time, the creator the
// replay gave its type left out, or null.
fn record_of(
    host: &MemoryHost,
    creators: &BTreeSet<String>,
    type_name: &str,
    key: &str,
    now: i64,
) -> Value {
    let store = host.store();
    match (store.live(type_name), store.row(type_name, key)) {
        (Some(spec), Some(row)) => without_creator(
            shape::record(
                host.core(),
                spec,
                key,
                row,
                &store.sub_tables(type_name, key),
                now,
            ),
            creators,
            type_name,
        ),
        _ => Value::Null,
    }
}

// added_uses is the uses a type's draft adds to its live version, by id,
// when the type has rows: each row then predates them.
fn added_uses(store: &Store, type_name: &str) -> Vec<String> {
    let Some(draft) = store.draft(type_name) else {
        return Vec::new();
    };
    if !store.has_rows(type_name) {
        return Vec::new();
    }
    let live = store.live(type_name);
    draft
        .uses
        .iter()
        .filter(|entry| {
            !live.is_some_and(|live| {
                live.uses
                    .iter()
                    .any(|known| known.id() == entry.id() && known.behavior == entry.behavior)
            })
        })
        .map(|entry| entry.id().to_owned())
        .collect()
}

// without_predated is a recorded record with the owned fields of each use
// its row predates left out where the core's record holds no value of
// them: a row that predates its use holds none of its columns, where the
// engine's readers gave their defaults, which the core reads alike (D103).
fn without_predated(expected: &Value, actual: &Value, uses: &[(String, Vec<String>)]) -> Value {
    let mut expected = expected.clone();
    for (use_id, fields) in uses {
        let held = actual["behaviors"].get(use_id.as_str());
        let Some(recorded) = expected
            .get_mut("behaviors")
            .and_then(|behaviors| behaviors.get_mut(use_id.as_str()))
            .and_then(Value::as_object_mut)
        else {
            continue;
        };
        for field in fields {
            if held.and_then(|held| held.get(field)).is_none() {
                recorded.remove(field);
            }
        }
    }
    expected
}

// with_creator gives a document's type an own field createdBy, a string,
// when it lists a behavior that reads its creator from it
// (CREATOR_READERS) and declares none, as the engine kept an instance's
// creator beside it: whether it did.
fn with_creator(defined: &mut shape::Defined) -> bool {
    let reads = defined
        .spec
        .uses
        .iter()
        .any(|entry| CREATOR_READERS.contains(&entry.behavior.as_str()));
    if !reads || defined.spec.field(CREATED_BY).is_some() {
        return false;
    }
    defined.spec = std::mem::replace(&mut defined.spec, TypeSpec::new(""))
        .with_field(FieldSpec::new(CREATED_BY, "string"));
    defined.fields.push(FieldDef {
        name: CREATED_BY.to_owned(),
        required: false,
        json_type: "string".to_owned(),
        type_name: "string".to_owned(),
        date_time: false,
    });
    true
}

// without_creator is a record or an event of a type the replay gave an own
// field createdBy with that field left out of its own fields (a record's
// `data`, an event change's `data` and its patch's), which the recording's
// document does not declare.
fn without_creator(mut value: Value, creators: &BTreeSet<String>, type_name: &str) -> Value {
    if !creators.contains(type_name) {
        return value;
    }
    for path in ["/data", "/change/data", "/change/patch/data"] {
        if let Some(data) = value.pointer_mut(path).and_then(Value::as_object_mut) {
            data.remove(CREATED_BY);
        }
    }
    value
}

// sweep_event writes a deadline command's change-log entry as the engine's
// sweep event of the operation its handler calls on its own use
// (SWEEP_OPERATIONS): kind `operation`, with that operation's behavior,
// name and parameters and the patch the command wrote. Any other entry
// stays as it is.
fn sweep_event(spec: Option<&TypeSpec>, logged: &Logged, event: Value) -> Value {
    let Action::Deadline { use_id, name, .. } = &logged.command.action else {
        return event;
    };
    if logged.kind() != "deadline" {
        return event;
    }
    let behavior = spec
        .and_then(|spec| spec.use_of(use_id))
        .map_or(use_id.as_str(), |(_, entry)| entry.behavior.as_str());
    let Some((_, operation, params)) = SWEEP_OPERATIONS
        .iter()
        .find(|(known, _, _)| *known == behavior)
    else {
        return event;
    };
    let mut event = event;
    event["kind"] = json!("operation");
    let patch = event["change"]["patch"].clone();
    event["change"] = json!({
        "behavior": behavior,
        "operation": operation,
        "params": params(name),
        "patch": patch,
    });
    event
}

// computed_fields lists, by use id, the fields each use of a type computes
// on a read: those its declaration computes, and the members its
// read_fields gives for a row with nothing in it.
fn computed_fields(core: &Core, spec: &TypeSpec) -> BTreeMap<String, Vec<String>> {
    spec.uses
        .iter()
        .filter_map(|entry| {
            let mut fields: Vec<String> = core
                .declaration(&entry.behavior)
                .map(|declaration| declaration.computed.clone())
                .unwrap_or_default();
            let read = core
                .read_fields(spec, entry.id(), &Row::default(), &BTreeMap::new())
                .ok()?;
            for field in read.keys() {
                if !fields.contains(field) {
                    fields.push(field.clone());
                }
            }
            (!fields.is_empty()).then(|| (entry.id().to_owned(), fields))
        })
        .collect()
}

// without_computed is a recorded page of events with the fields each use
// computes on a read left out of each change's owned fields (a create's
// and an update's `behaviors`, a patch's), a use left with none left out,
// and `behaviors` left out when it holds none.
fn without_computed(
    page: &Value,
    computed: &BTreeMap<String, BTreeMap<String, Vec<String>>>,
) -> Value {
    let mut page = page.clone();
    let Some(events) = page.get_mut("events").and_then(Value::as_array_mut) else {
        return page;
    };
    for event in events {
        let Some(fields) = event
            .get("schema")
            .and_then(Value::as_str)
            .and_then(|schema| computed.get(schema))
            .cloned()
        else {
            continue;
        };
        let Some(change) = event.get_mut("change").and_then(Value::as_object_mut) else {
            continue;
        };
        strip(change, &fields);
        if let Some(patch) = change.get_mut("patch").and_then(Value::as_object_mut) {
            strip(patch, &fields);
        }
    }
    page
}

// strip leaves the computed fields out of an object's `behaviors`.
fn strip(holder: &mut Map<String, Value>, fields: &BTreeMap<String, Vec<String>>) {
    let Some(behaviors) = holder.get_mut("behaviors").and_then(Value::as_object_mut) else {
        return;
    };
    for (use_id, names) in fields {
        if let Some(owned) = behaviors.get_mut(use_id).and_then(Value::as_object_mut) {
            owned.retain(|name, _| !names.contains(name));
            if owned.is_empty() {
                behaviors.remove(use_id);
            }
        }
    }
    if behaviors.is_empty() {
        holder.remove("behaviors");
    }
}

// rewrites_rows says why publishing a type's draft stops the scenario: it
// changes a config in a way the engine's afterConfigChange rewrote rows
// for, on a type with rows (PUBLISH_REWRITES), each listed behavior's use,
// by id, before and after; None when it does not.
fn rewrites_rows(store: &Store, type_name: &str) -> Option<&'static str> {
    let draft = store.draft(type_name)?;
    if !store.has_rows(type_name) {
        return None;
    }
    let live = store.live(type_name);
    PUBLISH_REWRITES
        .iter()
        .find_map(|(behavior, rewrites, reason)| {
            let config_of = |spec: Option<&TypeSpec>| -> Vec<(String, Value)> {
                spec.into_iter()
                    .flat_map(|spec| &spec.uses)
                    .filter(|entry| entry.behavior == *behavior)
                    .map(|entry| (entry.id().to_owned(), entry.config_or_empty()))
                    .collect()
            };
            let (before, after) = (config_of(live), config_of(Some(draft)));
            let mut ids: Vec<&String> = before.iter().chain(&after).map(|(id, _)| id).collect();
            ids.sort();
            ids.dedup();
            let rewritten = ids.into_iter().any(|id| {
                let find = |configs: &[(String, Value)]| {
                    configs
                        .iter()
                        .find(|(known, _)| known == id)
                        .map(|(_, config)| config.clone())
                };
                rewrites(find(&before).as_ref(), find(&after).as_ref())
            });
            rewritten.then_some(*reason)
        })
}

/// Whether the engine's Approvals afterConfigChange rewrote rows for a
/// change of an Approvals use's config: removed from the type, which
/// deleted every row's sign-offs, and a gate removed, whose sign-offs it
/// consumed. A gate added or changed, and resetOnChange, it rewrote nothing
/// for.
pub fn approvals_rewrites(before: Option<&Value>, after: Option<&Value>) -> bool {
    let gates = |config: Option<&Value>| -> Map<String, Value> {
        config
            .and_then(|config| config.get("gates"))
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default()
    };
    match (before, after) {
        (None, _) => false,
        (Some(_), None) => true,
        (before, after) => {
            let new = gates(after);
            gates(before).keys().any(|state| !new.contains_key(state))
        }
    }
}

/// Whether the engine's Timers afterConfigChange rewrote rows for a change
/// of a Timers use's config: removed from the type, which deleted every
/// row's deadlines; a deadline removed, or its start changed, whose rows it
/// deleted; a new afterMs, which moved the pending deadlines from their
/// start; and a new field deadline, which it armed on every row from its
/// field. A deadline added that starts at the create or at an entry, a
/// then, an until, a permission or a sweepMs it rewrote nothing for.
pub fn timers_rewrites(before: Option<&Value>, after: Option<&Value>) -> bool {
    let deadlines = |config: Option<&Value>| -> Map<String, Value> {
        config
            .and_then(|config| config.get("deadlines"))
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default()
    };
    let after_ms = |deadline: &Value| deadline.get("afterMs").and_then(Value::as_i64).unwrap_or(0);
    match (before, after) {
        (None, None) => false,
        (Some(_), None) => true,
        (before, Some(_)) => {
            let (old, new) = (deadlines(before), deadlines(after));
            let changed = new.iter().any(|(name, deadline)| match old.get(name) {
                Some(was) if was.get("start") == deadline.get("start") => {
                    after_ms(was) != after_ms(deadline)
                }
                was => {
                    was.is_some()
                        || deadline
                            .get("start")
                            .is_some_and(|start| start.get("field").is_some())
                }
            });
            changed || old.keys().any(|name| !new.contains_key(name))
        }
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
    // An operation of a table or a namespace as a whole, which a row's core
    // does not run (D104), is left out.
    let mut operations: Vec<(String, bool)> = declaration
        .get("operations")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter(|operation| {
            !matches!(
                operation.get("scope").and_then(Value::as_str),
                Some("schema" | "namespace")
            )
        })
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
