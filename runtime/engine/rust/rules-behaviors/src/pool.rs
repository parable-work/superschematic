//! Pool, the members of a pool on a table (D104, step 3), a behavior only
//! the core implements, as Seats, Reconcile and Schedules are: each row of
//! the table a member, open, near its limit or out until a time, among
//! which `pick`, an operation of the table as a whole, chooses one to run
//! work on. A caller tells the pool what a provider said with `markOut` and
//! `reportRoom`; the core calls no provider: an out member past its time is
//! rechecked through the event `recheck`, which the API's behavior worker
//! hands to the project's handler, which asks the provider and reports.
//!
//! Pool passes D103's test. State of its own: each member's `availability`,
//! `outUntil`, `outReason` and `checkedAt`, its owned fields, and the
//! `recheck` deadline an out member arms. Rules over how it changes: a
//! member goes out by `markOut` or a check without room, for the provider's
//! time or `outForMs`, keeping the later time; it opens only through a check
//! with room made at or after its `outUntil` (a breaker whose half-open
//! probe is that check), never by the clock; an out member past its time is
//! rechecked at its `outUntil` and every `recheckAfterMs` after through the
//! event the project's handler answers; `pick` orders the open and near
//! members by tiers, `order` and key, counting a member out while its
//! table's Capacity has no room. Recurrence: a person's accounts at model
//! vendors, payment processors, SMS gateways, API credentials. It keeps no
//! sub-table and hears no change.
//!
//! `markOut({ until?, reason })` puts a member out until `until`, or
//! `outForMs` from now when it names none, and a time already past reads as
//! now. An out member keeps the later of its `outUntil` and the new one and
//! takes the new reason; a call that moves neither, an at-least-once
//! caller's repeat, is answered unchanged before any guard and writes
//! nothing. It arms the member's recheck at its `outUntil`.
//!
//! `reportRoom({ room, near?, resetsAt?, reason? })` records a check made
//! now, a fact recorded each time. With room it opens an out member whose
//! `outUntil` has come (`opened`), near when `near` says so, and keeps an
//! open or near one open or near as `near` says, clearing its recheck; a
//! check with room before an out member's `outUntil` records `checkedAt`
//! alone, since an early check may read a provider's figure from before the
//! refusal. With no room the member is out until the later of its
//! `outUntil` (when out) and `resetsAt`, or `outForMs` from now when the
//! check names no time, for `reason`, else its reason when out already,
//! else `no room`; its recheck is armed then, or `recheckAfterMs` from now
//! when that is later, so a reset already past cannot spin the handler.
//! `near` is read only with room, `resetsAt` and `reason` only without: a
//! provider's figure often carries all three.
//!
//! `pick({ match?, prefer?, exclude? })`, an operation of the table as a
//! whole that writes nothing (a command without a row, D104), reads the
//! members `match` keeps, its equalities over own fields pushed to the host
//! as a read across the table's `equal` and checked again here, a page at a
//! time, at most [`MAX_MEMBERS`] of them (`pool_too_large` past it, a
//! refusal rather than an answer from the first pages, which would pick a
//! worse member silently). A member is out while its availability is out,
//! or while its table's Capacity has no room ([`crate::capacity`], Budget's
//! meters); `exclude` leaves out the keys the caller tried in this attempt.
//! Of the rest, it answers the first of the open members `prefer` keeps,
//! then of its near ones, then of the other open and near ones, each tier
//! by the `order` field ascending, a member with no value after every one
//! with one, then by key. With none it answers `member: null` and `until`,
//! the soonest time a member `match` keeps may be back: an out member's
//! `outUntil`, `recheckAfterMs` from now for one past it awaiting a check,
//! the time Capacity gives a member room, the later of the two for one out
//! both ways; an excluded out member counts toward it, an excluded open one
//! does not. A UUID field's value in `match` and `prefer`, and a key in
//! `exclude`, compare in either form, by its base62 wire form
//! ([`crate::subjects::canonical_subject`]). A null member of `match` or
//! `prefer` is absent, as D12 has every optional value, since the Go, Rust
//! and Python servers and SDKs send none: a pick asks for a field's value,
//! never its absence. It needs no permission: what it answers is what a
//! read of the table shows, and its route holds who calls.
//!
//! The recheck's deadline, armed by markOut, a check without room and its
//! own fire, falls due on the worker: on a member no longer out it does
//! nothing; on one whose `outUntil` a raw write moved past the fire it arms
//! again there; otherwise it emits the event `recheck` (`{ outUntil,
//! outReason?, checkedAt?, dueAt, firedAt }`, about the row, which the
//! event's own key names) and arms the next `recheckAfterMs` on. A handler
//! is idempotent by the row's key and `dueAt`, which no two fires of a row
//! share. The fire calls no operation, so no guard is asked and a lease
//! never holds it. A deleted member's deadline goes with its row.
//!
//! The config's `permission` holds markOut and reportRoom, checked before
//! any guard (`settle`), after the parameters, so `forbidden` comes before
//! another use's veto (Lease's `held_by_another`). Pool composes with Lease
//! only through Lease's `exempt` (`Pool.markOut`, `Pool.reportRoom`), with
//! Budget only through Capacity, and with Workflow not at all.
//!
//! configChange: any config may change, each new time applying from the
//! next markOut, check or fire. Pool joins a type that has rows, each
//! reading as open, and cannot leave one: the members' availability and
//! their rechecks would stay behind.

