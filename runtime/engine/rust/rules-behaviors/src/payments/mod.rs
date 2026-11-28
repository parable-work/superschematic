//! Payments, what a row owes and how it is settled (D70, with its
//! amendment), on a table (D103's step 4), without the core ever calling a
//! payment provider. The row is a checkout, an order, a booking's deposit,
//! an invoice, or, with `refundOf`, a refund of one of those.
//!
//! Payments passes D103's test. State of its own: each attempt, the actions
//! waiting for the provider's adapter, each refund and, on a refund row,
//! each refund it asked, in sub-tables of the row; the provider events it
//! recorded, in a table kept for the table as a whole; and what is due,
//! authorized, captured, refunded, outstanding and overpaid, the currency
//! and the state, its owned columns. Rules over how it changes: an
//! attempt's status that only moves forward, an event that applies once,
//! an amount locked while an attempt is in flight, a Workflow move held
//! until what is due is authorized or captured, a capture after the row let
//! go of what it paid for refunded or kept, money paid twice asked back,
//! and one refund per refund row's approval. Recurrence: every application
//! that takes money takes it this way.
//!
//! What is owed. The amount due is an own integer field (`amount:
//! {field}`) in the config's currency, a literal ISO 4217 code
//! ([`crate::currency`]) or an own `Finance.Currency` field, or the claim's
//! Holds total (`amount: {holds: true}`), `{ amount, currency }`, which
//! carries its own, read from Holds' items as the run has them. Amounts
//! are whole numbers of the currency's minor units, and amounts of two
//! currencies are never added: every amount a provider reports must be in
//! the row's currency (`currency_mismatch`).
//!
//! What is owed from holds fails closed. While an item of the claim is
//! pending its price is not known, and a null total over priced items (two
//! currencies, or a sum past what an integer holds) is not 0: `due` reads
//! 0, and `openPayment` and a `require` move refuse with `total_unknown`.
//! A claim whose held items carry no price owes 0, so a free booking moves
//! where `require` waits for money.
//!
//! What is recorded ([`rows`]): an attempt per payment opened, with its
//! provider, the provider's reference once the adapter reports it, its
//! amount and currency, what was due when it opened, its status, what is
//! authorized and captured of it, and a card's brand and last four digits
//! for display; an action per thing the provider's adapter is to do
//! (`create`, `capture`, `void`, `refund`), whose id the adapter sends the
//! provider as its idempotency key, waiting until an event for it is
//! recorded; a refund per refund asked; on a refund row, each refund it
//! asked of the row it refunds (an issue); and every provider event
//! recorded, by provider and event id, with the normalised fields alone. A
//! provider's raw event is never stored, and no parameter takes card data:
//! every params schema is closed. An attempt's, an action's and a refund's
//! id is `pay_`, `act_` or `rf_` and a key the command makes
//! ([`superschematic_rules::Cx::new_key`]), unique per command and the same
//! for every run of it.
//!
//! The adapter. Code outside the core reads the actions waiting for its
//! provider through `listPaymentActions`, an operation of the table as a
//! whole over a read across the table, calls the provider, and reports
//! every outcome and every provider event through `recordPaymentEvent`,
//! which needs the config's `record.permission`. An event applies once per
//! table and provider (the engine's applied once per namespace: on a table
//! the events are the table's, kept for the table as a whole): a repeat
//! changes and writes nothing, and the same id with other content, or on
//! another row, is `key_reused`. An attempt's status only moves forward,
//! by the rank `open < requiresAction < processing < failed < authorized <
//! cancelled < captured`, so a late event is recorded and changes nothing,
//! a payment that succeeds after a failed try is caught, and money a
//! provider reports captured is counted whatever came before it.
//!
//! Paying twice. `openPayment` gives each earlier failed attempt a `void`
//! action as it opens a new one, and a capture that pays what is due voids
//! the other attempts still open. A capture that arrives anyway is counted:
//! what it brings past what is due is `Payments.overpaid`, asked back with
//! a refund keyed `overpaid:<eventId>`, which leaves the state `paid`.
//!
//! The Workflow. `require` holds a move into a state until every amount
//! due is authorized or captured (`unauthorized`) or captured (`unpaid`).
//! No move leaves a state while an attempt is `processing`
//! (`payment_in_progress`), so a hold's expiry fired by Timers does not let
//! go of what is being paid for. The guard reads what the move asks, and
//! the after-change hook reads the status from the row as the change left
//! it, never from which operation ran, so a Timers fire and a Reactions
//! move are caught as a caller's transition is: in a `voidOn` state every
//! attempt a provider can still void gets one `void` action; with
//! `capture: manual`, in a `captureOn` state every authorized attempt gets
//! one `capture` action; on a refund row, in an `issueOn` state, the row
//! asks the one its `refundOf` relation points at for its amount
//! (`refundFor`, a call on that row), once per issue.
//!
//! The refund pair. A refund row's Payments calls `refundFor` on the row
//! it refunds, which takes it only from a row whose `refundOf` relation
//! points here, in an `issueOn` state, for its own amount and currency
//! (`not_linked`). One approval pays one refund: while a refund the row
//! asked is pending or paid, refundFor returns it under its key and
//! refuses any other (`key_reused`), and after one failed it takes only
//! the key the refund row derives from itself and the failures before
//! (`<type>:<key>.<n>`). The adapter reports a refund's outcome on the row
//! refunded, which calls `settleRefundFor` on the refund row as the
//! adapter; `settleRefundFor` needs the refund row's `record.permission`
//! and refuses a status or an amount other than the one the refunded row
//! recorded (`not_linked`), read from that row's refunds as the run has
//! them. The build holds the two tables' `record.permission` alike.
//!
//! configChange: `require`, `voidOn`, `captureOn`, `capture`,
//! `afterRelease`, `issueOn` and the permissions may change. The currency,
//! the source of the amount and `refundOf` stay while the type has rows.
//! Payments joins a type that has rows and leaves only one that has none.

