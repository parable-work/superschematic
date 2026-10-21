//! Holds, one claim's holds on any supply, tied to its Workflow (D70): a
//! checkout, a booking, a rental. The config names the holds: a
//! single-valued `link` of the type's Links, one item, or `each`, an own
//! list field whose elements each name a `target` id of a `schema`, one
//! item per element at its index. Each hold asks a `quantity` (a count or
//! a field) of counted supply, or an `interval` (`start` with `end` or
//! `durationMs`, date-time fields read as Timers reads them) of timed
//! supply, read from the element for an `each` hold and from the type's
//! own fields for a `link` hold. An element that names no supply, or asks
//! nothing (a quantity under 1, no start), is no item.
//!
//! Holds reaches a supply only through the supply contract
//! ([`crate::supply`]), by name, so an extension's supply is held as
//! `Stock` and `Calendar` are, and it never reads a supply's tables. For
//! each move it writes the item's new state first, then calls the
//! supply, in the same transaction: a supply's holder check reads
//! `Holds.items` and takes a call only for an item already in the state
//! the call moves its record to.
//!
//! Taking. A create into a `takeOn` state (any create when the config
//! names none), a transition into one, and an update in one that changes
//! what the items ask while the claim holds nothing take the holds. The
//! guard asks each supply's `checkHold` about each item still to hold and
//! vetoes with `unavailable`, its details listing every item that does not
//! fit; afterChange then writes the items, pending, and holds each through
//! `holdFor`, each call a savepoint of the call's transaction, so the
//! claim holds every item or none. The unit price and currency `holdFor`
//! returns are kept on the item. `retakeHolds` takes again, refused as an
//! entry is, while the claim is in a `takeOn` state and an item is not
//! held: after a new version adds a hold, say (`not_taking` otherwise).
//!
//! Moving. afterChange reads the status from the instance as it is, never
//! from which operation ran, so a Timers fire, a Lease expiry and a
//! Reactions move are caught as a caller's transition is: in a `commitOn`
//! state each held item is committed (`commitFor`), in a `releaseOn` state
//! each pending or held one released (`releaseFor`, for a held one), in a
//! `returnOn` state each committed item of counted supply returned
//! (`returnFor`). A move into a `commitOn` state while an item is neither
//! held nor committed is `holds_missing`. Holds has no release of its own:
//! a claim lets go by a move of its status, which runs its guards and
//! leaves its event. Whether the status entered a state is read from
//! Workflow's status and enteredAt, which Holds keeps beside its own last
//! reading.
//!
//! An item's state only advances: `pending → held → committed →
//! returned`, or `pending | held → released`. A supply records each item
//! of a claim once, so a claim that let go of an item never holds it
//! again: `parse_config` refuses a transition from a `releaseOn` or
//! `returnOn` state into a `takeOn` state, and an item let go of stays so.
//!
//! While the claim holds (an item held or committed) an update that
//! changes a field a hold reads (an `each` list, a `link` hold's quantity
//! or interval fields) and a Links move of a `link` hold's link are
//! `held_fields`. A delete releases what the claim holds and returns what
//! it committed of counted supply; it is `committed` while it holds a
//! committed item it cannot return (no `returnOn`, or timed supply). The
//! claim records a reference to each supply while an item holds from it,
//! which hears the supply's delete; the supply refuses that delete itself
//! (`held`). More items than `maxItems` (100 when absent) is
//! `too_many_items`, and an item whose supply does not exist, or does not
//! serve the contract, is `no_supply`.
//!
//! The fields: `Holds.items`, each `{ hold, item, schema, id, quantity |
//! startMs and endMs, state, unitPrice?, currency? }`, by hold name in byte
//! order and then index: every item the claim has taken, as it stands,
//! and every item its fields and links ask that it has not, pending, so a
//! cart shows what it would hold; `Holds.state`, the least advanced item's
//! state, `none` before any is asked; and `Holds.total`, `{ amount,
//! currency }` when every item has a price in one currency, null
//! otherwise. Holds stores an item once it takes it. None is volatile: each
//! moves only with the claim's own events (its fields, its links, its
//! moves), so a Reactions rule hears `Holds.state` turn `committed`. A
//! move into a `commitOn` state checks the items as the field reads them.
//!
//! Expiry is a Timers deadline the schema writes, moving the claim into a
//! `releaseOn` state; the guidance names a `takeOn` state that no deadline
//! leaves.
//!
//! configChange: holds, states and `maxItems` may change; Holds joins a
//! schema that has instances (whose items are taken at their next entry
//! into a `takeOn` state, or by `retakeHolds`) and leaves only one that has
//! none, since what they hold would stay held.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, DescribeTarget, Hears, InstanceCx, InstanceView, OperationCx,
    TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{js_keys, number_value};
use superschematic_engine::storage::{Sql, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineErrorCode, Error, GuardRequest, Hooks, InstanceChange, Result,
};
use superschematic_engine_behaviors::{link_many, parse_date_time};

use crate::supply::{
    self, Ask, CheckAnswer, HolderItem, InstanceRef, Interval, ItemState, Price, Refusal,
    SupplyCall, SupplyOperation,
};

const NAME: &str = supply::HOLDS;
const ITEMS_TABLE: &str = "items";

/// The most items a claim's holds ask together when the config gives no
/// maxItems.
pub const DEFAULT_MAX_ITEMS: i64 = 100;

const WORKFLOW_STATUS: &str = "Workflow.status";
const WORKFLOW_ENTERED_AT: &str = "Workflow.enteredAt";
const LINKS_TARGETS: &str = "Links.targets";

/// A move into a `commitOn` state while an item is not held.
pub const HOLDS_MISSING: &str = "holds_missing";
/// An update of a field a hold reads while the claim holds.
pub const HELD_FIELDS: &str = "held_fields";
/// A delete while the claim holds a committed item it cannot return.
pub const COMMITTED: &str = "committed";
/// More items than maxItems.
pub const TOO_MANY_ITEMS: &str = "too_many_items";
/// retakeHolds outside a takeOn state, or with nothing to take.
pub const NOT_TAKING: &str = "not_taking";
/// An item whose supply does not exist or serves no contract.
pub const NO_SUPPLY: &str = "no_supply";

/// Where a hold's items come from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum HoldSource {
    /// A single-valued link of the type's Links: one item, index 0.
    Link {
        link: String,
        /// The link's target schema, from the Links config.
        schema: String,
    },
    /// An own list field whose elements each name a supply.
    Each {
        field: String,
        target: String,
        schema: String,
    },
}

/// A count, or a field that holds one.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Amount {
    Count(i64),
    Field(String),
}

/// How a timed hold's interval ends.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum IntervalEnd {
    /// A date-time field.
    Field(String),
    DurationMs(i64),
}

