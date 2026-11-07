//! Budget, reserve-then-settle budgets on a row (D16), ported onto the pure
//! core (D103) from the engine's
//! (`runtime/engine-workqueue/rust/src/budget.rs`), in units the deployment
//! names. Each meter of the config has, per row, a row of Budget's `meters`
//! sub-table: what the row has used, what it has reserved, the limit
//! setLimit gave it, the start of the period its usage counts from, and the
//! usage a provider last reported. `reserved` is the row's own reservation,
//! the one its claims made, plus what it holds for the rows inside it; the
//! own part is kept apart, with the lease token it was made under, so
//! settling releases exactly that.
//!
//! Budget passes D103's test of a behavior. Its state is its `meters`,
//! `holds` and `buckets` sub-tables: what a row has used and reserved of
//! each meter, what it holds for the rows inside it, and a rolling meter's
//! usage by bucket. Its rules hold a reservation to every limit up a chain
//! of scopes, never refuse usage, settle what a lease reserved when the
//! lease ends, start a meter's count again at each period (hourly, daily,
//! weekly, monthly or everyMs, in UTC), count a rolling window, and take the
//! usage a provider reports in place of its own count until the provider's
//! period rolls over (D104). And it recurs: an API's quotas, a compute
//! budget, a workspace's caps over its members.
//!
//! A meter's limit is the row's `limitField` while it holds a count, else
//! the config's limit; without `limitField`, the one setLimit set, else the
//! config's limit, as `reserveField` falls back to `reserve`. With none, the
//! meter counts without a limit of its own. A reservation fits while used
//! plus reserved plus the amount is within the limit. What a claim reserves
//! of a meter is the row's `reserveField` when it holds a positive integer,
//! else the config's `reserve`.
//!
//! Periods (D104). A meter's `reset` starts its used again at 0 at each
//! period's start, in UTC: each hour, each day, each Monday 00:00 (ISO
//! 8601's week), each first of the month 00:00, or each multiple of
//! `everyMs` (a whole number of minutes) from the epoch; reservations are
//! kept. A read never writes: a meter whose period has passed reads 0, and
//! the next write of its row starts the new period first. With `window:
//! "rolling"`, which needs a reset, the meter counts the usage of its last
//! period rather than of the current one: its usage kept in buckets of a
//! sixtieth of the period each (a month is thirty days for a window, since
//! a calendar month has no one length), in the `buckets` sub-table, each
//! write adding to the current bucket and deleting the ones that left the
//! window. The window counts every bucket that overlaps the last period,
//! the current one and the sixty before it, so no stretch of a period's
//! length lets more than the limit through, a boundary's included; a
//! bucket leaves it a period after its end, and its usage is counted for as
//! much as a bucket longer than a period.
//!
//! Observations (D104). `observe({ meter, used, limit?, resetsAt? })`, for
//! a caller holding `observePermission`, records the usage a provider
//! reports: while it holds, the meter's used is the provider's figure plus
//! the usage recorded since, and its limit the provider's when it gave one.
//! It holds until `resetsAt`, else until the meter's next period start, and
//! for a meter with no reset until the next observe. It passes nothing up a
//! chain of scopes: a scope counts the usage its inner rows record, which
//! the provider's figure already holds.
//!
//! Scopes. A meter may name a scope: a to-one relation of the table (Links
//! is a relation under D103), whose target encloses the row and implements
//! Budget with the same meter, which the build checks. Budget never writes
//! another row's state (D16): the row calls an operation of its scope
//! through `Cx::call_on`, `reserveFor`, `settleFor` or `recordUsageFor`,
//! which applies the scope's own check, records what the scope holds for
//! the row, writes the scope's sub-tables, and passes the change on up the
//! scope's own scope, in the command's change set, as the command's caller.
//! The scope operations cannot free budget a row still holds: a scope reads
//! the row's `meters` as the run has them ([`View::sub_rows_of`]) before it
//! releases anything, holds no more for a row than the row has reserved,
//! and takes reservations and usage only from a row whose scope relation
//! for the meter points at it. A scope relation does not move while a
//! reservation of the meter is held through it (the guard, on the update of
//! the relation's column); the row records the scope that holds the
//! reservation, and releases go there.
//!
//! Usage is never refused: `recordUsage` adds the amount to used, releases
//! the part of it the own reservation covers at every scope, and reports
//! each row of the chain left over its limit. With `onExceeded` the holder
//! of the active lease gets a directive through Lease's `direct`, once per
//! meter per lease token; with `escalate` each row of the chain left over
//! its limit moves its own status by its own config, through Workflow's
//! `transition`; a move a guard vetoes or the caller may not make leaves
//! the status.
//!
//! `checkReserve`, read-only, answers whether `reserve` would fit now, here
//! and up the chain, after the reservations of an ended lease are settled
//! as `reserve` settles them first, and when not, `until`: the earliest
//! instant at which the clock alone lets it fit (a period's start, an
//! observation's lapse, a bucket leaving a rolling window), here and at each
//! scope it reads, with every reservation as it is; for daily meters alone
//! the engine's next UTC day. It lists the scopes it read, each with the
//! values of it the answer turns on.
//!
//! Lease. On a table that implements Lease a reservation is made under the
//! active lease and lives as long as it: `reserve` is refused while no
//! lease is active (`not_leased`), and after a Lease operation or Lease's
//! deadline, and before each `reserve`, the reservations of a lease that is
//! no longer the active one are settled. The lease is read by Lease's rule
//! from its owned fields (active while its holder holds a subject and the
//! clock is before its expiry), as the engine read `Lease.active`. Deleting
//! the row settles everything it reserved up the chain.
//!
//! Capacity (D104). Budget implements the contract Capacity, which Pool asks
//! of each member at a pick: `hasRoom` answers whether the row has room in
//! every meter now, each read as `checkReserve` reads it for an amount of 1
//! (used, the provider's figure while an observation holds, plus reserved,
//! less an ended lease's own reservation, plus 1, within the limit), and when
//! not, `until`, the earliest instant at which the clock alone gives every
//! meter room (a period's start, an observation's lapse, a bucket leaving a
//! rolling window), absent when none does. A scope's meters are not read: a
//! scope's room is shared by every row inside it, so it tells no member from
//! another. It is a pure answer over the row and its `meters` and `buckets`
//! as a read across the table gave them ([`Behavior::answer`]).
//!
//! `setLimit` of a meter whose limit an own field holds (`limitField`) is
//! refused: the core writes no own field from an operation (D103), so on a
//! table the limit is set by an update of the field, which the guard holds
//! to `limitPermission` and above what is used and reserved.
//!
//! Every refusal is a veto with a code the declaration lists, but a
//! permission the caller lacks, which is forbidden, and a parameter that
//! names what the config does not give, which is invalid_argument.
//!
//! configChange: a meter cannot be removed while the type has rows; limits,
//! reservations, scopes, resets and windows may change, each at a row's
//! next write (a fixed reset starts its period then; a rolling meter's
//! buckets of the old width count by their start). Budget can be added to a
//! type that has rows and cannot be removed from one.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, in the schema loader's words
//! ([`ConfigError::Schema`]), held to the file by this crate's tests, then
//! each meter held to the table's fields and relations, onExceeded to Lease
//! and escalate to the Workflow, in the engine's words. The rules across
//! types the engine held at define (a scope's target implements Budget with
//! the meter) are the build's: the core's define check reads no other type.
//! Messages name a row `{Type} {key}` where the engine named `{schema}
//! {id}`.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    key_of, Behavior, Change, Code, ConfigCx, ConfigError, ConfigTarget, ContractQuery, Cx,
    Declaration, GuardRequest, Hook, Refusal, Row, SchemaIssue, Stop, SubRow, Veto, View,
};

use crate::capacity::{self, Room};
use crate::declared;
use crate::moves::{self, StatusMove};
use crate::periods;
use crate::schema::{self, Issues};
use crate::values;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Budget";

/// Budget's declaration, the file the compiler registers for the work
/// queue and the engine embeds, read in place.
pub const DECLARATION: &str =
    include_str!("../../../../engine-workqueue/rust/declarations/Budget.behavior.json");

/// The sub-table of a row's meters, keyed by the meter's name, and the
/// field a read computes from it.
pub const METERS: &str = "meters";

/// The sub-table of what a scope holds for each row inside it, keyed
/// `<meter>:<type>:<key>`.
pub const HOLDS: &str = "holds";

/// The sub-table of a rolling meter's usage, keyed `<meter>:<start, fifteen
/// digits>`.
pub const BUCKETS: &str = "buckets";

/// The operation that records a provider's usage (D104).
pub const OBSERVE: &str = "observe";

/// The most enclosing scopes checkReserve walks up a meter's chain (the
/// engine's).
pub const MAX_CHAIN: usize = 16;

/// How many buckets a rolling meter's period holds: a sixtieth of the
/// period each (D104).
pub const BUCKETS_PER_PERIOD: i64 = 60;

/// How many buckets a rolling window counts: every one that overlaps the
/// last period, the current one and the sixty before it, so the usage of
/// any period's length that the window let through is within the limit,
/// which sixty would not hold: the oldest of them would leave as much as a
/// bucket before its usage was a period old.
pub const BUCKETS_COUNTED: i64 = BUCKETS_PER_PERIOD + 1;

/// The IR types a limitField or reserveField may have: an integer scalar,
/// its name's last segment, lowercased, int, int32, int64 or integer (the
/// engine's integer trait), held to the build's by its test.
pub const INTEGER_SEGMENTS: [&str; 4] = ["int", "int32", "int64", "integer"];

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 6] = [
    "escalate",
    "limitPermission",
    "meters",
    "observePermission",
    "onExceeded",
    "status",
];
const METER_KEYS: [&str; 7] = [
    "limit",
    "limitField",
    "reserve",
    "reserveField",
    "reset",
    "scope",
    "window",
];
const MOVE_KEYS: [&str; 2] = ["from", "transition"];
const RESETS: [&str; 4] = ["hourly", "daily", "weekly", "monthly"];
const METER_PATTERN: &str = "^[a-z][A-Za-z0-9]{0,63}$";
const DIRECT_PATTERN: &str = "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$";

// The largest integer a JavaScript number holds exactly.
const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

// The behavior whose lease a reservation lives within, and its fields and
// operation Budget reads and calls (D104).
const LEASE: &str = "Lease";
const LEASE_HOLDER: &str = "holder";
const LEASE_EXPIRES_AT: &str = "expiresAt";
const LEASE_TOKEN: &str = "token";
const LEASE_DIRECT: &str = "direct";

