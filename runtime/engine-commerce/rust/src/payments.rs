//! `Payments` (D70): what an instance owes and how it is settled, without
//! the engine ever calling a payment provider. The instance is a checkout,
//! an order, a booking's deposit, an invoice, or, with `refundOf`, a
//! refund of one of those.
//!
//! What is owed. The amount due is an own integer field (`amount: {field}`)
//! in the config's currency, a literal ISO 4217 code or an own
//! `Finance.Currency` field, or the claim's `Holds.total` (`amount: {holds:
//! true}`), `{ amount, currency }`, which carries its own. Amounts are whole
//! numbers of the currency's minor units, and amounts of two currencies are
//! never added: a literal is checked against the active ISO 4217 list
//! ([`crate::currency`]) when the schema is defined, and every amount a
//! provider reports must be in the instance's currency
//! (`currency_mismatch`).
//!
//! What is recorded, all in Payments' own tables, the same SQL on SQLite
//! and Postgres:
//! - an attempt per payment opened (`openPayment`), with its provider, the
//!   provider's reference once the adapter reports it, its amount and
//!   currency, the amount due when it was opened, its status (`open`,
//!   `requiresAction`, `processing`, `authorized`, `captured`, `failed`,
//!   `cancelled`), what is authorized and captured of it, and a card's brand
//!   and last four digits for display;
//! - an action per thing the provider's adapter is to do (`create`,
//!   `capture`, `void`, `refund`), whose id the adapter sends the provider
//!   as its idempotency key, waiting until an event for it is recorded;
//! - a refund per refund asked (`requested`, `sent`, `succeeded`,
//!   `failed`), with the instance that asked it when it is a refund
//!   instance's;
//! - on a refund instance, each refund it asked of the instance it refunds
//!   (an issue);
//! - every provider event recorded, by provider and event id, with the
//!   normalised fields only. A provider's raw event, which holds the
//!   payer's name, email and address, is never stored, and no parameter
//!   takes card data: every params schema is closed, `providerRef` is
//!   limited by pattern, the display hint is a brand and a four-digit
//!   `last4`, and metadata is at most ten string pairs.
//!
//! The adapter. Code outside the engine reads the actions waiting for its
//! provider through the schema-level `listPaymentActions`, calls the
//! provider, and reports every outcome and every provider event through
//! `recordPaymentEvent`, which needs the config's `record.permission`. An
//! event applies once per `(provider, eventId)` in the namespace: a repeat
//! changes and writes nothing, and the same id with other content is
//! `key_reused`. An attempt's status only moves forward, by the rank
//! `open < requiresAction < processing < failed < authorized < cancelled <
//! captured`, so a late event is recorded and changes nothing, a payment
//! that succeeds after a failed try is caught, and money a provider reports
//! captured is counted whatever came before it. A capture past what is
//! authorized (or, with nothing authorized, past the attempt's amount) is
//! `over_capture`.
//!
//! The Workflow. `require` holds a move into a state until every amount
//! due is authorized or captured (`unauthorized`) or captured (`unpaid`).
//! No move leaves a state while an attempt is `processing`
//! (`payment_in_progress`), so a hold's expiry fired by Timers does not let
//! go of what is being paid for. The guard reads what the move asks, and
//! afterChange reads the status from the instance as it is, never from
//! which operation ran, so a Timers fire and a Reactions move are caught
//! as a caller's transition is:
//! - in a `voidOn` state every attempt not yet captured, failed or
//!   cancelled, and not processing, gets one `void` action, and
//!   `openPayment` is refused (`nothing_due`); money reported captured
//!   there is refunded when `afterRelease` is `refund`, and kept for an
//!   operator when it is `keep`, the default;
//! - with `capture: manual`, in a `captureOn` state every authorized
//!   attempt gets one `capture` action of what is authorized;
//! - on a refund instance, in an `issueOn` state, the instance asks the one
//!   its `refundOf` link points at for its amount (`refundFor`), once per
//!   issue; the provider's answer comes back through `settleRefundFor`, and
//!   the refund's `Payments.state` turns `refunded`, which a Reactions rule
//!   hears.
//!
//! While an attempt is in flight (open, requiring action, processing or
//! authorized), what is due does not change: `due` reads the amount the
//! newest one was opened with, and a change of the amount field, or of
//! `Holds.total`, is `amount_locked`; the currency field does not change
//! while any attempt exists (`currency_mismatch`). A refund instance's
//! amount, currency and `refundOf` link stay once it has issued.
//!
//! The refund pair. A refund instance's Payments invokes `refundFor` on the
//! instance it refunds, which accepts it only from an instance of a schema
//! whose Payments `refundOf` link points here, in an `issueOn` state, for
//! its own amount and currency (`not_linked`), as a supply's holder check
//! accepts only a claim's own items, so a caller cannot ask a refund by
//! calling it directly. `settleRefundFor` needs the refund instance's
//! `record.permission`, the adapter's, and takes only the instance its link
//! points at and a refund it asked.
//!
//! A refund draws on one captured attempt; one larger than any attempt has
//! left to refund is `exceeds_refundable`, its details naming what is.
//!
//! configChange: `require`, `voidOn`, `captureOn`, `capture`,
//! `afterRelease`, `issueOn` and the permissions may change. The currency,
//! the source of the amount and `refundOf` stay while the schema has
//! instances. Payments joins a schema that has instances and leaves only
//! one that has none.

mod guidance;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    page, page_request, BehaviorMigration, ColumnSpec, IndexSpec, InstanceCx, InstanceView, Page,
    SchemaCx, TableSpec,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::json::{js_keys, number_value, stringify};
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    InstanceChange, InstanceRecord, OperationCx, Result,
};

use crate::currency;
use crate::supply::InstanceRef;

/// The behavior's name.
pub const NAME: &str = "Payments";

/// How many attempts `Payments.attempts` shows, newest first.
pub const ATTEMPTS_SHOWN: usize = 20;

/// The largest integer a JavaScript number holds exactly.
const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

/// The fields Payments reads of an instance, its own and others', by
/// qualified name.
const WORKFLOW_STATUS: &str = "Workflow.status";
const WORKFLOW_ENTERED_AT: &str = "Workflow.enteredAt";
const LINKS_TARGETS: &str = "Links.targets";
const HOLDS_TOTAL: &str = "Holds.total";
const PAYMENTS_DUE: &str = "Payments.due";
const PAYMENTS_CURRENCY: &str = "Payments.currency";

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
}

use codes::*;

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

/// Where the amount due comes from.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum AmountSource {
    /// An own integer field, by JSON key.
    Field(String),
    /// The claim's `Holds.total`, which carries its currency.
    Holds,
}

/// The currency of an amount from a field.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CurrencySource {
    /// An ISO 4217 code.
    Code(String),
    /// An own `Finance.Currency` field, by JSON key.
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
    /// Whether `captureOn` states capture what is authorized; automatic
    /// capture otherwise.
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
    /// The link to the instance this one refunds.
    pub refund_of: Option<String>,
    pub issue_on: Vec<String>,
    /// Whether the type composes Workflow.
    pub workflow: bool,
}

impl PaymentsConfig {
    /// What a move into a state waits for; `None` for nothing.
    pub fn requirement(&self, state: &str) -> Option<Requirement> {
        self.require
            .iter()
            .find(|(known, _)| known == state)
            .map(|(_, requirement)| *requirement)
    }

    /// Whether the instance is a refund of another.
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

fn text_at<'v>(value: &'v Value, path: &[&str]) -> Option<&'v str> {
    path.iter()
        .try_fold(value, |at, key| at.get(*key))
        .and_then(Value::as_str)
}

// field_types reads the JSON types an own field's schema admits.
fn field_types<'t>(target: &'t ConfigTarget<'_>, field: &str) -> Vec<&'t str> {
    match target
        .field_schemas()
        .get(field)
        .and_then(|schema| schema.get("type"))
    {
        Some(Value::Array(types)) => types.iter().filter_map(Value::as_str).collect(),
        Some(Value::String(one)) => vec![one.as_str()],
        _ => Vec::new(),
    }
}

// check_field holds a field the config names to an own field of the type
// whose values are of `kind` (or null).
fn check_field(target: &ConfigTarget<'_>, at: &str, field: &str, kind: &str) -> Result<()> {
    if !target.fields().iter().any(|known| known == field) {
        return Err(Error::config(format!(
            "{at} \"{field}\" is not a field of {} (its fields: {})",
            target.type_name(),
            target.fields().join(", ")
        )));
    }
    let types = field_types(target, field);
    if !types.contains(&kind) || types.iter().any(|one| *one != kind && *one != "null") {
        return Err(Error::config(format!(
            "{at} \"{field}\" is not {} {kind} field of {}",
            if kind == "integer" { "an" } else { "a" },
            target.type_name()
        )));
    }
    Ok(())
}

// workflow_states reads the type's Workflow states; `None` when its config
// is not of a shape Workflow takes, which Workflow refuses itself.
fn workflow_states(target: &ConfigTarget<'_>) -> Option<Vec<String>> {
    target
        .configs()
        .get("Workflow")
        .and_then(|flow| flow.get("states"))
        .and_then(Value::as_array)
        .map(|states| {
            states
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
}

// check_states holds the states a config names to the type's Workflow.
fn check_states<'s>(
    target: &ConfigTarget<'_>,
    at: &str,
    names: impl IntoIterator<Item = &'s String>,
) -> Result<()> {
    let names: Vec<&String> = names.into_iter().collect();
    if names.is_empty() {
        return Ok(());
    }
    if !target.behaviors().iter().any(|name| name == "Workflow") {
        return Err(Error::config(format!(
            "{at} names states of Workflow, which the type does not list"
        )));
    }
    let Some(states) = workflow_states(target) else {
        return Ok(());
    };
    for name in names {
        if !states.contains(name) {
            return Err(Error::config(format!(
                "{at} names \"{name}\", which is not a state of the type's Workflow ({})",
                states.join(", ")
            )));
        }
    }
    Ok(())
}

// check_refund_of holds refundOf's link to a required, single-valued link
// of the type's Links config and, when the schema is defined or
// published, its target schema to one that composes Payments.
fn check_refund_of(target: &ConfigTarget<'_>, link: &str) -> Result<()> {
    let at = format!("refundOf.link {link}");
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
        if let Some(links) = links {
            return Err(Error::config(format!(
                "{at} is not a link of the type's Links config (its links: {})",
                js_keys(links).join(", ")
            )));
        }
        return Ok(());
    };
    if spec.get("many") == Some(&Value::Bool(true)) {
        return Err(Error::config(format!(
            "{at} holds a list of targets, and a refund refunds one instance"
        )));
    }
    if spec.get("required") != Some(&Value::Bool(true)) {
        return Err(Error::config(format!(
            "{at} is not required, and every refund names what it refunds"
        )));
    }
    let (Some(schemas), Some(schema)) =
        (target.schemas(), spec.get("schema").and_then(Value::as_str))
    else {
        return Ok(());
    };
    let Some(refunded) = schemas.get(schema)? else {
        return Err(Error::config(format!(
            "{at}: schema {schema}, which the link points at, has no live version; publish it first"
        )));
    };
    if !refunded.behaviors.iter().any(|name| name == NAME) {
        return Err(Error::config(format!(
            "{at}: {schema}, which the link points at, does not compose Payments"
        )));
    }
    if refunded
        .configs
        .get(NAME)
        .is_some_and(|config| config.get("refundOf").is_some())
    {
        return Err(Error::config(format!(
            "{at}: {schema}, which the link points at, is a refund itself"
        )));
    }
    Ok(())
}

