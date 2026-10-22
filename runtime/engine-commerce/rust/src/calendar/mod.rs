//! `Calendar` (D70): time a supply opens, on a bookable resource: a
//! stylist, a chair, a room, a collection counter, a pool of bikes. It is a
//! timed supply of the contract ([`crate::supply`], `SupplyKind::Timed`):
//! a claim's `Holds` holds, commits and releases intervals of it through
//! `holdFor`, `commitFor` and `releaseFor`, and asks `checkHold` first.
//!
//! The config gives weekly opening hours as wall times in an IANA
//! `timeZone` ([`time`]); a `capacity`, an integer or an own integer field,
//! of holds that may overlap; a start grid (`slotMs`, any whole minute when
//! absent); length bounds (`minMs`, `slotMs` or a minute when absent;
//! `maxMs`, a day); buffers kept free before and after each hold; a lead
//! time and a horizon, against the engine's clock; and the `permission`
//! that sets an instance's own hours and closures.
//!
//! `holdFor` checks, in order, and vetoes with the first that fails:
//! - the interval lies inside the opening hours (`outside_hours`) and
//!   overlaps no closure (`closed`); these read only the interval's first
//!   `maxMs` and a minute, so an interval of centuries costs what the
//!   longest hold does and is `too_long` once that much is open;
//! - it starts on the grid (`off_grid`);
//! - its length is within the bounds (`too_short`, `too_long`);
//! - it starts no sooner than the lead time (`too_soon`) and no further
//!   than the horizon (`too_far`) from now;
//! - the most holds over any instant of it, each with its buffers, and its
//!   own buffers, stay below capacity (`unavailable`, with the hold that
//!   conflicts at the peak). Counting overlapping holds instead would
//!   count a long hold that two short ones overlap at different times
//!   twice.
//!
//! `checkHold` answers the same without the holder check or a write, as
//! the supply's record of the item allows (an item held already fits, one
//! committed or released is `not_requested`), counting the intervals its
//! `also` lists as held. `findAvailability`, at schema scope, lists the
//! slots of a duration that would fit across the schema's instances over a
//! window of at most 31 days, soonest first: one sweep of each instance's
//! holds, which jumps past a full instant, a closure or a closed stretch,
//! and stops at [`MAX_FIND_WORK`] (`truncated`). `listBookings` pages an
//! instance's holds over such a window, a deleted claim's committed time
//! among them.
//!
//! Storage, the same SQL on both databases: two columns of the instance,
//! its own `hours` (JSON text) and `time_zone`, null for the config's, and
//! a count of the closures it has made, which names the next; the
//! contract's record table (`holds`), whose index on (instance, start)
//! serves the overlap read; and `closures`.
//!
//! Every field is volatile: `hours` and `timeZone` follow the config while
//! the instance has none of its own, and `closures` reads the first 100
//! that have not ended by the clock.
//!
//! An instance is not deleted while it holds time (`held`): an item held,
//! or committed time that has not ended. `setOpeningHours` changes its
//! own time zone, as a new version does the config's, only while it holds
//! no time that has not ended (`has_bookings`).
//!
//! configChange: hours, the grid, buffers, lead time and horizon may change
//! and apply to holds made after; `capacity` falls no lower than the most
//! holds that have not ended overlap on any instance; the `timeZone`
//! changes only while no hold has future time. Calendar joins a schema
//! that has instances, and leaves only one that has none. An update that
//! lowers a capacity field below the instance's peak is `has_bookings`.

mod guidance;
pub mod time;

use jiff::tz::TimeZone;
use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, IndexSpec, InstanceView, PublishCx,
    SchemaCx, SqlReader, TableSpec,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::json::{js_keys, number_value, stringify};
use superschematic_engine::storage::{sql_string, Dialect, Row, Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, OperationCx, Result,
};

use crate::supply::{
    self, Ask, Begun, CheckAnswer, InstanceRef, Interval, ItemState, Refusal, SupplyCall,
    SupplyKind, SupplyOperation,
};
use time::{
    grid, inside, on_grid, opening, peak, peak_at, wall_text, zone, Offsets, WeekHours, DAY_MS,
    EARLIEST_MS, LATEST_MS, MINUTE_MS,
};

/// The behavior's name.
pub const NAME: &str = "Calendar";

/// The table of the contract's records: one per holder's item.
pub const HOLDS_TABLE: &str = "holds";
/// The table of closures.
pub const CLOSURES_TABLE: &str = "closures";

/// The longest window listBookings and findAvailability read.
pub const MAX_WINDOW_MS: i64 = 31 * DAY_MS;
/// The most closures the `closures` field shows.
pub const MAX_CLOSURES_SHOWN: i64 = 100;
/// The most instances one findAvailability reads.
pub const MAX_INSTANCES: usize = 100;
/// The slots findAvailability returns when the call gives no limit.
pub const DEFAULT_SLOTS: i64 = 100;
/// The longest hold when the config gives no maxMs.
pub const DEFAULT_MAX_MS: i64 = DAY_MS;
/// The most work one findAvailability does: each start it judges counts
/// one, and one more for each hold it weighs that start against. Past
/// it, the answer is `truncated`.
pub const MAX_FIND_WORK: usize = 2_000_000;

/// The vetoes Calendar adds to the contract's.
pub const OUTSIDE_HOURS: &str = "outside_hours";
pub const CLOSED: &str = "closed";
pub const OFF_GRID: &str = "off_grid";
pub const TOO_SHORT: &str = "too_short";
pub const TOO_LONG: &str = "too_long";
pub const TOO_SOON: &str = "too_soon";
pub const TOO_FAR: &str = "too_far";
pub const HAS_BOOKINGS: &str = "has_bookings";
pub const WINDOW_TOO_LONG: &str = "window_too_long";
pub const NO_CLOSURE: &str = "no_closure";

/// How many holds may overlap.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Capacity {
    Fixed(i64),
    /// An own integer field of the type, whose value of at least 1 is the
    /// instance's; anything else is 0.
    Field(String),
}

/// Calendar's config, parsed: the defaults filled in.
#[derive(Clone, Debug)]
pub struct CalendarConfig {
    pub time_zone: String,
    pub tz: TimeZone,
    pub hours: WeekHours,
    /// The hours as the config gives them, which the `hours` field shows.
    pub hours_json: Value,
    pub capacity: Capacity,
    pub slot_ms: Option<i64>,
    pub min_ms: i64,
    pub max_ms: i64,
    pub buffer_before: i64,
    pub buffer_after: i64,
    pub lead_ms: i64,
    pub horizon_ms: Option<i64>,
    pub permission: Option<String>,
    /// The type's own fields of one JSON type, string, number, integer or
    /// boolean, which findAvailability's `where` may name.
    pub filterable: Vec<String>,
}

impl CalendarConfig {
    /// The grid's step.
    pub fn grid_ms(&self) -> i64 {
        self.slot_ms.unwrap_or(MINUTE_MS)
    }

    /// The time an interval keeps from other holds: its buffers too.
    fn occupied(&self, interval: &Interval) -> Interval {
        Interval {
            start_ms: interval.start_ms - self.buffer_before,
            end_ms: interval.end_ms + self.buffer_after,
        }
    }

    /// What the rules read of an interval: all of it while it is no longer
    /// than maxMs, and its first maxMs and a minute past that, which is
    /// enough to find it too long, so an interval of centuries reads no
    /// more hours, closures or holds than the longest hold does.
    fn judged(&self, asked: &Interval) -> Interval {
        Interval {
            start_ms: asked.start_ms,
            end_ms: asked
                .end_ms
                .min(asked.start_ms.saturating_add(self.max_ms + MINUTE_MS)),
        }
    }

