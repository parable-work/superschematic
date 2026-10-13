//! Timers, named deadlines on an instance (D56), the port of
//! `src/behaviors/core/timers.ts`: a ticket answered within four hours of
//! entering open, an order paid within thirty minutes of its create, a
//! reminder a day before an appointment. A deadline starts from a fact of
//! the instance: its create (`{ create: true }`), each entry of its
//! Workflow status into a state (`{ enters }`), or the time an own field
//! holds (`{ field }`), one whose JSON Schema format is date-time
//! ([`ConfigTarget::field_schemas`]). It is due `afterMs` after the start,
//! or before it with a negative `afterMs`, which only a field start takes,
//! and when it comes due it fires, once.
//!
//! A fire marks the deadline fired and, with `then`, moves the status
//! through Workflow's transition while the status is one of then's `from`:
//! the state the deadline starts in unless the config names others (the
//! enters state, or the Workflow's initial state for a create start; a
//! field start names them), so an item resolved in time is never moved.
//! Whatever the move meets, a guard's veto or a transition's permission the
//! caller lacks (forbidden), the deadline is fired, and its outcome records
//! which: moved, skipped (the status was not one of from), vetoed or
//! forbidden. So one stuck instance never holds the sweep back, and a guard
//! that refused is not asked again. `until` cancels a pending deadline once
//! a change leaves the status out of the state it starts in (`leaves`) or
//! in one of its states (`enters`). `parse_config` requires Workflow on the
//! type for an enters start, a then and an until, which the declaration's
//! requires cannot say, since a field deadline needs none.
//!
//! A deadline arms when the fact it starts from changes, which afterChange
//! reads from the instance as the change leaves it, in its transaction,
//! never from which operation ran: a Lease expiry, a Reactions move and a
//! fire move the status as a caller's transition does. The fact is the
//! instance's createdAt, the Workflow's enteredAt while the status is the
//! enters state, or the field's value, read through `instances().get` as
//! the caller (`Workflow.status` and `Workflow.enteredAt`) and from the
//! context's data, and the row keeps it (`armed_from`). A write that leaves
//! it as it is arms nothing, so a snooze or a reset outlives it; a status
//! that left the state and came back within one call has a new enteredAt,
//! which arms again. A create arms whatever the list order, since every
//! initialize has run by afterChange. A field moved arms again and clears
//! fired; a field cleared cancels its deadline. A due time already past
//! fires at the next sweep. An enters deadline does not arm on an instance
//! whose status predates Workflow's enteredAt, until its next entry.
//!
//! `deadlines` holds a row per started deadline: the time it starts from
//! (`base_at`), the fact (`armed_from`), when it is due (`due_at`, null
//! once until cancelled it), whether and when it fired and its outcome. The
//! `Timers.deadlines` field reads them: `{ <name>: { dueAt, fired,
//! firedAt?, outcome? } }`, no deadline that has not started or was
//! cancelled. Nothing in it follows the clock, so it is not volatile.
//!
//! The runner's schedule `fire` runs every `sweepMs` on each schema in each
//! namespace: it reads the deadlines due and not fired, in order of due
//! time, a keyset of due time, id and name past each page, through the
//! partial index on them (`WHERE fired = 0`, on both databases), at most
//! [`MAX_SWEEP`] a run, and invokes `fireTimer` on each as the runner's
//! principal. A veto the invoke meets, a Lease's guard say, leaves that
//! deadline for the next run and the keyset moves past it; any other
//! failure, a policy that refuses the runner write on the schema say, fails
//! the run, which the runner retries and its status shows. `fireTimer`
//! fires one deadline that is due and has not fired, and is `unchanged()`
//! on any other, so the sweep and a caller never fire one twice; one whose
//! until holds when it comes due is cancelled instead. `resetTimer` arms a
//! deadline again from what it starts from now, and `snoozeTimer` moves a
//! started one to an absolute dueAt, a fired one firing again then; both
//! need the config's permission (forbidden without it, and for everyone
//! when the config names none), and both are refused a deadline with no due
//! time to give (`not_armed`). Deleting the instance deletes its rows.
//!
//! A field's time is read as JavaScript's `Date.parse` reads the ISO 8601
//! forms a date-time takes ([`parse_date_time`]); a time without an offset
//! is read as UTC, where JavaScript reads the host's local time.
//!
//! configChange: any; Timers joins and leaves a schema with instances.
//! afterConfigChange deletes the rows of a deadline the version removes, or
//! whose start it changes, which arm again at each instance's next write;
//! moves the due time of each pending deadline a snooze has not moved from
//! its start when afterMs changes; and arms a new field deadline on every
//! instance from its stored fields. A version without Timers deletes every
//! row. On a type that composes Lease, `Timers.fireTimer` belongs in
//! Lease's exempt, or the holder's lease refuses the runner's fire.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, Every, IndexSpec, InstanceCx, InstanceView, OperationCx,
    Schedule, ScheduleCx, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{js_keys, number_value};
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, Hooks, InstanceChange, Result,
};

