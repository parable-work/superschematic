//! Stock, units a supply counts (D70): an inventory record per product, a
//! ticket type, a coupon's redemptions. An instance keeps what is on hand
//! and what claims hold, as two columns of its own, and every change of
//! what is on hand is a row of an append-only movement table: its kind
//! (`receive`, `adjust` and `count` by a caller with the config's
//! permission, `commit` and `return` by a claim), its delta, what was on
//! hand after it, its reason and reference, the claim and item it was
//! for, who made it and when. `on_hand` always equals the sum of the
//! movements' deltas, and `held` the sum of the quantities the supply's
//! records hold.
//!
//! The fields: `Stock.onHand`, `Stock.held`, `Stock.available` (`onHand −
//! held`, never below 0 unless `backorder`), and `Stock.low`, true while
//! available is at or below `lowAt`, a count or an own integer field.
//! `low` is volatile, since a new version's `lowAt` moves it without an
//! event, and it reads the config as it is now. A list filters on it
//! (`where: { "Stock.low": true }`) through a column of its own, which
//! each write of the instance sets: after a version changes a literal
//! `lowAt`, the filter answers by the old one for an instance until its
//! next write, while the field answers by the new one at once.
//!
//! The supply contract ([`crate::supply`]): `holdFor` holds what an item
//! asks while it fits what is available (`unavailable`, its details giving
//! what is available) and is within `maxPerHold` (`over_max`), and returns
//! the unit price from the config's price field, with its currency;
//! `commitFor` takes the item's units out of what is on hand and what is
//! held (they are sold), a `commit` movement, and without `backorder` is
//! `unavailable` while that would leave fewer than 0 on hand, as a count
//! with the override can; `releaseFor` frees them; `returnFor` puts what
//! the item committed back on hand, a `return` movement; `checkHold`
//! answers whether `holdFor` would fit now, beside the quantities its
//! `also` lists. Each takes a call only for an item its claim's
//! `Holds.items` already shows in the state the call moves the supply's
//! record to, and a call made again changes nothing (`not_requested`
//! otherwise). With `backorder` a hold may take more than is available;
//! no count passes what a JSON number holds exactly.
//!
//! `receiveStock`, `adjustStock` and `countStock` need the config's
//! permission (forbidden without it). An adjustment down that leaves fewer
//! on hand than claims hold is `below_held` unless the config allows
//! backorder, and one below 0 always; one up is taken even while it
//! leaves too few. A count below what claims hold is
//! `below_held` unless the caller holds the permission's `.override`. An
//! adjustment of 0 and a count of what is on hand change nothing and
//! record no movement. `listStockMovements` pages the movements newest
//! first, by D60's cursor. Every operation that moves a count appends the
//! instance's own event, with `Stock.onHand`, `held` and `available` in
//! its diff.
//!
//! An instance is not deleted while it holds units for a claim (`held`);
//! what claims committed is sold, a deleted claim's included, and its
//! records go with the instance.
//!
//! configChange: the price field, `lowAt`, `maxPerHold` and the permission
//! may change; `backorder` may turn off only while no instance is below
//! zero or holds more than it has on hand, which afterConfigChange checks
//! from Stock's own tables. Stock joins a schema that has instances, each at zero, and
//! leaves only one that has none, since its movements would be lost.
//!
//! Storage (D58): two columns and a filter column on the instance, the
//! movement table keyed by instance and sequence, and the contract's
//! record table ([`supply::rows_table`]); every statement is the same on
//! both databases.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorFilter, BehaviorMigration, ColumnSpec, InstanceCx, InstanceView,
    OperationCx, PublishCx, TableSpec,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::json::number_value;
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, Result,
};

use crate::currency;
use crate::supply::{
    self, Ask, Begun, CheckAnswer, Free, Price, Refusal, SupplyCall, SupplyKind, SupplyOperation,
    Unavailable,
};

pub(crate) const NAME: &str = "Stock";

/// The contract's record table.
const HOLDS_TABLE: &str = "holds";
/// The append-only movement table.
const MOVEMENTS_TABLE: &str = "movements";

