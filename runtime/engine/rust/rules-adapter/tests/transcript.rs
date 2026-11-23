//! The recorded scenarios are the specification (D103): the transcripts
//! `superschematic-rules-testkit` writes from `core-workflow.json`,
//! `core-timers.json`, `core-reactions.json`,
//! `core-reactions-changes.json`, `core-constants.json`,
//! `core-staterules.json`, `core-approvals.json`, `core-rollups.json` and
//! `core-dependencies.json` (D104's step 4), and from the scenarios
//! written by hand in their format where the engine recorded none,
//! `core-workflow-principals.json` (D104: a transition's `by`,
//! `overridePermission`, `repeat` and `from`; D113: `finished`) and
//! `core-freezes-and-sign-offs.json` (D104: Constants' `oneWay`,
//! StateRules' `by`, and Approvals' `resetOn` and `pinned`); from the work
//! queue's recordings of the behaviors D104 ports to tables,
//! `workqueue-lease.json`, `workqueue-presence.json`,
//! `workqueue-budget.json` and `workqueue-retries.json` (`core-lease`,
//! `core-presence`, `core-budget`, `core-retries`); and from their
//! upgrades' scenarios written by hand, `core-retries-waits.json` (Retries'
//! waits and Lease's fencing token, presented as a command's
//! `preconditions`) and `core-periods-and-presence.json` (Budget's periods
//! and observe, and Presence's `ttlByState`); and from the scenarios
//! written by hand of the behaviors only the core implements,
//! `core-seats.json` (D104: Seats' seats, kept in a sub-table, and a `by`
//! naming a seat), `core-reconcile.json` (D104: Reconcile's
//! observations on a field beside a Workflow field, and its stale
//! deadline), `core-schedules.json` (D104: Schedules' named schedules
//! after a delay, at an interval and on a cron line in a time zone, their
//! fires, retries and endOn, and the event each fire emits) and
//! `core-pool.json` (D104: Pool's members out and back, their rechecks and
//! the event each emits, and its pick, a command without a row over a read
//! across the table, Budget's meters read through Capacity), whose
//! hand-written transcripts hold each command's row version too
//! (`runtime/engine/testdata/rules/<name>.transcript.json`, whose format its
//! `transcript` module states) replayed through this adapter on rusqlite
//! and on Postgres, as the HTTP runtime's `src/rules-transcript.test.ts`
//! replays them through TypeScript's. Each scenario gets a table per type
//! and database, its own fields' columns (a link's `<link>Id` among them)
//! beside the owned columns the layout rule gives each use of any version
//! and `_version` with sqlgen's trigger, a sub-table per table a use's
//! behavior declares, each column of its declared type (a `Generic.Int64`
//! a whole number's, D104), a deadline queue when a use's behavior arms
//! deadlines, and an event queue when one emits events (D104); each check
//! runs on the core alone, answering whether the
//! type has rows from its table; each command runs through `execute` in a
//! transaction of its own over a layout of every type of its database, the
//! caller's preconditions it carries passed through to the core as written
//! (D104), the
//! command's type at the version live at it and each other at the version
//! the scenario last made live (a deadline the runner ran among them, run
//! only while it is armed at its time and its message spent with it, a
//! write a type's change trigger queued, run as written, and an operation
//! of the table as a whole, `tableOperation`, which names no row, D104),
//! its refusal or its result compared whole and the row it names, when it
//! names one, read back after it and compared, numbers by value,
//! with its version when the transcript holds one (so a repeat answered
//! unchanged is held to writing nothing), that row's sub-table rows and its
//! armed deadlines, the other rows its change set wrote, and the events it
//! wrote, read from each type's event queue in the order written and then
//! deleted, so no queue holds one the step lacks (D104); and each raw
//! step writes owned columns with
//! SQL past the adapter. The Postgres replay is skipped without
//! `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`.
//!
//! The tables here follow the layout rule as the core's README states it,
//! restated by this test: a type-anchored use's columns are named for its
//! behavior (Workflow's `workflow_status` and `workflow_entered_at`, Timers'
//! sub-table `<table>_timers_deadlines`), and a field-anchored use's for
//! its field, its value field the field's own column (core-reconcile's
//! `status` and `status_entered_at`, `observed` and `observed_observed_at`).
//! The layout the generator writes is held by the generated packages' own
//! end-to-end tests.

#![cfg(any(feature = "sqlite", feature = "postgres"))]

mod common;

use std::collections::{BTreeMap, HashMap, HashSet};

use serde_json::{json, Map, Value};
use superschematic_rules::{Answer, Answers, Core, ReadRequest, Refusal, RowWrite, Step};
use superschematic_rules_adapter::{
    Action, Column, Command, Convert, Deadlines, Error, Events, ExecuteOptions, Executed, Host,
    Layout, Operation, OwnedField, QueuedEvent, Row, SharedTable, SubRow, SubTable, Table,
    TypeSpec, Use,
};
use superschematic_rules_testkit::{
    transcript_path, Transcript, TranscriptExpect, TranscriptScenario, TranscriptStep,
    TranscriptType,
};

use common::{camel, column_types, p, snake, storage_key, Param};

/// The transcripts every dialect replays, each with the steps it holds,
/// the testkit's floor (`tests/transcript.rs`): each dialect replays every
/// one, so a regression cannot pass by skipping.
const TRANSCRIPTS: [(&str, usize); 32] = [
    ("core-workflow", 96),
    ("core-timers", TIMERS_FLOOR),
    ("core-reactions", REACTIONS_FLOOR),
    ("core-reactions-changes", REACTIONS_CHANGES_FLOOR),
    ("core-workflow-principals", WORKFLOW_PRINCIPALS_FLOOR),
    ("core-constants", CONSTANTS_FLOOR),
    ("core-staterules", STATERULES_FLOOR),
    ("core-approvals", APPROVALS_FLOOR),
    ("core-freezes-and-sign-offs", FREEZES_AND_SIGN_OFFS_FLOOR),
    ("core-lease", LEASE_FLOOR),
    ("core-presence", PRESENCE_FLOOR),
    ("core-budget", BUDGET_FLOOR),
    ("core-retries", RETRIES_FLOOR),
    ("core-retries-waits", RETRIES_WAITS_FLOOR),
    ("core-periods-and-presence", PERIODS_AND_PRESENCE_FLOOR),
    ("core-seats", SEATS_FLOOR),
    ("core-reconcile", RECONCILE_FLOOR),
    ("core-schedules", SCHEDULES_FLOOR),
    ("core-pool", POOL_FLOOR),
    ("core-rollups", ROLLUPS_FLOOR),
    ("core-dependencies", DEPENDENCIES_FLOOR),
    ("core-numbering", NUMBERING_FLOOR),
    ("core-lease-numbering", LEASE_NUMBERING_FLOOR),
    ("core-revisions", REVISIONS_FLOOR),
    ("core-comments", COMMENTS_FLOOR),
    ("core-comments-beside", COMMENTS_BESIDE_FLOOR),
    ("core-derived-provenance", DERIVED_PROVENANCE_FLOOR),
    ("core-holds", HOLDS_FLOOR),
    ("core-holds-timers", HOLDS_TIMERS_FLOOR),
    ("core-commerce", COMMERCE_FLOOR),
    ("core-bookings", BOOKINGS_FLOOR),
    ("core-calendar", CALENDAR_FLOOR),
];

