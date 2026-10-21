//! Payments (D70), the core-payments suite: an instance owes an amount in
//! a currency, `openPayment` records an attempt and the action that
//! creates it at the provider, the adapter lists the actions waiting for
//! it and reports what the provider did through `recordPaymentEvent`,
//! which applies each event once and moves an attempt only forward, so a
//! repeated or a reordered delivery changes nothing it should not; the
//! amount stays while an attempt is in flight (`amount_locked`); `require`
//! holds a move until the instance is paid, `voidOn` states void what is
//! open and no move leaves a state while a payment is processing; manual
//! capture captures in `captureOn` states and never past what is
//! authorized; refunds draw on what is captured; an amount in another
//! currency is refused; and a claim's `Holds.total` is what it owes. On
//! SQLite and, with the variable set, on Postgres, with the ledger's
//! invariants checked after every step.

mod payments_support;
mod support;

use serde_json::{json, Value};

use payments_support::*;
use superschematic_engine::{BehaviorSet, DefineOptions, EngineErrorCode};
use superschematic_engine_commerce::currency;
use superschematic_engine_testkit::payments_fixtures::HoldsTotal;
use support::{alice, code, refusal, targets, veto};

fn code_of<T: std::fmt::Debug>(result: Result<T, superschematic_engine::Error>) -> Option<String> {
    veto(result).3
}

// lists_the_waiting_actions: the adapter sees c-1's create action, with
// the attempt and where the payer comes back to, beside a second
// checkout's, a page at a time; listing needs its permission.
fn lists_the_waiting_actions(engine: &superschematic_engine::Engine, attempt: &str) {
    // The adapter sees the create action, with the attempt's id as its
    // idempotency key's source and where the payer comes back to.
    checkout(engine, "c-2", 900);
    open_payment(engine, "c-2", "cart-9").expect("c-2's attempt");
    let waiting = actions(engine, "Checkout");
    assert_eq!(waiting.len(), 2);
    assert_eq!(waiting[0]["kind"], json!("create"));
    assert_eq!(waiting[0]["attempt"], json!(attempt));
    assert_eq!(waiting[0]["amount"], json!(2500));
    assert_eq!(waiting[0]["id"], json!("c-1"));
    assert_eq!(waiting[0]["returnUrl"], json!("https://shop.test/back"));
    assert!(waiting[0]["action"].as_str().unwrap().starts_with("act_"));
    assert_eq!(waiting[1]["id"], json!("c-2"));
    // Listing needs the adapter's permission.
    let listed = engine.instances().invoke_schema(
        &alice(),
        "Checkout",
        "listPaymentActions",
        &json!({"provider": PROVIDER}),
        None,
    );
    assert_eq!(code(listed), EngineErrorCode::Forbidden);
}

#[test]
fn an_attempt_opens_once_per_key_and_its_create_action_waits_for_the_adapter() {
    for target in targets("an_attempt_opens_once_per_key") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));
        checkout(&engine, "c-1", 2500);
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(fields["due"], json!(2500));
        assert_eq!(fields["outstanding"], json!(2500));
        assert_eq!(fields["currency"], json!("GBP"));
        assert_eq!(fields["state"], json!("unpaid"));
        assert_eq!(fields["attempts"], json!([]));

        let opened = open_payment(&engine, "c-1", "cart-7").expect("openPayment");
        assert_eq!(opened["amount"], json!(2500));
        assert_eq!(opened["currency"], json!("GBP"));
        assert_eq!(opened["status"], json!("open"));
        assert_eq!(opened["provider"], json!(PROVIDER));
        let attempt = opened["attempt"].as_str().unwrap().to_owned();
        assert!(attempt.starts_with("pay_"), "{attempt}");
        assert_eq!(
            payments(&engine, "Checkout", "c-1")["state"],
            json!("pending")
        );
        assert_ledger(&engine, "Checkout", "c-1");

        // The same key returns the same attempt and writes nothing.
        let seq = read(&engine, "Checkout", "c-1").seq;
        assert_eq!(open_payment(&engine, "c-1", "cart-7").unwrap(), opened);
        assert_eq!(read(&engine, "Checkout", "c-1").seq, seq);
        // The key with other parameters is key_reused.
        let reused = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-1",
            "openPayment",
            json!({"provider": "other", "idempotencyKey": "cart-7"}),
        );
        assert_eq!(code_of(reused).as_deref(), Some("key_reused"));
        // A second attempt while one is in flight has nothing to pay.
        let (_, _, _, vetoed, details) = veto(open_payment(&engine, "c-1", "cart-8"));
        assert_eq!(vetoed.as_deref(), Some("nothing_due"));
        assert_eq!(details.unwrap()["pending"], json!(2500));

        lists_the_waiting_actions(&engine, &attempt);
        // Nothing is due on an instance that owes nothing.
        checkout(&engine, "c-0", 0);
        assert_eq!(
            code_of(open_payment(&engine, "c-0", "zero")).as_deref(),
            Some("nothing_due")
        );
    }
}