    /// The holds whose time with buffers can meet an interval's with
    /// buffers start before `.1` and end after `.0`.
    fn reach(&self, interval: &Interval) -> (i64, i64) {
        let pad = self.buffer_before + self.buffer_after;
        (interval.start_ms - pad, interval.end_ms + pad)
    }

    /// The instance's capacity, from its own fields.
    fn capacity_of(&self, data: &Map<String, Value>) -> i64 {
        match &self.capacity {
            Capacity::Fixed(count) => *count,
            Capacity::Field(field) => data
                .get(field)
                .and_then(integer)
                .filter(|count| *count >= 1)
                .unwrap_or(0),
        }
    }
}

/// Calendar: time a supply opens.
#[derive(Clone, Copy, Debug, Default)]
pub struct Calendar;

/// What an instance's hours are in: its own, or the config's.
#[derive(Clone, Debug)]
struct Settings {
    time_zone: String,
    tz: TimeZone,
    hours: WeekHours,
    hours_json: Value,
}

/// A closure of an instance.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Closure {
    closure: String,
    interval: Interval,
    reason: String,
}

impl Closure {
    fn to_json(&self) -> Value {
        json!({
            "closure": self.closure,
            "startMs": self.interval.start_ms,
            "endMs": self.interval.end_ms,
            "reason": self.reason,
        })
    }
}

/// A record that takes time: held, or committed.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Taken {
    holder: InstanceRef,
    hold: String,
    item: i64,
    interval: Interval,
    state: ItemState,
    /// A deleted holder's committed time, which stays booked.
    retired: bool,
}

impl Taken {
    fn to_json(&self) -> Value {
        json!({
            "holder": self.holder.to_json(),
            "hold": self.hold,
            "item": self.item,
            "startMs": self.interval.start_ms,
            "endMs": self.interval.end_ms,
            "state": self.state.as_str(),
        })
    }

    /// Whether the record is the live one of the item a call names.
    fn is(&self, call: &SupplyCall) -> bool {
        !self.retired
            && self.holder == call.holder
            && self.hold == call.hold
            && self.item == call.item
    }
}

/// One instance as the rules read it.
struct Resource {
    settings: Settings,
    capacity: i64,
    closures: Vec<Closure>,
    taken: Vec<Taken>,
}

/// A refusal of what an interval asks: a code, why, and the members its
/// details add to the call's.
#[derive(Clone, Debug, PartialEq)]
struct Problem {
    code: &'static str,
    reason: String,
    extra: Map<String, Value>,
}

fn problem(code: &'static str, reason: String, extra: Value) -> Problem {
    Problem {
        code,
        reason,
        extra: match extra {
            Value::Object(members) => members,
            _ => Map::new(),
        },
    }
}

// integer reads a JSON integer, as a number that holds one.
fn integer(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    match number.as_i64() {
        Some(exact) => Some(exact),
        None => {
            let value = number_value(number);
            (value.is_finite() && value.fract() == 0.0).then_some(value as i64)
        }
    }
}

fn int_param(params: &Value, name: &str) -> Option<i64> {
    params.get(name).and_then(integer)
}

/// A wall time as a reader says it, `2026-10-25 01:30`.
fn wall_label(wall: i64) -> String {
    let day = wall.div_euclid(DAY_MS);
    let date = jiff::Timestamp::from_millisecond(day * DAY_MS)
        .map(|at| at.strftime("%Y-%m-%d").to_string())
        .unwrap_or_else(|_| day.to_string());
    format!("{date} {}", wall_text(wall - day * DAY_MS))
}

fn params_error(operation: &str, path: &str, message: impl Into<String>) -> Error {
    EngineError::operation_params(NAME, operation, vec![SchemaIssue::new(path, message)]).into()
}

fn veto(schema: &str, id: &str, operation: &str, problem: Problem) -> Error {
    Error::veto(
        NAME,
        operation,
        schema,
        id,
        Veto {
            reason: problem.reason,
            code: Some(problem.code.to_owned()),
            details: (!problem.extra.is_empty()).then_some(problem.extra),
        },
    )
}

fn forbidden(message: String) -> Error {
    EngineError::new(EngineErrorCode::Forbidden, message).into()
}

// permitted holds an operation to the config's permission, which nobody
// holds when the config names none.
fn permitted(view: &InstanceView<'_, CalendarConfig>, operation: &str) -> Result<()> {
    let who = format!(
        "{} may not {operation} on {} {}",
        view.principal().subject,
        view.schema(),
        view.id()
    );
    match &view.config().permission {
        None => Err(forbidden(format!(
            "{who}: {}'s Calendar config names no permission, so nobody may",
            view.schema()
        ))),
        Some(permission) if !view.can(permission)? => Err(forbidden(format!(
            "{who}: it needs permission {permission}"
        ))),
        Some(_) => Ok(()),
    }
}

// may_force holds a closure over held time to the permission's override.
fn may_force(view: &InstanceView<'_, CalendarConfig>) -> Result<()> {
    let Some(permission) = &view.config().permission else {
        return permitted(view, "addClosure");
    };
    let needed = format!("{permission}.override");
    if view.can(&needed)? {
        return Ok(());
    }
    Err(forbidden(format!(
        "{} may not close {} {} over held time: force needs permission {needed}",
        view.principal().subject,
        view.schema(),
        view.id()
    )))
}

// settings_of is an instance's hours and zone: its own columns, else the
// config's.
fn settings_of(
    config: &CalendarConfig,
    own_hours: Option<&str>,
    own_zone: Option<&str>,
) -> Result<Settings> {
    let (time_zone, tz) = match own_zone {
        Some(name) => (
            name.to_owned(),
            zone(name).ok_or_else(|| {
                Error::behavior(NAME, format!("an instance keeps the time zone {name}"))
            })?,
        ),
        None => (config.time_zone.clone(), config.tz.clone()),
    };
    let (hours, hours_json) = match own_hours {
        Some(text) => {
            let json: Value = serde_json::from_str(text)
                .map_err(|error| Error::behavior(NAME, format!("an instance's hours: {error}")))?;
            let hours = WeekHours::parse(&json).map_err(|problem| {
                Error::behavior(
                    NAME,
                    format!("an instance's hours{}: {}", problem.path, problem.message),
                )
            })?;
            (hours, json)
        }
        None => (config.hours.clone(), config.hours_json.clone()),
    };
    Ok(Settings {
        time_zone,
        tz,
        hours,
        hours_json,
    })
}

fn settings<C>(view: &InstanceView<'_, C>, config: &CalendarConfig) -> Result<Settings> {
    let columns = view.columns().get()?;
    settings_of(
        config,
        columns.opt_text("hours")?,
        columns.opt_text("time_zone")?,
    )
}

fn marks(count: usize) -> String {
    vec!["?"; count].join(", ")
}

fn taken_of(row: &Row) -> Result<Taken> {
    let state = row.text("state")?;
    Ok(Taken {
        holder: InstanceRef::new(row.text("holder_schema")?, row.text("holder_id")?),
        hold: row.text("hold")?.to_owned(),
        item: row.i64("item")?,
        interval: Interval {
            start_ms: row.i64("start_ms")?,
            end_ms: row.i64("end_ms")?,
        },
        state: ItemState::parse(state)
            .ok_or_else(|| Error::behavior(NAME, format!("a record holds the state {state}")))?,
        retired: row.i64("retired")? != 0,
    })
}

const TAKEN_COLUMNS: &str =
    "id, holder_schema, holder_id, hold, item, start_ms, end_ms, state, retired";