fn parse_config(raw: &Value, target: &ConfigTarget<'_>) -> Result<PaymentsConfig> {
    let amount = match raw.get("amount") {
        Some(amount) if amount.get("holds") == Some(&Value::Bool(true)) => AmountSource::Holds,
        Some(amount) => AmountSource::Field(
            amount
                .get("field")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
        ),
        None => return Err(Error::config("amount names what the instance owes")),
    };
    match &amount {
        AmountSource::Field(field) => check_field(target, "amount.field", field, "integer")?,
        AmountSource::Holds => {
            let position = |name: &str| target.behaviors().iter().position(|one| one == name);
            match (position("Holds"), position(NAME)) {
                (None, _) => {
                    return Err(Error::config(
                        "amount.holds reads Holds.total, and the type does not list Holds",
                    ))
                }
                // Holds' afterChange writes the total Payments' reads.
                (Some(holds), Some(payments)) if holds > payments => {
                    return Err(Error::config(
                        "amount.holds reads Holds.total, so the type lists Payments after Holds",
                    ))
                }
                _ => {}
            }
        }
    }
    let currency = match raw.get("currency") {
        None => None,
        Some(Value::String(code)) => {
            if let Some(why) = currency::refusal("currency", code) {
                return Err(Error::config(why));
            }
            Some(CurrencySource::Code(code.clone()))
        }
        Some(given) => {
            let field = given
                .get("field")
                .and_then(Value::as_str)
                .unwrap_or_default();
            check_field(target, "currency.field", field, "string")?;
            Some(CurrencySource::Field(field.to_owned()))
        }
    };
    if matches!(amount, AmountSource::Field(_)) && currency.is_none() {
        return Err(Error::config(
            "an amount from a field names its currency: a code or { field }",
        ));
    }
    let manual_capture = raw.get("capture").and_then(Value::as_str) == Some("manual");
    let capture_on = strings(raw.get("captureOn"));
    if !capture_on.is_empty() && !manual_capture {
        return Err(Error::config("captureOn captures with capture manual"));
    }
    let mut require = Vec::new();
    if let Some(given) = raw.get("require").and_then(Value::as_object) {
        for state in js_keys(given) {
            let requirement = match given[state].as_str() {
                Some("paid") => Requirement::Paid,
                _ => Requirement::Authorized,
            };
            require.push((state.to_owned(), requirement));
        }
    }
    let void_on = strings(raw.get("voidOn"));
    let issue_on = strings(raw.get("issueOn"));
    check_states(target, "require", require.iter().map(|(state, _)| state))?;
    check_states(target, "voidOn", &void_on)?;
    check_states(target, "captureOn", &capture_on)?;
    check_states(target, "issueOn", &issue_on)?;
    if let Some(states) = workflow_states(target) {
        let initial = target
            .configs()
            .get("Workflow")
            .and_then(|flow| flow.get("initial"))
            .and_then(Value::as_str)
            .or_else(|| states.first().map(String::as_str));
        if let Some((state, _)) = require
            .iter()
            .find(|(state, _)| Some(state.as_str()) == initial)
        {
            return Err(Error::config(format!(
                "require names {state}, the Workflow's initial state, which a create enters before anything is paid"
            )));
        }
    }
    let refund_of = text_at(raw, &["refundOf", "link"]).map(str::to_owned);
    if let Some(link) = &refund_of {
        if matches!(amount, AmountSource::Holds) {
            return Err(Error::config(
                "a refund's amount is an own field: amount { field } with refundOf",
            ));
        }
        check_refund_of(target, link)?;
        for (key, what) in [
            ("refunds", "refunds"),
            ("require", "require"),
            ("voidOn", "voidOn"),
            ("capture", "capture"),
            ("afterRelease", "afterRelease"),
        ] {
            if raw.get(key).is_some() {
                return Err(Error::config(format!(
                    "{what} is for an instance that takes payments, and a refund instance (refundOf) takes none"
                )));
            }
        }
    }
    if refund_of.is_none() && !issue_on.is_empty() {
        return Err(Error::config("issueOn issues a refund with refundOf"));
    }
    if refund_of.is_some() && issue_on.is_empty() {
        return Err(Error::config(
            "refundOf issues the refund in its issueOn states",
        ));
    }
    Ok(PaymentsConfig {
        amount,
        currency,
        manual_capture,
        capture_on,
        require,
        void_on,
        refund_after_release: raw.get("afterRelease").and_then(Value::as_str) == Some("refund"),
        refund_permission: text_at(raw, &["refunds", "permission"]).map(str::to_owned),
        record_permission: text_at(raw, &["record", "permission"])
            .unwrap_or_default()
            .to_owned(),
        refund_of,
        issue_on,
        workflow: target.behaviors().iter().any(|name| name == "Workflow"),
    })
}

// ---------------------------------------------------------------------------
// Statuses and kinds
// ---------------------------------------------------------------------------

/// An attempt's status at its provider.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AttemptStatus {
    Open,
    RequiresAction,
    Processing,
    Authorized,
    Captured,
    Failed,
    Cancelled,
}

impl AttemptStatus {
    pub const ALL: [AttemptStatus; 7] = [
        AttemptStatus::Open,
        AttemptStatus::RequiresAction,
        AttemptStatus::Processing,
        AttemptStatus::Authorized,
        AttemptStatus::Captured,
        AttemptStatus::Failed,
        AttemptStatus::Cancelled,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            AttemptStatus::Open => "open",
            AttemptStatus::RequiresAction => "requiresAction",
            AttemptStatus::Processing => "processing",
            AttemptStatus::Authorized => "authorized",
            AttemptStatus::Captured => "captured",
            AttemptStatus::Failed => "failed",
            AttemptStatus::Cancelled => "cancelled",
        }
    }

    pub fn parse(text: &str) -> Option<AttemptStatus> {
        AttemptStatus::ALL
            .into_iter()
            .find(|status| status.as_str() == text)
    }

    /// How far forward the status is: an event moves an attempt only to a
    /// status of a higher rank. A failure ranks above processing and below
    /// an authorization, so a payment that succeeds after a failed try is
    /// caught, and a late failure of an earlier try changes nothing;
    /// money captured ranks above everything, so it is counted whatever
    /// came before it.
    pub fn rank(self) -> u8 {
        match self {
            AttemptStatus::Open => 0,
            AttemptStatus::RequiresAction => 1,
            AttemptStatus::Processing => 2,
            AttemptStatus::Failed => 3,
            AttemptStatus::Authorized => 4,
            AttemptStatus::Cancelled => 5,
            AttemptStatus::Captured => 6,
        }
    }

    /// Whether the attempt still holds what is due: open, requiring action,
    /// processing or authorized.
    pub fn in_flight(self) -> bool {
        matches!(
            self,
            AttemptStatus::Open
                | AttemptStatus::RequiresAction
                | AttemptStatus::Processing
                | AttemptStatus::Authorized
        )
    }

    /// Whether a provider can still cancel or void it: in flight and not
    /// processing.
    pub fn voidable(self) -> bool {
        self.in_flight() && self != AttemptStatus::Processing
    }
}

/// `Payments.state`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PaymentsState {
    Unpaid,
    Pending,
    Authorized,
    Paid,
    PartiallyRefunded,
    Refunded,
}

impl PaymentsState {
    pub fn as_str(self) -> &'static str {
        match self {
            PaymentsState::Unpaid => "unpaid",
            PaymentsState::Pending => "pending",
            PaymentsState::Authorized => "authorized",
            PaymentsState::Paid => "paid",
            PaymentsState::PartiallyRefunded => "partiallyRefunded",
            PaymentsState::Refunded => "refunded",
        }
    }
}

/// What an action asks the adapter to do.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ActionKind {
    Create,
    Capture,
    Void,
    Refund,
}

impl ActionKind {
    pub fn as_str(self) -> &'static str {
        match self {
            ActionKind::Create => "create",
            ActionKind::Capture => "capture",
            ActionKind::Void => "void",
            ActionKind::Refund => "refund",
        }
    }
}

/// A refund's status.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RefundStatus {
    Requested,
    Sent,
    Succeeded,
    Failed,
}

impl RefundStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            RefundStatus::Requested => "requested",
            RefundStatus::Sent => "sent",
            RefundStatus::Succeeded => "succeeded",
            RefundStatus::Failed => "failed",
        }
    }

    pub fn parse(text: &str) -> Option<RefundStatus> {
        [
            RefundStatus::Requested,
            RefundStatus::Sent,
            RefundStatus::Succeeded,
            RefundStatus::Failed,
        ]
        .into_iter()
        .find(|status| status.as_str() == text)
    }

    /// Whether the provider has not answered it yet.
    pub fn pending(self) -> bool {
        matches!(self, RefundStatus::Requested | RefundStatus::Sent)
    }
}

/// What a provider event reports.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum EventKind {
    RequiresAction,
    Processing,
    Authorized,
    Captured,
    Failed,
    Cancelled,
    Refunded,
    RefundFailed,
}

impl EventKind {
    pub fn as_str(self) -> &'static str {
        match self {
            EventKind::RequiresAction => "requiresAction",
            EventKind::Processing => "processing",
            EventKind::Authorized => "authorized",
            EventKind::Captured => "captured",
            EventKind::Failed => "failed",
            EventKind::Cancelled => "cancelled",
            EventKind::Refunded => "refunded",
            EventKind::RefundFailed => "refundFailed",
        }
    }

    pub fn parse(text: &str) -> Option<EventKind> {
        [
            EventKind::RequiresAction,
            EventKind::Processing,
            EventKind::Authorized,
            EventKind::Captured,
            EventKind::Failed,
            EventKind::Cancelled,
            EventKind::Refunded,
            EventKind::RefundFailed,
        ]
        .into_iter()
        .find(|kind| kind.as_str() == text)
    }

    /// The attempt status the event moves an attempt to; `None` for a
    /// refund's outcome.
    pub fn status(self) -> Option<AttemptStatus> {
        match self {
            EventKind::RequiresAction => Some(AttemptStatus::RequiresAction),
            EventKind::Processing => Some(AttemptStatus::Processing),
            EventKind::Authorized => Some(AttemptStatus::Authorized),
            EventKind::Captured => Some(AttemptStatus::Captured),
            EventKind::Failed => Some(AttemptStatus::Failed),
            EventKind::Cancelled => Some(AttemptStatus::Cancelled),
            EventKind::Refunded | EventKind::RefundFailed => None,
        }
    }
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

/// One attempt, as Payments' table holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Attempt {
    pub seq: i64,
    pub attempt: String,
    pub idempotency_key: String,
    pub provider: String,
    pub provider_ref: Option<String>,
    pub amount: i64,
    pub currency: String,
    /// What was due when it was opened.
    pub due: i64,
    pub status: AttemptStatus,
    pub authorized: i64,
    pub captured: i64,
    pub brand: Option<String>,
    pub last4: Option<String>,
    pub return_url: Option<String>,
    pub opened_at: i64,
    pub updated_at: i64,
}

impl Attempt {
    /// `{ attempt, provider, providerRef?, amount, currency, status,
    /// authorized?, captured?, brand?, last4?, openedAt, updatedAt }`.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("attempt".into(), Value::from(self.attempt.as_str()));
        out.insert("provider".into(), Value::from(self.provider.as_str()));
        if let Some(reference) = &self.provider_ref {
            out.insert("providerRef".into(), Value::from(reference.as_str()));
        }
        out.insert("amount".into(), Value::from(self.amount));
        out.insert("currency".into(), Value::from(self.currency.as_str()));
        out.insert("status".into(), Value::from(self.status.as_str()));
        if self.authorized > 0 {
            out.insert("authorized".into(), Value::from(self.authorized));
        }
        if self.captured > 0 {
            out.insert("captured".into(), Value::from(self.captured));
        }
        if let Some(brand) = &self.brand {
            out.insert("brand".into(), Value::from(brand.as_str()));
        }
        if let Some(last4) = &self.last4 {
            out.insert("last4".into(), Value::from(last4.as_str()));
        }
        out.insert("openedAt".into(), Value::from(self.opened_at));
        out.insert("updatedAt".into(), Value::from(self.updated_at));
        Value::Object(out)
    }

    /// What is authorized of it and not captured, while it is authorized.
    pub fn holding(&self) -> i64 {
        if self.status == AttemptStatus::Authorized {
            (self.authorized - self.captured).max(0)
        } else {
            0
        }
    }

    /// The most a provider may capture of it: what it authorized, or its
    /// amount when no authorization was reported (automatic capture).
    pub fn capturable(&self) -> i64 {
        if self.authorized > 0 {
            self.authorized
        } else {
            self.amount
        }
    }
}

/// One refund, as Payments' table holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Refund {
    pub seq: i64,
    pub refund: String,
    pub idempotency_key: String,
    pub attempt: String,
    pub provider: String,
    pub amount: i64,
    pub currency: String,
    pub reason: String,
    pub status: RefundStatus,
    pub refunded: i64,
    pub origin: Option<InstanceRef>,
}

impl Refund {
    /// What it holds of the attempt it draws on: what it asks while
    /// pending, what was refunded once it succeeded.
    pub fn draws(&self) -> i64 {
        match self.status {
            RefundStatus::Requested | RefundStatus::Sent => self.amount,
            RefundStatus::Succeeded => self.refunded,
            RefundStatus::Failed => 0,
        }
    }
}

/// One refund a refund instance asked of the instance it refunds.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Issue {
    pub seq: i64,
    pub of: InstanceRef,
    pub refund: String,
    pub idempotency_key: String,
    pub amount: i64,
    pub currency: String,
    pub status: RefundStatus,
    pub refunded: i64,
    /// When the refund instance entered the state it issued in
    /// (`Workflow.enteredAt`): a failed issue is asked again only after the
    /// instance enters an issueOn state again.
    pub entered_at: Option<i64>,
}