/// When a meter's used starts again at 0, in UTC (D104).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BudgetReset {
    Hourly,
    Daily,
    Weekly,
    Monthly,
    /// At each multiple of a length from the epoch, in milliseconds.
    Every(i64),
}

impl BudgetReset {
    /// A reset as the configSchema lets it through.
    fn parse(raw: &Value) -> Option<BudgetReset> {
        match raw {
            Value::String(name) => match name.as_str() {
                "hourly" => Some(BudgetReset::Hourly),
                "daily" => Some(BudgetReset::Daily),
                "weekly" => Some(BudgetReset::Weekly),
                "monthly" => Some(BudgetReset::Monthly),
                _ => None,
            },
            Value::Object(members) => members
                .get("everyMs")
                .and_then(integer)
                .filter(|ms| *ms > 0)
                .map(BudgetReset::Every),
            _ => None,
        }
    }

    /// The start, in UTC, of the period an instant falls in: the hour, the
    /// day, the Monday, the first of the month, or the multiple of everyMs
    /// from the epoch.
    pub fn period_start(&self, at: i64) -> i64 {
        match self {
            BudgetReset::Hourly => periods::hour_start(at),
            BudgetReset::Daily => periods::day_start(at),
            BudgetReset::Weekly => periods::week_start(at),
            BudgetReset::Monthly => periods::month_start(at),
            BudgetReset::Every(ms) => periods::start_of(at, *ms, 0),
        }
    }

    /// The start of the period after the one an instant falls in.
    pub fn next_start(&self, at: i64) -> i64 {
        match self {
            BudgetReset::Hourly => periods::hour_start(at) + periods::HOUR_MS,
            BudgetReset::Daily => periods::day_start(at) + periods::DAY_MS,
            BudgetReset::Weekly => periods::week_start(at) + periods::WEEK_MS,
            BudgetReset::Monthly => periods::next_month_start(at),
            BudgetReset::Every(ms) => periods::start_of(at, *ms, 0) + ms,
        }
    }

    /// A rolling window's length: 1 h, 1 d, 7 d, 30 d, everyMs.
    pub fn window_ms(&self) -> i64 {
        match self {
            BudgetReset::Hourly => periods::HOUR_MS,
            BudgetReset::Daily => periods::DAY_MS,
            BudgetReset::Weekly => periods::WEEK_MS,
            BudgetReset::Monthly => periods::MONTH_WINDOW_MS,
            BudgetReset::Every(ms) => *ms,
        }
    }

    /// A rolling window's bucket: window_ms / 60.
    pub fn bucket_ms(&self) -> i64 {
        (self.window_ms() / BUCKETS_PER_PERIOD).max(1)
    }
}

/// A meter's enclosing scope: the to-one relation the config names
/// (`workspace`), the own field that holds its key (`workspaceId`), and its
/// target type (`Workspace`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BudgetScope {
    pub relation: String,
    pub field: String,
    pub target: String,
}

/// One meter of a Budget config, parsed.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BudgetMeter {
    pub limit: Option<i64>,
    pub limit_field: Option<String>,
    pub reserve: Option<i64>,
    pub reserve_field: Option<String>,
    pub scope: Option<BudgetScope>,
    pub reset: Option<BudgetReset>,
    /// Whether it counts the last period from its buckets (D104).
    pub rolling: bool,
}

impl BudgetMeter {
    /// A meter as another type's raw config gives it (a scope's), its
    /// scope's relation alone, unchecked.
    pub fn from_raw(raw: &Value) -> BudgetMeter {
        let text = |key: &str| raw.get(key).and_then(Value::as_str).map(str::to_owned);
        BudgetMeter {
            limit: raw.get("limit").and_then(integer),
            limit_field: text("limitField"),
            reserve: raw.get("reserve").and_then(integer),
            reserve_field: text("reserveField"),
            scope: text("scope").map(|relation| BudgetScope {
                relation,
                field: String::new(),
                target: String::new(),
            }),
            reset: raw.get("reset").and_then(BudgetReset::parse),
            rolling: raw.get("window").and_then(Value::as_str) == Some("rolling"),
        }
    }

    // fixed reports whether the meter starts its used again at each period:
    // a reset and no rolling window.
    fn fixed(&self) -> Option<BudgetReset> {
        self.reset.filter(|_| !self.rolling)
    }

    // window is a rolling meter's bucket length, when it rolls.
    fn window(&self) -> Option<i64> {
        self.reset
            .filter(|_| self.rolling)
            .map(|reset| reset.bucket_ms())
    }
}

/// Budget's config, parsed.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BudgetConfig {
    /// The meters, by name, in name order (by code point), as the engine's
    /// canonical document held a config's members.
    pub meters: Vec<(String, BudgetMeter)>,
    pub limit_permission: Option<String>,
    pub observe_permission: Option<String>,
    /// onExceeded's directive name.
    pub on_exceeded: Option<String>,
    pub escalate: Option<StatusMove>,
    /// The Workflow use escalate moves; None without escalate and status.
    pub status: Option<String>,
    /// The type's Lease use, when it implements Lease: a reservation lives
    /// within its active lease.
    pub lease: Option<String>,
}

impl BudgetConfig {
    /// A meter by name; `None` for a name the config does not give.
    pub fn meter(&self, name: &str) -> Option<&BudgetMeter> {
        self.meters
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, meter)| meter)
    }

    /// The meters' names, in name order.
    pub fn names(&self) -> Vec<&str> {
        self.meters.iter().map(|(name, _)| name.as_str()).collect()
    }

    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then each meter held to the type's fields and relations,
    /// onExceeded to Lease and escalate to the Workflow, a rule's reason in
    /// the engine's words, which the core prefixes with the type and the
    /// behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<BudgetConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        // In name order, as the engine's canonical document held a config's
        // members, so a config reads alike however its host ordered them.
        let mut given: Vec<(&String, &Value)> = raw
            .get("meters")
            .and_then(Value::as_object)
            .into_iter()
            .flatten()
            .collect();
        given.sort_by(|a, b| a.0.cmp(b.0));
        let mut meters = Vec::new();
        for (name, given) in given {
            let mut meter = BudgetMeter::from_raw(given);
            if let Some(field) = &meter.limit_field {
                check_integer_field(target, &format!("meter {name}: limitField"), field)?;
            }
            if let Some(field) = &meter.reserve_field {
                check_integer_field(target, &format!("meter {name}: reserveField"), field)?;
            }
            if let Some(scope) = &meter.scope {
                meter.scope = Some(scope_relation(target, name, &scope.relation)?);
            }
            if meter.rolling && meter.reset.is_none() {
                return Err(format!(
                    "meter {name}: window rolling counts the last period, and the meter names no reset to give it one"
                )
                .into());
            }
            meters.push((name.clone(), meter));
        }
        let lease = target
            .uses()
            .iter()
            .find(|entry| entry.behavior == LEASE)
            .map(|entry| entry.id().to_owned());
        if raw.get("onExceeded").is_some() && lease.is_none() {
            return Err(
                "onExceeded sends a directive through Lease's direct, which the type does not list"
                    .into(),
            );
        }
        let escalate = raw.get("escalate").map(StatusMove::parse);
        let named = raw.get("status").and_then(Value::as_str);
        let mut status = None;
        if escalate.is_some() || named.is_some() {
            match moves::flow_for(target, named)? {
                None => {
                    if escalate.is_some() {
                        return Err(moves::no_workflow("escalate"));
                    }
                }
                Some(flow) => {
                    if let Some(rule) = &escalate {
                        moves::check_move(&flow, "escalate", rule)?;
                    }
                    status = Some(flow.use_id);
                }
            }
        }
        let text = |key: &str| raw.get(key).and_then(Value::as_str).map(str::to_owned);
        Ok(BudgetConfig {
            meters,
            limit_permission: text("limitPermission"),
            observe_permission: text("observePermission"),
            on_exceeded: raw
                .get("onExceeded")
                .and_then(|given| given.get("direct"))
                .and_then(Value::as_str)
                .map(str::to_owned),
            escalate,
            status,
            lease,
        })
    }
}

// check_integer_field holds limitField and reserveField to an own field of
// the type of an integer type.
fn check_integer_field(
    target: &ConfigTarget<'_>,
    at: &str,
    field: &str,
) -> Result<(), ConfigError> {
    let Some(found) = target.fields().iter().find(|known| known.name == field) else {
        let names: Vec<&str> = target
            .fields()
            .iter()
            .map(|known| known.name.as_str())
            .collect();
        return Err(format!(
            "{at} \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            names.join(", ")
        )
        .into());
    };
    if !is_integer_type(&found.type_name) {
        return Err(format!(
            "{at} \"{field}\" is not an integer field of {}",
            target.type_name()
        )
        .into());
    }
    Ok(())
}

/// Whether an IR type is an integer scalar: its name's last segment,
/// lowercased, is one of [`INTEGER_SEGMENTS`].
pub fn is_integer_type(type_name: &str) -> bool {
    let last = type_name.rsplit('.').next().unwrap_or(type_name);
    INTEGER_SEGMENTS.contains(&last.to_lowercase().as_str())
}

// scope_relation holds a meter's scope to a to-one relation of the type,
// the own field that holds its key, and its target.
fn scope_relation(
    target: &ConfigTarget<'_>,
    meter: &str,
    relation: &str,
) -> Result<BudgetScope, ConfigError> {
    let found = target.fields().iter().find_map(|field| {
        field
            .relation
            .as_ref()
            .filter(|known| known.name == relation)
            .map(|known| (field, known))
    });
    match found {
        Some((field, known)) => Ok(BudgetScope {
            relation: relation.to_owned(),
            field: field.name.clone(),
            target: known.type_name.clone(),
        }),
        None => {
            let names: Vec<&str> = target
                .fields()
                .iter()
                .filter_map(|field| field.relation.as_ref().map(|known| known.name.as_str()))
                .collect();
            Err(format!(
                "meter {meter}: scope {relation} is not a to-one relation of {} (its relations: {})",
                target.type_name(),
                if names.is_empty() {
                    "none".to_owned()
                } else {
                    names.join(", ")
                }
            )
            .into())
        }
    }
}

/// Budget: reserve-then-settle meters on a row and its enclosing scopes.
#[derive(Clone, Copy, Debug, Default)]
pub struct Budget;

impl Behavior for Budget {
    type Config = BudgetConfig;