use std::cmp::Ordering;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, FieldSpec, Hook, KeyedRow,
    SchemaIssue, Veto, View,
};

use crate::capacity::{self, Room};
use crate::constants::OneWayKind;
use crate::declared;
use crate::schema::{self, Issues};
use crate::subjects::canonical_subject;
use crate::values;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Pool";

/// Pool's declaration, the file `make behaviors` writes for the package
/// `@superschematic/rules`, read in place.
pub const DECLARATION: &str = include_str!("../declarations/Pool.behavior.json");

/// The operations, by their declared names: markOut and reportRoom of a
/// row, pick of the table as a whole.
pub const MARK_OUT: &str = "markOut";
pub const REPORT_ROOM: &str = "reportRoom";
pub const PICK: &str = "pick";

/// The deadline an out member arms, and the event its fire emits.
pub const RECHECK: &str = "recheck";

/// How long a member is out when markOut or a check names no time: thirty
/// minutes.
pub const DEFAULT_OUT_FOR_MS: i64 = 1_800_000;

/// How often an out member past its time is rechecked: two minutes.
pub const DEFAULT_RECHECK_AFTER_MS: i64 = 120_000;

/// The most members a pick reads (D104): four pages of LINKED_PAGE.
pub const MAX_MEMBERS: usize = 2000;

/// The most keys a pick's exclude names.
pub const MAX_EXCLUDE: usize = 100;

/// The IR types an order field takes: Constants' ONE_WAY_NUMBERS, held to
/// the build's list by its test.
pub const ORDER_TYPES: [&str; 3] = ["number", "Generic.Int64", "Generic.Float64"];

/// A member's availabilities.
pub const OPEN: &str = "open";
pub const NEAR: &str = "near";
pub const OUT: &str = "out";

/// The reason a check without room gives when it names none and the member
/// was not out.
const NO_ROOM: &str = "no room";

/// The longest a reason may be.
const MAX_REASON: usize = 256;

// The members the configSchema takes.
const CONFIG_KEYS: [&str; 4] = ["order", "outForMs", "permission", "recheckAfterMs"];

// The IR types whose values a match compares in their base62 wire form.
const UUID_TYPES: [&str; 2] = ["Identity.UUID", "Identity.UserID"];

/// Pool's config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PoolConfig {
    /// The own number field whose value ranks the members.
    pub order: String,
    pub out_for_ms: i64,
    pub recheck_after_ms: i64,
    pub permission: Option<String>,
    /// The type's name and own fields as its spec gives them (name, IR
    /// type, whether it holds an instant): what match and prefer are held
    /// to at a pick's parameter check, and which of their values are UUIDs.
    pub type_name: String,
    pub fields: Vec<FieldSpec>,
}

