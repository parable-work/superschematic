//! Timers, named deadlines on a row (D56), ported onto the pure core (D103)
//! from the engine's (`../behaviors/src/timers.rs`): a ticket answered
//! within four hours of entering open, an order paid within thirty minutes
//! of its create, a reminder a day before an appointment. A deadline starts
//! from a fact of the row: its create (`{ create: true }`), each entry of
//! its Workflow status into a state (`{ enters }`), or the time an own field
//! holds (`{ field }`), one whose type's values are instants
//! ([`ConfigTarget::fields`], the spec's `dateTime`). It is due `afterMs`
//! after the start, or before it with a negative `afterMs`, which only a
//! field start takes, and when it comes due it fires, once.
//!
//! Timers is a behavior by D103's test. It has state of its own: its
//! `deadlines` sub-table, a row per started deadline, and the deadlines it
//! arms. It has rules over how that state changes: a deadline starts and
//! arms when its fact changes, a status that leaves its state cancels it, it
//! fires once when it falls due, and a caller with the config's permission
//! resets or snoozes it. And it recurs: an SLA, an expiring hold, a
//! reminder.
//!
//! A fire marks the deadline fired and, with `then`, moves the status
//! through Workflow's transition while the status is one of then's `from`:
//! the state the deadline starts in unless the config names others (the
//! enters state, or the Workflow's initial state for a create start; a
//! field start names them), so an item resolved in time is never moved.
//! Whatever the move meets, a guard's veto or a transition's permission the
//! caller lacks (forbidden), the deadline is fired, and its outcome records
//! which: moved, skipped (the status was not one of from), vetoed or
//! forbidden. So one stuck row never holds the others back, and a guard
//! that refused is not asked again. `until` cancels a pending deadline once
//! a change leaves the status out of the state it starts in (`leaves`) or
//! in one of its states (`enters`). `parse_config` requires a Workflow on
//! the type for an enters start, a then and an until, which the
//! declaration's requires cannot say, since a field deadline needs none.
//!
//! Which Workflow a deadline reads and moves is the config's `status`, the
//! id of a Workflow use of the type: the field it is anchored on
//! (`status: 'payment'`), or `Workflow` for one anchored on the type. A type
//! with one Workflow implies it; one with several names it. The engine read
//! the instance's one `Workflow`.
//!
//! A deadline arms when the fact it starts from changes, which the
//! after-change hook reads from the row as the change leaves it, never from
//! which operation ran: a Reactions move and a fire move the status as a
//! caller's transition does. The fact is the create's time, the Workflow's
//! `enteredAt` while the status is the enters state, or the field's value,
//! read from the row (`get_of(status, "status")`, `get_of(status,
//! "enteredAt")`, `own()`), and the sub-table row keeps it (`armedFrom`). A
//! write that leaves it as it is arms nothing, so a snooze or a reset
//! outlives it; a status that left the state and came back within one call
//! has a new `enteredAt`, which arms again. A create arms whatever the list
//! order, since every initializer has run by the after-change hooks. A
//! field moved arms again and clears fired; a field cleared deletes its
//! deadline. A due time already past falls due at once. An enters deadline
//! does not arm on a row whose status predates Workflow's `enteredAt`, until
//! its next entry.
//!
//! The `deadlines` sub-table holds a row per started deadline, keyed by the
//! deadline's name: the time it starts from (`baseAt`), the fact
//! (`armedFrom`), when it is due (`dueAt`, null once until cancelled it),
//! whether and when it fired (`fired`, `firedAt`) and its outcome. A read of
//! the row shows them as the engine's `Timers.deadlines` field
//! ([`deadlines_field`], through [`Behavior::read_fields`]): `{ <name>: {
//! dueAt, fired, firedAt?, outcome? } }`, no deadline that has not started
//! or was cancelled. Nothing in it follows the clock.
//!
//! Each pending deadline is armed in the core (`Cx::arm(name, dueAt,
//! null)`) whenever its sub-table row is armed or snoozed, and cleared
//! (`Cx::clear`) whenever it fires, is cancelled or is deleted, so a
//! deadline is armed exactly while its sub-table row is pending (`dueAt`
//! set and not fired), at its `dueAt`. The engine's runner swept the due
//! deadlines every `sweepMs`; on a table, the API's worker claims each
//! armed deadline as it falls due and runs the core's deadline command
//! (D53, D103), so `sweepMs` is parsed and not read. A deadline that falls
//! due calls the use's own `fireTimer` (`Cx::call`), so every use's guards
//! run as the sweep's invoke ran them, and passes on any refusal: a guard's
//! veto of the fire refuses the command, which leaves the deadline armed,
//! and the worker releases its message to fall due again a minute on (D53,
//! D103), as the sweep tried it at its next run. `fireTimer` fires one deadline that is due and has not fired, and is
//! `Cx::unchanged()` on any other, so the worker and a caller never fire one
//! twice; one whose until holds when it comes due is cancelled instead. A
//! caller's fire before the worker's clears the deadline, which spends its
//! armed message. `resetTimer` arms a deadline again from what it starts
//! from now, and `snoozeTimer` moves a started one to an absolute `dueAt`, a
//! fired one firing again then; both need the config's permission
//! (forbidden without it, and for everyone when the config names none), and
//! both are refused a deadline with no due time to give (`not_armed`).
//! Deleting the row deletes its sub-table rows and its deadlines, which the
//! host's delete does.
//!
//! A create start's fact is the sub-table row's `baseAt` once it has one,
//! and before that the create's time, which the after-change hook of a
//! create through the core reads, as does a heard insert (below). A row
//! created before Timers joined its table has no create time: it gets no
//! create deadline, and `resetTimer` of one refuses `not_armed`. The engine
//! read the instance's `createdAt`.
//!
//! Timers hears its row's writes (D103: its declaration's `hearsChanges`),
//! whoever makes them: a raw SQL write, a repository's bulk or hard write and
//! a write through the core alike reach it from the table's change queue,
//! which the API's worker runs after the commit ([`Behavior::heard`]). A
//! heard write runs the reconcile an after-change hook runs, on the row as
//! it is now, so a raw write that moves a field start or the status arms,
//! moves or clears the deadline, and a raw insert arms a create deadline
//! from the insert's time; a write the core made already left the row
//! reconciled, and hearing it changes nothing. A row that is gone runs no
//! hook: the core's change command writes it gone, and the host takes its
//! deadlines. And a deadline that falls due reconciles its row before it
//! fires, so one whose fact a raw write moved, before that write's change
//! ran, is armed again at its new time rather than fired on its old arming;
//! for a row only the core wrote this changes nothing.
//!
//! A field's time is epoch milliseconds, as a table's adapter reads a
//! date-time column, or ISO 8601 text, as an engine schema's date-time
//! holds it, read as JavaScript's `Date.parse` reads the ISO 8601 forms
//! ([`parse_date_time`]); a time without an offset is read as UTC, where
//! JavaScript reads the host's local time. Its fact is the number's
//! decimal text, or the text.
//!
//! configChange: any; Timers joins and leaves a type with rows. The engine's
//! afterConfigChange rewrote every instance's deadlines at the publish; the
//! core writes no row at a define or a publish, since a build-time
//! project's config change is a build and D27 plans no data migration of a
//! sub-table. A row's deadlines reconcile at its next write through the
//! core instead: one whose fact changed arms again, and one the config no
//! longer names is deleted and cleared. A deadline the config no longer
//! names that falls due deletes its sub-table row and changes nothing else.
//! A pending deadline keeps the due time it was armed with until what it
//! starts from moves, though a new `afterMs` would move it. On a type that
//! composes Lease, `Timers.fireTimer` belongs in Lease's exempt, or the
//! holder's lease refuses the worker's fire.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, each issue at its path in the
//! schema loader's words ([`ConfigError::Schema`]), then each deadline held
//! to the type, its own fields and its Workflow, in Timers' own words
//! ([`ConfigError::Rule`]). The core runs no JSON Schema, so the schema's
//! checks are written here, held to the declaration file by this crate's
//! tests. Messages name the row `{type} {key}`, where the engine's named
//! `{schema} {id}`.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, Code, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, Heard, Hook, Row,
    SchemaIssue, Stop, SubRow, UseSpec, Veto, View, WriteKind,
};