    // The declaration file names its three sub-tables, and its meters field
    // a read computes (D103, D104).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<BudgetConfig, ConfigError> {
        BudgetConfig::parse(raw, target)
    }

    // A meter stays while the type has rows; Budget joins a type with rows
    // and cannot leave one.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&BudgetConfig>,
        after: Option<&BudgetConfig>,
    ) -> Hook<Option<String>> {
        let Some(before) = before else {
            return Ok(None);
        };
        let Some(after) = after else {
            return Ok(Some(
                "the budgets its instances hold, and what enclosing scopes hold for them, would stay behind"
                    .to_owned(),
            ));
        };
        let gone = before
            .names()
            .into_iter()
            .find(|name| after.meter(name).is_none());
        let Some(gone) = gone else {
            return Ok(None);
        };
        if !cx.has_rows()? {
            return Ok(None);
        }
        Ok(Some(format!(
            "meter {gone} is gone, and its instances and the scopes they draw on may hold it"
        )))
    }

    fn check_params(
        &self,
        _config: &BudgetConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        params_issues(operation, params).into_issues()
    }

    // guard keeps a scope relation put while a reservation is held through
    // it, and a limitField to limitPermission and above what is committed.
    fn guard(
        &self,
        view: &View<'_, BudgetConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let GuardRequest::Update { patch, after } = request else {
            return Ok(None);
        };
        let config = view.config();
        for (name, spec) in &config.meters {
            let Some(scope) = &spec.scope else {
                continue;
            };
            if !patch.contains_key(&scope.field)
                || values::equal(
                    view.own_field(&scope.field).as_ref(),
                    after.get(&scope.field),
                )
            {
                continue;
            }
            let reserved = meter_row(&view.sub_rows(METERS)?, name).reserved;
            if reserved > 0 {
                let mut details = Map::new();
                details.insert("meter".into(), Value::from(name.as_str()));
                details.insert("reserved".into(), Value::from(reserved));
                return Ok(Some(
                    Veto::new(format!(
                        "meter {name} has {reserved} reserved through {}; settle it before {} changes",
                        scope.relation, scope.field
                    ))
                    .with_code("scope_reserved")
                    .with_details(details),
                ));
            }
        }
        for (name, spec) in &config.meters {
            let Some(field) = &spec.limit_field else {
                continue;
            };
            if !patch.contains_key(field) {
                continue;
            }
            let Some(permission) = &config.limit_permission else {
                return Ok(Some(
                    Veto::new(format!(
                        "{field} holds the limit of meter {name}, which changes only with limitPermission, and the config names none"
                    ))
                    .with_code("not_configured"),
                ));
            };
            if !view.can(permission)? {
                return Err(view.forbidden(format!(
                    "{} may not change {field}, the limit of meter {name}, of {}: it needs permission {permission}",
                    view.caller().subject,
                    target(view)
                )));
            }
            let limit = count_of(after.get(field)).or(spec.limit);
            let state = state_of(view)?;
            let row = meter_row(&state.meters, name);
            let committed = used_at(spec, name, &row, &state.buckets, view.now()) + row.reserved;
            if let Some(limit) = limit.filter(|limit| *limit < committed) {
                return Ok(Some(below_committed(name, committed, limit)));
            }
        }
        Ok(None)
    }

    fn operation(&self, cx: &Cx<'_, BudgetConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            "reserve" => reserve(cx, params),
            "checkReserve" => {
                let plan = plan_of(cx, "checkReserve", params)?;
                check_reserve(cx, &plan)
            }
            "recordUsage" => record_usage(cx, params),
            "settle" => settle(cx, params),
            "setLimit" => set_limit(cx, params),
            OBSERVE => observe(cx, params),
            "reserveFor" => reserve_for(cx, params),
            "settleFor" => settle_for(cx, params),
            "recordUsageFor" => record_usage_for(cx, params),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // A delete passes everything the row reserved back to where it is held,
    // and the scope reads the row as gone; a Lease operation or deadline
    // settles what a lease that is no longer the active one reserved.
    fn after_change(&self, cx: &Cx<'_, BudgetConfig>, change: &Change<'_>) -> Hook<()> {
        match change {
            Change::Delete => {
                for row in cx.sub_rows(METERS)? {
                    let meter = Meter::of(&row);
                    if meter.reserved > 0 {
                        pass_up(
                            cx,
                            &row.key,
                            "settleFor",
                            &[("amount", meter.reserved)],
                            meter.scope.as_ref(),
                        )?;
                    }
                }
                Ok(())
            }
            Change::Operation { use_id, .. } | Change::Deadline { use_id, .. }
                if cx.config().lease.as_deref() == Some(*use_id) =>
            {
                settle_ended(cx)
            }
            _ => Ok(()),
        }
    }

    // A read of the row shows each meter of the config at the read's time,
    // as the engine's Budget.meters field: { used, reserved, limit,
    // remaining }, limit and remaining null without a limit. Without the
    // time it shows nothing: what a meter counts turns on the clock.
    fn read_fields_at(
        &self,
        config: &BudgetConfig,
        row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
        now: i64,
    ) -> Map<String, Value> {
        let rows = tables.get(METERS).map_or(&[][..], Vec::as_slice);
        let buckets = tables.get(BUCKETS).map_or(&[][..], Vec::as_slice);
        let mut meters = Map::new();
        for (name, spec) in &config.meters {
            let meter = meter_row(rows, name);
            meters.insert(
                name.clone(),
                record_of(spec, name, &meter, buckets, &row.own, now),
            );
        }
        let mut fields = Map::new();
        fields.insert(METERS.into(), Value::Object(meters));
        fields
    }

    // Capacity's hasRoom, over the row's meters and buckets at the time:
    // see the module's header. Any other query it answers none to.
    fn answer(
        &self,
        config: &BudgetConfig,
        query: &ContractQuery<'_>,
        row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
        now: i64,
    ) -> Option<Value> {
        if query.contract != capacity::NAME || query.query != capacity::HAS_ROOM {
            return None;
        }
        Some(room_of(config, row, tables, now).to_json())
    }
}

// room_of is whether a row has room in every meter of the config at a time,
// each read as checkReserve reads it for an amount of 1, and when not, the
// earliest instant at which the clock alone gives every meter room.
fn room_of(
    config: &BudgetConfig,
    row: &Row,
    tables: &BTreeMap<String, Vec<SubRow>>,
    now: i64,
) -> Room {
    let meters = tables.get(METERS).map_or(&[][..], Vec::as_slice);
    let buckets = tables.get(BUCKETS).map_or(&[][..], Vec::as_slice);
    let active = active_token(lease_state_of(config, row, now).as_ref());
    let readings: Vec<Reading> = config
        .meters
        .iter()
        .map(|(name, spec)| {
            let meter = meter_row(meters, name);
            let stale = if config.lease.is_some()
                && meter.reservation > 0
                && !same_token(meter.token, active)
            {
                meter.reservation
            } else {
                0
            };
            Reading {
                spec: spec.clone(),
                meter: name.clone(),
                reserved: meter.reserved - stale,
                row: meter,
                buckets: buckets.to_vec(),
                own: row.own.clone(),
                amount: 1,
            }
        })
        .collect();
    if readings.iter().all(|reading| reading.fits(now)) {
        return Room::OPEN;
    }
    let mut instants: Vec<i64> = readings
        .iter()
        .flat_map(|reading| reading.moves(now))
        .collect();
    instants.sort_unstable();
    instants.dedup();
    Room {
        room: false,
        until: instants
            .into_iter()
            .find(|at| readings.iter().all(|reading| reading.fits(*at))),
    }
}

// ---- a meter's state ----

/// A row, by type and key.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Target {
    type_name: String,
    key: String,
}

impl Target {
    fn named(&self) -> String {
        format!("{} {}", self.type_name, self.key)
    }
}

/// A meter's row of the `meters` sub-table, or the values of one not
/// written yet.
#[derive(Clone, Debug, Default)]
struct Meter {
    used: i64,
    reserved: i64,
    limit_value: Option<i64>,
    period_start: Option<i64>,
    /// The row's own reservation, a part of reserved.
    reservation: i64,
    /// The lease token the own reservation was made under.
    token: Option<i64>,
    /// The lease token onExceeded's directive was last sent under.
    directed: Option<i64>,
    /// Where the reservation is held up the chain.
    scope: Option<Target>,
    observed: Option<i64>,
    observed_limit: Option<i64>,
    observed_at: Option<i64>,
    observed_until: Option<i64>,
}

impl Meter {
    fn of(row: &SubRow) -> Meter {
        let number = |name: &str| row.value(name).and_then(integer);
        let text = |name: &str| row.value(name).and_then(Value::as_str).map(str::to_owned);
        Meter {
            used: number("used").unwrap_or(0),
            reserved: number("reserved").unwrap_or(0),
            limit_value: number("limitValue"),
            period_start: number("periodStart"),
            reservation: number("reservation").unwrap_or(0),
            token: number("token"),
            directed: number("directed"),
            scope: text("scopeType").map(|type_name| Target {
                type_name,
                key: text("scopeKey").unwrap_or_default(),
            }),
            observed: number("observed"),
            observed_limit: number("observedLimit"),
            observed_at: number("observedAt"),
            observed_until: number("observedUntil"),
        }
    }

    // holds reports whether the row's observation holds at a time: until
    // its resetsAt, else while the meter's period is the one it was made
    // in, else (no reset) until the next observe.
    fn holds(&self, spec: &BudgetMeter, at: i64) -> bool {
        let Some(observed_at) = self.observed_at else {
            return false;
        };
        match (self.observed_until, spec.reset) {
            (Some(until), _) => at < until,
            (None, Some(reset)) => reset.period_start(at) == reset.period_start(observed_at),
            (None, None) => true,
        }
    }

    // lapse is when the row's observation stops holding: its resetsAt,
    // else the meter's next period start after it; None for one that holds
    // until the next observe.
    fn lapse(&self, spec: &BudgetMeter) -> Option<i64> {
        let observed_at = self.observed_at?;
        self.observed_until
            .or_else(|| spec.reset.map(|reset| reset.next_start(observed_at)))
    }
}

/// A row's own Budget state as the run has it: its meters' rows and its
/// rolling meters' buckets.
struct State {
    meters: Vec<SubRow>,
    buckets: Vec<SubRow>,
}

fn state_of<C>(view: &View<'_, C>) -> Hook<State> {
    Ok(State {
        meters: view.sub_rows(METERS)?,
        buckets: view.sub_rows(BUCKETS)?,
    })
}

// meter_row is a meter's row among a row's meters, or the zeros of one not
// written yet.
fn meter_row(rows: &[SubRow], meter: &str) -> Meter {
    rows.iter()
        .find(|row| row.key == meter)
        .map(Meter::of)
        .unwrap_or_default()
}

