//! Rollups, values derived from linked instances (D16, amended): the cases
//! of `test/core-rollups.test.ts` (every function over the instances that
//! point at one through a Links link, latest's order by creation, computed
//! at each read with no event on the instance read, the bound of 500
//! linked instances, reads as the caller, the Workflow gate, all and any
//! limited to outcomes, the config rules at define and publish, a rollup
//! over its own schema, a link that no longer points at the schema, and
//! configChange), on SQLite and, with the variable set, on Postgres. The
//! guidance is held to the TypeScript text in `behaviors/tests/guidance.rs`.

mod support;

use serde_json::{json, Map, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, EngineOptions,
    Error, GetOptions, ListOptions, Policy, Principal, ReadEvents, SchemaTarget, ValidateOptions,
    WriteOptions,
};
use superschematic_engine_behaviors::{
    Approvals, Comments, Constants, Dependencies, Links, Numbering, Revisions, Rollups, StateRules,
    Timers, Variants, Workflow, MAX_ROLLUP_READ,
};
use support::{alice, issues_of, refusal, schema_document, targets, veto, RecordingPolicy, Target};

/// The core's behaviors with Rollups, in the TypeScript engine's order.
fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Comments)
        .with(Revisions)
        .with(Dependencies)
        .with(Links)
        .with(Rollups)
        .with(Constants)
        .with(Variants)
        .with(StateRules)
        .with(Approvals)
        .with(Numbering)
        .with(Timers)
}

fn bob() -> Principal {
    Principal::new("bob", Vec::<String>::new())
}

// A task moves from todo to doing and then to done, or is dropped; done and
// dropped are terminal.
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

// A project is active until it is done or dropped.
fn project_flow() -> Value {
    json!({
        "states": ["active", "done", "dropped"],
        "transitions": [{"from": "active", "to": "done"}, {"from": "active", "to": "dropped"}]
    })
}

fn task_behaviors() -> Value {
    json!([
        {"name": "Workflow", "config": task_flow()},
        {"name": "Links", "config": {"links": {"project": {"schema": "Project"}}}}
    ])
}

// A Task has a title, an estimate (a number), hours (an integer), a kind
// (an enum), urgent (a boolean), labels (a list) and notes (any JSON).
fn tasks(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Task",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "estimate", "typeRef": {"name": "number"}},
            {"name": "hours", "typeRef": {"name": "Generic.Int64"}},
            {"name": "kind", "typeRef": {"name": "Kind"}},
            {"name": "urgent", "typeRef": {"name": "boolean"}},
            {"name": "labels", "typeRef": {"name": "string", "isArray": true}},
            {"name": "notes", "typeRef": {"name": "Generic.JSON"}}
        ]),
        json!({
            "enums": {
                "Kind": {
                    "name": "Kind",
                    "values": [
                        {"name": "BUG", "serializedAs": "bug"},
                        {"name": "FEATURE", "serializedAs": "feature"}
                    ]
                }
            }
        }),
    );
    document["types"]["Task"]["behaviors"] = behaviors;
    document
}

fn rollup(function: &str, extra: Value) -> Value {
    let mut out = json!({"schema": "Task", "link": "project", "function": function});
    if let Value::Object(extra) = extra {
        for (key, value) in extra {
            out[key] = value;
        }
    }
    out
}

fn every() -> Value {
    json!({
        "tasks": rollup("count", json!({})),
        "byStatus": rollup("countBy", json!({"field": "Workflow.status"})),
        "byKind": rollup("countBy", json!({"field": "kind"})),
        "byUrgency": rollup("countBy", json!({"field": "urgent"})),
        "estimate": rollup("sum", json!({"field": "estimate"})),
        "smallest": rollup("min", json!({"field": "estimate"})),
        "longest": rollup("max", json!({"field": "hours"})),
        "finished": rollup("all", json!({"gatedStates": ["done"]})),
        "started": rollup("any", json!({}))
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

fn projects_with(rollups: Value, workflow: bool) -> Value {
    let mut behaviors = Vec::new();
    if workflow {
        behaviors.push(json!({"name": "Workflow", "config": project_flow()}));
    }
    behaviors.push(json!({"name": "Rollups", "config": {"rollups": rollups}}));
    schema("Project", Value::Array(behaviors))
}

fn projects(rollups: Value) -> Value {
    projects_with(rollups, true)
}

fn define(engine: &Engine, document: &Value, principal: &Principal) -> Result<(), Error> {
    engine
        .schemas()
        .define(principal, document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) {
    define(engine, document, &alice()).expect("define");
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().expect("a name"),
            SchemaTarget::default(),
        )
        .expect("publish");
}

fn create(engine: &Engine, schema: &str, id: &str, data: Value) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
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

// task creates a Task titled by its id, with more fields, and links it to a
// project.
fn task(engine: &Engine, id: &str, data: Value, project: Option<&str>) {
    let mut data = data;
    data["title"] = json!(id);
    create(engine, "Task", id, data);
    if let Some(project) = project {
        invoke(
            engine,
            "Task",
            id,
            "link",
            json!({"name": "project", "id": project}),
        );
    }
}

fn move_as(
    engine: &Engine,
    principal: &Principal,
    schema: &str,
    id: &str,
    to: &str,
) -> Result<Value, Error> {
    engine.instances().invoke(
        principal,
        schema,
        id,
        "transition",
        &json!({ "to": to }),
        WriteOptions::default(),
    )
}

fn move_to(engine: &Engine, schema: &str, id: &str, to: &str) -> Result<Value, Error> {
    move_as(engine, &alice(), schema, id, to)
}

fn read(engine: &Engine, schema: &str, id: &str) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .expect("get")
        .expect("the instance")
}

fn rollups_of(engine: &Engine, id: &str) -> Value {
    read(engine, "Project", id).behaviors["Rollups"]["values"].clone()
}

fn open(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        configure(options);
    })
}