/// The largest integer a JavaScript number holds exactly.
const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

/// `over_max`: a hold of more than `maxPerHold`.
pub const OVER_MAX: &str = "over_max";
/// `below_held`: an adjustment or a count below what claims hold.
pub const BELOW_HELD: &str = "below_held";

const MOVEMENT_COLUMNS: &str =
    "seq, kind, delta, on_hand, reason, reference, holder_schema, holder_id, hold, item, actor, at";

/// Where a price's currency comes from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CurrencySource {
    /// An ISO 4217 code the config gives.
    Code(String),
    /// An own `Finance.Currency` field of the type, by JSON key.
    Field(String),
}

/// The unit price `holdFor` returns.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StockPrice {
    /// An own integer field of the type, in the currency's minor units.
    pub field: String,
    pub currency: CurrencySource,
}

/// What `Stock.low` compares available with.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LowAt {
    Count(i64),
    /// An own integer field of the type, while it holds one.
    Field(String),
}

/// Stock's config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StockConfig {
    /// What receiving, adjusting and counting need; a count below what is
    /// held needs its `.override`.
    pub permission: String,
    pub price: Option<StockPrice>,
    pub low_at: Option<LowAt>,
    pub max_per_hold: Option<i64>,
    pub backorder: bool,
}

/// An instance's counts.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
struct Counts {
    on_hand: i64,
    held: i64,
}

impl Counts {
    // free is on hand less held, within what JavaScript holds exactly.
    fn free(&self) -> i64 {
        self.on_hand
            .saturating_sub(self.held)
            .clamp(-MAX_SAFE_INTEGER, MAX_SAFE_INTEGER)
    }

    // available is what a new hold can take: never below 0 unless the
    // config allows backorder.
    fn available(&self, config: &StockConfig) -> i64 {
        let free = self.free();
        if config.backorder {
            free
        } else {
            free.max(0)
        }
    }
}

// key is the instance's rows as Stock's SQL names them.
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
    (value.is_finite() && value.fract() == 0.0 && value.abs() <= MAX_SAFE_INTEGER as f64)
        .then_some(value as i64)
}

fn counts<C>(view: &InstanceView<'_, C>) -> Result<Counts> {
    let row = view.columns().get()?;
    Ok(Counts {
        on_hand: row.opt_i64("on_hand")?.unwrap_or(0),
        held: row.opt_i64("held")?.unwrap_or(0),
    })
}

// low_at is the count Stock.low compares available with now; `None`
// without lowAt, or while its field holds no integer.
fn low_at(view: &InstanceView<'_, StockConfig>) -> Result<Option<i64>> {
    Ok(match &view.config().low_at {
        None => None,
        Some(LowAt::Count(count)) => Some(*count),
        Some(LowAt::Field(field)) => view.data()?.get(field).and_then(integer),
    })
}

fn is_low(view: &InstanceView<'_, StockConfig>, counts: &Counts) -> Result<bool> {
    Ok(low_at(view)?.is_some_and(|at| counts.available(view.config()) <= at))
}

// save sets the counts and the filter column low.
fn save(cx: &InstanceCx<'_, StockConfig>, counts: &Counts) -> Result<()> {
    let low = is_low(cx, counts)?;
    cx.columns().set(&[
        ("on_hand", SqlValue::from(counts.on_hand)),
        ("held", SqlValue::from(counts.held)),
        ("low", SqlValue::from(i64::from(low))),
    ])
}

// result is what receiveStock, adjustStock and countStock return.
fn result(config: &StockConfig, counts: &Counts, movement: Option<i64>) -> Value {
    json!({
        "onHand": counts.on_hand,
        "held": counts.held,
        "available": counts.available(config),
        "movement": movement,
    })
}

/// A movement to record.
struct Movement<'a> {
    kind: &'a str,
    delta: i64,
    on_hand: i64,
    reason: Option<&'a str>,
    reference: Option<&'a str>,
    call: Option<&'a SupplyCall>,
}

