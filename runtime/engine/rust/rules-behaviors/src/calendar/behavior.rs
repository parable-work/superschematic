//! Calendar, time a supply opens (D70), on a table (D103's step 4): a
//! stylist, a chair, a room, a collection counter, a pool of bikes. It is a
//! timed supply of the contract Supply ([`crate::supply`]): a claim's Holds
//! holds, commits and releases intervals of it through `holdFor`,
//! `commitFor` and `releaseFor`, which it calls on the row, and asks
//! `checkHold` of the row before a take, which Calendar answers through its
//! answer hook over the row and its sub-tables.
//!
//! Calendar passes D103's test. State of its own: the record of each claim
//! item that holds time of the row (`bookings`), the row's closures
//! (`closures`) and its own hours, time zone and count of closures
//! (`settings`), each a sub-table of the row. Rules over how it changes:
//! hours in a time zone, a grid, length bounds, buffers, a lead time and a
//! horizon a hold is held to, a capacity no instant may pass, closures
//! refused over held time, a zone that does not move under time to come,
//! and records that move only forward. Recurrence: every salon, clinic,
//! rental desk and meeting room books time.
//!
//! The config gives weekly opening hours as wall times in an IANA
//! `timeZone` ([`super::time`]), in the database jiff bundles alone; a
//! `capacity`, an integer or an own integer field, of holds that may
//! overlap; a start grid (`slotMs`, any whole minute when absent); length
//! bounds (`minMs`, `slotMs` or a minute when absent; `maxMs`, a day);
//! buffers kept free before and after each hold; a lead time and a
//! horizon, against the command's time; and the `permission` that sets a
//! row's own hours and closures.
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
//!   conflicts at the peak).
//!
//! Two bookings of one room: each writing call takes the supply's lock
//! ([`crate::supply::lock_key`]) through [`crate::supply::begin`] before it
//! reads the row's bookings, and writes the row held to the version it
//! read, so the second decides over the first's hold and finds it full.
//! Holds' guard takes the same lock before it asks `checkHold`.
//!
//! `checkHold` answers the same without the holder check or a write, as the
//! supply's record of the item allows (an item held already fits, one
//! committed or released is `not_requested`), counting the intervals its
//! `also` lists as held. `findAvailability`, an operation of the table as a
//! whole, lists the slots of a duration that would fit across the table's
//! rows over a window of at most 31 days, soonest first: at most 100 rows
//! by key, read across the table with their sub-tables, and one sweep of
//! each row's holds, which jumps past a full instant, a closure or a closed
//! stretch, and stops at [`MAX_FIND_WORK`] (`truncated`). `listBookings`
//! pages a row's holds over such a window, a deleted claim's committed
//! time among them, marked `retired`.
//!
//! Every field is computed at a read: `hours` and `timeZone` follow the
//! config while the row has none of its own, and `closures` reads the first
//! 100 that have not ended by the read's time.
//!
//! A row is not deleted while it holds time (`held`): an item held, or
//! committed time that has not ended. `setOpeningHours` changes its own
//! time zone only while it holds no time that has not ended
//! (`has_bookings`). An update that lowers a capacity field below the
//! row's peak is `has_bookings`.
//!
//! configChange: hours, the grid, buffers, lead time and horizon may change
//! and apply to holds made after. The engine held a new capacity to every
//! instance's peak and a new time zone to a schema whose holds had ended;
//! the core reads no row at a publish (D103), so on a type with rows a new
//! `timeZone` and a capacity that may fall are refused. Calendar joins a
//! type that has rows, and leaves only one that has none.

use std::collections::BTreeMap;
use std::sync::OnceLock;

use jiff::tz::TimeZone;
use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, ContractQuery, Cx, Declaration, GuardRequest,
    Hook, Refusal, Row, SchemaIssue, Stop, SubRow, TableRef, Veto, View,
};

use super::time::{
    full_until, grid, inside, merge, on_grid, opening, peak, peak_at, wall_text, zone, Offsets,
    WeekHours, DAY_MS, EARLIEST_MS, LATEST_MS, MINUTE_MS,
};
use crate::ajv;
use crate::declared;
use crate::paging;
use crate::supply::{
    self, Ask, Begun, CheckAnswer, Interval, ItemState, RowRef, SupplyCall, SupplyKind,
    SupplyOperation, SupplyRecord,
};

/// The behavior's name.
pub const NAME: &str = "Calendar";

/// Calendar's declaration, the commerce package's file, read in place.
pub const DECLARATION: &str =
    include_str!("../../../../../engine-commerce/rust/declarations/Calendar.behavior.json");

/// The sub-table of the contract's records: one per holder's item.
pub const BOOKINGS: &str = "bookings";
/// The sub-table of closures.
pub const CLOSURES: &str = "closures";
/// The sub-table of a row's own hours, zone and count of closures.
pub const SETTINGS: &str = "settings";

/// The operations, by their declared names.
pub const SET_OPENING_HOURS: &str = "setOpeningHours";
pub const ADD_CLOSURE: &str = "addClosure";
pub const REMOVE_CLOSURE: &str = "removeClosure";
pub const LIST_BOOKINGS: &str = "listBookings";
pub const FIND_AVAILABILITY: &str = "findAvailability";

/// The settings a row keeps, by name.
const HOURS: &str = "hours";
const TIME_ZONE: &str = "timeZone";
const CLOSURES_MADE: &str = "closuresMade";

