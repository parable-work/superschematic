//! Dependencies, the core's blockers: the cases of
//! `test/core-dependencies.test.ts` (the blocked field and the Workflow
//! gate over one rule, across schemas; a blocker's outcome, which finishes
//! it only when satisfiedBy lists it; gates on states a transition leaves;
//! the edges addBlocker refuses; the blockers a create gives, held to the
//! same checks; the lists; a blocker's delete, which removes its edges as
//! the caller; reads as the caller; and its config rules), on SQLite and,
//! with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    AccessRequest, BehaviorSet, CreateOptions, DefineOptions, Engine, EngineError, EngineErrorCode,
    EngineEvent, EngineOptions, InstanceRecord, Policy, Principal, ReadEvents, Result,
    SchemaTarget, WriteOptions,
};
use superschematic_engine_behaviors::{Dependencies, Workflow};
use superschematic_engine_testkit::behaviors::{Finisher, Reader};
use support::{
    alice, issue, issues_of, refusal, schema_document, targets, veto, RecordingPolicy, Target,
};

fn bob() -> Principal {
    Principal::new("bob", Vec::<String>::new())
}

fn behaviors() -> BehaviorSet {
    BehaviorSet::new().with(Workflow).with(Dependencies)
}

// A task moves from todo to doing and then to done, or is dropped; done
// and dropped are terminal, and done is the gated state.
fn task_flow() -> Value {
    json!({
        "states": ["todo", "doing", "done", "dropped"],
        "transitions": [
            {"from": "todo", "to": "doing"},
            {"from": "doing", "to": "done"},
            {"from": "todo", "to": "dropped"},
            {"from": "doing", "to": "dropped"}
        ]
    })
}

// A milestone is active until it ships.
fn milestone_flow() -> Value {
    json!({"states": ["active", "shipped"], "transitions": [{"from": "active", "to": "shipped"}]})
}