/// core-timers' floor, as the core's milestone states it: 139 steps, 85
/// commands (12 of them deadlines and 29 changes the runner ran) and 54
/// checks (D103), the scenario whose fire StateRules vetoes among them
/// since StateRules runs on the core (D104).
const TIMERS_FLOOR: usize = 139;

/// core-reactions' floor, as the core's milestone states it: 618 steps,
/// 504 commands (266 of them changes the runner ran) and 114 checks, the
/// scenario that composes Dependencies among them (D104's step 4).
const REACTIONS_FLOOR: usize = 618;

/// core-reactions-changes' floor, as the core's milestone states it: 62
/// steps, 48 commands (24 of them changes) and 14 checks, the scenario
/// beside Revisions holding its first define and publish (D103's step 4).
const REACTIONS_CHANGES_FLOOR: usize = 62;

/// core-workflow-principals' floor, as the core's milestone states it: 101
/// steps of the scenarios written by hand (D104, and D113's of a state
/// finished names), 63 commands, each holding its row's version, and 38
/// checks. What the transcript leaves out, with its reasons, is the
/// scenario whose gate is a test behavior of the reference host's, and the
/// opens and reads.
const WORKFLOW_PRINCIPALS_FLOOR: usize = 101;

/// core-constants' floor, as the core's milestone states it: 49 steps, 26
/// commands (6 creates, 15 updates, its object, list and string fields
/// among their values, and 5 operations, in the scenario that composes
/// Comments and Revisions, D103's step 4) and 23 checks (D104).
const CONSTANTS_FLOOR: usize = 49;

/// core-staterules' floor, as the core's milestone states it: 88 steps, 49
/// commands (transitions, test.Recorder's operations, each a write of the
/// row's own fields through `update()`, and Revisions' writes beside
/// StateRules') and 39 checks (D104, D103's step 4).
const STATERULES_FLOOR: usize = 88;

/// core-approvals' floor, as the core's milestone states it: 146 steps, 113
/// commands (86 of them operations, the sign-offs kept in a sub-table, and
/// 5 changes) and 33 checks (D104), the scenario beside Comments and
/// Revisions among them (D103's step 4).
const APPROVALS_FLOOR: usize = 146;

/// core-freezes-and-sign-offs' floor, as the core's milestone states it:
/// 115 steps of the scenarios written by hand (D104), 75 commands, each
/// holding its row's version, among them updates of booleans, numbers and
/// date-times, and 40 checks.
const FREEZES_AND_SIGN_OFFS_FLOOR: usize = 115;

/// core-lease's floor, as the core's milestone states it: 474 steps, 354
/// commands (252 operations of a row, 86 of the commands presenting Lease's
/// token as its precondition, 3 deadlines the runner ran: a lease's expiry,
/// and 8 calls of expireHolder, each a command without a row, 2 of them
/// refused, D104) and 120 checks (D104), the scenario that composes
/// Dependencies among them (D104's step 4) and Comments' posts beside the
/// row (D103's step 4); its directives held as sub-table rows and its
/// expiry as an armed deadline. What the recording composed beside Lease
/// that the core does not run (Queue, the runner's sweep cadence) the
/// transcript leaves out with its reasons.
const LEASE_FLOOR: usize = 474;

/// core-presence's floor, as the core's milestone states it: 141 steps, 89
/// commands (8 of them deadlines the runner ran: a miss, 3 of which wrote
/// the rows of the leases it expired, its others) and 52 checks (D104).
const PRESENCE_FLOOR: usize = 141;

/// core-budget's floor, as the core's milestone states it: 495 steps, 331
/// commands (162 operations and 68 updates, 46 of the commands writing a
/// scope's row, their others) and 164 checks (D104); its meters, holds and
/// buckets held as sub-table rows. The scenarios that compose Queue, and the
/// limitField scenarios past their setLimit, the transcript leaves out
/// with its reasons.
const BUDGET_FLOOR: usize = 495;

/// core-retries' floor, as the core's milestone states it: 250 steps, 196
/// commands (149 of them recordAttempt, each attempt a row of its
/// sub-table) and 54 checks (D104). The scenarios that give resultField
/// stop at their define, and those that compose Queue are left out, with
/// their reasons.
const RETRIES_FLOOR: usize = 250;

/// core-retries-waits' floor: 158 steps of the scenarios written by hand
/// (D104), 118 commands, each holding its row's version (24 of them
/// presenting Lease's token as its precondition and 9 deadlines the runner
/// ran: then's moves, one armed again at a status's entry, one another
/// principal's lease refuses and one the window arms again, and a lease's
/// expiry), and 40 checks, the define refusals among them.
const RETRIES_WAITS_FLOOR: usize = 158;

/// core-periods-and-presence's floor, as the core's milestone states it:
/// 77 steps of the scenarios written by hand (D104), 52 commands, each
/// holding its row's version (1 of them a miss the runner ran), and 25
/// checks, the define refusals among them: each reset period, a rolling
/// window across a boundary, observe and its permission, and ttlByState on
/// a beat and on a transition.
const PERIODS_AND_PRESENCE_FLOOR: usize = 77;

/// core-seats' floor, as the core's milestone states it, raised by the
/// update of a fromField held to its seat's max: 88 steps of the scenarios
/// written by hand (D104), 22 define and publish checks, nine of them
/// refused, and 66 commands, each holding its row's version: creates,
/// updates (one refused seat_full, one displacing in its after-change
/// hook), transitions whose `by` names a seat and Seats' operations, its
/// occupants held as sub-table rows, four of the commands repeats answered
/// unchanged.
const SEATS_FLOOR: usize = 88;

/// core-reconcile's floor, as the core's milestone states it: 58 steps of
/// the scenarios written by hand (D104), 22 checks, nine of them refused,
/// and 36 commands, each holding its row's version: creates, an update,
/// Lease's acquire, observations on a field beside a Workflow field, two of
/// them repeats answered unchanged, and 2 stale deadlines the runner ran.
const RECONCILE_FLOOR: usize = 58;

