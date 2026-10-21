//! The supply contract (D70, `superschematic_engine_commerce::supply`):
//! a claim holds, commits, releases and returns what its items ask of a
//! supply through the contract's operations, all of its items or none; a
//! supply takes a call only for an item its claim's `Holds.items` already
//! shows in the state the call moves the supply's record to, so a caller
//! cannot hold or free supply by calling it directly; a call made again
//! changes nothing; what does not fit is `unavailable`, its details naming
//! what was asked and what is free; and a supply is not deleted while it
//! holds anything. A test supply on the contract's helpers alone, counted
//! and timed, and a stand-in for `Holds` (`commerce_fixtures`) play the two
//! sides, on SQLite and, with the variable set, on Postgres. After every
//! step each held or committed item of a claim matches its supply's
//! record, and each record that takes supply matches its claim's item.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, CreateOptions, Engine, Error, GetOptions, InstanceRecord, ListOptions,
    WriteOptions,
};
use superschematic_engine_commerce::supply::{HolderItem, ItemState};
use superschematic_engine_testkit::commerce_fixtures::{HoldsStandIn, TestSupply};
use support::{alice, publish, schema_document, targets, veto, Target};

fn open(target: &Target) -> Engine {
    let engine = target.open(
        BehaviorSet::new()
            .with(TestSupply::COUNTED)
            .with(TestSupply::TIMED)
            .with(HoldsStandIn),
    );
    let int = |name: &str| json!({"name": name, "typeRef": {"name": "Generic.Int64"}});
    let title = json!({"name": "title", "typeRef": {"name": "string"}});
    let mut shelf = schema_document(
        "Shelf",
        json!([title, int("units"), int("priceCents")]),
        json!({}),
    );
    shelf["types"]["Shelf"]["behaviors"] =
        json!([{"name": "test.Supply", "config": {"priceField": "priceCents", "currency": "GBP"}}]);
    let mut room = schema_document("Room", json!([title, int("units")]), json!({}));
    room["types"]["Room"]["behaviors"] = json!([{"name": "test.Slots"}]);
    let mut cart = schema_document("Cart", json!([title]), json!({}));
    cart["types"]["Cart"]["behaviors"] = json!([{"name": "Holds"}]);
    for document in [shelf, room, cart] {
        publish(&engine, &document, None);
    }
    engine
}

fn create(engine: &Engine, schema: &str, id: &str, data: Value) -> InstanceRecord {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create")
}

