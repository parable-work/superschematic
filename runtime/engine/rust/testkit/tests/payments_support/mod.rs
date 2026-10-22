//! What the Payments tests (D70) share: an engine with the core's
//! behaviors and Payments on a clock that stands still, with the runner;
//! a Checkout whose Workflow holds, places, expires and cancels, and whose
//! Payments config each test gives; the principals that pay, record what a
//! provider did and refund; a provider event as an adapter reports it; and
//! the ledger's invariants, checked after every step.

#![allow(dead_code)]

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, Engine, Error, GetOptions, InstanceRecord, Principal,
    RunnerOptions, WriteOptions,
};
use superschematic_engine_commerce::Payments;

use crate::support::{alice, publish, schema_document, Target};

/// When every Payments test's clock starts.
pub const T0: i64 = 1_793_000_000_000;

/// The provider every test pays at.
pub const PROVIDER: &str = "acmepay";

/// The permission the adapter holds.
pub const RECORD: &str = "payments.record";

/// The permission a refund needs.
pub const REFUND: &str = "payments.refund";

/// A principal with the permissions given.
pub fn principal(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(subject, permissions.iter().copied())
}

/// The provider's adapter: it records what the provider did.
pub fn adapter() -> Principal {
    principal("adapter", &[RECORD])
}

/// Staff who refund directly.
pub fn refunder() -> Principal {
    principal("rita", &[REFUND])
}

/// The runner's principal.
pub fn runner() -> Principal {
    principal("runner", &[])
}

/// An engine with the core's behaviors, Payments and the extra behaviors
/// given, on a clock that stands at [`T0`], with the runner.
pub fn open(target: &Target, extra: BehaviorSet) -> Engine {
    let behaviors = superschematic_engine_behaviors::core()
        .extend(BehaviorSet::new().with(Payments))
        .extend(extra);
    target.open_with(behaviors, |options| {
        options.clock = Clock::Manual(T0);
        options.runner = Some(RunnerOptions::new(runner()));
    })
}

/// A checkout's Workflow: a cart is held, then placed, shipped, or let go
/// by expiry or cancellation.
pub fn checkout_flow() -> Value {
    json!({
        "name": "Workflow",
        "config": {
            "states": ["cart", "held", "placed", "shipped", "expired", "cancelled"],
            "initial": "cart",
            "transitions": [
                {"from": "cart", "to": "held"},
                {"from": "held", "to": "placed"},
                {"from": "placed", "to": "shipped"},
                {"from": "held", "to": "expired"},
                {"from": "held", "to": "cancelled"},
                {"from": "cart", "to": "cancelled"}
            ],
            "outcomes": {"expired": "failure", "cancelled": "neutral"}
        }
    })
}

/// A Payments config over the checkout's totalCents in GBP, which
/// requires placed to be paid, voids in expired and cancelled, and lets
/// the adapter record and staff refund; `more` adds or replaces members.
pub fn payments_config(more: Value) -> Value {
    let mut config = json!({
        "amount": {"field": "totalCents"},
        "currency": "GBP",
        "require": {"placed": "paid"},
        "voidOn": ["expired", "cancelled"],
        "record": {"permission": RECORD},
        "refunds": {"permission": REFUND}
    });
    if let (Some(config), Value::Object(more)) = (config.as_object_mut(), more) {
        for (key, value) in more {
            if value.is_null() {
                config.remove(&key);
            } else {
                config.insert(key, value);
            }
        }
    }
    config
}

/// The checkout's own fields: a title, its total in minor units and a
/// currency code.
pub fn checkout_fields() -> Value {
    json!([
        {"name": "title", "typeRef": {"name": "string"}},
        {"name": "totalCents", "typeRef": {"name": "Generic.Int64"}},
        {"name": "currency", "typeRef": {"name": "string"}}
    ])
}

/// A schema named `name` like the checkout, composing the behaviors given.
pub fn checkout_document(name: &str, behaviors: Value) -> Value {
    let mut document = schema_document(name, checkout_fields(), json!({}));
    document["types"][name]["behaviors"] = behaviors;
    document
}

/// Publishes a Checkout with the Workflow and a Payments config.
pub fn publish_checkout(engine: &Engine, config: Value) {
    publish(
        engine,
        &checkout_document(
            "Checkout",
            json!([checkout_flow(), {"name": "Payments", "config": config}]),
        ),
        None,
    );
}

/// Creates an instance with fixed id.
pub fn create(
    engine: &Engine,
    schema: &str,
    id: &str,
    data: Value,
    behaviors: Option<Value>,
) -> Result<InstanceRecord, Error> {
    engine.instances().create(
        &alice(),
        schema,
        &data,
        CreateOptions {
            id: Some(id),
            behaviors: behaviors.as_ref(),
            ..Default::default()
        },
    )
}

/// Creates a checkout of a total.
pub fn checkout(engine: &Engine, id: &str, total: i64) {
    create(
        engine,
        "Checkout",
        id,
        json!({"title": id, "totalCents": total, "currency": "GBP"}),
        None,
    )
    .expect("create the checkout");
}

