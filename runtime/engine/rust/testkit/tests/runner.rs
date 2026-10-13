//! The runner (D16, amended; D58), the ports of `test/runner.test.ts` and
//! `test/runner-schedules.test.ts`: reactions run after the commit, from the
//! log, as the runner's principal, one subscription's events in log order,
//! with a cursor that commits with the reaction's writes, retries, a halt,
//! a depth limit, and schedules on the engine's clock; on SQLite and, with
//! the variable set, on Postgres, where two engines on one database prove
//! the runner's lease: one runs, the other takes over once it stops.
//!
//! What the TypeScript tests prove of JavaScript itself (a reaction that
//! throws a non-Error, a hook that returns a promise, a child process
//! killed mid-reaction) has no Rust counterpart; a reaction's failure here
//! is an [`Error`], named in the status as the runner's `describe` names it.

mod support;

use std::sync::{Arc, Mutex, OnceLock, Weak};
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    AccessRequest, BehaviorSet, Clock, Engine, EngineErrorCode, EngineEvent, Error, Policy,
    Principal, ReadEvents, RunnerOptions, RunnerPass, ScheduleState, SubscriptionKey,
    SubscriptionState, SubscriptionStatus,
};
use superschematic_engine_testkit::behaviors::{Ledger, LedgerConfig, Pacer, Probe};
use support::{
    alice, code, publish, schema_document, targets, texts, usage, RecordingPolicy, Target,
};

// One schedule run: its name, the run before it and the time.
type Run = (String, Option<i64>, i64);

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

/// A schema of one string field whose instance type composes test.Ledger
/// with a config.
fn ledger_document(name: &str, config: Value) -> Value {
    let mut document = plain_document(name);
    document["types"][name]["behaviors"] = if config == json!({}) {
        json!([{"name": "test.Ledger"}])
    } else {
        json!([{"name": "test.Ledger", "config": config}])
    };
    document
}

/// A schema of one string field whose instance type composes test.Pacer.
fn pacer_document(name: &str, config: Value) -> Value {
    let mut document = plain_document(name);
    document["types"][name]["behaviors"] = json!([{"name": "test.Pacer", "config": config}]);
    document
}

fn plain_document(name: &str) -> Value {
    schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    )
}

// An engine with test.Ledger and its probe, a manual clock and the runner
// principal, its options changed by `configure`.
fn open_runner(
    target: &Target,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> (Engine, Probe) {
    let ledger = Ledger::default();
    let probe = ledger.probe.clone();
    let engine = target.open_with(BehaviorSet::new().with(ledger), |options| {
        options.clock = Clock::Manual(1_000_000);
        options.runner = Some(RunnerOptions::new(runner()));
        configure(options);
    });
    (engine, probe)
}

fn pass(handled: i64, skipped: i64, failed: i64, scheduled: i64) -> RunnerPass {
    RunnerPass {
        handled,
        skipped,
        failed,
        scheduled,
        pruned: None,
    }
}

fn create(engine: &Engine, schema: &str, id: &str, title: &str) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &json!({ "title": title }),
            superschematic_engine::CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

fn notes(engine: &Engine, schema: &str, id: &str) -> Option<Value> {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
        .and_then(|record| record.behaviors.get("test.Ledger").cloned())
        .and_then(|fields| fields.get("notes").cloned())
}

fn note_count(engine: &Engine, schema: &str, id: &str) -> usize {
    notes(engine, schema, id)
        .and_then(|notes| notes.as_array().map(Vec::len))
        .unwrap_or(0)
}

fn history(engine: &Engine, schema: &str, id: &str) -> Vec<EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some(schema),
                instance_id: Some(id),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

fn subscription(engine: &Engine, schema: &str) -> SubscriptionStatus {
    engine
        .runner()
        .status()
        .expect("status")
        .subscriptions
        .into_iter()
        .find(|status| status.schema == schema)
        .unwrap_or_else(|| panic!("no subscription on {schema}"))
}

fn key(schema: &str) -> SubscriptionKey {
    SubscriptionKey {
        behavior: "test.Ledger".into(),
        namespace: "default".into(),
        schema: schema.into(),
    }
}

// mark writes a note of the event on the instance it names.
fn mark(
    cx: &superschematic_engine::behaviors::ReactionCx<'_, LedgerConfig>,
    event: &EngineEvent,
) -> superschematic_engine::Result<()> {
    cx.instances().invoke(
        &event.schema,
        event.instance_id.as_deref().unwrap_or_default(),
        "mark",
        &json!({ "note": format!("{} {}", event.kind, event.cursor) }),
        None,
    )?;
    Ok(())
}

// mark_callers is a reaction that writes a note for each caller's change
// and ignores what the runner's work wrote.
fn mark_callers(probe: &Probe) {
    probe.on_react(|cx, event| {
        if event.cause.is_none() {
            mark(cx, event)?;
        }
        Ok(())
    });
}

fn advance(engine: &Engine, ms: i64) {
    engine.clock().advance(ms).expect("a manual clock");
}

fn now(engine: &Engine) -> i64 {
    engine.clock().now()
}

fn wait_for(what: &str, mut done: impl FnMut() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(20);
    while !done() {
        assert!(Instant::now() < deadline, "{what}: still not done");
        std::thread::sleep(Duration::from_millis(5));
    }
}

#[test]
fn a_reaction_runs_after_the_commit_as_the_runner_and_the_event_it_writes_records_its_cause() {
    for target in targets("a_reaction_runs_after_the_commit") {
        let (engine, probe) = open_runner(&target, |_| {});
        publish(&engine, &ledger_document("Order", json!({})), None);
        let seen: Arc<Mutex<Vec<(i64, String, String)>>> = Arc::default();
        let into = Arc::clone(&seen);
        probe.on_react(move |cx, event| {
            into.lock().unwrap().push((
                event.cursor,
                cx.principal().subject.clone(),
                cx.schema().to_owned(),
            ));
            if event.cause.is_none() {
                mark(cx, event)?;
            }
            Ok(())
        });
        create(&engine, "Order", "o1", "Desk");
        assert!(seen.lock().unwrap().is_empty());
        assert_eq!(engine.runner().run_due().unwrap(), pass(2, 0, 0, 0));
        let events = history(&engine, "Order", "o1");
        let (created, marked) = (&events[0], &events[1]);
        assert_eq!(
            events
                .iter()
                .map(|event| (
                    event.kind.as_str(),
                    event.actor.as_str(),
                    serde_json::to_value(&event.cause).unwrap()
                ))
                .collect::<Vec<_>>(),
            vec![
                ("create", "alice", Value::Null),
                (
                    "operation",
                    "runner",
                    json!({"behavior": "test.Ledger", "event": created.cursor, "depth": 1})
                ),
            ]
        );
        assert_eq!(
            notes(&engine, "Order", "o1"),
            Some(json!([format!("create {}", created.cursor)]))
        );
        assert_eq!(
            *seen.lock().unwrap(),
            vec![
                (created.cursor, "runner".to_owned(), "Order".to_owned()),
                (marked.cursor, "runner".to_owned(), "Order".to_owned()),
            ]
        );
        assert_eq!(
            serde_json::to_value(subscription(&engine, "Order")).unwrap(),
            json!({
                "behavior": "test.Ledger", "namespace": "default", "schema": "Order", "state": "active",
                "cursor": marked.cursor, "attempts": 0, "retryAt": null, "failure": null, "skipped": 0, "lastSkip": null
            })
        );
        assert_eq!(engine.runner().status().unwrap().head, marked.cursor);
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 0, 0));
    }
}