/// How often the runner fires the due deadlines when the config gives no
/// sweepMs.
pub const DEFAULT_SWEEP_MS: i64 = 5000;

/// The most deadlines one run of the sweep fires; what is left waits for
/// the next run.
pub const MAX_SWEEP: i64 = 1000;

/// How many deadlines the sweep reads at a time.
const SWEEP_BATCH: i64 = 100;

const NAME: &str = "Timers";

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

/// Timers' config, parsed: the defaults filled in.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TimersConfig {
    /// The deadlines, by name, in the config's order.
    pub deadlines: Vec<(String, TimersDeadline)>,
    pub sweep_ms: i64,
    /// The permission a caller needs to reset or snooze a deadline; nobody
    /// may when `None`.
    pub permission: Option<String>,
}

impl TimersConfig {
    /// A deadline by name.
    pub fn deadline(&self, name: &str) -> Option<&TimersDeadline> {
        self.deadlines
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, deadline)| deadline)
    }

    // reads_flow reports whether the config reads the instance's Workflow
    // status: for an enters start, a then or an until.
    fn reads_flow(&self) -> bool {
        self.deadlines.iter().any(|(_, deadline)| {
            matches!(deadline.start, TimersStart::Enters(_))
                || deadline.then.is_some()
                || deadline.until.is_some()
        })
    }
}

/// A deadline's row, as the deadlines table holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
struct DeadlineRow {
    base_at: Option<i64>,
    armed_from: Option<String>,
    /// `None` once until cancelled it.
    due_at: Option<i64>,
    fired: bool,
    fired_at: Option<i64>,
    outcome: Option<String>,
}

/// What a deadline starts from, read from the instance as it is.
struct Facts {
    created_at: i64,
    status: Option<String>,
    entered_at: Option<i64>,
}

/// What a deadline starts from now: the fact its row keeps, and the time it
/// is due from.
struct Start {
    fact: String,
    at: i64,
}

// The behavior's rows of one instance, as its SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

// rows_of reads the instance's deadline rows, by name.
fn rows_of(view: &InstanceView<'_, TimersConfig>) -> Result<Vec<(String, DeadlineRow)>> {
    let table = view.sql().table("deadlines")?;
    view.sql()
        .all(
            format!("SELECT name, base_at, armed_from, due_at, fired, fired_at, outcome FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
            &key(view),
        )?
        .iter()
        .map(|row: &Row| {
            Ok((
                row.text("name")?.to_owned(),
                DeadlineRow {
                    base_at: row.opt_i64("base_at")?,
                    armed_from: row.opt_text("armed_from")?.map(str::to_owned),
                    due_at: row.opt_i64("due_at")?,
                    fired: row.i64("fired")? == 1,
                    fired_at: row.opt_i64("fired_at")?,
                    outcome: row.opt_text("outcome")?.map(str::to_owned),
                },
            ))
        })
        .collect()
}

fn row_named<'r>(rows: &'r [(String, DeadlineRow)], name: &str) -> Option<&'r DeadlineRow> {
    rows.iter()
        .find(|(known, _)| known == name)
        .map(|(_, row)| row)
}

// facts_of reads what the config's deadlines start from, as the caller: the
// instance's createdAt for a create start, and its Workflow status and when
// it was entered when the config reads them. A config of field deadlines
// alone reads nothing: the context's data holds the field.
fn facts_of(view: &InstanceView<'_, TimersConfig>) -> Result<Facts> {
    let config = view.config();
    let flow = config.reads_flow();
    if !flow
        && !config
            .deadlines
            .iter()
            .any(|(_, deadline)| deadline.start == TimersStart::Create)
    {
        return Ok(Facts {
            created_at: view.now(),
            status: None,
            entered_at: None,
        });
    }
    let fields: &[&str] = if flow {
        &["Workflow.status", "Workflow.enteredAt"]
    } else {
        &[]
    };
    let record = view
        .instances()
        .get(view.schema(), view.id(), Some(fields))?;
    let workflow = record
        .as_ref()
        .and_then(|record| record.behaviors.get("Workflow"));
    Ok(Facts {
        created_at: record
            .as_ref()
            .map_or(view.now(), |record| record.created_at),
        status: workflow
            .and_then(|fields| fields.get("status"))
            .and_then(Value::as_str)
            .map(str::to_owned),
        entered_at: workflow
            .and_then(|fields| fields.get("enteredAt"))
            .and_then(|at| match at {
                Value::Number(at) => Some(number_value(at) as i64),
                _ => None,
            }),
    })
}

