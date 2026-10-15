//! Presence (D16), a heartbeat on an instance that stands for a worker, on
//! the Rust engine (D58): the cases of the work queue's
//! `test/presence.test.ts` (the first deadline at create, beat by the
//! principal the instance stands for and no other, miss applied once and
//! only past the deadline, onMissed only from its from states and onBeat
//! back, the runner's sweep on the engine clock, the guard on
//! principalField, the leases a miss expires on the releaseLeases schemas
//! through Lease's expireHolder, sparing the ones renewed since the last
//! beat and recording the rest, the codes its refusals carry, and the
//! config rules), on SQLite and, with the variable set, on Postgres. A
//! TypeScript case that opens a second engine is a test of its own here.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, Engine, EngineOptions, Principal, RunnerOptions};
use support::{alice, refusal, targets, veto, Target};
use workqueue_support::{
    advance, cause_of, define_refusal, job_flow, jobs_document, principal, publish, run_due,
    Instances, T0,
};

const TTL: i64 = 30000;

fn wren() -> Principal {
    principal("wren", &[])
}

fn otto() -> Principal {
    principal("otto", &[])
}

/// A worker is idle or busy while it beats, missing once it stops, and
/// stopped for good.
fn worker_flow() -> Value {
    json!({
        "states": ["idle", "busy", "missing", "stopped"],
        "transitions": [
            {"from": "idle", "to": "busy"},
            {"from": "busy", "to": "idle"},
            {"from": "idle", "to": "missing"},
            {"from": "busy", "to": "missing"},
            {"from": "missing", "to": "idle"},
            {"from": "idle", "to": "stopped"},
            {"from": "busy", "to": "stopped"},
            {"from": "missing", "to": "stopped"}
        ]
    })
}

fn presence_config() -> Value {
    json!({
        "ttlMs": TTL,
        "principalField": "subject",
        "onMissed": {"transition": "missing", "from": ["idle", "busy"]},
        "onBeat": {"transition": "idle", "from": ["missing"]}
    })
}

fn with(config: Value, more: Value) -> Value {
    let mut config = config;
    if let (Value::Object(config), Value::Object(more)) = (&mut config, more) {
        config.extend(more);
    }
    config
}

/// A General schema named Worker: the subject of the principal it stands
/// for, a name and its slots.
fn workers_document(behaviors: Value) -> Value {
    json!({
        "kind": "General",
        "name": "Worker",
        "types": {
            "Worker": {
                "name": "Worker",
                "role": "EmbeddedStruct",
                "behaviors": behaviors,
                "fields": [
                    {"name": "subject", "typeRef": {"name": "string"}, "required": true},
                    {"name": "name", "typeRef": {"name": "string"}},
                    {"name": "slots", "typeRef": {"name": "Generic.Int64"}}
                ]
            }
        }
    })
}

fn workers(engine: &Engine) -> Instances<'_> {
    Instances {
        engine,
        schema: "Worker",
    }
}

fn open(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_workqueue_with(|options| {
        options.clock = Clock::Manual(T0);
        configure(options);
    })
}

fn with_runner(permissions: &'static [&'static str]) -> impl FnOnce(&mut EngineOptions) {
    move |options| options.runner = Some(RunnerOptions::new(principal("runner", permissions)))
}

// world opens an engine whose Worker composes Workflow and Presence with
// the config given, on a clock at T0, with worker w1 for wren.
fn world(target: &Target, config: Value) -> Engine {
    let engine = open(target, with_runner(&[]));
    publish(
        &engine,
        &workers_document(json!([
            {"name": "Workflow", "config": worker_flow()},
            {"name": "Presence", "config": config}
        ])),
    );
    workers(&engine).create("w1", json!({"subject": "wren", "name": "Wren"}));
    engine
}

fn call(
    engine: &Engine,
    who: &Principal,
    operation: &str,
    id: &str,
) -> superschematic_engine::Result<Value> {
    workers(engine).invoke_on(who, id, operation, json!({}), None)
}

fn presence_of(engine: &Engine, id: &str) -> Value {
    workers(engine).behavior(id, "Presence")
}

fn status_of(engine: &Engine, id: &str) -> Value {
    workers(engine).status(id)
}

// ---- beat and miss ----

#[test]
fn a_create_sets_the_first_deadline_one_ttl_on() {
    for target in targets("presence_create") {
        let engine = world(&target, presence_config());
        assert_eq!(
            presence_of(&engine, "w1"),
            json!({"deadline": T0 + TTL, "missed": false})
        );
    }
}