#[test]
fn started_the_runner_wakes_on_a_commit_and_stops() {
    for target in targets("started_the_runner_wakes_on_a_commit") {
        let (engine, probe) = open_runner(&target, |_| {});
        let engine = engine.shared();
        publish(&engine, &ledger_document("Order", json!({})), None);
        mark_callers(&probe);
        engine.runner().start().unwrap();
        engine.runner().start().unwrap();
        assert!(engine.runner().running());
        create(&engine, "Order", "o1", "Desk");
        wait_for("o1's note", || notes(&engine, "Order", "o1").is_some());
        assert_eq!(note_count(&engine, "Order", "o1"), 1);
        engine.runner().stop();
        assert!(!engine.runner().running());
        create(&engine, "Order", "o2", "Lamp");
        std::thread::sleep(Duration::from_millis(100));
        assert_eq!(notes(&engine, "Order", "o2"), None);
        // Started again, it resumes from its cursor.
        engine.runner().start().unwrap();
        wait_for("o2's note", || notes(&engine, "Order", "o2").is_some());
        engine.shutdown();
        assert!(!engine.runner().running());
    }
}

#[test]
fn start_needs_a_shared_engine_and_a_principal_and_bad_options_refuse_to_open() {
    for target in targets("start_needs_a_shared_engine") {
        let (engine, _probe) = open_runner(&target, |_| {});
        assert!(usage(engine.runner().start()).contains("needs a shared engine"));
        let plain = target.open_with(BehaviorSet::new(), |_| {});
        assert!(usage(plain.runner().start())
            .starts_with("runner.start needs a principal: open the engine with runner"));
        assert!(usage(plain.runner().run_due()).starts_with("runner.runDue needs a principal"));
        assert_eq!(
            serde_json::to_value(plain.runner().status().unwrap()).unwrap(),
            json!({"running": false, "principal": null, "head": 0, "subscriptions": [], "schedules": [], "error": null})
        );
        drop(plain);
        for (options, message) in [
            (
                json!({"principal": {"subject": "", "permissions": []}}),
                "runner.principal is the principal reactions and schedules act as: { subject, permissions }",
            ),
            (
                json!({"principal": {"subject": "runner"}}),
                "runner.principal.permissions is a list of permissions",
            ),
            (
                json!({"principal": {"subject": "runner", "permissions": []}, "maxDepth": 0}),
                "runner.maxDepth is an integer from 1 to 1000, got 0",
            ),
            (
                json!({"principal": {"subject": "runner", "permissions": []}, "batchSize": 501}),
                "runner.batchSize is an integer from 1 to 500, got 501",
            ),
            (
                json!({"principal": {"subject": "runner", "permissions": []}, "retryInitialMs": 10, "retryMaxMs": 5}),
                "runner.retryMaxMs (5) is less than runner.retryInitialMs (10)",
            ),
        ] {
            let refused = RunnerOptions::from_json(&options).and_then(|options| {
                Engine::open({
                    let mut engine = target.options(BehaviorSet::new());
                    engine.runner = Some(options);
                    engine
                })
                .map(|_| ())
            });
            assert_eq!(usage(refused), message, "{options}");
        }
    }
}

#[test]
fn a_write_that_rolls_back_leaves_no_event_so_nothing_reacts() {
    for target in targets("a_write_that_rolls_back") {
        let (engine, probe) = open_runner(&target, |_| {});
        publish(&engine, &ledger_document("Order", json!({})), None);
        mark_callers(&probe);
        create(&engine, "Order", "o1", "Desk");
        engine.runner().run_due().unwrap();
        let cursor = subscription(&engine, "Order").cursor;
        assert_eq!(
            code(engine.instances().create(
                &alice(),
                "Order",
                &json!({"title": 3}),
                Default::default()
            )),
            EngineErrorCode::InvalidInstance
        );
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 0, 0));
        assert_eq!(subscription(&engine, "Order").cursor, cursor);
    }
}

#[test]
fn a_reaction_that_fails_after_its_invoke_leaves_neither_its_effect_nor_a_cursor_move() {
    for target in targets("a_reaction_that_fails_after_its_invoke") {
        let (engine, probe) = open_runner(&target, |_| {});
        publish(&engine, &ledger_document("Order", json!({})), None);
        let start = subscription(&engine, "Order").cursor;
        let crash = Arc::new(Mutex::new(true));
        let crashing = Arc::clone(&crash);
        probe.on_react(move |cx, event| {
            if event.cause.is_some() {
                return Ok(());
            }
            mark(cx, event)?;
            if *crashing.lock().unwrap() {
                return Err(Error::behavior("test.Ledger", "crash before the commit"));
            }
            Ok(())
        });
        create(&engine, "Order", "o1", "Desk");
        let cursor = history(&engine, "Order", "o1")[0].cursor;
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 1, 0));
        assert_eq!(notes(&engine, "Order", "o1"), None);
        assert_eq!(history(&engine, "Order", "o1").len(), 1);
        let failed = subscription(&engine, "Order");
        assert_eq!(
            serde_json::to_value((
                &failed.state,
                failed.cursor,
                failed.attempts,
                failed.retry_at,
                &failed.failure
            ))
            .unwrap(),
            json!(["retrying", start, 1, now(&engine) + 1_000, {
                "cursor": cursor, "at": now(&engine),
                "error": "BehaviorError: behavior test.Ledger: crash before the commit"
            }])
        );
        *crash.lock().unwrap() = false;
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 0, 0));
        assert_eq!(engine.runner().next_wake(), Some(now(&engine) + 1_000));
        advance(&engine, 1_000);
        assert_eq!(engine.runner().run_due().unwrap().handled, 2);
        assert_eq!(
            notes(&engine, "Order", "o1"),
            Some(json!([format!("create {cursor}")]))
        );
        assert_eq!(
            history(&engine, "Order", "o1")
                .iter()
                .filter(|event| event.kind == "operation")
                .count(),
            1
        );
        let recovered = subscription(&engine, "Order");
        assert_eq!(
            (
                recovered.state,
                recovered.attempts,
                recovered.retry_at,
                recovered.failure
            ),
            (SubscriptionState::Active, 0, None, None)
        );
    }
}