// record appends a movement and returns its sequence number, per instance
// from 1.
fn record(cx: &InstanceCx<'_, StockConfig>, movement: &Movement<'_>) -> Result<i64> {
    let table = cx.sql().table(MOVEMENTS_TABLE)?;
    let last = cx.sql().get(
        format!(
            "SELECT MAX(seq) AS seq FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
        ),
        &key(cx),
    )?;
    let seq = last
        .as_ref()
        .map(|row| row.opt_i64("seq"))
        .transpose()?
        .flatten()
        .unwrap_or(0)
        + 1;
    let mut values = key(cx);
    values.extend([
        SqlValue::from(seq),
        SqlValue::from(movement.kind),
        SqlValue::from(movement.delta),
        SqlValue::from(movement.on_hand),
        SqlValue::from(movement.reason),
        SqlValue::from(movement.reference),
        SqlValue::from(movement.call.map(|call| call.holder.schema.as_str())),
        SqlValue::from(movement.call.map(|call| call.holder.id.as_str())),
        SqlValue::from(movement.call.map(|call| call.hold.as_str())),
        SqlValue::from(movement.call.map(|call| call.item)),
        SqlValue::from(cx.principal().subject.as_str()),
        SqlValue::from(cx.now()),
    ]);
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, {MOVEMENT_COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
        ),
        &values,
    )?;
    Ok(seq)
}

fn movement_of(row: &Row) -> Result<Value> {
    let mut out = Map::new();
    out.insert("seq".into(), Value::from(row.i64("seq")?));
    out.insert("kind".into(), Value::from(row.text("kind")?));
    out.insert("delta".into(), Value::from(row.i64("delta")?));
    out.insert("onHand".into(), Value::from(row.i64("on_hand")?));
    if let Some(reason) = row.opt_text("reason")? {
        out.insert("reason".into(), Value::from(reason));
    }
    if let Some(reference) = row.opt_text("reference")? {
        out.insert("reference".into(), Value::from(reference));
    }
    if let (Some(schema), Some(id)) = (row.opt_text("holder_schema")?, row.opt_text("holder_id")?) {
        out.insert("holder".into(), json!({ "schema": schema, "id": id }));
    }
    if let Some(hold) = row.opt_text("hold")? {
        out.insert("hold".into(), Value::from(hold));
    }
    if let Some(item) = row.opt_i64("item")? {
        out.insert("item".into(), Value::from(item));
    }
    out.insert("actor".into(), Value::from(row.text("actor")?));
    out.insert("at".into(), Value::from(row.i64("at")?));
    Ok(Value::Object(out))
}

fn vetoed<C>(
    view: &InstanceView<'_, C>,
    operation: &str,
    reason: String,
    code: &str,
    details: Value,
) -> Error {
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: match details {
                Value::Object(details) => Some(details),
                _ => None,
            },
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

fn params_error(operation: &str, path: &str, message: &str) -> Error {
    EngineError::operation_params(NAME, operation, vec![SchemaIssue::new(path, message)]).into()
}

// permitted refuses a caller without the config's permission.
fn permitted(cx: &OperationCx<'_, StockConfig>, what: &str) -> Result<()> {
    let permission = &cx.config().permission;
    if !cx.can(permission)? {
        return Err(forbidden(cx, what, permission));
    }
    Ok(())
}

// on_hand_after is what is on hand after a change, refused as the
// operation's parameters past what JavaScript holds exactly.
fn on_hand_after(operation: &str, path: &str, on_hand: i64, delta: i64) -> Result<i64> {
    count_after(operation, path, "on hand", on_hand, delta)
}

// count_after is a count after a change, refused as the operation's
// parameters past what JavaScript holds exactly: on hand, or held, which
// a backorder hold may raise past what is on hand.
fn count_after(operation: &str, path: &str, what: &str, count: i64, delta: i64) -> Result<i64> {
    count
        .checked_add(delta)
        .filter(|after| after.abs() <= MAX_SAFE_INTEGER)
        .ok_or_else(|| {
            params_error(
                operation,
                path,
                &format!("would leave more {what} than a JSON number holds exactly"),
            )
        })
}

fn text<'p>(params: &'p Value, name: &str) -> Option<&'p str> {
    params.get(name).and_then(Value::as_str)
}