/// core-schedules' floor, as the core's milestone states it: 161 steps of
/// the scenarios written by hand (D104), 29 checks and 131 commands, each
/// holding its row's version (19 of them deadlines the runner ran, each a
/// schedule's fire that emits its event run, and 10 changes, a raw write's
/// among them heard and ending the row's schedules), and 1 raw write; the
/// events each command wrote held to its table's event queue.
const SCHEDULES_FLOOR: usize = 161;

/// core-pool's floor, as the core's milestone states it: 82 steps of the
/// scenarios written by hand (D104), 17 checks, 7 of them refused, and 65
/// commands: 12 creates, 27 operations of a row, each holding its row's
/// version, one of them a repeat answered unchanged, 4 rechecks the runner
/// ran, each emitting its event, and 22 picks, each a command without a row
/// whose result and events the replay compares.
const POOL_FLOOR: usize = 82;

/// core-rollups' floor, as the core's milestone states it: 180 steps, 108
/// commands (the engine's link and unlink as updates of a relation's
/// column, and transitions the gate holds, not_held among the refusals)
/// and 72 checks (D104's step 4); the gate reads the rows that point at the
/// row through a linked read of the layout's relation column.
const ROLLUPS_FLOOR: usize = 180;

/// core-dependencies' floor, as the core's milestone states it: 266 steps,
/// 159 commands (85 creates, a create's blockers among them, 70 operations
/// and 4 deletes) and 107 checks (D104's step 4); the edges held as
/// sub-table rows, read across tables for the gate and the cycle check.
const DEPENDENCIES_FLOOR: usize = 266;

/// core-numbering's floor (D103's step 4): 104 steps, 58 commands and 46
/// checks, each create's number taken from Numbering's shared table of
/// counts, which this replay keeps as a table of its own, and the scenario
/// beside Revisions among them.
const NUMBERING_FLOOR: usize = 104;

/// core-lease-numbering's floor: 14 steps, 8 commands and 6 checks.
const LEASE_NUMBERING_FLOOR: usize = 14;

/// core-revisions' floor (D103's step 4): 112 steps, 75 commands and 37
/// checks, each revision and proposal a row of a sub-table of the use, and
/// each approval an operation's write of the row's own fields.
const REVISIONS_FLOOR: usize = 112;

/// core-comments' floor (D103's step 4): 238 steps, 203 commands and 35
/// checks, each message, edit and reaction a row of a sub-table of the use,
/// each write beside the row, and each conversation's high-water marks a
/// row of the use's shared table.
const COMMENTS_FLOOR: usize = 238;

/// core-comments-beside's floor (D103's step 4): 25 steps, 19 commands and
/// 6 checks, each command's row version held to the one the lease's acquire
/// left, which a write beside the row does not move.
const COMMENTS_BESIDE_FLOOR: usize = 25;

/// core-derived-provenance's floor (D73 on the core, D103's step 4): 75
/// steps of the scenarios written by hand, 49 commands, each holding its
/// row's version, and 26 checks: Derived's derivations and Provenance's
/// records each a row of a sub-table of the use, Derived's pulls and
/// settles commands without a row over a read across the table, a
/// settle's writes of the rows it reaches through applyDerived, and
/// Provenance's inReview an owned column.
const DERIVED_PROVENANCE_FLOOR: usize = 75;

/// core-holds' floor (D70 on the core, D103's step 4): 71 steps of the
/// engine's commerce-holds.json, 55 commands and 16 checks, each take a
/// supply's lock and each supply's write of a claim's item a call on the
/// supply's row, its records and movements rows of its sub-tables.
const HOLDS_FLOOR: usize = 71;

/// core-holds-timers' floor (D70 on the core, D103's step 4): 14 steps, 10
/// commands and 4 checks, a hold let go by the Timers deadline that moves
/// its claim to expired.
const HOLDS_TIMERS_FLOOR: usize = 14;

/// core-commerce's floor (D70 on the core, D103's step 4): 66 steps of the
/// scenarios written by hand, 43 commands and 23 checks: a link hold over a
/// relation, two claims for the last unit, a delete that releases, and the
/// define refusals.
const COMMERCE_FLOOR: usize = 66;

/// core-bookings' floor (D70 on the core, D103's step 4): 83 steps of the
/// engine's commerce-calendar.json, 63 commands and 20 checks, each booking
/// a call on its desk's row under the supply's lock, its records, closures
/// and settings rows of the desk's sub-tables, and findAvailability a
/// command without a row over a read across the desks.
const BOOKINGS_FLOOR: usize = 83;

/// core-calendar's floor (D70 on the core, D103's step 4): 47 steps of the
/// scenarios written by hand, 30 commands and 17 checks: a booking's
/// Workflow anchored on its status field, the one place taken once,
/// closures and the zone refused over held time, and the define refusals.
const CALENDAR_FLOOR: usize = 47;

/// How many runs a check makes before the test calls it unsettled.
const CHECK_ROUNDS: usize = 16;

fn transcript(name: &str) -> Transcript {
    let path = transcript_path(name);
    let text = std::fs::read_to_string(&path)
        .unwrap_or_else(|error| panic!("{}: {error}", path.display()));
    serde_json::from_str(&text).expect("the transcript reads")
}

/// A behavior's declaration file, as much of it as the layout rule reads.
struct Declared {
    name: String,
    value: Option<String>,
    /// Each declared field with a column type: its name and type.
    fields: Vec<(String, String)>,
    operations: Vec<Operation>,
    /// Each sub-table it keeps per row: its name, its key field, and its
    /// other fields with their types.
    tables: Vec<DeclaredTable>,
    /// Whether it arms deadlines, which its table's queue holds.
    arms_deadlines: bool,
    /// Whether it emits events (its declaration lists `events`, D104),
    /// which its table's event queue holds.
    emits: bool,
}

/// A sub-table a declaration declares.
struct DeclaredTable {
    name: String,
    key: String,
    fields: Vec<(String, String)>,
    /// Whether it is kept for the table as a whole (D103's step 4).
    shared: bool,
}

