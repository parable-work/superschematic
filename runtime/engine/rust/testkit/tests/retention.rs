//! Event log retention (D16, amended; D58), the port of
//! `test/retention.test.ts`: the log keeps events by age, by count or both,
//! pruned a namespace at a time and never past an event a subscription has
//! yet to handle; a read from before a namespace's floor is
//! `cursor_expired`; a reaction's `before()` and an instance's sequence
//! carry on from what retention kept; a value only pruned events held
//! goes with them; and an event stream the started runner prunes past
//! ends, its resume answered 410 `cursor_expired`. On SQLite and, with the
//! variable set, on Postgres.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, Engine, EngineErrorCode, EngineErrorKind, EngineEvent,
    EngineOptions, Error, EventsAfter, NamespaceOptions, PruneResult, ReadEvents, RetentionOptions,
    RetentionStatus, RunnerOptions, RunnerPass, SubscriptionKey, SubscriptionState, ValueOptions,
};
use superschematic_engine_testkit::behaviors::{Ledger, Probe};
use support::{alice, code, publish, refusal, schema_document, targets, texts, usage, Target};

fn plain_document(name: &str) -> Value {
    schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    )
}

fn ledger_document(name: &str) -> Value {
    let mut document = plain_document(name);
    document["types"][name]["behaviors"] = json!([{"name": "test.Ledger"}]);
    document
}

fn runner() -> RunnerOptions {
    RunnerOptions::new(superschematic_engine::Principal::new(
        "runner",
        Vec::<String>::new(),
    ))
}

// An engine with test.Ledger, the runner, a 1 KiB value threshold and a
// manual clock, its options changed by `configure`.
fn open(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> (Engine, Probe) {
    let ledger = Ledger::default();
    let probe = ledger.probe.clone();
    let engine = target.open_with(BehaviorSet::new().with(ledger), |options| {
        options.clock = Clock::Manual(1_000_000);
        options.runner = Some(runner());
        options.values = ValueOptions {
            threshold_bytes: Some(1024),
            ..ValueOptions::default()
        };
        configure(options);
    });
    (engine, probe)
}

fn retention(options: RetentionOptions) -> impl FnOnce(&mut EngineOptions) {
    move |engine| engine.retention = Some(options)
}

fn count(n: i64) -> RetentionOptions {
    RetentionOptions {
        max_events: Some(n),
        ..RetentionOptions::default()
    }
}

fn create(engine: &Engine, schema: &str, id: &str, data: Value, namespace: Option<&str>) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                namespace,
                ..CreateOptions::default()
            },
        )
        .expect("create");
}

fn read(engine: &Engine, after: Option<i64>, namespace: Option<&str>) -> Vec<i64> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                namespace,
                after: after.map(EventsAfter::Cursor),
                ..ReadEvents::default()
            },
        )
        .expect("read")
        .events
        .iter()
        .map(|event| event.cursor)
        .collect()
}

fn floor(engine: &Engine, namespace: Option<&str>) -> i64 {
    engine.events().floor(namespace).expect("floor")
}

fn head(engine: &Engine) -> i64 {
    engine.events().head().expect("head")
}

fn prune(engine: &Engine) -> i64 {
    engine.runner().prune().expect("prune").pruned
}

fn status_of(engine: &Engine) -> RetentionStatus {
    engine
        .runner()
        .status()
        .expect("status")
        .retention
        .expect("the engine has retention")
}

fn payloads(engine: &Engine) -> Vec<String> {
    texts(
        engine,
        "SELECT hash FROM engine_payloads ORDER BY hash",
        &[],
    )
}

fn key() -> SubscriptionKey {
    SubscriptionKey {
        behavior: "test.Ledger".into(),
        namespace: "default".into(),
        schema: "Order".into(),
    }
}

