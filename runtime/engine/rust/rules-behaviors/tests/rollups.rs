//! Rollups on the pure core (D104's step 4), through the core's public API
//! over a host of several types in memory: the declaration and the config
//! held to the file's schemas, each config rule (a gate's Workflow and its
//! states, latest waiting, a rollup over the table itself held to its own
//! relation); every function over the rows that point at a row through a
//! relation, and only those, computed at a read with nothing written; all
//! and any with outcomes, a state Workflow's finished names counting (D113);
//! the gate, whoever asks, an any gate, a table whose relation no longer
//! points here holding none, every linked page read, the read bound; a rollup
//! over the table itself (a task done as its subtasks are); and the config
//! change.

mod support;
mod tables;

use serde_json::{json, Map, Value};
use superschematic_rules::{Action, Caller, FieldSpec, Outcome, TypeSpec, UseSpec, LINKED_PAGE};
use superschematic_rules_behaviors::{core, rollups};
use support::{changed, define, refused, result, rule, shape, undescribed, veto_of};
use tables::Host;

const T0: i64 = 1_772_323_200_000;

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn task_flow() -> Value {
    json!({
        "states": ["todo", "doing", "done", "dropped"],
        "transitions": [
            {"from": "todo", "to": "doing"},
            {"from": "doing", "to": "done"},
            {"from": "todo", "to": "dropped"},
            {"from": "doing", "to": "dropped"}
        ],
        "outcomes": {"dropped": "failure"}
    })
}

fn project_flow() -> Value {
    json!({
        "states": ["active", "done", "dropped"],
        "transitions": [{"from": "active", "to": "done"}, {"from": "active", "to": "dropped"}]
    })
}

// A task points at its project through `project` (projectId), and at a
// person through `owner`.
fn task() -> TypeSpec {
    TypeSpec::new("Task")
        .with(UseSpec::on_type("Workflow", task_flow()))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("estimate", "number"))
        .with_field(FieldSpec::new("kind", "string"))
        .with_field(FieldSpec::new("urgent", "boolean"))
        .with_field(FieldSpec::relation(
            "projectId",
            "Identity.UUID",
            "project",
            "Project",
        ))
        .with_field(FieldSpec::relation(
            "ownerId",
            "Identity.UUID",
            "owner",
            "Person",
        ))
}

fn project(rollups: Value) -> TypeSpec {
    TypeSpec::new("Project")
        .with(UseSpec::on_type("Workflow", project_flow()))
        .with(UseSpec::on_type("Rollups", json!({ "rollups": rollups })))
        .with_field(FieldSpec::new("title", "string"))
}

fn every_function() -> Value {
    json!({
        "tasks": {"schema": "Task", "link": "project", "function": "count"},
        "byStatus": {"schema": "Task", "link": "project", "function": "countBy", "field": "Workflow.status"},
        "byKind": {"schema": "Task", "link": "project", "function": "countBy", "field": "kind"},
        "byUrgency": {"schema": "Task", "link": "project", "function": "countBy", "field": "urgent"},
        "estimate": {"schema": "Task", "link": "project", "function": "sum", "field": "estimate"},
        "smallest": {"schema": "Task", "link": "project", "function": "min", "field": "estimate"},
        "largest": {"schema": "Task", "link": "project", "function": "max", "field": "estimate"},
        "finished": {"schema": "Task", "link": "project", "function": "all", "gatedStates": ["done"]},
        "anyFailed": {"schema": "Task", "link": "project", "function": "any", "outcomes": ["failure"]}
    })
}

fn host(rollups: Value) -> Host {
    let mut host = Host::new(core(), [task(), project(rollups)]);
    for key in ["p1", "p2"] {
        changed(host.create("Project", &alice(), T0, key, json!({"title": key})));
    }
    host
}

fn add_task(host: &mut Host, key: &str, own: Value) {
    changed(host.create("Task", &alice(), T0, key, own));
}

