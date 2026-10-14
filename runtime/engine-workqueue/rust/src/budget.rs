//! Budget, reserve-then-settle budgets on an instance (D16), the port of
//! `typescript/src/budget.ts`, in units the deployment names. Each meter of
//! the config has, per instance, a row of Budget's own table: what the
//! instance has used, what it has reserved, the limit setLimit gave it, and
//! for a daily meter the UTC day its usage counts from. `reserved` is the
//! instance's own reservation, the one its claims made, plus what it holds
//! for the instances inside it; the own part is kept apart, with the lease
//! token it was made under, so settling releases exactly that.
//!
//! A meter's limit is the instance's `limitField` while it holds a count,
//! else the config's limit; without `limitField`, the one setLimit set,
//! else the config's limit, as `reserveField` falls back to `reserve`. With
//! none, the meter counts without a limit of its own. A reservation fits
//! while used plus reserved plus the amount is within the limit. What a
//! claim reserves of a meter is the instance's `reserveField` when it holds
//! a positive integer, else the config's `reserve`.
//!
//! Scopes. A meter may name a scope: a link of the type's Links config,
//! holding one target (D56), whose target encloses the instance and whose
//! schema composes Budget with the same meter, which `parse_config` checks
//! when the schema is defined. Budget never writes another instance's rows
//! (D16): the instance invokes an operation on its scope, `reserveFor`,
//! `settleFor` or `recordUsageFor`, which applies the scope's own check,
//! records what the scope holds for the instance, writes the scope's rows,
//! appends the scope's own event and passes the change on up the scope's
//! own scope, all in the caller's transaction, as the caller. The scope
//! operations cannot free budget an instance still holds: a scope reads the
//! instance through its `meters` field before it releases anything, holds
//! no more for an instance than the instance has reserved, and takes
//! reservations and usage only from an instance whose scope link for the
//! meter points at it. A scope link does not move while a reservation of a
//! meter is held through it (the guard); the row records the scope that
//! holds the reservation, and releases go there.
//!
//! Usage is never refused: `recordUsage` adds the amount to used, releases
//! the part of it the own reservation covers at every scope, and reports
//! each instance of the chain left over its limit. With `onExceeded` the
//! holder of the active lease gets a directive through Lease's `direct`,
//! once per meter per lease token; with `escalate` each instance of the
//! chain left over its limit moves its own status by its own config,
//! through Workflow's `transition`, as the caller; a move a guard vetoes or
//! the caller may not make leaves the status.
//!
//! `checkReserve`, read-only, answers whether `reserve` would fit now, here
//! and up the chain, after the reservations of an ended lease are settled
//! as `reserve` settles them first, and when not, whether the next UTC
//! day's start alone makes it fit; and it lists the scopes it read, each
//! with the values of it the answer turns on, as a reference hears them
//! ([`ScopeValue`]), which Queue copies (D16, amended: behaviors that serve
//! claimable work).
//!
//! Lease. On a type that composes Lease a reservation is made under the
//! active lease and lives as long as it: `reserve` is refused while no
//! lease is active, and after a caller's Lease or Queue operation, and
//! before each `reserve`, the reservations of a lease that is no longer the
//! active one are settled. Deleting the instance settles everything it
//! reserved and holds.
//!
//! A daily meter's usage counts from the start of the UTC day; a read never
//! writes, and the next write of its row starts the new day first. The
//! engine's clock gives the time.
//!
//! Every refusal is a veto with a code the declaration lists, but a
//! permission the caller lacks, which is forbidden.
//!
//! Storage (D58): two tables of Budget's own, keyed by the instance and
//! the meter; every statement is the same on both databases but the order
//! of meters, which Postgres reads in byte order (`COLLATE "C"`).
//!
//! configChange: a meter cannot be removed while the schema has instances;
//! limits, reservations, scopes and resets may change. Budget can be added
//! to a schema that has instances and cannot be removed from one.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, InstanceCx, InstanceView, TableSpec,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::json::{js_keys, number_value};
use superschematic_engine::storage::{Row, Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, InstanceRecord, OperationCx, Result,
};
use superschematic_engine_behaviors::link_many;

const NAME: &str = "Budget";
const DAY_MS: i64 = 86_400_000;

/// The most enclosing scopes checkReserve walks up a meter's chain.
const MAX_CHAIN: usize = 16;

/// The largest integer a JavaScript number holds exactly.
const MAX_SAFE_INTEGER: f64 = 9_007_199_254_740_991.0;

/// The fields Budget reads of an instance, its own and others', by
/// qualified name.
const METERS: &str = "Budget.meters";
const LINKS_TARGETS: &str = "Links.targets";
const LEASE_TOKEN: &str = "Lease.token";
const LEASE_ACTIVE: &str = "Lease.active";
const WORKFLOW_STATUS: &str = "Workflow.status";

const COLUMNS: &str = "meter, used, reserved, limit_value, period_start, reservation, token, directed, scope_schema, scope_id";

/// One meter of a Budget config, parsed.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BudgetMeter {
    pub limit: Option<i64>,
    pub limit_field: Option<String>,
    pub reserve: Option<i64>,
    pub reserve_field: Option<String>,
    /// The Links link whose target is the enclosing scope.
    pub scope: Option<String>,
    /// Whether used starts again at 0 at each UTC day.
    pub daily: bool,
}

/// A move of the status: to `transition`, from one of `from`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BudgetTransition {
    pub transition: String,
    pub from: Vec<String>,
}

/// Budget's config, parsed.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BudgetConfig {
    /// The meters, by name, in the config's order.
    pub meters: Vec<(String, BudgetMeter)>,
    pub limit_permission: Option<String>,
    /// onExceeded's directive: the name Lease's direct sends.
    pub on_exceeded: Option<String>,
    /// The move of the status usage that leaves the instance over a limit
    /// makes.
    pub escalate: Option<BudgetTransition>,
    /// Whether the type composes Lease, whose active lease a reservation
    /// lives within.
    pub leased: bool,
}

impl BudgetConfig {
    /// A meter by name; `None` for a name the config does not give.
    pub fn meter(&self, name: &str) -> Option<&BudgetMeter> {
        self.meters
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, meter)| meter)
    }

    /// The meters' names, in the config's order.
    pub fn names(&self) -> Vec<&str> {
        self.meters.iter().map(|(name, _)| name.as_str()).collect()
    }
}

/// A value of a scope a checkReserve answer turns on, as a reference hears
/// it: a JSON pointer into the scope as a read returns it, and the number
/// it crosses.
#[derive(Clone, Debug, PartialEq)]
pub struct ScopeValue {
    pub path: String,
    pub crosses: Option<i64>,
}

impl ScopeValue {
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("path".into(), Value::from(self.path.as_str()));
        if let Some(crosses) = self.crosses {
            out.insert("crosses".into(), Value::from(crosses));
        }
        Value::Object(out)
    }
}

/// An enclosing scope a checkReserve answer read, and the values of it the
/// answer turns on.
#[derive(Clone, Debug, PartialEq)]
pub struct ScopeRead {
    pub schema: String,
    pub id: String,
    pub hears: Vec<ScopeValue>,
}