#[test]
fn options_need_an_age_or_a_count_and_numbers_in_range() {
    for target in targets("retention_options") {
        for (options, message) in [
            (json!({}), "give at least one"),
            (json!({"everyMs": 5_000}), "give at least one"),
            (
                json!({"maxAgeMs": 0}),
                "retention.maxAgeMs is an integer from 1",
            ),
            (
                json!({"maxEvents": 1.5}),
                "retention.maxEvents is an integer from 1",
            ),
            (
                json!({"maxEvents": 1, "everyMs": 999}),
                "retention.everyMs is an integer from 1000",
            ),
            (
                json!({"maxEvents": 1, "batchSize": 10_001}),
                "retention.batchSize is an integer from 1 to 10000",
            ),
            (
                json!({"maxEvents": 1, "maxHoldMs": 0}),
                "retention.maxHoldMs is an integer from 1",
            ),
            (json!("all"), "retention is { maxAgeMs?"),
        ] {
            let refused = RetentionOptions::from_json(&options).and_then(|options| {
                let mut engine = target.options(BehaviorSet::new());
                engine.retention = Some(options);
                Engine::open(engine).map(|_| ())
            });
            let message_got = usage(refused);
            assert!(message_got.contains(message), "{options}: {message_got}");
        }
        let (engine, _) = open(&target, |_| {});
        assert!(usage(engine.runner().prune()).starts_with("runner.prune needs retention"));
        assert_eq!(engine.runner().status().unwrap().retention, None);
        assert_eq!(engine.runner().run_due().unwrap(), RunnerPass::default());
    }
}

#[test]
fn by_count_the_newest_events_stay_and_a_read_from_before_the_floor_is_cursor_expired() {
    for target in targets("retention_by_count") {
        let (engine, _) = open(&target, retention(count(3)));
        publish(&engine, &plain_document("Note"), None);
        for id in ["n1", "n2", "n3", "n4"] {
            create(&engine, "Note", id, json!({"title": id}), None);
        }
        assert_eq!(read(&engine, Some(0), None), vec![1, 2, 3, 4, 5, 6]);
        assert_eq!(engine.runner().prune().unwrap(), PruneResult { pruned: 3 });
        assert_eq!((floor(&engine, None), head(&engine)), (3, 6));
        assert_eq!(read(&engine, Some(3), None), vec![4, 5, 6]);
        for after in [1, 2] {
            let error = refusal(engine.events().read(
                &alice(),
                &ReadEvents {
                    after: Some(EventsAfter::Cursor(after)),
                    ..ReadEvents::default()
                },
            ));
            assert_eq!(error.code(), EngineErrorCode::CursorExpired);
            assert!(matches!(
                error.kind(),
                EngineErrorKind::CursorExpired { after: a, floor: 3, head: 6 } if *a == after
            ));
        }
        assert_eq!(
            code(engine.events().read(
                &alice(),
                &ReadEvents {
                    schema: Some("Note"),
                    instance_id: Some("n1"),
                    after: Some(EventsAfter::Cursor(1)),
                    ..ReadEvents::default()
                }
            )),
            EngineErrorCode::CursorExpired
        );
        // A read from the start reads what the log holds, from the floor.
        assert_eq!(read(&engine, None, None), read(&engine, Some(3), None));
        assert_eq!(read(&engine, Some(0), None), read(&engine, Some(3), None));
        // Nothing an instance read returns changed, and the log goes on.
        assert_eq!(
            engine
                .instances()
                .update(
                    &alice(),
                    "Note",
                    "n1",
                    &json!({"title": "first"}),
                    Default::default()
                )
                .unwrap()
                .seq,
            2
        );
        assert_eq!(read(&engine, Some(3), None), vec![4, 5, 6, 7]);
        assert_eq!(prune(&engine), 1);
        assert_eq!(
            serde_json::to_value(status_of(&engine).namespaces).unwrap(),
            json!([{"namespace": "default", "floor": 4, "pruned": 4, "heldAt": null, "heldBy": null, "heldState": null, "heldSince": null, "heldUntil": null}])
        );
        assert_eq!(prune(&engine), 0);
    }
}