const ATTEMPT_COLUMNS: &str = "seq, attempt, idempotency_key, provider, provider_ref, amount, currency, due, status, authorized, captured, brand, last4, return_url, opened_at, updated_at";
const REFUND_COLUMNS: &str = "seq, refund, idempotency_key, attempt, provider, amount, currency, reason, status, refunded, origin_schema, origin_id";
const ISSUE_COLUMNS: &str =
    "seq, of_schema, of_id, refund, idempotency_key, amount, currency, status, refunded, entered_at";

fn defect(message: impl Into<String>) -> Error {
    Error::behavior(NAME, message)
}

fn attempt_of(row: &Row) -> Result<Attempt> {
    let status = row.text("status")?;
    Ok(Attempt {
        seq: row.i64("seq")?,
        attempt: row.text("attempt")?.to_owned(),
        idempotency_key: row.text("idempotency_key")?.to_owned(),
        provider: row.text("provider")?.to_owned(),
        provider_ref: row.opt_text("provider_ref")?.map(str::to_owned),
        amount: row.i64("amount")?,
        currency: row.text("currency")?.to_owned(),
        due: row.i64("due")?,
        status: AttemptStatus::parse(status)
            .ok_or_else(|| defect(format!("an attempt holds the status {status}")))?,
        authorized: row.i64("authorized")?,
        captured: row.i64("captured")?,
        brand: row.opt_text("brand")?.map(str::to_owned),
        last4: row.opt_text("last4")?.map(str::to_owned),
        return_url: row.opt_text("return_url")?.map(str::to_owned),
        opened_at: row.i64("opened_at")?,
        updated_at: row.i64("updated_at")?,
    })
}

fn refund_of_row(row: &Row) -> Result<Refund> {
    let status = row.text("status")?;
    let origin = match (row.opt_text("origin_schema")?, row.opt_text("origin_id")?) {
        (Some(schema), Some(id)) => Some(InstanceRef::new(schema, id)),
        _ => None,
    };
    Ok(Refund {
        seq: row.i64("seq")?,
        refund: row.text("refund")?.to_owned(),
        idempotency_key: row.text("idempotency_key")?.to_owned(),
        attempt: row.text("attempt")?.to_owned(),
        provider: row.text("provider")?.to_owned(),
        amount: row.i64("amount")?,
        currency: row.text("currency")?.to_owned(),
        reason: row.text("reason")?.to_owned(),
        status: RefundStatus::parse(status)
            .ok_or_else(|| defect(format!("a refund holds the status {status}")))?,
        refunded: row.i64("refunded")?,
        origin,
    })
}

fn issue_of(row: &Row) -> Result<Issue> {
    let status = row.text("status")?;
    Ok(Issue {
        seq: row.i64("seq")?,
        of: InstanceRef::new(row.text("of_schema")?, row.text("of_id")?),
        refund: row.text("refund")?.to_owned(),
        idempotency_key: row.text("idempotency_key")?.to_owned(),
        amount: row.i64("amount")?,
        currency: row.text("currency")?.to_owned(),
        status: RefundStatus::parse(status)
            .ok_or_else(|| defect(format!("an issue holds the status {status}")))?,
        refunded: row.i64("refunded")?,
        entered_at: row.opt_i64("entered_at")?,
    })
}

// key is the instance's rows as Payments' SQL names them.
fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

fn with(mut values: Vec<SqlValue>, more: impl IntoIterator<Item = SqlValue>) -> Vec<SqlValue> {
    values.extend(more);
    values
}

/// Every attempt of the instance, oldest first.
fn attempts<C>(view: &InstanceView<'_, C>) -> Result<Vec<Attempt>> {
    let table = view.sql().table("attempts")?;
    let rows = view.sql().all(
        format!("SELECT {ATTEMPT_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY seq"),
        &key(view),
    )?;
    rows.iter().map(attempt_of).collect()
}

fn attempt_where<C>(
    view: &InstanceView<'_, C>,
    column: &str,
    value: &str,
) -> Result<Option<Attempt>> {
    let table = view.sql().table("attempts")?;
    let row = view.sql().get(
        format!("SELECT {ATTEMPT_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND {column} = ?"),
        &with(key(view), [SqlValue::from(value)]),
    )?;
    row.as_ref().map(attempt_of).transpose()
}

/// Every refund of the instance, oldest first.
fn refunds<C>(view: &InstanceView<'_, C>) -> Result<Vec<Refund>> {
    let table = view.sql().table("refunds")?;
    let rows = view.sql().all(
        format!("SELECT {REFUND_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY seq"),
        &key(view),
    )?;
    rows.iter().map(refund_of_row).collect()
}

fn refund_where<C>(
    view: &InstanceView<'_, C>,
    column: &str,
    value: &str,
) -> Result<Option<Refund>> {
    let table = view.sql().table("refunds")?;
    let row = view.sql().get(
        format!("SELECT {REFUND_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND {column} = ?"),
        &with(key(view), [SqlValue::from(value)]),
    )?;
    row.as_ref().map(refund_of_row).transpose()
}

/// Every refund a refund instance asked, oldest first.
fn issues<C>(view: &InstanceView<'_, C>) -> Result<Vec<Issue>> {
    let table = view.sql().table("issues")?;
    let rows = view.sql().all(
        format!("SELECT {ISSUE_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY seq"),
        &key(view),
    )?;
    rows.iter().map(issue_of).collect()
}

// ---------------------------------------------------------------------------
// Reading what is owed
// ---------------------------------------------------------------------------

/// What `Payments`' fields read of an instance.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Summary {
    pub due: i64,
    pub authorized: i64,
    pub captured: i64,
    pub refunded: i64,
    pub outstanding: i64,
    pub currency: Option<String>,
    pub state: PaymentsState,
}

/// What the config's source says is due now, and in which currency.
struct Source {
    due: i64,
    currency: Option<String>,
}

// integer reads a JSON integer within JavaScript's safe range.
fn integer(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    let exact = match number.as_i64() {
        Some(exact) => exact,
        None => {
            let value = number_value(number);
            if !value.is_finite() || value.fract() != 0.0 {
                return None;
            }
            value as i64
        }
    };
    (exact.abs() <= MAX_SAFE_INTEGER).then_some(exact)
}

fn own_record(
    view: &InstanceView<'_, PaymentsConfig>,
    fields: &[&str],
) -> Result<Option<InstanceRecord>> {
    view.instances().get(view.schema(), view.id(), Some(fields))
}

// holds_total reads the claim's Holds.total: `{ amount, currency }`, or
// none.
fn holds_total(view: &InstanceView<'_, PaymentsConfig>) -> Result<Option<(i64, String)>> {
    let record = own_record(view, &[HOLDS_TOTAL])?;
    let total = record
        .as_ref()
        .and_then(|record| record.behavior_field("Holds", "total"));
    Ok(total.and_then(|total| {
        Some((
            total.get("amount").and_then(integer)?,
            total.get("currency")?.as_str()?.to_owned(),
        ))
    }))
}

// source_with reads what the config's source says is due, with the own
// fields given (the instance's, or an update's after).
fn source_with(
    view: &InstanceView<'_, PaymentsConfig>,
    data: &Map<String, Value>,
) -> Result<Source> {
    let config = view.config();
    let named = match &config.currency {
        Some(CurrencySource::Code(code)) => Some(code.clone()),
        Some(CurrencySource::Field(field)) => {
            data.get(field).and_then(Value::as_str).map(str::to_owned)
        }
        None => None,
    };
    match &config.amount {
        AmountSource::Field(field) => Ok(Source {
            due: data.get(field).and_then(integer).unwrap_or(0).max(0),
            currency: named,
        }),
        AmountSource::Holds => Ok(match holds_total(view)? {
            Some((amount, currency)) => Source {
                due: amount.max(0),
                currency: Some(currency),
            },
            None => Source {
                due: 0,
                currency: named,
            },
        }),
    }
}

fn source(view: &InstanceView<'_, PaymentsConfig>) -> Result<Source> {
    let data = view.data()?;
    source_with(view, &data)
}

/// The newest attempt in flight; `None` when none is.
fn in_flight(attempts: &[Attempt]) -> Option<&Attempt> {
    attempts
        .iter()
        .rev()
        .find(|attempt| attempt.status.in_flight())
}

fn summary(view: &InstanceView<'_, PaymentsConfig>) -> Result<Summary> {
    if view.config().is_refund() {
        return refund_summary(view);
    }
    let attempts = attempts(view)?;
    let refunds = refunds(view)?;
    let live = source(view)?;
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
    let pending = attempts.iter().any(|attempt| attempt.status.in_flight());
    let state = if refunded > 0 {
        if refunded >= captured {
            PaymentsState::Refunded
        } else {
            PaymentsState::PartiallyRefunded
        }
    } else if captured > 0 && captured >= due {
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
        currency,
        state,
    })
}

// refund_summary reads a refund instance: what it refunds, and how far its
// latest issue has come: pending while asked, refunded once the provider
// refunded it, unpaid before it is asked or after it failed.
fn refund_summary(view: &InstanceView<'_, PaymentsConfig>) -> Result<Summary> {
    let live = source(view)?;
    let latest = issues(view)?.pop();
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
        currency: latest.map(|issue| issue.currency).or(live.currency),
        state,
    })
}

// status_of reads the instance's Workflow status; `None` without Workflow.
fn status_of(view: &InstanceView<'_, PaymentsConfig>) -> Result<Option<String>> {
    if !view.config().workflow {
        return Ok(None);
    }
    let record = own_record(view, &[WORKFLOW_STATUS])?;
    Ok(record
        .as_ref()
        .and_then(|record| record.behavior_field("Workflow", "status"))
        .and_then(Value::as_str)
        .map(str::to_owned))
}

// link_target reads where a link of the instance's Links points.
fn link_target(
    view: &InstanceView<'_, PaymentsConfig>,
    schema: &str,
    id: &str,
    link: &str,
) -> Result<Option<InstanceRef>> {
    let record = view.instances().get(schema, id, Some(&[LINKS_TARGETS]))?;
    Ok(record
        .as_ref()
        .and_then(|record| record.behavior_field("Links", "targets"))
        .and_then(|targets| targets.get(link))
        .and_then(InstanceRef::from_json))
}

// ---------------------------------------------------------------------------
// Refusals and ids
// ---------------------------------------------------------------------------

fn details(value: Value) -> Option<Map<String, Value>> {
    match value {
        Value::Object(details) => Some(details),
        _ => None,
    }
}

// vetoed is a refusal with one of the codes the declaration lists.
fn vetoed<C>(
    view: &InstanceView<'_, C>,
    action: &str,
    reason: String,
    code: &str,
    more: Option<Value>,
) -> Error {
    Error::veto(
        NAME,
        action,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: more.and_then(details),
        },
    )
}

fn veto(reason: String, code: &str, more: Option<Value>) -> Veto {
    Veto {
        reason,
        code: Some(code.to_owned()),
        details: more.and_then(details),
    }
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

fn params_error(operation: &str, path: &str, message: impl Into<String>) -> Error {
    EngineError::operation_params(NAME, operation, vec![SchemaIssue::new(path, message)]).into()
}

// new_id is a version 4 UUID from the engine's source, after a prefix: an
// attempt's, an action's or a refund's id, which the adapter sends its
// provider as an idempotency key, so it is unique across namespaces and
// databases that share a provider account.
fn new_id<C>(view: &InstanceView<'_, C>, prefix: &str) -> String {
    let bytes = view.uuids().next_bytes();
    let hex: String = bytes.iter().map(|byte| format!("{byte:02x}")).collect();
    format!(
        "{prefix}_{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    )
}

fn key_char(byte: u8) -> bool {
    byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b':' | b'-')
}

// issue_key is the idempotency key a refund instance asks with: its schema
// and id when they fit a key, else a hash of them, and the issue's number.
fn issue_key(refund: &InstanceRef, number: usize) -> String {
    let plain = format!("{}:{}", refund.schema, refund.id);
    let base = if plain.len() <= 200 && plain.bytes().all(key_char) {
        plain
    } else {
        // FNV-1a over schema, a NUL and id: stable across builds.
        let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
        for byte in refund.schema.bytes().chain([0]).chain(refund.id.bytes()) {
            hash ^= u64::from(byte);
            hash = hash.wrapping_mul(0x0100_0000_01b3);
        }
        format!(
            "{}:h{hash:016x}",
            if refund.schema.bytes().all(key_char) && refund.schema.len() <= 100 {
                refund.schema.as_str()
            } else {
                "refund"
            }
        )
    };
    format!("{base}.{number}")
}

fn text_param<'p>(params: &'p Value, name: &str) -> Option<&'p str> {
    params.get(name).and_then(Value::as_str)
}