impl PoolConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then its rules, each a reason the core prefixes with the type
    /// and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<PoolConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let order = raw
            .get("order")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let fields = target.fields();
        let Some(found) = fields.iter().find(|field| field.name == order) else {
            let names: Vec<&str> = fields.iter().map(|field| field.name.as_str()).collect();
            return Err(format!(
                "order {} is not a field of {} (its fields: {})",
                schema::quoted(&order),
                target.type_name(),
                if names.is_empty() {
                    "none".to_owned()
                } else {
                    names.join(", ")
                }
            )
            .into());
        };
        if !ORDER_TYPES.contains(&found.type_name.as_str()) {
            return Err(format!(
                "order {} is not a number field of {}: it ranks the members by a number (number, Generic.Int64 or Generic.Float64)",
                schema::quoted(&order),
                target.type_name()
            )
            .into());
        }
        Ok(PoolConfig {
            order,
            out_for_ms: raw
                .get("outForMs")
                .and_then(whole)
                .unwrap_or(DEFAULT_OUT_FOR_MS),
            recheck_after_ms: raw
                .get("recheckAfterMs")
                .and_then(whole)
                .unwrap_or(DEFAULT_RECHECK_AFTER_MS),
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
            type_name: target.type_name().to_owned(),
            fields: fields.to_vec(),
        })
    }

    // field is an own field of the type by its name.
    fn field(&self, name: &str) -> Option<&FieldSpec> {
        self.fields.iter().find(|field| field.name == name)
    }

    // holds_uuid reports whether an own field holds a UUID, whose values a
    // match compares in their base62 wire form.
    fn holds_uuid(&self, name: &str) -> bool {
        self.field(name)
            .is_some_and(|field| UUID_TYPES.contains(&field.type_name.as_str()))
    }
}

/// Pool: the members of a pool, each row of a table.
#[derive(Clone, Copy, Debug, Default)]
pub struct Pool;

impl Behavior for Pool {
    type Config = PoolConfig;