#[test]
fn by_age_an_old_event_goes_and_the_first_young_one_stops_the_floor_and_with_a_count_either_lets_go(
) {
    for target in targets("retention_by_age") {
        let (engine, _) = open(&target, |options| {
            options.clock = Clock::Manual(10_000);
            options.retention = Some(RetentionOptions {
                max_age_ms: Some(1_000),
                ..RetentionOptions::default()
            });
        });
        publish(&engine, &plain_document("Note"), None);
        engine.clock().set(10_500).unwrap();
        create(&engine, "Note", "n1", json!({"title": "n1"}), None);
        engine.clock().set(10_900).unwrap();
        assert_eq!(prune(&engine), 0);
        engine.clock().set(11_200).unwrap();
        assert_eq!(prune(&engine), 2, "the define and the publish, at 10000");
        assert_eq!(read(&engine, Some(2), None), vec![3]);
        engine.clock().set(11_600).unwrap();
        assert_eq!(prune(&engine), 1);
        assert_eq!((floor(&engine, None), head(&engine)), (3, 3));
        assert!(read(&engine, Some(3), None).is_empty());
        assert_eq!(
            engine
                .instances()
                .create(
                    &alice(),
                    "Note",
                    &json!({"title": "n2"}),
                    CreateOptions {
                        id: Some("n2"),
                        ..Default::default()
                    }
                )
                .unwrap()
                .seq,
            1
        );
        assert_eq!(read(&engine, Some(3), None), vec![4]);
    }
    for target in targets("retention_by_age_and_count") {
        let (engine, _) = open(&target, |options| {
            options.retention = Some(RetentionOptions {
                max_age_ms: Some(1_000),
                max_events: Some(2),
                ..RetentionOptions::default()
            });
        });
        publish(&engine, &plain_document("Note"), None);
        for id in ["n1", "n2"] {
            create(&engine, "Note", id, json!({"title": id}), None);
        }
        assert_eq!(prune(&engine), 2, "the count lets the oldest two go");
        engine.clock().advance(1_001).unwrap();
        assert_eq!(prune(&engine), 2, "the age lets the rest go");
    }
}

#[test]
fn never_past_an_event_a_subscription_has_yet_to_handle_in_its_namespace_alone() {
    for target in targets("retention_holds") {
        let (engine, probe) = open(&target, |options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into()],
                shared: None,
            };
            options.retention = Some(count(1));
            options.runner = Some(RunnerOptions {
                max_attempts: Some(1),
                ..runner()
            });
        });
        publish(&engine, &ledger_document("Order"), None);
        publish(&engine, &plain_document("Note"), Some("east"));
        let published = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    kinds: Some(vec!["publish".into()]),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events[0]
            .cursor;
        for id in ["o1", "o2"] {
            create(&engine, "Order", id, json!({"title": id}), None);
            create(&engine, "Note", id, json!({"title": id}), Some("east"));
        }
        // The runner has not run: the subscription holds default where it
        // starts, after the publish; east has none.
        assert_eq!(prune(&engine), 5);
        assert_eq!(floor(&engine, None), published);
        assert_eq!(
            read(&engine, Some(floor(&engine, Some("east"))), Some("east")),
            vec![head(&engine)]
        );
        let held = status_of(&engine)
            .namespaces
            .into_iter()
            .find(|entry| entry.namespace == "default")
            .unwrap();
        assert_eq!((held.held_at, held.held_by), (Some(published), Some(key())));
        // A reaction that fails halts the subscription at the event: the
        // event stays, and so does everything after it.
        probe.on_react(|_, _| Err(Error::behavior("test.Ledger", "not yet")));
        engine.runner().run_due().unwrap();
        let halted = engine.runner().status().unwrap().subscriptions[0].clone();
        assert_eq!(halted.state, SubscriptionState::Halted);
        create(&engine, "Order", "o3", json!({"title": "o3"}), None);
        prune(&engine);
        assert_eq!(floor(&engine, None), published);
        let failed = halted.failure.clone().unwrap().cursor.unwrap();
        assert!(read(&engine, Some(published), None).contains(&failed));
        // Resumed and caught up, it lets retention go on.
        probe.on_react(|cx, event| {
            if event.cause.is_none() {
                cx.instances().invoke(
                    &event.schema,
                    event.instance_id.as_deref().unwrap_or_default(),
                    "mark",
                    &json!({"note": format!("{} {}", event.kind, event.cursor)}),
                    None,
                )?;
            }
            Ok(())
        });
        engine.runner().resume(&key(), false).unwrap();
        engine.runner().run_due().unwrap();
        let caught = engine.runner().status().unwrap().subscriptions[0].clone();
        assert_eq!(
            (caught.state, caught.cursor),
            (SubscriptionState::Active, head(&engine))
        );
        prune(&engine);
        assert_eq!(floor(&engine, None), head(&engine) - 1);
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Order", "o1", Default::default())
                .unwrap()
                .unwrap()
                .behaviors["test.Ledger"]["notes"],
            json!([format!("create {failed}")])
        );
    }
}

