//! The supply contract (D70): how a claim holds supply. A supply behavior
//! (`Stock`, `Calendar`, or an extension's) serves the instance operations
//! `holdFor`, `commitFor`, `releaseFor` and, for counted supply,
//! `returnFor`, which write, and `checkHold`, which only reads. `Holds`
//! invokes them by name on the instance an item names, so it holds an
//! extension's supply as it holds the core's, and it never reads a
//! supply's tables.
//!
//! Every operation takes the same parameters ([`SupplyCall`]): the holder,
//! `{schema, id}`; the hold's name in the holder's `Holds` config; the
//! item's index in that hold (its element's index in an `each` hold's
//! list, 0 for a `link` hold); and what is asked, a `quantity` of counted
//! supply or an `interval`, `{startMs, endMs}`, of timed supply.
//!
//! The holder check. A supply takes a writing call only from the claim
//! itself, as Budget's scope operations take one only from the instance
//! inside the scope: it reads the holder's own `Holds.items`
//! ([`check_holder`]) and refuses the call (`not_requested`) unless an item
//! of it, by hold and index, names this supply, asks what the call asks,
//! and is already in the state the call moves the supply's record to
//! ([`SupplyOperation::state`]). `Holds` writes an item's new state before
//! it calls the supply, in the same transaction, so a caller that invokes
//! `releaseFor` over HTTP for an item its claim still holds is refused, as
//! is one that holds for an item its claim has not taken.
//!
//! The record. A supply keeps a row per holder, hold and item
//! ([`rows_table`]), and a call moves it only forward
//! ([`SupplyOperation::step`]): a call whose row is already in the state
//! it asks for changes and writes nothing, and one whose row is in no
//! state the call moves from is `not_requested`. [`begin`] runs the holder
//! check and the step; the supply then checks what is free, for `holdFor`,
//! and [`finish`] records the row and answers.
//!
//! What does not fit is `unavailable` ([`Unavailable`]): its details name
//! the supply, the hold and item, what was asked, and what is free, a
//! count `available` or the `conflict`ing interval. A supply's own codes
//! (`outside_hours`, `too_soon`) carry the same members. `checkHold`
//! answers with the refusal rather than vetoing ([`CheckAnswer`]), so a
//! claim's guard can list every item that does not fit
//! ([`claim_details`]).
//!
//! A supply refuses its own delete while it holds anything (`held`,
//! [`holding`]).

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    ColumnSpec, IndexSpec, InstanceCx, InstanceView, TableSpec,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::storage::{Row, Sql, SqlValue};
use superschematic_engine::{EngineError, Error, OperationCx, Result};

/// The behavior whose field a supply reads of a holder.
pub const HOLDS: &str = "Holds";
/// The field the holder check reads, by qualified name.
pub const HOLDS_ITEMS: &str = "Holds.items";
/// The field the holder check reads, by its name in `Holds`.
pub const ITEMS: &str = "items";

/// A call the holder's `Holds.items` does not ask for, or the supply's
/// record cannot move by.
pub const NOT_REQUESTED: &str = "not_requested";
/// What was asked does not fit what the supply has free.
pub const UNAVAILABLE: &str = "unavailable";
/// A supply's delete while it holds something for a claim.
pub const HELD: &str = "held";

/// The largest integer a JavaScript number holds exactly.
const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

/// What a supply holds: units it counts, or time it opens.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SupplyKind {
    /// Units (`Stock`): calls ask a `quantity`, and committed units can be
    /// returned.
    Counted,
    /// Time on a resource (`Calendar`): calls ask an `interval`.
    Timed,
}

impl SupplyKind {
    /// The contract's operations a supply of the kind serves, in the order
    /// its declaration lists them.
    pub fn operations(self) -> &'static [SupplyOperation] {
        match self {
            SupplyKind::Counted => &[
                SupplyOperation::Hold,
                SupplyOperation::Commit,
                SupplyOperation::Release,
                SupplyOperation::Return,
                SupplyOperation::Check,
            ],
            SupplyKind::Timed => &[
                SupplyOperation::Hold,
                SupplyOperation::Commit,
                SupplyOperation::Release,
                SupplyOperation::Check,
            ],
        }
    }
}

/// An item's state, in the holder's `Holds.items` and in a supply's
/// record. States only advance: `pending → held → committed → returned`,
/// or `pending | held → released`. A supply records no `pending` item.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ItemState {
    Pending,
    Held,
    Committed,
    Released,
    Returned,
}

impl ItemState {
    /// Every state, in the order an item can reach them.
    pub const ALL: [ItemState; 5] = [
        ItemState::Pending,
        ItemState::Held,
        ItemState::Committed,
        ItemState::Released,
        ItemState::Returned,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            ItemState::Pending => "pending",
            ItemState::Held => "held",
            ItemState::Committed => "committed",
            ItemState::Released => "released",
            ItemState::Returned => "returned",
        }
    }

    pub fn parse(text: &str) -> Option<ItemState> {
        ItemState::ALL
            .into_iter()
            .find(|state| state.as_str() == text)
    }

    /// Whether the supply counts the item as taken: held, or committed and
    /// not returned.
    pub fn takes(self) -> bool {
        matches!(self, ItemState::Held | ItemState::Committed)
    }

    /// Whether an item in this state may move to `to`.
    pub fn advances_to(self, to: ItemState) -> bool {
        matches!(
            (self, to),
            (ItemState::Pending, ItemState::Held | ItemState::Released)
                | (ItemState::Held, ItemState::Committed | ItemState::Released)
                | (ItemState::Committed, ItemState::Returned)
        )
    }
}

/// One of the contract's operations.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SupplyOperation {
    /// `holdFor`: takes what an item asks.
    Hold,
    /// `commitFor`: what was held is the claim's for good (sold, booked).
    Commit,
    /// `releaseFor`: what was held is free again.
    Release,
    /// `returnFor`: counted units a claim committed come back.
    Return,
    /// `checkHold`: whether `holdFor` would fit now; reads only.
    Check,
}

/// What [`SupplyOperation::step`] makes of a supply's record.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Step {
    /// The call moves the record.
    Apply,
    /// The record is where the call would move it, or the call has nothing
    /// to move (a release of an item never held): it changes nothing.
    Done,
}