    // The declaration file names its four owned fields, that it arms
    // deadlines, markOut and reportRoom of a row and pick of the table, the
    // event recheck, the contract Capacity it asks and its veto.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<PoolConfig, ConfigError> {
        PoolConfig::parse(raw, target)
    }

    // Any config may change; Pool joins a type with rows and does not leave
    // one, whose members' availability and rechecks would stay behind.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        before: Option<&PoolConfig>,
        after: Option<&PoolConfig>,
    ) -> Hook<Option<String>> {
        Ok((before.is_some() && after.is_none())
            .then(|| "the members' availability and their rechecks would stay behind".to_owned()))
    }

    // Each operation's paramsSchema in the validator's words, then pick's
    // match and prefer held to the type's own fields.
    fn check_params(
        &self,
        config: &PoolConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(config, operation, params).into_issues()
    }

    // A new member is open, as a breaker starts closed.
    fn initialize(&self, cx: &Cx<'_, PoolConfig>, _params: &Value) -> Hook<()> {
        cx.set("availability", OPEN)
    }

    // markOut and reportRoom: the permission, before any guard; then
    // markOut's repeat, answered unchanged. A check is a fact, recorded
    // each time.
    fn settle(
        &self,
        view: &View<'_, PoolConfig>,
        operation: &str,
        params: &Value,
    ) -> Hook<Option<Value>> {
        let verb = match operation {
            MARK_OUT => "mark",
            REPORT_ROOM => "report a check of",
            _ => return Ok(None),
        };
        if let Some(permission) = &view.config().permission {
            if !view.can(permission)? {
                let who = match view.caller().subject.as_str() {
                    "" => "a caller with no subject",
                    subject => subject,
                };
                let target = format!("{} {}", view.type_name(), view.key().unwrap_or_default());
                let what = if operation == MARK_OUT {
                    format!("{verb} {target} out")
                } else {
                    format!("{verb} {target}")
                };
                return Err(view.forbidden(format!(
                    "{who} may not {what}: it needs permission {permission}"
                )));
            }
        }
        if operation != MARK_OUT {
            return Ok(None);
        }
        let state = State::of(view);
        let reason = params
            .get("reason")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let next = next_until(view, params);
        match (&state.out_until, &state.out_reason) {
            (Some(until), Some(kept))
                if state.availability == OUT && *until >= next && kept == reason =>
            {
                Ok(Some(json!({
                    "availability": OUT,
                    "outUntil": until,
                    "outReason": kept,
                    "unchanged": true,
                })))
            }
            _ => Ok(None),
        }
    }

    fn operation(&self, cx: &Cx<'_, PoolConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            MARK_OUT => mark_out(cx, params),
            REPORT_ROOM => report_room(cx, params),
            PICK => pick(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // The recheck: see the module's header.
    fn deadline(&self, cx: &Cx<'_, PoolConfig>, name: &str, payload: &Value) -> Hook<()> {
        if name != RECHECK {
            return Err(cx.defect(format!(
                "deadline {name} fell due, and Pool arms only {RECHECK}"
            )));
        }
        let state = State::of(cx);
        if state.availability != OUT {
            return Ok(());
        }
        let fired = cx.now();
        let due = payload.get("dueAt").and_then(whole).unwrap_or(fired);
        if let Some(until) = state.out_until.filter(|until| *until > fired) {
            return cx.arm(RECHECK, until, json!({ "dueAt": until }));
        }
        let mut event = Map::new();
        event.insert(
            "outUntil".into(),
            Value::from(state.out_until.unwrap_or(due)),
        );
        if let Some(reason) = &state.out_reason {
            event.insert("outReason".into(), Value::from(reason.as_str()));
        }
        if let Some(checked) = state.checked_at {
            event.insert("checkedAt".into(), Value::from(checked));
        }
        event.insert("dueAt".into(), Value::from(due));
        event.insert("firedAt".into(), Value::from(fired));
        cx.emit(RECHECK, Value::Object(event))?;
        let next = fired + cx.config().recheck_after_ms;
        cx.arm(RECHECK, next, json!({ "dueAt": next }))
    }
}

/// A member's own state as the run has it, an absent availability read as
/// open.
struct State {
    availability: String,
    out_until: Option<i64>,
    out_reason: Option<String>,
    checked_at: Option<i64>,
}

impl State {
    fn of(view: &View<'_, PoolConfig>) -> State {
        State::read(|field| view.get(field))
    }

    // read reads the owned fields through a getter: the run's own row's, or
    // a row a read across the table gave.
    fn read(get: impl Fn(&str) -> Option<Value>) -> State {
        State {
            availability: get("availability")
                .and_then(|value| value.as_str().map(str::to_owned))
                .unwrap_or_else(|| OPEN.to_owned()),
            out_until: get("outUntil").as_ref().and_then(whole),
            out_reason: get("outReason").and_then(|value| value.as_str().map(str::to_owned)),
            checked_at: get("checkedAt").as_ref().and_then(whole),
        }
    }
}

// next_until is the time a markOut names: its until, or outForMs from now,
// a time already past read as now.
fn next_until(view: &View<'_, PoolConfig>, params: &Value) -> i64 {
    let now = view.now();
    params
        .get("until")
        .and_then(whole)
        .unwrap_or(now + view.config().out_for_ms)
        .max(now)
}

fn mark_out(cx: &Cx<'_, PoolConfig>, params: &Value) -> Hook<Value> {
    let state = State::of(cx);
    let reason = params
        .get("reason")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let mut until = next_until(cx, params);
    if state.availability == OUT {
        until = until.max(state.out_until.unwrap_or(until));
    }
    cx.set("availability", OUT)?;
    cx.set("outUntil", until)?;
    cx.set("outReason", reason)?;
    cx.arm(RECHECK, until, json!({ "dueAt": until }))?;
    Ok(json!({ "availability": OUT, "outUntil": until, "outReason": reason }))
}

fn report_room(cx: &Cx<'_, PoolConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let state = State::of(cx);
    let now = cx.now();
    let out = state.availability == OUT;
    if params.get("room") == Some(&Value::Bool(true)) {
        // An out member opens only on a check made at or after its time.
        if let (true, Some(until)) = (out, state.out_until.filter(|until| *until > now)) {
            cx.set("checkedAt", now)?;
            let mut answer = Map::new();
            answer.insert("availability".into(), Value::from(OUT));
            answer.insert("checkedAt".into(), Value::from(now));
            answer.insert("outUntil".into(), Value::from(until));
            if let Some(reason) = &state.out_reason {
                answer.insert("outReason".into(), Value::from(reason.as_str()));
            }
            return Ok(Value::Object(answer));
        }
        let availability = if params.get("near") == Some(&Value::Bool(true)) {
            NEAR
        } else {
            OPEN
        };
        cx.set("availability", availability)?;
        cx.set("checkedAt", now)?;
        cx.set("outUntil", Value::Null)?;
        cx.set("outReason", Value::Null)?;
        cx.clear(RECHECK)?;
        return Ok(if out {
            json!({ "availability": availability, "checkedAt": now, "opened": true })
        } else {
            json!({ "availability": availability, "checkedAt": now })
        });
    }
    let named = params
        .get("resetsAt")
        .and_then(whole)
        .unwrap_or(now + config.out_for_ms);
    let kept = state.out_until.filter(|_| out);
    let until = kept.map_or(named, |kept| kept.max(named)).max(now);
    let reason = params
        .get("reason")
        .and_then(Value::as_str)
        .map(str::to_owned)
        .or_else(|| state.out_reason.clone().filter(|_| out))
        .unwrap_or_else(|| NO_ROOM.to_owned());
    cx.set("availability", OUT)?;
    cx.set("outUntil", until)?;
    cx.set("outReason", reason.as_str())?;
    cx.set("checkedAt", now)?;
    let recheck = until.max(now + config.recheck_after_ms);
    cx.arm(RECHECK, recheck, json!({ "dueAt": recheck }))?;
    Ok(json!({
        "availability": OUT,
        "checkedAt": now,
        "outUntil": until,
        "outReason": reason,
    }))
}

/// A member as a pick weighs it.
struct Member<'r> {
    row: &'r KeyedRow,
    availability: String,
    /// Whether it is out, its own way or its Capacity's.
    out: bool,
    /// When it may be back, for one out; none when only a write would.
    back: Option<i64>,
    preferred: bool,
}

