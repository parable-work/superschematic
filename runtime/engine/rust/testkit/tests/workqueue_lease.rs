//! Lease (D16), the work queue's exclusive, time-bounded lease, on the
//! Rust engine (D58): the cases of
//! `runtime/engine-workqueue/typescript/test/lease.test.ts` (acquire,
//! heartbeat and release with the fencing token, presented as Lease's
//! precondition; the guard that keeps the lease exclusive and refuses a
//! write that presents a stale token, whoever calls, and with requireToken
//! one that presents none; expiry, applied once by expire, by an acquire
//! over a lapsed lease or by the runner's sweep, with onExpiry and escalate
//! moving the status only through Workflow's transition and only from
//! their from states, and the reason it gives; an abandon; a gone holder's
//! leases; maxExpiries and resetExpiries; the longest hold; directives
//! tied to the lease they were sent under, from a principal or another
//! behavior; the codes its refusals carry; and the config rules), on
//! SQLite and, with the variable set, on Postgres. A TypeScript case that
//! opens a second engine is a test of its own here. The guidance is held
//! to the TypeScript text in the work queue's crate
//! (`tests/guidance_lease.rs`).

mod support;
mod workqueue_support;

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use serde_json::{json, Value};

use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Clock, DriverError, Engine, EngineErrorCode, EngineOptions, Error, Principal, RunnerOptions,
    RunnerPass, ValueDriver, ValueOptions, WriteOptions,
};
use superschematic_engine_testkit::workqueue_fixtures::{Gate, Signal};
use superschematic_engine_workqueue::Lease;
use support::{alice, code, count, issue, issues_of, refusal, targets, veto, Target};
use workqueue_support::{
    advance, cause_of, define_refusal, iso, job_flow, jobs_document, principal, publish,
    row_values, run_due, Instances, T0,
};

fn worker() -> Principal {
    principal("wren", &[])
}

fn other() -> Principal {
    principal("otto", &[])
}

fn operator() -> Principal {
    principal("opal", &["jobs.override"])
}

fn sender() -> Principal {
    principal("sid", &["jobs.direct"])
}

fn runner() -> Principal {
    principal("runner", &[])
}

fn requeue() -> Value {
    json!({"onExpiry": {"transition": "queued", "from": ["running"]}})
}

// with merges members into a config.
fn with(config: Value, more: Value) -> Value {
    let mut config = config;
    if let (Value::Object(config), Value::Object(more)) = (&mut config, more) {
        config.extend(more);
    }
    config
}

// lease is Lease's entry of a type's behaviors: with its config, or none
// when it is null.
fn lease(config: &Value) -> Value {
    if config.is_null() {
        json!({"name": "Lease"})
    } else {
        json!({"name": "Lease", "config": config})
    }
}

/// An engine whose Job composes Workflow, Lease with a config, Comments and
/// more, with job j1 queued, on a clock at T0.
struct World {
    engine: Engine,
}

impl World {
    fn jobs(&self) -> Instances<'_> {
        Instances {
            engine: &self.engine,
            schema: "Job",
        }
    }

    // invoke calls an operation of a job, presenting a token as Lease's
    // precondition when one is given.
    fn invoke(
        &self,
        who: &Principal,
        operation: &str,
        params: Value,
        id: &str,
        token: Option<i64>,
    ) -> Result<Value, Error> {
        self.jobs().invoke_on(who, id, operation, params, token)
    }

    fn call(&self, who: &Principal, operation: &str) -> Result<Value, Error> {
        self.invoke(who, operation, json!({}), "j1", None)
    }

    fn lease_of(&self, id: &str) -> Value {
        self.jobs().behavior(id, "Lease")
    }

    fn status_of(&self, id: &str) -> Value {
        self.jobs().status(id)
    }

    fn advance(&self, ms: i64) -> i64 {
        advance(&self.engine, ms)
    }

    fn token(&self, who: &Principal, id: &str) -> i64 {
        self.invoke(who, "acquire", json!({}), id, None)
            .expect("acquire")["token"]
            .as_i64()
            .expect("a token")
    }
}

fn world_with(
    target: &Target,
    config: Value,
    extra: &[Value],
    configure: impl FnOnce(&mut EngineOptions),
) -> World {
    let engine = target.open_workqueue_with(|options| {
        options.clock = Clock::Manual(T0);
        configure(options);
    });
    let mut behaviors = vec![
        json!({"name": "Workflow", "config": job_flow()}),
        lease(&config),
        json!({"name": "Comments"}),
    ];
    behaviors.extend(extra.iter().cloned());
    publish(&engine, &jobs_document(Value::Array(behaviors)));
    let world = World { engine };
    world.jobs().create("j1", json!({"title": "Build"}));
    world
}

fn world(target: &Target, config: Value) -> World {
    world_with(target, config, &[], |_| {})
}

// reason is a veto's reason.
fn reason<T: std::fmt::Debug>(result: Result<T, Error>) -> String {
    veto(result).2
}

// veto_code is a veto's code.
fn veto_code<T: std::fmt::Debug>(result: Result<T, Error>) -> Option<String> {
    veto(result).3
}

// forbidden is a forbidden refusal's message.
fn forbidden<T: std::fmt::Debug>(result: Result<T, Error>) -> String {
    let refused = refusal(result);
    assert_eq!(refused.code(), EngineErrorCode::Forbidden, "{refused}");
    refused.message().to_owned()
}

// ---- acquire, heartbeat and release ----

#[test]
fn acquire_takes_the_lease_with_token_1_and_the_default_length_and_heartbeat() {
    for target in targets("lease_acquire") {
        let w = world(&target, Value::Null);
        assert_eq!(
            w.lease_of("j1"),
            json!({"token": 0, "active": false, "expiries": 0})
        );
        assert_eq!(
            w.call(&worker(), "acquire").unwrap(),
            json!({"token": 1, "expiresAt": T0 + 60000, "heartbeatMs": 20000})
        );
        assert_eq!(
            w.lease_of("j1"),
            json!({"holder": "wren", "token": 1, "acquiredAt": T0, "renewedAt": T0, "expiresAt": T0 + 60000, "active": true, "expiries": 0})
        );
    }
}

#[test]
fn acquire_is_refused_while_the_lease_is_active_to_another_principal_and_to_the_holder() {
    for target in targets("lease_acquire_refused") {
        let w = world(&target, Value::Null);
        w.call(&worker(), "acquire").unwrap();
        let (behavior, _, why, _, _) = veto(w.call(&other(), "acquire"));
        assert_eq!(behavior, "Lease");
        assert_eq!(
            why,
            format!(
                "another principal holds its lease, until {}",
                iso(T0 + 60000)
            )
        );
        assert_eq!(
            reason(w.call(&worker(), "acquire")),
            format!(
                "the caller already holds its lease, until {}; heartbeat renews it",
                iso(T0 + 60000)
            )
        );
        assert_eq!(w.lease_of("j1")["token"], 1);
    }
}

#[test]
fn a_heartbeat_renews_the_lease_and_needs_the_holder_its_current_token_and_an_active_lease() {
    for target in targets("lease_heartbeat") {
        let w = world(&target, Value::Null);
        w.call(&worker(), "acquire").unwrap();
        w.invoke(&worker(), "release", json!({}), "j1", Some(1))
            .unwrap();
        assert_eq!(w.token(&worker(), "j1"), 3);
        w.advance(30000);
        let seq = w.jobs().seq("j1");
        assert_eq!(
            w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(3))
                .unwrap(),
            json!({"expiresAt": T0 + 90000, "directives": []})
        );
        // Each heartbeat is a writing operation, with its event.
        assert_eq!(w.jobs().seq("j1"), seq + 1);
        // A process of the holder still on the lease it released is stale.
        assert_eq!(
            reason(w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(1))),
            "token 1 is stale: the lease is at token 3"
        );
        // The current token does not make another principal the holder.
        assert_eq!(
            reason(w.invoke(&other(), "heartbeat", json!({}), "j1", Some(3))),
            "another principal holds its lease"
        );
        // Past its expiry time, though no one has applied the expiry yet,
        // the lease cannot be renewed.
        w.advance(60000);
        assert!(
            reason(w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(3)))
                .starts_with(&format!("its lease expired at {}", iso(T0 + 90000)))
        );
        assert_eq!(
            w.lease_of("j1"),
            json!({"holder": "wren", "token": 3, "acquiredAt": T0, "renewedAt": T0 + 30000, "expiresAt": T0 + 90000, "active": false, "expiries": 0})
        );
    }
}

/// A value driver over a map that counts its reads and writes.
#[derive(Default)]
struct MapDriver {
    values: Mutex<BTreeMap<String, String>>,
    reads: AtomicUsize,
    writes: AtomicUsize,
}

impl ValueDriver for MapDriver {
    fn read(&self, hash: &str) -> std::result::Result<Option<String>, DriverError> {
        self.reads.fetch_add(1, Ordering::SeqCst);
        Ok(self.values.lock().unwrap().get(hash).cloned())
    }

    fn write(&self, hash: &str, json: &str) -> std::result::Result<(), DriverError> {
        self.writes.fetch_add(1, Ordering::SeqCst);
        self.values
            .lock()
            .unwrap()
            .entry(hash.to_owned())
            .or_insert_with(|| json.to_owned());
        Ok(())
    }

    fn remove(&self, hash: &str) -> std::result::Result<(), DriverError> {
        self.values.lock().unwrap().remove(hash);
        Ok(())
    }
}

