//! Holds, one claim's holds on any supply, tied to its Workflow (D70), the
//! `core-holds` suite: a checkout takes every line of Stock and a pickup
//! slot of a timed test supply as it enters `held`, all or none, keeps the
//! unit prices and their total, commits on `placed`, releases on `expired`
//! and `cancelled` and returns on `returned`, whichever operation moved the
//! status; a hold expires through a Timers deadline the runner fires; in
//! the late-payment race Payments holds what is being paid for past the
//! expiry, Reactions places the checkout once it is paid, and money
//! captured after the release is refunded; `holds_missing`,
//! `held_fields`, `committed`, `too_many_items`, `no_supply` and
//! `not_taking`; an update that changes what a claim in `held` asks while
//! it holds nothing takes it; `retakeHolds` after a new version adds a
//! hold; `listHolds`; a delete lets go; and two claims racing for the last
//! units never both hold them. On SQLite and, with the variable set, on
//! Postgres. After every step the ledger holds and every held or committed
//! item matches its supply's record (`commerce_support`).

mod commerce_support;
mod support;

use std::sync::{Arc, Barrier};

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineOptions, Error, InstanceRecord,
    Principal, RunnerOptions, SchemaTarget, WriteOptions,
};
use superschematic_engine_testkit::commerce_fixtures::TestSupply;

use commerce_support::{assert_ledger, assert_matched, items_of, principal, read, Supply};
use support::{alice, schema_document, targets, veto, Target};

const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;

/// When every test's clock starts: `Date.UTC(2026, 9, 21, 9)`.
const T0: i64 = 1_792_573_200_000;

/// When a checkout's pickup starts, the next day at 10:00 UTC.
const PICKUP: &str = "2026-10-22T10:00:00Z";
const PICKUP_MS: i64 = 1_792_663_200_000;

fn stocker() -> Principal {
    principal("sam", &["stock.adjust"])
}

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

fn behaviors() -> BehaviorSet {
    superschematic_engine_behaviors::core()
        .extend(superschematic_engine_commerce::commerce())
        .with(TestSupply::TIMED)
}

fn int(name: &str) -> Value {
    json!({"name": name, "typeRef": {"name": "Generic.Int64"}})
}

fn title() -> Value {
    json!({"name": "title", "typeRef": {"name": "string"}})
}

fn flow() -> Value {
    json!({
        "states": ["cart", "held", "placed", "expired", "cancelled", "returned"],
        "initial": "cart",
        "transitions": [
            {"from": "cart", "to": "held"},
            {"from": "cart", "to": "placed"},
            {"from": "held", "to": "placed"},
            {"from": "held", "to": "expired"},
            {"from": "held", "to": "cancelled"},
            {"from": "cart", "to": "cancelled"},
            {"from": "placed", "to": "returned"}
        ],
        "outcomes": {"expired": "failure", "cancelled": "neutral"}
    })
}

fn lines_hold() -> Value {
    json!({"each": {"field": "lines", "target": "inventoryId"}, "schema": "Inventory", "quantity": {"field": "quantity"}})
}

fn pickup_hold() -> Value {
    json!({"link": "counter", "interval": {"start": "pickupAt", "durationMs": 15 * MINUTE}})
}

fn holds_config(holds: Value) -> Value {
    json!({
        "holds": holds,
        "takeOn": ["held"],
        "commitOn": ["placed"],
        "releaseOn": ["expired", "cancelled"],
        "returnOn": ["returned"]
    })
}

fn timers() -> Value {
    json!({"deadlines": {
        "holdExpiry": {"start": {"enters": "held"}, "afterMs": 15 * MINUTE, "then": {"transition": "expired"}},
        "backstop": {"start": {"enters": "held"}, "afterMs": 2 * HOUR, "then": {"transition": "expired"}}
    }})
}

/// The Checkout schema: lines of Inventory, a pickup slot at a Counter,
/// Workflow, Links, Holds with the config given, and Timers.
fn checkout(holds: Value) -> Value {
    let mut document = schema_document(
        "Checkout",
        json!([
            title(),
            {"name": "lines", "typeRef": {"name": "Line", "isArray": true}},
            {"name": "pickupAt", "typeRef": {"name": "Temporal.DateTime"}}
        ]),
        json!({"types": {"Line": {"name": "Line", "role": "EmbeddedStruct", "fields": [
            {"name": "inventoryId", "typeRef": {"name": "string"}, "required": true},
            int("quantity")
        ]}}}),
    );
    document["types"]["Checkout"]["behaviors"] = json!([
        {"name": "Workflow", "config": flow()},
        {"name": "Links", "config": {"links": {"counter": {"schema": "Counter"}}}},
        {"name": "Holds", "config": holds},
        {"name": "Timers", "config": timers()},
    ]);
    document
}

/// The permission the payment provider's adapter holds.
const RECORD: &str = "payments.record";