fn receive_stock(_: &Stock, cx: &OperationCx<'_, StockConfig>, params: &Value) -> Result<Value> {
    permitted(cx, "receive stock of")?;
    let quantity = params.get("quantity").and_then(integer).unwrap_or(0);
    let mut counts = counts(cx)?;
    counts.on_hand = on_hand_after("receiveStock", "/quantity", counts.on_hand, quantity)?;
    let seq = record(
        cx,
        &Movement {
            kind: "receive",
            delta: quantity,
            on_hand: counts.on_hand,
            reason: text(params, "reason"),
            reference: text(params, "reference"),
            call: None,
        },
    )?;
    save(cx, &counts)?;
    Ok(result(cx.config(), &counts, Some(seq)))
}

fn adjust_stock(_: &Stock, cx: &OperationCx<'_, StockConfig>, params: &Value) -> Result<Value> {
    permitted(cx, "adjust the stock of")?;
    let delta = params.get("delta").and_then(integer).unwrap_or(0);
    let mut counts = counts(cx)?;
    if delta == 0 {
        cx.unchanged();
        return Ok(result(cx.config(), &counts, None));
    }
    let after = on_hand_after("adjustStock", "/delta", counts.on_hand, delta)?;
    // An adjustment down may not leave fewer than 0, or than claims hold
    // without backorder; one up is taken even while it leaves too few, as
    // a commit after a count with the override, or under backorder, can.
    if delta < 0 && (after < 0 || (after < counts.held && !cx.config().backorder)) {
        return Err(vetoed(
            cx,
            "adjustStock",
            format!(
                "an adjustment of {delta} leaves {after} on hand, {}",
                if after < 0 {
                    "below 0".to_owned()
                } else {
                    format!("fewer than the {} claims hold", counts.held)
                }
            ),
            BELOW_HELD,
            json!({ "onHand": after, "held": counts.held }),
        ));
    }
    counts.on_hand = after;
    let seq = record(
        cx,
        &Movement {
            kind: "adjust",
            delta,
            on_hand: after,
            reason: text(params, "reason"),
            reference: None,
            call: None,
        },
    )?;
    save(cx, &counts)?;
    Ok(result(cx.config(), &counts, Some(seq)))
}

fn count_stock(_: &Stock, cx: &OperationCx<'_, StockConfig>, params: &Value) -> Result<Value> {
    permitted(cx, "count the stock of")?;
    let counted = params.get("onHand").and_then(integer).unwrap_or(0);
    let mut counts = counts(cx)?;
    if counted == counts.on_hand {
        cx.unchanged();
        return Ok(result(cx.config(), &counts, None));
    }
    if counted < counts.held {
        let permission = format!("{}.override", cx.config().permission);
        if !cx.can(&permission)? {
            return Err(vetoed(
                cx,
                "countStock",
                format!(
                    "a count of {counted} is below the {} claims hold; it needs permission {permission}",
                    counts.held
                ),
                BELOW_HELD,
                json!({ "onHand": counted, "held": counts.held }),
            ));
        }
    }
    let delta = counted - counts.on_hand;
    counts.on_hand = counted;
    let seq = record(
        cx,
        &Movement {
            kind: "count",
            delta,
            on_hand: counted,
            reason: text(params, "reason"),
            reference: None,
            call: None,
        },
    )?;
    save(cx, &counts)?;
    Ok(result(cx.config(), &counts, Some(seq)))
}