use crate::declared;
use crate::schema::{self, Issues};
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Timers";

/// The sub-table that holds a row per started deadline, keyed by its name.
pub const DEADLINES: &str = "deadlines";

/// The operation that fires a due deadline, which a deadline that falls due
/// calls.
pub const FIRE_TIMER: &str = "fireTimer";

/// The operation that arms a deadline again from what it starts from.
pub const RESET_TIMER: &str = "resetTimer";

/// The operation that moves a started deadline's due time.
pub const SNOOZE_TIMER: &str = "snoozeTimer";

/// Timers' declaration, the file the compiler registers and the engine
/// embeds, read in place: what a host describes the behavior with.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Timers.behavior.json");

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 4] = ["deadlines", "permission", "status", "sweepMs"];
const DEADLINE_KEYS: [&str; 4] = ["afterMs", "start", "then", "until"];
const THEN_KEYS: [&str; 2] = ["from", "transition"];

// The bounds the configSchema holds afterMs and sweepMs to.
const AFTER_MS_LIMIT: i64 = 3_153_600_000_000;
const SWEEP_MS_MINIMUM: i64 = 1000;

/// What a deadline starts from: the create, each entry of the status into
/// a state, or an own date-time field.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TimersStart {
    Create,
    Enters(String),
    Field(String),
}

/// The move a fire makes: to `transition`, while the status is one of
/// `from`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TimersThen {
    pub transition: String,
    pub from: Vec<String>,
}

/// What cancels a pending deadline: the status out of the state it starts
/// in, or in one of the states given.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TimersUntil {
    Leaves(String),
    Enters(Vec<String>),
}

/// One deadline of a Timers config, parsed: afterMs and then's from filled
/// in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TimersDeadline {
    pub start: TimersStart,
    /// How long after its start it is due, in milliseconds; negative,
    /// before it, for a field start.
    pub after_ms: i64,
    pub then: Option<TimersThen>,
    pub until: Option<TimersUntil>,
}

/// Timers' config, parsed: the defaults filled in, and the Workflow its
/// deadlines read resolved.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TimersConfig {
    /// The deadlines, by name, in name order (by code point), as the
    /// engine's canonical document held a config's members.
    pub deadlines: Vec<(String, TimersDeadline)>,
    /// The permission a caller needs to reset or snooze a deadline; nobody
    /// may when `None`.
    pub permission: Option<String>,
    /// The id of the Workflow use the deadlines read and move: the one the
    /// config's `status` names, or the type's one Workflow. `None` when no
    /// deadline reads one.
    pub status: Option<String>,
}

impl TimersConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then each deadline held to the type, its own fields and its
    /// Workflow, a rule's reason in the engine's words, which the core
    /// prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<TimersConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let workflows: Vec<&UseSpec> = target
            .uses()
            .iter()
            .filter(|entry| entry.behavior == workflow::NAME)
            .collect();
        let ids = || -> String {
            let ids: Vec<&str> = workflows.iter().map(|entry| entry.id()).collect();
            if ids.is_empty() {
                "none".to_owned()
            } else {
                ids.join(", ")
            }
        };
        let status = match raw.get("status").and_then(Value::as_str) {
            Some(named) => match workflows.iter().find(|entry| entry.id() == named) {
                Some(entry) => Status::Of(entry),
                None => {
                    return Err(format!(
                        "status \"{named}\" is not a Workflow of the type (its Workflows: {})",
                        ids()
                    )
                    .into())
                }
            },
            None => match workflows.as_slice() {
                [] => Status::None,
                [entry] => Status::Of(entry),
                _ => Status::Several(ids()),
            },
        };
        let flow = match &status {
            Status::Of(entry) => flow_of(&entry.config_or_empty()),
            _ => None,
        };
        let empty = Map::new();
        let deadlines = raw
            .get("deadlines")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        // In name order, as the engine's canonical document held a config,
        // so a config reads alike however its host ordered its members.
        let mut names: Vec<&String> = deadlines.keys().collect();
        names.sort();
        let mut parsed = Vec::new();
        for name in names {
            parsed.push((
                name.clone(),
                parse_deadline(target, &status, flow.as_ref(), name, &deadlines[name])?,
            ));
        }
        let mut config = TimersConfig {
            deadlines: parsed,
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
            status: None,
        };
        if let (true, Status::Of(entry)) = (config.reads_flow(), &status) {
            config.status = Some(entry.id().to_owned());
        }
        Ok(config)
    }

    /// A deadline by name.
    pub fn deadline(&self, name: &str) -> Option<&TimersDeadline> {
        self.deadlines
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, deadline)| deadline)
    }

    // reads_flow reports whether the config reads the row's Workflow
    // status: for an enters start, a then or an until.
    fn reads_flow(&self) -> bool {
        self.deadlines.iter().any(|(_, deadline)| {
            matches!(deadline.start, TimersStart::Enters(_))
                || deadline.then.is_some()
                || deadline.until.is_some()
        })
    }
}