/// The declaration files of the core's behaviors.
fn declarations() -> Vec<Declared> {
    superschematic_rules_behaviors::DECLARATIONS
        .iter()
        .map(|text| {
            let file: Value = serde_json::from_str(text).expect("a declaration file reads");
            let list = |name: &str| {
                file.get(name)
                    .and_then(Value::as_array)
                    .cloned()
                    .unwrap_or_default()
            };
            let typed = |fields: &[Value]| -> Vec<(String, String)> {
                fields
                    .iter()
                    .filter_map(|field| {
                        Some((
                            field["name"].as_str()?.to_owned(),
                            field.get("type")?.as_str()?.to_owned(),
                        ))
                    })
                    .collect()
            };
            Declared {
                name: file["name"].as_str().unwrap_or_default().to_owned(),
                value: file.get("value").and_then(Value::as_str).map(str::to_owned),
                fields: typed(&list("fields")),
                // Each operation of a row and of the table as a whole
                // (D104), the layout's; one of a namespace is left out.
                operations: list("operations")
                    .iter()
                    .filter_map(|operation| {
                        let name = operation["name"].as_str().unwrap_or_default();
                        let writes = operation.get("writes") == Some(&Value::Bool(true));
                        match operation.get("scope").and_then(Value::as_str) {
                            Some("namespace") => None,
                            Some("schema") => Some(Operation::of_table(name, writes)),
                            _ => Some(Operation::new(name, writes)),
                        }
                    })
                    .collect(),
                tables: list("tables")
                    .iter()
                    .map(|table| {
                        let key = table["key"].as_str().unwrap_or_default().to_owned();
                        let fields = table["fields"].as_array().cloned().unwrap_or_default();
                        DeclaredTable {
                            name: table["name"].as_str().unwrap_or_default().to_owned(),
                            fields: typed(&fields)
                                .into_iter()
                                .filter(|(name, _)| *name != key)
                                .collect(),
                            key,
                            shared: table["scope"] == "schema",
                        }
                    })
                    .collect(),
                arms_deadlines: file.get("armsDeadlines") == Some(&Value::Bool(true)),
                emits: !list("events").is_empty(),
            }
        })
        .collect()
}

/// A declared field's column by its declared name and IR type, a
/// date-time held as epoch milliseconds.
fn column_of(name: &str, column: String, type_name: &str) -> Column {
    let column = Column::new(name, column, type_name);
    if type_name == "Temporal.DateTime" {
        return column.converted(Convert::EpochMillis);
    }
    column
}

/// The uses of a type across its versions, each with the owned columns the
/// layout rule gives it, its declared operations and its sub-tables, each
/// `<table>_<member>_<declared name>` in snake case with a column of the
/// row's key, `<type>_id`.
fn uses_of(declared: &[Declared], type_def: &TranscriptType, table: &str) -> Vec<Use> {
    let mut uses: Vec<Use> = Vec::new();
    for version in &type_def.versions {
        for entry in &version.uses {
            let anchor = entry.field();
            let id = entry.id();
            if uses.iter().any(|known| known.id == id) {
                continue;
            }
            let declaration = declared
                .iter()
                .find(|declaration| declaration.name == entry.behavior)
                .unwrap_or_else(|| panic!("the core declares no behavior {}", entry.behavior));
            let fields = declaration
                .fields
                .iter()
                .map(|(name, type_name)| {
                    let value = anchor.is_some() && declaration.value.as_deref() == Some(name);
                    let column = match anchor {
                        None => format!("{}_{}", storage_key(&entry.behavior), snake(name)),
                        Some(anchor) if value => snake(anchor),
                        Some(anchor) => format!("{}_{}", snake(anchor), snake(name)),
                    };
                    OwnedField {
                        name: name.clone(),
                        column: column_of(&camel(&column), column, type_name),
                        value,
                    }
                })
                .collect();
            let member = match anchor {
                Some(anchor) => anchor.to_owned(),
                None => camel(&storage_key(&entry.behavior)),
            };
            let key_of = |declared: &DeclaredTable| OwnedField {
                name: declared.key.clone(),
                column: Column::new(&declared.key, snake(&declared.key), "string"),
                value: false,
            };
            let fields_of = |declared: &DeclaredTable| -> Vec<OwnedField> {
                declared
                    .fields
                    .iter()
                    .map(|(name, type_name)| OwnedField {
                        name: name.clone(),
                        column: column_of(name, snake(name), type_name),
                        value: false,
                    })
                    .collect()
            };
            // A table kept for the table as a whole (D103's step 4): keyed
            // by its declared key alone, with a version of its own.
            let shared = declaration
                .tables
                .iter()
                .filter(|declared| declared.shared)
                .map(|declared| SharedTable {
                    name: declared.name.clone(),
                    table: format!("{table}_{}_{}", snake(&member), snake(&declared.name)),
                    key: key_of(declared),
                    fields: fields_of(declared),
                    version: "_version".to_owned(),
                })
                .collect();
            let tables = declaration
                .tables
                .iter()
                .filter(|declared| !declared.shared)
                .map(|declared| SubTable {
                    name: declared.name.clone(),
                    table: format!("{table}_{}_{}", snake(&member), snake(&declared.name)),
                    row: format!("{}_id", snake(&type_def.name)),
                    key: OwnedField {
                        name: declared.key.clone(),
                        column: Column::new(&declared.key, snake(&declared.key), "string"),
                        value: false,
                    },
                    fields: declared
                        .fields
                        .iter()
                        .map(|(name, type_name)| OwnedField {
                            name: name.clone(),
                            column: column_of(name, snake(name), type_name),
                            value: false,
                        })
                        .collect(),
                })
                .collect();
            uses.push(Use {
                id: id.to_owned(),
                behavior: entry.behavior.clone(),
                member,
                anchor: anchor.map(str::to_owned),
                fields,
                operations: declaration.operations.clone(),
                tables,
                shared,
            });
        }
    }
    uses
}

/// The IR type of an own field of a JSON type.
fn ir_type(json_type: &str) -> &'static str {
    match json_type {
        "string" => "string",
        "number" | "integer" => "number",
        "boolean" => "boolean",
        _ => "Generic.JSON",
    }
}

/// The SQL type of a column in a dialect.
fn sql_type(postgres: bool, column: &Column) -> &'static str {
    let t = column_types(postgres);
    if column.convert == Some(Convert::EpochMillis) {
        return t.timestamp;
    }
    match column.type_name.as_str() {
        "string" => t.text,
        "number" => t.number,
        "Generic.Int64" => t.integer,
        "boolean" => t.boolean,
        _ => t.json,
    }
}

/// Whether a declaration of a behavior any of the uses run arms deadlines.
fn arms_deadlines(declared: &[Declared], uses: &[Use]) -> bool {
    uses.iter().any(|used| {
        declared
            .iter()
            .any(|declaration| declaration.name == used.behavior && declaration.arms_deadlines)
    })
}

/// Whether a declaration of a behavior any of the uses run emits events
/// (D104), which its table's event queue holds.
fn emits(declared: &[Declared], uses: &[Use]) -> bool {
    uses.iter().any(|used| {
        declared
            .iter()
            .any(|declaration| declaration.name == used.behavior && declaration.emits)
    })
}

/// A database of a dialect the replay runs on.
enum Db {
    #[cfg(feature = "sqlite")]
    Sqlite(rusqlite::Connection),
    #[cfg(feature = "postgres")]
    Postgres(tokio_postgres::Client),
}

