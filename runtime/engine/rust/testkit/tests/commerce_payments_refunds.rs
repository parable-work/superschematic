//! Payments' refund instances (D70): a Refund with Workflow, Approvals'
//! two sign-offs, a required link to the checkout it refunds, Numbering,
//! Reactions and Payments with `refundOf` and `issueOn`. Entering
//! `approved`, once two holders of refunds.approve other than its creator
//! have signed, asks the checkout's `refundFor`, which records the refund
//! and the action the adapter runs; the provider's refund event, reported
//! to the checkout, comes back through `settleRefundFor`, the Refund's
//! `Payments.state` turns `refunded`, and a Reactions rule moves it to
//! `issued`. A refund the checkout cannot pay is refused at the move; a
//! failed one turns the Refund `failed`, and approving it again asks
//! again. Nobody asks or settles a refund by calling the pair directly. On
//! SQLite and, with the variable set, on Postgres.

mod payments_support;
mod support;

use serde_json::{json, Value};

use payments_support::*;
use superschematic_engine::{BehaviorSet, DefineOptions, Engine, EngineErrorCode, Principal};
use support::{alice, code, publish, refusal, schema_document, targets, veto};

const APPROVE: &str = "refunds.approve";

fn bob() -> Principal {
    principal("bob", &[APPROVE])
}

fn carol() -> Principal {
    principal("carol", &[APPROVE])
}

fn vetoed<T: std::fmt::Debug>(result: Result<T, superschematic_engine::Error>) -> Option<String> {
    veto(result).3
}

fn refund_payments(more: Value) -> Value {
    let mut config = json!({
        "refundOf": {"link": "order"},
        "amount": {"field": "amountCents"},
        "currency": "GBP",
        "issueOn": ["approved"],
        "record": {"permission": RECORD}
    });
    if let (Some(config), Value::Object(more)) = (config.as_object_mut(), more) {
        config.extend(more);
    }
    config
}

fn refund_document(payments: Value) -> Value {
    let mut document = schema_document(
        "Refund",
        json!([
            {"name": "amountCents", "typeRef": {"name": "Generic.Int64"}, "required": true},
            {"name": "reason", "typeRef": {"name": "string"}, "required": true}
        ]),
        json!({}),
    );
    document["types"]["Refund"]["behaviors"] = json!([
        {"name": "Workflow", "config": {
            "states": ["requested", "approved", "issued", "rejected", "failed"],
            "initial": "requested",
            "transitions": [
                {"from": "requested", "to": "approved"},
                {"from": "requested", "to": "rejected"},
                {"from": "approved", "to": "issued"},
                {"from": "approved", "to": "failed"},
                {"from": "failed", "to": "approved"}
            ],
            "outcomes": {"rejected": "neutral"}
        }},
        {"name": "Links", "config": {"links": {"order": {"schema": "Checkout", "required": true}}}},
        {"name": "Approvals", "config": {"gates": {"approved": {"count": 2, "permission": APPROVE}}}},
        {"name": "Numbering", "config": {"format": "RF-{yyyy}-{seq:5}", "reset": "yearly"}},
        {"name": "Payments", "config": payments},
        {"name": "Reactions", "config": {"rules": [
            {"when": {"changes": {"field": "Payments.state", "to": "refunded"}}, "then": {"transition": "issued"}},
            {"when": {"changes": {"field": "Payments.state", "from": "pending", "to": "unpaid"}}, "then": {"transition": "failed"}}
        ]}}
    ]);
    document
}

// shop opens an engine with a paid checkout c-1 of 3000 and the Refund
// schema.
fn shop(target: &support::Target) -> (Engine, Value) {
    let engine = open(target, BehaviorSet::new());
    publish_checkout(&engine, payments_config(json!({"refunds": null})));
    publish(&engine, &refund_document(refund_payments(json!({}))), None);
    checkout(&engine, "c-1", 3000);
    let attempt = open_payment(&engine, "c-1", "k").unwrap()["attempt"].clone();
    record(
        &engine,
        "Checkout",
        "c-1",
        event("e-paid", "captured", 3000, json!({"attempt": attempt})),
    )
    .unwrap();
    (engine, attempt)
}

fn ask(engine: &Engine, id: &str, amount: i64) {
    create(
        engine,
        "Refund",
        id,
        json!({"amountCents": amount, "reason": "damaged"}),
        Some(json!({"Links": {"order": "c-1"}})),
    )
    .expect("create the refund");
}

fn sign(engine: &Engine, id: &str, who: &Principal) {
    invoke(
        engine,
        who,
        "Refund",
        id,
        "signOff",
        json!({"state": "approved"}),
    )
    .expect("signOff");
}

fn refund_actions(engine: &Engine) -> Vec<Value> {
    actions(engine, "Checkout")
        .into_iter()
        .filter(|action| action["kind"] == json!("refund"))
        .collect()
}