#[test]
fn a_provider_event_applies_once_and_a_repeat_writes_nothing() {
    for target in targets("a_provider_event_applies_once") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));
        checkout(&engine, "c-1", 2500);
        let attempt = open_payment(&engine, "c-1", "k").unwrap()["attempt"]
            .as_str()
            .unwrap()
            .to_owned();

        // Only the adapter records what a provider did.
        let forged = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-1",
            "recordPaymentEvent",
            event("evt_1", "captured", 2500, json!({"attempt": attempt})),
        );
        assert_eq!(code(forged), EngineErrorCode::Forbidden);

        let processing = record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "evt_1",
                "processing",
                2500,
                json!({"attempt": attempt, "providerRef": "pi_123"}),
            ),
        )
        .expect("processing");
        assert_eq!(processing["applied"], json!(true));
        assert_eq!(processing["attempt"]["status"], json!("processing"));
        assert_eq!(processing["attempt"]["providerRef"], json!("pi_123"));
        // The create action stops waiting once the provider reports the
        // attempt.
        assert_eq!(kinds(&engine, "Checkout", "c-1"), Vec::<String>::new());

        // By the provider's reference alone, with display hints.
        let captured = record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "evt_2",
                "captured",
                2500,
                json!({"providerRef": "pi_123", "brand": "visa", "last4": "4242", "metadata": {"order": "c-1"}}),
            ),
        )
        .expect("captured");
        assert_eq!(captured["applied"], json!(true));
        assert_eq!(captured["state"], json!("paid"));
        assert_eq!(captured["attempt"]["captured"], json!(2500));
        assert_eq!(captured["attempt"]["last4"], json!("4242"));
        assert_ledger(&engine, "Checkout", "c-1");

        // A repeat changes nothing and writes nothing: no event, the same seq.
        let seq = read(&engine, "Checkout", "c-1").seq;
        let again = record(
            &engine,
            "Checkout",
            "c-1",
            event("evt_2", "captured", 2500, json!({"providerRef": "pi_123"})),
        )
        .expect("the repeat");
        assert_eq!(again["applied"], json!(false));
        assert_eq!(again["state"], json!("paid"));
        assert_eq!(read(&engine, "Checkout", "c-1").seq, seq);
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(fields["captured"], json!(2500));
        assert_eq!(fields["outstanding"], json!(0));
        // The same event id with other content is key_reused, and so is it
        // reported to another instance.
        let other = record(
            &engine,
            "Checkout",
            "c-1",
            event("evt_2", "captured", 100, json!({"providerRef": "pi_123"})),
        );
        assert_eq!(code_of(other).as_deref(), Some("key_reused"));
        checkout(&engine, "c-2", 100);
        let elsewhere = record(
            &engine,
            "Checkout",
            "c-2",
            event("evt_2", "captured", 2500, json!({"providerRef": "pi_123"})),
        );
        assert_eq!(code_of(elsewhere).as_deref(), Some("key_reused"));

        // An attempt or a reference the instance does not have.
        for named in [
            json!({"attempt": "pay_nope"}),
            json!({"providerRef": "pi_nope"}),
            json!({}),
        ] {
            let missing = record(
                &engine,
                "Checkout",
                "c-1",
                event("evt_x", "failed", 0, named),
            );
            assert_eq!(code_of(missing).as_deref(), Some("no_attempt"));
        }
        // A reference that contradicts the attempt's.
        let contradicted = record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "evt_y",
                "failed",
                0,
                json!({"attempt": attempt, "providerRef": "pi_999"}),
            ),
        );
        assert_eq!(code_of(contradicted).as_deref(), Some("no_attempt"));
        // Another provider's event about it.
        let mut elsewhere = event("evt_z", "failed", 0, json!({"attempt": attempt}));
        elsewhere["provider"] = json!("stripe");
        assert_eq!(
            code_of(record(&engine, "Checkout", "c-1", elsewhere)).as_deref(),
            Some("no_attempt")
        );
        assert_ledger(&engine, "Checkout", "c-1");
    }
}