impl SupplyOperation {
    /// The operation's name, as the declaration and `Holds` name it.
    pub fn name(self) -> &'static str {
        match self {
            SupplyOperation::Hold => "holdFor",
            SupplyOperation::Commit => "commitFor",
            SupplyOperation::Release => "releaseFor",
            SupplyOperation::Return => "returnFor",
            SupplyOperation::Check => "checkHold",
        }
    }

    /// The operation a name names.
    pub fn parse(name: &str) -> Option<SupplyOperation> {
        [
            SupplyOperation::Hold,
            SupplyOperation::Commit,
            SupplyOperation::Release,
            SupplyOperation::Return,
            SupplyOperation::Check,
        ]
        .into_iter()
        .find(|operation| operation.name() == name)
    }

    /// Whether the operation writes.
    pub fn writes(self) -> bool {
        self != SupplyOperation::Check
    }

    /// The state the call moves the supply's record to, which the holder's
    /// item must already show; `None` for `checkHold`.
    pub fn state(self) -> Option<ItemState> {
        match self {
            SupplyOperation::Hold => Some(ItemState::Held),
            SupplyOperation::Commit => Some(ItemState::Committed),
            SupplyOperation::Release => Some(ItemState::Released),
            SupplyOperation::Return => Some(ItemState::Returned),
            SupplyOperation::Check => None,
        }
    }

    /// What the call does to a supply's record in `recorded` (`None` for
    /// no row): moves it, leaves it, or, when the record is in no state the
    /// call moves from, `None`, which is `not_requested`.
    pub fn step(self, recorded: Option<ItemState>) -> Option<Step> {
        let to = self.state()?;
        if recorded == Some(to) {
            return Some(Step::Done);
        }
        match (self, recorded) {
            (SupplyOperation::Hold, None) => Some(Step::Apply),
            // Nothing was held for an item a claim lets go before it holds.
            (SupplyOperation::Release, None) => Some(Step::Done),
            (_, Some(from)) if from.advances_to(to) => Some(Step::Apply),
            _ => None,
        }
    }
}

/// An instance a call names: a holder, or a supply.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct InstanceRef {
    pub schema: String,
    pub id: String,
}

impl InstanceRef {
    pub fn new(schema: impl Into<String>, id: impl Into<String>) -> Self {
        InstanceRef {
            schema: schema.into(),
            id: id.into(),
        }
    }

    /// `{schema, id}`.
    pub fn to_json(&self) -> Value {
        json!({ "schema": self.schema, "id": self.id })
    }

    /// The reference in `{schema, id}`; `None` when either is not a
    /// non-empty string.
    pub fn from_json(value: &Value) -> Option<InstanceRef> {
        let text = |name: &str| {
            value
                .get(name)
                .and_then(Value::as_str)
                .filter(|text| !text.is_empty())
                .map(str::to_owned)
        };
        Some(InstanceRef {
            schema: text("schema")?,
            id: text("id")?,
        })
    }

    /// The instance a view reads.
    pub fn of<C>(view: &InstanceView<'_, C>) -> InstanceRef {
        InstanceRef::new(view.schema(), view.id())
    }
}

/// Time from `start_ms`, inclusive, to `end_ms`, exclusive, in epoch
/// milliseconds.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Interval {
    pub start_ms: i64,
    pub end_ms: i64,
}

impl Interval {
    /// Whether two intervals share an instant: one that ends as the other
    /// starts does not.
    pub fn overlaps(&self, other: &Interval) -> bool {
        self.start_ms < other.end_ms && other.start_ms < self.end_ms
    }

    /// `{startMs, endMs}`.
    pub fn to_json(&self) -> Value {
        json!({ "startMs": self.start_ms, "endMs": self.end_ms })
    }
}

/// What a call asks of a supply.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Ask {
    Quantity(i64),
    Interval(Interval),
}

impl Ask {
    pub fn kind(&self) -> SupplyKind {
        match self {
            Ask::Quantity(_) => SupplyKind::Counted,
            Ask::Interval(_) => SupplyKind::Timed,
        }
    }

    pub fn quantity(&self) -> Option<i64> {
        match self {
            Ask::Quantity(quantity) => Some(*quantity),
            Ask::Interval(_) => None,
        }
    }

    pub fn interval(&self) -> Option<Interval> {
        match self {
            Ask::Interval(interval) => Some(*interval),
            Ask::Quantity(_) => None,
        }
    }

    /// `{quantity}` or `{interval: {startMs, endMs}}`, as a call's
    /// parameters give it and `unavailable`'s `asked` repeats it.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        self.put(&mut out);
        Value::Object(out)
    }

    fn put(&self, out: &mut Map<String, Value>) {
        match self {
            Ask::Quantity(quantity) => {
                out.insert("quantity".into(), Value::from(*quantity));
            }
            Ask::Interval(interval) => {
                out.insert("interval".into(), interval.to_json());
            }
        }
    }
}

/// A contract call's parameters.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SupplyCall {
    pub holder: InstanceRef,
    /// The hold's name in the holder's `Holds` config.
    pub hold: String,
    /// The item's index in its hold.
    pub item: i64,
    pub ask: Ask,
}

impl SupplyCall {
    /// The call's parameters, as `Holds` sends them.
    pub fn to_params(&self) -> Value {
        let mut out = Map::new();
        out.insert("holder".into(), self.holder.to_json());
        out.insert("hold".into(), Value::from(self.hold.as_str()));
        out.insert("item".into(), Value::from(self.item));
        self.ask.put(&mut out);
        Value::Object(out)
    }

