//! Retries (D16), logical retries per failure class, on the Rust engine
//! (D58): the cases of the work queue's `test/retries.test.ts` (attempts
//! counted against per-class caps and a total, narrowed or widened by the
//! instance's limitsField; exhaustion at a terminal class, a cap or a stuck
//! signature, which moves the status through Workflow only from the from
//! states and then holds the instance; kept results, the best scoring one
//! with keepBest, written to resultField with an update's checks and
//! guards, StateRules' among them; caps a worker cannot raise for itself;
//! an attempt's detail in its event and its class's hint in its result;
//! and the config rules), on SQLite and, with the variable set, on
//! Postgres. A TypeScript case that opens a second engine is a test of its
//! own here.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, Engine, EngineErrorCode, Error, Principal};
use support::{alice, count, issue, issues_of, refusal, targets, veto, Target};
use workqueue_support::{define_refusal, job_flow, principal, publish, Instances, T0};

fn worker() -> Principal {
    principal("wren", &[])
}

fn other() -> Principal {
    principal("otto", &[])
}

fn classes() -> Value {
    json!({"timeout": {"attempts": 3}, "invalid": {"attempts": 2}, "rejected": "terminal"})
}

fn base() -> Value {
    json!({"classes": classes(), "totalAttempts": 4, "exhaustedState": "failed"})
}

fn with(config: Value, more: Value) -> Value {
    let mut config = config;
    if let (Value::Object(config), Value::Object(more)) = (&mut config, more) {
        config.extend(more);
    }
    config
}

/// A General schema named Job whose instance type has a title, an output
/// string of at most 20 characters, and two JSON fields, report and caps,
/// and composes the behaviors given.
fn retries_document(behaviors: Value) -> Value {
    json!({
        "kind": "General",
        "name": "Job",
        "types": {
            "Job": {
                "name": "Job",
                "role": "EmbeddedStruct",
                "behaviors": behaviors,
                "fields": [
                    {"name": "title", "typeRef": {"name": "string"}, "required": true},
                    {"name": "output", "typeRef": {"name": "string"}, "validateMaxLength": 20},
                    {"name": "report", "typeRef": {"name": "Generic.JSON"}},
                    {"name": "caps", "typeRef": {"name": "Generic.JSON"}}
                ]
            }
        }
    })
}

fn jobs(engine: &Engine) -> Instances<'_> {
    Instances {
        engine,
        schema: "Job",
    }
}

// world opens an engine whose Job composes Workflow, the behaviors given
// (Lease when none) and Retries with the config given, with job j1 queued.
fn world_with(target: &Target, config: Value, data: Value, behaviors: Value) -> Engine {
    let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(T0));
    let mut all = vec![json!({"name": "Workflow", "config": job_flow()})];
    all.extend(behaviors.as_array().cloned().unwrap_or_default());
    all.push(json!({"name": "Retries", "config": config}));
    publish(&engine, &retries_document(Value::Array(all)));
    jobs(&engine).create("j1", with(json!({"title": "Build"}), data));
    engine
}

fn world(target: &Target, config: Value) -> Engine {
    world_with(target, config, json!({}), json!([{"name": "Lease"}]))
}

fn invoke(
    engine: &Engine,
    who: &Principal,
    operation: &str,
    params: Value,
    id: &str,
) -> Result<Value, Error> {
    jobs(engine).invoke_on(who, id, operation, params, None)
}

fn try_attempt(engine: &Engine, params: Value) -> Result<Value, Error> {
    invoke(engine, &worker(), "recordAttempt", params, "j1")
}

fn attempt(engine: &Engine, params: Value) -> Value {
    try_attempt(engine, params).expect("recordAttempt")
}

fn attempt_on(engine: &Engine, params: Value, id: &str) -> Value {
    invoke(engine, &worker(), "recordAttempt", params, id).expect("recordAttempt")
}

fn data_of(engine: &Engine) -> Value {
    Value::Object(jobs(engine).read("j1").data)
}

fn retries_of(engine: &Engine) -> Value {
    jobs(engine).behavior("j1", "Retries")
}

fn status_of(engine: &Engine) -> Value {
    jobs(engine).status("j1")
}

fn counts(timeout: i64, invalid: i64, rejected: i64) -> Value {
    json!({"timeout": timeout, "invalid": invalid, "rejected": rejected})
}

