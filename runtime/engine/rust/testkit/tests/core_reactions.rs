//! Reactions, the core's rules that move Workflow statuses after a change
//! commits (D16, amended): the cases of `test/core-reactions.test.ts` (a
//! task that starts moves its project to active, a project whose tasks are
//! all finished is done, as the runner's principal, through Workflow's
//! transition, whose events record the cause; allTerminal and anyTerminal
//! over the outcomes of the linking instances' terminal states, so a run
//! completes when its steps all pass and fails when one fails, in whatever
//! order the events and the rules come; rules on the instance itself that
//! chain; a target the rule cannot move left as it is, and a misconfigured
//! rule that halts its subscription; parseConfig's checks and
//! configChange), on SQLite and, with the variable set, on Postgres. Each
//! engine opens with the runner, as the TypeScript file's do, and a clock
//! that stands still until a test moves it. The guidance is held to the
//! TypeScript text in `behaviors/tests/guidance.rs`.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineEvent, EngineOptions, Error,
    EventsAfter, GetOptions, Principal, ReadEvents, RunnerOptions, SchemaTarget, SubscriptionState,
    SubscriptionStatus, WriteOptions,
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

/// The runner's principal, with no permissions.
fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

/// When a test's clock starts (`testClock()`).
const START: i64 = 1_000_000;

fn open_with(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        options.clock = Clock::Manual(START);
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

fn project_flow() -> Value {
    json!({
        "name": "Workflow",
        "config": {
            "states": ["planned", "active", "done", "cancelled"],
            "transitions": [
                {"from": "planned", "to": "active"},
                {"from": "active", "to": "done"},
                {"from": "planned", "to": "cancelled"},
                {"from": "active", "to": "cancelled"}
            ]
        }
    })
}

fn task_flow() -> Value {
    json!({
        "name": "Workflow",
        "config": {
            "states": ["todo", "doing", "done", "dropped"],
            "transitions": [
                {"from": "todo", "to": "doing"},
                {"from": "doing", "to": "done"},
                {"from": "todo", "to": "dropped"},
                {"from": "doing", "to": "dropped"}
            ]
        }
    })
}

// A step of a run is done when it passes or fails, and failed is a failure;
// a run is open until it completes or fails, and may be retried.
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
                {"from": "failed", "to": "open"}
            ]
        }
    })
}

fn close_when_tasks_finish() -> Value {
    json!({"when": {"allTerminal": {"schema": "Task", "link": "project"}}, "then": {"transition": "done"}})
}

fn complete_when_steps_pass() -> Value {
    json!({"when": {"allTerminal": {"schema": "Step", "link": "run", "outcomes": ["success"]}}, "then": {"transition": "completed"}})
}

fn fail_when_a_step_fails() -> Value {
    json!({"when": {"anyTerminal": {"schema": "Step", "link": "run", "outcomes": ["failure"]}}, "then": {"transition": "failed"}})
}

fn start_project_with_task() -> Value {
    json!({"when": {"enters": "doing"}, "then": {"link": "project", "transition": "active"}})
}

fn reactions(rules: &[Value]) -> Value {
    json!({"name": "Reactions", "config": {"rules": rules}})
}

fn projects(extra: &[Value], rules: &[Value]) -> Value {
    let mut behaviors = vec![project_flow()];
    behaviors.extend(extra.iter().cloned());
    behaviors.push(reactions(rules));
    schema("Project", Value::Array(behaviors))
}

fn tasks(rules: &[Value]) -> Value {
    schema(
        "Task",
        json!([
            task_flow(),
            {"name": "Links", "config": {"links": {"project": {"schema": "Project"}}}},
            reactions(rules)
        ]),
    )
}

fn runs(rules: &[Value]) -> Value {
    schema("Run", json!([run_flow(), reactions(rules)]))
}

