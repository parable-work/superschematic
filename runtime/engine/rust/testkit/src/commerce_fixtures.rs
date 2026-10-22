//! The behaviors the commerce tests hold the supply contract
//! (`superschematic_engine_commerce::supply`, D70) to, before `Stock`,
//! `Calendar` and `Holds` exist: [`TestSupply`], a supply on the contract's
//! helpers alone, as an extension's would be, counted (`test.Supply`) or
//! timed (`test.Slots`); and [`HoldsStandIn`], a claim registered as
//! `Holds` whose `Holds.items` the supply's holder check reads, which
//! stages items and moves them as `Holds` does: it writes an item's new
//! state, then calls the supply. The core meta-schema admits only the
//! core's behaviors, so an engine that composes them loads against the
//! open one ([`crate::open_meta_schema`]).

use std::sync::LazyLock;

use serde_json::{json, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, InstanceView, OperationFn, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::{Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, Error, GuardRequest, Hooks, InstanceChange, OperationCx, Result,
};
use superschematic_engine_commerce::supply::{
    self, Ask, Begun, CheckAnswer, Free, HolderItem, InstanceRef, Interval, ItemState, Price,
    Refusal, SupplyCall, SupplyKind, SupplyOperation, Unavailable,
};

/// The table a [`TestSupply`] records holds in.
pub const SUPPLY_TABLE: &str = "records";

/// The own field that holds how much a [`TestSupply`] instance has: units
/// of counted supply, holds at once of timed.
pub const UNITS: &str = "units";

/// A supply on the contract's helpers: `test.Supply` counts units,
/// `test.Slots` holds time. An instance has its own field `units`; its
/// config may name an own integer field as its unit price (`priceField`)
/// and a `currency`. Its field `records` lists what it records for each
/// holder's item.
#[derive(Clone, Copy, Debug)]
pub struct TestSupply {
    pub kind: SupplyKind,
}

impl TestSupply {
    /// test.Supply, counted.
    pub const COUNTED: TestSupply = TestSupply {
        kind: SupplyKind::Counted,
    };
    /// test.Slots, timed.
    pub const TIMED: TestSupply = TestSupply {
        kind: SupplyKind::Timed,
    };

    pub fn name(&self) -> &'static str {
        match self.kind {
            SupplyKind::Counted => "test.Supply",
            SupplyKind::Timed => "test.Slots",
        }
    }
}

fn supply_declaration(supply: TestSupply) -> String {
    json!({
        "name": supply.name(),
        "description": "A supply on the contract's helpers alone, as an extension's would be.",
        "configSchema": {
            "type": "object",
            "additionalProperties": false,
            "properties": {"priceField": {"type": "string"}, "currency": {"type": "string"}}
        },
        "fields": [{"name": "records", "description": "What the supply records for each holder's item."}],
        "operations": supply::operation_declarations(supply.kind),
        "vetoes": supply::veto_declarations(),
    })
    .to_string()
}

static COUNTED_DECLARATION: LazyLock<String> =
    LazyLock::new(|| supply_declaration(TestSupply::COUNTED));
static TIMED_DECLARATION: LazyLock<String> =
    LazyLock::new(|| supply_declaration(TestSupply::TIMED));

impl Behavior for TestSupply {
    type Config = Value;

    fn declaration(&self) -> &str {
        match self.kind {
            SupplyKind::Counted => &COUNTED_DECLARATION,
            SupplyKind::Timed => &TIMED_DECLARATION,
        }
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![
                BehaviorMigration::new(1, "records").table(supply::rows_table(SUPPLY_TABLE))
            ],
            guard: Some(|_, view, request| {
                if !matches!(request, GuardRequest::Delete { .. }) {
                    return Ok(None);
                }
                let holding = supply::holding(view, SUPPLY_TABLE)?;
                Ok((holding > 0).then(|| Veto {
                    reason: format!("it holds {holding} items for claims"),
                    code: Some(supply::HELD.to_owned()),
                    details: None,
                }))
            }),
            operations: [
                ("holdFor", hold_for as OperationFn<Self>),
                ("commitFor", commit_for),
                ("releaseFor", release_for),
                ("returnFor", return_for),
                ("checkHold", check_hold),
            ]
            .into_iter()
            // Timed supply serves no returnFor.
            .filter(|(name, _)| {
                self.kind
                    .operations()
                    .iter()
                    .any(|operation| operation.name() == *name)
            })
            .collect(),
            fields: vec![("records", |_, view| {
                let records = supply::records(view, SUPPLY_TABLE)?;
                Ok(Some(Value::Array(
                    records.iter().map(supply::SupplyRecord::to_json).collect(),
                )))
            })],
            after_change: Some(|_, cx, change| match change {
                InstanceChange::Delete => supply::forget(cx, SUPPLY_TABLE),
                _ => Ok(()),
            }),
            ..Hooks::default()
        }
    }
}

