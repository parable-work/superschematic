//! Reactions' holds and revised (D16, amended): the cases of
//! `test/core-reactions-holds-revised.test.ts`. holds fires when a change
//! of a linked instance makes an all or any rollup of the instance hold:
//! on the edge from not holding to holding, judged with the other linked
//! instances as they are when the rule runs, and up a tree of the type's
//! own schema to the runner's depth limit. revised fires when the instance
//! a link points to gains a revision, on each instance whose link points
//! there, of a link pinned to a revision only the ones the target has
//! moved past. parseConfig holds both to the type's Rollups and Links, and
//! a link to a schema with neither Revisions nor Branches halts the
//! subscription. On SQLite and, with the variable set, on Postgres. The
//! two cases of a release, which compose Branches, wait for Branches.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineEvent, EngineOptions, Error,
    GetOptions, Principal, ReadEvents, RunnerOptions, SchemaTarget, SubscriptionSkip,
    SubscriptionState, WriteOptions,
};
use superschematic_engine_behaviors::{
    Approvals, Comments, Constants, Dependencies, Links, Numbering, Reactions, Revisions, Rollups,
    StateRules, Timers, Variants, Workflow,
};
use support::{alice, issues_of, refusal, schema_document, targets, Target};

/// The core's behaviors with Rollups and Reactions, in the TypeScript
/// engine's order.
fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Comments)
        .with(Revisions)
        .with(Dependencies)
        .with(Links)
        .with(Rollups)
        .with(Reactions)
        .with(Constants)
        .with(Variants)
        .with(StateRules)
        .with(Approvals)
        .with(Numbering)
        .with(Timers)
}

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

fn open_with(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        options.clock = Clock::Manual(1_000_000);
        options.runner = Some(RunnerOptions::new(runner()));
        configure(options);
    })
}

fn open(target: &Target) -> Engine {
    open_with(target, |_| {})
}

fn schema(name: &str, behaviors: Value) -> Value {
    let mut document = schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"][name]["behaviors"] = behaviors;
    document
}

// A step passes or fails, failed a failure; a run is open until it
// completes or fails, and a failed run may be opened again.
fn step_flow() -> Value {
    json!({
        "name": "Workflow",
        "config": {
            "states": ["todo", "doing", "passed", "failed"],
            "transitions": [
                {"from": "todo", "to": "doing"},
                {"from": "doing", "to": "passed"},
                {"from": "doing", "to": "failed"}
            ],
            "outcomes": {"failed": "failure"}
        }
    })
}

fn run_flow() -> Value {
    json!({
        "name": "Workflow",
        "config": {
            "states": ["open", "completed", "failed"],
            "transitions": [
                {"from": "open", "to": "completed"},
                {"from": "open", "to": "failed"},
                {"from": "failed", "to": "open"},
                {"from": "completed", "to": "open"}
            ]
        }
    })
}

fn run_rollups() -> Value {
    json!({
        "name": "Rollups",
        "config": {
            "rollups": {
                "stepsPassed": {"schema": "Step", "link": "run", "function": "all", "outcomes": ["success"]},
                "stepFailed": {"schema": "Step", "link": "run", "function": "any", "outcomes": ["failure"]},
                "steps": {"schema": "Step", "link": "run", "function": "count"}
            }
        }
    })
}

fn complete_when_passed() -> Value {
    json!({"when": {"holds": "stepsPassed"}, "then": {"transition": "completed"}})
}

fn fail_when_one_failed() -> Value {
    json!({"when": {"holds": "stepFailed"}, "then": {"transition": "failed"}})
}

// Work is done against a spec, and goes back for review when it changes.
fn work_flow() -> Value {
    json!({
        "name": "Workflow",
        "config": {
            "states": ["todo", "review", "done"],
            "transitions": [
                {"from": "todo", "to": "review"},
                {"from": "review", "to": "todo"},
                {"from": "todo", "to": "done"},
                {"from": "done", "to": "review"}
            ]
        }
    })
}

fn reactions(rules: &[Value]) -> Value {
    json!({"name": "Reactions", "config": {"rules": rules}})
}

fn define(engine: &Engine, document: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) {
    define(engine, document).expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )
        .expect("publish");
}