/// The provider every payment is made at.
const PROVIDER: &str = "acmepay";

/// The Checkout schema with Payments of its `Holds.total`, which must be
/// paid to enter placed, voids in expired and cancelled and refunds money
/// captured after it lets go (`afterRelease: refund`), and Reactions that
/// place it once Payments says it is paid.
fn paid_checkout() -> Value {
    let mut document = checkout(holds_config(
        json!({"lines": lines_hold(), "pickup": pickup_hold()}),
    ));
    let behaviors = document["types"]["Checkout"]["behaviors"]
        .as_array_mut()
        .expect("the checkout's behaviors");
    behaviors.push(json!({"name": "Payments", "config": {
        "amount": {"holds": true},
        "require": {"placed": "paid"},
        "voidOn": ["expired", "cancelled"],
        "afterRelease": "refund",
        "record": {"permission": RECORD}
    }}));
    behaviors.push(json!({"name": "Reactions", "config": {"rules": [
        {"when": {"changes": {"field": "Payments.state", "to": "paid"}}, "then": {"transition": "placed"}}
    ]}}));
    document
}

/// The provider's adapter, which records what the provider did.
fn adapter() -> Principal {
    principal("adapter", &[RECORD])
}

/// Opens a payment of what a checkout owes; returns the attempt's id.
fn open_payment(engine: &Engine, id: &str) -> Value {
    let opened = invoke(
        engine,
        &alice(),
        "Checkout",
        id,
        "openPayment",
        json!({"provider": PROVIDER, "idempotencyKey": format!("{id}-pay")}),
    )
    .expect("openPayment");
    opened["attempt"].clone()
}

/// Records a provider event of an attempt in GBP, as the adapter reports
/// it.
fn pay_event(engine: &Engine, id: &str, event_id: &str, kind: &str, amount: i64, attempt: &Value) {
    invoke(
        engine,
        &adapter(),
        "Checkout",
        id,
        "recordPaymentEvent",
        json!({
            "provider": PROVIDER,
            "eventId": event_id,
            "kind": kind,
            "amount": amount,
            "currency": "GBP",
            "at": engine.clock().now(),
            "attempt": attempt
        }),
    )
    .unwrap_or_else(|error| panic!("record {event_id}: {error}"));
}

/// The actions waiting for the provider's adapter for one checkout, by
/// kind and amount.
fn waiting(engine: &Engine, id: &str) -> Vec<(String, Value)> {
    let page = engine
        .instances()
        .invoke_schema(
            &adapter(),
            "Checkout",
            "listPaymentActions",
            &json!({"provider": PROVIDER, "limit": 100}),
            None,
        )
        .expect("listPaymentActions");
    page["items"]
        .as_array()
        .cloned()
        .unwrap_or_default()
        .into_iter()
        .filter(|action| action["id"] == json!(id))
        .map(|action| {
            (
                action["kind"].as_str().unwrap_or_default().to_owned(),
                action["amount"].clone(),
            )
        })
        .collect()
}

fn payments_of(engine: &Engine, id: &str) -> Value {
    read(engine, "Checkout", id)
        .expect("the checkout")
        .behaviors["Payments"]
        .clone()
}

fn publish(engine: &Engine, document: &Value) -> Result<(), Error> {
    let record = engine.schemas().define(
        &alice(),
        document,
        DefineOptions {
            namespace: None,
            source: None,
        },
    )?;
    engine
        .schemas()
        .publish(&alice(), &record.name, SchemaTarget { namespace: None })?;
    Ok(())
}

fn configure(options: &mut EngineOptions) {
    options.clock = Clock::Manual(T0);
    options.runner = Some(RunnerOptions::new(runner()));
}

/// An engine with Inventory (Stock, 1250 GBP a unit), Counter (a timed
/// test supply, 500 GBP a slot) and the Checkout schema.
fn open_with(target: &Target, holds: Value) -> Engine {
    open_document(target, &checkout(holds))
}

/// Inventory: Stock at its priceCents in GBP.
fn inventory() -> Value {
    let mut inventory =
        schema_document("Inventory", json!([title(), int("priceCents")]), json!({}));
    inventory["types"]["Inventory"]["behaviors"] = json!([{"name": "Stock", "config": {
        "permission": "stock.adjust",
        "price": {"field": "priceCents", "currency": "GBP"}
    }}]);
    inventory
}

/// Counter: a timed test supply at its feeCents in GBP.
fn counter() -> Value {
    let mut counter = schema_document(
        "Counter",
        json!([title(), int("units"), int("feeCents")]),
        json!({}),
    );
    counter["types"]["Counter"]["behaviors"] =
        json!([{"name": "test.Slots", "config": {"priceField": "feeCents", "currency": "GBP"}}]);
    counter
}

