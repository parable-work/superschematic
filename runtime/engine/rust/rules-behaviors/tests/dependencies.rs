//! Dependencies on the pure core (D104's step 4), through the core's public
//! API over a host of several types in memory: the declaration (its
//! blockers sub-table, blocked computed at a read, listDependents left out)
//! and the config held to the file's schemas and to the table's one
//! Workflow; addBlocker's checks in the engine's words and order, the edge
//! in the sub-table; blocked and the gate over one rule, across tables, a
//! blocker finished only by an outcome satisfiedBy lists, a state finished
//! names counting (D113) and a reopened one blocking again; a gate on a
//! state a transition leaves; a row past its last gate taking no open
//! blocker; cycles through any table; removeBlocker; listBlockers' pages
//! and cursor; a gone blocker neither blocking nor listed; a create's
//! blockers held to the same checks at pointers into its parameters; and
//! the config change.

mod support;
mod tables;

use serde_json::{json, Value};
use superschematic_rules::{Action, Caller, Command, FieldSpec, Outcome, TypeSpec, UseSpec};
use superschematic_rules_behaviors::{core, dependencies};
use support::{changed, define, refused, result, rule, shape, undescribed, veto_of};
use tables::{Host, Tables};

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
        ]
    })
}

fn task(config: Value) -> TypeSpec {
    TypeSpec::new("Task")
        .with(UseSpec::on_type("Workflow", task_flow()))
        .with(UseSpec::on_type("Dependencies", config))
        .with_field(FieldSpec::new("title", "string"))
}

fn milestone() -> TypeSpec {
    TypeSpec::new("Milestone")
        .with(UseSpec::on_type(
            "Workflow",
            json!({"states": ["active", "shipped"], "transitions": [{"from": "active", "to": "shipped"}]}),
        ))
        .with_field(FieldSpec::new("title", "string"))
}

fn check() -> TypeSpec {
    TypeSpec::new("Check").with(UseSpec::on_type(
        "Workflow",
        json!({
            "states": ["running", "passed", "failed", "skipped"],
            "transitions": [{"from": "running", "to": "passed"}, {"from": "running", "to": "failed"}, {"from": "running", "to": "skipped"}],
            "outcomes": {"failed": "failure", "skipped": "neutral"}
        }),
    ))
}

fn note() -> TypeSpec {
    TypeSpec::new("Note").with_field(FieldSpec::new("title", "string"))
}

fn host(config: Value) -> Host {
    let mut host = Host::new(core(), [task(config), milestone(), check(), note()]);
    for key in ["t1", "t2", "t3"] {
        changed(host.create("Task", &alice(), T0, key, json!({"title": key})));
    }
    changed(host.create("Milestone", &alice(), T0, "m1", json!({"title": "Launch"})));
    changed(host.create("Check", &alice(), T0, "c1", json!({})));
    changed(host.create("Note", &alice(), T0, "n1", json!({"title": "Aside"})));
    host
}

fn every() -> Value {
    json!({"schemas": ["Task", "Milestone", "Check", "Note"], "gatedStates": ["done"]})
}

