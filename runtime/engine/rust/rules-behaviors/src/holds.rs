//! Holds, one claim's holds on any supply, tied to its Workflow (D70), on a
//! table (D103's step 4): a checkout, a booking, a rental. The config names
//! the holds: a `link`, a to-one relation of the type (one item, the
//! relation's target), or `each`, an own list field whose elements each
//! name a `target` key of a `schema`, one item per element at its index.
//! Each hold asks a `quantity` (a count or a field) of counted supply, or
//! an `interval` (`start` with `end` or `durationMs`, date-time fields) of
//! timed supply, read from the element for an `each` hold and from the
//! type's own fields for a `link` hold. An element that names no supply, or
//! asks nothing (a quantity under 1, no start), is no item.
//!
//! Holds passes D103's test. State of its own: each item it has taken, its
//! supply, what it asks, its state and its price, in its `items` sub-table,
//! and the least advanced state and the total, its owned columns. Rules
//! over how it changes: a take of every item or none as the status enters
//! a `takeOn` state, commits, releases and returns as it enters the
//! others, items that only advance, fields a hold reads frozen while the
//! claim holds, and a delete that lets go of what is held. Recurrence:
//! every checkout, booking and rental holds what it asks until paid or let
//! go.
//!
//! Holds reaches a supply only through the contract Supply
//! ([`crate::supply`]): it finds the use of the item's supply type that
//! implements Supply by the declarations, asks it `checkHold` through the
//! answer hook ([`superschematic_rules::View::ask`]) and calls its
//! `holdFor`, `commitFor`, `releaseFor` and `returnFor` on the supply's row
//! ([`superschematic_rules::Cx::call_on`]), so a project's own supply is
//! held as Stock and Calendar are, and it reads none of a supply's
//! sub-tables. For each move it writes the item's new state first, then
//! calls the supply, in the same run: a supply's holder check reads the
//! claim's items as the run has them and takes a call only for an item
//! already in the state the call moves its record to.
//!
//! Taking. A create into a `takeOn` state (any create when the config names
//! none), an entry of one, and an update in one that changes what the
//! items ask while the claim holds nothing take the holds. The guard takes
//! the lock of each supply the take reads, in their keys' order
//! ([`crate::supply::lock_key`]), asks each supply's `checkHold` about each
//! item still to hold, with what the claim's other items still to hold ask
//! of the same row (`also`), and vetoes with `unavailable`, its details
//! listing every item that does not fit; an interval outside 1900 to 9000
//! is listed without asking the supply. The after-change hook then holds
//! each item through `holdFor`, in the same run, so the claim holds every
//! item or none, and keeps the unit price and currency `holdFor` returns.
//! `retakeHolds` takes again, refused as an entry is, while the claim is in
//! a `takeOn` state and an item is not held (`not_taking` otherwise).
//!
//! An entry is read from the Workflow the config names (`status`, implied
//! with one) as the row has it after the change, never from which
//! operation ran, so a Timers fire, a Lease expiry and a Reactions move are
//! caught as a caller's transition is: the status is a `takeOn` state and
//! no item the claim holds was taken at the Workflow's `enteredAt`, which
//! each item keeps (`takenAt`).
//!
//! Moving. In a `commitOn` state each held item is committed (`commitFor`),
//! in a `releaseOn` state each held one released (`releaseFor`) and each
//! pending one let go alone, in a `returnOn` state each committed item of
//! counted supply returned (`returnFor`). A move into a `commitOn` state
//! while an item is neither held nor committed is `holds_missing`. Holds
//! has no release of its own: a claim lets go by a move of its status,
//! which runs its guards and writes its row.
//!
//! An item's state only advances: `pending → held → committed →
//! returned`, or `pending | held → released`. A supply records each item
//! of a claim once, so `parse_config` refuses a transition from a
//! `releaseOn` or `returnOn` state into a `takeOn` state, and one from a
//! `takeOn` state to itself, which would start the hold's expiry again.
//!
//! While the claim holds (an item held or committed) an update that changes
//! a field a hold reads (an `each` list, a `link` hold's relation, quantity
//! or interval fields) is `held_fields`. A delete releases what the claim
//! holds; what it committed stays committed, units sold and time booked,
//! since a return is a move into a `returnOn` state and never a delete.
//! Each supply retires its records of the claim's items, so a claim created
//! again with the key holds afresh. A delete is `committed` while the claim
//! holds committed time that has not ended. More items than `maxItems`
//! (100 when absent) is `too_many_items`, and an item whose supply does not
//! exist, or implements no Supply, is `no_supply`.
//!
//! The fields: `Holds.items`, computed at a read, each `{ hold, item,
//! schema, id, quantity | startMs and endMs, state, unitPrice?, currency?
//! }`, by hold name in byte order and then index: every item the claim has
//! taken, as it stands, and every item its fields ask that it has not,
//! pending, so a cart shows what it would hold; `Holds.state`, the least
//! advanced item's state, `none` before any is asked; and `Holds.total`,
//! `{ amount, currency }`, the sum of the priced items ([`total_of`]): an
//! item with no price adds nothing, none priced is `{ amount: 0, currency:
//! null }`, and it is null only when the priced items' currencies differ or
//! the sum passes what a JSON number holds exactly. `state` and `total` are
//! owned columns each write of the row sets, so a Reactions rule hears
//! `Holds.state` turn `committed`.
//!
//! Expiry is a Timers deadline the schema writes, moving the claim into a
//! `releaseOn` state.
//!
//! configChange: holds, states and `maxItems` may change; Holds joins a
//! type that has rows (whose items are taken at their next entry into a
//! `takeOn` state, or by `retakeHolds`) and leaves only one that has none,
//! since what they hold would stay held.

use std::collections::BTreeMap;
use std::sync::OnceLock;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, ContractQuery, Cx, Declaration,
    GuardRequest, Hook, Row, SchemaIssue, SubRow, TypeSpec, UseSpec, Veto, View,
};