#[test]
fn reordered_delivery_moves_an_attempt_only_forward() {
    for target in targets("reordered_delivery") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));

        // captured arrives before processing and requiresAction: the late
        // ones are recorded and change nothing.
        checkout(&engine, "c-1", 1200);
        let attempt = open_payment(&engine, "c-1", "k1").unwrap()["attempt"].clone();
        let first = record(
            &engine,
            "Checkout",
            "c-1",
            event("e1", "captured", 1200, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(first["attempt"]["status"], json!("captured"));
        for (id, kind) in [
            ("e2", "processing"),
            ("e3", "requiresAction"),
            ("e4", "authorized"),
            ("e5", "failed"),
            ("e6", "cancelled"),
        ] {
            let seq = read(&engine, "Checkout", "c-1").seq;
            let late = record(
                &engine,
                "Checkout",
                "c-1",
                event(id, kind, 1200, json!({"attempt": attempt})),
            )
            .unwrap();
            assert_eq!(late["applied"], json!(false), "{kind}");
            assert_eq!(late["attempt"]["status"], json!("captured"), "{kind}");
            assert_eq!(late["state"], json!("paid"), "{kind}");
            // Recorded, unlike a repeat: the event is in the log.
            assert_eq!(read(&engine, "Checkout", "c-1").seq, seq + 1, "{kind}");
        }
        assert_ledger(&engine, "Checkout", "c-1");

        // A failed try, then success on the same attempt: the payment is
        // caught; a late failure of the earlier try changes nothing.
        checkout(&engine, "c-2", 800);
        let attempt = open_payment(&engine, "c-2", "k2").unwrap()["attempt"].clone();
        let failed = record(
            &engine,
            "Checkout",
            "c-2",
            event("f1", "failed", 800, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(failed["attempt"]["status"], json!("failed"));
        assert_eq!(failed["state"], json!("unpaid"));
        let authorized = record(
            &engine,
            "Checkout",
            "c-2",
            event("f2", "authorized", 800, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(authorized["applied"], json!(true));
        assert_eq!(authorized["state"], json!("authorized"));
        let late = record(
            &engine,
            "Checkout",
            "c-2",
            event("f0", "failed", 800, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(late["applied"], json!(false));
        let fields = payments(&engine, "Checkout", "c-2");
        assert_eq!(fields["authorized"], json!(800));
        assert_eq!(fields["captured"], json!(0));
        // Captured after a cancellation was reported: the money is real.
        let cancelled = record(
            &engine,
            "Checkout",
            "c-2",
            event("f3", "cancelled", 800, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(cancelled["state"], json!("unpaid"));
        assert_eq!(payments(&engine, "Checkout", "c-2")["authorized"], json!(0));
        let captured = record(
            &engine,
            "Checkout",
            "c-2",
            event("f4", "captured", 800, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(captured["applied"], json!(true));
        assert_eq!(captured["state"], json!("paid"));
        assert_ledger(&engine, "Checkout", "c-2");
    }
}

#[test]
fn the_amount_is_locked_while_an_attempt_is_in_flight() {
    for target in targets("the_amount_is_locked") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(
            &engine,
            payments_config(json!({"currency": {"field": "currency"}})),
        );
        checkout(&engine, "c-1", 2500);
        // Before any attempt the amount moves freely.
        update(&engine, "Checkout", "c-1", json!({"totalCents": 3000})).expect("before");
        let attempt = open_payment(&engine, "c-1", "k1").unwrap();
        assert_eq!(attempt["amount"], json!(3000));
        let (behavior, action, _, vetoed, details) = veto(update(
            &engine,
            "Checkout",
            "c-1",
            json!({"totalCents": 100}),
        ));
        assert_eq!((behavior.as_str(), action.as_str()), ("Payments", "update"));
        assert_eq!(vetoed.as_deref(), Some("amount_locked"));
        assert_eq!(details.unwrap()["due"], json!(3000));
        // A patch that leaves the amount as it is, or other fields, passes.
        update(
            &engine,
            "Checkout",
            "c-1",
            json!({"totalCents": 3000, "title": "renamed"}),
        )
        .expect("the same amount");
        // The currency stays while any attempt exists.
        let moved = update(&engine, "Checkout", "c-1", json!({"currency": "EUR"}));
        assert_eq!(code_of(moved).as_deref(), Some("currency_mismatch"));

        // Once the attempt is cancelled the amount moves again, and a new
        // attempt opens for it.
        record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "e1",
                "cancelled",
                3000,
                json!({"attempt": attempt["attempt"]}),
            ),
        )
        .unwrap();
        update(&engine, "Checkout", "c-1", json!({"totalCents": 1000})).expect("after");
        let second = open_payment(&engine, "c-1", "k2").unwrap();
        assert_eq!(second["amount"], json!(1000));
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(fields["due"], json!(1000));
        let shown: Vec<&Value> = fields["attempts"]
            .as_array()
            .unwrap()
            .iter()
            .map(|a| &a["status"])
            .collect();
        assert_eq!(shown, [&json!("open"), &json!("cancelled")]);
        // A delete waits while an attempt is in flight.
        let deleted = engine.instances().delete(
            &alice(),
            "Checkout",
            "c-1",
            superschematic_engine::WriteOptions::default(),
        );
        assert_eq!(code_of(deleted).as_deref(), Some("payment_in_progress"));
        assert_ledger(&engine, "Checkout", "c-1");
    }
}

#[test]
fn require_holds_a_move_and_void_on_voids_what_is_open() {
    for target in targets("require_and_void_on") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({"afterRelease": "refund"})));
        checkout(&engine, "c-1", 2000);
        transition(&engine, &alice(), "Checkout", "c-1", "held").unwrap();
        let (behavior, action, _, vetoed, details) =
            veto(transition(&engine, &alice(), "Checkout", "c-1", "placed"));
        assert_eq!(
            (behavior.as_str(), action.as_str()),
            ("Payments", "transition")
        );
        assert_eq!(vetoed.as_deref(), Some("unpaid"));
        assert_eq!(details.unwrap()["requires"], json!("paid"));
        let attempt = open_payment(&engine, "c-1", "k1").unwrap()["attempt"].clone();
        // Authorized is not paid.
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e1", "authorized", 2000, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(
            code_of(transition(&engine, &alice(), "Checkout", "c-1", "placed")).as_deref(),
            Some("unpaid")
        );
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e2", "captured", 2000, json!({"attempt": attempt})),
        )
        .unwrap();
        transition(&engine, &alice(), "Checkout", "c-1", "placed").expect("paid, so placed");

        // A processing payment holds the claim: the expiry waits.
        checkout(&engine, "c-2", 700);
        transition(&engine, &alice(), "Checkout", "c-2", "held").unwrap();
        let attempt = open_payment(&engine, "c-2", "k2").unwrap()["attempt"].clone();
        record(
            &engine,
            "Checkout",
            "c-2",
            event("p1", "processing", 700, json!({"attempt": attempt})),
        )
        .unwrap();
        let (_, _, _, vetoed, _) =
            veto(transition(&engine, &alice(), "Checkout", "c-2", "expired"));
        assert_eq!(vetoed.as_deref(), Some("payment_in_progress"));
        assert_eq!(status(&engine, "Checkout", "c-2"), "held");
        // It fails; the expiry goes through and voids nothing it cannot.
        record(
            &engine,
            "Checkout",
            "c-2",
            event("p2", "failed", 700, json!({"attempt": attempt})),
        )
        .unwrap();
        transition(&engine, &alice(), "Checkout", "c-2", "expired").expect("expired");
        assert_eq!(kinds(&engine, "Checkout", "c-2"), Vec::<String>::new());

        // Entering a voidOn state voids what is open and authorized, once.
        checkout(&engine, "c-3", 500);
        transition(&engine, &alice(), "Checkout", "c-3", "held").unwrap();
        let attempt = open_payment(&engine, "c-3", "k3").unwrap()["attempt"].clone();
        transition(&engine, &alice(), "Checkout", "c-3", "cancelled").expect("cancelled");
        assert_eq!(kinds(&engine, "Checkout", "c-3"), ["create", "void"]);
        update(&engine, "Checkout", "c-3", json!({"title": "touched"})).expect("a later change");
        assert_eq!(kinds(&engine, "Checkout", "c-3"), ["create", "void"]);
        // Nothing is due in a voidOn state.
        assert_eq!(
            code_of(open_payment(&engine, "c-3", "k4")).as_deref(),
            Some("nothing_due")
        );
        // The provider reports a capture after all: afterRelease refunds it.
        record(
            &engine,
            "Checkout",
            "c-3",
            event("v1", "captured", 500, json!({"attempt": attempt})),
        )
        .unwrap();
        let waiting = actions(&engine, "Checkout");
        let refund = waiting
            .iter()
            .find(|action| action["id"] == json!("c-3") && action["kind"] == json!("refund"))
            .expect("the refund of money captured after the release");
        assert_eq!(refund["amount"], json!(500));
        // The void and the create stop waiting once it is captured.
        assert_eq!(kinds(&engine, "Checkout", "c-3"), ["refund"]);
        record(
            &engine,
            "Checkout",
            "c-3",
            event("v2", "refunded", 500, json!({"refund": refund["refund"]})),
        )
        .unwrap();
        assert_eq!(
            payments(&engine, "Checkout", "c-3")["state"],
            json!("refunded")
        );
        for id in ["c-1", "c-2", "c-3"] {
            assert_ledger(&engine, "Checkout", id);
        }
    }
}

#[test]
fn after_release_keep_leaves_money_captured_late_for_an_operator() {
    for target in targets("after_release_keep") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));
        checkout(&engine, "c-1", 500);
        transition(&engine, &alice(), "Checkout", "c-1", "held").unwrap();
        let attempt = open_payment(&engine, "c-1", "k").unwrap()["attempt"].clone();
        transition(&engine, &alice(), "Checkout", "c-1", "expired").unwrap();
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e1", "captured", 500, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(kinds(&engine, "Checkout", "c-1"), Vec::<String>::new());
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(fields["captured"], json!(500));
        assert_eq!(fields["state"], json!("paid"));
    }
}