fn op(host: &mut Host, key: &str, name: &str, params: Value) -> Outcome {
    host.invoke(
        "Task",
        &alice(),
        T0 + 1000,
        key,
        "Dependencies",
        name,
        params,
    )
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

fn blocked(host: &mut Host, key: &str) -> Value {
    let read = changed(host.run(
        "Task",
        &alice(),
        T0 + 2000,
        Action::Read {
            key: key.to_owned(),
        },
    ));
    read.result["Dependencies"][dependencies::BLOCKED].clone()
}

fn message(outcome: Outcome) -> String {
    refused(outcome)["message"]
        .as_str()
        .unwrap_or_default()
        .to_owned()
}

/// The declaration keeps the edges in a sub-table keyed by number,
/// computes blocked at a read, and leaves listDependents to the engine; the
/// checks written by hand are the file's schemas.
#[test]
fn the_declaration_and_the_checks_are_the_files() {
    let core = core();
    let declaration = core.declaration("Dependencies").expect("registered");
    assert_eq!(declaration.computed, ["blocked"]);
    let names: Vec<&str> = declaration
        .operations
        .iter()
        .map(|operation| operation.name.as_str())
        .collect();
    assert_eq!(names, ["addBlocker", "removeBlocker", "listBlockers"]);
    assert_eq!(declaration.tables[0].name, "blockers");
    assert_eq!(declaration.tables[0].key.as_deref(), Some("number"));
    assert_eq!(
        declaration.tables[0].fields,
        ["number", "blockerType", "blockerKey", "addedBy", "addedAt"]
    );
    assert!(declaration.create_params);
    assert_eq!(
        declaration.vetoes,
        ["blocked", "already_blocking", "cycle", "gated"]
    );
    let file: Value = serde_json::from_str(dependencies::DECLARATION).expect("it parses");
    let list =
        |items: Value| json!({"type": "array", "minItems": 1, "uniqueItems": true, "items": items});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "schemas": list(json!({"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_-]{0,127}$"})),
                "gatedStates": list(json!({"type": "string", "minLength": 1})),
                "satisfiedBy": list(json!({"enum": ["success", "failure", "neutral"]}))
            }
        })
    );
    let blocker = json!({
        "type": "object",
        "additionalProperties": false,
        "required": ["id"],
        "properties": {
            "schema": {"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_-]{0,127}$"},
            "id": {"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"}
        }
    });
    assert_eq!(
        undescribed(&file["createParamsSchema"]),
        json!({"type": "object", "additionalProperties": false, "properties": {"blockers": {"type": "array", "maxItems": 500, "items": blocker}}})
    );
    assert_eq!(undescribed(&file["operations"][0]["paramsSchema"]), blocker);
    assert_eq!(
        undescribed(&file["operations"][2]["paramsSchema"]),
        json!({"type": "object", "additionalProperties": false, "properties": {"limit": {"type": "integer", "minimum": 1, "maximum": 500}, "cursor": {"type": "string"}}})
    );
}

/// The config's shape in the schema loader's words, its gated states held
/// to the table's one Workflow, and the defaults.
#[test]
fn a_config_is_held_to_its_schema_and_the_tables_workflow() {
    let core = core();
    assert_eq!(
        shape(
            &core,
            &task(json!({"satisfiedBy": ["success", "success"], "extra": 1, "schemas": []}))
        ),
        [
            "/config: unknown key \"extra\"",
            "/config/satisfiedBy: must NOT have duplicate items (items ## 0 and 1 are identical)",
            "/config/schemas: must NOT have fewer than 1 items"
        ]
    );
    assert_eq!(
        rule(&core, &task(json!({"gatedStates": ["shipped"]})), "Dependencies"),
        "gated state \"shipped\" is not a state of the type's Workflow (todo, doing, done, dropped)"
    );
    let bare = TypeSpec::new("Task").with(UseSpec::on_type("Dependencies", json!({})));
    assert_eq!(
        rule(&core, &bare, "Dependencies"),
        "it gates the type's Workflow, which the type does not list"
    );
    assert!(define(&core, &task(json!({}))).is_ok());
    // The gated states default to the terminal ones: done and dropped.
    let mut host = host(json!({}));
    changed(op(&mut host, "t1", "addBlocker", json!({"id": "t2"})));
    changed(mv(&mut host, "Task", "t1", "doing"));
    assert_eq!(veto_of(mv(&mut host, "Task", "t1", "dropped")).0, "blocked");
}