// label is a Refund's number, as Numbering prints it.
fn label(engine: &Engine, id: &str) -> String {
    read(engine, "Refund", id)
        .behavior_field("Numbering", "label")
        .and_then(Value::as_str)
        .expect("a number")
        .to_owned()
}

fn run_due(engine: &Engine) {
    engine.runner().run_due().expect("run due");
}

// refuses_forgeries_once_asked: a settlement without the adapter's
// permission, about a refund rf-1 did not ask, or from another instance, is
// refused, and rf-1's link and amount stay once it has asked.
fn refuses_forgeries_once_asked(engine: &Engine, refund_id: &Value) {
    // A forged settlement: without the adapter's permission, about a
    // refund it did not ask, or from another instance.
    let settle = |who: &Principal, of: &str, refund: &Value| {
        invoke(
            engine,
            who,
            "Refund",
            "rf-1",
            "settleRefundFor",
            json!({"of": {"schema": "Checkout", "id": of}, "refund": refund, "status": "succeeded", "amount": 1000}),
        )
    };
    assert_eq!(
        code(settle(&alice(), "c-1", refund_id)),
        EngineErrorCode::Forbidden
    );
    assert_eq!(
        vetoed(settle(&adapter(), "c-1", &json!("rf_nope"))).as_deref(),
        Some("no_attempt")
    );
    assert_eq!(
        vetoed(settle(&adapter(), "c-9", refund_id)).as_deref(),
        Some("not_linked")
    );
    // The Refund's link and amount stay once it has asked.
    let moved = invoke(
        engine,
        &alice(),
        "Refund",
        "rf-1",
        "link",
        json!({"name": "order", "id": "c-1"}),
    );
    assert_eq!(vetoed(moved).as_deref(), Some("not_linked"));
    let changed = update(engine, "Refund", "rf-1", json!({"amountCents": 2000}));
    assert_eq!(vetoed(changed).as_deref(), Some("amount_locked"));
}

#[test]
fn a_refund_is_issued_after_two_sign_offs_and_settled_by_the_provider() {
    for target in targets("a_refund_is_issued_after_two_sign_offs") {
        let (engine, attempt) = shop(&target);
        ask(&engine, "rf-1", 1000);
        let year = &superschematic_engine_testkit::iso_of(T0)[..4];
        assert_eq!(label(&engine, "rf-1"), format!("RF-{year}-00001"));
        let fields = payments(&engine, "Refund", "rf-1");
        assert_eq!(fields["state"], json!("unpaid"));
        assert_eq!(fields["due"], json!(1000));

        // Nobody asks a refund by calling refundFor directly: the Refund is
        // not approved yet.
        let forged = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-1",
            "refundFor",
            json!({"refund": {"schema": "Refund", "id": "rf-1"}, "amount": 1000, "idempotencyKey": "Refund:rf-1.0"}),
        );
        assert_eq!(vetoed(forged).as_deref(), Some("not_linked"));

        // One sign-off is not enough; the creator cannot sign.
        sign(&engine, "rf-1", &bob());
        let (behavior, _, _, vetoed_with, _) =
            veto(transition(&engine, &alice(), "Refund", "rf-1", "approved"));
        assert_eq!(
            (behavior.as_str(), vetoed_with.as_deref()),
            ("Approvals", Some("approvals_missing"))
        );
        assert_eq!(refund_actions(&engine), Vec::<Value>::new());
        sign(&engine, "rf-1", &carol());
        transition(&engine, &alice(), "Refund", "rf-1", "approved").expect("approved");

        // The checkout recorded the refund and its action.
        let waiting = refund_actions(&engine);
        assert_eq!(waiting.len(), 1);
        assert_eq!(waiting[0]["amount"], json!(1000));
        assert_eq!(waiting[0]["attempt"], attempt);
        assert_eq!(waiting[0]["id"], json!("c-1"));
        let refund_id = waiting[0]["refund"].clone();
        assert_eq!(
            payments(&engine, "Refund", "rf-1")["state"],
            json!("pending")
        );
        assert_eq!(payments(&engine, "Checkout", "c-1")["refunded"], json!(0));

        refuses_forgeries_once_asked(&engine, &refund_id);

        // The provider refunds it.
        let done = record(
            &engine,
            "Checkout",
            "c-1",
            event("e-rf", "refunded", 1000, json!({"refund": refund_id})),
        )
        .expect("refunded");
        assert_eq!(done["state"], json!("partiallyRefunded"));
        let fields = payments(&engine, "Refund", "rf-1");
        assert_eq!(fields["state"], json!("refunded"));
        assert_eq!(fields["refunded"], json!(1000));
        assert_eq!(refund_actions(&engine), Vec::<Value>::new());
        assert_ledger(&engine, "Checkout", "c-1");

        // The rule hears Payments.state turn refunded.
        assert_eq!(status(&engine, "Refund", "rf-1"), "approved");
        run_due(&engine);
        assert_eq!(status(&engine, "Refund", "rf-1"), "issued");

        // A repeat of the provider's event changes nothing anywhere.
        let seq = read(&engine, "Refund", "rf-1").seq;
        let again = record(
            &engine,
            "Checkout",
            "c-1",
            event("e-rf", "refunded", 1000, json!({"refund": refund_id})),
        )
        .unwrap();
        assert_eq!(again["applied"], json!(false));
        assert_eq!(read(&engine, "Refund", "rf-1").seq, seq);

        // A second refund is numbered next.
        ask(&engine, "rf-2", 500);
        assert_eq!(label(&engine, "rf-2"), format!("RF-{year}-00002"));
    }
}