// own_count is what a meter counts of its own at a time: a rolling meter's
// buckets that overlap the last period, the one the time falls in and the
// sixty before it (BUCKETS_COUNTED); a fixed
// reset's used, 0 once its period has passed (a row with no periodStart
// counting as not rolled); a meter with no reset, its used.
fn own_count(spec: &BudgetMeter, meter: &str, row: &Meter, buckets: &[SubRow], at: i64) -> i64 {
    if let Some(bucket) = spec.window() {
        return buckets_of(buckets, meter)
            .filter(|(starts_at, _)| starts_at + BUCKETS_COUNTED * bucket > at)
            .map(|(_, used)| used)
            .sum();
    }
    match spec.fixed() {
        Some(reset)
            if row
                .period_start
                .is_some_and(|start| start < reset.period_start(at)) =>
        {
            0
        }
        _ => row.used,
    }
}

// buckets_of is a meter's buckets: each one's start and its usage.
fn buckets_of<'r>(buckets: &'r [SubRow], meter: &'r str) -> impl Iterator<Item = (i64, i64)> + 'r {
    buckets
        .iter()
        .filter(move |row| row.value("meter").and_then(Value::as_str) == Some(meter))
        .filter_map(|row| {
            Some((
                row.value("startsAt").and_then(integer)?,
                row.value("used").and_then(integer).unwrap_or(0),
            ))
        })
}

// used_at is a meter's used at a time: the provider's figure while its
// observation holds, else its own count.
fn used_at(spec: &BudgetMeter, meter: &str, row: &Meter, buckets: &[SubRow], at: i64) -> i64 {
    if row.holds(spec, at) {
        row.observed.unwrap_or(0)
    } else {
        own_count(spec, meter, row, buckets, at)
    }
}

/// The one rule for a meter's limit on a row: its limitField while it
/// holds a count, else the config's; without limitField, setLimit's, else
/// the config's; `None` for none.
fn limit_of(spec: &BudgetMeter, row: &Meter, own: &Map<String, Value>) -> Option<i64> {
    if let Some(field) = &spec.limit_field {
        return count_of(own.get(field)).or(spec.limit);
    }
    row.limit_value.or(spec.limit)
}

// limit_at is a meter's limit at a time: the provider's while its
// observation holds and gave one, else the meter's own.
fn limit_at(spec: &BudgetMeter, row: &Meter, own: &Map<String, Value>, at: i64) -> Option<i64> {
    match row.observed_limit {
        Some(limit) if row.holds(spec, at) => Some(limit),
        _ => limit_of(spec, row, own),
    }
}

// record_of is a meter as a read shows it at a time.
fn record_of(
    spec: &BudgetMeter,
    meter: &str,
    row: &Meter,
    buckets: &[SubRow],
    own: &Map<String, Value>,
    at: i64,
) -> Value {
    let used = used_at(spec, meter, row, buckets, at);
    let limit = limit_at(spec, row, own, at);
    json!({
        "used": used,
        "reserved": row.reserved,
        "limit": limit,
        "remaining": limit.map(|limit| limit - used - row.reserved),
    })
}

// spec_of is the spec of a meter the config gives, which every caller
// checked; another is a defect.
fn spec_of<'c>(view: &View<'c, BudgetConfig>, meter: &str) -> Hook<&'c BudgetMeter> {
    view.config()
        .meter(meter)
        .ok_or_else(|| view.defect(format!("meter {meter} is not one of its config's")))
}

// known_meter returns a meter's spec, or refuses a name the config does
// not give.
fn known_meter<'c>(
    view: &View<'c, BudgetConfig>,
    operation: &str,
    meter: &str,
) -> Hook<&'c BudgetMeter> {
    view.config().meter(meter).ok_or_else(|| {
        params_error(
            view,
            operation,
            "/meter",
            format!(
                "{} has no meter {meter} (its meters: {})",
                view.type_name(),
                view.config().names().join(", ")
            ),
        )
    })
}

// load reads a meter's row for a write, making it when the row has none
// yet and starting a fixed reset's new period first: the one place a row is
// made or its period moves, so a read never writes.
fn load(cx: &Cx<'_, BudgetConfig>, meter: &str) -> Hook<Meter> {
    let spec = spec_of(cx, meter)?;
    let rows = cx.sub_rows(METERS)?;
    let start = spec.fixed().map(|reset| reset.period_start(cx.now()));
    let Some(found) = rows.iter().find(|row| row.key == meter) else {
        let mut values = Map::new();
        values.insert("used".into(), Value::from(0));
        values.insert("reserved".into(), Value::from(0));
        values.insert("reservation".into(), Value::from(0));
        if let Some(start) = start {
            values.insert("periodStart".into(), Value::from(start));
        }
        cx.insert_row(METERS, meter, values)?;
        return Ok(Meter {
            period_start: start,
            ..Meter::default()
        });
    };
    let row = Meter::of(found);
    match start {
        Some(start) if row.period_start.is_none_or(|kept| kept < start) => {
            let used = if row.period_start.is_none() {
                row.used
            } else {
                0
            };
            save(
                cx,
                meter,
                &[("used", used.into()), ("periodStart", start.into())],
            )?;
            Ok(Meter {
                used,
                period_start: Some(start),
                ..row
            })
        }
        _ => Ok(row),
    }
}

// save sets values of a meter's row; null clears one.
fn save(cx: &Cx<'_, BudgetConfig>, meter: &str, values: &[(&str, Value)]) -> Hook<()> {
    let values: Map<String, Value> = values
        .iter()
        .map(|(name, value)| ((*name).to_owned(), value.clone()))
        .collect();
    cx.update_row(METERS, meter, values)
}

// bucket adds usage to a rolling meter's current bucket and deletes its
// buckets that left the window.
fn bucket(cx: &Cx<'_, BudgetConfig>, meter: &str, length: i64, amount: i64) -> Hook<()> {
    let now = cx.now();
    let starts_at = now.div_euclid(length) * length;
    let key = format!("{meter}:{starts_at:015}");
    let buckets = cx.sub_rows(BUCKETS)?;
    match buckets.iter().find(|row| row.key == key) {
        Some(row) => {
            let used = row.value("used").and_then(integer).unwrap_or(0);
            let mut values = Map::new();
            values.insert("used".into(), Value::from(used + amount));
            cx.update_row(BUCKETS, &key, values)?;
        }
        None => {
            let mut values = Map::new();
            values.insert("meter".into(), Value::from(meter));
            values.insert("startsAt".into(), Value::from(starts_at));
            values.insert("used".into(), Value::from(amount));
            cx.insert_row(BUCKETS, &key, values)?;
        }
    }
    for row in &buckets {
        let departed = row.value("meter").and_then(Value::as_str) == Some(meter)
            && row
                .value("startsAt")
                .and_then(integer)
                .is_some_and(|start| start + BUCKETS_COUNTED * length <= now);
        if departed {
            cx.delete_row(BUCKETS, &row.key)?;
        }
    }
    Ok(())
}

// claim_amount is what reserve takes of a meter when it is given no meter:
// the row's reserveField when it holds a positive integer, else the
// config's reserve.
fn claim_amount<C>(view: &View<'_, C>, spec: &BudgetMeter) -> Option<i64> {
    spec.reserve_field
        .as_ref()
        .and_then(|field| count_of(view.own_field(field).as_ref()))
        .filter(|amount| *amount > 0)
        .or(spec.reserve)
}

// plan_of is what reserve and checkReserve take, by meter: one meter's
// amount, or every meter's claim amount.
fn plan_of(
    view: &View<'_, BudgetConfig>,
    operation: &str,
    params: &Value,
) -> Hook<Vec<(String, i64)>> {
    let amount = params.get("amount").and_then(integer);
    let Some(meter) = params.get("meter").and_then(Value::as_str) else {
        if amount.is_some() {
            return Err(params_error(
                view,
                operation,
                "/amount",
                "an amount is reserved of one meter, which meter names".to_owned(),
            ));
        }
        return Ok(view
            .config()
            .meters
            .iter()
            .filter_map(|(name, spec)| Some((name.clone(), claim_amount(view, spec)?)))
            .collect());
    };
    let spec = known_meter(view, operation, meter)?;
    let wanted = match amount {
        Some(amount) => amount,
        None => claim_amount(view, spec).ok_or_else(|| {
            params_error(
                view,
                operation,
                "/amount",
                format!("meter {meter} has no configured reservation, so reserve takes an amount"),
            )
        })?,
    };
    Ok(vec![(meter.to_owned(), wanted)])
}

// scope_of is where a meter's scope relation points now: its target type
// and the key its field holds; None when the meter has no scope or the
// field holds no key.
fn scope_of<C>(view: &View<'_, C>, spec: &BudgetMeter) -> Option<Target> {
    let scope = spec.scope.as_ref()?;
    let key = key_of(&view.own_field(&scope.field)?)?;
    Some(Target {
        type_name: scope.target.clone(),
        key,
    })
}

// budget_use is the id of a type's Budget use: what a scope operation is
// called on and a row's meters are read under. A type with none answers
// Budget's name, whose call the core refuses as no use of it.
fn budget_use<C>(view: &View<'_, C>, type_name: &str) -> Hook<String> {
    Ok(view
        .read_spec(type_name)?
        .and_then(|spec| spec.uses.iter().find(|entry| entry.behavior == NAME))
        .map_or_else(|| NAME.to_owned(), |entry| entry.id().to_owned()))
}

/// The row's lease, read by Lease's rule from its owned fields (D104):
/// active while its holder holds a subject and the clock is before its
/// expiry; its token 0 when it has none.
struct LeaseState {
    token: i64,
    active: bool,
}

// lease_of reads the lease of the row the view runs on, as the run has it;
// None on a type without Lease.
fn lease_of(view: &View<'_, BudgetConfig>) -> Option<LeaseState> {
    let lease = view.config().lease.as_deref()?;
    Some(lease_state(|field| view.get_of(lease, field), view.now()))
}

// lease_state_of reads the lease of a row as a read gave it; None on a type
// without Lease.
fn lease_state_of(config: &BudgetConfig, row: &Row, now: i64) -> Option<LeaseState> {
    let lease = config.lease.as_deref()?;
    Some(lease_state(|field| row.owned(lease, field).cloned(), now))
}

// lease_state is Lease's rule over its owned fields at a time: active while
// its holder holds a subject and the time is before its expiry.
fn lease_state(get: impl Fn(&str) -> Option<Value>, now: i64) -> LeaseState {
    let holder = matches!(
        get(LEASE_HOLDER),
        Some(Value::String(holder)) if !holder.is_empty()
    );
    let expires_at = get(LEASE_EXPIRES_AT).as_ref().and_then(integer);
    LeaseState {
        token: get(LEASE_TOKEN).as_ref().and_then(integer).unwrap_or(0),
        active: holder && expires_at.is_some_and(|at| now < at),
    }
}