/// The engine's `Timers.deadlines` of a row: `{ <name>: { dueAt, fired,
/// firedAt?, outcome? } }`, from its `deadlines` sub-table rows, in the
/// config's deadlines' order, no deadline not started or cancelled, and
/// none the config no longer names.
pub fn deadlines_field(config: &TimersConfig, rows: &[SubRow]) -> Value {
    let mut deadlines = Map::new();
    for (name, _) in &config.deadlines {
        let Some(row) = row_named(rows, name) else {
            continue;
        };
        let Some(due_at) = millis(row.value("dueAt")) else {
            continue;
        };
        let mut state = Map::new();
        state.insert("dueAt".into(), Value::from(due_at));
        state.insert("fired".into(), Value::Bool(fired(row)));
        if let Some(fired_at) = millis(row.value("firedAt")) {
            state.insert("firedAt".into(), Value::from(fired_at));
        }
        if let Some(outcome) = row.value("outcome").and_then(Value::as_str) {
            state.insert("outcome".into(), Value::from(outcome));
        }
        deadlines.insert(name.clone(), Value::Object(state));
    }
    Value::Object(deadlines)
}

/// Timers: named deadlines on each row.
#[derive(Clone, Copy, Debug, Default)]
pub struct Timers;

impl Behavior for Timers {
    type Config = TimersConfig;

