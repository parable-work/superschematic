//! Lease beside the core's behaviors on one type, on the Rust engine
//! (D58): the cases of the work queue's `test/lease-approvals.test.ts`
//! (a sign-off is another principal's write, which an active lease
//! refuses unless the config exempts signOff and withdrawSignOff),
//! `test/lease-timers.test.ts` (the runner's fire is another principal's
//! write, which the sweep leaves for its next run unless the config
//! exempts fireTimer), `test/lease-numbering.test.ts` (a create holds no
//! lease, and assignNumber is the holder's unless exempt) and
//! `test/lease-branches.test.ts` (Lease's release and Branches'
//! releaseCommit on one type), on SQLite and, with the variable set, on
//! Postgres. The Branches case waits for Branches in the Rust engine.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, Engine, Error, Principal, RunnerOptions};
use support::{alice, targets, veto};
use workqueue_support::{
    advance, cause_of, job_flow, jobs_document, principal, publish, run_due, Instances,
};

fn worker() -> Principal {
    principal("wren", &[])
}

fn other() -> Principal {
    principal("otto", &[])
}

fn jobs(engine: &Engine) -> Instances<'_> {
    Instances {
        engine,
        schema: "Job",
    }
}

// ---- beside Approvals ----

fn reviewer() -> Principal {
    principal("rae", &["jobs.review"])
}

// running opens an engine whose Job composes Workflow, Lease with the
// config given and Approvals gating done, with j1 leased by wren and
// running.
fn approvals_running(target: &support::Target, lease: Value) -> Engine {
    let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(1_000_000));
    publish(
        &engine,
        &jobs_document(json!([
            {"name": "Workflow", "config": job_flow()},
            {"name": "Lease", "config": lease},
            {"name": "Approvals", "config": {"gates": {"done": {"count": 1, "permission": "jobs.review"}}}}
        ])),
    );
    let jobs = jobs(&engine);
    jobs.create("j1", json!({"title": "Index"}));
    jobs.invoke_on(&worker(), "j1", "acquire", json!({}), None)
        .unwrap();
    jobs.invoke_on(
        &worker(),
        "j1",
        "transition",
        json!({"to": "running"}),
        None,
    )
    .unwrap();
    engine
}

#[test]
fn without_exempt_the_holders_lease_refuses_another_principals_sign_off() {
    for target in targets("lease_approvals_refused") {
        let engine = approvals_running(&target, json!({}));
        let (behavior, action, _, code, _) = veto(jobs(&engine).invoke_on(
            &reviewer(),
            "j1",
            "signOff",
            json!({"state": "done"}),
            None,
        ));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("Lease", "signOff", Some("held_by_another"))
        );
    }
}

#[test]
fn with_sign_off_exempt_a_reviewer_signs_a_leased_job_off_and_the_holders_change_consumes_it() {
    for target in targets("lease_approvals_exempt") {
        let engine = approvals_running(
            &target,
            json!({"exempt": ["Approvals.signOff", "Approvals.withdrawSignOff"]}),
        );
        let jobs = jobs(&engine);
        let invoke = |who: &Principal, operation: &str, params: Value| {
            jobs.invoke_on(who, "j1", operation, params, None)
        };
        let gates = || jobs.behavior("j1", "Approvals")["gates"].clone();
        let done = json!({"state": "done"});
        invoke(&reviewer(), "signOff", done.clone()).unwrap();
        invoke(&reviewer(), "withdrawSignOff", done.clone()).unwrap();
        invoke(&reviewer(), "signOff", done.clone()).unwrap();
        assert_eq!(gates(), json!({"done": {"signedBy": ["rae"]}}));

        // The holder changes the job: what was signed is not what moves.
        jobs.update(&worker(), "j1", json!({"title": "Index again"}), None)
            .unwrap();
        assert_eq!(gates(), json!({}));
        assert_eq!(
            veto(invoke(&worker(), "transition", json!({"to": "done"}))).3,
            Some("approvals_missing".to_owned())
        );
        invoke(&reviewer(), "signOff", done).unwrap();
        assert_eq!(
            invoke(&worker(), "transition", json!({"to": "done"})).unwrap(),
            json!({"from": "running", "to": "done"})
        );
        assert_eq!((jobs.status("j1"), gates()), (json!("done"), json!({})));
        // Exempt are the two Approvals operations, not every other
        // principal's write.
        assert_eq!(
            veto(jobs.update(&reviewer(), "j1", json!({"title": "Mine"}), None)).3,
            Some("held_by_another".to_owned())
        );
    }
}

// ---- beside Timers ----

const MINUTE: i64 = 60_000;

fn runner() -> Principal {
    principal("runner", &[])
}