// taken_over reads the records of the instances `ids` that take time from
// `from` to `to`, by instance and start, through the index on (instance,
// start).
fn taken_over(
    sql: &SqlReader<'_>,
    namespace: &str,
    schema: &str,
    ids: &[&str],
    from: i64,
    to: i64,
) -> Result<Vec<(String, Taken)>> {
    if ids.is_empty() {
        return Ok(Vec::new());
    }
    let table = sql.table(HOLDS_TABLE)?;
    let mut values = vec![SqlValue::from(namespace), SqlValue::from(schema)];
    values.extend(ids.iter().map(|id| SqlValue::from(*id)));
    values.extend([SqlValue::from(to), SqlValue::from(from)]);
    let select = format!(
        "SELECT {TAKEN_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id IN ({}) AND state IN ('held', 'committed') AND start_ms < ? AND end_ms > ?",
        marks(ids.len())
    );
    sql.all(
        Sql::per(
            format!("{select} ORDER BY id, start_ms, end_ms, holder_schema, holder_id, hold, item"),
            format!("{select} ORDER BY id COLLATE \"C\", start_ms, end_ms, holder_schema COLLATE \"C\", holder_id COLLATE \"C\", hold COLLATE \"C\", item"),
        ),
        &values,
    )?
    .iter()
    .map(|row| Ok((row.text("id")?.to_owned(), taken_of(row)?)))
    .collect()
}

// closures_over reads the closures of the instances `ids` that overlap
// `from` to `to`, by instance and start.
fn closures_over(
    sql: &SqlReader<'_>,
    namespace: &str,
    schema: &str,
    ids: &[&str],
    from: i64,
    to: i64,
    limit: Option<i64>,
) -> Result<Vec<(String, Closure)>> {
    if ids.is_empty() {
        return Ok(Vec::new());
    }
    let table = sql.table(CLOSURES_TABLE)?;
    let mut values = vec![SqlValue::from(namespace), SqlValue::from(schema)];
    values.extend(ids.iter().map(|id| SqlValue::from(*id)));
    values.extend([SqlValue::from(to), SqlValue::from(from)]);
    let limited = match limit {
        Some(limit) => {
            values.push(SqlValue::from(limit));
            " LIMIT ?"
        }
        None => "",
    };
    let select = format!(
        "SELECT id, closure, start_ms, end_ms, reason FROM {table} WHERE namespace = ? AND schema = ? AND id IN ({}) AND start_ms < ? AND end_ms > ?",
        marks(ids.len())
    );
    sql.all(
        Sql::per(
            format!("{select} ORDER BY id, start_ms, end_ms, closure{limited}"),
            format!("{select} ORDER BY id COLLATE \"C\", start_ms, end_ms, closure COLLATE \"C\"{limited}"),
        ),
        &values,
    )?
    .iter()
    .map(|row| {
        Ok((
            row.text("id")?.to_owned(),
            Closure {
                closure: row.text("closure")?.to_owned(),
                interval: Interval {
                    start_ms: row.i64("start_ms")?,
                    end_ms: row.i64("end_ms")?,
                },
                reason: row.text("reason")?.to_owned(),
            },
        ))
    })
    .collect()
}

// resource reads an instance as the rules for an interval read it: its
// settings, its capacity, the closures over what the rules judge of the
// interval and the records whose time with buffers can meet it.
fn resource(view: &InstanceView<'_, CalendarConfig>, asked: &Interval) -> Result<Resource> {
    let config = view.config();
    let sql = view.sql();
    let ids = [view.id()];
    let asked = &config.judged(asked);
    let (from, to) = config.reach(asked);
    Ok(Resource {
        settings: settings(view, config)?,
        capacity: config.capacity_of(&*view.data()?),
        closures: closures_over(
            &sql,
            view.namespace(),
            view.schema(),
            &ids,
            asked.start_ms,
            asked.end_ms,
            None,
        )?
        .into_iter()
        .map(|(_, closure)| closure)
        .collect(),
        taken: taken_over(&sql, view.namespace(), view.schema(), &ids, from, to)?
            .into_iter()
            .map(|(_, taken)| taken)
            .collect(),
    })
}

// capacity_left is what capacity leaves of an interval: capacity less the
// most of the other records over it, and of the intervals `also` the
// claim's other items ask, each with buffers; and the one that conflicts
// at the peak.
fn capacity_left(
    config: &CalendarConfig,
    resource: &Resource,
    asked: &Interval,
    except: Option<&SupplyCall>,
    also: &[Interval],
) -> (i64, Option<Interval>) {
    let others: Vec<Interval> = resource
        .taken
        .iter()
        .filter(|taken| except.is_none_or(|call| !taken.is(call)))
        .map(|taken| taken.interval)
        .chain(also.iter().copied())
        .collect();
    let occupied: Vec<Interval> = others
        .iter()
        .map(|interval| config.occupied(interval))
        .collect();
    let (most, first) = peak(&occupied, &config.occupied(asked));
    (resource.capacity - most, first.map(|index| others[index]))
}

// judge holds an interval to holdFor's rules, in their order, at `now`,
// leaving out the call's own record and counting the intervals `also` the
// claim's other items ask: the first refusal, or how many more holds could
// take it. The hours and closures are read over what the rules judge of
// the interval (`CalendarConfig::judged`), so the work is the longest
// hold's at most: an interval longer than that is too long once its first
// maxMs is open.
fn judge(
    config: &CalendarConfig,
    label: &str,
    resource: &Resource,
    asked: &Interval,
    now: i64,
    except: Option<&SupplyCall>,
    also: &[Interval],
) -> std::result::Result<i64, Problem> {
    let settings = &resource.settings;
    if asked.start_ms < EARLIEST_MS || asked.end_ms > LATEST_MS {
        return Err(problem(
            OUTSIDE_HOURS,
            format!("{label} opens no time before 1900 or from 9000 on"),
            json!({ "timeZone": settings.time_zone }),
        ));
    }
    let judged = config.judged(asked);
    let offsets = Offsets::over(&settings.tz, judged.start_ms, judged.end_ms);
    let span = format!(
        "{} to {} {}",
        wall_label(offsets.wall(asked.start_ms)),
        wall_label(offsets.wall(asked.end_ms)),
        settings.time_zone
    );
    let open = opening(&settings.hours, &offsets, judged.start_ms, judged.end_ms);
    if !inside(&open, &judged) {
        return Err(problem(
            OUTSIDE_HOURS,
            format!("{label} is not open for all of {span}"),
            json!({ "timeZone": settings.time_zone }),
        ));
    }
    if let Some(closure) = resource
        .closures
        .iter()
        .find(|closure| closure.interval.overlaps(&judged))
    {
        return Err(problem(
            CLOSED,
            format!("{label} is closed for part of {span}: {}", closure.reason),
            json!({ "closure": closure.to_json() }),
        ));
    }
    let grid_ms = config.grid_ms();
    if !on_grid(&settings.hours, &offsets, asked.start_ms, grid_ms) {
        return Err(problem(
            OFF_GRID,
            format!(
                "{span} does not start a whole number of {} after an opening time of {label}",
                guidance::duration(grid_ms)
            ),
            json!({ "slotMs": grid_ms }),
        ));
    }
    let length = asked.end_ms - asked.start_ms;
    if length < config.min_ms {
        return Err(problem(
            TOO_SHORT,
            format!(
                "{} is shorter than {label}'s shortest hold, {}",
                guidance::duration(length),
                guidance::duration(config.min_ms)
            ),
            json!({ "minMs": config.min_ms }),
        ));
    }
    if length > config.max_ms {
        return Err(problem(
            TOO_LONG,
            format!(
                "{} is longer than {label}'s longest hold, {}",
                guidance::duration(length),
                guidance::duration(config.max_ms)
            ),
            json!({ "maxMs": config.max_ms }),
        ));
    }
    let earliest = now + config.lead_ms;
    if asked.start_ms < earliest {
        return Err(problem(
            TOO_SOON,
            if config.lead_ms == 0 {
                format!("{span} starts in the past")
            } else {
                format!(
                    "{span} starts sooner than {} from now",
                    guidance::duration(config.lead_ms)
                )
            },
            json!({ "earliestMs": earliest }),
        ));
    }
    if let Some(horizon) = config.horizon_ms {
        let latest = now + horizon;
        if asked.start_ms > latest {
            return Err(problem(
                TOO_FAR,
                format!(
                    "{span} starts further than {} from now",
                    guidance::duration(horizon)
                ),
                json!({ "latestMs": latest }),
            ));
        }
    }
    let (left, conflict) = capacity_left(config, resource, asked, except, also);
    if left <= 0 {
        let reason = match resource.capacity {
            capacity if capacity <= 0 => format!("{label} takes no holds"),
            1 => format!("{label} is held for part of {span}"),
            capacity => format!("{label} holds {capacity} at once, and is full for part of {span}"),
        };
        return Err(problem(
            supply::UNAVAILABLE,
            reason,
            match conflict {
                Some(conflict) => json!({ "conflict": conflict.to_json() }),
                None => json!({ "available": 0 }),
            },
        ));
    }
    Ok(left)
}