/// What checkReserve answers.
#[derive(Clone, Debug, PartialEq)]
pub struct ReserveCheck {
    pub fits: bool,
    /// When it does not fit, the start of the next UTC day if that alone
    /// makes it fit; `None` otherwise.
    pub until: Option<i64>,
    /// The enclosing scopes the answer read, innermost first.
    pub scopes: Vec<ScopeRead>,
}

impl ReserveCheck {
    /// The answer as checkReserve returns it.
    pub fn to_json(&self) -> Value {
        json!({
            "fits": self.fits,
            "until": self.until,
            "scopes": self.scopes.iter().map(|scope| json!({
                "schema": scope.schema,
                "id": scope.id,
                "hears": scope.hears.iter().map(ScopeValue::to_json).collect::<Vec<_>>(),
            })).collect::<Vec<_>>(),
        })
    }
}

/// An instance, by schema and id.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Target {
    schema: String,
    id: String,
}

impl Target {
    fn key(&self) -> String {
        format!("{}\u{0}{}", self.schema, self.id)
    }
}

/// A meter's row, or the values of one not written yet.
#[derive(Clone, Debug, Default)]
struct Meter {
    used: i64,
    reserved: i64,
    limit_value: Option<i64>,
    period_start: Option<i64>,
    /// The instance's own reservation, a part of reserved.
    reservation: i64,
    /// The lease token the own reservation was made under.
    token: Option<i64>,
    /// The lease token onExceeded's directive was last sent under.
    directed: Option<i64>,
    /// Where the reservation is held up the chain: the scope it was last
    /// passed to.
    scope: Option<Target>,
}

/// An instance of a chain whose usage of a meter is over its limit.
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
}

// key is the instance's rows as Budget's SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

// integer reads a JSON integer, as JavaScript holds it.
fn integer(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    if let Some(exact) = number.as_i64() {
        return Some(exact);
    }
    let value = number_value(number);
    (value.is_finite() && value.fract() == 0.0).then_some(value as i64)
}

// count_of reads a count: a safe integer of at least 0 (isCount).
fn count_of(value: Option<&Value>) -> Option<i64> {
    let number = value?.as_number()?;
    let value = number_value(number);
    (value.fract() == 0.0 && value.abs() <= MAX_SAFE_INTEGER && value >= 0.0)
        .then_some(value as i64)
}

fn day_start(now: i64) -> i64 {
    now.div_euclid(DAY_MS) * DAY_MS
}

// vetoed is a refusal with one of the codes the declaration lists.
fn vetoed<C>(
    view: &InstanceView<'_, C>,
    operation: &str,
    reason: String,
    code: &str,
    details: Option<Value>,
) -> Error {
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: details.and_then(|details| match details {
                Value::Object(details) => Some(details),
                _ => None,
            }),
        },
    )
}

fn forbidden<C>(view: &InstanceView<'_, C>, what: &str, permission: &str) -> Error {
    EngineError::new(
        EngineErrorCode::Forbidden,
        format!(
            "{} may not {what} {} {}: it needs permission {permission}",
            view.principal().subject,
            view.schema(),
            view.id()
        ),
    )
    .into()
}

fn params_error(operation: &str, path: &str, message: String) -> Error {
    EngineError::operation_params(NAME, operation, vec![SchemaIssue::new(path, message)]).into()
}

fn meter_of(row: &Row) -> Result<Meter> {
    let scope = match row.opt_text("scope_schema")? {
        Some(schema) => Some(Target {
            schema: schema.to_owned(),
            id: row.opt_text("scope_id")?.unwrap_or_default().to_owned(),
        }),
        None => None,
    };
    Ok(Meter {
        used: row.i64("used")?,
        reserved: row.i64("reserved")?,
        limit_value: row.opt_i64("limit_value")?,
        period_start: row.opt_i64("period_start")?,
        reservation: row.i64("reservation")?,
        token: row.opt_i64("token")?,
        directed: row.opt_i64("directed")?,
        scope,
    })
}

// rows reads the instance's meter rows, by meter, in byte order of their
// names on both databases.
fn rows(view: &InstanceView<'_, BudgetConfig>) -> Result<Vec<(String, Meter)>> {
    let table = view.sql().table("meters")?;
    let select =
        format!("SELECT {COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ?");
    let found = view.sql().all(
        Sql::per(
            format!("{select} ORDER BY meter"),
            format!("{select} ORDER BY meter COLLATE \"C\""),
        ),
        &key(view),
    )?;
    found
        .iter()
        .map(|row| Ok((row.text("meter")?.to_owned(), meter_of(row)?)))
        .collect()
}

fn row_of(view: &InstanceView<'_, BudgetConfig>, meter: &str) -> Result<Meter> {
    let table = view.sql().table("meters")?;
    let mut values = key(view);
    values.push(SqlValue::from(meter));
    let row = view.sql().get(
        format!(
            "SELECT {COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND meter = ?"
        ),
        &values,
    )?;
    row.as_ref().map_or_else(|| Ok(Meter::default()), meter_of)
}

// rolled reports whether a daily meter's day has passed since its usage
// started counting, so its used is 0 now.
fn rolled<C>(view: &InstanceView<'_, C>, spec: &BudgetMeter, meter: &Meter) -> bool {
    spec.daily
        && meter
            .period_start
            .is_some_and(|start| start < day_start(view.now()))
}

fn used_now<C>(view: &InstanceView<'_, C>, spec: &BudgetMeter, meter: &Meter) -> i64 {
    if rolled(view, spec, meter) {
        0
    } else {
        meter.used
    }
}

// field_limit is the limit of a meter with limitField when the field holds
// value: the value when it is a count, else the config's limit.
fn field_limit(spec: &BudgetMeter, value: Option<&Value>) -> Option<i64> {
    count_of(value).or(spec.limit)
}

/// The one rule for a meter's limit on an instance: its limitField while
/// it holds a count, else the config's; without limitField, setLimit's,
/// else the config's; `None` for none.
fn limit_of<C>(
    view: &InstanceView<'_, C>,
    spec: &BudgetMeter,
    meter: &Meter,
) -> Result<Option<i64>> {
    if let Some(field) = &spec.limit_field {
        return Ok(field_limit(spec, view.data()?.get(field)));
    }
    Ok(meter.limit_value.or(spec.limit))
}

fn record_of<C>(view: &InstanceView<'_, C>, spec: &BudgetMeter, meter: &Meter) -> Result<Value> {
    let used = used_now(view, spec, meter);
    let limit = limit_of(view, spec, meter)?;
    Ok(json!({
        "used": used,
        "reserved": meter.reserved,
        "limit": limit,
        "remaining": limit.map(|limit| limit - used - meter.reserved),
    }))
}

// spec_of is the spec of a meter the config gives, which every caller
// checked; another is a defect.
fn spec_of<'a>(view: &InstanceView<'a, BudgetConfig>, meter: &str) -> Result<&'a BudgetMeter> {
    view.config()
        .meter(meter)
        .ok_or_else(|| Error::behavior(NAME, format!("meter {meter} is not one of its config's")))
}

// known_meter returns a meter's spec, or refuses a name the config does
// not give (specOf).
fn known_meter<'a>(
    view: &InstanceView<'a, BudgetConfig>,
    operation: &str,
    meter: &str,
) -> Result<&'a BudgetMeter> {
    view.config().meter(meter).ok_or_else(|| {
        params_error(
            operation,
            "/meter",
            format!(
                "{} has no meter {meter} (its meters: {})",
                view.schema(),
                view.config().names().join(", ")
            ),
        )
    })
}