// active_token is the token of the active lease; None when no lease is
// active.
fn active_token(lease: Option<&LeaseState>) -> Option<i64> {
    lease.filter(|lease| lease.active).map(|lease| lease.token)
}

// same_token is whether a token a row keeps is the one given: a row's
// null is no token's.
fn same_token(kept: Option<i64>, token: Option<i64>) -> bool {
    matches!((kept, token), (Some(kept), Some(token)) if kept == token)
}

// ---- the writes ----

// pass_up hands a change of a meter on to a scope, as an operation of the
// scope's Budget that names this row (call_on): to where the reservation
// is held for a release, to where the scope relation points now otherwise.
// Nothing without a scope.
fn pass_up(
    cx: &Cx<'_, BudgetConfig>,
    meter: &str,
    operation: &str,
    params: &[(&str, i64)],
    target: Option<&Target>,
) -> Hook<Option<Value>> {
    let Some(target) = target else {
        return Ok(None);
    };
    let mut out = Map::new();
    out.insert("meter".into(), Value::from(meter));
    out.insert("schema".into(), Value::from(cx.type_name()));
    out.insert("id".into(), Value::from(cx.key().unwrap_or_default()));
    for (name, value) in params {
        out.insert((*name).to_owned(), Value::from(*value));
    }
    let use_id = budget_use(cx, &target.type_name)?;
    cx.call_on(
        &target.type_name,
        &target.key,
        &use_id,
        operation,
        &Value::Object(out),
    )
    .map(Some)
}

// take reserves an amount of a meter here and up the chain. Everything the
// row has reserved is held at one scope: a reservation through another,
// after the relation or a new version moved the meter's scope, waits until
// what is held at the first is settled. `own` is the own reservation's
// lease token, for reserve's; None for a scope's reserveFor. A refusal is
// the veto of the operation the take runs for.
fn take(
    cx: &Cx<'_, BudgetConfig>,
    meter: &str,
    amount: i64,
    own: Option<Option<i64>>,
) -> Hook<Meter> {
    let spec = spec_of(cx, meter)?;
    let row = load(cx, meter)?;
    let state = state_of(cx)?;
    let used = used_at(spec, meter, &row, &state.buckets, cx.now());
    if let Some(limit) = limit_at(spec, &row, &cx.own(), cx.now()) {
        if used + row.reserved + amount > limit {
            let remaining = (limit - used - row.reserved).max(0);
            return Err(cx.veto(
                Veto::new(format!(
                    "meter {meter} has {remaining} of its limit {limit} left, not {amount}"
                ))
                .with_code("over_limit")
                .with_details(object(json!({
                    "meter": meter,
                    "amount": amount,
                    "remaining": remaining,
                    "limit": limit,
                    "scope": {"schema": cx.type_name(), "id": cx.key().unwrap_or_default()},
                }))),
            ));
        }
    }
    let scope = scope_of(cx, spec);
    if let (Some(scope), Some(held)) = (&scope, &row.scope) {
        if row.reserved > 0 && scope != held {
            return Err(cx.veto(
                Veto::new(format!(
                    "meter {meter} has {} reserved through {}; settle it before reserving through {}",
                    row.reserved,
                    held.named(),
                    scope.named()
                ))
                .with_code("scope_moved"),
            ));
        }
    }
    let mut values = vec![("reserved", Value::from(row.reserved + amount))];
    if let Some(token) = own {
        values.push(("reservation", Value::from(row.reservation + amount)));
        values.push(("token", token.map_or(Value::Null, Value::from)));
    }
    if let Some(scope) = &scope {
        values.push(("scopeType", Value::from(scope.type_name.as_str())));
        values.push(("scopeKey", Value::from(scope.key.as_str())));
    }
    save(cx, meter, &values)?;
    pass_up(
        cx,
        meter,
        "reserveFor",
        &[("amount", amount)],
        scope.as_ref(),
    )?;
    Ok(row)
}

// release releases an amount of reserved here, and passes it on to the
// scope that holds it.
fn release(cx: &Cx<'_, BudgetConfig>, meter: &str, amount: i64, own_part: i64) -> Hook<()> {
    if amount == 0 {
        return Ok(());
    }
    let row = load(cx, meter)?;
    let reservation = row.reservation - own_part;
    let mut values = vec![
        ("reserved", Value::from((row.reserved - amount).max(0))),
        ("reservation", Value::from(reservation)),
    ];
    if reservation == 0 {
        values.push(("token", Value::Null));
    }
    save(cx, meter, &values)?;
    pass_up(
        cx,
        meter,
        "settleFor",
        &[("amount", amount)],
        row.scope.as_ref(),
    )?;
    Ok(())
}

// settle_own releases the row's own reservation of a meter.
fn settle_own(cx: &Cx<'_, BudgetConfig>, meter: &str) -> Hook<i64> {
    let amount = meter_row(&cx.sub_rows(METERS)?, meter).reservation;
    release(cx, meter, amount, amount)?;
    Ok(amount)
}

// settle_ended settles the own reservations made under a lease that is no
// longer the active one, after a Lease operation or deadline and before a
// reserve.
fn settle_ended(cx: &Cx<'_, BudgetConfig>) -> Hook<()> {
    let outstanding: Vec<(String, Option<i64>)> = cx
        .sub_rows(METERS)?
        .iter()
        .map(|row| (row.key.clone(), Meter::of(row)))
        .filter(|(_, meter)| meter.reservation > 0)
        .map(|(name, meter)| (name, meter.token))
        .collect();
    if outstanding.is_empty() {
        return Ok(());
    }
    let active = active_token(lease_of(cx).as_ref());
    for (meter, token) in outstanding {
        if !same_token(token, active) && cx.config().meter(&meter).is_some() {
            settle_own(cx, &meter)?;
        }
    }
    Ok(())
}

// tolerated is whether a refusal of a call leaves the usage as it is: a
// veto, or a permission the caller lacks.
fn tolerated(stop: &Stop) -> bool {
    stop.refusal()
        .is_some_and(|refusal| matches!(refusal.code, Code::Vetoed | Code::Forbidden))
}

/// A row of a chain whose usage of a meter is over its limit.
#[derive(Clone, Debug)]
struct Overrun {
    schema: String,
    id: String,
    used: i64,
    limit: i64,
    escalated: bool,
}

impl Overrun {
    fn to_json(&self) -> Value {
        json!({
            "schema": self.schema,
            "id": self.id,
            "used": self.used,
            "limit": self.limit,
            "escalated": self.escalated,
        })
    }

    // of reads an overrun a scope's recordUsageFor returned.
    fn of(value: &Value) -> Option<Overrun> {
        Some(Overrun {
            schema: value.get("schema")?.as_str()?.to_owned(),
            id: value.get("id")?.as_str()?.to_owned(),
            used: value.get("used").and_then(integer)?,
            limit: value.get("limit").and_then(integer)?,
            escalated: value.get("escalated") == Some(&Value::Bool(true)),
        })
    }
}

// direct sends onExceeded's directive to the holder of the active lease,
// once per meter per lease token. A refusal of Lease's direct, a veto or a
// permission the caller lacks, sends none; usage is never refused.
fn direct(cx: &Cx<'_, BudgetConfig>, meter: &str, overrun: &Overrun) -> Hook<bool> {
    let (Some(name), Some(lease)) = (&cx.config().on_exceeded, &cx.config().lease) else {
        return Ok(false);
    };
    let Some(state) = lease_of(cx) else {
        return Ok(false);
    };
    let row = meter_row(&cx.sub_rows(METERS)?, meter);
    if !state.active || same_token(row.directed, Some(state.token)) {
        return Ok(false);
    }
    let sent = cx.call(
        lease,
        LEASE_DIRECT,
        &json!({
            "name": name,
            "data": {
                "meter": meter,
                "used": overrun.used,
                "limit": overrun.limit,
                "scope": {"schema": overrun.schema, "id": overrun.id},
            },
        }),
    );
    match sent {
        Ok(_) => {}
        Err(stop) if tolerated(&stop) => return Ok(false),
        Err(stop) => return Err(stop),
    }
    save(cx, meter, &[("directed", Value::from(state.token))])?;
    Ok(true)
}

// escalate moves the status with the config's escalate, from its from
// states, through Workflow's transition. Usage is never refused, so a veto
// or a permission the caller lacks leaves the status.
fn escalate(cx: &Cx<'_, BudgetConfig>) -> Hook<bool> {
    let (Some(rule), Some(status)) = (&cx.config().escalate, &cx.config().status) else {
        return Ok(false);
    };
    let current = cx
        .get_of(status, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned));
    match moves::apply(cx, status, Some(rule), current.as_deref()) {
        Ok(moved) => Ok(moved),
        Err(Stop::Refuse(refusal)) if refusal.code == Code::Forbidden => Ok(false),
        Err(stop) => Err(stop),
    }
}

// pass_usage hands usage of a meter on to the scope its relation points at
// now, with the part of the reservation it released, which is released
// where the reservation is held; the two are one scope unless the relation
// or a new version moved the meter's scope. It returns the scopes'
// overruns.
fn pass_usage(
    cx: &Cx<'_, BudgetConfig>,
    meter: &str,
    amount: i64,
    released: i64,
    held: Option<&Target>,
) -> Hook<Vec<Value>> {
    let scope = scope_of(cx, spec_of(cx, meter)?);
    let mut carried = released;
    if released > 0 && held.is_some() && held != scope.as_ref() {
        pass_up(cx, meter, "settleFor", &[("amount", released)], held)?;
        carried = 0;
    }
    let upstream = pass_up(
        cx,
        meter,
        "recordUsageFor",
        &[("amount", amount), ("released", carried)],
        scope.as_ref(),
    )?;
    Ok(upstream
        .as_ref()
        .and_then(|answer| answer.get("overruns"))
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default())
}