    /// Reads a call's parameters, which the operation's paramsSchema
    /// ([`params_schema`]) accepted, for a supply of `kind`: an interval
    /// must end after it starts. `behavior` names the supply in a refusal.
    pub fn parse(
        behavior: &str,
        operation: SupplyOperation,
        kind: SupplyKind,
        params: &Value,
    ) -> Result<SupplyCall> {
        let refuse = |path: &str, message: &str| -> Error {
            EngineError::operation_params(
                behavior,
                operation.name(),
                vec![SchemaIssue::new(path, message)],
            )
            .into()
        };
        let holder = params
            .get("holder")
            .and_then(InstanceRef::from_json)
            .ok_or_else(|| refuse("/holder", "must be {schema, id}, each a non-empty string"))?;
        let hold = params
            .get("hold")
            .and_then(Value::as_str)
            .filter(|hold| !hold.is_empty())
            .ok_or_else(|| refuse("/hold", "must be a non-empty string"))?
            .to_owned();
        let item = params
            .get("item")
            .and_then(integer)
            .filter(|item| *item >= 0)
            .ok_or_else(|| refuse("/item", "must be an integer of at least 0"))?;
        let ask = match kind {
            SupplyKind::Counted => Ask::Quantity(
                params
                    .get("quantity")
                    .and_then(integer)
                    .filter(|quantity| *quantity >= 1)
                    .ok_or_else(|| refuse("/quantity", "must be an integer of at least 1"))?,
            ),
            SupplyKind::Timed => {
                let interval = params.get("interval");
                let at = |name: &str| {
                    interval
                        .and_then(|interval| interval.get(name))
                        .and_then(integer)
                };
                let (Some(start_ms), Some(end_ms)) = (at("startMs"), at("endMs")) else {
                    return Err(refuse(
                        "/interval",
                        "must be {startMs, endMs}, each an integer",
                    ));
                };
                if end_ms <= start_ms {
                    return Err(refuse("/interval/endMs", "must be after startMs"));
                }
                Ask::Interval(Interval { start_ms, end_ms })
            }
        };
        Ok(SupplyCall {
            holder,
            hold,
            item,
            ask,
        })
    }
}

/// A supply's unit price, which `holdFor` returns and `Holds` keeps on the
/// item, so a line keeps the price it was held at.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Price {
    /// In the currency's minor units.
    pub unit_price: i64,
    pub currency: Option<String>,
}

/// One element of a holder's `Holds.items`:
/// `{ hold, item, schema, id, quantity?, startMs?, endMs?, state,
/// unitPrice?, currency? }`, where `schema` and `id` name the supply.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HolderItem {
    pub hold: String,
    pub item: i64,
    pub supply: InstanceRef,
    pub ask: Ask,
    pub state: ItemState,
    pub price: Option<Price>,
}

impl HolderItem {
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("hold".into(), Value::from(self.hold.as_str()));
        out.insert("item".into(), Value::from(self.item));
        out.insert("schema".into(), Value::from(self.supply.schema.as_str()));
        out.insert("id".into(), Value::from(self.supply.id.as_str()));
        match self.ask {
            Ask::Quantity(quantity) => {
                out.insert("quantity".into(), Value::from(quantity));
            }
            Ask::Interval(interval) => {
                out.insert("startMs".into(), Value::from(interval.start_ms));
                out.insert("endMs".into(), Value::from(interval.end_ms));
            }
        }
        out.insert("state".into(), Value::from(self.state.as_str()));
        if let Some(price) = &self.price {
            out.insert("unitPrice".into(), Value::from(price.unit_price));
            if let Some(currency) = &price.currency {
                out.insert("currency".into(), Value::from(currency.as_str()));
            }
        }
        Value::Object(out)
    }

    /// An element of `Holds.items`; `None` for one that is not of this
    /// shape.
    pub fn from_json(value: &Value) -> Option<HolderItem> {
        let text = |name: &str| value.get(name).and_then(Value::as_str);
        let number = |name: &str| value.get(name).and_then(integer);
        let ask = match (number("quantity"), number("startMs"), number("endMs")) {
            (Some(quantity), None, None) => Ask::Quantity(quantity),
            (None, Some(start_ms), Some(end_ms)) => Ask::Interval(Interval { start_ms, end_ms }),
            _ => return None,
        };
        let price = number("unitPrice").map(|unit_price| Price {
            unit_price,
            currency: text("currency").map(str::to_owned),
        });
        Some(HolderItem {
            hold: text("hold")?.to_owned(),
            item: number("item")?,
            supply: InstanceRef::from_json(value)?,
            ask,
            state: ItemState::parse(text("state")?)?,
            price,
        })
    }

    /// Whether the element is the one a call names, by hold and index.
    pub fn is(&self, call: &SupplyCall) -> bool {
        self.hold == call.hold && self.item == call.item
    }
}

/// The items of a holder's `Holds.items`, in its order; an element not of
/// [`HolderItem`]'s shape is left out.
pub fn holder_items(items: Option<&Value>) -> Vec<HolderItem> {
    items
        .and_then(Value::as_array)
        .map(|items| items.iter().filter_map(HolderItem::from_json).collect())
        .unwrap_or_default()
}

/// The holder check: reads the holder of a writing call through its own
/// `Holds.items`, as the call's principal reaches it, and returns the item
/// the call names. Refuses, `not_requested`, a holder that is gone or does
/// not compose `Holds`, and an item it does not have, that names another
/// supply, that asks other than the call, or that is not yet in the state
/// the call moves the supply's record to.
pub fn check_holder<C>(
    view: &InstanceView<'_, C>,
    operation: SupplyOperation,
    call: &SupplyCall,
) -> Result<HolderItem> {
    let Some(needs) = operation.state() else {
        return Err(Error::behavior(
            view.behavior(),
            "checkHold reads only, and takes no holder check",
        ));
    };
    let refuse = |reason: String, found: Option<ItemState>| {
        not_requested(view, operation, call, reason, found, None)
    };
    let holder = &call.holder;
    if view.schemas().config(&holder.schema, HOLDS)?.is_none() {
        return Err(refuse(
            format!(
                "{} does not compose {HOLDS}, so holds nothing",
                holder.schema
            ),
            None,
        ));
    }
    let Some(record) = view
        .instances()
        .get(&holder.schema, &holder.id, Some(&[HOLDS_ITEMS]))?
    else {
        return Err(refuse(
            format!("{} {} does not exist", holder.schema, holder.id),
            None,
        ));
    };
    let items = holder_items(record.behavior_field(HOLDS, ITEMS));
    let Some(item) = items.into_iter().find(|item| item.is(call)) else {
        return Err(refuse(
            format!(
                "{} {} has no item {} of hold {}",
                holder.schema, holder.id, call.item, call.hold
            ),
            None,
        ));
    };
    if item.supply.schema != view.schema() || item.supply.id != view.id() {
        return Err(refuse(
            format!(
                "item {} of hold {} of {} {} names {} {}, not this supply",
                call.item, call.hold, holder.schema, holder.id, item.supply.schema, item.supply.id
            ),
            Some(item.state),
        ));
    }
    if item.ask != call.ask {
        return Err(refuse(
            format!(
                "item {} of hold {} of {} {} asks {}, not {}",
                call.item,
                call.hold,
                holder.schema,
                holder.id,
                superschematic_engine::json::stringify(&item.ask.to_json()),
                superschematic_engine::json::stringify(&call.ask.to_json())
            ),
            Some(item.state),
        ));
    }
    if item.state != needs {
        return Err(refuse(
            format!(
                "item {} of hold {} of {} {} is {}, and {} needs it {}",
                call.item,
                call.hold,
                holder.schema,
                holder.id,
                item.state.as_str(),
                operation.name(),
                needs.as_str()
            ),
            Some(item.state),
        ));
    }
    Ok(item)
}