fn mv(host: &mut Host, type_name: &str, key: &str, to: &str) -> Outcome {
    host.invoke(
        type_name,
        &alice(),
        T0 + 1000,
        key,
        "Workflow",
        "transition",
        json!({ "to": to }),
    )
}

// values is what a read of the row shows of Rollups.
fn values(host: &mut Host, key: &str) -> Value {
    let read = changed(host.run(
        "Project",
        &alice(),
        T0 + 2000,
        Action::Read {
            key: key.to_owned(),
        },
    ));
    assert!(read.rows.is_empty() && read.sub_rows.is_empty());
    read.result["Rollups"][rollups::VALUES].clone()
}

/// The declaration declares values, computed at a read and taking no
/// column, no operation and the veto not_held; the checks Rollups writes by
/// hand are the file's configSchema.
#[test]
fn the_declaration_and_the_config_checks_are_the_files() {
    let declared = core();
    let declaration = declared.declaration("Rollups").expect("registered");
    assert_eq!(declaration.fields, ["values"]);
    assert_eq!(declaration.computed, ["values"]);
    assert!(declaration.operations.is_empty() && declaration.tables.is_empty());
    assert_eq!(declaration.vetoes, ["not_held"]);
    let file: Value = serde_json::from_str(rollups::DECLARATION).expect("it parses");
    let schema = undescribed(&file["configSchema"]);
    let entry = &schema["properties"]["rollups"]["additionalProperties"];
    assert_eq!(schema["required"], json!(["rollups"]));
    assert_eq!(
        schema["properties"]["rollups"]["propertyNames"],
        json!({"pattern": "^[a-z][A-Za-z0-9]{0,63}$"})
    );
    assert_eq!(schema["properties"]["rollups"]["minProperties"], json!(1));
    assert_eq!(entry["required"], json!(["schema", "link", "function"]));
    assert_eq!(
        entry["properties"]["function"]["enum"],
        json!(["count", "countBy", "sum", "min", "max", "latest", "all", "any"])
    );
    assert_eq!(
        entry["allOf"][0]["if"]["properties"]["function"]["enum"],
        json!(["countBy", "sum", "min", "max", "latest"])
    );
    assert_eq!(
        entry["properties"]["outcomes"]["items"],
        json!({"enum": ["success", "failure", "neutral"]})
    );
    assert_eq!(
        entry["properties"]["link"]["pattern"],
        "^[a-z][A-Za-z0-9]{0,63}$"
    );
    assert_eq!(
        entry["properties"]["schema"]["pattern"],
        "^[A-Za-z][A-Za-z0-9_-]{0,127}$"
    );
}