fn steps(rules: Option<&[Value]>) -> Value {
    let mut behaviors = vec![
        step_flow(),
        json!({"name": "Links", "config": {"links": {"run": {"schema": "Run"}}}}),
    ];
    if let Some(rules) = rules {
        behaviors.push(reactions(rules));
    }
    schema("Step", Value::Array(behaviors))
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

fn create(engine: &Engine, schema: &str, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &json!({ "title": id }),
            CreateOptions {
                id: Some(id),
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

fn link(engine: &Engine, schema: &str, id: &str, name: &str, to: &str) {
    invoke(
        engine,
        schema,
        id,
        "link",
        json!({ "name": name, "id": to }),
    );
}

fn unlink(engine: &Engine, schema: &str, id: &str, name: &str) {
    invoke(engine, schema, id, "unlink", json!({ "name": name }));
}

fn status(engine: &Engine, schema: &str, id: &str) -> Value {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
        .expect("the instance")
        .behaviors["Workflow"]["status"]
        .clone()
}

fn statuses(engine: &Engine, schema: &str, ids: &[&str]) -> Vec<Value> {
    ids.iter().map(|id| status(engine, schema, id)).collect()
}

fn events_after(engine: &Engine, schema: &str, id: &str, after: Option<i64>) -> Vec<EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some(schema),
                instance_id: Some(id),
                after: after.map(EventsAfter::Cursor),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

fn events(engine: &Engine, schema: &str, id: &str) -> Vec<EngineEvent> {
    events_after(engine, schema, id, None)
}

fn last(engine: &Engine, schema: &str, id: &str) -> EngineEvent {
    events(engine, schema, id).pop().expect("an event")
}

// move_to moves an instance by hand, as alice, and returns the event.
fn move_to(engine: &Engine, schema: &str, id: &str, to: &str) -> EngineEvent {
    invoke(engine, schema, id, "transition", json!({ "to": to }));
    last(engine, schema, id)
}

fn cause(event: &EngineEvent) -> Value {
    serde_json::to_value(&event.cause).expect("the cause")
}

// reacted is the cause a Reactions move records of the event it heard.
fn reacted(event: &EngineEvent, depth: i64) -> Value {
    json!({"behavior": "Reactions", "event": event.cursor, "depth": depth})
}

fn run_due(engine: &Engine) {
    engine.runner().run_due().expect("run due");
}

fn subscriptions(engine: &Engine) -> Vec<SubscriptionStatus> {
    engine
        .runner()
        .status()
        .expect("the runner's status")
        .subscriptions
}

fn subscription(engine: &Engine, schema: &str) -> SubscriptionStatus {
    subscriptions(engine)
        .into_iter()
        .find(|subscription| subscription.schema == schema)
        .expect("the schema's subscription")
}

fn all_well(engine: &Engine) -> bool {
    subscriptions(engine).iter().all(|subscription| {
        subscription.state == SubscriptionState::Active && subscription.failure.is_none()
    })
}

// world publishes the projects and tasks, project p1 and tasks t1 and t2,
// linked to it.
fn world_with(engine: &Engine, extra: &[Value]) {
    publish(engine, &projects(extra, &[close_when_tasks_finish()]));
    publish(engine, &tasks(&[start_project_with_task()]));
    create(engine, "Project", "p1");
    for id in ["t1", "t2"] {
        create(engine, "Task", id);
        link(engine, "Task", id, "project", "p1");
    }
}

fn world(engine: &Engine) {
    world_with(engine, &[]);
}

#[test]
fn a_task_that_starts_moves_its_project_to_active_as_the_runner_and_the_move_records_its_cause() {
    for target in targets("reactions_start") {
        let engine = open(&target);
        world(&engine);
        let now = engine.clock().advance(1_000).expect("advance");
        let started = move_to(&engine, "Task", "t1", "doing");
        assert_eq!(status(&engine, "Project", "p1"), "planned");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p1"), "active");
        let moved = last(&engine, "Project", "p1");
        assert_eq!(
            (
                moved.kind.as_str(),
                moved.actor.as_str(),
                moved.change.clone(),
                cause(&moved)
            ),
            (
                "operation",
                "runner",
                json!({
                    "behavior": "Workflow",
                    "operation": "transition",
                    "params": {"to": "active"},
                    "patch": {"behaviors": {"Workflow": {"status": "active", "enteredAt": now}}}
                }),
                reacted(&started, 1)
            )
        );

        // The second task finds the project active already: nothing to do,
        // and no failure.
        move_to(&engine, "Task", "t2", "doing");
        run_due(&engine);
        assert_eq!(last(&engine, "Project", "p1").cursor, moved.cursor);
        let listed: Vec<(String, SubscriptionState, bool)> = subscriptions(&engine)
            .into_iter()
            .map(|subscription| {
                (
                    subscription.schema,
                    subscription.state,
                    subscription.failure.is_none(),
                )
            })
            .collect();
        assert_eq!(
            listed,
            [
                ("Project".to_owned(), SubscriptionState::Active, true),
                ("Task".to_owned(), SubscriptionState::Active, true)
            ]
        );
    }
}

#[test]
fn a_project_whose_tasks_are_all_in_a_terminal_state_is_done() {
    for target in targets("reactions_all_terminal") {
        let engine = open(&target);
        world(&engine);
        move_to(&engine, "Task", "t1", "doing");
        move_to(&engine, "Task", "t2", "doing");
        move_to(&engine, "Task", "t1", "done");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p1"), "active");
        let dropped = move_to(&engine, "Task", "t2", "dropped");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p1"), "done");
        assert_eq!(cause(&last(&engine, "Project", "p1")), reacted(&dropped, 1));
    }
}

#[test]
fn a_task_deleted_or_linked_elsewhere_settles_the_project_it_linked_to_before() {
    for target in targets("reactions_settles") {
        let engine = open(&target);
        world(&engine);
        move_to(&engine, "Task", "t1", "doing");
        move_to(&engine, "Task", "t1", "done");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p1"), "active");
        engine
            .instances()
            .delete(&alice(), "Task", "t2", WriteOptions::default())
            .expect("delete");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p1"), "done");

        create(&engine, "Project", "p2");
        create(&engine, "Project", "p3");
        for id in ["t3", "t4"] {
            create(&engine, "Task", id);
            link(&engine, "Task", id, "project", "p2");
        }
        move_to(&engine, "Task", "t3", "doing");
        move_to(&engine, "Task", "t3", "done");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p2"), "active");
        link(&engine, "Task", "t4", "project", "p3");
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Project", &["p2", "p3"]),
            [json!("done"), json!("planned")]
        );
    }
}

#[test]
fn a_project_with_no_tasks_left_or_whose_guards_refuse_stays_as_it_is() {
    for target in targets("reactions_stays") {
        let engine = open(&target);
        world_with(&engine, &[json!({"name": "Dependencies"})]);
        move_to(&engine, "Task", "t1", "doing");
        run_due(&engine);
        unlink(&engine, "Task", "t1", "project");
        unlink(&engine, "Task", "t2", "project");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p1"), "active");

        // p2 waits on p3, an open blocker: Dependencies vetoes its move to
        // done.
        create(&engine, "Project", "p2");
        create(&engine, "Project", "p3");
        invoke(&engine, "Project", "p2", "addBlocker", json!({"id": "p3"}));
        link(&engine, "Task", "t1", "project", "p2");
        move_to(&engine, "Project", "p2", "active");
        move_to(&engine, "Task", "t1", "done");
        run_due(&engine);
        assert_eq!(status(&engine, "Project", "p2"), "active");
        assert!(all_well(&engine));
    }
}

#[test]
fn all_terminal_with_outcomes_fires_only_when_every_linking_instance_has_a_listed_outcome() {
    for target in targets("reactions_all_outcomes") {
        let engine = open(&target);
        publish(&engine, &runs(&[complete_when_steps_pass()]));
        publish(&engine, &steps(None));
        for (run, ids) in [("r1", &["s1", "s2"][..]), ("r2", &["s3"][..])] {
            create(&engine, "Run", run);
            for id in ids {
                create(&engine, "Step", id);
                link(&engine, "Step", id, "run", run);
                move_to(&engine, "Step", id, "doing");
            }
        }
        run_due(&engine);
        move_to(&engine, "Step", "s1", "passed");
        move_to(&engine, "Step", "s2", "failed");
        let passed = move_to(&engine, "Step", "s3", "passed");
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Run", &["r1", "r2"]),
            [json!("open"), json!("completed")]
        );
        assert_eq!(cause(&last(&engine, "Run", "r2")), reacted(&passed, 1));
    }
}

#[test]
fn any_terminal_fires_when_a_linking_instance_enters_a_listed_outcome_or_is_linked_in_one() {
    for target in targets("reactions_any_terminal") {
        let engine = open(&target);
        publish(&engine, &runs(&[fail_when_a_step_fails()]));
        publish(&engine, &steps(None));
        for id in ["r1", "r2", "r3"] {
            create(&engine, "Run", id);
        }
        for id in ["s1", "s2", "s3"] {
            create(&engine, "Step", id);
            move_to(&engine, "Step", id, "doing");
        }
        for id in ["s1", "s2"] {
            link(&engine, "Step", id, "run", "r1");
        }
        move_to(&engine, "Step", "s1", "passed");
        run_due(&engine);
        assert_eq!(
            status(&engine, "Run", "r1"),
            "open",
            "a success is not a listed outcome"
        );
        let failed = move_to(&engine, "Step", "s2", "failed");
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "failed");
        assert_eq!(cause(&last(&engine, "Run", "r1")), reacted(&failed, 1));

        // A retried run is not failed again by a change of the failed step
        // that moves neither its status nor its link.
        move_to(&engine, "Run", "r1", "open");
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s2",
                &json!({"title": "flaky"}),
                WriteOptions::default(),
            )
            .expect("update");
        run_due(&engine);
        assert_eq!(status(&engine, "Run", "r1"), "open");

        // A failed step linked to a run, or moved to another, fails that
        // run.
        move_to(&engine, "Step", "s3", "failed");
        run_due(&engine);
        assert_eq!(
            status(&engine, "Run", "r3"),
            "open",
            "a step that links nowhere has no run to fail"
        );
        link(&engine, "Step", "s3", "run", "r3");
        link(&engine, "Step", "s2", "run", "r2");
        run_due(&engine);
        assert_eq!(
            statuses(&engine, "Run", &["r1", "r2", "r3"]),
            [json!("open"), json!("failed"), json!("failed")]
        );
        unlink(&engine, "Step", "s2", "run");
        move_to(&engine, "Run", "r2", "open");
        run_due(&engine);
        assert_eq!(
            status(&engine, "Run", "r2"),
            "open",
            "an unlink fails nothing"
        );
        assert!(all_well(&engine));
    }
}