#[test]
fn a_heartbeat_neither_rewrites_nor_loads_a_large_field_the_value_store_holds() {
    for target in targets("lease_heartbeat_values") {
        let driver = Arc::new(MapDriver::default());
        let engine = target.open_workqueue_with(|options| {
            options.clock = Clock::Manual(T0);
            options.values = ValueOptions {
                threshold_bytes: Some(1024),
                driver: Some(Arc::clone(&driver) as Arc<dyn ValueDriver>),
                cache_bytes: Some(0),
                ..ValueOptions::default()
            };
        });
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease"},
                {"name": "Comments"}
            ])),
        );
        let jobs = Instances {
            engine: &engine,
            schema: "Job",
        };
        let topic = "build log ".repeat(200);
        jobs.create("j1", json!({"title": "Build", "topic": topic}));
        let row = || {
            row_values(
                &engine,
                "SELECT data, value_refs FROM engine_instances WHERE id = 'j1'",
            )
        };
        let counts = || {
            (
                driver.reads.load(Ordering::SeqCst),
                driver.writes.load(Ordering::SeqCst),
            )
        };
        let before = row();
        assert_eq!(before[1], SqlValue::from("[\"/topic\"]"));
        assert_eq!(counts(), (0, 1));
        jobs.invoke_on(&worker(), "j1", "acquire", json!({}), None)
            .unwrap();
        for _ in 1..=3 {
            advance(&engine, 10000);
            jobs.invoke_on(&worker(), "j1", "heartbeat", json!({}), Some(1))
                .unwrap();
        }
        assert_eq!(row(), before);
        assert_eq!(counts(), (0, 1));
        assert_eq!(jobs.read("j1").data["topic"], topic);
    }
}

#[test]
fn release_ends_the_lease_and_advances_the_token_so_the_next_acquire_is_two_tokens_on() {
    for target in targets("lease_release") {
        let w = world(&target, Value::Null);
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            w.invoke(&worker(), "release", json!({}), "j1", Some(1))
                .unwrap(),
            json!({})
        );
        assert_eq!(
            w.lease_of("j1"),
            json!({"token": 2, "active": false, "expiries": 0, "ended": {"reason": "release", "at": T0}})
        );
        assert_eq!(w.token(&other(), "j1"), 3);
    }
}

#[test]
fn only_the_holder_releases_with_its_current_token() {
    for target in targets("lease_release_holder") {
        let w = world(&target, Value::Null);
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            reason(w.invoke(&other(), "release", json!({}), "j1", Some(1))),
            "another principal holds its lease, and only the holder may release it"
        );
        let (_, _, why, code, _) = veto(w.call(&worker(), "release"));
        assert_eq!(
            (code.as_deref(), why.as_str()),
            (
                Some("token_required"),
                "the holder releases its lease presenting its token as Lease's precondition"
            )
        );
        assert_eq!(
            reason(w.invoke(&worker(), "release", json!({}), "j1", Some(2))),
            "token 2 is stale: the lease is at token 1"
        );
        assert_eq!(w.lease_of("j1")["holder"], "wren");
    }
}

#[test]
fn a_principal_with_override_permission_releases_any_lease() {
    for target in targets("lease_release_override") {
        let w = world(&target, json!({"overridePermission": "jobs.override"}));
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            forbidden(w.invoke(&other(), "release", json!({}), "j1", Some(1))),
            "otto may not release another principal's lease of Job j1: it needs permission jobs.override"
        );
        assert_eq!(w.call(&operator(), "release").unwrap(), json!({}));
        assert_eq!(
            w.lease_of("j1"),
            json!({"token": 2, "active": false, "expiries": 0, "ended": {"reason": "release", "at": T0}})
        );
    }
}

#[test]
fn acquire_takes_a_lease_shorter_than_the_config_with_a_heartbeat_scaled_to_it() {
    for target in targets("lease_acquire_shorter") {
        let w = world(&target, Value::Null);
        assert_eq!(
            w.invoke(&worker(), "acquire", json!({"ttlMs": 30000}), "j1", None)
                .unwrap(),
            json!({"token": 1, "expiresAt": T0 + 30000, "heartbeatMs": 10000})
        );
        w.advance(20000);
        // Each heartbeat renews it by the length it was acquired with.
        assert_eq!(
            w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(1))
                .unwrap()["expiresAt"],
            T0 + 50000
        );
        let refused = refusal(w.invoke(&other(), "acquire", json!({"ttlMs": 90000}), "j1", None));
        assert_eq!(
            issues_of(&refused),
            [issue(
                "/ttlMs",
                "90000 is longer than the config's ttlMs, 60000"
            )]
        );
    }
}

#[test]
fn acquire_needs_acquire_permission_when_the_config_names_one() {
    for target in targets("lease_acquire_permission") {
        let w = world(&target, json!({"acquirePermission": "jobs.work"}));
        assert_eq!(
            forbidden(w.call(&worker(), "acquire")),
            "wren may not acquire the lease of Job j1: it needs permission jobs.work"
        );
        assert_eq!(w.token(&principal("wren", &["jobs"]), "j1"), 1);
    }
}

// ---- the guard ----

#[test]
fn while_the_lease_is_active_another_principals_writes_are_refused_and_the_holders_go_through() {
    for target in targets("lease_guard") {
        let w = world(&target, Value::Null);
        let jobs = w.jobs();
        w.call(&worker(), "acquire").unwrap();
        let (behavior, action, why, _, _) =
            veto(jobs.update(&other(), "j1", json!({"title": "Mine"}), None));
        assert_eq!(
            (behavior.as_str(), action.as_str(), why),
            (
                "Lease",
                "update",
                format!(
                    "another principal holds its lease, until {}",
                    iso(T0 + 60000)
                )
            )
        );
        assert_eq!(veto(jobs.delete(&other(), "j1", None)).1, "delete");
        assert_eq!(
            veto(w.invoke(
                &other(),
                "comment",
                json!({"body": "Taking over."}),
                "j1",
                None
            ))
            .1,
            "comment"
        );
        assert_eq!(
            veto(w.invoke(&other(), "transition", json!({"to": "running"}), "j1", None)).1,
            "transition"
        );
        // A read-only operation is no change, and Lease's own operations
        // check their callers themselves.
        assert_eq!(
            w.call(&other(), "listComments").unwrap(),
            json!({"items": [], "next": null})
        );
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": false})
        );

        jobs.update(&worker(), "j1", json!({"title": "Build the site"}), None)
            .unwrap();
        w.invoke(
            &worker(),
            "comment",
            json!({"body": "Started."}),
            "j1",
            None,
        )
        .unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        assert_eq!(w.status_of("j1"), "running");
        // A write does not renew the lease: only a heartbeat does.
        assert_eq!(w.lease_of("j1")["expiresAt"], T0 + 60000);
    }
}

#[test]
fn an_exempt_operation_and_an_override_pass_while_another_holds_the_lease() {
    for target in targets("lease_guard_exempt") {
        let w = world(
            &target,
            json!({"exempt": ["Comments.comment"], "overridePermission": "jobs.override"}),
        );
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            w.invoke(
                &other(),
                "comment",
                json!({"body": "Is it done yet?"}),
                "j1",
                None
            )
            .unwrap()["id"],
            1
        );
        assert_eq!(
            veto(w.invoke(&other(), "transition", json!({"to": "running"}), "j1", None)).0,
            "Lease"
        );
        w.jobs()
            .update(
                &operator(),
                "j1",
                json!({"title": "Renamed by an operator"}),
                None,
            )
            .unwrap();
        w.invoke(
            &operator(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        assert_eq!(w.jobs().read("j1").data["title"], "Renamed by an operator");
        assert_eq!(w.status_of("j1"), "running");
    }
}

#[test]
fn an_expired_holder_no_longer_holds_the_instance_and_others_writes_go_through() {
    for target in targets("lease_guard_expired") {
        let w = world(&target, Value::Null);
        let jobs = w.jobs();
        w.call(&worker(), "acquire").unwrap();
        w.advance(60000);
        let why = format!(
            "the caller's lease expired at {}, so it no longer holds the instance",
            iso(T0 + 60000)
        );
        assert_eq!(
            reason(jobs.update(&worker(), "j1", json!({"title": "Late"}), None)),
            why
        );
        assert_eq!(
            reason(w.invoke(&worker(), "comment", json!({"body": "Late."}), "j1", None)),
            why
        );
        assert_eq!(
            reason(w.invoke(&worker(), "release", json!({}), "j1", Some(1))),
            format!(
                "its lease expired at {}; expire applies the expiry",
                iso(T0 + 60000)
            )
        );
        jobs.update(&other(), "j1", json!({"title": "Picked up"}), None)
            .unwrap();
        assert_eq!(jobs.read("j1").data["title"], "Picked up");
        assert_eq!(w.lease_of("j1")["holder"], "wren");
    }
}

#[test]
fn with_no_lease_held_the_guard_refuses_nothing() {
    for target in targets("lease_guard_free") {
        let w = world(&target, Value::Null);
        let jobs = w.jobs();
        jobs.update(&other(), "j1", json!({"title": "Free"}), None)
            .unwrap();
        w.invoke(&other(), "transition", json!({"to": "running"}), "j1", None)
            .unwrap();
        assert!(jobs.delete(&other(), "j1", None).unwrap());
    }
}

// ---- expiry ----

#[test]
fn expire_applies_an_expiry_once_and_on_expiry_moves_the_status() {
    for target in targets("lease_expire") {
        let w = world(&target, requeue());
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": false})
        );
        w.advance(60000);
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": true, "reason": "ttl"})
        );
        assert_eq!(
            w.lease_of("j1"),
            json!({"token": 2, "active": false, "expiries": 1, "ended": {"reason": "ttl", "at": T0 + 60000}})
        );
        assert_eq!(w.status_of("j1"), "queued");
        // The status moved through Workflow's transition, in expire's
        // event, as the principal that expired it.
        let last = w.jobs().last_event("j1");
        assert_eq!(last.actor, "otto");
        assert_eq!(last.change["operation"], "expire");
        assert_eq!(
            last.change["patch"]["behaviors"]["Workflow"]["status"],
            "queued"
        );
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": false})
        );
    }
}

