//! Reactions on the pure core (D103), through the core's public API over a
//! source in memory that keeps rows of several types and their specs, as a
//! table's adapter keeps them: each when kind firing and not (enters by an
//! insert, an update and a new entry; changes from absent, to and from, a
//! delete, and one that acts only while the field still holds; allTerminal
//! with and without outcomes, over more than one page, with none linked
//! and from a row that is gone; anyTerminal by an entry and by a link moved
//! here; both over a state the linking type's Workflow names in finished,
//! which a row leaves and enters again, D113), a relation to a type keyed by
//! a number, each then that leaves its target as it is, a move the worker
//! may not make refusing the change, the config's checks in the core's
//! words, the cycle check, what it listens to, and the config held to the
//! declaration file's configSchema.

use std::collections::BTreeMap;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    key_of, merge_patch, Action, Answer, Behavior, Caller, ChangeSet, Code, Command, ConfigError,
    ConfigTarget, Core, Declaration, FieldSpec, GuardRequest, Hook, KeyedRow, LinkedRows, Listen,
    Outcome, ReadRequest, Refusal, Row, RowChange, RowWrite, Source, TypeSpec, UseSpec, Veto, View,
    WriteKind, LINKED_PAGE,
};
use superschematic_rules_behaviors::{core, Reactions, ReactionsConfig};

const NOW: i64 = 1_000_000;

// ---- the host ----

// Memory answers the core's reads from rows of any type and their specs,
// and applies a change set's row writes as an adapter does.
#[derive(Default)]
struct Memory {
    rows: BTreeMap<(String, String), Row>,
    specs: BTreeMap<String, TypeSpec>,
}

impl Source for Memory {
    fn answer(&mut self, request: &ReadRequest) -> Result<Answer, Refusal> {
        match request {
            ReadRequest::Row { type_name, key } => Ok(Answer::Row(
                self.rows.get(&(type_name.clone(), key.clone())).cloned(),
            )),
            ReadRequest::Spec { type_name } => Ok(Answer::Spec(self.specs.get(type_name).cloned())),
            ReadRequest::HasRows { type_name } => Ok(Answer::HasRows(
                self.rows.keys().any(|(name, _)| name == type_name),
            )),
            ReadRequest::SubRows { .. } => Ok(Answer::SubRows(Vec::new())),
            ReadRequest::Linked {
                type_name,
                field,
                key,
                after,
                limit,
            } => {
                let mut rows: Vec<KeyedRow> = self
                    .rows
                    .iter()
                    .filter(|((name, row_key), row)| {
                        name == type_name
                            && row.own.get(field).and_then(key_of).as_deref() == Some(key.as_str())
                            && after.as_ref().is_none_or(|after| row_key > after)
                    })
                    .map(|((_, row_key), row)| KeyedRow::new(row_key.clone(), row.clone()))
                    .collect();
                let more = rows.len() > *limit;
                rows.truncate(*limit);
                Ok(Answer::Linked(LinkedRows { rows, more }))
            }
            other => Err(Refusal::internal(format!(
                "this host answers no read of this kind: {other:?}"
            ))),
        }
    }
}

impl Memory {
    fn apply(&mut self, changes: &ChangeSet) {
        for write in &changes.rows {
            let at = (
                write.type_name().to_owned(),
                write.key().expect("a keyed row").to_owned(),
            );
            match write {
                RowWrite::Update {
                    expected_version,
                    own,
                    owned,
                    ..
                } => {
                    let row = self.rows.get_mut(&at).expect("the row");
                    assert_eq!(row.version, *expected_version, "the version read");
                    row.own = merge_patch(&row.own, own);
                    for (use_id, fields) in owned {
                        let held = row.owned.entry(use_id.clone()).or_default();
                        for (field, value) in fields {
                            held.insert(field.clone(), value.clone());
                        }
                    }
                    row.version += 1;
                }
                RowWrite::Gone { .. } => {}
                other => panic!("a write this host does not take: {other:?}"),
            }
        }
    }
}

struct Host {
    core: Core,
    memory: Memory,
}

impl Host {
    fn new(specs: impl IntoIterator<Item = TypeSpec>) -> Self {
        let mut memory = Memory::default();
        for spec in specs {
            memory.specs.insert(spec.name.clone(), spec);
        }
        Host {
            core: core().with(Freeze),
            memory,
        }
    }

    // put writes a row: its own fields and its status, entered at a time.
    fn put(&mut self, type_name: &str, key: &str, own: Value, status: &str, entered: i64) {
        let row = Row {
            own: object(own),
            owned: [(
                "status".to_owned(),
                object(json!({"status": status, "enteredAt": entered})),
            )]
            .into_iter()
            .collect(),
            version: 1,
        };
        self.memory
            .rows
            .insert((type_name.to_owned(), key.to_owned()), row);
    }

    fn remove(&mut self, type_name: &str, key: &str) {
        self.memory
            .rows
            .remove(&(type_name.to_owned(), key.to_owned()));
    }

    // change runs a write of a row through the core as the worker and
    // applies what it writes.
    fn change(&mut self, type_name: &str, key: &str, change: RowChange) -> Outcome {
        self.change_as(&worker(), type_name, key, change)
    }

    fn change_as(
        &mut self,
        caller: &Caller,
        type_name: &str,
        key: &str,
        change: RowChange,
    ) -> Outcome {
        let spec = self.memory.specs[type_name].clone();
        let command = Command::new(
            caller.clone(),
            NOW,
            Action::Change {
                key: key.to_owned(),
                change,
            },
        )
        .with_request("r1");
        let outcome = self.core.drive(&spec, &command, &mut self.memory);
        if let Outcome::Changed(changes) = &outcome {
            self.memory.apply(changes);
        }
        outcome
    }

    fn status(&self, type_name: &str, key: &str) -> String {
        self.memory.rows[&(type_name.to_owned(), key.to_owned())].owned["status"]["status"]
            .as_str()
            .unwrap_or_default()
            .to_owned()
    }
}

// ---- the fixtures ----

fn worker() -> Caller {
    Caller::new("worker", Vec::<String>::new())
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().expect("an object")
}

// A project's status: planned starts, active ends done or failed, and
// either may be cancelled; done with success, failed with failure.
fn project_flow() -> Value {
    json!({
        "states": ["planned", "active", "done", "failed", "cancelled"],
        "transitions": [
            {"from": "planned", "to": "active"},
            {"from": "planned", "to": "cancelled"},
            {"from": "active", "to": "done"},
            {"from": "active", "to": "failed"},
            {"from": "active", "to": "cancelled"}
        ],
        "outcomes": {"done": "success", "failed": "failure", "cancelled": "neutral"}
    })
}