#[test]
fn manual_capture_captures_in_capture_on_states_and_never_past_what_is_authorized() {
    for target in targets("manual_capture") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(
            &engine,
            payments_config(json!({
                "capture": "manual",
                "captureOn": ["shipped"],
                "require": {"placed": "authorized"}
            })),
        );
        checkout(&engine, "c-1", 4000);
        transition(&engine, &alice(), "Checkout", "c-1", "held").unwrap();
        let attempt = open_payment(&engine, "c-1", "k").unwrap()["attempt"].clone();
        assert_eq!(
            code_of(transition(&engine, &alice(), "Checkout", "c-1", "placed")).as_deref(),
            Some("unauthorized")
        );
        // Nothing to capture until the provider authorizes.
        assert_eq!(
            code_of(invoke(
                &engine,
                &alice(),
                "Checkout",
                "c-1",
                "capturePayment",
                json!({"attempt": attempt})
            ))
            .as_deref(),
            Some("attempt_closed")
        );
        record(
            &engine,
            "Checkout",
            "c-1",
            event("a1", "authorized", 4000, json!({"attempt": attempt})),
        )
        .unwrap();
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(
            (fields["state"].clone(), fields["authorized"].clone()),
            (json!("authorized"), json!(4000))
        );
        transition(&engine, &alice(), "Checkout", "c-1", "placed").expect("authorized, so placed");
        assert_eq!(kinds(&engine, "Checkout", "c-1"), Vec::<String>::new());

        // A capture past what is authorized, asked or reported, is refused.
        let asked = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-1",
            "capturePayment",
            json!({"attempt": attempt, "amount": 4001}),
        );
        assert_eq!(code_of(asked).as_deref(), Some("over_capture"));
        let (_, _, _, vetoed, details) = veto(record(
            &engine,
            "Checkout",
            "c-1",
            event("x1", "captured", 4500, json!({"attempt": attempt})),
        ));
        assert_eq!(vetoed.as_deref(), Some("over_capture"));
        assert_eq!(details.unwrap()["capturable"], json!(4000));
        assert_ledger(&engine, "Checkout", "c-1");

        // Entering shipped captures what is authorized, once.
        transition(&engine, &alice(), "Checkout", "c-1", "shipped").expect("shipped");
        let waiting = actions(&engine, "Checkout");
        assert_eq!(waiting.len(), 1);
        assert_eq!(
            (waiting[0]["kind"].clone(), waiting[0]["amount"].clone()),
            (json!("capture"), json!(4000))
        );
        // capturePayment returns the waiting action rather than another.
        let again = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-1",
            "capturePayment",
            json!({"attempt": attempt}),
        )
        .unwrap();
        assert_eq!(again["action"], waiting[0]["action"]);
        record(
            &engine,
            "Checkout",
            "c-1",
            event("c1", "captured", 4000, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(payments(&engine, "Checkout", "c-1")["state"], json!("paid"));
        assert_eq!(kinds(&engine, "Checkout", "c-1"), Vec::<String>::new());
        // A void of a captured attempt is closed.
        assert_eq!(
            code_of(invoke(
                &engine,
                &alice(),
                "Checkout",
                "c-1",
                "voidPayment",
                json!({"attempt": attempt})
            ))
            .as_deref(),
            Some("attempt_closed")
        );
        assert_ledger(&engine, "Checkout", "c-1");

        // A partial capture by hand, then the rest.
        checkout(&engine, "c-2", 1000);
        let attempt = open_payment(&engine, "c-2", "k").unwrap()["attempt"].clone();
        record(
            &engine,
            "Checkout",
            "c-2",
            event("b1", "authorized", 1000, json!({"attempt": attempt})),
        )
        .unwrap();
        let part = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-2",
            "capturePayment",
            json!({"attempt": attempt, "amount": 600}),
        )
        .unwrap();
        assert_eq!(part["amount"], json!(600));
        let rest = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-2",
            "capturePayment",
            json!({"attempt": attempt, "amount": 400}),
        )
        .unwrap();
        assert_ne!(rest["action"], part["action"]);
        assert_eq!(
            code_of(invoke(
                &engine,
                &alice(),
                "Checkout",
                "c-2",
                "capturePayment",
                json!({"attempt": attempt, "amount": 1})
            ))
            .as_deref(),
            Some("over_capture")
        );
        // A void of an authorized attempt queues one void.
        let void = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-2",
            "voidPayment",
            json!({"attempt": attempt}),
        )
        .unwrap();
        let void_again = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-2",
            "voidPayment",
            json!({"attempt": attempt}),
        )
        .unwrap();
        assert_eq!(void["action"], void_again["action"]);
        assert_eq!(void["kind"], json!("void"));
        assert_eq!(
            code_of(invoke(
                &engine,
                &alice(),
                "Checkout",
                "c-2",
                "voidPayment",
                json!({"attempt": "pay_nope"})
            ))
            .as_deref(),
            Some("no_attempt")
        );
        assert_ledger(&engine, "Checkout", "c-2");
    }
}

