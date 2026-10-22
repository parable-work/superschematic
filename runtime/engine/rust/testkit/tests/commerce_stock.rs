//! Stock, units a supply counts (D70), the `core-stock` suite: its counts
//! and movements, which a caller with the config's permission receives,
//! adjusts and counts, paged newest first; what a claim holds, commits,
//! releases and returns through the supply contract, with `unavailable`,
//! `over_max`, backorder and the unit price; `below_held` and its
//! override; `Stock.low` as a field and a list filter; the delete refused
//! while it holds; and its config and rule for a new version. A stand-in
//! for `Holds` (`commerce_fixtures::HoldsStandIn`) plays the claim, on
//! SQLite and, with the variable set, on Postgres. After every step the
//! ledger holds (`onHand` is the sum of the movements, `held` of the
//! records that hold) and every held or committed item matches its record
//! (`commerce_support`).

mod commerce_support;
mod support;

use std::sync::Arc;

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, InstanceRecord,
    ListOptions, PermissionMatcher, Principal, SchemaTarget, WriteOptions,
};
use superschematic_engine_commerce::Stock;
use superschematic_engine_testkit::commerce_fixtures::HoldsStandIn;

use commerce_support::{assert_ledger, assert_matched, ids, items_of, principal, read, Supply};
use support::{alice, code, refusal, schema_document, targets, veto, Target};

fn stocker() -> Principal {
    principal("sam", &["stock.adjust"])
}

fn auditor() -> Principal {
    principal("ada", &["stock.adjust", "stock.adjust.override"])
}

fn int(name: &str) -> Value {
    json!({"name": name, "typeRef": {"name": "Generic.Int64"}})
}

fn inventory(config: Value) -> Value {
    let mut document = schema_document(
        "Inventory",
        json!([
            {"name": "title", "typeRef": {"name": "string"}},
            int("priceCents"),
            {"name": "currency", "typeRef": {"name": "string"}},
            int("reorderAt")
        ]),
        json!({}),
    );
    document["types"]["Inventory"]["behaviors"] = json!([{"name": "Stock", "config": config}]);
    document
}