#[test]
fn by_count_each_namespace_keeps_its_own_newest_events() {
    for target in targets("retention_per_namespace") {
        let (engine, _) = open(&target, |options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into()],
                shared: None,
            };
            options.retention = Some(count(3));
        });
        publish(&engine, &plain_document("Note"), None);
        publish(&engine, &plain_document("Note"), Some("east"));
        create(
            &engine,
            "Note",
            "q1",
            json!({"title": "quiet"}),
            Some("east"),
        );
        for at in 1..=10 {
            let id = format!("n{at}");
            create(&engine, "Note", &id, json!({"title": id}), None);
        }
        assert_eq!(prune(&engine), 9);
        assert_eq!(read(&engine, Some(floor(&engine, None)), None).len(), 3);
        assert_eq!(floor(&engine, Some("east")), 0);
        assert_eq!(read(&engine, Some(0), Some("east")).len(), 3);
        create(
            &engine,
            "Note",
            "q2",
            json!({"title": "quiet again"}),
            Some("east"),
        );
        create(&engine, "Note", "n11", json!({"title": "n11"}), None);
        assert_eq!(prune(&engine), 2);
        assert_eq!(
            read(&engine, Some(floor(&engine, Some("east"))), Some("east")).len(),
            3
        );
        assert_eq!(read(&engine, Some(floor(&engine, None)), None).len(), 3);
    }
}