impl Db {
    fn postgres(&self) -> bool {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(_) => false,
            #[cfg(feature = "postgres")]
            Db::Postgres(_) => true,
        }
    }

    /// Runs DDL or a write of the test's own, and returns the rows it
    /// changed (SQLite's count leaves out a trigger's writes).
    async fn exec(&mut self, text: &str, values: &[Param]) -> u64 {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                conn.execute(
                    text,
                    rusqlite::params_from_iter(common::sqlite_params(values)),
                )
                .unwrap_or_else(|error| panic!("{text}: {error}")) as u64
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let texts = common::pg::texts(values);
                client
                    .execute(text, &common::pg::refs(&texts))
                    .await
                    .unwrap_or_else(|error| panic!("{text}: {error}"))
            }
        }
    }

    async fn has_rows(&mut self, table: &str) -> bool {
        let text = format!("SELECT 1 FROM \"{table}\" LIMIT 1");
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let mut statement = conn.prepare(&text).expect("the read prepares");
                statement.exists([]).expect("the read runs")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => !client
                .query(&text, &[])
                .await
                .expect("the read runs")
                .is_empty(),
        }
    }

    /// Runs a command through the adapter in a transaction of its own,
    /// committed when it applies and rolled back when it is refused.
    async fn execute(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        command: Command,
    ) -> Result<Executed, Error> {
        let options = ExecuteOptions::default();
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let tx = conn.transaction().expect("a transaction");
                let outcome = superschematic_rules_adapter::sqlite::execute(
                    host, &tx, type_name, command, &options,
                );
                if outcome.is_ok() {
                    tx.commit().expect("the transaction commits");
                }
                outcome
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let tx = client.transaction().await.expect("a transaction");
                let outcome = superschematic_rules_adapter::postgres::execute(
                    host, &tx, type_name, command, &options,
                )
                .await;
                if outcome.is_ok() {
                    tx.commit().await.expect("the transaction commits");
                } else {
                    tx.rollback().await.expect("the transaction rolls back");
                }
                outcome
            }
        }
    }

    /// Reads a row's rows of a use's sub-table back in a fresh transaction.
    async fn read_sub_rows(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
        use_id: &str,
        table: &str,
    ) -> Vec<SubRow> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let tx = conn.transaction().expect("a transaction");
                superschematic_rules_adapter::sqlite::read_sub_rows(
                    host, &tx, type_name, key, use_id, table,
                )
                .expect("the sub-table rows read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let tx = client.transaction().await.expect("a transaction");
                superschematic_rules_adapter::postgres::read_sub_rows(
                    host, &tx, type_name, key, use_id, table,
                )
                .await
                .expect("the sub-table rows read")
            }
        }
    }

    /// A row's armed deadlines, its live messages in the queue, by use id
    /// then name: each its use id, name, time and payload.
    async fn armed(&mut self, queue: &str, key: &str) -> Vec<Value> {
        let order = if self.postgres() {
            " COLLATE \"C\""
        } else {
            ""
        };
        let text = format!(
            "SELECT use_id, name, at, CAST(payload AS TEXT) FROM \"{queue}\" WHERE row_key = {} AND state IN ('ready', 'claimed') ORDER BY use_id{order}, name{order}",
            p(self.postgres(), 1)
        );
        let rows: Vec<(String, String, i64, Option<String>)> = match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let mut statement = conn.prepare(&text).expect("the read prepares");
                let rows = statement
                    .query_map([key], |row| {
                        Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?))
                    })
                    .expect("the read runs");
                rows.collect::<Result<_, _>>().expect("the messages read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => client
                .query(&text, &[&key])
                .await
                .expect("the read runs")
                .iter()
                .map(|row| (row.get(0), row.get(1), row.get(2), row.get(3)))
                .collect(),
        };
        rows.into_iter()
            .map(|(use_id, name, at, payload)| {
                let payload = payload
                    .map(|text| serde_json::from_str(&text).expect("a payload of JSON"))
                    .unwrap_or(Value::Null);
                json!({"useId": use_id, "name": name, "at": at, "payload": payload})
            })
            .collect()
    }

    /// A type's queued events, read back through the adapter in a fresh
    /// transaction (D104).
    async fn read_events(&mut self, host: &Host<'_>, type_name: &str) -> Vec<QueuedEvent> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let tx = conn.transaction().expect("a transaction");
                superschematic_rules_adapter::sqlite::read_queued_events(host, &tx, type_name)
                    .expect("the events read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let tx = client.transaction().await.expect("a transaction");
                superschematic_rules_adapter::postgres::read_queued_events(host, &tx, type_name)
                    .await
                    .expect("the events read")
            }
        }
    }

    /// Reads a row back in a fresh transaction.
    async fn read_row(&mut self, host: &Host<'_>, type_name: &str, key: &str) -> Option<Row> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let tx = conn.transaction().expect("a transaction");
                superschematic_rules_adapter::sqlite::read_row(host, &tx, type_name, key)
                    .expect("the row reads")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let tx = client.transaction().await.expect("a transaction");
                superschematic_rules_adapter::postgres::read_row(host, &tx, type_name, key)
                    .await
                    .expect("the row reads")
            }
        }
    }
}

/// How many steps of each kind a replay ran, and what the transcript left
/// out.
#[derive(Default)]
struct Counts {
    commands: usize,
    checks: usize,
    raws: usize,
    skipped: usize,
}

/// One scenario's replay, its tables named `<prefix>_s<index>_d<database>_..`.
struct Scenario<'s> {
    prefix: &'s str,
    index: usize,
    scenario: &'s TranscriptScenario,
    databases: Vec<&'s str>,
    declared: &'s [Declared],
    created: HashSet<String>,
    /// The index of each type's version the scenario last made live.
    live: HashMap<String, usize>,
    mismatches: Vec<String>,
}

impl<'s> Scenario<'s> {
    fn new(
        prefix: &'s str,
        index: usize,
        scenario: &'s TranscriptScenario,
        declared: &'s [Declared],
    ) -> Self {
        let mut databases: Vec<&str> = Vec::new();
        for step in &scenario.steps {
            let database = database_of(step);
            if !databases.contains(&database) {
                databases.push(database);
            }
        }
        Scenario {
            prefix,
            index,
            scenario,
            databases,
            declared,
            created: HashSet::new(),
            live: HashMap::new(),
            mismatches: Vec::new(),
        }
    }