// timers_running opens an engine whose Job composes Workflow, Lease with
// the config given and a Timers deadline that fails a running job ten
// minutes after it starts, with j1 leased by wren and running.
fn timers_running(target: &support::Target, lease: Value) -> Engine {
    let engine = target.open_workqueue_with(|options| {
        options.clock = Clock::Manual(1_000_000);
        options.runner = Some(RunnerOptions::new(runner()));
    });
    let mut config = json!({"ttlMs": 60 * MINUTE});
    if let (Value::Object(config), Value::Object(more)) = (&mut config, lease) {
        config.extend(more);
    }
    publish(
        &engine,
        &jobs_document(json!([
            {"name": "Workflow", "config": job_flow()},
            {"name": "Lease", "config": config},
            {"name": "Timers", "config": {"deadlines": {"timeout": {"start": {"enters": "running"}, "afterMs": 10 * MINUTE, "then": {"transition": "failed"}}}}}
        ])),
    );
    run_due(&engine);
    let jobs = jobs(&engine);
    jobs.create("j1", json!({"title": "Index"}));
    jobs.invoke_on(&worker(), "j1", "acquire", json!({}), None)
        .unwrap();
    jobs.invoke_on(
        &worker(),
        "j1",
        "transition",
        json!({"to": "running"}),
        None,
    )
    .unwrap();
    engine
}

fn timeout(engine: &Engine) -> Value {
    jobs(engine).behavior("j1", "Timers")["deadlines"]["timeout"].clone()
}

#[test]
fn without_exempt_the_holders_lease_refuses_the_runners_fire_and_the_sweep_leaves_the_deadline() {
    for target in targets("lease_timers_refused") {
        let engine = timers_running(&target, json!({}));
        advance(&engine, 11 * MINUTE);
        let pass = run_due(&engine);
        assert_eq!(
            (
                pass.failed,
                jobs(&engine).status("j1"),
                timeout(&engine)["fired"].clone()
            ),
            (0, json!("running"), json!(false))
        );
        let (behavior, action, _, code, _) = veto(jobs(&engine).invoke_on(
            &runner(),
            "j1",
            "fireTimer",
            json!({"deadline": "timeout"}),
            None,
        ));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("Lease", "fireTimer", Some("held_by_another"))
        );
        // The holder may fire it itself.
        assert_eq!(
            jobs(&engine)
                .invoke_on(
                    &worker(),
                    "j1",
                    "fireTimer",
                    json!({"deadline": "timeout"}),
                    None
                )
                .unwrap(),
            json!({"fired": true, "outcome": "moved"})
        );
    }
}

#[test]
fn with_fire_timer_exempt_the_runner_fires_a_leased_job_and_its_move_passes_the_guard() {
    for target in targets("lease_timers_exempt") {
        let engine = timers_running(&target, json!({"exempt": ["Timers.fireTimer"]}));
        advance(&engine, 11 * MINUTE);
        run_due(&engine);
        assert_eq!(
            (
                jobs(&engine).status("j1"),
                timeout(&engine)["outcome"].clone()
            ),
            (json!("failed"), json!("moved"))
        );
        let fire = jobs(&engine).last_event("j1");
        assert_eq!(
            (fire.actor.as_str(), cause_of(&fire)["schedule"].clone()),
            ("runner", json!("fire"))
        );
        // Exempt is the fire alone, not every other principal's write.
        assert_eq!(
            veto(jobs(&engine).update(&runner(), "j1", json!({"title": "Mine"}), None)).3,
            Some("held_by_another".to_owned())
        );
    }
}

// ---- beside Numbering ----

#[test]
fn a_create_is_numbered_under_anothers_lease_and_a_backfill_reaches_a_leased_instance_when_exempt()
{
    for target in targets("lease_numbering") {
        let engine = target.open_workqueue();
        let jobs = jobs(&engine);
        let numbering = json!({"name": "Numbering", "config": {"format": "J-{seq:3}"}});
        publish(&engine, &jobs_document(json!([{"name": "Lease"}])));
        for id in ["j1", "j2"] {
            jobs.create(id, json!({ "title": id }));
            jobs.invoke_on(&worker(), id, "acquire", json!({}), None)
                .unwrap();
        }
        publish(
            &engine,
            &jobs_document(json!([{"name": "Lease"}, numbering])),
        );
        assert_eq!(
            jobs.create_as(&worker(), "j3", json!({"title": "j3"}))
                .unwrap()
                .behaviors["Numbering"],
            json!({"seq": 1, "label": "J-001"})
        );

        // The backfill of a leased instance is the holder's alone.
        let (behavior, _, _, code, _) =
            veto(jobs.invoke_on(&alice(), "j1", "assignNumber", json!({}), None));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Lease", Some("held_by_another"))
        );
        assert_eq!(
            jobs.invoke_on(&worker(), "j1", "assignNumber", json!({}), None)
                .unwrap(),
            json!({"seq": 2, "label": "J-002"})
        );

        // Exempted, it reaches every instance, leased or not.
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Lease", "config": {"exempt": ["Numbering.assignNumber"]}},
                numbering
            ])),
        );
        assert_eq!(
            jobs.invoke_on(&alice(), "j2", "assignNumber", json!({}), None)
                .unwrap(),
            json!({"seq": 3, "label": "J-003"})
        );
        assert_eq!(jobs.behavior("j2", "Lease")["holder"], "wren");
    }
}