/// What each item of a hold asks.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum HoldAsk {
    Quantity(Amount),
    Interval { start: String, end: IntervalEnd },
}

/// One hold of the config.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HoldSpec {
    pub source: HoldSource,
    pub ask: HoldAsk,
}

impl HoldSpec {
    /// The own fields the hold reads: an `each` hold's list, a `link`
    /// hold's quantity and interval fields.
    pub fn reads(&self) -> Vec<String> {
        match &self.source {
            HoldSource::Each { field, .. } => vec![field.clone()],
            HoldSource::Link { .. } => {
                let mut out = Vec::new();
                match &self.ask {
                    HoldAsk::Quantity(Amount::Field(field)) => out.push(field.clone()),
                    HoldAsk::Quantity(Amount::Count(_)) => {}
                    HoldAsk::Interval { start, end } => {
                        out.push(start.clone());
                        if let IntervalEnd::Field(end) = end {
                            out.push(end.clone());
                        }
                    }
                }
                out
            }
        }
    }
}

/// Holds' config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HoldsConfig {
    /// The holds, by name, in the config's order.
    pub holds: Vec<(String, HoldSpec)>,
    /// The states whose entry takes the holds; `None` for the create alone.
    pub take_on: Option<Vec<String>>,
    pub commit_on: Vec<String>,
    pub release_on: Vec<String>,
    pub return_on: Vec<String>,
    pub max_items: i64,
    /// The Workflow's initial state.
    pub initial: Option<String>,
}

impl HoldsConfig {
    pub fn hold(&self, name: &str) -> Option<&HoldSpec> {
        self.holds
            .iter()
            .find(|(known, _)| known == name)
            .map(|(_, spec)| spec)
    }

    fn takes_in(&self, status: Option<&str>) -> bool {
        match (&self.take_on, status) {
            (Some(states), Some(status)) => states.iter().any(|state| state == status),
            _ => false,
        }
    }

    // takes_at_create says whether a create into `initial` takes.
    fn takes_at_create(&self) -> bool {
        match &self.take_on {
            None => true,
            Some(_) => self.takes_in(self.initial.as_deref()),
        }
    }

    /// The own fields some hold reads.
    pub fn reads(&self) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        for (_, spec) in &self.holds {
            for field in spec.reads() {
                if !out.contains(&field) {
                    out.push(field);
                }
            }
        }
        out
    }

    // settled says whether a status lets go or keeps: a commitOn, releaseOn
    // or returnOn state.
    fn settled(&self, status: Option<&str>) -> bool {
        status.is_some_and(|status| {
            [&self.commit_on, &self.release_on, &self.return_on]
                .iter()
                .any(|states| states.iter().any(|state| state == status))
        })
    }
}

fn contains(states: &[String], status: Option<&str>) -> bool {
    status.is_some_and(|status| states.iter().any(|state| state == status))
}

// key is the instance's rows as Holds' SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

fn integer(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    if let Some(exact) = number.as_i64() {
        return Some(exact);
    }
    let value = number_value(number);
    (value.is_finite() && value.fract() == 0.0).then_some(value as i64)
}

const ITEM_COLUMNS: &str =
    "hold, item, supply_schema, supply_id, quantity, start_ms, end_ms, state, unit_price, currency";

/// The claim's stored items, by hold name in byte order, then index.
pub fn stored(view: &InstanceView<'_, HoldsConfig>) -> Result<Vec<HolderItem>> {
    let table = view.sql().table(ITEMS_TABLE)?;
    let select =
        format!("SELECT {ITEM_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ?");
    let rows = view.sql().all(
        Sql::per(
            format!("{select} ORDER BY hold, item"),
            format!("{select} ORDER BY hold COLLATE \"C\", item"),
        ),
        &key(view),
    )?;
    let mut out = Vec::with_capacity(rows.len());
    for row in &rows {
        let ask = match (
            row.opt_i64("quantity")?,
            row.opt_i64("start_ms")?,
            row.opt_i64("end_ms")?,
        ) {
            (Some(quantity), _, _) => Ask::Quantity(quantity),
            (None, Some(start_ms), Some(end_ms)) => Ask::Interval(Interval { start_ms, end_ms }),
            _ => return Err(Error::behavior(NAME, "a stored item asks nothing")),
        };
        let state = row.text("state")?;
        let state = ItemState::parse(state)
            .ok_or_else(|| Error::behavior(NAME, format!("a stored item is {state}")))?;
        let currency = row.opt_text("currency")?.map(str::to_owned);
        out.push(HolderItem {
            hold: row.text("hold")?.to_owned(),
            item: row.i64("item")?,
            supply: InstanceRef::new(row.text("supply_schema")?, row.text("supply_id")?),
            ask,
            state,
            price: row.opt_i64("unit_price")?.map(|unit_price| Price {
                unit_price,
                currency,
            }),
        });
    }
    Ok(out)
}

// put writes one item.
fn put(cx: &InstanceCx<'_, HoldsConfig>, item: &HolderItem) -> Result<()> {
    let table = cx.sql().table(ITEMS_TABLE)?;
    let mut values = key(cx);
    values.extend([
        SqlValue::from(item.hold.as_str()),
        SqlValue::from(item.item),
        SqlValue::from(item.supply.schema.as_str()),
        SqlValue::from(item.supply.id.as_str()),
        SqlValue::from(item.ask.quantity()),
        SqlValue::from(item.ask.interval().map(|interval| interval.start_ms)),
        SqlValue::from(item.ask.interval().map(|interval| interval.end_ms)),
        SqlValue::from(item.state.as_str()),
        SqlValue::from(item.price.as_ref().map(|price| price.unit_price)),
        SqlValue::from(item.price.as_ref().and_then(|price| price.currency.clone())),
    ]);
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, {ITEM_COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT (namespace, schema, id, hold, item) DO UPDATE SET
               supply_schema = excluded.supply_schema, supply_id = excluded.supply_id,
               quantity = excluded.quantity, start_ms = excluded.start_ms, end_ms = excluded.end_ms,
               state = excluded.state, unit_price = excluded.unit_price, currency = excluded.currency"
        ),
        &values,
    )?;
    Ok(())
}

// drop_item deletes one item.
fn drop_item(cx: &InstanceCx<'_, HoldsConfig>, item: &HolderItem) -> Result<()> {
    let table = cx.sql().table(ITEMS_TABLE)?;
    let mut values = key(cx);
    values.extend([
        SqlValue::from(item.hold.as_str()),
        SqlValue::from(item.item),
    ]);
    cx.sql().run(
        format!(
            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND hold = ? AND item = ?"
        ),
        &values,
    )?;
    Ok(())
}

