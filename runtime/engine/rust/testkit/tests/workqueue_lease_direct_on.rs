//! Lease's directOn on the Rust engine (D58): the cases of the work
//! queue's `test/lease-direct-on.test.ts`. When the target of a link moves
//! on, a new revision of Revisions or a release of Branches, the runner
//! sends the holder of each instance linked to it the config's directive
//! through Lease's direct, as its principal, once per lease and move of a
//! target (direct's dedupeKey). An instance with no lease hears nothing; a
//! pinned link tells only the instances the target moved past; a
//! many-valued link hears each of its targets. Without directOn Lease's
//! reactions are off, and they start at the publish that turns them on.
//! On SQLite and, with the variable set, on Postgres.
//!
//! The Recipe schema is a version graph's root (Branches) where the engine
//! has Branches; until the Rust engine does (PR #377), it is a plain schema
//! that the cases on revisions only link to, and the cases on releases
//! wait.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, Engine, EngineOptions, Principal, RunnerOptions};
use support::{alice, refusal, targets, Target};
use workqueue_support::{job_flow, jobs_document, principal, publish, run_due, Instances, T0};

fn runner() -> Principal {
    principal("runner", &["jobs.direct"])
}

fn wren() -> Principal {
    principal("wren", &[])
}

fn otto() -> Principal {
    principal("otto", &[])
}

/// Plan keeps revisions; a job pins one.
fn plans() -> Value {
    json!({
        "kind": "General",
        "name": "Plan",
        "types": {"Plan": {"name": "Plan", "role": "EmbeddedStruct", "behaviors": [{"name": "Revisions"}], "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]}}
    })
}

/// Recipe is a version graph's root, with covers, which a release points
/// at; on an engine without Branches, a plain schema.
fn recipes(engine: &Engine) -> Value {
    let behaviors = if engine.behaviors().has("Branches") {
        json!([{"name": "Branches", "config": {"kinds": {"cover": {"type": "Cover"}}}}])
    } else {
        // TODO(integration): Branches arrives with PR #377; then every
        // case publishes the version graph.
        json!([])
    };
    json!({
        "kind": "General",
        "name": "Recipe",
        "types": {
            "Recipe": {
                "name": "Recipe",
                "role": "EmbeddedStruct",
                "behaviors": behaviors,
                "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
            },
            "Cover": {"name": "Cover", "role": "EmbeddedStruct", "fields": [{"name": "photoUrl", "typeRef": {"name": "string"}, "required": true}]}
        }
    })
}

fn links() -> Value {
    json!({"plan": {"schema": "Plan", "pinned": true}, "notes": {"schema": "Plan"}, "recipe": {"schema": "Recipe"}})
}

fn rebase() -> Value {
    json!({"revised": {"link": "plan"}, "name": "rebase", "data": {"why": "the plan moved"}})
}

fn jobs_with(lease: Value, more: Value) -> Value {
    let mut all = links();
    if let (Value::Object(all), Value::Object(more)) = (&mut all, more) {
        all.extend(more);
    }
    jobs_document(json!([
        {"name": "Workflow", "config": job_flow()},
        {"name": "Links", "config": {"links": all}},
        {"name": "Lease", "config": lease}
    ]))
}

fn direct_config(direct_on: Value) -> Value {
    json!({"directPermission": "jobs.direct", "directOn": direct_on})
}

/// Plan p1 at revision 1, notes p2, and jobs j1 to j3 linked to both, j1
/// held by wren and j3 by otto, j2 free.
struct World {
    engine: Engine,
    j1: i64,
    j3: i64,
}

impl World {
    fn jobs(&self) -> Instances<'_> {
        Instances {
            engine: &self.engine,
            schema: "Job",
        }
    }

    fn plans(&self) -> Instances<'_> {
        Instances {
            engine: &self.engine,
            schema: "Plan",
        }
    }

    fn recipes(&self) -> Instances<'_> {
        Instances {
            engine: &self.engine,
            schema: "Recipe",
        }
    }

    // directives are what a heartbeat of a job's holder returns.
    fn directives(&self, who: &Principal, id: &str, token: i64) -> Vec<Value> {
        self.jobs()
            .invoke_on(who, id, "heartbeat", json!({}), Some(token))
            .unwrap()["directives"]
            .as_array()
            .unwrap()
            .clone()
    }

    fn keys(&self, who: &Principal, id: &str, token: i64) -> Vec<Value> {
        self.directives(who, id, token)
            .iter()
            .map(|directive| directive["dedupeKey"].clone())
            .collect()
    }

    fn revise(&self, id: &str, title: &str) {
        self.plans()
            .update(&alice(), id, json!({ "title": title }), None)
            .unwrap();
    }

    fn link(&self, who: &Principal, job: &str, name: &str, id: &str, token: i64) {
        self.jobs()
            .invoke_on(
                who,
                job,
                "link",
                json!({"name": name, "id": id}),
                Some(token),
            )
            .unwrap();
    }

    fn subscription(&self, schema: &str) -> Option<superschematic_engine::SubscriptionStatus> {
        self.engine
            .runner()
            .status()
            .expect("the runner's status")
            .subscriptions
            .into_iter()
            .find(|subscription| subscription.schema == schema && subscription.behavior == "Lease")
    }

    fn state(&self, schema: &str) -> Value {
        self.subscription(schema)
            .map_or(Value::Null, |subscription| {
                serde_json::to_value(subscription.state).expect("JSON")
            })
    }

    // tagged writes a cover on a draft of soup's primary line, commits it
    // and merges it in, tagged, and returns the merge's commit.
    fn tagged(&self, name: &str) -> Value {
        let call = |operation: &str, params: Value| {
            self.recipes()
                .invoke_on(&alice(), "soup", operation, params, None)
                .unwrap()
        };
        let main = call("refs", json!({}))["items"]
            .as_array()
            .unwrap()
            .iter()
            .find(|item| item["name"] == "main")
            .cloned()
            .unwrap();
        let draft = call("branch", json!({"fromRef": main["id"], "name": name}));
        let saved = call(
            "save",
            json!({"ref": draft["id"], "version": draft["version"], "edits": {"cover": {"upsert": [{"photoUrl": format!("{name}.jpg")}]}}}),
        );
        let committed = call(
            "commit",
            json!({"ref": saved["ref"]["id"], "version": saved["ref"]["version"]}),
        );
        call(
            "merge",
            json!({"source": committed["ref"]["id"], "target": main["id"], "targetVersion": main["version"], "tag": true}),
        )["commit"]["id"]
            .clone()
    }

    fn release(&self, commit: &Value, version: i64) {
        self.recipes()
            .invoke_on(
                &alice(),
                "soup",
                "releaseCommit",
                json!({"commit": commit, "version": version}),
                None,
            )
            .unwrap();
    }
}