/// An engine with Inventory, Counter and the Checkout schema given.
fn open_document(target: &Target, checkout: &Value) -> Engine {
    let engine = target.open_with(behaviors(), configure);
    publish(&engine, &inventory()).expect("publish Inventory");
    publish(&engine, &counter()).expect("publish Counter");
    publish(&engine, checkout).expect("publish Checkout");
    engine.runner().run_due().expect("the runner's first pass");
    for (id, units, price) in [("anvil", 3, 1250), ("rope", 5, 300)] {
        create(
            &engine,
            "Inventory",
            id,
            json!({"title": id, "priceCents": price}),
            json!({}),
        );
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            id,
            "receiveStock",
            json!({"quantity": units, "reason": "delivery"}),
        )
        .expect("receive");
    }
    create(
        &engine,
        "Counter",
        "front",
        json!({"title": "Front", "units": 1, "feeCents": 500}),
        json!({}),
    );
    engine
}

fn open(target: &Target) -> Engine {
    open_with(
        target,
        holds_config(json!({"lines": lines_hold(), "pickup": pickup_hold()})),
    )
}

fn create(
    engine: &Engine,
    schema: &str,
    id: &str,
    data: Value,
    behaviors: Value,
) -> InstanceRecord {
    try_create(engine, schema, id, data, behaviors).expect("create")
}

fn try_create(
    engine: &Engine,
    schema: &str,
    id: &str,
    data: Value,
    behaviors: Value,
) -> Result<InstanceRecord, Error> {
    engine.instances().create(
        &alice(),
        schema,
        &data,
        CreateOptions {
            id: Some(id),
            behaviors: behaviors
                .as_object()
                .filter(|given| !given.is_empty())
                .map(|_| &behaviors),
            ..Default::default()
        },
    )
}

fn invoke(
    engine: &Engine,
    who: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine
        .instances()
        .invoke(who, schema, id, operation, &params, WriteOptions::default())
}

fn move_to(engine: &Engine, id: &str, to: &str) -> Result<Value, Error> {
    invoke(
        engine,
        &alice(),
        "Checkout",
        id,
        "transition",
        json!({ "to": to }),
    )
}

fn update(engine: &Engine, id: &str, patch: Value) -> Result<InstanceRecord, Error> {
    engine
        .instances()
        .update(&alice(), "Checkout", id, &patch, WriteOptions::default())
}

fn line(id: &str, quantity: i64) -> Value {
    json!({"inventoryId": id, "quantity": quantity})
}

/// A checkout in cart with the lines given, and a pickup at the front
/// counter when `pickup`.
fn cart(engine: &Engine, id: &str, lines: Value, pickup: bool) -> InstanceRecord {
    let (data, links) = if pickup {
        (
            json!({"title": id, "lines": lines, "pickupAt": PICKUP}),
            json!({"Links": {"counter": "front"}}),
        )
    } else {
        (json!({"title": id, "lines": lines}), json!({}))
    };
    create(engine, "Checkout", id, data, links)
}

fn holds_of(engine: &Engine, id: &str) -> Value {
    read(engine, "Checkout", id)
        .expect("the checkout")
        .behaviors["Holds"]
        .clone()
}

fn status(engine: &Engine, id: &str) -> Value {
    read(engine, "Checkout", id)
        .expect("the checkout")
        .behaviors["Workflow"]["status"]
        .clone()
}

fn stock(engine: &Engine, id: &str) -> Value {
    read(engine, "Inventory", id)
        .expect("the inventory")
        .behaviors["Stock"]
        .clone()
}

fn states(engine: &Engine, id: &str) -> Vec<&'static str> {
    items_of(engine, "Checkout", id)
        .iter()
        .map(|item| item.state.as_str())
        .collect()
}

// holds checks the invariants after a step.
fn holds(engine: &Engine) {
    assert_ledger(engine, "Inventory", false);
    assert_matched(
        engine,
        &["Checkout"],
        &[
            Supply::Stock("Inventory"),
            Supply::Field("Counter", "test.Slots"),
        ],
    );
}

// sweep moves the clock on and runs what is due.
fn sweep(engine: &Engine, ms: i64) {
    engine.clock().advance(ms).expect("advance the clock");
    engine.runner().run_due().expect("the runner");
}