fn config() -> Value {
    json!({
        "permission": "stock.adjust",
        "price": {"field": "priceCents", "currency": "GBP"},
        "lowAt": {"field": "reorderAt"},
        "maxPerHold": 5
    })
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

/// A permission matcher where a permission covers itself alone, so the
/// `.override` of a permission is a permission of its own; the default,
/// dotted one grants every permission nested under one held.
struct Exact;

impl PermissionMatcher for Exact {
    fn matches(&self, held: &[String], required: &[String]) -> bool {
        required.is_empty() || required.iter().any(|need| held.contains(need))
    }
}

fn open_with(target: &Target, stock: Value) -> Engine {
    open_matching(target, stock, None)
}

fn open_matching(
    target: &Target,
    stock: Value,
    matcher: Option<Arc<dyn PermissionMatcher>>,
) -> Engine {
    let engine = target.open_with(
        BehaviorSet::new().with(Stock).with(HoldsStandIn),
        |options| {
            options.permission_matcher = matcher;
        },
    );
    publish(&engine, &inventory(stock)).expect("publish Inventory");
    let mut cart = schema_document(
        "Cart",
        json!([{"name": "title", "typeRef": {"name": "string"}}]),
        json!({}),
    );
    cart["types"]["Cart"]["behaviors"] = json!([{"name": "Holds"}]);
    publish(&engine, &cart).expect("publish Cart");
    engine
}

fn open(target: &Target) -> Engine {
    open_with(target, config())
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

fn stock(engine: &Engine, id: &str) -> Value {
    read(engine, "Inventory", id)
        .expect("the inventory")
        .behaviors["Stock"]
        .clone()
}

fn line(id: &str, item: i64, quantity: i64) -> Value {
    json!({"hold": "lines", "item": item, "schema": "Inventory", "id": id, "quantity": quantity})
}

fn cart(engine: &Engine, id: &str, lines: Value) {
    create(engine, "Cart", id, json!({}));
    invoke(
        engine,
        &alice(),
        "Cart",
        id,
        "stage",
        json!({ "items": lines }),
    )
    .expect("stage");
}

fn advance(engine: &Engine, id: &str, to: &str) -> Result<Value, Error> {
    invoke(engine, &alice(), "Cart", id, "move", json!({ "to": to }))
}

fn states(engine: &Engine, id: &str) -> Vec<&'static str> {
    items_of(engine, "Cart", id)
        .iter()
        .map(|item| item.state.as_str())
        .collect()
}

// holds checks the invariants after a step.
fn holds(engine: &Engine, backorder: bool) {
    assert_ledger(engine, "Inventory", backorder);
    assert_matched(engine, &["Cart"], &[Supply::Stock("Inventory")]);
}

fn movements(engine: &Engine, id: &str, params: Value) -> Value {
    invoke(
        engine,
        &alice(),
        "Inventory",
        id,
        "listStockMovements",
        params,
    )
    .expect("listStockMovements")
}

#[test]
fn stock_is_received_adjusted_and_counted_with_its_permission_each_a_movement() {
    for target in targets("stock_movements") {
        let engine = open(&target);
        let created = create(
            &engine,
            "Inventory",
            "anvil",
            json!({"title": "Anvil", "priceCents": 1250, "reorderAt": 2}),
        );
        assert_eq!(
            created.behaviors["Stock"],
            json!({"onHand": 0, "held": 0, "available": 0, "low": true})
        );
        holds(&engine, false);

        // Receiving needs the permission.
        let forbidden = invoke(
            &engine,
            &alice(),
            "Inventory",
            "anvil",
            "receiveStock",
            json!({"quantity": 10, "reason": "delivery"}),
        );
        assert_eq!(code(forbidden), EngineErrorCode::Forbidden);
        assert_eq!(
            invoke(
                &engine,
                &stocker(),
                "Inventory",
                "anvil",
                "receiveStock",
                json!({"quantity": 10, "reason": "delivery", "reference": "DN-1"}),
            )
            .expect("receive"),
            json!({"onHand": 10, "held": 0, "available": 10, "movement": 1})
        );
        assert_eq!(stock(&engine, "anvil")["low"], json!(false));
        holds(&engine, false);

        // An adjustment moves what is on hand by its delta; 0 changes and
        // records nothing.
        assert_eq!(
            invoke(
                &engine,
                &stocker(),
                "Inventory",
                "anvil",
                "adjustStock",
                json!({"delta": -3, "reason": "damaged"}),
            )
            .expect("adjust"),
            json!({"onHand": 7, "held": 0, "available": 7, "movement": 2})
        );
        let seq = read(&engine, "Inventory", "anvil").expect("anvil").seq;
        assert_eq!(
            invoke(
                &engine,
                &stocker(),
                "Inventory",
                "anvil",
                "adjustStock",
                json!({"delta": 0, "reason": "nothing"}),
            )
            .expect("adjust"),
            json!({"onHand": 7, "held": 0, "available": 7, "movement": null})
        );
        // A count of what is on hand records nothing either.
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "anvil",
            "countStock",
            json!({"onHand": 7, "reason": "stock take"}),
        )
        .expect("count");
        assert_eq!(read(&engine, "Inventory", "anvil").expect("anvil").seq, seq);
        assert_eq!(
            invoke(
                &engine,
                &stocker(),
                "Inventory",
                "anvil",
                "countStock",
                json!({"onHand": 5, "reason": "stock take"}),
            )
            .expect("count"),
            json!({"onHand": 5, "held": 0, "available": 5, "movement": 3})
        );
        // Below 0 is refused, backorder or not.
        let (behavior, action, reason, code, details) = veto(invoke(
            &engine,
            &stocker(),
            "Inventory",
            "anvil",
            "adjustStock",
            json!({"delta": -6, "reason": "lost"}),
        ));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("Stock", "adjustStock", Some("below_held"))
        );
        assert_eq!(reason, "an adjustment of -6 leaves -1 on hand, below 0");
        assert_eq!(details, Some(json!({"onHand": -1, "held": 0})));
        holds(&engine, false);

        // The movements page newest first.
        let first = movements(&engine, "anvil", json!({"limit": 2}));
        let kinds: Vec<Value> = first["items"]
            .as_array()
            .expect("items")
            .iter()
            .map(|item| json!([item["seq"], item["kind"], item["delta"], item["onHand"]]))
            .collect();
        assert_eq!(
            kinds,
            [json!([3, "count", -2, 5]), json!([2, "adjust", -3, 7])]
        );
        assert_eq!(first["items"][0]["reason"], "stock take");
        assert_eq!(first["items"][0]["actor"], "sam");
        let rest = movements(
            &engine,
            "anvil",
            json!({"limit": 2, "cursor": first["next"]}),
        );
        assert_eq!(rest["next"], Value::Null);
        let item = &rest["items"][0];
        assert_eq!(
            [
                &item["seq"],
                &item["kind"],
                &item["delta"],
                &item["reference"]
            ],
            [&json!(1), &json!("receive"), &json!(10), &json!("DN-1")]
        );
        let refused = refusal(invoke(
            &engine,
            &alice(),
            "Inventory",
            "anvil",
            "listStockMovements",
            json!({"cursor": "nonsense"}),
        ));
        assert_eq!(refused.code(), EngineErrorCode::InvalidArgument);
    }
}