// A check runs until it passes, fails or is skipped: a failure and a
// neutral outcome beside the success its config leaves unnamed.
fn check_flow() -> Value {
    json!({
        "states": ["running", "passed", "failed", "skipped"],
        "transitions": [
            {"from": "running", "to": "passed"},
            {"from": "running", "to": "failed"},
            {"from": "running", "to": "skipped"}
        ],
        "outcomes": {"failed": "failure", "skipped": "neutral"}
    })
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

fn default_dependencies() -> Value {
    json!({"schemas": ["Task", "Milestone"], "gatedStates": ["done"]})
}

fn tasks(dependencies: Value, extra: &[Value]) -> Value {
    let mut behaviors = vec![
        json!({"name": "Workflow", "config": task_flow()}),
        json!({"name": "Dependencies", "config": dependencies}),
    ];
    behaviors.extend(extra.iter().cloned());
    schema("Task", Value::Array(behaviors))
}

fn define(engine: &Engine, document: &Value) -> Result<()> {
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

fn create(
    engine: &Engine,
    schema: &str,
    id: Option<&str>,
    behaviors: Option<Value>,
) -> Result<InstanceRecord> {
    engine.instances().create(
        &alice(),
        schema,
        &json!({"title": id.unwrap_or("untitled")}),
        CreateOptions {
            id,
            behaviors: behaviors.as_ref(),
            ..Default::default()
        },
    )
}

// A policy that allows alice everything and anyone else what the rule
// says.
fn recording(
    rule: impl Fn(&AccessRequest<'_>) -> bool + Send + Sync + 'static,
) -> impl FnOnce(&mut EngineOptions) {
    let policy = RecordingPolicy::new(move |request| {
        request.principal().subject == "alice" || rule(request)
    });
    move |options| options.policy = Some(Policy::custom(policy))
}

fn open(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        configure(options);
    })
}

// Tasks t1, t2 and t3 in todo, and milestone m1, active.
fn world(
    target: &Target,
    configure: impl FnOnce(&mut EngineOptions),
    dependencies: Option<Value>,
) -> Engine {
    let engine = open(target, configure);
    publish(
        &engine,
        &schema(
            "Milestone",
            json!([{"name": "Workflow", "config": milestone_flow()}]),
        ),
    );
    publish(&engine, &schema("Note", json!([])));
    publish(
        &engine,
        &tasks(dependencies.unwrap_or_else(default_dependencies), &[]),
    );
    for id in ["t1", "t2", "t3"] {
        create(&engine, "Task", Some(id), None).expect("task");
    }
    create(&engine, "Milestone", Some("m1"), None).expect("milestone");
    create(&engine, "Note", Some("n1"), None).expect("note");
    engine
}

fn invoke(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value> {
    engine.instances().invoke(
        principal,
        schema,
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn task(engine: &Engine, id: &str, operation: &str, params: Value) -> Result<Value> {
    invoke(engine, &alice(), "Task", id, operation, params)
}

fn move_to(engine: &Engine, id: &str, to: &str) -> Result<Value> {
    task(engine, id, "transition", json!({ "to": to }))
}

fn get(engine: &Engine, schema: &str, id: &str) -> Option<InstanceRecord> {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
}

fn blocked(engine: &Engine, schema: &str, id: &str) -> Value {
    get(engine, schema, id)
        .expect("the instance")
        .behavior_field("Dependencies", "blocked")
        .cloned()
        .unwrap_or(Value::Null)
}

fn events(engine: &Engine, schema: &str, id: &str) -> Vec<EngineEvent> {
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

fn reason<T: std::fmt::Debug>(result: Result<T>) -> String {
    veto(result).2
}

fn changes(error: &EngineError) -> Vec<Value> {
    error
        .changes()
        .iter()
        .map(|change| json!({"path": change.path, "message": change.message}))
        .collect()
}

#[test]
fn an_instance_is_blocked_while_a_blocker_is_not_in_a_terminal_state_and_add_blocker_says_so() {
    for target in targets("dependencies_blocked") {
        let engine = world(&target, |_| {}, None);
        assert_eq!(blocked(&engine, "Task", "t1"), json!(false));
        assert_eq!(
            task(&engine, "t1", "addBlocker", json!({"id": "t2"})).expect("addBlocker"),
            json!({"schema": "Task", "id": "t2", "status": "todo", "open": true})
        );
        let t1 = get(&engine, "Task", "t1").expect("t1");
        assert_eq!(
            json!([t1.data, t1.behaviors]),
            json!([{"title": "t1"}, {"Workflow": {"status": "todo", "enteredAt": t1.created_at}, "Dependencies": {"blocked": true}}])
        );
        assert_eq!(
            events(&engine, "Task", "t1")
                .last()
                .expect("an event")
                .change,
            json!({
                "behavior": "Dependencies",
                "operation": "addBlocker",
                "params": {"id": "t2"},
                "patch": {"behaviors": {"Dependencies": {"blocked": true}}}
            })
        );
        move_to(&engine, "t2", "doing").expect("move");
        assert_eq!(blocked(&engine, "Task", "t1"), json!(true));
        move_to(&engine, "t2", "done").expect("move");
        // Read when read: the blocker's change shows at t1's next read, with
        // no event on t1.
        assert_eq!(blocked(&engine, "Task", "t1"), json!(false));
        assert_eq!(get(&engine, "Task", "t1").expect("t1").seq, 2);
    }
    for target in targets("dependencies_blocked_dropped") {
        let dropped = world(&target, |_| {}, None);
        task(&dropped, "t1", "addBlocker", json!({"id": "t2"})).expect("addBlocker");
        move_to(&dropped, "t2", "dropped").expect("move");
        assert_eq!(
            blocked(&dropped, "Task", "t1"),
            json!(false),
            "every terminal state of the blocker counts, not only the gated one"
        );
    }
}

#[test]
fn the_guard_holds_a_transition_into_a_gated_state_while_blocked_whoever_asks() {
    for target in targets("dependencies_guard") {
        let engine = target.open_with(behaviors().with(Finisher).with(Reader), |_| {});
        publish(
            &engine,
            &tasks(
                json!({"gatedStates": ["done"]}),
                &[
                    json!({"name": "test.Finisher"}),
                    json!({"name": "test.Reader"}),
                ],
            ),
        );
        for id in ["t1", "t2", "t3"] {
            create(&engine, "Task", Some(id), None).expect("task");
        }
        task(&engine, "t1", "addBlocker", json!({"id": "t2"})).expect("addBlocker");
        move_to(&engine, "t1", "doing").expect("move");
        let (behavior, action, why, _, _) = veto(move_to(&engine, "t1", "done"));
        assert_eq!(
            json!([behavior, action, why]),
            json!([
                "Dependencies",
                "transition",
                "Task t1 cannot move to done while it is blocked by Task t2 (todo)"
            ])
        );
        assert_eq!(
            veto(task(&engine, "t1", "finish", json!({}))).0,
            "Dependencies"
        );
        assert_eq!(
            veto(task(
                &engine,
                "t3",
                "poke",
                json!({"schema": "Task", "id": "t1", "operation": "transition", "params": {"to": "done"}})
            ))
            .0,
            "Dependencies"
        );
        assert_eq!(
            get(&engine, "Task", "t1").expect("t1").behaviors["Workflow"]["status"],
            json!("doing")
        );
        // Dropping is terminal but not gated here.
        assert_eq!(
            move_to(&engine, "t1", "dropped").expect("move"),
            json!({"from": "doing", "to": "dropped"})
        );
        create(&engine, "Task", Some("t4"), None).expect("t4");
        task(&engine, "t4", "addBlocker", json!({"id": "t2"})).expect("addBlocker");
        move_to(&engine, "t4", "doing").expect("move");
        move_to(&engine, "t2", "doing").expect("move");
        move_to(&engine, "t2", "done").expect("move");
        assert_eq!(
            task(&engine, "t4", "finish", json!({})).expect("finish"),
            json!({"from": "doing", "to": "done"})
        );
    }
}

#[test]
fn a_blocker_of_another_schema_is_read_through_its_own_workflow_config() {
    for target in targets("dependencies_other_schema") {
        let engine = world(&target, |_| {}, None);
        assert_eq!(
            task(
                &engine,
                "t1",
                "addBlocker",
                json!({"schema": "Milestone", "id": "m1"})
            )
            .expect("addBlocker"),
            json!({"schema": "Milestone", "id": "m1", "status": "active", "open": true})
        );
        move_to(&engine, "t1", "doing").expect("move");
        assert_eq!(blocked(&engine, "Task", "t1"), json!(true));
        assert_eq!(
            reason(move_to(&engine, "t1", "done")),
            "Task t1 cannot move to done while it is blocked by Milestone m1 (active)"
        );
        invoke(
            &engine,
            &alice(),
            "Milestone",
            "m1",
            "transition",
            json!({"to": "shipped"}),
        )
        .expect("ship");
        assert_eq!(blocked(&engine, "Task", "t1"), json!(false));
        assert_eq!(
            move_to(&engine, "t1", "done").expect("move"),
            json!({"from": "doing", "to": "done"})
        );
    }
}

#[test]
fn add_blocker_refuses_itself_an_unlisted_schema_one_without_workflow_a_missing_one_a_repeat_and_a_cycle(
) {
    for target in targets("dependencies_refusals") {
        let engine = world(&target, |_| {}, Some(json!({"schemas": ["Task", "Note"]})));
        let add = |id: &str, blocker: Value| task(&engine, id, "addBlocker", blocker);
        let params = |result: Result<Value>| issues_of(&refusal(result));
        assert_eq!(
            params(add("t1", json!({"id": "t1"}))),
            [issue("/id", "Task t1 cannot block itself")]
        );
        assert_eq!(
            params(add("t1", json!({"schema": "Milestone", "id": "m1"}))),
            [issue(
                "/schema",
                "a blocker of Task is an instance of Task, Note, not Milestone"
            )]
        );
        assert_eq!(
            params(add("t1", json!({"schema": "Note", "id": "n1"}))),
            [issue(
                "/schema",
                "Note does not compose Workflow, so its instances cannot block"
            )]
        );
        assert_eq!(
            params(add("t1", json!({"id": "t9"}))),
            [issue("/id", "Task t9 does not exist")]
        );
        assert_eq!(params(add("t1", json!({"id": "bad id"})))[0].0, "/id");
        add("t1", json!({"id": "t2"})).expect("addBlocker");
        assert_eq!(
            reason(add("t1", json!({"id": "t2"}))),
            "Task t2 already blocks it"
        );
        add("t2", json!({"id": "t3"})).expect("addBlocker");
        assert_eq!(
            reason(add("t3", json!({"id": "t1"}))),
            "Task t1 is blocked by Task t3, directly or through others: the edge would close a cycle"
        );
        assert_eq!(
            reason(add("t2", json!({"id": "t1"}))),
            "Task t1 is blocked by Task t2, directly or through others: the edge would close a cycle"
        );
    }
}

#[test]
fn an_instance_in_a_gated_state_no_transition_leaves_takes_no_open_blocker() {
    for target in targets("dependencies_terminal_gate") {
        let engine = world(&target, |_| {}, None);
        move_to(&engine, "t1", "doing").expect("move");
        move_to(&engine, "t1", "done").expect("move");
        assert_eq!(
            reason(task(&engine, "t1", "addBlocker", json!({"id": "t2"}))),
            "it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Task t2 (todo)"
        );
        move_to(&engine, "t3", "dropped").expect("move");
        assert_eq!(
            task(&engine, "t1", "addBlocker", json!({"id": "t3"})).expect("addBlocker"),
            json!({"schema": "Task", "id": "t3", "status": "dropped", "open": false})
        );
        assert_eq!(blocked(&engine, "Task", "t1"), json!(false));
    }
}

#[test]
fn a_blocker_is_finished_only_in_a_terminal_state_whose_outcome_satisfied_by_lists() {
    for target in targets("dependencies_outcomes") {
        let engine = open(&target, |_| {});
        publish(
            &engine,
            &schema(
                "Check",
                json!([{"name": "Workflow", "config": check_flow()}]),
            ),
        );
        publish(
            &engine,
            &schema(
                "Milestone",
                json!([{"name": "Workflow", "config": milestone_flow()}]),
            ),
        );
        publish(
            &engine,
            &tasks(
                json!({"schemas": ["Task", "Check", "Milestone"], "gatedStates": ["done"]}),
                &[],
            ),
        );
        for id in ["t1", "t2"] {
            create(&engine, "Task", Some(id), None).expect("task");
        }
        for id in ["c1", "c2", "c3"] {
            create(&engine, "Check", Some(id), None).expect("check");
        }
        create(&engine, "Milestone", Some("m1"), None).expect("milestone");
        let check = |id: &str, to: &str| {
            invoke(
                &engine,
                &alice(),
                "Check",
                id,
                "transition",
                json!({ "to": to }),
            )
            .expect("check")
        };
        let add = |id: &str, blocker: Value| task(&engine, id, "addBlocker", blocker);
        let remove = |id: &str, blocker: Value| task(&engine, id, "removeBlocker", blocker);

        // A failed check does not let the step after it through.
        add("t1", json!({"schema": "Check", "id": "c1"})).expect("add");
        move_to(&engine, "t1", "doing").expect("move");
        check("c1", "failed");
        assert_eq!(blocked(&engine, "Task", "t1"), json!(true));
        assert_eq!(
            task(&engine, "t1", "listBlockers", json!({})).expect("list"),
            json!({"items": [{"schema": "Check", "id": "c1", "status": "failed", "open": true}], "next": null})
        );
        assert_eq!(
            reason(move_to(&engine, "t1", "done")),
            "Task t1 cannot move to done while it is blocked by Check c1 (failed)"
        );
        // So does a skipped one, whose outcome is neutral; a passed one and
        // a shipped milestone, whose config names no outcome, are
        // successes.
        remove("t1", json!({"schema": "Check", "id": "c1"})).expect("remove");
        assert_eq!(
            blocked(&engine, "Task", "t1"),
            json!(false),
            "removing the failed blocker is how the work goes on without it"
        );
        add("t1", json!({"schema": "Check", "id": "c2"})).expect("add");
        check("c2", "skipped");
        assert_eq!(blocked(&engine, "Task", "t1"), json!(true));
        remove("t1", json!({"schema": "Check", "id": "c2"})).expect("remove");
        add("t1", json!({"schema": "Check", "id": "c3"})).expect("add");
        add("t1", json!({"schema": "Milestone", "id": "m1"})).expect("add");
        check("c3", "passed");
        invoke(
            &engine,
            &alice(),
            "Milestone",
            "m1",
            "transition",
            json!({"to": "shipped"}),
        )
        .expect("ship");
        assert_eq!(blocked(&engine, "Task", "t1"), json!(false));
        assert_eq!(
            move_to(&engine, "t1", "done").expect("move"),
            json!({"from": "doing", "to": "done"})
        );
        // An instance in a terminal gated state takes no blocker that
        // failed: it is not finished.
        assert_eq!(
            reason(add("t1", json!({"schema": "Check", "id": "c1"}))),
            "it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Check c1 (failed)"
        );

        // A dependent whose satisfiedBy lists neutral goes on past a skipped
        // check, and still not past a failed one.
        publish(
            &engine,
            &tasks(
                json!({"schemas": ["Task", "Check", "Milestone"], "gatedStates": ["done"], "satisfiedBy": ["success", "neutral"]}),
                &[],
            ),
        );
        add("t2", json!({"schema": "Check", "id": "c2"})).expect("add");
        assert_eq!(blocked(&engine, "Task", "t2"), json!(false));
        assert_eq!(
            add("t2", json!({"schema": "Check", "id": "c1"})).expect("add"),
            json!({"schema": "Check", "id": "c1", "status": "failed", "open": true})
        );
        assert_eq!(blocked(&engine, "Task", "t2"), json!(true));
    }
}

#[test]
fn a_gate_on_a_state_a_transition_leaves_holds_the_start_of_work() {
    for target in targets("dependencies_start_gate") {
        let engine = world(
            &target,
            |_| {},
            Some(json!({"schemas": ["Task", "Milestone"], "gatedStates": ["doing", "done"]})),
        );
        task(&engine, "t1", "addBlocker", json!({"id": "t2"})).expect("add");
        let (behavior, _, why, _, _) = veto(move_to(&engine, "t1", "doing"));
        assert_eq!(
            json!([behavior, why]),
            json!([
                "Dependencies",
                "Task t1 cannot move to doing while it is blocked by Task t2 (todo)"
            ])
        );
        assert_eq!(
            get(&engine, "Task", "t1").expect("t1").behaviors["Workflow"]["status"],
            json!("todo")
        );
        move_to(&engine, "t2", "dropped").expect("move");
        assert_eq!(
            move_to(&engine, "t1", "doing").expect("move"),
            json!({"from": "todo", "to": "doing"}),
            "dropped is terminal and a success: the blocker is finished"
        );
        // An instance in doing, a gated state a transition leaves, takes an
        // open blocker, which holds its move to done.
        assert_eq!(
            task(
                &engine,
                "t1",
                "addBlocker",
                json!({"schema": "Milestone", "id": "m1"})
            )
            .expect("add"),
            json!({"schema": "Milestone", "id": "m1", "status": "active", "open": true})
        );
        assert_eq!(blocked(&engine, "Task", "t1"), json!(true));
        assert_eq!(
            reason(move_to(&engine, "t1", "done")),
            "Task t1 cannot move to done while it is blocked by Milestone m1 (active)"
        );
        invoke(
            &engine,
            &alice(),
            "Milestone",
            "m1",
            "transition",
            json!({"to": "shipped"}),
        )
        .expect("ship");
        assert_eq!(
            move_to(&engine, "t1", "done").expect("move"),
            json!({"from": "doing", "to": "done"})
        );
    }
}

#[test]
fn remove_blocker_removes_an_edge_and_one_that_is_not_an_edge_is_refused() {
    for target in targets("dependencies_remove") {
        let engine = world(&target, |_| {}, None);
        task(&engine, "t1", "addBlocker", json!({"id": "t2"})).expect("add");
        assert_eq!(
            task(&engine, "t1", "removeBlocker", json!({"id": "t2"})).expect("remove"),
            json!({"schema": "Task", "id": "t2"})
        );
        assert_eq!(blocked(&engine, "Task", "t1"), json!(false));
        assert_eq!(
            issues_of(&refusal(task(
                &engine,
                "t1",
                "removeBlocker",
                json!({"id": "t2"})
            ))),
            [issue("/id", "Task t2 does not block Task t1")]
        );
        // With no edge, the blocker can go without asking t1 anything.
        assert!(engine
            .instances()
            .delete(&alice(), "Task", "t2", WriteOptions::default())
            .expect("delete"));
    }
}

#[test]
fn list_blockers_pages_the_blockers_and_list_dependents_the_dependents_the_caller_may_read() {
    for target in targets("dependencies_lists") {
        let engine = open(
            &target,
            recording(|request| request.schema() != Some("Epic")),
        );
        publish(
            &engine,
            &schema(
                "Milestone",
                json!([{"name": "Workflow", "config": milestone_flow()}]),
            ),
        );
        publish(&engine, &tasks(default_dependencies(), &[]));
        let epic = |schemas: Value| {
            schema(
                "Epic",
                json!([{"name": "Workflow", "config": task_flow()}, {"name": "Dependencies", "config": {"schemas": schemas}}]),
            )
        };
        publish(&engine, &epic(json!(["Milestone"])));
        for id in ["t1", "t2", "t3"] {
            create(&engine, "Task", Some(id), None).expect("task");
        }
        create(&engine, "Milestone", Some("m1"), None).expect("milestone");
        create(&engine, "Epic", Some("e1"), None).expect("epic");
        let on = |schema: &str, id: &str, operation: &str, params: Value| {
            invoke(&engine, &alice(), schema, id, operation, params)
        };
        on(
            "Task",
            "t1",
            "addBlocker",
            json!({"schema": "Milestone", "id": "m1"}),
        )
        .expect("add");
        on("Task", "t1", "addBlocker", json!({"id": "t2"})).expect("add");
        on(
            "Epic",
            "e1",
            "addBlocker",
            json!({"schema": "Milestone", "id": "m1"}),
        )
        .expect("add");
        on(
            "Task",
            "t3",
            "addBlocker",
            json!({"schema": "Milestone", "id": "m1"}),
        )
        .expect("add");
        move_to(&engine, "t2", "dropped").expect("move");

        let first = on("Task", "t1", "listBlockers", json!({"limit": 1})).expect("list");
        assert_eq!(
            first["items"],
            json!([{"schema": "Milestone", "id": "m1", "status": "active", "open": true}])
        );
        assert_eq!(
            on(
                "Task",
                "t1",
                "listBlockers",
                json!({"cursor": first["next"]})
            )
            .expect("list"),
            json!({"items": [{"schema": "Task", "id": "t2", "status": "dropped", "open": false}], "next": null})
        );
        assert_eq!(
            refusal(on("Task", "t1", "listBlockers", json!({"cursor": "nope"}))).code(),
            EngineErrorCode::InvalidArgument
        );

        // Milestone does not compose Dependencies, so a task lists what it
        // blocks.
        assert_eq!(
            on("Task", "t2", "listDependents", json!({})).expect("list"),
            json!({"items": [{"schema": "Task", "id": "t1"}], "next": null})
        );
        on("Task", "t2", "addBlocker", json!({"id": "t3"})).expect("add");
        on(
            "Epic",
            "e1",
            "removeBlocker",
            json!({"schema": "Milestone", "id": "m1"}),
        )
        .expect("remove");
        create(&engine, "Epic", Some("e2"), None).expect("epic");
        publish(&engine, &epic(json!(["Milestone", "Task"])));
        on(
            "Epic",
            "e2",
            "addBlocker",
            json!({"schema": "Task", "id": "t3"}),
        )
        .expect("add");
        assert_eq!(
            on("Task", "t3", "listDependents", json!({})).expect("list"),
            json!({"items": [{"schema": "Task", "id": "t2"}, {"schema": "Epic", "id": "e2"}], "next": null})
        );
        // bob may not read Epic, so its dependents drop out of his pages.
        assert_eq!(
            invoke(
                &engine,
                &bob(),
                "Task",
                "t3",
                "listDependents",
                json!({"limit": 2})
            )
            .expect("list"),
            json!({"items": [{"schema": "Task", "id": "t2"}], "next": null})
        );
    }
}

#[test]
fn deleting_a_blocker_removes_its_edges_through_remove_blocker_as_the_caller() {
    for target in targets("dependencies_blocker_delete") {
        let engine = world(
            &target,
            recording(|request| {
                request.schema() == Some("Milestone") || request.action() == "read"
            }),
            None,
        );
        for id in ["t1", "t2"] {
            task(
                &engine,
                id,
                "addBlocker",
                json!({"schema": "Milestone", "id": "m1"}),
            )
            .expect("add");
        }
        // bob may write milestones, not tasks, so he cannot clear their
        // edges.
        let refused = refusal(engine.instances().delete(
            &bob(),
            "Milestone",
            "m1",
            WriteOptions::default(),
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not call removeBlocker (write) on Task in namespace default"
            )
        );
        assert!(get(&engine, "Milestone", "m1").is_some());
        let after = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
            .last()
            .map_or(0, |event| event.cursor);
        assert!(engine
            .instances()
            .delete(&alice(), "Milestone", "m1", WriteOptions::default())
            .expect("delete"));
        let seen: Vec<Value> = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    after: Some(superschematic_engine::EventsAfter::Cursor(after)),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
            .iter()
            .map(|event| {
                json!([
                    event.kind,
                    event.schema,
                    event.instance_id,
                    event.change.get("patch")
                ])
            })
            .collect();
        assert_eq!(
            seen,
            [
                json!(["delete", "Milestone", "m1", null]),
                json!(["operation", "Task", "t1", {"behaviors": {"Dependencies": {"blocked": false}}}]),
                json!(["operation", "Task", "t2", {"behaviors": {"Dependencies": {"blocked": false}}}]),
            ]
        );
        assert_eq!(blocked(&engine, "Task", "t1"), json!(false));
        assert_eq!(
            task(&engine, "t1", "listBlockers", json!({})).expect("list"),
            json!({"items": [], "next": null})
        );
    }
}

#[test]
fn deleting_a_dependent_deletes_its_edges() {
    for target in targets("dependencies_dependent_delete") {
        let engine = world(&target, |_| {}, None);
        task(&engine, "t1", "addBlocker", json!({"id": "t2"})).expect("add");
        assert!(engine
            .instances()
            .delete(&alice(), "Task", "t1", WriteOptions::default())
            .expect("delete"));
        assert_eq!(
            task(&engine, "t2", "listDependents", json!({})).expect("list"),
            json!({"items": [], "next": null})
        );
        engine
            .instances()
            .create(
                &alice(),
                "Task",
                &json!({"title": "again"}),
                CreateOptions {
                    id: Some("t1"),
                    ..Default::default()
                },
            )
            .expect("create");
        assert_eq!(blocked(&engine, "Task", "t1"), json!(false));
        assert!(engine
            .instances()
            .delete(&alice(), "Task", "t2", WriteOptions::default())
            .expect("delete"));
    }
}

#[test]
fn blockers_are_read_as_the_caller() {
    for target in targets("dependencies_read_as_caller") {
        let engine = world(
            &target,
            recording(|request| request.schema() == Some("Task")),
            None,
        );
        task(
            &engine,
            "t1",
            "addBlocker",
            json!({"schema": "Milestone", "id": "m1"}),
        )
        .expect("add");
        let read = |id: &str| {
            engine
                .instances()
                .get(&bob(), "Task", id, Default::default())
        };
        assert_eq!(refusal(read("t1")).code(), EngineErrorCode::Forbidden);
        assert_eq!(
            read("t2").expect("get").expect("t2").behaviors["Dependencies"]["blocked"],
            json!(false)
        );
        assert_eq!(
            refusal(invoke(
                &engine,
                &bob(),
                "Task",
                "t2",
                "addBlocker",
                json!({"schema": "Milestone", "id": "m1"})
            ))
            .code(),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn its_config_gated_states_are_states_of_the_types_workflow_every_terminal_one_by_default() {
    for target in targets("dependencies_config") {
        let engine = open(&target, |_| {});
        let refused = |config: Value| refusal(define(&engine, &tasks(config, &[])));
        let gated = refused(json!({"gatedStates": ["lost"]}));
        assert!(
            gated.message().contains("behavior Dependencies config: gated state \"lost\" is not a state of the type's Workflow (todo, doing, done, dropped)"),
            "{gated}"
        );
        // The core meta-schema holds the config to the declaration's
        // configSchema first.
        let at = |config: Value, path: &str| {
            let error = refused(config.clone());
            assert!(
                error.issues().iter().any(|issue| issue.path == path),
                "{config}: {error}"
            );
        };
        at(
            json!({"gatedStates": []}),
            "/types/Task/behaviors/1/config/gatedStates",
        );
        at(
            json!({"schemas": ["task list"]}),
            "/types/Task/behaviors/1/config/schemas/0",
        );
        at(
            json!({"satisfiedBy": []}),
            "/types/Task/behaviors/1/config/satisfiedBy",
        );
        at(
            json!({"satisfiedBy": ["done"]}),
            "/types/Task/behaviors/1/config/satisfiedBy/0",
        );
        at(
            json!({"satisfiedBy": ["success", "success"]}),
            "/types/Task/behaviors/1/config/satisfiedBy",
        );
        let lone = refusal(define(
            &engine,
            &schema("Task", json!([{"name": "Dependencies"}])),
        ));
        assert!(
            lone.message().contains(
                "behavior Dependencies requires behavior Workflow, which the type does not list"
            ),
            "{lone}"
        );
        publish(&engine, &tasks(json!({}), &[]));
        create(&engine, "Task", Some("t1"), None).expect("t1");
        create(&engine, "Task", Some("t2"), None).expect("t2");
        task(&engine, "t1", "addBlocker", json!({"id": "t2"})).expect("add");
        move_to(&engine, "t1", "doing").expect("move");
        // Every terminal state is gated by default: dropped too.
        assert_eq!(veto(move_to(&engine, "t1", "dropped")).0, "Dependencies");
    }
}

#[test]
fn config_change_lets_schemas_gated_states_and_satisfied_by_change_and_it_does_not_leave() {
    for target in targets("dependencies_versions") {
        let engine = open(&target, |_| {});
        publish(
            &engine,
            &schema(
                "Milestone",
                json!([{"name": "Workflow", "config": milestone_flow()}]),
            ),
        );
        let flow_only = schema("Task", json!([{"name": "Workflow", "config": task_flow()}]));
        publish(&engine, &flow_only);
        create(&engine, "Task", Some("t1"), None).expect("t1");
        create(&engine, "Task", Some("t2"), None).expect("t2");
        publish(&engine, &tasks(json!({"gatedStates": ["done"]}), &[]));
        assert_eq!(
            blocked(&engine, "Task", "t1"),
            json!(false),
            "an instance that exists starts with no blocker"
        );
        task(&engine, "t1", "addBlocker", json!({"id": "t2"})).expect("add");
        publish(
            &engine,
            &tasks(json!({"schemas": ["Task", "Milestone"]}), &[]),
        );
        move_to(&engine, "t1", "doing").expect("move");
        assert_eq!(
            veto(move_to(&engine, "t1", "dropped")).0,
            "Dependencies",
            "dropped is gated now"
        );
        publish(
            &engine,
            &tasks(
                json!({"schemas": ["Task", "Milestone"], "gatedStates": ["doing", "done"], "satisfiedBy": ["success", "neutral"]}),
                &[],
            ),
        );
        assert_eq!(
            move_to(&engine, "t1", "dropped").expect("move"),
            json!({"from": "doing", "to": "dropped"}),
            "dropped is not gated now"
        );
        define(&engine, &tasks(json!({"schemas": ["Milestone"]}), &[])).expect("define");
        assert!(
            engine
                .schemas()
                .publish(&alice(), "Task", SchemaTarget::default())
                .expect("publish")
                .published
        );
        assert_eq!(
            blocked(&engine, "Task", "t1"),
            json!(true),
            "an edge made before stays"
        );
        let removed = refusal(define(&engine, &flow_only));
        assert_eq!(
            changes(&removed),
            [json!({
                "path": "Task.behaviors.Dependencies",
                "message": "behavior Dependencies cannot be removed from type Task, which has instances: the edges and references its instances hold would stay behind"
            })]
        );
    }
}

#[test]
fn a_create_gives_blockers_and_it_is_blocked_from_its_creates_event() {
    for target in targets("dependencies_create") {
        let engine = world(&target, |_| {}, None);
        let created = create(
            &engine,
            "Task",
            Some("t4"),
            Some(
                json!({"Dependencies": {"blockers": [{"id": "t1"}, {"schema": "Milestone", "id": "m1"}]}}),
            ),
        )
        .expect("create");
        let workflow = json!({"status": "todo", "enteredAt": created.created_at});
        assert_eq!(
            json!([created.data, created.behaviors]),
            json!([{"title": "t4"}, {"Workflow": workflow, "Dependencies": {"blocked": true}}])
        );
        let made: Vec<Value> = events(&engine, "Task", "t4")
            .iter()
            .map(|event| json!([event.kind, event.change]))
            .collect();
        assert_eq!(
            made,
            [
                json!(["create", {"data": {"title": "t4"}, "behaviors": {"Workflow": workflow, "Dependencies": {"blocked": true}}}])
            ]
        );
        assert_eq!(
            task(&engine, "t4", "listBlockers", json!({})).expect("list")["items"],
            json!([
                {"schema": "Task", "id": "t1", "status": "todo", "open": true},
                {"schema": "Milestone", "id": "m1", "status": "active", "open": true}
            ])
        );
        move_to(&engine, "t4", "doing").expect("move");
        assert_eq!(veto(move_to(&engine, "t4", "done")).0, "Dependencies");
        // The edges are references, as addBlocker's are: a blocker's delete
        // removes its edge.
        assert!(engine
            .instances()
            .delete(&alice(), "Milestone", "m1", WriteOptions::default())
            .expect("delete"));
        assert_eq!(
            events(&engine, "Task", "t4")
                .last()
                .expect("an event")
                .change,
            json!({
                "behavior": "Dependencies",
                "operation": "removeBlocker",
                "params": {"schema": "Milestone", "id": "m1"},
                "patch": {}
            })
        );
        assert_eq!(
            task(&engine, "t1", "listDependents", json!({})).expect("list"),
            json!({"items": [{"schema": "Task", "id": "t4"}], "next": null})
        );
    }
}

#[test]
fn a_creates_blockers_are_held_to_add_blockers_checks_at_pointers_into_its_parameters() {
    for target in targets("dependencies_create_checks") {
        let engine = world(&target, |_| {}, None);
        let create_with = |blockers: Value| {
            create(
                &engine,
                "Task",
                Some("t4"),
                Some(json!({"Dependencies": {"blockers": blockers}})),
            )
        };
        let issues = |blockers: Value| issues_of(&refusal(create_with(blockers)));
        assert_eq!(
            issues(json!([{"id": "t1"}, {"schema": "Note", "id": "n1"}])),
            [issue(
                "/behaviors/Dependencies/blockers/1/schema",
                "a blocker of Task is an instance of Task, Milestone, not Note"
            )]
        );
        assert_eq!(
            issues(json!([{"id": "t9"}])),
            [issue(
                "/behaviors/Dependencies/blockers/0/id",
                "Task t9 does not exist"
            )]
        );
        // An instance cannot block itself: the edge would be a cycle.
        assert_eq!(
            issues(json!([{"id": "t4"}])),
            [issue(
                "/behaviors/Dependencies/blockers/0/id",
                "Task t4 cannot block itself"
            )]
        );
        // A veto of a create's blocker carries addBlocker's declared code,
        // and its entry.
        let (behavior, action, why, code, details) = veto(create_with(
            json!([{"id": "t1"}, {"schema": "Task", "id": "t1"}]),
        ));
        assert_eq!(
            json!([behavior, action, why, code, details]),
            json!([
                "Dependencies",
                "create",
                "Task t1 already blocks it",
                "already_blocking",
                {"path": "/behaviors/Dependencies/blockers/1"}
            ])
        );
        // Its createParamsSchema holds the shape.
        assert_eq!(
            issues(json!("t1")),
            [issue("/behaviors/Dependencies/blockers", "must be array")]
        );
        assert_eq!(
            issues(json!([{"id": "t1", "open": true}])),
            [issue(
                "/behaviors/Dependencies/blockers/0",
                "must NOT have additional properties: open"
            )]
        );
        assert_eq!(
            issues_of(&refusal(create(
                &engine,
                "Task",
                None,
                Some(json!({"Dependencies": {"blocker": []}}))
            ))),
            [issue(
                "/behaviors/Dependencies",
                "must NOT have additional properties: blocker"
            )]
        );
        assert!(get(&engine, "Task", "t4").is_none());
        assert_eq!(
            task(&engine, "t1", "listDependents", json!({})).expect("list"),
            json!({"items": [], "next": null})
        );
    }
}

#[test]
fn a_create_in_a_gated_state_no_transition_leaves_takes_no_blocker_that_is_not_finished() {
    for target in targets("dependencies_create_gated") {
        let engine = world(&target, |_| {}, None);
        publish(
            &engine,
            &schema(
                "Check",
                json!([{"name": "Workflow", "config": check_flow()}]),
            ),
        );
        create(&engine, "Check", Some("c1"), None).expect("c1");
        invoke(
            &engine,
            &alice(),
            "Check",
            "c1",
            "transition",
            json!({"to": "failed"}),
        )
        .expect("fail");
        // Gate starts done, a terminal state and so gated; it lists
        // Dependencies before Workflow.
        let gate_flow = json!({"states": ["todo", "done"], "initial": "done", "transitions": [{"from": "todo", "to": "done"}]});
        let gate = |dependencies: Value| {
            publish(
                &engine,
                &schema(
                    "Gate",
                    json!([{"name": "Dependencies", "config": dependencies}, {"name": "Workflow", "config": gate_flow}]),
                ),
            );
        };
        let create_gate = |blocker: Value| {
            create(
                &engine,
                "Gate",
                None,
                Some(json!({"Dependencies": {"blockers": [blocker]}})),
            )
        };
        gate(json!({"schemas": ["Task", "Check"]}));
        let (_, action, why, code, _) = veto(create_gate(json!({"schema": "Task", "id": "t1"})));
        assert_eq!(
            json!([action, why, code]),
            json!([
                "create",
                "it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Task t1 (todo)",
                "gated"
            ])
        );
        // A failed check is terminal, and still not finished: satisfiedBy
        // lists success alone.
        assert_eq!(
            reason(create_gate(json!({"schema": "Check", "id": "c1"}))),
            "it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Check c1 (failed)"
        );
        move_to(&engine, "t1", "doing").expect("move");
        move_to(&engine, "t1", "done").expect("move");
        // Its behaviors' fields come in the order the type lists them.
        let finished = create_gate(json!({"schema": "Task", "id": "t1"})).expect("create");
        assert_eq!(
            json!([finished.data, finished.behaviors]),
            json!([{"title": "untitled"}, {"Dependencies": {"blocked": false}, "Workflow": {"status": "done", "enteredAt": finished.created_at}}])
        );
        let keys: Vec<&String> = finished.behaviors.keys().collect();
        assert_eq!(keys, ["Dependencies", "Workflow"]);
        gate(json!({"schemas": ["Task", "Check"], "satisfiedBy": ["success", "failure"]}));
        let checked = create_gate(json!({"schema": "Check", "id": "c1"})).expect("create");
        assert_eq!(
            Value::Object(checked.behaviors),
            json!({"Dependencies": {"blocked": false}, "Workflow": {"status": "done", "enteredAt": checked.created_at}})
        );
    }
}

#[test]
fn a_create_in_a_gated_state_a_transition_leaves_takes_an_open_blocker() {
    for target in targets("dependencies_create_open") {
        let engine = world(
            &target,
            |_| {},
            Some(
                json!({"schemas": ["Task", "Milestone"], "gatedStates": ["todo", "doing", "done"]}),
            ),
        );
        let created = create(
            &engine,
            "Task",
            Some("t4"),
            Some(json!({"Dependencies": {"blockers": [{"id": "t1"}]}})),
        )
        .expect("create");
        assert_eq!(
            Value::Object(created.behaviors.clone()),
            json!({"Workflow": {"status": "todo", "enteredAt": created.created_at}, "Dependencies": {"blocked": true}})
        );
        assert_eq!(
            reason(move_to(&engine, "t4", "doing")),
            "Task t4 cannot move to doing while it is blocked by Task t1 (todo)"
        );
        move_to(&engine, "t1", "dropped").expect("move");
        assert_eq!(
            move_to(&engine, "t4", "doing").expect("move"),
            json!({"from": "todo", "to": "doing"})
        );
    }
}

#[test]
fn the_gates_veto_lists_the_open_blockers_in_its_details() {
    for target in targets("dependencies_veto_details") {
        let engine = world(&target, |_| {}, None);
        task(&engine, "t1", "addBlocker", json!({"id": "t2"})).expect("add");
        task(
            &engine,
            "t1",
            "addBlocker",
            json!({"schema": "Milestone", "id": "m1"}),
        )
        .expect("add");
        move_to(&engine, "t1", "doing").expect("move");
        let (_, _, _, code, details) = veto(move_to(&engine, "t1", "done"));
        assert_eq!(
            json!([code, details]),
            json!([
                "blocked",
                {"blockers": [{"schema": "Task", "id": "t2", "status": "todo"}, {"schema": "Milestone", "id": "m1", "status": "active"}]}
            ])
        );
    }
}
