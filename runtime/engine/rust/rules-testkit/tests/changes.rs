//! The reference host's outbox of changes (D103), with the behaviors the
//! core runs: which types hear their writes (a use whose behavior hears its
//! row's writes, and a type another's listener names through a relation of
//! it), the change each write queues, the core's and a raw one alike, with
//! the row before and after it, an update that moves nothing queuing none,
//! the queue run first in first out as change commands, a row found gone
//! taking its deadlines, and the linked read.

use serde_json::{json, Map, Value};

use superschematic_rules::{
    Action, Answer, Caller, Command, FieldSpec, ReadRequest, RowWrite, TypeSpec, UseSpec, WriteKind,
};
use superschematic_rules_testkit::MemoryHost;

const NOW: i64 = 1_000_000;
const HOUR: i64 = 3_600_000;

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn worker() -> Caller {
    Caller::new("worker", Vec::<String>::new())
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}

fn flow() -> Value {
    json!({
        "states": ["todo", "doing", "done"],
        "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done"}]
    })
}

// A project whose Reactions hears its tasks through task.project, a task
// with Timers, and a note no behavior hears.
fn host() -> MemoryHost {
    let mut host = MemoryHost::new(superschematic_rules_behaviors::core());
    let project = TypeSpec::new("Project")
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with(UseSpec::on_type(
            "Reactions",
            json!({"rules": [{"when": {"allTerminal": {"schema": "Task", "link": "project"}}, "then": {"transition": "done"}}]}),
        ));
    let task = TypeSpec::new("Task")
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with(UseSpec::on_type(
            "Timers",
            json!({"deadlines": {"respond": {"start": {"create": true}, "afterMs": HOUR}}}),
        ))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::relation(
            "projectId",
            "string",
            "project",
            "Project",
        ));
    let note = TypeSpec::new("Note").with(UseSpec::on_field("status", "Workflow", flow()));
    for spec in [project, task, note] {
        let name = spec.name.clone();
        host.define(spec, &alice(), NOW).expect("defined");
        host.publish(&name, &alice(), NOW).expect("published");
    }
    host
}

fn create(host: &mut MemoryHost, type_name: &str, key: &str, own: Value) {
    host.execute(
        type_name,
        Command::new(
            alice(),
            NOW,
            Action::Create {
                id: Some(key.into()),
                own: object(own),
                params: Map::new(),
            },
        ),
    )
    .expect("created");
}

fn transition(host: &mut MemoryHost, type_name: &str, key: &str, to: &str) {
    host.execute(
        type_name,
        Command::new(
            alice(),
            NOW + 1,
            Action::Operation {
                key: key.into(),
                use_id: "status".into(),
                operation: "transition".into(),
                params: json!({ "to": to }),
            },
        ),
    )
    .expect("moved");
}

/// A type hears its writes when one of its uses' behaviors hears its row's
/// writes, or a listener of another type names it through a relation of
/// it; a type nothing hears queues nothing.
#[test]
fn the_types_whose_writes_are_heard() {
    let host = host();
    assert_eq!(
        host.hearing().into_iter().collect::<Vec<_>>(),
        ["Project", "Task"]
    );
    assert!(host.store().hears("Task"));
    assert!(!host.store().hears("Note"));
}

/// Every write of a heard type queues a change, the core's and a raw one
/// alike: an insert with the row after it, an update with both, a delete
/// with the row before it; an update that moves no field queues none.
#[test]
fn each_write_of_a_heard_type_queues_a_change() {
    let mut host = host();
    create(&mut host, "Project", "p1", json!({}));
    create(
        &mut host,
        "Task",
        "t1",
        json!({"title": "t1", "projectId": "p1"}),
    );
    create(&mut host, "Note", "n1", json!({}));
    transition(&mut host, "Task", "t1", "doing");
    let queued: Vec<(String, String, WriteKind)> = host
        .store()
        .changes()
        .iter()
        .map(|queued| {
            (
                queued.type_name.clone(),
                queued.key.clone(),
                queued.change.write,
            )
        })
        .collect();
    let row = |type_name: &str, key: &str, write| (type_name.to_owned(), key.to_owned(), write);
    assert_eq!(
        queued,
        [
            row("Project", "p1", WriteKind::Insert),
            row("Task", "t1", WriteKind::Insert),
            row("Task", "t1", WriteKind::Update)
        ]
    );
    let moved = &host.store().changes()[2].change;
    assert_eq!(moved.at, NOW + 1);
    assert_eq!(
        moved
            .before
            .as_ref()
            .and_then(|image| image.owned("status", "status")),
        Some(&json!("todo"))
    );
    assert_eq!(
        moved
            .after
            .as_ref()
            .and_then(|image| image.owned("status", "status")),
        Some(&json!("doing"))
    );
    assert_eq!(
        moved
            .after
            .as_ref()
            .and_then(|image| image.own_field("projectId")),
        Some(&json!("p1"))
    );

    // A raw write of an owned field is heard; one that moves nothing is not.
    let raw = [("status".to_owned(), object(json!({"status": "done"})))]
        .into_iter()
        .collect();
    assert!(host.store_mut().write_owned("Task", "t1", &raw, NOW + 2));
    assert!(host.store_mut().write_owned("Task", "t1", &raw, NOW + 3));
    host.store_mut()
        .write_row("Task", "t2", object(json!({"projectId": "p1"})), NOW + 4);
    let tail: Vec<(WriteKind, i64)> = host.store().changes()[3..]
        .iter()
        .map(|queued| (queued.change.write, queued.change.at))
        .collect();
    assert_eq!(
        tail,
        [(WriteKind::Update, NOW + 2), (WriteKind::Insert, NOW + 4)]
    );
}

