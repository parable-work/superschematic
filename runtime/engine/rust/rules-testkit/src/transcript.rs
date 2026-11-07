//! A transcript (D103): a recording of the engine's scenarios written out
//! as the core's commands and what each must answer, which every
//! language's adapter replays against real tables, on SQLite and on
//! Postgres, as the reference host replays the recording itself
//! ([`crate::replay`]). The recorded scenarios are the specification; a
//! transcript is that specification in the core's own terms, so an adapter
//! needs no replayer of the engine's calls: it creates each type's table,
//! runs each step's command through its host loop and compares.
//!
//! [`transcript`] writes one from a recording by replaying it on the
//! reference host and recording, for each step the replay maps, the command
//! it ran and what the core and the host answered, which the replay has
//! held to the engine's answers. `core-workflow.json` is written to
//! `runtime/engine/testdata/rules/core-workflow.transcript.json`
//! ([`transcript_path`]) and checked in; a test fails when the file differs
//! from what the writer writes.
//!
//! The document:
//!
//! ```json
//! {
//!   "file": "core-workflow",
//!   "source": "runtime/engine/testdata/scenarios/core-workflow.json",
//!   "behaviors": ["Workflow"],
//!   "scenarios": [
//!     {
//!       "name": "Workflow (node) > a transition moves the status",
//!       "clock": {"fixed": 1000000},
//!       "types": [
//!         {
//!           "name": "Order",
//!           "fields": [{"name": "title", "type": "string", "required": true}],
//!           "versions": [
//!             {"name": "Order", "uses": [{"behavior": "Workflow", "config": {"states": ["draft", "open"], "transitions": [{"from": "draft", "to": "open"}]}}]}
//!           ]
//!         }
//!       ],
//!       "steps": [
//!         {"kind": "check", "step": 2, "call": "schemas.define", "database": "e1",
//!          "spec": {"name": "Order", "uses": [...]},
//!          "command": {"caller": {"subject": "alice"}, "now": 1000000, "action": {"kind": "define"}},
//!          "expect": {"result": null}},
//!         {"kind": "command", "step": 4, "call": "instances.create", "database": "e1", "type": "Order", "version": 0,
//!          "command": {"caller": {"subject": "alice"}, "now": 1000000, "request": "r4",
//!                      "action": {"kind": "create", "id": "o1", "own": {"title": "Desk"}}},
//!          "expect": {"result": null, "key": "o1",
//!                     "row": {"own": {"title": "Desk"}, "owned": {"Workflow": {"status": "draft", "enteredAt": 1000000}}}}},
//!         {"kind": "command", "step": 5, "call": "instances.invoke", "database": "e1", "type": "Order", "version": 0,
//!          "command": {"caller": {"subject": "alice"}, "now": 1000000, "request": "r5",
//!                      "action": {"kind": "operation", "key": "o1", "useId": "Workflow", "operation": "transition", "params": {"to": "lost"}}},
//!          "expect": {"refused": {"code": "vetoed", "status": 409, "class": "BehaviorVetoError", "message": "...", "details": {...}},
//!                     "row": {"own": {"title": "Desk"}, "owned": {"Workflow": {"status": "draft", "enteredAt": 1000000}}}}},
//!         {"kind": "raw", "step": 6, "call": "storage.run", "database": "e1", "type": "Order", "key": "o1",
//!          "owned": {"Workflow": {"enteredAt": null}}}
//!       ],
//!       "skipped": [{"step": 7, "call": "events.read", "reason": "..."}]
//!     }
//!   ]
//! }
//! ```
//!
//! A type's `versions` and a check's `spec` are `TypeSpec`s with their own
//! `fields` (each one's name, IR type and `dateTime`), which a config that
//! names a field is held to. A deadline that fell due is a `command` step
//! whose action is `deadline`, with the `step` and `call` of the
//! `runner.runDue` that ran it, so several steps may share one step number;
//! its command carries the runner's caller, its `now` and its `request`.
//! A command's `expect` holds, beside the row, the row's sub-table rows
//! after the step and its armed deadlines:
//!
//! ```json
//! "subRows": {"Timers": {"deadlines": [{"key": "respond", "values": {"armedFrom": "1791536400000", "baseAt": 1791536400000, "dueAt": 1791550800000, "fired": false}}]}},
//! "deadlines": [{"useId": "Timers", "name": "respond", "at": 1791550800000, "payload": null}]
//! ```
//!
//! the sub-table rows by use id and table, each table's rows in key order
//! and absent when it has none (an absent `subRows` holds none at all), each
//! row's values by declared field, a null one absent; and the deadlines by
//! use id and then name, absent when none. Both are absent after a delete,
//! and a refused command's are as they were.
//!
//! A command that presents the caller's preconditions (D16, D104) carries
//! them as the core's command does, each use's entry by its use id, absent
//! when there are none, which the replay maps from a recorded update's,
//! delete's or invoke's `preconditions` option; an adapter runs it as
//! written, from its repository's option to the core:
//!
//! ```json
//! "command": {"caller": {"subject": "wren"}, "now": 1000000, "request": "r9",
//!             "action": {"kind": "operation", "key": "j1", "useId": "Lease", "operation": "heartbeat", "params": {}},
//!             "preconditions": {"Lease": {"token": 1}}}
//! ```
//!
//! A write of a row that the type's change trigger queued (D103) is a
//! `command` step whose action is `change`, with the `step` and `call` of
//! the `runner.runDue` that ran it, the runner's caller, its `now` and its
//! `request`, as a deadline's is: each a write's key, kind, time and the
//! row's own and owned fields before and after it, which an adapter runs as
//! written, never from its own change queue. A command that writes rows
//! other than its own (a rule that moves the project a task points at)
//! lists them in `others`, in the change set's order, each read back after
//! the step, its row absent when it was deleted:
//!
//! ```json
//! "others": [{"type": "Project", "key": "p1", "row": {"own": {"title": "Launch"}, "owned": {"Workflow": {"status": "active", "enteredAt": 1001000}}}}]
//! ```
//!
//! A command whose change set wrote events about rows (D104,
//! `superschematic_rules::Cx::emit`) lists them in `events`, in the change
//! set's order, each the row's type and key, the use that emitted it, its
//! kind, the command's time and its payload; absent when it wrote none:
//!
//! ```json
//! "events": [{"type": "Problem", "key": "p1", "useId": "Schedules", "kind": "run", "at": 1772323500000, "payload": {"name": "nudge", "run": 1, "attempt": 1, "kind": "after", "spec": "300000", "dueAt": 1772323500000, "firedAt": 1772323500000}}]
//! ```
//!
//! An operation of the table as a whole (D104), a recording's or a file's
//! `instances.invokeSchema`, is a `command` step whose action is
//! `tableOperation`, a command without a row: its `expect` holds its result
//! or its refusal, the rows its change set wrote (`others`) and its
//! `events`, and no `key`, `row`, `subRows`, `deadlines` or `version`, since
//! it names no row (its step's `version` is the type's version index, as for
//! any command):
//!
//! ```json
//! {"kind": "command", "step": 9, "call": "instances.invokeSchema", "database": "e1", "type": "Account", "version": 0,
//!  "command": {"caller": {"subject": "alice", "permissions": ["accounts.pool"]}, "now": 1772323200000, "request": "request-6",
//!              "action": {"kind": "tableOperation", "useId": "Pool", "operation": "pick", "params": {}}},
//!  "expect": {"result": {"member": {"key": "a1", "availability": "open", "order": 1, "preferred": true}}}}
//! ```
//!
//! An adapter knows it by its action's kind: it runs it through its host
//! loop with no row, compares the refusal whole or the result, reads back
//! each of `others`, and reads its events from every event queue, as for
//! any command.
//!
//! A transcript written from a hand-written scenario file
//! ([`hand_written_transcript`], `runtime/engine/testdata/rules/scenarios`,
//! D104), which holds cases the engine recorded none of, also holds each
//! command's row version after the step, as the host holds it, in its
//! `expect`:
//!
//! ```json
//! "expect": {"result": {"from": "answered", "to": "answered", "unchanged": true}, "row": {...}, "version": 2}
//! ```
//!
//! absent after a delete, and in a recording's transcript. An adapter
//! compares it, when present, with the `_version` it reads back after the
//! step, so a command that writes nothing (a repeat answered unchanged) is
//! held to leaving the row's version where it was.
//!
//! A recording's Links is each type's to-one relations: a link with one
//! target is an own field `<link>Id`, in `fields` as a string, whose
//! versions' specs name the relation (`relation: {name, type}`); an adapter
//! makes its column as any own field's, and a linked read finds it by its
//! name.
//!
//! How an adapter replays a scenario:
//!
//! 1. For each type, create its table in a database of the scenario's own:
//!    a TEXT key column, a column for each own field (`fields`, by its JSON
//!    type), each owned column the layout rule gives each use of any of its
//!    versions (the uses are type-anchored in the recordings, so Workflow's
//!    are `workflow_status` and `workflow_entered_at`; a hand-written file's
//!    may be anchored on fields, `"anchor": {"field": "observed"}`, whose
//!    value is the field's own column and whose other owned columns it
//!    prefixes: `observed`, `observed_observed_at`), and `_version`, with
//!    sqlgen's `@optimistic` trigger, which moves it on every update, a raw
//!    one among them. For each use of any version whose behavior declares sub-tables (the
//!    core's `declarations`, `tables`), create a sub-table `<the type's
//!    table>_<member in snake case>_<table in snake case>`, the member being
//!    the use's field, or its behavior's storage key for a type anchor
//!    (`s3_d0_ticket_timers_deadlines`): `<type in snake case>_id` TEXT
//!    referencing the type's table ON DELETE CASCADE, the key's column
//!    (TEXT) and each other declared field's column by its declared type,
//!    with the primary key (row column, key column) and no `id`. When any
//!    use's behavior arms deadlines (`armsDeadlines`), create
//!    `<the type's table>_deadline_queue`, the deadline queue's columns
//!    (D103), its `row_key` TEXT and its `id` with the dialect's UUID
//!    default (Postgres `gen_random_uuid()`, SQLite the expression of
//!    `internal/sqlmigrate/sqlite_types.go`'s `sqliteUUIDDefault`), since an
//!    arm's insert names no `id`. When any use's behavior declares events
//!    (`events`), create `<the type's table>_event_queue`: `id` (the
//!    dialect's UUID default), `row_key` TEXT, `use_id` TEXT, `kind` TEXT,
//!    `at` integer, `payload` JSON, and the D53 lifecycle columns, as the
//!    deadline queue's.
//! 2. Run each step in order. A `command` runs its command, as written
//!    (its `now` and `request` too), through the host loop on the table,
//!    with the type's `versions[version]` as the spec, and compares: the
//!    refusal whole (code, status, class, message, details), or the
//!    result, a create's key, and the row the command named, read back from
//!    the table after the step (its own and owned fields; absent after a
//!    delete), its sub-table rows and armed deadlines, and the other rows it
//!    wrote (`others`). After each command, read each of the scenario's
//!    types' event queues (readQueuedEvents), compare for each type the
//!    list read with the step's `events` of that type, in order, delete the
//!    messages read, and hold that no queue holds an event `events` lacks.
//!    A field with no
//!    value is absent in `row`, and an adapter reads a NULL column as
//!    absent. A `deadline` command runs only while its deadline is armed at
//!    its `at`, which the adapter reads before each run, and its unit
//!    spends that arming by key and `at` right after the row's write. A
//!    `check` runs its command on the core
//!    with no table (the define and publish checks read no row, though a
//!    publish asks whether the type has rows, which the adapter answers
//!    from the step's database's table) and compares the refusal, or no
//!    refusal. A `raw`
//!    step writes the owned columns it names with SQL of the adapter's
//!    test's own, past the host loop, as the escape hatch D103 keeps open,
//!    and expects nothing; the trigger moves the row's version on, as the
//!    host's raw write does, so a later step's `version` holds it.
//! 3. A scenario's `skipped` steps are not in `steps`; they are listed with
//!    the replayer's reasons, or the writer's for a step the replayer
//!    checked that no adapter step maps (an `open`, a read of the host's),
//!    so a reader sees what the transcript leaves out. A scenario the wasm
//!    core cannot run (a test behavior, a permission matcher of the test's
//!    own) holds no types and no steps, and lists each step with the
//!    reason.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use superschematic_engine_testkit::ScenarioFile;
use superschematic_rules::{
    Action, Command, Core, OutboxEvent, Refusal, RowWrite, SubRow, TypeSpec,
};