// ---- beside Branches ----

// recipe_document is a schema Recipe whose instances are version graphs of
// steps and take a lease.
fn recipe_document() -> Value {
    json!({
        "kind": "General",
        "name": "Recipe",
        "types": {
            "Recipe": {
                "name": "Recipe",
                "role": "EmbeddedStruct",
                "behaviors": [{"name": "Lease"}, {"name": "Branches", "config": {"kinds": {"step": {"type": "Step", "order": "position"}}}}],
                "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
            },
            "Step": {
                "name": "Step",
                "role": "EmbeddedStruct",
                "fields": [
                    {"name": "instruction", "typeRef": {"name": "string"}, "required": true},
                    {"name": "position", "typeRef": {"name": "Int"}, "required": true}
                ]
            }
        }
    })
}

#[test]
#[ignore = "needs Branches in the Rust engine (PR #377)"]
fn a_type_composes_lease_and_branches_and_each_runs_its_own_release() {
    for target in targets("lease_branches") {
        let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(1_000_000));
        publish(&engine, &recipe_document());
        let recipes = Instances {
            engine: &engine,
            schema: "Recipe",
        };
        recipes.create("soup", json!({"title": "Soup"}));
        let invoke = |who: &Principal,
                      operation: &str,
                      params: Value,
                      token: Option<i64>|
         -> Result<Value, Error> {
            recipes.invoke_on(who, "soup", operation, params, token)
        };
        let lease = || recipes.behavior("soup", "Lease");
        let composed: Vec<String> = engine
            .schemas()
            .behaviors(&alice(), "Recipe", Default::default())
            .expect("the behaviors")
            .into_iter()
            .map(|behavior| behavior.name)
            .collect();
        assert_eq!(composed, ["Lease", "Branches"]);
        let token = invoke(&worker(), "acquire", json!({}), None).unwrap()["token"]
            .as_i64()
            .unwrap();
        let draft = invoke(&worker(), "branch", json!({"name": "edit"}), None).unwrap();
        let saved = invoke(
            &worker(),
            "save",
            json!({"ref": draft["id"], "version": draft["version"], "edits": {"step": {"upsert": [{"instruction": "Boil", "position": 1}]}}}),
            None,
        )
        .unwrap();
        let committed = invoke(
            &worker(),
            "commit",
            json!({"ref": draft["id"], "version": saved["ref"]["version"]}),
            None,
        )
        .unwrap();
        let main = invoke(&worker(), "refs", json!({}), None).unwrap()["items"][0].clone();
        let merged = invoke(
            &worker(),
            "merge",
            json!({"source": committed["ref"]["id"], "target": main["id"], "targetVersion": main["version"], "tag": true}),
            None,
        )
        .unwrap();
        let commit = merged["commit"]["id"].clone();
        // Lease holds Branches' writes to the holder, as it holds any
        // write.
        assert_eq!(
            veto(invoke(
                &other(),
                "releaseCommit",
                json!({"commit": commit, "version": 0}),
                None
            ))
            .3,
            Some("held_by_another".to_owned())
        );
        assert_eq!(
            invoke(
                &worker(),
                "releaseCommit",
                json!({"commit": commit, "version": 0}),
                None
            )
            .unwrap(),
            json!({"commit": commit, "version": 1})
        );
        assert_eq!(
            invoke(&worker(), "released", json!({}), None).unwrap()["release"]["commit"],
            commit
        );
        assert_eq!(
            (lease()["holder"].clone(), lease()["active"].clone()),
            (json!("wren"), json!(true))
        );
        // Lease's release ends the lease, and leaves the release pointer as
        // it is.
        assert_eq!(
            invoke(&worker(), "release", json!({}), Some(token)).unwrap(),
            json!({})
        );
        assert_eq!(
            (
                lease().get("holder").cloned(),
                lease()["active"].clone(),
                lease()["ended"]["reason"].clone()
            ),
            (None, json!(false), json!("release"))
        );
        let releases: Vec<Value> = invoke(&other(), "releases", json!({}), None).unwrap()["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| json!([item["version"], item["commit"]]))
            .collect();
        assert_eq!(releases, [json!([1, commit])]);
    }
}