// fit is what does not fit of a call's quantity now: over maxPerHold, or
// beyond what is available beside the quantities `also` the claim's other
// items ask, unless the config allows backorder.
fn fit(
    view: &InstanceView<'_, StockConfig>,
    call: &SupplyCall,
    also: i64,
) -> Result<Option<Refusal>> {
    let quantity = call.ask.quantity().unwrap_or(0);
    let config = view.config();
    if let Some(max) = config.max_per_hold.filter(|max| quantity > *max) {
        let mut details = Unavailable::of(view, call, Free::Available(0)).details();
        details.remove("available");
        details.insert("max".into(), Value::from(max));
        return Ok(Some(Refusal {
            code: OVER_MAX.to_owned(),
            reason: format!(
                "{quantity} asked of {} {}, and one item holds at most {max}",
                view.schema(),
                view.id()
            ),
            details,
        }));
    }
    let counts = counts(view)?;
    let free = counts.free().saturating_sub(also);
    if config.backorder || quantity <= free {
        return Ok(None);
    }
    let available = free.max(0);
    Ok(Some(
        Unavailable::of(view, call, Free::Available(available)).refusal(if also > 0 {
            format!(
                "{quantity} asked of {} {}, which has {available} available beside the {also} the claim's other items ask",
                view.schema(),
                view.id()
            )
        } else {
            format!(
                "{quantity} asked of {} {}, which has {available} available",
                view.schema(),
                view.id()
            )
        }),
    ))
}

// price_of is the unit price the config's price field holds now, with its
// currency; `None` without a price, or while the field holds no integer.
fn price_of(view: &InstanceView<'_, StockConfig>) -> Result<Option<Price>> {
    let Some(price) = &view.config().price else {
        return Ok(None);
    };
    let data = view.data()?;
    let Some(unit_price) = data.get(&price.field).and_then(integer) else {
        return Ok(None);
    };
    let currency = match &price.currency {
        CurrencySource::Code(code) => Some(code.clone()),
        CurrencySource::Field(field) => data.get(field).and_then(Value::as_str).map(str::to_owned),
    };
    Ok(Some(Price {
        unit_price,
        currency,
    }))
}

// contract runs a writing call of the supply contract: the holder check
// and the record's step, what fits for holdFor, the counts and the
// movement, then the record.
fn contract(
    cx: &OperationCx<'_, StockConfig>,
    params: &Value,
    operation: SupplyOperation,
) -> Result<Value> {
    let call = SupplyCall::parse(NAME, operation, SupplyKind::Counted, params)?;
    let record = match supply::begin(cx, HOLDS_TABLE, operation, &call)? {
        Begun::Done(record) => return Ok(supply::done(cx, operation, record.as_ref())),
        Begun::Apply { recorded, .. } => recorded,
    };
    // A record holds what its item asked when it was taken, which begin
    // held the call to.
    let quantity = record
        .as_ref()
        .and_then(|record| record.ask.quantity())
        .or(call.ask.quantity())
        .unwrap_or(0);
    let mut counts = counts(cx)?;
    let mut price = None;
    match operation {
        SupplyOperation::Hold => {
            if let Some(refusal) = fit(cx, &call, 0)? {
                return Err(refusal.veto(cx, operation));
            }
            counts.held = count_after("holdFor", "/quantity", "held", counts.held, quantity)?;
            price = price_of(cx)?;
        }
        SupplyOperation::Commit => {
            let on_hand = on_hand_after("commitFor", "/quantity", counts.on_hand, -quantity)?;
            // Without backorder nothing is sold that is not on hand: after
            // a count with the override below what claims hold, a commit
            // waits for stock to be received or counted.
            if on_hand < 0 && !cx.config().backorder {
                return Err(
                    Unavailable::of(cx, &call, Free::Available(counts.on_hand.max(0)))
                        .refusal(format!(
                            "{quantity} committed of {} {}, which has {} on hand",
                            cx.schema(),
                            cx.id(),
                            counts.on_hand
                        ))
                        .veto(cx, operation),
                );
            }
            counts.held = count_after("commitFor", "/quantity", "held", counts.held, -quantity)?;
            counts.on_hand = on_hand;
            record_claim(cx, "commit", -quantity, counts.on_hand, &call)?;
        }
        SupplyOperation::Release => {
            counts.held = count_after("releaseFor", "/quantity", "held", counts.held, -quantity)?;
        }
        SupplyOperation::Return => {
            counts.on_hand = on_hand_after("returnFor", "/quantity", counts.on_hand, quantity)?;
            record_claim(cx, "return", quantity, counts.on_hand, &call)?;
        }
        SupplyOperation::Check => {
            return Err(Error::behavior(NAME, "checkHold writes nothing"));
        }
    }
    save(cx, &counts)?;
    supply::finish(cx, HOLDS_TABLE, operation, &call, price)
}