    /// The table of a type in one of the scenario's databases, with a
    /// version's spec.
    fn table_of(&self, database: &str, type_name: &str, spec: Option<&TypeSpec>) -> Table {
        let type_def = self
            .scenario
            .types
            .iter()
            .find(|known| known.name == type_name);
        let at = self
            .databases
            .iter()
            .position(|known| *known == database)
            .unwrap_or(0);
        let table = format!("{}_s{}_d{at}_{}", self.prefix, self.index, snake(type_name));
        let uses = type_def
            .map(|known| uses_of(self.declared, known, &table))
            .unwrap_or_default();
        let deadlines = arms_deadlines(self.declared, &uses).then(|| Deadlines {
            queue: format!("{type_name}Deadline"),
            table: format!("{table}_deadline_queue"),
            row_key: "row_key".into(),
            use_id: "use_id".into(),
            name: "name".into(),
            at: "at".into(),
            payload: "payload".into(),
        });
        let events = emits(self.declared, &uses).then(|| Events {
            queue: format!("{type_name}Event"),
            table: format!("{table}_event_queue"),
            row_key: "row_key".into(),
            use_id: "use_id".into(),
            kind: "kind".into(),
            at: "at".into(),
            payload: "payload".into(),
        });
        Table {
            type_name: type_name.to_owned(),
            table,
            key: Column::new("id", "id", "string"),
            version: "_version".into(),
            live: None,
            own: type_def
                .map(|known| {
                    known
                        .fields
                        .iter()
                        .map(|field| {
                            Column::new(&field.name, &field.name, ir_type(&field.json_type))
                        })
                        .collect()
                })
                .unwrap_or_default(),
            audit: Vec::new(),
            uses,
            deadlines,
            changes: None,
            events,
            spec: spec
                .cloned()
                .or_else(|| type_def.and_then(|known| known.versions.first().cloned()))
                .unwrap_or_else(|| TypeSpec::new(type_name)),
        }
    }

    /// The table, created the first time a step needs it.
    async fn ensure(&mut self, db: &mut Db, table: &Table) {
        if !self.created.insert(table.table.clone()) {
            return;
        }
        let postgres = db.postgres();
        let t = column_types(postgres);
        let mut columns = vec![format!("id {}", t.key)];
        for column in table.own.iter().chain(
            table
                .uses
                .iter()
                .flat_map(|used| used.fields.iter().map(|field| &field.column)),
        ) {
            columns.push(format!(
                "\"{}\" {}",
                column.column,
                sql_type(postgres, column)
            ));
        }
        columns.push(format!("_version {}", t.version));
        db.exec(
            &format!("CREATE TABLE \"{}\" ({})", table.table, columns.join(", ")),
            &[],
        )
        .await;
        if postgres {
            for ddl in common::postgres_bump(&table.table) {
                db.exec(&ddl, &[]).await;
            }
        } else {
            db.exec(&common::sqlite_bump(&table.table), &[]).await;
        }
        // Each sub-table, its rows deleted with the row's, and keyed by the
        // row and their key.
        for sub in table.uses.iter().flat_map(|used| &used.tables) {
            let mut columns = vec![
                format!(
                    "\"{}\" TEXT NOT NULL REFERENCES \"{}\" (id) ON DELETE CASCADE",
                    sub.row, table.table
                ),
                format!("\"{}\" TEXT NOT NULL", sub.key.column.column),
            ];
            for field in &sub.fields {
                columns.push(format!(
                    "\"{}\" {}",
                    field.column.column,
                    sql_type(postgres, &field.column)
                ));
            }
            columns.push(format!(
                "PRIMARY KEY (\"{}\", \"{}\")",
                sub.row, sub.key.column.column
            ));
            db.exec(
                &format!("CREATE TABLE \"{}\" ({})", sub.table, columns.join(", ")),
                &[],
            )
            .await;
        }
        // Each shared table (D103's step 4), keyed by its key alone and
        // held to a version of its own.
        for shared in table.uses.iter().flat_map(|used| &used.shared) {
            let mut columns = vec![format!("\"{}\" {}", shared.key.column.column, t.key)];
            for field in &shared.fields {
                columns.push(format!(
                    "\"{}\" {}",
                    field.column.column,
                    sql_type(postgres, &field.column)
                ));
            }
            columns.push(format!("\"{}\" {}", shared.version, t.version));
            db.exec(
                &format!("CREATE TABLE \"{}\" ({})", shared.table, columns.join(", ")),
                &[],
            )
            .await;
            if postgres {
                for ddl in common::postgres_bump(&shared.table) {
                    db.exec(&ddl, &[]).await;
                }
            } else {
                db.exec(&common::sqlite_bump(&shared.table), &[]).await;
            }
        }
        if let Some(queue) = &table.deadlines {
            for ddl in common::deadline_queue_ddl(postgres, &queue.table, "TEXT") {
                db.exec(&ddl, &[]).await;
            }
        }
        if let Some(queue) = &table.events {
            for ddl in common::event_queue_ddl(postgres, &queue.table, "TEXT") {
                db.exec(&ddl, &[]).await;
            }
        }
    }

    fn compare(&mut self, step: usize, call: &str, what: &str, expected: Value, actual: Value) {
        if !same(&expected, &actual) {
            self.mismatches.push(format!(
                "step {step} ({call}) {what}:\n    expected {expected}\n    actual   {actual}"
            ));
        }
    }

    async fn replay(&mut self, db: &mut Db, core: &Core, counts: &mut Counts) {
        for step in &self.scenario.steps {
            match step {
                TranscriptStep::Check { .. } => {
                    counts.checks += 1;
                    self.check(db, core, step).await;
                }
                TranscriptStep::Raw { .. } => {
                    counts.raws += 1;
                    self.raw(db, step).await;
                }
                TranscriptStep::Command { .. } => {
                    counts.commands += 1;
                    self.command(db, core, step).await;
                }
            }
        }
    }

    /// A check, on the core alone: a publish asks whether the type has
    /// rows, which its table in the step's database answers.
    async fn check(&mut self, db: &mut Db, core: &Core, step: &TranscriptStep) {
        let TranscriptStep::Check {
            step: at,
            call,
            database,
            spec,
            command,
            expect,
        } = step
        else {
            return;
        };
        // A check that passes makes its spec the type's live version, as
        // the define or publish the recording made after it.
        if expect.refused.is_none() {
            if let Some(index) = self
                .scenario
                .types
                .iter()
                .find(|known| known.name == spec.name)
                .and_then(|known| known.versions.iter().position(|version| version == spec))
            {
                self.live.insert(spec.name.clone(), index);
            }
        }
        let table = self.table_of(database, &spec.name, None);
        self.ensure(db, &table).await;
        let mut answers = Answers::new();
        let mut refused: Option<Refusal> = None;
        for round in 0.. {
            assert!(round < CHECK_ROUNDS, "step {at}: the check never settled");
            match core.run(spec, command, &answers) {
                Step::Done(outcome) => {
                    refused = outcome.refusal().cloned();
                    break;
                }
                Step::Read(requests) => {
                    for request in requests {
                        let ReadRequest::HasRows { type_name } = &request else {
                            panic!("step {at}: a check asked for {request:?}");
                        };
                        let asked = self.table_of(database, type_name, None);
                        self.ensure(db, &asked).await;
                        let has = db.has_rows(&asked.table).await;
                        answers.answer(request, Answer::HasRows(has));
                    }
                }
                other => panic!("step {at}: a check asked for {other:?}"),
            }
        }
        // A use a published version drops loses its shared tables' rows, as
        // the migration that drops its tables does (D103's step 4).
        if refused.is_none() && matches!(command.action, Action::Publish { .. }) {
            for used in table
                .uses
                .iter()
                .filter(|used| !spec.uses.iter().any(|entry| entry.id() == used.id))
            {
                for shared in &used.shared {
                    db.exec(&format!("DELETE FROM \"{}\"", shared.table), &[])
                        .await;
                }
            }
        }
        self.compare(*at, call, "refused", json!(expect.refused), json!(refused));
    }