fn create(engine: &Engine, schema: &str, id: &str, title: &str, links: Option<Value>) {
    let behaviors = links.map(|links| json!({ "Links": links }));
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &json!({ "title": title }),
            CreateOptions {
                id: Some(id),
                behaviors: behaviors.as_ref(),
                ..Default::default()
            },
        )
        .expect("create");
}

fn invoke(engine: &Engine, schema: &str, id: &str, operation: &str, params: Value) -> Value {
    engine
        .instances()
        .invoke(
            &alice(),
            schema,
            id,
            operation,
            &params,
            WriteOptions::default(),
        )
        .expect("invoke")
}

fn update(engine: &Engine, schema: &str, id: &str, patch: Value) {
    engine
        .instances()
        .update(&alice(), schema, id, &patch, WriteOptions::default())
        .expect("update");
}

fn delete(engine: &Engine, schema: &str, id: &str) {
    engine
        .instances()
        .delete(&alice(), schema, id, WriteOptions::default())
        .expect("delete");
}

fn read(engine: &Engine, schema: &str, id: &str) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
        .expect("the instance")
}

fn status(engine: &Engine, schema: &str, id: &str) -> Value {
    read(engine, schema, id).behaviors["Workflow"]["status"].clone()
}

fn statuses(engine: &Engine, schema: &str, ids: &[&str]) -> Vec<Value> {
    ids.iter().map(|id| status(engine, schema, id)).collect()
}

fn last(engine: &Engine, schema: &str, id: &str) -> EngineEvent {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some(schema),
                instance_id: Some(id),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
        .pop()
        .expect("an event")
}

// move_to moves an instance through each state by hand and returns the
// last event.
fn move_to(engine: &Engine, schema: &str, id: &str, to: &[&str]) -> EngineEvent {
    for state in to {
        invoke(engine, schema, id, "transition", json!({ "to": state }));
    }
    last(engine, schema, id)
}

fn cause(event: &EngineEvent) -> Value {
    serde_json::to_value(&event.cause).expect("the cause")
}

fn run_due(engine: &Engine) {
    engine.runner().run_due().expect("run due");
}

// runs publishes Step, which links to its Run, and Run with its rollups and
// the rules; Run r1 has steps s1 and s2.
fn runs_with(engine: &Engine, rules: &[Value]) {
    publish(
        engine,
        &schema(
            "Step",
            json!([step_flow(), {"name": "Links", "config": {"links": {"run": {"schema": "Run"}}}}]),
        ),
    );
    publish(
        engine,
        &schema("Run", json!([run_flow(), run_rollups(), reactions(rules)])),
    );
    create(engine, "Run", "r1", "Nightly", None);
    for id in ["s1", "s2"] {
        create(engine, "Step", id, id, Some(json!({"run": "r1"})));
    }
}

fn runs(engine: &Engine) {
    runs_with(engine, &[complete_when_passed(), fail_when_one_failed()]);
}

#[test]
fn a_change_that_makes_an_all_rollup_hold_sets_the_rule_off_once() {
    for target in targets("holds_all_once") {
        let engine = open(&target);
        runs(&engine);
        move_to(&engine, "Step", "s1", &["doing", "passed"]);
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "open");
        let passed = move_to(&engine, "Step", "s2", &["doing", "passed"]);
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "completed");
        assert_eq!(
            cause(&last(&engine, "Run", "r1")),
            json!({"behavior": "Reactions", "event": passed.cursor, "depth": 1})
        );
        // Opened again by hand, it stays open: a step's update moves no
        // step into the rollup, so the rollup, holding already, has no edge.
        move_to(&engine, "Run", "r1", &["open"]);
        update(&engine, "Step", "s1", json!({"title": "Build again"}));
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "open");
    }
}

#[test]
fn an_any_rollup_fires_when_it_comes_to_hold_not_again_while_it_holds_and_again_once_stopped() {
    for target in targets("holds_any_edge") {
        let engine = open(&target);
        runs(&engine);
        move_to(&engine, "Step", "s1", &["doing", "failed"]);
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "failed");
        // Retried, the run stays open when s2 fails too: s1's failure still
        // holds the rollup.
        move_to(&engine, "Run", "r1", &["open"]);
        move_to(&engine, "Step", "s2", &["doing", "failed"]);
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "open");
        // Both failed steps leave the run: unlinking the second ends the
        // rollup's hold, and fires nothing; stepsPassed over no step sets no
        // rule off either.
        for id in ["s1", "s2"] {
            invoke(&engine, "Step", id, "unlink", json!({"name": "run"}));
        }
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "open");
        // A new step that fails makes it hold again.
        create(&engine, "Step", "s3", "s3", Some(json!({"run": "r1"})));
        move_to(&engine, "Step", "s3", &["doing", "failed"]);
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "failed");
    }
}