/// The least advanced state of the items: `none` before any.
pub fn state_of(items: &[HolderItem]) -> &'static str {
    items
        .iter()
        .map(|item| item.state)
        .min_by_key(|state| ItemState::ALL.iter().position(|known| known == state))
        .map_or("none", ItemState::as_str)
}

/// The sum of the items' prices times their quantities, as `{ amount,
/// currency }`, when every item has a price in one currency; null
/// otherwise.
pub fn total_of(items: &[HolderItem]) -> Value {
    let mut amount: i64 = 0;
    let mut currency: Option<&str> = None;
    if items.is_empty() {
        return Value::Null;
    }
    for item in items {
        let Some(price) = &item.price else {
            return Value::Null;
        };
        let Some(code) = price.currency.as_deref() else {
            return Value::Null;
        };
        if currency.is_some_and(|known| known != code) {
            return Value::Null;
        }
        currency = Some(code);
        let units = item.ask.quantity().unwrap_or(1);
        let Some(next) = price
            .unit_price
            .checked_mul(units)
            .and_then(|line| amount.checked_add(line))
        else {
            return Value::Null;
        };
        amount = next;
    }
    json!({ "amount": amount, "currency": currency })
}

fn summary(items: &[HolderItem]) -> Value {
    json!({
        "items": items.iter().map(HolderItem::to_json).collect::<Vec<_>>(),
        "state": state_of(items),
        "total": total_of(items),
    })
}

/// The status and when it was entered, as Workflow's fields read now.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
struct Status {
    status: Option<String>,
    entered_at: Option<i64>,
}

// status_of reads the claim's Workflow status and enteredAt, and its Links
// targets when a hold reads a link.
fn status_of(view: &InstanceView<'_, HoldsConfig>) -> Result<(Status, Map<String, Value>)> {
    let links = view
        .config()
        .holds
        .iter()
        .any(|(_, spec)| matches!(spec.source, HoldSource::Link { .. }));
    let fields: &[&str] = if links {
        &[WORKFLOW_STATUS, WORKFLOW_ENTERED_AT, LINKS_TARGETS]
    } else {
        &[WORKFLOW_STATUS, WORKFLOW_ENTERED_AT]
    };
    let record = view
        .instances()
        .get(view.schema(), view.id(), Some(fields))?;
    let field = |behavior: &str, name: &str| {
        record
            .as_ref()
            .and_then(|record| record.behavior_field(behavior, name))
    };
    let status = Status {
        status: field("Workflow", "status")
            .and_then(Value::as_str)
            .map(str::to_owned),
        entered_at: field("Workflow", "enteredAt").and_then(integer),
    };
    let targets = field("Links", "targets")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    Ok((status, targets))
}

/// The items the claim's holds ask now, as `Holds.items` reads them: each
/// stored item past pending as it is, and each item its own fields and
/// links ask that is not held yet, pending.
pub fn items_now(view: &InstanceView<'_, HoldsConfig>) -> Result<Vec<HolderItem>> {
    let (_, targets) = status_of(view)?;
    let data = view.data()?;
    Ok(merge(&stored(view)?, &plan(view.config(), &data, &targets)))
}

// link_target is a link's target id: from Links.targets as a read returns
// it ({ schema, id }), or from a create's Links parameters (an id, or
// { id }).
fn link_target(given: Option<&Value>) -> Option<String> {
    match given? {
        Value::String(id) => Some(id.clone()),
        Value::Object(link) => link.get("id").and_then(Value::as_str).map(str::to_owned),
        _ => None,
    }
}

fn date_time(value: Option<&Value>) -> Option<i64> {
    parse_date_time(value?.as_str()?)
}

// ask_of is what an item asks, read from `source` (an element, or the
// type's own fields); `None` when it asks nothing.
fn ask_of(spec: &HoldSpec, source: &Map<String, Value>) -> Option<Ask> {
    match &spec.ask {
        HoldAsk::Quantity(Amount::Count(count)) => Some(Ask::Quantity(*count)),
        HoldAsk::Quantity(Amount::Field(field)) => source
            .get(field)
            .and_then(integer)
            .filter(|quantity| *quantity >= 1)
            .map(Ask::Quantity),
        HoldAsk::Interval { start, end } => {
            let start_ms = date_time(source.get(start))?;
            let end_ms = match end {
                IntervalEnd::Field(field) => date_time(source.get(field))?,
                IntervalEnd::DurationMs(ms) => start_ms.checked_add(*ms)?,
            };
            (end_ms > start_ms).then_some(Ask::Interval(Interval { start_ms, end_ms }))
        }
    }
}

/// The items the claim's holds ask now, each pending: from its own fields
/// `data` and its links' targets (`targets`, by link name).
pub fn plan(
    config: &HoldsConfig,
    data: &Map<String, Value>,
    targets: &Map<String, Value>,
) -> Vec<HolderItem> {
    let mut out = Vec::new();
    for (name, spec) in &config.holds {
        match &spec.source {
            HoldSource::Link { link, schema } => {
                let (Some(id), Some(ask)) = (link_target(targets.get(link)), ask_of(spec, data))
                else {
                    continue;
                };
                out.push(HolderItem {
                    hold: name.clone(),
                    item: 0,
                    supply: InstanceRef::new(schema.as_str(), id),
                    ask,
                    state: ItemState::Pending,
                    price: None,
                });
            }
            HoldSource::Each {
                field,
                target,
                schema,
            } => {
                let Some(elements) = data.get(field).and_then(Value::as_array) else {
                    continue;
                };
                for (index, element) in elements.iter().enumerate() {
                    let Some(element) = element.as_object() else {
                        continue;
                    };
                    let Some(id) = element
                        .get(target)
                        .and_then(Value::as_str)
                        .filter(|id| !id.is_empty())
                    else {
                        continue;
                    };
                    let Some(ask) = ask_of(spec, element) else {
                        continue;
                    };
                    out.push(HolderItem {
                        hold: name.clone(),
                        item: index as i64,
                        supply: InstanceRef::new(schema.as_str(), id),
                        ask,
                        state: ItemState::Pending,
                        price: None,
                    });
                }
            }
        }
    }
    out.sort_by(|a, b| {
        a.hold
            .as_bytes()
            .cmp(b.hold.as_bytes())
            .then(a.item.cmp(&b.item))
    });
    out
}

/// The items after a take: each stored item that is past pending stays as
/// it is (a supply records each item once); each planned item takes the
/// place of a pending one at its hold and index; a pending item the plan
/// no longer asks goes.
pub fn merge(stored: &[HolderItem], planned: &[HolderItem]) -> Vec<HolderItem> {
    let mut out: Vec<HolderItem> = stored
        .iter()
        .filter(|item| item.state != ItemState::Pending)
        .cloned()
        .collect();
    for item in planned {
        if !out
            .iter()
            .any(|known| known.hold == item.hold && known.item == item.item)
        {
            out.push(item.clone());
        }
    }
    out.sort_by(|a, b| {
        a.hold
            .as_bytes()
            .cmp(b.hold.as_bytes())
            .then(a.item.cmp(&b.item))
    });
    out
}