// never_completes runs the eight combinations of a step rule or none, the
// step that moves last, and the runner between moves or once, for one
// order of the run's rules: the run is failed once and never completed.
fn never_completes(rules: &[Value], test: &str) {
    // A rule on the step that fails its run too, which runs in the steps'
    // own subscription.
    let fail_the_run =
        json!({"when": {"enters": "failed"}, "then": {"link": "run", "transition": "failed"}});
    for step_rules in [None, Some(vec![fail_the_run.clone()])] {
        for last_step in ["passed", "failed"] {
            for eager in [true, false] {
                let name = format!(
                    "{}, the {last_step} step moves last, {}",
                    if step_rules.is_none() {
                        "no step rule"
                    } else {
                        "a step rule fails the run too"
                    },
                    if eager {
                        "the runner runs between moves"
                    } else {
                        "the runner runs once"
                    }
                );
                for target in targets(test) {
                    let engine = open(&target);
                    publish(&engine, &runs(rules));
                    publish(&engine, &steps(step_rules.as_deref()));
                    create(&engine, "Run", "r1");
                    for id in ["s1", "s2"] {
                        create(&engine, "Step", id);
                        link(&engine, "Step", id, "run", "r1");
                        move_to(&engine, "Step", id, "doing");
                    }
                    let settle = || {
                        if eager {
                            run_due(&engine);
                        }
                    };
                    settle();
                    move_to(
                        &engine,
                        "Step",
                        "s1",
                        if last_step == "passed" {
                            "failed"
                        } else {
                            "passed"
                        },
                    );
                    settle();
                    move_to(&engine, "Step", "s2", last_step);
                    run_due(&engine);
                    let moves: Vec<Value> = events(&engine, "Run", "r1")
                        .into_iter()
                        .filter(|event| event.kind == "operation")
                        .map(|event| event.change["params"].clone())
                        .collect();
                    assert_eq!(moves, [json!({"to": "failed"})], "{name}");
                }
            }
        }
    }
}