// start_of is what a deadline starts from now; `None` when there is
// nothing: an enters deadline while the status is in another state, or was
// entered before Workflow kept when, and a field that holds no date-time.
fn start_of(deadline: &TimersDeadline, data: &Map<String, Value>, facts: &Facts) -> Option<Start> {
    match &deadline.start {
        TimersStart::Create => Some(Start {
            fact: facts.created_at.to_string(),
            at: facts.created_at,
        }),
        TimersStart::Enters(state) => match (&facts.status, facts.entered_at) {
            (Some(status), Some(at)) if status == state => Some(Start {
                fact: at.to_string(),
                at,
            }),
            _ => None,
        },
        TimersStart::Field(field) => {
            let text = data.get(field)?.as_str()?;
            parse_date_time(text).map(|at| Start {
                fact: text.to_owned(),
                at,
            })
        }
    }
}

// cancelled reports whether a deadline's until holds for the status: it is
// not the state the deadline starts in (leaves), or is one of the states
// given (enters). An instance with no status cancels nothing.
fn cancelled(deadline: &TimersDeadline, status: Option<&str>) -> bool {
    let (Some(until), Some(status)) = (&deadline.until, status) else {
        return false;
    };
    match until {
        TimersUntil::Leaves(state) => status != state,
        TimersUntil::Enters(states) => states.iter().any(|state| state == status),
    }
}

// The upsert that starts a deadline: due at the given time, not fired,
// its start and fact kept.
fn arm_sql(table: &str) -> String {
    format!(
        "INSERT INTO {table} (namespace, schema, id, name, base_at, armed_from, due_at, fired) VALUES (?, ?, ?, ?, ?, ?, ?, 0)
         ON CONFLICT (namespace, schema, id, name) DO UPDATE SET
           base_at = excluded.base_at, armed_from = excluded.armed_from, due_at = excluded.due_at, fired = 0, fired_at = NULL, outcome = NULL"
    )
}

// arm starts a deadline from what it starts from: due afterMs after it, not
// fired, and the fact kept, so a later write that leaves the fact as it is
// arms nothing.
fn arm(
    cx: &InstanceCx<'_, TimersConfig>,
    name: &str,
    deadline: &TimersDeadline,
    start: &Start,
) -> Result<i64> {
    let due_at = start.at + deadline.after_ms;
    let mut params = key(cx);
    params.extend([
        SqlValue::from(name),
        SqlValue::Integer(start.at),
        SqlValue::from(start.fact.as_str()),
        SqlValue::Integer(due_at),
    ]);
    cx.sql()
        .run(arm_sql(&cx.sql().table("deadlines")?), &params)?;
    Ok(due_at)
}

// cancel takes a pending deadline's due time away; its row keeps the fact
// it started from, so only a new one arms it again.
fn cancel(cx: &InstanceCx<'_, TimersConfig>, name: &str) -> Result<()> {
    let table = cx.sql().table("deadlines")?;
    let mut params = key(cx);
    params.push(SqlValue::from(name));
    cx.sql().run(
        format!("UPDATE {table} SET due_at = NULL WHERE namespace = ? AND schema = ? AND id = ? AND name = ?"),
        &params,
    )?;
    Ok(())
}

// refuse is a veto of one of the behavior's operations.
fn refuse<C>(view: &InstanceView<'_, C>, operation: &str, reason: String, code: &str) -> Error {
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: None,
        },
    )
}

// deadline_of is the config's deadline an operation names, which must be
// one.
fn deadline_of<'c>(
    view: &InstanceView<'c, TimersConfig>,
    operation: &str,
    name: &str,
) -> Result<&'c TimersDeadline> {
    let config = view.config();
    config.deadline(name).ok_or_else(|| {
        let names: Vec<&str> = config
            .deadlines
            .iter()
            .map(|(known, _)| known.as_str())
            .collect();
        refuse(
            view,
            operation,
            format!(
                "{name} is not a deadline of {}'s Timers (its deadlines: {})",
                view.schema(),
                names.join(", ")
            ),
            "no_deadline",
        )
    })
}