// load reads a meter's row for a write, making it when the instance has
// none yet and starting a daily meter's new day first: the one place a row
// is made or its day moves, so a read never writes.
fn load(cx: &InstanceCx<'_, BudgetConfig>, meter: &str) -> Result<Meter> {
    let spec = spec_of(cx, meter)?;
    let table = cx.sql().table("meters")?;
    let today = day_start(cx.now());
    let mut values = key(cx);
    values.push(SqlValue::from(meter));
    values.push(SqlValue::from(spec.daily.then_some(today)));
    cx.sql().run(
        format!("INSERT INTO {table} (namespace, schema, id, meter, period_start) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING"),
        &values,
    )?;
    let row = row_of(cx, meter)?;
    if spec.daily && row.period_start.is_none_or(|start| start < today) {
        let used = if row.period_start.is_none() {
            row.used
        } else {
            0
        };
        save(
            cx,
            meter,
            &[
                ("used", SqlValue::from(used)),
                ("period_start", SqlValue::from(today)),
            ],
        )?;
        return Ok(Meter {
            used,
            period_start: Some(today),
            ..row
        });
    }
    Ok(row)
}

fn save(cx: &InstanceCx<'_, BudgetConfig>, meter: &str, values: &[(&str, SqlValue)]) -> Result<()> {
    let table = cx.sql().table("meters")?;
    let set = values
        .iter()
        .map(|(name, _)| format!("{name} = ?"))
        .collect::<Vec<_>>()
        .join(", ");
    let mut params: Vec<SqlValue> = values.iter().map(|(_, value)| value.clone()).collect();
    params.extend(key(cx));
    params.push(SqlValue::from(meter));
    cx.sql().run(
        format!(
            "UPDATE {table} SET {set} WHERE namespace = ? AND schema = ? AND id = ? AND meter = ?"
        ),
        &params,
    )?;
    Ok(())
}

// claim_amount is what reserve takes of a meter when it is given no meter:
// the instance's reserveField when it holds a positive integer, else the
// config's reserve.
fn claim_amount<C>(view: &InstanceView<'_, C>, spec: &BudgetMeter) -> Result<Option<i64>> {
    if let Some(field) = &spec.reserve_field {
        if let Some(value) = count_of(view.data()?.get(field)).filter(|value| *value > 0) {
            return Ok(Some(value));
        }
    }
    Ok(spec.reserve)
}

// plan_of is what reserve and checkReserve take, by meter: one meter's
// amount, or every meter's claim amount.
fn plan_of(
    view: &InstanceView<'_, BudgetConfig>,
    operation: &str,
    params: &Value,
) -> Result<Vec<(String, i64)>> {
    let meter = params.get("meter").and_then(Value::as_str);
    let amount = params.get("amount").and_then(integer);
    let Some(meter) = meter else {
        if amount.is_some() {
            return Err(params_error(
                operation,
                "/amount",
                "an amount is reserved of one meter, which meter names".to_owned(),
            ));
        }
        let mut plan = Vec::new();
        for (name, spec) in &view.config().meters {
            if let Some(wanted) = claim_amount(view, spec)? {
                plan.push((name.clone(), wanted));
            }
        }
        return Ok(plan);
    };
    let spec = known_meter(view, operation, meter)?;
    let wanted = match amount {
        Some(amount) => amount,
        None => claim_amount(view, spec)?.ok_or_else(|| {
            params_error(
                operation,
                "/amount",
                format!("meter {meter} has no configured reservation, so reserve takes an amount"),
            )
        })?,
    };
    Ok(vec![(meter.to_owned(), wanted)])
}

fn field<'r>(record: Option<&'r InstanceRecord>, behavior: &str, name: &str) -> Option<&'r Value> {
    record?.behavior_field(behavior, name)
}

// links_in reads an instance's Links targets field, of what a read
// returns: the links that hold one target. A list, which parse_config
// refuses as a scope, encloses nothing.
fn links_in(record: Option<&InstanceRecord>) -> Vec<(String, Target)> {
    let Some(links) = field(record, "Links", "targets").and_then(Value::as_object) else {
        return Vec::new();
    };
    js_keys(links)
        .into_iter()
        .filter_map(|name| {
            let link = links[name].as_object()?;
            Some((
                name.to_owned(),
                Target {
                    schema: link.get("schema")?.as_str()?.to_owned(),
                    id: link.get("id")?.as_str()?.to_owned(),
                },
            ))
        })
        .collect()
}

fn link_in(links: Vec<(String, Target)>, name: &str) -> Option<Target> {
    links
        .into_iter()
        .find(|(known, _)| known == name)
        .map(|(_, target)| target)
}

// scope_of reads where a meter's scope link points now, through the
// instance's Links targets field, as the caller; `None` when the meter has
// no scope or the link is not set.
fn scope_of(view: &InstanceView<'_, BudgetConfig>, meter: &str) -> Result<Option<Target>> {
    let Some(link) = &spec_of(view, meter)?.scope else {
        return Ok(None);
    };
    let record = view
        .instances()
        .get(view.schema(), view.id(), Some(&[LINKS_TARGETS]))?;
    Ok(link_in(links_in(record.as_ref()), link))
}

/// The instance's lease, read through Lease's token and active fields.
struct LeaseState {
    token: Option<i64>,
    active: bool,
}

// lease_of reads the lease; `None` on a type without Lease.
fn lease_of(view: &InstanceView<'_, BudgetConfig>) -> Result<Option<LeaseState>> {
    if !view.config().leased {
        return Ok(None);
    }
    let record =
        view.instances()
            .get(view.schema(), view.id(), Some(&[LEASE_TOKEN, LEASE_ACTIVE]))?;
    Ok(record.map(|record| LeaseState {
        token: field(Some(&record), "Lease", "token").and_then(integer),
        active: field(Some(&record), "Lease", "active") == Some(&Value::Bool(true)),
    }))
}

// active_token is the token of the active lease; `None` when no lease is
// active.
fn active_token(lease: Option<&LeaseState>) -> Option<i64> {
    lease
        .filter(|lease| lease.active)
        .and_then(|lease| lease.token)
}

// same_token is whether a token a row keeps is the one given: a row's
// null is no token's.
fn same_token(kept: Option<i64>, token: Option<i64>) -> bool {
    matches!((kept, token), (Some(kept), Some(token)) if kept == token)
}

fn meter_in<'r>(record: Option<&'r InstanceRecord>, meter: &str) -> Option<&'r Value> {
    field(record, NAME, "meters")?.get(meter)
}

fn reserved_in(record: Option<&InstanceRecord>, meter: &str) -> i64 {
    meter_in(record, meter)
        .and_then(|meter| meter.get("reserved"))
        .and_then(integer)
        .unwrap_or(0)
}

fn inner_target(params: &Value) -> Target {
    let text = |name: &str| {
        params
            .get(name)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    };
    Target {
        schema: text("schema"),
        id: text("id"),
    }
}