// Projects p1, p2 and p3; tasks t1 to t3 point at p1, t4 at p2 and t5 at
// none.
fn world(engine: &Engine, rollups: Value) {
    publish(engine, &tasks(task_behaviors()));
    publish(engine, &projects(rollups));
    for id in ["p1", "p2", "p3"] {
        create(engine, "Project", id, json!({ "title": id }));
    }
    task(
        engine,
        "t1",
        json!({"estimate": 3, "hours": 2, "kind": "bug", "urgent": true, "labels": ["a"]}),
        Some("p1"),
    );
    task(
        engine,
        "t2",
        json!({"estimate": 5.5, "kind": "feature", "urgent": false}),
        Some("p1"),
    );
    task(engine, "t3", json!({"notes": {"size": 8}}), Some("p1"));
    task(
        engine,
        "t4",
        json!({"estimate": 8, "hours": 10, "kind": "bug"}),
        Some("p2"),
    );
    task(engine, "t5", json!({"estimate": 100, "hours": 100}), None);
}

fn opened_world(target: &Target) -> Engine {
    let engine = open(target, |_| {});
    world(&engine, every());
    engine
}

fn moved(from: &str, to: &str) -> Value {
    json!({ "from": from, "to": to })
}

#[test]
fn each_function_runs_over_the_instances_that_point_at_the_instance_through_the_link_and_only_those(
) {
    for target in targets("rollups_functions") {
        let engine = opened_world(&target);
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({
                "byKind": {"bug": 1, "feature": 1},
                "byStatus": {"todo": 3},
                "byUrgency": {"false": 1, "true": 1},
                "estimate": 8.5,
                "finished": false,
                "longest": 2,
                "smallest": 3,
                "started": false,
                "tasks": 3
            })
        );
        move_to(&engine, "Task", "t1", "doing").expect("doing");
        move_to(&engine, "Task", "t1", "done").expect("done");
        move_to(&engine, "Task", "t2", "dropped").expect("dropped");
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({
                "byKind": {"bug": 1, "feature": 1},
                "byStatus": {"done": 1, "dropped": 1, "todo": 1},
                "byUrgency": {"false": 1, "true": 1},
                "estimate": 8.5,
                "finished": false,
                "longest": 2,
                "smallest": 3,
                "started": true,
                "tasks": 3
            })
        );
        move_to(&engine, "Task", "t3", "dropped").expect("dropped");
        assert_eq!(
            rollups_of(&engine, "p1")["finished"],
            json!(true),
            "every terminal state counts, not only done"
        );
        assert_eq!(
            rollups_of(&engine, "p2"),
            json!({
                "byKind": {"bug": 1},
                "byStatus": {"todo": 1},
                "byUrgency": {},
                "estimate": 8,
                "finished": false,
                "longest": 10,
                "smallest": 8,
                "started": false,
                "tasks": 1
            })
        );
        // None point at p3: all of none holds, any of none does not, and min
        // and max have no value.
        let none = json!({"byKind": {}, "byStatus": {}, "byUrgency": {}, "estimate": 0, "finished": true, "started": false, "tasks": 0});
        assert_eq!(rollups_of(&engine, "p3"), none);
        let p3 = read(&engine, "Project", "p3");
        assert_eq!(
            json!([p3.data, p3.behaviors]),
            json!([
                {"title": "p3"},
                {
                    "Workflow": {"status": "active", "enteredAt": p3.created_at},
                    "Rollups": {"values": none}
                }
            ])
        );
        // The counts by value list their keys sorted.
        let keys: Vec<String> = rollups_of(&engine, "p1")["byStatus"]
            .as_object()
            .expect("counts")
            .keys()
            .cloned()
            .collect();
        assert_eq!(keys, ["done", "dropped"]);
    }
}