#[test]
fn a_claim_holds_commits_releases_and_returns_units_through_the_contract() {
    for target in targets("stock_contract") {
        let engine = open_matching(&target, config(), Some(Arc::new(Exact)));
        create(
            &engine,
            "Inventory",
            "anvil",
            json!({"title": "Anvil", "priceCents": 1250, "reorderAt": 1}),
        );
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "anvil",
            "receiveStock",
            json!({"quantity": 3, "reason": "delivery"}),
        )
        .expect("receive");
        cart(&engine, "c1", json!([line("anvil", 0, 2)]));
        cart(&engine, "c2", json!([line("anvil", 0, 2)]));
        holds(&engine, false);

        // c1 holds 2 of 3, at the price on the record.
        advance(&engine, "c1", "held").expect("hold");
        assert_eq!(
            items_of(&engine, "Cart", "c1")[0].to_json(),
            json!({"hold": "lines", "item": 0, "schema": "Inventory", "id": "anvil", "quantity": 2, "state": "held", "unitPrice": 1250, "currency": "GBP"})
        );
        assert_eq!(
            stock(&engine, "anvil"),
            json!({"onHand": 3, "held": 2, "available": 1, "low": true})
        );
        holds(&engine, false);

        // c2's 2 do not fit the 1 available.
        let check = veto(invoke(&engine, &alice(), "Cart", "c2", "check", json!({})));
        assert_eq!(
            check.4.expect("details"),
            json!({"items": [{
                "code": "unavailable",
                "reason": "2 asked of Inventory anvil, which has 1 available",
                "supply": {"schema": "Inventory", "id": "anvil"},
                "hold": "lines", "item": 0, "asked": {"quantity": 2}, "available": 1
            }]})
        );
        let (behavior, action, _, code, _) = veto(advance(&engine, "c2", "held"));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("Stock", "holdFor", Some("unavailable"))
        );
        assert_eq!(states(&engine, "c2"), ["pending"]);
        holds(&engine, false);

        // A count below what is held needs the override; an adjustment
        // below it is refused.
        let (_, _, reason, code, details) = veto(invoke(
            &engine,
            &stocker(),
            "Inventory",
            "anvil",
            "countStock",
            json!({"onHand": 1, "reason": "stock take"}),
        ));
        assert_eq!(code.as_deref(), Some("below_held"));
        assert_eq!(
            reason,
            "a count of 1 is below the 2 claims hold; it needs permission stock.adjust.override"
        );
        assert_eq!(details, Some(json!({"onHand": 1, "held": 2})));
        assert_eq!(
            veto(invoke(
                &engine,
                &stocker(),
                "Inventory",
                "anvil",
                "adjustStock",
                json!({"delta": -2, "reason": "lost"}),
            ))
            .3
            .as_deref(),
            Some("below_held")
        );
        assert_eq!(
            invoke(
                &engine,
                &auditor(),
                "Inventory",
                "anvil",
                "countStock",
                json!({"onHand": 1, "reason": "stock take"}),
            )
            .expect("count with the override"),
            json!({"onHand": 1, "held": 2, "available": 0, "movement": 2})
        );
        holds(&engine, false);
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "anvil",
            "receiveStock",
            json!({"quantity": 4, "reason": "delivery"}),
        )
        .expect("receive");

        // A commit takes the units out of what is on hand and what is held,
        // a commit movement for the claim; a return puts them back.
        advance(&engine, "c1", "committed").expect("commit");
        assert_eq!(
            stock(&engine, "anvil"),
            json!({"onHand": 3, "held": 0, "available": 3, "low": false})
        );
        let last = &movements(&engine, "anvil", json!({"limit": 1}))["items"][0];
        assert_eq!(
            [
                &last["kind"],
                &last["delta"],
                &last["holder"],
                &last["hold"],
                &last["item"]
            ],
            [
                &json!("commit"),
                &json!(-2),
                &json!({"schema": "Cart", "id": "c1"}),
                &json!("lines"),
                &json!(0)
            ]
        );
        assert!(last.get("reason").is_none());
        holds(&engine, false);
        advance(&engine, "c1", "returned").expect("return");
        assert_eq!(stock(&engine, "anvil")["onHand"], json!(5));
        holds(&engine, false);

        // A held item released frees its units; a call made again changes
        // nothing.
        advance(&engine, "c2", "held").expect("hold");
        assert_eq!(stock(&engine, "anvil")["held"], json!(2));
        advance(&engine, "c2", "released").expect("release");
        assert_eq!(stock(&engine, "anvil")["held"], json!(0));
        let seq = read(&engine, "Inventory", "anvil").expect("anvil").seq;
        assert_eq!(
            invoke(
                &engine,
                &alice(),
                "Inventory",
                "anvil",
                "releaseFor",
                json!({"holder": {"schema": "Cart", "id": "c2"}, "hold": "lines", "item": 0, "quantity": 2}),
            )
            .expect("again"),
            json!({"state": "released", "changed": false, "unitPrice": 1250, "currency": "GBP"})
        );
        assert_eq!(read(&engine, "Inventory", "anvil").expect("anvil").seq, seq);
        holds(&engine, false);

        // A direct call for an item the claim does not show is refused.
        cart(&engine, "c3", json!([line("anvil", 0, 1)]));
        let (behavior, _, reason, code, _) = veto(invoke(
            &engine,
            &alice(),
            "Inventory",
            "anvil",
            "holdFor",
            json!({"holder": {"schema": "Cart", "id": "c3"}, "hold": "lines", "item": 0, "quantity": 1}),
        ));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Stock", Some("not_requested"))
        );
        assert_eq!(
            reason,
            "item 0 of hold lines of Cart c3 is pending, and holdFor needs it held"
        );
        holds(&engine, false);
    }
}