/// A config's shape is held to the configSchema, each issue at its path in
/// the schema loader's words, a field exactly where a function reads one
/// and gates and outcomes on all and any alone.
#[test]
fn a_config_is_held_to_its_schema() {
    let core = core();
    let of = |config: Value| {
        let spec = TypeSpec::new("Project")
            .with(UseSpec::on_type("Workflow", project_flow()))
            .with(UseSpec::on_type("Rollups", config));
        shape(&core, &spec)
    };
    assert_eq!(
        of(json!({})),
        ["/config: must have required property 'rollups'"]
    );
    assert_eq!(
        of(json!({"rollups": {}})),
        ["/config/rollups: must NOT have fewer than 1 properties"]
    );
    assert_eq!(
        of(
            json!({"rollups": {"Tasks": {"schema": "Task", "link": "project", "function": "count"}}})
        ),
        [
            "/config/rollups: must match pattern \"^[a-z][A-Za-z0-9]{0,63}$\"",
            "/config/rollups: property name must be valid"
        ]
    );
    assert_eq!(
        of(json!({"rollups": {"total": {"schema": "Task", "link": "project", "function": "sum"}}})),
        [
            "/config/rollups/total: must match \"then\" schema",
            "/config/rollups/total: must have required property 'field'"
        ]
    );
    assert_eq!(
        of(
            json!({"rollups": {"tasks": {"schema": "Task", "link": "project", "function": "count", "field": "title"}}})
        ),
        [
            "/config/rollups/tasks: must match \"else\" schema",
            "/config/rollups/tasks: must NOT be valid"
        ]
    );
    assert_eq!(
        of(
            json!({"rollups": {"tasks": {"schema": "Task", "link": "project", "function": "count", "gatedStates": ["done"]}}})
        ),
        [
            "/config/rollups/tasks: must match \"then\" schema",
            "/config/rollups/tasks/function: must be one of \"all\", \"any\""
        ]
    );
    assert_eq!(
        of(json!({"rollups": {"done": {"schema": "Task", "link": "project", "function": "all", "outcomes": ["success", "success"], "extra": 1}}})),
        [
            "/config/rollups/done: unknown key \"extra\"",
            "/config/rollups/done/outcomes: must NOT have duplicate items (items ## 0 and 1 are identical)"
        ]
    );
    assert_eq!(
        of(json!({"rollups": {"mean": {"schema": "Task", "link": "Project", "function": "mean"}}})),
        [
            "/config/rollups/mean/function: must be one of \"count\", \"countBy\", \"sum\", \"min\", \"max\", \"latest\", \"all\", \"any\"",
            "/config/rollups/mean/link: must match pattern \"^[a-z][A-Za-z0-9]{0,63}$\""
        ]
    );
}

/// A gate names states of the table's one Workflow; latest waits; a rollup
/// over the table itself names a relation of it to itself.
#[test]
fn each_config_rule_is_held_to_the_table() {
    let core = core();
    let refusal = |spec: TypeSpec| rule(&core, &spec, "Rollups");
    assert_eq!(
        refusal(project(json!({"finished": {"schema": "Task", "link": "project", "function": "all", "gatedStates": ["shipped"]}}))),
        "rollup finished: gated state \"shipped\" is not a state of the type's Workflow (active, done, dropped)"
    );
    let bare = TypeSpec::new("Project").with(UseSpec::on_type(
        "Rollups",
        json!({"rollups": {"finished": {"schema": "Task", "link": "project", "function": "all", "gatedStates": ["done"]}}}),
    ));
    assert_eq!(
        refusal(bare),
        "rollup finished gates states of the type's Workflow, which the type does not compose"
    );
    let two = TypeSpec::new("Project")
        .with(UseSpec::on_field("status", "Workflow", project_flow()))
        .with(UseSpec::on_field("review", "Workflow", project_flow()))
        .with(UseSpec::on_type(
            "Rollups",
            json!({"rollups": {"finished": {"schema": "Task", "link": "project", "function": "all", "gatedStates": ["done"]}}}),
        ));
    assert_eq!(
        refusal(two),
        "rollup finished gates states of the type's Workflow, and the type has several (status, review): Rollups gates a type's one Workflow"
    );
    let latest = refusal(project(
        json!({"last": {"schema": "Task", "link": "project", "function": "latest", "field": "kind"}}),
    ));
    assert!(latest.contains("latest waits for a later step"), "{latest}");
    let own = TypeSpec::new("Task")
        .with(UseSpec::on_type("Workflow", task_flow()))
        .with(UseSpec::on_type(
            "Rollups",
            json!({"rollups": {"subtasks": {"schema": "Task", "link": "parent", "function": "count"}}}),
        ))
        .with_field(FieldSpec::relation(
            "projectId",
            "Identity.UUID",
            "project",
            "Project",
        ));
    assert_eq!(
        refusal(own),
        "rollup subtasks: Task has no to-one relation parent to itself (its relations to itself: none)"
    );
    // A rollup over another table is the build's to hold to it.
    assert!(define(
        &core,
        &project(json!({"tasks": {"schema": "Ghost", "link": "project", "function": "count"}}))
    )
    .is_ok());
}