fn world_with(
    target: &Target,
    lease: Value,
    more: Value,
    configure: impl FnOnce(&mut EngineOptions),
) -> World {
    let engine = target.open_workqueue_with(|options| {
        options.clock = Clock::Manual(T0);
        let mut runner = RunnerOptions::new(runner());
        runner.max_attempts = Some(1);
        options.runner = Some(runner);
        configure(options);
    });
    publish(&engine, &plans());
    publish(&engine, &recipes(&engine));
    publish(&engine, &jobs_with(lease, more));
    let created = |schema: &str, id: &str, data: Value| {
        Instances {
            engine: &engine,
            schema,
        }
        .create(id, data);
    };
    created("Plan", "p1", json!({"title": "v1"}));
    created("Plan", "p2", json!({"title": "notes"}));
    for id in ["j1", "j2", "j3"] {
        engine
            .instances()
            .create(
                &alice(),
                "Job",
                &json!({ "title": id }),
                superschematic_engine::CreateOptions {
                    id: Some(id),
                    behaviors: Some(&json!({"Links": {"plan": "p1", "notes": "p2"}})),
                    ..Default::default()
                },
            )
            .expect("create");
    }
    let jobs = Instances {
        engine: &engine,
        schema: "Job",
    };
    let token = |who: &Principal, id: &str| {
        jobs.invoke_on(who, id, "acquire", json!({}), None).unwrap()["token"]
            .as_i64()
            .unwrap()
    };
    let (j1, j3) = (token(&wren(), "j1"), token(&otto(), "j3"));
    World { engine, j1, j3 }
}

fn world(target: &Target) -> World {
    world_with(target, direct_config(json!([rebase()])), json!({}), |_| {})
}