#[test]
fn latest_is_a_fields_value_on_the_linked_instance_created_last_the_greater_id_on_a_tie() {
    for target in targets("rollups_latest") {
        let engine = open(&target, |options| options.clock = Clock::Manual(100));
        publish(&engine, &tasks(task_behaviors()));
        publish(
            &engine,
            &projects(json!({
                "lastKind": rollup("latest", json!({"field": "kind"})),
                "lastNotes": rollup("latest", json!({"field": "notes"})),
                "lastStatus": rollup("latest", json!({"field": "Workflow.status"}))
            })),
        );
        create(&engine, "Project", "p1", json!({"title": "p1"}));
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({}),
            "over no instance it has no value"
        );
        task(
            &engine,
            "t2",
            json!({"kind": "bug", "notes": {"size": 1}}),
            Some("p1"),
        );
        task(&engine, "t1", json!({"kind": "feature"}), Some("p1"));
        // t1 and t2 were created at once: the greater id is the latest.
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"lastKind": "bug", "lastNotes": {"size": 1}, "lastStatus": "todo"})
        );
        engine.clock().set(200).expect("a manual clock");
        task(&engine, "t0", json!({"notes": [1, 2]}), Some("p1"));
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"lastNotes": [1, 2], "lastStatus": "todo"}),
            "the latest holds no kind, so lastKind has no value"
        );
        move_to(&engine, "Task", "t0", "doing").expect("doing");
        assert_eq!(rollups_of(&engine, "p1")["lastStatus"], json!("doing"));
        // Creation orders, not linking: an older instance linked now is not
        // the latest.
        engine.clock().set(50).expect("a manual clock");
        task(&engine, "old", json!({"kind": "feature"}), None);
        engine.clock().set(300).expect("a manual clock");
        invoke(
            &engine,
            "Task",
            "old",
            "link",
            json!({"name": "project", "id": "p1"}),
        );
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"lastNotes": [1, 2], "lastStatus": "doing"})
        );
        // The latest gone, the one created before it is.
        engine
            .instances()
            .delete(&alice(), "Task", "t0", WriteOptions::default())
            .expect("delete");
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"lastKind": "bug", "lastNotes": {"size": 1}, "lastStatus": "todo"})
        );
    }
}

#[test]
fn a_bare_status_names_the_linked_types_own_field_and_workflow_status_its_workflows() {
    for target in targets("rollups_status") {
        let engine = open(&target, |_| {});
        let mut document = tasks(task_behaviors());
        document["types"]["Task"]["fields"]
            .as_array_mut()
            .expect("fields")
            .push(json!({"name": "status", "typeRef": {"name": "string"}}));
        publish(&engine, &document);
        publish(
            &engine,
            &projects(json!({
                "byOwn": rollup("countBy", json!({"field": "status"})),
                "byWorkflow": rollup("countBy", json!({"field": "Workflow.status"})),
                "lastOwn": rollup("latest", json!({"field": "status"})),
                "lastWorkflow": rollup("latest", json!({"field": "Workflow.status"}))
            })),
        );
        create(&engine, "Project", "p1", json!({"title": "p1"}));
        task(&engine, "t1", json!({"status": "billed"}), Some("p1"));
        task(&engine, "t2", json!({"status": "billed"}), Some("p1"));
        move_to(&engine, "Task", "t2", "doing").expect("doing");
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({
                "byOwn": {"billed": 2},
                "byWorkflow": {"doing": 1, "todo": 1},
                "lastOwn": "billed",
                "lastWorkflow": "doing"
            })
        );
    }
}

fn project_events(engine: &Engine) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Project"),
                instance_id: Some("p1"),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