#[test]
fn all_terminal_first_and_any_terminal_never_complete_a_run_with_a_failed_step() {
    never_completes(
        &[complete_when_steps_pass(), fail_when_a_step_fails()],
        "reactions_order_all",
    );
}

#[test]
fn any_terminal_first_and_all_terminal_never_complete_a_run_with_a_failed_step() {
    never_completes(
        &[fail_when_a_step_fails(), complete_when_steps_pass()],
        "reactions_order_any",
    );
}

#[test]
fn rules_on_the_instance_itself_chain_and_one_whose_state_the_instance_has_left_does_nothing() {
    for target in targets("reactions_chain") {
        let engine = open(&target);
        publish(
            &engine,
            &schema(
                "Order",
                json!([
                    {
                        "name": "Workflow",
                        "config": {
                            "states": ["placed", "paid", "packed", "shipped"],
                            "transitions": [
                                {"from": "placed", "to": "paid"},
                                {"from": "paid", "to": "packed"},
                                {"from": "packed", "to": "shipped"}
                            ]
                        }
                    },
                    reactions(&[
                        json!({"when": {"enters": "paid"}, "then": {"transition": "packed"}}),
                        json!({"when": {"enters": "packed"}, "then": {"transition": "shipped"}})
                    ])
                ]),
            ),
        );
        create(&engine, "Order", "o1");
        let paid = move_to(&engine, "Order", "o1", "paid");
        run_due(&engine);
        assert_eq!(status(&engine, "Order", "o1"), "shipped");
        let after = events_after(&engine, "Order", "o1", Some(paid.cursor));
        let moves: Vec<(Value, Option<i64>)> = after
            .iter()
            .map(|event| {
                (
                    event.change["params"].clone(),
                    event.cause.as_ref().map(|cause| cause.depth),
                )
            })
            .collect();
        assert_eq!(
            moves,
            [
                (json!({"to": "packed"}), Some(1)),
                (json!({"to": "shipped"}), Some(2))
            ]
        );
        assert_eq!(
            after[1].cause.as_ref().and_then(|cause| cause.event),
            Some(after[0].cursor)
        );

        create(&engine, "Order", "o2");
        move_to(&engine, "Order", "o2", "paid");
        let packed = move_to(&engine, "Order", "o2", "packed");
        let shipped = move_to(&engine, "Order", "o2", "shipped");
        run_due(&engine);
        assert_eq!(last(&engine, "Order", "o2").cursor, shipped.cursor);
        assert_eq!(
            events_after(&engine, "Order", "o2", Some(packed.cursor)).len(),
            1
        );
        assert!(subscriptions(&engine)[0].failure.is_none());
    }
}

