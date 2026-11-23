//! What Payments keeps of a row, its sub-tables' rows read and written
//! (D70 on a table, D103's step 4): the statuses and kinds, an attempt, an
//! action, a refund and a refund row's issue, each as a sub-table row of
//! the use keyed by its id, with a `seq` that orders the row's rows of one
//! table as the engine's row ids did; and a provider event as a row of the
//! use's shared table, keyed by the provider and the event id.

use serde_json::{json, Map, Value};
use superschematic_rules::{SharedRow, SubRow};

use crate::supply::{integer, RowRef};

/// The sub-tables, by their declared names.
pub const ATTEMPTS: &str = "attempts";
pub const ACTIONS: &str = "actions";
pub const REFUNDS: &str = "refunds";
pub const ISSUES: &str = "issues";
/// The shared table of the provider events.
pub const EVENTS: &str = "events";

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

fn text(row: &SubRow, field: &str) -> Option<String> {
    row.value(field).and_then(Value::as_str).map(str::to_owned)
}

fn number(row: &SubRow, field: &str) -> Option<i64> {
    row.value(field).and_then(integer)
}

fn or_null<T: Into<Value>>(value: Option<T>) -> Value {
    value.map_or(Value::Null, Into::into)
}

/// One attempt, as the `attempts` sub-table holds it.
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
    fn of(row: &SubRow) -> Option<Attempt> {
        Some(Attempt {
            seq: number(row, "seq")?,
            attempt: row.key.clone(),
            idempotency_key: text(row, "idempotencyKey")?,
            provider: text(row, "provider")?,
            provider_ref: text(row, "providerRef"),
            amount: number(row, "amount")?,
            currency: text(row, "currency")?,
            due: number(row, "due")?,
            status: AttemptStatus::parse(&text(row, "status")?)?,
            authorized: number(row, "authorized").unwrap_or(0),
            captured: number(row, "captured").unwrap_or(0),
            brand: text(row, "brand"),
            last4: text(row, "last4"),
            return_url: text(row, "returnUrl"),
            opened_at: number(row, "openedAt").unwrap_or(0),
            updated_at: number(row, "updatedAt").unwrap_or(0),
        })
    }

    /// The row's values, the key left out.
    pub fn values(&self) -> Map<String, Value> {
        let mut out = Map::new();
        out.insert("seq".into(), Value::from(self.seq));
        out.insert(
            "idempotencyKey".into(),
            Value::from(self.idempotency_key.as_str()),
        );
        out.insert("provider".into(), Value::from(self.provider.as_str()));
        out.insert("providerRef".into(), or_null(self.provider_ref.clone()));
        out.insert("amount".into(), Value::from(self.amount));
        out.insert("currency".into(), Value::from(self.currency.as_str()));
        out.insert("due".into(), Value::from(self.due));
        out.insert("status".into(), Value::from(self.status.as_str()));
        out.insert("authorized".into(), Value::from(self.authorized));
        out.insert("captured".into(), Value::from(self.captured));
        out.insert("brand".into(), or_null(self.brand.clone()));
        out.insert("last4".into(), or_null(self.last4.clone()));
        out.insert("returnUrl".into(), or_null(self.return_url.clone()));
        out.insert("openedAt".into(), Value::from(self.opened_at));
        out.insert("updatedAt".into(), Value::from(self.updated_at));
        out
    }

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

/// One action, as the `actions` sub-table holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Action {
    pub seq: i64,
    pub action: String,
    pub kind: String,
    pub attempt: String,
    pub refund: Option<String>,
    pub provider: String,
    pub amount: i64,
    pub currency: String,
    pub at: i64,
    pub done_at: Option<i64>,
}

impl Action {
    fn of(row: &SubRow) -> Option<Action> {
        Some(Action {
            seq: number(row, "seq")?,
            action: row.key.clone(),
            kind: text(row, "kind")?,
            attempt: text(row, "attempt")?,
            refund: text(row, "refund"),
            provider: text(row, "provider")?,
            amount: number(row, "amount")?,
            currency: text(row, "currency")?,
            at: number(row, "at").unwrap_or(0),
            done_at: number(row, "doneAt"),
        })
    }