#[test]
fn a_checkout_takes_every_line_and_its_slot_as_it_enters_held_and_commits_as_it_is_placed() {
    for target in targets("holds_take_commit") {
        let engine = open(&target);
        let created = cart(
            &engine,
            "c1",
            json!([line("anvil", 2), line("rope", 1)]),
            true,
        );
        // In cart the items are asked, not held.
        assert_eq!(created.behaviors["Holds"]["state"], json!("pending"));
        assert_eq!(created.behaviors["Holds"]["total"], Value::Null);
        assert_eq!(stock(&engine, "anvil")["held"], json!(0));
        holds(&engine);

        move_to(&engine, "c1", "held").expect("hold");
        assert_eq!(
            holds_of(&engine, "c1"),
            json!({
                "items": [
                    {"hold": "lines", "item": 0, "schema": "Inventory", "id": "anvil", "quantity": 2, "state": "held", "unitPrice": 1250, "currency": "GBP"},
                    {"hold": "lines", "item": 1, "schema": "Inventory", "id": "rope", "quantity": 1, "state": "held", "unitPrice": 300, "currency": "GBP"},
                    {"hold": "pickup", "item": 0, "schema": "Counter", "id": "front", "startMs": PICKUP_MS, "endMs": PICKUP_MS + 15 * MINUTE, "state": "held", "unitPrice": 500, "currency": "GBP"}
                ],
                "state": "held",
                "total": {"amount": 2 * 1250 + 300 + 500, "currency": "GBP"}
            })
        );
        assert_eq!(
            stock(&engine, "anvil"),
            json!({"onHand": 3, "held": 2, "available": 1, "low": false})
        );
        holds(&engine);

        // A price change after the hold leaves the line at its price.
        engine
            .instances()
            .update(
                &alice(),
                "Inventory",
                "anvil",
                &json!({"priceCents": 9999}),
                WriteOptions::default(),
            )
            .expect("reprice");
        assert_eq!(holds_of(&engine, "c1")["total"]["amount"], json!(3300));

        move_to(&engine, "c1", "placed").expect("place");
        assert_eq!(
            states(&engine, "c1"),
            ["committed", "committed", "committed"]
        );
        assert_eq!(holds_of(&engine, "c1")["state"], json!("committed"));
        assert_eq!(
            stock(&engine, "anvil"),
            json!({"onHand": 1, "held": 0, "available": 1, "low": false})
        );
        holds(&engine);

        // A return gives the units back; the slot stays committed.
        move_to(&engine, "c1", "returned").expect("return");
        assert_eq!(states(&engine, "c1"), ["returned", "returned", "committed"]);
        assert_eq!(holds_of(&engine, "c1")["state"], json!("committed"));
        assert_eq!(stock(&engine, "anvil")["onHand"], json!(3));
        holds(&engine);
    }
}

#[test]
fn a_claim_holds_all_of_its_items_or_none_and_the_veto_lists_each_that_does_not_fit() {
    for target in targets("holds_all_or_none") {
        let engine = open(&target);
        cart(&engine, "c1", json!([line("anvil", 2)]), true);
        move_to(&engine, "c1", "held").expect("hold");
        cart(
            &engine,
            "c2",
            json!([line("rope", 1), line("anvil", 2), line("rope", 9)]),
            true,
        );
        let before = stock(&engine, "rope");
        let (behavior, action, reason, code, details) = veto(move_to(&engine, "c2", "held"));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("Holds", "transition", Some("unavailable"))
        );
        assert_eq!(reason, "3 items do not fit their supply");
        let listed: Vec<Value> = details.expect("details")["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|entry| {
                json!([
                    entry["supply"]["id"],
                    entry["hold"],
                    entry["item"],
                    entry.get("available").or(entry.get("conflict"))
                ])
            })
            .collect();
        assert_eq!(
            listed,
            [
                json!(["anvil", "lines", 1, 1]),
                json!(["rope", "lines", 2, 5]),
                json!(["front", "pickup", 0, {"startMs": PICKUP_MS, "endMs": PICKUP_MS + 15 * MINUTE}])
            ]
        );
        assert_eq!(status(&engine, "c2"), "cart");
        assert_eq!(
            states(&engine, "c2"),
            ["pending", "pending", "pending", "pending"]
        );
        assert_eq!(stock(&engine, "rope"), before);
        holds(&engine);

        // listHolds answers each pending item with its supply's check.
        let listed =
            invoke(&engine, &alice(), "Checkout", "c2", "listHolds", json!({})).expect("listHolds");
        let fits: Vec<Value> = listed["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| item["check"]["fits"].clone())
            .collect();
        assert_eq!(
            fits,
            [json!(true), json!(false), json!(false), json!(false)]
        );
        assert_eq!(listed["state"], "pending");

        // Once c1 lets go, c2 takes everything that fits; nothing is
        // held when one line still does not.
        move_to(&engine, "c1", "cancelled").expect("cancel");
        assert_eq!(states(&engine, "c1"), ["released", "released"]);
        let (_, _, _, code, details) = veto(move_to(&engine, "c2", "held"));
        assert_eq!(code.as_deref(), Some("unavailable"));
        assert_eq!(
            details.expect("details")["items"].as_array().map(Vec::len),
            Some(1)
        );
        update(
            &engine,
            "c2",
            json!({"lines": [line("rope", 1), line("anvil", 2), line("rope", 4)]}),
        )
        .expect("fewer rope");
        move_to(&engine, "c2", "held").expect("hold");
        assert_eq!(stock(&engine, "rope")["available"], json!(0));
        holds(&engine);
    }
}