mod operations;
pub mod rows;

use std::collections::BTreeMap;
use std::sync::OnceLock;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook,
    Row, SchemaIssue, SubRow, UseSpec, Veto, View,
};

use crate::ajv;
use crate::currency;
use crate::declared;
use crate::holds;
use crate::supply::{self, integer, RowRef};
use crate::workflow;

use rows::{
    attempts_of, issues_of, refunds_of, Attempt, AttemptStatus, PaymentsState, Refund,
    RefundStatus, ATTEMPTS, ISSUES, REFUNDS,
};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Payments";

/// Payments' declaration, read in place: the copy `make behaviors` writes of
/// the one the compiler registers.
pub const DECLARATION: &str = include_str!("../../declarations/Payments.behavior.json");

/// How many attempts `Payments.attempts` shows, newest first.
pub const ATTEMPTS_SHOWN: usize = 20;

/// The largest integer a JavaScript number holds exactly.
const MAX_SAFE_INTEGER: i64 = supply::MAX_SAFE_INTEGER;

/// The operations, by their declared names.
pub const OPEN_PAYMENT: &str = "openPayment";
pub const RECORD_PAYMENT_EVENT: &str = "recordPaymentEvent";
pub const CAPTURE_PAYMENT: &str = "capturePayment";
pub const VOID_PAYMENT: &str = "voidPayment";
pub const REFUND_PAYMENT: &str = "refundPayment";
pub const LIST_PAYMENT_ACTIONS: &str = "listPaymentActions";
pub const REFUND_FOR: &str = "refundFor";
pub const SETTLE_REFUND_FOR: &str = "settleRefundFor";

/// The veto codes the declaration lists.
pub mod codes {
    pub const NOTHING_DUE: &str = "nothing_due";
    pub const AMOUNT_LOCKED: &str = "amount_locked";
    pub const CURRENCY_MISMATCH: &str = "currency_mismatch";
    pub const OVER_CAPTURE: &str = "over_capture";
    pub const EXCEEDS_REFUNDABLE: &str = "exceeds_refundable";
    pub const UNPAID: &str = "unpaid";
    pub const UNAUTHORIZED: &str = "unauthorized";
    pub const PAYMENT_IN_PROGRESS: &str = "payment_in_progress";
    pub const NO_ATTEMPT: &str = "no_attempt";
    pub const NOT_LINKED: &str = "not_linked";
    pub const KEY_REUSED: &str = "key_reused";
    pub const ATTEMPT_CLOSED: &str = "attempt_closed";
    pub const NOT_CONFIGURED: &str = "not_configured";
    pub const TOTAL_UNKNOWN: &str = "total_unknown";
}

use codes::*;

/// The owned columns the summary sets.
const SUMMARY_FIELDS: [&str; 8] = [
    "due",
    "authorized",
    "captured",
    "refunded",
    "outstanding",
    "overpaid",
    "currency",
    "state",
];

fn file() -> &'static Value {
    static FILE: OnceLock<Value> = OnceLock::new();
    FILE.get_or_init(|| ajv::file(DECLARATION))
}

/// Where the amount due comes from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum AmountSource {
    /// An own integer field.
    Field(String),
    /// The claim's Holds total, which carries its currency.
    Holds,
}

/// The currency of an amount from a field.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CurrencySource {
    /// An ISO 4217 code.
    Code(String),
    /// An own `Finance.Currency` field.
    Field(String),
}

/// What a move into a state waits for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Requirement {
    /// Every amount due authorized or captured.
    Authorized,
    /// Every amount due captured.
    Paid,
}

impl Requirement {
    pub fn as_str(self) -> &'static str {
        match self {
            Requirement::Authorized => "authorized",
            Requirement::Paid => "paid",
        }
    }
}

/// Payments' config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PaymentsConfig {
    pub amount: AmountSource,
    /// `None` only with `amount: {holds: true}`, whose total carries its
    /// own; given there, the total's currency must be it.
    pub currency: Option<CurrencySource>,
    /// Whether `captureOn` states capture what is authorized.
    pub manual_capture: bool,
    pub capture_on: Vec<String>,
    /// By state, in the config's order.
    pub require: Vec<(String, Requirement)>,
    pub void_on: Vec<String>,
    /// Whether money captured in a `voidOn` state is refunded.
    pub refund_after_release: bool,
    /// What `refundPayment` needs; `None` refuses it.
    pub refund_permission: Option<String>,
    /// What `recordPaymentEvent`, `settleRefundFor` and
    /// `listPaymentActions` need: the adapter's.
    pub record_permission: String,
    /// The to-one relation to the row this one refunds.
    pub refund_of: Option<String>,
    /// The own field that relation's key is held in.
    pub refund_of_field: Option<String>,
    pub issue_on: Vec<String>,
    /// The Workflow use whose status the config's states name.
    pub workflow: Option<String>,
    /// The Holds use whose total `amount: {holds: true}` reads.
    pub holds: Option<String>,
}