#[test]
fn a_linked_instances_change_shows_at_the_next_read_with_no_event_on_the_instance_read() {
    for target in targets("rollups_reads") {
        let engine = opened_world(&target);
        let before = read(&engine, "Project", "p1");
        let events = project_events(&engine).len();
        move_to(&engine, "Task", "t3", "doing").expect("doing");
        engine
            .instances()
            .update(
                &alice(),
                "Task",
                "t2",
                &json!({"estimate": 1.5}),
                WriteOptions::default(),
            )
            .expect("update");
        invoke(
            &engine,
            "Task",
            "t5",
            "link",
            json!({"name": "project", "id": "p1"}),
        );
        invoke(&engine, "Task", "t1", "unlink", json!({"name": "project"}));
        invoke(
            &engine,
            "Task",
            "t4",
            "link",
            json!({"name": "project", "id": "p1"}),
        );
        let after = read(&engine, "Project", "p1");
        assert_eq!(
            json!([after.seq, after.behaviors["Rollups"]["values"]]),
            json!([
                before.seq,
                {
                    "byKind": {"bug": 1, "feature": 1},
                    "byStatus": {"doing": 1, "todo": 3},
                    "byUrgency": {"false": 1},
                    "estimate": 109.5,
                    "finished": false,
                    "longest": 100,
                    "smallest": 1.5,
                    "started": false,
                    "tasks": 4
                }
            ])
        );
        assert_eq!(
            rollups_of(&engine, "p2")["tasks"],
            json!(0),
            "t4 moved to p1"
        );
        engine
            .instances()
            .delete(&alice(), "Task", "t5", WriteOptions::default())
            .expect("delete");
        assert_eq!(rollups_of(&engine, "p1")["tasks"], json!(3));
        assert_eq!(
            project_events(&engine).len(),
            events,
            "no event on the project"
        );
        // The project's own change carries no rollup in its event: they are
        // the same before and after it.
        engine
            .instances()
            .update(
                &alice(),
                "Project",
                "p1",
                &json!({"title": "Renamed"}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(
            project_events(&engine).last().expect("an event").change,
            json!({"data": {"title": "Renamed"}})
        );
    }
}

#[test]
fn a_transition_into_a_gated_state_waits_for_its_rollup_to_hold_whoever_asks() {
    for target in targets("rollups_gate") {
        let engine = opened_world(&target);
        move_to(&engine, "Task", "t1", "doing").expect("doing");
        let vetoed = veto(move_to(&engine, "Project", "p1", "done"));
        assert_eq!(
            (vetoed.0.as_str(), vetoed.1.as_str(), vetoed.2.as_str(), vetoed.3.as_deref(), vetoed.4),
            (
                "Rollups",
                "transition",
                "Project p1 cannot move to done until rollup finished holds: 3 of the 3 instances of Task that point at it through project are not in a terminal state",
                Some("not_held"),
                Some(json!({"rollup": "finished", "to": "done", "over": false, "linked": 3, "counted": 0}))
            )
        );
        assert_eq!(
            read(&engine, "Project", "p1").behaviors["Workflow"]["status"],
            json!("active")
        );
        move_to(&engine, "Task", "t1", "done").expect("done");
        move_to(&engine, "Task", "t2", "dropped").expect("dropped");
        assert!(veto(move_to(&engine, "Project", "p1", "done"))
            .2
            .contains("1 of the 3 instances of Task"));
        assert_eq!(
            move_to(&engine, "Project", "p2", "dropped").expect("dropped is not gated"),
            moved("active", "dropped")
        );
        move_to(&engine, "Task", "t3", "dropped").expect("dropped");
        assert_eq!(
            move_to(&engine, "Project", "p1", "done").expect("done"),
            moved("active", "done")
        );
        assert_eq!(
            move_to(&engine, "Project", "p3", "done").expect("a project with no task can finish"),
            moved("active", "done")
        );
    }
}

#[test]
fn an_any_rollup_gates_too_once_one_linked_instance_is_in_a_terminal_state() {
    for target in targets("rollups_any_gate") {
        let engine = open(&target, |_| {});
        world(
            &engine,
            json!({"started": rollup("any", json!({"gatedStates": ["done", "dropped"]}))}),
        );
        let vetoed = veto(move_to(&engine, "Project", "p1", "dropped"));
        assert_eq!(
            vetoed.2,
            "Project p1 cannot move to dropped until rollup started holds: none of the 3 instances of Task that point at it through project is in a terminal state"
        );
        assert_eq!(
            (vetoed.3.as_deref(), vetoed.4),
            (
                Some("not_held"),
                Some(
                    json!({"rollup": "started", "to": "dropped", "over": false, "linked": 3, "counted": 0})
                )
            )
        );
        assert!(veto(move_to(&engine, "Project", "p3", "done")).2.ends_with(
            "until rollup started holds: no instance of Task points at it through project"
        ));
        move_to(&engine, "Task", "t2", "dropped").expect("dropped");
        assert_eq!(
            move_to(&engine, "Project", "p1", "dropped").expect("dropped"),
            moved("active", "dropped")
        );
    }
}

#[test]
fn all_and_any_with_outcomes_count_only_the_linked_instances_whose_terminal_outcome_they_list() {
    for target in targets("rollups_outcomes") {
        let engine = open(&target, |_| {});
        let mut flow = task_flow();
        flow["outcomes"] = json!({"dropped": "failure"});
        publish(
            &engine,
            &tasks(json!([
                {"name": "Workflow", "config": flow},
                {"name": "Links", "config": {"links": {"project": {"schema": "Project"}}}}
            ])),
        );
        publish(
            &engine,
            &projects(json!({
                "finished": rollup("all", json!({})),
                "succeeded": rollup("all", json!({"outcomes": ["success"], "gatedStates": ["done"]})),
                "failed": rollup("any", json!({"outcomes": ["failure"], "gatedStates": ["dropped"]}))
            })),
        );
        for id in ["p1", "p2"] {
            create(&engine, "Project", id, json!({ "title": id }));
        }
        task(&engine, "t1", json!({}), Some("p1"));
        task(&engine, "t2", json!({}), Some("p1"));
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"failed": false, "finished": false, "succeeded": false})
        );
        move_to(&engine, "Task", "t1", "doing").expect("doing");
        move_to(&engine, "Task", "t1", "done").expect("done");
        move_to(&engine, "Task", "t2", "dropped").expect("dropped");
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"failed": true, "finished": true, "succeeded": false}),
            "without outcomes, every terminal state counts"
        );
        assert_eq!(
            veto(move_to(&engine, "Project", "p1", "done")).2,
            "Project p1 cannot move to done until rollup succeeded holds: 1 of the 2 instances of Task that point at it through project are not in a terminal state whose outcome is success"
        );
        assert_eq!(
            move_to(&engine, "Project", "p1", "dropped").expect("dropped"),
            moved("active", "dropped")
        );
        // All of none holds and any of none does not, outcomes or not.
        assert_eq!(
            rollups_of(&engine, "p2"),
            json!({"failed": false, "finished": true, "succeeded": true})
        );
        task(&engine, "t3", json!({}), Some("p2"));
        move_to(&engine, "Task", "t3", "doing").expect("doing");
        move_to(&engine, "Task", "t3", "done").expect("done");
        assert_eq!(
            veto(move_to(&engine, "Project", "p2", "dropped")).2,
            "Project p2 cannot move to dropped until rollup failed holds: none of the 1 instances of Task that point at it through project is in a terminal state whose outcome is failure"
        );
        assert_eq!(
            move_to(&engine, "Project", "p2", "done").expect("done"),
            moved("active", "done")
        );
    }
}