#[test]
fn a_hold_expires_through_its_timers_deadline_and_the_runner_lets_it_go() {
    for target in targets("holds_expiry") {
        let engine = open(&target);
        cart(&engine, "c1", json!([line("anvil", 3)]), false);
        move_to(&engine, "c1", "held").expect("hold");
        assert_eq!(stock(&engine, "anvil")["available"], json!(0));
        sweep(&engine, 15 * MINUTE - 1);
        assert_eq!(status(&engine, "c1"), "held");
        sweep(&engine, 5_000);
        assert_eq!(status(&engine, "c1"), "expired");
        assert_eq!(
            read(&engine, "Checkout", "c1").expect("c1").behaviors["Timers"]["deadlines"]
                ["holdExpiry"]["outcome"],
            "moved"
        );
        assert_eq!(states(&engine, "c1"), ["released"]);
        assert_eq!(holds_of(&engine, "c1")["state"], "released");
        assert_eq!(stock(&engine, "anvil")["available"], json!(3));
        holds(&engine);
        // What expired is no longer the claim's: a second claim takes it.
        cart(&engine, "c2", json!([line("anvil", 3)]), false);
        move_to(&engine, "c2", "held").expect("hold");
        holds(&engine);
    }
}

#[test]
fn a_payment_in_progress_holds_what_is_paid_for_past_the_expiry() {
    for target in targets("holds_late_payment") {
        let engine = open_document(&target, &paid_checkout());
        paid_after_the_expiry(&engine);
        failed_after_the_expiry(&engine);
        captured_after_the_release(&engine);
    }
}

/// c1 is paid while its expiry fires: the expiry is vetoed, and the
/// capture places it through Reactions and commits what it holds.
fn paid_after_the_expiry(engine: &Engine) {
    cart(engine, "c1", json!([line("anvil", 2)]), false);
    move_to(engine, "c1", "held").expect("hold");
    // Payments owes what Holds totals.
    let owed = payments_of(engine, "c1");
    assert_eq!(
        (owed["due"].clone(), owed["currency"].clone()),
        (json!(2500), json!("GBP"))
    );
    // Not paid, so not placed.
    let (behavior, _, _, code, _) = veto(move_to(engine, "c1", "placed"));
    assert_eq!(
        (behavior.as_str(), code.as_deref()),
        ("Payments", Some("unpaid"))
    );
    let attempt = open_payment(engine, "c1");
    pay_event(engine, "c1", "c1-processing", "processing", 2500, &attempt);

    // The expiry fires, meets Payments' payment_in_progress, and is
    // spent: the claim keeps what it holds.
    sweep(engine, 15 * MINUTE + 5_000);
    let deadlines =
        read(engine, "Checkout", "c1").expect("c1").behaviors["Timers"]["deadlines"].clone();
    assert_eq!(deadlines["holdExpiry"]["fired"], json!(true));
    assert_eq!(deadlines["holdExpiry"]["outcome"], "vetoed");
    assert_eq!(status(engine, "c1"), "held");
    assert_eq!(states(engine, "c1"), ["held"]);
    assert_eq!(stock(engine, "anvil")["held"], json!(2));
    // What is held is what is being paid for: it does not change.
    let (_, action, _, code, _) = veto(update(engine, "c1", json!({"lines": [line("anvil", 1)]})));
    assert_eq!(
        (action.as_str(), code.as_deref()),
        ("update", Some("held_fields"))
    );
    holds(engine);

    // The provider captures it: Payments says paid, the runner hands
    // the change to Reactions, which places the checkout, and Holds
    // commits what it held.
    pay_event(engine, "c1", "c1-captured", "captured", 2500, &attempt);
    assert_eq!(payments_of(engine, "c1")["state"], json!("paid"));
    assert_eq!(status(engine, "c1"), "held");
    sweep(engine, 0);
    assert_eq!(status(engine, "c1"), "placed");
    assert_eq!(states(engine, "c1"), ["committed"]);
    assert_eq!(stock(engine, "anvil")["onHand"], json!(1));
    assert_eq!(stock(engine, "anvil")["held"], json!(0));
    assert!(
        waiting(engine, "c1").is_empty(),
        "{:?}",
        waiting(engine, "c1")
    );
    holds(engine);
}

/// c2's payment fails after its expiry was vetoed: the backstop lets go.
fn failed_after_the_expiry(engine: &Engine) {
    // A payment that fails after the expiry was refused lets the
    // backstop release the claim, and nothing is left to void.
    cart(engine, "c2", json!([line("anvil", 1)]), false);
    move_to(engine, "c2", "held").expect("hold");
    let attempt = open_payment(engine, "c2");
    pay_event(engine, "c2", "c2-processing", "processing", 1250, &attempt);
    sweep(engine, 15 * MINUTE + 5_000);
    assert_eq!(status(engine, "c2"), "held");
    pay_event(engine, "c2", "c2-failed", "failed", 1250, &attempt);
    sweep(engine, 2 * HOUR);
    assert_eq!(status(engine, "c2"), "expired");
    assert_eq!(states(engine, "c2"), ["released"]);
    assert_eq!(stock(engine, "anvil")["available"], json!(1));
    assert!(
        waiting(engine, "c2").is_empty(),
        "{:?}",
        waiting(engine, "c2")
    );
    holds(engine);
}