/// Every function over the rows that point at the row through the
/// relation, and only those: another project's tasks and a task with no
/// project are not counted; a read writes nothing.
#[test]
fn every_function_reads_the_rows_that_point_at_the_row() {
    let mut host = host(every_function());
    assert_eq!(
        values(&mut host, "p1"),
        json!({"tasks": 0, "byStatus": {}, "byKind": {}, "byUrgency": {}, "estimate": 0, "finished": true, "anyFailed": false})
    );
    add_task(
        &mut host,
        "t1",
        json!({"title": "t1", "estimate": 3, "kind": "bug", "urgent": true, "projectId": "p1"}),
    );
    add_task(
        &mut host,
        "t2",
        json!({"title": "t2", "estimate": 5.5, "kind": "feature", "urgent": false, "projectId": "p1"}),
    );
    add_task(&mut host, "t3", json!({"title": "t3", "projectId": "p1"}));
    add_task(
        &mut host,
        "t4",
        json!({"title": "t4", "estimate": 100, "kind": "bug", "projectId": "p2"}),
    );
    add_task(&mut host, "t5", json!({"title": "t5", "estimate": 7}));
    changed(mv(&mut host, "Task", "t1", "doing"));
    assert_eq!(
        values(&mut host, "p1"),
        json!({
            "tasks": 3,
            "byStatus": {"doing": 1, "todo": 2},
            "byKind": {"bug": 1, "feature": 1},
            "byUrgency": {"false": 1, "true": 1},
            "estimate": 8.5,
            "smallest": 3,
            "largest": 5.5,
            "finished": false,
            "anyFailed": false
        })
    );
    changed(mv(&mut host, "Task", "t3", "dropped"));
    assert_eq!(values(&mut host, "p1")["anyFailed"], json!(true));
    assert_eq!(values(&mut host, "p2")["tasks"], json!(1));
    assert_eq!(values(&mut host, "p2")["estimate"], json!(100));
}

/// The gate: a transition into a gated state waits for its rollup to hold,
/// vetoed not_held with the counts; other states pass; a done project holds
/// once every task is in a terminal state.
#[test]
fn a_gated_transition_waits_for_its_rollup_to_hold() {
    let mut host = host(every_function());
    add_task(&mut host, "t1", json!({"title": "t1", "projectId": "p1"}));
    add_task(&mut host, "t2", json!({"title": "t2", "projectId": "p1"}));
    let (code, details) = veto_of(mv(&mut host, "Project", "p1", "done"));
    assert_eq!(code, rollups::NOT_HELD);
    assert_eq!(
        details,
        json!({"rollup": "finished", "to": "done", "over": false, "linked": 2, "counted": 0})
    );
    assert_eq!(
        refused(mv(&mut host, "Project", "p1", "done"))["message"],
        "behavior Rollups vetoes transition of Project p1: Project p1 cannot move to done until rollup finished holds: 2 of the 2 instances of Task that point at it through project are not in a terminal state"
    );
    changed(mv(&mut host, "Task", "t1", "dropped"));
    changed(mv(&mut host, "Task", "t2", "doing"));
    changed(mv(&mut host, "Task", "t2", "done"));
    assert_eq!(
        result(mv(&mut host, "Project", "p1", "done")),
        json!({"from": "active", "to": "done"})
    );
    // A project with no tasks holds all, and dropped is not gated.
    assert_eq!(
        result(mv(&mut host, "Project", "p2", "done")),
        json!({"from": "active", "to": "done"})
    );
}