fn amount_param(params: &Value, name: &str) -> Option<i64> {
    params.get(name).and_then(integer)
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

/// What an action is for: the attempt it acts on, and for a refund the
/// refund.
struct ActionFor<'a> {
    kind: ActionKind,
    attempt: &'a Attempt,
    refund: Option<&'a str>,
    amount: i64,
}

// queue records an action waiting for the attempt's provider and returns
// its id.
fn queue<C>(cx: &InstanceCx<'_, C>, action: &ActionFor<'_>) -> Result<String> {
    let id = new_id(cx, "act");
    let table = cx.sql().table("actions")?;
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, action, kind, attempt, refund, provider, amount, currency, at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
        ),
        &with(
            key(cx),
            [
                SqlValue::from(id.as_str()),
                SqlValue::from(action.kind.as_str()),
                SqlValue::from(action.attempt.attempt.as_str()),
                SqlValue::from(action.refund),
                SqlValue::from(action.attempt.provider.as_str()),
                SqlValue::from(action.amount),
                SqlValue::from(action.attempt.currency.as_str()),
                SqlValue::from(cx.now()),
            ],
        ),
    )?;
    Ok(id)
}

// action_json is what capturePayment and voidPayment return.
fn action_json(action: &str, kind: ActionKind, attempt: &Attempt, amount: i64) -> Value {
    json!({
        "action": action,
        "kind": kind.as_str(),
        "attempt": attempt.attempt,
        "amount": amount,
        "currency": attempt.currency,
    })
}

// waiting_action finds an action of the attempt of a kind: one still
// waiting when `waiting`, any otherwise. Returns its id and amount.
fn find_action<C>(
    view: &InstanceView<'_, C>,
    attempt: &str,
    kind: ActionKind,
    waiting: bool,
) -> Result<Option<(String, i64)>> {
    let table = view.sql().table("actions")?;
    let only = if waiting { " AND done_at IS NULL" } else { "" };
    let row = view.sql().get(
        format!("SELECT action, amount FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND attempt = ? AND kind = ?{only} ORDER BY seq LIMIT 1"),
        &with(key(view), [SqlValue::from(attempt), SqlValue::from(kind.as_str())]),
    )?;
    row.map(|row| Ok((row.text("action")?.to_owned(), row.i64("amount")?)))
        .transpose()
}

// pending_captures sums the capture actions of an attempt still waiting.
fn pending_captures<C>(view: &InstanceView<'_, C>, attempt: &str) -> Result<i64> {
    let table = view.sql().table("actions")?;
    let row = view.sql().get(
        format!("SELECT COALESCE(SUM(amount), 0) AS total FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND attempt = ? AND kind = 'capture' AND done_at IS NULL"),
        &with(key(view), [SqlValue::from(attempt)]),
    )?;
    Ok(match row {
        Some(row) => row.i64("total")?,
        None => 0,
    })
}

// finish marks the waiting actions of an attempt of the kinds given done.
fn finish<C>(
    cx: &InstanceCx<'_, C>,
    attempt: &str,
    kinds: &[ActionKind],
    refund: Option<&str>,
) -> Result<()> {
    if kinds.is_empty() {
        return Ok(());
    }
    let table = cx.sql().table("actions")?;
    let marks = vec!["?"; kinds.len()].join(", ");
    let mut values = vec![SqlValue::from(cx.now())];
    values.extend(key(cx));
    values.push(SqlValue::from(attempt));
    values.extend(kinds.iter().map(|kind| SqlValue::from(kind.as_str())));
    let only_refund = match refund {
        Some(refund) => {
            values.push(SqlValue::from(refund));
            " AND refund = ?"
        }
        None => "",
    };
    cx.sql().run(
        format!("UPDATE {table} SET done_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND attempt = ? AND kind IN ({marks}){only_refund} AND done_at IS NULL"),
        &values,
    )?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Attempts and refunds written
// ---------------------------------------------------------------------------

fn save_attempt<C>(cx: &InstanceCx<'_, C>, attempt: &Attempt) -> Result<()> {
    let table = cx.sql().table("attempts")?;
    cx.sql().run(
        format!(
            "UPDATE {table} SET provider_ref = ?, status = ?, authorized = ?, captured = ?, brand = ?, last4 = ?, updated_at = ?
             WHERE namespace = ? AND schema = ? AND id = ? AND attempt = ?"
        ),
        &[
            SqlValue::from(attempt.provider_ref.as_deref()),
            SqlValue::from(attempt.status.as_str()),
            SqlValue::from(attempt.authorized),
            SqlValue::from(attempt.captured),
            SqlValue::from(attempt.brand.as_deref()),
            SqlValue::from(attempt.last4.as_deref()),
            SqlValue::from(cx.now()),
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::from(cx.id()),
            SqlValue::from(attempt.attempt.as_str()),
        ],
    )?;
    Ok(())
}

fn refund_json(refund: &Refund, action: &str) -> Value {
    json!({
        "refund": refund.refund,
        "action": action,
        "amount": refund.amount,
        "currency": refund.currency,
        "status": refund.status.as_str(),
    })
}

// refund_action is the id of a refund's action.
fn refund_action<C>(view: &InstanceView<'_, C>, refund: &Refund) -> Result<String> {
    let table = view.sql().table("actions")?;
    let row = view.sql().get(
        format!("SELECT action FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND kind = 'refund' AND refund = ? ORDER BY seq LIMIT 1"),
        &with(key(view), [SqlValue::from(refund.refund.as_str())]),
    )?;
    match row {
        Some(row) => Ok(row.text("action")?.to_owned()),
        None => Err(defect(format!("refund {} has no action", refund.refund))),
    }
}

/// A refund asked: of what, why, and by whom when a refund instance asked.
struct Asked<'a> {
    operation: &'a str,
    amount: i64,
    reason: &'a str,
    key: &'a str,
    origin: Option<&'a InstanceRef>,
}

// refundable is what each captured attempt has left to refund: what it
// captured less what its refunds draw.
fn refundable(attempts: &[Attempt], refunds: &[Refund]) -> Vec<(usize, i64)> {
    attempts
        .iter()
        .enumerate()
        .filter(|(_, attempt)| attempt.captured > 0)
        .map(|(index, attempt)| {
            let drawn: i64 = refunds
                .iter()
                .filter(|refund| refund.attempt == attempt.attempt)
                .map(Refund::draws)
                .sum();
            (index, (attempt.captured - drawn).max(0))
        })
        .collect()
}

// record_refund records a refund and its action, or returns the one the
// key recorded. A refund draws on the oldest captured attempt with enough
// left; one larger than any has is exceeds_refundable.
fn record_refund(cx: &OperationCx<'_, PaymentsConfig>, asked: &Asked<'_>) -> Result<Value> {
    if let Some(existing) = refund_where(cx, "idempotency_key", asked.key)? {
        let same = existing.amount == asked.amount
            && existing.origin.as_ref() == asked.origin
            && (asked.origin.is_some() || existing.reason == asked.reason);
        if !same {
            return Err(vetoed(
                cx,
                asked.operation,
                format!(
                    "idempotencyKey {} asked refund {} of {} before",
                    asked.key, existing.refund, existing.amount
                ),
                KEY_REUSED,
                Some(json!({"refund": existing.refund})),
            ));
        }
        cx.unchanged();
        let action = refund_action(cx, &existing)?;
        return Ok(refund_json(&existing, &action));
    }
    let attempts = attempts(cx)?;
    let refunds = refunds(cx)?;
    insert_refund(cx, asked, &attempts, &refunds)
}

fn insert_refund(
    cx: &InstanceCx<'_, PaymentsConfig>,
    asked: &Asked<'_>,
    attempts: &[Attempt],
    refunds: &[Refund],
) -> Result<Value> {
    let left = refundable(attempts, refunds);
    let Some((index, _)) = left.iter().find(|(_, left)| *left >= asked.amount).copied() else {
        let most = left.iter().map(|(_, left)| *left).max().unwrap_or(0);
        let captured: i64 = attempts.iter().map(|attempt| attempt.captured).sum();
        let pending: i64 = refunds
            .iter()
            .filter(|refund| refund.status.pending())
            .map(|refund| refund.amount)
            .sum();
        let refunded: i64 = refunds
            .iter()
            .filter(|refund| refund.status == RefundStatus::Succeeded)
            .map(|refund| refund.refunded)
            .sum();
        return Err(vetoed(
            cx,
            asked.operation,
            format!(
                "{} {} has {most} it can refund in one refund, not {}",
                cx.schema(),
                cx.id(),
                asked.amount
            ),
            EXCEEDS_REFUNDABLE,
            Some(json!({
                "refundable": most,
                "captured": captured,
                "refunded": refunded,
                "pending": pending,
            })),
        ));
    };
    let attempt = &attempts[index];
    let refund = Refund {
        seq: 0,
        refund: new_id(cx, "rf"),
        idempotency_key: asked.key.to_owned(),
        attempt: attempt.attempt.clone(),
        provider: attempt.provider.clone(),
        amount: asked.amount,
        currency: attempt.currency.clone(),
        reason: asked.reason.to_owned(),
        status: RefundStatus::Requested,
        refunded: 0,
        origin: asked.origin.cloned(),
    };
    let table = cx.sql().table("refunds")?;
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, refund, idempotency_key, attempt, provider, amount, currency, reason, status, refunded, origin_schema, origin_id, at, updated_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)"
        ),
        &with(
            key(cx),
            [
                SqlValue::from(refund.refund.as_str()),
                SqlValue::from(refund.idempotency_key.as_str()),
                SqlValue::from(refund.attempt.as_str()),
                SqlValue::from(refund.provider.as_str()),
                SqlValue::from(refund.amount),
                SqlValue::from(refund.currency.as_str()),
                SqlValue::from(refund.reason.as_str()),
                SqlValue::from(refund.status.as_str()),
                SqlValue::from(refund.origin.as_ref().map(|origin| origin.schema.as_str())),
                SqlValue::from(refund.origin.as_ref().map(|origin| origin.id.as_str())),
                SqlValue::from(cx.now()),
                SqlValue::from(cx.now()),
            ],
        ),
    )?;
    let action = queue(
        cx,
        &ActionFor {
            kind: ActionKind::Refund,
            attempt,
            refund: Some(&refund.refund),
            amount: refund.amount,
        },
    )?;
    Ok(refund_json(&refund, &action))
}