/// The longest window listBookings and findAvailability read.
pub const MAX_WINDOW_MS: i64 = 31 * DAY_MS;
/// The most closures the `closures` field shows.
pub const MAX_CLOSURES_SHOWN: usize = 100;
/// The most rows one findAvailability reads.
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

/// The IR types of an own field findAvailability's `where` may name: one
/// that holds a string, a number or a boolean.
pub const FILTER_TYPES: [&str; 7] = [
    "string",
    "number",
    "integer",
    "boolean",
    "Generic.Int64",
    "Generic.Int32",
    "Generic.Float64",
];

fn file() -> &'static Value {
    static FILE: OnceLock<Value> = OnceLock::new();
    FILE.get_or_init(|| ajv::file(DECLARATION))
}

/// How many holds may overlap.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Capacity {
    Fixed(i64),
    /// An own integer field of the type, whose value of at least 1 is the
    /// row's; anything else is 0.
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
    /// The type's own fields that hold a string, a number or a boolean,
    /// which findAvailability's `where` may name.
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

    /// The row's capacity, from its own fields.
    fn capacity_of(&self, own: &Map<String, Value>) -> i64 {
        match &self.capacity {
            Capacity::Fixed(count) => *count,
            Capacity::Field(field) => own
                .get(field)
                .and_then(supply::integer)
                .filter(|count| *count >= 1)
                .unwrap_or(0),
        }
    }

    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the zone against the bundled database, the hours to
    /// ranges that close after they open and do not overlap, a capacity
    /// field to an integer field, and the bounds to each other, each a
    /// rule's reason the core prefixes with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<CalendarConfig, ConfigError> {
        let issues = ajv::config_issues(file(), raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues));
        }
        let time_zone = raw
            .get("timeZone")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let tz = zone(&time_zone).ok_or_else(|| {
            format!(
                "timeZone \"{time_zone}\" is not a time zone of the IANA database the core bundles: an IANA name, such as Europe/London"
            )
        })?;
        let hours_json = raw.get("hours").cloned().unwrap_or_else(|| json!({}));
        let hours = WeekHours::parse(&hours_json)
            .map_err(|problem| format!("hours{}: {}", problem.path, problem.message))?;
        let capacity = match raw.get("capacity") {
            None => Capacity::Fixed(1),
            Some(Value::Object(members)) => {
                let field = members
                    .get("field")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                crate::stock::own_field(
                    target,
                    "capacity.field",
                    field,
                    &crate::stock::INTEGER_TYPES,
                    "an integer",
                )?;
                Capacity::Field(field.to_owned())
            }
            Some(count) => Capacity::Fixed(supply::integer(count).unwrap_or(1)),
        };
        let number = |name: &str| raw.get(name).and_then(supply::integer);
        let slot_ms = number("slotMs");
        let min_ms = number("minMs").or(slot_ms).unwrap_or(MINUTE_MS);
        let max_ms = number("maxMs").unwrap_or(DEFAULT_MAX_MS);
        if min_ms > max_ms {
            return Err(format!(
                "minMs ({min_ms}) is longer than maxMs ({max_ms}): no hold could be made"
            )
            .into());
        }
        let buffers = raw.get("bufferMs");
        let buffer = |name: &str| {
            buffers
                .and_then(|buffers| buffers.get(name))
                .and_then(supply::integer)
                .unwrap_or(0)
        };
        let filterable = target
            .fields()
            .iter()
            .filter(|field| {
                !field.date_time
                    && field.relation.is_none()
                    && FILTER_TYPES.contains(&field.type_name.as_str())
            })
            .map(|field| field.name.clone())
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
            lead_ms: number("leadMs").unwrap_or(0),
            horizon_ms: number("horizonMs"),
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
            filterable,
        })
    }
}

/// What a row's hours are in: its own, or the config's.
#[derive(Clone, Debug)]
struct Settings {
    time_zone: String,
    tz: TimeZone,
    hours: WeekHours,
    hours_json: Value,
    closures_made: i64,
}

/// A closure of a row.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Closure {
    closure: String,
    interval: Interval,
    reason: String,
}

impl Closure {
    fn of(row: &SubRow) -> Option<Closure> {
        let number = |name: &str| row.value(name).and_then(supply::integer);
        Some(Closure {
            closure: row.key.clone(),
            interval: Interval {
                start_ms: number("startMs")?,
                end_ms: number("endMs")?,
            },
            reason: row
                .value("reason")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
        })
    }

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
    holder: RowRef,
    hold: String,
    item: i64,
    interval: Interval,
    state: ItemState,
    /// A deleted holder's committed time, which stays booked.
    retired: bool,
}

impl Taken {
    fn of(record: &SupplyRecord) -> Option<Taken> {
        if !record.state.takes() {
            return None;
        }
        Some(Taken {
            holder: record.holder.clone(),
            hold: record.hold.clone(),
            item: record.item,
            interval: record.ask.interval()?,
            state: record.state,
            retired: record.retired,
        })
    }

    /// `{ holder, hold, item, startMs, endMs, state, retired? }`,
    /// `retired` only for a deleted claim's committed time.
    fn to_json(&self) -> Value {
        let mut out = json!({
            "holder": self.holder.to_json(),
            "hold": self.hold,
            "item": self.item,
            "startMs": self.interval.start_ms,
            "endMs": self.interval.end_ms,
            "state": self.state.as_str(),
        });
        if self.retired {
            out["retired"] = Value::Bool(true);
        }
        out
    }