fn invoke(
    engine: &Engine,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine.instances().invoke(
        &alice(),
        schema,
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn read(engine: &Engine, schema: &str, id: &str) -> InstanceRecord {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
        .expect("the instance")
}

fn ids(engine: &Engine, schema: &str) -> Vec<String> {
    engine
        .instances()
        .list(&alice(), schema, ListOptions::default())
        .expect("list")
        .items
        .into_iter()
        .map(|record| record.id)
        .collect()
}

fn items(engine: &Engine, cart: &str) -> Vec<HolderItem> {
    read(engine, "Cart", cart)
        .behavior_field("Holds", "items")
        .and_then(Value::as_array)
        .expect("Holds.items")
        .iter()
        .map(|item| HolderItem::from_json(item).expect("an item"))
        .collect()
}

fn records(engine: &Engine, schema: &str, id: &str) -> Vec<Value> {
    let behavior = if schema == "Room" {
        "test.Slots"
    } else {
        "test.Supply"
    };
    read(engine, schema, id)
        .behavior_field(behavior, "records")
        .and_then(Value::as_array)
        .cloned()
        .expect("records")
}

fn states(engine: &Engine, cart: &str) -> Vec<&'static str> {
    items(engine, cart)
        .iter()
        .map(|item| item.state.as_str())
        .collect()
}

fn stage(engine: &Engine, cart: &str, staged: Value) {
    invoke(engine, "Cart", cart, "stage", json!({ "items": staged })).expect("stage");
}

fn advance(engine: &Engine, cart: &str, to: &str) -> Result<Value, Error> {
    invoke(engine, "Cart", cart, "move", json!({ "to": to }))
}

/// Each held or committed item of every cart has its supply's record in
/// the same state, asking the same; each record that takes supply has its
/// cart's item in the same state.
fn assert_matched(engine: &Engine) {
    for cart in ids(engine, "Cart") {
        for item in items(engine, &cart) {
            if !item.state.takes() {
                continue;
            }
            let found = records(engine, &item.supply.schema, &item.supply.id)
                .into_iter()
                .find(|record| {
                    record["holder"] == json!({"schema": "Cart", "id": cart})
                        && record["hold"] == json!(item.hold)
                        && record["item"] == json!(item.item)
                })
                .unwrap_or_else(|| panic!("no record of {cart}'s item {item:?}"));
            assert_eq!(
                found["state"],
                json!(item.state.as_str()),
                "{cart}: {item:?}"
            );
            match item.ask.quantity() {
                Some(quantity) => assert_eq!(found["quantity"], json!(quantity)),
                None => assert_eq!(
                    found["interval"],
                    item.ask.interval().expect("an interval").to_json()
                ),
            }
        }
    }
    for (schema, supply) in ids(engine, "Shelf")
        .into_iter()
        .map(|id| ("Shelf", id))
        .chain(ids(engine, "Room").into_iter().map(|id| ("Room", id)))
    {
        for record in records(engine, schema, &supply) {
            let state =
                ItemState::parse(record["state"].as_str().expect("a state")).expect("a state");
            if !state.takes() {
                continue;
            }
            let cart = record["holder"]["id"].as_str().expect("a holder");
            let item = items(engine, cart)
                .into_iter()
                .find(|item| item.hold == record["hold"] && json!(item.item) == record["item"])
                .unwrap_or_else(|| {
                    panic!("{schema} {supply} records {record}, which {cart} lacks")
                });
            assert_eq!(item.state, state, "{schema} {supply}: {record}");
            assert_eq!(
                (item.supply.schema.as_str(), item.supply.id.as_str()),
                (schema, supply.as_str())
            );
        }
    }
}

fn line(shelf: &str, item: i64, quantity: i64) -> Value {
    json!({"hold": "lines", "item": item, "schema": "Shelf", "id": shelf, "quantity": quantity})
}

fn slot(room: &str, start_ms: i64, end_ms: i64) -> Value {
    json!({"hold": "pickup", "item": 0, "schema": "Room", "id": room, "startMs": start_ms, "endMs": end_ms})
}

fn events(engine: &Engine, schema: &str, id: &str) -> i64 {
    read(engine, schema, id).seq
}

#[test]
fn a_claim_holds_commits_returns_and_releases_counted_supply_through_the_contract() {
    for target in targets("a_claim_holds_counted_supply") {
        let engine = open(&target);
        create(
            &engine,
            "Shelf",
            "anvil",
            json!({"title": "Anvil", "units": 3, "priceCents": 1250}),
        );
        for cart in ["c1", "c2"] {
            create(&engine, "Cart", cart, json!({}));
            stage(&engine, cart, json!([line("anvil", 0, 2)]));
        }
        assert_eq!(states(&engine, "c1"), ["pending"]);
        assert_matched(&engine);

        // c1 holds 2 of 3, at the shelf's price.
        assert_eq!(
            advance(&engine, "c1", "held").expect("hold"),
            json!({"moved": 1})
        );
        let held = &items(&engine, "c1")[0];
        assert_eq!(
            held.to_json(),
            json!({"hold": "lines", "item": 0, "schema": "Shelf", "id": "anvil", "quantity": 2, "state": "held", "unitPrice": 1250, "currency": "GBP"})
        );
        assert_matched(&engine);

        // c2's 2 do not fit: check lists the item, and the hold is vetoed
        // with the supply's details, leaving c2 pending.
        let (behavior, action, _, code, details) =
            veto(invoke(&engine, "Cart", "c2", "check", json!({})));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("Holds", "check", Some("unavailable"))
        );
        assert_eq!(
            details.expect("details"),
            json!({"items": [{
                "code": "unavailable",
                "reason": "2 asked of Shelf anvil, which has 1 free",
                "supply": {"schema": "Shelf", "id": "anvil"},
                "hold": "lines", "item": 0, "asked": {"quantity": 2}, "available": 1
            }]})
        );
        let (behavior, action, _, code, details) = veto(advance(&engine, "c2", "held"));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("test.Supply", "holdFor", Some("unavailable"))
        );
        assert_eq!(
            details.expect("details"),
            json!({"supply": {"schema": "Shelf", "id": "anvil"}, "hold": "lines", "item": 0, "asked": {"quantity": 2}, "available": 1})
        );
        assert_eq!(states(&engine, "c2"), ["pending"]);
        assert_eq!(records(&engine, "Shelf", "anvil").len(), 1);
        assert_matched(&engine);

        // Committed units stay taken; returned ones come back.
        advance(&engine, "c1", "committed").expect("commit");
        assert_eq!(states(&engine, "c1"), ["committed"]);
        assert_eq!(
            veto(advance(&engine, "c2", "held")).3.as_deref(),
            Some("unavailable")
        );
        assert_matched(&engine);
        advance(&engine, "c1", "returned").expect("return");
        assert_eq!(states(&engine, "c1"), ["returned"]);
        assert_eq!(
            invoke(&engine, "Cart", "c2", "check", json!({})).expect("check"),
            json!({"fits": true})
        );
        advance(&engine, "c2", "held").expect("hold");
        assert_matched(&engine);

        // c2 lets go, and its record keeps the price it was held at.
        advance(&engine, "c2", "released").expect("release");
        let record = records(&engine, "Shelf", "anvil")
            .into_iter()
            .find(|record| record["holder"]["id"] == "c2")
            .expect("c2's record");
        assert_eq!(
            [
                record["state"].clone(),
                record["unitPrice"].clone(),
                record["currency"].clone()
            ],
            [json!("released"), json!(1250), json!("GBP")]
        );
        assert_matched(&engine);
    }
}