    // The declaration file names its deadlines sub-table and says it arms
    // deadlines and hears its row's writes (D103).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<TimersConfig, ConfigError> {
        TimersConfig::parse(raw, target)
    }

    // Any change: Timers joins and leaves a type with rows, and a row's
    // deadlines reconcile at its next write.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&TimersConfig>,
        _after: Option<&TimersConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    fn check_params(
        &self,
        _config: &TimersConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    fn operation(&self, cx: &Cx<'_, TimersConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            FIRE_TIMER => fire_timer(cx, params),
            RESET_TIMER => reset_timer(cx, params),
            SNOOZE_TIMER => snooze_timer(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // A deadline that falls due reconciles its row first, so one a raw
    // write moved since it was armed is armed again rather than fired, then
    // fires through the use's own fireTimer, so every use's guards run on
    // it; one the config no longer names deletes its row and changes
    // nothing else.
    fn deadline(&self, cx: &Cx<'_, TimersConfig>, name: &str, _payload: &Value) -> Hook<()> {
        reconcile(cx, None)?;
        if cx.config().deadline(name).is_none() {
            if row_named(&cx.sub_rows(DEADLINES)?, name).is_some() {
                cx.delete_row(DEADLINES, name)?;
            }
            return Ok(());
        }
        cx.call(cx.use_id(), FIRE_TIMER, &json!({ "deadline": name }))?;
        Ok(())
    }

    // after_change reconciles the row as the change left it; a create's
    // time is a create start's fact. A delete takes the row's sub-table rows
    // and deadlines with it.
    fn after_change(&self, cx: &Cx<'_, TimersConfig>, change: &Change<'_>) -> Hook<()> {
        if matches!(change, Change::Delete) {
            return Ok(());
        }
        reconcile(cx, matches!(change, Change::Create).then(|| cx.now()))
    }

    // A write of the row, heard from its table's change queue, reconciles
    // the row as it is now; an insert's time is a create start's fact. A
    // write of another type's row is not Timers' to hear.
    fn heard(&self, cx: &Cx<'_, TimersConfig>, heard: &Heard<'_>) -> Hook<()> {
        let Heard::Own { change } = heard else {
            return Ok(());
        };
        reconcile(cx, (change.write == WriteKind::Insert).then_some(change.at))
    }

    // A read of the row shows its started deadlines, as the engine's
    // Timers.deadlines field.
    fn read_fields(
        &self,
        config: &TimersConfig,
        _row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let rows = tables.get(DEADLINES).map_or(&[][..], Vec::as_slice);
        let mut fields = Map::new();
        fields.insert(DEADLINES.into(), deadlines_field(config, rows));
        fields
    }
}

// reconcile arms each deadline whose fact has changed, then cancels each
// pending one whose until holds, from the row as the run has it, and
// deletes the rows of deadlines the config no longer names. `created_at`
// is the time of the row's create, a create start's fact while its row has
// none: a create's after-change hook's, or a heard insert's.
fn reconcile(cx: &Cx<'_, TimersConfig>, created_at: Option<i64>) -> Hook<()> {
    let config = cx.config();
    let facts = facts_of(cx);
    let rows = cx.sub_rows(DEADLINES)?;
    let own = cx.own();
    for (name, deadline) in &config.deadlines {
        let row = row_named(&rows, name);
        let start = start_of(deadline, &own, &facts, row, created_at);
        if matches!(deadline.start, TimersStart::Field(_)) && start.is_none() {
            // The field cleared, or never set: no deadline.
            if row.is_some() {
                cx.delete_row(DEADLINES, name)?;
                cx.clear(name)?;
            }
            continue;
        }
        let mut pending = row.is_some_and(is_pending);
        if let Some(start) = &start {
            let armed_from = row.and_then(|row| row.value("armedFrom"));
            if armed_from.and_then(Value::as_str) != Some(start.fact.as_str()) {
                arm(cx, name, deadline, start, row.is_some())?;
                pending = true;
            }
        }
        if pending && cancelled(deadline, facts.status.as_deref()) {
            cancel(cx, name)?;
        }
    }
    for row in &rows {
        if config.deadline(&row.key).is_none() {
            cx.delete_row(DEADLINES, &row.key)?;
            cx.clear(&row.key)?;
        }
    }
    Ok(())
}

fn fire_timer(cx: &Cx<'_, TimersConfig>, params: &Value) -> Hook<Value> {
    let name = deadline_param(params);
    let deadline = deadline_of(cx, name)?;
    let rows = cx.sub_rows(DEADLINES)?;
    // Not started, cancelled, fired already or not due: nothing changes.
    let due = row_named(&rows, name)
        .filter(|row| !fired(row))
        .and_then(|row| millis(row.value("dueAt")))
        .is_some_and(|due_at| cx.now() >= due_at);
    if !due {
        cx.unchanged()?;
        return Ok(json!({ "fired": false }));
    }
    let status = if deadline.then.is_none() && deadline.until.is_none() {
        None
    } else {
        facts_of(cx).status
    };
    if cancelled(deadline, status.as_deref()) {
        cancel(cx, name)?;
        return Ok(json!({ "fired": false }));
    }
    let outcome = match &deadline.then {
        Some(then) => Some(fire_move(cx, then, status.as_deref())?),
        None => None,
    };
    let mut values = Map::new();
    values.insert("fired".into(), Value::Bool(true));
    values.insert("firedAt".into(), Value::from(cx.now()));
    values.insert("outcome".into(), outcome.map_or(Value::Null, Value::from));
    cx.update_row(DEADLINES, name, values)?;
    cx.clear(name)?;
    Ok(match outcome {
        Some(outcome) => json!({ "fired": true, "outcome": outcome }),
        None => json!({ "fired": true }),
    })
}

fn reset_timer(cx: &Cx<'_, TimersConfig>, params: &Value) -> Hook<Value> {
    let name = deadline_param(params);
    let deadline = deadline_of(cx, name)?;
    permitted(cx, "reset", name)?;
    let facts = facts_of(cx);
    let rows = cx.sub_rows(DEADLINES)?;
    let row = row_named(&rows, name);
    let Some(start) = start_of(deadline, &cx.own(), &facts, row, None) else {
        let reason = match &deadline.start {
            TimersStart::Enters(state) => format!(
                "deadline {name} starts when the status enters {state}, and {} {}; it starts at the next entry",
                target(cx),
                if facts.status.as_deref() == Some(state.as_str()) {
                    "entered it before Workflow kept when".to_owned()
                } else {
                    format!("is {}", facts.status.as_deref().unwrap_or("in no state"))
                }
            ),
            TimersStart::Create => format!(
                "deadline {name} starts from the create, and {} was created before Timers kept its deadlines, so it has no create time to start from",
                target(cx)
            ),
            TimersStart::Field(field) => format!(
                "deadline {name} starts from {field}, which holds no date-time on {}",
                target(cx)
            ),
        };
        return Err(not_armed(cx, reason));
    };
    if cancelled(deadline, facts.status.as_deref()) {
        return Err(not_armed(
            cx,
            format!(
                "deadline {name} is cancelled while {} is {}",
                target(cx),
                facts.status.as_deref().unwrap_or_default()
            ),
        ));
    }
    let due_at = start.at + deadline.after_ms;
    if let Some(row) = row {
        if !fired(row)
            && millis(row.value("dueAt")) == Some(due_at)
            && millis(row.value("baseAt")) == Some(start.at)
            && row.value("armedFrom").and_then(Value::as_str) == Some(start.fact.as_str())
        {
            cx.unchanged()?;
            return Ok(json!({ "dueAt": due_at }));
        }
    }
    Ok(json!({ "dueAt": arm(cx, name, deadline, &start, row.is_some())? }))
}

fn snooze_timer(cx: &Cx<'_, TimersConfig>, params: &Value) -> Hook<Value> {
    let name = deadline_param(params);
    let deadline = deadline_of(cx, name)?;
    permitted(cx, "snooze", name)?;
    let rows = cx.sub_rows(DEADLINES)?;
    let Some(row) = row_named(&rows, name).filter(|row| millis(row.value("dueAt")).is_some())
    else {
        return Err(not_armed(
            cx,
            format!(
                "deadline {name} of {} has not started or was cancelled, so it has no due time to move",
                target(cx)
            ),
        ));
    };
    if deadline.until.is_some() {
        let status = facts_of(cx).status;
        if cancelled(deadline, status.as_deref()) {
            return Err(not_armed(
                cx,
                format!(
                    "deadline {name} is cancelled while {} is {}",
                    target(cx),
                    status.as_deref().unwrap_or_default()
                ),
            ));
        }
    }
    let due_at = millis(params.get("dueAt")).unwrap_or_default();
    if !fired(row) && millis(row.value("dueAt")) == Some(due_at) {
        cx.unchanged()?;
        return Ok(json!({ "dueAt": due_at }));
    }
    let mut values = Map::new();
    values.insert("dueAt".into(), Value::from(due_at));
    values.insert("fired".into(), Value::Bool(false));
    values.insert("firedAt".into(), Value::Null);
    values.insert("outcome".into(), Value::Null);
    cx.update_row(DEADLINES, name, values)?;
    cx.arm(name, due_at, Value::Null)?;
    Ok(json!({ "dueAt": due_at }))
}

/// The Workflow the deadlines read, as the config's `status` and the
/// type's uses resolve it.
enum Status<'a> {
    /// The type lists no Workflow.
    None,
    /// The Workflow use the config names, or the type's one.
    Of(&'a UseSpec),
    /// The type lists several, by id, and the config names none.
    Several(String),
}

/// What a deadline starts from, read from the row as it is: the status of
/// the Workflow the config reads, and when it was entered.
#[derive(Default)]
struct Facts {
    status: Option<String>,
    entered_at: Option<i64>,
}

/// What a deadline starts from now: the fact its row keeps, and the time it
/// is due from.
struct Start {
    fact: String,
    at: i64,
}

// facts_of reads the status and its entry time of the Workflow the config
// reads, as the run has them; none when no deadline reads one.
fn facts_of(view: &View<'_, TimersConfig>) -> Facts {
    let Some(use_id) = &view.config().status else {
        return Facts::default();
    };
    Facts {
        status: view
            .get_of(use_id, workflow::STATUS)
            .and_then(|status| status.as_str().map(str::to_owned)),
        entered_at: millis(view.get_of(use_id, workflow::ENTERED_AT).as_ref()),
    }
}

// start_of is what a deadline starts from now; `None` when there is
// nothing: a create start on a row created before Timers kept its
// deadlines, an enters deadline while the status is in another state, or
// was entered before Workflow kept when, and a field that holds no
// date-time. A create start reads its row's baseAt once it has one, and
// before that `created_at`, the create's time in the create's after-change
// hook or a heard insert's, so an insert the core made and armed already
// arms nothing again when it is heard.
fn start_of(
    deadline: &TimersDeadline,
    own: &Map<String, Value>,
    facts: &Facts,
    row: Option<&SubRow>,
    created_at: Option<i64>,
) -> Option<Start> {
    match &deadline.start {
        TimersStart::Create => match row.and_then(|row| millis(row.value("baseAt"))) {
            Some(at) => {
                let fact = row
                    .and_then(|row| row.value("armedFrom"))
                    .and_then(Value::as_str)
                    .map_or_else(|| at.to_string(), str::to_owned);
                Some(Start { fact, at })
            }
            None => created_at.map(|at| Start {
                fact: at.to_string(),
                at,
            }),
        },
        TimersStart::Enters(state) => match (&facts.status, facts.entered_at) {
            (Some(status), Some(at)) if status == state => Some(Start {
                fact: at.to_string(),
                at,
            }),
            _ => None,
        },
        TimersStart::Field(field) => match own.get(field)? {
            Value::String(text) => parse_date_time(text).map(|at| Start {
                fact: text.clone(),
                at,
            }),
            number @ Value::Number(_) => millis(Some(number)).map(|at| Start {
                fact: at.to_string(),
                at,
            }),
            _ => None,
        },
    }
}

// cancelled reports whether a deadline's until holds for the status: it is
// not the state the deadline starts in (leaves), or is one of the states
// given (enters). A row with no status cancels nothing.
fn cancelled(deadline: &TimersDeadline, status: Option<&str>) -> bool {
    let (Some(until), Some(status)) = (&deadline.until, status) else {
        return false;
    };
    match until {
        TimersUntil::Leaves(state) => status != state,
        TimersUntil::Enters(states) => states.iter().any(|state| state == status),
    }
}

// arm starts a deadline from what it starts from: due afterMs after it, not
// fired, and the fact kept, so a later write that leaves the fact as it is
// arms nothing; and arms it in the core at its due time.
fn arm(
    cx: &Cx<'_, TimersConfig>,
    name: &str,
    deadline: &TimersDeadline,
    start: &Start,
    exists: bool,
) -> Hook<i64> {
    let due_at = start.at + deadline.after_ms;
    let mut values = Map::new();
    values.insert("baseAt".into(), Value::from(start.at));
    values.insert("armedFrom".into(), Value::from(start.fact.as_str()));
    values.insert("dueAt".into(), Value::from(due_at));
    values.insert("fired".into(), Value::Bool(false));
    if exists {
        values.insert("firedAt".into(), Value::Null);
        values.insert("outcome".into(), Value::Null);
        cx.update_row(DEADLINES, name, values)?;
    } else {
        cx.insert_row(DEADLINES, name, values)?;
    }
    cx.arm(name, due_at, Value::Null)?;
    Ok(due_at)
}

// cancel takes a pending deadline's due time away and clears it; its row
// keeps the fact it started from, so only a new one arms it again.
fn cancel(cx: &Cx<'_, TimersConfig>, name: &str) -> Hook<()> {
    let mut values = Map::new();
    values.insert("dueAt".into(), Value::Null);
    cx.update_row(DEADLINES, name, values)?;
    cx.clear(name)
}

// fire_move makes a fire's move through the Workflow's transition, while
// the status is one of then's from, and says what it met. A veto or a
// forbidden leaves the status as it is (the refused call is undone); any
// other refusal fails the fire.
fn fire_move(
    cx: &Cx<'_, TimersConfig>,
    then: &TimersThen,
    status: Option<&str>,
) -> Hook<&'static str> {
    if !status.is_some_and(|status| then.from.iter().any(|from| from == status)) {
        return Ok("skipped");
    }
    let Some(use_id) = &cx.config().status else {
        return Err(cx.defect("a then reached its fire with no Workflow to move"));
    };
    match cx.call(
        use_id,
        workflow::TRANSITION,
        &json!({ "to": then.transition }),
    ) {
        Ok(_) => Ok("moved"),
        Err(stop) => match stop.refusal().map(|refusal| refusal.code) {
            Some(Code::Vetoed) => Ok("vetoed"),
            Some(Code::Forbidden) => Ok("forbidden"),
            _ => Err(stop),
        },
    }
}

// deadline_of is the config's deadline an operation names, which must be
// one.
fn deadline_of<'c>(view: &View<'c, TimersConfig>, name: &str) -> Hook<&'c TimersDeadline> {
    let config = view.config();
    config.deadline(name).ok_or_else(|| {
        let names: Vec<&str> = config
            .deadlines
            .iter()
            .map(|(known, _)| known.as_str())
            .collect();
        view.veto(
            Veto::new(format!(
                "{name} is not a deadline of {}'s Timers (its deadlines: {})",
                view.type_name(),
                names.join(", ")
            ))
            .with_code("no_deadline"),
        )
    })
}