#[test]
fn on_expiry_moves_the_status_only_from_its_from_states_and_a_terminal_expiry_does_not_count() {
    for target in targets("lease_expire_terminal") {
        let w = world(&target, requeue());
        w.jobs().create("j2", json!({"title": "Queued"}));
        w.call(&worker(), "acquire").unwrap();
        for to in ["running", "done"] {
            w.invoke(&worker(), "transition", json!({ "to": to }), "j1", None)
                .unwrap();
        }
        w.invoke(&worker(), "acquire", json!({}), "j2", None)
            .unwrap();
        w.advance(60000);
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": true, "reason": "ttl"})
        );
        let lease = w.lease_of("j1");
        assert_eq!(
            (
                w.status_of("j1"),
                lease["expiries"].clone(),
                lease.get("holder")
            ),
            (json!("done"), json!(0), None)
        );
        assert_eq!(
            w.invoke(&other(), "expire", json!({}), "j2", None).unwrap(),
            json!({"expired": true, "reason": "ttl"})
        );
        assert_eq!(
            (w.status_of("j2"), w.lease_of("j2")["expiries"].clone()),
            (json!("queued"), json!(1))
        );
    }
}

#[test]
fn acquire_over_a_lapsed_lease_applies_its_expiry_first_then_leases_to_the_new_principal() {
    for target in targets("lease_acquire_lapsed") {
        let w = world(&target, requeue());
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        w.advance(60000);
        assert_eq!(
            w.call(&other(), "acquire").unwrap(),
            json!({"token": 3, "expiresAt": T0 + 120000, "heartbeatMs": 20000})
        );
        assert_eq!(
            w.lease_of("j1"),
            json!({"holder": "otto", "token": 3, "acquiredAt": T0 + 60000, "renewedAt": T0 + 60000, "expiresAt": T0 + 120000, "active": true, "expiries": 1})
        );
        assert_eq!(w.status_of("j1"), "queued");
        // The expired holder's writes are now another principal's.
        assert_eq!(
            reason(
                w.jobs()
                    .update(&worker(), "j1", json!({"title": "Late"}), None)
            ),
            format!(
                "another principal holds its lease, until {}",
                iso(T0 + 120000)
            )
        );
    }
}

#[test]
fn at_max_expiries_the_expiry_escalates_and_the_lease_waits_for_reset_expiries() {
    for target in targets("lease_max_expiries") {
        let w = world(
            &target,
            with(
                requeue(),
                json!({"maxExpiries": 2, "escalate": {"transition": "failed", "from": ["running"]}, "overridePermission": "jobs.override"}),
            ),
        );
        let lose = || {
            w.call(&worker(), "acquire").unwrap();
            w.invoke(
                &worker(),
                "transition",
                json!({"to": "running"}),
                "j1",
                None,
            )
            .unwrap();
            w.advance(60000);
            w.call(&other(), "expire").unwrap()
        };
        lose();
        assert_eq!(
            (w.status_of("j1"), w.lease_of("j1")["expiries"].clone()),
            (json!("queued"), json!(1))
        );
        // The expiry that reaches the cap escalates.
        lose();
        assert_eq!(
            (w.status_of("j1"), w.lease_of("j1")["expiries"].clone()),
            (json!("failed"), json!(2))
        );
        assert_eq!(
            reason(w.call(&other(), "acquire")),
            "its lease has expired 2 times, the most its config allows; resetExpiries lets it be leased again"
        );
        assert_eq!(
            code(w.call(&other(), "resetExpiries")),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            w.call(&operator(), "resetExpiries").unwrap(),
            json!({"expiries": 2})
        );
        assert_eq!(w.lease_of("j1")["expiries"], 0);
        assert_eq!(w.token(&worker(), "j1"), 5);
    }
}

#[test]
fn reset_expiries_is_refused_when_the_config_names_no_override_permission() {
    for target in targets("lease_reset_unconfigured") {
        let w = world(&target, Value::Null);
        assert_eq!(
            reason(w.call(&operator(), "resetExpiries")),
            "its config names no overridePermission, which resetting its expiries needs"
        );
    }
}

#[test]
fn an_acquire_whose_expiry_of_a_lapsed_lease_would_reach_the_cap_is_refused() {
    for target in targets("lease_acquire_cap") {
        let w = world(
            &target,
            with(
                requeue(),
                json!({"maxExpiries": 1, "escalate": {"transition": "failed", "from": ["running"]}}),
            ),
        );
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        w.advance(60000);
        assert_eq!(
            reason(w.call(&other(), "acquire")),
            "its lease has expired 1 times, the most its config allows; resetExpiries lets it be leased again"
        );
        let state = |w: &World| {
            let lease = w.lease_of("j1");
            (
                lease.get("holder").cloned(),
                lease["expiries"].clone(),
                w.status_of("j1"),
            )
        };
        assert_eq!(state(&w), (Some(json!("wren")), json!(0), json!("running")));
        w.call(&other(), "expire").unwrap();
        assert_eq!(state(&w), (None, json!(1), json!("failed")));
    }
}

#[test]
fn a_move_a_guard_vetoes_leaves_the_status_and_the_lease_expires_all_the_same() {
    for target in targets("lease_move_vetoed") {
        // An open blocker gates every terminal state, failed included, so
        // the escalation is vetoed.
        let w = world_with(
            &target,
            with(
                requeue(),
                json!({"maxExpiries": 1, "escalate": {"transition": "failed", "from": ["running"]}}),
            ),
            &[json!({"name": "Dependencies"})],
            |_| {},
        );
        w.jobs().create("j0", json!({"title": "First"}));
        w.invoke(&alice(), "addBlocker", json!({"id": "j0"}), "j1", None)
            .unwrap();
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        w.advance(60000);
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": true, "reason": "ttl"})
        );
        let lease = w.lease_of("j1");
        assert_eq!(
            (
                lease.get("holder"),
                lease["expiries"].clone(),
                w.status_of("j1")
            ),
            (None, json!(1), json!("running"))
        );
    }
}

#[test]
fn a_move_that_needs_a_permission_the_expirer_lacks_fails_the_expiry() {
    for target in targets("lease_move_forbidden") {
        // A transition that needs a permission the expirer lacks is no
        // veto: the expiry fails and leaves the lease lapsed.
        let mut guarded = job_flow();
        for edge in guarded["transitions"].as_array_mut().unwrap() {
            if edge["from"] == "running" && edge["to"] == "queued" {
                edge["permission"] = json!("jobs.requeue");
            }
        }
        let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(T0));
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": guarded},
                {"name": "Lease", "config": requeue()}
            ])),
        );
        let w = World { engine };
        w.jobs().create("j1", json!({"title": "Build"}));
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        w.advance(60000);
        assert_eq!(code(w.call(&other(), "expire")), EngineErrorCode::Forbidden);
        let lease = w.lease_of("j1");
        assert_eq!(
            (
                lease["holder"].clone(),
                lease["active"].clone(),
                w.status_of("j1")
            ),
            (json!("wren"), json!(false), json!("running"))
        );
        assert_eq!(
            w.call(&principal("sweeper", &["jobs.requeue"]), "expire")
                .unwrap(),
            json!({"expired": true, "reason": "ttl"})
        );
        let lease = w.lease_of("j1");
        assert_eq!(
            (
                lease.get("holder"),
                lease["expiries"].clone(),
                w.status_of("j1")
            ),
            (None, json!(1), json!("queued"))
        );
    }
}

#[test]
fn a_release_moves_unfinished_work_back_with_on_expiry_and_counts_no_expiry() {
    for target in targets("lease_release_moves") {
        let w = world(&target, requeue());
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        w.invoke(&worker(), "release", json!({}), "j1", Some(1))
            .unwrap();
        assert_eq!(
            (w.status_of("j1"), w.lease_of("j1")["expiries"].clone()),
            (json!("queued"), json!(0))
        );
        w.call(&worker(), "acquire").unwrap();
        for to in ["running", "done"] {
            w.invoke(&worker(), "transition", json!({ "to": to }), "j1", None)
                .unwrap();
        }
        w.invoke(&worker(), "release", json!({}), "j1", Some(3))
            .unwrap();
        assert_eq!(w.status_of("j1"), "done");
    }
}

// ---- the longest hold ----

fn limited() -> Value {
    json!({"ttlMs": 10000, "heartbeatMs": 3000, "maxHoldMs": 25000, "maxHoldField": "timeLimitMs"})
}

#[test]
fn heartbeats_renew_a_lease_up_to_its_longest_hold_and_no_further() {
    for target in targets("lease_hold") {
        let w = world(&target, limited());
        w.call(&worker(), "acquire").unwrap();
        let beat = |w: &World| {
            w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(1))
                .unwrap()["expiresAt"]
                .clone()
        };
        w.advance(8000);
        assert_eq!(beat(&w), T0 + 18000);
        w.advance(8000);
        assert_eq!(beat(&w), T0 + 25000);
        w.advance(8000);
        assert_eq!(beat(&w), T0 + 25000);
        w.advance(1000);
        assert!(
            reason(w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(1)))
                .starts_with("its lease expired at")
        );
        // It reached its longest hold while its holder still renewed it.
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": true, "reason": "maxHold"})
        );
    }
}