// attempt0 is a counted timeout failure's result with total n, not kept
// and not exhausted.
fn attempt0(n: i64) -> Value {
    json!({"failure": "timeout", "score": null, "kept": false, "total": n, "classAttempts": counts(n, 0, 0), "exhausted": false, "stuck": false, "hint": null})
}

fn attempts_rows(engine: &Engine) -> i64 {
    count(engine, "SELECT COUNT(*) AS n FROM bhv_retries__attempts")
}

// ---- attempts ----

#[test]
fn a_success_is_kept_its_result_written_to_result_field_and_counts_nothing() {
    for target in targets("retries_success") {
        let engine = world(&target, with(base(), json!({"resultField": "report"})));
        assert_eq!(
            retries_of(&engine),
            json!({"total": 0, "classAttempts": counts(0, 0, 0), "exhausted": false, "stuck": false})
        );
        assert_eq!(
            attempt(&engine, json!({"score": 0.5, "result": {"ok": true}})),
            json!({"failure": null, "score": 0.5, "kept": true, "total": 0, "classAttempts": counts(0, 0, 0), "exhausted": false, "stuck": false, "hint": null})
        );
        assert_eq!(data_of(&engine)["report"], json!({"ok": true}));
        assert_eq!(
            attempt(&engine, json!({"result": {"ok": "again"}}))["score"],
            Value::Null
        );
        assert_eq!(
            (
                data_of(&engine)["report"].clone(),
                retries_of(&engine)["bestScore"].clone()
            ),
            (json!({"ok": "again"}), json!(0.5))
        );
    }
}

#[test]
fn a_failure_names_a_class_a_result_needs_result_field_and_each_attempt_is_one_event() {
    for target in targets("retries_params") {
        let engine = world(&target, base());
        assert_eq!(
            issues_of(&refusal(try_attempt(&engine, json!({"failure": "oom"})))),
            [issue(
                "/failure",
                "oom is not a failure class of Job (its classes: invalid, rejected, timeout)"
            )]
        );
        assert_eq!(
            refusal(try_attempt(&engine, json!({"failure": ""}))).code(),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            issues_of(&refusal(try_attempt(&engine, json!({"result": "done"})))),
            [issue(
                "/result",
                "the config names no resultField to keep a result in"
            )]
        );
        let seq = jobs(&engine).seq("j1");
        attempt(
            &engine,
            json!({"failure": "timeout", "signature": "deadline"}),
        );
        invoke(
            &engine,
            &other(),
            "recordAttempt",
            json!({"failure": "invalid"}),
            "j1",
        )
        .unwrap();
        let events = jobs(&engine).events("j1");
        let last: Vec<Value> = events[events.len() - 2..]
            .iter()
            .map(|event| json!([event.seq, event.actor, event.change["params"]]))
            .collect();
        assert_eq!(
            last,
            [
                json!([seq + 1, "wren", {"failure": "timeout", "signature": "deadline"}]),
                json!([seq + 2, "otto", {"failure": "invalid"}])
            ]
        );
    }
}

#[test]
fn a_terminal_class_exhausts_the_instance_at_once_and_it_is_not_taken_again() {
    for target in targets("retries_terminal") {
        let engine = world(&target, base());
        invoke(&engine, &worker(), "acquire", json!({}), "j1").unwrap();
        invoke(
            &engine,
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
        )
        .unwrap();
        assert_eq!(
            attempt(&engine, json!({"failure": "rejected"})),
            json!({"failure": "rejected", "score": null, "kept": false, "total": 1, "classAttempts": counts(0, 0, 1), "exhausted": true, "stuck": false, "hint": null})
        );
        assert_eq!(status_of(&engine), "failed");
        jobs(&engine)
            .invoke_on(&worker(), "j1", "release", json!({}), Some(1))
            .unwrap();
        assert_eq!(
            veto(invoke(&engine, &other(), "acquire", json!({}), "j1")).2,
            "its retries are exhausted, so it is not taken again"
        );
        assert_eq!(
            veto(try_attempt(&engine, json!({"failure": "timeout"}))).2,
            "its retries are exhausted"
        );
    }
}