#[test]
fn max_hold_bounds_a_halted_subscription_and_past_it_the_subscription_halts_behind_its_floor() {
    for target in targets("retention_max_hold") {
        let (engine, probe) = open(&target, |options| {
            options.retention = Some(RetentionOptions {
                max_events: Some(1),
                max_hold_ms: Some(60_000),
                ..RetentionOptions::default()
            });
            options.runner = Some(RunnerOptions {
                max_attempts: Some(1),
                ..runner()
            });
        });
        publish(&engine, &ledger_document("Order"), None);
        probe.on_react(|_, _| Err(Error::behavior("test.Ledger", "not yet")));
        create(&engine, "Order", "o1", json!({"title": "o1"}), None);
        engine.runner().run_due().unwrap();
        let halted = engine.runner().status().unwrap().subscriptions[0].clone();
        assert_eq!(halted.state, SubscriptionState::Halted);
        engine.clock().advance(10_000).unwrap();
        create(&engine, "Order", "o2", json!({"title": "o2"}), None);
        prune(&engine);
        let held = status_of(&engine).namespaces[0].clone();
        assert_eq!(
            serde_json::to_value((
                held.held_at,
                &held.held_by,
                held.held_state,
                held.held_since,
                held.held_until
            ))
            .unwrap(),
            json!([halted.cursor, {"behavior": "test.Ledger", "namespace": "default", "schema": "Order"}, "halted", 1_000_000, 1_060_000])
        );
        assert_eq!(floor(&engine, None), halted.cursor);
        assert_eq!(status_of(&engine).max_hold_ms, Some(60_000));
        engine.clock().set(1_060_000).unwrap();
        prune(&engine);
        assert_eq!(floor(&engine, None), halted.cursor, "held up to heldUntil");
        engine.clock().set(1_060_001).unwrap();
        prune(&engine);
        let failed = halted.failure.clone().unwrap().cursor.unwrap();
        assert_eq!(floor(&engine, None), failed);
        // Resumed, it finds itself behind the floor and halts with
        // cursor_expired; resume with skip moves it to the floor.
        probe.on_react(|_, _| Ok(()));
        engine.runner().resume(&key(), false).unwrap();
        engine.runner().run_due().unwrap();
        let expired = engine.runner().status().unwrap().subscriptions[0].clone();
        assert_eq!(expired.state, SubscriptionState::Halted);
        let failure = expired.failure.unwrap();
        assert_eq!(failure.cursor, Some(floor(&engine, None)));
        assert!(
            failure.error.starts_with("cursor_expired"),
            "{}",
            failure.error
        );
        engine.runner().resume(&key(), true).unwrap();
        engine.runner().run_due().unwrap();
        let caught = engine.runner().status().unwrap().subscriptions[0].clone();
        assert_eq!(
            (caught.state, caught.cursor),
            (SubscriptionState::Active, head(&engine))
        );
    }
}

#[test]
fn max_hold_bounds_a_subscription_in_an_archived_namespace_and_a_running_one_holds_whatever_its_age(
) {
    for target in targets("retention_archived_hold") {
        let (engine, _) = open(&target, |options| {
            options.retention = Some(RetentionOptions {
                max_events: Some(1),
                max_hold_ms: Some(1_000),
                ..RetentionOptions::default()
            });
        });
        engine.namespaces().create(&alice(), "acme").unwrap();
        publish(&engine, &ledger_document("Order"), Some("acme"));
        create(&engine, "Order", "o1", json!({"title": "o1"}), Some("acme"));
        create(&engine, "Order", "o2", json!({"title": "o2"}), Some("acme"));
        engine.clock().advance(10_000).unwrap();
        prune(&engine);
        let start = floor(&engine, Some("acme"));
        let acme = |engine: &Engine| {
            status_of(engine)
                .namespaces
                .into_iter()
                .find(|entry| entry.namespace == "acme")
                .unwrap()
        };
        assert_eq!(acme(&engine).held_state, Some(SubscriptionState::Active));
        assert_eq!(acme(&engine).held_until, None);
        assert_eq!(read(&engine, Some(start), Some("acme")).len(), 2);
        engine.namespaces().archive(&alice(), "acme").unwrap();
        assert_eq!(acme(&engine).held_state, Some(SubscriptionState::Archived));
        assert!(acme(&engine).held_until.is_some());
        prune(&engine);
        assert_eq!(
            read(&engine, Some(floor(&engine, Some("acme"))), Some("acme")).len(),
            1
        );
    }
}