#[test]
fn only_the_principal_the_instance_stands_for_can_beat_it() {
    for target in targets("presence_beat") {
        let engine = world(&target, presence_config());
        advance(&engine, 10000);
        assert_eq!(
            call(&engine, &wren(), "beat", "w1").unwrap(),
            json!({"deadline": T0 + 10000 + TTL})
        );
        assert_eq!(
            presence_of(&engine, "w1"),
            json!({"deadline": T0 + 10000 + TTL, "lastBeatAt": T0 + 10000, "missed": false})
        );
        // Knowing the subject the instance holds does not make another
        // principal it.
        let (behavior, action, reason, code, _) = veto(call(&engine, &otto(), "beat", "w1"));
        assert_eq!(
            (
                behavior.as_str(),
                action.as_str(),
                reason.as_str(),
                code.as_deref()
            ),
            (
                "Presence",
                "beat",
                "it stands for another principal, who alone may beat it",
                Some("not_principal")
            )
        );
        assert_eq!(presence_of(&engine, "w1")["lastBeatAt"], T0 + 10000);
    }
}

#[test]
fn miss_acts_once_and_only_past_the_deadline() {
    for target in targets("presence_miss") {
        let engine = world(&target, presence_config());
        advance(&engine, TTL - 1);
        assert_eq!(
            call(&engine, &otto(), "miss", "w1").unwrap(),
            json!({"missed": false})
        );
        assert_eq!(status_of(&engine, "w1"), "idle");
        // A miss that finds the deadline ahead changes nothing, and appends
        // no event.
        assert_eq!(workers(&engine).seq("w1"), 1);
        advance(&engine, 1);
        assert_eq!(
            call(&engine, &otto(), "miss", "w1").unwrap(),
            json!({"missed": true, "released": {}})
        );
        assert_eq!(
            presence_of(&engine, "w1"),
            json!({"deadline": T0 + TTL, "missed": true, "released": {}})
        );
        assert_eq!(status_of(&engine, "w1"), "missing");
        let seq = workers(&engine).seq("w1");
        assert_eq!(
            call(&engine, &otto(), "miss", "w1").unwrap(),
            json!({"missed": false})
        );
        assert_eq!(workers(&engine).seq("w1"), seq);
    }
}

#[test]
fn on_missed_moves_the_status_only_from_its_from_states() {
    for target in targets("presence_missed_stopped") {
        let engine = world(&target, presence_config());
        workers(&engine)
            .invoke_on(&wren(), "w1", "transition", json!({"to": "stopped"}), None)
            .unwrap();
        advance(&engine, TTL);
        assert_eq!(
            call(&engine, &otto(), "miss", "w1").unwrap(),
            json!({"missed": true, "released": {}})
        );
        assert_eq!(status_of(&engine, "w1"), "stopped");
        assert_eq!(presence_of(&engine, "w1")["missed"], true);
    }
}

#[test]
fn a_beat_recovers_a_missed_worker_and_on_beat_moves_the_status_back_only_from_its_from_states() {
    for target in targets("presence_recover") {
        let engine = world(&target, presence_config());
        advance(&engine, TTL);
        call(&engine, &otto(), "miss", "w1").unwrap();
        advance(&engine, 5000);
        assert_eq!(
            call(&engine, &wren(), "beat", "w1").unwrap(),
            json!({"deadline": T0 + TTL + 5000 + TTL})
        );
        assert_eq!(
            presence_of(&engine, "w1"),
            json!({"deadline": T0 + TTL + 5000 + TTL, "lastBeatAt": T0 + TTL + 5000, "missed": false, "released": {}})
        );
        assert_eq!(status_of(&engine, "w1"), "idle");
        // From busy, which onBeat does not list, a beat leaves the status.
        workers(&engine)
            .invoke_on(&wren(), "w1", "transition", json!({"to": "busy"}), None)
            .unwrap();
        call(&engine, &wren(), "beat", "w1").unwrap();
        assert_eq!(status_of(&engine, "w1"), "busy");
    }
}