#[test]
fn failures_count_against_their_class_and_the_total_and_the_total_exhausts() {
    for target in targets("retries_total") {
        let engine = world(&target, base());
        jobs(&engine).create("j2", json!({"title": "Second"}));
        for n in 1..=3 {
            assert_eq!(
                attempt(&engine, json!({"failure": "timeout"})),
                with(attempt0(n), json!({"classAttempts": counts(n, 0, 0)}))
            );
        }
        // timeout is at its cap, but invalid has room; the total reaches its
        // cap.
        assert_eq!(
            attempt(&engine, json!({"failure": "invalid"})),
            json!({"failure": "invalid", "score": null, "kept": false, "total": 4, "classAttempts": counts(3, 1, 0), "exhausted": true, "stuck": false, "hint": null})
        );
        assert_eq!(status_of(&engine), "failed");
    }
}

#[test]
fn a_failure_of_a_class_with_no_room_left_is_not_counted_and_exhausts() {
    for target in targets("retries_no_room") {
        let engine = world(&target, base());
        for _ in 0..3 {
            attempt(&engine, json!({"failure": "timeout"}));
        }
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout"})),
            with(
                attempt0(3),
                json!({"exhausted": true, "classAttempts": counts(3, 0, 0)})
            )
        );
    }
}

#[test]
fn a_class_that_reaches_its_cap_while_no_other_class_has_room_exhausts() {
    for target in targets("retries_class_cap") {
        let engine = world(&target, with(base(), json!({"totalAttempts": 10})));
        attempt(&engine, json!({"failure": "invalid"}));
        assert_eq!(
            attempt(&engine, json!({"failure": "invalid"}))["exhausted"],
            false
        );
        attempt(&engine, json!({"failure": "timeout"}));
        attempt(&engine, json!({"failure": "timeout"}));
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout"})),
            with(
                attempt0(5),
                json!({"exhausted": true, "classAttempts": counts(3, 2, 0)})
            )
        );
    }
}

#[test]
fn interleaved_failures_never_pass_a_class_cap_or_the_total() {
    for target in targets("retries_interleaved") {
        let engine = world(&target, with(base(), json!({"totalAttempts": 6})));
        // A fixed pseudo-random sequence of classes, over many instances.
        let mut seed: i64 = 7;
        let mut next = || {
            seed = (seed * 1_103_515_245 + 12345) % 2_147_483_648;
            seed
        };
        let names = ["timeout", "invalid"];
        for instance in 0..20 {
            let id = format!("p{instance}");
            jobs(&engine).create(&id, json!({ "title": id }));
            let mut last: Option<Value> = None;
            for _ in 0..12 {
                if last.as_ref().is_some_and(|last| last["exhausted"] == true) {
                    break;
                }
                let attempt = attempt_on(
                    &engine,
                    json!({"failure": names[(next() % 2) as usize]}),
                    &id,
                );
                let (timeout, invalid, total) = (
                    attempt["classAttempts"]["timeout"].as_i64().unwrap(),
                    attempt["classAttempts"]["invalid"].as_i64().unwrap(),
                    attempt["total"].as_i64().unwrap(),
                );
                assert!(timeout <= 3 && invalid <= 2 && total <= 6, "{attempt}");
                assert_eq!(total, timeout + invalid);
                last = Some(attempt);
            }
            assert_eq!(last.unwrap()["exhausted"], true, "{id} exhausted");
        }
    }
}

#[test]
fn limits_field_narrows_the_caps_for_the_instance() {
    for target in targets("retries_limits_narrow") {
        let engine = world_with(
            &target,
            with(base(), json!({"limitsField": "caps"})),
            json!({"caps": {"timeout": 1}}),
            json!([{"name": "Lease"}]),
        );
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout"}))["exhausted"],
            false
        );
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout"})),
            with(
                attempt0(1),
                json!({"exhausted": true, "classAttempts": counts(1, 0, 0)})
            )
        );
    }
}

#[test]
fn limits_field_narrows_the_total_for_the_instance() {
    for target in targets("retries_limits_total") {
        let engine = world_with(
            &target,
            with(base(), json!({"limitsField": "caps"})),
            json!({"caps": {"totalAttempts": 2, "timeout": 5}}),
            json!([{"name": "Lease"}]),
        );
        attempt(&engine, json!({"failure": "timeout"}));
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout"})),
            with(
                attempt0(2),
                json!({"exhausted": true, "classAttempts": counts(2, 0, 0)})
            )
        );
    }
}