fn save_refund<C>(cx: &InstanceCx<'_, C>, refund: &Refund) -> Result<()> {
    let table = cx.sql().table("refunds")?;
    cx.sql().run(
        format!("UPDATE {table} SET status = ?, refunded = ?, updated_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND refund = ?"),
        &[
            SqlValue::from(refund.status.as_str()),
            SqlValue::from(refund.refunded),
            SqlValue::from(cx.now()),
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::from(cx.id()),
            SqlValue::from(refund.refund.as_str()),
        ],
    )?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

fn open_payment(
    _: &Payments,
    cx: &OperationCx<'_, PaymentsConfig>,
    params: &Value,
) -> Result<Value> {
    const OP: &str = "openPayment";
    let provider = text_param(params, "provider").unwrap_or_default();
    let idempotency_key = text_param(params, "idempotencyKey").unwrap_or_default();
    let return_url = text_param(params, "returnUrl");
    if cx.config().is_refund() {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "{} is a refund (refundOf), which takes no payments",
                cx.schema()
            ),
            NOT_CONFIGURED,
            None,
        ));
    }
    let attempts = attempts(cx)?;
    if let Some(existing) = attempts
        .iter()
        .find(|attempt| attempt.idempotency_key == idempotency_key)
    {
        if existing.provider != provider || existing.return_url.as_deref() != return_url {
            return Err(vetoed(
                cx,
                OP,
                format!(
                    "idempotencyKey {idempotency_key} opened attempt {} at {} before",
                    existing.attempt, existing.provider
                ),
                KEY_REUSED,
                Some(json!({"attempt": existing.attempt})),
            ));
        }
        cx.unchanged();
        return Ok(opened(existing));
    }
    if let Some(status) = status_of(cx)? {
        if cx.config().void_on.contains(&status) {
            return Err(vetoed(
                cx,
                OP,
                format!(
                    "{} {} is {status}, which voids its payments, so nothing is due",
                    cx.schema(),
                    cx.id()
                ),
                NOTHING_DUE,
                Some(json!({"state": status})),
            ));
        }
    }
    let live = source(cx)?;
    let flight = in_flight(&attempts);
    let due = flight.map_or(live.due, |attempt| attempt.due);
    let captured: i64 = attempts.iter().map(|attempt| attempt.captured).sum();
    let pending: i64 = attempts
        .iter()
        .filter(|attempt| attempt.status.in_flight())
        .map(|attempt| (attempt.amount - attempt.captured).max(0))
        .sum();
    let amount = due - captured - pending;
    if amount <= 0 {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "{} {} has nothing outstanding to pay: {due} due, {captured} captured, {pending} in attempts in flight",
                cx.schema(),
                cx.id()
            ),
            NOTHING_DUE,
            Some(json!({"due": due, "captured": captured, "pending": pending})),
        ));
    }
    let Some(currency) = attempts
        .last()
        .map(|attempt| attempt.currency.clone())
        .or(live.currency.clone())
    else {
        return Err(vetoed(
            cx,
            OP,
            format!("{} {} names no currency", cx.schema(), cx.id()),
            CURRENCY_MISMATCH,
            None,
        ));
    };
    if let Some(why) = currency::refusal("the instance's currency", &currency) {
        return Err(vetoed(cx, OP, why, CURRENCY_MISMATCH, None));
    }
    // A currency the config names must be the one Holds.total carries.
    if let (Some(CurrencySource::Code(code)), Some(total)) = (&cx.config().currency, &live.currency)
    {
        if code != total {
            return Err(vetoed(
                cx,
                OP,
                format!("what is due is in {total}, and the config takes {code}"),
                CURRENCY_MISMATCH,
                Some(json!({"currency": total, "expected": code})),
            ));
        }
    }
    if live
        .currency
        .as_deref()
        .is_some_and(|live| live != currency)
    {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "what is due is in {}, and the instance's attempts are in {currency}",
                live.currency.as_deref().unwrap_or_default()
            ),
            CURRENCY_MISMATCH,
            Some(json!({"currency": live.currency, "expected": currency})),
        ));
    }
    let attempt = Attempt {
        seq: 0,
        attempt: new_id(cx, "pay"),
        idempotency_key: idempotency_key.to_owned(),
        provider: provider.to_owned(),
        provider_ref: None,
        amount,
        currency,
        due,
        status: AttemptStatus::Open,
        authorized: 0,
        captured: 0,
        brand: None,
        last4: None,
        return_url: return_url.map(str::to_owned),
        opened_at: cx.now(),
        updated_at: cx.now(),
    };
    let table = cx.sql().table("attempts")?;
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, attempt, idempotency_key, provider, amount, currency, due, status, authorized, captured, return_url, opened_at, updated_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?)"
        ),
        &with(
            key(cx),
            [
                SqlValue::from(attempt.attempt.as_str()),
                SqlValue::from(attempt.idempotency_key.as_str()),
                SqlValue::from(attempt.provider.as_str()),
                SqlValue::from(attempt.amount),
                SqlValue::from(attempt.currency.as_str()),
                SqlValue::from(attempt.due),
                SqlValue::from(attempt.status.as_str()),
                SqlValue::from(attempt.return_url.as_deref()),
                SqlValue::from(attempt.opened_at),
                SqlValue::from(attempt.updated_at),
            ],
        ),
    )?;
    queue(
        cx,
        &ActionFor {
            kind: ActionKind::Create,
            attempt: &attempt,
            refund: None,
            amount: attempt.amount,
        },
    )?;
    Ok(opened(&attempt))
}

fn opened(attempt: &Attempt) -> Value {
    json!({
        "attempt": attempt.attempt,
        "provider": attempt.provider,
        "amount": attempt.amount,
        "currency": attempt.currency,
        "status": attempt.status.as_str(),
    })
}

/// A provider event's parameters.
struct Event<'p> {
    provider: &'p str,
    event_id: &'p str,
    kind: EventKind,
    amount: i64,
    currency: &'p str,
    at: i64,
    attempt: Option<&'p str>,
    provider_ref: Option<&'p str>,
    refund: Option<&'p str>,
    brand: Option<&'p str>,
    last4: Option<&'p str>,
    metadata: Option<&'p Value>,
}

fn event_of<'p>(params: &'p Value) -> Result<Event<'p>> {
    const OP: &str = "recordPaymentEvent";
    let kind = text_param(params, "kind")
        .and_then(EventKind::parse)
        .ok_or_else(|| params_error(OP, "/kind", "must be one of the kinds the schema lists"))?;
    Ok(Event {
        provider: text_param(params, "provider").unwrap_or_default(),
        event_id: text_param(params, "eventId").unwrap_or_default(),
        kind,
        amount: amount_param(params, "amount").unwrap_or(0),
        currency: text_param(params, "currency").unwrap_or_default(),
        at: amount_param(params, "at").unwrap_or(0),
        attempt: text_param(params, "attempt"),
        provider_ref: text_param(params, "providerRef"),
        refund: text_param(params, "refund"),
        brand: text_param(params, "brand"),
        last4: text_param(params, "last4"),
        metadata: params.get("metadata"),
    })
}

/// A provider event as Payments recorded it.
struct Recorded {
    schema: String,
    id: String,
    kind: String,
    amount: i64,
    currency: String,
    attempt: Option<String>,
    provider_ref: Option<String>,
    refund: Option<String>,
}

fn recorded_event<C>(
    view: &InstanceView<'_, C>,
    provider: &str,
    event_id: &str,
) -> Result<Option<Recorded>> {
    let table = view.sql().table("events")?;
    let row = view.sql().get(
        format!("SELECT schema, id, kind, amount, currency, attempt, provider_ref, refund FROM {table} WHERE namespace = ? AND provider = ? AND event_id = ?"),
        &[
            SqlValue::from(view.namespace()),
            SqlValue::from(provider),
            SqlValue::from(event_id),
        ],
    )?;
    row.map(|row| {
        Ok(Recorded {
            schema: row.text("schema")?.to_owned(),
            id: row.text("id")?.to_owned(),
            kind: row.text("kind")?.to_owned(),
            amount: row.i64("amount")?,
            currency: row.text("currency")?.to_owned(),
            attempt: row.opt_text("attempt")?.map(str::to_owned),
            provider_ref: row.opt_text("provider_ref")?.map(str::to_owned),
            refund: row.opt_text("refund")?.map(str::to_owned),
        })
    })
    .transpose()
}

// same_event is whether a recorded event is the one a repeat reports.
fn same_event<C>(view: &InstanceView<'_, C>, recorded: &Recorded, event: &Event<'_>) -> bool {
    recorded.schema == view.schema()
        && recorded.id == view.id()
        && recorded.kind == event.kind.as_str()
        && recorded.amount == event.amount
        && recorded.currency == event.currency
        && event
            .attempt
            .is_none_or(|attempt| recorded.attempt.as_deref() == Some(attempt))
        && event
            .provider_ref
            .is_none_or(|reference| recorded.provider_ref.as_deref() == Some(reference))
        && recorded.refund.as_deref() == event.refund
}

fn record_event_row(
    cx: &InstanceCx<'_, PaymentsConfig>,
    event: &Event<'_>,
    attempt: Option<&str>,
    applied: bool,
) -> Result<()> {
    let table = cx.sql().table("events")?;
    let metadata = event.metadata.map(stringify);
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, provider, event_id, schema, id, kind, amount, currency, at, attempt, provider_ref, refund, applied, metadata, recorded_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
        ),
        &[
            SqlValue::from(cx.namespace()),
            SqlValue::from(event.provider),
            SqlValue::from(event.event_id),
            SqlValue::from(cx.schema()),
            SqlValue::from(cx.id()),
            SqlValue::from(event.kind.as_str()),
            SqlValue::from(event.amount),
            SqlValue::from(event.currency),
            SqlValue::from(event.at),
            SqlValue::from(attempt),
            SqlValue::from(event.provider_ref),
            SqlValue::from(event.refund),
            SqlValue::from(i64::from(applied)),
            SqlValue::from(metadata),
            SqlValue::from(cx.now()),
        ],
    )?;
    Ok(())
}

fn event_result(
    view: &InstanceView<'_, PaymentsConfig>,
    applied: bool,
    attempt: Option<&str>,
) -> Result<Value> {
    let attempt = match attempt {
        Some(attempt) => attempt_where(view, "attempt", attempt)?.map(|attempt| attempt.to_json()),
        None => None,
    };
    Ok(json!({
        "applied": applied,
        "attempt": attempt,
        "state": summary(view)?.state.as_str(),
    }))
}

fn record_payment_event(
    _: &Payments,
    cx: &OperationCx<'_, PaymentsConfig>,
    params: &Value,
) -> Result<Value> {
    const OP: &str = "recordPaymentEvent";
    let permission = &cx.config().record_permission;
    if !cx.can(permission)? {
        return Err(forbidden(cx, "record a payment event of", permission));
    }
    let event = event_of(params)?;
    if let Some(recorded) = recorded_event(cx, event.provider, event.event_id)? {
        if !same_event(cx, &recorded, &event) {
            return Err(vetoed(
                cx,
                OP,
                format!(
                    "event {} of {} was recorded before, as {} of {} {} on {} {}",
                    event.event_id,
                    event.provider,
                    recorded.kind,
                    recorded.amount,
                    recorded.currency,
                    recorded.schema,
                    recorded.id
                ),
                KEY_REUSED,
                Some(
                    json!({"eventId": event.event_id, "schema": recorded.schema, "id": recorded.id}),
                ),
            ));
        }
        cx.unchanged();
        return event_result(cx, false, recorded.attempt.as_deref());
    }
    if cx.config().is_refund() {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "{} is a refund (refundOf): its outcome is recorded on the instance it refunds",
                cx.schema()
            ),
            NOT_CONFIGURED,
            None,
        ));
    }
    let refund_kind = matches!(event.kind, EventKind::Refunded | EventKind::RefundFailed);
    if refund_kind || event.refund.is_some() {
        return refund_event(cx, &event);
    }
    attempt_event(cx, &event)
}

// resolve finds the attempt an event names, by attempt id, provider
// reference or both, at the event's provider.
fn resolve(cx: &OperationCx<'_, PaymentsConfig>, event: &Event<'_>) -> Result<Attempt> {
    const OP: &str = "recordPaymentEvent";
    let found = match (event.attempt, event.provider_ref) {
        (Some(attempt), _) => attempt_where(cx, "attempt", attempt)?,
        (None, Some(reference)) => attempt_where(cx, "provider_ref", reference)?,
        (None, None) => {
            return Err(vetoed(
                cx,
                OP,
                "an event names its attempt, its provider reference, or a refund".to_owned(),
                NO_ATTEMPT,
                None,
            ))
        }
    };
    let named = event.attempt.or(event.provider_ref).unwrap_or_default();
    let Some(attempt) = found else {
        return Err(vetoed(
            cx,
            OP,
            format!("{} {} has no attempt {named}", cx.schema(), cx.id()),
            NO_ATTEMPT,
            Some(json!({"attempt": event.attempt, "providerRef": event.provider_ref})),
        ));
    };
    if attempt.provider != event.provider {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "attempt {} is at {}, not {}",
                attempt.attempt, attempt.provider, event.provider
            ),
            NO_ATTEMPT,
            Some(json!({"attempt": attempt.attempt})),
        ));
    }
    if let (Some(given), Some(known)) = (event.provider_ref, &attempt.provider_ref) {
        if given != known {
            return Err(vetoed(
                cx,
                OP,
                format!(
                    "attempt {} has the provider reference {known}, not {given}",
                    attempt.attempt
                ),
                NO_ATTEMPT,
                Some(json!({"attempt": attempt.attempt})),
            ));
        }
    }
    if event.provider_ref.is_some() {
        // A reference another attempt of the instance holds is not this one's.
        if let Some(other) =
            attempt_where(cx, "provider_ref", event.provider_ref.unwrap_or_default())?
        {
            if other.attempt != attempt.attempt {
                return Err(vetoed(
                    cx,
                    OP,
                    format!(
                        "the provider reference {} is attempt {}'s, not {}'s",
                        event.provider_ref.unwrap_or_default(),
                        other.attempt,
                        attempt.attempt
                    ),
                    NO_ATTEMPT,
                    Some(json!({"attempt": attempt.attempt})),
                ));
            }
        }
    }
    Ok(attempt)
}

