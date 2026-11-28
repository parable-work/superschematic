//! A get and a list show the fields each use computes at a read (D103,
//! D104's step 4): Rollups' values over the rows that point at the row,
//! Dependencies' blocked over its blockers, through the core's read
//! command, and Revisions' pendingProposals over its own sub-table, each
//! named as the use's other fields are. The schemas are the recorded
//! scenarios' (`core-rollups`, `core-dependencies`, `core-revisions`).

mod common;

use common::{admin, object, Host};
use serde_json::{json, Map, Value};
use superschematic_rules::Caller;
use superschematic_rules_handler::Request;

fn workflow(states: &[&str], transitions: &[(&str, &str)]) -> Value {
    let transitions: Vec<Value> = transitions
        .iter()
        .map(|(from, to)| json!({"from": from, "to": to}))
        .collect();
    json!({"name": "Workflow", "config": {"states": states, "transitions": transitions}})
}

fn task_flow() -> Value {
    workflow(
        &["todo", "doing", "done", "dropped"],
        &[
            ("todo", "doing"),
            ("doing", "done"),
            ("todo", "dropped"),
            ("doing", "dropped"),
        ],
    )
}

fn operation(type_name: &str, id: &str, use_id: &str, name: &str, params: Value) -> Request {
    Request::Operation {
        type_name: type_name.into(),
        id: id.into(),
        use_id: use_id.into(),
        operation: name.into(),
        params,
        preconditions: Map::new(),
    }
}

fn list(host: &mut Host, type_name: &str) -> Vec<Value> {
    let page = host.ok(&Request::List {
        type_name: type_name.into(),
        filter: Value::Null,
        sort: Vec::new(),
        limit: None,
        cursor: None,
    });
    page["items"].as_array().cloned().unwrap_or_default()
}

#[test]
fn a_read_shows_rollups_values_over_the_rows_that_point_at_it() {
    let mut host = Host::new();
    host.publish(json!({"name": "Project",
        "fields": [{"name": "name", "typeRef": {"name": "string"}}],
        "behaviors": [{"name": "Rollups", "config": {"rollups": {
            "tasks": {"schema": "Task", "link": "project", "function": "count"},
            "estimate": {"schema": "Task", "link": "project", "function": "sum", "field": "estimate"},
            "byStatus": {"schema": "Task", "link": "project", "function": "countBy", "field": "Workflow.status"}
        }}}]}));
    host.publish(json!({"name": "Task",
        "fields": [
            {"name": "title", "typeRef": {"name": "string"}},
            {"name": "estimate", "typeRef": {"name": "number"}},
            {"name": "project", "typeRef": {"name": "Project"}, "relation": {"type": "Project"}}
        ],
        "behaviors": [task_flow()]}));
    host.create("Project", json!({"id": "p1", "name": "Launch"}))
        .expect("p1");
    host.create("Project", json!({"id": "p2", "name": "Idle"}))
        .expect("p2");
    for (id, estimate) in [("t1", 2), ("t2", 3)] {
        host.create(
            "Task",
            json!({"id": id, "title": id, "estimate": estimate, "projectId": "p1"}),
        )
        .expect("a task");
    }
    host.ok(&operation(
        "Task",
        "t2",
        "status",
        "transition",
        json!({"to": "doing"}),
    ));

    let described = host.ok(&Request::Describe {
        type_name: "Project".into(),
    });
    assert_eq!(
        described["computed"],
        json!([{"name": "rollupsValues", "use": "Rollups", "field": "values"}])
    );
    let p1 = host.get("Project", "p1").expect("read");
    assert_eq!(
        p1["rollupsValues"],
        json!({"tasks": 2, "estimate": 5, "byStatus": {"todo": 1, "doing": 1}}),
        "{p1}"
    );
    // The computed field comes before the version, as a response writes it.
    let names: Vec<&String> = p1.as_object().expect("an object").keys().collect();
    assert_eq!(names.last().map(|name| name.as_str()), Some("_version"));

    // A list computes each row's, the one no task points at among them.
    let projects = list(&mut host, "Project");
    let values: Vec<(&str, &Value)> = projects
        .iter()
        .map(|row| (row["id"].as_str().unwrap_or(""), &row["rollupsValues"]))
        .collect();
    assert_eq!(values[0].0, "p1");
    assert_eq!(values[0].1["tasks"], 2);
    assert_eq!(values[1].0, "p2");
    assert_eq!(values[1].1["tasks"], 0);

    // A delete of a task moves the value at the next read.
    host.run(&Request::Delete {
        type_name: "Task".into(),
        id: "t1".into(),
        preconditions: Map::new(),
    })
    .expect("deleted");
    let p1 = host.get("Project", "p1").expect("read");
    assert_eq!(p1["rollupsValues"]["tasks"], 1);
    assert_eq!(p1["rollupsValues"]["estimate"], 3);
}