#[test]
fn a_refund_draws_on_what_is_captured() {
    for target in targets("a_refund_draws_on_what_is_captured") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));
        checkout(&engine, "c-1", 3000);
        let attempt = open_payment(&engine, "c-1", "k").unwrap()["attempt"].clone();
        let refund = |amount: i64, key: &str| {
            invoke(
                &engine,
                &refunder(),
                "Checkout",
                "c-1",
                "refundPayment",
                json!({"amount": amount, "reason": "damaged", "idempotencyKey": key}),
            )
        };
        // Nothing captured, nothing refundable.
        let (_, _, _, vetoed, details) = veto(refund(100, "r0"));
        assert_eq!(vetoed.as_deref(), Some("exceeds_refundable"));
        assert_eq!(details.unwrap()["refundable"], json!(0));
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e1", "captured", 3000, json!({"attempt": attempt})),
        )
        .unwrap();

        // Refunding needs the permission.
        let denied = invoke(
            &engine,
            &alice(),
            "Checkout",
            "c-1",
            "refundPayment",
            json!({"amount": 100, "reason": "x", "idempotencyKey": "r1"}),
        );
        assert_eq!(code(denied), EngineErrorCode::Forbidden);

        let first = refund(1000, "r1").expect("a refund");
        assert_eq!(first["status"], json!("requested"));
        assert_eq!(first["currency"], json!("GBP"));
        // The same key returns it; with another amount it is key_reused.
        assert_eq!(refund(1000, "r1").unwrap(), first);
        assert_eq!(code_of(refund(1200, "r1")).as_deref(), Some("key_reused"));
        // Pending refunds count against what is refundable.
        let (_, _, _, vetoed, details) = veto(refund(2500, "r2"));
        assert_eq!(vetoed.as_deref(), Some("exceeds_refundable"));
        let details = details.unwrap();
        assert_eq!(details["refundable"], json!(2000));
        assert_eq!(details["pending"], json!(1000));
        let waiting = actions(&engine, "Checkout");
        assert_eq!(waiting.len(), 1);
        assert_eq!(waiting[0]["kind"], json!("refund"));
        assert_eq!(waiting[0]["refund"], first["refund"]);
        assert_eq!(waiting[0]["action"], first["action"]);

        // The provider refunds it: partially refunded.
        let sent = record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "r-e0",
                "processing",
                1000,
                json!({"refund": first["refund"]}),
            ),
        )
        .unwrap();
        assert_eq!(sent["applied"], json!(true));
        let done = record(
            &engine,
            "Checkout",
            "c-1",
            event("r-e1", "refunded", 1000, json!({"refund": first["refund"]})),
        )
        .unwrap();
        assert_eq!(done["state"], json!("partiallyRefunded"));
        assert_eq!(actions(&engine, "Checkout"), Vec::<Value>::new());
        // A late refundFailed changes nothing.
        let late = record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "r-e2",
                "refundFailed",
                1000,
                json!({"refund": first["refund"]}),
            ),
        )
        .unwrap();
        assert_eq!(late["applied"], json!(false));
        // The rest, then refunded.
        let second = refund(2000, "r3").expect("the rest");
        // An event in another currency is refused.
        let mut euros = event(
            "r-e3",
            "refunded",
            2000,
            json!({"refund": second["refund"]}),
        );
        euros["currency"] = json!("EUR");
        assert_eq!(
            code_of(record(&engine, "Checkout", "c-1", euros)).as_deref(),
            Some("currency_mismatch")
        );
        record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "r-e3",
                "refunded",
                2000,
                json!({"refund": second["refund"]}),
            ),
        )
        .unwrap();
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(fields["refunded"], json!(3000));
        assert_eq!(fields["state"], json!("refunded"));
        assert_eq!(
            code_of(refund(1, "r4")).as_deref(),
            Some("exceeds_refundable")
        );
        assert_ledger(&engine, "Checkout", "c-1");

        // A config with no refunds permission refuses refundPayment.
        publish(&engine, "Plain", payments_config(json!({"refunds": null})));
        create(&engine, "Plain", "p-1", json!({"totalCents": 10}), None).unwrap();
        let refused = invoke(
            &engine,
            &refunder(),
            "Plain",
            "p-1",
            "refundPayment",
            json!({"amount": 1, "reason": "x", "idempotencyKey": "k"}),
        );
        assert_eq!(code_of(refused).as_deref(), Some("not_configured"));
    }
}