// refusal makes a problem with a contract call's details: the supply, the
// hold and item, what was asked, then the problem's own members.
fn refusal(
    view: &InstanceView<'_, CalendarConfig>,
    call: &SupplyCall,
    problem: Problem,
) -> Refusal {
    let mut details = Map::new();
    details.insert("supply".into(), InstanceRef::of(view).to_json());
    details.insert("hold".into(), Value::from(call.hold.as_str()));
    details.insert("item".into(), Value::from(call.item));
    details.insert("asked".into(), call.ask.to_json());
    details.extend(problem.extra);
    Refusal {
        code: problem.code.to_owned(),
        reason: problem.reason,
        details,
    }
}

// fit is what does not fit of a contract call's interval, beside every
// other record and the intervals `also` the claim's other items ask.
fn fit(
    view: &InstanceView<'_, CalendarConfig>,
    call: &SupplyCall,
    also: &[Interval],
) -> Result<Option<Refusal>> {
    let Some(asked) = call.ask.interval() else {
        return Err(Error::behavior(NAME, "a call asks a quantity of time"));
    };
    let resource = resource(view, &asked)?;
    let label = format!("{} {}", view.schema(), view.id());
    Ok(judge(
        view.config(),
        &label,
        &resource,
        &asked,
        view.now(),
        Some(call),
        also,
    )
    .err()
    .map(|problem| refusal(view, call, problem)))
}

fn contract(
    cx: &OperationCx<'_, CalendarConfig>,
    params: &Value,
    operation: SupplyOperation,
) -> Result<Value> {
    let call = SupplyCall::parse(NAME, operation, SupplyKind::Timed, params)?;
    match supply::begin(cx, HOLDS_TABLE, operation, &call)? {
        Begun::Done(record) => Ok(supply::done(cx, operation, record.as_ref())),
        Begun::Apply { .. } => {
            if operation == SupplyOperation::Hold {
                if let Some(refusal) = fit(cx, &call, &[])? {
                    return Err(refusal.veto(cx, operation));
                }
            }
            supply::finish(cx, HOLDS_TABLE, operation, &call, None)
        }
    }
}

fn hold_for(_: &Calendar, cx: &OperationCx<'_, CalendarConfig>, params: &Value) -> Result<Value> {
    contract(cx, params, SupplyOperation::Hold)
}

fn commit_for(_: &Calendar, cx: &OperationCx<'_, CalendarConfig>, params: &Value) -> Result<Value> {
    contract(cx, params, SupplyOperation::Commit)
}

fn release_for(
    _: &Calendar,
    cx: &OperationCx<'_, CalendarConfig>,
    params: &Value,
) -> Result<Value> {
    contract(cx, params, SupplyOperation::Release)
}

// check_hold answers whether holdFor would hold the item now, as Stock's
// does: an item the supply holds already fits, one its record has moved
// past does not (not_requested), and any other is held to the rules beside
// the intervals `also` the claim's other items ask. It reads only, so it
// takes no holder check.
fn check_hold(_: &Calendar, cx: &OperationCx<'_, CalendarConfig>, params: &Value) -> Result<Value> {
    let call = SupplyCall::parse(NAME, SupplyOperation::Check, SupplyKind::Timed, params)?;
    let also: Vec<Interval> = supply::also(NAME, SupplyKind::Timed, params)?
        .iter()
        .filter_map(Ask::interval)
        .collect();
    let recorded = supply::recorded(cx, HOLDS_TABLE, &call)?;
    let answer = match supply::check_record(&call, recorded.as_ref()) {
        Some(answer) => answer,
        None => match fit(cx, &call, &also)? {
            Some(refusal) => CheckAnswer::Refused(refusal),
            None => CheckAnswer::Fits,
        },
    };
    Ok(answer.to_json())
}

// set_opening_hours sets the instance's own hours, its zone, or both; null
// for either goes back to the config's.
fn set_opening_hours(
    _: &Calendar,
    cx: &OperationCx<'_, CalendarConfig>,
    params: &Value,
) -> Result<Value> {
    const OPERATION: &str = "setOpeningHours";
    permitted(cx, OPERATION)?;
    let columns = cx.columns().get()?;
    let mut hours = columns.opt_text("hours")?.map(str::to_owned);
    let mut time_zone = columns.opt_text("time_zone")?.map(str::to_owned);
    let before = (hours.clone(), time_zone.clone());
    match params.get("hours") {
        None => {}
        Some(Value::Null) => hours = None,
        Some(given) => {
            WeekHours::parse(given).map_err(|problem| {
                params_error(
                    OPERATION,
                    &format!("/hours{}", problem.path),
                    problem.message,
                )
            })?;
            hours = Some(stringify(given));
        }
    }
    match params.get("timeZone") {
        None => {}
        Some(Value::Null) => time_zone = None,
        Some(given) => {
            let name = given.as_str().unwrap_or_default();
            if zone(name).is_none() {
                return Err(params_error(
                    OPERATION,
                    "/timeZone",
                    format!("{name} is not a time zone of the engine's database"),
                ));
            }
            time_zone = Some(name.to_owned());
        }
    }
    let was = settings_of(cx.config(), before.0.as_deref(), before.1.as_deref())?;
    let will = settings_of(cx.config(), hours.as_deref(), time_zone.as_deref())?;
    if was.time_zone != will.time_zone {
        // As a new version's timeZone: held time keeps its instants, and
        // its wall times would move under it.
        let future = all_taken_from(
            &cx.sql(),
            cx.namespace(),
            cx.schema(),
            Some(cx.id()),
            cx.now(),
        )?;
        if !future.is_empty() {
            return Err(veto(
                cx.schema(),
                cx.id(),
                OPERATION,
                problem(
                    HAS_BOOKINGS,
                    format!(
                        "{} {} changes its time zone from {} to {} only while it holds no time that has not ended, and it holds time for {}",
                        cx.schema(),
                        cx.id(),
                        was.time_zone,
                        will.time_zone,
                        guidance::plural(future.len() as i64, "claim item")
                    ),
                    json!({ "holds": future.iter().take(MAX_CLOSURES_SHOWN as usize).map(|(_, taken)| taken.to_json()).collect::<Vec<_>>() }),
                ),
            ));
        }
    }
    if (hours.clone(), time_zone.clone()) == before {
        cx.unchanged();
    } else {
        cx.columns().set(&[
            ("hours", SqlValue::from(hours.clone())),
            ("time_zone", SqlValue::from(time_zone.clone())),
        ])?;
    }
    Ok(json!({ "hours": will.hours_json, "timeZone": will.time_zone }))
}