#[test]
fn a_plans_new_revision_tells_the_holder_of_each_job_pinned_to_an_earlier_one_once_per_lease_and_revision(
) {
    for target in targets("direct_on_revision") {
        let w = world(&target);
        w.revise("p1", "v2");
        let revised = w.plans().last_event("p1");
        run_due(&w.engine);
        let sent: Vec<Value> = w
            .directives(&wren(), "j1", w.j1)
            .iter()
            .map(|directive| {
                json!({
                    "id": directive["id"], "name": directive["name"], "data": directive["data"],
                    "dedupeKey": directive["dedupeKey"], "createdBy": directive["createdBy"]
                })
            })
            .collect();
        assert_eq!(
            sent,
            [json!({
                "id": 1,
                "name": "rebase",
                "data": {"why": "the plan moved", "revised": {"link": "plan", "schema": "Plan", "id": "p1", "revision": 2}},
                "dedupeKey": "revised plan p1 2",
                "createdBy": "runner"
            })]
        );
        assert_eq!(w.directives(&otto(), "j3", w.j3)[0]["name"], "rebase");
        // Its event is the runner's, caused by the revision.
        let direct = w
            .jobs()
            .events("j1")
            .into_iter()
            .find(|event| event.change["operation"] == "direct")
            .expect("direct's event");
        assert_eq!(
            (direct.actor.as_str(), workqueue_support::cause_of(&direct)),
            (
                "runner",
                json!({"behavior": "Lease", "event": revised.cursor, "depth": 1})
            )
        );
        // The dedupe key is direct's: sent again under the lease, it
        // stands.
        assert_eq!(
            w.jobs()
                .invoke_on(
                    &runner(),
                    "j1",
                    "direct",
                    json!({"name": "rebase", "dedupeKey": "revised plan p1 2"}),
                    None
                )
                .unwrap(),
            json!({"id": 1, "created": false})
        );
        // j2 had no lease: nothing was sent, and nothing waits for its next
        // holder.
        let token = w
            .jobs()
            .invoke_on(&wren(), "j2", "acquire", json!({}), None)
            .unwrap()["token"]
            .as_i64()
            .unwrap();
        assert_eq!(w.directives(&wren(), "j2", token), Vec::<Value>::new());
        assert_eq!(w.state("Job"), "active");
    }
}

#[test]
fn a_pinned_link_tells_only_the_jobs_the_target_moved_past_a_plain_link_every_holder_and_a_lapsed_lease_nothing(
) {
    for target in targets("direct_on_pinned") {
        let w = world_with(
            &target,
            json!({"directPermission": "jobs.direct", "ttlMs": 60_000, "directOn": [rebase(), {"revised": {"link": "notes"}, "name": "reread"}]}),
            json!({}),
            |_| {},
        );
        w.revise("p1", "v2");
        // Before the runner hears it, j3 is pinned to revision 2.
        w.link(&otto(), "j3", "plan", "p1", w.j3);
        run_due(&w.engine);
        assert_eq!(w.keys(&wren(), "j1", w.j1), [json!("revised plan p1 2")]);
        assert_eq!(w.directives(&otto(), "j3", w.j3), Vec::<Value>::new());
        w.revise("p2", "notes, revised");
        run_due(&w.engine);
        let reread: Vec<Value> = w
            .directives(&otto(), "j3", w.j3)
            .iter()
            .map(|directive| json!([directive["name"], directive["data"]]))
            .collect();
        assert_eq!(
            reread,
            [
                json!(["reread", {"revised": {"link": "notes", "schema": "Plan", "id": "p2", "revision": 2}}])
            ]
        );
        // j1's lease lapses: direct refuses it, and the subscription goes
        // on.
        workqueue_support::advance(&w.engine, 60_001);
        w.revise("p2", "notes, again");
        run_due(&w.engine);
        assert_eq!(w.state("Job"), "active");
        // Its two directives, rebase and the first reread, and no third.
        assert_eq!(
            w.jobs()
                .events("j1")
                .iter()
                .filter(|event| event.change["operation"] == "direct")
                .count(),
            2
        );
    }
}

#[test]
#[ignore = "needs Branches in the Rust engine (PR #377)"]
fn a_release_of_a_recipe_the_job_links_to_tells_its_holder_with_the_commit() {
    for target in targets("direct_on_release") {
        let w = world_with(
            &target,
            direct_config(json!([{"revised": {"link": "recipe"}, "name": "reload"}])),
            json!({}),
            |_| {},
        );
        w.recipes().create("soup", json!({"title": "Soup"}));
        w.link(&wren(), "j1", "recipe", "soup", w.j1);
        let merged = w.tagged("cover");
        run_due(&w.engine);
        assert_eq!(w.directives(&wren(), "j1", w.j1), Vec::<Value>::new());
        w.release(&merged, 0);
        run_due(&w.engine);
        let sent: Vec<Value> = w
            .directives(&wren(), "j1", w.j1)
            .iter()
            .map(|directive| json!([directive["name"], directive["data"], directive["dedupeKey"]]))
            .collect();
        assert_eq!(
            sent,
            [
                json!(["reload", {"revised": {"link": "recipe", "schema": "Recipe", "id": "soup", "release": 1, "commit": merged}}, "released recipe soup 1"])
            ]
        );
    }
}