use crate::host::{Applied, Store, StoredRow};
use crate::replay::replay_recording;
use crate::shape::{FieldDef, RawWrite};

/// One recording's transcript.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Transcript {
    /// The recording's name (`core-workflow`).
    pub file: String,
    /// The recording it was written from, from the checkout's root.
    pub source: String,
    /// The behaviors the core that replays it registers, in its order:
    /// `superschematic_rules_behaviors::NAMES`, what the wasm core runs.
    pub behaviors: Vec<String>,
    pub scenarios: Vec<TranscriptScenario>,
}

/// One scenario: its types, its clock, its steps and what it leaves out.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptScenario {
    pub name: String,
    /// The clock the recording's engine read; every command carries its
    /// own `now`, so an adapter reads no clock of its own.
    pub clock: TranscriptClock,
    /// The types its steps run on, in the order the scenario defined them.
    #[serde(default)]
    pub types: Vec<TranscriptType>,
    #[serde(default)]
    pub steps: Vec<TranscriptStep>,
    /// The steps not in `steps`, with the replayer's reasons.
    #[serde(default)]
    pub skipped: Vec<TranscriptSkip>,
}

/// The clock a scenario's engine read.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum TranscriptClock {
    /// A test's fixed clock, at its first reading in epoch milliseconds.
    Fixed(i64),
    /// The wall clock, whose instants the replayer made strictly
    /// increasing from a start of its own.
    Wall,
}

