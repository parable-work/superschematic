//! Stock, units a supply counts (D70), on a table (D103's step 4): an
//! inventory record per product, a ticket type, a coupon's redemptions. A
//! row keeps what is on hand and what claims hold, two owned columns, and
//! every change of what is on hand is a row of its append-only `movements`
//! sub-table: its kind (`receive`, `adjust` and `count` by a caller with the
//! config's permission, `commit` and `return` by a claim), its delta, what
//! was on hand after it, its reason and reference, the claim and item it
//! was for, who made it and when, keyed by its sequence on the row from 1.
//! `onHand` always equals the sum of the movements' deltas, and `held` the
//! sum of the quantities the `holds` sub-table's records hold.
//!
//! Stock passes D103's test. State of its own: the counts, the ledger and
//! the record of each claim item it holds. Rules over how it changes: what
//! a hold may take (what is available, `maxPerHold`, `backorder`), what a
//! count or an adjustment may leave (`below_held`), a claim's calls moving
//! each record only forward, and the ledger summing to what is on hand.
//! Recurrence: every shop, box office and booking desk counts what it
//! sells.
//!
//! The fields: `Stock.onHand` and `Stock.held`, owned columns;
//! `Stock.available` (`onHand - held`, never below 0 unless `backorder`)
//! and `Stock.low`, true while available is at or below `lowAt`, a count or
//! an own integer field, which a read computes from the row and the config
//! as it is now, so a new version's `lowAt` moves it at once.
//!
//! The supply contract ([`crate::supply`]): `holdFor` holds what an item
//! asks while it fits what is available (`unavailable`, its details giving
//! what is available) and is within `maxPerHold` (`over_max`), and returns
//! the unit price from the config's price field, with its currency;
//! `commitFor` takes the item's units out of what is on hand and what is
//! held (they are sold), a `commit` movement, and without `backorder` is
//! `unavailable` while that would leave fewer than 0 on hand, as a count
//! with the override can; `releaseFor` frees them; `returnFor` puts what
//! the item committed back on hand, a `return` movement. Each takes a call
//! only for an item its claim's Holds shows in the state the call moves the
//! record to, and a call made again changes nothing (`not_requested`
//! otherwise). `checkHold`, the contract's query, answers whether `holdFor`
//! would fit now, beside the quantities its `also` lists, through the
//! answer hook over the row as a read gave it, and serves the same answer
//! as a read-only operation. With `backorder` a hold may take more than is
//! available; no count passes what a JSON number holds exactly.
//!
//! Two claims for the last unit: each writing call takes the supply's lock
//! ([`crate::supply::lock_key`]) before it reads the row, and writes the
//! row held to the version it read, so the second decides over the first's
//! hold and finds none available.
//!
//! `receiveStock`, `adjustStock` and `countStock` need the config's
//! permission (forbidden without it). An adjustment down that leaves fewer
//! on hand than claims hold is `below_held` unless the config allows
//! backorder, and one below 0 always; one up is taken even while it leaves
//! too few. A count below what claims hold is `below_held` unless the
//! caller holds the permission's `.override`, which under the default
//! dotted permissions the permission covers. An adjustment of 0 and a count
//! of what is on hand change nothing and record no movement.
//! `listStockMovements` pages the movements newest first, by D60's cursor.
//!
//! A row is not deleted while it holds units for a claim (`held`); what
//! claims committed is sold, a deleted claim's included, and its records
//! and movements go with the row.
//!
//! configChange: the price field, `lowAt`, `maxPerHold` and the permission
//! may change. `backorder` turns off only on a type with no rows: the
//! engine checked each row's counts at the publish, and the core reads no
//! rows at a publish (D103), so a type with rows keeps backorder on. Stock
//! joins a type that has rows, each at zero, and leaves only one that has
//! none, since its movements would be lost.

use std::collections::BTreeMap;
use std::sync::OnceLock;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, ContractQuery, Cx, Declaration,
    GuardRequest, Hook, Refusal, Row, SchemaIssue, SubRow, Veto, View,
};