// permitted holds resetTimer and snoozeTimer to the config's permission,
// which nobody holds when the config names none.
fn permitted(view: &InstanceView<'_, TimersConfig>, verb: &str, name: &str) -> Result<()> {
    let who = format!(
        "{} may not {verb} deadline {name} of {} {}",
        view.principal().subject,
        view.schema(),
        view.id()
    );
    let refused =
        |message: String| -> Error { EngineError::new(EngineErrorCode::Forbidden, message).into() };
    match &view.config().permission {
        None => Err(refused(format!(
            "{who}: {}'s Timers config names no permission, so nobody may",
            view.schema()
        ))),
        Some(permission) if !view.can(permission)? => {
            Err(refused(format!("{who}: it needs permission {permission}")))
        }
        Some(_) => Ok(()),
    }
}

// fire_move makes a fire's move through Workflow's transition, while the
// status is one of then's from, and says what it met. A veto or a
// forbidden leaves the status as it is; anything else fails the call.
fn fire_move(
    cx: &OperationCx<'_, TimersConfig>,
    then: &TimersThen,
    status: Option<&str>,
) -> Result<&'static str> {
    if !status.is_some_and(|status| then.from.iter().any(|from| from == status)) {
        return Ok("skipped");
    }
    match cx.call("Workflow", "transition", &json!({ "to": then.transition })) {
        Ok(_) => Ok("moved"),
        Err(error) if error.is_veto() => Ok("vetoed"),
        Err(error) if error.code() == Some(EngineErrorCode::Forbidden) => Ok("forbidden"),
        Err(error) => Err(error),
    }
}

fn deadline_param(params: &Value) -> &str {
    params
        .get("deadline")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

/// Timers: named deadlines on each instance.
#[derive(Clone, Copy, Debug, Default)]
pub struct Timers;

impl Behavior for Timers {
    type Config = TimersConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Timers.behavior.json")
    }

    // The configSchema holds the shape; this holds each deadline to the
    // type's fields and its Workflow, whose config it is given, and fills
    // in the defaults.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<TimersConfig> {
        let empty = Map::new();
        let deadlines = raw
            .get("deadlines")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let flow = flow_of(target);
        let mut parsed = Vec::new();
        for name in js_keys(deadlines) {
            parsed.push((
                name.to_owned(),
                parse_deadline(target, flow.as_ref(), name, &deadlines[name])?,
            ));
        }
        Ok(TimersConfig {
            deadlines: parsed,
            sweep_ms: match raw.get("sweepMs") {
                Some(Value::Number(ms)) => number_value(ms) as i64,
                _ => DEFAULT_SWEEP_MS,
            },
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            after_config_change: Some(after_config_change),
            guidance: Some(|_, config, target| crate::guidance::timers::guidance(config, target)),
            // The sweep reads the pending deadlines of a schema in a
            // namespace, earliest due first, a keyset past each page.
            migrations: vec![BehaviorMigration::new(1, "deadlines").table(
                TableSpec::new("deadlines")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("name", ColumnSpec::text().not_null())
                    .column("base_at", ColumnSpec::integer())
                    .column("armed_from", ColumnSpec::text())
                    .column("due_at", ColumnSpec::integer())
                    .column("fired", ColumnSpec::integer().not_null().default(0))
                    .column("fired_at", ColumnSpec::integer())
                    .column("outcome", ColumnSpec::text())
                    .primary_key(&["namespace", "schema", "id", "name"])
                    .index(
                        IndexSpec::new(
                            "deadlines_due",
                            &["namespace", "schema", "due_at", "id", "name"],
                        )
                        .filter("fired = 0"),
                    ),
            )],
            operations: vec![
                ("fireTimer", fire_timer),
                ("resetTimer", reset_timer),
                ("snoozeTimer", snooze_timer),
            ],
            // Each started deadline, in the order the config names them.
            fields: vec![("deadlines", |_, view| {
                let rows = rows_of(view)?;
                let mut deadlines = Map::new();
                for (name, _) in &view.config().deadlines {
                    let Some(row) = row_named(&rows, name) else {
                        continue;
                    };
                    let Some(due_at) = row.due_at else {
                        continue;
                    };
                    let mut state = Map::new();
                    state.insert("dueAt".into(), Value::from(due_at));
                    state.insert("fired".into(), Value::Bool(row.fired));
                    if let Some(fired_at) = row.fired_at {
                        state.insert("firedAt".into(), Value::from(fired_at));
                    }
                    if let Some(outcome) = &row.outcome {
                        state.insert("outcome".into(), Value::from(outcome.as_str()));
                    }
                    deadlines.insert(name.clone(), Value::Object(state));
                }
                Ok(Some(Value::Object(deadlines)))
            })],
            after_change: Some(after_change),
            schedules: vec![(
                "fire",
                Schedule {
                    every: Every::Config(|_, config: &TimersConfig| Ok(Some(config.sweep_ms))),
                    run: fire,
                },
            )],
            ..Hooks::default()
        }
    }
}