/// A type a scenario defines: its own fields, which its table holds beside
/// the owned columns, and each version it published, in order.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptType {
    pub name: String,
    /// The own fields every version declares, in the order the first
    /// declared them, a later version's new ones after.
    #[serde(default)]
    pub fields: Vec<TranscriptField>,
    /// Each published version as the core runs it; a command step names
    /// the one live when it ran.
    #[serde(default)]
    pub versions: Vec<TypeSpec>,
}

/// An own field of a type, from the scenario's define document.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptField {
    pub name: String,
    /// Its JSON type: `string`, `number`, `integer`, `boolean`, `object` or
    /// `array`, as the document's field gives it (`typeRef`'s scalar read as
    /// its JSON type).
    #[serde(rename = "type")]
    pub json_type: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub required: bool,
}

fn is_false(flag: &bool) -> bool {
    !*flag
}

/// One step an adapter replays, by the recording's step number (from 1)
/// and call.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
pub enum TranscriptStep {
    /// A command on a type's rows, through the host loop on its table.
    Command {
        step: usize,
        call: String,
        /// The database the recording's engine kept its rows in (its
        /// path): an adapter keeps one set of tables per database.
        database: String,
        #[serde(rename = "type")]
        type_name: String,
        /// The index in the type's `versions` of the one live at the step.
        version: usize,
        command: Command,
        expect: TranscriptExpect,
    },
    /// A define or publish check of a spec, which an adapter runs on the
    /// core with no table: `command`'s action is `define` or `publish`. A
    /// publish asks whether the type has rows, which an adapter answers
    /// from the type's table in `database`.
    Check {
        step: usize,
        call: String,
        database: String,
        spec: TypeSpec,
        command: Command,
        expect: TranscriptExpect,
    },
    /// A write of a row's owned fields past the adapter, as the test's own
    /// SQL made it: by use id, then declared name, null clearing one.
    Raw {
        step: usize,
        call: String,
        database: String,
        #[serde(rename = "type")]
        type_name: String,
        key: String,
        owned: BTreeMap<String, Map<String, Value>>,
    },
}

