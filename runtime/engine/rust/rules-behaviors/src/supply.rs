//! Supply (D70, D103), the second contract between behaviors on the core:
//! how a claim holds what a supply gives, which Stock and Calendar
//! implement and Holds asks, as Capacity joins Budget to Pool
//! ([`crate::capacity`]). A project's own supply implements it the same
//! way: its declaration names `{ "name": "Supply", "role": "implements" }`
//! among its contracts, serves the operations below, and answers the query
//! below through its pure answer hook, with the types and the holder check
//! this module gives.
//!
//! It is the first contract an operation answers (D104's "a contract an
//! operation answers"). A supply serves the row operations `holdFor`,
//! `commitFor`, `releaseFor` and, for counted supply, `returnFor`, which
//! write, and Holds calls them on the supply's row through
//! [`superschematic_rules::Cx::call_on`], each with its guards, its version
//! and its hooks, so the supply's own rules hold every call. It answers
//! the query `checkHold` through [`superschematic_rules::Behavior::answer`]
//! over the supply's row and sub-tables, which Holds' guard asks of each
//! item still to hold ([`superschematic_rules::View::ask`]), and serves the
//! same answer as a read-only operation for a caller. Holds finds the use
//! of the supply's type that implements the contract by the declarations
//! ([`superschematic_rules::View::implementers`]), never by a behavior's
//! name.
//!
//! Every call takes the same parameters ([`SupplyCall`]): the holder,
//! `{schema, id}`; the hold's name in the holder's Holds config; the item's
//! index in that hold (its element's index in an `each` hold's list, 0 for
//! a `link` hold); and what is asked, a `quantity` of counted supply or an
//! `interval`, `{startMs, endMs}`, of timed supply, from 1900 to 9000
//! ([`EARLIEST_MS`], [`LATEST_MS`]). `checkHold` also takes `also`
//! ([`ALSO`]): what the claim's other items still to hold ask of the same
//! row, which it counts as taken, so two lines of one supply are checked
//! together.
//!
//! The holder check ([`holder_check`]). A supply takes a writing call only
//! for an item the holder's Holds shows in the state the call moves it to:
//! it reads the holder as the run has it ([`superschematic_rules::View::row_of`]
//! and its Holds items with the run's writes), and refuses the call
//! (`not_requested`) unless an item of it, by hold and index, names this
//! supply, asks what the call asks, and is already in that state. Holds
//! writes an item's new state before it calls the supply, in the same run,
//! so a caller that runs `releaseFor` of a supply's row for an item its
//! claim still holds is refused, as is one that holds for an item its
//! claim has not taken. A holder that is gone, which a claim's delete
//! leaves its after-change hook to read, lets go through `releaseFor`
//! alone: what it held is released, what it committed stays committed.
//!
//! What does not fit is `unavailable` ([`Unavailable`]): its details name
//! the supply, the hold and item, what was asked, and what is free, a count
//! `available` or the `conflict`ing interval. A supply's own codes carry
//! the same members. `checkHold` answers with the refusal rather than
//! vetoing ([`CheckAnswer`]), so a claim's guard lists every item that
//! does not fit ([`claim_details`]).
//!
//! Two claims for the last unit of a supply, or two bookings of one room,
//! must not both succeed (D103's "a rule across rows"). Each takes the lock
//! [`lock_key`] names for the supply's row before the reads it decides on:
//! Holds' guard takes every supply's lock of a take in the keys' order, and
//! each supply's writing operation takes its own, so the reads a hold
//! decides on are made under it. A host maps the key to a transaction's
//! advisory lock on Postgres, and on SQLite and D1 to a check in the unit
//! that writes that each read made under it still answers as it did. Each
//! write of the supply's row is held to the version it was read at too, so
//! a host that took no lock still lets only one of them through: the other
//! runs again over the first's write and finds what is left.
//!
//! A supply refuses its own delete while it holds anything (`held`): an
//! item held, or committed time that has not ended.