fn fire_timer(_: &Timers, cx: &OperationCx<'_, TimersConfig>, params: &Value) -> Result<Value> {
    let name = deadline_param(params);
    let deadline = deadline_of(cx, "fireTimer", name)?;
    let rows = rows_of(cx)?;
    // Not started, cancelled, fired already or not due: nothing changes.
    let due = row_named(&rows, name)
        .filter(|row| !row.fired)
        .and_then(|row| row.due_at)
        .is_some_and(|due_at| cx.now() >= due_at);
    if !due {
        cx.unchanged();
        return Ok(json!({ "fired": false }));
    }
    let status = if deadline.then.is_none() && deadline.until.is_none() {
        None
    } else {
        facts_of(cx)?.status
    };
    if cancelled(deadline, status.as_deref()) {
        cancel(cx, name)?;
        return Ok(json!({ "fired": false }));
    }
    let outcome = match &deadline.then {
        Some(then) => Some(fire_move(cx, then, status.as_deref())?),
        None => None,
    };
    let table = cx.sql().table("deadlines")?;
    let mut values = vec![
        SqlValue::Integer(cx.now()),
        outcome.map_or(SqlValue::Null, SqlValue::from),
    ];
    values.extend(key(cx));
    values.push(SqlValue::from(name));
    cx.sql().run(
        format!("UPDATE {table} SET fired = 1, fired_at = ?, outcome = ? WHERE namespace = ? AND schema = ? AND id = ? AND name = ?"),
        &values,
    )?;
    Ok(match outcome {
        Some(outcome) => json!({ "fired": true, "outcome": outcome }),
        None => json!({ "fired": true }),
    })
}

fn reset_timer(_: &Timers, cx: &OperationCx<'_, TimersConfig>, params: &Value) -> Result<Value> {
    let name = deadline_param(params);
    let deadline = deadline_of(cx, "resetTimer", name)?;
    permitted(cx, "reset", name)?;
    let facts = facts_of(cx)?;
    let data = cx.data()?;
    let Some(start) = start_of(deadline, &data, &facts) else {
        let reason = match &deadline.start {
            TimersStart::Enters(state) => format!(
                "deadline {name} starts when the status enters {state}, and {} {} {}; it starts at the next entry",
                cx.schema(),
                cx.id(),
                if facts.status.as_deref() == Some(state.as_str()) {
                    "entered it before Workflow kept when".to_owned()
                } else {
                    format!("is {}", facts.status.as_deref().unwrap_or("in no state"))
                }
            ),
            other => format!(
                "deadline {name} starts from {}, which holds no date-time on {} {}",
                match other {
                    TimersStart::Field(field) => field.as_str(),
                    _ => "the create",
                },
                cx.schema(),
                cx.id()
            ),
        };
        return Err(refuse(cx, "resetTimer", reason, "not_armed"));
    };
    if cancelled(deadline, facts.status.as_deref()) {
        return Err(refuse(
            cx,
            "resetTimer",
            format!(
                "deadline {name} is cancelled while {} {} is {}",
                cx.schema(),
                cx.id(),
                facts.status.as_deref().unwrap_or_default()
            ),
            "not_armed",
        ));
    }
    let due_at = start.at + deadline.after_ms;
    let rows = rows_of(cx)?;
    if let Some(row) = row_named(&rows, name) {
        if !row.fired
            && row.due_at == Some(due_at)
            && row.base_at == Some(start.at)
            && row.armed_from.as_deref() == Some(start.fact.as_str())
        {
            cx.unchanged();
            return Ok(json!({ "dueAt": due_at }));
        }
    }
    Ok(json!({ "dueAt": arm(cx, name, deadline, &start)? }))
}