// not_armed is the veto of a reset or a snooze of a deadline with no due
// time to give.
fn not_armed(view: &View<'_, TimersConfig>, reason: String) -> Stop {
    view.veto(Veto::new(reason).with_code("not_armed"))
}

// permitted holds resetTimer and snoozeTimer to the config's permission,
// which nobody holds when the config names none.
fn permitted(view: &View<'_, TimersConfig>, verb: &str, name: &str) -> Hook<()> {
    let who = format!(
        "{} may not {verb} deadline {name} of {}",
        view.caller().subject,
        target(view)
    );
    match &view.config().permission {
        None => Err(view.forbidden(format!(
            "{who}: {}'s Timers config names no permission, so nobody may",
            view.type_name()
        ))),
        Some(permission) if !view.can(permission)? => {
            Err(view.forbidden(format!("{who}: it needs permission {permission}")))
        }
        Some(_) => Ok(()),
    }
}

// target names the row a message is about: its type and its key.
fn target<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

fn deadline_param(params: &Value) -> &str {
    params
        .get("deadline")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

fn row_named<'r>(rows: &'r [SubRow], name: &str) -> Option<&'r SubRow> {
    rows.iter().find(|row| row.key == name)
}

// fired reads a row's fired, a boolean as the core writes it; an adapter
// that read an integer column as its number reads 1 as fired.
fn fired(row: &SubRow) -> bool {
    match row.value("fired") {
        Some(Value::Bool(fired)) => *fired,
        Some(Value::Number(number)) => number.as_i64().is_some_and(|n| n != 0),
        _ => false,
    }
}

// is_pending reports whether a row is armed: due at a time and not fired.
fn is_pending(row: &SubRow) -> bool {
    millis(row.value("dueAt")).is_some() && !fired(row)
}

// millis reads epoch milliseconds: a whole number.
fn millis(value: Option<&Value>) -> Option<i64> {
    let number = value?.as_number()?;
    number.as_i64().or_else(|| {
        number
            .as_f64()
            .filter(|n| n.is_finite() && n.fract() == 0.0)
            .map(|n| n as i64)
    })
}

/// A Workflow config as the type holds it: what `parse_config` checks a
/// deadline's states against.
struct Flow {
    states: Vec<String>,
    initial: Option<String>,
    transitions: Vec<(String, String)>,
}

// flow_of reads a Workflow config, when it has the shape Workflow takes;
// one of another shape is Workflow's to refuse.
fn flow_of(raw: &Value) -> Option<Flow> {
    let states = raw.get("states")?.as_array()?;
    if !states.iter().all(Value::is_string) {
        return None;
    }
    let transitions = raw.get("transitions")?.as_array()?;
    let states: Vec<String> = states
        .iter()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect();
    let initial = match raw.get("initial") {
        Some(Value::String(initial)) => Some(initial.clone()),
        _ => states.first().cloned(),
    };
    Some(Flow {
        states,
        initial,
        transitions: transitions
            .iter()
            .filter_map(|edge| {
                Some((
                    edge.get("from")?.as_str()?.to_owned(),
                    edge.get("to")?.as_str()?.to_owned(),
                ))
            })
            .collect(),
    })
}

// check_field holds a field start to an own field of the type whose values
// are instants, as the type's spec lists its fields.
fn check_field(target: &ConfigTarget<'_>, at: &str, field: &str) -> Result<(), ConfigError> {
    let Some(found) = target.fields().iter().find(|known| known.name == field) else {
        let names: Vec<&str> = target
            .fields()
            .iter()
            .map(|known| known.name.as_str())
            .collect();
        return Err(format!(
            "{at}.start.field \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            if names.is_empty() {
                "none".to_owned()
            } else {
                names.join(", ")
            }
        )
        .into());
    };
    if !found.date_time {
        return Err(format!(
            "{at}.start.field \"{field}\" is not a date-time field of {}: a deadline starts from the time a value holds",
            target.type_name()
        )
        .into());
    }
    Ok(())
}