use serde_json::{json, Map, Value};
use superschematic_rules::{Hook, KeyedRow, Row, SubRow, Veto, View};

/// The contract's name, as declarations give it.
pub const NAME: &str = "Supply";

/// The behavior whose items a supply reads of a holder.
pub const HOLDS: &str = "Holds";

/// The operations of the contract, by their declared names.
pub const HOLD_FOR: &str = "holdFor";
pub const COMMIT_FOR: &str = "commitFor";
pub const RELEASE_FOR: &str = "releaseFor";
pub const RETURN_FOR: &str = "returnFor";
/// The query, which a supply also serves as a read-only operation.
pub const CHECK_HOLD: &str = "checkHold";

/// A call the holder's Holds does not ask for, or the supply's record
/// cannot move by.
pub const NOT_REQUESTED: &str = "not_requested";
/// What was asked does not fit what the supply has free.
pub const UNAVAILABLE: &str = "unavailable";
/// A supply's delete while it holds something for a claim.
pub const HELD: &str = "held";

/// `checkHold`'s parameter of what the claim's other items still to hold
/// ask of the same row.
pub const ALSO: &str = "also";

/// The largest integer a JavaScript number holds exactly.
pub const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

/// The earliest instant an interval of the contract starts:
/// 1900-01-01T00:00Z.
pub const EARLIEST_MS: i64 = -2_208_988_800_000;
/// The latest instant an interval of the contract ends:
/// 9000-01-01T00:00Z, well inside what a time zone database answers.
pub const LATEST_MS: i64 = 221_845_392_000_000;

/// The most asks `checkHold`'s `also` lists.
pub const MAX_ALSO: usize = 100;

/// The lock a rule over a supply's row takes before the reads it decides
/// on (D103): one per row, whichever behavior supplies it, so Holds takes
/// it without knowing which. A UUID key names its row in its base62 wire
/// form whichever form a claim wrote it in, so two claims of one supply
/// take one lock.
pub fn lock_key(type_name: &str, key: &str) -> String {
    format!(
        "{NAME}/{type_name}/{}",
        crate::subjects::canonical_subject(key)
    )
}

/// What a supply holds: units it counts, or time it opens.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SupplyKind {
    /// Units (Stock): calls ask a `quantity`, and committed units can be
    /// returned.
    Counted,
    /// Time on a resource (Calendar): calls ask an `interval`.
    Timed,
}

/// An item's state, in the holder's Holds items and in a supply's record.
/// States only advance: `pending → held → committed → returned`, or
/// `pending | held → released`. A supply records no `pending` item.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
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