    /// A raw write past the adapter, as an operator's repair or the test's
    /// own SQL: the escape hatch D103 keeps open.
    async fn raw(&mut self, db: &mut Db, step: &TranscriptStep) {
        let TranscriptStep::Raw {
            step: at,
            call,
            database,
            type_name,
            key,
            owned,
        } = step
        else {
            return;
        };
        let table = self.table_of(database, type_name, None);
        self.ensure(db, &table).await;
        let postgres = db.postgres();
        let mut sets = Vec::new();
        let mut values = Vec::new();
        for (use_id, fields) in owned {
            let used = table
                .uses
                .iter()
                .find(|used| used.id == *use_id)
                .expect("a use the layout has");
            for (name, value) in fields {
                let field = used
                    .fields
                    .iter()
                    .find(|field| field.name == *name)
                    .expect("a field the use has");
                values.push(match value {
                    Value::Null => Param::Null,
                    Value::Number(millis) if field.column.convert == Some(Convert::EpochMillis) => {
                        Param::text(common::iso(millis.as_i64().expect("epoch milliseconds")))
                    }
                    Value::String(text) => Param::text(text.clone()),
                    other => Param::text(other.to_string()),
                });
                sets.push(format!(
                    "\"{}\" = {}",
                    field.column.column,
                    p(postgres, values.len())
                ));
            }
        }
        values.push(Param::text(key.clone()));
        let text = format!(
            "UPDATE \"{}\" SET {} WHERE id = {}",
            table.table,
            sets.join(", "),
            p(postgres, values.len())
        );
        let changed = db.exec(&text, &values).await;
        self.compare(*at, call, "raw.changes", json!(1), json!(changed));
    }

    /// A command through the adapter on the version live at it, and the
    /// row it names read back after it.
    async fn command(&mut self, db: &mut Db, core: &Core, step: &TranscriptStep) {
        let TranscriptStep::Command {
            step: at,
            call,
            database,
            type_name,
            version,
            command,
            expect,
        } = step
        else {
            return;
        };
        let type_def = self
            .scenario
            .types
            .iter()
            .find(|known| known.name == *type_name);
        let spec = type_def.and_then(|known| known.versions.get(*version));
        self.live.insert(type_name.clone(), *version);
        let table = self.table_of(database, type_name, spec);
        self.ensure(db, &table).await;
        // Every other type of the database at the version the scenario
        // last made live: what a listener or a rule's move reads of it.
        let mut tables = vec![table];
        for other in &self.scenario.types {
            if other.name == *type_name {
                continue;
            }
            let spec = self
                .live
                .get(&other.name)
                .and_then(|at| other.versions.get(*at));
            let table = self.table_of(database, &other.name, spec);
            self.ensure(db, &table).await;
            tables.push(table);
        }
        let layout = Layout::new(tables);
        let host = Host::new(core, &layout);
        let (refused, result, key, written) =
            match db.execute(&host, type_name, command.clone()).await {
                Ok(executed) => (None, executed.result, executed.key, executed.changes.rows),
                Err(Error::Refused(refusal)) => (Some(*refusal), Value::Null, None, Vec::new()),
                Err(error) => panic!("step {at} ({call}): {error}"),
            };
        self.compare(*at, call, "refused", json!(expect.refused), json!(refused));
        self.compare(*at, call, "result", expect.result.clone(), result);
        if let Some(expected) = &expect.key {
            self.compare(*at, call, "key", json!(expected), json!(key));
        }
        // An operation of the table as a whole names no row (D104): its
        // step holds none to read back, and every row its change set wrote
        // is among its others.
        let row_key = match &command.action {
            Action::Create { id, .. } => id.clone(),
            action => action.key().map(str::to_owned),
        };
        if let Some(row_key) = row_key.filter(|key| !key.is_empty()) {
            let row = db.read_row(&host, type_name, &row_key).await;
            let version = row.as_ref().map(|row| row.version);
            let actual = row.map(|row| json!({"own": row.own, "owned": row.owned}));
            self.compare(*at, call, "row", json!(expect.row), json!(actual));
            // The version a hand-written step holds (D104), which a repeat
            // that writes nothing leaves where the step before it left it.
            if let Some(held) = expect.version {
                self.compare(*at, call, "version", json!(held), json!(version));
            }
            // The row's sub-table rows and its armed deadlines, each absent
            // when it has none.
            let expected = serde_json::to_value(expect).expect("an expectation of JSON");
            let table = layout.table(type_name).expect("the step's table");
            let mut sub_rows = Map::new();
            for used in &table.uses {
                let mut tables = BTreeMap::new();
                for sub in &used.tables {
                    let rows = db
                        .read_sub_rows(&host, type_name, &row_key, &used.id, &sub.name)
                        .await;
                    if !rows.is_empty() {
                        tables.insert(sub.name.clone(), rows);
                    }
                }
                if !tables.is_empty() {
                    sub_rows.insert(used.id.clone(), json!(tables));
                }
            }
            let actual = (!sub_rows.is_empty()).then_some(Value::Object(sub_rows));
            self.compare(
                *at,
                call,
                "subRows",
                json!(expected.get("subRows")),
                json!(actual),
            );
            let armed = match &table.deadlines {
                Some(queue) => db.armed(&queue.table, &row_key).await,
                None => Vec::new(),
            };
            let actual = (!armed.is_empty()).then_some(Value::Array(armed));
            self.compare(
                *at,
                call,
                "deadlines",
                json!(expected.get("deadlines")),
                json!(actual),
            );
        }
        // The other rows the change set wrote, each once, in its order,
        // read back after the step: a row a rule moved through a relation.
        let own_key = match &command.action {
            Action::Create { id, .. } => id.clone().or(key.clone()),
            action => action.key().map(str::to_owned),
        };
        let others = others_of(&written, type_name, own_key.as_deref());
        let mut actual = Vec::new();
        for (other_type, other_key) in others {
            let mut entry = json!({"type": other_type, "key": other_key});
            if let Some(row) = db.read_row(&host, &other_type, &other_key).await {
                entry["row"] = json!({"own": row.own, "owned": row.owned});
            }
            actual.push(entry);
        }
        let expected = serde_json::to_value(expect).expect("an expectation of JSON");
        let actual = (!actual.is_empty()).then_some(Value::Array(actual));
        self.compare(
            *at,
            call,
            "others",
            json!(expected.get("others")),
            json!(actual),
        );
        self.compare_events(db, &host, &layout, *at, call, expect)
            .await;
    }