// check_move holds a then to the type's Workflow: its transition and every
// from state are states of it, and a transition leads from each to it.
fn check_move(flow: &Flow, at: &str, transition: &str, from: &[String]) -> Result<(), ConfigError> {
    let states = flow.states.join(", ");
    if !flow.states.iter().any(|state| state == transition) {
        return Err(format!(
            "{at}.transition \"{transition}\" is not a state of the type's Workflow ({states})"
        )
        .into());
    }
    for state in from {
        if !flow.states.contains(state) {
            return Err(format!(
                "{at}.from names \"{state}\", which is not a state of the type's Workflow ({states})"
            )
            .into());
        }
        if state == transition {
            return Err(format!("{at} moves the status from \"{state}\" to itself").into());
        }
        if !flow
            .transitions
            .iter()
            .any(|(source, to)| source == state && to == transition)
        {
            return Err(format!(
                "{at}: no transition of the type's Workflow leads from \"{state}\" to \"{transition}\""
            )
            .into());
        }
    }
    Ok(())
}

// strings reads a list of strings the configSchema let through.
fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
}

// parse_deadline holds one deadline to the type, its fields and its
// Workflow, and fills in afterMs and then's from.
fn parse_deadline(
    target: &ConfigTarget<'_>,
    status: &Status<'_>,
    flow: Option<&Flow>,
    name: &str,
    raw: &Value,
) -> Result<TimersDeadline, ConfigError> {
    let at = format!("deadlines.{name}");
    let needs_flow = |place: &str| -> Result<(), ConfigError> {
        match status {
            Status::Of(_) => Ok(()),
            Status::None => Err(format!(
                "{at}.{place} reads the status of Workflow, which the type does not list"
            )
            .into()),
            Status::Several(ids) => Err(format!(
                "{at}.{place} reads the status of Workflow, and the type has several ({ids}): status names the field whose Workflow it reads"
            )
            .into()),
        }
    };
    let is_state = |place: &str, state: &str| -> Result<(), ConfigError> {
        match flow {
            Some(flow) if !flow.states.iter().any(|known| known == state) => Err(format!(
                "{at}.{place} \"{state}\" is not a state of the type's Workflow ({})",
                flow.states.join(", ")
            )
            .into()),
            _ => Ok(()),
        }
    };
    let raw_start = raw.get("start");
    let text_of = |key: &str| {
        raw_start
            .and_then(|start| start.get(key))
            .and_then(Value::as_str)
    };
    // The state the deadline starts in: its enters state, or the initial
    // state for a create start. A field start has none.
    let (start, start_state) = if let Some(enters) = text_of("enters") {
        needs_flow("start.enters")?;
        is_state("start.enters", enters)?;
        (
            TimersStart::Enters(enters.to_owned()),
            Some(enters.to_owned()),
        )
    } else if let Some(field) = text_of("field") {
        check_field(target, &at, field)?;
        (TimersStart::Field(field.to_owned()), None)
    } else {
        (
            TimersStart::Create,
            flow.and_then(|flow| flow.initial.clone()),
        )
    };
    let field_start = matches!(start, TimersStart::Field(_));
    let after_ms = millis(raw.get("afterMs")).unwrap_or_default();
    if after_ms < 0 && !field_start {
        return Err(format!(
            "{at}.afterMs is negative, which only a field start takes: a deadline that starts at the create or at an entry is not due before it"
        )
        .into());
    }
    let then = match raw.get("then") {
        None => None,
        Some(then) => {
            needs_flow("then")?;
            let transition = then
                .get("transition")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned();
            let from = match then.get("from") {
                Some(from) => strings(Some(from)),
                None if field_start => {
                    return Err(format!(
                        "{at}.then names no from: a deadline that starts at a field is in no state of its own, so then.from names the states its move leaves"
                    )
                    .into())
                }
                None => start_state.iter().cloned().collect(),
            };
            if let Some(flow) = flow {
                check_move(flow, &format!("{at}.then"), &transition, &from)?;
            }
            Some(TimersThen { transition, from })
        }
    };
    let until = match raw.get("until") {
        None => None,
        Some(until) => {
            needs_flow("until")?;
            if let Some(leaves) = until.get("leaves").and_then(Value::as_str) {
                if field_start {
                    return Err(format!(
                        "{at}.until.leaves: a deadline that starts at a field is in no state of its own to leave; until.enters names the states that cancel it"
                    )
                    .into());
                }
                is_state("until.leaves", leaves)?;
                if let Some(state) = start_state.as_deref().filter(|state| *state != leaves) {
                    return Err(format!(
                        "{at}.until.leaves \"{leaves}\" is not the state the deadline starts in, \"{state}\", which until.leaves cancels it on leaving"
                    )
                    .into());
                }
                Some(TimersUntil::Leaves(leaves.to_owned()))
            } else {
                let enters = strings(until.get("enters"));
                for state in &enters {
                    is_state("until.enters", state)?;
                    if start_state.as_deref() == Some(state.as_str()) {
                        return Err(format!(
                            "{at}.until.enters names \"{state}\", the state the deadline starts in, which would cancel it as it starts"
                        )
                        .into());
                    }
                }
                Some(TimersUntil::Enters(enters))
            }
        }
    };
    Ok(TimersDeadline {
        start,
        after_ms,
        then,
        until,
    })
}

// ---- the configSchema and the paramsSchemas, in ajv's words ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    if !config.contains_key("deadlines") {
        issues.push("", "required", "must have required property 'deadlines'");
    }
    for key in ["permission", "status"] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    if let Some(sweep) = config.get("sweepMs") {
        schema::integer_issues("/sweepMs", sweep, Some(SWEEP_MS_MINIMUM), None, &mut issues);
    }
    if let Some(deadlines) = config.get("deadlines") {
        deadlines_issues(deadlines, &mut issues);
    }
    issues
}

fn deadlines_issues(deadlines: &Value, issues: &mut Issues) {
    let at = "/deadlines";
    let Some(named) = deadlines.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (name, deadline) in named {
        // A name that is no deadline's fails the object's propertyNames,
        // which ajv reports at the object, after the name's own failures.
        let broken = schema::name_issues(name);
        if !broken.is_empty() {
            for (keyword, message) in broken {
                issues.push(at, keyword, message);
            }
            issues.push(at, "propertyNames", "property name must be valid");
        }
        deadline_issues(&schema::member(at, name), deadline, issues);
    }
}