#[test]
fn without_on_missed_and_on_beat_a_miss_and_a_beat_leave_the_status_and_workflow_is_not_needed() {
    for target in targets("presence_plain") {
        let engine = open(&target, |_| {});
        publish(
            &engine,
            &workers_document(
                json!([{"name": "Presence", "config": {"ttlMs": 1000, "principalField": "subject"}}]),
            ),
        );
        workers(&engine).create("w1", json!({"subject": "wren"}));
        advance(&engine, 1000);
        assert_eq!(
            call(&engine, &otto(), "miss", "w1").unwrap(),
            json!({"missed": true, "released": {}})
        );
        assert_eq!(workers(&engine).behavior("w1", "Workflow"), Value::Null);
        assert_eq!(
            call(&engine, &wren(), "beat", "w1").unwrap(),
            json!({"deadline": T0 + 2000})
        );
    }
}

// ---- the sweep ----

#[test]
fn the_runners_schedule_misses_each_worker_past_its_deadline_once_every_sweep_ms() {
    for target in targets("presence_sweep") {
        let engine = world(&target, with(presence_config(), json!({"sweepMs": 10000})));
        workers(&engine).create("w2", json!({"subject": "otto"}));
        // The runner finds the schedule and runs it an interval later.
        assert_eq!(run_due(&engine).scheduled, 0);
        advance(&engine, 10000);
        call(&engine, &otto(), "beat", "w2").unwrap();
        assert_eq!(run_due(&engine).scheduled, 1);
        assert_eq!(presence_of(&engine, "w1")["missed"], false);

        advance(&engine, 20000);
        assert_eq!(run_due(&engine).scheduled, 1);
        assert_eq!(status_of(&engine, "w1"), "missing");
        assert_eq!(presence_of(&engine, "w1")["missed"], true);
        assert_eq!(status_of(&engine, "w2"), "idle");
        let seq = workers(&engine).seq("w1");
        let miss = workers(&engine)
            .schema_events()
            .into_iter()
            .find(|event| event.kind == "operation" && event.instance_id.as_deref() == Some("w1"))
            .expect("the miss's event");
        assert_eq!(miss.actor, "runner");
        assert_eq!(
            cause_of(&miss),
            json!({"behavior": "Presence", "schedule": "miss", "depth": 1})
        );

        // A missed worker is not missed again; the other one is, at its own
        // deadline.
        advance(&engine, 10000);
        assert_eq!(run_due(&engine).scheduled, 1);
        assert_eq!(workers(&engine).seq("w1"), seq);
        assert_eq!(status_of(&engine, "w2"), "missing");
    }
}

#[test]
fn a_miss_that_fails_fails_the_run_which_the_runner_shows_and_leaves_the_worker_as_it_was() {
    for target in targets("presence_sweep_fails") {
        let mut flow = worker_flow();
        for edge in flow["transitions"].as_array_mut().unwrap() {
            if edge["to"] == "missing" {
                edge["permission"] = json!("workers.mark");
            }
        }
        let engine = open(&target, with_runner(&[]));
        publish(
            &engine,
            &workers_document(json!([
                {"name": "Workflow", "config": flow},
                {"name": "Presence", "config": presence_config()}
            ])),
        );
        workers(&engine).create("w1", json!({"subject": "wren"}));
        run_due(&engine);
        advance(&engine, TTL);
        assert_eq!(run_due(&engine).failed, 1);
        let status = engine.runner().status().expect("the runner's status");
        let schedule = status
            .schedules
            .iter()
            .find(|schedule| schedule.behavior == "Presence")
            .expect("Presence's schedule");
        assert_eq!(schedule.schedule, "miss");
        let error = schedule.error.clone().unwrap_or_default();
        assert!(
            error.contains("runner may not move Worker w1 from idle to missing: the transition needs permission workers.mark"),
            "{error}"
        );
        assert_eq!(
            presence_of(&engine, "w1"),
            json!({"deadline": T0 + TTL, "missed": false})
        );
        assert_eq!(status_of(&engine, "w1"), "idle");
    }
}

// ---- the guard ----

#[test]
fn an_update_that_changes_principal_field_is_refused_whoever_asks() {
    for target in targets("presence_guard") {
        let engine = world(&target, presence_config());
        let (behavior, action, reason, code, _) =
            veto(workers(&engine).update(&alice(), "w1", json!({"subject": "otto"}), None));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("Presence", "update", Some("principal_fixed"))
        );
        assert_eq!(
            reason,
            "subject holds the principal the instance stands for, so it cannot change once set"
        );
        workers(&engine)
            .update(
                &alice(),
                "w1",
                json!({"name": "Wren the second", "subject": "wren"}),
                None,
            )
            .unwrap();
        assert_eq!(workers(&engine).read("w1").data["name"], "Wren the second");
        assert_eq!(
            veto(call(&engine, &otto(), "beat", "w1")).2,
            "it stands for another principal, who alone may beat it"
        );
    }
}