fn snooze_timer(_: &Timers, cx: &OperationCx<'_, TimersConfig>, params: &Value) -> Result<Value> {
    let name = deadline_param(params);
    let deadline = deadline_of(cx, "snoozeTimer", name)?;
    permitted(cx, "snooze", name)?;
    let rows = rows_of(cx)?;
    let Some(row) = row_named(&rows, name).filter(|row| row.due_at.is_some()) else {
        return Err(refuse(
            cx,
            "snoozeTimer",
            format!(
                "deadline {name} of {} {} has not started or was cancelled, so it has no due time to move",
                cx.schema(),
                cx.id()
            ),
            "not_armed",
        ));
    };
    if deadline.until.is_some() {
        let status = facts_of(cx)?.status;
        if cancelled(deadline, status.as_deref()) {
            return Err(refuse(
                cx,
                "snoozeTimer",
                format!(
                    "deadline {name} is cancelled while {} {} is {}",
                    cx.schema(),
                    cx.id(),
                    status.as_deref().unwrap_or_default()
                ),
                "not_armed",
            ));
        }
    }
    let due_at = match params.get("dueAt") {
        Some(Value::Number(due_at)) => number_value(due_at) as i64,
        _ => 0,
    };
    if !row.fired && row.due_at == Some(due_at) {
        cx.unchanged();
        return Ok(json!({ "dueAt": due_at }));
    }
    let table = cx.sql().table("deadlines")?;
    let mut values = vec![SqlValue::Integer(due_at)];
    values.extend(key(cx));
    values.push(SqlValue::from(name));
    cx.sql().run(
        format!("UPDATE {table} SET due_at = ?, fired = 0, fired_at = NULL, outcome = NULL WHERE namespace = ? AND schema = ? AND id = ? AND name = ?"),
        &values,
    )?;
    Ok(json!({ "dueAt": due_at }))
}

// after_change arms each deadline whose fact has changed, then cancels
// each pending one whose until holds, from the instance as the change left
// it.
fn after_change(
    _: &Timers,
    cx: &InstanceCx<'_, TimersConfig>,
    change: &InstanceChange<'_>,
) -> Result<()> {
    let table = cx.sql().table("deadlines")?;
    if matches!(change, InstanceChange::Delete) {
        cx.sql().run(
            format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
            &key(cx),
        )?;
        return Ok(());
    }
    let facts = facts_of(cx)?;
    let rows = rows_of(cx)?;
    let data = cx.data()?;
    for (name, deadline) in &cx.config().deadlines {
        let row = row_named(&rows, name);
        let start = start_of(deadline, &data, &facts);
        if matches!(deadline.start, TimersStart::Field(_)) && start.is_none() {
            // The field cleared, or never set: no deadline.
            if row.is_some() {
                let mut params = key(cx);
                params.push(SqlValue::from(name.as_str()));
                cx.sql().run(
                    format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND name = ?"),
                    &params,
                )?;
            }
            continue;
        }
        let mut pending = row.is_some_and(|row| row.due_at.is_some() && !row.fired);
        if let Some(start) = &start {
            if row.and_then(|row| row.armed_from.as_deref()) != Some(start.fact.as_str()) {
                arm(cx, name, deadline, start)?;
                pending = true;
            }
        }
        if pending && cancelled(deadline, facts.status.as_deref()) {
            cancel(cx, name)?;
        }
    }
    Ok(())
}