fn attempt_event(cx: &OperationCx<'_, PaymentsConfig>, event: &Event<'_>) -> Result<Value> {
    const OP: &str = "recordPaymentEvent";
    let mut attempt = resolve(cx, event)?;
    if event.currency != attempt.currency {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "the event is in {}, and attempt {} is in {}",
                event.currency, attempt.attempt, attempt.currency
            ),
            CURRENCY_MISMATCH,
            Some(json!({"currency": event.currency, "expected": attempt.currency})),
        ));
    }
    let Some(to) = event.kind.status() else {
        return Err(defect("a refund's outcome reached the attempt's"));
    };
    let from = attempt.status;
    let mut applied = false;
    if to == AttemptStatus::Captured {
        // Money the provider captured is counted, whatever came before it.
        let capturable = attempt.capturable();
        if attempt.captured + event.amount > capturable {
            return Err(vetoed(
                cx,
                OP,
                format!(
                    "attempt {} has {} captured of {capturable} it may capture, so {} more is past it",
                    attempt.attempt, attempt.captured, event.amount
                ),
                OVER_CAPTURE,
                Some(json!({
                    "attempt": attempt.attempt,
                    "captured": attempt.captured,
                    "capturable": capturable,
                    "amount": event.amount,
                })),
            ));
        }
        attempt.captured += event.amount;
        attempt.status = AttemptStatus::Captured;
        applied = event.amount > 0 || from != AttemptStatus::Captured;
    } else if to.rank() > from.rank() {
        if to == AttemptStatus::Authorized {
            attempt.authorized = event.amount;
        }
        attempt.status = to;
        applied = true;
    }
    if attempt.provider_ref.is_none() {
        attempt.provider_ref = event.provider_ref.map(str::to_owned);
    }
    if applied {
        if let Some(brand) = event.brand {
            attempt.brand = Some(brand.to_owned());
        }
        if let Some(last4) = event.last4 {
            attempt.last4 = Some(last4.to_owned());
        }
    }
    save_attempt(cx, &attempt)?;
    // The create action is done once the provider reports anything of the
    // attempt; a capture and a void once the attempt is captured, failed or
    // cancelled.
    let mut done = vec![ActionKind::Create];
    if !attempt.status.in_flight() {
        done.extend([ActionKind::Capture, ActionKind::Void]);
    }
    finish(cx, &attempt.attempt, &done, None)?;
    record_event_row(cx, event, Some(&attempt.attempt), applied)?;
    // Money captured after the instance let go of what it paid for.
    if applied
        && to == AttemptStatus::Captured
        && event.amount > 0
        && cx.config().refund_after_release
    {
        if let Some(status) = status_of(cx)? {
            if cx.config().void_on.contains(&status) {
                let key = format!("release:{}", event.event_id);
                let key: String = key.chars().take(255).collect();
                let reason = format!("captured after {} {} was {status}", cx.schema(), cx.id());
                let attempts = attempts(cx)?;
                let refunds = refunds(cx)?;
                insert_refund(
                    cx,
                    &Asked {
                        operation: OP,
                        amount: event.amount,
                        reason: &reason,
                        key: &key,
                        origin: None,
                    },
                    &attempts,
                    &refunds,
                )?;
            }
        }
    }
    event_result(cx, applied, Some(&attempt.attempt))
}

fn refund_event(cx: &OperationCx<'_, PaymentsConfig>, event: &Event<'_>) -> Result<Value> {
    const OP: &str = "recordPaymentEvent";
    let Some(id) = event.refund else {
        return Err(vetoed(
            cx,
            OP,
            format!("a {} event names its refund", event.kind.as_str()),
            NO_ATTEMPT,
            None,
        ));
    };
    let Some(mut refund) = refund_where(cx, "refund", id)? else {
        return Err(vetoed(
            cx,
            OP,
            format!("{} {} has no refund {id}", cx.schema(), cx.id()),
            NO_ATTEMPT,
            Some(json!({"refund": id})),
        ));
    };
    if refund.provider != event.provider
        || event
            .attempt
            .is_some_and(|attempt| attempt != refund.attempt)
    {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "refund {id} draws on attempt {} at {}",
                refund.attempt, refund.provider
            ),
            NO_ATTEMPT,
            Some(json!({"refund": id})),
        ));
    }
    if event.currency != refund.currency {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "the event is in {}, and refund {id} is in {}",
                event.currency, refund.currency
            ),
            CURRENCY_MISMATCH,
            Some(json!({"currency": event.currency, "expected": refund.currency})),
        ));
    }
    let to = match event.kind {
        EventKind::Refunded => RefundStatus::Succeeded,
        EventKind::RefundFailed => RefundStatus::Failed,
        EventKind::Processing | EventKind::RequiresAction => RefundStatus::Sent,
        other => {
            return Err(params_error(
                OP,
                "/refund",
                format!(
                    "names a refund, and a {} event is an attempt's",
                    other.as_str()
                ),
            ))
        }
    };
    // A refund moves only forward: requested, sent, then its outcome.
    let applied = refund.status.pending()
        && (to != RefundStatus::Sent || refund.status == RefundStatus::Requested);
    if applied && to == RefundStatus::Succeeded && event.amount > refund.amount {
        return Err(vetoed(
            cx,
            OP,
            format!("refund {id} asked {}, not {}", refund.amount, event.amount),
            EXCEEDS_REFUNDABLE,
            Some(json!({"refundable": refund.amount, "refund": id})),
        ));
    }
    if applied {
        refund.status = to;
        if to == RefundStatus::Succeeded {
            refund.refunded = event.amount;
        }
        save_refund(cx, &refund)?;
        if !to.pending() {
            finish(cx, &refund.attempt, &[ActionKind::Refund], Some(id))?;
        }
    }
    record_event_row(cx, event, Some(&refund.attempt), applied)?;
    if applied && !to.pending() {
        if let Some(origin) = &refund.origin {
            cx.instances().invoke(
                &origin.schema,
                &origin.id,
                "settleRefundFor",
                &json!({
                    "of": {"schema": cx.schema(), "id": cx.id()},
                    "refund": refund.refund,
                    "status": to.as_str(),
                    "amount": refund.refunded,
                }),
                None,
            )?;
        }
    }
    event_result(cx, applied, Some(&refund.attempt))
}

// attempt_named reads an attempt a capture or a void names.
fn attempt_named(
    cx: &OperationCx<'_, PaymentsConfig>,
    operation: &str,
    params: &Value,
) -> Result<Attempt> {
    let named = text_param(params, "attempt").unwrap_or_default();
    attempt_where(cx, "attempt", named)?.ok_or_else(|| {
        vetoed(
            cx,
            operation,
            format!("{} {} has no attempt {named}", cx.schema(), cx.id()),
            NO_ATTEMPT,
            Some(json!({"attempt": named})),
        )
    })
}

fn capture_payment(
    _: &Payments,
    cx: &OperationCx<'_, PaymentsConfig>,
    params: &Value,
) -> Result<Value> {
    const OP: &str = "capturePayment";
    let attempt = attempt_named(cx, OP, params)?;
    if attempt.status != AttemptStatus::Authorized {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "attempt {} is {}, and only an authorized one is captured",
                attempt.attempt,
                attempt.status.as_str()
            ),
            ATTEMPT_CLOSED,
            Some(json!({"attempt": attempt.attempt, "status": attempt.status.as_str()})),
        ));
    }
    let asked = amount_param(params, "amount");
    if let Some((action, amount)) = find_action(cx, &attempt.attempt, ActionKind::Capture, true)? {
        if asked.is_none_or(|asked| asked == amount) {
            cx.unchanged();
            return Ok(action_json(&action, ActionKind::Capture, &attempt, amount));
        }
    }
    let left = attempt.authorized - attempt.captured - pending_captures(cx, &attempt.attempt)?;
    let amount = asked.unwrap_or(left);
    if amount <= 0 || amount > left {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "attempt {} has {} authorized left to capture, not {amount}",
                attempt.attempt,
                left.max(0)
            ),
            OVER_CAPTURE,
            Some(json!({"attempt": attempt.attempt, "capturable": left.max(0), "amount": amount})),
        ));
    }
    let action = queue(
        cx,
        &ActionFor {
            kind: ActionKind::Capture,
            attempt: &attempt,
            refund: None,
            amount,
        },
    )?;
    Ok(action_json(&action, ActionKind::Capture, &attempt, amount))
}

fn void_amount(attempt: &Attempt) -> i64 {
    if attempt.status == AttemptStatus::Authorized {
        attempt.holding()
    } else {
        attempt.amount
    }
}

fn void_payment(
    _: &Payments,
    cx: &OperationCx<'_, PaymentsConfig>,
    params: &Value,
) -> Result<Value> {
    const OP: &str = "voidPayment";
    let attempt = attempt_named(cx, OP, params)?;
    if !attempt.status.voidable() {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "attempt {} is {}, and only an open or authorized one is voided",
                attempt.attempt,
                attempt.status.as_str()
            ),
            ATTEMPT_CLOSED,
            Some(json!({"attempt": attempt.attempt, "status": attempt.status.as_str()})),
        ));
    }
    if let Some((action, amount)) = find_action(cx, &attempt.attempt, ActionKind::Void, true)? {
        cx.unchanged();
        return Ok(action_json(&action, ActionKind::Void, &attempt, amount));
    }
    let amount = void_amount(&attempt);
    let action = queue(
        cx,
        &ActionFor {
            kind: ActionKind::Void,
            attempt: &attempt,
            refund: None,
            amount,
        },
    )?;
    Ok(action_json(&action, ActionKind::Void, &attempt, amount))
}

fn refund_payment(
    _: &Payments,
    cx: &OperationCx<'_, PaymentsConfig>,
    params: &Value,
) -> Result<Value> {
    const OP: &str = "refundPayment";
    let Some(permission) = &cx.config().refund_permission else {
        return Err(vetoed(
            cx,
            OP,
            "its config names no refunds permission, which refunding directly needs; a refund instance (refundOf) refunds it".to_owned(),
            NOT_CONFIGURED,
            None,
        ));
    };
    if !cx.can(permission)? {
        return Err(forbidden(cx, "refund", permission));
    }
    record_refund(
        cx,
        &Asked {
            operation: OP,
            amount: amount_param(params, "amount").unwrap_or(0),
            reason: text_param(params, "reason").unwrap_or_default(),
            key: text_param(params, "idempotencyKey").unwrap_or_default(),
            origin: None,
        },
    )
}

fn refund_for(_: &Payments, cx: &OperationCx<'_, PaymentsConfig>, params: &Value) -> Result<Value> {
    const OP: &str = "refundFor";
    let refund = params
        .get("refund")
        .and_then(InstanceRef::from_json)
        .ok_or_else(|| params_error(OP, "/refund", "must be {schema, id}"))?;
    let amount = amount_param(params, "amount").unwrap_or(0);
    let refuse = |reason: String| {
        vetoed(
            cx,
            OP,
            reason,
            NOT_LINKED,
            Some(json!({"refund": refund.to_json()})),
        )
    };
    if cx.config().is_refund() {
        return Err(refuse(format!(
            "{} is a refund itself, and refunds nothing of its own",
            cx.schema()
        )));
    }
    // The refund instance's schema says which link names what it refunds.
    let theirs = cx.schemas().config(&refund.schema, NAME)?;
    let link = theirs
        .as_ref()
        .and_then(|config| text_at(config, &["refundOf", "link"]))
        .map(str::to_owned);
    let Some(link) = link else {
        return Err(refuse(format!(
            "{} is not a refund: its Payments names no refundOf",
            refund.schema
        )));
    };
    let issue_on = strings(theirs.as_ref().and_then(|config| config.get("issueOn")));
    let record = cx.instances().get(
        &refund.schema,
        &refund.id,
        Some(&[
            LINKS_TARGETS,
            WORKFLOW_STATUS,
            PAYMENTS_DUE,
            PAYMENTS_CURRENCY,
        ]),
    )?;
    let Some(record) = record else {
        return Err(refuse(format!(
            "{} {} does not exist",
            refund.schema, refund.id
        )));
    };
    let target = record
        .behavior_field("Links", "targets")
        .and_then(|targets| targets.get(&link))
        .and_then(InstanceRef::from_json);
    if target
        .as_ref()
        .is_none_or(|target| target.schema != cx.schema() || target.id != cx.id())
    {
        return Err(refuse(format!(
            "{} {}'s {link} link does not point at {} {}",
            refund.schema,
            refund.id,
            cx.schema(),
            cx.id()
        )));
    }
    let status = record
        .behavior_field("Workflow", "status")
        .and_then(Value::as_str);
    if !status.is_some_and(|status| issue_on.iter().any(|state| state == status)) {
        return Err(refuse(format!(
            "{} {} is {}, not in a state that issues it ({})",
            refund.schema,
            refund.id,
            status.unwrap_or("without a status"),
            issue_on.join(", ")
        )));
    }
    let due = record
        .behavior_field(NAME, "due")
        .and_then(integer)
        .unwrap_or(0);
    if due != amount {
        return Err(refuse(format!(
            "{} {} refunds {due}, not {amount}",
            refund.schema, refund.id
        )));
    }
    let theirs_currency = record
        .behavior_field(NAME, "currency")
        .and_then(Value::as_str);
    let mine = attempts(cx)?.last().map(|attempt| attempt.currency.clone());
    if theirs_currency != mine.as_deref() {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "{} {} refunds in {}, and {} {} was paid in {}",
                refund.schema,
                refund.id,
                theirs_currency.unwrap_or("no currency"),
                cx.schema(),
                cx.id(),
                mine.as_deref().unwrap_or("no currency")
            ),
            CURRENCY_MISMATCH,
            Some(json!({"currency": theirs_currency, "expected": mine})),
        ));
    }
    let reason = format!("refund {} {}", refund.schema, refund.id);
    let answer = record_refund(
        cx,
        &Asked {
            operation: OP,
            amount,
            reason: &reason,
            key: text_param(params, "idempotencyKey").unwrap_or_default(),
            origin: Some(&refund),
        },
    )?;
    Ok(json!({
        "refund": answer["refund"],
        "action": answer["action"],
        "amount": answer["amount"],
        "currency": answer["currency"],
    }))
}