fn publish(engine: &superschematic_engine::Engine, name: &str, config: Value) {
    support::publish(
        engine,
        &checkout_document(
            name,
            json!([checkout_flow(), {"name": "Payments", "config": config}]),
        ),
        None,
    );
}

#[test]
fn an_amount_in_another_currency_is_refused() {
    for target in targets("another_currency") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));
        checkout(&engine, "c-1", 1500);
        let attempt = open_payment(&engine, "c-1", "k").unwrap()["attempt"].clone();
        let mut euros = event("e1", "captured", 1500, json!({"attempt": attempt}));
        euros["currency"] = json!("EUR");
        let (_, _, _, vetoed, details) = veto(record(&engine, "Checkout", "c-1", euros));
        assert_eq!(vetoed.as_deref(), Some("currency_mismatch"));
        assert_eq!(details.unwrap()["expected"], json!("GBP"));
        // Refused, so nothing was recorded: the event in GBP applies.
        let captured = record(
            &engine,
            "Checkout",
            "c-1",
            event("e1", "captured", 1500, json!({"attempt": attempt})),
        )
        .unwrap();
        assert_eq!(captured["applied"], json!(true));

        // A currency from a field: an instance that names none pays nothing.
        publish(
            &engine,
            "Open",
            payments_config(json!({"currency": {"field": "currency"}})),
        );
        create(&engine, "Open", "o-1", json!({"totalCents": 100}), None).unwrap();
        let (_, _, _, vetoed, _) = veto(invoke(
            &engine,
            &alice(),
            "Open",
            "o-1",
            "openPayment",
            json!({"provider": PROVIDER, "idempotencyKey": "k"}),
        ));
        assert_eq!(vetoed.as_deref(), Some("currency_mismatch"));
        create(
            &engine,
            "Open",
            "o-2",
            json!({"totalCents": 100, "currency": "JPY"}),
            None,
        )
        .unwrap();
        let opened = invoke(
            &engine,
            &alice(),
            "Open",
            "o-2",
            "openPayment",
            json!({"provider": PROVIDER, "idempotencyKey": "k"}),
        )
        .unwrap();
        assert_eq!(opened["currency"], json!("JPY"));
        if currency::listed() {
            // A field that holds no active code is refused at openPayment.
            create(
                &engine,
                "Open",
                "o-3",
                json!({"totalCents": 100, "currency": "XYZ"}),
                None,
            )
            .unwrap();
            let (_, _, _, vetoed, _) = veto(invoke(
                &engine,
                &alice(),
                "Open",
                "o-3",
                "openPayment",
                json!({"provider": PROVIDER, "idempotencyKey": "k"}),
            ));
            assert_eq!(vetoed.as_deref(), Some("currency_mismatch"));
        }
    }
}