#[test]
fn a_supply_takes_a_call_only_for_what_the_claim_shows() {
    for target in targets("a_supply_takes_a_call_only_for_what_the_claim_shows") {
        let engine = open(&target);
        create(
            &engine,
            "Shelf",
            "anvil",
            json!({"title": "Anvil", "units": 5}),
        );
        create(
            &engine,
            "Shelf",
            "vice",
            json!({"title": "Vice", "units": 5}),
        );
        create(&engine, "Cart", "c1", json!({}));
        stage(&engine, "c1", json!([line("anvil", 0, 2)]));
        let call = |holder: &str, hold: &str, item: i64, quantity: i64| json!({"holder": {"schema": "Cart", "id": holder}, "hold": hold, "item": item, "quantity": quantity});
        let refused = |shelf: &str, operation: &str, params: Value| {
            let (behavior, action, reason, code, details) =
                veto(invoke(&engine, "Shelf", shelf, operation, params));
            assert_eq!(
                (behavior.as_str(), action.as_str(), code.as_deref()),
                ("test.Supply", operation, Some("not_requested"))
            );
            (reason, details.expect("details"))
        };

        // A pending item is not held by calling the supply directly.
        let (reason, details) = refused("anvil", "holdFor", call("c1", "lines", 0, 2));
        assert_eq!(
            reason,
            "item 0 of hold lines of Cart c1 is pending, and holdFor needs it held"
        );
        assert_eq!(
            details,
            json!({"holder": {"schema": "Cart", "id": "c1"}, "hold": "lines", "item": 0, "needs": "held", "holderState": "pending"})
        );
        // Nor for a holder or an item that does not exist, another supply,
        // another quantity, or a holder that composes no Holds.
        assert_eq!(
            refused("anvil", "holdFor", call("nobody", "lines", 0, 2)).0,
            "Cart nobody does not exist"
        );
        assert_eq!(
            refused("anvil", "holdFor", call("c1", "lines", 1, 2)).0,
            "Cart c1 has no item 1 of hold lines"
        );
        assert_eq!(
            refused("vice", "holdFor", call("c1", "lines", 0, 2)).0,
            "item 0 of hold lines of Cart c1 names Shelf anvil, not this supply"
        );
        invoke(
            &engine,
            "Cart",
            "c1",
            "mark",
            json!({"hold": "lines", "item": 0, "state": "held"}),
        )
        .expect("mark");
        assert_eq!(
            refused("anvil", "holdFor", call("c1", "lines", 0, 3)).0,
            r#"item 0 of hold lines of Cart c1 asks {"quantity":2}, not {"quantity":3}"#
        );
        let (reason, _) = refused(
            "anvil",
            "holdFor",
            json!({"holder": {"schema": "Shelf", "id": "vice"}, "hold": "lines", "item": 0, "quantity": 2}),
        );
        assert_eq!(reason, "Shelf does not compose Holds, so holds nothing");
        invoke(
            &engine,
            "Cart",
            "c1",
            "mark",
            json!({"hold": "lines", "item": 0, "state": "pending"}),
        )
        .expect("mark");

        // Once held through the claim, a direct release or commit is
        // refused: the claim's item is still held.
        advance(&engine, "c1", "held").expect("hold");
        let (reason, details) = refused("anvil", "releaseFor", call("c1", "lines", 0, 2));
        assert_eq!(
            reason,
            "item 0 of hold lines of Cart c1 is held, and releaseFor needs it released"
        );
        assert_eq!(details["needs"], "released");
        assert_eq!(
            refused("anvil", "commitFor", call("c1", "lines", 0, 2)).1["needs"],
            "committed"
        );
        assert_eq!(records(&engine, "Shelf", "anvil")[0]["state"], "held");

        // The record moves only forward: a claim that marks its item
        // returned before it commits it is refused by the supply's record.
        invoke(
            &engine,
            "Cart",
            "c1",
            "mark",
            json!({"hold": "lines", "item": 0, "state": "returned"}),
        )
        .expect("mark");
        let (reason, details) = refused("anvil", "returnFor", call("c1", "lines", 0, 2));
        assert_eq!(
            reason,
            "this supply records item 0 of hold lines of Cart c1 as held, which returnFor does not move"
        );
        assert_eq!(
            details,
            json!({"holder": {"schema": "Cart", "id": "c1"}, "hold": "lines", "item": 0, "needs": "returned", "holderState": "returned", "supplyState": "held"})
        );
        invoke(
            &engine,
            "Cart",
            "c1",
            "mark",
            json!({"hold": "lines", "item": 0, "state": "held"}),
        )
        .expect("mark");
        assert_matched(&engine);

        // A call made again changes nothing and appends no event.
        let seq = events(&engine, "Shelf", "anvil");
        assert_eq!(
            invoke(
                &engine,
                "Shelf",
                "anvil",
                "holdFor",
                call("c1", "lines", 0, 2)
            )
            .expect("again"),
            json!({"state": "held", "changed": false})
        );
        assert_eq!(events(&engine, "Shelf", "anvil"), seq);
        advance(&engine, "c1", "released").expect("release");
        let seq = events(&engine, "Shelf", "anvil");
        assert_eq!(
            invoke(
                &engine,
                "Shelf",
                "anvil",
                "releaseFor",
                call("c1", "lines", 0, 2)
            )
            .expect("again"),
            json!({"state": "released", "changed": false})
        );
        assert_eq!(events(&engine, "Shelf", "anvil"), seq);
        assert_matched(&engine);

        // An item let go before it was held leaves the supply without a
        // record.
        create(&engine, "Cart", "c2", json!({}));
        stage(&engine, "c2", json!([line("vice", 0, 1)]));
        advance(&engine, "c2", "released").expect("release");
        assert_eq!(states(&engine, "c2"), ["released"]);
        assert!(records(&engine, "Shelf", "vice").is_empty());
        assert_matched(&engine);
    }
}