#[test]
fn an_instance_whose_principal_field_holds_no_principal_cannot_be_beaten() {
    for target in targets("presence_no_principal") {
        let engine = open(&target, |_| {});
        publish(
            &engine,
            &workers_document(
                json!([{"name": "Presence", "config": {"ttlMs": TTL, "principalField": "subject"}}]),
            ),
        );
        workers(&engine).create("w0", json!({"subject": ""}));
        let (_, _, reason, code, _) = veto(call(&engine, &wren(), "beat", "w0"));
        assert_eq!(
            (reason.as_str(), code.as_deref()),
            (
                "its subject holds no principal, so no one may beat it",
                Some("no_principal")
            )
        );
    }
}

// ---- releaseLeases ----

// fleet opens an engine with Presence on Worker and Lease on Job, whose
// expiry puts a running job back in the queue: worker w1 stands for wren,
// who holds the leases of jobs j1 and j2, and otto holds j3's. The leases
// last 60 s, so none lapses before the worker's 30 s TTL.
fn fleet(target: &Target, runner: &'static [&'static str]) -> Engine {
    let engine = open(target, with_runner(runner));
    publish(
        &engine,
        &jobs_document(json!([
            {"name": "Workflow", "config": job_flow()},
            {"name": "Lease", "config": {"onExpiry": {"transition": "queued", "from": ["running"]}, "overridePermission": "jobs.override"}}
        ])),
    );
    publish(
        &engine,
        &workers_document(json!([
            {"name": "Workflow", "config": worker_flow()},
            {"name": "Presence", "config": with(presence_config(), json!({"releaseLeases": ["Job"]}))}
        ])),
    );
    workers(&engine).create("w1", json!({"subject": "wren"}));
    let jobs = Instances {
        engine: &engine,
        schema: "Job",
    };
    for (id, who) in [("j1", wren()), ("j2", wren()), ("j3", otto())] {
        jobs.create(id, json!({ "title": id }));
        jobs.invoke_on(&who, id, "acquire", json!({}), None)
            .unwrap();
        jobs.invoke_on(&who, id, "transition", json!({"to": "running"}), None)
            .unwrap();
    }
    run_due(&engine);
    engine
}

// jobs_of reads each job's status, holder and count of expiries.
fn jobs_of(engine: &Engine) -> Vec<Value> {
    let jobs = Instances {
        engine,
        schema: "Job",
    };
    ["j1", "j2", "j3"]
        .iter()
        .map(|id| {
            let behaviors = jobs.read(id).behaviors;
            json!([
                behaviors["Workflow"]["status"],
                behaviors["Lease"]
                    .get("holder")
                    .cloned()
                    .unwrap_or(Value::Null),
                behaviors["Lease"]["expiries"]
            ])
        })
        .collect()
}

#[test]
fn a_miss_expires_every_lease_the_workers_principal_holds_on_the_release_leases_schemas_and_no_other(
) {
    for target in targets("presence_release") {
        let engine = fleet(&target, &["jobs.override"]);
        advance(&engine, TTL);
        assert_eq!(run_due(&engine).failed, 0);
        assert_eq!(status_of(&engine, "w1"), "missing");
        assert_eq!(
            jobs_of(&engine),
            [
                json!(["queued", null, 1]),
                json!(["queued", null, 1]),
                json!(["running", "otto", 0])
            ]
        );
        // Each expiry is Lease's expire on the job, with its own event,
        // caused by the sweep.
        let expiries: Vec<Value> = Instances {
            engine: &engine,
            schema: "Job",
        }
        .schema_events()
        .iter()
        .filter(|event| event.kind == "operation" && event.actor == "runner")
        .map(|event| {
            json!([
                event.instance_id,
                event.change["operation"],
                event.change["params"],
                cause_of(event)
            ])
        })
        .collect();
        let cause = json!({"behavior": "Presence", "schedule": "miss", "depth": 1});
        assert_eq!(
            expiries,
            [
                json!(["j1", "expire", {"holder": "wren", "notRenewedAfter": T0}, cause]),
                json!(["j2", "expire", {"holder": "wren", "notRenewedAfter": T0}, cause])
            ]
        );
    }
}