fn hold_for(supply: &TestSupply, cx: &OperationCx<'_, Value>, params: &Value) -> Result<Value> {
    contract(supply, cx, params, SupplyOperation::Hold)
}

fn commit_for(supply: &TestSupply, cx: &OperationCx<'_, Value>, params: &Value) -> Result<Value> {
    contract(supply, cx, params, SupplyOperation::Commit)
}

fn release_for(supply: &TestSupply, cx: &OperationCx<'_, Value>, params: &Value) -> Result<Value> {
    contract(supply, cx, params, SupplyOperation::Release)
}

fn return_for(supply: &TestSupply, cx: &OperationCx<'_, Value>, params: &Value) -> Result<Value> {
    contract(supply, cx, params, SupplyOperation::Return)
}

// check_hold answers whether holdFor would fit now, beside what `also`
// lists; it takes no holder check, since it reads only.
fn check_hold(supply: &TestSupply, cx: &OperationCx<'_, Value>, params: &Value) -> Result<Value> {
    let call = SupplyCall::parse(supply.name(), SupplyOperation::Check, supply.kind, params)?;
    let also = supply::also(supply.name(), supply.kind, params)?;
    Ok(match fit(supply, cx, &call, &also)? {
        Some(refusal) => CheckAnswer::Refused(refusal),
        None => CheckAnswer::Fits,
    }
    .to_json())
}

// contract runs a writing call of the contract: the holder check and the
// record's step, what is free for holdFor, then the record.
fn contract(
    supply: &TestSupply,
    cx: &OperationCx<'_, Value>,
    params: &Value,
    operation: SupplyOperation,
) -> Result<Value> {
    let call = SupplyCall::parse(supply.name(), operation, supply.kind, params)?;
    match supply::begin(cx, SUPPLY_TABLE, operation, &call)? {
        Begun::Done(record) => Ok(supply::done(cx, operation, record.as_ref())),
        Begun::Apply { .. } => {
            let price = if operation == SupplyOperation::Hold {
                if let Some(refusal) = fit(supply, cx, &call, &[])? {
                    return Err(refusal.veto(cx, operation));
                }
                price_of(cx)?
            } else {
                None
            };
            supply::finish(cx, SUPPLY_TABLE, operation, &call, price)
        }
    }
}

// fit is what does not fit of a call's ask, beside what the instance
// takes for every other item, a deleted claim's committed records
// included, and what `also` asks: units beyond those free, or an interval
// when `units` holds already overlap it.
fn fit(
    supply: &TestSupply,
    view: &InstanceView<'_, Value>,
    call: &SupplyCall,
    also: &[Ask],
) -> Result<Option<Refusal>> {
    let units = view.data()?.get(UNITS).and_then(Value::as_i64).unwrap_or(0);
    let others: Vec<Ask> = supply::records(view, SUPPLY_TABLE)?
        .into_iter()
        .filter(|record| {
            record.state.takes()
                && !(!record.retired
                    && record.holder == call.holder
                    && record.hold == call.hold
                    && record.item == call.item)
        })
        .map(|record| record.ask)
        .chain(also.iter().copied())
        .collect();
    match (supply.kind, call.ask) {
        (SupplyKind::Counted, Ask::Quantity(quantity)) => {
            let taken: i64 = others.iter().filter_map(Ask::quantity).sum();
            let available = (units - taken).max(0);
            Ok((quantity > available).then(|| {
                Unavailable::of(view, call, Free::Available(available)).refusal(format!(
                    "{quantity} asked of {} {}, which has {available} free",
                    view.schema(),
                    view.id()
                ))
            }))
        }
        (SupplyKind::Timed, Ask::Interval(asked)) => {
            let overlapping: Vec<Interval> = others
                .iter()
                .filter_map(Ask::interval)
                .filter(|held| held.overlaps(&asked))
                .collect();
            Ok(match overlapping.first() {
                Some(conflict) if overlapping.len() as i64 >= units => Some(
                    Unavailable::of(view, call, Free::Conflict(*conflict)).refusal(format!(
                        "{} {} holds {} at once, and {} of them overlap the time asked",
                        view.schema(),
                        view.id(),
                        units,
                        overlapping.len()
                    )),
                ),
                None if units < 1 => Some(
                    Unavailable::of(view, call, Free::Available(0)).refusal(format!(
                        "{} {} holds nothing",
                        view.schema(),
                        view.id()
                    )),
                ),
                _ => None,
            })
        }
        _ => Err(Error::behavior(
            supply.name(),
            "a call asks what its kind of supply does not hold",
        )),
    }
}