fn deadline_issues(at: &str, deadline: &Value, issues: &mut Issues) {
    let Some(members) = deadline.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &DEADLINE_KEYS, issues);
    if !members.contains_key("start") {
        issues.push(at, "required", "must have required property 'start'");
    }
    if let Some(after) = members.get("afterMs") {
        schema::integer_issues(
            &schema::member(at, "afterMs"),
            after,
            Some(-AFTER_MS_LIMIT),
            Some(AFTER_MS_LIMIT),
            issues,
        );
    }
    if let Some(start) = members.get("start") {
        one_of(
            &schema::member(at, "start"),
            start,
            &[start_create, start_enters, start_field],
            issues,
        );
    }
    if let Some(then) = members.get("then") {
        then_issues(&schema::member(at, "then"), then, issues);
    }
    if let Some(until) = members.get("until") {
        one_of(
            &schema::member(at, "until"),
            until,
            &[until_leaves, until_enters],
            issues,
        );
    }
}

// The branches of start's oneOf and of until's: each a closed object of
// one required member.
fn start_create(at: &str, value: &Value, issues: &mut Issues) {
    closed(at, value, "create", is_true, issues);
}

fn start_enters(at: &str, value: &Value, issues: &mut Issues) {
    closed(at, value, "enters", schema::text_issues, issues);
}

fn start_field(at: &str, value: &Value, issues: &mut Issues) {
    closed(at, value, "field", schema::text_issues, issues);
}

fn until_leaves(at: &str, value: &Value, issues: &mut Issues) {
    closed(at, value, "leaves", schema::text_issues, issues);
}

fn until_enters(at: &str, value: &Value, issues: &mut Issues) {
    closed(at, value, "enters", state_list_issues, issues);
}

fn then_issues(at: &str, then: &Value, issues: &mut Issues) {
    let Some(members) = then.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &THEN_KEYS, issues);
    match members.get("transition") {
        None => issues.push(at, "required", "must have required property 'transition'"),
        Some(transition) => {
            schema::text_issues(&schema::member(at, "transition"), transition, issues);
        }
    }
    if let Some(from) = members.get("from") {
        state_list_issues(&schema::member(at, "from"), from, issues);
    }
}

// state_list_issues lists what a list of states breaks of `{ type: array,
// minItems: 1, uniqueItems: true, items: { type: string, minLength: 1 } }`.
fn state_list_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    for (i, item) in items.iter().enumerate() {
        schema::text_issues(&format!("{at}/{i}"), item, issues);
    }
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// A check of one branch of a oneOf, at a pointer, into issues.
type Branch = fn(&str, &Value, &mut Issues);

// one_of lists what a value breaks of a oneOf, as ajv reports it: nothing
// when exactly one branch holds; else every branch's issues and the oneOf's
// own.
fn one_of(at: &str, value: &Value, branches: &[Branch], issues: &mut Issues) {
    let mut held = 0;
    let mut found = Issues::default();
    for branch in branches {
        let mut own = Issues::default();
        branch(at, value, &mut own);
        if own.is_empty() {
            held += 1;
        } else {
            found.extend(own);
        }
    }
    if held == 1 {
        return;
    }
    issues.extend(found);
    issues.push(at, "oneOf", "must match exactly one schema in oneOf");
}

// closed lists what a value breaks of a closed object of one required
// member, whose value `member` checks.
fn closed(
    at: &str,
    value: &Value,
    key: &str,
    member: fn(&str, &Value, &mut Issues),
    issues: &mut Issues,
) {
    let Some(members) = value.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &[key], issues);
    match members.get(key) {
        None => issues.push(
            at,
            "required",
            format!("must have required property '{key}'"),
        ),
        Some(found) => member(&schema::member(at, key), found, issues),
    }
}

// is_true lists what a value breaks of `{ const: true }`.
fn is_true(at: &str, value: &Value, issues: &mut Issues) {
    if value != &Value::Bool(true) {
        issues.push(at, "const", "must be equal to constant");
    }
}