// after_config_change: a removed deadline's rows go, and a changed
// start's, which arm again at the next write; a new afterMs moves each
// pending deadline its start fixes; a new field deadline arms from every
// instance's fields.
fn after_config_change(
    _: &Timers,
    cx: &superschematic_engine::behaviors::PublishCx<'_, TimersConfig>,
) -> Result<()> {
    let table = cx.sql().table("deadlines")?;
    let scope = [SqlValue::from(cx.namespace()), SqlValue::from(cx.schema())];
    let Some(config) = cx.config() else {
        cx.sql().run(
            format!("DELETE FROM {table} WHERE namespace = ? AND schema = ?"),
            &scope,
        )?;
        return Ok(());
    };
    let before: &[(String, TimersDeadline)] = cx
        .before()
        .map_or(&[], |before| before.deadlines.as_slice());
    let old_of = |name: &str| {
        before
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, deadline)| deadline)
    };
    let remove = |name: &str| -> Result<()> {
        let mut params = scope.to_vec();
        params.push(SqlValue::from(name));
        cx.sql().run(
            format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND name = ?"),
            &params,
        )?;
        Ok(())
    };
    let mut armed: Vec<(&str, &TimersDeadline)> = Vec::new();
    for (name, deadline) in &config.deadlines {
        let old = old_of(name);
        if let Some(old) = old.filter(|old| old.start == deadline.start) {
            if old.after_ms != deadline.after_ms {
                // A snoozed deadline keeps the time it was snoozed to.
                let mut params = vec![SqlValue::Integer(deadline.after_ms)];
                params.extend(scope.iter().cloned());
                params.push(SqlValue::from(name.as_str()));
                params.push(SqlValue::Integer(old.after_ms));
                cx.sql().run(
                    format!("UPDATE {table} SET due_at = base_at + ? WHERE namespace = ? AND schema = ? AND name = ? AND fired = 0 AND due_at = base_at + ?"),
                    &params,
                )?;
            }
            continue;
        }
        if old.is_some() {
            remove(name)?;
        }
        if matches!(deadline.start, TimersStart::Field(_)) {
            armed.push((name, deadline));
        }
    }
    for (name, _) in before {
        if config.deadline(name).is_none() {
            remove(name)?;
        }
    }
    if armed.is_empty() {
        return Ok(());
    }
    let upsert = arm_sql(&table);
    let facts = Facts {
        created_at: cx.now(),
        status: None,
        entered_at: None,
    };
    cx.each_instance(|instance| {
        for (name, deadline) in &armed {
            let Some(start) = start_of(deadline, &instance.data, &facts) else {
                continue;
            };
            let mut params = scope.to_vec();
            params.extend([
                SqlValue::from(instance.id.as_str()),
                SqlValue::from(*name),
                SqlValue::Integer(start.at),
                SqlValue::from(start.fact.as_str()),
                SqlValue::Integer(start.at + deadline.after_ms),
            ]);
            cx.sql().run(upsert.as_str(), &params)?;
        }
        Ok(())
    })
}

// fire invokes fireTimer on each deadline due and not fired, earliest due
// first, through the partial index, at most MAX_SWEEP a run. The keyset
// moves past each page, so a deadline whose fire is refused (a Lease's
// guard, say) is read once a run and holds nothing back.
fn fire(_: &Timers, cx: &ScheduleCx<'_, TimersConfig>) -> Result<()> {
    let table = cx.sql().table("deadlines")?;
    let mut after: Option<(i64, String, String)> = None;
    let mut swept = 0;
    while swept < MAX_SWEEP {
        let mut params = vec![
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::Integer(cx.now()),
        ];
        let past = match &after {
            Some((due_at, id, name)) => {
                params.extend([
                    SqlValue::Integer(*due_at),
                    SqlValue::from(id.as_str()),
                    SqlValue::from(name.as_str()),
                ]);
                " AND (due_at, id, name) > (?, ?, ?)"
            }
            None => "",
        };
        params.push(SqlValue::Integer(SWEEP_BATCH.min(MAX_SWEEP - swept)));
        let due = cx.sql().all(
            format!(
                "SELECT due_at, id, name FROM {table}
                 WHERE namespace = ? AND schema = ? AND fired = 0 AND due_at <= ?{past}
                 ORDER BY due_at, id, name LIMIT ?"
            ),
            &params,
        )?;
        for row in &due {
            match cx.instances().invoke(
                cx.schema(),
                row.text("id")?,
                "fireTimer",
                &json!({ "deadline": row.text("name")? }),
                None,
            ) {
                Ok(_) => {}
                Err(error) if error.is_veto() => {}
                Err(error) => return Err(error),
            }
        }
        swept += due.len() as i64;
        if (due.len() as i64) < SWEEP_BATCH {
            return Ok(());
        }
        let last = due.last().expect("a full page has a last row");
        after = Some((
            last.i64("due_at")?,
            last.text("id")?.to_owned(),
            last.text("name")?.to_owned(),
        ));
    }
    Ok(())
}

/// A Workflow config as the schema holds it: what `parse_config` checks a
/// deadline's states against.
struct Flow {
    states: Vec<String>,
    initial: Option<String>,
    transitions: Vec<(String, String)>,
}