fn record_claim(
    cx: &InstanceCx<'_, StockConfig>,
    kind: &str,
    delta: i64,
    on_hand: i64,
    call: &SupplyCall,
) -> Result<i64> {
    record(
        cx,
        &Movement {
            kind,
            delta,
            on_hand,
            reason: None,
            reference: None,
            call: Some(call),
        },
    )
}

// check_hold answers whether holdFor would hold the item now: an item the
// supply holds already fits, one its record has moved past does not
// (not_requested), and any other fits what is available beside the
// quantities `also` the claim's other items ask.
fn check_hold(_: &Stock, cx: &OperationCx<'_, StockConfig>, params: &Value) -> Result<Value> {
    let call = SupplyCall::parse(NAME, SupplyOperation::Check, SupplyKind::Counted, params)?;
    let also = supply::also(NAME, SupplyKind::Counted, params)?
        .iter()
        .filter_map(Ask::quantity)
        .fold(0_i64, i64::saturating_add);
    let recorded = supply::recorded(cx, HOLDS_TABLE, &call)?;
    let answer = match supply::check_record(&call, recorded.as_ref()) {
        Some(answer) => answer,
        None => match fit(cx, &call, also)? {
            Some(refusal) => CheckAnswer::Refused(refusal),
            None => CheckAnswer::Fits,
        },
    };
    Ok(answer.to_json())
}

fn list_stock_movements(
    _: &Stock,
    cx: &OperationCx<'_, StockConfig>,
    params: &Value,
) -> Result<Value> {
    let request = page_request(NAME, "listStockMovements", params)?;
    let table = cx.sql().table(MOVEMENTS_TABLE)?;
    let mut values = key(cx);
    let before = if request.after > 0 {
        values.push(SqlValue::from(request.after));
        " AND seq < ?"
    } else {
        ""
    };
    values.push(SqlValue::from(request.limit + 1));
    let rows = cx.sql().all(
        format!(
            "SELECT {MOVEMENT_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ?{before} ORDER BY seq DESC LIMIT ?"
        ),
        &values,
    )?;
    let mut numbered = Vec::with_capacity(rows.len());
    for row in &rows {
        numbered.push((row.i64("seq")?, movement_of(row)?));
    }
    let found = page(numbered, request.limit, |(seq, _)| *seq);
    Ok(json!({
        "items": found.items.into_iter().map(|(_, item)| item).collect::<Vec<_>>(),
        "next": found.next,
    }))
}

// held_records is how many items the instance's records hold for claims.
fn held_records(view: &InstanceView<'_, StockConfig>) -> Result<i64> {
    let table = view.sql().table(HOLDS_TABLE)?;
    let row = view.sql().get(
        format!(
            "SELECT COUNT(*) AS n FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND state = 'held'"
        ),
        &key(view),
    )?;
    Ok(match row {
        Some(row) => row.i64("n")?,
        None => 0,
    })
}

// check_integer_field holds a field the config names to an own integer
// field of the type.
fn check_integer_field(target: &ConfigTarget<'_>, at: &str, field: &str) -> Result<()> {
    check_field_type(target, at, field, "integer")
}

fn check_field_type(target: &ConfigTarget<'_>, at: &str, field: &str, kind: &str) -> Result<()> {
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
    if !types.contains(&Some(kind))
        || types
            .iter()
            .any(|one| *one != Some(kind) && *one != Some("null"))
    {
        return Err(Error::config(format!(
            "{at} \"{field}\" is not {} field of {}",
            if kind == "integer" {
                "an integer"
            } else {
                "a string"
            },
            target.type_name()
        )));
    }
    Ok(())
}

fn field_of(value: &Value) -> Option<String> {
    value
        .get("field")
        .and_then(Value::as_str)
        .map(str::to_owned)
}

/// Stock: units a supply counts, held and committed by claims.
#[derive(Clone, Copy, Debug, Default)]
pub struct Stock;