fn interval_params(operation: &str, params: &Value) -> Result<Interval> {
    let start_ms = int_param(params, "startMs")
        .ok_or_else(|| params_error(operation, "/startMs", "must be an integer"))?;
    let end_ms = int_param(params, "endMs")
        .ok_or_else(|| params_error(operation, "/endMs", "must be an integer"))?;
    if end_ms <= start_ms {
        return Err(params_error(operation, "/endMs", "must be after startMs"));
    }
    Ok(Interval { start_ms, end_ms })
}

// add_closure closes the instance over an interval, refused over held time
// unless forced.
fn add_closure(
    _: &Calendar,
    cx: &OperationCx<'_, CalendarConfig>,
    params: &Value,
) -> Result<Value> {
    const OPERATION: &str = "addClosure";
    permitted(cx, OPERATION)?;
    let interval = interval_params(OPERATION, params)?;
    let reason = params
        .get("reason")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    let force = params.get("force") == Some(&Value::Bool(true));
    if force {
        may_force(cx)?;
    }
    let held = taken_over(
        &cx.sql(),
        cx.namespace(),
        cx.schema(),
        &[cx.id()],
        interval.start_ms,
        interval.end_ms,
    )?;
    if !held.is_empty() && !force {
        return Err(veto(
            cx.schema(),
            cx.id(),
            OPERATION,
            problem(
                HAS_BOOKINGS,
                format!(
                    "{} {} holds time for {} over the closure; force keeps them and needs the permission's override",
                    cx.schema(),
                    cx.id(),
                    guidance::plural(held.len() as i64, "claim item")
                ),
                json!({ "holds": held.iter().map(|(_, taken)| taken.to_json()).collect::<Vec<_>>() }),
            ),
        ));
    }
    let made = cx.columns().get()?.opt_i64("closures_made")?.unwrap_or(0) + 1;
    cx.columns()
        .set(&[("closures_made", SqlValue::from(made))])?;
    let closure = Closure {
        closure: format!("closure-{made}"),
        interval,
        reason,
    };
    let table = cx.sql().table(CLOSURES_TABLE)?;
    cx.sql().run(
        format!("INSERT INTO {table} (namespace, schema, id, closure, start_ms, end_ms, reason, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"),
        &[
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::from(cx.id()),
            SqlValue::from(closure.closure.as_str()),
            SqlValue::from(interval.start_ms),
            SqlValue::from(interval.end_ms),
            SqlValue::from(closure.reason.as_str()),
            SqlValue::from(cx.now()),
        ],
    )?;
    Ok(closure.to_json())
}

fn remove_closure(
    _: &Calendar,
    cx: &OperationCx<'_, CalendarConfig>,
    params: &Value,
) -> Result<Value> {
    const OPERATION: &str = "removeClosure";
    permitted(cx, OPERATION)?;
    let name = params
        .get("closure")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let table = cx.sql().table(CLOSURES_TABLE)?;
    let key = [
        SqlValue::from(cx.namespace()),
        SqlValue::from(cx.schema()),
        SqlValue::from(cx.id()),
        SqlValue::from(name),
    ];
    let row = cx.sql().get(
        format!("SELECT closure, start_ms, end_ms, reason FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND closure = ?"),
        &key,
    )?;
    let Some(row) = row else {
        return Err(veto(
            cx.schema(),
            cx.id(),
            OPERATION,
            problem(
                NO_CLOSURE,
                format!("{} {} has no closure {name}", cx.schema(), cx.id()),
                Value::Null,
            ),
        ));
    };
    let closure = Closure {
        closure: row.text("closure")?.to_owned(),
        interval: Interval {
            start_ms: row.i64("start_ms")?,
            end_ms: row.i64("end_ms")?,
        },
        reason: row.text("reason")?.to_owned(),
    };
    cx.sql().run(
        format!(
            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND closure = ?"
        ),
        &key,
    )?;
    Ok(closure.to_json())
}

// window reads a listBookings or findAvailability window, at most 31 days.
fn window(operation: &str, params: &Value, refuse: impl Fn(Problem) -> Error) -> Result<Interval> {
    let window = interval_params(operation, params)?;
    let length = window.end_ms - window.start_ms;
    if length > MAX_WINDOW_MS {
        return Err(refuse(problem(
            WINDOW_TOO_LONG,
            format!(
                "{operation} reads at most {} at once, not {}",
                guidance::duration(MAX_WINDOW_MS),
                guidance::duration(length)
            ),
            json!({ "maxMs": MAX_WINDOW_MS }),
        )));
    }
    Ok(window)
}

// list_bookings pages the instance's records that take time over a window,
// by start.
fn list_bookings(
    _: &Calendar,
    cx: &OperationCx<'_, CalendarConfig>,
    params: &Value,
) -> Result<Value> {
    const OPERATION: &str = "listBookings";
    let window = window(OPERATION, params, |problem| {
        veto(cx.schema(), cx.id(), OPERATION, problem)
    })?;
    let request = page_request(NAME, OPERATION, params)?;
    let table = cx.sql().table(HOLDS_TABLE)?;
    let select = format!(
        "SELECT {TAKEN_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND state IN ('held', 'committed') AND start_ms < ? AND end_ms > ?"
    );
    let rows = cx.sql().all(
        Sql::per(
            format!("{select} ORDER BY start_ms, end_ms, holder_schema, holder_id, hold, item LIMIT ? OFFSET ?"),
            format!("{select} ORDER BY start_ms, end_ms, holder_schema COLLATE \"C\", holder_id COLLATE \"C\", hold COLLATE \"C\", item LIMIT ? OFFSET ?"),
        ),
        &[
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::from(cx.id()),
            SqlValue::from(window.end_ms),
            SqlValue::from(window.start_ms),
            SqlValue::from(request.limit + 1),
            SqlValue::from(request.after),
        ],
    )?;
    let numbered = rows
        .iter()
        .enumerate()
        .map(|(at, row)| Ok((request.after + at as i64 + 1, taken_of(row)?.to_json())))
        .collect::<Result<Vec<_>>>()?;
    let page = page(numbered, request.limit, |(number, _)| *number);
    Ok(json!({
        "items": page.items.into_iter().map(|(_, item)| item).collect::<Vec<_>>(),
        "next": page.next,
    }))
}

// json_values reads a where member's values: a value, or a list of 1 to 100.
fn where_values(value: &Value) -> Option<Vec<&Value>> {
    let listed: Vec<&Value> = match value {
        Value::Array(items) if (1..=100).contains(&items.len()) => items.iter().collect(),
        Value::Array(_) | Value::Object(_) => return None,
        one => vec![one],
    };
    listed
        .iter()
        .all(|one| !matches!(one, Value::Array(_) | Value::Object(_)))
        .then_some(listed)
}

fn sql_value(value: &Value) -> SqlValue {
    match value {
        Value::Bool(flag) => SqlValue::Integer(i64::from(*flag)),
        Value::String(text) => SqlValue::from(text.as_str()),
        Value::Number(number) => match number.as_i64() {
            Some(exact) => SqlValue::Integer(exact),
            None => SqlValue::Real(number_value(number)),
        },
        _ => SqlValue::Null,
    }
}