/// The `not_requested` refusal of a call. Its details name the holder, the
/// hold and the item, the state the call needs the holder's item in
/// (`needs`), and, when known, the state the holder shows (`holderState`)
/// and the one the supply records (`supplyState`).
pub fn not_requested<C>(
    view: &InstanceView<'_, C>,
    operation: SupplyOperation,
    call: &SupplyCall,
    reason: String,
    holder_state: Option<ItemState>,
    supply_state: Option<ItemState>,
) -> Error {
    let mut details = Map::new();
    details.insert("holder".into(), call.holder.to_json());
    details.insert("hold".into(), Value::from(call.hold.as_str()));
    details.insert("item".into(), Value::from(call.item));
    if let Some(needs) = operation.state() {
        details.insert("needs".into(), Value::from(needs.as_str()));
    }
    if let Some(state) = holder_state {
        details.insert("holderState".into(), Value::from(state.as_str()));
    }
    if let Some(state) = supply_state {
        details.insert("supplyState".into(), Value::from(state.as_str()));
    }
    Refusal {
        code: NOT_REQUESTED.to_owned(),
        reason,
        details,
    }
    .veto(view, operation)
}

/// A supply's refusal of what a call asks: a code its declaration lists,
/// why, and details.
#[derive(Clone, Debug, PartialEq)]
pub struct Refusal {
    pub code: String,
    pub reason: String,
    pub details: Map<String, Value>,
}

impl Refusal {
    /// The refusal as a veto of the supply's operation.
    pub fn veto<C>(self, view: &InstanceView<'_, C>, operation: SupplyOperation) -> Error {
        Error::veto(
            view.behavior(),
            operation.name(),
            view.schema(),
            view.id(),
            Veto {
                reason: self.reason,
                code: Some(self.code),
                details: Some(self.details),
            },
        )
    }

    /// The refusal as one entry of a claim's details: its details with
    /// its `code` and `reason`.
    pub fn entry(&self) -> Value {
        let mut out = Map::new();
        out.insert("code".into(), Value::from(self.code.as_str()));
        out.insert("reason".into(), Value::from(self.reason.as_str()));
        for (key, value) in &self.details {
            out.insert(key.clone(), value.clone());
        }
        Value::Object(out)
    }
}

/// What is free of a supply that an `unavailable` call did not fit.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Free {
    /// Counted units free now.
    Available(i64),
    /// A held interval the asked one overlaps.
    Conflict(Interval),
}

/// The details of `unavailable`: `{ supply: {schema, id}, hold, item,
/// asked: {quantity} | {interval}, available | conflict }`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Unavailable {
    pub supply: InstanceRef,
    pub hold: String,
    pub item: i64,
    pub asked: Ask,
    pub free: Free,
}

impl Unavailable {
    /// The details of what a call to the supply a view reads asked.
    pub fn of<C>(view: &InstanceView<'_, C>, call: &SupplyCall, free: Free) -> Self {
        Unavailable {
            supply: InstanceRef::of(view),
            hold: call.hold.clone(),
            item: call.item,
            asked: call.ask,
            free,
        }
    }

    pub fn details(&self) -> Map<String, Value> {
        let mut out = Map::new();
        out.insert("supply".into(), self.supply.to_json());
        out.insert("hold".into(), Value::from(self.hold.as_str()));
        out.insert("item".into(), Value::from(self.item));
        out.insert("asked".into(), self.asked.to_json());
        match self.free {
            Free::Available(available) => {
                out.insert("available".into(), Value::from(available));
            }
            Free::Conflict(interval) => {
                out.insert("conflict".into(), interval.to_json());
            }
        }
        out
    }

    /// The `unavailable` refusal, with why.
    pub fn refusal(&self, reason: String) -> Refusal {
        Refusal {
            code: UNAVAILABLE.to_owned(),
            reason,
            details: self.details(),
        }
    }
}

/// `checkHold`'s answer: `{fits: true}`, or `{fits: false, code, reason,
/// details}` with the refusal `holdFor` would veto with.
#[derive(Clone, Debug, PartialEq)]
pub enum CheckAnswer {
    Fits,
    Refused(Refusal),
}

impl CheckAnswer {
    pub fn to_json(&self) -> Value {
        match self {
            CheckAnswer::Fits => json!({ "fits": true }),
            CheckAnswer::Refused(refusal) => json!({
                "fits": false,
                "code": refusal.code,
                "reason": refusal.reason,
                "details": refusal.details,
            }),
        }
    }

    /// The answer a supply's `checkHold` returned; `None` for one not of
    /// this shape.
    pub fn from_json(value: &Value) -> Option<CheckAnswer> {
        match value.get("fits")?.as_bool()? {
            true => Some(CheckAnswer::Fits),
            false => Some(CheckAnswer::Refused(Refusal {
                code: value.get("code")?.as_str()?.to_owned(),
                reason: value.get("reason")?.as_str()?.to_owned(),
                details: value
                    .get("details")
                    .and_then(Value::as_object)
                    .cloned()
                    .unwrap_or_default(),
            })),
        }
    }
}