#[test]
fn a_literal_currency_is_checked_against_iso_4217_when_the_schema_is_defined() {
    for target in targets("a_literal_currency") {
        let engine = open(&target, BehaviorSet::new());
        let define = |config: Value| {
            engine.schemas().define(
                &alice(),
                checkout_document(
                    "Odd",
                    json!([checkout_flow(), {"name": "Payments", "config": config}]),
                ),
                DefineOptions::default(),
            )
        };
        define(payments_config(json!({"currency": "JPY"}))).expect("an active code");
        // The configSchema's pattern refuses what is not three capitals.
        assert!(define(payments_config(json!({"currency": "gbp"}))).is_err());
        if currency::listed() {
            let refused = refusal(define(payments_config(json!({"currency": "XYZ"}))));
            assert!(refused.to_string().contains("XYZ"), "{refused}");
        } else {
            eprintln!("the linked superscalar has no Finance.Currency; XYZ passes its shape until the pin moves");
            define(payments_config(json!({"currency": "XYZ"}))).expect("XYZ has the shape");
        }
        // A config the type does not fit.
        for (config, says) in [
            (
                payments_config(json!({"amount": {"field": "title"}})),
                "not an integer field",
            ),
            (
                payments_config(json!({"amount": {"field": "nope"}})),
                "is not a field",
            ),
            (
                payments_config(json!({"currency": {"field": "totalCents"}})),
                "not a string field",
            ),
            (
                payments_config(json!({"require": {"nowhere": "paid"}})),
                "not a state",
            ),
            (
                payments_config(json!({"require": {"cart": "paid"}})),
                "initial state",
            ),
            (payments_config(json!({"voidOn": ["gone"]})), "not a state"),
            (
                payments_config(json!({"amount": {"holds": true}})),
                "does not list Holds",
            ),
        ] {
            let refused = refusal(define(config));
            assert!(refused.to_string().contains(says), "{says}: {refused}");
        }
    }
}