/// What a step must answer: the refusal whole, or, for an applied command,
/// its result, a create's key and the row after it.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptExpect {
    /// The core's refusal, as the adapter throws it (its class, code,
    /// status, message and details), not the engine's error the replay
    /// writes from it. Absent for a command applied or a check passed.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub refused: Option<Refusal>,
    /// What an applied command returns (the change set's `result`): an
    /// operation's result, null for the rest.
    #[serde(default, skip_serializing_if = "Value::is_null")]
    pub result: Value,
    /// The key of the row a create made, the host's when its command named
    /// none.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key: Option<String>,
    /// The command's row after the step, read back from the table: absent
    /// after a delete and for a check, and the row as it was for a refused
    /// command on a row.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub row: Option<TranscriptRow>,
    /// The command's row's sub-table rows after the step, by use id and
    /// table, each table's rows in key order; a table with none is absent,
    /// and so is the whole when the row has none.
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub sub_rows: BTreeMap<String, BTreeMap<String, Vec<SubRow>>>,
    /// The command's row's armed deadlines after the step, by use id and
    /// then name; absent when it has none.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub deadlines: Vec<TranscriptDeadline>,
    /// The rows other than the command's own that its change set wrote, in
    /// the change set's order, each read back after the step (its row
    /// absent when it was deleted); absent when none.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub others: Vec<TranscriptOther>,
    /// The events the command's change set wrote, in its order (D104): each
    /// the row's type and key, the use, the kind, the command's time and the
    /// payload; absent when none.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub events: Vec<TranscriptEvent>,
    /// The row's version after the step, as the host holds it: written for
    /// a hand-written transcript's commands (D104), whose repeats are held
    /// to writing nothing; absent in a recording's, and after a delete.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub version: Option<i64>,
}

/// A row other than the command's own that its change set wrote, read back
/// after the step.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptOther {
    #[serde(rename = "type")]
    pub type_name: String,
    pub key: String,
    /// The row after the step; absent when the step deleted it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub row: Option<TranscriptRow>,
}

/// An event a command's change set wrote to its row's table's event queue
/// (D104): the row's type and key, the use that emitted it, its kind, the
/// command's time in epoch milliseconds, and its payload.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptEvent {
    #[serde(rename = "type")]
    pub type_name: String,
    pub key: String,
    pub use_id: String,
    pub kind: String,
    pub at: i64,
    #[serde(default)]
    pub payload: Value,
}