fn veto(reason: String, code: &str, details: Option<Map<String, Value>>) -> Veto {
    Veto {
        reason,
        code: Some(code.to_owned()),
        details,
    }
}

fn call_of<C>(view: &InstanceView<'_, C>, item: &HolderItem) -> SupplyCall {
    SupplyCall {
        holder: InstanceRef::of(view),
        hold: item.hold.clone(),
        item: item.item,
        ask: item.ask,
    }
}

// absent says whether an error is the supply's absence: no instance, or no
// such operation on its schema.
fn absent(error: &Error) -> bool {
    matches!(
        error.code(),
        Some(EngineErrorCode::NotFound | EngineErrorCode::InvalidArgument)
    )
}

// check asks each pending item's supply whether it fits, and vetoes with
// too_many_items, no_supply, or unavailable listing every item that does
// not fit.
fn check(view: &InstanceView<'_, HoldsConfig>, items: &[HolderItem]) -> Result<Option<Veto>> {
    let max = view.config().max_items;
    if items.len() as i64 > max {
        let mut details = Map::new();
        details.insert("items".into(), Value::from(items.len()));
        details.insert("max".into(), Value::from(max));
        return Ok(Some(veto(
            format!(
                "the holds ask {} items, more than the {max} one claim holds",
                items.len()
            ),
            TOO_MANY_ITEMS,
            Some(details),
        )));
    }
    let mut refusals: Vec<Refusal> = Vec::new();
    for item in items.iter().filter(|item| item.state == ItemState::Pending) {
        let call = call_of(view, item);
        let answer = match view.instances().invoke(
            &item.supply.schema,
            &item.supply.id,
            SupplyOperation::Check.name(),
            &call.to_params(),
            None,
        ) {
            Ok(answer) => answer,
            Err(error) if absent(&error) => {
                let mut details = Map::new();
                details.insert("supply".into(), item.supply.to_json());
                details.insert("hold".into(), Value::from(item.hold.as_str()));
                details.insert("item".into(), Value::from(item.item));
                return Ok(Some(veto(
                    format!(
                        "item {} of hold {} names {} {}, which holds nothing for a claim: {error}",
                        item.item, item.hold, item.supply.schema, item.supply.id
                    ),
                    NO_SUPPLY,
                    Some(details),
                )));
            }
            Err(error) => return Err(error),
        };
        match CheckAnswer::from_json(&answer) {
            Some(CheckAnswer::Fits) => {}
            Some(CheckAnswer::Refused(refusal)) => refusals.push(refusal),
            None => {
                return Err(Error::behavior(
                    NAME,
                    format!(
                        "{} {} answered checkHold with {answer}",
                        item.supply.schema, item.supply.id
                    ),
                ))
            }
        }
    }
    if refusals.is_empty() {
        return Ok(None);
    }
    Ok(Some(veto(
        if refusals.len() == 1 {
            refusals[0].reason.clone()
        } else {
            format!("{} items do not fit their supply", refusals.len())
        },
        supply::UNAVAILABLE,
        Some(supply::claim_details(&refusals)),
    )))
}

// moved writes an item's new state, then calls its supply's operation,
// and keeps the price holdFor returns. A return whose supply is gone
// moves the item alone: the units went with it.
fn moved(
    cx: &InstanceCx<'_, HoldsConfig>,
    item: &mut HolderItem,
    operation: SupplyOperation,
) -> Result<()> {
    let Some(to) = operation.state() else {
        return Ok(());
    };
    item.state = to;
    put(cx, item)?;
    let call = call_of(cx, item);
    let answer = match cx.instances().invoke(
        &item.supply.schema,
        &item.supply.id,
        operation.name(),
        &call.to_params(),
        None,
    ) {
        Ok(answer) => answer,
        Err(error)
            if operation == SupplyOperation::Return
                && error.code() == Some(EngineErrorCode::NotFound)
                && cx
                    .instances()
                    .get(&item.supply.schema, &item.supply.id, Some(&[]))?
                    .is_none() =>
        {
            return Ok(());
        }
        Err(error) => return Err(error),
    };
    if operation == SupplyOperation::Hold {
        item.price = answer
            .get("unitPrice")
            .and_then(integer)
            .map(|unit_price| Price {
                unit_price,
                currency: answer
                    .get("currency")
                    .and_then(Value::as_str)
                    .map(str::to_owned),
            });
        put(cx, item)?;
    }
    Ok(())
}

// take writes the items, the pending ones first, then holds each pending
// one; too many items is too_many_items. A supply that refuses vetoes the
// call, which rolls every hold back.
fn take(cx: &InstanceCx<'_, HoldsConfig>, items: Vec<HolderItem>) -> Result<Vec<HolderItem>> {
    let max = cx.config().max_items;
    if items.len() as i64 > max {
        return Err(Error::veto(
            NAME,
            "take",
            cx.schema(),
            cx.id(),
            veto(
                format!(
                    "the holds ask {} items, more than the {max} one claim holds",
                    items.len()
                ),
                TOO_MANY_ITEMS,
                None,
            ),
        ));
    }
    let before = stored(cx)?;
    for gone in before.iter().filter(|item| {
        item.state == ItemState::Pending
            && !items
                .iter()
                .any(|known| known.hold == item.hold && known.item == item.item)
    }) {
        drop_item(cx, gone)?;
    }
    for item in items.iter().filter(|item| item.state == ItemState::Pending) {
        put(cx, item)?;
    }
    let mut out = items;
    for item in out.iter_mut() {
        if item.state == ItemState::Pending {
            moved(cx, item, SupplyOperation::Hold)?;
        }
    }
    Ok(out)
}

// settle moves the items as the status asks: commits the held ones in a
// commitOn state, releases the pending and held ones in a releaseOn
// state, returns the committed ones of counted supply in a returnOn state.
fn settle(
    cx: &InstanceCx<'_, HoldsConfig>,
    items: &mut [HolderItem],
    status: Option<&str>,
) -> Result<()> {
    let config = cx.config();
    let operation = if contains(&config.commit_on, status) {
        SupplyOperation::Commit
    } else if contains(&config.release_on, status) {
        SupplyOperation::Release
    } else if contains(&config.return_on, status) {
        SupplyOperation::Return
    } else {
        return Ok(());
    };
    for item in items.iter_mut() {
        match (operation, item.state) {
            (SupplyOperation::Commit, ItemState::Held)
            | (SupplyOperation::Release, ItemState::Held) => moved(cx, item, operation)?,
            // Nothing was held for a pending item: it is let go alone.
            (SupplyOperation::Release, ItemState::Pending) => {
                item.state = ItemState::Released;
                put(cx, item)?;
            }
            (SupplyOperation::Return, ItemState::Committed) if item.ask.quantity().is_some() => {
                moved(cx, item, operation)?;
            }
            _ => {}
        }
    }
    Ok(())
}