use crate::ajv;
use crate::currency;
use crate::declared;
use crate::paging;
use crate::supply::{
    self, Ask, Begun, CheckAnswer, Free, Price, RowRef, SupplyCall, SupplyKind, SupplyOperation,
    Unavailable, MAX_SAFE_INTEGER,
};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Stock";

/// Stock's declaration, the commerce package's file, read in place.
pub const DECLARATION: &str =
    include_str!("../../../../engine-commerce/rust/declarations/Stock.behavior.json");

/// The operations, by their declared names.
pub const RECEIVE_STOCK: &str = "receiveStock";
pub const ADJUST_STOCK: &str = "adjustStock";
pub const COUNT_STOCK: &str = "countStock";
pub const LIST_STOCK_MOVEMENTS: &str = "listStockMovements";

/// The contract's record sub-table.
pub const HOLDS: &str = "holds";
/// The append-only movement sub-table.
pub const MOVEMENTS: &str = "movements";

/// The owned columns.
pub const ON_HAND: &str = "onHand";
pub const HELD: &str = "held";

/// `over_max`: a hold of more than `maxPerHold`.
pub const OVER_MAX: &str = "over_max";
/// `below_held`: an adjustment or a count below what claims hold.
pub const BELOW_HELD: &str = "below_held";

/// The IR types an own field that holds a count or a price may be.
pub const INTEGER_TYPES: [&str; 3] = ["Generic.Int64", "Generic.Int32", "integer"];

/// The IR types an own field that holds a currency may be.
pub const CURRENCY_TYPES: [&str; 2] = ["Finance.Currency", "string"];

fn file() -> &'static Value {
    static FILE: OnceLock<Value> = OnceLock::new();
    FILE.get_or_init(|| ajv::file(DECLARATION))
}

/// Where a price's currency comes from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CurrencySource {
    /// An ISO 4217 code the config gives.
    Code(String),
    /// An own field of the type that holds one.
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

impl StockConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the fields it names against the type's own and a literal
    /// currency against ISO 4217, each a rule's reason the core prefixes
    /// with the type and the behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<StockConfig, ConfigError> {
        let issues = ajv::config_issues(file(), raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues));
        }
        let price = match raw.get("price") {
            Some(price) => {
                let field = field_of(price).unwrap_or_default();
                own_field(target, "price.field", &field, &INTEGER_TYPES, "an integer")?;
                let currency = match price.get("currency") {
                    Some(Value::String(code)) => {
                        if let Some(refusal) = currency::refusal("price.currency", code) {
                            return Err(refusal.into());
                        }
                        CurrencySource::Code(code.clone())
                    }
                    Some(given) => {
                        let field = field_of(given).unwrap_or_default();
                        own_field(
                            target,
                            "price.currency.field",
                            &field,
                            &CURRENCY_TYPES,
                            "a string",
                        )?;
                        CurrencySource::Field(field)
                    }
                    None => return Err("price names no currency".into()),
                };
                Some(StockPrice { field, currency })
            }
            None => None,
        };
        let low_at = match raw.get("lowAt") {
            Some(Value::Number(_)) => raw.get("lowAt").and_then(supply::integer).map(LowAt::Count),
            Some(given) => {
                let field = field_of(given).unwrap_or_default();
                own_field(target, "lowAt.field", &field, &INTEGER_TYPES, "an integer")?;
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
            max_per_hold: raw.get("maxPerHold").and_then(supply::integer),
            backorder: raw.get("backorder") == Some(&Value::Bool(true)),
        })
    }
}

fn field_of(value: &Value) -> Option<String> {
    value
        .get("field")
        .and_then(Value::as_str)
        .map(str::to_owned)
}

// field_named is the own field of the type a config names, or its refusal.
pub(crate) fn field_named<'t>(
    target: &ConfigTarget<'t>,
    at: &str,
    field: &str,
) -> Result<&'t superschematic_rules::FieldSpec, ConfigError> {
    let fields = target.fields();
    fields
        .iter()
        .find(|known| known.name == field)
        .ok_or_else(|| {
            let names: Vec<&str> = fields.iter().map(|known| known.name.as_str()).collect();
            format!(
                "{at} \"{field}\" is not a field of {} (its fields: {})",
                target.type_name(),
                if names.is_empty() {
                    "none".to_owned()
                } else {
                    names.join(", ")
                }
            )
            .into()
        })
}