#[test]
fn a_reactions_before_reads_the_instance_though_retention_pruned_its_create() {
    for target in targets("retention_before") {
        let (engine, probe) = open(&target, retention(count(1)));
        publish(&engine, &ledger_document("Order"), None);
        create(&engine, "Order", "o1", json!({"title": "Desk"}), None);
        let created: Arc<Mutex<Option<EngineEvent>>> = Arc::default();
        let into = Arc::clone(&created);
        probe.on_react(move |_, event| {
            into.lock().unwrap().get_or_insert_with(|| event.clone());
            Ok(())
        });
        engine.runner().run_due().unwrap();
        for title in ["Lamp", "Chair"] {
            engine
                .instances()
                .update(
                    &alice(),
                    "Order",
                    "o1",
                    &json!({"title": title}),
                    Default::default(),
                )
                .unwrap();
        }
        prune(&engine);
        let first = created.lock().unwrap().clone().unwrap();
        assert_eq!(floor(&engine, None), first.cursor);
        let seen: Arc<Mutex<Vec<Value>>> = Arc::default();
        let expired: Arc<Mutex<Vec<String>>> = Arc::default();
        let (into, refused) = (Arc::clone(&seen), Arc::clone(&expired));
        probe.on_react(move |cx, event| {
            if event.instance_id.as_deref() != Some("o1") {
                return Ok(());
            }
            into.lock()
                .unwrap()
                .push(serde_json::to_value(cx.before(event)?).unwrap());
            if let Err(Error::Engine(error)) = cx.before(&first) {
                refused
                    .lock()
                    .unwrap()
                    .push(error.code().as_str().to_owned());
            }
            Ok(())
        });
        engine.runner().run_due().unwrap();
        assert_eq!(
            *seen.lock().unwrap(),
            vec![
                json!({"data": {"title": "Desk"}, "behaviors": {"test.Ledger": {}}}),
                json!({"data": {"title": "Lamp"}, "behaviors": {"test.Ledger": {}}}),
            ]
        );
        assert_eq!(
            *expired.lock().unwrap(),
            vec!["cursor_expired", "cursor_expired"]
        );
        // With every event of it pruned, a reaction to its next one still
        // reads it as it was.
        create(&engine, "Order", "p1", json!({"title": "Other"}), None);
        engine.runner().run_due().unwrap();
        prune(&engine);
        seen.lock().unwrap().clear();
        engine
            .instances()
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": "Stool"}),
                Default::default(),
            )
            .unwrap();
        engine.runner().run_due().unwrap();
        assert_eq!(
            *seen.lock().unwrap(),
            vec![json!({"data": {"title": "Chair"}, "behaviors": {"test.Ledger": {}}})]
        );
    }
}