#[test]
fn a_rollup_reads_at_most_500_linked_instances_and_past_that_it_is_over_and_its_gate_does_not_hold()
{
    for target in targets("rollups_bound") {
        let engine = open(&target, |_| {});
        publish(&engine, &tasks(task_behaviors()));
        publish(
            &engine,
            &schema(
                "Board",
                json!([{"name": "Links", "config": {"links": {"project": {"schema": "Project"}}}}]),
            ),
        );
        publish(
            &engine,
            &projects(json!({
                "tasks": rollup("count", json!({})),
                "estimate": rollup("sum", json!({"field": "estimate"})),
                "finished": rollup("all", json!({"gatedStates": ["done"]})),
                "boards": {"schema": "Board", "link": "project", "function": "count"}
            })),
        );
        create(&engine, "Project", "p1", json!({"title": "Big"}));
        create(&engine, "Board", "b1", json!({"title": "Wall"}));
        invoke(
            &engine,
            "Board",
            "b1",
            "link",
            json!({"name": "project", "id": "p1"}),
        );
        for index in 1..=MAX_ROLLUP_READ {
            let id = format!("t{index}");
            task(&engine, &id, json!({"estimate": 1}), Some("p1"));
            move_to(&engine, "Task", &id, "dropped").expect("dropped");
        }
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"boards": 1, "estimate": 500, "finished": true, "tasks": 500})
        );
        task(&engine, "last", json!({"estimate": 1}), Some("p1"));
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"boards": 1, "estimate": {"over": true}, "finished": {"over": true}, "tasks": {"over": true}})
        );
        let vetoed = veto(move_to(&engine, "Project", "p1", "done"));
        assert_eq!(
            vetoed.2,
            "Project p1 cannot move to done: rollup finished has no value, since more than 500 instances of Task that point at it through project exist"
        );
        assert_eq!(
            (vetoed.3.as_deref(), vetoed.4),
            (
                Some("not_held"),
                Some(json!({"rollup": "finished", "to": "done", "over": true}))
            )
        );
        invoke(
            &engine,
            "Task",
            "last",
            "unlink",
            json!({"name": "project"}),
        );
        assert_eq!(
            move_to(&engine, "Project", "p1", "done").expect("done"),
            moved("active", "done")
        );
    }
}