#[test]
fn limits_field_ignores_what_is_not_a_cap_of_a_class_with_one() {
    for target in targets("retries_limits_ignored") {
        // Unknown and terminal classes, and values that are no cap, change
        // nothing.
        let engine = world_with(
            &target,
            with(base(), json!({"limitsField": "caps"})),
            json!({"caps": {"oom": 1, "rejected": 5, "timeout": -1, "invalid": 1.5, "totalAttempts": 0}}),
            json!([{"name": "Lease"}]),
        );
        for _ in 1..=3 {
            assert_eq!(
                attempt(&engine, json!({"failure": "timeout"}))["exhausted"],
                false
            );
        }
        assert_eq!(
            attempt(&engine, json!({"failure": "invalid"}))["exhausted"],
            true
        );
    }
}

#[test]
fn a_cap_of_zero_makes_the_first_failure_of_that_class_exhaust() {
    for target in targets("retries_limits_zero") {
        // Nothing is exhausted before it, and other classes count as before.
        let engine = world_with(
            &target,
            with(base(), json!({"limitsField": "caps"})),
            json!({"caps": {"timeout": 0}}),
            json!([{"name": "Lease"}]),
        );
        assert_eq!(retries_of(&engine)["exhausted"], false);
        assert_eq!(
            invoke(&engine, &worker(), "acquire", json!({}), "j1").unwrap()["token"],
            1
        );
        assert_eq!(
            attempt(&engine, json!({"failure": "invalid"})),
            with(
                attempt0(1),
                json!({"failure": "invalid", "classAttempts": counts(0, 1, 0)})
            )
        );
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout"})),
            with(
                attempt0(1),
                json!({"exhausted": true, "classAttempts": counts(0, 1, 0)})
            )
        );
    }
}

#[test]
fn a_failure_returns_its_class_hint_and_an_attempt_keeps_its_detail_in_its_event() {
    for target in targets("retries_hint") {
        let mut hinted = classes();
        hinted["invalid"] = json!({"attempts": 2, "hint": "Return JSON that matches the schema."});
        let engine = world(&target, with(base(), json!({ "classes": hinted })));
        let detail = json!({"errors": [{"path": "/title", "message": "required"}]});
        assert_eq!(
            attempt(&engine, json!({"failure": "invalid", "detail": detail}))["hint"],
            "Return JSON that matches the schema."
        );
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout"}))["hint"],
            Value::Null
        );
        assert_eq!(attempt(&engine, json!({}))["hint"], Value::Null);
        let events = jobs(&engine).events("j1");
        assert_eq!(
            events[events.len() - 3].change["params"],
            json!({"failure": "invalid", "detail": detail})
        );
        assert_eq!(
            refusal(try_attempt(&engine, json!({"detail": "not an object"}))).code(),
            EngineErrorCode::InvalidArgument
        );
    }
}

#[test]
fn limits_field_changes_only_with_limits_permission_and_never_by_the_holder_of_the_lease() {
    for target in targets("retries_limits_permission") {
        let config = with(
            base(),
            json!({"limitsField": "caps", "limitsPermission": "jobs.caps"}),
        );
        let lead = principal("lena", &["jobs.caps"]);
        let engine = world_with(
            &target,
            config,
            json!({"caps": {"timeout": 1}}),
            json!([{"name": "Lease"}]),
        );
        let refused =
            refusal(jobs(&engine).update(&worker(), "j1", json!({"caps": {"timeout": 9}}), None));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "wren may not change caps, the caps of Job j1: it needs permission jobs.caps"
            )
        );
        jobs(&engine)
            .update(&lead, "j1", json!({"caps": {"timeout": 2}}), None)
            .unwrap();
        // The holder of the lease, permission or not, leaves them as they
        // are; an update that leaves them passes.
        let holder = principal("hal", &["jobs.caps"]);
        invoke(&engine, &holder, "acquire", json!({}), "j1").unwrap();
        let (behavior, _, _, code, _) =
            veto(jobs(&engine).update(&holder, "j1", json!({"caps": {"timeout": 9}}), None));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Retries", Some("limits_fixed"))
        );
        jobs(&engine)
            .update(
                &holder,
                "j1",
                json!({"title": "Renamed", "caps": {"timeout": 2}}),
                None,
            )
            .unwrap();
        assert_eq!(data_of(&engine)["caps"], json!({"timeout": 2}));
    }
}