#[test]
fn a_subscription_handles_its_events_in_log_order_and_none_after_one_that_fails() {
    for target in targets("a_subscription_handles_its_events_in_log_order") {
        let (engine, probe) = open_runner(&target, |_| {});
        publish(&engine, &ledger_document("Order", json!({})), None);
        let refuse: Arc<Mutex<Option<i64>>> = Arc::default();
        let refusing = Arc::clone(&refuse);
        probe.on_react(move |cx, event| {
            if event.cause.is_some() {
                return Ok(());
            }
            if Some(event.cursor) == *refusing.lock().unwrap() {
                return Err(Error::behavior("test.Ledger", "not yet"));
            }
            mark(cx, event)
        });
        create(&engine, "Order", "o1", "Desk");
        create(&engine, "Order", "o2", "Lamp");
        engine
            .instances()
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": "Desk, oak"}),
                Default::default(),
            )
            .unwrap();
        create(&engine, "Order", "o3", "Rug");
        let caller: Vec<EngineEvent> = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Order"),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events
            .into_iter()
            .filter(|event| event.instance_id.is_some())
            .collect();
        let (created1, created2, updated1, created3) =
            (&caller[0], &caller[1], &caller[2], &caller[3]);
        *refuse.lock().unwrap() = Some(created2.cursor);
        assert_eq!(engine.runner().run_due().unwrap(), pass(1, 0, 1, 0));
        assert_eq!(subscription(&engine, "Order").cursor, created1.cursor);
        assert_eq!(notes(&engine, "Order", "o2"), None);
        assert_eq!(notes(&engine, "Order", "o3"), None);
        *refuse.lock().unwrap() = None;
        advance(&engine, 1_000);
        engine.runner().run_due().unwrap();
        assert_eq!(
            notes(&engine, "Order", "o1"),
            Some(json!([
                format!("create {}", created1.cursor),
                format!("update {}", updated1.cursor)
            ]))
        );
        assert_eq!(
            notes(&engine, "Order", "o2"),
            Some(json!([format!("create {}", created2.cursor)]))
        );
        assert_eq!(
            notes(&engine, "Order", "o3"),
            Some(json!([format!("create {}", created3.cursor)]))
        );
        let caused: Vec<Option<i64>> = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Order"),
                    after: Some(superschematic_engine::EventsAfter::Cursor(created3.cursor)),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events
            .iter()
            .map(|event| event.cause.as_ref().and_then(|cause| cause.event))
            .collect();
        assert_eq!(
            caused,
            vec![
                Some(created1.cursor),
                Some(created2.cursor),
                Some(updated1.cursor),
                Some(created3.cursor)
            ]
        );
    }
}

#[test]
fn failures_retry_with_backoff_halt_after_max_attempts_and_resume_and_skip_passes_over_the_event() {
    for target in targets("failures_retry_with_backoff") {
        let (engine, probe) = open_runner(&target, |options| {
            options.runner = Some(RunnerOptions {
                max_attempts: Some(3),
                retry_initial_ms: Some(100),
                retry_max_ms: Some(150),
                ..RunnerOptions::new(runner())
            });
        });
        publish(&engine, &ledger_document("Order", json!({})), None);
        publish(&engine, &ledger_document("Note", json!({})), None);
        let refuse = Arc::new(Mutex::new(true));
        let refusing = Arc::clone(&refuse);
        probe.on_react(move |cx, event| {
            if event.cause.is_some() {
                return Ok(());
            }
            if cx.schema() == "Order" && *refusing.lock().unwrap() {
                return Err(superschematic_engine::EngineError::invalid_argument("refused").into());
            }
            mark(cx, event)
        });
        create(&engine, "Order", "o1", "Desk");
        create(&engine, "Note", "n1", "Call");
        let failing = history(&engine, "Order", "o1")[0].cursor;
        assert_eq!(engine.runner().run_due().unwrap().failed, 1);
        assert_eq!(note_count(&engine, "Note", "n1"), 1);
        let mut retries = Vec::new();
        for wait in [99, 1, 149, 1] {
            advance(&engine, wait);
            engine.runner().run_due().unwrap();
            let status = subscription(&engine, "Order");
            retries.push((
                status.state,
                status.attempts,
                status.retry_at.map(|at| at - now(&engine)),
            ));
        }
        // 100 ms after the first failure, then min(200, 150) after the
        // second; the third halts.
        assert_eq!(
            retries,
            vec![
                (SubscriptionState::Retrying, 1, Some(1)),
                (SubscriptionState::Retrying, 2, Some(150)),
                (SubscriptionState::Retrying, 2, Some(1)),
                (SubscriptionState::Halted, 3, None),
            ]
        );
        assert_eq!(
            serde_json::to_value(subscription(&engine, "Order").failure).unwrap(),
            json!({"cursor": failing, "at": now(&engine), "error": "invalid_argument: refused"})
        );
        advance(&engine, 1_000_000);
        create(&engine, "Order", "o2", "Lamp");
        create(&engine, "Note", "n2", "Write");
        engine.runner().run_due().unwrap();
        assert_eq!(notes(&engine, "Order", "o2"), None);
        assert_eq!(note_count(&engine, "Note", "n2"), 1);
        assert_eq!(
            subscription(&engine, "Order").state,
            SubscriptionState::Halted
        );
        let resumed = engine.runner().resume(&key("Order"), false).unwrap();
        assert_eq!(
            (resumed.state, resumed.attempts, resumed.failure),
            (SubscriptionState::Active, 0, None)
        );
        assert_eq!(engine.runner().run_due().unwrap().failed, 1);
        assert_eq!(
            subscription(&engine, "Order").state,
            SubscriptionState::Retrying
        );
        *refuse.lock().unwrap() = false;
        let skipped = engine.runner().resume(&key("Order"), true).unwrap();
        assert_eq!(
            serde_json::to_value((
                &skipped.state,
                skipped.cursor,
                skipped.skipped,
                &skipped.last_skip
            ))
            .unwrap(),
            json!(["active", failing, 1, {"cursor": failing, "reason": "resume"}])
        );
        engine.runner().run_due().unwrap();
        assert_eq!(notes(&engine, "Order", "o1"), None);
        assert_eq!(note_count(&engine, "Order", "o2"), 1);
        assert_eq!(
            code(engine.runner().resume(&key("Order"), true)),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            code(engine.runner().resume(&key("Ghost"), false)),
            EngineErrorCode::NotFound
        );
    }
}