#[test]
fn the_instances_max_hold_field_overrides_max_hold_ms_and_a_value_that_is_not_a_positive_integer_does_not(
) {
    for target in targets("lease_hold_field") {
        let w = world(&target, limited());
        w.jobs()
            .create("short", json!({"title": "Short", "timeLimitMs": 12000}));
        w.jobs()
            .create("zero", json!({"title": "Unset", "timeLimitMs": 0}));
        assert_eq!(
            w.invoke(&worker(), "acquire", json!({}), "short", None)
                .unwrap()["expiresAt"],
            T0 + 10000
        );
        w.invoke(&worker(), "acquire", json!({}), "zero", None)
            .unwrap();
        w.advance(9000);
        assert_eq!(
            w.invoke(&worker(), "heartbeat", json!({}), "short", Some(1))
                .unwrap()["expiresAt"],
            T0 + 12000
        );
        assert_eq!(
            w.invoke(&worker(), "heartbeat", json!({}), "zero", Some(1))
                .unwrap()["expiresAt"],
            T0 + 19000
        );
        w.advance(3000);
        assert_eq!(w.lease_of("short")["active"], false);
        assert_eq!(w.lease_of("zero")["active"], true);
        // A lease past its longest hold is never held longer by a short
        // acquire.
        assert_eq!(
            w.invoke(&other(), "expire", json!({}), "short", None)
                .unwrap(),
            json!({"expired": true, "reason": "maxHold"})
        );
    }
}

#[test]
fn the_field_that_limits_the_hold_cannot_change_while_a_lease_is_held() {
    for target in targets("lease_hold_fixed") {
        let w = world(&target, limited());
        let jobs = w.jobs();
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            reason(jobs.update(&worker(), "j1", json!({"timeLimitMs": 999_999}), None)),
            "timeLimitMs limits how long its lease may be held, so it cannot change while the lease is held"
        );
        jobs.update(&worker(), "j1", json!({"title": "Allowed"}), None)
            .unwrap();
        w.invoke(&worker(), "release", json!({}), "j1", Some(1))
            .unwrap();
        jobs.update(&worker(), "j1", json!({"timeLimitMs": 999_999}), None)
            .unwrap();
        assert_eq!(jobs.read("j1").data["timeLimitMs"], 999_999);
    }
}

// ---- directives ----

fn directed() -> Value {
    json!({"overridePermission": "jobs.override", "directPermission": "jobs.direct"})
}

fn directives(w: &World, who: &Principal, params: Value, token: i64) -> Value {
    w.invoke(who, "heartbeat", params, "j1", Some(token))
        .unwrap()["directives"]
        .clone()
}

#[test]
fn a_directive_reaches_the_holder_on_each_heartbeat_until_it_acknowledges_it() {
    for target in targets("lease_directives") {
        let w = world(&target, directed());
        assert_eq!(
            reason(w.invoke(&sender(), "direct", json!({"name": "cancel"}), "j1", None)),
            "no lease is active, so there is no holder to direct"
        );
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            w.invoke(
                &sender(),
                "direct",
                json!({"name": "cancel", "data": {"reason": "superseded"}}),
                "j1",
                None
            )
            .unwrap(),
            json!({"id": 1, "created": true})
        );
        w.advance(10);
        assert_eq!(
            w.invoke(&sender(), "direct", json!({"name": "pause"}), "j1", None)
                .unwrap(),
            json!({"id": 2, "created": true})
        );
        let both = json!([
            {"id": 1, "name": "cancel", "data": {"reason": "superseded"}, "createdAt": T0, "createdBy": "sid"},
            {"id": 2, "name": "pause", "createdAt": T0 + 10, "createdBy": "sid"}
        ]);
        assert_eq!(directives(&w, &worker(), json!({}), 1), both);
        // Delivery is at least once: they come again until acknowledged.
        assert_eq!(directives(&w, &worker(), json!({}), 1), both);
        assert_eq!(
            w.invoke(&worker(), "acknowledge", json!({"ids": [1]}), "j1", Some(1))
                .unwrap(),
            json!({})
        );
        assert_eq!(directives(&w, &worker(), json!({}), 1), json!([both[1]]));
        // Acknowledging one again changes nothing.
        assert_eq!(
            w.invoke(&worker(), "acknowledge", json!({"ids": [1]}), "j1", Some(1))
                .unwrap(),
            json!({})
        );
    }
}

#[test]
fn a_heartbeat_acknowledges_directives_in_the_same_write_and_refuses_an_id_not_sent_under_its_token(
) {
    for target in targets("lease_heartbeat_acknowledge") {
        let w = world(&target, directed());
        w.call(&worker(), "acquire").unwrap();
        for name in ["cancel", "pause"] {
            w.invoke(&sender(), "direct", json!({ "name": name }), "j1", None)
                .unwrap();
        }
        let seq = w.jobs().seq("j1");
        let refused = refusal(w.invoke(
            &worker(),
            "heartbeat",
            json!({"acknowledge": [1, 3]}),
            "j1",
            Some(1),
        ));
        assert_eq!(
            issues_of(&refused),
            [issue(
                "/acknowledge",
                "no directive 3 was sent under token 1"
            )]
        );
        assert_eq!(w.jobs().seq("j1"), seq);
        let beat = directives(&w, &worker(), json!({"acknowledge": [1]}), 1);
        assert_eq!(beat.as_array().unwrap().len(), 1);
        assert_eq!(beat[0]["id"], 2);
        // One write, one event, for the heartbeat and the acknowledgement.
        assert_eq!(w.jobs().seq("j1"), seq + 1);
        assert_eq!(
            directives(&w, &worker(), json!({"acknowledge": []}), 1)
                .as_array()
                .unwrap()
                .len(),
            1
        );
        assert_eq!(
            directives(&w, &worker(), json!({"acknowledge": [2]}), 1),
            json!([])
        );
    }
}

#[test]
fn a_directive_sent_with_a_dedupe_key_is_sent_once_per_lease_and_again_to_the_next() {
    for target in targets("lease_dedupe") {
        let w = world(&target, directed());
        w.call(&worker(), "acquire").unwrap();
        let once = json!({"name": "cancel", "dedupeKey": "budget:cpu"});
        let direct = |params: Value| w.invoke(&sender(), "direct", params, "j1", None).unwrap();
        assert_eq!(direct(once.clone()), json!({"id": 1, "created": true}));
        assert_eq!(
            direct(with(once.clone(), json!({"data": {"again": true}}))),
            json!({"id": 1, "created": false})
        );
        assert_eq!(
            direct(json!({"name": "cancel"})),
            json!({"id": 2, "created": true})
        );
        assert_eq!(
            directives(&w, &worker(), json!({"acknowledge": [1]}), 1),
            json!([{"id": 2, "name": "cancel", "createdAt": T0, "createdBy": "sid"}])
        );
        assert_eq!(direct(once.clone()), json!({"id": 1, "created": false}));
        w.invoke(&worker(), "release", json!({}), "j1", Some(1))
            .unwrap();
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(direct(once), json!({"id": 1, "created": true}));
        assert_eq!(
            directives(&w, &worker(), json!({}), 3),
            json!([{"id": 1, "name": "cancel", "dedupeKey": "budget:cpu", "createdAt": T0, "createdBy": "sid"}])
        );
    }
}

#[test]
fn only_the_holder_acknowledges_with_its_current_token_the_directives_sent_under_it() {
    for target in targets("lease_acknowledge") {
        let w = world(&target, directed());
        w.call(&worker(), "acquire").unwrap();
        w.invoke(&sender(), "direct", json!({"name": "cancel"}), "j1", None)
            .unwrap();
        assert_eq!(
            reason(w.invoke(&other(), "acknowledge", json!({"ids": [1]}), "j1", Some(1))),
            "another principal holds its lease"
        );
        assert_eq!(
            reason(w.invoke(&worker(), "acknowledge", json!({"ids": [1]}), "j1", Some(2))),
            "token 2 is stale: the lease is at token 1"
        );
        let refused = refusal(w.invoke(
            &worker(),
            "acknowledge",
            json!({"ids": [1, 3]}),
            "j1",
            Some(1),
        ));
        assert_eq!(
            issues_of(&refused),
            [issue("/ids", "no directive 3 was sent under token 1")]
        );
        // Nothing was acknowledged by the refused call.
        assert_eq!(
            directives(&w, &worker(), json!({}), 1)
                .as_array()
                .unwrap()
                .len(),
            1
        );
    }
}

#[test]
fn the_directives_of_a_lease_that_ends_never_reach_the_next_holder() {
    for target in targets("lease_directives_end") {
        let w = world(&target, directed());
        w.call(&worker(), "acquire").unwrap();
        w.invoke(&sender(), "direct", json!({"name": "cancel"}), "j1", None)
            .unwrap();
        w.invoke(&worker(), "release", json!({}), "j1", Some(1))
            .unwrap();
        w.call(&other(), "acquire").unwrap();
        assert_eq!(directives(&w, &other(), json!({}), 3), json!([]));
        // Nor across an expiry; a directive's number counts within its
        // lease.
        w.invoke(&sender(), "direct", json!({"name": "pause"}), "j1", None)
            .unwrap();
        w.advance(60000);
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(directives(&w, &worker(), json!({}), 5), json!([]));
        assert_eq!(
            w.invoke(&sender(), "direct", json!({"name": "pause"}), "j1", None)
                .unwrap(),
            json!({"id": 1, "created": true})
        );
    }
}