#[test]
fn a_miss_spares_a_lease_its_holder_renewed_after_the_workers_last_beat_and_records_the_ones_it_expired(
) {
    for target in targets("presence_release_spared") {
        let engine = fleet(&target, &["jobs.override"]);
        let jobs = Instances {
            engine: &engine,
            schema: "Job",
        };
        advance(&engine, 5000);
        call(&engine, &wren(), "beat", "w1").unwrap();
        // The worker's beats stop at T0 + 5000; j2's heartbeats go on.
        advance(&engine, 15000);
        jobs.invoke_on(&wren(), "j2", "heartbeat", json!({}), Some(1))
            .unwrap();
        advance(&engine, 15000);
        assert_eq!(run_due(&engine).failed, 0);
        assert_eq!(status_of(&engine, "w1"), "missing");
        assert_eq!(
            jobs_of(&engine),
            [
                json!(["queued", null, 1]),
                json!(["running", "wren", 0]),
                json!(["running", "otto", 0])
            ]
        );
        // The miss's event carries what it released; the expiry's, the time
        // it was given.
        assert_eq!(
            presence_of(&engine, "w1")["released"],
            json!({"Job": ["j1"]})
        );
        let miss = workers(&engine)
            .events("w1")
            .into_iter()
            .find(|event| event.change["operation"] == "miss")
            .expect("the miss's event");
        assert_eq!(
            miss.change["patch"]["behaviors"]["Presence"],
            json!({"missed": true, "released": {"Job": ["j1"]}})
        );
        assert_eq!(
            jobs.last_event("j1").change["params"],
            json!({"holder": "wren", "notRenewedAfter": T0 + 5000})
        );
    }
}

#[test]
fn a_miss_leaves_the_leases_while_another_instance_for_the_same_principal_is_present() {
    for target in targets("presence_release_elsewhere") {
        let engine = fleet(&target, &["jobs.override"]);
        let jobs = Instances {
            engine: &engine,
            schema: "Job",
        };
        workers(&engine).create("w2", json!({"subject": "wren"}));
        advance(&engine, TTL);
        call(&engine, &wren(), "beat", "w2").unwrap();
        // The holders renew their leases, so none lapses before the second
        // worker is missed.
        for (id, who) in [("j1", wren()), ("j2", wren()), ("j3", otto())] {
            jobs.invoke_on(&who, id, "heartbeat", json!({}), Some(1))
                .unwrap();
        }
        run_due(&engine);
        assert_eq!(
            (
                presence_of(&engine, "w1")["missed"].clone(),
                presence_of(&engine, "w2")["missed"].clone()
            ),
            (json!(true), json!(false))
        );
        assert_eq!(
            jobs_of(&engine),
            [
                json!(["running", "wren", 0]),
                json!(["running", "wren", 0]),
                json!(["running", "otto", 0])
            ]
        );
        advance(&engine, TTL);
        run_due(&engine);
        assert_eq!(presence_of(&engine, "w2")["missed"], true);
        assert_eq!(
            jobs_of(&engine),
            [
                json!(["queued", null, 1]),
                json!(["queued", null, 1]),
                json!(["running", "otto", 0])
            ]
        );
    }
}

#[test]
fn without_leases_override_permission_the_runners_miss_fails_and_nothing_is_missed_or_released() {
    for target in targets("presence_release_forbidden") {
        let engine = fleet(&target, &[]);
        advance(&engine, TTL);
        assert_eq!(run_due(&engine).failed, 1);
        let status = engine.runner().status().expect("the runner's status");
        let error = status
            .schedules
            .iter()
            .find(|schedule| schedule.behavior == "Presence")
            .and_then(|schedule| schedule.error.clone())
            .unwrap_or_default();
        assert!(
            error.contains("runner may not expire the leases of a holder on Job: it needs permission jobs.override"),
            "{error}"
        );
        assert_eq!(presence_of(&engine, "w1")["missed"], false);
        assert_eq!(
            jobs_of(&engine),
            [
                json!(["running", "wren", 0]),
                json!(["running", "wren", 0]),
                json!(["running", "otto", 0])
            ]
        );
    }
}

// ---- the config ----

fn define(engine: &Engine, config: Value, behaviors: Value) -> String {
    let mut all = behaviors.as_array().cloned().unwrap_or_default();
    all.push(json!({"name": "Presence", "config": config}));
    define_refusal(engine, &workers_document(Value::Array(all)))
}