#[test]
fn the_depth_limit_stops_a_loop_of_reactions_and_the_subscription_counts_what_it_passed_over() {
    for target in targets("the_depth_limit_stops_a_loop") {
        let (engine, probe) = open_runner(&target, |options| {
            options.runner = Some(RunnerOptions {
                max_depth: Some(3),
                ..RunnerOptions::new(runner())
            });
        });
        publish(&engine, &ledger_document("Order", json!({})), None);
        probe.on_react(mark);
        create(&engine, "Order", "o1", "Desk");
        assert_eq!(engine.runner().run_due().unwrap(), pass(3, 1, 0, 0));
        let events = history(&engine, "Order", "o1");
        assert_eq!(
            events
                .iter()
                .map(|event| (
                    event.cause.as_ref().and_then(|cause| cause.event),
                    event.cause.as_ref().map_or(0, |cause| cause.depth)
                ))
                .collect::<Vec<_>>(),
            vec![
                (None, 0),
                (Some(events[0].cursor), 1),
                (Some(events[1].cursor), 2),
                (Some(events[2].cursor), 3),
            ]
        );
        let status = subscription(&engine, "Order");
        assert_eq!(
            serde_json::to_value((
                &status.state,
                status.cursor,
                status.skipped,
                &status.last_skip
            ))
            .unwrap(),
            json!(["active", events[3].cursor, 1, {"cursor": events[3].cursor, "reason": "depth"}])
        );
        assert_eq!(note_count(&engine, "Order", "o1"), 3);
    }
}

#[test]
fn the_runner_acts_as_its_principal_and_a_refusal_halts() {
    for target in targets("the_runner_acts_as_its_principal") {
        let grant = Arc::new(Mutex::new(false));
        let granted = Arc::clone(&grant);
        let policy = RecordingPolicy::new(move |request: &AccessRequest<'_>| {
            if request.principal().subject == "runner" {
                return request.action() == "read" || *granted.lock().unwrap();
            }
            // alice may not call mark herself.
            request.operation() != Some("mark")
        });
        let asked = policy.clone();
        let (engine, probe) = open_runner(&target, |options| {
            options.policy = Some(Policy::custom(policy));
            options.runner = Some(RunnerOptions {
                max_attempts: Some(2),
                ..RunnerOptions::new(runner())
            });
        });
        publish(&engine, &ledger_document("Order", json!({})), None);
        mark_callers(&probe);
        create(&engine, "Order", "o1", "Desk");
        assert_eq!(
            code(engine.instances().invoke(
                &alice(),
                "Order",
                "o1",
                "mark",
                &json!({"note": "mine"}),
                Default::default()
            )),
            EngineErrorCode::Forbidden
        );
        asked.take();
        assert_eq!(engine.runner().run_due().unwrap().failed, 1);
        assert_eq!(
            subscription(&engine, "Order").failure.unwrap().error,
            "forbidden: runner may not call mark (write) on Order in namespace default"
        );
        assert!(asked
            .take()
            .iter()
            .any(|request| request["action"] == "write"
                && request["schema"] == "Order"
                && request["operation"] == "mark"));
        advance(&engine, 1_000);
        engine.runner().run_due().unwrap();
        assert_eq!(
            subscription(&engine, "Order").state,
            SubscriptionState::Halted
        );
        *grant.lock().unwrap() = true;
        engine.runner().resume(&key("Order"), false).unwrap();
        engine.runner().run_due().unwrap();
        assert_eq!(note_count(&engine, "Order", "o1"), 1);
        assert_eq!(history(&engine, "Order", "o1")[1].actor, "runner");
    }
}

#[test]
fn stop_and_start_resume_from_the_saved_cursor_and_so_does_a_new_engine_on_the_storage() {
    for target in targets("stop_and_start_resume") {
        let (engine, probe) = open_runner(&target, |options| options.clock = Clock::System);
        let engine = engine.shared();
        publish(&engine, &ledger_document("Order", json!({})), None);
        mark_callers(&probe);
        engine.runner().start().unwrap();
        create(&engine, "Order", "o1", "Desk");
        wait_for("o1's note", || notes(&engine, "Order", "o1").is_some());
        engine.runner().stop();
        create(&engine, "Order", "o2", "Lamp");
        create(&engine, "Order", "o3", "Rug");
        engine.shutdown();
        drop(engine);
        let (reopened, probe) = open_runner(&target, |options| options.clock = Clock::System);
        mark_callers(&probe);
        reopened.runner().run_due().unwrap();
        for id in ["o1", "o2", "o3"] {
            assert_eq!(note_count(&reopened, "Order", id), 1, "{id}");
        }
    }
}

#[test]
fn a_schedule_runs_an_interval_after_the_runner_finds_it_once_for_missed_ticks_and_retries() {
    for target in targets("a_schedule_runs_an_interval_after") {
        let (engine, probe) = open_runner(&target, |options| {
            options.clock = Clock::Manual(0);
            options.runner = Some(RunnerOptions {
                retry_initial_ms: Some(50_000),
                retry_max_ms: Some(500_000),
                ..RunnerOptions::new(runner())
            });
        });
        publish(&engine, &ledger_document("Order", json!({})), None);
        publish(&engine, &plain_document("Note"), None);
        create(&engine, "Order", "o1", "Desk");
        let runs: Arc<Mutex<Vec<Run>>> = Arc::default();
        let fail = Arc::new(Mutex::new(false));
        let (into, failing) = (Arc::clone(&runs), Arc::clone(&fail));
        probe.on_sweep(move |cx| {
            into.lock()
                .unwrap()
                .push((cx.schedule().to_owned(), cx.previous(), cx.now()));
            if *failing.lock().unwrap() {
                return Err(Error::behavior("test.Ledger", "the sweep failed"));
            }
            cx.instances().invoke(
                "Order",
                "o1",
                "mark",
                &json!({"note": format!("sweep at {}", cx.now())}),
                None,
            )?;
            Ok(())
        });
        assert_eq!(engine.runner().run_due().unwrap().scheduled, 0);
        assert_eq!(
            serde_json::to_value(engine.runner().status().unwrap().schedules).unwrap(),
            json!([{
                "behavior": "test.Ledger", "schedule": "sweep", "namespace": "default", "schema": "Order",
                "state": "active", "everyMs": 60_000, "previous": null, "next": 60_000, "failures": 0, "error": null
            }])
        );
        assert_eq!(engine.runner().next_wake(), Some(60_000));
        engine.clock().set(59_999).unwrap();
        assert_eq!(engine.runner().run_due().unwrap().scheduled, 0);
        engine.clock().set(60_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap().scheduled, 1);
        engine.clock().set(60_000 + 10 * 60_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap().scheduled, 1);
        assert_eq!(
            *runs.lock().unwrap(),
            vec![
                ("sweep".to_owned(), None, 60_000),
                ("sweep".to_owned(), Some(60_000), 660_000)
            ]
        );
        let swept: Vec<Value> = history(&engine, "Order", "o1")
            .into_iter()
            .filter(|event| event.kind == "operation")
            .map(|event| json!([event.actor, event.cause]))
            .collect();
        assert_eq!(
            swept,
            vec![
                json!(["runner", {"behavior": "test.Ledger", "schedule": "sweep", "depth": 1}]),
                json!(["runner", {"behavior": "test.Ledger", "schedule": "sweep", "depth": 1}]),
            ]
        );
        *fail.lock().unwrap() = true;
        let mut failures = Vec::new();
        engine.clock().set(720_000).unwrap();
        for _ in 0..4 {
            assert_eq!(engine.runner().run_due().unwrap().failed, 1);
            let status = engine.runner().status().unwrap().schedules[0].clone();
            let next = status
                .next
                .expect("a retrying schedule runs next at a time");
            failures.push((status.state, status.failures, next - now(&engine)));
            engine.clock().set(next).unwrap();
        }
        // 50 s after the first failure, then never later than the next tick.
        assert_eq!(
            failures,
            vec![
                (ScheduleState::Retrying, 1, 50_000),
                (ScheduleState::Retrying, 2, 60_000),
                (ScheduleState::Retrying, 3, 60_000),
                (ScheduleState::Retrying, 4, 60_000),
            ]
        );
        assert_eq!(
            engine.runner().status().unwrap().schedules[0]
                .error
                .as_deref(),
            Some("BehaviorError: behavior test.Ledger: the sweep failed")
        );
        *fail.lock().unwrap() = false;
        assert_eq!(engine.runner().run_due().unwrap().scheduled, 1);
        let recovered = engine.runner().status().unwrap().schedules[0].clone();
        assert_eq!(
            (
                recovered.state,
                recovered.failures,
                recovered.error,
                recovered.previous
            ),
            (ScheduleState::Active, 0, None, Some(now(&engine)))
        );
    }
}