/// Invokes an operation as a principal.
pub fn invoke(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine.instances().invoke(
        principal,
        schema,
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

/// Moves an instance's Workflow as a principal.
pub fn transition(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: &str,
    to: &str,
) -> Result<Value, Error> {
    invoke(
        engine,
        principal,
        schema,
        id,
        "transition",
        json!({"to": to}),
    )
}

/// Updates an instance's own fields as alice.
pub fn update(
    engine: &Engine,
    schema: &str,
    id: &str,
    patch: Value,
) -> Result<InstanceRecord, Error> {
    engine
        .instances()
        .update(&alice(), schema, id, &patch, WriteOptions::default())
}

/// Opens a payment at the provider as alice.
pub fn open_payment(engine: &Engine, id: &str, key: &str) -> Result<Value, Error> {
    invoke(
        engine,
        &alice(),
        "Checkout",
        id,
        "openPayment",
        json!({"provider": PROVIDER, "idempotencyKey": key, "returnUrl": "https://shop.test/back"}),
    )
}

/// A provider event as the adapter reports it, in GBP at T0; `more` adds
/// or replaces members.
pub fn event(event_id: &str, kind: &str, amount: i64, more: Value) -> Value {
    let mut out = json!({
        "provider": PROVIDER,
        "eventId": event_id,
        "kind": kind,
        "amount": amount,
        "currency": "GBP",
        "at": T0
    });
    if let (Some(out), Value::Object(more)) = (out.as_object_mut(), more) {
        out.extend(more);
    }
    out
}

/// Records a provider event as the adapter.
pub fn record(engine: &Engine, schema: &str, id: &str, params: Value) -> Result<Value, Error> {
    invoke(engine, &adapter(), schema, id, "recordPaymentEvent", params)
}

/// An instance as alice reads it.
pub fn read(engine: &Engine, schema: &str, id: &str) -> InstanceRecord {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
        .expect("the instance")
}

/// An instance's Payments fields.
pub fn payments(engine: &Engine, schema: &str, id: &str) -> Value {
    read(engine, schema, id)
        .behaviors
        .get("Payments")
        .cloned()
        .expect("Payments' fields")
}

/// An instance's Workflow status.
pub fn status(engine: &Engine, schema: &str, id: &str) -> String {
    read(engine, schema, id)
        .behavior_field("Workflow", "status")
        .and_then(Value::as_str)
        .expect("a status")
        .to_owned()
}

/// The actions waiting for the provider, as the adapter lists them: every
/// page.
pub fn actions(engine: &Engine, schema: &str) -> Vec<Value> {
    let mut out = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let mut params = json!({"provider": PROVIDER, "limit": 2});
        if let Some(cursor) = &cursor {
            params["cursor"] = json!(cursor);
        }
        let page = engine
            .instances()
            .invoke_schema(&adapter(), schema, "listPaymentActions", &params, None)
            .expect("listPaymentActions");
        out.extend(page["items"].as_array().cloned().unwrap_or_default());
        match page["next"].as_str() {
            Some(next) => cursor = Some(next.to_owned()),
            None => return out,
        }
    }
}

/// The kinds of the waiting actions of an instance, in order.
pub fn kinds(engine: &Engine, schema: &str, id: &str) -> Vec<String> {
    actions(engine, schema)
        .iter()
        .filter(|action| action["id"] == json!(id))
        .map(|action| action["kind"].as_str().unwrap_or_default().to_owned())
        .collect()
}

/// The ledger's invariants, as a read shows them: the attempts' captures
/// add up to `captured`, none is past what it may capture, nothing more is
/// refunded than captured, no more overpaid than captured, and
/// `outstanding` is `due − captured`, never below 0.
pub fn assert_ledger(engine: &Engine, schema: &str, id: &str) {
    let fields = payments(engine, schema, id);
    let number = |value: &Value| value.as_i64().unwrap_or(0);
    let attempts = fields["attempts"].as_array().cloned().unwrap_or_default();
    let captured: i64 = attempts
        .iter()
        .map(|attempt| number(&attempt["captured"]))
        .sum();
    assert_eq!(captured, number(&fields["captured"]), "{fields}");
    for attempt in &attempts {
        let authorized = number(&attempt["authorized"]);
        let cap = if authorized > 0 {
            authorized
        } else {
            number(&attempt["amount"])
        };
        assert!(number(&attempt["captured"]) <= cap, "{attempt}");
    }
    assert!(
        number(&fields["refunded"]) <= number(&fields["captured"]),
        "{fields}"
    );
    assert!(
        (0..=number(&fields["captured"])).contains(&number(&fields["overpaid"])),
        "{fields}"
    );
    assert_eq!(
        number(&fields["outstanding"]),
        (number(&fields["due"]) - number(&fields["captured"])).max(0),
        "{fields}"
    );
}