// count adds usage of a meter here, to its used or a rolling meter's
// bucket and to a holding observation's figure, releases freed of what is
// reserved, and reports the row when it is then over its limit, after its
// escalate has moved its status.
fn count(
    cx: &Cx<'_, BudgetConfig>,
    meter: &str,
    amount: i64,
    freed: i64,
    own_part: i64,
) -> Hook<(Meter, Option<Overrun>)> {
    let spec = spec_of(cx, meter)?;
    let row = load(cx, meter)?;
    let reservation = row.reservation - own_part;
    let mut values = vec![
        ("reserved", Value::from((row.reserved - freed).max(0))),
        ("reservation", Value::from(reservation)),
    ];
    if reservation == 0 {
        values.push(("token", Value::Null));
    }
    match spec.window() {
        Some(length) => bucket(cx, meter, length, amount)?,
        None => values.push(("used", Value::from(row.used + amount))),
    }
    if row.holds(spec, cx.now()) {
        values.push(("observed", Value::from(row.observed.unwrap_or(0) + amount)));
    }
    save(cx, meter, &values)?;
    let state = state_of(cx)?;
    let after = meter_row(&state.meters, meter);
    let used = used_at(spec, meter, &after, &state.buckets, cx.now());
    let limit = limit_at(spec, &after, &cx.own(), cx.now());
    let after = Meter { used, ..after };
    match limit {
        Some(limit) if used > limit => {
            let overrun = Overrun {
                schema: cx.type_name().to_owned(),
                id: cx.key().unwrap_or_default().to_owned(),
                used,
                limit,
                escalated: escalate(cx)?,
            };
            Ok((after, Some(overrun)))
        }
        _ => Ok((after, None)),
    }
}

// ---- the operations ----

fn reserve(cx: &Cx<'_, BudgetConfig>, params: &Value) -> Hook<Value> {
    let plan = plan_of(cx, "reserve", params)?;
    let mut token = None;
    if cx.config().lease.is_some() {
        let Some(lease) = lease_of(cx).filter(|lease| lease.active) else {
            return Err(cx.veto(
                Veto::new(
                    "no lease is active, and on a type that composes Lease a reservation is made under the active lease",
                )
                .with_code("not_leased"),
            ));
        };
        settle_ended(cx)?;
        token = Some(lease.token);
    }
    let mut reserved = Map::new();
    for (meter, wanted) in &plan {
        take(cx, meter, *wanted, Some(token))?;
        reserved.insert(meter.clone(), Value::from(*wanted));
    }
    Ok(json!({ "reserved": reserved }))
}

fn record_usage(cx: &Cx<'_, BudgetConfig>, params: &Value) -> Hook<Value> {
    let meter = meter_param(params);
    known_meter(cx, "recordUsage", meter)?;
    let amount = amount_of(params, "amount");
    let drawn = amount.min(meter_row(&cx.sub_rows(METERS)?, meter).reservation);
    let (row, overrun) = count(cx, meter, amount, drawn, drawn)?;
    let mut overruns: Vec<Value> = overrun.iter().map(Overrun::to_json).collect();
    let upstream = pass_usage(cx, meter, amount, drawn, row.scope.as_ref())?;
    let first = overrun.or_else(|| upstream.first().and_then(Overrun::of));
    overruns.extend(upstream);
    let directed = match &first {
        Some(first) => direct(cx, meter, first)?,
        None => false,
    };
    Ok(json!({
        "meter": meter,
        "used": row.used,
        "released": drawn,
        "overruns": overruns,
        "directed": directed,
    }))
}

fn settle(cx: &Cx<'_, BudgetConfig>, params: &Value) -> Hook<Value> {
    let meter = params.get("meter").and_then(Value::as_str);
    if let Some(meter) = meter {
        known_meter(cx, "settle", meter)?;
    }
    let names: Vec<String> = match meter {
        Some(meter) => vec![meter.to_owned()],
        None => cx.config().names().into_iter().map(str::to_owned).collect(),
    };
    let mut released = Map::new();
    for name in names {
        let amount = settle_own(cx, &name)?;
        released.insert(name, Value::from(amount));
    }
    Ok(json!({ "released": released }))
}

fn set_limit(cx: &Cx<'_, BudgetConfig>, params: &Value) -> Hook<Value> {
    let Some(permission) = &cx.config().limit_permission else {
        return Err(cx.veto(
            Veto::new("its config names no limitPermission, which changing a limit needs")
                .with_code("not_configured"),
        ));
    };
    if !cx.can(permission)? {
        return Err(cx.forbidden(format!(
            "{} may not change a limit of {}: it needs permission {permission}",
            cx.caller().subject,
            target(cx)
        )));
    }
    let meter = meter_param(params);
    let spec = known_meter(cx, "setLimit", meter)?;
    if let Some(field) = &spec.limit_field {
        return Err(params_error(
            cx,
            "setLimit",
            "/meter",
            format!(
                "meter {meter} keeps its limit in {field}, an own field of {}: setLimit writes no own field on a table, so update {field}, which needs limitPermission (D104)",
                cx.type_name()
            ),
        ));
    }
    let limit = amount_of(params, "limit");
    let state = state_of(cx)?;
    let row = meter_row(&state.meters, meter);
    let own = cx.own();
    let previous = limit_at(spec, &row, &own, cx.now());
    let committed = used_at(spec, meter, &row, &state.buckets, cx.now()) + row.reserved;
    if limit < committed {
        return Err(cx.veto(below_committed(meter, committed, limit)));
    }
    load(cx, meter)?;
    save(cx, meter, &[("limitValue", Value::from(limit))])?;
    Ok(json!({ "meter": meter, "limit": limit, "previous": previous }))
}

// observe records the usage a provider reports, which replaces what the
// meter counts while it holds (D104); it passes nothing up a chain.
fn observe(cx: &Cx<'_, BudgetConfig>, params: &Value) -> Hook<Value> {
    let Some(permission) = &cx.config().observe_permission else {
        return Err(cx.veto(
            Veto::new("its config names no observePermission, which observe needs")
                .with_code("not_configured"),
        ));
    };
    if !cx.can(permission)? {
        return Err(cx.forbidden(format!(
            "{} may not observe the usage of {}: it needs permission {permission}",
            cx.caller().subject,
            target(cx)
        )));
    }
    let meter = meter_param(params);
    let spec = known_meter(cx, OBSERVE, meter)?;
    let used = amount_of(params, "used");
    let limit = params.get("limit").and_then(integer);
    let resets_at = params.get("resetsAt").and_then(integer);
    load(cx, meter)?;
    save(
        cx,
        meter,
        &[
            ("observed", Value::from(used)),
            ("observedLimit", limit.map_or(Value::Null, Value::from)),
            ("observedAt", Value::from(cx.now())),
            ("observedUntil", resets_at.map_or(Value::Null, Value::from)),
        ],
    )?;
    let row = meter_row(&cx.sub_rows(METERS)?, meter);
    Ok(json!({
        "meter": meter,
        "used": used,
        "limit": limit_at(spec, &row, &cx.own(), cx.now()),
        "resetsAt": resets_at.or_else(|| spec.reset.map(|reset| reset.next_start(cx.now()))),
    }))
}

fn reserve_for(cx: &Cx<'_, BudgetConfig>, params: &Value) -> Hook<Value> {
    let meter = meter_param(params);
    known_meter(cx, "reserveFor", meter)?;
    let inner = inner_target(params);
    let amount = amount_of(params, "amount");
    let reserved = inner_of(cx, "reserveFor", meter, &inner)?;
    let held = held_for(cx, meter, &inner)?;
    if held + amount > reserved {
        return Err(cx.veto(
            Veto::new(format!(
                "{} has {reserved} of meter {meter} reserved and {held} of it is held here, so {amount} more is not its to hold",
                inner.named()
            ))
            .with_code("exceeds_reservation"),
        ));
    }
    take(cx, meter, amount, None)?;
    hold(cx, meter, &inner, held + amount)?;
    Ok(json!({ "held": held + amount }))
}

fn settle_for(cx: &Cx<'_, BudgetConfig>, params: &Value) -> Hook<Value> {
    let meter = meter_param(params);
    known_meter(cx, "settleFor", meter)?;
    let inner = inner_target(params);
    let held = held_for(cx, meter, &inner)?;
    let still = if held == 0 {
        0
    } else {
        inner_reserved(cx, meter, &inner)?
    };
    let released = amount_of(params, "amount").min((held - still).max(0));
    if released > 0 {
        hold(cx, meter, &inner, held - released)?;
        release(cx, meter, released, 0)?;
    } else {
        // Nothing held here is the inner row's to release.
        cx.unchanged()?;
    }
    Ok(json!({ "released": released }))
}

fn record_usage_for(cx: &Cx<'_, BudgetConfig>, params: &Value) -> Hook<Value> {
    let meter = meter_param(params);
    known_meter(cx, "recordUsageFor", meter)?;
    let inner = inner_target(params);
    let amount = amount_of(params, "amount");
    let still = inner_of(cx, "recordUsageFor", meter, &inner)?;
    let held = held_for(cx, meter, &inner)?;
    let freed = amount_of(params, "released").min((held - still).max(0));
    if freed > 0 {
        hold(cx, meter, &inner, held - freed)?;
    }
    let (row, overrun) = count(cx, meter, amount, freed, 0)?;
    let mut overruns: Vec<Value> = overrun.iter().map(Overrun::to_json).collect();
    overruns.extend(pass_usage(cx, meter, amount, freed, row.scope.as_ref())?);
    Ok(json!({ "released": freed, "overruns": overruns }))
}

// ---- the scope operations' reads ----

fn inner_target(params: &Value) -> Target {
    let text = |name: &str| {
        params
            .get(name)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    };
    Target {
        type_name: text("schema"),
        key: text("id"),
    }
}

// inner_of reads a row that asks a scope operation of this one: its
// reservation of the meter, as the run has its meters. It refuses one whose
// type's meter does not draw on a scope relation that, on the row as the
// run has it, points here.
fn inner_of(cx: &Cx<'_, BudgetConfig>, operation: &str, meter: &str, inner: &Target) -> Hook<i64> {
    let draws = draws_here(cx, meter, inner)?;
    if !draws {
        return Err(params_error(
            cx,
            operation,
            "/id",
            format!(
                "{} does not draw meter {meter} from {}",
                inner.named(),
                target(cx)
            ),
        ));
    }
    inner_reserved(cx, meter, inner)
}

// draws_here reports whether an inner row's meter draws on this row: its
// type's Budget names a scope for the meter, a to-one relation of its type
// whose field, on the row as the run has it, holds this row's key, and
// whose target is this row's type.
fn draws_here(cx: &Cx<'_, BudgetConfig>, meter: &str, inner: &Target) -> Hook<bool> {
    let Some(spec) = cx.read_spec(&inner.type_name)? else {
        return Ok(false);
    };
    let Some(entry) = spec.uses.iter().find(|entry| entry.behavior == NAME) else {
        return Ok(false);
    };
    let config = entry.config_or_empty();
    let Some(relation) = config
        .get("meters")
        .and_then(|meters| meters.get(meter))
        .and_then(|given| given.get("scope"))
        .and_then(Value::as_str)
    else {
        return Ok(false);
    };
    let Some((field, target_type)) = spec.fields.iter().find_map(|field| {
        field
            .relation
            .as_ref()
            .filter(|known| known.name == relation)
            .map(|known| (field.name.clone(), known.type_name.clone()))
    }) else {
        return Ok(false);
    };
    if target_type != cx.type_name() {
        return Ok(false);
    }
    let Some(row) = cx.row_of(&inner.type_name, &inner.key)? else {
        return Ok(false);
    };
    Ok(row.own.get(&field).and_then(key_of).as_deref() == cx.key())
}