/// addBlocker's checks, in the engine's words and order: itself, a table
/// the config does not list, one without Workflow, a row that does not
/// exist, the pattern, one already there, a cycle.
#[test]
fn add_blocker_refuses_in_the_engines_words() {
    let mut host = host(json!({"schemas": ["Task", "Note"]}));
    assert_eq!(
        message(op(&mut host, "t1", "addBlocker", json!({"id": "t1"}))),
        "operation addBlocker of behavior Dependencies: /id: Task t1 cannot block itself"
    );
    assert_eq!(
        message(op(&mut host, "t1", "addBlocker", json!({"schema": "Milestone", "id": "m1"}))),
        "operation addBlocker of behavior Dependencies: /schema: a blocker of Task is an instance of Task, Note, not Milestone"
    );
    assert_eq!(
        message(op(&mut host, "t1", "addBlocker", json!({"schema": "Note", "id": "n1"}))),
        "operation addBlocker of behavior Dependencies: /schema: Note does not compose Workflow, so its instances cannot block"
    );
    assert_eq!(
        message(op(&mut host, "t1", "addBlocker", json!({"id": "t9"}))),
        "operation addBlocker of behavior Dependencies: /id: Task t9 does not exist"
    );
    assert_eq!(
        message(op(&mut host, "t1", "addBlocker", json!({"id": "bad id", "open": true}))),
        "operation addBlocker of behavior Dependencies: must NOT have additional properties: open; /id: must match pattern \"^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$\""
    );
    assert_eq!(
        result(op(&mut host, "t1", "addBlocker", json!({"id": "t2"}))),
        json!({"schema": "Task", "id": "t2", "status": "todo", "open": true})
    );
    let edge = host.tables.table("Task", "t1", "Dependencies", "blockers");
    assert_eq!(edge.len(), 1);
    assert_eq!(edge[0].key, "0000000001");
    assert_eq!(
        Value::Object(edge[0].values.clone()),
        json!({"blockerType": "Task", "blockerKey": "t2", "addedBy": "alice", "addedAt": T0 + 1000})
    );
    assert_eq!(
        message(op(
            &mut host,
            "t1",
            "addBlocker",
            json!({"schema": "Task", "id": "t2"})
        )),
        "behavior Dependencies vetoes addBlocker of Task t1: Task t2 already blocks it"
    );
    changed(op(&mut host, "t2", "addBlocker", json!({"id": "t3"})));
    let (code, _) = veto_of(op(&mut host, "t3", "addBlocker", json!({"id": "t1"})));
    assert_eq!(code, "cycle");
    assert_eq!(
        message(op(&mut host, "t3", "addBlocker", json!({"id": "t1"}))),
        "behavior Dependencies vetoes addBlocker of Task t3: Task t1 is blocked by Task t3, directly or through others: the edge would close a cycle"
    );
}

/// blocked and the gate are one rule over every blocker, across tables;
/// the gate holds a transition into a gated state, whoever asks, and lets
/// others through.
#[test]
fn blocked_and_the_gate_read_every_blocker_across_tables() {
    let mut host = host(every());
    assert_eq!(blocked(&mut host, "t1"), json!(false));
    changed(op(&mut host, "t1", "addBlocker", json!({"id": "t2"})));
    changed(op(
        &mut host,
        "t1",
        "addBlocker",
        json!({"schema": "Milestone", "id": "m1"}),
    ));
    assert_eq!(blocked(&mut host, "t1"), json!(true));
    changed(mv(&mut host, "Task", "t1", "doing"));
    let refusal = refused(mv(&mut host, "Task", "t1", "done"));
    assert_eq!(
        refusal["message"],
        "behavior Dependencies vetoes transition of Task t1: Task t1 cannot move to done while it is blocked by Task t2 (todo), Milestone m1 (active)"
    );
    assert_eq!(
        refusal["details"]["vetoDetails"],
        json!({"blockers": [{"schema": "Task", "id": "t2", "status": "todo"}, {"schema": "Milestone", "id": "m1", "status": "active"}]})
    );
    changed(mv(&mut host, "Task", "t2", "doing"));
    changed(mv(&mut host, "Task", "t2", "done"));
    changed(mv(&mut host, "Milestone", "m1", "shipped"));
    assert_eq!(blocked(&mut host, "t1"), json!(false));
    changed(mv(&mut host, "Task", "t1", "done"));
}