#[test]
fn a_claims_holds_total_is_what_it_owes() {
    for target in targets("a_claims_holds_total") {
        let engine = open(&target, BehaviorSet::new().with(HoldsTotal));
        let config = payments_config(json!({"amount": {"holds": true}, "currency": null}));
        support::publish(
            &engine,
            &checkout_document(
                "Checkout",
                json!([checkout_flow(), {"name": "Holds"}, {"name": "Payments", "config": config}]),
            ),
            None,
        );
        checkout(&engine, "c-1", 0);
        let set_total = |total: Value| {
            invoke(
                &engine,
                &alice(),
                "Checkout",
                "c-1",
                "setTotal",
                json!({"total": total}),
            )
        };
        assert_eq!(payments(&engine, "Checkout", "c-1")["due"], json!(0));
        assert_eq!(
            code_of(open_payment(&engine, "c-1", "k0")).as_deref(),
            Some("nothing_due")
        );
        set_total(json!({"amount": 4200, "currency": "EUR"})).unwrap();
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(
            (fields["due"].clone(), fields["currency"].clone()),
            (json!(4200), json!("EUR"))
        );
        let opened = open_payment(&engine, "c-1", "k1").unwrap();
        assert_eq!(
            (opened["amount"].clone(), opened["currency"].clone()),
            (json!(4200), json!("EUR"))
        );
        // The total does not move while the attempt is in flight.
        let (behavior, action, _, vetoed, _) =
            veto(set_total(json!({"amount": 5000, "currency": "EUR"})));
        assert_eq!(
            (behavior.as_str(), action.as_str()),
            ("Payments", "setTotal")
        );
        assert_eq!(vetoed.as_deref(), Some("amount_locked"));
        assert_eq!(payments(&engine, "Checkout", "c-1")["due"], json!(4200));
        record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "e1",
                "captured",
                4200,
                json!({"attempt": opened["attempt"], "currency": "EUR"}),
            ),
        )
        .unwrap();
        // Paid: the total moves again, and what it adds is outstanding.
        set_total(json!({"amount": 5000, "currency": "EUR"})).expect("after it is paid");
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(
            (fields["outstanding"].clone(), fields["state"].clone()),
            (json!(800), json!("pending"))
        );
        let more = open_payment(&engine, "c-1", "k2").unwrap();
        assert_eq!(more["amount"], json!(800));
        assert_ledger(&engine, "Checkout", "c-1");

        // A Payments listed before Holds would read a total not yet written.
        let refused = engine.schemas().define(
            &alice(),
            checkout_document(
                "Early",
                json!([checkout_flow(), {"name": "Payments", "config": payments_config(json!({"amount": {"holds": true}, "currency": null}))}, {"name": "Holds"}]),
            ),
            DefineOptions::default(),
        );
        assert!(refusal(refused).to_string().contains("after Holds"));
        // A config's currency must be the total's.
        support::publish(
            &engine,
            &checkout_document(
                "Pounds",
                json!([checkout_flow(), {"name": "Holds"}, {"name": "Payments", "config": payments_config(json!({"amount": {"holds": true}}))}]),
            ),
            None,
        );
        create(&engine, "Pounds", "p-1", json!({}), None).unwrap();
        invoke(
            &engine,
            &alice(),
            "Pounds",
            "p-1",
            "setTotal",
            json!({"total": {"amount": 10, "currency": "EUR"}}),
        )
        .unwrap();
        let (_, _, _, vetoed, _) = veto(invoke(
            &engine,
            &alice(),
            "Pounds",
            "p-1",
            "openPayment",
            json!({"provider": PROVIDER, "idempotencyKey": "k"}),
        ));
        assert_eq!(vetoed.as_deref(), Some("currency_mismatch"));
    }
}