    /// Whether the record is the live one of the item a call names.
    fn is(&self, call: &SupplyCall) -> bool {
        !self.retired
            && self.holder == call.holder
            && self.hold == call.hold
            && self.item == call.item
    }
}

/// One row as the rules read it.
struct Resource {
    settings: Settings,
    capacity: i64,
    closures: Vec<Closure>,
    taken: Vec<Taken>,
    records: Vec<SupplyRecord>,
}

/// A row's sub-tables of the use, by name.
type Tables = BTreeMap<String, Vec<SubRow>>;

// settings_of is a row's hours and zone: its own settings, else the
// config's; why its own do not read, which is the row's defect, otherwise.
fn settings_of(config: &CalendarConfig, rows: &[SubRow]) -> Result<Settings, String> {
    let value = |name: &str| {
        rows.iter()
            .find(|row| row.key == name)
            .and_then(|row| row.value("value"))
            .filter(|value| !value.is_null())
    };
    let (time_zone, tz) = match value(TIME_ZONE).and_then(Value::as_str) {
        Some(name) => (
            name.to_owned(),
            zone(name).ok_or_else(|| format!("a row keeps the time zone {name}"))?,
        ),
        None => (config.time_zone.clone(), config.tz.clone()),
    };
    let (hours, hours_json) = match value(HOURS) {
        Some(json) => {
            let hours = WeekHours::parse(json)
                .map_err(|problem| format!("a row's hours{}: {}", problem.path, problem.message))?;
            (hours, json.clone())
        }
        None => (config.hours.clone(), config.hours_json.clone()),
    };
    Ok(Settings {
        time_zone,
        tz,
        hours,
        hours_json,
        closures_made: value(CLOSURES_MADE).and_then(supply::integer).unwrap_or(0),
    })
}

fn table<'t>(tables: &'t Tables, name: &str) -> &'t [SubRow] {
    supply::table_of(tables, name)
}

// resource_of reads a row as the rules read it: its settings, its
// capacity, its closures and its records, those that take time among them.
fn resource_of(
    config: &CalendarConfig,
    own: &Map<String, Value>,
    tables: &Tables,
) -> Result<Resource, String> {
    let mut closures: Vec<Closure> = table(tables, CLOSURES)
        .iter()
        .filter_map(Closure::of)
        .collect();
    closures.sort_by(|a, b| {
        (a.interval.start_ms, a.interval.end_ms)
            .cmp(&(b.interval.start_ms, b.interval.end_ms))
            .then_with(|| a.closure.as_bytes().cmp(b.closure.as_bytes()))
    });
    let records = supply::records_of(table(tables, BOOKINGS));
    let mut taken: Vec<Taken> = records.iter().filter_map(Taken::of).collect();
    taken.sort_by(|a, b| {
        (a.interval.start_ms, a.interval.end_ms)
            .cmp(&(b.interval.start_ms, b.interval.end_ms))
            .then_with(|| a.holder.schema.as_bytes().cmp(b.holder.schema.as_bytes()))
            .then_with(|| a.holder.id.as_bytes().cmp(b.holder.id.as_bytes()))
            .then_with(|| a.hold.as_bytes().cmp(b.hold.as_bytes()))
            .then(a.item.cmp(&b.item))
            .then(a.retired.cmp(&b.retired))
    });
    Ok(Resource {
        settings: settings_of(config, table(tables, SETTINGS))?,
        capacity: config.capacity_of(own),
        closures,
        taken,
        records,
    })
}