/// One of the contract's writing operations.
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
    /// The operation's name, as the declaration and Holds name it.
    pub fn name(self) -> &'static str {
        match self {
            SupplyOperation::Hold => HOLD_FOR,
            SupplyOperation::Commit => COMMIT_FOR,
            SupplyOperation::Release => RELEASE_FOR,
            SupplyOperation::Return => RETURN_FOR,
        }
    }

    /// The operation a name names.
    pub fn parse(name: &str) -> Option<SupplyOperation> {
        [
            SupplyOperation::Hold,
            SupplyOperation::Commit,
            SupplyOperation::Release,
            SupplyOperation::Return,
        ]
        .into_iter()
        .find(|operation| operation.name() == name)
    }

    /// The state the call moves the supply's record to, which the holder's
    /// item must already show.
    pub fn state(self) -> ItemState {
        match self {
            SupplyOperation::Hold => ItemState::Held,
            SupplyOperation::Commit => ItemState::Committed,
            SupplyOperation::Release => ItemState::Released,
            SupplyOperation::Return => ItemState::Returned,
        }
    }

    /// What the call does to a supply's record in `recorded` (`None` for
    /// no record): moves it, leaves it, or, when the record is in no state
    /// the call moves from, `None`, which is `not_requested`.
    pub fn step(self, recorded: Option<ItemState>) -> Option<Step> {
        let to = self.state();
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

/// A row a call names: a holder, or a supply, by its type and key.
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct RowRef {
    pub schema: String,
    pub id: String,
}

impl RowRef {
    pub fn new(schema: impl Into<String>, id: impl Into<String>) -> Self {
        RowRef {
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
    pub fn from_json(value: &Value) -> Option<RowRef> {
        let text = |name: &str| {
            value
                .get(name)
                .and_then(Value::as_str)
                .filter(|text| !text.is_empty())
                .map(str::to_owned)
        };
        Some(RowRef {
            schema: text("schema")?,
            id: text("id")?,
        })
    }

    /// The row a view reads.
    pub fn of<C>(view: &View<'_, C>) -> RowRef {
        RowRef::new(view.type_name(), view.key().unwrap_or_default())
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

    /// An interval in `{startMs, endMs}`.
    pub fn from_json(value: &Value) -> Option<Interval> {
        Some(Interval {
            start_ms: integer(value.get("startMs")?)?,
            end_ms: integer(value.get("endMs")?)?,
        })
    }
}

/// What a call asks of a supply.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Ask {
    Quantity(i64),
    Interval(Interval),
}

impl Ask {
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

    /// What a call's parameters, or one of `also`'s, ask of a supply of
    /// `kind`; `None` when they ask nothing of it.
    pub fn from_params(kind: SupplyKind, params: &Value) -> Option<Ask> {
        match kind {
            SupplyKind::Counted => params.get("quantity").and_then(integer).map(Ask::Quantity),
            SupplyKind::Timed => params
                .get("interval")
                .and_then(Interval::from_json)
                .map(Ask::Interval),
        }
    }
}

/// A contract call's parameters, which the operation's paramsSchema held
/// ([`crate::ajv`]).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SupplyCall {
    pub holder: RowRef,
    /// The hold's name in the holder's Holds config.
    pub hold: String,
    /// The item's index in its hold.
    pub item: i64,
    pub ask: Ask,
}

impl SupplyCall {
    /// The call's parameters, as Holds sends them.
    pub fn to_params(&self) -> Value {
        let mut out = Map::new();
        out.insert("holder".into(), self.holder.to_json());
        out.insert("hold".into(), Value::from(self.hold.as_str()));
        out.insert("item".into(), Value::from(self.item));
        self.ask.put(&mut out);
        Value::Object(out)
    }

    /// The parameters of a `checkHold` that also counts what the claim's
    /// other items still to hold ask of the same row ([`ALSO`]); `also`
    /// empty leaves the member out.
    pub fn to_check_params(&self, also: &[Ask]) -> Value {
        let mut params = self.to_params();
        if !also.is_empty() {
            params[ALSO] = Value::Array(also.iter().map(Ask::to_json).collect());
        }
        params
    }

    /// Reads a call's parameters, which its paramsSchema held, for a supply
    /// of `kind`; `None` for parameters of another shape, which the schema
    /// refused first.
    pub fn from_params(kind: SupplyKind, params: &Value) -> Option<SupplyCall> {
        Some(SupplyCall {
            holder: RowRef::from_json(params.get("holder")?)?,
            hold: params.get("hold")?.as_str()?.to_owned(),
            item: integer(params.get("item")?)?,
            ask: Ask::from_params(kind, params)?,
        })
    }

    /// The asks `also` lists, of a supply of `kind`.
    pub fn also(kind: SupplyKind, params: &Value) -> Vec<Ask> {
        params
            .get(ALSO)
            .and_then(Value::as_array)
            .map(|listed| {
                listed
                    .iter()
                    .filter_map(|one| Ask::from_params(kind, one))
                    .collect()
            })
            .unwrap_or_default()
    }
}

/// A supply's unit price, which `holdFor` returns and Holds keeps on the
/// item, so a line keeps the price it was held at.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Price {
    /// In the currency's minor units.
    pub unit_price: i64,
    pub currency: Option<String>,
}

/// One element of a holder's Holds items:
/// `{ hold, item, schema, id, quantity?, startMs?, endMs?, state,
/// unitPrice?, currency? }`, where `schema` and `id` name the supply.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HolderItem {
    pub hold: String,
    pub item: i64,
    pub supply: RowRef,
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

    /// Whether the element is the one a call names, by hold and index.
    pub fn is(&self, call: &SupplyCall) -> bool {
        self.hold == call.hold && self.item == call.item
    }

    /// The call of an operation for this item from its holder.
    pub fn call(&self, holder: &RowRef) -> SupplyCall {
        SupplyCall {
            holder: holder.clone(),
            hold: self.hold.clone(),
            item: self.item,
            ask: self.ask,
        }
    }
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
    pub fn veto<C>(self, view: &View<'_, C>) -> superschematic_rules::Stop {
        view.veto(
            Veto::new(self.reason)
                .with_code(self.code)
                .with_details(self.details),
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
    pub supply: RowRef,
    pub hold: String,
    pub item: i64,
    pub asked: Ask,
    pub free: Free,
}

impl Unavailable {
    /// The details of what a call to a supply asked.
    pub fn of(supply: &RowRef, call: &SupplyCall, free: Free) -> Self {
        Unavailable {
            supply: supply.clone(),
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

    /// The answer a supply's `checkHold` gave; `None` for one not of this
    /// shape.
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

/// What [`holder_check`] found: the item the call names, as the holder's
/// Holds shows it, and whether the holder is gone.
#[derive(Clone, Debug, PartialEq)]
pub struct Holder {
    pub item: HolderItem,
    pub gone: bool,
}

/// The holder check: reads the holder of a writing call as the run has it,
/// its Holds items with the run's writes, and returns the item the call
/// names. Refuses, `not_requested`, a holder whose type composes no Holds,
/// and an item it does not have, that names another supply, that asks
/// other than the call, or that is not yet in the state the call moves
/// the supply's record to. A holder that is gone is refused but for
/// `releaseFor`, which lets go of what it took: its item is what the call
/// asks, in the state released.
pub fn holder_check<C>(
    view: &View<'_, C>,
    operation: SupplyOperation,
    call: &SupplyCall,
) -> Hook<Holder> {
    let needs = operation.state();
    let refuse = |reason: String, found: Option<ItemState>| {
        not_requested(view, operation, call, reason, found, None).veto(view)
    };
    let holder = &call.holder;
    let spec = view.read_spec(&holder.schema)?;
    let found = spec.and_then(|spec| {
        spec.uses
            .iter()
            .position(|entry| entry.behavior == HOLDS)
            .map(|index| (spec, index))
    });
    let Some((spec, index)) = found else {
        return Err(refuse(
            format!(
                "{} does not compose {HOLDS}, so holds nothing",
                holder.schema
            ),
            None,
        ));
    };
    let Some(row) = view.row_of(&holder.schema, &holder.id)? else {
        // A claim's delete lets go of what it holds from its after-change
        // hook, when the run has deleted it: what it held is released,
        // what it committed stays so. Nothing returns a gone holder's
        // units: a return is a live claim's move into a returnOn state.
        if operation == SupplyOperation::Release {
            return Ok(Holder {
                item: HolderItem {
                    hold: call.hold.clone(),
                    item: call.item,
                    supply: RowRef::of(view),
                    ask: call.ask,
                    state: needs,
                    price: None,
                },
                gone: true,
            });
        }
        return Err(refuse(
            format!("{} {} does not exist", holder.schema, holder.id),
            None,
        ));
    };
    let use_id = spec.uses[index].id().to_owned();
    let stored = view.sub_rows_of(&holder.schema, &holder.id, &use_id, crate::holds::ITEMS)?;
    let items = crate::holds::holder_items(spec, index, &row, &stored)
        .map_err(|reason| view.defect(reason))?;
    let Some(item) = items.into_iter().find(|item| item.is(call)) else {
        return Err(refuse(
            format!(
                "{} {} has no item {} of hold {}",
                holder.schema, holder.id, call.item, call.hold
            ),
            None,
        ));
    };
    let supply = RowRef::of(view);
    if item.supply != supply {
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
                item.ask.to_json(),
                call.ask.to_json()
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
    Ok(Holder { item, gone: false })
}

/// The `not_requested` refusal of a call. Its details name the holder, the
/// hold and the item, the state the call needs the holder's item in
/// (`needs`), and, when known, the state the holder shows (`holderState`)
/// and the one the supply records (`supplyState`).
pub fn not_requested<C>(
    _view: &View<'_, C>,
    operation: SupplyOperation,
    call: &SupplyCall,
    reason: String,
    holder_state: Option<ItemState>,
    supply_state: Option<ItemState>,
) -> Refusal {
    let mut details = Map::new();
    details.insert("holder".into(), call.holder.to_json());
    details.insert("hold".into(), Value::from(call.hold.as_str()));
    details.insert("item".into(), Value::from(call.item));
    details.insert("needs".into(), Value::from(operation.state().as_str()));
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
}

/// A supply's record of one holder's item, a row of the supply's record
/// sub-table keyed by [`record_key`]: what the item asked, its state, the
/// price it was held at, when it last moved, and whether it is a deleted
/// holder's (`retired`), which keeps its state under a key of its own and
/// no longer answers for the holder's key.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SupplyRecord {
    /// Its key in the record sub-table.
    pub key: String,
    pub holder: RowRef,
    pub hold: String,
    pub item: i64,
    pub ask: Ask,
    pub state: ItemState,
    pub price: Option<Price>,
    /// When the record last moved, by the command's time.
    pub at: i64,
    pub retired: bool,
}

impl SupplyRecord {
    /// The record a sub-table row holds; `None` for one of another shape.
    pub fn from_row(row: &SubRow) -> Option<SupplyRecord> {
        let number = |name: &str| row.value(name).and_then(integer);
        let text = |name: &str| row.value(name).and_then(Value::as_str).map(str::to_owned);
        let ask = match (number("quantity"), number("startMs"), number("endMs")) {
            (Some(quantity), _, _) => Ask::Quantity(quantity),
            (None, Some(start_ms), Some(end_ms)) => Ask::Interval(Interval { start_ms, end_ms }),
            _ => return None,
        };
        Some(SupplyRecord {
            key: row.key.clone(),
            holder: RowRef::new(text("holderSchema")?, text("holderId")?),
            hold: text("hold")?,
            item: number("item")?,
            ask,
            state: ItemState::parse(&text("state")?)?,
            price: number("unitPrice").map(|unit_price| Price {
                unit_price,
                currency: text("currency"),
            }),
            at: number("at").unwrap_or_default(),
            retired: row.value("retired") == Some(&Value::Bool(true)),
        })
    }

    /// The values a sub-table row holds of it, the key left out: every
    /// field a record table declares, null for one it has no value of.
    pub fn values(&self) -> Map<String, Value> {
        let mut out = Map::new();
        out.insert(
            "holderSchema".into(),
            Value::from(self.holder.schema.as_str()),
        );
        out.insert("holderId".into(), Value::from(self.holder.id.as_str()));
        out.insert("hold".into(), Value::from(self.hold.as_str()));
        out.insert("item".into(), Value::from(self.item));
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
        out.insert(
            "unitPrice".into(),
            self.price
                .as_ref()
                .map_or(Value::Null, |price| Value::from(price.unit_price)),
        );
        out.insert(
            "currency".into(),
            self.price
                .as_ref()
                .and_then(|price| price.currency.clone())
                .map_or(Value::Null, Value::from),
        );
        out.insert("at".into(), Value::from(self.at));
        out.insert("retired".into(), Value::Bool(self.retired));
        out
    }

    /// `{ holder, hold, item, quantity | interval, state, unitPrice?,
    /// currency?, at, retired? }`, `retired` only when true.
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
        if self.retired {
            out.insert("retired".into(), Value::Bool(true));
        }
        Value::Object(out)
    }

    /// Whether the record is the live one of the item a call names.
    pub fn is(&self, call: &SupplyCall) -> bool {
        !self.retired
            && self.holder == call.holder
            && self.hold == call.hold
            && self.item == call.item
    }
}

/// The key of the live record of the item a call names: the holder's type
/// and key, the hold and the item's index, joined by `/`.
pub fn record_key(call: &SupplyCall) -> String {
    format!(
        "{}/{}/{}/{}",
        call.holder.schema, call.holder.id, call.hold, call.item
    )
}

/// Every record of a record sub-table, retired ones too, in key order.
pub fn records_of(rows: &[SubRow]) -> Vec<SupplyRecord> {
    rows.iter().filter_map(SupplyRecord::from_row).collect()
}

/// The live record of the item a call names among a record table's rows.
pub fn recorded_in(rows: &[SubRow], call: &SupplyCall) -> Option<SupplyRecord> {
    let key = record_key(call);
    rows.iter()
        .filter(|row| row.key == key)
        .filter_map(SupplyRecord::from_row)
        .find(|record| record.is(call))
}

/// What [`begin`] makes of a writing call.
#[derive(Clone, Debug, PartialEq)]
pub enum Begun {
    /// The call moves the record: the holder's item, and the record as it
    /// is (`None` for none yet).
    Apply {
        holder: Holder,
        recorded: Option<SupplyRecord>,
    },
    /// The call changes nothing: the record, if any, and whether the call
    /// retired it for a gone holder.
    Done(Option<SupplyRecord>),
}

/// Starts a writing call of the contract on the supply's row: the holder
/// check ([`holder_check`]), the supply's lock ([`lock_key`]), then the
/// step its record in the sub-table `table` takes
/// ([`SupplyOperation::step`]), refusing a record the call does not move
/// from, or one that asks otherwise, with `not_requested`. A gone holder's
/// `releaseFor` releases a held record, which [`finish`] then retires,
/// and retires any other at once, changing nothing else: what it
/// committed stays committed.
pub fn begin<C>(
    cx: &superschematic_rules::Cx<'_, C>,
    table: &str,
    operation: SupplyOperation,
    call: &SupplyCall,
) -> Hook<Begun> {
    let holder = holder_check(cx, operation, call)?;
    cx.lock(lock_key(cx.type_name(), cx.key().unwrap_or_default()))?;
    let rows = cx.sub_rows(table)?;
    let record = recorded_in(&rows, call);
    // A record moves only with what it was taken for, so a commit or a
    // return moves no more than the item held.
    if let Some(found) = record.as_ref().filter(|found| found.ask != call.ask) {
        return Err(not_requested(
            cx,
            operation,
            call,
            format!(
                "this supply records item {} of hold {} of {} {} asking {}, not {}",
                call.item,
                call.hold,
                call.holder.schema,
                call.holder.id,
                found.ask.to_json(),
                call.ask.to_json()
            ),
            Some(holder.item.state),
            Some(found.state),
        )
        .veto(cx));
    }
    if holder.gone {
        return Ok(match record {
            Some(found) if found.state == ItemState::Held => Begun::Apply {
                holder,
                recorded: Some(found),
            },
            Some(found) => {
                retire(cx, table, &found)?;
                Begun::Done(Some(SupplyRecord {
                    retired: true,
                    ..found
                }))
            }
            None => Begun::Done(None),
        });
    }
    match operation.step(record.as_ref().map(|record| record.state)) {
        Some(Step::Apply) => Ok(Begun::Apply {
            holder,
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
                Some(holder.item.state),
                state,
            )
            .veto(cx))
        }
    }
}

/// Retires a deleted holder's record: it keeps its state, so what it took
/// stays taken (committed units sold, committed time booked), under a key
/// of its own derived from the command, so a holder created again with the
/// key holds afresh.
pub fn retire<C>(
    cx: &superschematic_rules::Cx<'_, C>,
    table: &str,
    record: &SupplyRecord,
) -> Hook<()> {
    let key = format!("{}#{}", record.key, cx.new_key("retired")?);
    cx.delete_row(table, &record.key)?;
    cx.insert_row(
        table,
        &key,
        SupplyRecord {
            key: key.clone(),
            retired: true,
            at: cx.now(),
            ..record.clone()
        }
        .values(),
    )
}

/// Records the move a call makes in the supply's record table `table`, and
/// answers it ([`outcome`]). `holdFor` records `price`; the other calls
/// keep the price the record has. A gone holder's release retires the
/// record it released ([`retire`]).
pub fn finish<C>(
    cx: &superschematic_rules::Cx<'_, C>,
    table: &str,
    operation: SupplyOperation,
    call: &SupplyCall,
    begun: &Begun,
    price: Option<Price>,
) -> Hook<Value> {
    let (holder, recorded) = match begun {
        Begun::Apply { holder, recorded } => (holder, recorded),
        Begun::Done(record) => return done(cx, operation, record.as_ref()),
    };
    let state = operation.state();
    let price = if operation == SupplyOperation::Hold {
        price
    } else {
        recorded.as_ref().and_then(|record| record.price.clone())
    };
    let record = SupplyRecord {
        key: record_key(call),
        holder: call.holder.clone(),
        hold: call.hold.clone(),
        item: call.item,
        ask: call.ask,
        state,
        price,
        at: cx.now(),
        retired: false,
    };
    if recorded.is_some() {
        cx.update_row(table, &record.key, record.values())?;
    } else {
        cx.insert_row(table, &record.key, record.values())?;
    }
    if holder.gone {
        retire(cx, table, &record)?;
    }
    Ok(outcome(state, true, record.price.as_ref()))
}

/// Answers a call that moved no record's state, with the record as it is,
/// and, unless [`begin`] retired it for a gone holder, says the call
/// changed nothing ([`superschematic_rules::Cx::unchanged`]).
pub fn done<C>(
    cx: &superschematic_rules::Cx<'_, C>,
    operation: SupplyOperation,
    record: Option<&SupplyRecord>,
) -> Hook<Value> {
    let retired = record.is_some_and(|record| record.retired);
    if !retired {
        cx.unchanged()?;
    }
    let state = record.map_or(operation.state(), |record| record.state);
    Ok(outcome(
        state,
        retired,
        record.and_then(|record| record.price.as_ref()),
    ))
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

/// What `checkHold` answers from the supply's record of the item a call
/// names, as `holdFor` would take it: an item the record holds already,
/// asking the same, fits; a record in any other state is `not_requested`,
/// which `holdFor` would refuse; `None` when the supply has no record of
/// it, and what is free decides.
pub fn check_record(call: &SupplyCall, record: Option<&SupplyRecord>) -> Option<CheckAnswer> {
    let found = record?;
    if found.state == ItemState::Held && found.ask == call.ask {
        return Some(CheckAnswer::Fits);
    }
    let mut details = Map::new();
    details.insert("holder".into(), call.holder.to_json());
    details.insert("hold".into(), Value::from(call.hold.as_str()));
    details.insert("item".into(), Value::from(call.item));
    details.insert("supplyState".into(), Value::from(found.state.as_str()));
    Some(CheckAnswer::Refused(Refusal {
        code: NOT_REQUESTED.to_owned(),
        reason: format!(
            "this supply records item {} of hold {} of {} {} as {}{}, which holdFor does not move",
            call.item,
            call.hold,
            call.holder.schema,
            call.holder.id,
            found.state.as_str(),
            if found.ask == call.ask {
                String::new()
            } else {
                format!(" asking {}", found.ask.to_json())
            }
        ),
        details,
    }))
}

/// A supply's row as a read across its table gives it to its answer hook,
/// read through a view: the row and every sub-table of the uses of its
/// type that implement the contract, so a guard asks a supply of a row it
/// read by key ([`superschematic_rules::View::ask`]). `None` when the type
/// holds no row of the key.
pub fn keyed_row<C>(view: &View<'_, C>, type_name: &str, key: &str) -> Hook<Option<KeyedRow>> {
    let Some(row) = view.read_row(type_name, key)? else {
        return Ok(None);
    };
    let mut keyed = KeyedRow::new(key, row.clone());
    for table in view.contract_tables(type_name, NAME)? {
        let rows = view.read_sub_rows(type_name, key, &table.use_id, &table.table)?;
        keyed
            .tables
            .entry(table.use_id.clone())
            .or_default()
            .insert(table.table.clone(), rows.to_vec());
    }
    Ok(Some(keyed))
}

/// The sub-table rows of one of a use's tables in a keyed row, or none.
pub fn table_of<'k>(
    tables: &'k std::collections::BTreeMap<String, Vec<SubRow>>,
    name: &str,
) -> &'k [SubRow] {
    tables.get(name).map_or(&[][..], Vec::as_slice)
}

/// An owned field of a row as an integer.
pub fn owned_integer(row: &Row, use_id: &str, field: &str) -> Option<i64> {
    row.owned(use_id, field).and_then(integer)
}

/// A JSON integer, as JavaScript holds one: 2 or 2.0, within what a double
/// holds exactly; a column's decimal text too.
pub fn integer(value: &Value) -> Option<i64> {
    match value {
        Value::Number(number) => number.as_i64().or_else(|| {
            number
                .as_f64()
                .filter(|n| n.is_finite() && n.fract() == 0.0 && n.abs() <= MAX_SAFE_INTEGER as f64)
                .map(|n| n as i64)
        }),
        Value::String(text) => text.parse().ok(),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn states_only_advance() {
        use ItemState::*;
        assert!(Pending.advances_to(Held) && Pending.advances_to(Released));
        assert!(Held.advances_to(Committed) && Held.advances_to(Released));
        assert!(Committed.advances_to(Returned));
        assert!(!Committed.advances_to(Released) && !Released.advances_to(Held));
        assert!(Held.takes() && Committed.takes() && !Released.takes());
        assert_eq!(SupplyOperation::Hold.step(None), Some(Step::Apply));
        assert_eq!(SupplyOperation::Hold.step(Some(Held)), Some(Step::Done));
        assert_eq!(SupplyOperation::Release.step(None), Some(Step::Done));
        assert_eq!(SupplyOperation::Commit.step(Some(Held)), Some(Step::Apply));
        assert_eq!(SupplyOperation::Commit.step(Some(Released)), None);
        assert_eq!(
            SupplyOperation::Return.step(Some(Committed)),
            Some(Step::Apply)
        );
    }

    #[test]
    fn a_call_reads_and_writes_as_its_parameters() {
        let call = SupplyCall {
            holder: RowRef::new("Checkout", "c1"),
            hold: "lines".into(),
            item: 2,
            ask: Ask::Quantity(3),
        };
        let params = call.to_check_params(&[Ask::Quantity(1)]);
        assert_eq!(
            params,
            json!({"holder": {"schema": "Checkout", "id": "c1"}, "hold": "lines", "item": 2, "quantity": 3, "also": [{"quantity": 1}]})
        );
        assert_eq!(
            SupplyCall::from_params(SupplyKind::Counted, &params),
            Some(call)
        );
        assert_eq!(
            SupplyCall::also(SupplyKind::Counted, &params),
            [Ask::Quantity(1)]
        );
        assert_eq!(lock_key("Inventory", "tea"), "Supply/Inventory/tea");
    }
}