// where_condition is the SQL that keeps an instance whose own field holds
// one of the values, null for none, on the relation's JSON `data`.
fn where_condition(
    dialect: Dialect,
    field: &str,
    values: &[&Value],
    params: &mut Vec<SqlValue>,
) -> String {
    let null = values.iter().any(|value| value.is_null());
    let given: Vec<SqlValue> = values
        .iter()
        .filter(|value| !value.is_null())
        .map(|value| sql_value(value))
        .collect();
    let mut alternatives = Vec::new();
    match dialect {
        Dialect::Sqlite => {
            let path = format!("$.\"{field}\"");
            if !given.is_empty() {
                params.push(SqlValue::from(path.as_str()));
                alternatives.push(format!("json_extract(data, ?) IN ({})", marks(given.len())));
                params.extend(given);
            }
            if null {
                params.push(SqlValue::from(path.as_str()));
                alternatives.push("json_extract(data, ?) IS NULL".to_owned());
            }
        }
        Dialect::Postgres => {
            let key = sql_string(field);
            let value = format!("((data::jsonb)->{key})");
            let (texts, numbers): (Vec<SqlValue>, Vec<SqlValue>) = given
                .into_iter()
                .partition(|value| matches!(value, SqlValue::Text(_)));
            if !texts.is_empty() {
                alternatives.push(format!(
                    "(jsonb_typeof({value}) = 'string' AND ((data::jsonb)->>{key}) IN ({}))",
                    marks(texts.len())
                ));
                params.extend(texts);
            }
            if !numbers.is_empty() {
                alternatives.push(format!(
                    "(CASE jsonb_typeof({value}) WHEN 'number' THEN ((data::jsonb)->>{key})::numeric WHEN 'boolean' THEN (CASE WHEN {value} = 'true'::jsonb THEN 1 ELSE 0 END) END) IN ({})",
                    numbers.iter().map(|_| "?::numeric").collect::<Vec<_>>().join(", ")
                ));
                params.extend(numbers);
            }
            if null {
                alternatives.push(format!(
                    "({value} IS NULL OR jsonb_typeof({value}) = 'null')"
                ));
            }
        }
    }
    format!("({})", alternatives.join(" OR "))
}

// find_availability lists the slots of a duration that holdFor would take
// now across the schema's instances, soonest first.
fn find_availability(
    _: &Calendar,
    cx: &SchemaCx<'_, CalendarConfig>,
    params: &Value,
) -> Result<Value> {
    const OPERATION: &str = "findAvailability";
    let config = cx.config();
    let schema_veto = |problem: Problem| veto(cx.schema(), "", OPERATION, problem);
    let window = window(OPERATION, params, schema_veto)?;
    let duration = int_param(params, "durationMs")
        .ok_or_else(|| params_error(OPERATION, "/durationMs", "must be an integer"))?;
    let limit = int_param(params, "limit").unwrap_or(DEFAULT_SLOTS);
    let label = format!("{}'s Calendar", cx.schema());
    if duration < config.min_ms {
        return Err(schema_veto(problem(
            TOO_SHORT,
            format!(
                "{} is shorter than {label}'s shortest hold, {}",
                guidance::duration(duration),
                guidance::duration(config.min_ms)
            ),
            json!({ "minMs": config.min_ms }),
        )));
    }
    if duration > config.max_ms {
        return Err(schema_veto(problem(
            TOO_LONG,
            format!(
                "{} is longer than {label}'s longest hold, {}",
                guidance::duration(duration),
                guidance::duration(config.max_ms)
            ),
            json!({ "maxMs": config.max_ms }),
        )));
    }

    // The instances: those of ids, then those of where, by id.
    let relation = cx.sql().instances()?;
    let mut conditions: Vec<String> = Vec::new();
    let mut values: Vec<SqlValue> = Vec::new();
    if let Some(ids) = params.get("ids").and_then(Value::as_array) {
        conditions.push(format!("id IN ({})", marks(ids.len())));
        values.extend(ids.iter().map(sql_value));
    }
    if let Some(filter) = params.get("where") {
        let Some(members) = filter.as_object() else {
            return Err(params_error(
                OPERATION,
                "/where",
                "must be an object of field values",
            ));
        };
        for field in js_keys(members) {
            if !config.filterable.iter().any(|known| known == field) {
                return Err(params_error(
                    OPERATION,
                    &format!("/where/{field}"),
                    format!(
                        "{field} is not an own field of {} that holds a string, a number or a boolean",
                        cx.schema()
                    ),
                ));
            }
            let Some(listed) = where_values(&members[field]) else {
                return Err(params_error(
                    OPERATION,
                    &format!("/where/{field}"),
                    "must be a value, or a list of 1 to 100 values, each a string, a number, a boolean or null",
                ));
            };
            conditions.push(where_condition(cx.dialect(), field, &listed, &mut values));
        }
    }
    let condition = if conditions.is_empty() {
        String::new()
    } else {
        format!(" WHERE {}", conditions.join(" AND "))
    };
    values.push(SqlValue::from(MAX_INSTANCES as i64 + 1));
    let select = format!("SELECT id, data, hours, time_zone FROM {relation}{condition}");
    let rows = cx.sql().all(
        Sql::per(
            format!("{select} ORDER BY id LIMIT ?"),
            format!("{select} ORDER BY id COLLATE \"C\" LIMIT ?"),
        ),
        &values,
    )?;
    let mut truncated = rows.len() > MAX_INSTANCES;
    let rows = &rows[..rows.len().min(MAX_INSTANCES)];
    let ids: Vec<&str> = rows
        .iter()
        .map(|row| row.text("id"))
        .collect::<std::result::Result<_, _>>()?;
    let (from, to) = config.reach(&window);
    let sql: &SqlReader<'_> = &cx.sql();
    let mut taken = taken_over(sql, cx.namespace(), cx.schema(), &ids, from, to)?;
    let mut closures = closures_over(
        sql,
        cx.namespace(),
        cx.schema(),
        &ids,
        window.start_ms,
        window.end_ms,
        None,
    )?;

    let now = cx.now();
    let earliest = window.start_ms.max(now + config.lead_ms);
    let latest = match config.horizon_ms {
        Some(horizon) => now + horizon,
        None => i64::MAX,
    };
    let wanted = usize::try_from(limit).unwrap_or(0) + 1;
    let mut slots: Vec<(i64, String, i64)> = Vec::new();
    let mut work = 0usize;
    'instances: for row in rows {
        let id = row.text("id")?;
        let data: Map<String, Value> = row
            .opt_text("data")?
            .and_then(|text| serde_json::from_str(text).ok())
            .unwrap_or_default();
        let capacity = config.capacity_of(&data);
        let closures = drain(&mut closures, id);
        let taken = drain(&mut taken, id);
        if capacity <= 0 {
            continue;
        }
        let settings = settings_of(config, row.opt_text("hours")?, row.opt_text("time_zone")?)?;
        let offsets = Offsets::over(&settings.tz, window.start_ms, window.end_ms);
        let open = opening(&settings.hours, &offsets, window.start_ms, window.end_ms);
        let closed = time::merge(closures.iter().map(|closure| closure.interval).collect());
        // Every hold's time with its buffers, by start: a sweep of the
        // starts takes each in as the window reaches it and lets it go as
        // the window passes it.
        let mut occupied: Vec<Interval> = taken
            .iter()
            .map(|taken| config.occupied(&taken.interval))
            .collect();
        occupied.sort_by_key(|interval| (interval.start_ms, interval.end_ms));
        let starts = grid(
            &settings.hours,
            &offsets,
            earliest,
            window.end_ms - duration + 1,
            config.grid_ms(),
        );
        let (mut next, mut active, mut skip_to, mut found) = (0, Vec::new(), i64::MIN, 0);
        for start in starts {
            if start > latest || found == wanted {
                break;
            }
            if start < skip_to {
                continue;
            }
            if work >= MAX_FIND_WORK {
                truncated = true;
                break 'instances;
            }
            work += 1;
            let slot = Interval {
                start_ms: start,
                end_ms: start + duration,
            };
            if !inside(&open, &slot) {
                // No start before the next open stretch fits.
                skip_to = open
                    .iter()
                    .map(|stretch| stretch.start_ms)
                    .find(|from| *from > start)
                    .unwrap_or(i64::MAX);
                continue;
            }
            if let Some(closure) = closed.iter().find(|closure| closure.overlaps(&slot)) {
                skip_to = closure.end_ms;
                continue;
            }
            let reach = config.occupied(&slot);
            while next < occupied.len() && occupied[next].start_ms < reach.end_ms {
                active.push(occupied[next]);
                next += 1;
            }
            active.retain(|interval: &Interval| interval.end_ms > reach.start_ms);
            work += active.len();
            let (most, at) = peak_at(&active, &reach);
            let left = capacity - most;
            if left > 0 {
                slots.push((start, id.to_owned(), left));
                found += 1;
            } else if let Some(at) = at {
                // Every start whose time with buffers takes in the full
                // instant is full too.
                skip_to = at.saturating_add(config.buffer_before).saturating_add(1);
            }
        }
    }
    slots.sort();
    if slots.len() >= wanted {
        truncated = true;
        slots.truncate(wanted - 1);
    }
    Ok(json!({
        "slots": slots
            .into_iter()
            .map(|(start, id, remaining)| json!({
                "id": id,
                "startMs": start,
                "endMs": start + duration,
                "remaining": remaining,
            }))
            .collect::<Vec<_>>(),
        "truncated": truncated,
    }))
}