#[test]
fn a_step_linked_in_a_state_it_counts_in_or_linked_away_moves_the_rollup_as_its_link_does() {
    for target in targets("holds_links") {
        let engine = open(&target);
        runs(&engine);
        create(&engine, "Run", "r2", "Weekly", None);
        move_to(&engine, "Step", "s1", &["doing", "passed"]);
        move_to(&engine, "Step", "s2", &["doing"]);
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Run", &["r1", "r2"]),
            [json!("open"), json!("open")]
        );
        // s2, still doing, moves to r2: r1's rollup holds now, r2's does
        // not.
        invoke(
            &engine,
            "Step",
            "s2",
            "link",
            json!({"name": "run", "id": "r2"}),
        );
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Run", &["r1", "r2"]),
            [json!("completed"), json!("open")]
        );
        // A passed step linked to r2 changes nothing there while s2 is
        // doing.
        invoke(
            &engine,
            "Step",
            "s1",
            "link",
            json!({"name": "run", "id": "r2"}),
        );
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r2"), "open");
        move_to(&engine, "Step", "s2", &["passed"]);
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r2"), "completed");
    }
}

#[test]
fn an_all_rollup_sets_the_rule_off_over_at_least_one_instance() {
    for target in targets("holds_at_least_one") {
        let engine = open(&target);
        runs(&engine);
        move_to(&engine, "Step", "s1", &["doing", "passed"]);
        move_to(&engine, "Step", "s2", &["doing"]);
        run_due(&engine);
        delete(&engine, "Step", "s2");
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "completed");
        // r2's only step goes: Rollups' all holds over none, as its gate
        // has it, but no rule fires on that.
        create(&engine, "Run", "r2", "Weekly", None);
        create(&engine, "Step", "s3", "s3", Some(json!({"run": "r2"})));
        move_to(&engine, "Step", "s3", &["doing"]);
        delete(&engine, "Step", "s3");
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r2"), "open");
        assert_eq!(
            read(&engine, "Run", "r2").behaviors["Rollups"]["values"]["stepsPassed"],
            true
        );
    }
}

#[test]
fn up_a_tree_of_its_own_schema_each_parent_completes_as_its_last_subtask_does_to_the_depth_limit() {
    let tasks = schema(
        "Task",
        json!([
            {"name": "Workflow", "config": {"states": ["todo", "doing", "done"], "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done"}]}},
            {"name": "Links", "config": {"links": {"parent": {"schema": "Task"}}}},
            {"name": "Rollups", "config": {"rollups": {"subtasksDone": {"schema": "Task", "link": "parent", "function": "all"}}}},
            reactions(&[json!({"when": {"holds": "subtasksDone"}, "then": {"transition": "done"}})])
        ]),
    );
    for (max_depth, moved) in [
        (8, ["done", "done", "done"]),
        (1, ["doing", "done", "done"]),
    ] {
        for target in targets("holds_tree") {
            let engine = open_with(&target, |options| {
                options.runner = Some(RunnerOptions {
                    max_depth: Some(max_depth),
                    ..RunnerOptions::new(runner())
                });
            });
            publish(&engine, &tasks);
            create(&engine, "Task", "t1", "Launch", None);
            create(
                &engine,
                "Task",
                "t2",
                "Build",
                Some(json!({"parent": "t1"})),
            );
            create(&engine, "Task", "t3", "Test", Some(json!({"parent": "t2"})));
            for id in ["t1", "t2", "t3"] {
                move_to(&engine, "Task", id, &["doing"]);
            }
            move_to(&engine, "Task", "t3", &["done"]);
            run_due(&engine);
            assert_eq!(
                statuses(&engine, "Task", &["t1", "t2", "t3"]),
                moved.map(Value::from),
                "maxDepth {max_depth}"
            );
            if max_depth == 1 {
                let status = engine.runner().status().expect("the runner's status");
                assert_eq!(
                    status.subscriptions[0].last_skip,
                    Some(SubscriptionSkip {
                        cursor: last(&engine, "Task", "t2").cursor,
                        reason: "depth".to_owned()
                    })
                );
            }
        }
    }
}