#[test]
fn a_move_the_runner_principal_may_not_make_halts_the_subscription() {
    for target in targets("reactions_forbidden") {
        let engine = open_with(&target, |options| {
            options.runner = Some(RunnerOptions {
                max_attempts: Some(2),
                ..RunnerOptions::new(runner())
            });
        });
        let mut guarded = project_flow();
        guarded["config"]["transitions"][1]["permission"] = json!("projects.close");
        publish(
            &engine,
            &schema(
                "Project",
                json!([guarded, reactions(&[close_when_tasks_finish()])]),
            ),
        );
        publish(&engine, &tasks(&[start_project_with_task()]));
        create(&engine, "Project", "p1");
        create(&engine, "Task", "t1");
        link(&engine, "Task", "t1", "project", "p1");
        move_to(&engine, "Task", "t1", "doing");
        run_due(&engine);
        move_to(&engine, "Task", "t1", "done");
        run_due(&engine);
        engine.clock().advance(1_000).expect("advance");
        run_due(&engine);
        let project = subscription(&engine, "Project");
        assert_eq!(project.state, SubscriptionState::Halted);
        assert_eq!(
            project.failure.map(|failure| failure.error).as_deref(),
            Some("forbidden: runner may not move Project p1 from active to done: the transition needs permission projects.close")
        );
        assert_eq!(status(&engine, "Project", "p1"), "active");
    }
}