// An engine with test.Ledger and test.Pacer.
fn open_paced(
    target: &Target,
    start: i64,
) -> (Engine, superschematic_engine_testkit::behaviors::PacerProbe) {
    let pacer = Pacer::default();
    let probe = pacer.probe.clone();
    let engine = target.open_with(
        BehaviorSet::new().with(Ledger::default()).with(pacer),
        |options| {
            options.clock = Clock::Manual(start);
            options.runner = Some(RunnerOptions::new(runner()));
        },
    );
    (engine, probe)
}

fn schedules(engine: &Engine) -> Value {
    let mut out = serde_json::Map::new();
    for status in engine.runner().status().unwrap().schedules {
        out.insert(
            status.schema.clone(),
            json!([
                status.state,
                status.every_ms,
                status.failures,
                status.next,
                status.error,
                status.previous
            ]),
        );
    }
    Value::Object(out)
}

#[test]
fn a_schedules_interval_follows_the_config_of_each_schema_and_a_new_configs_from_the_next_run() {
    for target in targets("a_schedules_interval_follows_the_config") {
        let (engine, probe) = open_paced(&target, 0);
        publish(
            &engine,
            &pacer_document("Fast", json!({"everyMs": 5_000})),
            None,
        );
        publish(
            &engine,
            &pacer_document("Slow", json!({"everyMs": 20_000})),
            None,
        );
        let runs: Arc<Mutex<Vec<String>>> = Arc::default();
        let into = Arc::clone(&runs);
        probe.on_tick(move |cx| {
            into.lock()
                .unwrap()
                .push(format!("{} {}", cx.schema(), cx.now()));
            Ok(())
        });
        assert_eq!(engine.runner().run_due().unwrap().scheduled, 0);
        assert_eq!(
            schedules(&engine),
            json!({
                "Fast": ["active", 5_000, 0, 5_000, null, null],
                "Slow": ["active", 20_000, 0, 20_000, null, null]
            })
        );
        for at in (5_000..=20_000).step_by(5_000) {
            engine.clock().set(at).unwrap();
            engine.runner().run_due().unwrap();
        }
        assert_eq!(
            *runs.lock().unwrap(),
            vec![
                "Fast 5000",
                "Fast 10000",
                "Fast 15000",
                "Fast 20000",
                "Slow 20000"
            ]
        );
        publish(
            &engine,
            &pacer_document("Slow", json!({"everyMs": 10_000})),
            None,
        );
        assert_eq!(schedules(&engine)["Slow"][1], json!(10_000));
        assert_eq!(schedules(&engine)["Slow"][3], json!(40_000));
        engine.clock().set(40_000).unwrap();
        engine.runner().run_due().unwrap();
        assert_eq!(schedules(&engine)["Slow"][5], json!(40_000));
        assert_eq!(schedules(&engine)["Slow"][3], json!(50_000));
    }
}

#[test]
fn an_interval_a_config_cannot_give_fails_the_schedule_there_and_runs_once_a_publish_gives_one() {
    for target in targets("an_interval_a_config_cannot_give") {
        let (engine, probe) = open_paced(&target, 0);
        publish(
            &engine,
            &pacer_document("Fast", json!({"everyMs": 5_000})),
            None,
        );
        publish(
            &engine,
            &pacer_document("Short", json!({"everyMs": 10})),
            None,
        );
        publish(
            &engine,
            &pacer_document("Broken", json!({"everyMs": -1})),
            None,
        );
        probe.on_every(|config| {
            if config.every_ms == Some(-1) {
                return Err(Error::behavior("test.Pacer", "no interval for -1"));
            }
            Ok(config.every_ms)
        });
        let runs: Arc<Mutex<Vec<String>>> = Arc::default();
        let into = Arc::clone(&runs);
        probe.on_tick(move |cx| {
            into.lock()
                .unwrap()
                .push(format!("{} {}", cx.schema(), cx.now()));
            Ok(())
        });
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 2, 0));
        assert_eq!(
            schedules(&engine),
            json!({
                "Broken": ["retrying", null, 1, 1_000, "BehaviorError: behavior test.Pacer: no interval for -1", null],
                "Fast": ["active", 5_000, 0, 5_000, null, null],
                "Short": ["retrying", null, 1, 1_000, "BehaviorError: behavior test.Pacer: schedule tick: everyMs(config) returns an integer of at least 1000, got 10", null]
            })
        );
        engine.clock().set(1_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 2, 0));
        assert_eq!(schedules(&engine)["Short"][2], json!(2));
        assert_eq!(schedules(&engine)["Short"][3], json!(3_000));
        engine.clock().set(5_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 2, 1));
        assert_eq!(*runs.lock().unwrap(), vec!["Fast 5000"]);
        assert_eq!(engine.runner().status().unwrap().error, None);
        publish(
            &engine,
            &pacer_document("Short", json!({"everyMs": 2_000})),
            None,
        );
        engine.clock().set(9_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 1, 1));
        assert_eq!(
            schedules(&engine)["Short"],
            json!(["active", 2_000, 0, 11_000, null, 9_000])
        );
        assert_eq!(schedules(&engine)["Broken"][2], json!(4));
        assert_eq!(*runs.lock().unwrap(), vec!["Fast 5000", "Short 9000"]);
    }
}