/// A blocker is finished only in a terminal state whose outcome satisfiedBy
/// lists, success by default: a failed check blocks until it is removed.
#[test]
fn a_blocker_finishes_by_an_outcome_satisfied_by_lists() {
    let mut host = host(every());
    changed(op(
        &mut host,
        "t1",
        "addBlocker",
        json!({"schema": "Check", "id": "c1"}),
    ));
    changed(mv(&mut host, "Check", "c1", "failed"));
    assert_eq!(blocked(&mut host, "t1"), json!(true));
    changed(mv(&mut host, "Task", "t1", "doing"));
    assert_eq!(
        message(mv(&mut host, "Task", "t1", "done")),
        "behavior Dependencies vetoes transition of Task t1: Task t1 cannot move to done while it is blocked by Check c1 (failed)"
    );
    assert_eq!(
        result(op(
            &mut host,
            "t1",
            "removeBlocker",
            json!({"schema": "Check", "id": "c1"})
        )),
        json!({"schema": "Check", "id": "c1"})
    );
    assert_eq!(blocked(&mut host, "t1"), json!(false));
    let mut lenient = host_of(json!({"schemas": ["Check"], "satisfiedBy": ["success", "failure"]}));
    changed(op(
        &mut lenient,
        "t1",
        "addBlocker",
        json!({"schema": "Check", "id": "c1"}),
    ));
    changed(mv(&mut lenient, "Check", "c1", "failed"));
    assert_eq!(blocked(&mut lenient, "t1"), json!(false));
}

fn host_of(config: Value) -> Host {
    host(config)
}

/// A state the blocker's Workflow names in finished counts (D113), and a
/// blocker reopened from it blocks again, the dependent staying where its
/// gate let it go.
#[test]
fn a_finished_blocker_reopened_blocks_again() {
    let reopenable = TypeSpec::new("Step").with(UseSpec::on_type(
        "Workflow",
        json!({
            "states": ["todo", "done"],
            "transitions": [{"from": "todo", "to": "done"}, {"from": "done", "to": "todo"}],
            "finished": {"done": "success"}
        }),
    ));
    let mut host = Host::new(
        core(),
        [
            task(json!({"schemas": ["Step"], "gatedStates": ["doing", "done"]})),
            reopenable,
        ],
    );
    changed(host.create("Task", &alice(), T0, "t1", json!({})));
    changed(host.create("Step", &alice(), T0, "s1", json!({})));
    changed(op(
        &mut host,
        "t1",
        "addBlocker",
        json!({"schema": "Step", "id": "s1"}),
    ));
    changed(mv(&mut host, "Step", "s1", "done"));
    changed(mv(&mut host, "Task", "t1", "doing"));
    changed(mv(&mut host, "Step", "s1", "todo"));
    assert_eq!(blocked(&mut host, "t1"), json!(true));
    assert_eq!(veto_of(mv(&mut host, "Task", "t1", "done")).0, "blocked");
}

/// A row in a gated state no transition leaves takes no open blocker, and
/// takes a finished one; one in a gated state a transition leaves takes an
/// open one, which holds its next gated move.
#[test]
fn a_row_past_its_last_gate_takes_no_open_blocker() {
    let mut host = host(every());
    changed(mv(&mut host, "Task", "t1", "doing"));
    changed(mv(&mut host, "Task", "t1", "done"));
    assert_eq!(
        message(op(&mut host, "t1", "addBlocker", json!({"id": "t2"}))),
        "behavior Dependencies vetoes addBlocker of Task t1: it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Task t2 (todo)"
    );
    changed(mv(&mut host, "Task", "t3", "dropped"));
    assert_eq!(
        result(op(&mut host, "t1", "addBlocker", json!({"id": "t3"}))),
        json!({"schema": "Task", "id": "t3", "status": "dropped", "open": false})
    );
    let mut started = host_of(json!({"gatedStates": ["doing", "done"]}));
    changed(mv(&mut started, "Task", "t1", "doing"));
    changed(op(&mut started, "t1", "addBlocker", json!({"id": "t2"})));
    assert_eq!(veto_of(mv(&mut started, "Task", "t1", "done")).0, "blocked");
}