#[test]
#[ignore = "needs Branches in the Rust engine (PR #377)"]
fn a_link_pinned_to_a_release_tells_only_the_jobs_pinned_to_an_earlier_release() {
    for target in targets("direct_on_release_pinned") {
        let w = world_with(
            &target,
            direct_config(json!([{"revised": {"link": "recipe"}, "name": "reload"}])),
            json!({"recipe": {"schema": "Recipe", "pinned": "release"}}),
            |_| {},
        );
        w.recipes().create("soup", json!({"title": "Soup"}));
        let first = w.tagged("first");
        w.release(&first, 0);
        w.link(&wren(), "j1", "recipe", "soup", w.j1);
        w.link(&otto(), "j3", "recipe", "soup", w.j3);
        // Release 1 was made before either pinned it: no one is behind.
        run_due(&w.engine);
        assert_eq!(w.directives(&wren(), "j1", w.j1), Vec::<Value>::new());
        let second = w.tagged("second");
        w.release(&second, 1);
        // Before the runner hears it, j3 is pinned to release 2.
        w.link(&otto(), "j3", "recipe", "soup", w.j3);
        run_due(&w.engine);
        assert_eq!(
            w.keys(&wren(), "j1", w.j1),
            [json!("released recipe soup 2")]
        );
        assert_eq!(w.directives(&otto(), "j3", w.j3), Vec::<Value>::new());
    }
}

#[test]
fn a_many_valued_link_tells_the_holder_of_each_job_that_holds_a_target_the_target_moved_past() {
    for target in targets("direct_on_many") {
        let w = world_with(
            &target,
            direct_config(json!([{"revised": {"link": "plans"}, "name": "replan"}])),
            json!({"plans": {"schema": "Plan", "many": true, "max": 3, "pinned": true}}),
            |_| {},
        );
        w.plans().create("p3", json!({"title": "p3"}));
        w.link(&wren(), "j1", "plans", "p2", w.j1);
        w.link(&wren(), "j1", "plans", "p3", w.j1);
        w.link(&otto(), "j3", "plans", "p3", w.j3);
        w.revise("p2", "notes, revised");
        w.revise("p3", "p3, revised");
        run_due(&w.engine);
        // Both of j1's targets moved to revision 2: a directive for each.
        let sent: Vec<Value> = w
            .directives(&wren(), "j1", w.j1)
            .iter()
            .map(|directive| json!([directive["data"]["revised"], directive["dedupeKey"]]))
            .collect();
        assert_eq!(
            sent,
            [
                json!([{"link": "plans", "schema": "Plan", "id": "p2", "revision": 2}, "revised plans p2 2"]),
                json!([{"link": "plans", "schema": "Plan", "id": "p3", "revision": 2}, "revised plans p3 2"])
            ]
        );
        assert_eq!(w.keys(&otto(), "j3", w.j3), [json!("revised plans p3 2")]);
    }
}

#[test]
fn without_direct_on_the_reactions_are_off_and_a_version_that_adds_it_hears_only_what_comes_after_its_publish(
) {
    for target in targets("direct_on_off") {
        let w = world_with(
            &target,
            json!({"directPermission": "jobs.direct"}),
            json!({}),
            |_| {},
        );
        w.revise("p1", "v2");
        run_due(&w.engine);
        assert!(w.subscription("Job").is_none());
        publish(
            &w.engine,
            &jobs_with(direct_config(json!([rebase()])), json!({})),
        );
        run_due(&w.engine);
        assert_eq!(w.directives(&wren(), "j1", w.j1), Vec::<Value>::new());
        w.revise("p1", "v3");
        run_due(&w.engine);
        assert_eq!(w.keys(&wren(), "j1", w.j1), [json!("revised plan p1 3")]);
    }
}