// drain takes one instance's entries out of a list read by instance.
fn drain<T>(entries: &mut Vec<(String, T)>, id: &str) -> Vec<T> {
    let (mine, rest): (Vec<_>, Vec<_>) = std::mem::take(entries)
        .into_iter()
        .partition(|(owner, _)| owner == id);
    *entries = rest;
    mine.into_iter().map(|(_, entry)| entry).collect()
}

// peak_from is the most records that have not ended by `now` overlap at
// any instant, without buffers: what capacity may not fall below.
fn peak_from(taken: &[Taken], now: i64) -> i64 {
    let future: Vec<Interval> = taken
        .iter()
        .filter(|taken| taken.interval.end_ms > now)
        .map(|taken| taken.interval)
        .collect();
    let Some(last) = future.iter().map(|interval| interval.end_ms).max() else {
        return 0;
    };
    peak(
        &future,
        &Interval {
            start_ms: now.min(last - 1),
            end_ms: last,
        },
    )
    .0
}

fn all_taken_from(
    sql: &SqlReader<'_>,
    namespace: &str,
    schema: &str,
    id: Option<&str>,
    now: i64,
) -> Result<Vec<(String, Taken)>> {
    let table = sql.table(HOLDS_TABLE)?;
    let mut values = vec![SqlValue::from(namespace), SqlValue::from(schema)];
    let one = match id {
        Some(id) => {
            values.push(SqlValue::from(id));
            " AND id = ?"
        }
        None => "",
    };
    values.push(SqlValue::from(now));
    sql.all(
        format!("SELECT {TAKEN_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ?{one} AND state IN ('held', 'committed') AND end_ms > ?"),
        &values,
    )?
    .iter()
    .map(|row| Ok((row.text("id")?.to_owned(), taken_of(row)?)))
    .collect()
}

fn guard(
    _: &Calendar,
    view: &InstanceView<'_, CalendarConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    match request {
        GuardRequest::Delete { .. } => {
            let holding = supply::holding(view, HOLDS_TABLE)?;
            Ok((holding > 0).then(|| Veto {
                reason: format!(
                    "it holds time for {}",
                    guidance::plural(holding, "claim item")
                ),
                code: Some(supply::HELD.to_owned()),
                details: None,
            }))
        }
        GuardRequest::Update { patch, after, .. } => {
            let Capacity::Field(field) = &view.config().capacity else {
                return Ok(None);
            };
            if !patch.contains_key(field) {
                return Ok(None);
            }
            let capacity = view.config().capacity_of(after);
            let taken: Vec<Taken> = all_taken_from(
                &view.sql(),
                view.namespace(),
                view.schema(),
                Some(view.id()),
                view.now(),
            )?
            .into_iter()
            .map(|(_, taken)| taken)
            .collect();
            let most = peak_from(&taken, view.now());
            Ok((most > capacity).then(|| Veto {
                reason: format!(
                    "{field} would hold {capacity}, and {} overlap at once",
                    guidance::plural(most, "hold")
                ),
                code: Some(HAS_BOOKINGS.to_owned()),
                details: Some(
                    json!({ "field": field, "capacity": capacity, "peak": most })
                        .as_object()
                        .cloned()
                        .unwrap_or_default(),
                ),
            }))
        }
        _ => Ok(None),
    }
}

// after_config_change holds a new capacity to the peak of every instance,
// and a new time zone to a schema whose holds have all ended.
fn after_config_change(_: &Calendar, cx: &PublishCx<'_, CalendarConfig>) -> Result<()> {
    let (Some(config), Some(before)) = (cx.config(), cx.before()) else {
        if cx.config().is_none() {
            for table in [HOLDS_TABLE, CLOSURES_TABLE] {
                let name = cx.sql().table(table)?;
                cx.sql().run(
                    format!("DELETE FROM {name} WHERE namespace = ? AND schema = ?"),
                    &[SqlValue::from(cx.namespace()), SqlValue::from(cx.schema())],
                )?;
            }
        }
        return Ok(());
    };
    let zone_moved = config.time_zone != before.time_zone;
    let capacity_moved = config.capacity != before.capacity;
    if !zone_moved && !capacity_moved {
        return Ok(());
    }
    let now = cx.now();
    let taken = all_taken_from(&cx.sql(), cx.namespace(), cx.schema(), None, now)?;
    if zone_moved && !taken.is_empty() {
        return Err(Error::config(format!(
            "Calendar's timeZone changes from {} to {} only while no instance of {} holds time that has not ended, and {} do",
            before.time_zone,
            config.time_zone,
            cx.schema(),
            guidance::plural(taken.len() as i64, "hold")
        )));
    }
    if capacity_moved && !taken.is_empty() {
        let mut problem = None;
        cx.each_instance(|instance| {
            if problem.is_some() {
                return Ok(());
            }
            let mine: Vec<Taken> = taken
                .iter()
                .filter(|(id, _)| *id == instance.id)
                .map(|(_, taken)| taken.clone())
                .collect();
            let most = peak_from(&mine, now);
            let capacity = config.capacity_of(&instance.data);
            if most > capacity {
                problem = Some(format!(
                    "Calendar's capacity falls to {capacity} on {} {}, where {} overlap at once",
                    cx.schema(),
                    instance.id,
                    guidance::plural(most, "hold that has not ended")
                ));
            }
            Ok(())
        })?;
        if let Some(problem) = problem {
            return Err(Error::config(problem));
        }
    }
    Ok(())
}

// check_integer_field holds capacity.field to an integer field of the
// type.
fn check_integer_field(target: &ConfigTarget<'_>, field: &str) -> Result<()> {
    if !target.fields().iter().any(|known| known == field) {
        return Err(Error::config(format!(
            "capacity.field \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            target.fields().join(", ")
        )));
    }
    if field_types(target, field) != ["integer"] {
        return Err(Error::config(format!(
            "capacity.field \"{field}\" is not an integer field of {}",
            target.type_name()
        )));
    }
    Ok(())
}