#[test]
fn a_hold_is_held_to_max_per_hold_and_to_what_is_available_unless_backorder() {
    for target in targets("stock_limits") {
        let engine = open(&target);
        create(
            &engine,
            "Inventory",
            "rope",
            json!({"title": "Rope", "priceCents": 300}),
        );
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "rope",
            "receiveStock",
            json!({"quantity": 10, "reason": "delivery"}),
        )
        .expect("receive");
        cart(&engine, "c1", json!([line("rope", 0, 6)]));
        let answer = invoke(
            &engine,
            &alice(),
            "Inventory",
            "rope",
            "checkHold",
            json!({"holder": {"schema": "Cart", "id": "c1"}, "hold": "lines", "item": 0, "quantity": 6}),
        )
        .expect("checkHold");
        assert_eq!(
            answer,
            json!({
                "fits": false,
                "code": "over_max",
                "reason": "6 asked of Inventory rope, and one item holds at most 5",
                "details": {"supply": {"schema": "Inventory", "id": "rope"}, "hold": "lines", "item": 0, "asked": {"quantity": 6}, "max": 5}
            })
        );
        assert_eq!(
            veto(advance(&engine, "c1", "held")).3.as_deref(),
            Some("over_max")
        );
        holds(&engine, false);
    }
    for target in targets("stock_backorder") {
        let engine = open_with(
            &target,
            json!({"permission": "stock.adjust", "backorder": true}),
        );
        create(&engine, "Inventory", "rope", json!({"title": "Rope"}));
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "rope",
            "receiveStock",
            json!({"quantity": 1, "reason": "delivery"}),
        )
        .expect("receive");
        cart(&engine, "c1", json!([line("rope", 0, 3)]));
        advance(&engine, "c1", "held").expect("a backorder holds past what is on hand");
        assert_eq!(
            stock(&engine, "rope"),
            json!({"onHand": 1, "held": 3, "available": -2, "low": false})
        );
        // No price without the config's.
        assert!(items_of(&engine, "Cart", "c1")[0].price.is_none());
        // An adjustment below what is held is taken with backorder.
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "rope",
            "adjustStock",
            json!({"delta": -1, "reason": "lost"}),
        )
        .expect("adjust");
        holds(&engine, true);

        // backorder turns off only while no instance holds past what it has.
        let refused = publish(&engine, &inventory(json!({"permission": "stock.adjust"})))
            .expect_err("backorder off while rope is below zero");
        assert!(
            refused
                .to_string()
                .contains("backorder cannot turn off while Inventory rope holds 3 units for claims with 0 on hand"),
            "{refused}"
        );
        advance(&engine, "c1", "released").expect("release");
        publish(&engine, &inventory(json!({"permission": "stock.adjust"}))).expect("backorder off");
        holds(&engine, false);
    }
}

