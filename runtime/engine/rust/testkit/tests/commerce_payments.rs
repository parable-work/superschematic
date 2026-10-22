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
//! currency is refused; a new attempt voids one that failed, a capture
//! that pays what is due voids the others, and what a late capture brings
//! past what is due is `overpaid` and asked back; and a claim's real
//! `Holds.total` is what it owes, failing closed (`total_unknown`) while it
//! cannot be read, and 0 for a claim with nothing priced. On SQLite and,
//! with the variable set, on Postgres, with the ledger's invariants checked
//! after every step.

mod payments_support;
mod support;

use serde_json::{json, Value};

use payments_support::*;
use superschematic_engine::{BehaviorSet, DefineOptions, EngineErrorCode};
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

#[test]
fn a_finance_currency_field_takes_only_active_iso_4217_codes() {
    for target in targets("a_finance_currency_field") {
        let engine = open(&target, BehaviorSet::new());
        let mut document = checkout_document(
            "Priced",
            json!([checkout_flow(), {"name": "Payments", "config": payments_config(json!({"currency": {"field": "currency"}}))}]),
        );
        document["types"]["Priced"]["fields"] = json!([
            {"name": "title", "typeRef": {"name": "string"}},
            {"name": "totalCents", "typeRef": {"name": "Generic.Int64"}},
            {"name": "currency", "typeRef": {"name": "Finance.Currency"}}
        ]);
        support::publish(&engine, &document, None);
        create(
            &engine,
            "Priced",
            "p-1",
            json!({"totalCents": 100, "currency": "GBP"}),
            None,
        )
        .expect("an active code");
        // The engine's validation reads Finance.Currency from superscalar's
        // core: a code of the right shape ISO 4217 does not list is refused
        // as the field's value, before Payments reads it.
        for code in ["ABC", "XYZ", "gbp"] {
            let refused = refusal(create(
                &engine,
                "Priced",
                "p-2",
                json!({"totalCents": 100, "currency": code}),
                None,
            ));
            assert!(
                refused.to_string().contains("currency"),
                "{code}: {refused}"
            );
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
        for code in ["XYZ", "ABC"] {
            let refused = refusal(define(payments_config(json!({ "currency": code }))));
            assert!(
                refused.to_string().contains(&format!(
                    "\"{code}\" is not an active ISO 4217 currency code"
                )),
                "{refused}"
            );
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

// claim_documents are an Item, which Stock counts at its priceCents in
// its currency, or at no price when priceCents is absent, and a Basket of
// lines of Items, which Holds takes in held and commits in placed, and
// which owes its Holds.total through Payments (`payments` replaces
// members of the config, as payments_config does).
fn claim_documents(payments: Value) -> (Value, Value) {
    let mut item = support::schema_document(
        "Item",
        json!([
            {"name": "title", "typeRef": {"name": "string"}},
            {"name": "priceCents", "typeRef": {"name": "Generic.Int64"}},
            {"name": "currency", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    item["types"]["Item"]["behaviors"] = json!([{"name": "Stock", "config": {
        "permission": "stock.adjust",
        "price": {"field": "priceCents", "currency": {"field": "currency"}}
    }}]);
    let mut basket = support::schema_document(
        "Basket",
        json!([
            {"name": "title", "typeRef": {"name": "string"}},
            {"name": "lines", "typeRef": {"name": "Line", "isArray": true}}
        ]),
        json!({"types": {"Line": {"name": "Line", "role": "EmbeddedStruct", "fields": [
            {"name": "itemId", "typeRef": {"name": "string"}, "required": true},
            {"name": "quantity", "typeRef": {"name": "Generic.Int64"}}
        ]}}}),
    );
    let mut config = json!({
        "amount": {"holds": true},
        "require": {"placed": "paid"},
        "voidOn": ["expired", "cancelled"],
        "record": {"permission": RECORD}
    });
    if let (Some(config), Value::Object(more)) = (config.as_object_mut(), payments) {
        config.extend(more);
    }
    basket["types"]["Basket"]["behaviors"] = json!([
        checkout_flow(),
        {"name": "Holds", "config": {
            "holds": {"lines": {"each": {"field": "lines", "target": "itemId"}, "schema": "Item", "quantity": {"field": "quantity"}}},
            "takeOn": ["held"],
            "commitOn": ["placed"],
            "releaseOn": ["expired", "cancelled"]
        }},
        {"name": "Payments", "config": config}
    ]);
    (item, basket)
}

// claims opens an engine with Stock and Holds beside Payments, the Item
// and Basket schemas, and items: two in GBP, one in EUR, one at the
// largest price an integer field holds, and one with no price.
fn claims(target: &support::Target) -> superschematic_engine::Engine {
    let engine = open(
        target,
        BehaviorSet::new()
            .with(superschematic_engine_commerce::Stock)
            .with(superschematic_engine_commerce::Holds),
    );
    let (item, basket) = claim_documents(json!({}));
    support::publish(&engine, &item, None);
    support::publish(&engine, &basket, None);
    let stocker = principal("sam", &["stock.adjust"]);
    for (id, price, currency, units) in [
        ("anvil", Some(1250), "GBP", 10),
        ("rope", Some(300), "GBP", 10),
        ("lamp", Some(900), "EUR", 10),
        ("gold", Some(9_007_199_254_740_991_i64), "GBP", 2000),
        ("leaflet", None, "GBP", 10),
    ] {
        let mut data = json!({"title": id, "currency": currency});
        if let Some(price) = price {
            data["priceCents"] = json!(price);
        }
        create(&engine, "Item", id, data, None).expect("create the item");
        invoke(
            &engine,
            &stocker,
            "Item",
            id,
            "receiveStock",
            json!({"quantity": units, "reason": "delivery"}),
        )
        .expect("receive");
    }
    engine
}

fn basket(engine: &superschematic_engine::Engine, id: &str, lines: Value) {
    create(
        engine,
        "Basket",
        id,
        json!({"title": id, "lines": lines}),
        None,
    )
    .expect("create the basket");
}

fn pay_basket(
    engine: &superschematic_engine::Engine,
    id: &str,
    key: &str,
) -> Result<Value, superschematic_engine::Error> {
    invoke(
        engine,
        &alice(),
        "Basket",
        id,
        "openPayment",
        json!({"provider": PROVIDER, "idempotencyKey": key}),
    )
}

#[test]
fn a_claims_holds_total_is_what_it_owes() {
    for target in targets("a_claims_holds_total") {
        let engine = claims(&target);
        basket(
            &engine,
            "b-1",
            json!([{"itemId": "anvil", "quantity": 2}, {"itemId": "rope", "quantity": 1}]),
        );
        // In the cart its lines are pending, whose prices come when they
        // are held: what it owes is not known yet.
        let (_, _, _, vetoed, _) = veto(pay_basket(&engine, "b-1", "k0"));
        assert_eq!(vetoed.as_deref(), Some("total_unknown"));

        transition(&engine, &alice(), "Basket", "b-1", "held").expect("held");
        let fields = payments(&engine, "Basket", "b-1");
        assert_eq!(
            (fields["due"].clone(), fields["currency"].clone()),
            (json!(2800), json!("GBP"))
        );
        let opened = pay_basket(&engine, "b-1", "k1").expect("openPayment");
        assert_eq!(
            (opened["amount"].clone(), opened["currency"].clone()),
            (json!(2800), json!("GBP"))
        );
        // Placed waits for the money.
        let (_, _, _, vetoed, _) = veto(transition(&engine, &alice(), "Basket", "b-1", "placed"));
        assert_eq!(vetoed.as_deref(), Some("unpaid"));
        record(
            &engine,
            "Basket",
            "b-1",
            event(
                "e1",
                "captured",
                2800,
                json!({"attempt": opened["attempt"]}),
            ),
        )
        .unwrap();
        transition(&engine, &alice(), "Basket", "b-1", "placed").expect("paid, placed");
        assert_ledger(&engine, "Basket", "b-1");

        // A Payments listed before Holds would read a total not yet written.
        let (_, early) = claim_documents(json!({}));
        let mut early = early;
        early["name"] = json!("Early");
        let behaviors = early["types"]["Basket"]["behaviors"].clone();
        early["types"]["Early"] = early["types"]["Basket"].take();
        early["types"]["Early"]["name"] = json!("Early");
        early["types"].as_object_mut().unwrap().remove("Basket");
        early["types"]["Early"]["behaviors"] = json!([behaviors[0], behaviors[2], behaviors[1]]);
        let refused = engine
            .schemas()
            .define(&alice(), early, DefineOptions::default());
        assert!(refusal(refused).to_string().contains("after Holds"));
    }
}

#[test]
fn a_holds_total_that_cannot_be_read_owes_nothing_known_and_a_free_claim_owes_nothing() {
    for target in targets("a_holds_total_that_cannot_be_read") {
        let engine = claims(&target);
        // Two currencies are never added: nothing can be paid, and placed
        // waits as it would for the money.
        basket(
            &engine,
            "mixed",
            json!([{"itemId": "anvil", "quantity": 1}, {"itemId": "lamp", "quantity": 1}]),
        );
        // A sum past what an integer holds.
        basket(
            &engine,
            "huge",
            json!([{"itemId": "gold", "quantity": 1025}]),
        );
        for id in ["mixed", "huge"] {
            transition(&engine, &alice(), "Basket", id, "held").expect("held");
            let fields = payments(&engine, "Basket", id);
            assert_eq!(fields["due"], json!(0), "{id}");
            let (behavior, _, _, vetoed, _) = veto(pay_basket(&engine, id, "k"));
            assert_eq!(
                (behavior.as_str(), vetoed.as_deref()),
                ("Payments", Some("total_unknown")),
                "{id}"
            );
            let (behavior, _, _, vetoed, details) =
                veto(transition(&engine, &alice(), "Basket", id, "placed"));
            assert_eq!(
                (behavior.as_str(), vetoed.as_deref()),
                ("Payments", Some("total_unknown")),
                "{id}"
            );
            assert_eq!(details.unwrap()["requires"], json!("paid"), "{id}");
            assert_eq!(status(&engine, "Basket", id), "held", "{id}");
            // Letting it go needs no payment.
            transition(&engine, &alice(), "Basket", id, "cancelled").expect("cancelled");
        }

        // A claim whose items carry no price owes 0: it needs no payment,
        // and moves where require waits for one.
        basket(
            &engine,
            "free",
            json!([{"itemId": "leaflet", "quantity": 2}]),
        );
        transition(&engine, &alice(), "Basket", "free", "held").expect("held");
        let fields = payments(&engine, "Basket", "free");
        assert_eq!(fields["due"], json!(0));
        let (_, _, _, vetoed, _) = veto(pay_basket(&engine, "free", "k"));
        assert_eq!(vetoed.as_deref(), Some("nothing_due"));
        transition(&engine, &alice(), "Basket", "free", "placed").expect("a free claim is placed");
        assert_ledger(&engine, "Basket", "free");

        // A config's currency must be the total's.
        let (_, mut pounds) = claim_documents(json!({"currency": "EUR"}));
        pounds["name"] = json!("Euros");
        pounds["types"]["Euros"] = pounds["types"]["Basket"].take();
        pounds["types"]["Euros"]["name"] = json!("Euros");
        pounds["types"].as_object_mut().unwrap().remove("Basket");
        support::publish(&engine, &pounds, None);
        create(
            &engine,
            "Euros",
            "e-1",
            json!({"title": "e-1", "lines": [{"itemId": "rope", "quantity": 1}]}),
            None,
        )
        .unwrap();
        transition(&engine, &alice(), "Euros", "e-1", "held").expect("held");
        let (_, _, _, vetoed, _) = veto(invoke(
            &engine,
            &alice(),
            "Euros",
            "e-1",
            "openPayment",
            json!({"provider": PROVIDER, "idempotencyKey": "k"}),
        ));
        assert_eq!(vetoed.as_deref(), Some("currency_mismatch"));
    }
}

#[test]
fn a_late_capture_of_an_earlier_attempt_is_voided_and_what_it_overpays_is_asked_back() {
    for target in targets("a_late_capture_of_an_earlier_attempt") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));
        checkout(&engine, "c-1", 2500);
        let first = open_payment(&engine, "c-1", "k1").unwrap()["attempt"].clone();
        record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "e1",
                "failed",
                2500,
                json!({"attempt": first, "providerRef": "pi_1"}),
            ),
        )
        .unwrap();
        assert_eq!(kinds(&engine, "Checkout", "c-1"), Vec::<String>::new());

        // The payer tries again: the new attempt's create, and a void of
        // the one that failed, whose payment the provider may still finish.
        let second = open_payment(&engine, "c-1", "k2").unwrap()["attempt"].clone();
        let waiting = actions(&engine, "Checkout");
        let by_kind: Vec<(String, Value, Value)> = waiting
            .iter()
            .map(|action| {
                (
                    action["kind"].as_str().unwrap().to_owned(),
                    action["attempt"].clone(),
                    action["amount"].clone(),
                )
            })
            .collect();
        assert_eq!(
            by_kind,
            vec![
                ("create".to_owned(), second.clone(), json!(2500)),
                ("void".to_owned(), first.clone(), json!(2500)),
            ]
        );
        // The second is processing at the provider.
        record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "e2",
                "processing",
                2500,
                json!({"attempt": second, "providerRef": "pi_2"}),
            ),
        )
        .unwrap();

        // The provider finishes the first payment anyway: it is counted,
        // and pays what is due.
        let late = record(
            &engine,
            "Checkout",
            "c-1",
            event("e3", "captured", 2500, json!({"attempt": first})),
        )
        .unwrap();
        assert_eq!(late["applied"], json!(true));
        assert_eq!(late["state"], json!("paid"));
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(
            (fields["captured"].clone(), fields["overpaid"].clone()),
            (json!(2500), json!(0))
        );
        assert_ledger(&engine, "Checkout", "c-1");

        // The second, processing when the first captured, captures too:
        // what it brings past what is due is overpaid, asked back with a
        // refund, and the checkout stays paid.
        let twice = record(
            &engine,
            "Checkout",
            "c-1",
            event("e4", "captured", 2500, json!({"attempt": second})),
        )
        .unwrap();
        assert_eq!(twice["state"], json!("paid"));
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(fields["captured"], json!(5000));
        assert_eq!(fields["overpaid"], json!(2500));
        assert_eq!(fields["outstanding"], json!(0));
        let refunds: Vec<Value> = actions(&engine, "Checkout")
            .into_iter()
            .filter(|action| action["kind"] == json!("refund"))
            .collect();
        assert_eq!(refunds.len(), 1);
        assert_eq!(refunds[0]["amount"], json!(2500));
        assert_ledger(&engine, "Checkout", "c-1");
        // A repeat of the event asks nothing again.
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e4", "captured", 2500, json!({"attempt": second})),
        )
        .unwrap();
        assert_eq!(
            kinds(&engine, "Checkout", "c-1")
                .iter()
                .filter(|kind| *kind == "refund")
                .count(),
            1
        );

        // The provider refunds the excess: the checkout is still paid.
        let done = record(
            &engine,
            "Checkout",
            "c-1",
            event(
                "e5",
                "refunded",
                2500,
                json!({"refund": refunds[0]["refund"]}),
            ),
        )
        .unwrap();
        assert_eq!(done["state"], json!("paid"));
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(
            (
                fields["refunded"].clone(),
                fields["overpaid"].clone(),
                fields["state"].clone()
            ),
            (json!(2500), json!(2500), json!("paid"))
        );
        assert_ledger(&engine, "Checkout", "c-1");

        // A staff refund cannot take the keys Payments asks itself under.
        let forged = invoke(
            &engine,
            &refunder(),
            "Checkout",
            "c-1",
            "refundPayment",
            json!({"amount": 1, "reason": "x", "idempotencyKey": "overpaid:e9"}),
        );
        assert_eq!(code(forged), EngineErrorCode::InvalidArgument);
    }
}