// refer keeps a reference to each supply an item holds from, which hears
// its delete, and drops the others.
fn refer(cx: &InstanceCx<'_, HoldsConfig>, items: &[HolderItem]) -> Result<()> {
    let mut holding: Vec<&InstanceRef> = Vec::new();
    for item in items.iter().filter(|item| item.state == ItemState::Held) {
        if !holding.contains(&&item.supply) {
            holding.push(&item.supply);
        }
    }
    let existing = cx.references().list()?;
    for reference in &existing {
        if !holding
            .iter()
            .any(|supply| supply.schema == reference.schema && supply.id == reference.id)
        {
            cx.references()
                .remove(&reference.schema, &reference.id, &reference.key)?;
        }
    }
    for supply in holding {
        if !existing
            .iter()
            .any(|reference| reference.schema == supply.schema && reference.id == supply.id)
        {
            cx.references()
                .add(&supply.schema, &supply.id, "", Some(Hears::Delete))?;
        }
    }
    Ok(())
}

// seen is the status Holds last read, and when it was entered.
fn seen(cx: &InstanceView<'_, HoldsConfig>) -> Result<Status> {
    let row = cx.columns().get()?;
    Ok(Status {
        status: row.opt_text("status")?.map(str::to_owned),
        entered_at: row.opt_i64("entered_at")?,
    })
}

fn holding(items: &[HolderItem]) -> bool {
    items.iter().any(|item| item.state.takes())
}

// after_change takes, commits, releases and returns as the status the
// change left asks, and keeps the references.
fn after_change(
    _: &Holds,
    cx: &InstanceCx<'_, HoldsConfig>,
    change: &InstanceChange<'_>,
) -> Result<()> {
    if matches!(change, InstanceChange::Delete) {
        return let_go(cx);
    }
    let config = cx.config();
    let (status, targets) = status_of(cx)?;
    let last = seen(cx)?;
    if last != status {
        cx.columns().set(&[
            ("status", SqlValue::from(status.status.clone())),
            ("entered_at", SqlValue::from(status.entered_at)),
        ])?;
    }
    let mut items = stored(cx)?;
    let entered = last != status && config.takes_in(status.status.as_deref());
    // An update that changed what the items ask, which the guard checked.
    let asks_moved = match change {
        InstanceChange::Update { patch, before } => {
            let data = cx.data()?;
            config.takes_in(status.status.as_deref())
                && !holding(&items)
                && config
                    .reads()
                    .iter()
                    .any(|field| patch.contains_key(field) && before.get(field) != data.get(field))
        }
        _ => false,
    };
    let creates = matches!(change, InstanceChange::Create) && config.take_on.is_none();
    if creates || entered || asks_moved {
        let data = cx.data()?;
        let planned = plan(config, &data, &targets);
        items = take(cx, merge(&items, &planned))?;
    }
    settle(cx, &mut items, status.status.as_deref())?;
    refer(cx, &items)
}

// let_go releases what a deleted claim held and returns what it committed
// of counted supply, through the supply, which reads the claim as gone,
// then deletes its rows.
fn let_go(cx: &InstanceCx<'_, HoldsConfig>) -> Result<()> {
    for item in stored(cx)? {
        let operation = match item.state {
            ItemState::Held => SupplyOperation::Release,
            ItemState::Committed if item.ask.quantity().is_some() => SupplyOperation::Return,
            _ => continue,
        };
        let exists = cx
            .instances()
            .get(&item.supply.schema, &item.supply.id, Some(&[]))?
            .is_some();
        if !exists {
            continue;
        }
        cx.instances().invoke(
            &item.supply.schema,
            &item.supply.id,
            operation.name(),
            &call_of(cx, &item).to_params(),
            None,
        )?;
    }
    let table = cx.sql().table(ITEMS_TABLE)?;
    cx.sql().run(
        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
        &key(cx),
    )?;
    Ok(())
}

// guard: unavailable, too_many_items and no_supply for a take;
// holds_missing for a commit; held_fields while the claim holds;
// committed for a delete.
fn guard(
    _: &Holds,
    view: &InstanceView<'_, HoldsConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    let config = view.config();
    match request {
        GuardRequest::Create { data, behaviors } => {
            if !config.takes_at_create() {
                return Ok(None);
            }
            let targets = behaviors
                .get("Links")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default();
            check(view, &plan(config, data, &targets))
        }
        GuardRequest::Operation {
            behavior: "Workflow",
            operation: "transition",
            params,
            ..
        } => {
            let to = params.get("to").and_then(Value::as_str);
            let items = stored(view)?;
            if contains(&config.commit_on, to) {
                let items = items_now(view)?;
                let missing: Vec<&HolderItem> =
                    items.iter().filter(|item| !item.state.takes()).collect();
                if !missing.is_empty() {
                    let mut details = Map::new();
                    details.insert(
                        "items".into(),
                        Value::Array(
                            missing
                                .iter()
                                .map(|item| {
                                    json!({"hold": item.hold, "item": item.item, "state": item.state.as_str()})
                                })
                                .collect(),
                        ),
                    );
                    return Ok(Some(veto(
                        format!(
                            "{} of the claim's items {} not held, so it cannot move to {}",
                            missing.len(),
                            if missing.len() == 1 { "is" } else { "are" },
                            to.unwrap_or_default()
                        ),
                        HOLDS_MISSING,
                        Some(details),
                    )));
                }
                return Ok(None);
            }
            if !config.takes_in(to) {
                return Ok(None);
            }
            // A transition into the state it is in enters it again.
            let (_, targets) = status_of(view)?;
            let data = view.data()?;
            check(view, &merge(&items, &plan(config, &data, &targets)))
        }
        GuardRequest::Operation {
            behavior: "Links",
            operation: "link" | "unlink",
            params,
            ..
        } => {
            let name = params.get("name").and_then(Value::as_str);
            let reads = config.holds.iter().any(|(_, spec)| {
                matches!(&spec.source, HoldSource::Link { link, .. } if Some(link.as_str()) == name)
            });
            if !reads || !holding(&stored(view)?) {
                return Ok(None);
            }
            let mut details = Map::new();
            details.insert("fields".into(), json!([name]));
            Ok(Some(veto(
                format!(
                    "link {} names what the claim holds; it does not move while the claim holds",
                    name.unwrap_or_default()
                ),
                HELD_FIELDS,
                Some(details),
            )))
        }
        GuardRequest::Update { patch, after, .. } => {
            let before = view.data()?;
            let changed: Vec<String> = config
                .reads()
                .into_iter()
                .filter(|field| patch.contains_key(field) && before.get(field) != after.get(field))
                .collect();
            if changed.is_empty() {
                return Ok(None);
            }
            let items = stored(view)?;
            if holding(&items) {
                let mut details = Map::new();
                details.insert("fields".into(), json!(changed));
                return Ok(Some(veto(
                    format!(
                        "{} {} what the claim holds; {} not change while it holds",
                        changed.join(", "),
                        if changed.len() == 1 { "reads" } else { "read" },
                        if changed.len() == 1 {
                            "it does"
                        } else {
                            "they do"
                        }
                    ),
                    HELD_FIELDS,
                    Some(details),
                )));
            }
            let (status, targets) = status_of(view)?;
            if !config.takes_in(status.status.as_deref()) {
                return Ok(None);
            }
            check(view, &merge(&items, &plan(config, after, &targets)))
        }
        GuardRequest::Delete { .. } => {
            let stuck: Vec<HolderItem> = stored(view)?
                .into_iter()
                .filter(|item| {
                    item.state == ItemState::Committed
                        && (config.return_on.is_empty() || item.ask.quantity().is_none())
                })
                .collect();
            if stuck.is_empty() {
                return Ok(None);
            }
            let mut details = Map::new();
            details.insert(
                "items".into(),
                Value::Array(
                    stuck
                        .iter()
                        .map(|item| json!({"hold": item.hold, "item": item.item}))
                        .collect(),
                ),
            );
            Ok(Some(veto(
                format!(
                    "the claim holds {} committed {}, which {} not returned",
                    stuck.len(),
                    if stuck.len() == 1 { "item" } else { "items" },
                    if stuck.len() == 1 { "is" } else { "are" }
                ),
                COMMITTED,
                Some(details),
            )))
        }
        _ => Ok(None),
    }
}