// A task's status: todo starts, doing ends done, failed or dropped.
fn task_flow() -> Value {
    json!({
        "states": ["todo", "doing", "done", "failed", "dropped"],
        "transitions": [
            {"from": "todo", "to": "doing"},
            {"from": "todo", "to": "dropped"},
            {"from": "doing", "to": "done"},
            {"from": "doing", "to": "failed"},
            {"from": "doing", "to": "dropped"}
        ],
        "outcomes": {"done": "success", "failed": "failure", "dropped": "neutral"}
    })
}

// A project as fixture-reactions-db holds it: its status a Workflow field,
// Reactions on the type, Freeze after it, a name.
fn project(reactions: Value) -> TypeSpec {
    TypeSpec::new("Project")
        .with(UseSpec::on_field("status", "Workflow", project_flow()))
        .with(UseSpec::on_type("Reactions", reactions))
        .with(UseSpec::on_type("Freeze", None))
        .with_field(FieldSpec::new("name", "string"))
}

fn project_rules() -> Value {
    json!({"rules": [
        {"when": {"allTerminal": {"schema": "Task", "link": "project", "outcomes": ["success"]}}, "then": {"transition": "done"}},
        {"when": {"anyTerminal": {"schema": "Task", "link": "project", "outcomes": ["failure"]}}, "then": {"transition": "failed"}}
    ]})
}

// A task: a title, a priority, its project, its status a Workflow field.
fn task(reactions: Value) -> TypeSpec {
    TypeSpec::new("Task")
        .with(UseSpec::on_field("status", "Workflow", task_flow()))
        .with(UseSpec::on_type("Reactions", reactions))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("priority", "string"))
        .with_field(FieldSpec::relation(
            "projectId",
            "Identity.UUID",
            "project",
            "Project",
        ))
}

fn task_rules() -> Value {
    json!({"rules": [
        {"when": {"enters": "doing"}, "then": {"link": "project", "transition": "active"}},
        {"when": {"changes": {"field": "priority", "to": "urgent"}}, "then": {"transition": "doing"}}
    ]})
}

fn fresh() -> Host {
    Host::new([project(project_rules()), task(task_rules())])
}

// image is a row's own fields and status as a write's image holds them.
fn image(own: Value, status: Option<(&str, i64)>) -> Value {
    match status {
        Some((status, entered)) => {
            json!({"own": own, "owned": {"status": {"status": status, "enteredAt": entered}}})
        }
        None => json!({"own": own}),
    }
}

fn write(write: WriteKind, before: Option<Value>, after: Option<Value>) -> RowChange {
    RowChange {
        write,
        at: NOW,
        before: before.map(|image| serde_json::from_value(image).expect("an image")),
        after: after.map(|image| serde_json::from_value(image).expect("an image")),
    }
}

fn update(before: Value, after: Value) -> RowChange {
    write(WriteKind::Update, Some(before), Some(after))
}

fn changed(outcome: Outcome) -> ChangeSet {
    match outcome.into_result() {
        Ok(changes) => changes,
        Err(refusal) => panic!("refused: {}", refusal.error_json()),
    }
}

// written lists the rows a change set wrote, by type and key.
fn written(changes: &ChangeSet) -> Vec<String> {
    changes
        .rows
        .iter()
        .filter(|write| !matches!(write, RowWrite::Gone { .. }))
        .map(|write| format!("{} {}", write.type_name(), write.key().unwrap_or_default()))
        .collect()
}

// Freeze: vetoes a move of a row's status while its own frozen is true.
struct Freeze;

impl Behavior for Freeze {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Freeze").veto("frozen")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let frozen = view.own_field("frozen") == Some(Value::Bool(true));
        Ok(
            (request.operation_of("status", "transition").is_some() && frozen)
                .then(|| Veto::new("it is frozen").with_code("frozen")),
        )
    }
}

// define runs the core's define check of a type.
fn define(spec: &TypeSpec) -> Result<(), Refusal> {
    let command = Command::new(worker(), NOW, Action::Define);
    core()
        .with(Freeze)
        .drive(spec, &command, &mut Memory::default())
        .into_result()
        .map(|_| ())
}

// rule is the reason a define refuses a Reactions config for, after the
// core's prefix.
fn rule(spec: &TypeSpec) -> String {
    let refusal = define(spec).expect_err("refused");
    refusal
        .message
        .strip_prefix(&format!("type {}: behavior Reactions config: ", spec.name))
        .unwrap_or(&refusal.message)
        .to_owned()
}

// shape is what a define refuses a Reactions config's shape for: each
// issue as `<path into the use's entry>: <message>`.
fn shape(reactions: Value) -> Vec<String> {
    let refusal = define(&task(reactions)).expect_err("refused");
    refusal.details.expect("details")["issues"]
        .as_array()
        .expect("issues")
        .iter()
        .map(|issue| {
            format!(
                "{}: {}",
                issue["path"].as_str().unwrap(),
                issue["message"].as_str().unwrap()
            )
        })
        .collect()
}

// undescribed is a schema with every description left out, at any depth.
fn undescribed(schema: &Value) -> Value {
    match schema {
        Value::Object(members) => Value::Object(
            members
                .iter()
                .filter(|(key, _)| key.as_str() != "description")
                .map(|(key, member)| (key.clone(), undescribed(member)))
                .collect(),
        ),
        Value::Array(items) => Value::Array(items.iter().map(undescribed).collect()),
        other => other.clone(),
    }
}

// ---- the declaration and its schema ----

/// Reactions declares no field, sub-table, deadline or operation, and hears
/// its row's writes.
#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Reactions.declaration(),
        Declaration::new("Reactions").hears_changes()
    );
}