// field_types is a field's JSON types, without null.
fn field_types<'t>(target: &'t ConfigTarget<'_>, field: &str) -> Vec<&'t str> {
    let declared = target
        .field_schemas()
        .get(field)
        .and_then(|schema| schema.get("type"));
    let types: Vec<&str> = match declared {
        Some(Value::Array(types)) => types.iter().filter_map(Value::as_str).collect(),
        Some(Value::String(one)) => vec![one.as_str()],
        _ => Vec::new(),
    };
    types.into_iter().filter(|one| *one != "null").collect()
}

fn config_int(raw: &Value, name: &str) -> Option<i64> {
    raw.get(name).and_then(integer)
}

impl Behavior for Calendar {
    type Config = CalendarConfig;

    fn declaration(&self) -> &str {
        include_str!("../../declarations/Calendar.behavior.json")
    }

    // The configSchema holds the shape; this holds the zone to the
    // database, the hours to ranges that close after they open and do not
    // overlap, a capacity field to an integer field, and the bounds to
    // each other, and fills in the defaults.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<CalendarConfig> {
        let time_zone = raw
            .get("timeZone")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let tz = zone(&time_zone).ok_or_else(|| {
            Error::config(format!(
                "timeZone \"{time_zone}\" is not a time zone of the engine's database: an IANA name, such as Europe/London"
            ))
        })?;
        let hours_json = raw.get("hours").cloned().unwrap_or_else(|| json!({}));
        let hours = WeekHours::parse(&hours_json).map_err(|problem| {
            Error::config(format!("hours{}: {}", problem.path, problem.message))
        })?;
        let capacity = match raw.get("capacity") {
            None => Capacity::Fixed(1),
            Some(Value::Object(members)) => {
                let field = members
                    .get("field")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                check_integer_field(target, field)?;
                Capacity::Field(field.to_owned())
            }
            Some(count) => Capacity::Fixed(integer(count).unwrap_or(1)),
        };
        let slot_ms = config_int(raw, "slotMs");
        let min_ms = config_int(raw, "minMs").or(slot_ms).unwrap_or(MINUTE_MS);
        let max_ms = config_int(raw, "maxMs").unwrap_or(DEFAULT_MAX_MS);
        if min_ms > max_ms {
            return Err(Error::config(format!(
                "minMs ({min_ms}) is longer than maxMs ({max_ms}): no hold could be made"
            )));
        }
        let buffers = raw.get("bufferMs");
        let buffer = |name: &str| {
            buffers
                .and_then(|buffers| buffers.get(name))
                .and_then(integer)
                .unwrap_or(0)
        };
        let filterable = target
            .fields()
            .iter()
            .filter(|field| {
                matches!(
                    field_types(target, field).as_slice(),
                    ["string"] | ["number"] | ["integer"] | ["boolean"]
                )
            })
            .cloned()
            .collect();
        Ok(CalendarConfig {
            time_zone,
            tz,
            hours,
            hours_json,
            capacity,
            slot_ms,
            min_ms,
            max_ms,
            buffer_before: buffer("before"),
            buffer_after: buffer("after"),
            lead_ms: config_int(raw, "leadMs").unwrap_or(0),
            horizon_ms: config_int(raw, "horizonMs"),
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
            filterable,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            // Any change but a capacity below a peak, or a zone under held
            // time, which afterConfigChange reads; Calendar leaves only a
            // schema with no instances.
            config_change: Some(|_, before, after, change| {
                Ok(match (before, after) {
                    (Some(_), None) if change.instances()? => Some(
                        "Calendar is not removed from a schema that has instances, whose claims may hold its time".to_owned(),
                    ),
                    _ => None,
                })
            }),
            after_config_change: Some(after_config_change),
            guidance: Some(|_, config, target| guidance::guidance(config, target)),
            migrations: vec![BehaviorMigration::new(1, "calendar")
                .column("hours", ColumnSpec::text())
                .column("time_zone", ColumnSpec::text())
                .column("closures_made", ColumnSpec::integer().not_null().default(0))
                .table(supply::rows_table(HOLDS_TABLE))
                .table(
                    TableSpec::new(CLOSURES_TABLE)
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("closure", ColumnSpec::text().not_null())
                        .column("start_ms", ColumnSpec::integer().not_null())
                        .column("end_ms", ColumnSpec::integer().not_null())
                        .column("reason", ColumnSpec::text().not_null())
                        .column("at", ColumnSpec::integer().not_null())
                        .primary_key(&["namespace", "schema", "id", "closure"])
                        .index(IndexSpec::new(
                            "closures_start",
                            &["namespace", "schema", "id", "start_ms"],
                        )),
                )],
            guard: Some(guard),
            operations: vec![
                ("setOpeningHours", set_opening_hours),
                ("addClosure", add_closure),
                ("removeClosure", remove_closure),
                ("holdFor", hold_for),
                ("commitFor", commit_for),
                ("releaseFor", release_for),
                ("checkHold", check_hold),
                ("listBookings", list_bookings),
            ],
            schema_operations: vec![("findAvailability", find_availability)],
            fields: vec![
                ("timeZone", |_, view| {
                    Ok(Some(Value::from(settings(view, view.config())?.time_zone)))
                }),
                ("hours", |_, view| {
                    Ok(Some(settings(view, view.config())?.hours_json))
                }),
                // The closures that have not ended, soonest first.
                ("closures", |_, view| {
                    let closures = closures_over(
                        &view.sql(),
                        view.namespace(),
                        view.schema(),
                        &[view.id()],
                        view.now(),
                        i64::MAX,
                        Some(MAX_CLOSURES_SHOWN),
                    )?;
                    Ok(Some(Value::Array(
                        closures
                            .iter()
                            .map(|(_, closure)| closure.to_json())
                            .collect(),
                    )))
                }),
            ],
            after_change: Some(|_, cx, change| {
                if !matches!(change, InstanceChange::Delete) {
                    return Ok(());
                }
                supply::forget(cx, HOLDS_TABLE)?;
                let table = cx.sql().table(CLOSURES_TABLE)?;
                cx.sql().run(
                    format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
                    &[
                        SqlValue::from(cx.namespace()),
                        SqlValue::from(cx.schema()),
                        SqlValue::from(cx.id()),
                    ],
                )?;
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_window_holds_its_wall_times() {
        assert_eq!(wall_label(0), "1970-01-01 00:00");
        assert_eq!(wall_label(DAY_MS + 90 * MINUTE_MS), "1970-01-02 01:30");
        let merged = time::merge(vec![
            Interval {
                start_ms: 5,
                end_ms: 9,
            },
            Interval {
                start_ms: 0,
                end_ms: 5,
            },
        ]);
        assert_eq!(
            merged,
            [Interval {
                start_ms: 0,
                end_ms: 9
            }]
        );
    }

    #[test]
    fn where_takes_scalars_and_lists_of_them() {
        assert_eq!(where_values(&json!("a")).map(|v| v.len()), Some(1));
        assert_eq!(
            where_values(&json!([1, null, true])).map(|v| v.len()),
            Some(3)
        );
        assert!(where_values(&json!([])).is_none());
        assert!(where_values(&json!({"a": 1})).is_none());
        assert!(where_values(&json!([[1]])).is_none());
    }

    #[test]
    fn the_peak_from_now_leaves_out_what_has_ended() {
        let taken = |start_ms, end_ms| Taken {
            holder: InstanceRef::new("Booking", "b"),
            hold: "slot".into(),
            item: 0,
            interval: Interval { start_ms, end_ms },
            state: ItemState::Held,
            retired: false,
        };
        let all = [taken(0, 10), taken(5, 15), taken(20, 30), taken(25, 35)];
        assert_eq!(peak_from(&all, 0), 2);
        assert_eq!(peak_from(&all, 16), 2);
        assert_eq!(peak_from(&all, 31), 1);
        assert_eq!(peak_from(&all, 40), 0);
    }
}