/// A deadline armed on a row: its use, its name, the time it falls due,
/// in epoch milliseconds, and the payload it was armed with.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptDeadline {
    pub use_id: String,
    pub name: String,
    pub at: i64,
    #[serde(default)]
    pub payload: Value,
}

/// A row as an adapter reads it back: its own fields and its owned fields
/// by use id and declared name, each field with no value absent.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptRow {
    #[serde(default)]
    pub own: Map<String, Value>,
    #[serde(default)]
    pub owned: BTreeMap<String, Map<String, Value>>,
}

/// A step a transcript leaves out, as the replayer reports it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TranscriptSkip {
    pub step: usize,
    pub call: String,
    pub reason: String,
}

/// The path of a recording's transcript by its name (`core-workflow`):
/// `runtime/engine/testdata/rules/<name>.transcript.json`.
pub fn transcript_path(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/rules")
        .join(format!("{name}.transcript.json"))
}

/// The path of a hand-written scenario file by its name
/// (`core-workflow-principals`):
/// `runtime/engine/testdata/rules/scenarios/<name>.json`, cases written by
/// hand in the recorded format for what the engine recorded none of (D104),
/// never re-recorded.
pub fn hand_written_path(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/rules/scenarios")
        .join(format!("{name}.json"))
}

impl Transcript {
    /// The document as it is checked in: two-space indented JSON with a
    /// final newline.
    pub fn render(&self) -> String {
        let mut text = serde_json::to_string_pretty(self).unwrap_or_default();
        text.push('\n');
        text
    }

    /// The steps an adapter replays, across every scenario.
    pub fn steps(&self) -> usize {
        self.scenarios
            .iter()
            .map(|scenario| scenario.steps.len())
            .sum()
    }

    /// The steps it leaves out, across every scenario.
    pub fn skipped(&self) -> usize {
        self.scenarios
            .iter()
            .map(|scenario| scenario.skipped.len())
            .sum()
    }
}

/// Writes a recording's transcript: replays every scenario of `file` on
/// cores `make_core` builds, as [`crate::replay`] does, and records each
/// step it maps as a command, a check or a raw write with what the host
/// answered, and each step it skips with its reason. It fails on a step
/// whose answer differs from the recording's, since a transcript holds
/// only answers the replay has held to the engine's.
pub fn transcript(file: &ScenarioFile, make_core: &dyn Fn() -> Core) -> Result<Transcript, String> {
    written(
        file,
        make_core,
        false,
        format!("runtime/engine/testdata/scenarios/{}.json", file.name),
    )
}

/// Writes a hand-written scenario file's transcript ([`hand_written_path`],
/// D104) as [`transcript`] writes a recording's, its source the file under
/// `runtime/engine/testdata/rules/scenarios`, with each command's row
/// version after the step in its `expect`. It fails on a step whose answer
/// differs from the one the file writes.
pub fn hand_written_transcript(
    file: &ScenarioFile,
    make_core: &dyn Fn() -> Core,
) -> Result<Transcript, String> {
    written(
        file,
        make_core,
        true,
        format!("runtime/engine/testdata/rules/scenarios/{}.json", file.name),
    )
}

// written writes a scenario file's transcript from its source, with each
// command's row version when `versions` is set.
fn written(
    file: &ScenarioFile,
    make_core: &dyn Fn() -> Core,
    versions: bool,
    source: String,
) -> Result<Transcript, String> {
    let (report, recorded) = replay_recording(file, make_core, Some(versions));
    if !report.mismatches().is_empty() {
        return Err(format!(
            "the replay answered otherwise than the recording, so it writes no transcript:\n{}",
            report.details()
        ));
    }
    let mut scenarios = Vec::new();
    for ((scenario, replayed), recorded) in
        file.scenarios.iter().zip(&report.scenarios).zip(recorded)
    {
        let calls: Vec<String> = scenario
            .get("steps")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .map(|step| {
                step.get("call")
                    .and_then(Value::as_str)
                    .unwrap_or("?")
                    .to_owned()
            })
            .collect();
        let clock = recorded.clock.clone().unwrap_or(TranscriptClock::Wall);
        if let Some(reason) = foreign(scenario) {
            scenarios.push(TranscriptScenario {
                name: replayed.name.clone(),
                clock,
                types: Vec::new(),
                steps: Vec::new(),
                skipped: calls
                    .iter()
                    .enumerate()
                    .map(|(index, call)| TranscriptSkip {
                        step: index + 1,
                        call: call.clone(),
                        reason: reason.clone(),
                    })
                    .collect(),
            });
            continue;
        }
        let held: BTreeSet<usize> = recorded.steps.iter().map(TranscriptStep::step).collect();
        let skipped = calls
            .iter()
            .enumerate()
            .filter(|(index, _)| !held.contains(&(index + 1)))
            .map(|(index, call)| {
                let step = index + 1;
                let reason = replayed
                    .skipped
                    .iter()
                    .find(|skipped| skipped.step == step)
                    .map(|skipped| skipped.reason.clone())
                    .or_else(|| {
                        recorded
                            .left_out
                            .iter()
                            .find(|left| left.step == step)
                            .map(|left| left.reason.clone())
                    })
                    .unwrap_or_else(|| left_out_reason(call).to_owned());
                TranscriptSkip {
                    step,
                    call: call.clone(),
                    reason,
                }
            })
            .collect();
        scenarios.push(TranscriptScenario {
            name: replayed.name.clone(),
            clock,
            types: recorded.types,
            steps: recorded.steps,
            skipped,
        });
    }
    Ok(Transcript {
        file: file.name.clone(),
        source,
        behaviors: superschematic_rules_behaviors::NAMES
            .iter()
            .map(|name| (*name).to_owned())
            .collect(),
        scenarios,
    })
}