#[test]
fn direct_needs_direct_permission() {
    for target in targets("lease_direct_permission") {
        let w = world(&target, directed());
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            forbidden(w.invoke(&other(), "direct", json!({"name": "cancel"}), "j1", None)),
            "otto may not send a directive to the holder of Job j1: it needs permission jobs.direct"
        );
        // With directPermission set, overridePermission does not send
        // directives.
        assert_eq!(
            code(w.invoke(&operator(), "direct", json!({"name": "cancel"}), "j1", None)),
            EngineErrorCode::Forbidden
        );
        // The holder needs the permission too.
        assert_eq!(
            code(w.invoke(&worker(), "direct", json!({"name": "cancel"}), "j1", None)),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn direct_needs_override_permission_when_direct_permission_is_absent() {
    for target in targets("lease_direct_override") {
        let w = world(&target, json!({"overridePermission": "jobs.override"}));
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            w.invoke(&operator(), "direct", json!({"name": "cancel"}), "j1", None)
                .unwrap(),
            json!({"id": 1, "created": true})
        );
    }
}

#[test]
fn direct_is_refused_when_the_config_names_neither_permission() {
    for target in targets("lease_direct_none") {
        let w = world(&target, Value::Null);
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            reason(w.invoke(&operator(), "direct", json!({"name": "cancel"}), "j1", None)),
            "its config names no permission that sends directives (directPermission or overridePermission)"
        );
    }
}

#[test]
fn deleting_the_instance_takes_its_lease_and_its_directives_with_it() {
    for target in targets("lease_delete") {
        let w = world(&target, directed());
        w.call(&worker(), "acquire").unwrap();
        w.invoke(&sender(), "direct", json!({"name": "cancel"}), "j1", None)
            .unwrap();
        let directives = "SELECT COUNT(*) AS n FROM bhv_lease__directives";
        assert_eq!(count(&w.engine, directives), 1);
        assert!(w.jobs().delete(&worker(), "j1", None).unwrap());
        assert_eq!(count(&w.engine, directives), 0);
    }
}

// ---- the token fences writes ----

// retaken is two processes of one principal: one whose lease lapsed, and
// one that took the instance again. Both act as wren.
fn retaken(target: &Target, config: Value) -> (World, i64) {
    let w = world(target, with(requeue(), config));
    w.call(&worker(), "acquire").unwrap();
    w.invoke(
        &worker(),
        "transition",
        json!({"to": "running"}),
        "j1",
        Some(1),
    )
    .unwrap();
    w.advance(60000);
    let token = w.token(&worker(), "j1");
    w.invoke(
        &worker(),
        "transition",
        json!({"to": "running"}),
        "j1",
        Some(token),
    )
    .unwrap();
    (w, token)
}

#[test]
fn a_write_that_presents_a_token_other_than_the_current_one_is_refused() {
    for target in targets("lease_token_stale") {
        let (w, token) = retaken(&target, json!({}));
        let jobs = w.jobs();
        assert_eq!(token, 3);
        let (behavior, action, why, code, details) =
            veto(jobs.update(&worker(), "j1", json!({"title": "Stale"}), Some(1)));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref(), details),
            (
                "Lease",
                "update",
                Some("token_stale"),
                Some(json!({"token": 1, "current": 3}))
            )
        );
        assert_eq!(why, "token 1 is stale: the lease is at token 3");
        assert_eq!(
            veto_code(w.invoke(
                &worker(),
                "transition",
                json!({"to": "done"}),
                "j1",
                Some(1)
            )),
            Some("token_stale".to_owned())
        );
        assert_eq!(
            veto_code(w.invoke(
                &worker(),
                "comment",
                json!({"body": "Done, I think."}),
                "j1",
                Some(1)
            )),
            Some("token_stale".to_owned())
        );
        assert_eq!(
            veto_code(jobs.delete(&worker(), "j1", Some(1))),
            Some("token_stale".to_owned())
        );
        // Whoever calls: an operator with overridePermission who presents a
        // stale token is refused too.
        assert_eq!(
            veto_code(jobs.update(&operator(), "j1", json!({"title": "Stale"}), Some(2))),
            Some("token_stale".to_owned())
        );
        // The process that holds the current token writes.
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "done"}),
            "j1",
            Some(token),
        )
        .unwrap();
        assert_eq!(w.status_of("j1"), "done");
        // Without requireToken, a write that presents no token is the
        // principal's, which holds the lease.
        jobs.update(&worker(), "j1", json!({"title": "Unfenced"}), None)
            .unwrap();
        // A read-only operation presents nothing that can be stale.
        assert_eq!(
            w.invoke(&worker(), "listComments", json!({}), "j1", Some(1))
                .unwrap(),
            json!({"items": [], "next": null})
        );
        // The heartbeat, the acknowledgement and the release of the old
        // lease are refused the same way.
        assert_eq!(
            veto_code(w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(1))),
            Some("token_stale".to_owned())
        );
        assert_eq!(
            veto_code(w.invoke(&worker(), "release", json!({}), "j1", Some(1))),
            Some("token_stale".to_owned())
        );
    }
}

#[test]
fn a_token_on_a_free_instance_is_checked_too_and_the_current_one_is_a_compare_and_set_for_acquire()
{
    for target in targets("lease_token_free") {
        let w = world(&target, requeue());
        w.call(&worker(), "acquire").unwrap();
        w.invoke(&worker(), "release", json!({}), "j1", Some(1))
            .unwrap();
        assert_eq!(
            veto_code(
                w.jobs()
                    .update(&worker(), "j1", json!({"title": "After"}), Some(1))
            ),
            Some("token_stale".to_owned())
        );
        assert_eq!(
            veto_code(w.invoke(&other(), "acquire", json!({}), "j1", Some(1))),
            Some("token_stale".to_owned())
        );
        assert_eq!(
            w.invoke(&other(), "acquire", json!({}), "j1", Some(2))
                .unwrap()["token"],
            3
        );
    }
}

#[test]
fn with_require_token_a_write_under_an_active_lease_presents_the_current_token() {
    for target in targets("lease_require_token") {
        let (w, token) = retaken(
            &target,
            json!({"requireToken": true, "exempt": ["Comments.comment"], "overridePermission": "jobs.override"}),
        );
        let jobs = w.jobs();
        let (_, _, why, code, _) =
            veto(jobs.update(&worker(), "j1", json!({"title": "Bare"}), None));
        assert_eq!(
            (code.as_deref(), why.as_str()),
            (
                Some("token_required"),
                "its config requires every write under the lease to present the lease's token as Lease's precondition"
            )
        );
        assert_eq!(
            veto_code(w.invoke(&worker(), "transition", json!({"to": "done"}), "j1", None)),
            Some("token_required".to_owned())
        );
        assert_eq!(
            veto_code(jobs.delete(&worker(), "j1", None)),
            Some("token_required".to_owned())
        );
        // Another principal is refused for holding no lease, before any
        // token.
        assert_eq!(
            veto_code(jobs.update(&other(), "j1", json!({"title": "Mine"}), None)),
            Some("held_by_another".to_owned())
        );
        jobs.update(&worker(), "j1", json!({"title": "Fenced"}), Some(token))
            .unwrap();
        w.invoke(&other(), "comment", json!({"body": "Exempt."}), "j1", None)
            .unwrap();
        w.invoke(
            &worker(),
            "comment",
            json!({"body": "Exempt for the holder too."}),
            "j1",
            None,
        )
        .unwrap();
        assert_eq!(
            w.call(&worker(), "listComments").unwrap()["items"]
                .as_array()
                .unwrap()
                .len(),
            2
        );
        jobs.update(&operator(), "j1", json!({"title": "Overridden"}), None)
            .unwrap();
        assert_eq!(
            w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(token))
                .unwrap(),
            json!({"expiresAt": T0 + 120000, "directives": []})
        );
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": false})
        );
        // A create holds no lease, so it presents no token, whoever creates.
        assert_eq!(
            jobs.create_as(&worker(), "j2", json!({"title": "New"}))
                .unwrap()
                .seq,
            1
        );
        assert_eq!(w.lease_of("j2").get("holder"), None);
        // Once the lease is released, nothing is held to the token.
        w.invoke(&worker(), "release", json!({}), "j1", Some(token))
            .unwrap();
        jobs.update(&worker(), "j1", json!({"title": "Free"}), None)
            .unwrap();
        assert_eq!(jobs.read("j1").data["title"], "Free");
    }
}

#[test]
fn with_require_token_the_runners_sweep_still_expires_a_lapsed_lease() {
    for target in targets("lease_require_token_sweep") {
        let w = world_with(
            &target,
            with(requeue(), json!({"requireToken": true, "sweepMs": 1000})),
            &[],
            |options| options.runner = Some(RunnerOptions::new(runner())),
        );
        run_due(&w.engine);
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            Some(1),
        )
        .unwrap();
        w.advance(60000);
        assert_eq!(run_due(&w.engine).scheduled, 1);
        let lease = w.lease_of("j1");
        assert_eq!(
            (
                w.status_of("j1"),
                lease.get("holder"),
                lease["ended"].clone()
            ),
            (
                json!("queued"),
                None,
                json!({"reason": "ttl", "at": T0 + 60000})
            )
        );
    }
}