#[test]
fn limits_field_does_not_change_once_the_instance_exists_without_limits_permission() {
    for target in targets("retries_limits_unnamed") {
        let engine = world_with(
            &target,
            with(base(), json!({"limitsField": "caps"})),
            json!({"caps": {"timeout": 1}}),
            json!([{"name": "Lease"}]),
        );
        let (_, _, reason, code, _) =
            veto(jobs(&engine).update(&alice(), "j1", json!({"caps": {"timeout": 9}}), None));
        assert_eq!(
            (reason.as_str(), code.as_deref()),
            (
                "caps holds the instance's caps, which change only with limitsPermission, and the config names none",
                Some("not_configured")
            )
        );
    }
}

#[test]
fn a_config_whose_classes_are_all_terminal_exhausts_nothing_before_the_first_failure() {
    for target in targets("retries_all_terminal") {
        let engine = world(
            &target,
            json!({"classes": {"rejected": "terminal", "crashed": "terminal"}, "totalAttempts": 3, "exhaustedState": "failed"}),
        );
        assert_eq!(
            retries_of(&engine),
            json!({"total": 0, "classAttempts": {"crashed": 0, "rejected": 0}, "exhausted": false, "stuck": false})
        );
        assert_eq!(
            invoke(&engine, &worker(), "acquire", json!({}), "j1").unwrap()["token"],
            1
        );
        invoke(
            &engine,
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
        )
        .unwrap();
        assert_eq!(attempt(&engine, json!({}))["exhausted"], false);
        assert_eq!(
            attempt(&engine, json!({"failure": "crashed"}))["exhausted"],
            true
        );
        assert_eq!(status_of(&engine), "failed");
    }
}

// ---- kept results and stuck failures ----

#[test]
fn with_keep_best_a_failure_is_kept_only_when_it_beats_the_best_and_loses_no_predicate() {
    for target in targets("retries_keep_best") {
        let engine = world(
            &target,
            with(
                base(),
                json!({"totalAttempts": 10, "resultField": "report", "keepBest": {"minDelta": 0.1, "neverRegress": ["compiles"]}}),
            ),
        );
        let failed = |score: f64, compiles: bool, v: i64| {
            attempt(
                &engine,
                json!({"failure": "timeout", "score": score, "predicates": {"compiles": compiles}, "result": {"v": v}}),
            )
        };
        assert_eq!(failed(0.5, true, 1)["kept"], true);
        assert_eq!(failed(0.55, true, 2)["kept"], false);
        // A higher score that loses a predicate the kept result had is
        // recorded, not kept.
        assert_eq!(
            failed(0.9, false, 3),
            with(
                attempt0(3),
                json!({"score": 0.9, "classAttempts": counts(3, 0, 0)})
            )
        );
        assert_eq!(
            (
                data_of(&engine)["report"].clone(),
                retries_of(&engine)["bestScore"].clone()
            ),
            (json!({"v": 1}), json!(0.5))
        );
        // A predicate the attempt does not report counts as failed.
        assert_eq!(
            attempt(
                &engine,
                json!({"failure": "invalid", "score": 0.9, "result": {"v": 4}})
            )["kept"],
            false
        );
        assert_eq!(
            attempt(
                &engine,
                json!({"failure": "invalid", "score": 0.9, "predicates": {"compiles": true}, "result": {"v": 5}})
            )["kept"],
            true
        );
        assert_eq!(
            (
                data_of(&engine)["report"].clone(),
                retries_of(&engine)["bestScore"].clone()
            ),
            (json!({"v": 5}), json!(0.9))
        );
    }
}

#[test]
fn without_keep_best_only_a_successs_result_is_kept() {
    for target in targets("retries_success_only") {
        let engine = world(&target, with(base(), json!({"resultField": "report"})));
        assert_eq!(
            attempt(&engine, json!({"score": 0.2, "result": {"v": "success"}}))["kept"],
            true
        );
        assert_eq!(
            attempt(
                &engine,
                json!({"failure": "timeout", "score": 0.9, "result": {"v": "failure"}})
            )["kept"],
            false
        );
        assert_eq!(
            (
                data_of(&engine)["report"].clone(),
                retries_of(&engine)["bestScore"].clone()
            ),
            (json!({"v": "success"}), json!(0.2))
        );
    }
}