// refused defines a schema and returns the refusal's issues' messages,
// joined.
fn refused(engine: &Engine, document: &Value) -> String {
    issues_of(&refusal(define(engine, document)))
        .into_iter()
        .map(|(_, message)| message)
        .collect::<Vec<_>>()
        .join("; ")
}

#[test]
fn parse_config_holds_a_holds_rule_to_an_all_or_any_rollup_of_the_types_rollups() {
    for target in targets("holds_config") {
        let engine = open(&target);
        publish(
            &engine,
            &schema(
                "Step",
                json!([step_flow(), {"name": "Links", "config": {"links": {"run": {"schema": "Run"}}}}]),
            ),
        );
        let prefix = "type Run: behavior Reactions config: ";
        let runs = |behaviors: Value| schema("Run", behaviors);
        assert_eq!(
            refused(
                &engine,
                &runs(json!([run_flow(), reactions(&[complete_when_passed()])]))
            ),
            format!("{prefix}rule 1: when.holds names rollup stepsPassed of Rollups, which the type does not list")
        );
        assert_eq!(
            refused(
                &engine,
                &runs(json!([
                    run_flow(),
                    run_rollups(),
                    reactions(&[
                        json!({"when": {"holds": "stepsDone"}, "then": {"transition": "completed"}})
                    ])
                ]))
            ),
            format!("{prefix}rule 1: when.holds names rollup stepsDone, which is not a rollup of the type's Rollups (stepsPassed, stepFailed, steps)")
        );
        assert_eq!(
            refused(
                &engine,
                &runs(json!([
                    run_flow(),
                    run_rollups(),
                    reactions(&[
                        json!({"when": {"holds": "steps"}, "then": {"transition": "completed"}})
                    ])
                ]))
            ),
            format!("{prefix}rule 1: when.holds names rollup steps, a count rollup; holds takes an all or any rollup, which holds or does not")
        );
        let no_way_back = json!({"name": "Workflow", "config": {"states": ["open", "completed"], "transitions": [{"from": "completed", "to": "open"}]}});
        assert_eq!(
            refused(
                &engine,
                &runs(json!([
                    no_way_back,
                    run_rollups(),
                    reactions(&[complete_when_passed()])
                ]))
            ),
            format!("{prefix}rule 1: no transition of the type's Workflow leads to completed, so the rule could never move the instance")
        );
        let message = refused(
            &engine,
            &runs(json!([
                run_flow(),
                run_rollups(),
                reactions(&[
                    json!({"when": {"holds": "Steps"}, "then": {"transition": "completed"}})
                ])
            ])),
        );
        assert!(message.contains("must match pattern"), "{message}");
    }
}

// specs publishes Spec, which composes Revisions, and Work, which pins its
// spec link and links a design without a pin; w1 and w2 pin sp1 at its
// first revision, and w3 links sp2.
fn specs(engine: &Engine) {
    publish(
        engine,
        &schema("Spec", json!([{"name": "Revisions"}, {"name": "Comments"}])),
    );
    publish(
        engine,
        &schema(
            "Work",
            json!([
                work_flow(),
                {"name": "Links", "config": {"links": {"spec": {"schema": "Spec", "pinned": true}, "design": {"schema": "Spec"}}}},
                reactions(&[
                    json!({"when": {"revised": {"link": "spec"}}, "then": {"transition": "review"}}),
                    json!({"when": {"revised": {"link": "design"}}, "then": {"transition": "review"}})
                ])
            ]),
        ),
    );
    create(engine, "Spec", "sp1", "v1", None);
    create(engine, "Spec", "sp2", "Look", None);
    for id in ["w1", "w2"] {
        create(engine, "Work", id, id, Some(json!({"spec": "sp1"})));
    }
    create(engine, "Work", "w3", "w3", Some(json!({"design": "sp2"})));
}