impl Behavior for Stock {
    type Config = StockConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Stock.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to the type's
    // fields and a literal currency to ISO 4217.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<StockConfig> {
        let price = match raw.get("price") {
            Some(price) => {
                let field = field_of(price).unwrap_or_default();
                check_integer_field(target, "price.field", &field)?;
                let currency = match price.get("currency") {
                    Some(Value::String(code)) => {
                        if let Some(refusal) = currency::refusal("price.currency", code) {
                            return Err(Error::config(refusal));
                        }
                        CurrencySource::Code(code.clone())
                    }
                    Some(given) => {
                        let field = field_of(given).unwrap_or_default();
                        check_field_type(target, "price.currency.field", &field, "string")?;
                        CurrencySource::Field(field)
                    }
                    None => return Err(Error::config("price names no currency")),
                };
                Some(StockPrice { field, currency })
            }
            None => None,
        };
        let low_at = match raw.get("lowAt") {
            Some(Value::Number(_)) => raw.get("lowAt").and_then(integer).map(LowAt::Count),
            Some(given) => {
                let field = field_of(given).unwrap_or_default();
                check_integer_field(target, "lowAt.field", &field)?;
                Some(LowAt::Field(field))
            }
            None => None,
        };
        Ok(StockConfig {
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            price,
            low_at,
            max_per_hold: raw.get("maxPerHold").and_then(integer),
            backorder: raw.get("backorder") == Some(&Value::Bool(true)),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, change| {
                // Stock joins a schema with instances, each at zero.
                let Some(_) = before else {
                    return Ok(None);
                };
                if after.is_none() && change.instances()? {
                    return Ok(Some(
                        "its instances' stock and movements would be lost".to_owned(),
                    ));
                }
                Ok(None)
            }),
            after_config_change: Some(after_config_change),
            guidance: Some(|_, config, target| crate::guidance::stock::guidance(config, target)),
            migrations: vec![BehaviorMigration::new(1, "stock")
                .column("on_hand", ColumnSpec::integer().not_null().default(0))
                .column("held", ColumnSpec::integer().not_null().default(0))
                .column("low", ColumnSpec::integer().not_null().default(0))
                .index("low", &["low"])
                .table(
                    TableSpec::new(MOVEMENTS_TABLE)
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("seq", ColumnSpec::integer().not_null())
                        .column("kind", ColumnSpec::text().not_null())
                        .column("delta", ColumnSpec::integer().not_null())
                        .column("on_hand", ColumnSpec::integer().not_null())
                        .column("reason", ColumnSpec::text())
                        .column("reference", ColumnSpec::text())
                        .column("holder_schema", ColumnSpec::text())
                        .column("holder_id", ColumnSpec::text())
                        .column("hold", ColumnSpec::text())
                        .column("item", ColumnSpec::integer())
                        .column("actor", ColumnSpec::text().not_null())
                        .column("at", ColumnSpec::integer().not_null())
                        .primary_key(&["namespace", "schema", "id", "seq"]),
                )
                .table(supply::rows_table(HOLDS_TABLE))],
            guard: Some(|_, view, request| {
                if !matches!(request, GuardRequest::Delete { .. }) {
                    return Ok(None);
                }
                let holding = held_records(view)?;
                Ok((holding > 0).then(|| {
                    let mut details = Map::new();
                    details.insert("held".into(), Value::from(counts(view).map_or(0, |c| c.held)));
                    Veto {
                        reason: format!(
                            "it holds units for {} items of claims; they are released or committed first",
                            holding
                        ),
                        code: Some(supply::HELD.to_owned()),
                        details: Some(details),
                    }
                }))
            }),
            operations: vec![
                ("receiveStock", receive_stock),
                ("adjustStock", adjust_stock),
                ("countStock", count_stock),
                ("holdFor", |_, cx, params| {
                    contract(cx, params, SupplyOperation::Hold)
                }),
                ("commitFor", |_, cx, params| {
                    contract(cx, params, SupplyOperation::Commit)
                }),
                ("releaseFor", |_, cx, params| {
                    contract(cx, params, SupplyOperation::Release)
                }),
                ("returnFor", |_, cx, params| {
                    contract(cx, params, SupplyOperation::Return)
                }),
                ("checkHold", check_hold),
                ("listStockMovements", list_stock_movements),
            ],
            fields: vec![
                ("onHand", |_, view| {
                    Ok(Some(Value::from(counts(view)?.on_hand)))
                }),
                ("held", |_, view| Ok(Some(Value::from(counts(view)?.held)))),
                ("available", |_, view| {
                    Ok(Some(Value::from(counts(view)?.available(view.config()))))
                }),
                ("low", |_, view| {
                    let counts = counts(view)?;
                    Ok(Some(Value::Bool(is_low(view, &counts)?)))
                }),
            ],
            filters: vec![(
                "low",
                BehaviorFilter::new("low", "boolean").described(
                    "Whether available is at or below lowAt, as the instance's last write left it.",
                ),
            )],
            after_change: Some(|_, cx, change| match change {
                InstanceChange::Delete => {
                    let table = cx.sql().table(MOVEMENTS_TABLE)?;
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                        ),
                        &key(cx),
                    )?;
                    supply::forget(cx, HOLDS_TABLE)
                }
                // A create starts at zero; an update may move lowAt's
                // field. The filter column follows.
                InstanceChange::Create | InstanceChange::Update { .. } => {
                    let counts = counts(cx)?;
                    let low = i64::from(is_low(cx, &counts)?);
                    if cx.columns().get()?.opt_i64("low")? != Some(low) {
                        cx.columns().set(&[("low", SqlValue::from(low))])?;
                    }
                    Ok(())
                }
                InstanceChange::Operation { .. } => Ok(()),
            }),
            ..Hooks::default()
        }
    }
}