/// c3's payment is captured after its expiry let go of what it held.
fn captured_after_the_release(engine: &Engine) {
    // A payment opened and never reported processing does not hold the
    // claim: the expiry releases it and voids the attempt. When the
    // provider captures it after all, the claim does not take back
    // what it let go, and afterRelease refunds the money.
    cart(engine, "c3", json!([line("anvil", 1)]), false);
    move_to(engine, "c3", "held").expect("hold");
    let attempt = open_payment(engine, "c3");
    sweep(engine, 15 * MINUTE + 5_000);
    assert_eq!(status(engine, "c3"), "expired");
    assert_eq!(states(engine, "c3"), ["released"]);
    assert_eq!(
        waiting(engine, "c3")
            .iter()
            .map(|(kind, _)| kind.as_str())
            .collect::<Vec<_>>(),
        ["create", "void"]
    );
    pay_event(engine, "c3", "c3-captured", "captured", 1250, &attempt);
    assert_eq!(status(engine, "c3"), "expired");
    assert_eq!(states(engine, "c3"), ["released"]);
    assert_eq!(stock(engine, "anvil")["available"], json!(1));
    assert_eq!(waiting(engine, "c3"), [("refund".to_owned(), json!(1250))]);
    holds(engine);
}

#[test]
fn a_claim_commits_only_what_it_holds_and_what_it_holds_does_not_change() {
    for target in targets("holds_guards") {
        let engine = open(&target);
        cart(&engine, "c1", json!([line("anvil", 1)]), true);
        // Straight from cart to placed: nothing is held.
        let (behavior, _, reason, code, details) = veto(move_to(&engine, "c1", "placed"));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Holds", Some("holds_missing"))
        );
        assert_eq!(
            reason,
            "2 of the claim's items are not held, so it cannot move to placed"
        );
        assert_eq!(
            details,
            Some(json!({"items": [
                {"hold": "lines", "item": 0, "state": "pending"},
                {"hold": "pickup", "item": 0, "state": "pending"}
            ]}))
        );

        move_to(&engine, "c1", "held").expect("hold");
        let (_, action, reason, code, details) =
            veto(update(&engine, "c1", json!({"lines": [line("anvil", 2)]})));
        assert_eq!(
            (action.as_str(), code.as_deref()),
            ("update", Some("held_fields"))
        );
        assert_eq!(
            reason,
            "lines reads what the claim holds; it does not change while it holds"
        );
        assert_eq!(details, Some(json!({"fields": ["lines"]})));
        assert_eq!(
            veto(update(
                &engine,
                "c1",
                json!({"pickupAt": "2026-10-22T11:00:00Z"})
            ))
            .3
            .as_deref(),
            Some("held_fields")
        );
        update(&engine, "c1", json!({"title": "renamed"})).expect("a field no hold reads");
        let (_, action, _, code, _) = veto(invoke(
            &engine,
            &alice(),
            "Checkout",
            "c1",
            "unlink",
            json!({"name": "counter"}),
        ));
        assert_eq!(
            (action.as_str(), code.as_deref()),
            ("unlink", Some("held_fields"))
        );
        holds(&engine);

        // A supply that holds is not deleted.
        let (behavior, _, _, code, _) = veto(engine.instances().delete(
            &alice(),
            "Inventory",
            "anvil",
            WriteOptions::default(),
        ));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Stock", Some("held"))
        );

        // A placed checkout with a slot cannot be deleted: time is never
        // returned.
        move_to(&engine, "c1", "placed").expect("place");
        let (behavior, _, reason, code, details) = veto(engine.instances().delete(
            &alice(),
            "Checkout",
            "c1",
            WriteOptions::default(),
        ));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Holds", Some("committed"))
        );
        assert_eq!(
            reason,
            "the claim holds 1 committed item, which is not returned"
        );
        assert_eq!(
            details,
            Some(json!({"items": [{"hold": "pickup", "item": 0}]}))
        );

        // retakeHolds outside a takeOn state, and with every item held.
        assert_eq!(
            veto(invoke(
                &engine,
                &alice(),
                "Checkout",
                "c1",
                "retakeHolds",
                json!({})
            ))
            .3
            .as_deref(),
            Some("not_taking")
        );
        cart(&engine, "c2", json!([line("rope", 1)]), false);
        move_to(&engine, "c2", "held").expect("hold");
        let (_, _, reason, code, _) = veto(invoke(
            &engine,
            &alice(),
            "Checkout",
            "c2",
            "retakeHolds",
            json!({}),
        ));
        assert_eq!(code.as_deref(), Some("not_taking"));
        assert_eq!(reason, "the claim holds every item its holds ask");

        // An item whose supply does not exist.
        cart(&engine, "c3", json!([line("nothing", 1)]), false);
        let (_, _, _, code, details) = veto(move_to(&engine, "c3", "held"));
        assert_eq!(code.as_deref(), Some("no_supply"));
        assert_eq!(
            details,
            Some(
                json!({"supply": {"schema": "Inventory", "id": "nothing"}, "hold": "lines", "item": 0})
            )
        );
        holds(&engine);
    }
}