fn pick(cx: &Cx<'_, PoolConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let type_name = cx.type_name();
    let now = cx.now();
    let matched = named(params.get("match")).unwrap_or_default();
    let preferred = named(params.get("prefer"));
    let excluded: Vec<String> = params
        .get("exclude")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(canonical_subject)
        .collect();
    // match's equalities, each UUID in its base62 wire form, which the
    // host binds as a command's value of the field.
    let equal: Map<String, Value> = matched
        .iter()
        .map(|(field, value)| (field.clone(), wire(config, field, value)))
        .collect();
    let tables = cx.contract_tables(type_name, capacity::NAME)?;
    let mut rows: Vec<KeyedRow> = Vec::new();
    let mut after: Option<String> = None;
    loop {
        let page = cx.read_rows(type_name, &equal, &tables, after.as_deref())?;
        rows.extend(page.rows.iter().cloned());
        if rows.len() > MAX_MEMBERS {
            let mut details = Map::new();
            details.insert("max".into(), Value::from(MAX_MEMBERS));
            return Err(cx.veto(
                Veto::new(format!(
                    "{type_name}'s Pool reads every member match keeps at a pick, at most {MAX_MEMBERS}, and more match: narrow match, or pick with code of the project's own"
                ))
                .with_code("pool_too_large")
                .with_details(details),
            ));
        }
        match (page.more, page.rows.last()) {
            (true, Some(last)) => after = Some(last.key.clone()),
            _ => break,
        }
    }
    let use_id = cx.use_id();
    let mut members: Vec<Member<'_>> = Vec::new();
    for row in &rows {
        // match again, so a host that answered more than equal asked still
        // picks right.
        if !keeps(config, &matched, row) {
            continue;
        }
        let state = State::read(|field| row.row.owned(use_id, field).cloned());
        let room: Room = capacity::has_room(cx, type_name, row)?;
        let own_out = state.availability == OUT;
        let own_back = state
            .out_until
            .filter(|until| *until > now)
            .unwrap_or(now + config.recheck_after_ms);
        let back = match (own_out, room.room) {
            (true, true) => Some(own_back),
            (false, false) => room.until,
            (true, false) => room.until.map(|until| until.max(own_back)),
            (false, true) => None,
        };
        members.push(Member {
            row,
            out: own_out || !room.room,
            back,
            preferred: preferred
                .as_ref()
                .is_none_or(|prefer| keeps(config, prefer, row)),
            availability: state.availability,
        });
    }
    let is_excluded = |member: &Member<'_>| excluded.contains(&canonical_subject(&member.row.key));
    let tier = |member: &Member<'_>| match (member.preferred, member.availability.as_str()) {
        (true, NEAR) => 1,
        (true, _) => 0,
        (false, _) => 2,
    };
    let order_of = |member: &Member<'_>| member.row.row.own.get(&config.order).cloned();
    let chosen = members
        .iter()
        .filter(|member| !member.out && !is_excluded(member))
        .min_by(|a, b| {
            tier(a)
                .cmp(&tier(b))
                .then_with(|| ranked(order_of(a).as_ref(), order_of(b).as_ref()))
                .then_with(|| a.row.key.cmp(&b.row.key))
        });
    if let Some(member) = chosen {
        let order = order_of(member).map_or(Value::Null, |value| number_of(&value));
        return Ok(json!({
            "member": {
                "key": member.row.key,
                "availability": member.availability,
                "order": order,
                "preferred": member.preferred,
            }
        }));
    }
    let until = members
        .iter()
        .filter(|member| member.out)
        .filter_map(|member| member.back)
        .min();
    Ok(match until {
        Some(until) => json!({ "member": null, "until": until }),
        None => json!({ "member": null }),
    })
}