#[test]
fn stuck_after_the_same_signature_in_a_row_exhausts_as_stuck() {
    for target in targets("retries_stuck") {
        let engine = world(
            &target,
            with(base(), json!({"totalAttempts": 10, "stuckAfter": 2})),
        );
        invoke(&engine, &worker(), "acquire", json!({}), "j1").unwrap();
        invoke(
            &engine,
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
        )
        .unwrap();
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout", "signature": "a"}))["stuck"],
            false
        );
        assert_eq!(
            attempt(&engine, json!({"failure": "invalid", "signature": "a"})),
            with(
                attempt0(2),
                json!({"failure": "invalid", "classAttempts": counts(1, 1, 0), "exhausted": true, "stuck": true})
            )
        );
        assert_eq!(status_of(&engine), "failed");
        assert_eq!(
            veto(try_attempt(&engine, json!({}))).2,
            "its retries are exhausted, stuck on one failure"
        );
    }
}

#[test]
fn another_signature_a_kept_attempt_or_a_success_ends_the_streak() {
    for target in targets("retries_streak") {
        let wide = json!({"timeout": {"attempts": 5}, "invalid": {"attempts": 5}});
        let engine = world(
            &target,
            with(
                base(),
                json!({"classes": wide, "totalAttempts": 10, "stuckAfter": 2, "keepBest": {}}),
            ),
        );
        attempt(&engine, json!({"failure": "timeout", "signature": "a"}));
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout", "signature": "b"}))["stuck"],
            false
        );
        // A kept attempt with the same signature ends the streak.
        assert_eq!(
            attempt(
                &engine,
                json!({"failure": "invalid", "signature": "b", "score": 1})
            )["kept"],
            true
        );
        assert_eq!(
            attempt(
                &engine,
                json!({"failure": "invalid", "signature": "b", "score": 0.5})
            )["stuck"],
            false
        );
        attempt(&engine, json!({}));
        assert_eq!(
            attempt(&engine, json!({"failure": "timeout", "signature": "b"}))["stuck"],
            false
        );
        assert_eq!(
            attempt(&engine, json!({"failure": "invalid", "signature": "b"}))["stuck"],
            true
        );
    }
}

#[test]
fn a_result_is_written_through_update_so_one_the_live_version_refuses_fails_the_attempt() {
    for target in targets("retries_result_invalid") {
        let engine = world(&target, with(base(), json!({"resultField": "output"})));
        let seq = jobs(&engine).seq("j1");
        assert_eq!(
            refusal(try_attempt(&engine, json!({"result": "x".repeat(21)}))).code(),
            EngineErrorCode::InvalidInstance
        );
        assert_eq!(
            refusal(try_attempt(&engine, json!({"result": 42}))).code(),
            EngineErrorCode::InvalidInstance
        );
        assert_eq!(jobs(&engine).seq("j1"), seq);
        assert_eq!(attempts_rows(&engine), 0);
        attempt(&engine, json!({"result": "built"}));
        assert_eq!(data_of(&engine)["output"], "built");
    }
}

#[test]
fn a_result_state_rules_freezes_refuses_the_attempt_that_would_write_it() {
    for target in targets("retries_result_frozen") {
        let engine = world_with(
            &target,
            with(base(), json!({"resultField": "output"})),
            json!({}),
            json!([{"name": "Lease"}, {"name": "StateRules", "config": {"states": {"queued": {"frozen": ["output"]}}}}]),
        );
        let seq = jobs(&engine).seq("j1");
        let (behavior, action, _, code, details) =
            veto(try_attempt(&engine, json!({"result": "built"})));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref(), details),
            (
                "StateRules",
                "update",
                Some("frozen_fields"),
                Some(json!({"state": "queued", "fields": ["output"]}))
            )
        );
        assert_eq!(jobs(&engine).seq("j1"), seq);
        assert_eq!(attempts_rows(&engine), 0);
        // An attempt that keeps no result writes nothing StateRules
        // freezes.
        assert_eq!(attempt(&engine, json!({"failure": "timeout"}))["total"], 1);
        invoke(&engine, &worker(), "acquire", json!({}), "j1").unwrap();
        invoke(
            &engine,
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
        )
        .unwrap();
        attempt(&engine, json!({"result": "built"}));
        assert_eq!(data_of(&engine)["output"], "built");
    }
}

// ---- exhaustion and access ----

#[test]
fn exhaustion_moves_the_status_only_from_the_from_states_so_finished_work_stays_finished() {
    for target in targets("retries_finished") {
        let engine = world(&target, base());
        invoke(&engine, &worker(), "acquire", json!({}), "j1").unwrap();
        for to in ["running", "done"] {
            invoke(&engine, &worker(), "transition", json!({ "to": to }), "j1").unwrap();
        }
        assert_eq!(
            attempt(&engine, json!({"failure": "rejected"}))["exhausted"],
            true
        );
        assert_eq!(status_of(&engine), "done");
    }
}