    /// The row's values, the key left out.
    pub fn values(&self) -> Map<String, Value> {
        let mut out = Map::new();
        out.insert("seq".into(), Value::from(self.seq));
        out.insert("kind".into(), Value::from(self.kind.as_str()));
        out.insert("attempt".into(), Value::from(self.attempt.as_str()));
        out.insert("refund".into(), or_null(self.refund.clone()));
        out.insert("provider".into(), Value::from(self.provider.as_str()));
        out.insert("amount".into(), Value::from(self.amount));
        out.insert("currency".into(), Value::from(self.currency.as_str()));
        out.insert("at".into(), Value::from(self.at));
        out.insert("doneAt".into(), or_null(self.done_at));
        out
    }
}

/// One refund, as the `refunds` sub-table holds it.
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
    pub origin: Option<RowRef>,
    pub at: i64,
    pub updated_at: i64,
}

impl Refund {
    pub(crate) fn of(row: &SubRow) -> Option<Refund> {
        let origin = match (text(row, "originSchema"), text(row, "originId")) {
            (Some(schema), Some(id)) => Some(RowRef::new(schema, id)),
            _ => None,
        };
        Some(Refund {
            seq: number(row, "seq")?,
            refund: row.key.clone(),
            idempotency_key: text(row, "idempotencyKey")?,
            attempt: text(row, "attempt")?,
            provider: text(row, "provider")?,
            amount: number(row, "amount")?,
            currency: text(row, "currency")?,
            reason: text(row, "reason").unwrap_or_default(),
            status: RefundStatus::parse(&text(row, "status")?)?,
            refunded: number(row, "refunded").unwrap_or(0),
            origin,
            at: number(row, "at").unwrap_or(0),
            updated_at: number(row, "updatedAt").unwrap_or(0),
        })
    }

    /// The row's values, the key left out.
    pub fn values(&self) -> Map<String, Value> {
        let mut out = Map::new();
        out.insert("seq".into(), Value::from(self.seq));
        out.insert(
            "idempotencyKey".into(),
            Value::from(self.idempotency_key.as_str()),
        );
        out.insert("attempt".into(), Value::from(self.attempt.as_str()));
        out.insert("provider".into(), Value::from(self.provider.as_str()));
        out.insert("amount".into(), Value::from(self.amount));
        out.insert("currency".into(), Value::from(self.currency.as_str()));
        out.insert("reason".into(), Value::from(self.reason.as_str()));
        out.insert("status".into(), Value::from(self.status.as_str()));
        out.insert("refunded".into(), Value::from(self.refunded));
        out.insert(
            "originSchema".into(),
            or_null(self.origin.as_ref().map(|origin| origin.schema.clone())),
        );
        out.insert(
            "originId".into(),
            or_null(self.origin.as_ref().map(|origin| origin.id.clone())),
        );
        out.insert("at".into(), Value::from(self.at));
        out.insert("updatedAt".into(), Value::from(self.updated_at));
        out
    }

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

/// One refund a refund row asked of the row it refunds, as the `issues`
/// sub-table holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Issue {
    pub seq: i64,
    pub refund: String,
    pub of: RowRef,
    pub idempotency_key: String,
    pub amount: i64,
    pub currency: String,
    pub status: RefundStatus,
    pub refunded: i64,
    /// When the refund row entered the state it issued in (Workflow's
    /// `enteredAt`): a failed issue is asked again only after the row
    /// enters an issueOn state again.
    pub entered_at: Option<i64>,
    pub at: i64,
    pub updated_at: i64,
}

impl Issue {
    fn of(row: &SubRow) -> Option<Issue> {
        Some(Issue {
            seq: number(row, "seq")?,
            refund: row.key.clone(),
            of: RowRef::new(text(row, "ofSchema")?, text(row, "ofId")?),
            idempotency_key: text(row, "idempotencyKey")?,
            amount: number(row, "amount")?,
            currency: text(row, "currency").unwrap_or_default(),
            status: RefundStatus::parse(&text(row, "status")?)?,
            refunded: number(row, "refunded").unwrap_or(0),
            entered_at: number(row, "enteredAt"),
            at: number(row, "at").unwrap_or(0),
            updated_at: number(row, "updatedAt").unwrap_or(0),
        })
    }

    /// The row's values, the key left out.
    pub fn values(&self) -> Map<String, Value> {
        let mut out = Map::new();
        out.insert("seq".into(), Value::from(self.seq));
        out.insert("ofSchema".into(), Value::from(self.of.schema.as_str()));
        out.insert("ofId".into(), Value::from(self.of.id.as_str()));
        out.insert(
            "idempotencyKey".into(),
            Value::from(self.idempotency_key.as_str()),
        );
        out.insert("amount".into(), Value::from(self.amount));
        out.insert("currency".into(), Value::from(self.currency.as_str()));
        out.insert("status".into(), Value::from(self.status.as_str()));
        out.insert("refunded".into(), Value::from(self.refunded));
        out.insert("enteredAt".into(), or_null(self.entered_at));
        out.insert("at".into(), Value::from(self.at));
        out.insert("updatedAt".into(), Value::from(self.updated_at));
        out
    }
}