#[test]
fn a_refund_past_what_is_refundable_is_refused_at_the_move() {
    for target in targets("a_refund_past_what_is_refundable") {
        let (engine, _) = shop(&target);
        ask(&engine, "rf-1", 2000);
        ask(&engine, "rf-2", 1500);
        for id in ["rf-1", "rf-2"] {
            sign(&engine, id, &bob());
            sign(&engine, id, &carol());
        }
        transition(&engine, &alice(), "Refund", "rf-1", "approved").expect("2000 of 3000");
        // 1000 is left of what is captured once rf-1's refund is asked.
        let (behavior, _, _, vetoed_with, details) =
            veto(transition(&engine, &alice(), "Refund", "rf-2", "approved"));
        assert_eq!(
            (behavior.as_str(), vetoed_with.as_deref()),
            ("Payments", Some("exceeds_refundable"))
        );
        assert_eq!(details.unwrap()["refundable"], json!(1000));
        assert_eq!(status(&engine, "Refund", "rf-2"), "requested");
        assert_eq!(
            payments(&engine, "Refund", "rf-2")["state"],
            json!("unpaid")
        );
        assert_eq!(refund_actions(&engine).len(), 1);
    }
}

#[test]
fn a_failed_refund_turns_the_refund_failed_and_approving_it_again_asks_again() {
    for target in targets("a_failed_refund_turns_the_refund_failed") {
        let (engine, _) = shop(&target);
        ask(&engine, "rf-1", 1200);
        sign(&engine, "rf-1", &bob());
        sign(&engine, "rf-1", &carol());
        transition(&engine, &alice(), "Refund", "rf-1", "approved").unwrap();
        let first = refund_actions(&engine)[0]["refund"].clone();
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e-f", "refundFailed", 1200, json!({"refund": first})),
        )
        .unwrap();
        // Failed: nothing is asked again while the Refund stays approved.
        assert_eq!(
            payments(&engine, "Refund", "rf-1")["state"],
            json!("unpaid")
        );
        assert_eq!(refund_actions(&engine), Vec::<Value>::new());
        run_due(&engine);
        assert_eq!(status(&engine, "Refund", "rf-1"), "failed");

        // Approved again, with two new sign-offs, it asks again.
        engine.clock().advance(1000).expect("advance");
        sign(&engine, "rf-1", &bob());
        sign(&engine, "rf-1", &carol());
        transition(&engine, &alice(), "Refund", "rf-1", "approved").expect("approved again");
        let waiting = refund_actions(&engine);
        assert_eq!(waiting.len(), 1);
        assert_ne!(waiting[0]["refund"], first);
        record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "e-ok",
                "refunded",
                1200,
                json!({"refund": waiting[0]["refund"]}),
            ),
        )
        .unwrap();
        run_due(&engine);
        assert_eq!(status(&engine, "Refund", "rf-1"), "issued");
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(fields["refunded"], json!(1200));
        assert_eq!(fields["state"], json!("partiallyRefunded"));
        assert_ledger(&engine, "Checkout", "c-1");
    }
}

#[test]
fn a_refund_config_names_a_required_link_to_what_it_refunds() {
    for target in targets("a_refund_config_names_a_required_link") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));
        let define = |document: Value| {
            engine
                .schemas()
                .define(&alice(), &document, DefineOptions::default())
        };
        let mut optional = refund_document(refund_payments(json!({})));
        optional["types"]["Refund"]["behaviors"][1]["config"]["links"]["order"]["required"] =
            json!(false);
        assert!(refusal(define(optional))
            .to_string()
            .contains("not required"));
        let refused = define(refund_document(refund_payments(
            json!({"refundOf": {"link": "nope"}}),
        )));
        assert!(refusal(refused).to_string().contains("not a link"));
        let refused = define(refund_document(refund_payments(
            json!({"refunds": {"permission": REFUND}}),
        )));
        assert!(refusal(refused).to_string().contains("takes none"));
        // A refund instance takes no payments of its own.
        publish(&engine, &refund_document(refund_payments(json!({}))), None);
        checkout(&engine, "c-1", 100);
        ask(&engine, "rf-1", 100);
        let opened = invoke(
            &engine,
            &alice(),
            "Refund",
            "rf-1",
            "openPayment",
            json!({"provider": PROVIDER, "idempotencyKey": "k"}),
        );
        assert_eq!(vetoed(opened).as_deref(), Some("not_configured"));
    }
}