#[test]
fn a_schedule_a_schema_turns_off_runs_nothing_there_and_runs_again_after_a_publish_gives_it_one() {
    for target in targets("a_schedule_a_schema_turns_off") {
        let (engine, probe) = open_paced(&target, 0);
        probe.on_every(|config| Ok(config.every_ms));
        let runs: Arc<Mutex<Vec<String>>> = Arc::default();
        let into = Arc::clone(&runs);
        probe.on_tick(move |cx| {
            into.lock()
                .unwrap()
                .push(format!("{} {}", cx.schema(), cx.now()));
            Ok(())
        });
        publish(
            &engine,
            &pacer_document("On", json!({"everyMs": 5_000})),
            None,
        );
        publish(&engine, &pacer_document("Off", json!({})), None);
        let off = json!(["off", null, 0, null, null, null]);
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 0, 0));
        assert_eq!(
            schedules(&engine),
            json!({"Off": off, "On": ["active", 5_000, 0, 5_000, null, null]})
        );
        engine.clock().set(50_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 0, 1));
        assert_eq!(*runs.lock().unwrap(), vec!["On 50000"]);
        assert_eq!(schedules(&engine)["Off"], off);
        publish(
            &engine,
            &pacer_document("Off", json!({"everyMs": 2_000})),
            None,
        );
        assert_eq!(engine.runner().run_due().unwrap().scheduled, 0);
        assert_eq!(
            schedules(&engine)["Off"],
            json!(["active", 2_000, 0, 52_000, null, null])
        );
        engine.clock().set(52_000).unwrap();
        engine.runner().run_due().unwrap();
        assert_eq!(*runs.lock().unwrap(), vec!["On 50000", "Off 52000"]);
        publish(&engine, &pacer_document("On", json!({})), None);
        engine.clock().set(100_000).unwrap();
        engine.runner().run_due().unwrap();
        assert_eq!(
            *runs.lock().unwrap(),
            vec!["On 50000", "Off 52000", "Off 100000"]
        );
        assert_eq!(schedules(&engine)["On"], off);
        assert!(texts(
            &engine,
            "SELECT schema FROM engine_schedules WHERE schema = 'On'",
            &[]
        )
        .is_empty());
        publish(
            &engine,
            &pacer_document("On", json!({"everyMs": 5_000})),
            None,
        );
        engine.runner().run_due().unwrap();
        assert_eq!(
            schedules(&engine)["On"],
            json!(["active", 5_000, 0, 105_000, null, null])
        );
    }
}

fn ledger_notes(engine: &Engine) -> Vec<String> {
    texts(
        engine,
        "SELECT note FROM bhv_test_ledger__notes ORDER BY position",
        &[],
    )
}

#[test]
fn a_schedule_run_writes_its_behaviors_own_tables_and_its_writes_roll_back_with_it() {
    for target in targets("a_schedule_run_writes_its_behaviors_own_tables") {
        let (engine, probe) = open_runner(&target, |options| options.clock = Clock::Manual(0));
        publish(&engine, &ledger_document("Order", json!({})), None);
        create(&engine, "Order", "o1", "Desk");
        let fail = Arc::new(Mutex::new(false));
        let failing = Arc::clone(&fail);
        probe.on_sweep(move |cx| {
            let table = cx.sql().table("notes")?;
            cx.sql().run(
                format!("INSERT INTO {table} (namespace, schema, id, note) VALUES (?, ?, ?, ?)"),
                &[
                    SqlValue::from(cx.namespace()),
                    SqlValue::from(cx.schema()),
                    SqlValue::from("o1"),
                    SqlValue::from(format!(
                        "swept by {} at {}",
                        cx.principal().subject,
                        cx.now()
                    )),
                ],
            )?;
            if *failing.lock().unwrap() {
                return Err(Error::behavior(
                    "test.Ledger",
                    "the sweep failed after its write",
                ));
            }
            Ok(())
        });
        engine.runner().run_due().unwrap();
        engine.clock().set(60_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap().scheduled, 1);
        assert_eq!(ledger_notes(&engine), vec!["swept by runner at 60000"]);
        // The write is the behavior's alone: no event records it.
        assert_eq!(
            engine
                .events()
                .read(
                    &alice(),
                    &ReadEvents {
                        schema: Some("Order"),
                        ..ReadEvents::default()
                    }
                )
                .unwrap()
                .events
                .iter()
                .map(|event| event.kind.as_str())
                .collect::<Vec<_>>(),
            vec!["define", "publish", "create"]
        );
        *fail.lock().unwrap() = true;
        engine.clock().set(120_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap().failed, 1);
        assert_eq!(ledger_notes(&engine), vec!["swept by runner at 60000"]);
        let status = engine.runner().status().unwrap().schedules[0].clone();
        assert_eq!(
            (status.state, status.failures, status.error.as_deref()),
            (
                ScheduleState::Retrying,
                1,
                Some("BehaviorError: behavior test.Ledger: the sweep failed after its write")
            )
        );
    }
}

#[test]
fn the_relation_over_the_instances_stays_read_only_in_a_run_and_a_reaction_writes_nothing() {
    for target in targets("the_relation_stays_read_only") {
        let (engine, probe) = open_runner(&target, |options| options.clock = Clock::Manual(0));
        publish(&engine, &ledger_document("Order", json!({})), None);
        create(&engine, "Order", "o1", "Desk");
        let seen: Arc<Mutex<Vec<usize>>> = Arc::default();
        let into = Arc::clone(&seen);
        probe.on_sweep(move |cx| {
            let instances = cx.sql().instances()?;
            into.lock().unwrap().push(
                cx.sql()
                    .all(format!("SELECT id FROM {instances}"), &[])?
                    .len(),
            );
            cx.sql()
                .run(format!("UPDATE {instances} SET data = '{{}}'"), &[])?;
            Ok(())
        });
        engine.runner().run_due().unwrap();
        engine.clock().set(60_000).unwrap();
        assert_eq!(engine.runner().run_due().unwrap().failed, 1);
        assert_eq!(*seen.lock().unwrap(), vec![1]);
        assert!(engine.runner().status().unwrap().schedules[0]
            .error
            .as_deref()
            .unwrap_or_default()
            .contains("bhv_test_ledger___instances"));
        // A reaction's SQL still writes nothing.
        probe.on_react(|cx, event| {
            let table = cx.sql().table("notes")?;
            cx.sql().get(
                format!("INSERT INTO {table} (namespace, schema, id, note) VALUES (?, ?, ?, ?) RETURNING note"),
                &[
                    SqlValue::from(cx.namespace()),
                    SqlValue::from(cx.schema()),
                    SqlValue::from(event.instance_id.as_deref().unwrap_or_default()),
                    SqlValue::from("reacted"),
                ],
            )?;
            Ok(())
        });
        create(&engine, "Order", "o2", "Lamp");
        assert!(engine.runner().run_due().unwrap().failed >= 1);
        assert!(ledger_notes(&engine).is_empty());
        let status = subscription(&engine, "Order");
        assert_eq!(status.state, SubscriptionState::Retrying);
        assert!(status
            .failure
            .unwrap()
            .error
            .contains("behavior test.Ledger"),);
    }
}