/// An any gate holds once one row is in a terminal state with a listed
/// outcome; with none it says no row points here.
#[test]
fn an_any_gate_holds_once_one_row_counts() {
    let mut host = host(json!({
        "started": {"schema": "Task", "link": "project", "function": "any", "outcomes": ["success"], "gatedStates": ["done"]}
    }));
    assert_eq!(
        refused(mv(&mut host, "Project", "p1", "done"))["details"]["reason"],
        "Project p1 cannot move to done until rollup started holds: no instance of Task points at it through project"
    );
    add_task(&mut host, "t1", json!({"title": "t1", "projectId": "p1"}));
    changed(mv(&mut host, "Task", "t1", "dropped"));
    assert_eq!(
        refused(mv(&mut host, "Project", "p1", "done"))["details"]["reason"],
        "Project p1 cannot move to done until rollup started holds: none of the 1 instances of Task that point at it through project is in a terminal state whose outcome is success"
    );
    add_task(&mut host, "t2", json!({"title": "t2", "projectId": "p1"}));
    changed(mv(&mut host, "Task", "t2", "doing"));
    changed(mv(&mut host, "Task", "t2", "done"));
    changed(mv(&mut host, "Project", "p1", "done"));
}

/// A state the linking table's Workflow names in finished counts as a
/// terminal state does (D113), so a task done that may be reopened closes
/// its project, and a reopened one holds it again.
#[test]
fn a_finished_state_counts_as_a_terminal_one() {
    let reopenable = TypeSpec::new("Task")
        .with(UseSpec::on_type(
            "Workflow",
            json!({
                "states": ["todo", "done"],
                "transitions": [{"from": "todo", "to": "done"}, {"from": "done", "to": "todo"}],
                "finished": {"done": "success"}
            }),
        ))
        .with_field(FieldSpec::relation(
            "projectId",
            "Identity.UUID",
            "project",
            "Project",
        ));
    let mut host = Host::new(
        core(),
        [
            reopenable,
            project(
                json!({"finished": {"schema": "Task", "link": "project", "function": "all", "gatedStates": ["done"]}}),
            ),
        ],
    );
    changed(host.create("Project", &alice(), T0, "p1", json!({"title": "p1"})));
    changed(host.create("Task", &alice(), T0, "t1", json!({"projectId": "p1"})));
    changed(mv(&mut host, "Task", "t1", "done"));
    assert_eq!(values(&mut host, "p1"), json!({"finished": true}));
    changed(mv(&mut host, "Task", "t1", "todo"));
    assert_eq!(values(&mut host, "p1"), json!({"finished": false}));
    assert_eq!(
        veto_of(mv(&mut host, "Project", "p1", "done")).0,
        "not_held"
    );
}

/// A table whose relation no longer points at this table, and one no spec
/// names, holds none of its rows.
#[test]
fn a_relation_that_points_elsewhere_holds_none() {
    let mut host = host(json!({
        "owned": {"schema": "Task", "link": "owner", "function": "count"},
        "ghosts": {"schema": "Ghost", "link": "project", "function": "count"}
    }));
    add_task(&mut host, "t1", json!({"projectId": "p1", "ownerId": "p1"}));
    assert_eq!(values(&mut host, "p1"), json!({"owned": 0, "ghosts": 0}));
}

/// A rollup reads every linked page: past the first one it still counts
/// every row, and its gate holds once every row is in a terminal state.
#[test]
fn a_rollup_reads_every_linked_page() {
    let mut host = host(json!({
        "tasks": {"schema": "Task", "link": "project", "function": "count"},
        "byStatus": {"schema": "Task", "link": "project", "function": "countBy", "field": "Workflow.status"},
        "finished": {"schema": "Task", "link": "project", "function": "all", "gatedStates": ["done"]}
    }));
    let rows = 2 * LINKED_PAGE + 1;
    for n in 0..rows {
        add_task(&mut host, &format!("t{n:05}"), json!({"projectId": "p1"}));
    }
    assert_eq!(
        values(&mut host, "p1"),
        json!({"tasks": rows, "byStatus": {"todo": rows}, "finished": false})
    );
    // The last row is on the third page: the gate waits for it too.
    for n in 0..rows - 1 {
        changed(mv(&mut host, "Task", &format!("t{n:05}"), "dropped"));
    }
    let (code, _) = veto_of(mv(&mut host, "Project", "p1", "done"));
    assert_eq!(code, "not_held");
    changed(mv(
        &mut host,
        "Task",
        &format!("t{:05}", rows - 1),
        "dropped",
    ));
    assert_eq!(
        values(&mut host, "p1"),
        json!({"tasks": rows, "byStatus": {"dropped": rows}, "finished": true})
    );
    changed(mv(&mut host, "Project", "p1", "done"));
}