fn retake_holds(_: &Holds, cx: &OperationCx<'_, HoldsConfig>, _: &Value) -> Result<Value> {
    let config = cx.config();
    let (status, targets) = status_of(cx)?;
    let in_take = match &config.take_on {
        Some(_) => config.takes_in(status.status.as_deref()),
        None => !config.settled(status.status.as_deref()),
    };
    let refuse = |reason: String| {
        Error::veto(
            NAME,
            "retakeHolds",
            cx.schema(),
            cx.id(),
            veto(reason, NOT_TAKING, None),
        )
    };
    if !in_take {
        return Err(refuse(format!(
            "the claim is {}, no state that takes holds",
            status.status.as_deref().unwrap_or("in no state")
        )));
    }
    let data = cx.data()?;
    let items = merge(&stored(cx)?, &plan(config, &data, &targets));
    if !items.iter().any(|item| item.state == ItemState::Pending) {
        return Err(refuse(
            "the claim holds every item its holds ask".to_owned(),
        ));
    }
    if let Some(refused) = check(cx, &items)? {
        return Err(Error::veto(
            NAME,
            "retakeHolds",
            cx.schema(),
            cx.id(),
            refused,
        ));
    }
    let items = take(cx, items)?;
    refer(cx, &items)?;
    Ok(summary(&items))
}

fn list_holds(_: &Holds, cx: &OperationCx<'_, HoldsConfig>, _: &Value) -> Result<Value> {
    let items = items_now(cx)?;
    let mut listed = Vec::with_capacity(items.len());
    for item in &items {
        let mut entry = item.to_json();
        if item.state == ItemState::Pending {
            let answer = match cx.instances().invoke(
                &item.supply.schema,
                &item.supply.id,
                SupplyOperation::Check.name(),
                &call_of(cx, item).to_params(),
                None,
            ) {
                Ok(answer) => answer,
                Err(error) if absent(&error) => json!({
                    "fits": false,
                    "code": NO_SUPPLY,
                    "reason": format!("{} {} holds nothing for a claim: {error}", item.supply.schema, item.supply.id),
                }),
                Err(error) => return Err(error),
            };
            if let Value::Object(entry) = &mut entry {
                entry.insert("check".into(), answer);
            }
        }
        listed.push(entry);
    }
    Ok(json!({
        "items": listed,
        "state": state_of(&items),
        "total": total_of(&items),
    }))
}

/// A Workflow config as the schema holds it.
struct Flow {
    states: Vec<String>,
    initial: Option<String>,
    transitions: Vec<(String, String)>,
}