#[test]
fn a_linked_schema_that_does_not_link_here_halts_the_subscription_with_what_is_wrong() {
    for (form, when) in [
        (
            "allTerminal",
            json!({"allTerminal": {"schema": "Note", "link": "project"}}),
        ),
        (
            "anyTerminal",
            json!({"anyTerminal": {"schema": "Note", "link": "project", "outcomes": ["failure"]}}),
        ),
    ] {
        for target in targets("reactions_no_link") {
            let engine = open_with(&target, |options| {
                options.runner = Some(RunnerOptions {
                    max_attempts: Some(1),
                    ..RunnerOptions::new(runner())
                });
            });
            publish(&engine, &schema("Note", json!([task_flow()])));
            publish(
                &engine,
                &projects(
                    &[],
                    &[json!({"when": when, "then": {"transition": "done"}})],
                ),
            );
            create(&engine, "Note", "n1");
            run_due(&engine);
            let project = subscription(&engine, "Project");
            assert_eq!(
                (project.state, project.failure.map(|failure| failure.error)),
                (
                    SubscriptionState::Halted,
                    Some(format!("BehaviorError: behavior Reactions: {form} names link project of Note, which has no such link to Project"))
                )
            );
        }
    }
}

// refused defines a Task schema with the behaviors and returns the
// refusal's issues' messages, joined.
fn refused(engine: &Engine, behaviors: Value) -> String {
    let error = refusal(define(engine, &schema("Task", behaviors)));
    issues_of(&error)
        .into_iter()
        .map(|(_, message)| message)
        .collect::<Vec<_>>()
        .join("; ")
}