/// A claim's details of `unavailable`: `{ items: [...] }`, one entry for
/// each item that does not fit, each the supply's details with its `code`
/// and `reason`.
pub fn claim_details(refusals: &[Refusal]) -> Map<String, Value> {
    let mut out = Map::new();
    out.insert(
        "items".into(),
        Value::Array(refusals.iter().map(Refusal::entry).collect()),
    );
    out
}

/// A supply's record of one holder's item.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SupplyRecord {
    pub holder: InstanceRef,
    pub hold: String,
    pub item: i64,
    pub ask: Ask,
    pub state: ItemState,
    pub price: Option<Price>,
    /// When the record last moved, by the engine's clock.
    pub at: i64,
}

impl SupplyRecord {
    /// `{ holder, hold, item, quantity | interval, state, unitPrice?,
    /// currency?, at }`.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("holder".into(), self.holder.to_json());
        out.insert("hold".into(), Value::from(self.hold.as_str()));
        out.insert("item".into(), Value::from(self.item));
        self.ask.put(&mut out);
        out.insert("state".into(), Value::from(self.state.as_str()));
        if let Some(price) = &self.price {
            out.insert("unitPrice".into(), Value::from(price.unit_price));
            if let Some(currency) = &price.currency {
                out.insert("currency".into(), Value::from(currency.as_str()));
            }
        }
        out.insert("at".into(), Value::from(self.at));
        Value::Object(out)
    }
}

const ROW_COLUMNS: &str = "holder_schema, holder_id, hold, item, quantity, start_ms, end_ms, state, unit_price, currency, at";

/// A supply's table of records, keyed by its instance, the holder, the
/// hold and the item, with an index for the overlap read of timed supply
/// (instance, then `start_ms`). A supply names it in a migration of its
/// own, and may add columns.
pub fn rows_table(name: &str) -> TableSpec {
    TableSpec::new(name)
        .column("namespace", ColumnSpec::text().not_null())
        .column("schema", ColumnSpec::text().not_null())
        .column("id", ColumnSpec::text().not_null())
        .column("holder_schema", ColumnSpec::text().not_null())
        .column("holder_id", ColumnSpec::text().not_null())
        .column("hold", ColumnSpec::text().not_null())
        .column("item", ColumnSpec::integer().not_null())
        .column("quantity", ColumnSpec::integer())
        .column("start_ms", ColumnSpec::integer())
        .column("end_ms", ColumnSpec::integer())
        .column("state", ColumnSpec::text().not_null())
        .column("unit_price", ColumnSpec::integer())
        .column("currency", ColumnSpec::text())
        .column("at", ColumnSpec::integer().not_null())
        .primary_key(&[
            "namespace",
            "schema",
            "id",
            "holder_schema",
            "holder_id",
            "hold",
            "item",
        ])
        .index(IndexSpec::new(
            format!("{name}_start"),
            &["namespace", "schema", "id", "start_ms"],
        ))
}

fn instance_key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

fn call_key<C>(view: &InstanceView<'_, C>, call: &SupplyCall) -> Vec<SqlValue> {
    let mut key = instance_key(view);
    key.extend([
        SqlValue::from(call.holder.schema.as_str()),
        SqlValue::from(call.holder.id.as_str()),
        SqlValue::from(call.hold.as_str()),
        SqlValue::from(call.item),
    ]);
    key
}

fn record_of(row: &Row) -> Result<SupplyRecord> {
    let ask = match (
        row.opt_i64("quantity")?,
        row.opt_i64("start_ms")?,
        row.opt_i64("end_ms")?,
    ) {
        (Some(quantity), _, _) => Ask::Quantity(quantity),
        (None, Some(start_ms), Some(end_ms)) => Ask::Interval(Interval { start_ms, end_ms }),
        _ => {
            return Err(Error::behavior(
                "supply",
                "a supply's record holds neither a quantity nor an interval",
            ))
        }
    };
    let state = row.text("state")?;
    let state = ItemState::parse(state).ok_or_else(|| {
        Error::behavior(
            "supply",
            format!("a supply's record holds the state {state}"),
        )
    })?;
    let currency = row.opt_text("currency")?.map(str::to_owned);
    Ok(SupplyRecord {
        holder: InstanceRef::new(row.text("holder_schema")?, row.text("holder_id")?),
        hold: row.text("hold")?.to_owned(),
        item: row.i64("item")?,
        ask,
        state,
        price: row.opt_i64("unit_price")?.map(|unit_price| Price {
            unit_price,
            currency,
        }),
        at: row.i64("at")?,
    })
}

/// The supply's record of the item a call names, in its table `table`;
/// `None` when it has none.
pub fn recorded<C>(
    view: &InstanceView<'_, C>,
    table: &str,
    call: &SupplyCall,
) -> Result<Option<SupplyRecord>> {
    let table = view.sql().table(table)?;
    let row = view.sql().get(
        format!(
            "SELECT {ROW_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND holder_schema = ? AND holder_id = ? AND hold = ? AND item = ?"
        ),
        &call_key(view, call),
    )?;
    row.as_ref().map(record_of).transpose()
}

/// Every record of the instance in its table `table`, by holder, hold and
/// item, text in byte order on both databases.
pub fn records<C>(view: &InstanceView<'_, C>, table: &str) -> Result<Vec<SupplyRecord>> {
    let table = view.sql().table(table)?;
    let select =
        format!("SELECT {ROW_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ?");
    let rows = view.sql().all(
        Sql::per(
            format!("{select} ORDER BY holder_schema, holder_id, hold, item"),
            format!("{select} ORDER BY holder_schema COLLATE \"C\", holder_id COLLATE \"C\", hold COLLATE \"C\", item"),
        ),
        &instance_key(view),
    )?;
    rows.iter().map(record_of).collect()
}