// price_of reads the unit price the config's priceField names.
fn price_of(cx: &OperationCx<'_, Value>) -> Result<Option<Price>> {
    let config = cx.config();
    let Some(field) = config.get("priceField").and_then(Value::as_str) else {
        return Ok(None);
    };
    Ok(cx
        .data()?
        .get(field)
        .and_then(Value::as_i64)
        .map(|unit_price| Price {
            unit_price,
            currency: config
                .get("currency")
                .and_then(Value::as_str)
                .map(str::to_owned),
        }))
}

/// A claim registered as `Holds`, standing in for the behavior: its field
/// `items` is `Holds.items`, which a supply's holder check reads. `stage`
/// sets its items, each pending; `move` moves each item that can go to a
/// state there, writing the state, then calling the supply's operation,
/// and keeps the price `holdFor` returns, all of the items or none;
/// `mark` writes an item's state without calling the supply; `check` asks
/// `checkHold` of each pending item and vetoes `unavailable`, listing every
/// item that does not fit.
#[derive(Clone, Copy, Debug, Default)]
pub struct HoldsStandIn;

/// The table the stand-in keeps its items in.
const ITEMS_TABLE: &str = "items";

static HOLDS_DECLARATION: LazyLock<String> = LazyLock::new(|| {
    let item = json!({"type": "object"});
    let state =
        json!({"type": "string", "enum": ["pending", "held", "committed", "released", "returned"]});
    json!({
        "name": supply::HOLDS,
        "description": "A stand-in for Holds, which the supply contract's tests drive.",
        "fields": [{"name": supply::ITEMS, "description": "Each item: its hold and index, its supply, what it asks, its state and price."}],
        "operations": [
            {
                "name": "stage",
                "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["items"], "properties": {"items": {"type": "array", "items": item}}},
                "resultSchema": true,
                "writes": true
            },
            {
                "name": "move",
                "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["to"], "properties": {"to": state, "hold": {"type": "string"}, "item": {"type": "integer"}}},
                "resultSchema": true,
                "writes": true
            },
            {
                "name": "mark",
                "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["hold", "item", "state"], "properties": {"hold": {"type": "string"}, "item": {"type": "integer"}, "state": state}},
                "resultSchema": true,
                "writes": true
            },
            {
                "name": "check",
                "paramsSchema": {"type": "object", "additionalProperties": false},
                "resultSchema": true
            }
        ],
        "vetoes": [{"code": supply::UNAVAILABLE, "description": "An item does not fit its supply."}]
    })
    .to_string()
});

impl Behavior for HoldsStandIn {
    type Config = Value;