// resource reads the row a hook runs on, its sub-tables of the use as the
// run has them.
fn resource(view: &View<'_, CalendarConfig>) -> Hook<Resource> {
    let mut tables = Tables::new();
    for name in [BOOKINGS, CLOSURES, SETTINGS] {
        tables.insert(name.to_owned(), view.sub_rows(name)?);
    }
    resource_of(view.config(), &view.own(), &tables).map_err(|reason| view.defect(reason))
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

/// A count of a noun, `1 claim item` or `2 claim items`.
fn plural(count: i64, noun: &str) -> String {
    if count == 1 {
        format!("{count} {noun}")
    } else {
        format!("{count} {noun}s")
    }
}

/// A span of milliseconds as a reader says it: `90 seconds`, `15 minutes`,
/// `31 days`, `1500 ms`.
fn duration(ms: i64) -> String {
    for (unit, noun) in [
        (86_400_000, "day"),
        (3_600_000, "hour"),
        (60_000, "minute"),
        (1000, "second"),
    ] {
        if ms != 0 && ms % unit == 0 {
            return plural(ms / unit, noun);
        }
    }
    format!("{ms} ms")
}

/// A wall time as a reader says it, `2026-10-25 01:30`.
fn wall_label(wall: i64) -> String {
    let day = wall.div_euclid(DAY_MS);
    let date = jiff::Timestamp::from_millisecond(day * DAY_MS)
        .map(|at| at.strftime("%Y-%m-%d").to_string())
        .unwrap_or_else(|_| day.to_string());
    format!("{date} {}", wall_text(wall - day * DAY_MS))
}

fn vetoed<C>(view: &View<'_, C>, problem: Problem) -> Stop {
    let mut veto = Veto::new(problem.reason).with_code(problem.code);
    if !problem.extra.is_empty() {
        veto = veto.with_details(problem.extra);
    }
    view.veto(veto)
}

fn params_error<C>(
    view: &View<'_, C>,
    operation: &str,
    path: &str,
    message: impl Into<String>,
) -> Stop {
    Refusal::operation_params(
        NAME,
        view.use_id(),
        operation,
        vec![SchemaIssue::new(path, message)],
    )
    .into()
}

fn label<C>(view: &View<'_, C>) -> String {
    format!("{} {}", view.type_name(), view.key().unwrap_or_default())
}

// permitted holds an operation to the config's permission, which nobody
// holds when the config names none.
fn permitted(view: &View<'_, CalendarConfig>, operation: &str) -> Hook<()> {
    let who = format!(
        "{} may not {operation} on {}",
        view.caller().subject,
        label(view)
    );
    match &view.config().permission {
        None => Err(view.forbidden(format!(
            "{who}: {}'s Calendar config names no permission, so nobody may",
            view.type_name()
        ))),
        Some(permission) if !view.can(permission)? => {
            Err(view.forbidden(format!("{who}: it needs permission {permission}")))
        }
        Some(_) => Ok(()),
    }
}

// may_force holds a closure over held time to the permission's override.
fn may_force(view: &View<'_, CalendarConfig>) -> Hook<()> {
    let Some(permission) = &view.config().permission else {
        return permitted(view, ADD_CLOSURE);
    };
    let needed = format!("{permission}.override");
    if view.can(&needed)? {
        return Ok(());
    }
    Err(view.forbidden(format!(
        "{} may not close {} over held time: force needs permission {needed}",
        view.caller().subject,
        label(view)
    )))
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
) -> Result<i64, Problem> {
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
                duration(grid_ms)
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
                duration(length),
                duration(config.min_ms)
            ),
            json!({ "minMs": config.min_ms }),
        ));
    }
    if length > config.max_ms {
        return Err(problem(
            TOO_LONG,
            format!(
                "{} is longer than {label}'s longest hold, {}",
                duration(length),
                duration(config.max_ms)
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
                    duration(config.lead_ms)
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
                format!("{span} starts further than {} from now", duration(horizon)),
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
fn refusal(here: &RowRef, call: &SupplyCall, problem: Problem) -> supply::Refusal {
    let mut details = Map::new();
    details.insert("supply".into(), here.to_json());
    details.insert("hold".into(), Value::from(call.hold.as_str()));
    details.insert("item".into(), Value::from(call.item));
    details.insert("asked".into(), call.ask.to_json());
    details.extend(problem.extra);
    supply::Refusal {
        code: problem.code.to_owned(),
        reason: problem.reason,
        details,
    }
}

// fit is what does not fit of a contract call's interval, beside every
// other record and the intervals `also` the claim's other items ask.
fn fit(
    config: &CalendarConfig,
    here: &RowRef,
    resource: &Resource,
    call: &SupplyCall,
    now: i64,
    also: &[Interval],
) -> Option<supply::Refusal> {
    let asked = call.ask.interval()?;
    let label = format!("{} {}", here.schema, here.id);
    judge(config, &label, resource, &asked, now, Some(call), also)
        .err()
        .map(|problem| refusal(here, call, problem))
}

// check_hold answers whether holdFor would hold the item now, as Stock's
// does: an item the supply holds already fits, one its record has moved
// past does not (not_requested), and any other is held to the rules beside
// the intervals `also` the claim's other items ask.
fn check_hold(
    config: &CalendarConfig,
    here: &RowRef,
    resource: &Resource,
    params: &Value,
    now: i64,
) -> Option<CheckAnswer> {
    let call = SupplyCall::from_params(SupplyKind::Timed, params)?;
    let also: Vec<Interval> = SupplyCall::also(SupplyKind::Timed, params)
        .iter()
        .filter_map(Ask::interval)
        .collect();
    let recorded = resource
        .records
        .iter()
        .find(|record| record.is(&call))
        .cloned();
    Some(match supply::check_record(&call, recorded.as_ref()) {
        Some(answer) => answer,
        None => match fit(config, here, resource, &call, now, &also) {
            Some(refusal) => CheckAnswer::Refused(refusal),
            None => CheckAnswer::Fits,
        },
    })
}

// contract runs a writing call of the supply contract: the holder check,
// the lock and the record's step, then, for holdFor, what fits, then the
// record.
fn contract(
    cx: &Cx<'_, CalendarConfig>,
    operation: SupplyOperation,
    params: &Value,
) -> Hook<Value> {
    let Some(call) = SupplyCall::from_params(SupplyKind::Timed, params) else {
        return Err(cx.defect(format!(
            "{} took parameters of another shape",
            operation.name()
        )));
    };
    let begun = supply::begin(cx, BOOKINGS, operation, &call)?;
    if operation == SupplyOperation::Hold && matches!(begun, Begun::Apply { .. }) {
        let resource = resource(cx)?;
        if let Some(refusal) = fit(
            cx.config(),
            &RowRef::of(cx),
            &resource,
            &call,
            cx.now(),
            &[],
        ) {
            return Err(refusal.veto(cx));
        }
    }
    supply::finish(cx, BOOKINGS, operation, &call, &begun, None)
}

// future is the records that take time that has not ended by now.
fn future(resource: &Resource, now: i64) -> Vec<&Taken> {
    resource
        .taken
        .iter()
        .filter(|taken| taken.interval.end_ms > now)
        .collect()
}

// put_setting writes one of the row's settings; a null value deletes it.
fn put_setting(cx: &Cx<'_, CalendarConfig>, rows: &[SubRow], name: &str, value: Value) -> Hook<()> {
    let exists = rows.iter().any(|row| row.key == name);
    let mut values = Map::new();
    values.insert("value".into(), value.clone());
    match (exists, value.is_null()) {
        (false, true) => Ok(()),
        (true, true) => cx.delete_row(SETTINGS, name),
        (true, false) => cx.update_row(SETTINGS, name, values),
        (false, false) => cx.insert_row(SETTINGS, name, values),
    }
}

// set_opening_hours sets the row's own hours, its zone, or both; null for
// either goes back to the config's.
fn set_opening_hours(cx: &Cx<'_, CalendarConfig>, params: &Value) -> Hook<Value> {
    permitted(cx, SET_OPENING_HOURS)?;
    let config = cx.config();
    let rows = cx.sub_rows(SETTINGS)?;
    let own = |name: &str| {
        rows.iter()
            .find(|row| row.key == name)
            .and_then(|row| row.value("value"))
            .filter(|value| !value.is_null())
            .cloned()
    };
    let before = (own(HOURS), own(TIME_ZONE));
    let mut hours = before.0.clone();
    let mut time_zone = before.1.clone();
    match params.get("hours") {
        None => {}
        Some(Value::Null) => hours = None,
        Some(given) => {
            WeekHours::parse(given).map_err(|problem| {
                params_error(
                    cx,
                    SET_OPENING_HOURS,
                    &format!("/hours{}", problem.path),
                    problem.message,
                )
            })?;
            hours = Some(given.clone());
        }
    }
    match params.get("timeZone") {
        None => {}
        Some(Value::Null) => time_zone = None,
        Some(given) => {
            let name = given.as_str().unwrap_or_default();
            if zone(name).is_none() {
                return Err(params_error(
                    cx,
                    SET_OPENING_HOURS,
                    "/timeZone",
                    format!("{name} is not a time zone of the IANA database the core bundles"),
                ));
            }
            time_zone = Some(Value::from(name));
        }
    }
    let settings = |hours: &Option<Value>, time_zone: &Option<Value>| -> Hook<Settings> {
        let mut rows = Vec::new();
        if let Some(hours) = hours {
            rows.push(SubRow::new(
                HOURS,
                Map::from_iter([("value".to_owned(), hours.clone())]),
            ));
        }
        if let Some(time_zone) = time_zone {
            rows.push(SubRow::new(
                TIME_ZONE,
                Map::from_iter([("value".to_owned(), time_zone.clone())]),
            ));
        }
        settings_of(config, &rows).map_err(|reason| cx.defect(reason))
    };
    let was = settings(&before.0, &before.1)?;
    let will = settings(&hours, &time_zone)?;
    if was.time_zone != will.time_zone {
        // As a new version's timeZone: held time keeps its instants, and
        // its wall times would move under it.
        let resource = resource(cx)?;
        let future = future(&resource, cx.now());
        if !future.is_empty() {
            return Err(vetoed(
                cx,
                problem(
                    HAS_BOOKINGS,
                    format!(
                        "{} changes its time zone from {} to {} only while it holds no time that has not ended, and it holds time for {}",
                        label(cx),
                        was.time_zone,
                        will.time_zone,
                        plural(future.len() as i64, "claim item")
                    ),
                    json!({ "holds": future.iter().take(MAX_CLOSURES_SHOWN).map(|taken| taken.to_json()).collect::<Vec<_>>() }),
                ),
            ));
        }
    }
    if (hours.clone(), time_zone.clone()) == before {
        cx.unchanged()?;
    } else {
        put_setting(cx, &rows, HOURS, hours.unwrap_or(Value::Null))?;
        put_setting(cx, &rows, TIME_ZONE, time_zone.unwrap_or(Value::Null))?;
    }
    Ok(json!({ "hours": will.hours_json, "timeZone": will.time_zone }))
}

fn interval_params(
    cx: &View<'_, CalendarConfig>,
    operation: &str,
    params: &Value,
) -> Hook<Interval> {
    let number = |name: &str| params.get(name).and_then(supply::integer);
    let start_ms = number("startMs")
        .ok_or_else(|| params_error(cx, operation, "/startMs", "must be an integer"))?;
    let end_ms = number("endMs")
        .ok_or_else(|| params_error(cx, operation, "/endMs", "must be an integer"))?;
    if end_ms <= start_ms {
        return Err(params_error(
            cx,
            operation,
            "/endMs",
            "must be after startMs",
        ));
    }
    Ok(Interval { start_ms, end_ms })
}

// add_closure closes the row over an interval, refused over held time
// unless forced.
fn add_closure(cx: &Cx<'_, CalendarConfig>, params: &Value) -> Hook<Value> {
    permitted(cx, ADD_CLOSURE)?;
    let interval = interval_params(cx, ADD_CLOSURE, params)?;
    let reason = params
        .get("reason")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    let force = params.get("force") == Some(&Value::Bool(true));
    if force {
        may_force(cx)?;
    }
    let resource = resource(cx)?;
    let held: Vec<&Taken> = resource
        .taken
        .iter()
        .filter(|taken| taken.interval.overlaps(&interval))
        .collect();
    if !held.is_empty() && !force {
        return Err(vetoed(
            cx,
            problem(
                HAS_BOOKINGS,
                format!(
                    "{} holds time for {} over the closure; force keeps them and needs the permission's override",
                    label(cx),
                    plural(held.len() as i64, "claim item")
                ),
                json!({ "holds": held.iter().map(|taken| taken.to_json()).collect::<Vec<_>>() }),
            ),
        ));
    }
    let made = resource.settings.closures_made + 1;
    let settings = cx.sub_rows(SETTINGS)?;
    put_setting(cx, &settings, CLOSURES_MADE, Value::from(made))?;
    let closure = Closure {
        closure: format!("closure-{made}"),
        interval,
        reason,
    };
    let mut values = Map::new();
    values.insert("startMs".into(), Value::from(interval.start_ms));
    values.insert("endMs".into(), Value::from(interval.end_ms));
    values.insert("reason".into(), Value::from(closure.reason.as_str()));
    values.insert("by".into(), Value::from(cx.caller().subject.as_str()));
    values.insert("at".into(), Value::from(cx.now()));
    cx.insert_row(CLOSURES, &closure.closure, values)?;
    Ok(closure.to_json())
}

fn remove_closure(cx: &Cx<'_, CalendarConfig>, params: &Value) -> Hook<Value> {
    permitted(cx, REMOVE_CLOSURE)?;
    let name = params
        .get("closure")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let rows = cx.sub_rows(CLOSURES)?;
    let Some(closure) = rows
        .iter()
        .find(|row| row.key == name)
        .and_then(Closure::of)
    else {
        return Err(vetoed(
            cx,
            problem(
                NO_CLOSURE,
                format!("{} has no closure {name}", label(cx)),
                Value::Null,
            ),
        ));
    };
    cx.delete_row(CLOSURES, name)?;
    Ok(closure.to_json())
}

// window reads a listBookings or findAvailability window, at most 31 days.
fn window(view: &View<'_, CalendarConfig>, operation: &str, params: &Value) -> Hook<Interval> {
    let window = interval_params(view, operation, params)?;
    let length = window.end_ms - window.start_ms;
    if length > MAX_WINDOW_MS {
        return Err(vetoed(
            view,
            problem(
                WINDOW_TOO_LONG,
                format!(
                    "{operation} reads at most {} at once, not {}",
                    duration(MAX_WINDOW_MS),
                    duration(length)
                ),
                json!({ "maxMs": MAX_WINDOW_MS }),
            ),
        ));
    }
    Ok(window)
}

// list_bookings pages the row's records that take time over a window, by
// start.
fn list_bookings(cx: &Cx<'_, CalendarConfig>, params: &Value) -> Hook<Value> {
    let window = window(cx, LIST_BOOKINGS, params)?;
    let request = paging::page_request(params).map_err(|issue| {
        Stop::from(Refusal::operation_params(
            NAME,
            cx.use_id(),
            LIST_BOOKINGS,
            vec![issue],
        ))
    })?;
    let resource = resource(cx)?;
    let numbered: Vec<(i64, Value)> = resource
        .taken
        .iter()
        .filter(|taken| taken.interval.overlaps(&window))
        .skip(usize::try_from(request.after).unwrap_or(0))
        .take(usize::try_from(request.limit + 1).unwrap_or(usize::MAX))
        .enumerate()
        .map(|(at, taken)| (request.after + at as i64 + 1, taken.to_json()))
        .collect();
    let page = paging::page(numbered, request.limit, |(number, _)| *number);
    Ok(json!({
        "items": page.items.into_iter().map(|(_, item)| item).collect::<Vec<_>>(),
        "next": page.next,
    }))
}

// where_values reads a where member's values: a value, or a list of 1 to
// 100.
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

// keeps says whether a row's own field holds one of a where member's
// values, null for none: a boolean as 1 or 0, a number by its value.
fn keeps(own: &Map<String, Value>, field: &str, values: &[&Value]) -> bool {
    let held = own.get(field).filter(|value| !value.is_null());
    values.iter().any(|wanted| match (held, wanted) {
        (None, Value::Null) => true,
        (None, _) | (Some(_), Value::Null) => false,
        (Some(Value::Bool(flag)), Value::Number(number)) => {
            number.as_f64() == Some(if *flag { 1.0 } else { 0.0 })
        }
        (Some(held), wanted) => crate::values::equal(Some(held), Some(wanted)),
    })
}

// find_availability lists the slots of a duration that holdFor would take
// now across the table's rows, soonest first.
fn find_availability(cx: &Cx<'_, CalendarConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let type_name = cx.type_name();
    let window = window(cx, FIND_AVAILABILITY, params)?;
    let duration_ms = params
        .get("durationMs")
        .and_then(supply::integer)
        .ok_or_else(|| params_error(cx, FIND_AVAILABILITY, "/durationMs", "must be an integer"))?;
    let limit = params
        .get("limit")
        .and_then(supply::integer)
        .unwrap_or(DEFAULT_SLOTS);
    let label = format!("{type_name}'s Calendar");
    if duration_ms < config.min_ms {
        return Err(vetoed(
            cx,
            problem(
                TOO_SHORT,
                format!(
                    "{} is shorter than {label}'s shortest hold, {}",
                    duration(duration_ms),
                    duration(config.min_ms)
                ),
                json!({ "minMs": config.min_ms }),
            ),
        ));
    }
    if duration_ms > config.max_ms {
        return Err(vetoed(
            cx,
            problem(
                TOO_LONG,
                format!(
                    "{} is longer than {label}'s longest hold, {}",
                    duration(duration_ms),
                    duration(config.max_ms)
                ),
                json!({ "maxMs": config.max_ms }),
            ),
        ));
    }
    let ids: Option<Vec<String>> = params.get("ids").and_then(Value::as_array).map(|ids| {
        ids.iter()
            .filter_map(Value::as_str)
            .map(crate::subjects::canonical_subject)
            .collect()
    });
    let mut wanted_where: Vec<(String, Vec<&Value>)> = Vec::new();
    if let Some(filter) = params.get("where") {
        let Some(members) = filter.as_object() else {
            return Err(params_error(
                cx,
                FIND_AVAILABILITY,
                "/where",
                "must be an object of field values",
            ));
        };
        for (field, value) in members {
            if !config.filterable.iter().any(|known| known == field) {
                return Err(params_error(
                    cx,
                    FIND_AVAILABILITY,
                    &format!("/where/{field}"),
                    format!(
                        "{field} is not an own field of {type_name} that holds a string, a number or a boolean"
                    ),
                ));
            }
            let Some(listed) = where_values(value) else {
                return Err(params_error(
                    cx,
                    FIND_AVAILABILITY,
                    &format!("/where/{field}"),
                    "must be a value, or a list of 1 to 100 values, each a string, a number, a boolean or null",
                ));
            };
            wanted_where.push((field.clone(), listed));
        }
    }

    // The rows: those ids names and where keeps, by key, at most
    // MAX_INSTANCES and one more to tell truncated, each with the use's
    // sub-tables, read across the table a page at a time.
    let tables: Vec<TableRef> = [BOOKINGS, CLOSURES, SETTINGS]
        .iter()
        .map(|name| TableRef::new(cx.use_id(), *name))
        .collect();
    let mut rows: Vec<superschematic_rules::KeyedRow> = Vec::new();
    let mut after: Option<String> = None;
    'pages: loop {
        let page = cx.read_rows(type_name, &Map::new(), &tables, after.as_deref())?;
        for row in &page.rows {
            let named = ids
                .as_ref()
                .is_none_or(|ids| ids.contains(&crate::subjects::canonical_subject(&row.key)));
            let kept = wanted_where
                .iter()
                .all(|(field, values)| keeps(&row.row.own, field, values));
            if named && kept {
                rows.push(row.clone());
                if rows.len() > MAX_INSTANCES {
                    break 'pages;
                }
            }
        }
        match (page.more, page.rows.last()) {
            (true, Some(last)) => after = Some(last.key.clone()),
            _ => break,
        }
    }
    let mut truncated = rows.len() > MAX_INSTANCES;
    rows.truncate(MAX_INSTANCES);

    let now = cx.now();
    let earliest = window.start_ms.max(now + config.lead_ms);
    let latest = match config.horizon_ms {
        Some(horizon) => now + horizon,
        None => i64::MAX,
    };
    let wanted = usize::try_from(limit).unwrap_or(0) + 1;
    let mut slots: Vec<(i64, String, i64)> = Vec::new();
    let mut work = 0usize;
    let empty = Tables::new();
    'rows: for row in &rows {
        let tables = row.tables.get(cx.use_id()).unwrap_or(&empty);
        let resource =
            resource_of(config, &row.row.own, tables).map_err(|reason| cx.defect(reason))?;
        if resource.capacity <= 0 {
            continue;
        }
        let settings = &resource.settings;
        let offsets = Offsets::over(&settings.tz, window.start_ms, window.end_ms);
        let open = opening(&settings.hours, &offsets, window.start_ms, window.end_ms);
        let closed = merge(
            resource
                .closures
                .iter()
                .filter(|closure| closure.interval.overlaps(&window))
                .map(|closure| closure.interval)
                .collect(),
        );
        // Every hold's time with its buffers, by start: a sweep of the
        // starts takes each in as the window reaches it and lets it go as
        // the window passes it.
        let mut occupied: Vec<Interval> = resource
            .taken
            .iter()
            .map(|taken| config.occupied(&taken.interval))
            .collect();
        occupied.sort_by_key(|interval| (interval.start_ms, interval.end_ms));
        let starts = grid(
            &settings.hours,
            &offsets,
            earliest,
            window.end_ms - duration_ms + 1,
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
                break 'rows;
            }
            work += 1;
            let slot = Interval {
                start_ms: start,
                end_ms: start + duration_ms,
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
            let left = resource.capacity - most;
            if left > 0 {
                slots.push((start, row.key.clone(), left));
                found += 1;
            } else if let Some(at) = at {
                // Every start whose time with buffers reaches back into
                // the full stretch from that instant is full too.
                let until = full_until(&active, at, resource.capacity);
                work += active.len();
                skip_to = until.saturating_add(config.buffer_before);
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
                "endMs": start + duration_ms,
                "remaining": remaining,
            }))
            .collect::<Vec<_>>(),
        "truncated": truncated,
    }))
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