#[test]
fn a_capture_that_pays_what_is_due_voids_the_other_open_attempts() {
    for target in targets("a_capture_that_pays_what_is_due_voids") {
        let engine = open(&target, BehaviorSet::new());
        publish_checkout(&engine, payments_config(json!({})));
        checkout(&engine, "c-1", 2500);
        let first = open_payment(&engine, "c-1", "k1").unwrap()["attempt"].clone();
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e1", "failed", 2500, json!({"attempt": first})),
        )
        .unwrap();
        let second = open_payment(&engine, "c-1", "k2").unwrap()["attempt"].clone();
        // The first captures late while the second is still open: the
        // second is voided, and nothing is overpaid.
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e2", "captured", 2500, json!({"attempt": first})),
        )
        .unwrap();
        let voids: Vec<Value> = actions(&engine, "Checkout")
            .into_iter()
            .filter(|action| action["kind"] == json!("void"))
            .map(|action| action["attempt"].clone())
            .collect();
        assert_eq!(voids, vec![second.clone()]);
        record(
            &engine,
            "Checkout",
            "c-1",
            event("e3", "cancelled", 2500, json!({"attempt": second})),
        )
        .unwrap();
        let fields = payments(&engine, "Checkout", "c-1");
        assert_eq!(
            (fields["state"].clone(), fields["overpaid"].clone()),
            (json!("paid"), json!(0))
        );
        assert_eq!(kinds(&engine, "Checkout", "c-1"), Vec::<String>::new());
        assert_ledger(&engine, "Checkout", "c-1");
    }
}