// inner_reserved is what an inner row has reserved of a meter, as the run
// has its meters: none for a row that is gone, or whose type keeps no
// Budget.
fn inner_reserved(cx: &Cx<'_, BudgetConfig>, meter: &str, inner: &Target) -> Hook<i64> {
    let Some(spec) = cx.read_spec(&inner.type_name)? else {
        return Ok(0);
    };
    let Some(use_id) = spec
        .uses
        .iter()
        .find(|entry| entry.behavior == NAME)
        .map(|entry| entry.id().to_owned())
    else {
        return Ok(0);
    };
    let rows = cx.sub_rows_of(&inner.type_name, &inner.key, &use_id, METERS)?;
    Ok(meter_row(&rows, meter).reserved)
}

// hold_key is a hold's key: the meter, the inner row's type and its key.
fn hold_key(meter: &str, inner: &Target) -> String {
    format!("{meter}:{}:{}", inner.type_name, inner.key)
}

// held_for is what this row holds of a meter for an inner row.
fn held_for(view: &View<'_, BudgetConfig>, meter: &str, inner: &Target) -> Hook<i64> {
    let key = hold_key(meter, inner);
    Ok(view
        .sub_rows(HOLDS)?
        .iter()
        .find(|row| row.key == key)
        .and_then(|row| row.value("amount").and_then(integer))
        .unwrap_or(0))
}

// hold sets what this row holds of a meter for an inner row; at 0 its row
// is deleted.
fn hold(cx: &Cx<'_, BudgetConfig>, meter: &str, inner: &Target, amount: i64) -> Hook<()> {
    let key = hold_key(meter, inner);
    let exists = cx.sub_rows(HOLDS)?.iter().any(|row| row.key == key);
    if amount == 0 {
        if exists {
            cx.delete_row(HOLDS, &key)?;
        }
        return Ok(());
    }
    let mut values = Map::new();
    values.insert("amount".into(), Value::from(amount));
    if exists {
        return cx.update_row(HOLDS, &key, values);
    }
    values.insert("meter".into(), Value::from(meter));
    values.insert("innerType".into(), Value::from(inner.type_name.as_str()));
    values.insert("innerKey".into(), Value::from(inner.key.as_str()));
    cx.insert_row(HOLDS, &key, values)
}

// ---- checkReserve ----

/// A value of a scope a checkReserve answer turns on: a JSON pointer into
/// the scope as a read returns it, and the number it crosses.
#[derive(Clone, Debug, PartialEq)]
struct ScopeValue {
    path: String,
    crosses: Option<i64>,
}

/// An enclosing scope a checkReserve answer read, and the values of it the
/// answer turns on.
#[derive(Clone, Debug, PartialEq)]
struct ScopeRead {
    schema: String,
    id: String,
    hears: Vec<ScopeValue>,
}

// hear adds a value to what an answer turns on at a scope, once.
fn hear(scope: &mut ScopeRead, path: String, crosses: Option<i64>) {
    if !scope
        .hears
        .iter()
        .any(|one| one.path == path && one.crosses == crosses)
    {
        scope.hears.push(ScopeValue { path, crosses });
    }
}

/// A meter of one row of a chain checkReserve reads, as it reads it: its
/// spec, its row, its rolling buckets, the row's own fields, and what its
/// reservations count as after an ended lease is settled.
struct Reading {
    spec: BudgetMeter,
    meter: String,
    row: Meter,
    buckets: Vec<SubRow>,
    own: Map<String, Value>,
    reserved: i64,
    amount: i64,
}

impl Reading {
    // fits reports whether the plan's amount fits the meter at a time,
    // with every reservation as it is.
    fn fits(&self, at: i64) -> bool {
        match limit_at(&self.spec, &self.row, &self.own, at) {
            Some(limit) => {
                used_at(&self.spec, &self.meter, &self.row, &self.buckets, at)
                    + self.reserved
                    + self.amount
                    <= limit
            }
            None => true,
        }
    }

    // moves lists the instants after a time at which the clock alone moves
    // what the meter counts: its period's next start, its observation's
    // lapse, each rolling bucket's departure.
    fn moves(&self, after: i64) -> Vec<i64> {
        let mut at = Vec::new();
        if let Some(reset) = self.spec.fixed() {
            at.push(reset.next_start(after));
        }
        if self.row.holds(&self.spec, after) {
            if let Some(lapse) = self.row.lapse(&self.spec) {
                at.push(lapse);
            }
        }
        if let Some(bucket) = self.spec.window() {
            at.extend(
                buckets_of(&self.buckets, &self.meter)
                    .map(|(starts_at, _)| starts_at + BUCKETS_COUNTED * bucket),
            );
        }
        at.retain(|instant| *instant > after);
        at
    }
}

// check_reserve answers whether a plan would fit now: here, after the own
// reservation of an ended lease is settled as reserve settles it first,
// and at each scope up the chain, read through its meters as the run has
// them, less what that settlement releases there. When it does not fit,
// until is the earliest instant at which the clock alone lets it fit. Each
// scope comes with the values of it the answer turns on: its remaining
// crosses the amount less the release exactly when the reservation's fit
// there flips; where a meter with a fixed reset keeps it out now, its
// reserved crossing the limit less the amount and the release, or a new
// limit, flips whether its next period lets it in; and its own scope
// relation moves the chain. Where the release is not settled yet, each
// number is given as it is now and as the settlement leaves it.
fn check_reserve(cx: &Cx<'_, BudgetConfig>, plan: &[(String, i64)]) -> Hook<Value> {
    let now = cx.now();
    let active = active_token(lease_of(cx).as_ref());
    let state = state_of(cx)?;
    let own = cx.own();
    let mut scopes: Vec<ScopeRead> = Vec::new();
    let mut readings: Vec<Reading> = Vec::new();
    let mut moved = false;
    for (meter, amount) in plan {
        let (meter, amount) = (meter.as_str(), *amount);
        let spec = spec_of(cx, meter)?;
        let row = meter_row(&state.meters, meter);
        let stale =
            if cx.config().lease.is_some() && row.reservation > 0 && !same_token(row.token, active)
            {
                row.reservation
            } else {
                0
            };
        let reserved = row.reserved - stale;
        let mut target = scope_of(cx, spec);
        if target.is_some() && reserved > 0 && row.scope.is_some() && row.scope != target {
            moved = true;
        }
        // The settlement goes where the reservation is held, and on up from
        // there.
        let released = if stale > 0 && row.scope.is_some() && row.scope == target {
            stale
        } else {
            0
        };
        readings.push(Reading {
            spec: spec.clone(),
            meter: meter.to_owned(),
            row,
            buckets: state.buckets.clone(),
            own: own.clone(),
            reserved,
            amount,
        });
        let unsettled: &[i64] = if released > 0 { &[released, 0] } else { &[0] };
        let mut walked: Vec<Target> = Vec::new();
        while let Some(at) = target.take() {
            if walked.len() >= MAX_CHAIN || walked.contains(&at) {
                break;
            }
            walked.push(at.clone());
            let Some(record) = cx.row_of(&at.type_name, &at.key)? else {
                break;
            };
            let Some(scope_spec) = cx.read_spec(&at.type_name)? else {
                break;
            };
            let Some(entry) = scope_spec.uses.iter().find(|entry| entry.behavior == NAME) else {
                break;
            };
            let config = entry.config_or_empty();
            let Some(theirs) = config.get("meters").and_then(|meters| meters.get(meter)) else {
                break;
            };
            let theirs = BudgetMeter::from_raw(theirs);
            let index = match scopes
                .iter()
                .position(|scope| scope.schema == at.type_name && scope.id == at.key)
            {
                Some(index) => index,
                None => {
                    scopes.push(ScopeRead {
                        schema: at.type_name.clone(),
                        id: at.key.clone(),
                        hears: Vec::new(),
                    });
                    scopes.len() - 1
                }
            };
            let rows = cx.sub_rows_of(&at.type_name, &at.key, entry.id(), METERS)?;
            let buckets = if theirs.rolling {
                cx.sub_rows_of(&at.type_name, &at.key, entry.id(), BUCKETS)?
            } else {
                Vec::new()
            };
            let counted = meter_row(&rows, meter);
            let scope = &mut scopes[index];
            for unsettled in unsettled {
                hear(
                    scope,
                    format!("/behaviors/{NAME}/meters/{meter}/remaining"),
                    Some(amount - unsettled),
                );
            }
            let reading = Reading {
                reserved: (counted.reserved - released).max(0),
                spec: theirs.clone(),
                meter: meter.to_owned(),
                row: counted,
                buckets,
                own: record.own.clone(),
                amount,
            };
            if let Some(limit) = limit_at(&reading.spec, &reading.row, &reading.own, now) {
                if theirs.fixed().is_some() && !reading.fits(now) {
                    for unsettled in unsettled {
                        hear(
                            scope,
                            format!("/behaviors/{NAME}/meters/{meter}/reserved"),
                            Some(limit - amount + unsettled + 1),
                        );
                    }
                    hear(
                        scope,
                        format!("/behaviors/{NAME}/meters/{meter}/limit"),
                        None,
                    );
                }
            }
            readings.push(reading);
            if let Some(relation) = theirs.scope.as_ref().map(|scope| scope.relation.clone()) {
                hear(scope, format!("/behaviors/Links/targets/{relation}"), None);
                target = scope_spec.fields.iter().find_map(|field| {
                    let known = field
                        .relation
                        .as_ref()
                        .filter(|known| known.name == relation)?;
                    Some(Target {
                        type_name: known.type_name.clone(),
                        key: key_of(record.own.get(&field.name)?)?,
                    })
                });
            }
        }
    }
    let fits = !moved && readings.iter().all(|reading| reading.fits(now));
    let until = if fits || moved {
        None
    } else {
        let mut instants: Vec<i64> = readings
            .iter()
            .flat_map(|reading| reading.moves(now))
            .collect();
        instants.sort_unstable();
        instants.dedup();
        instants
            .into_iter()
            .find(|at| readings.iter().all(|reading| reading.fits(*at)))
    };
    Ok(json!({
        "fits": fits,
        "until": until,
        "scopes": scopes.iter().map(|scope| json!({
            "schema": scope.schema,
            "id": scope.id,
            "hears": scope.hears.iter().map(|value| {
                let mut out = Map::new();
                out.insert("path".into(), Value::from(value.path.as_str()));
                if let Some(crosses) = value.crosses {
                    out.insert("crosses".into(), Value::from(crosses));
                }
                Value::Object(out)
            }).collect::<Vec<_>>(),
        })).collect::<Vec<_>>(),
    }))
}