// foreign says why the core a transcript is replayed on, the wasm module's
// (superschematic_rules_behaviors::core()), cannot run a scenario the
// reference host replays with a fixture: a test behavior, or a permission
// matcher of the test's own. None for one it runs.
fn foreign(scenario: &Value) -> Option<String> {
    let tests: Vec<&str> = scenario
        .get("testBehaviors")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .collect();
    if !tests.is_empty() {
        return Some(format!(
            "it composes test behaviors ({}), which the reference host runs as fixtures and the core an adapter embeds does not register",
            tests.join(", ")
        ));
    }
    let matcher = scenario
        .get("steps")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter(|step| step.get("call").and_then(Value::as_str) == Some("open"))
        .any(|step| {
            step.get("args")
                .and_then(|args| args.get(0))
                .and_then(|options| options.get("permissionMatcher"))
                .is_some()
        });
    matcher.then(|| {
        "its engine checks permissions with a matcher of the test's own, which the reference host runs as a fixture and the core an adapter embeds does not have".to_owned()
    })
}

// left_out_reason says why a step the replayer checked on the reference
// host is no step of a transcript.
fn left_out_reason(call: &str) -> &'static str {
    match call {
        "open" => "it opens an engine: an adapter keeps one set of tables for each database the steps name",
        "close" => "it closes an engine: an adapter's tables outlive it",
        "instances.get" | "instances.list" => {
            "a read of rows, which the transcript holds after each command that names one"
        }
        "schemas.list" | "schemas.live" | "schemas.behaviors" => {
            "a read of the types the host keeps, which the transcript holds as each type's versions"
        }
        "events.read" => {
            "a read of the engine's event log, which a table has no counterpart of: its writes are heard through its change queue (D103), and the event stream comes later"
        }
        "schemas.publish" => "nothing waited to be published, which the host answers without the core",
        "runner.runDue" => {
            "no change waited and no deadline was armed and due, so the runner ran no command"
        }
        _ => "the replayer checked it on the reference host, and no step of an adapter's maps it",
    }
}

/// What a transcript holds of one scenario, which the replayer records as
/// it makes each step ([`crate::replay`]'s mapping, so a transcript holds
/// the commands the replay ran, never a second mapping of the calls).
#[derive(Clone, Debug, Default)]
pub(crate) struct Recorded {
    /// The clock of the first engine the scenario opened.
    clock: Option<TranscriptClock>,
    types: Vec<TranscriptType>,
    steps: Vec<TranscriptStep>,
    /// Steps the replayer made that the transcript leaves out, with why.
    left_out: Vec<TranscriptSkip>,
    /// Whether each command's row version is written, as a hand-written
    /// file's transcript holds it.
    versions: bool,
}

impl Recorded {
    /// What a transcript holds of a scenario, each command's row version
    /// with it when `versions` is set.
    pub(crate) fn new(versions: bool) -> Self {
        Recorded {
            versions,
            ..Recorded::default()
        }
    }

    /// An engine opened on a clock.
    pub(crate) fn opened(&mut self, clock: TranscriptClock) {
        self.clock.get_or_insert(clock);
    }

    /// A define or publish check, and the refusal it came to.
    pub(crate) fn check(
        &mut self,
        at: &(usize, String),
        database: &str,
        spec: TypeSpec,
        command: Command,
        refused: Option<&Refusal>,
    ) {
        self.steps.push(TranscriptStep::Check {
            step: at.0,
            call: at.1.clone(),
            database: database.to_owned(),
            spec,
            command,
            expect: TranscriptExpect {
                refused: refused.cloned(),
                ..TranscriptExpect::default()
            },
        });
    }