// unknown_keys lists the members a closed object does not take, in the
// schema loader's words.
fn unknown_keys(at: &str, members: &Map<String, Value>, known: &[&str], issues: &mut Issues) {
    for key in members.keys().filter(|key| !known.contains(&key.as_str())) {
        issues.push(
            at,
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
}

// params_issues lists what an operation's parameters break of its closed
// paramsSchema, in the behavior registry's words: each names its deadline,
// and snoozeTimer the time it moves it to.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    let keys: &[&str] = if operation == SNOOZE_TIMER {
        &["deadline", "dueAt"]
    } else {
        &["deadline"]
    };
    for key in given.keys().filter(|key| !keys.contains(&key.as_str())) {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    for key in keys {
        if !given.contains_key(*key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(deadline) = given.get("deadline") {
        schema::text_issues("/deadline", deadline, &mut issues);
    }
    if let (true, Some(due_at)) = (operation == SNOOZE_TIMER, given.get("dueAt")) {
        schema::integer_issues("/dueAt", due_at, Some(0), None, &mut issues);
    }
    issues
}

/// The time a date-time string holds, in epoch milliseconds, as
/// JavaScript's `Date.parse` reads the ISO 8601 forms (V8's), ported from
/// the engine's verbatim: a date (`YYYY-MM-DD`, or a signed six-digit
/// year), alone at midnight UTC or with a time after `T`, `t` or a space
/// (`HH:mm`, `:ss` and a fraction whose first three digits count), then
/// `Z`, `z` or an offset (`+HH:mm`, `+HHmm`). A day past its month's end
/// runs into the next, and `24:00` is the next midnight. A time without an
/// offset is read as UTC, where JavaScript reads the host's local time.
/// `None` where `Date.parse` gives NaN for these forms, and for every other
/// form.
pub fn parse_date_time(text: &str) -> Option<i64> {
    let bytes = text.as_bytes();
    let mut at = 0;
    let digits = |at: &mut usize, count: usize| -> Option<i64> {
        let slice = bytes.get(*at..*at + count)?;
        if !slice.iter().all(u8::is_ascii_digit) {
            return None;
        }
        *at += count;
        Some(
            slice
                .iter()
                .fold(0, |value, digit| value * 10 + i64::from(digit - b'0')),
        )
    };
    let expect = |at: &mut usize, byte: u8| -> Option<()> {
        (bytes.get(*at) == Some(&byte)).then(|| *at += 1)
    };
    let year = match bytes.first()? {
        sign @ (b'+' | b'-') => {
            at += 1;
            let year = digits(&mut at, 6)?;
            if *sign == b'-' {
                if year == 0 {
                    return None;
                }
                -year
            } else {
                year
            }
        }
        _ => digits(&mut at, 4)?,
    };
    expect(&mut at, b'-')?;
    let month = digits(&mut at, 2)?;
    expect(&mut at, b'-')?;
    let day = digits(&mut at, 2)?;
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) {
        return None;
    }
    let days = days_from_civil(year, month) + day - 1;
    if at == bytes.len() {
        return clip(days * 86_400_000);
    }
    if !matches!(bytes[at], b'T' | b't' | b' ') {
        return None;
    }
    at += 1;
    let hour = digits(&mut at, 2)?;
    expect(&mut at, b':')?;
    let minute = digits(&mut at, 2)?;
    let mut second = 0;
    let mut millis = 0;
    if bytes.get(at) == Some(&b':') {
        at += 1;
        second = digits(&mut at, 2)?;
        if bytes.get(at) == Some(&b'.') {
            at += 1;
            let start = at;
            while bytes.get(at).is_some_and(u8::is_ascii_digit) {
                at += 1;
            }
            if at == start {
                return None;
            }
            let fraction = &bytes[start..at.min(start + 3)];
            millis = fraction
                .iter()
                .fold(0, |value, digit| value * 10 + i64::from(digit - b'0'))
                * 10_i64.pow(3 - fraction.len() as u32);
        }
    }
    if hour > 24
        || minute > 59
        || second > 59
        || (hour == 24 && (minute, second, millis) != (0, 0, 0))
    {
        return None;
    }
    let offset = match bytes.get(at) {
        None => 0,
        Some(b'Z' | b'z') => {
            at += 1;
            0
        }
        Some(sign @ (b'+' | b'-')) => {
            let sign = if *sign == b'-' { -1 } else { 1 };
            at += 1;
            let hours = digits(&mut at, 2)?;
            if bytes.get(at) == Some(&b':') {
                at += 1;
            }
            let minutes = digits(&mut at, 2)?;
            if hours > 23 || minutes > 59 {
                return None;
            }
            sign * (hours * 60 + minutes)
        }
        Some(_) => return None,
    };
    if at != bytes.len() {
        return None;
    }
    clip(days * 86_400_000 + ((hour * 60 + minute - offset) * 60 + second) * 1000 + millis)
}

// clip is TimeClip: a time past 8.64e15 ms either side of the epoch is
// NaN.
fn clip(ms: i64) -> Option<i64> {
    (ms.abs() <= 8_640_000_000_000_000).then_some(ms)
}

// days_from_civil is the days from the epoch to the first of a month of
// the proleptic Gregorian calendar (Howard Hinnant's algorithm).
fn days_from_civil(year: i64, month: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let of_era = year - era * 400;
    let shifted = if month > 2 { month - 3 } else { month + 9 };
    let of_year = (153 * shifted + 2) / 5;
    let of_era_days = of_era * 365 + of_era / 4 - of_era / 100 + of_year;
    era * 146_097 + of_era_days - 719_468
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_date_time_reads_as_javascripts_date_parse_reads_it() {
        // Each recorded from Node.js's Date.parse.
        for (text, ms) in [
            ("2026-10-12T11:00:00+02:00", Some(1_791_795_600_000)),
            ("2026-10-12t11:00:00z", Some(1_791_802_800_000)),
            ("2026-10-12T11:00:00.123456Z", Some(1_791_802_800_123)),
            ("2026-10-12T11:00:00.1Z", Some(1_791_802_800_100)),
            ("2026-10-12T11:00Z", Some(1_791_802_800_000)),
            ("2026-10-12", Some(1_791_763_200_000)),
            ("2026-10-12 11:00:00Z", Some(1_791_802_800_000)),
            ("+002026-10-12T11:00:00Z", Some(1_791_802_800_000)),
            ("2026-02-30T11:00:00Z", Some(1_772_449_200_000)),
            ("2026-10-12T24:00:00Z", Some(1_791_849_600_000)),
            ("2026-10-12T11:00:60Z", None),
            ("2026-10-12T11:00:00+0200", Some(1_791_795_600_000)),
            ("2026-10-12T11:00:00.Z", None),
            ("2026-13-12T11:00:00Z", None),
            ("2026-10-12T11:00:00.9999Z", Some(1_791_802_800_999)),
            ("2026-10-12T11:00:00,5Z", None),
            ("2026-10-12T11:00:00-00:00", Some(1_791_802_800_000)),
            ("2026-10-12T11:00:00+24:00", None),
            ("2026-10-12T11:00:00+23:59", Some(1_791_716_460_000)),
            ("1970-01-01T00:00:00.000Z", Some(0)),
            ("1969-12-31T23:59:59.999Z", Some(-1)),
            ("2000-02-29T00:00:00.000Z", Some(951_782_400_000)),
            ("not a time", None),
            ("", None),
        ] {
            assert_eq!(parse_date_time(text), ms, "{text}");
        }
    }

    #[test]
    fn a_deadline_is_cancelled_by_its_until_alone() {
        let deadline = |until| TimersDeadline {
            start: TimersStart::Enters("open".to_owned()),
            after_ms: 0,
            then: None,
            until,
        };
        assert!(!cancelled(&deadline(None), Some("closed")));
        let leaves = deadline(Some(TimersUntil::Leaves("open".to_owned())));
        assert!(cancelled(&leaves, Some("waiting")));
        assert!(!cancelled(&leaves, Some("open")));
        assert!(!cancelled(&leaves, None));
        let enters = deadline(Some(TimersUntil::Enters(vec!["closed".to_owned()])));
        assert!(cancelled(&enters, Some("closed")));
        assert!(!cancelled(&enters, Some("waiting")));
    }

    #[test]
    fn a_rows_deadlines_read_as_the_engines_field() {
        let config = TimersConfig {
            deadlines: ["respond", "idle", "remind"]
                .map(|name| {
                    (
                        name.to_owned(),
                        TimersDeadline {
                            start: TimersStart::Create,
                            after_ms: 0,
                            then: None,
                            until: None,
                        },
                    )
                })
                .into(),
            permission: None,
            status: None,
        };
        let rows: Vec<SubRow> = serde_json::from_value(json!([
            {"key": "gone", "values": {"dueAt": 5, "fired": false}},
            {"key": "idle", "values": {"armedFrom": "1", "baseAt": 1}},
            {"key": "remind", "values": {"dueAt": 9, "fired": 1, "firedAt": 9}},
            {"key": "respond", "values": {"dueAt": 7, "fired": true, "firedAt": 8, "outcome": "moved"}}
        ]))
        .expect("rows");
        assert_eq!(
            deadlines_field(&config, &rows),
            json!({
                "respond": {"dueAt": 7, "fired": true, "firedAt": 8, "outcome": "moved"},
                "remind": {"dueAt": 9, "fired": true, "firedAt": 9}
            })
        );
        assert!(is_pending(&SubRow::new(
            "a",
            json!({"dueAt": 1, "fired": false})
                .as_object()
                .cloned()
                .unwrap()
        )));
        assert_eq!(millis(Some(&json!(1.0))), Some(1));
        assert_eq!(millis(Some(&json!(1.5))), None);
    }
}