#[test]
fn a_claim_holds_all_of_its_items_or_none() {
    for target in targets("a_claim_holds_all_of_its_items_or_none") {
        let engine = open(&target);
        create(
            &engine,
            "Shelf",
            "anvil",
            json!({"title": "Anvil", "units": 5}),
        );
        create(
            &engine,
            "Shelf",
            "vice",
            json!({"title": "Vice", "units": 1}),
        );
        create(&engine, "Cart", "c1", json!({}));
        stage(
            &engine,
            "c1",
            json!([line("anvil", 0, 1), line("vice", 1, 2), line("anvil", 2, 1)]),
        );
        let (behavior, _, _, code, details) = veto(advance(&engine, "c1", "held"));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("test.Supply", Some("unavailable"))
        );
        assert_eq!(
            details.expect("details")["supply"],
            json!({"schema": "Shelf", "id": "vice"})
        );
        assert_eq!(states(&engine, "c1"), ["pending", "pending", "pending"]);
        assert!(records(&engine, "Shelf", "anvil").is_empty());
        assert_matched(&engine);

        // check lists only the item that does not fit.
        let details = veto(invoke(&engine, "Cart", "c1", "check", json!({})))
            .4
            .expect("details");
        let listed: Vec<Value> = details["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|entry| json!([entry["hold"], entry["item"], entry["available"]]))
            .collect();
        assert_eq!(listed, [json!(["lines", 1, 1])]);

        // A supply holding something is not deleted.
        stage(&engine, "c1", json!([line("anvil", 0, 1)]));
        advance(&engine, "c1", "held").expect("hold");
        let (behavior, action, _, code, _) = veto(engine.instances().delete(
            &alice(),
            "Shelf",
            "anvil",
            WriteOptions::default(),
        ));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("test.Supply", "delete", Some("held"))
        );
        assert!(engine
            .instances()
            .delete(&alice(), "Shelf", "vice", WriteOptions::default())
            .expect("delete"));
        advance(&engine, "c1", "released").expect("release");
        assert!(engine
            .instances()
            .delete(&alice(), "Shelf", "anvil", WriteOptions::default())
            .expect("delete"));
    }
}