fn settle_refund_for(
    _: &Payments,
    cx: &OperationCx<'_, PaymentsConfig>,
    params: &Value,
) -> Result<Value> {
    const OP: &str = "settleRefundFor";
    let permission = &cx.config().record_permission;
    if !cx.can(permission)? {
        return Err(forbidden(cx, "settle a refund of", permission));
    }
    let of = params
        .get("of")
        .and_then(InstanceRef::from_json)
        .ok_or_else(|| params_error(OP, "/of", "must be {schema, id}"))?;
    let named = text_param(params, "refund").unwrap_or_default();
    let Some(link) = cx.config().refund_of.clone() else {
        return Err(vetoed(
            cx,
            OP,
            format!("{} is not a refund (refundOf)", cx.schema()),
            NOT_LINKED,
            None,
        ));
    };
    let target = link_target(cx, cx.schema(), cx.id(), &link)?;
    if target.as_ref() != Some(&of) {
        return Err(vetoed(
            cx,
            OP,
            format!(
                "{} {} refunds what its {link} link points at, not {} {}",
                cx.schema(),
                cx.id(),
                of.schema,
                of.id
            ),
            NOT_LINKED,
            Some(json!({"of": of.to_json()})),
        ));
    }
    let Some(mut issue) = issues(cx)?
        .into_iter()
        .find(|issue| issue.refund == named && issue.of == of)
    else {
        return Err(vetoed(
            cx,
            OP,
            format!("{} {} asked no refund {named}", cx.schema(), cx.id()),
            NO_ATTEMPT,
            Some(json!({"refund": named})),
        ));
    };
    let to = match text_param(params, "status") {
        Some("succeeded") => RefundStatus::Succeeded,
        _ => RefundStatus::Failed,
    };
    if issue.status.pending() {
        issue.status = to;
        issue.refunded = if to == RefundStatus::Succeeded {
            amount_param(params, "amount").unwrap_or(0)
        } else {
            0
        };
        let table = cx.sql().table("issues")?;
        cx.sql().run(
            format!("UPDATE {table} SET status = ?, refunded = ?, updated_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND refund = ?"),
            &[
                SqlValue::from(issue.status.as_str()),
                SqlValue::from(issue.refunded),
                SqlValue::from(cx.now()),
                SqlValue::from(cx.namespace()),
                SqlValue::from(cx.schema()),
                SqlValue::from(cx.id()),
                SqlValue::from(issue.refund.as_str()),
            ],
        )?;
    } else {
        cx.unchanged();
    }
    Ok(json!({ "state": summary(cx)?.state.as_str() }))
}

fn list_payment_actions(
    _: &Payments,
    cx: &SchemaCx<'_, PaymentsConfig>,
    params: &Value,
) -> Result<Value> {
    const OP: &str = "listPaymentActions";
    let permission = &cx.config().record_permission;
    if !cx.can(permission)? {
        return Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not list the payment actions of {}: it needs permission {permission}",
                cx.principal().subject,
                cx.schema()
            ),
        )
        .into());
    }
    let request = page_request(NAME, OP, params)?;
    let provider = text_param(params, "provider").unwrap_or_default();
    let actions = cx.sql().table("actions")?;
    let attempts = cx.sql().table("attempts")?;
    let rows = cx.sql().all(
        format!(
            "SELECT a.seq AS seq, a.action AS action, a.kind AS kind, a.attempt AS attempt, a.refund AS refund, a.amount AS amount,
                    a.currency AS currency, a.id AS id, a.at AS at, t.provider_ref AS provider_ref, t.return_url AS return_url
             FROM {actions} a LEFT JOIN {attempts} t
               ON t.namespace = a.namespace AND t.schema = a.schema AND t.id = a.id AND t.attempt = a.attempt
             WHERE a.namespace = ? AND a.schema = ? AND a.provider = ? AND a.done_at IS NULL AND a.seq > ?
             ORDER BY a.seq LIMIT ?"
        ),
        &[
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::from(provider),
            SqlValue::Integer(request.after),
            SqlValue::Integer(request.limit + 1),
        ],
    )?;
    let found = page(rows, request.limit, |row| {
        row.i64("seq").unwrap_or_default()
    });
    let items = found
        .items
        .iter()
        .map(|row| {
            let kind = row.text("kind")?;
            let mut out = Map::new();
            out.insert("action".into(), Value::from(row.text("action")?));
            out.insert("kind".into(), Value::from(kind));
            out.insert("attempt".into(), Value::from(row.text("attempt")?));
            out.insert("amount".into(), Value::from(row.i64("amount")?));
            out.insert("currency".into(), Value::from(row.text("currency")?));
            out.insert("id".into(), Value::from(row.text("id")?));
            out.insert("at".into(), Value::from(row.i64("at")?));
            if let Some(reference) = row.opt_text("provider_ref")? {
                out.insert("providerRef".into(), Value::from(reference));
            }
            if let Some(refund) = row.opt_text("refund")? {
                out.insert("refund".into(), Value::from(refund));
            }
            if kind == ActionKind::Create.as_str() {
                if let Some(url) = row.opt_text("return_url")? {
                    out.insert("returnUrl".into(), Value::from(url));
                }
            }
            Ok(Value::Object(out))
        })
        .collect::<Result<Vec<_>>>()?;
    Ok(Page {
        items,
        next: found.next,
    }
    .to_json())
}

// ---------------------------------------------------------------------------
// What follows a change
// ---------------------------------------------------------------------------

// void_open gives each attempt a provider can still cancel or void one
// void action, in a voidOn state.
fn void_open(cx: &InstanceCx<'_, PaymentsConfig>) -> Result<()> {
    for attempt in attempts(cx)? {
        if !attempt.status.voidable()
            || find_action(cx, &attempt.attempt, ActionKind::Void, false)?.is_some()
        {
            continue;
        }
        queue(
            cx,
            &ActionFor {
                kind: ActionKind::Void,
                attempt: &attempt,
                refund: None,
                amount: void_amount(&attempt),
            },
        )?;
    }
    Ok(())
}

// capture_authorized gives each authorized attempt one capture action of
// what it holds, in a captureOn state.
fn capture_authorized(cx: &InstanceCx<'_, PaymentsConfig>) -> Result<()> {
    for attempt in attempts(cx)? {
        if attempt.status != AttemptStatus::Authorized
            || attempt.holding() <= 0
            || find_action(cx, &attempt.attempt, ActionKind::Capture, false)?.is_some()
        {
            continue;
        }
        queue(
            cx,
            &ActionFor {
                kind: ActionKind::Capture,
                attempt: &attempt,
                refund: None,
                amount: attempt.holding(),
            },
        )?;
    }
    Ok(())
}

// issue asks the instance a refund instance refunds for its amount, once
// per issue: again only after the last one failed and the instance entered
// an issueOn state again since it was asked.
fn issue(cx: &InstanceCx<'_, PaymentsConfig>, action: &str) -> Result<()> {
    let all = issues(cx)?;
    let entered_at = own_record(cx, &[WORKFLOW_ENTERED_AT])?
        .as_ref()
        .and_then(|record| record.behavior_field("Workflow", "enteredAt"))
        .and_then(integer);
    if let Some(last) = all.last() {
        if last.status != RefundStatus::Failed || last.entered_at == entered_at {
            return Ok(());
        }
    }
    let link = cx.config().refund_of.clone().unwrap_or_default();
    let Some(of) = link_target(cx, cx.schema(), cx.id(), &link)? else {
        return Err(vetoed(
            cx,
            action,
            format!(
                "{} {}'s {link} link points at nothing to refund",
                cx.schema(),
                cx.id()
            ),
            NOT_LINKED,
            None,
        ));
    };
    let live = source(cx)?;
    if live.due <= 0 {
        return Err(vetoed(
            cx,
            action,
            format!(
                "{} {} refunds nothing: its amount is 0",
                cx.schema(),
                cx.id()
            ),
            NOTHING_DUE,
            None,
        ));
    }
    let me = InstanceRef::new(cx.schema(), cx.id());
    let idempotency_key = issue_key(&me, all.len());
    let answer = cx.instances().invoke(
        &of.schema,
        &of.id,
        "refundFor",
        &json!({"refund": me.to_json(), "amount": live.due, "idempotencyKey": idempotency_key}),
        None,
    )?;
    let refund = answer
        .get("refund")
        .and_then(Value::as_str)
        .ok_or_else(|| defect("refundFor returned no refund"))?;
    let currency = answer
        .get("currency")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let table = cx.sql().table("issues")?;
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, of_schema, of_id, refund, idempotency_key, amount, currency, status, refunded, entered_at, at, updated_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'requested', 0, ?, ?, ?)"
        ),
        &with(
            key(cx),
            [
                SqlValue::from(of.schema.as_str()),
                SqlValue::from(of.id.as_str()),
                SqlValue::from(refund),
                SqlValue::from(idempotency_key.as_str()),
                SqlValue::from(live.due),
                SqlValue::from(currency),
                SqlValue::from(entered_at),
                SqlValue::from(cx.now()),
                SqlValue::from(cx.now()),
            ],
        ),
    )?;
    Ok(())
}

// holds_locked refuses a change that moved Holds.total while an attempt is
// in flight: what was opened is what is paid.
fn holds_locked(cx: &InstanceCx<'_, PaymentsConfig>, action: &str) -> Result<()> {
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
        return Err(vetoed(
            cx,
            action,
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
        ));
    }
    Ok(())
}

fn forget(cx: &InstanceCx<'_, PaymentsConfig>) -> Result<()> {
    for table in ["attempts", "actions", "refunds", "issues"] {
        let table = cx.sql().table(table)?;
        cx.sql().run(
            format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
            &key(cx),
        )?;
    }
    // The events stay, keyed by provider and event id, so a provider's
    // redelivery is still a repeat.
    Ok(())
}