/// How many of the instance's records in its table `table` take supply:
/// held, or committed and not returned. A supply refuses its own delete
/// while it is above 0 (`held`).
pub fn holding<C>(view: &InstanceView<'_, C>, table: &str) -> Result<i64> {
    let table = view.sql().table(table)?;
    let row = view.sql().get(
        format!(
            "SELECT COUNT(*) AS n FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND state IN ('held', 'committed')"
        ),
        &instance_key(view),
    )?;
    Ok(match row {
        Some(row) => row.i64("n")?,
        None => 0,
    })
}

/// Removes every record of the instance in its table `table`, as a
/// supply's afterChange does after its delete.
pub fn forget<C>(cx: &InstanceCx<'_, C>, table: &str) -> Result<()> {
    let table = cx.sql().table(table)?;
    cx.sql().run(
        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
        &instance_key(cx),
    )?;
    Ok(())
}

/// What [`begin`] makes of a writing call.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Begun {
    /// The call moves the record: the holder's item, and the record as it
    /// is (`None` for no row).
    Apply {
        item: HolderItem,
        recorded: Option<SupplyRecord>,
    },
    /// The call changes nothing: the record, if any.
    Done(Option<SupplyRecord>),
}

/// Starts a writing call of the contract: the holder check
/// ([`check_holder`]), then the step the supply's record in its table
/// `table` takes ([`SupplyOperation::step`]), refusing a record the call
/// does not move from with `not_requested`.
pub fn begin<C>(
    cx: &InstanceCx<'_, C>,
    table: &str,
    operation: SupplyOperation,
    call: &SupplyCall,
) -> Result<Begun> {
    let item = check_holder(cx, operation, call)?;
    let record = recorded(cx, table, call)?;
    match operation.step(record.as_ref().map(|record| record.state)) {
        Some(Step::Apply) => Ok(Begun::Apply {
            item,
            recorded: record,
        }),
        Some(Step::Done) => Ok(Begun::Done(record)),
        None => {
            let state = record.as_ref().map(|record| record.state);
            Err(not_requested(
                cx,
                operation,
                call,
                format!(
                    "this supply records item {} of hold {} of {} {} as {}, which {} does not move",
                    call.item,
                    call.hold,
                    call.holder.schema,
                    call.holder.id,
                    state.map_or("not held", ItemState::as_str),
                    operation.name()
                ),
                Some(item.state),
                state,
            ))
        }
    }
}

/// Records the move a call makes, in the supply's table `table`, and
/// answers it ([`outcome`]). `holdFor` records `price`; the other calls
/// keep the price the record has.
pub fn finish<C>(
    cx: &OperationCx<'_, C>,
    table: &str,
    operation: SupplyOperation,
    call: &SupplyCall,
    price: Option<Price>,
) -> Result<Value> {
    let Some(state) = operation.state() else {
        return Err(Error::behavior(cx.behavior(), "checkHold records nothing"));
    };
    let name = cx.sql().table(table)?;
    let mut values = call_key(cx, call);
    values.extend([
        SqlValue::from(call.ask.quantity()),
        SqlValue::from(call.ask.interval().map(|interval| interval.start_ms)),
        SqlValue::from(call.ask.interval().map(|interval| interval.end_ms)),
        SqlValue::from(state.as_str()),
        SqlValue::from(price.as_ref().map(|price| price.unit_price)),
        SqlValue::from(price.as_ref().and_then(|price| price.currency.clone())),
        SqlValue::from(cx.now()),
    ]);
    let keep_price = if operation == SupplyOperation::Hold {
        "unit_price = excluded.unit_price, currency = excluded.currency, "
    } else {
        ""
    };
    cx.sql().run(
        format!(
            "INSERT INTO {name} (namespace, schema, id, {ROW_COLUMNS}) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT (namespace, schema, id, holder_schema, holder_id, hold, item)
             DO UPDATE SET state = excluded.state, {keep_price}at = excluded.at"
        ),
        &values,
    )?;
    let record = recorded(cx, table, call)?;
    Ok(outcome(
        state,
        true,
        record.and_then(|record| record.price).as_ref(),
    ))
}

/// Answers a call that changed nothing, with the record as it is, and
/// tells the engine so: no event, and the instance keeps its seq.
pub fn done<C>(
    cx: &OperationCx<'_, C>,
    operation: SupplyOperation,
    record: Option<&SupplyRecord>,
) -> Value {
    cx.unchanged();
    let state = record
        .map(|record| record.state)
        .or(operation.state())
        .unwrap_or(ItemState::Released);
    outcome(
        state,
        false,
        record.and_then(|record| record.price.as_ref()),
    )
}

/// A writing call's result: `{ state, changed, unitPrice?, currency? }`,
/// the state the supply records the item in after it, whether the call
/// moved it, and the record's price.
pub fn outcome(state: ItemState, changed: bool, price: Option<&Price>) -> Value {
    let mut out = Map::new();
    out.insert("state".into(), Value::from(state.as_str()));
    out.insert("changed".into(), Value::Bool(changed));
    if let Some(price) = price {
        out.insert("unitPrice".into(), Value::from(price.unit_price));
        if let Some(currency) = &price.currency {
            out.insert("currency".into(), Value::from(currency.as_str()));
        }
    }
    Value::Object(out)
}

/// The paramsSchema every contract operation of a supply of `kind` takes.
pub fn params_schema(kind: SupplyKind) -> Value {
    let mut properties = Map::new();
    properties.insert(
        "holder".into(),
        json!({
            "description": "The claim the call is for: its schema and id.",
            "type": "object",
            "additionalProperties": false,
            "required": ["schema", "id"],
            "properties": {
                "schema": {"description": "The claim's schema.", "type": "string", "minLength": 1},
                "id": {"description": "The claim's id.", "type": "string", "minLength": 1}
            }
        }),
    );
    properties.insert(
        "hold".into(),
        json!({"description": "The hold's name in the claim's Holds config.", "type": "string", "minLength": 1}),
    );
    properties.insert(
        "item".into(),
        json!({"description": "The item's index in its hold: its element's in an each hold's list, 0 for a link hold.", "type": "integer", "minimum": 0, "maximum": MAX_SAFE_INTEGER}),
    );
    let asked = match kind {
        SupplyKind::Counted => {
            properties.insert(
                "quantity".into(),
                json!({"description": "How many units the item asks.", "type": "integer", "minimum": 1, "maximum": MAX_SAFE_INTEGER}),
            );
            "quantity"
        }
        SupplyKind::Timed => {
            properties.insert(
                "interval".into(),
                json!({
                    "description": "The time the item asks, from startMs to endMs, in epoch milliseconds.",
                    "type": "object",
                    "additionalProperties": false,
                    "required": ["startMs", "endMs"],
                    "properties": {
                        "startMs": {"description": "When it starts.", "type": "integer"},
                        "endMs": {"description": "When it ends, after startMs.", "type": "integer"}
                    }
                }),
            );
            "interval"
        }
    };
    json!({
        "type": "object",
        "additionalProperties": false,
        "required": ["holder", "hold", "item", asked],
        "properties": properties,
    })
}