#[test]
fn a_create_after_a_delete_goes_on_from_the_sequence_retention_kept() {
    for target in targets("retention_sequence") {
        let (engine, _) = open(&target, retention(count(1)));
        publish(&engine, &plain_document("Note"), None);
        create(&engine, "Note", "n1", json!({"title": "first"}), None);
        engine
            .instances()
            .update(
                &alice(),
                "Note",
                "n1",
                &json!({"title": "second"}),
                Default::default(),
            )
            .unwrap();
        engine
            .instances()
            .delete(&alice(), "Note", "n1", Default::default())
            .unwrap();
        create(&engine, "Note", "n2", json!({"title": "other"}), None);
        prune(&engine);
        assert_eq!(
            read(&engine, Some(floor(&engine, None)), None),
            vec![head(&engine)]
        );
        let again = engine
            .instances()
            .create(
                &alice(),
                "Note",
                &json!({"title": "again"}),
                CreateOptions {
                    id: Some("n1"),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(again.seq, 4);
    }
}

#[test]
fn a_value_only_pruned_events_held_goes_with_them_and_one_an_instance_or_its_base_holds_stays() {
    for target in targets("retention_values") {
        let (engine, _) = open(&target, retention(count(1)));
        publish(
            &engine,
            &schema_document(
                "Doc",
                json!([
                    {"name": "title", "typeRef": {"name": "string"}, "required": true},
                    {"name": "body", "typeRef": {"name": "string"}}
                ]),
                json!({}),
            ),
            None,
        );
        let (x, y) = ("x".repeat(2_000), "y".repeat(2_000));
        create(
            &engine,
            "Doc",
            "d1",
            json!({"title": "d1", "body": x}),
            None,
        );
        engine
            .instances()
            .update(
                &alice(),
                "Doc",
                "d1",
                &json!({"body": y}),
                Default::default(),
            )
            .unwrap();
        assert_eq!(payloads(&engine).len(), 2);
        create(&engine, "Doc", "d2", json!({"title": "d2"}), None);
        prune(&engine);
        assert_eq!(payloads(&engine).len(), 1);
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Doc", "d1", Default::default())
                .unwrap()
                .unwrap()
                .data["body"],
            json!(y)
        );
        assert_eq!(
            texts(
                &engine,
                "SELECT holder FROM engine_payload_holders ORDER BY holder",
                &[]
            ),
            vec!["base", "instance"]
        );
        engine
            .instances()
            .delete(&alice(), "Doc", "d1", Default::default())
            .unwrap();
        assert_eq!(payloads(&engine).len(), 1);
        create(&engine, "Doc", "d3", json!({"title": "d3"}), None);
        prune(&engine);
        assert!(payloads(&engine).is_empty());
        assert!(texts(&engine, "SELECT holder FROM engine_payload_holders", &[]).is_empty());
    }
}

#[test]
fn the_runner_prunes_at_its_first_pass_and_every_every_ms_after_a_batch_at_a_time() {
    for target in targets("retention_on_the_runner") {
        let (engine, _) = open(&target, |options| {
            options.clock = Clock::Manual(50_000);
            options.retention = Some(RetentionOptions {
                max_events: Some(1),
                every_ms: Some(10_000),
                batch_size: Some(2),
                ..RetentionOptions::default()
            });
        });
        publish(&engine, &plain_document("Note"), None);
        for id in ["n1", "n2", "n3"] {
            create(&engine, "Note", id, json!({"title": id}), None);
        }
        assert_eq!(
            (status_of(&engine).previous, status_of(&engine).next),
            (None, None)
        );
        assert_eq!(
            engine.runner().run_due().unwrap(),
            RunnerPass {
                pruned: Some(4),
                ..RunnerPass::default()
            }
        );
        assert_eq!(floor(&engine, None), 4);
        assert_eq!(
            (status_of(&engine).previous, status_of(&engine).next),
            (Some(50_000), Some(60_000))
        );
        create(&engine, "Note", "n4", json!({"title": "n4"}), None);
        assert_eq!(
            engine.runner().run_due().unwrap().pruned,
            Some(0),
            "not due"
        );
        assert_eq!(engine.runner().next_wake(), Some(60_000));
        engine.clock().set(60_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap().pruned, Some(1));
        assert_eq!(floor(&engine, None), 5);
    }
}

#[test]
fn the_started_runner_prunes() {
    for target in targets("retention_started") {
        let (engine, _) = open(&target, |options| {
            options.clock = Clock::System;
            options.retention = Some(RetentionOptions {
                max_events: Some(1),
                batch_size: Some(1),
                ..RetentionOptions::default()
            });
        });
        let engine = engine.shared();
        publish(&engine, &plain_document("Note"), None);
        create(&engine, "Note", "n1", json!({"title": "n1"}), None);
        engine.runner().start().unwrap();
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(20);
        while floor(&engine, None) < 2 {
            assert!(
                std::time::Instant::now() < deadline,
                "the runner did not prune"
            );
            std::thread::sleep(std::time::Duration::from_millis(5));
        }
        engine.runner().stop();
        assert_eq!(floor(&engine, None), 2);
    }
}

#[test]
fn a_stream_the_started_runner_prunes_past_ends_and_its_resume_is_410_cursor_expired() {
    use std::time::{Duration, Instant};
    use superschematic_engine::api::{
        handle, start_stream, ApiOptions, Caller, Pull, Request, StreamOptions,
    };
    for target in targets("retention_stream") {
        let (engine, _) = open(&target, |options| {
            options.clock = Clock::System;
            options.retention = Some(count(2));
        });
        let engine = engine.shared();
        publish(&engine, &plain_document("Note"), None);
        // Caught up at the head, its client then reads nothing while the
        // log grows and the runner prunes.
        let mut stream = start_stream(
            &engine,
            &StreamOptions {
                page_size: Some(1),
                heartbeat_ms: Some(60_000),
            },
            &alice(),
            &ReadEvents {
                after: Some(EventsAfter::Head),
                ..ReadEvents::default()
            },
        )
        .unwrap();
        let ready = head(&engine);
        for id in ["n1", "n2", "n3", "n4"] {
            create(&engine, "Note", id, json!({"title": id}), None);
        }
        engine.runner().start().unwrap();
        let deadline = Instant::now() + Duration::from_secs(20);
        while floor(&engine, None) <= ready {
            assert!(Instant::now() < deadline, "the runner did not prune");
            std::thread::sleep(Duration::from_millis(5));
        }
        engine.runner().stop();
        let (floor_at, head_at) = (floor(&engine, None), head(&engine));
        assert_eq!(floor_at, head_at - 2, "the newest two are kept");
        loop {
            match stream.next(Duration::from_millis(100)) {
                Pull::End => break,
                Pull::Data(bytes) => {
                    let text = String::from_utf8(bytes).unwrap();
                    assert!(
                        text.starts_with(": open") || text.starts_with("event: ready"),
                        "no event the runner pruned is sent: {text}"
                    );
                }
                Pull::Pending => {}
            }
            assert!(Instant::now() < deadline, "the stream did not end");
        }
        let resumed = handle(
            &engine,
            &ApiOptions::default(),
            &Request {
                method: "GET".to_owned(),
                path: "/namespaces/default/events".to_owned(),
                headers: vec![
                    ("accept".to_owned(), "text/event-stream".to_owned()),
                    ("last-event-id".to_owned(), ready.to_string()),
                ],
                caller: Caller {
                    principal: Some(alice()),
                    ..Caller::default()
                },
                request_id: "r1".to_owned(),
                ..Request::default()
            },
        );
        assert_eq!(resumed.status, 410);
        let problem: Value = serde_json::from_slice(resumed.bytes()).unwrap();
        assert_eq!(problem["code"], "cursor_expired", "{problem}");
        assert_eq!(
            problem["details"],
            json!({"after": ready, "floor": floor_at, "head": head_at})
        );
    }
}

#[test]
fn a_subscription_behind_its_floor_halts_with_cursor_expired_and_resume_with_skip_moves_it() {
    for target in targets("retention_behind_the_floor") {
        // A storage whose schema composes test.Ledger, pruned by an engine
        // that does not have it, where no subscription has run.
        {
            let (first, _) = open(&target, |_| {});
            publish(&first, &ledger_document("Order"), None);
            publish(&first, &plain_document("Note"), None);
            create(&first, "Note", "n1", json!({"title": "n1"}), None);
        }
        let floor_at = {
            let without = target.open_with(BehaviorSet::new(), |options| {
                options.runner = Some(runner());
                options.retention = Some(count(1));
            });
            without.runner().prune().unwrap();
            floor(&without, None)
        };
        let (engine, probe) = open(&target, |_| {});
        create(&engine, "Order", "o1", json!({"title": "o1"}), None);
        let seen: Arc<Mutex<Vec<i64>>> = Arc::default();
        let into = Arc::clone(&seen);
        probe.on_react(move |_, event| {
            into.lock().unwrap().push(event.cursor);
            Ok(())
        });
        assert_eq!(engine.runner().run_due().unwrap().failed, 1);
        let halted = engine.runner().status().unwrap().subscriptions[0].clone();
        assert_eq!(halted.state, SubscriptionState::Halted);
        let failure = halted.failure.unwrap();
        assert_eq!(failure.cursor, Some(floor_at));
        assert!(failure.error.starts_with("cursor_expired: "));
        engine.runner().resume(&key(), true).unwrap();
        engine.runner().run_due().unwrap();
        assert_eq!(*seen.lock().unwrap(), vec![head(&engine)]);
        assert_eq!(
            serde_json::to_value(&engine.runner().status().unwrap().subscriptions[0].last_skip)
                .unwrap(),
            json!({"cursor": floor_at, "reason": "resume"})
        );
    }
}