/// The checks Reactions writes by hand are the declaration's configSchema,
/// keyword for keyword: the schema, its descriptions left out, is exactly
/// the one they implement, so a keyword the file gains fails this test
/// until the checks take it. The shape refusals the recorded config
/// scenario holds (`core-reactions.json`, its steps 13 to 15) come out in
/// its words.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(superschematic_rules_behaviors::reactions::DECLARATION)
        .expect("the declaration file parses");
    let link = json!({"pattern": "^[a-z][A-Za-z0-9]{0,63}$", "type": "string"});
    let schema = json!({"pattern": "^[A-Za-z][A-Za-z0-9_-]{0,127}$", "type": "string"});
    let outcomes = json!({"items": {"enum": ["success", "failure", "neutral"]}, "minItems": 1, "type": "array", "uniqueItems": true});
    let terminal = |required: Value| {
        json!({
            "additionalProperties": false,
            "properties": {"link": link, "outcomes": outcomes, "schema": schema},
            "required": required,
            "type": "object"
        })
    };
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "rules": {
                    "items": {
                        "additionalProperties": false,
                        "properties": {
                            "then": {
                                "additionalProperties": false,
                                "properties": {
                                    "link": link,
                                    "transition": {"minLength": 1, "type": "string"}
                                },
                                "required": ["transition"],
                                "type": "object"
                            },
                            "when": {
                                "additionalProperties": false,
                                "maxProperties": 1,
                                "minProperties": 1,
                                "properties": {
                                    "allTerminal": terminal(json!(["schema", "link"])),
                                    "anyTerminal": terminal(json!(["schema", "link", "outcomes"])),
                                    "changes": {
                                        "additionalProperties": false,
                                        "properties": {
                                            "field": {"maxLength": 512, "pattern": "^[^.]+(\\.[^.]+)*$", "type": "string"},
                                            "from": {},
                                            "to": {}
                                        },
                                        "required": ["field"],
                                        "type": "object"
                                    },
                                    "enters": {"minLength": 1, "type": "string"},
                                    "holds": link,
                                    "revised": {
                                        "additionalProperties": false,
                                        "properties": {"link": link},
                                        "required": ["link"],
                                        "type": "object"
                                    }
                                },
                                "type": "object"
                            }
                        },
                        "required": ["when", "then"],
                        "type": "object"
                    },
                    "maxItems": 64,
                    "minItems": 1,
                    "type": "array"
                },
                "status": {"minLength": 1, "type": "string"}
            },
            "required": ["rules"],
            "type": "object"
        })
    );
    assert_eq!(file.get("operations"), None);
    assert_eq!(file.get("fields"), None);

    // The recorded config scenario's shape refusals.
    let one = |when: Value| json!({"rules": [{"when": when, "then": {"transition": "done"}}]});
    assert_eq!(
        shape(one(
            json!({"enters": "doing", "allTerminal": {"schema": "Task", "link": "project"}})
        )),
        ["/config/rules/0/when: must NOT have more than 1 properties"]
    );
    assert_eq!(
        shape(one(
            json!({"anyTerminal": {"schema": "Project", "link": "project"}})
        )),
        ["/config/rules/0/when/anyTerminal: must have required property 'outcomes'"]
    );
    assert_eq!(
        shape(one(
            json!({"allTerminal": {"schema": "Project", "link": "project", "outcomes": ["lost"]}})
        )),
        ["/config/rules/0/when/allTerminal/outcomes/0: must be one of \"success\", \"failure\", \"neutral\""]
    );
    // And the rest of the schema's keywords, in ajv's words.
    assert_eq!(
        shape(json!({"rules": [
            {"when": {}, "then": {"transition": "", "link": "Bad", "x": 1}},
            {"when": {"changes": {"field": "a..b", "x": 1}}},
            {"when": {"allTerminal": {"schema": "2nd", "link": "p", "outcomes": ["success", "success"]}}, "then": {"transition": "done"}},
            {"when": {"revised": {}}, "then": {"transition": "done"}}
        ], "status": "", "extra": true})),
        [
            "/config: unknown key \"extra\"",
            "/config/rules/0/then: unknown key \"x\"",
            "/config/rules/0/then/link: must match pattern \"^[a-z][A-Za-z0-9]{0,63}$\"",
            "/config/rules/0/then/transition: must NOT have fewer than 1 characters",
            "/config/rules/0/when: must NOT have fewer than 1 properties",
            "/config/rules/1: must have required property 'then'",
            "/config/rules/1/when/changes: unknown key \"x\"",
            "/config/rules/1/when/changes/field: must match pattern \"^[^.]+(\\.[^.]+)*$\"",
            "/config/rules/2/when/allTerminal/outcomes: must NOT have duplicate items (items ## 1 and 0 are identical)",
            "/config/rules/2/when/allTerminal/schema: must match pattern \"^[A-Za-z][A-Za-z0-9_-]{0,127}$\"",
            "/config/rules/3/when/revised: must have required property 'link'",
            "/config/status: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        shape(json!({"rules": []})),
        ["/config/rules: must NOT have fewer than 1 items"]
    );
}

// ---- its own row's writes ----