#[test]
fn exhausted_its_status_moves_only_into_exhausted_state() {
    for target in targets("retries_narrow") {
        let engine = world(&target, with(base(), json!({"from": ["running"]})));
        assert_eq!(
            attempt(&engine, json!({"failure": "rejected"}))["exhausted"],
            true
        );
        assert_eq!(status_of(&engine), "queued");
        assert_eq!(
            veto(invoke(
                &engine,
                &alice(),
                "transition",
                json!({"to": "running"}),
                "j1"
            ))
            .2,
            "its retries are exhausted, so its status moves only to failed"
        );
        invoke(
            &engine,
            &alice(),
            "transition",
            json!({"to": "failed"}),
            "j1",
        )
        .unwrap();
        assert_eq!(status_of(&engine), "failed");
    }
}

#[test]
fn record_attempt_needs_the_permission_the_config_names() {
    for target in targets("retries_permission") {
        let engine = world(&target, with(base(), json!({"permission": "jobs.work"})));
        let refused = refusal(try_attempt(&engine, json!({"failure": "timeout"})));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "wren may not record an attempt of Job j1: it needs permission jobs.work"
            )
        );
        assert_eq!(
            invoke(
                &engine,
                &principal("wren", &["jobs.work"]),
                "recordAttempt",
                json!({"failure": "timeout"}),
                "j1"
            )
            .unwrap()["total"],
            1
        );
    }
}

#[test]
fn while_a_lease_is_active_leases_guard_keeps_record_attempt_to_its_holder() {
    for target in targets("retries_leased") {
        let engine = world(&target, base());
        invoke(&engine, &worker(), "acquire", json!({}), "j1").unwrap();
        assert_eq!(
            veto(invoke(
                &engine,
                &other(),
                "recordAttempt",
                json!({"failure": "timeout"}),
                "j1"
            ))
            .0,
            "Lease"
        );
        assert_eq!(attempt(&engine, json!({"failure": "timeout"}))["total"], 1);
    }
}

// ---- claimed through Queue ----

// queued opens an engine whose Job composes Workflow, Lease, Queue and
// Retries with the config given, with jobs j1 and j2 queued, j1 the older.
fn queued(target: &Target, config: Value) -> Engine {
    let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(T0));
    publish(
        &engine,
        &retries_document(json!([
            {"name": "Workflow", "config": job_flow()},
            {"name": "Lease", "config": {"onExpiry": {"transition": "queued", "from": ["running"]}}},
            {"name": "Queue", "config": {"claim": {"from": ["queued"], "to": "running"}}},
            {"name": "Retries", "config": config}
        ])),
    );
    for id in ["j1", "j2"] {
        jobs(&engine).create(id, json!({ "title": id }));
    }
    engine
}

fn claim_next(engine: &Engine, who: &Principal) -> Value {
    jobs(engine)
        .invoke_schema(who, "claimNext", json!({}))
        .unwrap()["claimed"]
        .clone()
}

#[test]
fn an_exhausted_instance_still_in_a_claimable_state_is_not_claimed() {
    for target in targets("retries_queue_exhausted") {
        let engine = queued(&target, with(base(), json!({"from": ["running"]})));
        assert_eq!(
            attempt(&engine, json!({"failure": "rejected"}))["exhausted"],
            true
        );
        assert_eq!(status_of(&engine), "queued");
        assert_eq!(
            veto(invoke(&engine, &worker(), "claim", json!({}), "j1")).2,
            "its retries are exhausted, so it is not taken again"
        );
        assert_eq!(
            jobs(&engine)
                .invoke_schema(&worker(), "countClaimable", json!({}))
                .unwrap()["count"],
            1
        );
        assert_eq!(claim_next(&engine, &worker())["id"], "j2");
        assert_eq!(
            (
                status_of(&engine),
                jobs(&engine).behavior("j1", "Lease").get("holder").cloned()
            ),
            (json!("queued"), None)
        );
        assert_eq!(claim_next(&engine, &other()), Value::Null);
    }
}