#[test]
fn it_reads_as_the_caller_without_read_on_the_linked_schema_the_instance_cannot_be_read_or_moved() {
    for target in targets("rollups_caller") {
        let policy = RecordingPolicy::new(|request| {
            request.principal().subject == "alice" || request.schema() == Some("Project")
        });
        let asked = policy.clone();
        let engine = open(&target, |options| {
            options.policy = Some(Policy::custom(policy));
        });
        world(&engine, every());
        asked.take();
        rollups_of(&engine, "p1");
        let questions: Vec<Value> = asked
            .take()
            .iter()
            .map(|question| {
                json!([
                    question["action"],
                    question["schema"],
                    question.get("operation")
                ])
            })
            .collect();
        assert_eq!(
            questions,
            [
                json!(["read", "Project", null]),
                json!(["read", "Task", null]),
                json!(["read", "Task", "listLinked"]),
                json!(["read", "Task", null]),
                json!(["read", "Task", null])
            ],
            "the project, then the tasks' Links config, listLinked, the tasks and their Workflow config"
        );
        assert_eq!(
            refusal(
                engine
                    .instances()
                    .get(&bob(), "Project", "p1", GetOptions::default())
            )
            .code(),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            refusal(
                engine
                    .instances()
                    .list(&bob(), "Project", ListOptions::default())
            )
            .code(),
            EngineErrorCode::Forbidden
        );
        let refused = refusal(move_as(&engine, &bob(), "Project", "p3", "done"));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not read Task in namespace default"
            )
        );
        assert_eq!(
            read(&engine, "Project", "p3").behaviors["Workflow"]["status"],
            json!("active")
        );
        // Defining a rollup asks read on the schema it names, as the caller
        // who defines it.
        let definer = refusal(define(
            &engine,
            &projects(json!({"tasks": rollup("count", json!({}))})),
            &bob(),
        ));
        assert_eq!(
            (definer.code(), definer.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not read Task in namespace default"
            )
        );
    }
}