/// Calendar: time a supply opens.
#[derive(Clone, Copy, Debug, Default)]
pub struct Calendar;

impl Behavior for Calendar {
    type Config = CalendarConfig;

    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<CalendarConfig, ConfigError> {
        CalendarConfig::parse(raw, target)
    }

    // See the module's header: any change but a new time zone, or a
    // capacity that may fall, on a type with rows; Calendar leaves only a
    // type with none.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&CalendarConfig>,
        after: Option<&CalendarConfig>,
    ) -> Hook<Option<String>> {
        let (Some(before), Some(after)) = (before, after) else {
            if before.is_some() && cx.has_rows()? {
                return Ok(Some(
                    "Calendar is not removed from a schema that has instances, whose claims may hold its time".to_owned(),
                ));
            }
            return Ok(None);
        };
        if before.time_zone != after.time_zone && cx.has_rows()? {
            return Ok(Some(format!(
                "Calendar's timeZone changes from {} to {} only while the type has no rows: a row may hold time that has not ended, whose wall times would move under it, and a publish reads no row",
                before.time_zone, after.time_zone
            )));
        }
        let falls = match (&before.capacity, &after.capacity) {
            (Capacity::Fixed(was), Capacity::Fixed(will)) => will < was,
            (was, will) => was != will,
        };
        if falls && cx.has_rows()? {
            return Ok(Some(
                "Calendar's capacity falls, or moves to another source, only while the type has no rows: a row may hold more time at once than the new capacity, and a publish reads no row".to_owned(),
            ));
        }
        Ok(None)
    }

    fn check_params(
        &self,
        _config: &CalendarConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        ajv::params_issues(file(), operation, params)
    }

    fn guard(
        &self,
        view: &View<'_, CalendarConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        match request {
            GuardRequest::Delete => {
                let resource = resource(view)?;
                let now = view.now();
                let holding = resource
                    .taken
                    .iter()
                    .filter(|taken| taken.state == ItemState::Held || taken.interval.end_ms > now)
                    .count() as i64;
                Ok((holding > 0).then(|| {
                    Veto::new(format!(
                        "it holds time for {}",
                        plural(holding, "claim item")
                    ))
                    .with_code(supply::HELD)
                }))
            }
            GuardRequest::Update { patch, after } => {
                let Capacity::Field(field) = &view.config().capacity else {
                    return Ok(None);
                };
                if !patch.contains_key(field) {
                    return Ok(None);
                }
                let capacity = view.config().capacity_of(after);
                let resource = resource(view)?;
                let most = peak_from(&resource.taken, view.now());
                Ok((most > capacity).then(|| {
                    Veto::new(format!(
                        "{field} would hold {capacity}, and {} overlap at once",
                        plural(most, "hold")
                    ))
                    .with_code(HAS_BOOKINGS)
                    .with_details(
                        json!({ "field": field, "capacity": capacity, "peak": most })
                            .as_object()
                            .cloned()
                            .unwrap_or_default(),
                    )
                }))
            }
            _ => Ok(None),
        }
    }

    fn operation(&self, cx: &Cx<'_, CalendarConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            SET_OPENING_HOURS => set_opening_hours(cx, params),
            ADD_CLOSURE => add_closure(cx, params),
            REMOVE_CLOSURE => remove_closure(cx, params),
            LIST_BOOKINGS => list_bookings(cx, params),
            FIND_AVAILABILITY => find_availability(cx, params),
            supply::CHECK_HOLD => {
                let resource = resource(cx)?;
                check_hold(cx.config(), &RowRef::of(cx), &resource, params, cx.now())
                    .map(|answer| answer.to_json())
                    .ok_or_else(|| cx.defect("checkHold took parameters of another shape"))
            }
            _ => match SupplyOperation::parse(name) {
                Some(operation) if operation != SupplyOperation::Return => {
                    contract(cx, operation, params)
                }
                _ => Err(cx.defect(format!("it declares no operation {name}"))),
            },
        }
    }

    // The fields, at the read's time: the zone and the hours the row's
    // own settings or the config give, and its closures that have not
    // ended, soonest first, at most 100.
    fn read_fields_at(
        &self,
        config: &CalendarConfig,
        row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
        now: i64,
    ) -> Map<String, Value> {
        let mut fields = Map::new();
        let Ok(resource) = resource_of(config, &row.own, tables) else {
            return fields;
        };
        fields.insert(
            "timeZone".into(),
            Value::from(resource.settings.time_zone.as_str()),
        );
        fields.insert("hours".into(), resource.settings.hours_json.clone());
        fields.insert(
            "closures".into(),
            Value::Array(
                resource
                    .closures
                    .iter()
                    .filter(|closure| closure.interval.end_ms > now)
                    .take(MAX_CLOSURES_SHOWN)
                    .map(Closure::to_json)
                    .collect(),
            ),
        );
        fields
    }

    // Supply's checkHold, over the row and its sub-tables as a read gave
    // them, at the asking command's time.
    fn answer(
        &self,
        config: &CalendarConfig,
        query: &ContractQuery<'_>,
        row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
        now: i64,
    ) -> Option<Value> {
        if query.contract != supply::NAME || query.query != supply::CHECK_HOLD {
            return None;
        }
        let here = RowRef::from_json(query.params.get("supply")?)?;
        let resource = resource_of(config, &row.own, tables).ok()?;
        check_hold(config, &here, &resource, query.params, now).map(|answer| answer.to_json())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_span_reads_as_a_reader_says_it() {
        assert_eq!(wall_label(0), "1970-01-01 00:00");
        assert_eq!(wall_label(DAY_MS + 90 * MINUTE_MS), "1970-01-02 01:30");
        assert_eq!(duration(900_000), "15 minutes");
        assert_eq!(duration(DAY_MS * 31), "31 days");
        assert_eq!(duration(1500), "1500 ms");
        assert_eq!(plural(1, "claim item"), "1 claim item");
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
        let own = json!({"floor": 2, "quiet": true})
            .as_object()
            .cloned()
            .unwrap();
        assert!(keeps(&own, "floor", &[&json!(1), &json!(2)]));
        assert!(keeps(&own, "quiet", &[&json!(1)]));
        assert!(keeps(&own, "view", &[&Value::Null]));
        assert!(!keeps(&own, "floor", &[&Value::Null]));
    }

    #[test]
    fn the_peak_from_now_leaves_out_what_has_ended() {
        let taken = |start_ms, end_ms| Taken {
            holder: RowRef::new("Booking", "b"),
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