/// A rollup reads at most MAX_ROLLUP_READ rows: past them the rollup is
/// {"over": true} and its gate does not hold.
#[test]
fn past_the_bound_a_rollup_has_no_value() {
    let mut host = host(json!({
        "tasks": {"schema": "Task", "link": "project", "function": "count"},
        "finished": {"schema": "Task", "link": "project", "function": "all", "gatedStates": ["done"]}
    }));
    for n in 0..=rollups::MAX_ROLLUP_READ {
        add_task(&mut host, &format!("t{n:05}"), json!({"projectId": "p1"}));
    }
    assert_eq!(
        values(&mut host, "p1"),
        json!({"tasks": {"over": true}, "finished": {"over": true}})
    );
    let (code, details) = veto_of(mv(&mut host, "Project", "p1", "done"));
    assert_eq!(code, "not_held");
    assert_eq!(
        details,
        json!({"rollup": "finished", "to": "done", "over": true})
    );
}

/// A rollup over the table itself through its own relation: a task is done
/// only once its subtasks are.
#[test]
fn a_rollup_over_the_table_itself_reads_its_children() {
    let spec = TypeSpec::new("Task")
        .with(UseSpec::on_type("Workflow", task_flow()))
        .with(UseSpec::on_type(
            "Rollups",
            json!({"rollups": {
                "subtasks": {"schema": "Task", "link": "parent", "function": "count"},
                "done": {"schema": "Task", "link": "parent", "function": "all", "gatedStates": ["done"]}
            }}),
        ))
        .with_field(FieldSpec::relation(
            "parentId",
            "Identity.UUID",
            "parent",
            "Task",
        ));
    let mut host = Host::new(core(), [spec]);
    for (key, parent) in [("t1", None), ("t2", Some("t1")), ("t3", Some("t1"))] {
        let own = match parent {
            Some(parent) => json!({ "parentId": parent }),
            None => json!({}),
        };
        changed(host.create("Task", &alice(), T0, key, own));
    }
    changed(mv(&mut host, "Task", "t1", "doing"));
    assert_eq!(veto_of(mv(&mut host, "Task", "t1", "done")).0, "not_held");
    changed(mv(&mut host, "Task", "t2", "dropped"));
    changed(mv(&mut host, "Task", "t3", "dropped"));
    changed(mv(&mut host, "Task", "t1", "done"));
    let read = changed(host.run("Task", &alice(), T0, Action::Read { key: "t1".into() }));
    assert_eq!(
        read.result,
        json!({"Rollups": {"values": {"subtasks": 2, "done": true}}})
    );
}

/// Nothing is stored: a config changes freely, and Rollups joins and leaves
/// a table with rows.
#[test]
fn a_config_changes_freely_and_rollups_comes_and_goes() {
    let mut host = host(every_function());
    add_task(&mut host, "t1", json!({"projectId": "p1"}));
    let publish = |host: &Host, from: TypeSpec, to: TypeSpec| -> Outcome {
        let command =
            superschematic_rules::Command::new(alice(), T0, Action::Publish { from: Some(from) });
        host.core
            .drive(&to, &command, &mut tables::Tables::default())
    };
    let live = host.spec("Project");
    let narrowed =
        project(json!({"tasks": {"schema": "Task", "link": "project", "function": "count"}}));
    changed(publish(&host, live.clone(), narrowed));
    let without = TypeSpec::new("Project")
        .with(UseSpec::on_type("Workflow", project_flow()))
        .with_field(FieldSpec::new("title", "string"));
    changed(publish(&host, live, without.clone()));
    changed(publish(&host, without, host.spec("Project")));
    let _ = Map::<String, Value>::new();
}