/// The resultSchema of the contract's writing operations ([`outcome`]).
pub fn outcome_schema() -> Value {
    json!({
        "type": "object",
        "additionalProperties": false,
        "required": ["state", "changed"],
        "properties": {
            "state": {"description": "The item's state in this supply's record after the call.", "type": "string", "enum": ["held", "committed", "released", "returned"]},
            "changed": {"description": "Whether the call moved the record; false for a call made again.", "type": "boolean"},
            "unitPrice": {"description": "The unit price the item was held at, in the currency's minor units, when the supply names one.", "type": "integer"},
            "currency": {"description": "The unit price's currency.", "type": "string"}
        }
    })
}

/// checkHold's resultSchema ([`CheckAnswer`]).
pub fn check_schema() -> Value {
    json!({
        "type": "object",
        "additionalProperties": false,
        "required": ["fits"],
        "properties": {
            "fits": {"description": "Whether holdFor would hold the item now.", "type": "boolean"},
            "code": {"description": "When it would not: the code holdFor would veto with.", "type": "string"},
            "reason": {"description": "When it would not: why.", "type": "string"},
            "details": {"description": "When it would not: the veto's details.", "type": "object"}
        }
    })
}

/// The contract's operations as a supply of `kind` declares them, in
/// [`SupplyKind::operations`]' order.
pub fn operation_declarations(kind: SupplyKind) -> Vec<Value> {
    kind.operations()
        .iter()
        .map(|operation| {
            let (description, result) = match operation {
                SupplyOperation::Hold => ("Holds what an item of a claim's Holds asks, for that claim, once the claim's item is held. Refused when it does not fit, and for an item the claim does not hold.", outcome_schema()),
                SupplyOperation::Commit => ("Makes what an item of a claim holds the claim's for good, once the claim's item is committed.", outcome_schema()),
                SupplyOperation::Release => ("Frees what an item of a claim holds, once the claim's item is released.", outcome_schema()),
                SupplyOperation::Return => ("Takes back units an item of a claim committed, once the claim's item is returned.", outcome_schema()),
                SupplyOperation::Check => ("Answers whether holdFor would hold what an item asks now, and when not, why.", check_schema()),
            };
            let mut out = Map::new();
            out.insert("name".into(), Value::from(operation.name()));
            out.insert("description".into(), Value::from(description));
            out.insert("paramsSchema".into(), params_schema(kind));
            out.insert("resultSchema".into(), result);
            if operation.writes() {
                out.insert("writes".into(), Value::Bool(true));
            }
            Value::Object(out)
        })
        .collect()
}

/// The vetoes every supply declares.
pub fn veto_declarations() -> Vec<Value> {
    vec![
        json!({"code": NOT_REQUESTED, "description": "The claim the call names does not ask it: it has no such item for this supply, the item asks otherwise, or it is not in the state the call needs; or this supply's record of the item does not move that way."}),
        json!({"code": UNAVAILABLE, "description": "What the item asks does not fit what this supply has free (details: supply, hold, item, asked, and available or conflict)."}),
        json!({"code": HELD, "description": "A supply is not deleted while it holds anything for a claim."}),
    ]
}