#[test]
fn a_subscription_starts_at_the_publish_that_composes_the_behavior_and_hears_what_watches_names() {
    for target in targets("a_subscription_starts_at_the_publish") {
        let (engine, probe) = open_runner(&target, |_| {});
        publish(&engine, &plain_document("Note"), None);
        publish(&engine, &plain_document("Order"), None);
        create(&engine, "Order", "o1", "Before");
        let seen: Arc<Mutex<Vec<String>>> = Arc::default();
        let into = Arc::clone(&seen);
        probe.on_react(move |cx, event| {
            if event.cause.is_none() {
                into.lock().unwrap().push(format!(
                    "{} hears {} {} {}",
                    cx.schema(),
                    event.kind,
                    event.schema,
                    event.instance_id.as_deref().unwrap_or_default()
                ));
            }
            Ok(())
        });
        publish(
            &engine,
            &ledger_document("Order", json!({"watch": ["Note"]})),
            None,
        );
        create(&engine, "Order", "o2", "After");
        create(&engine, "Note", "n1", "Call");
        engine.runner().run_due().unwrap();
        assert_eq!(
            *seen.lock().unwrap(),
            vec!["Order hears create Order o2", "Order hears create Note n1"]
        );
        // Removed, its subscription is inactive; composed again, it starts
        // at that publish.
        publish(&engine, &plain_document("Order"), None);
        create(&engine, "Order", "o3", "Between");
        engine.runner().run_due().unwrap();
        assert_eq!(
            subscription(&engine, "Order").state,
            SubscriptionState::Inactive
        );
        publish(&engine, &ledger_document("Order", json!({})), None);
        create(&engine, "Order", "o4", "Again");
        engine.runner().run_due().unwrap();
        assert_eq!(
            seen.lock().unwrap()[2..].to_vec(),
            vec!["Order hears create Order o4"]
        );
        assert_eq!(
            subscription(&engine, "Order").state,
            SubscriptionState::Active
        );
    }
}

#[test]
fn a_watches_that_answers_none_turns_the_reactions_off_and_turned_on_they_start_at_that_publish() {
    for target in targets("a_watches_that_answers_none") {
        let (engine, probe) = open_runner(&target, |_| {});
        let seen: Arc<Mutex<Vec<String>>> = Arc::default();
        let into = Arc::clone(&seen);
        probe.on_react(move |_, event| {
            if event.cause.is_none() {
                into.lock().unwrap().push(format!(
                    "{} {}",
                    event.kind,
                    event.instance_id.as_deref().unwrap_or_default()
                ));
            }
            Ok(())
        });
        publish(
            &engine,
            &ledger_document("Order", json!({"off": true})),
            None,
        );
        create(&engine, "Order", "o1", "Off");
        assert_eq!(engine.runner().run_due().unwrap().handled, 0);
        // Off and never run, it is not listed.
        assert!(engine.runner().status().unwrap().subscriptions.is_empty());
        publish(&engine, &ledger_document("Order", json!({})), None);
        create(&engine, "Order", "o2", "On");
        engine.runner().run_due().unwrap();
        assert_eq!(*seen.lock().unwrap(), vec!["create o2"]);
        publish(
            &engine,
            &ledger_document("Order", json!({"off": true})),
            None,
        );
        create(&engine, "Order", "o3", "Off again");
        engine.runner().run_due().unwrap();
        assert_eq!(subscription(&engine, "Order").state, SubscriptionState::Off);
        publish(
            &engine,
            &ledger_document("Order", json!({"watch": []})),
            None,
        );
        create(&engine, "Order", "o4", "Back");
        engine.runner().run_due().unwrap();
        assert_eq!(*seen.lock().unwrap(), vec!["create o2", "create o4"]);
        assert_eq!(
            subscription(&engine, "Order").state,
            SubscriptionState::Active
        );
    }
}

#[test]
fn a_reaction_reads_an_instance_as_the_log_had_it_before_an_event_and_invokes_schema_operations() {
    for target in targets("a_reaction_reads_an_instance_before") {
        let (engine, probe) = open_runner(&target, |_| {});
        publish(&engine, &ledger_document("Order", json!({})), None);
        let before: Arc<Mutex<Vec<Value>>> = Arc::default();
        let counted: Arc<Mutex<Vec<Value>>> = Arc::default();
        let (into, counts) = (Arc::clone(&before), Arc::clone(&counted));
        probe.on_react(move |cx, event| {
            if event.cause.is_none() {
                into.lock()
                    .unwrap()
                    .push(serde_json::to_value(cx.before(event)?).unwrap());
                counts.lock().unwrap().push(cx.instances().invoke_schema(
                    "Order",
                    "count",
                    &json!({}),
                )?);
            }
            Ok(())
        });
        create(&engine, "Order", "o1", "Desk");
        engine
            .instances()
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": "Lamp"}),
                Default::default(),
            )
            .unwrap();
        engine
            .instances()
            .invoke(
                &alice(),
                "Order",
                "o1",
                "mark",
                &json!({"note": "kept"}),
                Default::default(),
            )
            .unwrap();
        engine
            .instances()
            .delete(&alice(), "Order", "o1", Default::default())
            .unwrap();
        engine.runner().run_due().unwrap();
        let order = |title: &str, notes: Option<Value>| {
            json!({"data": {"title": title}, "behaviors": {"test.Ledger": match notes {
                None => json!({}),
                Some(notes) => json!({"notes": notes}),
            }}})
        };
        assert_eq!(
            *before.lock().unwrap(),
            vec![
                Value::Null,
                order("Desk", None),
                order("Lamp", None),
                order("Lamp", Some(json!(["kept"])))
            ]
        );
        assert_eq!(*counted.lock().unwrap(), vec![json!(0); 4]);
        probe.on_react(|cx, _| {
            cx.instances()
                .invoke_schema("Order", "mark", &json!({"note": "x"}))?;
            Ok(())
        });
        create(&engine, "Order", "o2", "Rug");
        engine.runner().run_due().unwrap();
        assert!(subscription(&engine, "Order")
            .failure
            .unwrap()
            .error
            .starts_with("not_found: Order's mark is an instance operation"));
    }
}