    fn declaration(&self) -> &str {
        &HOLDS_DECLARATION
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "items").table(
                TableSpec::new(ITEMS_TABLE)
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("hold", ColumnSpec::text().not_null())
                    .column("item", ColumnSpec::integer().not_null())
                    .column("item_json", ColumnSpec::text().not_null())
                    .primary_key(&["namespace", "schema", "id", "hold", "item"]),
            )],
            operations: vec![
                ("stage", |_, cx, params| {
                    let table = cx.sql().table(ITEMS_TABLE)?;
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                        ),
                        &key(cx),
                    )?;
                    let staged = params
                        .get("items")
                        .and_then(Value::as_array)
                        .cloned()
                        .unwrap_or_default();
                    for mut given in staged {
                        if let Some(given) = given.as_object_mut() {
                            given.insert("state".into(), Value::from("pending"));
                        }
                        let item = HolderItem::from_json(&given).ok_or_else(|| {
                            Error::behavior(supply::HOLDS, format!("not an item: {given}"))
                        })?;
                        put(cx, &item)?;
                    }
                    Ok(json!({}))
                }),
                ("move", |_, cx, params| {
                    let to = params
                        .get("to")
                        .and_then(Value::as_str)
                        .and_then(ItemState::parse)
                        .ok_or_else(|| Error::behavior(supply::HOLDS, "move takes a state"))?;
                    let operation = match to {
                        ItemState::Held => SupplyOperation::Hold,
                        ItemState::Committed => SupplyOperation::Commit,
                        ItemState::Released => SupplyOperation::Release,
                        ItemState::Returned => SupplyOperation::Return,
                        ItemState::Pending => {
                            return Err(Error::behavior(supply::HOLDS, "no item moves to pending"))
                        }
                    };
                    let hold = params.get("hold").and_then(Value::as_str);
                    let index = params.get("item").and_then(Value::as_i64);
                    let mut moved = 0;
                    for mut item in items(cx)? {
                        if hold.is_some_and(|hold| hold != item.hold)
                            || index.is_some_and(|index| index != item.item)
                            || !item.state.advances_to(to)
                            || (to == ItemState::Returned && item.ask.quantity().is_none())
                        {
                            continue;
                        }
                        // The item's new state first, which the supply's
                        // holder check reads, then the supply's call.
                        item.state = to;
                        put(cx, &item)?;
                        let call = SupplyCall {
                            holder: InstanceRef::of(cx),
                            hold: item.hold.clone(),
                            item: item.item,
                            ask: item.ask,
                        };
                        let answer = cx.instances().invoke(
                            &item.supply.schema,
                            &item.supply.id,
                            operation.name(),
                            &call.to_params(),
                            None,
                        )?;
                        if to == ItemState::Held {
                            item.price =
                                answer
                                    .get("unitPrice")
                                    .and_then(Value::as_i64)
                                    .map(|unit_price| Price {
                                        unit_price,
                                        currency: answer
                                            .get("currency")
                                            .and_then(Value::as_str)
                                            .map(str::to_owned),
                                    });
                            put(cx, &item)?;
                        }
                        moved += 1;
                    }
                    Ok(json!({ "moved": moved }))
                }),
                ("mark", |_, cx, params| {
                    let hold = params
                        .get("hold")
                        .and_then(Value::as_str)
                        .unwrap_or_default();
                    let index = params
                        .get("item")
                        .and_then(Value::as_i64)
                        .unwrap_or_default();
                    let state = params
                        .get("state")
                        .and_then(Value::as_str)
                        .and_then(ItemState::parse)
                        .ok_or_else(|| Error::behavior(supply::HOLDS, "mark takes a state"))?;
                    let mut item = items(cx)?
                        .into_iter()
                        .find(|item| item.hold == hold && item.item == index)
                        .ok_or_else(|| Error::behavior(supply::HOLDS, "no such item"))?;
                    item.state = state;
                    put(cx, &item)?;
                    Ok(json!({}))
                }),
                ("check", |_, cx, _| {
                    let mut refusals = Vec::new();
                    for item in items(cx)? {
                        if item.state != ItemState::Pending {
                            continue;
                        }
                        let call = SupplyCall {
                            holder: InstanceRef::of(cx),
                            hold: item.hold.clone(),
                            item: item.item,
                            ask: item.ask,
                        };
                        let answer = cx.instances().invoke(
                            &item.supply.schema,
                            &item.supply.id,
                            SupplyOperation::Check.name(),
                            &call.to_params(),
                            None,
                        )?;
                        match CheckAnswer::from_json(&answer) {
                            Some(CheckAnswer::Fits) => {}
                            Some(CheckAnswer::Refused(refusal)) => refusals.push(refusal),
                            None => {
                                return Err(Error::behavior(
                                    supply::HOLDS,
                                    format!("checkHold answered {answer}"),
                                ))
                            }
                        }
                    }
                    if refusals.is_empty() {
                        return Ok(json!({ "fits": true }));
                    }
                    Err(Error::veto(
                        supply::HOLDS,
                        "check",
                        cx.schema(),
                        cx.id(),
                        Veto {
                            reason: format!("{} items do not fit their supply", refusals.len()),
                            code: Some(supply::UNAVAILABLE.to_owned()),
                            details: Some(supply::claim_details(&refusals)),
                        },
                    ))
                }),
            ],
            fields: vec![("items", |_, view| {
                Ok(Some(Value::Array(
                    items(view)?.iter().map(HolderItem::to_json).collect(),
                )))
            })],
            ..Hooks::default()
        }
    }
}

fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

// items reads the stand-in's items, by hold in byte order, then index.
fn items<C>(view: &InstanceView<'_, C>) -> Result<Vec<HolderItem>> {
    let table = view.sql().table(ITEMS_TABLE)?;
    let select =
        format!("SELECT item_json FROM {table} WHERE namespace = ? AND schema = ? AND id = ?");
    view.sql()
        .all(
            Sql::per(
                format!("{select} ORDER BY hold, item"),
                format!("{select} ORDER BY hold COLLATE \"C\", item"),
            ),
            &key(view),
        )?
        .iter()
        .map(|row| {
            let text = row.text("item_json")?;
            serde_json::from_str::<Value>(text)
                .ok()
                .as_ref()
                .and_then(HolderItem::from_json)
                .ok_or_else(|| Error::behavior(supply::HOLDS, format!("a stored item: {text}")))
        })
        .collect()
}

fn put(cx: &OperationCx<'_, Value>, item: &HolderItem) -> Result<()> {
    let table = cx.sql().table(ITEMS_TABLE)?;
    let mut values = key(cx);
    values.extend([
        SqlValue::from(item.hold.as_str()),
        SqlValue::from(item.item),
        SqlValue::from(item.to_json().to_string()),
    ]);
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, hold, item, item_json) VALUES (?, ?, ?, ?, ?, ?)
             ON CONFLICT (namespace, schema, id, hold, item) DO UPDATE SET item_json = excluded.item_json"
        ),
        &values,
    )?;
    Ok(())
}