/// A JSON integer within JavaScript's safe range, as a number that holds
/// one.
fn integer(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    let exact = match number.as_i64() {
        Some(exact) => exact,
        None => {
            let value = superschematic_engine::json::number_value(number);
            if !value.is_finite() || value.fract() != 0.0 {
                return None;
            }
            value as i64
        }
    };
    (exact.abs() <= MAX_SAFE_INTEGER).then_some(exact)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn call(ask: Ask) -> SupplyCall {
        SupplyCall {
            holder: InstanceRef::new("Checkout", "c-1"),
            hold: "lines".into(),
            item: 0,
            ask,
        }
    }

    #[test]
    fn states_only_advance() {
        use ItemState::*;
        let moves: Vec<(ItemState, ItemState)> = ItemState::ALL
            .into_iter()
            .flat_map(|from| ItemState::ALL.into_iter().map(move |to| (from, to)))
            .filter(|(from, to)| from.advances_to(*to))
            .collect();
        assert_eq!(
            moves,
            [
                (Pending, Held),
                (Pending, Released),
                (Held, Committed),
                (Held, Released),
                (Committed, Returned)
            ]
        );
        for state in ItemState::ALL {
            assert_eq!(ItemState::parse(state.as_str()), Some(state));
        }
        assert!(Held.takes() && Committed.takes());
        assert!(!Pending.takes() && !Released.takes() && !Returned.takes());
    }

    #[test]
    fn a_call_moves_a_record_forward_once() {
        use ItemState::*;
        use SupplyOperation::*;
        assert_eq!(Hold.step(None), Some(Step::Apply));
        assert_eq!(Hold.step(Some(Held)), Some(Step::Done));
        assert_eq!(Hold.step(Some(Released)), None);
        assert_eq!(Hold.step(Some(Committed)), None);
        assert_eq!(Commit.step(Some(Held)), Some(Step::Apply));
        assert_eq!(Commit.step(Some(Committed)), Some(Step::Done));
        assert_eq!(Commit.step(None), None);
        assert_eq!(Release.step(Some(Held)), Some(Step::Apply));
        assert_eq!(Release.step(Some(Released)), Some(Step::Done));
        assert_eq!(Release.step(None), Some(Step::Done));
        assert_eq!(Release.step(Some(Committed)), None);
        assert_eq!(Return.step(Some(Committed)), Some(Step::Apply));
        assert_eq!(Return.step(Some(Returned)), Some(Step::Done));
        assert_eq!(Return.step(Some(Held)), None);
        assert_eq!(Check.step(None), None);
        for operation in [Hold, Commit, Release, Return, Check] {
            assert_eq!(SupplyOperation::parse(operation.name()), Some(operation));
        }
    }

    #[test]
    fn params_read_as_the_schema_takes_them() {
        let counted = call(Ask::Quantity(2));
        let params = counted.to_params();
        assert_eq!(
            params,
            json!({"holder": {"schema": "Checkout", "id": "c-1"}, "hold": "lines", "item": 0, "quantity": 2})
        );
        assert_eq!(
            SupplyCall::parse("Stock", SupplyOperation::Hold, SupplyKind::Counted, &params)
                .unwrap(),
            counted
        );
        let timed = call(Ask::Interval(Interval {
            start_ms: 1000,
            end_ms: 2000,
        }));
        let params = timed.to_params();
        assert_eq!(params["interval"], json!({"startMs": 1000, "endMs": 2000}));
        assert_eq!(
            SupplyCall::parse(
                "Calendar",
                SupplyOperation::Hold,
                SupplyKind::Timed,
                &params
            )
            .unwrap(),
            timed
        );
        let backwards = json!({"holder": {"schema": "B", "id": "b"}, "hold": "slot", "item": 0, "interval": {"startMs": 2, "endMs": 2}});
        let refused = SupplyCall::parse(
            "Calendar",
            SupplyOperation::Hold,
            SupplyKind::Timed,
            &backwards,
        )
        .unwrap_err();
        assert!(refused.to_string().contains("/interval/endMs"), "{refused}");
        for kind in [SupplyKind::Counted, SupplyKind::Timed] {
            let schema = params_schema(kind);
            assert_eq!(schema["additionalProperties"], json!(false));
            assert_eq!(schema["required"].as_array().unwrap().len(), 4);
        }
    }

    #[test]
    fn a_holder_item_reads_back_as_it_is_written() {
        let item = HolderItem {
            hold: "lines".into(),
            item: 1,
            supply: InstanceRef::new("Inventory", "anvil"),
            ask: Ask::Quantity(3),
            state: ItemState::Held,
            price: Some(Price {
                unit_price: 1250,
                currency: Some("GBP".into()),
            }),
        };
        let written = item.to_json();
        assert_eq!(
            written,
            json!({"hold": "lines", "item": 1, "schema": "Inventory", "id": "anvil", "quantity": 3, "state": "held", "unitPrice": 1250, "currency": "GBP"})
        );
        assert_eq!(HolderItem::from_json(&written), Some(item));
        let slot = json!({"hold": "pickup", "item": 0, "schema": "Counter", "id": "front", "startMs": 10, "endMs": 20, "state": "pending"});
        let read = HolderItem::from_json(&slot).unwrap();
        assert_eq!(
            read.ask,
            Ask::Interval(Interval {
                start_ms: 10,
                end_ms: 20
            })
        );
        assert_eq!(read.to_json(), slot);
        assert_eq!(holder_items(Some(&json!([slot, {"hold": "x"}]))).len(), 1);
    }

    #[test]
    fn unavailable_names_what_was_asked_and_what_is_free() {
        let refusal = Unavailable {
            supply: InstanceRef::new("Inventory", "anvil"),
            hold: "lines".into(),
            item: 0,
            asked: Ask::Quantity(2),
            free: Free::Available(1),
        }
        .refusal("2 asked, 1 free".into());
        assert_eq!(
            Value::Object(refusal.details.clone()),
            json!({"supply": {"schema": "Inventory", "id": "anvil"}, "hold": "lines", "item": 0, "asked": {"quantity": 2}, "available": 1})
        );
        let answer = CheckAnswer::Refused(refusal.clone());
        assert_eq!(CheckAnswer::from_json(&answer.to_json()), Some(answer));
        assert_eq!(
            CheckAnswer::from_json(&CheckAnswer::Fits.to_json()),
            Some(CheckAnswer::Fits)
        );
        let slot = Unavailable {
            supply: InstanceRef::new("Counter", "front"),
            hold: "pickup".into(),
            item: 0,
            asked: Ask::Interval(Interval {
                start_ms: 15,
                end_ms: 25,
            }),
            free: Free::Conflict(Interval {
                start_ms: 10,
                end_ms: 20,
            }),
        };
        assert_eq!(
            Value::Object(claim_details(&[refusal, slot.refusal("taken".into())])),
            json!({"items": [
                {"code": "unavailable", "reason": "2 asked, 1 free", "supply": {"schema": "Inventory", "id": "anvil"}, "hold": "lines", "item": 0, "asked": {"quantity": 2}, "available": 1},
                {"code": "unavailable", "reason": "taken", "supply": {"schema": "Counter", "id": "front"}, "hold": "pickup", "item": 0, "asked": {"interval": {"startMs": 15, "endMs": 25}}, "conflict": {"startMs": 10, "endMs": 20}}
            ]})
        );
    }

    #[test]
    fn a_supply_declares_its_kind_of_the_contract() {
        let names = |kind| {
            operation_declarations(kind)
                .iter()
                .map(|operation| operation["name"].as_str().unwrap().to_owned())
                .collect::<Vec<_>>()
        };
        assert_eq!(
            names(SupplyKind::Counted),
            [
                "holdFor",
                "commitFor",
                "releaseFor",
                "returnFor",
                "checkHold"
            ]
        );
        assert_eq!(
            names(SupplyKind::Timed),
            ["holdFor", "commitFor", "releaseFor", "checkHold"]
        );
        let check = &operation_declarations(SupplyKind::Timed)[3];
        assert!(check.get("writes").is_none());
        let codes: Vec<Value> = veto_declarations()
            .iter()
            .map(|veto| veto["code"].clone())
            .collect();
        assert_eq!(
            codes,
            [json!("not_requested"), json!("unavailable"), json!("held")]
        );
    }
}