use crate::ajv;
use crate::declared;
use crate::supply::{
    self, Ask, CheckAnswer, Free, HolderItem, Interval, ItemState, Price, Refusal, RowRef,
    SupplyOperation, Unavailable, EARLIEST_MS, LATEST_MS, MAX_SAFE_INTEGER,
};
use crate::timers::parse_date_time;
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = supply::HOLDS;

/// Holds' declaration, the commerce package's file, read in place.
pub const DECLARATION: &str =
    include_str!("../../../../engine-commerce/rust/declarations/Holds.behavior.json");

/// The sub-table of the items the claim has taken.
pub const ITEMS: &str = "items";

/// The owned columns.
pub const STATE: &str = "state";
pub const TOTAL: &str = "total";

/// The operations.
pub const RETAKE_HOLDS: &str = "retakeHolds";
pub const LIST_HOLDS: &str = "listHolds";

/// The most items a claim's holds ask together when the config gives no
/// maxItems.
pub const DEFAULT_MAX_ITEMS: i64 = 100;

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
/// An item whose supply does not exist or implements no Supply.
pub const NO_SUPPLY: &str = "no_supply";

fn file() -> &'static Value {
    static FILE: OnceLock<Value> = OnceLock::new();
    FILE.get_or_init(|| ajv::file(DECLARATION))
}

/// Where a hold's items come from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum HoldSource {
    /// A to-one relation of the type: one item, index 0, the target the
    /// relation's own field holds the key of.
    Link {
        link: String,
        /// The own field that holds the target's key.
        field: String,
        /// The relation's target type.
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
    /// hold's relation field and its quantity and interval fields.
    pub fn reads(&self) -> Vec<String> {
        match &self.source {
            HoldSource::Each { field, .. } => vec![field.clone()],
            HoldSource::Link { field, .. } => {
                let mut out = vec![field.clone()];
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
    /// The Workflow use whose status moves the holds.
    pub status: String,
    /// That Workflow's initial state.
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

    // link_of is the link a field holds the target of, for a link hold.
    fn link_of(&self, field: &str) -> Option<&str> {
        self.holds.iter().find_map(|(_, spec)| match &spec.source {
            HoldSource::Link {
                link, field: held, ..
            } if held == field => Some(link.as_str()),
            _ => None,
        })
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

    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then its rules against the type's Workflow, relations and
    /// fields, each a reason the core prefixes with the type and the
    /// behavior. The supply types a hold names are the build's to hold to
    /// Supply (D103: the loader checks that a hold names a type that
    /// implements it), since a type's define check sees its own spec alone.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<HoldsConfig, ConfigError> {
        let issues = ajv::config_issues(file(), raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues));
        }
        let status = workflow_use(raw, target.uses())?;
        let flow = target
            .uses()
            .iter()
            .find(|entry| entry.id() == status)
            .and_then(|entry| Flow::of(&entry.config_or_empty()));
        let mut holds = Vec::new();
        if let Some(given) = raw.get("holds").and_then(Value::as_object) {
            for (name, spec) in given {
                holds.push((name.to_owned(), parse_hold(target, name, spec)?));
            }
        }
        let take_on = raw.get("takeOn").map(|states| strings(Some(states)));
        let commit_on = strings(raw.get("commitOn"));
        let release_on = strings(raw.get("releaseOn"));
        let return_on = strings(raw.get("returnOn"));
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
                        return Err(format!(
                            "{at} names \"{state}\", which is not a state of the type's Workflow ({})",
                            flow.states.join(", ")
                        )
                        .into());
                    }
                }
            }
            for (index, (at, states)) in lists.iter().enumerate() {
                for (other, others) in lists.iter().skip(index + 1) {
                    if let Some(state) = states.iter().find(|state| others.contains(state)) {
                        return Err(format!(
                            "{at} and {other} both name \"{state}\": a state takes, commits, releases or returns"
                        )
                        .into());
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
                        return Err(format!(
                            "the Workflow moves from {from}, where the claim lets go of what it holds, to {to}, where it takes it; a supply holds each item of a claim once"
                        )
                        .into());
                    }
                    // Entering a takeOn state again starts its expiry
                    // again: a claim could keep what it holds for good.
                    if from == to && take_on.contains(to) {
                        return Err(format!(
                            "the Workflow moves from {from} to itself, and {from} takes holds: entering it again would start a deadline that expires the hold again, so the claim could hold for good"
                        )
                        .into());
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
                .and_then(supply::integer)
                .unwrap_or(DEFAULT_MAX_ITEMS),
            status,
            initial: flow.and_then(|flow| flow.initial),
        })
    }
}

// workflow_use is the id of the Workflow use the config's status names, or
// the type's one.
fn workflow_use(raw: &Value, uses: &[UseSpec]) -> Result<String, ConfigError> {
    let workflows: Vec<&UseSpec> = uses
        .iter()
        .filter(|entry| entry.behavior == workflow::NAME)
        .collect();
    let ids = || -> String {
        let ids: Vec<&str> = workflows.iter().map(|entry| entry.id()).collect();
        if ids.is_empty() {
            "none".to_owned()
        } else {
            ids.join(", ")
        }
    };
    match raw.get("status").and_then(Value::as_str) {
        Some(named) => match workflows.iter().find(|entry| entry.id() == named) {
            Some(entry) => Ok(entry.id().to_owned()),
            None => Err(format!(
                "status \"{named}\" is not a Workflow of the type (its Workflows: {})",
                ids()
            )
            .into()),
        },
        None => match workflows.as_slice() {
            [] => Err("Holds moves with the status of Workflow, which the type does not list".into()),
            [entry] => Ok(entry.id().to_owned()),
            _ => Err(format!(
                "the type has several Workflows ({}): status names the one whose status moves the holds",
                ids()
            )
            .into()),
        },
    }
}

