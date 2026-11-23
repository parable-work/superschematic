//! Payments' operations and what its after-change hook does (D70 on a
//! table): opening an attempt, the adapter's events, a capture, a void and
//! a refund asked, the refund pair between a refund row and the row it
//! refunds (`refundFor` and `settleRefundFor`, each a call on the other
//! row), the actions waiting for a provider read across the table, and the
//! actions a status asks.

use serde_json::{json, Map, Value};
use superschematic_rules::{Cx, Hook, Refusal, SchemaIssue, Stop, TableRef};

use super::rows::{
    action_json, actions_of, attempts_of, event_key as stored_event_key, issues_of, next_seq,
    opened, refund_json, refunds_of, Action, ActionKind, Attempt, AttemptStatus, EventKind, Issue,
    Recorded, Refund, RefundStatus, ACTIONS, ATTEMPTS, EVENTS, ISSUES, REFUNDS,
};
use super::{
    attempts, codes::*, exact, in_flight, refunds, row_ref, source, status_of, summary,
    total_unknown, veto, CurrencySource, PaymentsConfig, LIST_PAYMENT_ACTIONS, NAME, OVERPAID,
    RELEASE,
};
use crate::currency;
use crate::paging;
use crate::supply::{integer, RowRef};
use crate::workflow;

/// Runs one of Payments' operations.
pub(super) fn run(cx: &Cx<'_, PaymentsConfig>, name: &str, params: &Value) -> Hook<Value> {
    match name {
        super::OPEN_PAYMENT => open_payment(cx, params),
        super::RECORD_PAYMENT_EVENT => record_payment_event(cx, params),
        super::CAPTURE_PAYMENT => capture_payment(cx, params),
        super::VOID_PAYMENT => void_payment(cx, params),
        super::REFUND_PAYMENT => refund_payment(cx, params),
        super::REFUND_FOR => refund_for(cx, params),
        super::SETTLE_REFUND_FOR => settle_refund_for(cx, params),
        LIST_PAYMENT_ACTIONS => list_payment_actions(cx, params),
        _ => Err(cx.defect(format!("it declares no operation {name}"))),
    }
}

fn vetoed(cx: &Cx<'_, PaymentsConfig>, reason: String, code: &str, more: Option<Value>) -> Stop {
    cx.veto(veto(reason, code, more))
}

fn forbidden(cx: &Cx<'_, PaymentsConfig>, what: &str, permission: &str) -> Stop {
    cx.forbidden(format!(
        "{} may not {what} {} {}: it needs permission {permission}",
        cx.caller().subject,
        cx.type_name(),
        cx.key().unwrap_or_default()
    ))
}

fn params_error(cx: &Cx<'_, PaymentsConfig>, operation: &str, path: &str, message: String) -> Stop {
    Refusal::operation_params(
        NAME,
        cx.use_id(),
        operation,
        vec![SchemaIssue::new(path, message)],
    )
    .into()
}

fn text_param<'p>(params: &'p Value, name: &str) -> Option<&'p str> {
    params.get(name).and_then(Value::as_str)
}

fn amount_param(params: &Value, name: &str) -> Option<i64> {
    params
        .get(name)
        .and_then(integer)
        .filter(|amount| exact(*amount))
}

// new_id is an attempt's, an action's or a refund's id: a prefix and a key
// the command makes, unique per command and the same for every run of it,
// which the adapter sends its provider as an idempotency key.
fn new_id(cx: &Cx<'_, PaymentsConfig>, prefix: &str) -> Hook<String> {
    Ok(format!("{prefix}_{}", cx.new_key(prefix)?))
}

fn key_char(byte: u8) -> bool {
    byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b':' | b'-')
}