// flow_of reads the type's Workflow config, when it has the shape Workflow
// takes; one of another shape is Workflow's to refuse.
fn flow_of(target: &ConfigTarget<'_>) -> Option<Flow> {
    let raw = target.configs().get("Workflow")?;
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
// are date-times.
fn check_field(target: &ConfigTarget<'_>, at: &str, field: &str) -> Result<()> {
    if !target.fields().iter().any(|known| known == field) {
        return Err(Error::config(format!(
            "{at}.start.field \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            target.fields().join(", ")
        )));
    }
    let format = target
        .field_schemas()
        .get(field)
        .and_then(|schema| schema.get("format"));
    if format.and_then(Value::as_str) != Some("date-time") {
        return Err(Error::config(format!(
            "{at}.start.field \"{field}\" is not a date-time field of {}: a deadline starts from the time a value holds",
            target.type_name()
        )));
    }
    Ok(())
}

// check_move holds a then to the type's Workflow: its transition and every
// from state are states of it, and a transition leads from each to it.
fn check_move(flow: &Flow, at: &str, transition: &str, from: &[String]) -> Result<()> {
    let states = flow.states.join(", ");
    if !flow.states.iter().any(|state| state == transition) {
        return Err(Error::config(format!(
            "{at}.transition \"{transition}\" is not a state of the type's Workflow ({states})"
        )));
    }
    for state in from {
        if !flow.states.contains(state) {
            return Err(Error::config(format!(
                "{at}.from names \"{state}\", which is not a state of the type's Workflow ({states})"
            )));
        }
        if state == transition {
            return Err(Error::config(format!(
                "{at} moves the status from \"{state}\" to itself"
            )));
        }
        if !flow
            .transitions
            .iter()
            .any(|(source, to)| source == state && to == transition)
        {
            return Err(Error::config(format!(
                "{at}: no transition of the type's Workflow leads from \"{state}\" to \"{transition}\""
            )));
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
    flow: Option<&Flow>,
    name: &str,
    raw: &Value,
) -> Result<TimersDeadline> {
    let at = format!("deadlines.{name}");
    let needs_flow = |place: &str| -> Result<()> {
        if target
            .behaviors()
            .iter()
            .any(|behavior| behavior == "Workflow")
        {
            return Ok(());
        }
        Err(Error::config(format!(
            "{at}.{place} reads the status of Workflow, which the type does not list"
        )))
    };
    let is_state = |place: &str, state: &str| -> Result<()> {
        match flow {
            Some(flow) if !flow.states.iter().any(|known| known == state) => {
                Err(Error::config(format!(
                    "{at}.{place} \"{state}\" is not a state of the type's Workflow ({})",
                    flow.states.join(", ")
                )))
            }
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
    let after_ms = match raw.get("afterMs") {
        Some(Value::Number(ms)) => number_value(ms) as i64,
        _ => 0,
    };
    if after_ms < 0 && !field_start {
        return Err(Error::config(format!(
            "{at}.afterMs is negative, which only a field start takes: a deadline that starts at the create or at an entry is not due before it"
        )));
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
                    return Err(Error::config(format!(
                        "{at}.then names no from: a deadline that starts at a field is in no state of its own, so then.from names the states its move leaves"
                    )))
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
                    return Err(Error::config(format!(
                        "{at}.until.leaves: a deadline that starts at a field is in no state of its own to leave; until.enters names the states that cancel it"
                    )));
                }
                is_state("until.leaves", leaves)?;
                if let Some(state) = start_state.as_deref().filter(|state| *state != leaves) {
                    return Err(Error::config(format!(
                        "{at}.until.leaves \"{leaves}\" is not the state the deadline starts in, \"{state}\", which until.leaves cancels it on leaving"
                    )));
                }
                Some(TimersUntil::Leaves(leaves.to_owned()))
            } else {
                let enters = strings(until.get("enters"));
                for state in &enters {
                    is_state("until.enters", state)?;
                    if start_state.as_deref() == Some(state.as_str()) {
                        return Err(Error::config(format!(
                            "{at}.until.enters names \"{state}\", the state the deadline starts in, which would cancel it as it starts"
                        )));
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

/// The time a date-time string holds, in epoch milliseconds, as
/// JavaScript's `Date.parse` reads the ISO 8601 forms (V8's): a date
/// (`YYYY-MM-DD`, or a signed six-digit year), alone at midnight UTC or
/// with a time after `T`, `t` or a space (`HH:mm`, `:ss` and a fraction
/// whose first three digits count), then `Z`, `z` or an offset (`+HH:mm`,
/// `+HHmm`). A day past its month's end runs into the next, and `24:00` is
/// the next midnight. A time without an offset is read as UTC, where
/// JavaScript reads the host's local time. `None` where `Date.parse` gives
/// NaN for these forms, and for every other form.
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
}