// inner_of reads an instance that asks a scope operation of this one: its
// reservation of the meter, through its meters field. It refuses one whose
// schema's meter does not draw on a scope link that points here.
fn inner_of(
    cx: &InstanceCx<'_, BudgetConfig>,
    operation: &str,
    meter: &str,
    inner: &Target,
) -> Result<i64> {
    let config = cx.schemas().config(&inner.schema, NAME)?;
    let link = config
        .as_ref()
        .and_then(|config| config.get("meters"))
        .and_then(|meters| meters.get(meter))
        .and_then(|spec| spec.get("scope"))
        .and_then(Value::as_str);
    let record = match link {
        Some(_) => cx
            .instances()
            .get(&inner.schema, &inner.id, Some(&[METERS, LINKS_TARGETS]))?,
        None => None,
    };
    let target = link.and_then(|link| link_in(links_in(record.as_ref()), link));
    if target.is_none_or(|target| target.schema != cx.schema() || target.id != cx.id()) {
        return Err(params_error(
            operation,
            "/id",
            format!(
                "{} {} does not draw meter {meter} from {} {}",
                inner.schema,
                inner.id,
                cx.schema(),
                cx.id()
            ),
        ));
    }
    Ok(reserved_in(record.as_ref(), meter))
}

fn held_for(view: &InstanceView<'_, BudgetConfig>, meter: &str, inner: &Target) -> Result<i64> {
    let table = view.sql().table("holds")?;
    let mut values = key(view);
    values.extend([
        SqlValue::from(meter),
        SqlValue::from(inner.schema.as_str()),
        SqlValue::from(inner.id.as_str()),
    ]);
    let row = view.sql().get(
        format!("SELECT amount FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND meter = ? AND inner_schema = ? AND inner_id = ?"),
        &values,
    )?;
    Ok(match row {
        Some(row) => row.i64("amount")?,
        None => 0,
    })
}

fn hold(cx: &InstanceCx<'_, BudgetConfig>, meter: &str, inner: &Target, amount: i64) -> Result<()> {
    let table = cx.sql().table("holds")?;
    let mut values = key(cx);
    values.extend([
        SqlValue::from(meter),
        SqlValue::from(inner.schema.as_str()),
        SqlValue::from(inner.id.as_str()),
    ]);
    if amount == 0 {
        cx.sql().run(
            format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND meter = ? AND inner_schema = ? AND inner_id = ?"),
            &values,
        )?;
        return Ok(());
    }
    values.push(SqlValue::from(amount));
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, meter, inner_schema, inner_id, amount) VALUES (?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT (namespace, schema, id, meter, inner_schema, inner_id) DO UPDATE SET amount = excluded.amount"
        ),
        &values,
    )?;
    Ok(())
}

// fit refuses a reservation the meter's limit cannot take.
fn fit(
    cx: &InstanceCx<'_, BudgetConfig>,
    operation: &str,
    meter: &str,
    row: &Meter,
    amount: i64,
) -> Result<()> {
    let limit = limit_of(cx, spec_of(cx, meter)?, row)?;
    let Some(limit) = limit else {
        return Ok(());
    };
    if row.used + row.reserved + amount <= limit {
        return Ok(());
    }
    let remaining = (limit - row.used - row.reserved).max(0);
    Err(vetoed(
        cx,
        operation,
        format!("meter {meter} has {remaining} of its limit {limit} left, not {amount}"),
        "over_limit",
        Some(json!({
            "meter": meter,
            "amount": amount,
            "remaining": remaining,
            "limit": limit,
            "scope": {"schema": cx.schema(), "id": cx.id()},
        })),
    ))
}

// pass_up hands a change of a meter on to a scope, as an operation of the
// scope that names this instance: to where the reservation is held for a
// release, to where the scope link points now otherwise. Nothing without a
// scope.
fn pass_up(
    cx: &InstanceCx<'_, BudgetConfig>,
    meter: &str,
    operation: &str,
    params: &[(&str, i64)],
    target: Option<&Target>,
) -> Result<Option<Value>> {
    let Some(target) = target else {
        return Ok(None);
    };
    let mut out = Map::new();
    out.insert("meter".into(), Value::from(meter));
    out.insert("schema".into(), Value::from(cx.schema()));
    out.insert("id".into(), Value::from(cx.id()));
    for (name, value) in params {
        out.insert((*name).to_owned(), Value::from(*value));
    }
    cx.instances()
        .invoke(
            &target.schema,
            &target.id,
            operation,
            &Value::Object(out),
            None,
        )
        .map(Some)
}

fn same(a: Option<&Target>, b: Option<&Target>) -> bool {
    matches!((a, b), (Some(a), Some(b)) if a == b)
}