// issue_key is the idempotency key a refund row asks with: its type and key
// when they fit a key, else a hash of them, and the issue's number.
fn issue_key(refund: &RowRef, number: usize) -> String {
    let plain = format!("{}:{}", refund.schema, refund.id);
    let base = if plain.len() <= 200 && plain.bytes().all(key_char) {
        plain
    } else {
        // FNV-1a over type, a NUL and key: stable across builds.
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

// event_key is an idempotency key of a refund Payments asks itself, after
// a provider event: `<prefix>:<eventId>`, within the 255 characters a key
// holds.
fn event_key(prefix: &str, event_id: &str) -> String {
    format!("{prefix}:{event_id}").chars().take(255).collect()
}

// ---- actions ----

/// What an action is for.
struct ActionFor<'a> {
    kind: ActionKind,
    attempt: &'a Attempt,
    refund: Option<&'a str>,
    amount: i64,
}

// queue records an action waiting for the attempt's provider and returns
// its id.
fn queue(cx: &Cx<'_, PaymentsConfig>, action: &ActionFor<'_>) -> Hook<String> {
    let id = new_id(cx, "act")?;
    let rows = cx.sub_rows(ACTIONS)?;
    let row = Action {
        seq: next_seq(&rows),
        action: id.clone(),
        kind: action.kind.as_str().to_owned(),
        attempt: action.attempt.attempt.clone(),
        refund: action.refund.map(str::to_owned),
        provider: action.attempt.provider.clone(),
        amount: action.amount,
        currency: action.attempt.currency.clone(),
        at: cx.now(),
        done_at: None,
    };
    cx.insert_row(ACTIONS, &id, row.values())?;
    Ok(id)
}

// find_action finds an action of the attempt of a kind: one still waiting
// when `waiting`, any otherwise, the oldest first.
fn find_action(
    cx: &Cx<'_, PaymentsConfig>,
    attempt: &str,
    kind: ActionKind,
    waiting: bool,
) -> Hook<Option<(String, i64)>> {
    Ok(actions_of(&cx.sub_rows(ACTIONS)?)
        .into_iter()
        .find(|action| {
            action.attempt == attempt
                && action.kind == kind.as_str()
                && (!waiting || action.done_at.is_none())
        })
        .map(|action| (action.action, action.amount)))
}

// pending_captures sums the capture actions of an attempt still waiting.
fn pending_captures(cx: &Cx<'_, PaymentsConfig>, attempt: &str) -> Hook<i64> {
    Ok(actions_of(&cx.sub_rows(ACTIONS)?)
        .iter()
        .filter(|action| {
            action.attempt == attempt
                && action.kind == ActionKind::Capture.as_str()
                && action.done_at.is_none()
        })
        .map(|action| action.amount)
        .sum())
}

// finish marks the waiting actions of an attempt of the kinds given done.
fn finish(
    cx: &Cx<'_, PaymentsConfig>,
    attempt: &str,
    kinds: &[ActionKind],
    refund: Option<&str>,
) -> Hook<()> {
    for action in actions_of(&cx.sub_rows(ACTIONS)?) {
        let kind = kinds.iter().any(|kind| kind.as_str() == action.kind);
        let named = refund.is_none_or(|refund| action.refund.as_deref() == Some(refund));
        if action.attempt == attempt && kind && named && action.done_at.is_none() {
            let mut values = Map::new();
            values.insert("doneAt".into(), Value::from(cx.now()));
            cx.update_row(ACTIONS, &action.action, values)?;
        }
    }
    Ok(())
}

// refund_action is the id of a refund's action.
fn refund_action(cx: &Cx<'_, PaymentsConfig>, refund: &Refund) -> Hook<String> {
    actions_of(&cx.sub_rows(ACTIONS)?)
        .into_iter()
        .find(|action| {
            action.kind == ActionKind::Refund.as_str()
                && action.refund.as_deref() == Some(refund.refund.as_str())
        })
        .map(|action| action.action)
        .ok_or_else(|| cx.defect(format!("refund {} has no action", refund.refund)))
}

// ---- attempts and refunds written ----

fn save_attempt(cx: &Cx<'_, PaymentsConfig>, attempt: &Attempt) -> Hook<()> {
    let saved = Attempt {
        updated_at: cx.now(),
        ..attempt.clone()
    };
    cx.update_row(ATTEMPTS, &saved.attempt, saved.values())
}

fn save_refund(cx: &Cx<'_, PaymentsConfig>, refund: &Refund) -> Hook<()> {
    let saved = Refund {
        updated_at: cx.now(),
        ..refund.clone()
    };
    cx.update_row(REFUNDS, &saved.refund, saved.values())
}

fn attempt_where(
    cx: &Cx<'_, PaymentsConfig>,
    matches: impl Fn(&Attempt) -> bool,
) -> Hook<Option<Attempt>> {
    Ok(attempts(cx)?.into_iter().find(|attempt| matches(attempt)))
}

fn refund_named(cx: &Cx<'_, PaymentsConfig>, id: &str) -> Hook<Option<Refund>> {
    Ok(refunds(cx)?.into_iter().find(|refund| refund.refund == id))
}

/// A refund asked: of what, why, and by whom when a refund row asked.
struct Asked<'a> {
    amount: i64,
    reason: &'a str,
    key: &'a str,
    origin: Option<&'a RowRef>,
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
fn record_refund(cx: &Cx<'_, PaymentsConfig>, asked: &Asked<'_>) -> Hook<Value> {
    let all = refunds(cx)?;
    if let Some(existing) = all
        .iter()
        .find(|refund| refund.idempotency_key == asked.key)
    {
        let same = existing.amount == asked.amount
            && existing.origin.as_ref() == asked.origin
            && (asked.origin.is_some() || existing.reason == asked.reason);
        if !same {
            return Err(vetoed(
                cx,
                format!(
                    "idempotencyKey {} asked refund {} of {} before",
                    asked.key, existing.refund, existing.amount
                ),
                KEY_REUSED,
                Some(json!({"refund": existing.refund})),
            ));
        }
        cx.unchanged()?;
        let action = refund_action(cx, existing)?;
        return Ok(refund_json(existing, &action));
    }
    let attempts = attempts(cx)?;
    insert_refund(cx, asked, &attempts, &all)
}

fn insert_refund(
    cx: &Cx<'_, PaymentsConfig>,
    asked: &Asked<'_>,
    attempts: &[Attempt],
    refunds: &[Refund],
) -> Hook<Value> {
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
            format!(
                "{} {} has {most} it can refund in one refund, not {}",
                cx.type_name(),
                cx.key().unwrap_or_default(),
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
    let rows = cx.sub_rows(REFUNDS)?;
    let refund = Refund {
        seq: next_seq(&rows),
        refund: new_id(cx, "rf")?,
        idempotency_key: asked.key.to_owned(),
        attempt: attempt.attempt.clone(),
        provider: attempt.provider.clone(),
        amount: asked.amount,
        currency: attempt.currency.clone(),
        reason: asked.reason.to_owned(),
        status: RefundStatus::Requested,
        refunded: 0,
        origin: asked.origin.cloned(),
        at: cx.now(),
        updated_at: cx.now(),
    };
    cx.insert_row(REFUNDS, &refund.refund, refund.values())?;
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

// ---- operations ----

fn open_payment(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Value> {
    let provider = text_param(params, "provider").unwrap_or_default();
    let idempotency_key = text_param(params, "idempotencyKey").unwrap_or_default();
    let return_url = text_param(params, "returnUrl");
    let config = cx.config();
    if config.is_refund() {
        return Err(vetoed(
            cx,
            format!(
                "{} is a refund (refundOf), which takes no payments",
                cx.type_name()
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
                format!(
                    "idempotencyKey {idempotency_key} opened attempt {} at {} before",
                    existing.attempt, existing.provider
                ),
                KEY_REUSED,
                Some(json!({"attempt": existing.attempt})),
            ));
        }
        cx.unchanged()?;
        return Ok(opened(existing));
    }
    if let Some(status) = status_of(cx) {
        if config.void_on.contains(&status) {
            return Err(vetoed(
                cx,
                format!(
                    "{} {} is {status}, which voids its payments, so nothing is due",
                    cx.type_name(),
                    cx.key().unwrap_or_default()
                ),
                NOTHING_DUE,
                Some(json!({"state": status})),
            ));
        }
    }
    let live = source(cx)?;
    if let Some(why) = &live.unknown {
        return Err(cx.veto(total_unknown(cx, why, json!({"source": "Holds.total"}))));
    }
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
            format!(
                "{} {} has nothing outstanding to pay: {due} due, {captured} captured, {pending} in attempts in flight",
                cx.type_name(),
                cx.key().unwrap_or_default()
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
            format!(
                "{} {} names no currency",
                cx.type_name(),
                cx.key().unwrap_or_default()
            ),
            CURRENCY_MISMATCH,
            None,
        ));
    };
    if let Some(why) = currency::refusal("the instance's currency", &currency) {
        return Err(vetoed(cx, why, CURRENCY_MISMATCH, None));
    }
    // A currency the config names must be the one the holds total carries.
    if let (Some(CurrencySource::Code(code)), Some(total)) = (&config.currency, &live.currency) {
        if code != total {
            return Err(vetoed(
                cx,
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
            format!(
                "what is due is in {}, and the instance's attempts are in {currency}",
                live.currency.as_deref().unwrap_or_default()
            ),
            CURRENCY_MISMATCH,
            Some(json!({"currency": live.currency, "expected": currency})),
        ));
    }
    let rows = cx.sub_rows(ATTEMPTS)?;
    let attempt = Attempt {
        seq: next_seq(&rows),
        attempt: new_id(cx, "pay")?,
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
    cx.insert_row(ATTEMPTS, &attempt.attempt, attempt.values())?;
    queue(
        cx,
        &ActionFor {
            kind: ActionKind::Create,
            attempt: &attempt,
            refund: None,
            amount: attempt.amount,
        },
    )?;
    void_abandoned(cx, &attempts)?;
    Ok(opened(&attempt))
}

// void_abandoned gives each earlier attempt that failed one void action as
// a new attempt opens: a provider may still complete a payment it reported
// failed, and the payer is now paying through the new one.
fn void_abandoned(cx: &Cx<'_, PaymentsConfig>, earlier: &[Attempt]) -> Hook<()> {
    for attempt in earlier {
        if attempt.status != AttemptStatus::Failed
            || find_action(cx, &attempt.attempt, ActionKind::Void, false)?.is_some()
        {
            continue;
        }
        queue(
            cx,
            &ActionFor {
                kind: ActionKind::Void,
                attempt,
                refund: None,
                amount: attempt.amount,
            },
        )?;
    }
    Ok(())
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

fn event_of<'p>(cx: &Cx<'_, PaymentsConfig>, params: &'p Value) -> Hook<Event<'p>> {
    let kind = text_param(params, "kind")
        .and_then(EventKind::parse)
        .ok_or_else(|| {
            params_error(
                cx,
                super::RECORD_PAYMENT_EVENT,
                "/kind",
                "must be one of the kinds the schema lists".to_owned(),
            )
        })?;
    Ok(Event {
        provider: text_param(params, "provider").unwrap_or_default(),
        event_id: text_param(params, "eventId").unwrap_or_default(),
        kind,
        amount: amount_param(params, "amount").unwrap_or(0),
        currency: text_param(params, "currency").unwrap_or_default(),
        at: params.get("at").and_then(integer).unwrap_or(0),
        attempt: text_param(params, "attempt"),
        provider_ref: text_param(params, "providerRef"),
        refund: text_param(params, "refund"),
        brand: text_param(params, "brand"),
        last4: text_param(params, "last4"),
        metadata: params.get("metadata"),
    })
}

// same_event is whether a recorded event is the one a repeat reports.
fn same_event(cx: &Cx<'_, PaymentsConfig>, recorded: &Recorded, event: &Event<'_>) -> bool {
    Some(recorded.row_key.as_str()) == cx.key()
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
    cx: &Cx<'_, PaymentsConfig>,
    event: &Event<'_>,
    attempt: Option<&str>,
    applied: bool,
) -> Hook<()> {
    let text = |value: Option<&str>| value.map_or(Value::Null, Value::from);
    let mut values = Map::new();
    values.insert("provider".into(), Value::from(event.provider));
    values.insert("eventId".into(), Value::from(event.event_id));
    values.insert("rowKey".into(), text(cx.key()));
    values.insert("kind".into(), Value::from(event.kind.as_str()));
    values.insert("amount".into(), Value::from(event.amount));
    values.insert("currency".into(), Value::from(event.currency));
    values.insert("at".into(), Value::from(event.at));
    values.insert("attempt".into(), text(attempt));
    values.insert("providerRef".into(), text(event.provider_ref));
    values.insert("refund".into(), text(event.refund));
    values.insert("applied".into(), Value::Bool(applied));
    values.insert(
        "metadata".into(),
        event.metadata.cloned().unwrap_or(Value::Null),
    );
    values.insert("recordedAt".into(), Value::from(cx.now()));
    cx.insert_shared(
        EVENTS,
        &stored_event_key(event.provider, event.event_id),
        values,
    )
}

fn event_result(cx: &Cx<'_, PaymentsConfig>, applied: bool, attempt: Option<&str>) -> Hook<Value> {
    let attempt = match attempt {
        Some(id) => {
            attempt_where(cx, |attempt| attempt.attempt == id)?.map(|attempt| attempt.to_json())
        }
        None => None,
    };
    Ok(json!({
        "applied": applied,
        "attempt": attempt,
        "state": summary(cx)?.state.as_str(),
    }))
}

fn record_payment_event(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let permission = &config.record_permission;
    if !cx.can(permission)? {
        return Err(forbidden(cx, "record a payment event of", permission));
    }
    let event = event_of(cx, params)?;
    let key = stored_event_key(event.provider, event.event_id);
    if let Some(recorded) = cx.shared_row(EVENTS, &key)?.as_ref().and_then(Recorded::of) {
        if !same_event(cx, &recorded, &event) {
            return Err(vetoed(
                cx,
                format!(
                    "event {} of {} was recorded before, as {} of {} {} on {} {}",
                    event.event_id,
                    event.provider,
                    recorded.kind,
                    recorded.amount,
                    recorded.currency,
                    cx.type_name(),
                    recorded.row_key
                ),
                KEY_REUSED,
                Some(
                    json!({"eventId": event.event_id, "schema": cx.type_name(), "id": recorded.row_key}),
                ),
            ));
        }
        cx.unchanged()?;
        return event_result(cx, false, recorded.attempt.as_deref());
    }
    if config.is_refund() {
        return Err(vetoed(
            cx,
            format!(
                "{} is a refund (refundOf): its outcome is recorded on the instance it refunds",
                cx.type_name()
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
fn resolve(cx: &Cx<'_, PaymentsConfig>, event: &Event<'_>) -> Hook<Attempt> {
    let found = match (event.attempt, event.provider_ref) {
        (Some(id), _) => attempt_where(cx, |attempt| attempt.attempt == id)?,
        (None, Some(reference)) => attempt_where(cx, |attempt| {
            attempt.provider_ref.as_deref() == Some(reference)
        })?,
        (None, None) => {
            return Err(vetoed(
                cx,
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
            format!(
                "{} {} has no attempt {named}",
                cx.type_name(),
                cx.key().unwrap_or_default()
            ),
            NO_ATTEMPT,
            Some(json!({"attempt": event.attempt, "providerRef": event.provider_ref})),
        ));
    };
    if attempt.provider != event.provider {
        return Err(vetoed(
            cx,
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
                format!(
                    "attempt {} has the provider reference {known}, not {given}",
                    attempt.attempt
                ),
                NO_ATTEMPT,
                Some(json!({"attempt": attempt.attempt})),
            ));
        }
    }
    if let Some(reference) = event.provider_ref {
        // A reference another attempt of the row holds is not this one's.
        if let Some(other) =
            attempt_where(cx, |other| other.provider_ref.as_deref() == Some(reference))?
        {
            if other.attempt != attempt.attempt {
                return Err(vetoed(
                    cx,
                    format!(
                        "the provider reference {reference} is attempt {}'s, not {}'s",
                        other.attempt, attempt.attempt
                    ),
                    NO_ATTEMPT,
                    Some(json!({"attempt": attempt.attempt})),
                ));
            }
        }
    }
    Ok(attempt)
}

fn attempt_event(cx: &Cx<'_, PaymentsConfig>, event: &Event<'_>) -> Hook<Value> {
    let mut attempt = resolve(cx, event)?;
    if event.currency != attempt.currency {
        return Err(vetoed(
            cx,
            format!(
                "the event is in {}, and attempt {} is in {}",
                event.currency, attempt.attempt, attempt.currency
            ),
            CURRENCY_MISMATCH,
            Some(json!({"currency": event.currency, "expected": attempt.currency})),
        ));
    }
    let Some(to) = event.kind.status() else {
        return Err(cx.defect("a refund's outcome reached the attempt's"));
    };
    let from = attempt.status;
    let mut applied = false;
    if to == AttemptStatus::Captured {
        // Money the provider captured is counted, whatever came before it.
        let capturable = attempt.capturable();
        if attempt.captured + event.amount > capturable {
            return Err(vetoed(
                cx,
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
    if applied && to == AttemptStatus::Captured && event.amount > 0 {
        let config = cx.config();
        // Money captured after the row let go of what it paid for.
        let released = status_of(cx).filter(|status| config.void_on.contains(status));
        match released {
            Some(status) if config.refund_after_release => {
                let key = event_key(RELEASE, event.event_id);
                let reason = format!(
                    "captured after {} {} was {status}",
                    cx.type_name(),
                    cx.key().unwrap_or_default()
                );
                let attempts = attempts(cx)?;
                let refunds = refunds(cx)?;
                insert_refund(
                    cx,
                    &Asked {
                        amount: event.amount,
                        reason: &reason,
                        key: &key,
                        origin: None,
                    },
                    &attempts,
                    &refunds,
                )?;
            }
            Some(_) => {}
            None => settle_capture(cx, &attempt, event)?,
        }
    }
    event_result(cx, applied, Some(&attempt.attempt))
}

// settle_capture follows a capture on a row that has not let go: once what
// is captured covers what is due, the other attempts still open are
// voided; and what this capture brought past what is due is recorded as
// overpaid and asked back with a refund of it, keyed `overpaid:<eventId>`.
fn settle_capture(cx: &Cx<'_, PaymentsConfig>, captured: &Attempt, event: &Event<'_>) -> Hook<()> {
    let all = attempts(cx)?;
    let due = match in_flight(&all) {
        Some(attempt) => attempt.due,
        None => {
            let live = source(cx)?;
            // What is due cannot be read: nothing is judged past it.
            if live.unknown.is_some() {
                return Ok(());
            }
            live.due
        }
    };
    let total: i64 = all.iter().map(|attempt| attempt.captured).sum();
    if total < due {
        return Ok(());
    }
    for attempt in &all {
        if attempt.attempt == captured.attempt
            || !attempt.status.voidable()
            || find_action(cx, &attempt.attempt, ActionKind::Void, false)?.is_some()
        {
            continue;
        }
        queue(
            cx,
            &ActionFor {
                kind: ActionKind::Void,
                attempt,
                refund: None,
                amount: void_amount(attempt),
            },
        )?;
    }
    let before = total - event.amount;
    let refunds = refunds(cx)?;
    let excess = (total - before.max(due)).clamp(0, event.amount);
    // What a refund can draw on: a refund draws on one attempt.
    let most = refundable(&all, &refunds)
        .iter()
        .map(|(_, left)| *left)
        .max()
        .unwrap_or(0);
    let amount = excess.min(most);
    if amount <= 0 {
        return Ok(());
    }
    let key = event_key(OVERPAID, event.event_id);
    let reason = format!(
        "captured {} {} past the {due} {} {} owes",
        amount,
        captured.currency,
        cx.type_name(),
        cx.key().unwrap_or_default()
    );
    insert_refund(
        cx,
        &Asked {
            amount,
            reason: &reason,
            key: &key,
            origin: None,
        },
        &all,
        &refunds,
    )?;
    Ok(())
}

// payments_use is the use of a type that is Payments: its one.
fn payments_use(cx: &Cx<'_, PaymentsConfig>, type_name: &str) -> Hook<Option<String>> {
    Ok(cx.read_spec(type_name)?.and_then(|spec| {
        spec.uses
            .iter()
            .find(|entry| entry.behavior == NAME)
            .map(|entry| entry.id().to_owned())
    }))
}

fn refund_event(cx: &Cx<'_, PaymentsConfig>, event: &Event<'_>) -> Hook<Value> {
    let Some(id) = event.refund else {
        return Err(vetoed(
            cx,
            format!("a {} event names its refund", event.kind.as_str()),
            NO_ATTEMPT,
            None,
        ));
    };
    let Some(mut refund) = refund_named(cx, id)? else {
        return Err(vetoed(
            cx,
            format!(
                "{} {} has no refund {id}",
                cx.type_name(),
                cx.key().unwrap_or_default()
            ),
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
                cx,
                super::RECORD_PAYMENT_EVENT,
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
            let Some(use_id) = payments_use(cx, &origin.schema)? else {
                return Err(cx.defect(format!(
                    "refund {id} was asked by {} {}, whose type is no Payments",
                    origin.schema, origin.id
                )));
            };
            cx.call_on(
                &origin.schema,
                &origin.id,
                &use_id,
                super::SETTLE_REFUND_FOR,
                &json!({
                    "of": {"schema": cx.type_name(), "id": cx.key().unwrap_or_default()},
                    "refund": refund.refund,
                    "status": to.as_str(),
                    "amount": refund.refunded,
                }),
            )?;
        }
    }
    event_result(cx, applied, Some(&refund.attempt))
}

// attempt_named reads an attempt a capture or a void names.
fn attempt_named(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Attempt> {
    let named = text_param(params, "attempt").unwrap_or_default();
    attempt_where(cx, |attempt| attempt.attempt == named)?.ok_or_else(|| {
        vetoed(
            cx,
            format!(
                "{} {} has no attempt {named}",
                cx.type_name(),
                cx.key().unwrap_or_default()
            ),
            NO_ATTEMPT,
            Some(json!({"attempt": named})),
        )
    })
}

fn capture_payment(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Value> {
    let attempt = attempt_named(cx, params)?;
    if attempt.status != AttemptStatus::Authorized {
        return Err(vetoed(
            cx,
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
            cx.unchanged()?;
            return Ok(action_json(&action, ActionKind::Capture, &attempt, amount));
        }
    }
    let left = attempt.authorized - attempt.captured - pending_captures(cx, &attempt.attempt)?;
    let amount = asked.unwrap_or(left);
    if amount <= 0 || amount > left {
        return Err(vetoed(
            cx,
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

fn void_payment(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Value> {
    let attempt = attempt_named(cx, params)?;
    if !attempt.status.voidable() {
        return Err(vetoed(
            cx,
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
        cx.unchanged()?;
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

fn refund_payment(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Value> {
    let Some(permission) = cx.config().refund_permission.clone() else {
        return Err(vetoed(
            cx,
            "its config names no refunds permission, which refunding directly needs; a refund instance (refundOf) refunds it".to_owned(),
            NOT_CONFIGURED,
            None,
        ));
    };
    if !cx.can(&permission)? {
        return Err(forbidden(cx, "refund", &permission));
    }
    let key = text_param(params, "idempotencyKey").unwrap_or_default();
    if let Some(prefix) = [OVERPAID, RELEASE].into_iter().find(|prefix| {
        key.strip_prefix(prefix)
            .is_some_and(|rest| rest.starts_with(':'))
    }) {
        return Err(params_error(
            cx,
            super::REFUND_PAYMENT,
            "/idempotencyKey",
            format!("keys {prefix}:\u{2026} are those of the refunds Payments asks itself"),
        ));
    }
    record_refund(
        cx,
        &Asked {
            amount: amount_param(params, "amount").unwrap_or(0),
            reason: text_param(params, "reason").unwrap_or_default(),
            key,
            origin: None,
        },
    )
}

/// What `refundFor` and `settleRefundFor` read of the other row of the
/// pair: its type's Payments config as JSON and use, its relation's own
/// field and target type, its Workflow use, and the row as the run has it.
struct Pair {
    config: Value,
    use_id: String,
    field: Option<(String, String)>,
    workflow: Option<String>,
    row: Option<superschematic_rules::Row>,
}

// pair reads the other row of a refund pair by its type and key.
fn pair(cx: &Cx<'_, PaymentsConfig>, of: &RowRef) -> Hook<Option<Pair>> {
    let Some(spec) = cx.read_spec(&of.schema)? else {
        return Ok(None);
    };
    let Some(entry) = spec.uses.iter().find(|entry| entry.behavior == NAME) else {
        return Ok(None);
    };
    let config = entry.config_or_empty();
    let link = config
        .get("refundOf")
        .and_then(|of| of.get("link"))
        .and_then(Value::as_str);
    let field = link.and_then(|link| {
        spec.fields.iter().find_map(|field| {
            field
                .relation
                .as_ref()
                .filter(|relation| relation.name == link)
                .map(|relation| (field.name.clone(), relation.type_name.clone()))
        })
    });
    let workflow = spec
        .uses
        .iter()
        .find(|entry| entry.behavior == workflow::NAME)
        .map(|entry| entry.id().to_owned());
    let use_id = entry.id().to_owned();
    let row = cx.row_of(&of.schema, &of.id)?;
    Ok(Some(Pair {
        config,
        use_id,
        field,
        workflow,
        row,
    }))
}

fn refund_for(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Value> {
    let refund = params
        .get("refund")
        .and_then(RowRef::from_json)
        .ok_or_else(|| {
            params_error(
                cx,
                super::REFUND_FOR,
                "/refund",
                "must be {schema, id}".to_owned(),
            )
        })?;
    let amount = amount_param(params, "amount").unwrap_or(0);
    let refuse = |reason: String| {
        vetoed(
            cx,
            reason,
            NOT_LINKED,
            Some(json!({"refund": refund.to_json()})),
        )
    };
    if cx.config().is_refund() {
        return Err(refuse(format!(
            "{} is a refund itself, and refunds nothing of its own",
            cx.type_name()
        )));
    }
    // The refund row's type says which relation names what it refunds.
    let theirs = pair(cx, &refund)?;
    let link = theirs.as_ref().and_then(|theirs| {
        theirs
            .config
            .get("refundOf")
            .and_then(|of| of.get("link"))
            .and_then(Value::as_str)
            .map(str::to_owned)
    });
    let (Some(theirs), Some(link)) = (theirs, link) else {
        return Err(refuse(format!(
            "{} is not a refund: its Payments names no refundOf",
            refund.schema
        )));
    };
    let issue_on: Vec<String> = theirs
        .config
        .get("issueOn")
        .and_then(Value::as_array)
        .map(|states| {
            states
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default();
    let Some(row) = &theirs.row else {
        return Err(refuse(format!(
            "{} {} does not exist",
            refund.schema, refund.id
        )));
    };
    let mine = row_ref(cx);
    let points_here = theirs.field.as_ref().is_some_and(|(field, target)| {
        *target == mine.schema
            && row
                .own
                .get(field)
                .and_then(superschematic_rules::key_of)
                == Some(mine.id.clone())
    });
    if !points_here {
        return Err(refuse(format!(
            "{} {}'s {link} link does not point at {} {}",
            refund.schema, refund.id, mine.schema, mine.id
        )));
    }
    let status = theirs
        .workflow
        .as_deref()
        .and_then(|use_id| row.owned(use_id, workflow::STATUS))
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
    let due = row
        .owned(&theirs.use_id, "due")
        .and_then(integer)
        .unwrap_or(0);
    if due != amount {
        return Err(refuse(format!(
            "{} {} refunds {due}, not {amount}",
            refund.schema, refund.id
        )));
    }
    let theirs_currency = row
        .owned(&theirs.use_id, "currency")
        .and_then(Value::as_str)
        .map(str::to_owned);
    let my_currency = attempts(cx)?.last().map(|attempt| attempt.currency.clone());
    if theirs_currency != my_currency {
        return Err(vetoed(
            cx,
            format!(
                "{} {} refunds in {}, and {} {} was paid in {}",
                refund.schema,
                refund.id,
                theirs_currency.as_deref().unwrap_or("no currency"),
                mine.schema,
                mine.id,
                my_currency.as_deref().unwrap_or("no currency")
            ),
            CURRENCY_MISMATCH,
            Some(json!({"currency": theirs_currency, "expected": my_currency})),
        ));
    }
    // One live refund per refund row, whatever the key: its approval pays
    // it once. A failed one is asked again under the key the refund row's
    // Payments derives from it and the failures before.
    let given = text_param(params, "idempotencyKey").unwrap_or_default();
    let asked: Vec<Refund> = refunds(cx)?
        .into_iter()
        .filter(|row| row.origin.as_ref() == Some(&refund))
        .collect();
    let answer = |found: &Value| {
        json!({
            "refund": found["refund"],
            "action": found["action"],
            "amount": found["amount"],
            "currency": found["currency"],
        })
    };
    if let Some(live) = asked.iter().find(|row| row.status != RefundStatus::Failed) {
        if live.idempotency_key != given || live.amount != amount {
            return Err(vetoed(
                cx,
                format!(
                    "{} {} asked refund {} of {} under idempotencyKey {}, which is {}; it asks one at a time",
                    refund.schema,
                    refund.id,
                    live.refund,
                    live.amount,
                    live.idempotency_key,
                    live.status.as_str()
                ),
                KEY_REUSED,
                Some(json!({"refund": live.refund, "idempotencyKey": live.idempotency_key})),
            ));
        }
        cx.unchanged()?;
        let action = refund_action(cx, live)?;
        return Ok(answer(&refund_json(live, &action)));
    }
    let expected = issue_key(&refund, asked.len());
    if given != expected {
        return Err(vetoed(
            cx,
            format!(
                "{} {} asks its refund under idempotencyKey {expected}, the one its Payments derives, not {given}",
                refund.schema, refund.id
            ),
            KEY_REUSED,
            Some(json!({"refund": refund.to_json(), "idempotencyKey": expected})),
        ));
    }
    let reason = format!("refund {} {}", refund.schema, refund.id);
    let recorded = record_refund(
        cx,
        &Asked {
            amount,
            reason: &reason,
            key: &expected,
            origin: Some(&refund),
        },
    )?;
    Ok(answer(&recorded))
}

fn settle_refund_for(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Value> {
    let config = cx.config();
    let permission = &config.record_permission;
    if !cx.can(permission)? {
        return Err(forbidden(cx, "settle a refund of", permission));
    }
    let of = params
        .get("of")
        .and_then(RowRef::from_json)
        .ok_or_else(|| {
            params_error(
                cx,
                super::SETTLE_REFUND_FOR,
                "/of",
                "must be {schema, id}".to_owned(),
            )
        })?;
    let named = text_param(params, "refund").unwrap_or_default();
    let (Some(link), Some(field)) = (&config.refund_of, &config.refund_of_field) else {
        return Err(vetoed(
            cx,
            format!("{} is not a refund (refundOf)", cx.type_name()),
            NOT_LINKED,
            None,
        ));
    };
    let mine = row_ref(cx);
    let target_type = cx
        .read_spec(cx.type_name())?
        .and_then(|spec| spec.field(field))
        .and_then(|field| field.relation.as_ref())
        .map(|relation| relation.type_name.clone());
    let target_key = cx
        .own_field(field)
        .as_ref()
        .and_then(superschematic_rules::key_of);
    if target_type.as_deref() != Some(of.schema.as_str())
        || target_key.as_deref() != Some(of.id.as_str())
    {
        return Err(vetoed(
            cx,
            format!(
                "{} {} refunds what its {link} link points at, not {} {}",
                mine.schema, mine.id, of.schema, of.id
            ),
            NOT_LINKED,
            Some(json!({"of": of.to_json()})),
        ));
    }
    let Some(mut issue) = issues_of(&cx.sub_rows(ISSUES)?)
        .into_iter()
        .find(|issue| issue.refund == named && issue.of == of)
    else {
        return Err(vetoed(
            cx,
            format!("{} {} asked no refund {named}", mine.schema, mine.id),
            NO_ATTEMPT,
            Some(json!({"refund": named})),
        ));
    };
    // The outcome is the refund's as the row it refunds recorded it, read
    // from that row's refunds as the run has them: a caller's status and
    // amount are checked against it, never taken on trust.
    let recorded = match pair(cx, &of)? {
        Some(theirs) if theirs.row.is_some() => {
            refunds_of(&cx.sub_rows_of(&of.schema, &of.id, &theirs.use_id, REFUNDS)?)
                .into_iter()
                .find(|row| row.refund == named && row.origin.as_ref() == Some(&mine))
        }
        _ => None,
    };
    let Some(recorded) = recorded else {
        return Err(vetoed(
            cx,
            format!(
                "{} {} records no refund {named} that {} {} asked",
                of.schema, of.id, mine.schema, mine.id
            ),
            NO_ATTEMPT,
            Some(json!({"refund": named})),
        ));
    };
    let to = match text_param(params, "status") {
        Some("succeeded") => RefundStatus::Succeeded,
        _ => RefundStatus::Failed,
    };
    let amount = amount_param(params, "amount").unwrap_or(0);
    let refunded = if to == RefundStatus::Succeeded {
        recorded.refunded
    } else {
        0
    };
    if recorded.status != to || amount != refunded {
        return Err(vetoed(
            cx,
            format!(
                "{} {} records refund {named} {} with {} refunded, not {} with {amount}",
                of.schema,
                of.id,
                recorded.status.as_str(),
                recorded.refunded,
                to.as_str()
            ),
            NOT_LINKED,
            Some(
                json!({"refund": named, "status": recorded.status.as_str(), "refunded": recorded.refunded}),
            ),
        ));
    }
    if issue.status.pending() {
        issue.status = to;
        issue.refunded = refunded;
        issue.updated_at = cx.now();
        cx.update_row(ISSUES, &issue.refund, issue.values())?;
    } else {
        cx.unchanged()?;
    }
    Ok(json!({ "state": summary(cx)?.state.as_str() }))
}

fn list_payment_actions(cx: &Cx<'_, PaymentsConfig>, params: &Value) -> Hook<Value> {
    let permission = &cx.config().record_permission;
    if !cx.can(permission)? {
        return Err(cx.forbidden(format!(
            "{} may not list the payment actions of {}: it needs permission {permission}",
            cx.caller().subject,
            cx.type_name()
        )));
    }
    let request = paging::page_request(params).map_err(|issue| {
        Stop::from(Refusal::operation_params(
            NAME,
            cx.use_id(),
            LIST_PAYMENT_ACTIONS,
            vec![issue],
        ))
    })?;
    let provider = text_param(params, "provider").unwrap_or_default();
    let use_id = cx.use_id();
    let tables = [
        TableRef::new(use_id, ACTIONS),
        TableRef::new(use_id, ATTEMPTS),
    ];
    // Every waiting action of the table, in the order each was asked: by
    // its time, then its row's key, then its place on the row.
    let mut waiting: Vec<(i64, String, i64, Value)> = Vec::new();
    let mut after: Option<String> = None;
    loop {
        let page = cx.read_rows(cx.type_name(), &Map::new(), &tables, after.as_deref())?;
        for row in &page.rows {
            let attempts = attempts_of(row.table(use_id, ATTEMPTS));
            for action in actions_of(row.table(use_id, ACTIONS)) {
                if action.done_at.is_some() || action.provider != provider {
                    continue;
                }
                let attempt = attempts
                    .iter()
                    .find(|attempt| attempt.attempt == action.attempt);
                let mut out = Map::new();
                out.insert("action".into(), Value::from(action.action.as_str()));
                out.insert("kind".into(), Value::from(action.kind.as_str()));
                out.insert("attempt".into(), Value::from(action.attempt.as_str()));
                out.insert("amount".into(), Value::from(action.amount));
                out.insert("currency".into(), Value::from(action.currency.as_str()));
                out.insert("id".into(), Value::from(row.key.as_str()));
                out.insert("at".into(), Value::from(action.at));
                if let Some(reference) = attempt.and_then(|attempt| attempt.provider_ref.as_deref())
                {
                    out.insert("providerRef".into(), Value::from(reference));
                }
                if let Some(refund) = &action.refund {
                    out.insert("refund".into(), Value::from(refund.as_str()));
                }
                if action.kind == ActionKind::Create.as_str() {
                    if let Some(url) = attempt.and_then(|attempt| attempt.return_url.as_deref()) {
                        out.insert("returnUrl".into(), Value::from(url));
                    }
                }
                waiting.push((action.at, row.key.clone(), action.seq, Value::Object(out)));
            }
        }
        match (page.more, page.rows.last()) {
            (true, Some(last)) => after = Some(last.key.clone()),
            _ => break,
        }
    }
    waiting.sort_by(|a, b| (a.0, &a.1, a.2).cmp(&(b.0, &b.1, b.2)));
    let numbered: Vec<(i64, Value)> = waiting
        .into_iter()
        .enumerate()
        .map(|(at, (_, _, _, item))| (at as i64 + 1, item))
        .filter(|(number, _)| *number > request.after)
        .collect();
    let found = paging::page(numbered, request.limit, |(number, _)| *number);
    Ok(json!({
        "items": found.items.into_iter().map(|(_, item)| item).collect::<Vec<_>>(),
        "next": found.next,
    }))
}

// ---- what a status asks ----

// void_open gives each attempt a provider can still cancel or void one
// void action, in a voidOn state.
pub(super) fn void_open(cx: &Cx<'_, PaymentsConfig>) -> Hook<()> {
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
pub(super) fn capture_authorized(cx: &Cx<'_, PaymentsConfig>) -> Hook<()> {
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

// issue asks the row a refund row refunds for its amount, once per issue:
// again only after the last one failed and the row entered an issueOn
// state again since it was asked.
pub(super) fn issue(cx: &Cx<'_, PaymentsConfig>) -> Hook<()> {
    let config = cx.config();
    let all = issues_of(&cx.sub_rows(ISSUES)?);
    let entered_at = config
        .workflow
        .as_deref()
        .and_then(|use_id| cx.get_of(use_id, workflow::ENTERED_AT))
        .as_ref()
        .and_then(integer);
    if let Some(last) = all.last() {
        if last.status != RefundStatus::Failed || last.entered_at == entered_at {
            return Ok(());
        }
    }
    let link = config.refund_of.clone().unwrap_or_default();
    let mine = row_ref(cx);
    let field = config.refund_of_field.clone().unwrap_or_default();
    let target_type = cx
        .read_spec(cx.type_name())?
        .and_then(|spec| spec.field(&field))
        .and_then(|field| field.relation.as_ref())
        .map(|relation| relation.type_name.clone());
    let target_key = cx
        .own_field(&field)
        .as_ref()
        .and_then(superschematic_rules::key_of);
    let (Some(schema), Some(id)) = (target_type, target_key) else {
        return Err(vetoed(
            cx,
            format!(
                "{} {}'s {link} link points at nothing to refund",
                mine.schema, mine.id
            ),
            NOT_LINKED,
            None,
        ));
    };
    let of = RowRef::new(schema, id);
    let live = source(cx)?;
    if live.due <= 0 {
        return Err(vetoed(
            cx,
            format!(
                "{} {} refunds nothing: its amount is 0",
                mine.schema, mine.id
            ),
            NOTHING_DUE,
            None,
        ));
    }
    let Some(use_id) = payments_use(cx, &of.schema)? else {
        return Err(vetoed(
            cx,
            format!(
                "{} {}'s {link} link points at {} {}, whose type is no Payments",
                mine.schema, mine.id, of.schema, of.id
            ),
            NOT_LINKED,
            None,
        ));
    };
    let idempotency_key = issue_key(&mine, all.len());
    let answer = cx.call_on(
        &of.schema,
        &of.id,
        &use_id,
        super::REFUND_FOR,
        &json!({"refund": mine.to_json(), "amount": live.due, "idempotencyKey": idempotency_key}),
    )?;
    let refund = answer
        .get("refund")
        .and_then(Value::as_str)
        .ok_or_else(|| cx.defect("refundFor returned no refund"))?;
    let currency = answer
        .get("currency")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let rows = cx.sub_rows(ISSUES)?;
    let row = Issue {
        seq: next_seq(&rows),
        refund: refund.to_owned(),
        of,
        idempotency_key,
        amount: live.due,
        currency: currency.to_owned(),
        status: RefundStatus::Requested,
        refunded: 0,
        entered_at,
        at: cx.now(),
        updated_at: cx.now(),
    };
    cx.insert_row(ISSUES, &row.refund, row.values())
}