#[test]
fn stock_low_reads_the_config_now_and_a_list_filters_on_it() {
    for target in targets("stock_low") {
        let engine = open_with(&target, json!({"permission": "stock.adjust", "lowAt": 2}));
        for (id, quantity) in [("anvil", 1), ("rope", 5), ("vice", 2)] {
            create(&engine, "Inventory", id, json!({"title": id}));
            invoke(
                &engine,
                &stocker(),
                "Inventory",
                id,
                "receiveStock",
                json!({"quantity": quantity, "reason": "delivery"}),
            )
            .expect("receive");
        }
        let low = |engine: &Engine| {
            let filter = json!({"Stock.low": true});
            engine
                .instances()
                .list(
                    &alice(),
                    "Inventory",
                    ListOptions {
                        filter: Some(&filter),
                        ..Default::default()
                    },
                )
                .expect("list")
                .items
                .into_iter()
                .map(|record| record.id)
                .collect::<Vec<_>>()
        };
        assert_eq!(low(&engine), ["anvil", "vice"]);
        // A hold moves available, and so low.
        cart(&engine, "c1", json!([line("rope", 0, 3)]));
        advance(&engine, "c1", "held").expect("hold");
        assert_eq!(low(&engine), ["anvil", "rope", "vice"]);
        holds(&engine, false);

        // A new lowAt moves the field at once; the filter follows each
        // instance's next write.
        publish(
            &engine,
            &inventory(json!({"permission": "stock.adjust", "lowAt": 1})),
        )
        .expect("lowAt 1");
        assert_eq!(stock(&engine, "vice")["low"], json!(false));
        assert_eq!(low(&engine), ["anvil", "rope", "vice"]);
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "vice",
            "receiveStock",
            json!({"quantity": 1, "reason": "delivery"}),
        )
        .expect("receive");
        assert_eq!(low(&engine), ["anvil", "rope"]);

        // lowAt from an own field: an update moves the filter too.
        publish(
            &engine,
            &inventory(json!({"permission": "stock.adjust", "lowAt": {"field": "reorderAt"}})),
        )
        .expect("lowAt field");
        engine
            .instances()
            .update(
                &alice(),
                "Inventory",
                "vice",
                &json!({"reorderAt": 3}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(stock(&engine, "vice")["low"], json!(true));
        assert!(low(&engine).contains(&"vice".to_owned()));
        holds(&engine, false);
    }
}

#[test]
fn a_supply_holding_units_is_not_deleted_and_its_rows_go_with_it() {
    for target in targets("stock_delete") {
        let engine = open(&target);
        for id in ["anvil", "vice"] {
            create(&engine, "Inventory", id, json!({"title": id}));
            invoke(
                &engine,
                &stocker(),
                "Inventory",
                id,
                "receiveStock",
                json!({"quantity": 2, "reason": "delivery"}),
            )
            .expect("receive");
        }
        cart(
            &engine,
            "c1",
            json!([line("anvil", 0, 1), line("vice", 1, 1)]),
        );
        advance(&engine, "c1", "held").expect("hold");
        let (behavior, action, _, code, details) = veto(engine.instances().delete(
            &alice(),
            "Inventory",
            "anvil",
            WriteOptions::default(),
        ));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("Stock", "delete", Some("held"))
        );
        assert_eq!(details, Some(json!({"held": 1})));
        holds(&engine, false);

        // Committed units are sold: the supply goes, and its rows.
        invoke(
            &engine,
            &alice(),
            "Cart",
            "c1",
            "move",
            json!({"to": "committed", "hold": "lines", "item": 0}),
        )
        .expect("commit");
        invoke(
            &engine,
            &alice(),
            "Cart",
            "c1",
            "move",
            json!({"to": "released", "hold": "lines", "item": 1}),
        )
        .expect("release");
        assert!(engine
            .instances()
            .delete(&alice(), "Inventory", "anvil", WriteOptions::default())
            .expect("delete"));
        assert_eq!(ids(&engine, "Inventory"), ["vice"]);
        let movements = commerce_support::behavior_table(&engine, "Stock", "movements");
        let left = commerce_support::rows(&engine, &format!("SELECT id FROM {movements}"), &[]);
        assert!(left.iter().all(|row| row["id"] == "vice"), "{left:?}");
        assert_ledger(&engine, "Inventory", false);
    }
}