impl PaymentsConfig {
    /// What a move into a state waits for; `None` for nothing.
    pub fn requirement(&self, state: &str) -> Option<Requirement> {
        self.require
            .iter()
            .find(|(known, _)| known == state)
            .map(|(_, requirement)| *requirement)
    }

    /// Whether the row is a refund of another.
    pub fn is_refund(&self) -> bool {
        self.refund_of.is_some()
    }

    /// The own field the amount reads, when it reads one.
    pub fn amount_field(&self) -> Option<&str> {
        match &self.amount {
            AmountSource::Field(field) => Some(field),
            AmountSource::Holds => None,
        }
    }

    /// The own field the currency reads, when it reads one.
    pub fn currency_field(&self) -> Option<&str> {
        match &self.currency {
            Some(CurrencySource::Field(field)) => Some(field),
            _ => None,
        }
    }

    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then its rules against the type's fields, Workflow, Holds and
    /// relations and a literal currency against ISO 4217, each a reason
    /// the core prefixes with the type and the behavior. What another
    /// table holds (refundOf's target implements Payments, with the same
    /// record.permission) is the build's to check, since a type's define
    /// check sees its own spec alone.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<PaymentsConfig, ConfigError> {
        let issues = ajv::config_issues(file(), raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues));
        }
        let uses = target.uses();
        let position = |behavior: &str| uses.iter().position(|entry| entry.behavior == behavior);
        let amount = match raw.get("amount") {
            Some(amount) if amount.get("holds") == Some(&Value::Bool(true)) => AmountSource::Holds,
            Some(amount) => AmountSource::Field(
                amount
                    .get("field")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
            ),
            None => return Err("amount names what the instance owes".into()),
        };
        let mut holds = None;
        match &amount {
            AmountSource::Field(field) => crate::stock::own_field(
                target,
                "amount.field",
                field,
                &crate::stock::INTEGER_TYPES,
                "an integer",
            )?,
            AmountSource::Holds => match (position(holds::NAME), Some(target.index())) {
                (None, _) => {
                    return Err(
                        "amount.holds reads Holds.total, and the type does not list Holds".into(),
                    )
                }
                // Holds' after-change hook writes the total Payments' reads.
                (Some(at), Some(mine)) if at > mine => {
                    return Err(
                        "amount.holds reads Holds.total, so the type lists Payments after Holds"
                            .into(),
                    )
                }
                (Some(at), _) => holds = Some(uses[at].id().to_owned()),
            },
        }
        let currency = match raw.get("currency") {
            None => None,
            Some(Value::String(code)) => {
                if let Some(why) = currency::refusal("currency", code) {
                    return Err(why.into());
                }
                Some(CurrencySource::Code(code.clone()))
            }
            Some(given) => {
                let field = given
                    .get("field")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                crate::stock::own_field(
                    target,
                    "currency.field",
                    field,
                    &crate::stock::CURRENCY_TYPES,
                    "a string",
                )?;
                Some(CurrencySource::Field(field.to_owned()))
            }
        };
        if matches!(amount, AmountSource::Field(_)) && currency.is_none() {
            return Err("an amount from a field names its currency: a code or { field }".into());
        }
        let manual_capture = raw.get("capture").and_then(Value::as_str) == Some("manual");
        let capture_on = strings(raw.get("captureOn"));
        if !capture_on.is_empty() && !manual_capture {
            return Err("captureOn captures with capture manual".into());
        }
        let mut require = Vec::new();
        if let Some(given) = raw.get("require").and_then(Value::as_object) {
            for (state, wanted) in given {
                let requirement = match wanted.as_str() {
                    Some("paid") => Requirement::Paid,
                    _ => Requirement::Authorized,
                };
                require.push((state.to_owned(), requirement));
            }
        }
        let void_on = strings(raw.get("voidOn"));
        let issue_on = strings(raw.get("issueOn"));
        let named = !(require.is_empty()
            && void_on.is_empty()
            && capture_on.is_empty()
            && issue_on.is_empty());
        let workflow = workflow_use(uses, named)?;
        let flow = workflow
            .as_ref()
            .and_then(|id| uses.iter().find(|entry| entry.id() == id))
            .map(UseSpec::config_or_empty);
        let states: Option<Vec<String>> = flow.as_ref().and_then(|flow| {
            flow.get("states")?.as_array().map(|states| {
                states
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect()
            })
        });
        if let Some(states) = &states {
            let lists: [(&str, Vec<&String>); 4] = [
                ("require", require.iter().map(|(state, _)| state).collect()),
                ("voidOn", void_on.iter().collect()),
                ("captureOn", capture_on.iter().collect()),
                ("issueOn", issue_on.iter().collect()),
            ];
            for (at, names) in lists {
                for name in names {
                    if !states.contains(name) {
                        return Err(format!(
                            "{at} names \"{name}\", which is not a state of the type's Workflow ({})",
                            states.join(", ")
                        )
                        .into());
                    }
                }
            }
            let initial = flow
                .as_ref()
                .and_then(|flow| flow.get("initial"))
                .and_then(Value::as_str)
                .or_else(|| states.first().map(String::as_str));
            if let Some((state, _)) = require
                .iter()
                .find(|(state, _)| Some(state.as_str()) == initial)
            {
                return Err(format!(
                    "require names {state}, the Workflow's initial state, which a create enters before anything is paid"
                )
                .into());
            }
        }
        let refund_of = raw
            .get("refundOf")
            .and_then(|of| of.get("link"))
            .and_then(Value::as_str)
            .map(str::to_owned);
        let mut refund_of_field = None;
        if let Some(link) = &refund_of {
            if matches!(amount, AmountSource::Holds) {
                return Err(
                    "a refund's amount is an own field: amount { field } with refundOf".into(),
                );
            }
            refund_of_field = Some(relation_field(target, link)?);
            for key in ["refunds", "require", "voidOn", "capture", "afterRelease"] {
                if raw.get(key).is_some() {
                    return Err(format!(
                        "{key} is for an instance that takes payments, and a refund instance (refundOf) takes none"
                    )
                    .into());
                }
            }
        }
        if refund_of.is_none() && !issue_on.is_empty() {
            return Err("issueOn issues a refund with refundOf".into());
        }
        if refund_of.is_some() && issue_on.is_empty() {
            return Err("refundOf issues the refund in its issueOn states".into());
        }
        let text_at = |outer: &str, inner: &str| {
            raw.get(outer)
                .and_then(|value| value.get(inner))
                .and_then(Value::as_str)
                .map(str::to_owned)
        };
        Ok(PaymentsConfig {
            amount,
            currency,
            manual_capture,
            capture_on,
            require,
            void_on,
            refund_after_release: raw.get("afterRelease").and_then(Value::as_str) == Some("refund"),
            refund_permission: text_at("refunds", "permission"),
            record_permission: text_at("record", "permission").unwrap_or_default(),
            refund_of,
            refund_of_field,
            issue_on,
            workflow,
            holds,
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

// workflow_use is the id of the type's Workflow use, which the config's
// states name: its one Workflow; a type with several takes the config
// only when it names no state. `named` says whether the config names any.
fn workflow_use(uses: &[UseSpec], named: bool) -> Result<Option<String>, ConfigError> {
    let workflows: Vec<&UseSpec> = uses
        .iter()
        .filter(|entry| entry.behavior == workflow::NAME)
        .collect();
    match workflows.as_slice() {
        [] if named => {
            Err("the config names states of Workflow, which the type does not list".into())
        }
        [] => Ok(None),
        [one] => Ok(Some(one.id().to_owned())),
        several if named => Err(format!(
            "the type has several Workflows ({}), and Payments reads the states of one",
            several
                .iter()
                .map(|entry| entry.id())
                .collect::<Vec<_>>()
                .join(", ")
        )
        .into()),
        _ => Ok(None),
    }
}

// relation_field is the own field holding the key of a to-one relation of
// the type by the relation's name, which refundOf names on a table.
fn relation_field(target: &ConfigTarget<'_>, link: &str) -> Result<String, ConfigError> {
    let relations: Vec<(&str, &str)> = target
        .fields()
        .iter()
        .filter_map(|field| {
            field
                .relation
                .as_ref()
                .map(|relation| (relation.name.as_str(), field.name.as_str()))
        })
        .collect();
    if let Some((_, field)) = relations.iter().find(|(name, _)| *name == link) {
        return Ok((*field).to_owned());
    }
    let listed = if relations.is_empty() {
        "it has none".to_owned()
    } else {
        format!(
            "its to-one relations: {}",
            relations
                .iter()
                .map(|(name, _)| *name)
                .collect::<Vec<_>>()
                .join(", ")
        )
    };
    Err(format!(
        "refundOf.link {link} is not a to-one relation of {} ({listed}), so a refund has no one row to refund",
        target.type_name()
    )
    .into())
}

// ---------------------------------------------------------------------------
// What a row keeps, and what is owed
// ---------------------------------------------------------------------------

/// What `Payments`' fields read of a row.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Summary {
    pub due: i64,
    pub authorized: i64,
    pub captured: i64,
    pub refunded: i64,
    pub outstanding: i64,
    /// What captures brought past what was due, which Payments asked back
    /// with refunds of it (`overpaid:<eventId>`).
    pub overpaid: i64,
    pub currency: Option<String>,
    pub state: PaymentsState,
}

impl Summary {
    fn values(&self) -> [(&'static str, Value); 8] {
        [
            ("due", Value::from(self.due)),
            ("authorized", Value::from(self.authorized)),
            ("captured", Value::from(self.captured)),
            ("refunded", Value::from(self.refunded)),
            ("outstanding", Value::from(self.outstanding)),
            ("overpaid", Value::from(self.overpaid)),
            (
                "currency",
                self.currency.as_deref().map_or(Value::Null, Value::from),
            ),
            ("state", Value::from(self.state.as_str())),
        ]
    }
}

/// What the config's source says is due now, and in which currency.
pub(crate) struct Source {
    pub due: i64,
    pub currency: Option<String>,
    /// Why what is due cannot be known, when it cannot: an item of the
    /// claim is pending, or the Holds total is null over priced items.
    pub unknown: Option<String>,
}

/// The row's attempts, oldest first, as the run has them.
pub(crate) fn attempts(view: &View<'_, PaymentsConfig>) -> Hook<Vec<Attempt>> {
    Ok(attempts_of(&view.sub_rows(ATTEMPTS)?))
}

/// The row's refunds, oldest first, as the run has them.
pub(crate) fn refunds(view: &View<'_, PaymentsConfig>) -> Hook<Vec<Refund>> {
    Ok(refunds_of(&view.sub_rows(REFUNDS)?))
}

/// What a claim's Holds says it owes.
enum HoldsOwed {
    /// The total's amount, and its currency when it names one.
    Total(i64, Option<String>),
    /// Nothing can be read: why.
    Unknown(String),
}

// holds_owed reads what the claim's Holds says it owes, as the run has its
// items, failing closed: an item still pending has no price yet; the
// total is what is owed when it is there, nothing when nothing is priced;
// a null total over priced items is unknown.
fn holds_owed(view: &View<'_, PaymentsConfig>, own: &Map<String, Value>) -> Hook<HoldsOwed> {
    let Some(use_id) = view.config().holds.clone() else {
        return Ok(HoldsOwed::Total(0, None));
    };
    let spec = view
        .read_spec(view.type_name())?
        .ok_or_else(|| view.defect("its type has no spec"))?;
    let Some(index) = spec.uses.iter().position(|entry| entry.id() == use_id) else {
        return Err(view.defect(format!("its type has no Holds use {use_id}")));
    };
    let stored = match view.key() {
        Some(key) => view.sub_rows_of(view.type_name(), key, &use_id, holds::ITEMS)?,
        None => Vec::new(),
    };
    let row = Row {
        own: own.clone(),
        ..Row::default()
    };
    let items = holds::holder_items(spec, index, &row, &stored).map_err(|why| view.defect(why))?;
    let pending = items
        .iter()
        .filter(|item| item.state == supply::ItemState::Pending)
        .count();
    if pending > 0 {
        return Ok(HoldsOwed::Unknown(format!(
            "{pending} of its {} items are pending, whose prices come when they are held",
            items.len()
        )));
    }
    let total = holds::total_of(&items);
    if !total.is_null() {
        let amount = total.get("amount").and_then(integer).unwrap_or(0);
        let currency = total.get("currency").and_then(Value::as_str);
        return Ok(HoldsOwed::Total(amount, currency.map(str::to_owned)));
    }
    let priced: Vec<&supply::HolderItem> =
        items.iter().filter(|item| item.price.is_some()).collect();
    let mut currencies: Vec<&str> = priced
        .iter()
        .filter_map(|item| item.price.as_ref()?.currency.as_deref())
        .collect();
    currencies.sort_unstable();
    currencies.dedup();
    let unnamed = priced.iter().any(|item| {
        item.price
            .as_ref()
            .is_some_and(|price| price.currency.is_none())
    });
    if unnamed {
        currencies.push("a price with no currency");
    }
    Ok(HoldsOwed::Unknown(if currencies.len() > 1 || unnamed {
        format!(
            "Holds.total is null: its priced items are in {}, which are never added",
            currencies.join(" and ")
        )
    } else {
        format!(
            "Holds.total is null over {} priced items: their sum is past what an integer holds",
            priced.len()
        )
    }))
}

// source_with reads what the config's source says is due, with the own
// fields given (the row's, or an update's after).
pub(crate) fn source_with(
    view: &View<'_, PaymentsConfig>,
    own: &Map<String, Value>,
) -> Hook<Source> {
    let config = view.config();
    let named = match &config.currency {
        Some(CurrencySource::Code(code)) => Some(code.clone()),
        Some(CurrencySource::Field(field)) => {
            own.get(field).and_then(Value::as_str).map(str::to_owned)
        }
        None => None,
    };
    Ok(match &config.amount {
        AmountSource::Field(field) => Source {
            due: own.get(field).and_then(integer).unwrap_or(0).max(0),
            currency: named,
            unknown: None,
        },
        AmountSource::Holds => match holds_owed(view, own)? {
            HoldsOwed::Total(amount, currency) => Source {
                due: amount.max(0),
                currency: currency.or(named),
                unknown: None,
            },
            HoldsOwed::Unknown(why) => Source {
                due: 0,
                currency: named,
                unknown: Some(why),
            },
        },
    })
}

pub(crate) fn source(view: &View<'_, PaymentsConfig>) -> Hook<Source> {
    source_with(view, &view.own())
}

/// The refusal of a payment, or of a move a payment gates, while what is
/// due cannot be read from the claim's holds.
pub(crate) fn total_unknown(view: &View<'_, PaymentsConfig>, why: &str, more: Value) -> Veto {
    veto(
        format!(
            "{} {} owes what its holds total, and that is not known: {why}",
            view.type_name(),
            view.key().unwrap_or_default()
        ),
        TOTAL_UNKNOWN,
        Some(more),
    )
}

/// The newest attempt in flight; `None` when none is.
pub(crate) fn in_flight(attempts: &[Attempt]) -> Option<&Attempt> {
    attempts
        .iter()
        .rev()
        .find(|attempt| attempt.status.in_flight())
}

/// The prefix of the key of a refund Payments asks of money captured past
/// what is due.
pub(crate) const OVERPAID: &str = "overpaid";

/// The prefix of the key of a refund Payments asks of money captured after
/// the row let go (`afterRelease: refund`).
pub(crate) const RELEASE: &str = "release";

// is_overpaid is whether a refund is one Payments asked of money captured
// past what was due.
fn is_overpaid(refund: &Refund) -> bool {
    refund.origin.is_none()
        && refund
            .idempotency_key
            .strip_prefix(OVERPAID)
            .is_some_and(|rest| rest.starts_with(':'))
}

/// What the row's fields read, from its tables as the run has them and
/// the own fields given.
pub(crate) fn summary_with(
    view: &View<'_, PaymentsConfig>,
    own: &Map<String, Value>,
) -> Hook<Summary> {
    if view.config().is_refund() {
        return refund_summary(view, own);
    }
    let attempts = attempts(view)?;
    let refunds = refunds(view)?;
    let live = source_with(view, own)?;
    let due = in_flight(&attempts).map_or(live.due, |attempt| attempt.due);
    let currency = attempts
        .last()
        .map(|attempt| attempt.currency.clone())
        .or(live.currency);
    let authorized: i64 = attempts.iter().map(Attempt::holding).sum();
    let captured: i64 = attempts.iter().map(|attempt| attempt.captured).sum();
    let refunded: i64 = refunds
        .iter()
        .filter(|refund| refund.status == RefundStatus::Succeeded)
        .map(|refund| refund.refunded)
        .sum();
    // Money captured past what was due, and its refunds, leave the state
    // alone: an order paid twice and refunded the second time is paid.
    let excess: Vec<&Refund> = refunds
        .iter()
        .filter(|refund| is_overpaid(refund))
        .collect();
    let overpaid: i64 = excess.iter().map(|refund| refund.amount).sum();
    let returned: i64 = excess
        .iter()
        .filter(|refund| refund.status == RefundStatus::Succeeded)
        .map(|refund| refund.refunded)
        .sum();
    let (kept, given_back) = (captured - returned, refunded - returned);
    let pending = attempts.iter().any(|attempt| attempt.status.in_flight());
    let state = if given_back > 0 {
        if given_back >= kept {
            PaymentsState::Refunded
        } else {
            PaymentsState::PartiallyRefunded
        }
    } else if kept > 0 && kept >= due {
        PaymentsState::Paid
    } else if authorized > 0 && captured + authorized >= due {
        PaymentsState::Authorized
    } else if pending || captured > 0 {
        PaymentsState::Pending
    } else {
        PaymentsState::Unpaid
    };
    Ok(Summary {
        due,
        authorized,
        captured,
        refunded,
        outstanding: (due - captured).max(0),
        overpaid,
        currency,
        state,
    })
}

pub(crate) fn summary(view: &View<'_, PaymentsConfig>) -> Hook<Summary> {
    summary_with(view, &view.own())
}

// refund_summary reads a refund row: what it refunds, and how far its
// latest issue has come: pending while asked, refunded once the provider
// refunded it, unpaid before it is asked or after it failed.
fn refund_summary(view: &View<'_, PaymentsConfig>, own: &Map<String, Value>) -> Hook<Summary> {
    let live = source_with(view, own)?;
    let latest = issues_of(&view.sub_rows(ISSUES)?).pop();
    let (state, refunded) = match &latest {
        Some(issue) if issue.status == RefundStatus::Succeeded => {
            (PaymentsState::Refunded, issue.refunded)
        }
        Some(issue) if issue.status.pending() => (PaymentsState::Pending, 0),
        _ => (PaymentsState::Unpaid, 0),
    };
    let due = latest
        .as_ref()
        .filter(|issue| issue.status != RefundStatus::Failed)
        .map_or(live.due, |issue| issue.amount);
    Ok(Summary {
        due,
        authorized: 0,
        captured: 0,
        refunded,
        outstanding: due,
        overpaid: 0,
        currency: latest.map(|issue| issue.currency).or(live.currency),
        state,
    })
}

/// The row's Workflow status, as the run has it; `None` without one.
pub(crate) fn status_of(view: &View<'_, PaymentsConfig>) -> Option<String> {
    let use_id = view.config().workflow.as_deref()?;
    view.get_of(use_id, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// write_summary sets each owned column the summary moved.
pub(crate) fn write_summary(cx: &Cx<'_, PaymentsConfig>) -> Hook<()> {
    let now = summary(cx)?;
    for (field, value) in now.values() {
        let held = cx.get(field).unwrap_or(Value::Null);
        if !crate::values::equal(Some(&held), Some(&value)) {
            cx.set(field, value)?;
        }
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

pub(crate) fn veto(reason: String, code: &str, more: Option<Value>) -> Veto {
    let veto = Veto::new(reason).with_code(code);
    match more {
        Some(Value::Object(details)) => veto.with_details(details),
        _ => veto,
    }
}

// ---------------------------------------------------------------------------
// What follows a change
// ---------------------------------------------------------------------------

// after_change follows the status as the change left it, then holds the
// amount from holds to an attempt in flight and sets the summary.
fn after_change(cx: &Cx<'_, PaymentsConfig>, change: &Change<'_>) -> Hook<()> {
    if matches!(change, Change::Delete) {
        // The row's tables go with it; the events stay, kept for the
        // table, so a provider's redelivery is still a repeat.
        return Ok(());
    }
    let config = cx.config();
    if let Some(status) = status_of(cx) {
        if config.is_refund() && config.issue_on.contains(&status) {
            // What the row it refunds reads of this one is the summary.
            write_summary(cx)?;
            operations::issue(cx)?;
        }
        if config.void_on.contains(&status) {
            operations::void_open(cx)?;
        }
        if config.manual_capture && config.capture_on.contains(&status) {
            operations::capture_authorized(cx)?;
        }
    }
    if config.amount == AmountSource::Holds {
        holds_locked(cx)?;
    }
    write_summary(cx)
}

// holds_locked refuses a change that moved what the holds total while an
// attempt is in flight: what was opened is what is paid.
fn holds_locked(cx: &Cx<'_, PaymentsConfig>) -> Hook<()> {
    let attempts = attempts(cx)?;
    let Some(flight) = in_flight(&attempts) else {
        return Ok(());
    };
    let live = source(cx)?;
    if live.due != flight.due
        || live
            .currency
            .as_deref()
            .is_some_and(|live| live != flight.currency)
    {
        return Err(cx.veto(veto(
            format!(
                "attempt {} is paying {} {} due, and Holds.total would make it {} {}",
                flight.attempt,
                flight.due,
                flight.currency,
                live.due,
                live.currency.as_deref().unwrap_or("")
            ),
            AMOUNT_LOCKED,
            Some(json!({"attempt": flight.attempt, "due": flight.due, "total": live.due})),
        )));
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// The guard
// ---------------------------------------------------------------------------

fn guard(view: &View<'_, PaymentsConfig>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
    let config = view.config();
    match request {
        GuardRequest::Operation {
            use_id,
            behavior,
            operation: "transition",
            params,
            ..
        } if *behavior == workflow::NAME && Some(*use_id) == config.workflow.as_deref() => {
            let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
            guard_move(view, to)
        }
        GuardRequest::Update { patch, after } => guard_update(view, patch, after),
        GuardRequest::Delete => {
            let flight = attempts(view)?
                .into_iter()
                .find(|attempt| attempt.status.in_flight());
            if let Some(attempt) = flight {
                return Ok(Some(veto(
                    format!(
                        "attempt {} is {}; void it, or let it finish, before the delete",
                        attempt.attempt,
                        attempt.status.as_str()
                    ),
                    PAYMENT_IN_PROGRESS,
                    Some(json!({"attempt": attempt.attempt, "status": attempt.status.as_str()})),
                )));
            }
            let pending = issues_of(&view.sub_rows(ISSUES)?)
                .into_iter()
                .find(|issue| issue.status.pending());
            Ok(pending.map(|issue| {
                veto(
                    format!("refund {} is waiting for its provider", issue.refund),
                    PAYMENT_IN_PROGRESS,
                    Some(json!({"refund": issue.refund})),
                )
            }))
        }
        _ => Ok(None),
    }
}

// guard_move holds a move of the Workflow: none while an attempt is
// processing, and none into a require state until it is met.
fn guard_move(view: &View<'_, PaymentsConfig>, to: &str) -> Hook<Option<Veto>> {
    let config = view.config();
    if config.is_refund() {
        return Ok(None);
    }
    let attempts = attempts(view)?;
    if let Some(attempt) = attempts
        .iter()
        .find(|attempt| attempt.status == AttemptStatus::Processing)
    {
        return Ok(Some(veto(
            format!(
                "attempt {} is processing at {}; {} {} does not move to {to} until it is settled",
                attempt.attempt,
                attempt.provider,
                view.type_name(),
                view.key().unwrap_or_default()
            ),
            PAYMENT_IN_PROGRESS,
            Some(json!({"attempt": attempt.attempt, "to": to})),
        )));
    }
    let Some(requirement) = config.requirement(to) else {
        return Ok(None);
    };
    if in_flight(&attempts).is_none() {
        if let Some(why) = source(view)?.unknown {
            return Ok(Some(total_unknown(
                view,
                &why,
                json!({"state": to, "requires": requirement.as_str()}),
            )));
        }
    }
    let now = summary(view)?;
    let (met, code, what) = match requirement {
        Requirement::Paid => (now.captured >= now.due, UNPAID, "captured"),
        Requirement::Authorized => (
            now.captured + now.authorized >= now.due,
            UNAUTHORIZED,
            "authorized or captured",
        ),
    };
    if met {
        return Ok(None);
    }
    Ok(Some(veto(
        format!(
            "{} {} moves to {to} once {} is {what}; {} is captured and {} authorized",
            view.type_name(),
            view.key().unwrap_or_default(),
            now.due,
            now.captured,
            now.authorized
        ),
        code,
        Some(json!({
            "state": to,
            "requires": requirement.as_str(),
            "due": now.due,
            "authorized": now.authorized,
            "captured": now.captured,
        })),
    )))
}

// guard_update keeps the amount while an attempt is in flight (or a refund
// has issued), the currency while any attempt exists, and a refund's
// refundOf relation once it has issued.
fn guard_update(
    view: &View<'_, PaymentsConfig>,
    patch: &Map<String, Value>,
    after: &Map<String, Value>,
) -> Hook<Option<Veto>> {
    let config = view.config();
    let before = view.own();
    let moved = |field: &str| {
        patch.contains_key(field) && !crate::values::equal(before.get(field), after.get(field))
    };
    let issued = || -> Hook<Option<rows::Issue>> {
        Ok(issues_of(&view.sub_rows(ISSUES)?)
            .into_iter()
            .rev()
            .find(|issue| issue.status != RefundStatus::Failed))
    };
    if let (Some(link), Some(field)) = (&config.refund_of, &config.refund_of_field) {
        if moved(field) {
            if let Some(issue) = issued()? {
                return Ok(Some(veto(
                    format!(
                        "{} {} asked {} {} for refund {}; its {link} link stays",
                        view.type_name(),
                        view.key().unwrap_or_default(),
                        issue.of.schema,
                        issue.of.id,
                        issue.refund
                    ),
                    NOT_LINKED,
                    Some(json!({"refund": issue.refund})),
                )));
            }
        }
    }
    let amount_moved = config.amount_field().is_some_and(moved);
    let currency_moved = config.currency_field().is_some_and(moved);
    if !amount_moved && !currency_moved {
        return Ok(None);
    }
    if config.is_refund() {
        return Ok(issued()?.map(|issue| {
            veto(
                format!(
                    "{} {} asked refund {} of {} {}; its amount and currency stay",
                    view.type_name(),
                    view.key().unwrap_or_default(),
                    issue.refund,
                    issue.amount,
                    issue.currency
                ),
                if amount_moved {
                    AMOUNT_LOCKED
                } else {
                    CURRENCY_MISMATCH
                },
                Some(json!({"refund": issue.refund})),
            )
        }));
    }
    let attempts = attempts(view)?;
    if amount_moved {
        if let Some(flight) = in_flight(&attempts) {
            return Ok(Some(veto(
                format!(
                    "attempt {} is paying {} due; {} does not change until it is captured, voided or failed",
                    flight.attempt,
                    flight.due,
                    config.amount_field().unwrap_or_default()
                ),
                AMOUNT_LOCKED,
                Some(json!({"attempt": flight.attempt, "due": flight.due})),
            )));
        }
    }
    if currency_moved {
        if let Some(last) = attempts.last() {
            let field = config.currency_field().unwrap_or_default();
            return Ok(Some(veto(
                format!(
                    "{} {} has attempts in {}; {field} stays",
                    view.type_name(),
                    view.key().unwrap_or_default(),
                    last.currency
                ),
                CURRENCY_MISMATCH,
                Some(json!({"currency": after.get(field), "expected": last.currency})),
            )));
        }
    }
    Ok(None)
}

// ---------------------------------------------------------------------------
// The behavior
// ---------------------------------------------------------------------------

/// Payments: what a row owes, and the attempts, actions and refunds that
/// settle it.
#[derive(Clone, Copy, Debug, Default)]
pub struct Payments;

impl Behavior for Payments {
    type Config = PaymentsConfig;

    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<PaymentsConfig, ConfigError> {
        PaymentsConfig::parse(raw, target)
    }

    // See the module's header.
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&PaymentsConfig>,
        after: Option<&PaymentsConfig>,
    ) -> Hook<Option<String>> {
        let (Some(before), Some(after)) = (before, after) else {
            if before.is_some() && cx.has_rows()? {
                return Ok(Some(
                    "the payments, refunds and actions its instances hold would stay behind"
                        .to_owned(),
                ));
            }
            return Ok(None);
        };
        if before.amount == after.amount
            && before.currency == after.currency
            && before.refund_of == after.refund_of
        {
            return Ok(None);
        }
        if !cx.has_rows()? {
            return Ok(None);
        }
        Ok(Some(
            "the currency, the source of the amount and refundOf stay while the schema has instances, whose payments were made in them"
                .to_owned(),
        ))
    }

    fn check_params(
        &self,
        _config: &PaymentsConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        ajv::params_issues(file(), operation, params)
    }

    // A create starts owing what its source says.
    fn initialize(&self, cx: &Cx<'_, PaymentsConfig>, _params: &Value) -> Hook<()> {
        write_summary(cx)
    }

    fn guard(
        &self,
        view: &View<'_, PaymentsConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        guard(view, request)
    }

    fn operation(&self, cx: &Cx<'_, PaymentsConfig>, name: &str, params: &Value) -> Hook<Value> {
        operations::run(cx, name, params)
    }

    fn after_change(&self, cx: &Cx<'_, PaymentsConfig>, change: &Change<'_>) -> Hook<()> {
        after_change(cx, change)
    }

    // The last twenty attempts, newest first.
    fn read_fields(
        &self,
        _config: &PaymentsConfig,
        _row: &Row,
        tables: &BTreeMap<String, Vec<SubRow>>,
    ) -> Map<String, Value> {
        let attempts = attempts_of(supply::table_of(tables, ATTEMPTS));
        let mut fields = Map::new();
        fields.insert(
            "attempts".into(),
            Value::Array(
                attempts
                    .iter()
                    .rev()
                    .take(ATTEMPTS_SHOWN)
                    .map(Attempt::to_json)
                    .collect(),
            ),
        );
        fields
    }
}

/// Whether an amount is one a JSON number holds exactly.
pub(crate) fn exact(amount: i64) -> bool {
    amount.abs() <= MAX_SAFE_INTEGER
}

/// The fields the summary sets, as the declaration names them.
pub fn summary_fields() -> [&'static str; 8] {
    SUMMARY_FIELDS
}

/// A row this one names, by its type and key.
pub(crate) fn row_ref(view: &View<'_, PaymentsConfig>) -> RowRef {
    RowRef::of(view)
}