// after_config_change refuses a version that turns backorder off while an
// instance of the namespace is below zero, or holds more than it has on
// hand, read from Stock's own tables: on hand is the sum of the movements,
// held the sum of the held records.
fn after_config_change(_: &Stock, cx: &PublishCx<'_, StockConfig>) -> Result<()> {
    let (Some(before), Some(after)) = (cx.before(), cx.config()) else {
        return Ok(());
    };
    if !before.backorder || after.backorder {
        return Ok(());
    }
    let holds = cx.sql().table(HOLDS_TABLE)?;
    let movements = cx.sql().table(MOVEMENTS_TABLE)?;
    let key = [
        SqlValue::from(cx.namespace()),
        SqlValue::from(cx.schema()),
        SqlValue::from(cx.namespace()),
        SqlValue::from(cx.schema()),
    ];
    let below = cx.sql().get(
        format!(
            "SELECT id, CAST(SUM(delta) AS BIGINT) AS on_hand FROM {movements} WHERE namespace = ? AND schema = ? GROUP BY id HAVING SUM(delta) < 0 ORDER BY id LIMIT 1"
        ),
        &key[..2],
    )?;
    if let Some(row) = below {
        return Err(Error::config(format!(
            "backorder cannot turn off while {} {} has {} on hand, below zero",
            cx.schema(),
            row.text("id")?,
            row.i64("on_hand")?
        )));
    }
    let over = cx.sql().get(
        format!(
            "SELECT h.id AS id, h.held AS held, COALESCE(m.on_hand, 0) AS on_hand
             FROM (SELECT id, CAST(SUM(quantity) AS BIGINT) AS held FROM {holds} WHERE namespace = ? AND schema = ? AND state = 'held' GROUP BY id) h
             LEFT JOIN (SELECT id, CAST(SUM(delta) AS BIGINT) AS on_hand FROM {movements} WHERE namespace = ? AND schema = ? GROUP BY id) m ON m.id = h.id
             WHERE h.held > COALESCE(m.on_hand, 0)
             ORDER BY h.id
             LIMIT 1"
        ),
        &key,
    )?;
    match over {
        Some(row) => Err(Error::config(format!(
            "backorder cannot turn off while {} {} holds {} units for claims with {} on hand",
            cx.schema(),
            row.text("id")?,
            row.i64("held")?,
            row.i64("on_hand")?
        ))),
        None => Ok(()),
    }
}