// keeps reports whether a row's own fields hold every value a match or a
// prefer names, a UUID field's in either form.
fn keeps(config: &PoolConfig, named: &Map<String, Value>, row: &KeyedRow) -> bool {
    named.iter().all(|(field, value)| {
        let held = row.row.own.get(field);
        if config.holds_uuid(field) {
            if let (Some(Value::String(held)), Value::String(value)) = (held, value) {
                return canonical_subject(held) == canonical_subject(value);
            }
        }
        values::equal(held, Some(value))
    })
}

// wire is a match's value as the host binds it: a UUID field's string in
// its base62 wire form, anything else as given.
fn wire(config: &PoolConfig, field: &str, value: &Value) -> Value {
    match value {
        Value::String(text) if config.holds_uuid(field) => Value::from(canonical_subject(text)),
        other => other.clone(),
    }
}

// ranked orders two members' order values ascending, a member with no value
// after every one with one.
fn ranked(a: Option<&Value>, b: Option<&Value>) -> Ordering {
    let present = |value: Option<&Value>| value.filter(|value| !value.is_null()).is_some();
    match (present(a), present(b)) {
        (true, false) => Ordering::Less,
        (false, true) => Ordering::Greater,
        _ => values::one_way_order(OneWayKind::Number, a, b).unwrap_or(Ordering::Equal),
    }
}

// number_of is an order value as a pick answers it: a number, or a decimal
// string an adapter read a BIGINT as, read as the number it holds.
fn number_of(value: &Value) -> Value {
    match value {
        Value::String(text) => text
            .parse::<i64>()
            .map(Value::from)
            .or_else(|_| text.parse::<f64>().map(Value::from))
            .unwrap_or_else(|_| value.clone()),
        other => other.clone(),
    }
}

// named is a match's or a prefer's members that hold a value: a null member
// is absent, as D12 has every optional value, in every server and SDK that
// sends one, so a pick asks for a field's value, never its absence.
fn named(value: Option<&Value>) -> Option<Map<String, Value>> {
    value.and_then(Value::as_object).map(|members| {
        members
            .iter()
            .filter(|(_, value)| !value.is_null())
            .map(|(field, value)| (field.clone(), value.clone()))
            .collect()
    })
}