    /// A version published, with its own fields.
    pub(crate) fn published(&mut self, spec: TypeSpec, fields: &[FieldDef]) {
        let at = match self.types.iter().position(|known| known.name == spec.name) {
            Some(at) => at,
            None => {
                self.types.push(TranscriptType {
                    name: spec.name.clone(),
                    fields: Vec::new(),
                    versions: Vec::new(),
                });
                self.types.len() - 1
            }
        };
        let known = &mut self.types[at];
        for field in fields {
            if !known.fields.iter().any(|kept| kept.name == field.name) {
                known.fields.push(TranscriptField {
                    name: field.name.clone(),
                    json_type: field.json_type.clone(),
                    required: field.required,
                });
            }
        }
        if known.versions.last() != Some(&spec) {
            known.versions.push(spec);
        }
    }

    /// A command on a type's rows, as the host ran it, what it came to and
    /// the row it names after it, with the row's sub-table rows and armed
    /// deadlines, from the store the host left.
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn command(
        &mut self,
        at: &(usize, String),
        database: &str,
        type_name: &str,
        live: Option<&TypeSpec>,
        command: Command,
        outcome: &Result<Applied, Refusal>,
        store: &Store,
    ) {
        let key = command.action.key().unwrap_or_default();
        let row = store.row(type_name, key);
        let sub_rows = store.sub_tables(type_name, key);
        let deadlines: Vec<TranscriptDeadline> = store
            .deadlines()
            .iter()
            .filter(|(at, _)| at.type_name == type_name && at.key == key)
            .map(|(at, armed)| TranscriptDeadline {
                use_id: at.use_id.clone(),
                name: at.name.clone(),
                at: armed.at,
                payload: armed.payload.clone(),
            })
            .collect();
        let version = live.and_then(|live| {
            self.types
                .iter()
                .find(|known| known.name == type_name)
                .and_then(|known| known.versions.iter().rposition(|spec| spec == live))
        });
        let Some(version) = version else {
            self.left_out.push(TranscriptSkip {
                step: at.0,
                call: at.1.clone(),
                reason: format!(
                    "{type_name} has no published version, which the reference host refuses before the core runs"
                ),
            });
            return;
        };
        let row_version = row.filter(|_| self.versions).map(|row| row.version);
        let expect = match outcome {
            Err(refusal) => TranscriptExpect {
                refused: Some(refusal.clone()),
                row: row.map(row_of),
                sub_rows,
                deadlines,
                version: row_version,
                ..TranscriptExpect::default()
            },
            Ok(applied) => TranscriptExpect {
                refused: None,
                result: applied.result().clone(),
                key: matches!(command.action, Action::Create { .. }).then(|| applied.key.clone()),
                row: row.map(row_of),
                sub_rows,
                deadlines,
                others: others_of(type_name, &applied.key, &applied.changes.rows, store),
                events: events_of(&applied.key, &command, &applied.changes.events),
                version: row_version,
            },
        };
        self.steps.push(TranscriptStep::Command {
            step: at.0,
            call: at.1.clone(),
            database: database.to_owned(),
            type_name: type_name.to_owned(),
            version,
            command,
            expect,
        });
    }

    /// A raw write of a row's owned fields past the adapter.
    pub(crate) fn raw(&mut self, at: &(usize, String), database: &str, write: RawWrite) {
        self.steps.push(TranscriptStep::Raw {
            step: at.0,
            call: at.1.clone(),
            database: database.to_owned(),
            type_name: write.type_name,
            key: write.key,
            owned: write.owned,
        });
    }
}

impl TranscriptStep {
    /// The recording's step number, from 1.
    pub fn step(&self) -> usize {
        match self {
            TranscriptStep::Command { step, .. }
            | TranscriptStep::Check { step, .. }
            | TranscriptStep::Raw { step, .. } => *step,
        }
    }
}

// others_of is the rows other than the command's own a change set wrote, in
// its order, each once, read back from the store the host left; a row found
// gone is the command's own, which a change set writes no other way.
fn others_of(type_name: &str, key: &str, rows: &[RowWrite], store: &Store) -> Vec<TranscriptOther> {
    let mut others: Vec<TranscriptOther> = Vec::new();
    for write in rows {
        let Some(other) = write.key() else {
            continue;
        };
        let own = write.type_name() == type_name && other == key;
        let listed = others
            .iter()
            .any(|known| known.type_name == write.type_name() && known.key == other);
        if own || listed || matches!(write, RowWrite::Gone { .. }) {
            continue;
        }
        others.push(TranscriptOther {
            type_name: write.type_name().to_owned(),
            key: other.to_owned(),
            row: store.row(write.type_name(), other).map(row_of),
        });
    }
    others
}