#[test]
fn an_update_in_held_that_holds_nothing_takes_what_it_now_asks() {
    for target in targets("holds_update_takes") {
        let engine = open(&target);
        cart(&engine, "c1", json!([]), false);
        move_to(&engine, "c1", "held").expect("an empty hold");
        assert_eq!(holds_of(&engine, "c1")["state"], "none");
        let (_, action, _, code, _) =
            veto(update(&engine, "c1", json!({"lines": [line("anvil", 4)]})));
        assert_eq!(
            (action.as_str(), code.as_deref()),
            ("update", Some("unavailable"))
        );
        update(&engine, "c1", json!({"lines": [line("anvil", 2)]})).expect("takes");
        assert_eq!(states(&engine, "c1"), ["held"]);
        assert_eq!(stock(&engine, "anvil")["held"], json!(2));
        holds(&engine);
    }
}

#[test]
fn retake_holds_takes_a_hold_a_new_version_adds() {
    for target in targets("holds_retake") {
        let engine = open_with(&target, holds_config(json!({"lines": lines_hold()})));
        let created = create(
            &engine,
            "Checkout",
            "c1",
            json!({"title": "c1", "lines": [line("anvil", 1)], "pickupAt": PICKUP}),
            json!({"Links": {"counter": "front"}}),
        );
        assert_eq!(
            created.behaviors["Holds"]["items"].as_array().map(Vec::len),
            Some(1)
        );
        move_to(&engine, "c1", "held").expect("hold");
        publish(
            &engine,
            &checkout(holds_config(
                json!({"lines": lines_hold(), "pickup": pickup_hold()}),
            )),
        )
        .expect("a pickup hold");
        assert_eq!(states(&engine, "c1"), ["held", "pending"]);
        let taken = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c1",
            "retakeHolds",
            json!({}),
        )
        .expect("retake");
        assert_eq!(taken["state"], "held");
        assert_eq!(taken["total"], json!({"amount": 1750, "currency": "GBP"}));
        assert_eq!(states(&engine, "c1"), ["held", "held"]);
        holds(&engine);
    }
}

#[test]
fn a_delete_lets_go_of_what_the_claim_holds_and_returns_what_it_committed() {
    for target in targets("holds_delete") {
        let engine = open(&target);
        cart(&engine, "c1", json!([line("anvil", 2)]), true);
        move_to(&engine, "c1", "held").expect("hold");
        assert!(engine
            .instances()
            .delete(&alice(), "Checkout", "c1", WriteOptions::default())
            .expect("delete"));
        assert_eq!(stock(&engine, "anvil")["held"], json!(0));
        let slots = read(&engine, "Counter", "front").expect("front").behaviors["test.Slots"]
            ["records"]
            .clone();
        assert_eq!(slots[0]["state"], "released");
        holds(&engine);

        // Committed units come back with a returnOn state.
        cart(&engine, "c2", json!([line("anvil", 2)]), false);
        move_to(&engine, "c2", "held").expect("hold");
        move_to(&engine, "c2", "placed").expect("place");
        assert_eq!(stock(&engine, "anvil")["onHand"], json!(1));
        assert!(engine
            .instances()
            .delete(&alice(), "Checkout", "c2", WriteOptions::default())
            .expect("delete"));
        assert_eq!(stock(&engine, "anvil")["onHand"], json!(3));
        holds(&engine);
        // The supply holds nothing now, and goes.
        assert!(engine
            .instances()
            .delete(&alice(), "Counter", "front", WriteOptions::default())
            .expect("delete the counter"));
    }
}