/// listBlockers pages the blockers in the order they were added, with each
/// one's state, its cursor its own; a cursor it did not write is refused;
/// removeBlocker of one that is no edge is refused.
#[test]
fn list_blockers_pages_and_remove_blocker_takes_one() {
    let mut host = host(every());
    changed(op(
        &mut host,
        "t1",
        "addBlocker",
        json!({"schema": "Milestone", "id": "m1"}),
    ));
    changed(op(&mut host, "t1", "addBlocker", json!({"id": "t2"})));
    changed(mv(&mut host, "Task", "t2", "dropped"));
    let first = result(op(&mut host, "t1", "listBlockers", json!({"limit": 1})));
    assert_eq!(
        first,
        json!({"items": [{"schema": "Milestone", "id": "m1", "status": "active", "open": true}], "next": "YWZ0ZXI6MQ"})
    );
    assert_eq!(
        result(op(
            &mut host,
            "t1",
            "listBlockers",
            json!({"cursor": "YWZ0ZXI6MQ"})
        )),
        json!({"items": [{"schema": "Task", "id": "t2", "status": "dropped", "open": false}], "next": null})
    );
    assert_eq!(
        message(op(&mut host, "t1", "listBlockers", json!({"cursor": "nope"}))),
        "operation listBlockers of behavior Dependencies: /cursor: is not a cursor this operation returned"
    );
    assert_eq!(
        message(op(&mut host, "t1", "removeBlocker", json!({"id": "t3"}))),
        "operation removeBlocker of behavior Dependencies: /id: Task t3 does not block Task t1"
    );
    changed(op(
        &mut host,
        "t1",
        "removeBlocker",
        json!({"schema": "Milestone", "id": "m1"}),
    ));
    assert_eq!(
        result(op(&mut host, "t1", "listBlockers", json!({}))),
        json!({"items": [{"schema": "Task", "id": "t2", "status": "dropped", "open": false}], "next": null})
    );
    // listDependents is the engine's alone.
    assert_eq!(
        refused(op(
            &mut host,
            "t2",
            dependencies::LIST_DEPENDENTS,
            json!({})
        ))["code"],
        "not_found"
    );
}

/// A blocker that is gone neither blocks nor is listed; its edge stays
/// until removeBlocker takes it, and a dependent's delete takes its edges.
#[test]
fn a_gone_blocker_neither_blocks_nor_is_listed() {
    let mut host = host(every());
    changed(op(
        &mut host,
        "t1",
        "addBlocker",
        json!({"schema": "Milestone", "id": "m1"}),
    ));
    assert_eq!(blocked(&mut host, "t1"), json!(true));
    changed(host.delete("Milestone", &alice(), T0, "m1"));
    assert_eq!(blocked(&mut host, "t1"), json!(false));
    assert_eq!(
        result(op(&mut host, "t1", "listBlockers", json!({}))),
        json!({"items": [], "next": null})
    );
    changed(op(
        &mut host,
        "t1",
        "removeBlocker",
        json!({"schema": "Milestone", "id": "m1"}),
    ));
    changed(op(&mut host, "t2", "addBlocker", json!({"id": "t3"})));
    changed(host.delete("Task", &alice(), T0, "t2"));
    assert!(host
        .tables
        .table("Task", "t2", "Dependencies", "blockers")
        .is_empty());
}