// events_of is the events a change set wrote, in its order, each with the
// command's time; a row with no key is the command's own create's, whose
// key the host gave.
fn events_of(key: &str, command: &Command, events: &[OutboxEvent]) -> Vec<TranscriptEvent> {
    events
        .iter()
        .map(|event| TranscriptEvent {
            type_name: event.row.type_name.clone(),
            key: event.row.key.clone().unwrap_or_else(|| key.to_owned()),
            use_id: event.use_id.clone(),
            kind: event.kind.clone(),
            at: command.now,
            payload: event.payload.clone(),
        })
        .collect()
}

// row_of is a row as an adapter reads it back: each field with no value
// absent, and a use with no field that has one absent.
fn row_of(row: &StoredRow) -> TranscriptRow {
    let present = |fields: &Map<String, Value>| -> Map<String, Value> {
        fields
            .iter()
            .filter(|(_, value)| !value.is_null())
            .map(|(name, value)| (name.clone(), value.clone()))
            .collect()
    };
    TranscriptRow {
        own: present(&row.own),
        owned: row
            .owned
            .iter()
            .map(|(use_id, fields)| (use_id.clone(), present(fields)))
            .filter(|(_, fields)| !fields.is_empty())
            .collect(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// The document reads and writes in the shape the module states.
    #[test]
    fn a_transcript_reads_and_writes_as_its_document() {
        let document = json!({
            "file": "core-workflow",
            "source": "runtime/engine/testdata/scenarios/core-workflow.json",
            "behaviors": ["Workflow"],
            "scenarios": [{
                "name": "a transition moves the status",
                "clock": {"fixed": 1000},
                "types": [{
                    "name": "Order",
                    "fields": [{"name": "title", "type": "string", "required": true}],
                    "versions": [{"name": "Order", "uses": [{"behavior": "Workflow", "config": {"states": ["draft"]}, "anchor": "type"}]}]
                }],
                "steps": [
                    {"kind": "check", "step": 2, "call": "schemas.define", "database": "e1",
                     "spec": {"name": "Order", "uses": []},
                     "command": {"caller": {"subject": "alice", "permissions": []}, "now": 1000, "action": {"kind": "define"}},
                     "expect": {}},
                    {"kind": "command", "step": 4, "call": "instances.create", "database": "e1", "type": "Order", "version": 0,
                     "command": {"caller": {"subject": "alice", "permissions": []}, "now": 1000, "action": {"kind": "create", "id": "o1", "own": {"title": "Desk"}, "params": {}}, "request": "r4"},
                     "expect": {"key": "o1", "row": {"own": {"title": "Desk"}, "owned": {"Workflow": {"status": "draft", "enteredAt": 1000}}}}},
                    {"kind": "command", "step": 5, "call": "instances.invoke", "database": "e1", "type": "Order", "version": 0,
                     "command": {"caller": {"subject": "alice", "permissions": []}, "now": 1000, "action": {"kind": "operation", "key": "o1", "useId": "Workflow", "operation": "transition", "params": {"to": "lost"}}, "request": "r5"},
                     "expect": {"refused": {"code": "vetoed", "status": 409, "class": "BehaviorVetoError", "message": "no", "details": {"behavior": "Workflow"}},
                                "row": {"own": {"title": "Desk"}, "owned": {"Workflow": {"status": "draft", "enteredAt": 1000}}}}},
                    {"kind": "raw", "step": 6, "call": "storage.run", "database": "e1", "type": "Order", "key": "o1",
                     "owned": {"Workflow": {"enteredAt": null}}}
                ],
                "skipped": [{"step": 7, "call": "events.read", "reason": "the outbox comes with step 3"}]
            }, {
                "name": "a matcher of the test's own",
                "clock": "wall",
                "skipped": [{"step": 1, "call": "open", "reason": "its permission matcher is the test's"}]
            }]
        });
        let transcript: Transcript = serde_json::from_value(document.clone()).expect("it reads");
        let scenario = &transcript.scenarios[0];
        assert_eq!(scenario.clock, TranscriptClock::Fixed(1000));
        assert_eq!(transcript.scenarios[1].clock, TranscriptClock::Wall);
        let TranscriptStep::Command { expect, .. } = &scenario.steps[2] else {
            panic!("a command");
        };
        assert_eq!(
            expect.refused.as_ref().map(|refusal| refusal.status),
            Some(409)
        );
        let mut written = serde_json::to_value(&transcript).expect("it writes");
        // A scenario with no types or steps writes them empty.
        written["scenarios"][1]
            .as_object_mut()
            .map(|scenario| {
                scenario.remove("types");
                scenario.remove("steps");
            })
            .expect("a scenario");
        assert_eq!(written, document);
        assert!(transcript_path("core-workflow")
            .ends_with("testdata/rules/core-workflow.transcript.json"));
    }
}