fn whole(value: &Value) -> Option<i64> {
    match value {
        Value::Number(number) => number.as_i64().or_else(|| {
            number
                .as_f64()
                .filter(|n| n.is_finite() && n.fract() == 0.0)
                .map(|n| n as i64)
        }),
        Value::String(text) => text.parse().ok(),
        _ => None,
    }
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
    for key in config
        .keys()
        .filter(|key| !CONFIG_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    match config.get("order") {
        None => issues.push("", "required", "must have required property 'order'"),
        Some(order) => schema::text_issues("/order", order, &mut issues),
    }
    if let Some(permission) = config.get("permission") {
        schema::text_issues("/permission", permission, &mut issues);
    }
    for key in ["outForMs", "recheckAfterMs"] {
        if let Some(value) = config.get(key) {
            schema::integer_issues(
                &schema::member("", key),
                value,
                Some(1000),
                None,
                &mut issues,
            );
        }
    }
    issues
}

// params_issues lists what an operation's parameters break of its closed
// paramsSchema, in the behavior registry's words, and then what pick's match
// and prefer break of the type's own fields.
fn params_issues(config: &PoolConfig, operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    let keys: &[&str] = match operation {
        MARK_OUT => &["reason", "until"],
        REPORT_ROOM => &["near", "reason", "resetsAt", "room"],
        PICK => &["exclude", "match", "prefer"],
        _ => return issues,
    };
    for key in given.keys().filter(|key| !keys.contains(&key.as_str())) {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    let instant = |at: &str, value: &Value, issues: &mut Issues| {
        schema::integer_issues(at, value, Some(0), None, issues);
    };
    match operation {
        MARK_OUT => {
            if let Some(until) = given.get("until") {
                instant("/until", until, &mut issues);
            }
            match given.get("reason") {
                None => issues.push("", "required", "must have required property 'reason'"),
                Some(reason) => reason_issues("/reason", reason, &mut issues),
            }
        }
        REPORT_ROOM => {
            match given.get("room") {
                None => issues.push("", "required", "must have required property 'room'"),
                Some(room) => schema::boolean_issues("/room", room, &mut issues),
            }
            if let Some(near) = given.get("near") {
                schema::boolean_issues("/near", near, &mut issues);
            }
            if let Some(resets) = given.get("resetsAt") {
                instant("/resetsAt", resets, &mut issues);
            }
            if let Some(reason) = given.get("reason") {
                reason_issues("/reason", reason, &mut issues);
            }
        }
        _ => {
            for key in ["match", "prefer"] {
                if let Some(named) = given.get(key) {
                    named_issues(config, key, named, &mut issues);
                }
            }
            if let Some(exclude) = given.get("exclude") {
                exclude_issues(exclude, &mut issues);
            }
        }
    }
    issues
}

// reason_issues lists what a reason breaks of `{ type: string, minLength:
// 1, maxLength: 256 }`.
fn reason_issues(at: &str, value: &Value, issues: &mut Issues) {
    match value {
        Value::String(text) if schema::length(text) > MAX_REASON => issues.push(
            at,
            "maxLength",
            format!("must NOT have more than {MAX_REASON} characters"),
        ),
        other => schema::text_issues(at, other, issues),
    }
}

// named_issues lists what a match or a prefer breaks of `{ type: object,
// additionalProperties: { type: [string, number, boolean, null] } }`, then
// of the type's own fields: each member that holds a value names an own
// field, and none that holds an instant. A null member is absent (named).
fn named_issues(config: &PoolConfig, key: &str, named: &Value, issues: &mut Issues) {
    let at = format!("/{key}");
    let Some(members) = named.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    for (field, value) in members {
        let path = schema::member(&at, field);
        if value.is_object() || value.is_array() {
            issues.push(&path, "type", "must be string,number,boolean,null");
            continue;
        }
        if value.is_null() {
            continue;
        }
        match config.field(field) {
            None => {
                let names: Vec<&str> = config
                    .fields
                    .iter()
                    .map(|field| field.name.as_str())
                    .collect();
                issues.push(
                    &path,
                    "field",
                    format!(
                        "{} has no own field {field} (its own fields: {})",
                        config.type_name,
                        if names.is_empty() {
                            "none".to_owned()
                        } else {
                            names.join(", ")
                        }
                    ),
                );
            }
            Some(found) if found.date_time => issues.push(
                &path,
                "field",
                format!("{field} holds an instant, which {key} compares no value of"),
            ),
            Some(_) => {}
        }
    }
}

// exclude_issues lists what an exclude breaks of `{ type: array, maxItems:
// 100, items: { type: string, minLength: 1 } }`.
fn exclude_issues(exclude: &Value, issues: &mut Issues) {
    let Some(items) = exclude.as_array() else {
        issues.push("/exclude", "type", "must be array");
        return;
    };
    if items.len() > MAX_EXCLUDE {
        issues.push(
            "/exclude",
            "maxItems",
            format!("must NOT have more than {MAX_EXCLUDE} items"),
        );
    }
    for (index, item) in items.iter().enumerate() {
        schema::text_issues(&format!("/exclude/{index}"), item, issues);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_order_with_no_value_comes_after_every_one_with_one() {
        let (one, two) = (json!(1), json!(2));
        assert_eq!(ranked(Some(&one), Some(&two)), Ordering::Less);
        assert_eq!(ranked(None, Some(&one)), Ordering::Greater);
        assert_eq!(ranked(Some(&Value::Null), Some(&two)), Ordering::Greater);
        assert_eq!(ranked(Some(&one), None), Ordering::Less);
        assert_eq!(ranked(None, Some(&Value::Null)), Ordering::Equal);
        assert_eq!(
            ranked(Some(&json!("10")), Some(&json!(9))),
            Ordering::Greater
        );
        assert_eq!(number_of(&json!("10")), json!(10));
        assert_eq!(number_of(&json!(2.5)), json!(2.5));
    }
}