/// A provider event as the shared `events` table holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Recorded {
    pub row_key: String,
    pub kind: String,
    pub amount: i64,
    pub currency: String,
    pub attempt: Option<String>,
    pub provider_ref: Option<String>,
    pub refund: Option<String>,
}

impl Recorded {
    pub fn of(row: &SharedRow) -> Option<Recorded> {
        let text = |field: &str| row.value(field).and_then(Value::as_str).map(str::to_owned);
        Some(Recorded {
            row_key: text("rowKey")?,
            kind: text("kind")?,
            amount: row.value("amount").and_then(integer)?,
            currency: text("currency")?,
            attempt: text("attempt"),
            provider_ref: text("providerRef"),
            refund: text("refund"),
        })
    }
}

/// The key of a provider event in the shared table: its provider and its
/// event id, joined by `/`.
pub fn event_key(provider: &str, event_id: &str) -> String {
    format!("{provider}/{event_id}")
}

/// Every row of one of the use's tables, as the type of `T` reads it,
/// by `seq` ascending, oldest first.
fn ordered<T>(rows: &[SubRow], of: fn(&SubRow) -> Option<T>, seq: fn(&T) -> i64) -> Vec<T> {
    let mut out: Vec<T> = rows.iter().filter_map(of).collect();
    out.sort_by_key(seq);
    out
}

pub fn attempts_of(rows: &[SubRow]) -> Vec<Attempt> {
    ordered(rows, Attempt::of, |attempt| attempt.seq)
}

pub fn actions_of(rows: &[SubRow]) -> Vec<Action> {
    ordered(rows, Action::of, |action| action.seq)
}

pub fn refunds_of(rows: &[SubRow]) -> Vec<Refund> {
    ordered(rows, Refund::of, |refund| refund.seq)
}

pub fn issues_of(rows: &[SubRow]) -> Vec<Issue> {
    ordered(rows, Issue::of, |issue| issue.seq)
}

/// The next `seq` of a table's rows: one more than the most it holds.
pub fn next_seq(rows: &[SubRow]) -> i64 {
    rows.iter()
        .filter_map(|row| number(row, "seq"))
        .max()
        .unwrap_or(0)
        + 1
}

/// What capturePayment and voidPayment return.
pub fn action_json(action: &str, kind: ActionKind, attempt: &Attempt, amount: i64) -> Value {
    json!({
        "action": action,
        "kind": kind.as_str(),
        "attempt": attempt.attempt,
        "amount": amount,
        "currency": attempt.currency,
    })
}

/// What a refund asked returns.
pub fn refund_json(refund: &Refund, action: &str) -> Value {
    json!({
        "refund": refund.refund,
        "action": action,
        "amount": refund.amount,
        "currency": refund.currency,
        "status": refund.status.as_str(),
    })
}

/// What openPayment returns.
pub fn opened(attempt: &Attempt) -> Value {
    json!({
        "attempt": attempt.attempt,
        "provider": attempt.provider,
        "amount": attempt.amount,
        "currency": attempt.currency,
        "status": attempt.status.as_str(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_attempt_reads_back_what_it_wrote() {
        let attempt = Attempt {
            seq: 2,
            attempt: "pay_x".into(),
            idempotency_key: "k".into(),
            provider: "acmepay".into(),
            provider_ref: Some("pi_1".into()),
            amount: 2500,
            currency: "GBP".into(),
            due: 2500,
            status: AttemptStatus::Processing,
            authorized: 0,
            captured: 0,
            brand: None,
            last4: None,
            return_url: None,
            opened_at: 7,
            updated_at: 8,
        };
        let row = SubRow::new("pay_x", attempt.values());
        assert_eq!(attempts_of(&[row]), [attempt]);
        assert!(AttemptStatus::Failed.rank() < AttemptStatus::Authorized.rank());
        assert!(AttemptStatus::Authorized.voidable() && !AttemptStatus::Processing.voidable());
        assert_eq!(event_key("acmepay", "e1"), "acmepay/e1");
    }
}