#[test]
fn its_config_at_define_holds_gated_states_and_a_linked_schema_with_the_link_and_fitting_fields() {
    for target in targets("rollups_config") {
        let engine = open(&target, |_| {});
        publish(
            &engine,
            &schema("Note", json!([{"name": "Workflow", "config": task_flow()}])),
        );
        publish(
            &engine,
            &schema(
                "Plain",
                json!([{"name": "Links", "config": {"links": {"project": {"schema": "Project"}}}}]),
            ),
        );
        publish(
            &engine,
            &tasks(json!([
                {"name": "Workflow", "config": task_flow()},
                {"name": "Links", "config": {"links": {"project": {"schema": "Project"}, "owner": {"schema": "Person"}}}}
            ])),
        );
        let prefix = "type Project: behavior Rollups config: ";
        let refused_with = |rollups: Value, workflow: bool| -> String {
            let error = refusal(define(&engine, &projects_with(rollups, workflow), &alice()));
            let issues = issues_of(&error);
            let at = if workflow {
                "/types/Project/behaviors/1/config"
            } else {
                "/types/Project/behaviors/0/config"
            };
            assert_eq!(issues.len(), 1, "{error}");
            assert_eq!(issues[0].0, at, "{error}");
            issues[0]
                .1
                .strip_prefix(prefix)
                .unwrap_or_else(|| panic!("{}", issues[0].1))
                .to_owned()
        };
        let refused = |rollups: Value| refused_with(rollups, true);
        assert_eq!(
            refused(json!({"finished": rollup("all", json!({"gatedStates": ["shipped"]}))})),
            "rollup finished: gated state \"shipped\" is not a state of the type's Workflow (active, done, dropped)"
        );
        assert_eq!(
            refused_with(
                json!({"finished": rollup("all", json!({"gatedStates": ["done"]}))}),
                false
            ),
            "rollup finished gates states of the type's Workflow, which the type does not compose"
        );
        let cases = [
            (
                json!({"tasks": {"schema": "Ghost", "link": "project", "function": "count"}}),
                "rollup tasks: schema Ghost has no live version; publish it first",
            ),
            (
                json!({"tasks": {"schema": "Note", "link": "project", "function": "count"}}),
                "rollup tasks: Note does not compose Links, so none of its instances points at Project",
            ),
            (
                json!({"tasks": rollup("count", json!({"link": "parent"}))}),
                "rollup tasks: Task has no link parent (its links: owner, project)",
            ),
            (
                json!({"tasks": rollup("count", json!({"link": "owner"}))}),
                "rollup tasks: Task's link owner points at Person, not Project",
            ),
            (
                json!({"byTitle": rollup("countBy", json!({"field": "missing"}))}),
                "rollup byTitle: Task has no field missing; countBy takes a string, enum or boolean field of its type, or Workflow.status when it composes Workflow",
            ),
            // Workflow's status goes by its qualified name: a bare status
            // names an own field, and Task has none.
            (
                json!({"byStatus": rollup("countBy", json!({"field": "status"}))}),
                "rollup byStatus: Task has no field status; countBy takes a string, enum or boolean field of its type, or Workflow.status when it composes Workflow",
            ),
            (
                json!({"byHours": rollup("countBy", json!({"field": "hours"}))}),
                "rollup byHours: countBy takes a string, enum or boolean field, and Task's hours holds an integer",
            ),
            (
                json!({"byLabel": rollup("countBy", json!({"field": "labels"}))}),
                "rollup byLabel: countBy takes a string, enum or boolean field, and Task's labels holds a list",
            ),
            (
                json!({"byStatus": {"schema": "Plain", "link": "project", "function": "countBy", "field": "Workflow.status"}}),
                "rollup byStatus: Plain has no field Workflow.status; countBy takes a string, enum or boolean field of its type, or Workflow.status when it composes Workflow",
            ),
            (
                json!({"total": rollup("sum", json!({"field": "title"}))}),
                "rollup total: sum takes a number or integer field, and Task's title holds a string",
            ),
            (
                json!({"least": rollup("min", json!({"field": "notes"}))}),
                "rollup least: min takes a number or integer field, and Task's notes holds any JSON value",
            ),
            (
                json!({"most": rollup("max", json!({"field": "Workflow.status"}))}),
                "rollup most: Task has no field Workflow.status; max takes a number or integer field of its type",
            ),
            (
                json!({"finished": {"schema": "Plain", "link": "project", "function": "all"}}),
                "rollup finished: all reads the terminal states of Plain's Workflow, which it does not compose",
            ),
            (
                json!({"last": rollup("latest", json!({"field": "missing"}))}),
                "rollup last: Task has no field missing; latest takes a field of its type, or Workflow.status when it composes Workflow",
            ),
            (
                json!({"last": {"schema": "Plain", "link": "project", "function": "latest", "field": "Workflow.status"}}),
                "rollup last: Plain has no field Workflow.status; latest takes a field of its type, or Workflow.status when it composes Workflow",
            ),
        ];
        for (rollups, message) in cases {
            assert_eq!(refused(rollups), message);
        }
        // The core meta-schema holds the config to the declaration's
        // configSchema first.
        let shape = |rollups: Value| -> Vec<String> {
            issues_of(&refusal(define(&engine, &projects(rollups), &alice())))
                .into_iter()
                .map(|(path, _)| path)
                .collect()
        };
        let at = "/types/Project/behaviors/1/config/rollups";
        assert!(shape(json!({})).contains(&at.to_owned()));
        assert!(shape(json!({"Tasks": rollup("count", json!({}))})).contains(&at.to_owned()));
        assert!(shape(json!({"total": rollup("sum", json!({}))})).contains(&format!("{at}/total")));
        assert!(shape(json!({"last": rollup("latest", json!({}))})).contains(&format!("{at}/last")));
        assert!(shape(
            json!({"last": rollup("latest", json!({"field": "kind", "gatedStates": ["done"]}))})
        )
        .iter()
        .any(|path| path.starts_with(&format!("{at}/last"))));
        assert!(
            shape(json!({"tasks": rollup("count", json!({"field": "title"}))}))
                .contains(&format!("{at}/tasks"))
        );
        assert!(
            shape(json!({"tasks": rollup("count", json!({"gatedStates": ["done"]}))}))
                .iter()
                .any(|path| path.starts_with(&format!("{at}/tasks")))
        );
        assert!(
            shape(json!({"tasks": rollup("count", json!({"outcomes": ["success"]}))}))
                .iter()
                .any(|path| path.starts_with(&format!("{at}/tasks")))
        );
        assert!(
            shape(json!({"finished": rollup("all", json!({"outcomes": []}))}))
                .contains(&format!("{at}/finished/outcomes"))
        );
        assert!(
            shape(json!({"finished": rollup("all", json!({"outcomes": ["done"]}))}))
                .contains(&format!("{at}/finished/outcomes/0"))
        );
        assert!(
            shape(json!({"mean": rollup("average", json!({"field": "estimate"}))}))
                .contains(&format!("{at}/mean/function"))
        );
        // What passes: every function, on every kind of field it takes.
        publish(&engine, &projects(every()));
        let composed: Vec<String> = engine
            .schemas()
            .behaviors(&alice(), "Project", ValidateOptions::default())
            .expect("behaviors")
            .into_iter()
            .map(|behavior| behavior.name)
            .collect();
        assert_eq!(composed, ["Workflow", "Rollups"]);
    }
}

#[test]
fn publish_checks_the_linked_schema_again_as_it_is_then() {
    for target in targets("rollups_publish") {
        let engine = open(&target, |_| {});
        publish(&engine, &tasks(task_behaviors()));
        define(
            &engine,
            &projects(json!({"tasks": rollup("count", json!({}))})),
            &alice(),
        )
        .expect("define");
        // Task has no instances, so a new version may drop Links.
        publish(
            &engine,
            &tasks(json!([{"name": "Workflow", "config": task_flow()}])),
        );
        let refused = refusal(engine.schemas().publish(
            &alice(),
            "Project",
            SchemaTarget::default(),
        ));
        let message = refused.message();
        assert!(
            message.starts_with("default/Project draft: /types/Project/behaviors/1/config: ")
                && message.contains("rollup tasks: Task does not compose Links"),
            "{message}"
        );
        assert!(engine
            .schemas()
            .live(&alice(), "Project", SchemaTarget::default())
            .expect("live")
            .is_none());
    }
}