/// A Workflow config as the type holds it.
struct Flow {
    states: Vec<String>,
    initial: Option<String>,
    transitions: Vec<(String, String)>,
}

impl Flow {
    fn of(raw: &Value) -> Option<Flow> {
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

// own_field holds a field a link hold reads to an own field of the type:
// an integer one for a quantity, one whose values are instants for an
// interval.
fn own_field(
    target: &ConfigTarget<'_>,
    at: &str,
    field: &str,
    date_time: bool,
) -> Result<(), ConfigError> {
    if date_time {
        let found = crate::stock::field_named(target, at, field)?;
        if !found.date_time {
            return Err(format!(
                "{at} \"{field}\" is not a date-time field of {}",
                target.type_name()
            )
            .into());
        }
        return Ok(());
    }
    crate::stock::own_field(
        target,
        at,
        field,
        &crate::stock::INTEGER_TYPES,
        "an integer",
    )
}

fn parse_hold(target: &ConfigTarget<'_>, name: &str, raw: &Value) -> Result<HoldSpec, ConfigError> {
    let at = format!("holds.{name}");
    let ask = if let Some(quantity) = raw.get("quantity") {
        HoldAsk::Quantity(match quantity {
            Value::Number(_) => Amount::Count(supply::integer(quantity).unwrap_or(1)),
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
                    .and_then(supply::integer)
                    .unwrap_or(0),
            ),
        };
        HoldAsk::Interval { start, end }
    };
    let source = match raw.get("link").and_then(Value::as_str) {
        Some(link) => {
            let relations: Vec<(&str, &str, &str)> = target
                .fields()
                .iter()
                .filter_map(|field| {
                    field.relation.as_ref().map(|relation| {
                        (
                            relation.name.as_str(),
                            field.name.as_str(),
                            relation.type_name.as_str(),
                        )
                    })
                })
                .collect();
            let Some((_, field, schema)) = relations.iter().find(|(known, _, _)| *known == link)
            else {
                let listed = if relations.is_empty() {
                    "it has none".to_owned()
                } else {
                    format!(
                        "its to-one relations: {}",
                        relations
                            .iter()
                            .map(|(name, _, _)| *name)
                            .collect::<Vec<_>>()
                            .join(", ")
                    )
                };
                return Err(format!(
                    "{at}.link {link} is not a to-one relation of {} ({listed}), so a hold has no one supply to read",
                    target.type_name()
                )
                .into());
            };
            match &ask {
                HoldAsk::Quantity(Amount::Field(field)) => {
                    own_field(target, &format!("{at}.quantity.field"), field, false)?;
                }
                HoldAsk::Quantity(Amount::Count(_)) => {}
                HoldAsk::Interval { start, end } => {
                    own_field(target, &format!("{at}.interval.start"), start, true)?;
                    if let IntervalEnd::Field(end) = end {
                        own_field(target, &format!("{at}.interval.end"), end, true)?;
                    }
                }
            }
            HoldSource::Link {
                link: link.to_owned(),
                field: (*field).to_owned(),
                schema: (*schema).to_owned(),
            }
        }
        None => {
            let each = raw.get("each");
            let field = field_of(each).unwrap_or_default();
            crate::stock::field_named(target, &format!("{at}.each.field"), &field)?;
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
    Ok(HoldSpec { source, ask })
}

/// An item the claim has taken, as its sub-table row holds it, with the
/// Workflow entry it was taken at.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Stored {
    pub item: HolderItem,
    pub taken_at: Option<i64>,
}

/// The key of an item's row: its hold and index, joined by `/`.
pub fn item_key(hold: &str, item: i64) -> String {
    format!("{hold}/{item}")
}

/// The claim's taken items, by hold name in byte order, then index.
pub fn stored_of(rows: &[SubRow]) -> Vec<Stored> {
    let mut out: Vec<Stored> = rows
        .iter()
        .filter_map(|row| {
            let number = |name: &str| row.value(name).and_then(supply::integer);
            let text = |name: &str| row.value(name).and_then(Value::as_str);
            let ask = match (number("quantity"), number("startMs"), number("endMs")) {
                (Some(quantity), _, _) => Ask::Quantity(quantity),
                (None, Some(start_ms), Some(end_ms)) => {
                    Ask::Interval(Interval { start_ms, end_ms })
                }
                _ => return None,
            };
            Some(Stored {
                item: HolderItem {
                    hold: text("hold")?.to_owned(),
                    item: number("item")?,
                    supply: RowRef::new(text("supplySchema")?, text("supplyId")?),
                    ask,
                    state: ItemState::parse(text("state")?)?,
                    price: number("unitPrice").map(|unit_price| Price {
                        unit_price,
                        currency: text("currency").map(str::to_owned),
                    }),
                },
                taken_at: number("takenAt"),
            })
        })
        .collect();
    out.sort_by(|a, b| order(&a.item, &b.item));
    out
}

fn order(a: &HolderItem, b: &HolderItem) -> std::cmp::Ordering {
    a.hold
        .as_bytes()
        .cmp(b.hold.as_bytes())
        .then(a.item.cmp(&b.item))
}

/// The least advanced state of the items: `none` before any.
pub fn state_of(items: &[HolderItem]) -> &'static str {
    items
        .iter()
        .map(|item| item.state)
        .min()
        .map_or("none", ItemState::as_str)
}

/// The sum of the priced items' unit prices times their quantities (1 for
/// time), as `{ amount, currency }`. An item without a price, such as a
/// pending one, time a supply gives no price, or units of a Stock with no
/// `price`, adds nothing. With no priced item it is `{ amount: 0,
/// currency: null }`. It is null only when the priced items' currencies
/// differ (a price without a currency differs from every one), or the sum
/// passes what a JSON number holds exactly, so a reader that needs money
/// fails closed on null.
pub fn total_of(items: &[HolderItem]) -> Value {
    let mut amount: i64 = 0;
    let mut currency: Option<&str> = None;
    let mut priced = false;
    for item in items {
        let Some(price) = &item.price else {
            continue;
        };
        let Some(code) = price.currency.as_deref() else {
            return Value::Null;
        };
        if priced && currency != Some(code) {
            return Value::Null;
        }
        priced = true;
        currency = Some(code);
        let units = item.ask.quantity().unwrap_or(1);
        let Some(next) = price
            .unit_price
            .checked_mul(units)
            .and_then(|line| amount.checked_add(line))
            .filter(|sum| sum.abs() <= MAX_SAFE_INTEGER)
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

// date_time reads an instant: epoch milliseconds from a table's adapter,
// or ISO 8601 text.
fn date_time(value: Option<&Value>) -> Option<i64> {
    match value? {
        Value::String(text) => parse_date_time(text),
        value => supply::integer(value),
    }
}

// key_text reads a key a field holds: text, or a whole number's decimal
// text.
fn key_text(value: Option<&Value>) -> Option<String> {
    match value? {
        Value::String(text) if !text.is_empty() => Some(text.clone()),
        Value::Number(number) => Some(number.to_string()),
        _ => None,
    }
}

// listed reads an `each` hold's list: a JSON array, or the JSON text of one,
// as a SQLite column holds a list of a struct its layout gives no
// conversion (D103's step 4).
fn listed(value: Option<&Value>) -> Option<Vec<Value>> {
    match value? {
        Value::Array(items) => Some(items.clone()),
        Value::String(text) => match serde_json::from_str(text) {
            Ok(Value::Array(items)) => Some(items),
            _ => None,
        },
        _ => None,
    }
}

// same_field says whether a field a hold reads holds one value before and
// after a write, a list compared as its elements whether it came as JSON or
// as its text.
fn same_field(before: Option<&Value>, after: Option<&Value>) -> bool {
    let normal = |value: Option<&Value>| match value {
        Some(Value::String(text)) => match serde_json::from_str::<Value>(text) {
            Ok(parsed @ Value::Array(_)) => Some(parsed),
            _ => value.cloned(),
        },
        other => other.cloned(),
    };
    crate::values::equal(normal(before).as_ref(), normal(after).as_ref())
}

// ask_of is what an item asks, read from `source` (an element, or the
// type's own fields); `None` when it asks nothing.
fn ask_of(spec: &HoldSpec, source: &Map<String, Value>) -> Option<Ask> {
    match &spec.ask {
        HoldAsk::Quantity(Amount::Count(count)) => Some(Ask::Quantity(*count)),
        HoldAsk::Quantity(Amount::Field(field)) => source
            .get(field)
            .and_then(supply::integer)
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

/// The items the claim's holds ask now, each pending, from its own fields.
pub fn plan(config: &HoldsConfig, own: &Map<String, Value>) -> Vec<HolderItem> {
    let mut out = Vec::new();
    for (name, spec) in &config.holds {
        match &spec.source {
            HoldSource::Link { field, schema, .. } => {
                let (Some(id), Some(ask)) = (key_text(own.get(field)), ask_of(spec, own)) else {
                    continue;
                };
                out.push(HolderItem {
                    hold: name.clone(),
                    item: 0,
                    supply: RowRef::new(schema.as_str(), id),
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
                let Some(elements) = listed(own.get(field)) else {
                    continue;
                };
                for (index, element) in elements.iter().enumerate() {
                    let Some(element) = element.as_object() else {
                        continue;
                    };
                    let Some(id) = key_text(element.get(target)) else {
                        continue;
                    };
                    let Some(ask) = ask_of(spec, element) else {
                        continue;
                    };
                    out.push(HolderItem {
                        hold: name.clone(),
                        item: index as i64,
                        supply: RowRef::new(schema.as_str(), id),
                        ask,
                        state: ItemState::Pending,
                        price: None,
                    });
                }
            }
        }
    }
    out.sort_by(order);
    out
}

/// The items after a take: each stored item that is past pending stays as
/// it is (a supply records each item once); each planned item takes the
/// place of a pending one at its hold and index.
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
    out.sort_by(order);
    out
}

/// A holder's items as its Holds shows them, for a supply's holder check
/// ([`crate::supply::holder_check`]): the Holds use at `index` of the
/// holder's spec, its row and its items' rows; why its config does not
/// parse, which is the holder's type's defect, otherwise.
pub fn holder_items(
    spec: &TypeSpec,
    index: usize,
    row: &Row,
    rows: &[SubRow],
) -> Result<Vec<HolderItem>, String> {
    let entry = &spec.uses[index];
    let config = HoldsConfig::parse(&entry.config_or_empty(), &ConfigTarget::new(spec, index))
        .map_err(|error| format!("{}'s Holds config: {error}", spec.name))?;
    let stored: Vec<HolderItem> = stored_of(rows)
        .into_iter()
        .map(|stored| stored.item)
        .collect();
    Ok(merge(&stored, &plan(&config, &row.own)))
}

// status_of is the claim's status and when it was entered, as the run has
// its Workflow's fields.
fn status_of(view: &View<'_, HoldsConfig>) -> (Option<String>, Option<i64>) {
    let use_id = &view.config().status;
    (
        view.get_of(use_id, workflow::STATUS)
            .and_then(|status| status.as_str().map(str::to_owned)),
        view.get_of(use_id, workflow::ENTERED_AT)
            .as_ref()
            .and_then(supply::integer),
    )
}

fn stored(view: &View<'_, HoldsConfig>) -> Hook<Vec<Stored>> {
    Ok(stored_of(&view.sub_rows(ITEMS)?))
}

fn taken(view: &View<'_, HoldsConfig>) -> Hook<Vec<HolderItem>> {
    Ok(stored(view)?
        .into_iter()
        .map(|stored| stored.item)
        .collect())
}

/// The items the claim's holds ask now, as `Holds.items` reads them: each
/// taken item as it is, and each item its own fields ask that is not taken
/// yet, pending.
fn items_now(view: &View<'_, HoldsConfig>) -> Hook<Vec<HolderItem>> {
    Ok(merge(&taken(view)?, &plan(view.config(), &view.own())))
}

fn contains(states: &[String], status: Option<&str>) -> bool {
    status.is_some_and(|status| states.iter().any(|state| state == status))
}

fn holding(items: &[HolderItem]) -> bool {
    items.iter().any(|item| item.state.takes())
}

fn veto(reason: String, code: &str, details: Option<Map<String, Value>>) -> Veto {
    let veto = Veto::new(reason).with_code(code);
    match details {
        Some(details) => veto.with_details(details),
        None => veto,
    }
}

// out_of_range is the refusal of an item whose interval lies outside what
// the contract holds, 1900 to 9000, which no supply is asked about: a
// claim's own date-time fields may hold any year.
fn out_of_range(item: &HolderItem) -> Option<Refusal> {
    let interval = item.ask.interval()?;
    if interval.start_ms >= EARLIEST_MS && interval.end_ms <= LATEST_MS {
        return None;
    }
    Some(
        Unavailable {
            supply: item.supply.clone(),
            hold: item.hold.clone(),
            item: item.item,
            asked: item.ask,
            free: Free::Available(0),
        }
        .refusal(format!(
            "item {} of hold {} asks time outside 1900 to 9000, which no supply holds",
            item.item, item.hold
        )),
    )
}

// alongside is what each pending item's checkHold counts beside it: the
// asks of the claim's other pending items of the same supply, so two lines
// of one supply are checked together, as the after-change hook then holds
// them.
fn alongside(items: &[HolderItem]) -> Vec<Vec<Ask>> {
    let pending: Vec<&HolderItem> = items
        .iter()
        .filter(|item| item.state == ItemState::Pending && out_of_range(item).is_none())
        .collect();
    items
        .iter()
        .map(|item| {
            pending
                .iter()
                .filter(|other| {
                    other.supply == item.supply
                        && !(other.hold == item.hold && other.item == item.item)
                })
                .map(|other| other.ask)
                .collect()
        })
        .collect()
}

// lock_supplies takes the lock of each supply the items name, in their
// keys' order, so two claims that take the same supplies take their locks
// alike and neither waits on the other's.
fn lock_supplies<'a>(
    view: &View<'_, HoldsConfig>,
    items: impl IntoIterator<Item = &'a HolderItem>,
) -> Hook<()> {
    let mut keys: Vec<String> = items
        .into_iter()
        .map(|item| supply::lock_key(&item.supply.schema, &item.supply.id))
        .collect();
    keys.sort();
    keys.dedup();
    for key in keys {
        view.lock(key)?;
    }
    Ok(())
}

// supply_use is the use of a supply's type that implements Supply, or why
// it holds nothing for a claim.
fn supply_use(view: &View<'_, HoldsConfig>, supply: &RowRef) -> Hook<Result<String, String>> {
    let found = view.implementers(&supply.schema, supply::NAME)?;
    Ok(match found.as_slice() {
        [] => Err(format!("{} implements no {}", supply.schema, supply::NAME)),
        [one] => Ok(one.clone()),
        several => Err(format!(
            "{} implements {} {} times ({})",
            supply.schema,
            supply::NAME,
            several.len(),
            several.join(", ")
        )),
    })
}

// check_one asks a pending item's supply whether it fits beside `also`:
// its answer, or why the supply holds nothing for a claim.
fn check_one(
    view: &View<'_, HoldsConfig>,
    item: &HolderItem,
    also: &[Ask],
) -> Hook<Result<CheckAnswer, String>> {
    if let Some(refusal) = out_of_range(item) {
        return Ok(Ok(CheckAnswer::Refused(refusal)));
    }
    if let Err(why) = supply_use(view, &item.supply)? {
        return Ok(Err(why));
    }
    let Some(keyed) = supply::keyed_row(view, &item.supply.schema, &item.supply.id)? else {
        return Ok(Err(format!(
            "{} {} does not exist",
            item.supply.schema, item.supply.id
        )));
    };
    let holder = RowRef::of(view);
    let mut params = item.call(&holder).to_check_params(also);
    params["supply"] = item.supply.to_json();
    let answers = view.ask(
        &item.supply.schema,
        &keyed,
        &ContractQuery::new(supply::NAME, supply::CHECK_HOLD, &params),
    )?;
    let Some((use_id, answer)) = answers.into_iter().next() else {
        return Ok(Err(format!(
            "{} implements no {}",
            item.supply.schema,
            supply::NAME
        )));
    };
    match CheckAnswer::from_json(&answer) {
        Some(answer) => Ok(Ok(answer)),
        None => Err(view.defect(format!(
            "{use_id} of {} {} answered checkHold with {answer}",
            item.supply.schema, item.supply.id
        ))),
    }
}

// check asks each pending item's supply whether it fits beside the claim's
// other pending items of the same supply, under the supplies' locks, and
// vetoes with too_many_items, no_supply, or unavailable listing every item
// that does not fit.
fn check(view: &View<'_, HoldsConfig>, items: &[HolderItem]) -> Hook<Option<Veto>> {
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
    lock_supplies(
        view,
        items
            .iter()
            .filter(|item| item.state == ItemState::Pending && out_of_range(item).is_none()),
    )?;
    let mut refusals: Vec<Refusal> = Vec::new();
    let also = alongside(items);
    for (item, also) in items.iter().zip(&also) {
        if item.state != ItemState::Pending {
            continue;
        }
        match check_one(view, item, also)? {
            Ok(CheckAnswer::Fits) => {}
            Ok(CheckAnswer::Refused(refusal)) => refusals.push(refusal),
            Err(why) => {
                let mut details = Map::new();
                details.insert("supply".into(), item.supply.to_json());
                details.insert("hold".into(), Value::from(item.hold.as_str()));
                details.insert("item".into(), Value::from(item.item));
                return Ok(Some(veto(
                    format!(
                        "item {} of hold {} names {} {}, which holds nothing for a claim: {why}",
                        item.item, item.hold, item.supply.schema, item.supply.id
                    ),
                    NO_SUPPLY,
                    Some(details),
                )));
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

// put writes one item's row, with the Workflow entry it was taken at.
fn put(cx: &Cx<'_, HoldsConfig>, item: &HolderItem, taken_at: Option<i64>) -> Hook<()> {
    let key = item_key(&item.hold, item.item);
    let mut values = Map::new();
    values.insert("hold".into(), Value::from(item.hold.as_str()));
    values.insert("item".into(), Value::from(item.item));
    values.insert(
        "supplySchema".into(),
        Value::from(item.supply.schema.as_str()),
    );
    values.insert("supplyId".into(), Value::from(item.supply.id.as_str()));
    let interval = item.ask.interval();
    values.insert(
        "quantity".into(),
        item.ask.quantity().map_or(Value::Null, Value::from),
    );
    values.insert(
        "startMs".into(),
        interval.map_or(Value::Null, |interval| Value::from(interval.start_ms)),
    );
    values.insert(
        "endMs".into(),
        interval.map_or(Value::Null, |interval| Value::from(interval.end_ms)),
    );
    values.insert("state".into(), Value::from(item.state.as_str()));
    values.insert(
        "unitPrice".into(),
        item.price
            .as_ref()
            .map_or(Value::Null, |price| Value::from(price.unit_price)),
    );
    values.insert(
        "currency".into(),
        item.price
            .as_ref()
            .and_then(|price| price.currency.clone())
            .map_or(Value::Null, Value::from),
    );
    values.insert("takenAt".into(), taken_at.map_or(Value::Null, Value::from));
    if cx.sub_rows(ITEMS)?.iter().any(|row| row.key == key) {
        cx.update_row(ITEMS, &key, values)
    } else {
        cx.insert_row(ITEMS, &key, values)
    }
}

// moved writes an item's new state, then calls its supply's operation, and
// keeps the price holdFor returns. A return whose supply is gone moves the
// item alone: the units went with it.
fn moved(
    cx: &Cx<'_, HoldsConfig>,
    item: &mut HolderItem,
    operation: SupplyOperation,
    taken_at: Option<i64>,
) -> Hook<()> {
    item.state = operation.state();
    put(cx, item, taken_at)?;
    let use_id = match supply_use(cx, &item.supply)? {
        Ok(use_id) => use_id,
        Err(why) => {
            if operation == SupplyOperation::Return {
                return Ok(());
            }
            let mut details = Map::new();
            details.insert("supply".into(), item.supply.to_json());
            details.insert("hold".into(), Value::from(item.hold.as_str()));
            details.insert("item".into(), Value::from(item.item));
            return Err(cx.veto(veto(
                format!(
                    "item {} of hold {} names {} {}, which holds nothing for a claim: {why}",
                    item.item, item.hold, item.supply.schema, item.supply.id
                ),
                NO_SUPPLY,
                Some(details),
            )));
        }
    };
    if operation == SupplyOperation::Return
        && cx.row_of(&item.supply.schema, &item.supply.id)?.is_none()
    {
        return Ok(());
    }
    let call = item.call(&RowRef::of(cx));
    let answer = cx.call_on(
        &item.supply.schema,
        &item.supply.id,
        &use_id,
        operation.name(),
        &call.to_params(),
    )?;
    if operation == SupplyOperation::Hold {
        item.price = answer
            .get("unitPrice")
            .and_then(supply::integer)
            .map(|unit_price| Price {
                unit_price,
                currency: answer
                    .get("currency")
                    .and_then(Value::as_str)
                    .map(str::to_owned),
            });
        put(cx, item, taken_at)?;
    }
    Ok(())
}

// take holds each pending item, under the supplies' locks; too many items
// is too_many_items. A supply that refuses refuses the command, which
// writes none of the holds.
fn take(
    cx: &Cx<'_, HoldsConfig>,
    items: Vec<HolderItem>,
    taken_at: Option<i64>,
) -> Hook<Vec<HolderItem>> {
    let max = cx.config().max_items;
    if items.len() as i64 > max {
        return Err(cx.veto(veto(
            format!(
                "the holds ask {} items, more than the {max} one claim holds",
                items.len()
            ),
            TOO_MANY_ITEMS,
            None,
        )));
    }
    lock_supplies(
        cx,
        items.iter().filter(|item| item.state == ItemState::Pending),
    )?;
    let mut out = items;
    for item in out.iter_mut() {
        if item.state == ItemState::Pending {
            moved(cx, item, SupplyOperation::Hold, taken_at)?;
        }
    }
    Ok(out)
}

// settle moves the items as the status asks: commits the held ones in a
// commitOn state, releases the pending and held ones in a releaseOn
// state, returns the committed ones of counted supply in a returnOn state.
fn settle(
    cx: &Cx<'_, HoldsConfig>,
    items: &mut [HolderItem],
    taken: &[Stored],
    status: Option<&str>,
) -> Hook<()> {
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
    let moves = |item: &HolderItem| match (operation, item.state) {
        (SupplyOperation::Commit | SupplyOperation::Release, ItemState::Held) => true,
        (SupplyOperation::Return, ItemState::Committed) => item.ask.quantity().is_some(),
        _ => false,
    };
    lock_supplies(cx, items.iter().filter(|item| moves(item)))?;
    for item in items.iter_mut() {
        let taken_at = taken
            .iter()
            .find(|stored| stored.item.hold == item.hold && stored.item.item == item.item)
            .and_then(|stored| stored.taken_at);
        match (operation, item.state) {
            (SupplyOperation::Release, ItemState::Pending) => {
                // Nothing was held for a pending item: it is let go alone.
                item.state = ItemState::Released;
                put(cx, item, taken_at)?;
            }
            _ if moves(item) => moved(cx, item, operation, taken_at)?,
            _ => {}
        }
    }
    Ok(())
}

// let_go lets go of what a deleted claim took, through each supply's
// releaseFor, which finds the claim gone: what it held is released, what
// it committed stays committed (units sold, time booked; a return is a
// move into a returnOn state, never a delete), and the supply retires its
// record of each item, so a claim created again with the key holds
// afresh. The claim's rows go with it.
fn let_go(cx: &Cx<'_, HoldsConfig>) -> Hook<()> {
    let items: Vec<HolderItem> = taken(cx)?
        .into_iter()
        .filter(|item| item.state != ItemState::Pending)
        .collect();
    lock_supplies(cx, &items)?;
    let holder = RowRef::of(cx);
    for item in &items {
        let Ok(use_id) = supply_use(cx, &item.supply)? else {
            continue;
        };
        if cx.row_of(&item.supply.schema, &item.supply.id)?.is_none() {
            continue;
        }
        cx.call_on(
            &item.supply.schema,
            &item.supply.id,
            &use_id,
            supply::RELEASE_FOR,
            &item.call(&holder).to_params(),
        )?;
    }
    Ok(())
}

// write_summary sets the state and the total the items give, when they
// moved.
fn write_summary(cx: &Cx<'_, HoldsConfig>, items: &[HolderItem]) -> Hook<()> {
    let state = Value::from(state_of(items));
    if cx.get(STATE).as_ref() != Some(&state) {
        cx.set(STATE, state)?;
    }
    let total = total_of(items);
    let held = cx.get(TOTAL).unwrap_or(Value::Null);
    if !crate::values::equal(Some(&held), Some(&total)) {
        cx.set(TOTAL, total)?;
    }
    Ok(())
}

fn not_taking(cx: &Cx<'_, HoldsConfig>, reason: String) -> superschematic_rules::Stop {
    cx.veto(veto(reason, NOT_TAKING, None))
}

fn retake_holds(cx: &Cx<'_, HoldsConfig>) -> Hook<Value> {
    let config = cx.config();
    let (status, entered_at) = status_of(cx);
    let in_take = match &config.take_on {
        Some(_) => config.takes_in(status.as_deref()),
        None => !config.settled(status.as_deref()),
    };
    if !in_take {
        return Err(not_taking(
            cx,
            format!(
                "the claim is {}, no state that takes holds",
                status.as_deref().unwrap_or("in no state")
            ),
        ));
    }
    let items = items_now(cx)?;
    if !items.iter().any(|item| item.state == ItemState::Pending) {
        return Err(not_taking(
            cx,
            "the claim holds every item its holds ask".to_owned(),
        ));
    }
    if let Some(refused) = check(cx, &items)? {
        return Err(cx.veto(refused));
    }
    let items = take(cx, items, entered_at)?;
    write_summary(cx, &items)?;
    Ok(summary(&items))
}

fn list_holds(view: &View<'_, HoldsConfig>) -> Hook<Value> {
    let items = items_now(view)?;
    let also = alongside(&items);
    let mut listed = Vec::with_capacity(items.len());
    for (item, also) in items.iter().zip(&also) {
        let mut entry = item.to_json();
        if item.state == ItemState::Pending {
            let answer = match check_one(view, item, also)? {
                Ok(answer) => answer.to_json(),
                Err(why) => json!({
                    "fits": false,
                    "code": NO_SUPPLY,
                    "reason": format!("{} {} holds nothing for a claim: {why}", item.supply.schema, item.supply.id),
                }),
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

/// Holds: one claim's holds on any supply, tied to its Workflow.
#[derive(Clone, Copy, Debug, Default)]
pub struct Holds;

impl Behavior for Holds {
    type Config = HoldsConfig;

    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<HoldsConfig, ConfigError> {
        HoldsConfig::parse(raw, target)
    }

    // Holds joins a type with rows, which take at their next entry; it
    // leaves only one with none.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&HoldsConfig>,
        after: Option<&HoldsConfig>,
    ) -> Hook<Option<String>> {
        if before.is_some() && after.is_none() && cx.has_rows()? {
            return Ok(Some("what its instances hold would stay held".to_owned()));
        }
        Ok(None)
    }

    fn check_params(
        &self,
        _config: &HoldsConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        ajv::params_issues(file(), operation, params)
    }

    fn initialize(&self, cx: &Cx<'_, HoldsConfig>, _params: &Value) -> Hook<()> {
        write_summary(cx, &plan(cx.config(), &cx.own()))
    }

    // unavailable, too_many_items and no_supply for a take; holds_missing
    // for a commit; held_fields while the claim holds; committed for a
    // delete.
    fn guard(
        &self,
        view: &View<'_, HoldsConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let config = view.config();
        match request {
            GuardRequest::Create { own, .. } => {
                if !config.takes_at_create() {
                    return Ok(None);
                }
                check(view, &plan(config, own))
            }
            GuardRequest::Operation {
                use_id,
                behavior,
                operation: "transition",
                params,
                ..
            } if *behavior == workflow::NAME && *use_id == config.status.as_str() => {
                let to = params.get("to").and_then(Value::as_str);
                if contains(&config.commit_on, to) {
                    let items = items_now(view)?;
                    let missing: Vec<&HolderItem> =
                        items.iter().filter(|item| !item.state.takes()).collect();
                    if missing.is_empty() {
                        return Ok(None);
                    }
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
                if !config.takes_in(to) {
                    return Ok(None);
                }
                check(view, &items_now(view)?)
            }
            GuardRequest::Update { patch, after } => {
                let before = view.own();
                let changed: Vec<String> = config
                    .reads()
                    .into_iter()
                    .filter(|field| {
                        patch.contains_key(field)
                            && !same_field(before.get(field), after.get(field))
                    })
                    .collect();
                if changed.is_empty() {
                    return Ok(None);
                }
                let items = taken(view)?;
                if holding(&items) {
                    let mut details = Map::new();
                    // A move of a link hold's relation names the link, as
                    // the engine's Links move did.
                    if let [only] = changed.as_slice() {
                        if let Some(link) = config.link_of(only) {
                            details.insert("fields".into(), json!([link]));
                            return Ok(Some(veto(
                                format!(
                                    "link {link} names what the claim holds; it does not move while the claim holds"
                                ),
                                HELD_FIELDS,
                                Some(details),
                            )));
                        }
                    }
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
                let (status, _) = status_of(view);
                if !config.takes_in(status.as_deref()) {
                    return Ok(None);
                }
                check(view, &merge(&items, &plan(config, after)))
            }
            GuardRequest::Delete => {
                // Committed time to come is the claim's booking, which no
                // move gives back: it waits until the time has passed.
                // Committed units are sold, and stay so after the delete.
                let now = view.now();
                let stuck: Vec<HolderItem> = taken(view)?
                    .into_iter()
                    .filter(|item| {
                        item.state == ItemState::Committed
                            && item
                                .ask
                                .interval()
                                .is_some_and(|interval| interval.end_ms > now)
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
                        "the claim holds {} committed {} of time that has not ended",
                        stuck.len(),
                        if stuck.len() == 1 { "item" } else { "items" }
                    ),
                    COMMITTED,
                    Some(details),
                )))
            }
            _ => Ok(None),
        }
    }

    fn operation(&self, cx: &Cx<'_, HoldsConfig>, name: &str, _params: &Value) -> Hook<Value> {
        match name {
            RETAKE_HOLDS => retake_holds(cx),
            LIST_HOLDS => list_holds(cx),
            _ => Err(cx.defect(format!("it declares no operation {name}"))),
        }
    }

    // Takes, commits, releases and returns as the status the change left
    // asks, then sets the state and the total.
    fn after_change(&self, cx: &Cx<'_, HoldsConfig>, change: &Change<'_>) -> Hook<()> {
        if matches!(change, Change::Delete) {
            return let_go(cx);
        }
        let config = cx.config();
        let (status, entered_at) = status_of(cx);
        let taken = stored(cx)?;
        let mut items: Vec<HolderItem> = taken.iter().map(|stored| stored.item.clone()).collect();
        let own = cx.own();
        // The status is in a takeOn state no taken item was taken at: an
        // entry this change made, whoever made it.
        let entered = config.takes_in(status.as_deref())
            && !taken
                .iter()
                .any(|stored| stored.taken_at.is_some() && stored.taken_at == entered_at);
        // An update that changed what the items ask, which the guard
        // checked.
        let asks_moved = match change {
            Change::Update { patch, before } => {
                config.takes_in(status.as_deref())
                    && !holding(&items)
                    && config.reads().iter().any(|field| {
                        patch.contains_key(field) && !same_field(before.get(field), own.get(field))
                    })
            }
            _ => false,
        };
        let creates = matches!(change, Change::Create) && config.take_on.is_none();
        if creates || entered || asks_moved {
            let planned = plan(config, &own);
            if merge(&items, &planned)
                .iter()
                .any(|item| item.state == ItemState::Pending)
            {
                items = take(cx, merge(&items, &planned), entered_at)?;
            }
        }
        settle(cx, &mut items, &taken, status.as_deref())?;
        write_summary(cx, &merge(&items, &plan(config, &own)))
    }

    fn read_fields(
        &self,
        config: &HoldsConfig,
        row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let stored: Vec<HolderItem> = stored_of(supply::table_of(tables, ITEMS))
            .into_iter()
            .map(|stored| stored.item)
            .collect();
        let items = merge(&stored, &plan(config, &row.own));
        let mut fields = Map::new();
        fields.insert(
            "items".into(),
            Value::Array(items.iter().map(HolderItem::to_json).collect()),
        );
        fields
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn item(hold: &str, index: i64, state: ItemState, ask: Ask) -> HolderItem {
        HolderItem {
            hold: hold.into(),
            item: index,
            supply: RowRef::new("Inventory", "anvil"),
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
    fn a_total_sums_the_priced_items_in_one_currency() {
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
        assert_eq!(total_of(&[]), json!({"amount": 0, "currency": null}));
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
            total_of(&[priced(i64::MAX, "GBP", Ask::Quantity(2))]),
            Value::Null
        );
    }

    #[test]
    fn two_pending_items_of_one_supply_are_checked_together() {
        use ItemState::*;
        let items = [
            item("lines", 0, Pending, Ask::Quantity(3)),
            item("lines", 1, Pending, Ask::Quantity(3)),
            item("lines", 2, Held, Ask::Quantity(1)),
        ];
        assert_eq!(
            alongside(&items),
            [
                vec![Ask::Quantity(3)],
                vec![Ask::Quantity(3)],
                vec![Ask::Quantity(3), Ask::Quantity(3)],
            ]
        );
    }

    #[test]
    fn a_merge_keeps_what_moved_past_pending() {
        use ItemState::*;
        let stored = [
            item("lines", 0, Held, Ask::Quantity(2)),
            item("lines", 1, Pending, Ask::Quantity(1)),
        ];
        let planned = [
            item("lines", 0, Pending, Ask::Quantity(5)),
            item("lines", 1, Pending, Ask::Quantity(4)),
            item("lines", 10, Pending, Ask::Quantity(4)),
            item("lines", 2, Pending, Ask::Quantity(4)),
        ];
        let merged = merge(&stored, &planned);
        assert_eq!(
            merged
                .iter()
                .map(|item| (item.item, item.state, item.ask))
                .collect::<Vec<_>>(),
            [
                (0, Held, Ask::Quantity(2)),
                (1, Pending, Ask::Quantity(4)),
                (2, Pending, Ask::Quantity(4)),
                (10, Pending, Ask::Quantity(4))
            ]
        );
    }
}