// ---- small readers ----

// target names the row a message is about: its type and its key.
fn target<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

fn below_committed(meter: &str, committed: i64, limit: i64) -> Veto {
    Veto::new(format!(
        "meter {meter} has {committed} used and reserved, more than the limit {limit}"
    ))
    .with_code("below_committed")
    .with_details(object(json!({ "meter": meter, "committed": committed })))
}

fn params_error<C>(view: &View<'_, C>, operation: &str, path: &str, message: String) -> Stop {
    Refusal::operation_params(
        view.behavior(),
        view.use_id(),
        operation,
        vec![SchemaIssue::new(path, message)],
    )
    .into()
}

fn object(value: Value) -> Map<String, Value> {
    match value {
        Value::Object(members) => members,
        _ => Map::new(),
    }
}

fn meter_param(params: &Value) -> &str {
    params
        .get("meter")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

fn amount_of(params: &Value, name: &str) -> i64 {
    params.get(name).and_then(integer).unwrap_or(0)
}

// integer reads a whole number, as JSON may write one (2 or 2.0), or a
// decimal string, as an adapter may read a BIGINT.
fn integer(value: &Value) -> Option<i64> {
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

// count_of reads a count: a safe integer of at least 0 (isCount).
fn count_of(value: Option<&Value>) -> Option<i64> {
    integer(value?).filter(|count| (0..=MAX_SAFE_INTEGER).contains(count))
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
    if !config.contains_key("meters") {
        issues.push("", "required", "must have required property 'meters'");
    }
    for key in ["limitPermission", "observePermission", "status"] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    if let Some(meters) = config.get("meters") {
        meters_issues("/meters", meters, &mut issues);
    }
    if let Some(on_exceeded) = config.get("onExceeded") {
        on_exceeded_issues("/onExceeded", on_exceeded, &mut issues);
    }
    if let Some(rule) = config.get("escalate") {
        move_issues("/escalate", rule, &mut issues);
    }
    issues
}

fn meters_issues(at: &str, meters: &Value, issues: &mut Issues) {
    let Some(named) = meters.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (name, meter) in named {
        // A name that is no meter's fails the object's propertyNames, which
        // ajv reports at the object, after the name's own failure.
        if !is_meter_name(name) {
            issues.push(
                at,
                "pattern",
                format!("must match pattern \"{METER_PATTERN}\""),
            );
            issues.push(at, "propertyNames", "property name must be valid");
        }
        meter_issues(&schema::member(at, name), meter, issues);
    }
}

fn meter_issues(at: &str, meter: &Value, issues: &mut Issues) {
    let Some(members) = meter.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &METER_KEYS, issues);
    for key in ["limit", "reserve"] {
        if let Some(value) = members.get(key) {
            schema::integer_issues(&schema::member(at, key), value, Some(1), None, issues);
        }
    }
    for key in ["limitField", "reserveField"] {
        if let Some(value) = members.get(key) {
            schema::text_issues(&schema::member(at, key), value, issues);
        }
    }
    if let Some(scope) = members.get("scope") {
        let at = schema::member(at, "scope");
        match scope {
            Value::String(name) if !is_meter_name(name) => issues.push(
                &at,
                "pattern",
                format!("must match pattern \"{METER_PATTERN}\""),
            ),
            Value::String(_) => {}
            _ => issues.push(&at, "type", "must be string"),
        }
    }
    if let Some(reset) = members.get("reset") {
        reset_issues(&schema::member(at, "reset"), reset, issues);
    }
    if let Some(window) = members.get("window") {
        if window.as_str() != Some("rolling") {
            issues.push(
                schema::member(at, "window"),
                "enum",
                "must be one of \"rolling\"",
            );
        }
    }
}

// reset_issues lists what a reset breaks of its oneOf, as ajv reports it:
// nothing when exactly one branch holds; else every branch's issues and the
// oneOf's own.
fn reset_issues(at: &str, reset: &Value, issues: &mut Issues) {
    let mut named = Issues::default();
    if !reset.as_str().is_some_and(|name| RESETS.contains(&name)) {
        named.push(
            at,
            "enum",
            format!(
                "must be one of {}",
                RESETS
                    .iter()
                    .map(|name| schema::quoted(name))
                    .collect::<Vec<_>>()
                    .join(", ")
            ),
        );
    }
    let mut every = Issues::default();
    match reset.as_object() {
        None => every.push(at, "type", "must be object"),
        Some(members) => {
            unknown_keys(at, members, &["everyMs"], &mut every);
            match members.get("everyMs") {
                None => every.push(at, "required", "must have required property 'everyMs'"),
                Some(ms) => {
                    let at = schema::member(at, "everyMs");
                    schema::integer_issues(&at, ms, Some(periods::MINUTE_MS), None, &mut every);
                    let whole = ms.as_number().and_then(|number| number.as_f64());
                    if whole.is_some_and(|n| n % periods::MINUTE_MS as f64 != 0.0) {
                        every.push(
                            &at,
                            "multipleOf",
                            format!("must be multiple of {}", periods::MINUTE_MS),
                        );
                    }
                }
            }
        }
    }
    let held = usize::from(named.is_empty()) + usize::from(every.is_empty());
    if held == 1 {
        return;
    }
    issues.extend(named);
    issues.extend(every);
    issues.push(at, "oneOf", "must match exactly one schema in oneOf");
}

fn on_exceeded_issues(at: &str, value: &Value, issues: &mut Issues) {
    let Some(members) = value.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &["direct"], issues);
    match members.get("direct") {
        None => issues.push(at, "required", "must have required property 'direct'"),
        Some(Value::String(name)) if !is_directive_name(name) => issues.push(
            schema::member(at, "direct"),
            "pattern",
            format!("must match pattern \"{DIRECT_PATTERN}\""),
        ),
        Some(Value::String(_)) => {}
        Some(_) => issues.push(schema::member(at, "direct"), "type", "must be string"),
    }
}

// move_issues lists what a status move breaks of `{ transition, from }`,
// both required, from a list of distinct states.
fn move_issues(at: &str, rule: &Value, issues: &mut Issues) {
    let Some(members) = rule.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &MOVE_KEYS, issues);
    for key in ["transition", "from"] {
        if !members.contains_key(key) {
            issues.push(
                at,
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if let Some(transition) = members.get("transition") {
        schema::text_issues(&schema::member(at, "transition"), transition, issues);
    }
    if let Some(from) = members.get("from") {
        let at = schema::member(at, "from");
        let Some(items) = from.as_array() else {
            issues.push(&at, "type", "must be array");
            return;
        };
        if items.is_empty() {
            issues.push(&at, "minItems", "must NOT have fewer than 1 items");
        }
        for (i, item) in items.iter().enumerate() {
            schema::text_issues(&format!("{at}/{i}"), item, issues);
        }
        if let Some((j, i)) = schema::duplicate(items) {
            issues.push(
                &at,
                "uniqueItems",
                format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
            );
        }
    }
}

// is_meter_name says whether a name matches METER_PATTERN: a lowercase
// letter, then at most 63 letters and digits, ASCII alone.
fn is_meter_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars.next().is_some_and(|first| first.is_ascii_lowercase())
        && chars.all(|c| c.is_ascii_alphanumeric())
        && name.len() <= 64
}

// is_directive_name says whether a name matches DIRECT_PATTERN: a letter,
// then at most 63 letters, digits, `_`, `.`, `:` and `-`, ASCII alone.
fn is_directive_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars
        .next()
        .is_some_and(|first| first.is_ascii_alphabetic())
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | ':' | '-'))
        && name.len() <= 64
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

// A member of an operation's closed paramsSchema: its name, whether it is
// required, and how its value is checked.
enum Member {
    Text,
    Count(i64),
}

// params_issues lists what an operation's parameters break of its closed
// paramsSchema, in the behavior registry's words.
fn params_issues(operation: &str, params: &Value) -> Issues {
    let mut issues = Issues::default();
    let members: &[(&str, bool, Member)] = match operation {
        "reserve" | "checkReserve" => &[
            ("amount", false, Member::Count(1)),
            ("meter", false, Member::Text),
        ],
        "recordUsage" => &[
            ("amount", true, Member::Count(1)),
            ("meter", true, Member::Text),
        ],
        "settle" => &[("meter", false, Member::Text)],
        "setLimit" => &[
            ("limit", true, Member::Count(0)),
            ("meter", true, Member::Text),
        ],
        "reserveFor" | "settleFor" => &[
            ("amount", true, Member::Count(1)),
            ("id", true, Member::Text),
            ("meter", true, Member::Text),
            ("schema", true, Member::Text),
        ],
        "recordUsageFor" => &[
            ("amount", true, Member::Count(1)),
            ("id", true, Member::Text),
            ("meter", true, Member::Text),
            ("released", true, Member::Count(0)),
            ("schema", true, Member::Text),
        ],
        OBSERVE => &[
            ("limit", false, Member::Count(0)),
            ("meter", true, Member::Text),
            ("resetsAt", false, Member::Count(0)),
            ("used", true, Member::Count(0)),
        ],
        _ => &[],
    };
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in given
        .keys()
        .filter(|key| !members.iter().any(|(name, ..)| name == key))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    for (name, required, member) in members {
        match given.get(*name) {
            None if *required => issues.push(
                "",
                "required",
                format!("must have required property '{name}'"),
            ),
            None => {}
            Some(value) => {
                let at = schema::member("", name);
                match member {
                    Member::Text => schema::text_issues(&at, value, &mut issues),
                    Member::Count(minimum) => {
                        schema::integer_issues(&at, value, Some(*minimum), None, &mut issues);
                    }
                }
            }
        }
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_integer_type_is_one_whose_last_segment_says_so() {
        for type_name in [
            "Generic.Int64",
            "Generic.Int32",
            "int",
            "integer",
            "Money.INT",
        ] {
            assert!(is_integer_type(type_name), "{type_name}");
        }
        for type_name in ["number", "string", "Generic.Float64", "Int64.Text"] {
            assert!(!is_integer_type(type_name), "{type_name}");
        }
    }
}