/// A create's blockers are held to addBlocker's checks, at pointers into
/// its parameters: a param's issue a CreateParamsError, an edge's veto with
/// its entry's path; a create with blockers is blocked from its create.
#[test]
fn a_creates_blockers_are_held_to_add_blockers_checks() {
    let mut host = host(json!({"schemas": ["Task", "Milestone"]}));
    let create = |host: &mut Host, key: &str, blockers: Value| -> Outcome {
        let mut params = serde_json::Map::new();
        params.insert("Dependencies".into(), json!({ "blockers": blockers }));
        host.run(
            "Task",
            &alice(),
            T0,
            Action::Create {
                id: Some(key.to_owned()),
                own: serde_json::Map::new(),
                params,
            },
        )
    };
    let error = refused(create(
        &mut host,
        "t4",
        json!([{"id": "t1"}, {"schema": "Note", "id": "n1"}]),
    ));
    assert_eq!(error["class"], "CreateParamsError");
    assert_eq!(
        error["details"]["issues"],
        json!([{"path": "/Dependencies/blockers/1/schema", "message": "a blocker of Task is an instance of Task, Milestone, not Note"}])
    );
    let error = refused(create(&mut host, "t4", json!([{"id": "t4"}])));
    assert_eq!(
        error["details"]["issues"][0]["message"],
        "Task t4 cannot block itself"
    );
    let error = refused(create(
        &mut host,
        "t4",
        json!([{"id": "t1"}, {"schema": "Task", "id": "t1"}]),
    ));
    assert_eq!(
        error["message"],
        "behavior Dependencies vetoes create of Task t4: Task t1 already blocks it"
    );
    assert_eq!(
        error["details"]["vetoDetails"],
        json!({"path": "/behaviors/Dependencies/blockers/1"})
    );
    assert_eq!(
        refused(create(&mut host, "t4", json!("t1")))["details"]["issues"],
        json!([{"path": "/Dependencies/blockers", "message": "must be array"}])
    );
    changed(create(
        &mut host,
        "t4",
        json!([{"id": "t1"}, {"schema": "Milestone", "id": "m1"}]),
    ));
    assert_eq!(blocked(&mut host, "t4"), json!(true));
    assert_eq!(
        host.tables
            .table("Task", "t4", "Dependencies", "blockers")
            .len(),
        2
    );
    // A create whose initial state is a gated state no transition leaves
    // takes no open blocker.
    let gate = TypeSpec::new("Gate")
        .with(UseSpec::on_type(
            "Workflow",
            json!({"states": ["done"], "transitions": []}),
        ))
        .with(UseSpec::on_type(
            "Dependencies",
            json!({"schemas": ["Task"]}),
        ));
    let mut gated = Host::new(core(), [task(json!({})), gate]);
    changed(gated.create("Task", &alice(), T0, "t1", json!({})));
    let mut params = serde_json::Map::new();
    params.insert(
        "Dependencies".into(),
        json!({"blockers": [{"schema": "Task", "id": "t1"}]}),
    );
    let error = refused(gated.run(
        "Gate",
        &alice(),
        T0,
        Action::Create {
            id: Some("g1".into()),
            own: serde_json::Map::new(),
            params,
        },
    ));
    assert_eq!(error["details"]["vetoCode"], "gated");
}

/// gatedStates, schemas and satisfiedBy may change; Dependencies joins a
/// table with rows and cannot leave one.
#[test]
fn a_config_changes_and_dependencies_cannot_leave_a_table_with_rows() {
    let host = host(every());
    let publish = |from: TypeSpec, to: TypeSpec| -> Outcome {
        let command = Command::new(alice(), T0, Action::Publish { from: Some(from) });
        host.core.drive(&to, &command, &mut host_tables(&host))
    };
    changed(publish(
        host.spec("Task"),
        task(json!({"satisfiedBy": ["failure"]})),
    ));
    let without = TypeSpec::new("Task")
        .with(UseSpec::on_type("Workflow", task_flow()))
        .with_field(FieldSpec::new("title", "string"));
    let refusal = refused(publish(host.spec("Task"), without.clone()));
    assert_eq!(
        refusal["details"]["changes"][0]["message"],
        "behavior Dependencies cannot be removed from type Task, which has instances: the edges and references its instances hold would stay behind"
    );
    changed(publish(without, host.spec("Task")));
}

// host_tables is a host's rows, for a check that reads whether a type has
// rows.
fn host_tables(host: &Host) -> Tables {
    let mut tables = Tables::default();
    tables.specs = host.tables.specs.clone();
    tables.rows = host.tables.rows.clone();
    tables
}