// own_field holds a field the config names to an own field of the type of
// one of the IR types given.
pub(crate) fn own_field(
    target: &ConfigTarget<'_>,
    at: &str,
    field: &str,
    types: &[&str],
    kind: &str,
) -> Result<(), ConfigError> {
    let found = field_named(target, at, field)?;
    if !types.contains(&found.type_name.as_str()) {
        return Err(format!(
            "{at} \"{field}\" is not {kind} field of {}",
            target.type_name()
        )
        .into());
    }
    Ok(())
}

/// A row's counts.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
struct Counts {
    on_hand: i64,
    held: i64,
}

impl Counts {
    fn of(row: &Row) -> Counts {
        Counts {
            on_hand: supply::owned_integer(row, NAME, ON_HAND).unwrap_or(0),
            held: supply::owned_integer(row, NAME, HELD).unwrap_or(0),
        }
    }

    fn read<C>(view: &View<'_, C>) -> Counts {
        let number = |field: &str| view.get(field).as_ref().and_then(supply::integer);
        Counts {
            on_hand: number(ON_HAND).unwrap_or(0),
            held: number(HELD).unwrap_or(0),
        }
    }

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

    fn save(&self, cx: &Cx<'_, StockConfig>) -> Hook<()> {
        cx.set(ON_HAND, self.on_hand)?;
        cx.set(HELD, self.held)
    }
}

// low_at is the count Stock.low compares available with now; `None`
// without lowAt, or while its field holds no integer.
fn low_at(config: &StockConfig, own: &Map<String, Value>) -> Option<i64> {
    match &config.low_at {
        None => None,
        Some(LowAt::Count(count)) => Some(*count),
        Some(LowAt::Field(field)) => own.get(field).and_then(supply::integer),
    }
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

// movements is the row's movements, by sequence ascending.
fn movements(rows: &[SubRow]) -> Vec<(i64, &SubRow)> {
    let mut out: Vec<(i64, &SubRow)> = rows
        .iter()
        .filter_map(|row| Some((row.key.parse::<i64>().ok()?, row)))
        .collect();
    out.sort_by_key(|(seq, _)| *seq);
    out
}

// record appends a movement and returns its sequence number, per row from
// 1: one more than the last the ledger holds.
fn record(cx: &Cx<'_, StockConfig>, movement: &Movement<'_>) -> Hook<i64> {
    let rows = cx.sub_rows(MOVEMENTS)?;
    let seq = movements(&rows).last().map_or(0, |(seq, _)| *seq) + 1;
    let mut values = Map::new();
    let text = |value: Option<&str>| value.map_or(Value::Null, Value::from);
    values.insert("kind".into(), Value::from(movement.kind));
    values.insert("delta".into(), Value::from(movement.delta));
    values.insert("onHand".into(), Value::from(movement.on_hand));
    values.insert("reason".into(), text(movement.reason));
    values.insert("reference".into(), text(movement.reference));
    values.insert(
        "holderSchema".into(),
        text(movement.call.map(|call| call.holder.schema.as_str())),
    );
    values.insert(
        "holderId".into(),
        text(movement.call.map(|call| call.holder.id.as_str())),
    );
    values.insert(
        "hold".into(),
        text(movement.call.map(|call| call.hold.as_str())),
    );
    values.insert(
        "item".into(),
        movement
            .call
            .map_or(Value::Null, |call| Value::from(call.item)),
    );
    values.insert("actor".into(), Value::from(cx.caller().subject.as_str()));
    values.insert("at".into(), Value::from(cx.now()));
    cx.insert_row(MOVEMENTS, &seq.to_string(), values)?;
    Ok(seq)
}

fn movement_of(seq: i64, row: &SubRow) -> Value {
    let mut out = Map::new();
    out.insert("seq".into(), Value::from(seq));
    if let Some(kind) = row.value("kind") {
        out.insert("kind".into(), kind.clone());
    }
    let number = |field: &str| row.value(field).and_then(supply::integer);
    out.insert("delta".into(), Value::from(number("delta").unwrap_or(0)));
    out.insert("onHand".into(), Value::from(number("onHand").unwrap_or(0)));
    for field in ["reason", "reference"] {
        if let Some(value) = row.value(field).filter(|value| value.is_string()) {
            out.insert(field.into(), value.clone());
        }
    }
    if let (Some(schema), Some(id)) = (
        row.value("holderSchema").and_then(Value::as_str),
        row.value("holderId").and_then(Value::as_str),
    ) {
        out.insert("holder".into(), json!({ "schema": schema, "id": id }));
    }
    if let Some(hold) = row.value("hold").filter(|value| value.is_string()) {
        out.insert("hold".into(), hold.clone());
    }
    if let Some(item) = number("item") {
        out.insert("item".into(), Value::from(item));
    }
    out.insert(
        "actor".into(),
        row.value("actor").cloned().unwrap_or(Value::from("")),
    );
    out.insert("at".into(), Value::from(number("at").unwrap_or(0)));
    Value::Object(out)
}

fn forbidden(cx: &Cx<'_, StockConfig>, what: &str, permission: &str) -> superschematic_rules::Stop {
    cx.forbidden(format!(
        "{} may not {what} {} {}: it needs permission {permission}",
        cx.caller().subject,
        cx.type_name(),
        cx.key().unwrap_or_default()
    ))
}

fn params_error(
    cx: &Cx<'_, StockConfig>,
    operation: &str,
    path: &str,
    message: &str,
) -> superschematic_rules::Stop {
    Refusal::operation_params(
        NAME,
        cx.use_id(),
        operation,
        vec![SchemaIssue::new(path, message)],
    )
    .into()
}

// permitted refuses a caller without the config's permission.
fn permitted(cx: &Cx<'_, StockConfig>, what: &str) -> Hook<()> {
    let permission = &cx.config().permission;
    if !cx.can(permission)? {
        return Err(forbidden(cx, what, permission));
    }
    Ok(())
}

// count_after is a count after a change, refused as the operation's
// parameters past what JavaScript holds exactly.
fn count_after(
    cx: &Cx<'_, StockConfig>,
    operation: &str,
    path: &str,
    what: &str,
    count: i64,
    delta: i64,
) -> Hook<i64> {
    count
        .checked_add(delta)
        .filter(|after| after.abs() <= MAX_SAFE_INTEGER)
        .ok_or_else(|| {
            params_error(
                cx,
                operation,
                path,
                &format!("would leave more {what} than a JSON number holds exactly"),
            )
        })
}

fn text<'p>(params: &'p Value, name: &str) -> Option<&'p str> {
    params.get(name).and_then(Value::as_str)
}