/// The queue runs first in first out, each change a change command as the
/// worker, the changes the commands queue with it: the task's first change
/// heard by its project's allTerminal, which reads the task as it is now,
/// done, whose move is queued and heard in turn, and the task's later
/// changes, which find the project done.
#[test]
fn the_queue_runs_as_change_commands_first_in_first_out() {
    let mut host = host();
    create(&mut host, "Project", "p1", json!({}));
    transition(&mut host, "Project", "p1", "doing");
    create(
        &mut host,
        "Task",
        "t1",
        json!({"title": "t1", "projectId": "p1"}),
    );
    transition(&mut host, "Task", "t1", "doing");
    transition(&mut host, "Task", "t1", "done");
    let ran = host.run_changes(&worker(), NOW + 10);
    let heard: Vec<String> = ran
        .iter()
        .map(|(queued, outcome)| {
            format!(
                "{} {} {} {}",
                queued.type_name,
                queued.key,
                queued.change.write.as_str(),
                outcome.as_ref().map_or("refused", |applied| {
                    if applied.changes.rows.is_empty() {
                        "quiet"
                    } else {
                        "wrote"
                    }
                })
            )
        })
        .collect();
    assert_eq!(
        heard,
        [
            "Project p1 insert quiet",
            "Project p1 update quiet",
            "Task t1 insert wrote",
            "Task t1 update quiet",
            "Task t1 update quiet",
            "Project p1 update quiet"
        ]
    );
    let project = host.store().row("Project", "p1").expect("p1");
    assert_eq!(project.owned["status"]["status"], json!("done"));
    assert!(host.store().changes().is_empty());
}

/// A change of a row that is gone writes it gone, which takes its
/// deadlines: a raw delete leaves them until its change runs; the core's
/// delete took them already.
#[test]
fn a_row_found_gone_takes_its_deadlines() {
    let mut host = host();
    create(&mut host, "Task", "t1", json!({"title": "t1"}));
    create(&mut host, "Task", "t2", json!({"title": "t2"}));
    host.run_changes(&worker(), NOW);
    assert_eq!(host.store().deadlines().len(), 2);
    // A raw delete past the core: its deadline waits for its change.
    assert!(host.store_mut().delete_row("Task", "t1", NOW + 1));
    assert_eq!(host.store().deadlines().len(), 2);
    let ran = host.run_changes(&worker(), NOW + 2);
    let (queued, outcome) = &ran[0];
    assert_eq!(queued.change.write, WriteKind::Delete);
    assert!(matches!(
        outcome.as_ref().expect("heard").changes.rows.as_slice(),
        [RowWrite::Gone { .. }]
    ));
    assert_eq!(host.store().deadlines().len(), 1);
    // The core's delete takes them at once; its change finds it gone.
    host.execute(
        "Task",
        Command::new(alice(), NOW, Action::Delete { key: "t2".into() }),
    )
    .expect("deleted");
    assert!(host.store().deadlines().is_empty());
    let ran = host.run_changes(&worker(), NOW + 3);
    assert!(matches!(
        ran[0].1.as_ref().expect("heard").changes.rows.as_slice(),
        [RowWrite::Gone { .. }]
    ));
}

/// The linked read answers the rows of a type whose own field holds a key,
/// in key order, a page at a time.
#[test]
fn a_linked_read_answers_a_page_in_key_order() {
    let mut host = host();
    create(&mut host, "Project", "p1", json!({}));
    for key in ["t3", "t1", "t2"] {
        create(
            &mut host,
            "Task",
            key,
            json!({"title": key, "projectId": "p1"}),
        );
    }
    create(&mut host, "Task", "t4", json!({"title": "t4"}));
    let page = host.store().linked("Task", "projectId", "p1", None, 2);
    let keys: Vec<&str> = page.rows.iter().map(|row| row.key.as_str()).collect();
    assert_eq!((keys, page.more), (vec!["t1", "t2"], true));
    let next = host
        .store()
        .linked("Task", "projectId", "p1", Some("t2"), 2);
    assert_eq!(next.rows.len(), 1);
    assert!(!next.more);
    let Some(Answer::Linked(none)) =
        host.store()
            .answer(&ReadRequest::linked("Nope", "projectId", "p1", None))
    else {
        panic!("a linked page");
    };
    assert!(none.rows.is_empty());
}