/// An enters rule fires when a write moves the status into its state: an
/// insert that leaves it there, an update from another state, or one that
/// enters it again (enteredAt moved); a write that leaves the status where
/// it was fires nothing.
#[test]
fn enters_fires_on_an_insert_an_update_and_a_new_entry() {
    let mut host = fresh();
    host.put("Project", "p1", json!({}), "planned", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
    let doing = image(json!({"projectId": "p1"}), Some(("doing", 2)));
    // An insert in the state.
    let inserted = changed(host.change(
        "Task",
        "t1",
        write(WriteKind::Insert, None, Some(doing.clone())),
    ));
    assert_eq!(written(&inserted), ["Project p1"]);
    assert_eq!(host.status("Project", "p1"), "active");
    // An update that leaves the status as it was fires nothing.
    let mut host = Host::new([project(project_rules()), task(task_rules())]);
    host.put("Project", "p1", json!({}), "planned", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
    let same = changed(host.change("Task", "t1", update(doing.clone(), doing.clone())));
    assert!(same.rows.is_empty());
    // One from another state fires, and so does one that enters it again.
    let todo = image(json!({"projectId": "p1"}), Some(("todo", 1)));
    changed(host.change("Task", "t1", update(todo, doing.clone())));
    assert_eq!(host.status("Project", "p1"), "active");
    let again = image(json!({"projectId": "p1"}), Some(("doing", 3)));
    let mut host = Host::new([project(project_rules()), task(task_rules())]);
    host.put("Project", "p1", json!({}), "planned", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 3);
    changed(host.change("Task", "t1", update(doing, again)));
    assert_eq!(host.status("Project", "p1"), "active");
}

/// A changes rule fires when a write moves its field: an insert from
/// absent, an update to `to`; not an update that leaves it, nor a delete;
/// with `from`, only a move from that value; and one with `to` on the row
/// itself acts only while the field still holds it.
#[test]
fn changes_fires_on_a_move_of_its_field() {
    let urgent = |priority: Option<&str>, status: &str| {
        image(json!({"priority": priority}), Some((status, 1)))
    };
    // An insert moves the field from absent.
    let mut host = fresh();
    host.put("Task", "t1", json!({"priority": "urgent"}), "todo", 1);
    changed(host.change(
        "Task",
        "t1",
        write(
            WriteKind::Insert,
            None,
            Some(urgent(Some("urgent"), "todo")),
        ),
    ));
    assert_eq!(host.status("Task", "t1"), "doing");
    // An update to it fires; one that leaves it does not.
    let mut host = host_with_task("todo", json!({"priority": "urgent"}));
    let moved = changed(host.change(
        "Task",
        "t1",
        update(urgent(Some("low"), "todo"), urgent(Some("urgent"), "todo")),
    ));
    assert_eq!(written(&moved), ["Task t1"]);
    let mut host = host_with_task("todo", json!({"priority": "urgent"}));
    let left = changed(host.change(
        "Task",
        "t1",
        update(
            urgent(Some("urgent"), "todo"),
            urgent(Some("urgent"), "todo"),
        ),
    ));
    assert!(left.rows.is_empty());
    // A delete moves nothing.
    let mut host = host_with_task("todo", json!({"priority": "urgent"}));
    host.remove("Task", "t1");
    let deleted = changed(host.change(
        "Task",
        "t1",
        write(
            WriteKind::Delete,
            Some(urgent(Some("urgent"), "todo")),
            None,
        ),
    ));
    assert!(matches!(&deleted.rows[..], [RowWrite::Gone { .. }]));
    // With to on the row itself, it acts only while the field still holds
    // what it moved to.
    let mut host = host_with_task("todo", json!({"priority": "low"}));
    let stale = changed(host.change(
        "Task",
        "t1",
        update(urgent(Some("low"), "todo"), urgent(Some("urgent"), "todo")),
    ));
    assert!(stale.rows.is_empty());
    assert_eq!(host.status("Task", "t1"), "todo");
    // With from, only a move from that value fires.
    let from = json!({"rules": [{"when": {"changes": {"field": "priority", "from": "low", "to": "urgent"}}, "then": {"transition": "doing"}}]});
    for (was, fires) in [("low", true), ("normal", false)] {
        let mut host = Host::new([project(project_rules()), task(from.clone())]);
        host.put("Task", "t1", json!({"priority": "urgent"}), "todo", 1);
        changed(host.change(
            "Task",
            "t1",
            update(urgent(Some(was), "todo"), urgent(Some("urgent"), "todo")),
        ));
        assert_eq!(host.status("Task", "t1") == "doing", fires, "{was}");
    }
}

// host_with_task is a host with a task t1 of no project.
fn host_with_task(status: &str, own: Value) -> Host {
    let mut host = fresh();
    host.put("Task", "t1", own, status, 1);
    host
}

/// A changes rule names a field of the table by its name: a relation,
/// which hears the key its column holds, and a field anchor, which hears
/// its behavior's value field; and the engine's qualified form.
#[test]
fn a_changes_rule_names_a_relation_a_field_anchor_or_the_qualified_form() {
    for field in ["status", "status.status"] {
        let rules = json!({"rules": [{"when": {"changes": {"field": field, "to": "doing"}}, "then": {"link": "project", "transition": "active"}}]});
        let mut host = Host::new([project(project_rules()), task(rules)]);
        host.put("Project", "p1", json!({}), "planned", 1);
        host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
        changed(host.change(
            "Task",
            "t1",
            update(
                image(json!({"projectId": "p1"}), Some(("todo", 1))),
                image(json!({"projectId": "p1"}), Some(("doing", 2))),
            ),
        ));
        assert_eq!(host.status("Project", "p1"), "active", "{field}");
    }
    let rules = json!({"rules": [{"when": {"changes": {"field": "project", "to": "p2"}}, "then": {"transition": "doing"}}]});
    let mut host = Host::new([project(project_rules()), task(rules)]);
    host.put("Task", "t1", json!({"projectId": "p2"}), "todo", 1);
    changed(host.change(
        "Task",
        "t1",
        update(
            image(json!({"projectId": "p1"}), Some(("todo", 1))),
            image(json!({"projectId": "p2"}), Some(("todo", 1))),
        ),
    ));
    assert_eq!(host.status("Task", "t1"), "doing");
}

// ---- the rows that point here ----

// tasks puts tasks of p1 in states.
fn tasks(host: &mut Host, states: &[(&str, &str)]) {
    for (key, status) in states {
        host.put("Task", key, json!({"projectId": "p1"}), status, 1);
    }
}

fn task_moved(key: &str, from: &str, to: &str) -> (String, RowChange) {
    (
        key.to_owned(),
        update(
            image(json!({"projectId": "p1"}), Some((from, 1))),
            image(json!({"projectId": "p1"}), Some((to, 2))),
        ),
    )
}

/// allTerminal fires when every row that points here is in a terminal
/// state whose outcome it lists, at least one: not while one is not, nor
/// while one ended with another outcome; with no outcomes listed, any
/// terminal state counts.
#[test]
fn all_terminal_fires_when_every_linked_row_is_terminal() {
    let mut host = fresh();
    host.put("Project", "p1", json!({}), "active", 1);
    tasks(&mut host, &[("t1", "done"), ("t2", "doing")]);
    let (key, change) = task_moved("t1", "doing", "done");
    changed(host.change("Task", &key, change));
    assert_eq!(host.status("Project", "p1"), "active");
    host.put("Task", "t2", json!({"projectId": "p1"}), "dropped", 2);
    let (key, change) = task_moved("t2", "doing", "dropped");
    changed(host.change("Task", &key, change));
    // Dropped ends neutral, which [success] does not count.
    assert_eq!(host.status("Project", "p1"), "active");
    host.put("Task", "t2", json!({"projectId": "p1"}), "done", 2);
    let (key, change) = task_moved("t2", "dropped", "done");
    let changes = changed(host.change("Task", &key, change));
    assert_eq!(written(&changes), ["Project p1"]);
    assert_eq!(host.status("Project", "p1"), "done");
    // With no outcomes, any terminal state counts.
    let any = json!({"rules": [{"when": {"allTerminal": {"schema": "Task", "link": "project"}}, "then": {"transition": "done"}}]});
    let mut host = Host::new([project(any), task(task_rules())]);
    host.put("Project", "p1", json!({}), "active", 1);
    tasks(&mut host, &[("t1", "dropped"), ("t2", "failed")]);
    let (key, change) = task_moved("t2", "doing", "failed");
    changed(host.change("Task", &key, change));
    assert_eq!(host.status("Project", "p1"), "done");
}

/// allTerminal reads the rows that point here a page at a time, to the
/// last; with none it fires nothing; and a row that is gone settles the
/// project it pointed at before.
#[test]
fn all_terminal_pages_through_its_rows_and_hears_a_row_that_is_gone() {
    let mut host = fresh();
    host.put("Project", "p1", json!({}), "active", 1);
    for n in 0..=LINKED_PAGE {
        host.put(
            "Task",
            &format!("t{n:04}"),
            json!({"projectId": "p1"}),
            "done",
            1,
        );
    }
    host.put(
        "Task",
        &format!("t{LINKED_PAGE:04}"),
        json!({"projectId": "p1"}),
        "doing",
        1,
    );
    let (_, change) = task_moved("t0000", "doing", "done");
    changed(host.change("Task", "t0000", change));
    // The last row, on the second page, is not done.
    assert_eq!(host.status("Project", "p1"), "active");
    host.put(
        "Task",
        &format!("t{LINKED_PAGE:04}"),
        json!({"projectId": "p1"}),
        "done",
        2,
    );
    let (_, change) = task_moved(&format!("t{LINKED_PAGE:04}"), "doing", "done");
    changed(host.change("Task", &format!("t{LINKED_PAGE:04}"), change));
    assert_eq!(host.status("Project", "p1"), "done");

    // A row gone: the project it pointed at, whose rows left are done, is
    // done; one with none left is left as it is.
    let mut host = host_of(&[("t1", "done")]);
    let gone = write(
        WriteKind::Delete,
        Some(image(json!({"projectId": "p1"}), Some(("doing", 1)))),
        None,
    );
    let changes = changed(host.change("Task", "t2", gone.clone()));
    assert!(matches!(&changes.rows[0], RowWrite::Gone { key, .. } if key == "t2"));
    assert_eq!(host.status("Project", "p1"), "done");
    let mut host = host_of(&[]);
    changed(host.change("Task", "t2", gone));
    assert_eq!(host.status("Project", "p1"), "active");
}

// host_of is a host with an active project p1 and tasks of it.
fn host_of(states: &[(&str, &str)]) -> Host {
    let mut host = fresh();
    host.put("Project", "p1", json!({}), "active", 1);
    tasks(&mut host, states);
    host
}

/// anyTerminal fires when a row that points here enters a terminal state
/// whose outcome it lists, or is pointed here while in one; not on a write
/// that leaves both as they were.
#[test]
fn any_terminal_fires_on_an_entry_and_on_a_link_moved_here() {
    let mut host = host_of(&[("t1", "failed")]);
    let (key, change) = task_moved("t1", "doing", "failed");
    changed(host.change("Task", &key, change));
    assert_eq!(host.status("Project", "p1"), "failed");
    // A link moved here while failed.
    let mut host = host_of(&[("t1", "failed")]);
    let moved = update(
        image(json!({"projectId": "p9"}), Some(("failed", 1))),
        image(json!({"projectId": "p1"}), Some(("failed", 1))),
    );
    changed(host.change("Task", "t1", moved));
    assert_eq!(host.status("Project", "p1"), "failed");
    // A write that moves neither does not.
    let mut host = host_of(&[("t1", "failed")]);
    let titled = update(
        image(
            json!({"projectId": "p1", "title": "a"}),
            Some(("failed", 1)),
        ),
        image(
            json!({"projectId": "p1", "title": "b"}),
            Some(("failed", 1)),
        ),
    );
    let changes = changed(host.change("Task", "t1", titled));
    assert!(changes.rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "active");
}

// ---- a state a transition leaves that counts as finished (D113) ----

// A task that is done may be reopened, and one that failed tried again:
// finished names done, a success, and failed, a failure, each of which a
// transition leaves; dropped stays terminal.
fn reopening_task_flow() -> Value {
    json!({
        "states": ["todo", "doing", "done", "failed", "dropped"],
        "transitions": [
            {"from": "todo", "to": "doing"},
            {"from": "todo", "to": "dropped"},
            {"from": "doing", "to": "done"},
            {"from": "doing", "to": "failed"},
            {"from": "doing", "to": "dropped"},
            {"from": "done", "to": "todo"},
            {"from": "failed", "to": "todo"}
        ],
        "outcomes": {"dropped": "neutral"},
        "finished": {"done": "success", "failed": "failure"}
    })
}

// A project that is done or failed goes back to active when its work
// starts again: finished names both, and cancelled stays terminal.
fn reopening_project_flow() -> Value {
    json!({
        "states": ["planned", "active", "done", "failed", "cancelled"],
        "transitions": [
            {"from": "planned", "to": "active"},
            {"from": "planned", "to": "cancelled"},
            {"from": "active", "to": "done"},
            {"from": "active", "to": "failed"},
            {"from": "active", "to": "cancelled"},
            {"from": "done", "to": "active"},
            {"from": "failed", "to": "active"}
        ],
        "outcomes": {"cancelled": "neutral"},
        "finished": {"done": "success", "failed": "failure"}
    })
}

// with_flow is a type with its status's Workflow config replaced.
fn with_flow(mut spec: TypeSpec, flow: Value) -> TypeSpec {
    let status = spec
        .uses
        .iter_mut()
        .find(|entry| entry.id() == "status")
        .expect("a status");
    *status = UseSpec::on_field("status", "Workflow", flow);
    spec
}

// reopening is a host of a project and its tasks, whose done and failed a
// transition leaves: the tasks' always, the project's when it reopens too.
fn reopening(project_reopens: bool) -> Host {
    let project = if project_reopens {
        with_flow(project(project_rules()), reopening_project_flow())
    } else {
        project(project_rules())
    };
    let mut host = Host::new([
        project,
        with_flow(task(task_rules()), reopening_task_flow()),
    ]);
    host.put("Project", "p1", json!({}), "active", 1);
    host
}

// step moves a task of p1 from a state to another, as its table has it
// after the write, and runs the write through the core.
fn step(host: &mut Host, key: &str, from: &str, to: &str) -> ChangeSet {
    host.put("Task", key, json!({"projectId": "p1"}), to, 2);
    let (key, change) = task_moved(key, from, to);
    changed(host.change("Task", &key, change))
}

/// allTerminal counts a row in a state its Workflow's finished names as it
/// counts one in a terminal state, with the outcome finished names: every
/// task done closes the project, though a task may leave done. A task
/// reopened moves nothing by itself, and a project whose done no transition
/// leaves stays done whatever its tasks do next.
#[test]
fn all_terminal_counts_a_state_finished_names_and_a_reopened_row_moves_nothing() {
    let mut host = reopening(false);
    tasks(&mut host, &[("t1", "doing"), ("t2", "doing")]);
    assert!(step(&mut host, "t1", "doing", "done").rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "active");
    let closed = step(&mut host, "t2", "doing", "done");
    assert_eq!(written(&closed), ["Project p1"]);
    assert_eq!(host.status("Project", "p1"), "done");
    // A task reopened leaves the project as it is.
    assert!(step(&mut host, "t2", "done", "todo").rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "done");
    // Its starting again finds no transition out of the project's done.
    assert!(step(&mut host, "t2", "todo", "doing").rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "done");

    // A failure finished names is not a success, which [success] does not
    // count; with no outcomes listed, every state finished names counts, as
    // every terminal state does.
    for (terminal, closes) in [
        (
            json!({"schema": "Task", "link": "project", "outcomes": ["success"]}),
            false,
        ),
        (json!({"schema": "Task", "link": "project"}), true),
    ] {
        let rules =
            json!({"rules": [{"when": {"allTerminal": terminal}, "then": {"transition": "done"}}]});
        let mut host = Host::new([
            project(rules),
            with_flow(task(task_rules()), reopening_task_flow()),
        ]);
        host.put("Project", "p1", json!({}), "active", 1);
        tasks(
            &mut host,
            &[("t1", "done"), ("t2", "doing"), ("t3", "dropped")],
        );
        step(&mut host, "t2", "doing", "failed");
        assert_eq!(host.status("Project", "p1") == "done", closes);
    }

    // Without finished, a state a transition leaves has no outcome, and a
    // task in it counts for nothing, as before.
    let mut plain = reopening_task_flow();
    plain
        .as_object_mut()
        .expect("an object")
        .shift_remove("finished");
    let mut host = Host::new([
        project(project_rules()),
        with_flow(task(task_rules()), plain),
    ]);
    host.put("Project", "p1", json!({}), "active", 1);
    tasks(&mut host, &[("t1", "done")]);
    assert!(step(&mut host, "t1", "doing", "done").rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "active");
}

/// A project whose done a transition leaves, and finished names, goes back
/// to active when a reopened task starts again, through the task's own
/// rule, and closes again when the task is done again.
#[test]
fn a_reopened_row_starting_again_moves_its_project_back_and_closes_it_again() {
    let mut host = reopening(true);
    tasks(&mut host, &[("t1", "done"), ("t2", "doing")]);
    let closed = step(&mut host, "t2", "doing", "done");
    assert_eq!(written(&closed), ["Project p1"]);
    assert_eq!(host.status("Project", "p1"), "done");
    // Reopened: nothing moves a project back by itself.
    assert!(step(&mut host, "t2", "done", "todo").rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "done");
    // Started again: the task's rule moves its project to active.
    let started = step(&mut host, "t2", "todo", "doing");
    assert_eq!(written(&started), ["Project p1"]);
    assert_eq!(host.status("Project", "p1"), "active");
    // Done again: every task is done, and the project closes again.
    let closed = step(&mut host, "t2", "doing", "done");
    assert_eq!(written(&closed), ["Project p1"]);
    assert_eq!(host.status("Project", "p1"), "done");
}

/// anyTerminal fires when a row enters a state its Workflow's finished
/// names with an outcome the rule lists, and again when the row enters it
/// again after leaving it; a row that leaves it fires nothing.
#[test]
fn any_terminal_fires_each_time_a_row_enters_a_state_finished_names() {
    let mut host = reopening(true);
    tasks(&mut host, &[("t1", "doing"), ("t2", "todo")]);
    let failed = step(&mut host, "t1", "doing", "failed");
    assert_eq!(written(&failed), ["Project p1"]);
    assert_eq!(host.status("Project", "p1"), "failed");
    // Tried again: leaving failed fires nothing.
    assert!(step(&mut host, "t1", "failed", "todo").rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "failed");
    step(&mut host, "t1", "todo", "doing");
    assert_eq!(host.status("Project", "p1"), "active");
    // Failed again: heard again.
    let again = step(&mut host, "t1", "doing", "failed");
    assert_eq!(written(&again), ["Project p1"]);
    assert_eq!(host.status("Project", "p1"), "failed");
    // A success finished names is not a failure: [failure] does not hear it.
    let mut host = reopening(true);
    tasks(&mut host, &[("t1", "doing"), ("t2", "todo")]);
    assert!(step(&mut host, "t1", "doing", "done").rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "active");
    // A link moved here while the row is in a state finished names.
    let mut host = reopening(true);
    host.put("Task", "t1", json!({"projectId": "p1"}), "failed", 1);
    let moved = update(
        image(json!({"projectId": "p9"}), Some(("failed", 1))),
        image(json!({"projectId": "p1"}), Some(("failed", 1))),
    );
    changed(host.change("Task", "t1", moved));
    assert_eq!(host.status("Project", "p1"), "failed");
}

/// A to-one relation to a type keyed by a number holds the key as a number,
/// which the core reads as its decimal text, the key a command names the
/// row by (key_of): a then with a link moves the row it holds, and a write
/// of a linking row reaches its target's allTerminal and anyTerminal rules.
#[test]
fn a_relation_to_a_number_key_is_heard_and_moved() {
    let numbered = || {
        TypeSpec::new("Task")
            .with(UseSpec::on_field("status", "Workflow", task_flow()))
            .with(UseSpec::on_type("Reactions", task_rules()))
            .with_field(FieldSpec::new("priority", "string"))
            .with_field(FieldSpec::relation(
                "projectId",
                "Generic.Int64",
                "project",
                "Project",
            ))
    };
    let mut host = Host::new([project(project_rules()), numbered()]);
    host.put("Project", "7", json!({}), "planned", 1);
    host.put("Task", "t1", json!({"projectId": 7}), "doing", 2);
    host.put("Task", "t2", json!({"projectId": 7}), "done", 2);
    let moved = |from: &str, to: &str| {
        update(
            image(json!({"projectId": 7}), Some((from, 1))),
            image(json!({"projectId": 7}), Some((to, 2))),
        )
    };
    let started = changed(host.change("Task", "t1", moved("todo", "doing")));
    assert_eq!(written(&started), ["Project 7"]);
    assert_eq!(host.status("Project", "7"), "active");
    host.put("Task", "t1", json!({"projectId": 7}), "done", 3);
    changed(host.change("Task", "t1", moved("doing", "done")));
    assert_eq!(host.status("Project", "7"), "done");

    let mut host = Host::new([project(project_rules()), numbered()]);
    host.put("Project", "7", json!({}), "active", 1);
    host.put("Task", "t1", json!({"projectId": 7}), "failed", 2);
    changed(host.change("Task", "t1", moved("doing", "failed")));
    assert_eq!(host.status("Project", "7"), "failed");

    for (value, key) in [
        (json!(7), Some("7")),
        (json!(7.0), Some("7")),
        (json!("p1"), Some("p1")),
        (json!(7.5), None),
        (json!(true), None),
        (Value::Null, None),
    ] {
        assert_eq!(key_of(&value).as_deref(), key, "key_of({value})");
    }
}

// ---- what a then leaves as it is ----

/// A then leaves its target as it is, with no refusal of the change: a
/// target already in the state, one with no transition to it, one whose
/// guards veto the move, one that is gone, and, for an enters rule on the
/// row itself, a row that has left the state since.
#[test]
fn a_then_leaves_a_target_it_cannot_move() {
    let start = || task_moved("t1", "todo", "doing");
    // Already in the state.
    let mut host = fresh();
    host.put("Project", "p1", json!({}), "active", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
    let (key, change) = start();
    assert!(changed(host.change("Task", &key, change)).rows.is_empty());
    // No transition to it (done is terminal).
    let mut host = fresh();
    host.put("Project", "p1", json!({}), "done", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
    let (key, change) = start();
    assert!(changed(host.change("Task", &key, change)).rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "done");
    // Vetoed by its guards.
    let mut host = fresh();
    host.put("Project", "p1", json!({"frozen": true}), "planned", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
    let (key, change) = start();
    assert!(changed(host.change("Task", &key, change)).rows.is_empty());
    assert_eq!(host.status("Project", "p1"), "planned");
    // Gone.
    let mut host = fresh();
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
    let (key, change) = start();
    assert!(changed(host.change("Task", &key, change)).rows.is_empty());
    // An enters rule on the row itself, the row since moved on.
    let rules = json!({"rules": [{"when": {"enters": "doing"}, "then": {"transition": "done"}}]});
    let mut host = Host::new([project(project_rules()), task(rules)]);
    host.put("Task", "t1", json!({}), "dropped", 3);
    let entered = update(
        image(json!({}), Some(("todo", 1))),
        image(json!({}), Some(("doing", 2))),
    );
    assert!(changed(host.change("Task", "t1", entered)).rows.is_empty());
    assert_eq!(host.status("Task", "t1"), "dropped");
}

/// A move the worker may not make (a transition's permission it lacks)
/// refuses the change command, which fails its message; nothing of it
/// applies.
#[test]
fn a_forbidden_move_refuses_the_change() {
    let mut flow = project_flow();
    flow["transitions"][0]["permission"] = json!("projects.start");
    let guarded = TypeSpec::new("Project")
        .with(UseSpec::on_field("status", "Workflow", flow))
        .with(UseSpec::on_type("Reactions", project_rules()));
    let mut host = Host::new([guarded, task(task_rules())]);
    host.put("Project", "p1", json!({}), "planned", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
    let (key, change) = task_moved("t1", "todo", "doing");
    let refusal = host
        .change("Task", &key, change.clone())
        .into_result()
        .expect_err("refused");
    assert_eq!(refusal.code, Code::Forbidden);
    assert_eq!(host.status("Project", "p1"), "planned");
    // A worker that holds it moves the project.
    changed(host.change_as(
        &Caller::new("worker", ["projects.start"]),
        "Task",
        &key,
        change,
    ));
    assert_eq!(host.status("Project", "p1"), "active");
}

/// A linking type with no Workflow, or several, and a then's target with
/// none, are defects at run time: the build checks what reads another
/// type, and a run finds what a later version changed.
#[test]
fn a_linking_type_or_a_target_with_no_workflow_is_a_defect() {
    let plain = TypeSpec::new("Task").with_field(FieldSpec::relation(
        "projectId",
        "Identity.UUID",
        "project",
        "Project",
    ));
    let mut host = Host::new([project(project_rules()), plain]);
    host.put("Project", "p1", json!({}), "active", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "done", 1);
    let refusal = host
        .change(
            "Task",
            "t1",
            update(
                image(json!({"projectId": "p1"}), None),
                image(json!({"projectId": "p1", "title": "x"}), None),
            ),
        )
        .into_result()
        .expect_err("a defect");
    assert_eq!(refusal.code, Code::BehaviorError);
    assert_eq!(
        refusal.message,
        "behavior Reactions: allTerminal names Task, which does not compose Workflow, so its rows have no terminal state"
    );
    let bare = TypeSpec::new("Project").with_field(FieldSpec::new("name", "string"));
    let mut host = Host::new([bare, task(task_rules())]);
    host.put("Project", "p1", json!({}), "planned", 1);
    host.put("Task", "t1", json!({"projectId": "p1"}), "doing", 2);
    let (key, change) = task_moved("t1", "todo", "doing");
    let refusal = host
        .change("Task", &key, change)
        .into_result()
        .expect_err("a defect");
    assert_eq!(
        refusal.message,
        "behavior Reactions: a rule moves Project p1 to active, but Project does not compose Workflow"
    );
}

// ---- what it listens to ----

/// Reactions listens to the types its allTerminal and anyTerminal rules
/// name, through their links, each once in rule order.
#[test]
fn it_listens_to_the_types_its_terminal_rules_name() {
    let core = core().with(Freeze);
    assert_eq!(
        core.listeners(&project(project_rules()))
            .expect("its listeners"),
        [Listen::new("Task", "project")]
    );
    assert!(core
        .listeners(&task(task_rules()))
        .expect("none")
        .is_empty());
}

// ---- the config's rules ----

fn config_of(spec: &TypeSpec) -> ReactionsConfig {
    let index = spec
        .use_of("Reactions")
        .map(|(index, _)| index)
        .expect("a Reactions use");
    let raw = spec.uses[index].config_or_empty();
    Reactions
        .parse_config(&raw, &ConfigTarget::new(spec, index))
        .unwrap_or_else(|error| panic!("{error}"))
}

/// A config resolves the Workflow its rules read (the type's one, or the
/// one status names) and each field a changes rule names.
#[test]
fn a_config_resolves_its_workflow_and_its_fields() {
    let config = config_of(&task(task_rules()));
    assert_eq!(config.status.as_deref(), Some("status"));
    let field = config.field("priority").expect("priority");
    assert_eq!(
        (field.use_id.as_deref(), field.field.as_str()),
        (None, "priority")
    );
    // A project's rules read no Workflow of its own row but in their thens.
    assert_eq!(
        config_of(&project(project_rules())).status.as_deref(),
        Some("status")
    );
    let linked = json!({"rules": [{"when": {"changes": {"field": "project"}}, "then": {"link": "project", "transition": "active"}}]});
    let config = config_of(&task(linked));
    assert_eq!(config.status, None);
    assert_eq!(
        config.field("project").map(|field| field.field.as_str()),
        Some("projectId")
    );
}

/// Each rule the config breaks, in the core's words, the first reported.
#[test]
fn the_config_is_held_to_the_type() {
    let one = |when: Value, then: Value| json!({"rules": [{"when": when, "then": then}]});
    let done = json!({"transition": "done"});
    // status names no Workflow of the type.
    let mut config = task_rules();
    config["status"] = json!("phase");
    assert_eq!(
        rule(&task(config)),
        "status \"phase\" is not a Workflow of the type (its Workflows: status)"
    );
    // A changes rule's field.
    assert_eq!(
        rule(&task(one(json!({"changes": {"field": "colour"}}), done.clone()))),
        "rule 1: when.changes names colour, which is not a field of Task (title, priority, project, status)"
    );
    assert_eq!(
        rule(&task(one(json!({"changes": {"field": "Nope.x"}}), done.clone()))),
        "rule 1: when.changes names Nope.x, which is not a field of a behavior the type lists (status, Reactions)"
    );
    assert_eq!(
        rule(&task(one(json!({"changes": {"field": "status.colour"}}), done.clone()))),
        "rule 1: when.changes names status.colour, but Workflow declares no field colour (status, enteredAt)"
    );
    assert_eq!(
        rule(&task(one(json!({"changes": {"field": "status.enteredAt"}}), done.clone()))),
        "rule 1: when.changes names status.enteredAt, which every move of the status moves; a rule on status hears those"
    );
    let timed = task(one(
        json!({"changes": {"field": "Timers.deadlines.nudge.fired", "to": true}}),
        done.clone(),
    ))
    .with(UseSpec::on_type(
        "Timers",
        json!({"deadlines": {"nudge": {"start": {"create": true}}}}),
    ));
    assert_eq!(
        rule(&timed),
        "rule 1: when.changes names Timers.deadlines.nudge.fired, which Timers computes at a read and no column holds, so a write of the row does not carry it: a rule on it waits for a later step (D103)"
    );
    // holds and revised wait for step 4.
    assert_eq!(
        rule(&task(one(json!({"holds": "steps"}), done.clone()))),
        "rule 1: when.holds names rollup steps: a rule that hears a rollup waits for Rollups on the core (D103's order of the work, step 4)"
    );
    assert_eq!(
        rule(&task(one(json!({"revised": {"link": "spec"}}), done.clone()))),
        "rule 1: when.revised names link spec: a rule that hears a revision or a release waits for Revisions and Branches on the core (D103's order of the work, step 4)"
    );
    // A rule that reads the status of a type with no Workflow, or several.
    let bare = TypeSpec::new("Task").with(UseSpec::on_type(
        "Reactions",
        one(json!({"enters": "doing"}), done.clone()),
    ));
    assert_eq!(
        rule(&bare),
        "rule 1: when.enters reads the status of Workflow, which the type does not list"
    );
    let several = task(one(json!({"changes": {"field": "priority"}}), done.clone()))
        .with(UseSpec::on_field("phase", "Workflow", task_flow()));
    assert_eq!(
        rule(&several),
        "rule 1: then reads the status of Workflow, and the type has several (status, phase): status names the field whose Workflow it reads"
    );
    // The engine's check_rules, in its words.
    assert_eq!(
        rule(&task(one(json!({"enters": "review"}), done.clone()))),
        "rule 1: when.enters \"review\" is not a state of the type's Workflow (todo, doing, done, failed, dropped)"
    );
    assert_eq!(
        rule(&task(one(json!({"changes": {"field": "status", "to": "review"}}), done.clone()))),
        "rule 1: when.changes.to \"review\" is not a state of the type's Workflow (todo, doing, done, failed, dropped)"
    );
    assert_eq!(
        rule(&task(one(json!({"changes": {"field": "status", "from": 1}}), done.clone()))),
        "rule 1: when.changes.from 1 is not a state of the type's Workflow (todo, doing, done, failed, dropped)"
    );
    assert_eq!(
        rule(&task(one(
            json!({"allTerminal": {"schema": "Task", "link": "parent"}}),
            done.clone()
        ))),
        "rule 1: when.allTerminal names link parent of Task, which has no such link to Task"
    );
    assert_eq!(
        rule(&task(one(
            json!({"enters": "doing"}),
            json!({"link": "parent", "transition": "active"})
        ))),
        "rule 1: then.link parent is not a to-one relation of Task (project)"
    );
    assert_eq!(
        rule(&task(one(json!({"enters": "doing"}), json!({"transition": "shipped"})))),
        "rule 1: then.transition \"shipped\" is not a state of the type's Workflow (todo, doing, done, failed, dropped)"
    );
    assert_eq!(
        rule(&task(one(json!({"enters": "todo"}), done.clone()))),
        "rule 1: no transition of the type's Workflow leads from todo to done, so the rule could never move the instance"
    );
    assert_eq!(
        rule(&task(one(
            json!({"changes": {"field": "priority"}}),
            json!({"transition": "todo"})
        ))),
        "rule 1: no transition of the type's Workflow leads to todo, so the rule could never move the instance"
    );
}

/// Rules on the row itself that move it round its states for ever are
/// refused: enters rules, and a changes rule on the status, with to or
/// without it.
#[test]
fn a_cycle_of_rules_on_the_row_itself_is_refused() {
    let flow = json!({"states": ["open", "held"], "transitions": [{"from": "open", "to": "held"}, {"from": "held", "to": "open"}]});
    let spec = |rules: Value| {
        TypeSpec::new("Task")
            .with(UseSpec::on_field("status", "Workflow", flow.clone()))
            .with(UseSpec::on_type("Reactions", rules))
    };
    let cycle =
        "its rules on the instance itself move it round the states open, held, open for ever";
    assert_eq!(
        rule(&spec(json!({"rules": [
            {"when": {"enters": "open"}, "then": {"transition": "held"}},
            {"when": {"enters": "held"}, "then": {"transition": "open"}}
        ]}))),
        cycle
    );
    assert_eq!(
        rule(&spec(json!({"rules": [
            {"when": {"changes": {"field": "status", "to": "open"}}, "then": {"transition": "held"}},
            {"when": {"changes": {"field": "status"}}, "then": {"transition": "open"}}
        ]}))),
        cycle
    );
    assert!(define(&spec(json!({"rules": [
        {"when": {"enters": "open"}, "then": {"transition": "held"}}
    ]})))
    .is_ok());
}