#[test]
fn heartbeat_acknowledge_and_a_release_need_the_holder_presenting_its_token_each_with_its_code() {
    for target in targets("lease_codes") {
        let w = world(&target, json!({"directPermission": "jobs.direct"}));
        let coded = |result: Result<Value, Error>| veto_code(result).unwrap_or_default();
        assert_eq!(
            coded(w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(0))),
            "not_leased"
        );
        assert_eq!(
            coded(w.invoke(&worker(), "release", json!({}), "j1", Some(0))),
            "not_leased"
        );
        assert_eq!(
            coded(w.invoke(&sender(), "direct", json!({"name": "stop"}), "j1", None)),
            "not_leased"
        );
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(coded(w.call(&worker(), "acquire")), "held_by_caller");
        assert_eq!(coded(w.call(&other(), "acquire")), "held_by_another");
        assert_eq!(coded(w.call(&worker(), "heartbeat")), "token_required");
        assert_eq!(
            coded(w.invoke(&worker(), "acknowledge", json!({"ids": [1]}), "j1", None)),
            "token_required"
        );
        assert_eq!(
            coded(w.invoke(&other(), "heartbeat", json!({}), "j1", Some(1))),
            "not_holder"
        );
        assert_eq!(
            coded(w.invoke(&other(), "release", json!({}), "j1", Some(1))),
            "not_holder"
        );
        w.advance(60000);
        let (_, _, _, code, details) =
            veto(w.invoke(&worker(), "heartbeat", json!({}), "j1", Some(1)));
        assert_eq!(
            (code.as_deref(), details),
            (Some("lapsed"), Some(json!({"expiredAt": T0 + 60000})))
        );
        assert_eq!(
            veto_code(
                w.jobs()
                    .update(&worker(), "j1", json!({"title": "Late"}), None)
            ),
            Some("lapsed".to_owned())
        );
        assert_eq!(
            coded(w.invoke(&sender(), "direct", json!({"name": "stop"}), "j1", None)),
            "lapsed"
        );
        assert_eq!(
            coded(w.call(&operator(), "resetExpiries")),
            "not_configured"
        );
    }
}

// ---- abandon ----

#[test]
fn a_release_with_abandon_counts_as_an_expiry_and_the_abandon_that_reaches_max_expiries_escalates()
{
    for target in targets("lease_abandon") {
        let w = world(
            &target,
            with(
                requeue(),
                json!({"maxExpiries": 2, "escalate": {"transition": "failed", "from": ["running"]}}),
            ),
        );
        let take = || {
            let token = w.token(&worker(), "j1");
            w.invoke(
                &worker(),
                "transition",
                json!({"to": "running"}),
                "j1",
                Some(token),
            )
            .unwrap();
            token
        };
        let state = |w: &World| {
            let lease = w.lease_of("j1");
            (
                w.status_of("j1"),
                lease["expiries"].clone(),
                lease["ended"].clone(),
            )
        };
        // A plain release hands the work back without counting.
        let token = take();
        w.invoke(&worker(), "release", json!({}), "j1", Some(token))
            .unwrap();
        assert_eq!(
            state(&w),
            (
                json!("queued"),
                json!(0),
                json!({"reason": "release", "at": T0})
            )
        );
        let token = take();
        assert_eq!(
            w.invoke(
                &worker(),
                "release",
                json!({"abandon": true}),
                "j1",
                Some(token)
            )
            .unwrap(),
            json!({})
        );
        assert_eq!(
            state(&w),
            (
                json!("queued"),
                json!(1),
                json!({"reason": "abandon", "at": T0})
            )
        );
        let last = w.jobs().last_event("j1");
        assert_eq!(last.change["params"], json!({"abandon": true}));
        // The patch is the diff of the fields that moved: a free lease has
        // no holder, acquiredAt, renewedAt or expiresAt.
        assert_eq!(
            last.change["patch"],
            json!({"behaviors": {
                "Workflow": {"status": "queued"},
                "Lease": {"holder": null, "token": 4, "acquiredAt": null, "renewedAt": null, "expiresAt": null, "active": false, "expiries": 1, "ended": {"reason": "abandon", "at": T0}}
            }})
        );
        // The abandon that reaches the cap escalates, and the lease cannot
        // be taken again.
        let token = take();
        w.invoke(
            &worker(),
            "release",
            json!({"abandon": true}),
            "j1",
            Some(token),
        )
        .unwrap();
        assert_eq!(
            (w.status_of("j1"), w.lease_of("j1")["expiries"].clone()),
            (json!("failed"), json!(2))
        );
        let (_, _, _, code, details) = veto(w.call(&worker(), "acquire"));
        assert_eq!(
            (code.as_deref(), details),
            (
                Some("max_expiries"),
                Some(json!({"expiries": 2, "maxExpiries": 2}))
            )
        );
    }
}

#[test]
fn an_abandon_of_finished_work_does_not_count_and_an_operator_may_abandon_a_lease_it_overrides() {
    for target in targets("lease_abandon_override") {
        let w = world(
            &target,
            with(requeue(), json!({"overridePermission": "jobs.override"})),
        );
        w.call(&worker(), "acquire").unwrap();
        for to in ["running", "done"] {
            w.invoke(&worker(), "transition", json!({ "to": to }), "j1", Some(1))
                .unwrap();
        }
        w.invoke(
            &worker(),
            "release",
            json!({"abandon": true}),
            "j1",
            Some(1),
        )
        .unwrap();
        assert_eq!(
            (w.status_of("j1"), w.lease_of("j1")["expiries"].clone()),
            (json!("done"), json!(0))
        );
        w.jobs().create("j2", json!({"title": "Next"}));
        w.invoke(&worker(), "acquire", json!({}), "j2", None)
            .unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j2",
            Some(1),
        )
        .unwrap();
        w.invoke(&operator(), "release", json!({"abandon": true}), "j2", None)
            .unwrap();
        let lease = w.lease_of("j2");
        assert_eq!(
            (
                w.status_of("j2"),
                lease["expiries"].clone(),
                lease["ended"]["reason"].clone()
            ),
            (json!("queued"), json!(1), json!("abandon"))
        );
    }
}

// ---- why a lease ended ----

#[test]
fn expire_says_why_ttl_max_hold_or_holder() {
    for target in targets("lease_ended") {
        let w = world(
            &target,
            with(
                requeue(),
                json!({"maxHoldMs": 90000, "overridePermission": "jobs.override"}),
            ),
        );
        for id in ["j2", "j3", "j4"] {
            w.jobs().create(id, json!({ "title": id }));
        }
        for id in ["j1", "j2", "j3", "j4"] {
            w.invoke(&worker(), "acquire", json!({}), id, None).unwrap();
            w.invoke(
                &worker(),
                "transition",
                json!({"to": "running"}),
                id,
                Some(1),
            )
            .unwrap();
        }
        // j2, j3 and j4 are renewed up to their longest hold; j1 is not
        // renewed.
        w.advance(50000);
        for id in ["j2", "j3", "j4"] {
            assert_eq!(
                w.invoke(&worker(), "heartbeat", json!({}), id, Some(1))
                    .unwrap(),
                json!({"expiresAt": T0 + 90000, "directives": []})
            );
        }
        w.advance(20000);
        assert_eq!(
            w.invoke(&operator(), "expire", json!({"holder": "wren"}), "j3", None)
                .unwrap(),
            json!({"expired": true, "reason": "holder"})
        );
        w.advance(20000);
        assert_eq!(
            w.invoke(&other(), "expire", json!({}), "j1", None).unwrap(),
            json!({"expired": true, "reason": "ttl"})
        );
        assert_eq!(
            w.invoke(&other(), "expire", json!({}), "j2", None).unwrap(),
            json!({"expired": true, "reason": "maxHold"})
        );
        // Each expiry's event carries the reason in Lease's ended.
        let ended: Vec<Value> = ["j1", "j2", "j3"]
            .iter()
            .map(|id| {
                w.jobs().last_event(id).change["patch"]["behaviors"]["Lease"]["ended"].clone()
            })
            .collect();
        assert_eq!(
            ended,
            [
                json!({"reason": "ttl", "at": T0 + 90000}),
                json!({"reason": "maxHold", "at": T0 + 90000}),
                json!({"reason": "holder", "at": T0 + 70000})
            ]
        );
        // expireHolder counts by reason: j4, lapsed at its longest hold, is
        // wren's last lease.
        assert_eq!(
            w.jobs()
                .invoke_schema(&operator(), "expireHolder", json!({"holder": "wren"}))
                .unwrap(),
            json!({"expired": 1, "reasons": {"maxHold": 1}, "ids": ["j4"]})
        );
        // A new acquire clears ended: it describes the last lease that
        // ended, not the one held.
        w.call(&other(), "acquire").unwrap();
        assert_eq!(w.lease_of("j1").get("ended"), None);
    }
}

#[test]
fn expire_with_a_holder_whose_lease_has_lapsed_gives_the_lapse_as_its_reason() {
    for target in targets("lease_expire_holder_lapsed") {
        let w = world(
            &target,
            with(requeue(), json!({"overridePermission": "jobs.override"})),
        );
        w.call(&worker(), "acquire").unwrap();
        w.advance(60000);
        assert_eq!(
            w.jobs()
                .invoke_schema(&operator(), "expireHolder", json!({"holder": "wren"}))
                .unwrap(),
            json!({"expired": 1, "reasons": {"ttl": 1}, "ids": ["j1"]})
        );
    }
}

// ---- expiry on the runner, and a gone holder ----