fn vetoed(
    cx: &Cx<'_, StockConfig>,
    reason: String,
    code: &str,
    details: Value,
) -> superschematic_rules::Stop {
    let mut veto = Veto::new(reason).with_code(code);
    if let Value::Object(details) = details {
        veto = veto.with_details(details);
    }
    cx.veto(veto)
}

fn receive_stock(cx: &Cx<'_, StockConfig>, params: &Value) -> Hook<Value> {
    permitted(cx, "receive stock of")?;
    let quantity = params
        .get("quantity")
        .and_then(supply::integer)
        .unwrap_or(0);
    let mut counts = Counts::read(cx);
    counts.on_hand = count_after(
        cx,
        RECEIVE_STOCK,
        "/quantity",
        "on hand",
        counts.on_hand,
        quantity,
    )?;
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
    counts.save(cx)?;
    Ok(result(cx.config(), &counts, Some(seq)))
}

fn adjust_stock(cx: &Cx<'_, StockConfig>, params: &Value) -> Hook<Value> {
    permitted(cx, "adjust the stock of")?;
    let delta = params.get("delta").and_then(supply::integer).unwrap_or(0);
    let mut counts = Counts::read(cx);
    if delta == 0 {
        cx.unchanged()?;
        return Ok(result(cx.config(), &counts, None));
    }
    let after = count_after(cx, ADJUST_STOCK, "/delta", "on hand", counts.on_hand, delta)?;
    // An adjustment down may not leave fewer than 0, or than claims hold
    // without backorder; one up is taken even while it leaves too few, as
    // a commit after a count with the override, or under backorder, can.
    if delta < 0 && (after < 0 || (after < counts.held && !cx.config().backorder)) {
        return Err(vetoed(
            cx,
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
    counts.save(cx)?;
    Ok(result(cx.config(), &counts, Some(seq)))
}

fn count_stock(cx: &Cx<'_, StockConfig>, params: &Value) -> Hook<Value> {
    permitted(cx, "count the stock of")?;
    let counted = params.get("onHand").and_then(supply::integer).unwrap_or(0);
    let mut counts = Counts::read(cx);
    if counted == counts.on_hand {
        cx.unchanged()?;
        return Ok(result(cx.config(), &counts, None));
    }
    if counted < counts.held {
        let permission = format!("{}.override", cx.config().permission);
        if !cx.can(&permission)? {
            return Err(vetoed(
                cx,
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
    counts.save(cx)?;
    Ok(result(cx.config(), &counts, Some(seq)))
}

// fit is what does not fit of a call's quantity now: over maxPerHold, or
// beyond what is available beside the quantities `also` the claim's other
// items ask, unless the config allows backorder.
fn fit(
    config: &StockConfig,
    supply: &RowRef,
    counts: &Counts,
    call: &SupplyCall,
    also: i64,
) -> Option<supply::Refusal> {
    let quantity = call.ask.quantity().unwrap_or(0);
    if let Some(max) = config.max_per_hold.filter(|max| quantity > *max) {
        let mut details = Unavailable::of(supply, call, Free::Available(0)).details();
        details.remove("available");
        details.insert("max".into(), Value::from(max));
        return Some(supply::Refusal {
            code: OVER_MAX.to_owned(),
            reason: format!(
                "{quantity} asked of {} {}, and one item holds at most {max}",
                supply.schema, supply.id
            ),
            details,
        });
    }
    let free = counts.free().saturating_sub(also);
    if config.backorder || quantity <= free {
        return None;
    }
    let available = free.max(0);
    Some(
        Unavailable::of(supply, call, Free::Available(available)).refusal(if also > 0 {
            format!(
                "{quantity} asked of {} {}, which has {available} available beside the {also} the claim's other items ask",
                supply.schema, supply.id
            )
        } else {
            format!(
                "{quantity} asked of {} {}, which has {available} available",
                supply.schema, supply.id
            )
        }),
    )
}

// price_of is the unit price the config's price field holds now, with its
// currency; `None` without a price, or while the field holds no integer.
fn price_of(config: &StockConfig, own: &Map<String, Value>) -> Option<Price> {
    let price = config.price.as_ref()?;
    let unit_price = own.get(&price.field).and_then(supply::integer)?;
    let currency = match &price.currency {
        CurrencySource::Code(code) => Some(code.clone()),
        CurrencySource::Field(field) => own.get(field).and_then(Value::as_str).map(str::to_owned),
    };
    Some(Price {
        unit_price,
        currency,
    })
}

// contract runs a writing call of the supply contract: the holder check,
// the lock and the record's step, what fits for holdFor, the counts and
// the movement, then the record.
fn contract(cx: &Cx<'_, StockConfig>, operation: SupplyOperation, params: &Value) -> Hook<Value> {
    let Some(call) = SupplyCall::from_params(SupplyKind::Counted, params) else {
        return Err(cx.defect(format!(
            "{} took parameters of another shape",
            operation.name()
        )));
    };
    let begun = supply::begin(cx, HOLDS, operation, &call)?;
    let Begun::Apply { recorded, .. } = &begun else {
        return supply::finish(cx, HOLDS, operation, &call, &begun, None);
    };
    // A record holds what its item asked when it was taken, which begin
    // held the call to.
    let quantity = recorded
        .as_ref()
        .and_then(|record| record.ask.quantity())
        .or(call.ask.quantity())
        .unwrap_or(0);
    let config = cx.config();
    let here = RowRef::of(cx);
    let mut counts = Counts::read(cx);
    let mut price = None;
    match operation {
        SupplyOperation::Hold => {
            if let Some(refusal) = fit(config, &here, &counts, &call, 0) {
                return Err(refusal.veto(cx));
            }
            counts.held = count_after(
                cx,
                supply::HOLD_FOR,
                "/quantity",
                "held",
                counts.held,
                quantity,
            )?;
            price = price_of(config, &cx.own());
        }
        SupplyOperation::Commit => {
            let on_hand = count_after(
                cx,
                supply::COMMIT_FOR,
                "/quantity",
                "on hand",
                counts.on_hand,
                -quantity,
            )?;
            // Without backorder nothing is sold that is not on hand: after
            // a count with the override below what claims hold, a commit
            // waits for stock to be received or counted.
            if on_hand < 0 && !config.backorder {
                return Err(
                    Unavailable::of(&here, &call, Free::Available(counts.on_hand.max(0)))
                        .refusal(format!(
                            "{quantity} committed of {} {}, which has {} on hand",
                            here.schema, here.id, counts.on_hand
                        ))
                        .veto(cx),
                );
            }
            counts.held = count_after(
                cx,
                supply::COMMIT_FOR,
                "/quantity",
                "held",
                counts.held,
                -quantity,
            )?;
            counts.on_hand = on_hand;
            record(
                cx,
                &Movement {
                    kind: "commit",
                    delta: -quantity,
                    on_hand: counts.on_hand,
                    reason: None,
                    reference: None,
                    call: Some(&call),
                },
            )?;
        }
        SupplyOperation::Release => {
            counts.held = count_after(
                cx,
                supply::RELEASE_FOR,
                "/quantity",
                "held",
                counts.held,
                -quantity,
            )?;
        }
        SupplyOperation::Return => {
            counts.on_hand = count_after(
                cx,
                supply::RETURN_FOR,
                "/quantity",
                "on hand",
                counts.on_hand,
                quantity,
            )?;
            record(
                cx,
                &Movement {
                    kind: "return",
                    delta: quantity,
                    on_hand: counts.on_hand,
                    reason: None,
                    reference: None,
                    call: Some(&call),
                },
            )?;
        }
    }
    counts.save(cx)?;
    supply::finish(cx, HOLDS, operation, &call, &begun, price)
}

// check_hold answers whether holdFor would hold the item now: an item the
// supply holds already fits, one its record has moved past does not
// (not_requested), and any other fits what is available beside the
// quantities `also` the claim's other items ask.
fn check_hold(
    config: &StockConfig,
    supply: &RowRef,
    row: &Row,
    records: &[SubRow],
    params: &Value,
) -> Option<CheckAnswer> {
    let call = SupplyCall::from_params(SupplyKind::Counted, params)?;
    let also = SupplyCall::also(SupplyKind::Counted, params)
        .iter()
        .filter_map(Ask::quantity)
        .fold(0_i64, i64::saturating_add);
    let recorded = supply::recorded_in(records, &call);
    Some(match supply::check_record(&call, recorded.as_ref()) {
        Some(answer) => answer,
        None => match fit(config, supply, &Counts::of(row), &call, also) {
            Some(refusal) => CheckAnswer::Refused(refusal),
            None => CheckAnswer::Fits,
        },
    })
}

fn list_stock_movements(cx: &Cx<'_, StockConfig>, params: &Value) -> Hook<Value> {
    let request = paging::page_request(params).map_err(|issue| {
        superschematic_rules::Stop::from(Refusal::operation_params(
            NAME,
            cx.use_id(),
            LIST_STOCK_MOVEMENTS,
            vec![issue],
        ))
    })?;
    let rows = cx.sub_rows(MOVEMENTS)?;
    let mut numbered: Vec<(i64, Value)> = movements(&rows)
        .into_iter()
        .rev()
        .filter(|(seq, _)| request.after == 0 || *seq < request.after)
        .map(|(seq, row)| (seq, movement_of(seq, row)))
        .collect();
    numbered.truncate(usize::try_from(request.limit + 1).unwrap_or(usize::MAX));
    let found = paging::page(numbered, request.limit, |(seq, _)| *seq);
    Ok(json!({
        "items": found.items.into_iter().map(|(_, item)| item).collect::<Vec<_>>(),
        "next": found.next,
    }))
}

// held_records is how many items the row's records hold for claims.
fn held_records(rows: &[SubRow]) -> usize {
    supply::records_of(rows)
        .iter()
        .filter(|record| record.state == supply::ItemState::Held)
        .count()
}

/// Stock: units a supply counts, held and committed by claims.
#[derive(Clone, Copy, Debug, Default)]
pub struct Stock;

impl Behavior for Stock {
    type Config = StockConfig;

    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<StockConfig, ConfigError> {
        StockConfig::parse(raw, target)
    }

    // See the module's header: Stock joins a type with rows, each at zero;
    // it leaves one with none; backorder turns off on one with none.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&StockConfig>,
        after: Option<&StockConfig>,
    ) -> Hook<Option<String>> {
        let Some(before) = before else {
            return Ok(None);
        };
        let Some(after) = after else {
            return Ok(if cx.has_rows()? {
                Some("its instances' stock and movements would be lost".to_owned())
            } else {
                None
            });
        };
        if before.backorder && !after.backorder && cx.has_rows()? {
            return Ok(Some(
                "backorder turns off only while the type has no rows: a row may be below zero, or hold more than it has on hand, and a publish reads no row".to_owned(),
            ));
        }
        Ok(None)
    }

    fn check_params(
        &self,
        _config: &StockConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        ajv::params_issues(file(), operation, params)
    }

    // A create starts at zero.
    fn initialize(&self, cx: &Cx<'_, StockConfig>, _params: &Value) -> Hook<()> {
        Counts::default().save(cx)
    }

    // A row is not deleted while it holds units for a claim.
    fn guard(
        &self,
        view: &View<'_, StockConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        if !matches!(request, GuardRequest::Delete) {
            return Ok(None);
        }
        let rows = view.read_sub_rows(
            view.type_name(),
            view.key().unwrap_or_default(),
            view.use_id(),
            HOLDS,
        )?;
        let holding = held_records(rows);
        if holding == 0 {
            return Ok(None);
        }
        let mut details = Map::new();
        details.insert("held".into(), Value::from(Counts::read(view).held));
        Ok(Some(
            Veto::new(format!(
                "it holds units for {holding} items of claims; they are released or committed first"
            ))
            .with_code(supply::HELD)
            .with_details(details),
        ))
    }

    fn operation(&self, cx: &Cx<'_, StockConfig>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            RECEIVE_STOCK => receive_stock(cx, params),
            ADJUST_STOCK => adjust_stock(cx, params),
            COUNT_STOCK => count_stock(cx, params),
            LIST_STOCK_MOVEMENTS => list_stock_movements(cx, params),
            supply::CHECK_HOLD => {
                let row = cx
                    .row_of(cx.type_name(), cx.key().unwrap_or_default())?
                    .unwrap_or_default();
                let records = cx.sub_rows(HOLDS)?;
                check_hold(cx.config(), &RowRef::of(cx), &row, &records, params)
                    .map(|answer| answer.to_json())
                    .ok_or_else(|| cx.defect("checkHold took parameters of another shape"))
            }
            _ => match SupplyOperation::parse(name) {
                Some(operation) => contract(cx, operation, params),
                None => Err(cx.defect(format!("it declares no operation {name}"))),
            },
        }
    }

    // A row's records and movements go with it.
    fn after_change(&self, _cx: &Cx<'_, StockConfig>, _change: &Change<'_>) -> Hook<()> {
        Ok(())
    }

    fn read_fields(
        &self,
        config: &StockConfig,
        row: &Row,
        _tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let counts = Counts::of(row);
        let mut fields = Map::new();
        fields.insert("available".into(), Value::from(counts.available(config)));
        let low = low_at(config, &row.own).is_some_and(|at| counts.available(config) <= at);
        fields.insert("low".into(), Value::Bool(low));
        fields
    }

    // Supply's checkHold, over the row and its records as a read gave
    // them.
    fn answer(
        &self,
        config: &StockConfig,
        query: &ContractQuery<'_>,
        row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
        _now: i64,
    ) -> Option<Value> {
        if query.contract != supply::NAME || query.query != supply::CHECK_HOLD {
            return None;
        }
        let here = RowRef::from_json(query.params.get("supply")?)?;
        let records = supply::table_of(tables, HOLDS);
        check_hold(config, &here, row, records, query.params).map(|answer| answer.to_json())
    }
}