    /// The events a command's change set wrote (D104), read from each
    /// type's event queue in the order written and compared with the
    /// step's of that type, then deleted, so each step's are its own and no
    /// queue holds one the step lacks; an event the step holds of a type
    /// whose table keeps no event queue is a mismatch too.
    async fn compare_events(
        &mut self,
        db: &mut Db,
        host: &Host<'_>,
        layout: &Layout,
        at: usize,
        call: &str,
        expect: &TranscriptExpect,
    ) {
        for table in layout.types.values() {
            let Some(queue) = &table.events else {
                continue;
            };
            let read = db.read_events(host, &table.type_name).await;
            let actual: Vec<Value> = read
                .into_iter()
                .map(|event| {
                    json!({
                        "type": table.type_name,
                        "key": event.key,
                        "useId": event.use_id,
                        "kind": event.kind,
                        "at": event.at,
                        "payload": event.payload,
                    })
                })
                .collect();
            let expected: Vec<Value> = expect
                .events
                .iter()
                .filter(|event| event.type_name == table.type_name)
                .map(|event| serde_json::to_value(event).expect("an event of JSON"))
                .collect();
            self.compare(
                at,
                call,
                &format!("events of {}", table.type_name),
                Value::Array(expected),
                Value::Array(actual),
            );
            db.exec(&format!("DELETE FROM \"{}\"", queue.table), &[])
                .await;
        }
        for event in &expect.events {
            if !layout
                .table(&event.type_name)
                .is_some_and(|table| table.events.is_some())
            {
                self.mismatches.push(format!(
                    "step {at} ({call}) events: the step writes an event of {}, whose table keeps no event queue",
                    event.type_name
                ));
            }
        }
    }
}

/// The rows other than the command's own, `own_key` of `type_name`, that
/// its change set wrote, each once, in its order: each its type and key.
fn others_of(
    written: &[RowWrite],
    type_name: &str,
    own_key: Option<&str>,
) -> Vec<(String, String)> {
    let mut others: Vec<(String, String)> = Vec::new();
    for write in written {
        let (Some(write_key), write_type) = (write.key(), write.type_name()) else {
            continue;
        };
        let other = (write_type.to_owned(), write_key.to_owned());
        let mine = write_type == type_name && own_key == Some(write_key);
        if !mine && !others.contains(&other) {
            others.push(other);
        }
    }
    others
}

/// Whether two JSON values are one value, as the core compares them (D104):
/// numbers by value, so a `5` the transcript holds is the `5.0` a column
/// may give back; objects by member, whatever their order; arrays in
/// order; the rest as they are. A refusal's message is text, compared as
/// it is.
fn same(expected: &Value, actual: &Value) -> bool {
    match (expected, actual) {
        (Value::Number(a), Value::Number(b)) => {
            a == b
                || match (a.as_i64(), b.as_i64()) {
                    (Some(a), Some(b)) => a == b,
                    _ => a.as_f64().is_some() && a.as_f64() == b.as_f64(),
                }
        }
        (Value::Array(a), Value::Array(b)) => {
            a.len() == b.len() && a.iter().zip(b).all(|(a, b)| same(a, b))
        }
        (Value::Object(a), Value::Object(b)) => {
            a.len() == b.len()
                && a.iter()
                    .all(|(name, a)| b.get(name).is_some_and(|b| same(a, b)))
        }
        _ => expected == actual,
    }
}

fn database_of(step: &TranscriptStep) -> &str {
    match step {
        TranscriptStep::Command { database, .. }
        | TranscriptStep::Check { database, .. }
        | TranscriptStep::Raw { database, .. } => database,
    }
}

/// Replays every transcript on db, each in a namespace of tables of its
/// own.
#[cfg(feature = "sqlite")]
async fn replay(mut db: Db, dialect: &str) {
    for (index, (name, floor)) in TRANSCRIPTS.into_iter().enumerate() {
        replay_one(&mut db, dialect, &format!("t{index}"), name, floor).await;
    }
}

/// Replays every scenario of a transcript on db, failing on any mismatch,
/// listed in full by scenario, or on fewer steps replayed than its floor.
async fn replay_one(db: &mut Db, dialect: &str, prefix: &str, name: &str, floor: usize) {
    let transcript = transcript(name);
    let declared = declarations();
    let core = superschematic_rules_behaviors::core();
    let mut counts = Counts::default();
    let mut failed = Vec::new();
    for (index, scenario) in transcript.scenarios.iter().enumerate() {
        counts.skipped += scenario.skipped.len();
        let mut replaying = Scenario::new(prefix, index, scenario, &declared);
        replaying.replay(db, &core, &mut counts).await;
        if !replaying.mismatches.is_empty() {
            failed.push(format!(
                "{}:\n  {}",
                scenario.name,
                replaying.mismatches.join("\n  ")
            ));
        }
    }
    let replayed = counts.commands + counts.checks + counts.raws;
    println!(
        "{} on {dialect}: {} scenarios, {replayed} steps replayed ({} commands, {} checks, {} raw writes), {} left out by the transcript with its reasons",
        transcript.file,
        transcript.scenarios.len(),
        counts.commands,
        counts.checks,
        counts.raws,
        counts.skipped
    );
    assert!(
        failed.is_empty(),
        "{} scenarios answered otherwise than the transcript:\n{}",
        failed.len(),
        failed.join("\n")
    );
    assert!(
        replayed >= floor,
        "{replayed} steps of {name} replayed, under the floor of {floor}"
    );
    assert_eq!(
        replayed,
        transcript.steps(),
        "every step the transcript holds is replayed"
    );
}

#[cfg(feature = "sqlite")]
#[tokio::test]
async fn transcript_replays_on_sqlite() {
    let conn = rusqlite::Connection::open_in_memory().expect("SQLite opens");
    replay(Db::Sqlite(conn), "SQLite").await;
}

#[cfg(feature = "postgres")]
#[tokio::test]
async fn transcript_replays_on_postgres() {
    let Some(url) = common::pg::dsn(
        "set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to replay the transcript on Postgres",
    ) else {
        return;
    };
    // Each transcript in a schema of its own: every transcript's tables in
    // one schema are more objects than its DROP SCHEMA may lock in one
    // transaction under Postgres's default max_locks_per_transaction.
    for (index, (name, floor)) in TRANSCRIPTS.into_iter().enumerate() {
        common::pg::on_schema(url.clone(), move |schema| async move {
            let mut db = Db::Postgres(schema.client().await);
            replay_one(&mut db, "Postgres", &format!("t{index}"), name, floor).await;
        })
        .await;
    }
}