#[test]
fn principal_field_is_a_string_field_and_on_missed_and_on_beat_need_workflow_and_its_states() {
    for target in targets("presence_config") {
        let engine = target.open_workqueue();
        let flow = json!([{"name": "Workflow", "config": worker_flow()}]);
        for (config, behaviors, wanted) in [
            (json!({"ttlMs": TTL, "principalField": "who"}), flow.clone(), "principalField \"who\" is not a field of Worker (its fields: subject, name, slots)"),
            (json!({"ttlMs": TTL, "principalField": "slots"}), flow.clone(), "principalField \"slots\" is not a string field of Worker"),
            (presence_config(), json!([]), "onMissed moves the status through Workflow, which the type does not list"),
            (with(presence_config(), json!({"onMissed": {"transition": "gone", "from": ["idle"]}})), flow.clone(), "onMissed.transition \"gone\" is not a state of the type's Workflow"),
            (with(presence_config(), json!({"onBeat": {"transition": "idle", "from": ["stopped"]}})), flow.clone(), "onBeat: no transition of the type's Workflow leads from \"stopped\" to \"idle\""),
            (with(presence_config(), json!({"ttlMs": 999})), flow.clone(), "ttlMs"),
        ] {
            let message = define(&engine, config.clone(), behaviors);
            assert!(message.contains(wanted), "{config}: {message}");
        }
    }
}

#[test]
fn release_leases_names_live_schemas_that_compose_lease() {
    for target in targets("presence_config_leases") {
        let engine = target.open_workqueue();
        let flow = json!([{"name": "Workflow", "config": worker_flow()}]);
        let releasing = |schema: &str| with(presence_config(), json!({"releaseLeases": [schema]}));
        let message = define(&engine, releasing("Job"), flow.clone());
        assert!(
            message.contains("releaseLeases: schema Job has no live version; publish it first"),
            "{message}"
        );
        publish(
            &engine,
            &jobs_document(json!([{"name": "Workflow", "config": job_flow()}])),
        );
        let message = define(&engine, releasing("Job"), flow.clone());
        assert!(
            message.contains(
                "releaseLeases: Job does not compose Lease, so it holds no lease to expire"
            ),
            "{message}"
        );
        publish(
            &engine,
            &workqueue_support::jobs_document_named(
                json!([{"name": "Workflow", "config": job_flow()}, {"name": "Lease"}]),
                "Task",
            ),
        );
        engine
            .schemas()
            .define(
                &alice(),
                workers_document(json!([
                    {"name": "Workflow", "config": worker_flow()},
                    {"name": "Presence", "config": releasing("Task")}
                ])),
                Default::default(),
            )
            .expect("define");
    }
}

#[test]
fn a_new_version_keeps_principal_field_and_presence_is_not_removed_from_a_schema_with_instances() {
    for target in targets("presence_config_change") {
        let engine = world(&target, presence_config());
        let next = |config: Option<Value>| {
            let mut behaviors = vec![json!({"name": "Workflow", "config": worker_flow()})];
            if let Some(config) = config {
                behaviors.push(json!({"name": "Presence", "config": config}));
            }
            workers_document(Value::Array(behaviors))
        };
        let moved = define_refusal(
            &engine,
            &next(Some(with(
                presence_config(),
                json!({"principalField": "name"}),
            ))),
        );
        assert!(moved.contains("principalField"), "{moved}");
        let removed = define_refusal(&engine, &next(None));
        assert!(removed.contains("would stay behind"), "{removed}");
        publish(
            &engine,
            &next(Some(with(
                presence_config(),
                json!({"ttlMs": 60000, "sweepMs": 1000}),
            ))),
        );
        assert_eq!(
            call(&engine, &wren(), "beat", "w1").unwrap(),
            json!({"deadline": T0 + 60000})
        );
        // With no instance, no principal is held where the old config said:
        // principalField may move.
        workers(&engine).delete(&alice(), "w1", None).unwrap();
        publish(
            &engine,
            &next(Some(with(
                presence_config(),
                json!({"principalField": "name"}),
            ))),
        );
    }
}

#[test]
fn beat_and_miss_take_no_parameters() {
    for target in targets("presence_params") {
        let engine = world(&target, presence_config());
        let refused = refusal(workers(&engine).invoke_on(
            &wren(),
            "w1",
            "beat",
            json!({"subject": "wren"}),
            None,
        ));
        assert!(
            refused
                .message()
                .contains("operation beat of behavior Presence"),
            "{refused}"
        );
    }
}