fn flow_of(target: &ConfigTarget<'_>) -> Option<Flow> {
    let raw = target.configs().get("Workflow")?;
    let states: Vec<String> = raw
        .get("states")?
        .as_array()?
        .iter()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect();
    let initial = match raw.get("initial") {
        Some(Value::String(initial)) => Some(initial.clone()),
        _ => states.first().cloned(),
    };
    let transitions = raw
        .get("transitions")
        .and_then(Value::as_array)
        .map(|edges| {
            edges
                .iter()
                .filter_map(|edge| {
                    Some((
                        edge.get("from")?.as_str()?.to_owned(),
                        edge.get("to")?.as_str()?.to_owned(),
                    ))
                })
                .collect()
        })
        .unwrap_or_default();
    Some(Flow {
        states,
        initial,
        transitions,
    })
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

fn field_of(value: Option<&Value>) -> Option<String> {
    value?.get("field")?.as_str().map(str::to_owned)
}

// own_field holds a field a link hold reads to an own field of the type
// whose JSON type is `kind` (or, for a date-time, a string of that
// format).
fn own_field(target: &ConfigTarget<'_>, at: &str, field: &str, kind: &str) -> Result<()> {
    if !target.fields().iter().any(|known| known == field) {
        return Err(Error::config(format!(
            "{at} \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            target.fields().join(", ")
        )));
    }
    let schema = target.field_schemas().get(field);
    let types: Vec<&str> = match schema.and_then(|schema| schema.get("type")) {
        Some(Value::Array(types)) => types.iter().filter_map(Value::as_str).collect(),
        Some(Value::String(one)) => vec![one.as_str()],
        _ => Vec::new(),
    };
    let fits = match kind {
        "date-time" => {
            schema
                .and_then(|schema| schema.get("format"))
                .and_then(Value::as_str)
                == Some("date-time")
        }
        _ => types.contains(&kind),
    };
    if !fits {
        return Err(Error::config(format!(
            "{at} \"{field}\" is not {} field of {}",
            match kind {
                "integer" => "an integer",
                "array" => "a list",
                _ => "a date-time",
            },
            target.type_name()
        )));
    }
    Ok(())
}

// supply_kind checks a hold's schema, when the namespace's schemas are at
// hand, against what it asks: Stock holds units, Calendar time.
fn supply_kind(target: &ConfigTarget<'_>, at: &str, schema: &str, ask: &HoldAsk) -> Result<()> {
    let Some(schemas) = target.schemas() else {
        return Ok(());
    };
    // A supply of the schema's own name holds as the type does.
    let behaviors = if schema == target.schema() {
        target.behaviors().to_vec()
    } else {
        match schemas.get(schema)? {
            Some(found) => found.behaviors,
            None => {
                return Err(Error::config(format!(
                    "{at}: schema {schema} has no live version; publish it first"
                )))
            }
        }
    };
    let counted = matches!(ask, HoldAsk::Quantity(_));
    let wrong = if counted {
        behaviors.iter().any(|name| name == "Calendar")
    } else {
        behaviors.iter().any(|name| name == "Stock")
    };
    if wrong {
        return Err(Error::config(format!(
            "{at} asks {} of {schema}, whose {} holds {}",
            if counted { "a quantity" } else { "an interval" },
            if counted { "Calendar" } else { "Stock" },
            if counted { "time" } else { "units" }
        )));
    }
    Ok(())
}

fn parse_hold(target: &ConfigTarget<'_>, name: &str, raw: &Value) -> Result<HoldSpec> {
    let at = format!("holds.{name}");
    let link = raw.get("link").and_then(Value::as_str);
    let ask = if let Some(quantity) = raw.get("quantity") {
        HoldAsk::Quantity(match quantity {
            Value::Number(_) => Amount::Count(integer(quantity).unwrap_or(1)),
            given => Amount::Field(field_of(Some(given)).unwrap_or_default()),
        })
    } else {
        let interval = raw.get("interval");
        let start = interval
            .and_then(|interval| interval.get("start"))
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let end = match interval.and_then(|interval| interval.get("end")) {
            Some(Value::String(end)) => IntervalEnd::Field(end.clone()),
            _ => IntervalEnd::DurationMs(
                interval
                    .and_then(|interval| interval.get("durationMs"))
                    .and_then(integer)
                    .unwrap_or(0),
            ),
        };
        HoldAsk::Interval { start, end }
    };
    let source = match link {
        Some(link) => {
            if !target.behaviors().iter().any(|known| known == "Links") {
                return Err(Error::config(format!(
                    "{at}.link is a link of Links, which the type does not list"
                )));
            }
            let links = target
                .configs()
                .get("Links")
                .and_then(|config| config.get("links"))
                .and_then(Value::as_object);
            let Some(spec) = links.and_then(|links| links.get(link)) else {
                return Err(Error::config(format!(
                    "{at}.link \"{link}\" is not a link of the type's Links config (its links: {})",
                    links.map(js_keys).unwrap_or_default().join(", ")
                )));
            };
            if link_many(spec) {
                return Err(Error::config(format!(
                    "{at}.link \"{link}\" holds a list of targets, and a hold reads one"
                )));
            }
            let schema = spec
                .get("schema")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned();
            match &ask {
                HoldAsk::Quantity(Amount::Field(field)) => {
                    own_field(target, &format!("{at}.quantity.field"), field, "integer")?;
                }
                HoldAsk::Quantity(Amount::Count(_)) => {}
                HoldAsk::Interval { start, end } => {
                    own_field(target, &format!("{at}.interval.start"), start, "date-time")?;
                    if let IntervalEnd::Field(end) = end {
                        own_field(target, &format!("{at}.interval.end"), end, "date-time")?;
                    }
                }
            }
            HoldSource::Link {
                link: link.to_owned(),
                schema,
            }
        }
        None => {
            let each = raw.get("each");
            let field = field_of(each).unwrap_or_default();
            own_field(target, &format!("{at}.each.field"), &field, "array")?;
            HoldSource::Each {
                field,
                target: each
                    .and_then(|each| each.get("target"))
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
                schema: raw
                    .get("schema")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
            }
        }
    };
    let schema = match &source {
        HoldSource::Link { schema, .. } | HoldSource::Each { schema, .. } => schema.clone(),
    };
    supply_kind(target, &at, &schema, &ask)?;
    Ok(HoldSpec { source, ask })
}

/// Holds: one claim's holds on any supply, tied to its Workflow.
#[derive(Clone, Copy, Debug, Default)]
pub struct Holds;

impl Behavior for Holds {
    type Config = HoldsConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Holds.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to the type:
    // its Workflow's states, its Links and its fields.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<HoldsConfig> {
        let mut holds = Vec::new();
        if let Some(given) = raw.get("holds").and_then(Value::as_object) {
            for name in js_keys(given) {
                holds.push((name.to_owned(), parse_hold(target, name, &given[name])?));
            }
        }
        let take_on = raw.get("takeOn").map(|states| strings(Some(states)));
        let commit_on = strings(raw.get("commitOn"));
        let release_on = strings(raw.get("releaseOn"));
        let return_on = strings(raw.get("returnOn"));
        let flow = flow_of(target);
        if let Some(flow) = &flow {
            let lists: [(&str, &[String]); 4] = [
                ("takeOn", take_on.as_deref().unwrap_or(&[])),
                ("commitOn", &commit_on),
                ("releaseOn", &release_on),
                ("returnOn", &return_on),
            ];
            for (at, states) in lists {
                for state in states {
                    if !flow.states.contains(state) {
                        return Err(Error::config(format!(
                            "{at} names \"{state}\", which is not a state of the type's Workflow ({})",
                            flow.states.join(", ")
                        )));
                    }
                }
            }
            for (index, (at, states)) in lists.iter().enumerate() {
                for (other, others) in lists.iter().skip(index + 1) {
                    if let Some(state) = states.iter().find(|state| others.contains(state)) {
                        return Err(Error::config(format!(
                            "{at} and {other} both name \"{state}\": a state takes, commits, releases or returns"
                        )));
                    }
                }
            }
            // A supply records each item of a claim once: a claim that let
            // go of an item never holds it again.
            if let Some(take_on) = &take_on {
                for (from, to) in &flow.transitions {
                    if take_on.contains(to)
                        && (release_on.contains(from) || return_on.contains(from))
                    {
                        return Err(Error::config(format!(
                            "the Workflow moves from {from}, where the claim lets go of what it holds, to {to}, where it takes it; a supply holds each item of a claim once"
                        )));
                    }
                }
            }
        }
        Ok(HoldsConfig {
            holds,
            take_on,
            commit_on,
            release_on,
            return_on,
            max_items: raw
                .get("maxItems")
                .and_then(integer)
                .unwrap_or(DEFAULT_MAX_ITEMS),
            initial: flow.and_then(|flow| flow.initial),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, change| {
                if before.is_some() && after.is_none() && change.instances()? {
                    return Ok(Some("what its instances hold would stay held".to_owned()));
                }
                Ok(None)
            }),
            guidance: Some(|_, config, target: &DescribeTarget| {
                crate::guidance::holds::guidance(config, target)
            }),
            migrations: vec![BehaviorMigration::new(1, "holds")
                .column("status", ColumnSpec::text())
                .column("entered_at", ColumnSpec::integer())
                .table(
                    TableSpec::new(ITEMS_TABLE)
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("hold", ColumnSpec::text().not_null())
                        .column("item", ColumnSpec::integer().not_null())
                        .column("supply_schema", ColumnSpec::text().not_null())
                        .column("supply_id", ColumnSpec::text().not_null())
                        .column("quantity", ColumnSpec::integer())
                        .column("start_ms", ColumnSpec::integer())
                        .column("end_ms", ColumnSpec::integer())
                        .column("state", ColumnSpec::text().not_null())
                        .column("unit_price", ColumnSpec::integer())
                        .column("currency", ColumnSpec::text())
                        .primary_key(&["namespace", "schema", "id", "hold", "item"]),
                )],
            guard: Some(guard),
            operations: vec![("retakeHolds", retake_holds), ("listHolds", list_holds)],
            fields: vec![
                ("items", |_, view| {
                    Ok(Some(Value::Array(
                        items_now(view)?.iter().map(HolderItem::to_json).collect(),
                    )))
                }),
                ("state", |_, view| {
                    Ok(Some(Value::from(state_of(&items_now(view)?))))
                }),
                ("total", |_, view| Ok(Some(total_of(&items_now(view)?)))),
            ],
            after_change: Some(after_change),
            ..Hooks::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn item(hold: &str, index: i64, state: ItemState, ask: Ask) -> HolderItem {
        HolderItem {
            hold: hold.into(),
            item: index,
            supply: InstanceRef::new("Inventory", "anvil"),
            ask,
            state,
            price: None,
        }
    }

    #[test]
    fn the_state_is_the_least_advanced_items() {
        use ItemState::*;
        assert_eq!(state_of(&[]), "none");
        let items = [
            item("lines", 0, Committed, Ask::Quantity(1)),
            item("lines", 1, Returned, Ask::Quantity(1)),
        ];
        assert_eq!(state_of(&items), "committed");
        let items = [
            item("lines", 0, Held, Ask::Quantity(1)),
            item("lines", 1, Pending, Ask::Quantity(1)),
        ];
        assert_eq!(state_of(&items), "pending");
    }

    #[test]
    fn a_total_needs_every_price_in_one_currency() {
        let priced = |unit_price: i64, currency: &str, ask: Ask| HolderItem {
            price: Some(Price {
                unit_price,
                currency: Some(currency.into()),
            }),
            ..item("lines", 0, ItemState::Held, ask)
        };
        let slot = Ask::Interval(Interval {
            start_ms: 0,
            end_ms: 10,
        });
        assert_eq!(total_of(&[]), Value::Null);
        assert_eq!(
            total_of(&[
                priced(1250, "GBP", Ask::Quantity(2)),
                priced(500, "GBP", slot)
            ]),
            json!({"amount": 3000, "currency": "GBP"})
        );
        assert_eq!(
            total_of(&[
                priced(1250, "GBP", Ask::Quantity(2)),
                priced(500, "EUR", slot)
            ]),
            Value::Null
        );
        assert_eq!(
            total_of(&[
                priced(1250, "GBP", Ask::Quantity(2)),
                item("lines", 1, ItemState::Held, Ask::Quantity(1))
            ]),
            Value::Null
        );
        assert_eq!(
            total_of(&[priced(i64::MAX, "GBP", Ask::Quantity(2))]),
            Value::Null
        );
    }

    #[test]
    fn a_plan_reads_each_element_and_a_link() {
        let config = HoldsConfig {
            holds: vec![
                (
                    "lines".into(),
                    HoldSpec {
                        source: HoldSource::Each {
                            field: "lines".into(),
                            target: "inventoryId".into(),
                            schema: "Inventory".into(),
                        },
                        ask: HoldAsk::Quantity(Amount::Field("quantity".into())),
                    },
                ),
                (
                    "pickup".into(),
                    HoldSpec {
                        source: HoldSource::Link {
                            link: "counter".into(),
                            schema: "Counter".into(),
                        },
                        ask: HoldAsk::Interval {
                            start: "pickupAt".into(),
                            end: IntervalEnd::DurationMs(900_000),
                        },
                    },
                ),
            ],
            take_on: Some(vec!["held".into()]),
            commit_on: vec!["placed".into()],
            release_on: vec!["expired".into()],
            return_on: Vec::new(),
            max_items: 100,
            initial: Some("cart".into()),
        };
        let data = json!({
            "lines": [
                {"inventoryId": "anvil", "quantity": 2},
                {"inventoryId": "", "quantity": 1},
                {"inventoryId": "vice", "quantity": 0},
                {"inventoryId": "rope", "quantity": 3}
            ],
            "pickupAt": "2026-10-21T10:00:00Z"
        });
        let targets = json!({"counter": {"schema": "Counter", "id": "front"}});
        let planned = plan(
            &config,
            data.as_object().unwrap(),
            targets.as_object().unwrap(),
        );
        let read: Vec<Value> = planned.iter().map(HolderItem::to_json).collect();
        let start = parse_date_time("2026-10-21T10:00:00Z").unwrap();
        assert_eq!(
            read,
            [
                json!({"hold": "lines", "item": 0, "schema": "Inventory", "id": "anvil", "quantity": 2, "state": "pending"}),
                json!({"hold": "lines", "item": 3, "schema": "Inventory", "id": "rope", "quantity": 3, "state": "pending"}),
                json!({"hold": "pickup", "item": 0, "schema": "Counter", "id": "front", "startMs": start, "endMs": start + 900_000, "state": "pending"}),
            ]
        );
        // A create's Links parameters name the target by id.
        let created = plan(
            &config,
            data.as_object().unwrap(),
            json!({"counter": "back"}).as_object().unwrap(),
        );
        assert_eq!(created[2].supply.id, "back");
        assert_eq!(config.reads(), ["lines".to_owned(), "pickupAt".to_owned()]);
    }

    #[test]
    fn a_merge_keeps_what_moved_past_pending() {
        use ItemState::*;
        let stored = [
            item("lines", 0, Held, Ask::Quantity(2)),
            item("lines", 1, Pending, Ask::Quantity(1)),
            item("lines", 2, Pending, Ask::Quantity(1)),
        ];
        let planned = [
            item("lines", 0, Pending, Ask::Quantity(5)),
            item("lines", 1, Pending, Ask::Quantity(4)),
        ];
        let merged = merge(&stored, &planned);
        assert_eq!(
            merged
                .iter()
                .map(|item| (item.item, item.state, item.ask))
                .collect::<Vec<_>>(),
            [(0, Held, Ask::Quantity(2)), (1, Pending, Ask::Quantity(4))]
        );
    }
}