#[test]
fn parse_config_refuses_rules_the_types_own_configs_show_are_wrong() {
    for target in targets("reactions_config") {
        let engine = open(&target);
        let links_to = |schema: &str| json!({"name": "Links", "config": {"links": {"project": {"schema": schema}}}});
        let rule = |when: Value, then: Value| reactions(&[json!({"when": when, "then": then})]);
        let prefix = "type Task: behavior Reactions config: ";
        let cases = [
            (
                json!([task_flow(), rule(json!({"enters": "review"}), json!({"transition": "done"}))]),
                "rule 1: when.enters \"review\" is not a state of the type's Workflow (todo, doing, done, dropped)",
            ),
            (
                json!([task_flow(), rule(json!({"enters": "doing"}), json!({"transition": "shipped"}))]),
                "rule 1: then.transition \"shipped\" is not a state of the type's Workflow (todo, doing, done, dropped)",
            ),
            (
                json!([task_flow(), rule(json!({"enters": "doing"}), json!({"link": "project", "transition": "active"}))]),
                "rule 1: then.link project needs Links on the type, which does not list it",
            ),
            (
                json!([task_flow(), links_to("Project"), rule(json!({"enters": "doing"}), json!({"link": "parent", "transition": "active"}))]),
                "rule 1: then.link parent is not a link of the type's Links (project)",
            ),
            (
                json!([task_flow(), rule(json!({"enters": "todo"}), json!({"transition": "done"}))]),
                "rule 1: no transition of the type's Workflow leads from todo to done, so the rule could never move the instance",
            ),
            (
                json!([task_flow(), links_to("Task"), rule(json!({"allTerminal": {"schema": "Task", "link": "project"}}), json!({"transition": "todo"}))]),
                "rule 1: no transition of the type's Workflow leads to todo, so the rule could never move the instance",
            ),
            (
                json!([task_flow(), links_to("Project"), rule(json!({"allTerminal": {"schema": "Task", "link": "project"}}), json!({"transition": "done"}))]),
                "rule 1: when.allTerminal names link project of Task, which has no such link to Task",
            ),
            (
                json!([task_flow(), links_to("Project"), rule(json!({"anyTerminal": {"schema": "Task", "link": "project", "outcomes": ["failure"]}}), json!({"transition": "dropped"}))]),
                "rule 1: when.anyTerminal names link project of Task, which has no such link to Task",
            ),
            (
                json!([task_flow(), links_to("Task"), rule(json!({"anyTerminal": {"schema": "Task", "link": "project", "outcomes": ["failure"]}}), json!({"transition": "todo"}))]),
                "rule 1: no transition of the type's Workflow leads to todo, so the rule could never move the instance",
            ),
        ];
        for (behaviors, message) in cases {
            assert_eq!(refused(&engine, behaviors), format!("{prefix}{message}"));
        }

        let looping = json!({
            "name": "Workflow",
            "config": {"states": ["open", "held"], "transitions": [{"from": "open", "to": "held"}, {"from": "held", "to": "open"}]}
        });
        assert_eq!(
            refused(
                &engine,
                json!([
                    looping,
                    reactions(&[
                        json!({"when": {"enters": "open"}, "then": {"transition": "held"}}),
                        json!({"when": {"enters": "held"}, "then": {"transition": "open"}})
                    ])
                ])
            ),
            format!("{prefix}its rules on the instance itself move it round the states open, held, open for ever")
        );
        assert_eq!(
            refused(
                &engine,
                json!([rule(json!({"enters": "open"}), json!({"transition": "held"}))])
            ),
            "type Task: behavior Reactions requires behavior Workflow, which the type does not list"
        );
        // The meta-schema holds a rule to one when, as the configSchema
        // does; anyTerminal names its outcomes, and an outcome is one of
        // three.
        for (when, wanted) in [
            (
                json!({"enters": "doing", "allTerminal": {"schema": "Task", "link": "project"}}),
                "must NOT have more than 1 properties",
            ),
            (
                json!({"anyTerminal": {"schema": "Project", "link": "project"}}),
                "must have required property 'outcomes'",
            ),
            (
                json!({"allTerminal": {"schema": "Project", "link": "project", "outcomes": ["lost"]}}),
                "must be one of \"success\", \"failure\", \"neutral\"",
            ),
        ] {
            let message = refused(
                &engine,
                json!([task_flow(), rule(when, json!({"transition": "done"}))]),
            );
            assert!(message.contains(wanted), "{message}");
        }
        // A rule on a task that fails its parent task when a subtask fails
        // passes: the link is the type's own.
        define(
            &engine,
            &schema(
                "Task",
                json!([
                    task_flow(),
                    links_to("Task"),
                    rule(
                        json!({"anyTerminal": {"schema": "Task", "link": "project", "outcomes": ["failure"]}}),
                        json!({"transition": "dropped"})
                    )
                ]),
            ),
        )
        .expect("a rule over the type's own link");
    }
}

#[test]
fn config_change_rules_may_change_and_reactions_comes_and_goes_on_a_schema_with_instances() {
    for target in targets("reactions_config_change") {
        let engine = open(&target);
        publish(&engine, &schema("Project", json!([project_flow()])));
        create(&engine, "Project", "p1");
        publish(
            &engine,
            &projects(
                &[],
                &[json!({"when": {"enters": "active"}, "then": {"transition": "done"}})],
            ),
        );
        publish(
            &engine,
            &projects(
                &[],
                &[json!({"when": {"enters": "planned"}, "then": {"transition": "active"}})],
            ),
        );
        publish(&engine, &schema("Project", json!([project_flow()])));
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Project", SchemaTarget::default())
                .expect("live")
                .expect("a live version")
                .version,
            Some(4)
        );
    }
}