#[test]
fn a_config_is_held_to_the_types_workflow_links_and_fields() {
    for target in targets("holds_config") {
        let engine = open(&target);
        let refused = |holds: Value| {
            publish(&engine, &checkout(holds))
                .expect_err("refused")
                .to_string()
        };
        let mut states = holds_config(json!({"lines": lines_hold()}));
        states["takeOn"] = json!(["shipped"]);
        assert!(refused(states)
            .contains("takeOn names \"shipped\", which is not a state of the type's Workflow"));
        let mut overlap = holds_config(json!({"lines": lines_hold()}));
        overlap["releaseOn"] = json!(["placed"]);
        assert!(refused(overlap).contains("commitOn and releaseOn both name \"placed\""));
        let mut back = holds_config(json!({"lines": lines_hold()}));
        back["releaseOn"] = json!(["cart"]);
        assert!(refused(back).contains("a supply holds each item of a claim once"));
        assert!(refused(holds_config(
            json!({"lines": {"link": "nowhere", "quantity": 1}})
        ))
        .contains("holds.lines.link \"nowhere\" is not a link of the type's Links config"));
        assert!(refused(holds_config(json!({"lines": {"each": {"field": "title", "target": "id"}, "schema": "Inventory", "quantity": 1}})))
            .contains("holds.lines.each.field \"title\" is not a list field of Checkout"));
        assert!(refused(holds_config(json!({"slot": {"link": "counter", "interval": {"start": "title", "durationMs": 60000}}})))
            .contains("holds.slot.interval.start \"title\" is not a date-time field of Checkout"));
        assert!(refused(holds_config(json!({"lines": {"each": {"field": "lines", "target": "inventoryId"}, "schema": "Inventory", "interval": {"start": "at", "durationMs": 60000}}})))
            .contains("holds.lines asks an interval of Inventory, whose Stock holds units"));
    }
}

#[test]
fn a_deploy_publishes_a_claim_after_the_supplies_it_names() {
    for target in targets("holds_deploy_order") {
        let engine = target.open_with(behaviors(), configure);
        // An engine document lists its types in name order: Checkout
        // names Inventory, which is not live yet when it comes first.
        let documents = [paid_checkout(), counter(), inventory()];
        let deployed = engine
            .deploy(&documents, None)
            .expect("the deploy publishes Checkout after its supplies");
        assert_eq!(
            deployed
                .iter()
                .map(|one| (one.name.as_str(), one.version, one.published))
                .collect::<Vec<_>>(),
            [
                ("Checkout", 1, true),
                ("Counter", 1, true),
                ("Inventory", 1, true)
            ]
        );
        // A second deploy writes nothing.
        let again = engine.deploy(&documents, None).expect("deploy again");
        assert!(again.iter().all(|one| !one.defined && !one.published));
        // A document no order fixes is refused, after the rest deployed.
        let mut broken = paid_checkout();
        broken["types"]["Checkout"]["behaviors"][2]["config"]["holds"]["lines"]["schema"] =
            json!("Nowhere");
        let refused = engine
            .deploy(&[broken, inventory()], None)
            .expect_err("Nowhere is never live");
        assert!(refused.to_string().contains("Nowhere"), "{refused}");
    }
}

#[test]
fn two_claims_racing_for_the_last_units_never_both_hold_them() {
    for target in targets("holds_race") {
        let engine = Arc::new(open(&target));
        // Two engines on one database stand for two replicas on Postgres;
        // on SQLite one process writes, and the threads share the engine.
        let other = match target.url() {
            Some(_) => Arc::new(target.open_with(behaviors(), configure)),
            None => Arc::clone(&engine),
        };
        // 2 units left, and each claim asks 2.
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "rope",
            "countStock",
            json!({"onHand": 2, "reason": "the last two"}),
        )
        .expect("count");
        let rounds = 8;
        for round in 0..rounds {
            let a = format!("a{round}");
            let b = format!("b{round}");
            cart(&engine, &a, json!([line("rope", 2)]), false);
            cart(&engine, &b, json!([line("rope", 2)]), false);
            let barrier = Arc::new(Barrier::new(2));
            let handles: Vec<_> = [
                (Arc::clone(&engine), a.clone()),
                (Arc::clone(&other), b.clone()),
            ]
            .into_iter()
            .map(|(engine, id)| {
                let barrier = Arc::clone(&barrier);
                std::thread::spawn(move || {
                    barrier.wait();
                    loop {
                        match move_to(&engine, &id, "held") {
                            Err(error)
                                if error.code()
                                    == Some(superschematic_engine::EngineErrorCode::Busy) =>
                            {
                                continue
                            }
                            other => return other.is_ok(),
                        }
                    }
                })
            })
            .collect();
            let won: Vec<bool> = handles
                .into_iter()
                .map(|handle| handle.join().expect("a thread"))
                .collect();
            assert_eq!(
                won.iter().filter(|won| **won).count(),
                1,
                "round {round}: {won:?}"
            );
            let rope = stock(&engine, "rope");
            assert!(
                rope["held"].as_i64().unwrap_or(0) <= rope["onHand"].as_i64().unwrap_or(0),
                "{rope}"
            );
            holds(&engine);
            // The winner lets go for the next round.
            let winner = if won[0] { &a } else { &b };
            move_to(&engine, winner, "cancelled").expect("cancel");
        }
    }
}