#[test]
fn a_config_is_held_to_the_type_and_a_currency_to_iso_4217() {
    for target in targets("stock_config") {
        let engine = target.open(BehaviorSet::new().with(Stock));
        let refused = |config: Value| {
            publish(&engine, &inventory(config))
                .expect_err("refused")
                .to_string()
        };
        assert!(refused(
            json!({"permission": "p", "price": {"field": "title", "currency": "GBP"}})
        )
        .contains("price.field \"title\" is not an integer field of Inventory"));
        assert!(
            refused(json!({"permission": "p", "price": {"field": "cost", "currency": "GBP"}}))
                .contains("price.field \"cost\" is not a field of Inventory")
        );
        assert!(refused(json!({"permission": "p", "price": {"field": "priceCents", "currency": {"field": "reorderAt"}}}))
            .contains("price.currency.field \"reorderAt\" is not a string field of Inventory"));
        assert!(
            refused(json!({"permission": "p", "lowAt": {"field": "title"}}))
                .contains("lowAt.field \"title\" is not an integer field of Inventory")
        );
        if superschematic_engine_commerce::currency::CHECKS_TABLE {
            assert!(refused(
                json!({"permission": "p", "price": {"field": "priceCents", "currency": "ABC"}})
            )
            .contains("price.currency \"ABC\" is not an active ISO 4217 currency code"));
        }
        publish(
            &engine,
            &inventory(json!({"permission": "p", "price": {"field": "priceCents", "currency": {"field": "currency"}}})),
        )
        .expect("a currency field");
        create(&engine, "Inventory", "anvil", json!({"title": "Anvil"}));
        // Stock leaves only a schema without instances.
        let mut bare = inventory(json!({}));
        bare["types"]["Inventory"]["behaviors"] = json!([]);
        let refused = publish(&engine, &bare).expect_err("instances hold stock");
        assert!(
            refused
                .to_string()
                .contains("its instances' stock and movements would be lost"),
            "{refused}"
        );
        assert_eq!(ids(&engine, "Inventory"), ["anvil"]);
    }
}

#[test]
fn the_default_matcher_grants_the_override_with_the_permission() {
    for target in targets("stock_dotted_override") {
        let engine = open(&target);
        create(&engine, "Inventory", "anvil", json!({"title": "Anvil"}));
        invoke(
            &engine,
            &stocker(),
            "Inventory",
            "anvil",
            "receiveStock",
            json!({"quantity": 2, "reason": "delivery"}),
        )
        .expect("receive");
        cart(&engine, "c1", json!([line("anvil", 0, 2)]));
        advance(&engine, "c1", "held").expect("hold");
        // stock.adjust covers stock.adjust.override under dotted
        // permissions, so its holder counts below what is held.
        assert_eq!(
            invoke(
                &engine,
                &stocker(),
                "Inventory",
                "anvil",
                "countStock",
                json!({"onHand": 1, "reason": "stock take"}),
            )
            .expect("count"),
            json!({"onHand": 1, "held": 2, "available": 0, "movement": 2})
        );
        holds(&engine, false);
    }
}