#[test]
fn a_read_shows_whether_dependencies_blockers_are_open() {
    let mut host = Host::new();
    host.publish(json!({"name": "Milestone",
        "fields": [{"name": "title", "typeRef": {"name": "string"}}],
        "behaviors": [workflow(&["active", "shipped"], &[("active", "shipped")])]}));
    host.publish(json!({"name": "Task",
        "fields": [{"name": "title", "typeRef": {"name": "string"}}],
        "behaviors": [task_flow(), {"name": "Dependencies",
            "config": {"schemas": ["Task", "Milestone"], "gatedStates": ["done"]}}]}));
    for id in ["t1", "t2"] {
        host.create("Task", json!({"id": id, "title": id}))
            .expect("a task");
    }
    host.create("Milestone", json!({"id": "m1", "title": "Launch"}))
        .expect("m1");
    let t1 = host.get("Task", "t1").expect("read");
    assert_eq!(t1["dependenciesBlocked"], false, "{t1}");
    host.ok(&operation(
        "Task",
        "t1",
        "Dependencies",
        "addBlocker",
        json!({"id": "t2"}),
    ));
    host.ok(&operation(
        "Task",
        "t1",
        "Dependencies",
        "addBlocker",
        json!({"schema": "Milestone", "id": "m1"}),
    ));
    assert_eq!(
        host.get("Task", "t1").expect("read")["dependenciesBlocked"],
        true
    );
    // Both blockers finish, and a list shows it.
    for to in ["doing", "done"] {
        host.ok(&operation(
            "Task",
            "t2",
            "status",
            "transition",
            json!({ "to": to }),
        ));
    }
    let tasks = list(&mut host, "Task");
    assert_eq!(tasks[0]["dependenciesBlocked"], true, "m1 is still active");
    assert_eq!(tasks[1]["dependenciesBlocked"], false);
    host.ok(&operation(
        "Milestone",
        "m1",
        "status",
        "transition",
        json!({"to": "shipped"}),
    ));
    let tasks = list(&mut host, "Task");
    assert_eq!(tasks[0]["dependenciesBlocked"], false);
}

#[test]
fn a_read_counts_revisions_pending_proposals_from_its_sub_table() {
    let mut host = Host::new();
    host.publish(json!({"name": "Document",
        "fields": [
            {"name": "title", "typeRef": {"name": "string"}},
            {"name": "body", "typeRef": {"name": "string"}}
        ],
        "behaviors": [{"name": "Revisions", "config": {"review": {"permission": "documents.review"}}}]}));
    host.create("Document", json!({"id": "d1", "title": "Guide"}))
        .expect("d1");
    host.create("Document", json!({"id": "d2", "title": "Notes"}))
        .expect("d2");
    let d1 = host.get("Document", "d1").expect("read");
    assert_eq!(d1["revisionsPendingProposals"], 0, "{d1}");
    assert_eq!(d1["revisionsRevision"], 1, "an owned field beside it");
    for body in ["One.", "Two."] {
        host.ok(&operation(
            "Document",
            "d1",
            "Revisions",
            "propose",
            json!({"patch": {"body": body}}),
        ));
    }
    assert_eq!(
        host.get("Document", "d1").expect("read")["revisionsPendingProposals"],
        2
    );
    let reviewer = Caller::new("rita", ["documents"]);
    host.as_(
        &reviewer,
        &operation(
            "Document",
            "d1",
            "Revisions",
            "approve",
            json!({"proposal": 1}),
        ),
    )
    .expect("approved");
    let documents = list(&mut host, "Document");
    assert_eq!(documents[0]["revisionsPendingProposals"], 1);
    assert_eq!(documents[0]["body"], "One.");
    assert_eq!(documents[1]["revisionsPendingProposals"], 0);
    // Without review there are none to count, and the field is absent.
    let mut plain = Host::new();
    plain.publish(json!({"name": "Page",
        "fields": [{"name": "title", "typeRef": {"name": "string"}}],
        "behaviors": [{"name": "Revisions"}]}));
    let page = plain
        .create("Page", json!({"id": "p1", "title": "Home"}))
        .expect("p1");
    assert!(page.get("revisionsPendingProposals").is_none());
    assert!(plain
        .get("Page", "p1")
        .expect("read")
        .get("revisionsPendingProposals")
        .is_none());
    let _ = (admin(), object(json!({})));
}