#[test]
fn a_watches_that_fails_is_a_failure_of_the_subscription_with_no_event_which_passes_once_it_works()
{
    for target in targets("a_watches_that_fails") {
        let (engine, probe) = open_runner(&target, |_| {});
        publish(&engine, &ledger_document("Order", json!({})), None);
        mark_callers(&probe);
        probe.on_watches(Some(Arc::new(|_| {
            Err(Error::behavior("test.Ledger", "no schemas today"))
        })));
        create(&engine, "Order", "o1", "Desk");
        assert_eq!(engine.runner().run_due().unwrap().failed, 1);
        let failed = subscription(&engine, "Order");
        assert_eq!(
            serde_json::to_value((&failed.state, failed.attempts, &failed.failure)).unwrap(),
            json!(["retrying", 1, {"cursor": null, "at": now(&engine), "error": "BehaviorError: behavior test.Ledger: no schemas today"}])
        );
        assert_eq!(
            code(engine.runner().resume(&key("Order"), true)),
            EngineErrorCode::InvalidArgument
        );
        probe.on_watches(Some(Arc::new(|_| Ok(Some(vec!["not a name".to_owned()])))));
        advance(&engine, 1_000);
        engine.runner().run_due().unwrap();
        assert!(subscription(&engine, "Order")
            .failure
            .unwrap()
            .error
            .starts_with("invalid_argument: schema name \"not a name\""));
        probe.on_watches(None);
        advance(&engine, 2_000);
        engine.runner().run_due().unwrap();
        assert_eq!(note_count(&engine, "Order", "o1"), 1);
        let recovered = subscription(&engine, "Order");
        assert_eq!(
            (recovered.state, recovered.attempts, recovered.failure),
            (SubscriptionState::Active, 0, None)
        );
    }
}

#[test]
fn run_due_inside_a_reaction_is_refused_as_a_failure_of_that_reaction() {
    for target in targets("run_due_inside_a_reaction") {
        let (engine, probe) = open_runner(&target, |_| {});
        let engine = engine.shared();
        let held: Arc<OnceLock<Weak<Engine>>> = Arc::default();
        let _ = held.set(Arc::downgrade(&engine));
        let reaching = Arc::clone(&held);
        probe.on_react(move |_, _| {
            let engine = reaching.get().and_then(Weak::upgrade).expect("the engine");
            engine.runner().run_due().map(|_| ())
        });
        publish(&engine, &ledger_document("Order", json!({})), None);
        create(&engine, "Order", "o1", "Desk");
        assert_eq!(engine.runner().run_due().unwrap().failed, 1);
        assert!(subscription(&engine, "Order")
            .failure
            .unwrap()
            .error
            .contains("the runner is already running"));
    }
}

#[test]
fn an_archived_namespace_runs_nothing_until_it_is_unarchived() {
    for target in targets("an_archived_namespace_runs_nothing") {
        let (engine, probe) = open_runner(&target, |_| {});
        mark_callers(&probe);
        engine.namespaces().create(&alice(), "acme").unwrap();
        publish(&engine, &ledger_document("Order", json!({})), Some("acme"));
        engine
            .instances()
            .create(
                &alice(),
                "Order",
                &json!({"title": "Desk"}),
                superschematic_engine::CreateOptions {
                    id: Some("o1"),
                    namespace: Some("acme"),
                    ..Default::default()
                },
            )
            .unwrap();
        engine.namespaces().archive(&alice(), "acme").unwrap();
        assert_eq!(engine.runner().run_due().unwrap(), pass(0, 0, 0, 0));
        let status = engine.runner().status().unwrap();
        assert_eq!(status.subscriptions[0].state, SubscriptionState::Archived);
        assert_eq!(status.schedules[0].state, ScheduleState::Archived);
        engine.namespaces().unarchive(&alice(), "acme").unwrap();
        assert_eq!(engine.runner().run_due().unwrap().handled, 2);
    }
}

// Two engines on one Postgres database, each with a runner: only the one
// whose process holds the runner's lease runs, and the other takes over
// once it stops.
#[cfg(feature = "postgres")]
#[test]
fn on_postgres_one_engines_runner_runs_and_another_takes_over_once_it_stops() {
    for target in targets("one_engines_runner_runs") {
        if target.dialect != superschematic_engine::storage::Dialect::Postgres {
            continue;
        }
        let open = |name: &'static str, seen: Arc<Mutex<Vec<(String, String)>>>| {
            let ledger = Ledger::default();
            ledger.probe.on_react(move |cx, event| {
                if event.cause.is_none() {
                    seen.lock().unwrap().push((
                        name.to_owned(),
                        event.instance_id.clone().unwrap_or_default(),
                    ));
                    mark(cx, event)?;
                }
                Ok(())
            });
            let mut options = target.options(BehaviorSet::new().with(ledger));
            options.runner = Some(RunnerOptions {
                lease_retry_ms: Some(50),
                ..RunnerOptions::new(runner())
            });
            if let superschematic_engine::Storage::Postgres { options, .. } = &mut options.storage {
                options.listen = true;
            }
            Engine::open(options).expect("open").shared()
        };
        let seen: Arc<Mutex<Vec<(String, String)>>> = Arc::default();
        let first = open("first", Arc::clone(&seen));
        let second = open("second", Arc::clone(&seen));
        publish(&first, &ledger_document("Order", json!({})), None);
        first.runner().start().unwrap();
        // The first takes the lease at its first pass.
        std::thread::sleep(Duration::from_millis(300));
        second.runner().start().unwrap();
        assert_eq!(
            code(second.runner().run_due()),
            EngineErrorCode::Busy,
            "run_due waits for the lease another process holds"
        );
        for id in ["o1", "o2", "o3"] {
            create(&second, "Order", id, id);
        }
        wait_for("the first runner's notes", || {
            ["o1", "o2", "o3"]
                .iter()
                .all(|id| note_count(&first, "Order", id) == 1)
        });
        std::thread::sleep(Duration::from_millis(300));
        assert!(
            seen.lock().unwrap().iter().all(|(who, _)| who == "first"),
            "only the lease holder runs: {:?}",
            seen.lock().unwrap()
        );
        first.runner().stop();
        create(&first, "Order", "o4", "o4");
        wait_for("the second runner takes over", || {
            note_count(&second, "Order", "o4") == 1
        });
        assert_eq!(
            seen.lock().unwrap().last().cloned(),
            Some(("second".to_owned(), "o4".to_owned()))
        );
        assert_eq!(
            seen.lock()
                .unwrap()
                .iter()
                .filter(|(_, id)| id == "o4")
                .count(),
            1
        );
        second.shutdown();
        first.shutdown();
    }
}