#[test]
fn a_runner_whose_principal_lacks_the_permission_fails_the_subscription_forbidden() {
    for target in targets("direct_on_forbidden") {
        let w = world_with(
            &target,
            direct_config(json!([rebase()])),
            json!({}),
            |options| {
                let mut runner = RunnerOptions::new(principal("runner", &[]));
                runner.max_attempts = Some(1);
                options.runner = Some(runner);
            },
        );
        w.revise("p1", "v2");
        run_due(&w.engine);
        let failed = w.subscription("Job").expect("Lease's subscription");
        assert_eq!(serde_json::to_value(failed.state).unwrap(), "halted");
        let error = failed.failure.expect("a failure").error;
        assert!(
            error.starts_with("forbidden: runner may not send a directive to the holder of Job j1: it needs permission jobs.direct")
                || error.starts_with("forbidden: runner may not send a directive to the holder of Job j3: it needs permission jobs.direct"),
            "{error}"
        );
    }
}

#[test]
fn parse_config_holds_direct_on_to_a_link_of_the_type_and_a_permission_one_entry_per_link() {
    for target in targets("direct_on_config") {
        let w = world(&target);
        let refused = |behaviors: Value| -> String {
            refusal(w.engine.schemas().define(
                &alice(),
                jobs_document(behaviors),
                Default::default(),
            ))
            .issues()
            .iter()
            .map(|issue| issue.message.clone())
            .collect::<Vec<_>>()
            .join("; ")
        };
        let prefix = "type Job: behavior Lease config: ";
        let lease = |config: Value| {
            let mut all = json!({"directPermission": "jobs.direct"});
            if let (Value::Object(all), Value::Object(config)) = (&mut all, config) {
                all.extend(config);
            }
            json!({"name": "Lease", "config": all})
        };
        let links = json!({"name": "Links", "config": {"links": links()}});
        assert_eq!(
            refused(json!([lease(json!({"directOn": [rebase()]}))])),
            format!("{prefix}directOn names links of Links, which the type does not list")
        );
        assert_eq!(
            refused(json!([links, lease(json!({"directOn": [{"revised": {"link": "spec"}, "name": "rebase"}]}))])),
            format!("{prefix}directOn[0].revised names link spec, which is not a link of the type's Links (plan, notes, recipe)")
        );
        let mut again = rebase();
        again["name"] = json!("again");
        assert_eq!(
            refused(json!([
                links,
                lease(json!({"directOn": [rebase(), again]}))
            ])),
            format!("{prefix}directOn[1] names link plan again: one directive per link")
        );
        let mut data = rebase();
        data["data"] = json!({"revised": true});
        assert_eq!(
            refused(json!([links, lease(json!({"directOn": [data]}))])),
            format!("{prefix}directOn[0].data holds revised, the member the runner sets to the link's move")
        );
        assert_eq!(
            refused(json!([links, {"name": "Lease", "config": {"directOn": [rebase()]}}])),
            format!("{prefix}directOn sends directives as the runner's principal, which needs directPermission, or overridePermission when that is absent; the config names neither")
        );
        let shape = refused(json!([
            links,
            lease(json!({"directOn": [{"revised": {}, "name": "rebase"}]}))
        ]));
        assert!(
            shape.contains("must have required property 'link'"),
            "{shape}"
        );
        let pattern = refused(json!([
            links,
            lease(json!({"directOn": [{"revised": {"link": "plan"}, "name": "not a name"}]}))
        ]));
        assert!(pattern.contains("must match pattern"), "{pattern}");
    }
}

#[test]
fn its_guidance_names_the_directive_and_when_it_comes() {
    for target in targets("direct_on_guidance") {
        let w = world_with(
            &target,
            direct_config(json!([rebase(), {"revised": {"link": "notes"}, "name": "reread"}])),
            json!({}),
            |_| {},
        );
        let described = w
            .engine
            .tools()
            .describe(&alice(), "Job", Default::default())
            .expect("describe");
        let summary = described["behaviors"]
            .as_array()
            .unwrap()
            .iter()
            .find(|behavior| behavior["name"] == "Lease")
            .expect("Lease")["summary"]
            .as_str()
            .unwrap()
            .to_owned();
        assert!(
            summary.contains("The runner sends the holder of an active lease directive rebase when the plan link's target gains a revision past the pinned one or a release and directive reread when the notes link's target gains a revision or a release; an instance with no lease held hears nothing, and its next holder nothing either."),
            "{summary}"
        );
        let heartbeat = described["operations"]
            .as_array()
            .unwrap()
            .iter()
            .find(|operation| operation["name"] == "heartbeat")
            .expect("heartbeat")["guidance"]["success"]
            .as_str()
            .unwrap()
            .to_owned();
        assert!(
            heartbeat.contains("(rebase or reread) carries data.revised: the link, the target, and its revision, or its release and the release's commit."),
            "{heartbeat}"
        );
    }
}