#[test]
fn a_rollup_over_its_own_schema_reads_the_version_being_defined_for_its_own_name() {
    for target in targets("rollups_own") {
        let engine = open(&target, |_| {});
        publish(
            &engine,
            &tasks(json!([
                {"name": "Workflow", "config": task_flow()},
                {"name": "Links", "config": {"links": {"parent": {"schema": "Task"}}}},
                {"name": "Rollups", "config": {"rollups": {
                    "subtasks": {"schema": "Task", "link": "parent", "function": "count"},
                    "done": {"schema": "Task", "link": "parent", "function": "all", "gatedStates": ["done"]}
                }}}
            ])),
        );
        for id in ["t1", "t2", "t3"] {
            create(&engine, "Task", id, json!({ "title": id }));
        }
        for id in ["t2", "t3"] {
            invoke(
                &engine,
                "Task",
                id,
                "link",
                json!({"name": "parent", "id": "t1"}),
            );
        }
        let values = |id: &str| read(&engine, "Task", id).behaviors["Rollups"]["values"].clone();
        assert_eq!(values("t1"), json!({"done": false, "subtasks": 2}));
        assert_eq!(values("t2"), json!({"done": true, "subtasks": 0}));
        move_to(&engine, "Task", "t1", "doing").expect("doing");
        assert_eq!(veto(move_to(&engine, "Task", "t1", "done")).0, "Rollups");
        move_to(&engine, "Task", "t2", "dropped").expect("dropped");
        move_to(&engine, "Task", "t3", "dropped").expect("dropped");
        assert_eq!(
            move_to(&engine, "Task", "t1", "done").expect("done"),
            moved("doing", "done")
        );
    }
}

#[test]
fn a_link_that_no_longer_points_at_this_schema_holds_none_of_the_linked_schemas_instances() {
    for target in targets("rollups_repointed") {
        let engine = open(&target, |_| {});
        publish(&engine, &schema("Other", json!([])));
        publish(&engine, &tasks(task_behaviors()));
        publish(
            &engine,
            &projects(json!({
                "tasks": rollup("count", json!({})),
                "estimate": rollup("sum", json!({"field": "estimate"}))
            })),
        );
        create(&engine, "Project", "p1", json!({"title": "p1"}));
        // With no instance, Task may drop Links, then name project again for
        // another schema.
        publish(
            &engine,
            &tasks(json!([{"name": "Workflow", "config": task_flow()}])),
        );
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"estimate": 0, "tasks": 0})
        );
        publish(
            &engine,
            &tasks(json!([
                {"name": "Workflow", "config": task_flow()},
                {"name": "Links", "config": {"links": {"project": {"schema": "Other"}}}}
            ])),
        );
        create(&engine, "Other", "p1", json!({"title": "same id"}));
        task(&engine, "t1", json!({"estimate": 4}), Some("p1"));
        assert_eq!(
            read(&engine, "Task", "t1").behaviors["Links"]["targets"],
            json!({"project": {"schema": "Other", "id": "p1"}})
        );
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"estimate": 0, "tasks": 0}),
            "a task that points at Other p1 is not Project p1's"
        );
    }
}

#[test]
fn config_change_rollups_change_freely_and_rollups_comes_and_goes_on_a_schema_with_instances() {
    for target in targets("rollups_versions") {
        let engine = open(&target, |_| {});
        publish(&engine, &tasks(task_behaviors()));
        let bare = schema(
            "Project",
            json!([{"name": "Workflow", "config": project_flow()}]),
        );
        publish(&engine, &bare);
        create(&engine, "Project", "p1", json!({"title": "p1"}));
        task(&engine, "t1", json!({"estimate": 2}), Some("p1"));
        publish(
            &engine,
            &projects(json!({"tasks": rollup("count", json!({}))})),
        );
        assert_eq!(rollups_of(&engine, "p1"), json!({"tasks": 1}));
        publish(
            &engine,
            &projects(json!({
                "estimate": rollup("sum", json!({"field": "estimate"})),
                "finished": rollup("all", json!({"gatedStates": ["done"]}))
            })),
        );
        assert_eq!(
            rollups_of(&engine, "p1"),
            json!({"estimate": 2, "finished": false})
        );
        assert_eq!(veto(move_to(&engine, "Project", "p1", "done")).0, "Rollups");
        publish(&engine, &bare);
        let p1 = read(&engine, "Project", "p1");
        let mut behaviors = Map::new();
        behaviors.insert(
            "Workflow".into(),
            json!({"status": "active", "enteredAt": p1.created_at}),
        );
        assert_eq!(
            json!([p1.data, p1.behaviors]),
            json!([{"title": "p1"}, behaviors])
        );
        assert_eq!(
            move_to(&engine, "Project", "p1", "done").expect("done"),
            moved("active", "done")
        );
    }
}