#[test]
fn a_specs_new_revision_moves_the_work_pinned_to_an_earlier_one_as_the_runner() {
    for target in targets("revised_moves") {
        let engine = open(&target);
        specs(&engine);
        // A comment is no revision.
        invoke(
            &engine,
            "Spec",
            "sp1",
            "comment",
            json!({"body": "Looks right"}),
        );
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Work", &["w1", "w2"]),
            [json!("todo"), json!("todo")]
        );
        update(&engine, "Spec", "sp1", json!({"title": "v2"}));
        let revised = last(&engine, "Spec", "sp1");
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Work", &["w1", "w2", "w3"]),
            [json!("review"), json!("review"), json!("todo")]
        );
        let moved = last(&engine, "Work", "w1");
        assert_eq!(
            (moved.actor.as_str(), cause(&moved)),
            (
                "runner",
                json!({"behavior": "Reactions", "event": revised.cursor, "depth": 1})
            )
        );
    }
}

#[test]
fn of_a_pinned_link_only_the_work_the_target_has_moved_past_moves() {
    for target in targets("revised_stale") {
        let engine = open(&target);
        specs(&engine);
        update(&engine, "Spec", "sp1", json!({"title": "v2"}));
        // Before the runner hears it, w1 is pinned to revision 2.
        invoke(
            &engine,
            "Work",
            "w1",
            "link",
            json!({"name": "spec", "id": "sp1"}),
        );
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Work", &["w1", "w2"]),
            [json!("todo"), json!("review")]
        );
    }
}

#[test]
fn of_a_link_with_no_pin_every_instance_that_points_at_the_target_moves_at_each_revision() {
    for target in targets("revised_unpinned") {
        let engine = open(&target);
        specs(&engine);
        create(&engine, "Work", "w4", "w4", Some(json!({"design": "sp2"})));
        move_to(&engine, "Work", "w4", &["done"]);
        update(&engine, "Spec", "sp2", json!({"title": "New look"}));
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Work", &["w3", "w4"]),
            [json!("review"), json!("review")]
        );
    }
}

#[test]
fn a_link_to_a_schema_with_neither_revisions_nor_branches_halts_the_subscription() {
    for target in targets("revised_neither") {
        let engine = open_with(&target, |options| {
            options.runner = Some(RunnerOptions {
                max_attempts: Some(1),
                ..RunnerOptions::new(runner())
            });
        });
        publish(
            &engine,
            &schema_document(
                "Note",
                json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
                json!({}),
            ),
        );
        publish(
            &engine,
            &schema(
                "Work",
                json!([
                    work_flow(),
                    {"name": "Links", "config": {"links": {"note": {"schema": "Note"}}}},
                    reactions(&[json!({"when": {"revised": {"link": "note"}}, "then": {"transition": "review"}})])
                ]),
            ),
        );
        create(&engine, "Note", "n1", "n1", None);
        update(&engine, "Note", "n1", json!({"title": "n1 again"}));
        run_due(&engine);
        let work = engine
            .runner()
            .status()
            .expect("the runner's status")
            .subscriptions
            .into_iter()
            .find(|subscription| subscription.schema == "Work")
            .expect("Work's subscription");
        assert_eq!(
            (work.state, work.failure.map(|failure| failure.error)),
            (
                SubscriptionState::Halted,
                Some("BehaviorError: behavior Reactions: revised names link note to Note, which composes neither Revisions nor Branches, so it gains no revision or release".to_owned())
            )
        );
    }
}

#[test]
fn parse_config_holds_a_revised_rule_to_a_link_of_the_types_links() {
    for target in targets("revised_config") {
        let engine = open(&target);
        publish(&engine, &schema("Spec", json!([{"name": "Revisions"}])));
        let rule = reactions(&[
            json!({"when": {"revised": {"link": "spec"}}, "then": {"transition": "review"}}),
        ]);
        let prefix = "type Work: behavior Reactions config: ";
        assert_eq!(
            refused(&engine, &schema("Work", json!([work_flow(), rule.clone()]))),
            format!(
                "{prefix}rule 1: when.revised names link spec of Links, which the type does not list"
            )
        );
        assert_eq!(
            refused(
                &engine,
                &schema(
                    "Work",
                    json!([
                        work_flow(),
                        {"name": "Links", "config": {"links": {"design": {"schema": "Spec"}}}},
                        rule
                    ])
                )
            ),
            format!("{prefix}rule 1: when.revised names link spec, which is not a link of the type's Links (design)")
        );
        let message = refused(
            &engine,
            &schema(
                "Work",
                json!([
                    work_flow(),
                    reactions(&[
                        json!({"when": {"revised": {}}, "then": {"transition": "review"}})
                    ])
                ]),
            ),
        );
        assert!(
            message.contains("must have required property 'link'"),
            "{message}"
        );
    }
}