#[test]
fn every_sweep_ms_the_runner_expires_the_leases_past_their_expiry_time_or_longest_hold() {
    for target in targets("lease_sweep") {
        let w = world_with(
            &target,
            with(
                requeue(),
                json!({"sweepMs": 2000, "maxHoldField": "timeLimitMs", "overridePermission": "jobs.override"}),
            ),
            &[],
            |options| options.runner = Some(RunnerOptions::new(runner())),
        );
        run_due(&w.engine);
        let status = w.engine.runner().status().expect("the runner's status");
        let schedule = status
            .schedules
            .iter()
            .find(|schedule| schedule.behavior == "Lease")
            .expect("Lease's schedule");
        assert_eq!(
            (schedule.schedule.as_str(), schedule.every_ms, schedule.next),
            ("expire", Some(2000), Some(T0 + 2000))
        );
        for id in ["j2", "j3"] {
            w.jobs().create(id, json!({ "title": id }));
        }
        for id in ["j1", "j2", "j3"] {
            w.invoke(&worker(), "acquire", json!({}), id, None).unwrap();
            w.invoke(&worker(), "transition", json!({"to": "running"}), id, None)
                .unwrap();
        }
        // j3's limit is cut below its expiry time: past it, the lease has
        // lapsed though expiresAt has not come.
        w.jobs()
            .update(&operator(), "j3", json!({"timeLimitMs": 30000}), None)
            .unwrap();
        w.advance(30000);
        w.invoke(&worker(), "heartbeat", json!({}), "j2", Some(1))
            .unwrap();
        assert_eq!(run_due(&w.engine).scheduled, 1);
        let state = |w: &World, id: &str| {
            let lease = w.lease_of(id);
            (
                w.status_of(id),
                lease.get("holder").cloned(),
                lease["expiries"].clone(),
            )
        };
        assert_eq!(
            ["j1", "j2", "j3"].map(|id| state(&w, id)),
            [
                (json!("running"), Some(json!("wren")), json!(0)),
                (json!("running"), Some(json!("wren")), json!(0)),
                (json!("queued"), None, json!(1))
            ]
        );
        w.advance(30000);
        assert_eq!(run_due(&w.engine).scheduled, 1);
        assert_eq!(
            ["j1", "j2", "j3"].map(|id| state(&w, id)),
            [
                (json!("queued"), None, json!(1)),
                (json!("running"), Some(json!("wren")), json!(0)),
                (json!("queued"), None, json!(1))
            ]
        );
        let last = w.jobs().last_event("j1");
        assert_eq!(
            (
                last.actor.as_str(),
                last.change["operation"].clone(),
                cause_of(&last)
            ),
            (
                "runner",
                json!("expire"),
                json!({"behavior": "Lease", "schedule": "expire", "depth": 1})
            )
        );
    }
}

#[test]
fn a_guards_veto_of_one_expiry_leaves_that_lease_for_the_next_sweep_and_the_sweep_goes_on() {
    for target in targets("lease_sweep_vetoed") {
        let engine = target.open_with(
            superschematic_engine_behaviors::core()
                .with(Lease)
                .with(Gate),
            |options| {
                options.clock = Clock::Manual(T0);
                options.runner = Some(RunnerOptions::new(runner()));
            },
        );
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease", "config": requeue()},
                {"name": "test.Gate"}
            ])),
        );
        let w = World { engine };
        for id in ["a", "stuck", "z"] {
            w.jobs().create(id, json!({ "title": id }));
            w.invoke(&worker(), "acquire", json!({}), id, None).unwrap();
        }
        run_due(&w.engine);
        w.advance(60000);
        assert_eq!(
            run_due(&w.engine),
            RunnerPass {
                handled: 0,
                skipped: 0,
                failed: 0,
                scheduled: 1,
                pruned: None
            }
        );
        assert_eq!(
            ["a", "stuck", "z"].map(|id| w.lease_of(id).get("holder").cloned()),
            [None, Some(json!("wren")), None]
        );
    }
}

#[test]
fn expire_with_a_holder_expires_that_holders_lease_at_once_and_needs_override_permission() {
    for target in targets("lease_expire_holder") {
        let w = world(
            &target,
            with(requeue(), json!({"overridePermission": "jobs.override"})),
        );
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        assert_eq!(
            code(w.invoke(&other(), "expire", json!({"holder": "wren"}), "j1", None)),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            w.invoke(&operator(), "expire", json!({"holder": "otto"}), "j1", None)
                .unwrap(),
            json!({"expired": false})
        );
        assert_eq!(
            w.invoke(&operator(), "expire", json!({"holder": "wren"}), "j1", None)
                .unwrap(),
            json!({"expired": true, "reason": "holder"})
        );
        let lease = w.lease_of("j1");
        assert_eq!(
            (
                w.status_of("j1"),
                lease.get("holder"),
                lease["expiries"].clone()
            ),
            (json!("queued"), None, json!(1))
        );
    }
}

#[test]
fn not_renewed_after_spares_an_active_lease_renewed_after_the_time_and_expire_holder_lists_what_it_expired(
) {
    for target in targets("lease_not_renewed_after") {
        let w = world(
            &target,
            with(requeue(), json!({"overridePermission": "jobs.override"})),
        );
        for id in ["j2", "j3"] {
            w.jobs().create(id, json!({ "title": id }));
            w.invoke(&worker(), "acquire", json!({}), id, None).unwrap();
        }
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(w.lease_of("j1")["renewedAt"], T0);
        w.advance(20000);
        w.invoke(&worker(), "heartbeat", json!({}), "j2", Some(1))
            .unwrap();
        let j2 = w.lease_of("j2");
        assert_eq!(
            (j2["renewedAt"].clone(), j2["acquiredAt"].clone()),
            (json!(T0 + 20000), json!(T0))
        );
        // j2 was renewed after T0 + 10000, so its holder's own heartbeats
        // show it alive; j1 and j3 were not.
        w.advance(1000);
        assert_eq!(
            w.invoke(
                &operator(),
                "expire",
                json!({"holder": "wren", "notRenewedAfter": T0 + 10000}),
                "j2",
                None
            )
            .unwrap(),
            json!({"expired": false})
        );
        assert_eq!(
            w.jobs()
                .invoke_schema(
                    &operator(),
                    "expireHolder",
                    json!({"holder": "wren", "notRenewedAfter": T0 + 10000})
                )
                .unwrap(),
            json!({"expired": 2, "reasons": {"holder": 2}, "ids": ["j1", "j3"]})
        );
        assert_eq!(
            ["j1", "j2", "j3"].map(|id| w.lease_of(id).get("holder").cloned()),
            [None, Some(json!("wren")), None]
        );
        // A lapsed lease expires whenever it was renewed.
        w.advance(60000);
        assert_eq!(
            w.jobs()
                .invoke_schema(
                    &operator(),
                    "expireHolder",
                    json!({"holder": "wren", "notRenewedAfter": T0})
                )
                .unwrap(),
            json!({"expired": 1, "reasons": {"ttl": 1}, "ids": ["j2"]})
        );
        let refused = refusal(w.invoke(
            &operator(),
            "expire",
            json!({"notRenewedAfter": T0}),
            "j1",
            None,
        ));
        assert_eq!(
            issues_of(&refused),
            [issue(
                "/notRenewedAfter",
                "notRenewedAfter spares a holder's active leases, so it needs holder"
            )]
        );
    }
}

#[test]
fn expire_holder_expires_every_lease_one_holder_has_on_the_schema_and_needs_override_permission() {
    for target in targets("lease_expire_holder_all") {
        let w = world(
            &target,
            with(requeue(), json!({"overridePermission": "jobs.override"})),
        );
        for id in ["j2", "j3"] {
            w.jobs().create(id, json!({ "title": id }));
        }
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &worker(),
            "transition",
            json!({"to": "running"}),
            "j1",
            None,
        )
        .unwrap();
        w.invoke(&worker(), "acquire", json!({}), "j2", None)
            .unwrap();
        w.invoke(&other(), "acquire", json!({}), "j3", None)
            .unwrap();
        w.advance(1000);
        assert_eq!(
            forbidden(
                w.jobs()
                    .invoke_schema(&other(), "expireHolder", json!({"holder": "wren"}))
            ),
            "otto may not expire the leases of a holder on Job: it needs permission jobs.override"
        );
        assert_eq!(
            w.jobs()
                .invoke_schema(&operator(), "expireHolder", json!({"holder": "wren"}))
                .unwrap(),
            json!({"expired": 2, "reasons": {"holder": 2}, "ids": ["j1", "j2"]})
        );
        let state = |w: &World, id: &str| {
            let lease = w.lease_of(id);
            (
                w.status_of(id),
                lease.get("holder").cloned(),
                lease["expiries"].clone(),
            )
        };
        assert_eq!(
            ["j1", "j2", "j3"].map(|id| state(&w, id)),
            [
                (json!("queued"), None, json!(1)),
                (json!("queued"), None, json!(1)),
                (json!("queued"), Some(json!("otto")), json!(0))
            ]
        );
        assert_eq!(
            w.jobs()
                .invoke_schema(&operator(), "expireHolder", json!({"holder": "wren"}))
                .unwrap(),
            json!({"expired": 0, "reasons": {}, "ids": []})
        );
    }
}

#[test]
fn expire_holder_is_forbidden_when_the_config_names_no_override_permission() {
    for target in targets("lease_expire_holder_unnamed") {
        let w = world(&target, Value::Null);
        assert_eq!(
            forbidden(w.jobs().invoke_schema(&operator(), "expireHolder", json!({"holder": "wren"}))),
            "opal may not expire the leases of a holder on Job: its Lease config names no overridePermission"
        );
    }
}