// take reserves an amount of a meter here and up the chain. Everything the
// instance has reserved is held at one scope: a reservation through
// another, after a new version moved the meter's scope, waits until what
// is held at the first is settled.
fn take(
    cx: &InstanceCx<'_, BudgetConfig>,
    operation: &str,
    meter: &str,
    amount: i64,
    own: Option<Option<i64>>,
) -> Result<Meter> {
    let row = load(cx, meter)?;
    fit(cx, operation, meter, &row, amount)?;
    let scope = scope_of(cx, meter)?;
    if let (Some(scope), Some(held)) = (&scope, &row.scope) {
        if row.reserved > 0 && scope != held {
            return Err(vetoed(
                cx,
                operation,
                format!(
                    "meter {meter} has {} reserved through {} {}; settle it before reserving through {} {}",
                    row.reserved, held.schema, held.id, scope.schema, scope.id
                ),
                "scope_moved",
                None,
            ));
        }
    }
    let mut values = vec![("reserved", SqlValue::from(row.reserved + amount))];
    if let Some(token) = own {
        values.push(("reservation", SqlValue::from(row.reservation + amount)));
        values.push(("token", SqlValue::from(token)));
    }
    if let Some(scope) = &scope {
        values.push(("scope_schema", SqlValue::from(scope.schema.as_str())));
        values.push(("scope_id", SqlValue::from(scope.id.as_str())));
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
fn release(
    cx: &InstanceCx<'_, BudgetConfig>,
    meter: &str,
    amount: i64,
    own_part: i64,
) -> Result<()> {
    if amount == 0 {
        return Ok(());
    }
    let row = load(cx, meter)?;
    let reservation = row.reservation - own_part;
    let mut values = vec![
        ("reserved", SqlValue::from((row.reserved - amount).max(0))),
        ("reservation", SqlValue::from(reservation)),
    ];
    if reservation == 0 {
        values.push(("token", SqlValue::Null));
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

// settle_own releases the instance's own reservation of a meter.
fn settle_own(cx: &InstanceCx<'_, BudgetConfig>, meter: &str) -> Result<i64> {
    let amount = row_of(cx, meter)?.reservation;
    release(cx, meter, amount, amount)?;
    Ok(amount)
}

// settle_ended settles the own reservations made under a lease that is no
// longer the active one, after a Lease or Queue operation and before a
// reserve. The lease is read only when one is outstanding.
fn settle_ended(cx: &InstanceCx<'_, BudgetConfig>) -> Result<()> {
    let table = cx.sql().table("meters")?;
    let select = format!(
        "SELECT meter, token FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND reservation > 0"
    );
    let outstanding = cx.sql().all(
        Sql::per(
            format!("{select} ORDER BY meter"),
            format!("{select} ORDER BY meter COLLATE \"C\""),
        ),
        &key(cx),
    )?;
    if outstanding.is_empty() {
        return Ok(());
    }
    let active = active_token(lease_of(cx)?.as_ref());
    for row in &outstanding {
        let meter = row.text("meter")?;
        if !same_token(row.opt_i64("token")?, active) && cx.config().meter(meter).is_some() {
            settle_own(cx, meter)?;
        }
    }
    Ok(())
}

// tolerated is whether a refusal of a call leaves the usage as it is: a
// veto, or a permission the caller lacks.
fn tolerated(error: &Error) -> bool {
    error.is_veto() || error.code() == Some(EngineErrorCode::Forbidden)
}

// direct sends onExceeded's directive to the holder of the active lease,
// once per meter per lease token. A refusal of Lease's direct, a
// permission the caller lacks say, sends none; usage is never refused.
fn direct(cx: &InstanceCx<'_, BudgetConfig>, meter: &str, overrun: &Overrun) -> Result<bool> {
    let Some(name) = &cx.config().on_exceeded else {
        return Ok(false);
    };
    let Some(lease) = lease_of(cx)? else {
        return Ok(false);
    };
    if !lease.active || same_token(row_of(cx, meter)?.directed, lease.token) {
        return Ok(false);
    }
    let sent = cx.call(
        "Lease",
        "direct",
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
        Err(error) if tolerated(&error) => return Ok(false),
        Err(error) => return Err(error),
    }
    save(cx, meter, &[("directed", SqlValue::from(lease.token))])?;
    Ok(true)
}

// pass_usage hands usage of a meter on to the scope its link points at
// now, with the part of the reservation it released, which is released
// where the reservation is held; the two are one scope unless a new
// version moved the meter's scope. It returns the scopes' overruns.
fn pass_usage(
    cx: &InstanceCx<'_, BudgetConfig>,
    meter: &str,
    amount: i64,
    released: i64,
    held: Option<&Target>,
) -> Result<Vec<Value>> {
    let scope = scope_of(cx, meter)?;
    let mut carried = released;
    if released > 0 && held.is_some() && !same(held, scope.as_ref()) {
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

// count adds usage of a meter here, releases freed of what is reserved,
// and reports the instance when it is then over its limit, after its
// escalate has moved its status.
fn count(
    cx: &InstanceCx<'_, BudgetConfig>,
    meter: &str,
    amount: i64,
    freed: i64,
    own_part: i64,
) -> Result<(Meter, Option<Overrun>)> {
    let row = load(cx, meter)?;
    let used = row.used + amount;
    let reservation = row.reservation - own_part;
    let mut values = vec![
        ("used", SqlValue::from(used)),
        ("reserved", SqlValue::from((row.reserved - freed).max(0))),
        ("reservation", SqlValue::from(reservation)),
    ];
    if reservation == 0 {
        values.push(("token", SqlValue::Null));
    }
    save(cx, meter, &values)?;
    let limit = limit_of(cx, spec_of(cx, meter)?, &row)?;
    let row = Meter { used, ..row };
    match limit {
        Some(limit) if used > limit => {
            let overrun = Overrun {
                schema: cx.schema().to_owned(),
                id: cx.id().to_owned(),
                used,
                limit,
                escalated: escalate(cx)?,
            };
            Ok((row, Some(overrun)))
        }
        _ => Ok((row, None)),
    }
}

// escalate moves the status with the config's escalate, from its from
// states, through Workflow's transition, as the caller. Usage is never
// refused, so a veto or a permission the caller lacks leaves the status.
fn escalate(cx: &InstanceCx<'_, BudgetConfig>) -> Result<bool> {
    let Some(rule) = &cx.config().escalate else {
        return Ok(false);
    };
    let record = cx
        .instances()
        .get(cx.schema(), cx.id(), Some(&[WORKFLOW_STATUS]))?;
    let status = field(record.as_ref(), "Workflow", "status").and_then(Value::as_str);
    if !status.is_some_and(|status| rule.from.iter().any(|from| from == status)) {
        return Ok(false);
    }
    match cx.call("Workflow", "transition", &json!({"to": rule.transition})) {
        Ok(_) => Ok(true),
        Err(error) if tolerated(&error) => Ok(false),
        Err(error) => Err(error),
    }
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

// check_reserve answers whether a plan would fit now: here, after the own
// reservation of an ended lease is settled as reserve settles it first,
// and at each scope up the chain, read through its meters and Links
// targets fields, less what that settlement releases there. When it does
// not fit, until is the next UTC day's start if the daily meters starting
// again make it fit. Each scope comes with the values of it the answer
// turns on: its remaining crosses the amount less the release exactly when
// the reservation's fit there flips; where a daily meter keeps it out now,
// its reserved crossing the limit less the amount and the release, or a
// new limit, flips whether the next day lets it in; and its own scope link
// moves the chain. Where the release is not settled yet, each number is
// given as it is now and as the settlement leaves it: a Lease or Queue
// operation settles it in Budget's after_change, which may run after
// another behavior has read this answer in the same write.
fn check_reserve(
    view: &InstanceView<'_, BudgetConfig>,
    plan: &[(String, i64)],
) -> Result<ReserveCheck> {
    let active = active_token(lease_of(view)?.as_ref());
    let mut scopes: Vec<ScopeRead> = Vec::new();
    let mut fits = true;
    let mut later = true;
    for (meter, amount) in plan {
        let (meter, amount) = (meter.as_str(), *amount);
        let spec = spec_of(view, meter)?;
        let row = row_of(view, meter)?;
        let stale = if view.config().leased && row.reservation > 0 && !same_token(row.token, active)
        {
            row.reservation
        } else {
            0
        };
        let reserved = row.reserved - stale;
        let limit = limit_of(view, spec, &row)?;
        let used = used_now(view, spec, &row);
        if let Some(limit) = limit {
            fits &= used + reserved + amount <= limit;
            later &= (if spec.daily { 0 } else { used }) + reserved + amount <= limit;
        }
        let mut target = scope_of(view, meter)?;
        if target.is_some()
            && reserved > 0
            && row.scope.is_some()
            && !same(row.scope.as_ref(), target.as_ref())
        {
            fits = false;
            later = false;
        }
        // The settlement goes where the reservation is held, and on up from
        // there.
        let released = if stale > 0 && same(row.scope.as_ref(), target.as_ref()) {
            stale
        } else {
            0
        };
        let unsettled: &[i64] = if released > 0 { &[released, 0] } else { &[0] };
        let mut walked: Vec<String> = Vec::new();
        while let Some(at) = target.take() {
            if walked.len() >= MAX_CHAIN || walked.contains(&at.key()) {
                break;
            }
            walked.push(at.key());
            let record =
                view.instances()
                    .get(&at.schema, &at.id, Some(&[METERS, LINKS_TARGETS]))?;
            let Some(record) = record else {
                break;
            };
            let index = match scopes
                .iter()
                .position(|scope| scope.schema == at.schema && scope.id == at.id)
            {
                Some(index) => index,
                None => {
                    scopes.push(ScopeRead {
                        schema: at.schema.clone(),
                        id: at.id.clone(),
                        hears: Vec::new(),
                    });
                    scopes.len() - 1
                }
            };
            let config = view.schemas().config(&at.schema, NAME)?;
            let theirs = config
                .as_ref()
                .and_then(|config| config.get("meters"))
                .and_then(|meters| meters.get(meter));
            let counted = meter_in(Some(&record), meter);
            let (Some(theirs), Some(counted)) = (theirs, counted) else {
                break;
            };
            let scope = &mut scopes[index];
            for unsettled in unsettled {
                hear(
                    scope,
                    format!("/behaviors/{NAME}/meters/{meter}/remaining"),
                    Some(amount - unsettled),
                );
            }
            let daily = theirs.get("reset").and_then(Value::as_str) == Some("daily");
            if let Some(limit) = counted.get("limit").and_then(integer) {
                let counted_used = counted.get("used").and_then(integer).unwrap_or(0);
                let held =
                    (counted.get("reserved").and_then(integer).unwrap_or(0) - released).max(0);
                let now = counted_used + held + amount <= limit;
                fits &= now;
                later &= (if daily { 0 } else { counted_used }) + held + amount <= limit;
                if daily && !now {
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
            let link = theirs.get("scope").and_then(Value::as_str);
            if let Some(link) = link {
                hear(scope, format!("/behaviors/Links/targets/{link}"), None);
                target = link_in(links_in(Some(&record)), link);
            }
        }
    }
    Ok(ReserveCheck {
        fits,
        until: (!fits && later).then(|| day_start(view.now()) + DAY_MS),
        scopes,
    })
}

// check_integer_field holds limitField and reserveField to an integer field
// of the type.
fn check_integer_field(target: &ConfigTarget<'_>, at: &str, field: &str) -> Result<()> {
    if !target.fields().iter().any(|known| known == field) {
        return Err(Error::config(format!(
            "{at} \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            target.fields().join(", ")
        )));
    }
    let declared = target
        .field_schemas()
        .get(field)
        .and_then(|schema| schema.get("type"));
    let types: Vec<Option<&str>> = match declared {
        Some(Value::Array(types)) => types.iter().map(Value::as_str).collect(),
        other => vec![other.and_then(Value::as_str)],
    };
    if !types.contains(&Some("integer"))
        || types
            .iter()
            .any(|one| *one != Some("integer") && *one != Some("null"))
    {
        return Err(Error::config(format!(
            "{at} \"{field}\" is not an integer field of {}",
            target.type_name()
        )));
    }
    Ok(())
}

// check_scope holds a meter's scope to a link of the type's Links config
// and, when the schema is defined or published, the link's target schema
// to one that composes Budget with the same meter.
fn check_scope(target: &ConfigTarget<'_>, meter: &str, link: &str) -> Result<()> {
    let at = format!("meter {meter}: scope {link}");
    if !target.behaviors().iter().any(|name| name == "Links") {
        return Err(Error::config(format!(
            "{at} is a link of Links, which the type does not list"
        )));
    }
    let links = target
        .configs()
        .get("Links")
        .and_then(|config| config.get("links"))
        .and_then(Value::as_object);
    let Some(spec) = links.and_then(|links| links.get(link)) else {
        // A Links config of the wrong shape is Links' to refuse.
        if let Some(links) = links {
            return Err(Error::config(format!(
                "{at} is not a link of the type's Links config (its links: {})",
                js_keys(links).join(", ")
            )));
        }
        return Ok(());
    };
    if link_many(spec) {
        return Err(Error::config(format!(
            "{at} holds a list of targets, and a meter draws on one scope"
        )));
    }
    let (Some(schemas), Some(schema)) =
        (target.schemas(), spec.get("schema").and_then(Value::as_str))
    else {
        return Ok(());
    };
    let Some(scope) = schemas.get(schema)? else {
        return Err(Error::config(format!(
            "{at}: schema {schema}, which the link points at, has no live version; publish it first"
        )));
    };
    if !scope.behaviors.iter().any(|name| name == NAME) {
        return Err(Error::config(format!(
            "{at}: {schema}, which the link points at, does not compose Budget"
        )));
    }
    let meters = scope
        .configs
        .get(NAME)
        .and_then(|config| config.get("meters"))
        .and_then(Value::as_object);
    if meters.is_none_or(|meters| !meters.contains_key(meter)) {
        return Err(Error::config(format!(
            "{at}: {schema}, which the link points at, has no meter {meter} (its meters: {})",
            meters.map(js_keys).unwrap_or_default().join(", ")
        )));
    }
    Ok(())
}

fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

// check_escalate holds escalate to the type's Workflow: every state one of
// its states, and a transition from each from state to the target.
fn check_escalate(target: &ConfigTarget<'_>, rule: &BudgetTransition) -> Result<()> {
    if !target.behaviors().iter().any(|name| name == "Workflow") {
        return Err(Error::config(
            "escalate moves the status through Workflow, which the type does not list",
        ));
    }
    let flow = target.configs().get("Workflow");
    let states = flow
        .and_then(|flow| flow.get("states"))
        .and_then(Value::as_array);
    let transitions = flow
        .and_then(|flow| flow.get("transitions"))
        .and_then(Value::as_array);
    // A Workflow config its own checks refuse is reported there.
    let (Some(states), Some(transitions)) = (states, transitions) else {
        return Ok(());
    };
    let states: Vec<&str> = states.iter().filter_map(Value::as_str).collect();
    if !states.contains(&rule.transition.as_str()) {
        return Err(Error::config(format!(
            "escalate.transition \"{}\" is not a state of the type's Workflow ({})",
            rule.transition,
            states.join(", ")
        )));
    }
    for from in &rule.from {
        if !states.contains(&from.as_str()) {
            return Err(Error::config(format!(
                "escalate.from names \"{from}\", which is not a state of the type's Workflow ({})",
                states.join(", ")
            )));
        }
        if *from == rule.transition {
            return Err(Error::config(format!(
                "escalate moves the status from \"{from}\" to itself"
            )));
        }
        let leads = transitions.iter().any(|transition| {
            transition.get("from").and_then(Value::as_str) == Some(from.as_str())
                && transition.get("to").and_then(Value::as_str) == Some(rule.transition.as_str())
        });
        if !leads {
            return Err(Error::config(format!(
                "escalate: no transition of the type's Workflow leads from \"{from}\" to \"{}\"",
                rule.transition
            )));
        }
    }
    Ok(())
}

/// Budget: reserve-then-settle meters on an instance and its enclosing
/// scopes.
#[derive(Clone, Copy, Debug, Default)]
pub struct Budget;

impl Behavior for Budget {
    type Config = BudgetConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Budget.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to the type:
    // its fields, its Links config and the schemas its scopes point at.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<BudgetConfig> {
        let mut meters = Vec::new();
        if let Some(given) = raw.get("meters").and_then(Value::as_object) {
            for name in js_keys(given) {
                let meter = &given[name];
                let text = |key: &str| meter.get(key).and_then(Value::as_str).map(str::to_owned);
                let limit_field = text("limitField");
                let reserve_field = text("reserveField");
                let scope = text("scope");
                if let Some(field) = &limit_field {
                    check_integer_field(target, &format!("meter {name}: limitField"), field)?;
                }
                if let Some(field) = &reserve_field {
                    check_integer_field(target, &format!("meter {name}: reserveField"), field)?;
                }
                if let Some(link) = &scope {
                    check_scope(target, name, link)?;
                }
                meters.push((
                    name.to_owned(),
                    BudgetMeter {
                        limit: meter.get("limit").and_then(integer),
                        limit_field,
                        reserve: meter.get("reserve").and_then(integer),
                        reserve_field,
                        scope,
                        daily: meter.get("reset").and_then(Value::as_str) == Some("daily"),
                    },
                ));
            }
        }
        let on_exceeded = raw
            .get("onExceeded")
            .and_then(|given| given.get("direct"))
            .and_then(Value::as_str)
            .map(str::to_owned);
        let leased = target.behaviors().iter().any(|name| name == "Lease");
        if raw.get("onExceeded").is_some() && !leased {
            return Err(Error::config(
                "onExceeded sends a directive through Lease's direct, which the type does not list",
            ));
        }
        let escalate = raw.get("escalate").map(|rule| BudgetTransition {
            transition: rule
                .get("transition")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            from: strings(rule.get("from")),
        });
        if let Some(rule) = &escalate {
            check_escalate(target, rule)?;
        }
        Ok(BudgetConfig {
            meters,
            limit_permission: raw
                .get("limitPermission")
                .and_then(Value::as_str)
                .map(str::to_owned),
            on_exceeded,
            escalate,
            leased,
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guidance: Some(|_, config, target| crate::guidance::budget::guidance(config, target)),
            config_change: Some(|_, before, after, change| {
                let Some(before) = before else {
                    return Ok(None);
                };
                let Some(after) = after else {
                    return Ok(Some(
                        "the budgets its instances hold, and what enclosing scopes hold for them, would stay behind"
                            .to_owned(),
                    ));
                };
                // No instance holds a meter, nor a scope one for an instance.
                if !change.instances()? {
                    return Ok(None);
                }
                Ok(before
                    .names()
                    .into_iter()
                    .find(|name| after.meter(name).is_none())
                    .map(|name| {
                        format!(
                            "meter {name} is gone, and its instances and the scopes they draw on may hold it"
                        )
                    }))
            }),
            migrations: vec![BehaviorMigration::new(1, "budget")
                .table(
                    TableSpec::new("meters")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("meter", ColumnSpec::text().not_null())
                        .column("used", ColumnSpec::integer().not_null().default(0))
                        .column("reserved", ColumnSpec::integer().not_null().default(0))
                        .column("limit_value", ColumnSpec::integer())
                        .column("period_start", ColumnSpec::integer())
                        .column("reservation", ColumnSpec::integer().not_null().default(0))
                        .column("token", ColumnSpec::integer())
                        .column("directed", ColumnSpec::integer())
                        .column("scope_schema", ColumnSpec::text())
                        .column("scope_id", ColumnSpec::text())
                        .primary_key(&["namespace", "schema", "id", "meter"]),
                )
                .table(
                    TableSpec::new("holds")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("meter", ColumnSpec::text().not_null())
                        .column("inner_schema", ColumnSpec::text().not_null())
                        .column("inner_id", ColumnSpec::text().not_null())
                        .column("amount", ColumnSpec::integer().not_null())
                        .primary_key(&[
                            "namespace",
                            "schema",
                            "id",
                            "meter",
                            "inner_schema",
                            "inner_id",
                        ]),
                )],
            guard: Some(guard),
            operations: vec![
                ("reserve", reserve),
                ("checkReserve", |_, cx, params| {
                    let plan = plan_of(cx, "checkReserve", params)?;
                    Ok(check_reserve(cx, &plan)?.to_json())
                }),
                ("recordUsage", record_usage),
                ("settle", settle),
                ("setLimit", set_limit),
                ("reserveFor", reserve_for),
                ("settleFor", settle_for),
                ("recordUsageFor", record_usage_for),
            ],
            fields: vec![("meters", |_, view| {
                let found = rows(view)?;
                let mut out = Map::new();
                for (name, spec) in &view.config().meters {
                    let row = found
                        .iter()
                        .find(|(meter, _)| meter == name)
                        .map(|(_, row)| row.clone())
                        .unwrap_or_default();
                    out.insert(name.clone(), record_of(view, spec, &row)?);
                }
                Ok(Some(Value::Object(out)))
            })],
            after_change: Some(|_, cx, change| match change {
                InstanceChange::Delete => {
                    // Everything the instance reserved and holds goes back
                    // where it is held; the scope reads the instance as
                    // gone, so nothing stays held.
                    for (meter, row) in rows(cx)? {
                        if row.reserved > 0 {
                            pass_up(
                                cx,
                                &meter,
                                "settleFor",
                                &[("amount", row.reserved)],
                                row.scope.as_ref(),
                            )?;
                        }
                    }
                    for table in ["meters", "holds"] {
                        let table = cx.sql().table(table)?;
                        cx.sql().run(
                            format!(
                                "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                            ),
                            &key(cx),
                        )?;
                    }
                    Ok(())
                }
                InstanceChange::Operation { behavior, .. }
                    if cx.config().leased && (*behavior == "Lease" || *behavior == "Queue") =>
                {
                    settle_ended(cx)
                }
                _ => Ok(()),
            }),
            ..Hooks::default()
        }
    }
}

// guard keeps a scope link put while a reservation is held through it, and
// a limitField to limitPermission and above what is committed.
fn guard(
    _: &Budget,
    view: &InstanceView<'_, BudgetConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    match request {
        // A create is let through: the scope links and the limitField it
        // gives are the instance's from its start, before anything is used
        // or reserved through them.
        GuardRequest::Create { .. } => Ok(None),
        // A scope link stays put while a reservation is held through it, so
        // a reservation is released where it is held.
        GuardRequest::Operation {
            behavior: "Links",
            operation: "link" | "unlink",
            params,
            ..
        } => {
            let link = params.get("name");
            for (meter, spec) in &view.config().meters {
                if spec.scope.is_none() || spec.scope.as_deref() != link.and_then(Value::as_str) {
                    continue;
                }
                let reserved = row_of(view, meter)?.reserved;
                if reserved > 0 {
                    let mut details = Map::new();
                    details.insert("meter".into(), Value::from(meter.as_str()));
                    details.insert("reserved".into(), Value::from(reserved));
                    return Ok(Some(Veto {
                        reason: format!(
                            "meter {meter} has {reserved} reserved through link {}; settle it before the link changes",
                            js_string(link)
                        ),
                        code: Some("scope_reserved".to_owned()),
                        details: Some(details),
                    }));
                }
            }
            Ok(None)
        }
        // limitField changes as a limit does: with limitPermission, and
        // never below what the instance has used and reserved, the config's
        // limit counting for a field the change empties. setLimit's own
        // update() was asked already.
        GuardRequest::Update {
            patch,
            after,
            caller,
            ..
        } if *caller != Some(NAME) => {
            for (meter, spec) in &view.config().meters {
                let Some(field) = &spec.limit_field else {
                    continue;
                };
                if !patch.contains_key(field) {
                    continue;
                }
                let Some(permission) = &view.config().limit_permission else {
                    return Ok(Some(Veto {
                        reason: format!(
                            "{field} holds the limit of meter {meter}, which changes only with limitPermission, and the config names none"
                        ),
                        code: Some("not_configured".to_owned()),
                        details: None,
                    }));
                };
                if !view.can(permission)? {
                    return Err(forbidden(
                        view,
                        &format!("change {field}, the limit of meter {meter}, of"),
                        permission,
                    ));
                }
                let limit = field_limit(spec, after.get(field));
                let row = row_of(view, meter)?;
                let committed = used_now(view, spec, &row) + row.reserved;
                if let Some(limit) = limit.filter(|limit| *limit < committed) {
                    let mut details = Map::new();
                    details.insert("meter".into(), Value::from(meter.as_str()));
                    details.insert("committed".into(), Value::from(committed));
                    return Ok(Some(Veto {
                        reason: format!(
                            "meter {meter} has {committed} used and reserved, more than the limit {limit}"
                        ),
                        code: Some("below_committed".to_owned()),
                        details: Some(details),
                    }));
                }
            }
            Ok(None)
        }
        _ => Ok(None),
    }
}

// js_string is String(value) of a link's name: its text, or undefined.
fn js_string(value: Option<&Value>) -> String {
    match value {
        None => "undefined".to_owned(),
        Some(Value::String(text)) => text.clone(),
        Some(other) => superschematic_engine::json::stringify(other),
    }
}

fn reserve(_: &Budget, cx: &OperationCx<'_, BudgetConfig>, params: &Value) -> Result<Value> {
    let plan = plan_of(cx, "reserve", params)?;
    let mut token = None;
    if cx.config().leased {
        let lease = lease_of(cx)?;
        let Some(lease) = lease.filter(|lease| lease.active) else {
            return Err(vetoed(
                cx,
                "reserve",
                "no lease is active, and on a type that composes Lease a reservation is made under the active lease".to_owned(),
                "not_leased",
                None,
            ));
        };
        settle_ended(cx)?;
        token = lease.token;
    }
    let mut reserved = Map::new();
    for (name, wanted) in &plan {
        take(cx, "reserve", name, *wanted, Some(token))?;
        reserved.insert(name.clone(), Value::from(*wanted));
    }
    Ok(json!({ "reserved": reserved }))
}

fn amount_of(params: &Value, name: &str) -> i64 {
    params.get(name).and_then(integer).unwrap_or(0)
}

fn meter_param(params: &Value) -> &str {
    params
        .get("meter")
        .and_then(Value::as_str)
        .unwrap_or_default()
}

fn record_usage(_: &Budget, cx: &OperationCx<'_, BudgetConfig>, params: &Value) -> Result<Value> {
    let meter = meter_param(params);
    known_meter(cx, "recordUsage", meter)?;
    let amount = amount_of(params, "amount");
    let drawn = amount.min(row_of(cx, meter)?.reservation);
    let (row, overrun) = count(cx, meter, amount, drawn, drawn)?;
    let first = overrun.clone();
    let mut overruns: Vec<Value> = overrun.iter().map(Overrun::to_json).collect();
    let upstream = pass_usage(cx, meter, amount, drawn, row.scope.as_ref())?;
    let first = first.or_else(|| upstream.first().and_then(overrun_of));
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

// overrun_of reads an overrun a scope's recordUsageFor returned.
fn overrun_of(value: &Value) -> Option<Overrun> {
    Some(Overrun {
        schema: value.get("schema")?.as_str()?.to_owned(),
        id: value.get("id")?.as_str()?.to_owned(),
        used: value.get("used").and_then(integer)?,
        limit: value.get("limit").and_then(integer)?,
        escalated: value.get("escalated") == Some(&Value::Bool(true)),
    })
}

fn settle(_: &Budget, cx: &OperationCx<'_, BudgetConfig>, params: &Value) -> Result<Value> {
    let meter = params.get("meter").and_then(Value::as_str);
    if let Some(meter) = meter {
        known_meter(cx, "settle", meter)?;
    }
    let names: Vec<&str> = match meter {
        Some(meter) => vec![meter],
        None => cx.config().names(),
    };
    let mut released = Map::new();
    for name in names {
        released.insert(name.to_owned(), Value::from(settle_own(cx, name)?));
    }
    Ok(json!({ "released": released }))
}

fn set_limit(_: &Budget, cx: &OperationCx<'_, BudgetConfig>, params: &Value) -> Result<Value> {
    let Some(permission) = &cx.config().limit_permission else {
        return Err(vetoed(
            cx,
            "setLimit",
            "its config names no limitPermission, which changing a limit needs".to_owned(),
            "not_configured",
            None,
        ));
    };
    if !cx.can(permission)? {
        return Err(forbidden(cx, "change a limit of", permission));
    }
    let meter = meter_param(params);
    let spec = known_meter(cx, "setLimit", meter)?;
    let limit = amount_of(params, "limit");
    let row = row_of(cx, meter)?;
    let previous = limit_of(cx, spec, &row)?;
    let committed = used_now(cx, spec, &row) + row.reserved;
    if limit < committed {
        return Err(vetoed(
            cx,
            "setLimit",
            format!("meter {meter} has {committed} used and reserved, more than the limit {limit}"),
            "below_committed",
            Some(json!({"meter": meter, "committed": committed})),
        ));
    }
    match &spec.limit_field {
        Some(field) => {
            let mut patch = Map::new();
            patch.insert(field.clone(), Value::from(limit));
            cx.update(&Value::Object(patch))?;
        }
        None => {
            load(cx, meter)?;
            save(cx, meter, &[("limit_value", SqlValue::from(limit))])?;
        }
    }
    Ok(json!({ "meter": meter, "limit": limit, "previous": previous }))
}

fn reserve_for(_: &Budget, cx: &OperationCx<'_, BudgetConfig>, params: &Value) -> Result<Value> {
    let meter = meter_param(params);
    known_meter(cx, "reserveFor", meter)?;
    let inner = inner_target(params);
    let amount = amount_of(params, "amount");
    let reserved = inner_of(cx, "reserveFor", meter, &inner)?;
    let held = held_for(cx, meter, &inner)?;
    if held + amount > reserved {
        return Err(vetoed(
            cx,
            "reserveFor",
            format!(
                "{} {} has {reserved} of meter {meter} reserved and {held} of it is held here, so {amount} more is not its to hold",
                inner.schema, inner.id
            ),
            "exceeds_reservation",
            None,
        ));
    }
    take(cx, "reserveFor", meter, amount, None)?;
    hold(cx, meter, &inner, held + amount)?;
    Ok(json!({ "held": held + amount }))
}

fn settle_for(_: &Budget, cx: &OperationCx<'_, BudgetConfig>, params: &Value) -> Result<Value> {
    let meter = meter_param(params);
    known_meter(cx, "settleFor", meter)?;
    let inner = inner_target(params);
    let held = held_for(cx, meter, &inner)?;
    let still = if held == 0 {
        0
    } else {
        let record = cx
            .instances()
            .get(&inner.schema, &inner.id, Some(&[METERS]))?;
        reserved_in(record.as_ref(), meter)
    };
    let released = amount_of(params, "amount").min((held - still).max(0));
    if released > 0 {
        hold(cx, meter, &inner, held - released)?;
        release(cx, meter, released, 0)?;
    } else {
        // Nothing held here is the inner instance's to release.
        cx.unchanged();
    }
    Ok(json!({ "released": released }))
}

fn record_usage_for(
    _: &Budget,
    cx: &OperationCx<'_, BudgetConfig>,
    params: &Value,
) -> Result<Value> {
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