#[test]
fn a_claim_holds_timed_supply_by_its_interval() {
    for target in targets("a_claim_holds_timed_supply") {
        let engine = open(&target);
        create(
            &engine,
            "Room",
            "front",
            json!({"title": "Front counter", "units": 1}),
        );
        for (cart, start, end) in [("c1", 1000, 2000), ("c2", 1500, 2500), ("c3", 2000, 3000)] {
            create(&engine, "Cart", cart, json!({}));
            stage(&engine, cart, json!([slot("front", start, end)]));
        }
        advance(&engine, "c1", "held").expect("hold");
        assert_eq!(
            items(&engine, "c1")[0].to_json(),
            json!({"hold": "pickup", "item": 0, "schema": "Room", "id": "front", "startMs": 1000, "endMs": 2000, "state": "held"})
        );
        assert_eq!(
            records(&engine, "Room", "front")[0]["interval"],
            json!({"startMs": 1000, "endMs": 2000})
        );

        // An overlapping interval does not fit, and names the one it meets.
        let details = veto(invoke(&engine, "Cart", "c2", "check", json!({})))
            .4
            .expect("details");
        assert_eq!(
            details["items"][0],
            json!({
                "code": "unavailable",
                "reason": "Room front holds 1 at once, and 1 of them overlap the time asked",
                "supply": {"schema": "Room", "id": "front"},
                "hold": "pickup", "item": 0,
                "asked": {"interval": {"startMs": 1500, "endMs": 2500}},
                "conflict": {"startMs": 1000, "endMs": 2000}
            })
        );
        assert_eq!(
            veto(advance(&engine, "c2", "held")).3.as_deref(),
            Some("unavailable")
        );
        // One that starts as the held one ends does.
        advance(&engine, "c3", "held").expect("hold");
        assert_matched(&engine);

        // Timed supply is committed and released; it is never returned.
        advance(&engine, "c1", "committed").expect("commit");
        assert_eq!(
            advance(&engine, "c1", "returned").expect("return"),
            json!({"moved": 0})
        );
        advance(&engine, "c3", "released").expect("release");
        advance(&engine, "c2", "held").expect_err("still overlaps c1's committed time");
        assert_eq!(states(&engine, "c1"), ["committed"]);
        assert_matched(&engine);

        // The interval is checked as the contract reads it.
        let refused = invoke(
            &engine,
            "Room",
            "front",
            "checkHold",
            json!({"holder": {"schema": "Cart", "id": "c2"}, "hold": "pickup", "item": 0, "interval": {"startMs": 5, "endMs": 5}}),
        )
        .expect_err("an empty interval");
        assert!(
            refused
                .to_string()
                .contains("/interval/endMs: must be after startMs"),
            "{refused}"
        );
    }
}