// ---- directives from another behavior ----

fn signalled(target: &Target, config: Value) -> World {
    let engine = target.open(
        superschematic_engine_behaviors::core()
            .with(Lease)
            .with(Signal),
    );
    publish(
        &engine,
        &jobs_document(json!([
            {"name": "Workflow", "config": job_flow()},
            lease(&config),
            {"name": "test.Signal"}
        ])),
    );
    let w = World { engine };
    w.jobs().create("j1", json!({"title": "Build"}));
    w
}

#[test]
fn a_behavior_of_the_type_sends_a_directive_through_call_without_the_permission_a_principals_direct_needs(
) {
    for target in targets("lease_signal") {
        let w = signalled(&target, json!({"directPermission": "jobs.direct"}));
        w.call(&worker(), "acquire").unwrap();
        assert_eq!(
            code(w.invoke(&worker(), "direct", json!({"name": "stop"}), "j1", None)),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            w.invoke(&worker(), "signal", json!({"name": "stop"}), "j1", None)
                .unwrap(),
            json!({"id": 1, "created": true})
        );
        let sent = directives(&w, &worker(), json!({}), 1);
        assert_eq!(
            (sent[0]["name"].clone(), sent[0]["createdBy"].clone()),
            (json!("stop"), json!("wren"))
        );
    }
}

#[test]
fn with_no_permission_in_the_config_a_principal_cannot_send_one_and_a_behavior_still_can() {
    for target in targets("lease_signal_unconfigured") {
        let w = signalled(&target, Value::Null);
        w.call(&worker(), "acquire").unwrap();
        let (behavior, action, why, _, _) =
            veto(w.invoke(&worker(), "direct", json!({"name": "stop"}), "j1", None));
        assert_eq!(
            (behavior.as_str(), action.as_str(), why.as_str()),
            (
                "Lease",
                "direct",
                "its config names no permission that sends directives (directPermission or overridePermission)"
            )
        );
        assert_eq!(
            w.invoke(&worker(), "signal", json!({"name": "stop"}), "j1", None)
                .unwrap(),
            json!({"id": 1, "created": true})
        );
        // There must still be a lease to direct.
        w.invoke(&worker(), "release", json!({}), "j1", Some(1))
            .unwrap();
        assert_eq!(
            reason(w.invoke(&worker(), "signal", json!({"name": "stop"}), "j1", None)),
            "no lease is active, so there is no holder to direct"
        );
    }
}

// ---- a call that changes nothing writes nothing ----

#[test]
fn a_call_that_changes_nothing_appends_no_event() {
    for target in targets("lease_unchanged") {
        let w = world(
            &target,
            json!({"overridePermission": "jobs.override", "directPermission": "jobs.direct"}),
        );
        w.call(&worker(), "acquire").unwrap();
        w.invoke(
            &sender(),
            "direct",
            json!({"name": "cancel", "dedupeKey": "once"}),
            "j1",
            None,
        )
        .unwrap();
        w.invoke(&worker(), "acknowledge", json!({"ids": [1]}), "j1", Some(1))
            .unwrap();
        let seq_and_events = |w: &World| (w.jobs().seq("j1"), w.jobs().events("j1").len());
        let before = seq_and_events(&w);
        assert_eq!(
            w.call(&other(), "expire").unwrap(),
            json!({"expired": false})
        );
        assert_eq!(
            w.invoke(
                &operator(),
                "expire",
                json!({"holder": "someone-else"}),
                "j1",
                None
            )
            .unwrap(),
            json!({"expired": false})
        );
        assert_eq!(
            w.invoke(
                &operator(),
                "expire",
                json!({"holder": "wren", "notRenewedAfter": T0 - 1}),
                "j1",
                None
            )
            .unwrap(),
            json!({"expired": false})
        );
        assert_eq!(
            w.invoke(
                &sender(),
                "direct",
                json!({"name": "cancel", "dedupeKey": "once"}),
                "j1",
                None
            )
            .unwrap(),
            json!({"id": 1, "created": false})
        );
        assert_eq!(
            w.invoke(&worker(), "acknowledge", json!({"ids": [1]}), "j1", Some(1))
                .unwrap(),
            json!({})
        );
        assert_eq!(
            w.call(&operator(), "resetExpiries").unwrap(),
            json!({"expiries": 0})
        );
        assert_eq!(seq_and_events(&w), before);
        // An If-Match read before them still matches.
        w.engine
            .instances()
            .update(
                &worker(),
                "Job",
                "j1",
                &json!({"title": "Same tag"}),
                WriteOptions {
                    expected_seq: Some(before.0),
                    ..WriteOptions::default()
                },
            )
            .unwrap();
    }
}

// ---- its config ----

#[test]
fn parse_config_holds_the_config_to_the_type() {
    for target in targets("lease_config") {
        let engine = target.open_workqueue();
        let refused = |config: Value| {
            define_refusal(
                &engine,
                &jobs_document(json!([
                    {"name": "Workflow", "config": job_flow()},
                    {"name": "Lease", "config": config},
                    {"name": "Comments"}
                ])),
            )
        };
        for (config, wanted) in [
            (json!({"heartbeatMs": 60000}), "behavior Lease config: heartbeatMs (60000) is more than half of ttlMs (60000): one late heartbeat would lose the lease"),
            (json!({"ttlMs": 3000, "heartbeatMs": 5000}), "heartbeatMs (5000) is more than half of ttlMs (3000)"),
            // Half of ttlMs is the most: one heartbeat can be late by a
            // whole interval.
            (json!({"ttlMs": 10000, "heartbeatMs": 5001}), "heartbeatMs (5001) is more than half of ttlMs (10000)"),
            (json!({"onExpiry": {"transition": "waiting", "from": ["running"]}}), "onExpiry.transition \"waiting\" is not a state of the type's Workflow (queued, running, done, failed)"),
            (json!({"onExpiry": {"transition": "queued", "from": ["paused"]}}), "onExpiry.from names \"paused\", which is not a state of the type's Workflow"),
            (json!({"onExpiry": {"transition": "queued", "from": ["done"]}}), "onExpiry: no transition of the type's Workflow leads from \"done\" to \"queued\""),
            (json!({"onExpiry": {"transition": "queued", "from": ["queued"]}}), "onExpiry moves the status from \"queued\" to itself"),
            (json!({"maxExpiries": 3, "escalate": {"transition": "done", "from": ["queued"]}}), "escalate: no transition of the type's Workflow leads from \"queued\" to \"done\""),
            (json!({"escalate": {"transition": "failed", "from": ["running"]}}), "escalate applies at the expiry that reaches maxExpiries, which the config does not give"),
            (json!({"exempt": ["Revisions.approve"]}), "exempt names Revisions.approve, but the type does not list Revisions (it lists Workflow, Lease, Comments)"),
            (json!({"exempt": ["Lease.release"]}), "exempt names Lease.release: Lease's own operations check their callers themselves"),
            (json!({"maxHoldField": "deadline"}), "maxHoldField \"deadline\" is not a field of Job (its fields: title, priority, timeLimitMs, topic, urgent)"),
            (json!({"maxHoldField": "title"}), "maxHoldField \"title\" is not an integer field of Job"),
        ] {
            let message = refused(config.clone());
            assert!(message.contains(wanted), "{config}: {message}");
        }
        let without_workflow = define_refusal(
            &engine,
            &jobs_document(json!([{"name": "Lease", "config": requeue()}])),
        );
        assert!(
            without_workflow.contains("behavior Lease config: onExpiry moves the status through Workflow, which the type does not list"),
            "{without_workflow}"
        );
        // The core meta-schema holds the shape first.
        let shape = refusal(engine.schemas().define(
            &alice(),
            jobs_document(json!([{"name": "Lease", "config": {"ttlMs": 500}}])),
            Default::default(),
        ));
        assert!(
            shape
                .issues()
                .iter()
                .any(|issue| issue.path == "/types/Job/behaviors/0/config/ttlMs"),
            "{shape}"
        );
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease", "config": with(requeue(), json!({"ttlMs": 10000, "heartbeatMs": 5000, "exempt": ["Comments.comment"], "maxHoldField": "timeLimitMs"}))},
                {"name": "Comments"}
            ])),
        );
    }
}

#[test]
fn it_can_be_added_to_a_schema_that_has_instances_which_start_free_and_cannot_be_removed_from_one()
{
    for target in targets("lease_config_change") {
        let engine = target.open_workqueue();
        let flow = json!({"name": "Workflow", "config": job_flow()});
        publish(&engine, &jobs_document(json!([flow])));
        let jobs = Instances {
            engine: &engine,
            schema: "Job",
        };
        jobs.create("j1", json!({"title": "Before"}));
        publish(
            &engine,
            &jobs_document(json!([flow, {"name": "Lease", "config": {"ttlMs": 30000}}])),
        );
        assert_eq!(
            jobs.behavior("j1", "Lease"),
            json!({"token": 0, "active": false, "expiries": 0})
        );
        // Its config may change.
        publish(
            &engine,
            &jobs_document(json!([flow, {"name": "Lease", "config": {"ttlMs": 20000}}])),
        );
        let refused = define_refusal(&engine, &jobs_document(json!([flow])));
        assert!(
            refused.contains("behavior Lease cannot be removed from type Job, which has instances: the leases and directives its instances hold would stay behind"),
            "{refused}"
        );
    }
}