#[test]
fn a_claimed_job_that_fails_until_its_caps_run_out_moves_to_exhausted_state_and_out_of_the_queue() {
    for target in targets("retries_queue_caps") {
        let engine = queued(&target, base());
        for _ in 1..=3 {
            let claimed = claim_next(&engine, &worker());
            assert_eq!(claimed["id"], "j1");
            assert_eq!(
                attempt(&engine, json!({"failure": "timeout"}))["exhausted"],
                false
            );
            jobs(&engine)
                .invoke_on(
                    &worker(),
                    "j1",
                    "release",
                    json!({}),
                    claimed["token"].as_i64(),
                )
                .unwrap();
            assert_eq!(status_of(&engine), "queued");
        }
        let last = claim_next(&engine, &worker());
        assert_eq!(
            attempt(&engine, json!({"failure": "invalid"})),
            with(
                attempt0(4),
                json!({"failure": "invalid", "classAttempts": counts(3, 1, 0), "exhausted": true})
            )
        );
        assert_eq!(status_of(&engine), "failed");
        jobs(&engine)
            .invoke_on(
                &worker(),
                "j1",
                "release",
                json!({}),
                last["token"].as_i64(),
            )
            .unwrap();
        assert_eq!(status_of(&engine), "failed");
        assert_eq!(claim_next(&engine, &worker())["id"], "j2");
    }
}

// ---- its config ----

#[test]
fn parse_config_holds_the_config_to_the_types_fields_and_its_workflows_states() {
    for target in targets("retries_config") {
        let engine = target.open_workqueue();
        let refused = |config: Value| {
            define_refusal(
                &engine,
                &retries_document(json!([
                    {"name": "Workflow", "config": job_flow()},
                    {"name": "Retries", "config": config}
                ])),
            )
        };
        for (config, wanted) in [
            (with(base(), json!({"exhaustedState": "dead"})), "behavior Retries config: exhaustedState \"dead\" is not a state of the type's Workflow (queued, running, done, failed)"),
            (with(base(), json!({"from": ["paused"]})), "from names \"paused\", which is not a state of the type's Workflow"),
            (with(base(), json!({"from": ["failed"]})), "from names exhaustedState \"failed\", which exhaustion cannot move the status from"),
            (with(base(), json!({"from": ["done"]})), "from: no transition of the type's Workflow leads from \"done\" to \"failed\""),
            (with(base(), json!({"resultField": "result"})), "resultField \"result\" is not a field of Job (its fields: title, output, report, caps)"),
            (with(base(), json!({"limitsField": "title"})), "limitsField \"title\" is not an object field of Job"),
        ] {
            let message = refused(config.clone());
            assert!(message.contains(wanted), "{config}: {message}");
        }
        // The core meta-schema holds the shape first, Workflow among the
        // requirements.
        let shape = refusal(engine.schemas().define(
            &alice(),
            retries_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Retries", "config": {"classes": classes(), "exhaustedState": "failed"}}
            ])),
            Default::default(),
        ));
        assert!(
            shape
                .issues()
                .iter()
                .any(|issue| issue.path == "/types/Job/behaviors/1/config"),
            "{shape}"
        );
        let required = define_refusal(
            &engine,
            &retries_document(json!([{"name": "Retries", "config": base()}])),
        );
        let requires = required.find("requires").expect("a requirement");
        assert!(required[requires..].contains("Workflow"), "{required}");
        publish(
            &engine,
            &retries_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Retries", "config": with(base(), json!({"limitsField": "caps", "resultField": "report", "from": ["running"]}))}
            ])),
        );
    }
}

#[test]
fn it_can_be_added_to_a_schema_that_has_instances_and_cannot_be_removed_from_one() {
    for target in targets("retries_config_change") {
        let engine = target.open_workqueue();
        let flow = json!({"name": "Workflow", "config": job_flow()});
        publish(&engine, &retries_document(json!([flow])));
        jobs(&engine).create("j1", json!({"title": "Before"}));
        publish(
            &engine,
            &retries_document(json!([flow, {"name": "Retries", "config": base()}])),
        );
        assert_eq!(attempt(&engine, json!({"failure": "timeout"}))["total"], 1);
        publish(
            &engine,
            &retries_document(
                json!([flow, {"name": "Retries", "config": with(base(), json!({"totalAttempts": 8}))}]),
            ),
        );
        let refused = define_refusal(&engine, &retries_document(json!([flow])));
        assert!(
            refused.contains("behavior Retries cannot be removed from type Job, which has instances: the attempts its instances recorded would stay behind"),
            "{refused}"
        );
    }
}