fn after_change(
    _: &Payments,
    cx: &InstanceCx<'_, PaymentsConfig>,
    change: &InstanceChange<'_>,
) -> Result<()> {
    if *change == InstanceChange::Delete {
        return forget(cx);
    }
    let action = match change {
        InstanceChange::Operation { operation, .. } => *operation,
        other => other.kind(),
    };
    if let Some(status) = status_of(cx)? {
        let config = cx.config();
        if config.is_refund() && config.issue_on.contains(&status) {
            issue(cx, action)?;
        }
        if config.void_on.contains(&status) {
            void_open(cx)?;
        }
        if config.manual_capture && config.capture_on.contains(&status) {
            capture_authorized(cx)?;
        }
    }
    if cx.config().amount == AmountSource::Holds {
        holds_locked(cx, action)?;
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// The guard
// ---------------------------------------------------------------------------

fn guard(
    _: &Payments,
    view: &InstanceView<'_, PaymentsConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    match request {
        GuardRequest::Create { .. } => Ok(None),
        GuardRequest::Operation {
            behavior: "Workflow",
            operation: "transition",
            params,
            ..
        } => {
            let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
            guard_move(view, to)
        }
        GuardRequest::Operation {
            behavior: "Links",
            operation: "link" | "unlink",
            params,
            ..
        } => {
            let Some(link) = &view.config().refund_of else {
                return Ok(None);
            };
            if params.get("name").and_then(Value::as_str) != Some(link.as_str()) {
                return Ok(None);
            }
            let issued = issues(view)?
                .into_iter()
                .rev()
                .find(|issue| issue.status != RefundStatus::Failed);
            Ok(issued.map(|issue| {
                veto(
                    format!(
                        "{} {} asked {} {} for refund {}; its {link} link stays",
                        view.schema(),
                        view.id(),
                        issue.of.schema,
                        issue.of.id,
                        issue.refund
                    ),
                    NOT_LINKED,
                    Some(json!({"refund": issue.refund})),
                )
            }))
        }
        GuardRequest::Update { patch, after, .. } => guard_update(view, patch, after),
        GuardRequest::Delete { .. } => {
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
            let pending = issues(view)?
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
fn guard_move(view: &InstanceView<'_, PaymentsConfig>, to: &str) -> Result<Option<Veto>> {
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
                view.schema(),
                view.id()
            ),
            PAYMENT_IN_PROGRESS,
            Some(json!({"attempt": attempt.attempt, "to": to})),
        )));
    }
    let Some(requirement) = config.requirement(to) else {
        return Ok(None);
    };
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
            view.schema(),
            view.id(),
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
// has issued), and the currency while any attempt exists.
fn guard_update(
    view: &InstanceView<'_, PaymentsConfig>,
    patch: &Map<String, Value>,
    after: &Map<String, Value>,
) -> Result<Option<Veto>> {
    let config = view.config();
    let amount_field = config
        .amount_field()
        .filter(|field| patch.contains_key(*field));
    let currency_field = config
        .currency_field()
        .filter(|field| patch.contains_key(*field));
    if amount_field.is_none() && currency_field.is_none() {
        return Ok(None);
    }
    let before = view.data()?;
    let moved = |field: &str| before.get(field) != after.get(field);
    let amount_moved = amount_field.is_some_and(moved);
    let currency_moved = currency_field.is_some_and(moved);
    if !amount_moved && !currency_moved {
        return Ok(None);
    }
    if config.is_refund() {
        let issued = issues(view)?
            .into_iter()
            .rev()
            .find(|issue| issue.status != RefundStatus::Failed);
        return Ok(issued.map(|issue| {
            veto(
                format!(
                    "{} {} asked refund {} of {} {}; its amount and currency stay",
                    view.schema(),
                    view.id(),
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
                    amount_field.unwrap_or_default()
                ),
                AMOUNT_LOCKED,
                Some(json!({"attempt": flight.attempt, "due": flight.due})),
            )));
        }
    }
    if currency_moved {
        if let Some(last) = attempts.last() {
            return Ok(Some(veto(
                format!(
                    "{} {} has attempts in {}; {} stays",
                    view.schema(),
                    view.id(),
                    last.currency,
                    currency_field.unwrap_or_default()
                ),
                CURRENCY_MISMATCH,
                Some(
                    json!({"currency": after.get(currency_field.unwrap_or_default()), "expected": last.currency}),
                ),
            )));
        }
    }
    Ok(None)
}

// ---------------------------------------------------------------------------
// The behavior
// ---------------------------------------------------------------------------

/// Payments: what an instance owes, and the attempts, actions and refunds
/// that settle it.
#[derive(Clone, Copy, Debug, Default)]
pub struct Payments;

fn summary_field(
    view: &InstanceView<'_, PaymentsConfig>,
    read: fn(&Summary) -> Value,
) -> Result<Option<Value>> {
    Ok(Some(read(&summary(view)?)))
}

impl Behavior for Payments {
    type Config = PaymentsConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Payments.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to the type:
    // its fields, its Workflow's states, its Links config, Holds' place,
    // and a literal currency to the ISO 4217 list.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<PaymentsConfig> {
        parse_config(raw, target)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guidance: Some(|_, config, target| guidance::guidance(config, target)),
            config_change: Some(|_, before, after, change| {
                let (Some(before), Some(after)) = (before, after) else {
                    if before.is_some() && change.instances()? {
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
                if !change.instances()? {
                    return Ok(None);
                }
                Ok(Some(
                    "the currency, the source of the amount and refundOf stay while the schema has instances, whose payments were made in them"
                        .to_owned(),
                ))
            }),
            migrations: vec![BehaviorMigration::new(1, "payments")
                .table(
                    TableSpec::new("attempts")
                        .row_id("seq")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("attempt", ColumnSpec::text().not_null())
                        .column("idempotency_key", ColumnSpec::text().not_null())
                        .column("provider", ColumnSpec::text().not_null())
                        .column("provider_ref", ColumnSpec::text())
                        .column("amount", ColumnSpec::integer().not_null())
                        .column("currency", ColumnSpec::text().not_null())
                        .column("due", ColumnSpec::integer().not_null())
                        .column("status", ColumnSpec::text().not_null())
                        .column("authorized", ColumnSpec::integer().not_null().default(0))
                        .column("captured", ColumnSpec::integer().not_null().default(0))
                        .column("brand", ColumnSpec::text())
                        .column("last4", ColumnSpec::text())
                        .column("return_url", ColumnSpec::text())
                        .column("opened_at", ColumnSpec::integer().not_null())
                        .column("updated_at", ColumnSpec::integer().not_null())
                        .index(IndexSpec::new("attempts_by_id", &["namespace", "attempt"]).unique())
                        .index(
                            IndexSpec::new(
                                "attempts_by_key",
                                &["namespace", "schema", "id", "idempotency_key"],
                            )
                            .unique(),
                        )
                        .index(IndexSpec::new(
                            "attempts_by_instance",
                            &["namespace", "schema", "id", "seq"],
                        ))
                        .index(IndexSpec::new(
                            "attempts_by_ref",
                            &["namespace", "schema", "id", "provider_ref"],
                        )),
                )
                .table(
                    TableSpec::new("actions")
                        .row_id("seq")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("action", ColumnSpec::text().not_null())
                        .column("kind", ColumnSpec::text().not_null())
                        .column("attempt", ColumnSpec::text().not_null())
                        .column("refund", ColumnSpec::text())
                        .column("provider", ColumnSpec::text().not_null())
                        .column("amount", ColumnSpec::integer().not_null())
                        .column("currency", ColumnSpec::text().not_null())
                        .column("at", ColumnSpec::integer().not_null())
                        .column("done_at", ColumnSpec::integer())
                        .index(IndexSpec::new("actions_by_id", &["namespace", "action"]).unique())
                        .index(
                            IndexSpec::new(
                                "actions_waiting",
                                &["namespace", "schema", "provider", "seq"],
                            )
                            .filter("done_at IS NULL"),
                        )
                        .index(IndexSpec::new(
                            "actions_by_attempt",
                            &["namespace", "schema", "id", "attempt"],
                        )),
                )
                .table(
                    TableSpec::new("refunds")
                        .row_id("seq")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("refund", ColumnSpec::text().not_null())
                        .column("idempotency_key", ColumnSpec::text().not_null())
                        .column("attempt", ColumnSpec::text().not_null())
                        .column("provider", ColumnSpec::text().not_null())
                        .column("amount", ColumnSpec::integer().not_null())
                        .column("currency", ColumnSpec::text().not_null())
                        .column("reason", ColumnSpec::text().not_null())
                        .column("status", ColumnSpec::text().not_null())
                        .column("refunded", ColumnSpec::integer().not_null().default(0))
                        .column("origin_schema", ColumnSpec::text())
                        .column("origin_id", ColumnSpec::text())
                        .column("at", ColumnSpec::integer().not_null())
                        .column("updated_at", ColumnSpec::integer().not_null())
                        .index(IndexSpec::new("refunds_by_id", &["namespace", "refund"]).unique())
                        .index(
                            IndexSpec::new(
                                "refunds_by_key",
                                &["namespace", "schema", "id", "idempotency_key"],
                            )
                            .unique(),
                        ),
                )
                .table(
                    TableSpec::new("issues")
                        .row_id("seq")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("of_schema", ColumnSpec::text().not_null())
                        .column("of_id", ColumnSpec::text().not_null())
                        .column("refund", ColumnSpec::text().not_null())
                        .column("idempotency_key", ColumnSpec::text().not_null())
                        .column("amount", ColumnSpec::integer().not_null())
                        .column("currency", ColumnSpec::text().not_null())
                        .column("status", ColumnSpec::text().not_null())
                        .column("refunded", ColumnSpec::integer().not_null().default(0))
                        .column("entered_at", ColumnSpec::integer())
                        .column("at", ColumnSpec::integer().not_null())
                        .column("updated_at", ColumnSpec::integer().not_null())
                        .index(IndexSpec::new(
                            "issues_by_instance",
                            &["namespace", "schema", "id", "seq"],
                        )),
                )
                .table(
                    TableSpec::new("events")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("provider", ColumnSpec::text().not_null())
                        .column("event_id", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("kind", ColumnSpec::text().not_null())
                        .column("amount", ColumnSpec::integer().not_null())
                        .column("currency", ColumnSpec::text().not_null())
                        .column("at", ColumnSpec::integer().not_null())
                        .column("attempt", ColumnSpec::text())
                        .column("provider_ref", ColumnSpec::text())
                        .column("refund", ColumnSpec::text())
                        .column("applied", ColumnSpec::integer().not_null())
                        .column("metadata", ColumnSpec::text())
                        .column("recorded_at", ColumnSpec::integer().not_null())
                        .primary_key(&["namespace", "provider", "event_id"]),
                )],
            guard: Some(guard),
            operations: vec![
                ("openPayment", open_payment),
                ("recordPaymentEvent", record_payment_event),
                ("capturePayment", capture_payment),
                ("voidPayment", void_payment),
                ("refundPayment", refund_payment),
                ("refundFor", refund_for),
                ("settleRefundFor", settle_refund_for),
            ],
            schema_operations: vec![("listPaymentActions", list_payment_actions)],
            fields: vec![
                ("due", |_, view| summary_field(view, |s| Value::from(s.due))),
                ("authorized", |_, view| {
                    summary_field(view, |s| Value::from(s.authorized))
                }),
                ("captured", |_, view| {
                    summary_field(view, |s| Value::from(s.captured))
                }),
                ("refunded", |_, view| {
                    summary_field(view, |s| Value::from(s.refunded))
                }),
                ("outstanding", |_, view| {
                    summary_field(view, |s| Value::from(s.outstanding))
                }),
                ("currency", |_, view| {
                    Ok(summary(view)?.currency.map(Value::from))
                }),
                ("state", |_, view| {
                    summary_field(view, |s| Value::from(s.state.as_str()))
                }),
                ("attempts", |_, view| {
                    let mut all = attempts(view)?;
                    all.reverse();
                    all.truncate(ATTEMPTS_SHOWN);
                    Ok(Some(Value::Array(
                        all.iter().map(Attempt::to_json).collect(),
                    )))
                }),
            ],
            after_change: Some(after_change),
            ..Hooks::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_attempt_moves_only_forward() {
        use AttemptStatus::*;
        let order: Vec<AttemptStatus> = {
            let mut all = AttemptStatus::ALL.to_vec();
            all.sort_by_key(|status| status.rank());
            all
        };
        assert_eq!(
            order,
            [
                Open,
                RequiresAction,
                Processing,
                Failed,
                Authorized,
                Cancelled,
                Captured
            ]
        );
        for status in AttemptStatus::ALL {
            assert_eq!(AttemptStatus::parse(status.as_str()), Some(status));
        }
        assert!(Open.in_flight() && Processing.in_flight() && Authorized.in_flight());
        assert!(!Captured.in_flight() && !Failed.in_flight() && !Cancelled.in_flight());
        assert!(!Processing.voidable() && Authorized.voidable() && Open.voidable());
    }

    #[test]
    fn an_issue_key_fits_the_key_pattern() {
        let plain = issue_key(&InstanceRef::new("Refund", "r-1"), 0);
        assert_eq!(plain, "Refund:r-1.0");
        let odd = issue_key(&InstanceRef::new("Refund", "r 1/é"), 2);
        assert!(odd.starts_with("Refund:h") && odd.ends_with(".2"), "{odd}");
        assert!(odd.bytes().all(key_char));
        assert_ne!(odd, issue_key(&InstanceRef::new("Refund", "r 1/è"), 2));
        let long = issue_key(&InstanceRef::new("Refund", "x".repeat(300)), 1);
        assert!(long.len() <= 255 && long.bytes().all(key_char));
    }

    #[test]
    fn each_event_kind_reads_back() {
        for kind in [
            EventKind::RequiresAction,
            EventKind::Processing,
            EventKind::Authorized,
            EventKind::Captured,
            EventKind::Failed,
            EventKind::Cancelled,
            EventKind::Refunded,
            EventKind::RefundFailed,
        ] {
            assert_eq!(EventKind::parse(kind.as_str()), Some(kind));
            assert_eq!(
                kind.status().is_none(),
                matches!(kind, EventKind::Refunded | EventKind::RefundFailed)
            );
        }
    }
}
