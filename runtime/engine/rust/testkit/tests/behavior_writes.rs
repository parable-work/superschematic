//! What a behavior writes and refuses, the cases of
//! `test/behavior-updates.test.ts` (an operation's update()),
//! `test/behavior-unchanged.test.ts` (an operation that says it changed
//! nothing), `test/behavior-validation.test.ts` (a behavior's validate and
//! checkType) and `test/behavior-refusals.test.ts` (veto codes and
//! preconditions); on SQLite and, with the variable set, on Postgres. A
//! guard's answer of the wrong shape and a validate that returns a promise
//! have no Rust counterpart: the types refuse them. The describe document
//! and the core behaviors' vetoes are milestones 6 and 8.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::errors::{ValidationIssue, Veto};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, ConfigTarget, CreateOptions, Engine, EngineErrorCode, Error,
    GuardRequest, Hooks, Principal, ReadEvents, Result, ValidationRequest, WriteOptions,
};
use superschematic_engine_testkit::behaviors::{Flag, Hold};
use support::{
    alice, code, defect, fields_of, issue, issues_of, named, publish, refusal, schema_document,
    targets, usage, validation_issues, veto,
};

type Seen = Arc<Mutex<Vec<Value>>>;

fn create(engine: &Engine, id: &str, data: Value) -> Result<superschematic_engine::InstanceRecord> {
    engine.instances().create(
        &alice(),
        "Item",
        &data,
        CreateOptions {
            id: Some(id),
            ..Default::default()
        },
    )
}

fn get(engine: &Engine, id: &str) -> Option<superschematic_engine::InstanceRecord> {
    engine
        .instances()
        .get(&alice(), "Item", id, Default::default())
        .unwrap()
}

fn invoke_with(
    engine: &Engine,
    id: &str,
    operation: &str,
    params: Value,
    options: WriteOptions<'_>,
) -> Result<Value> {
    engine
        .instances()
        .invoke(&alice(), "Item", id, operation, &params, options)
}

fn invoke(engine: &Engine, id: &str, operation: &str, params: Value) -> Result<Value> {
    invoke_with(engine, id, operation, params, WriteOptions::default())
}

fn item_events(engine: &Engine, id: &str) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Item"),
                instance_id: Some(id),
                ..Default::default()
            },
        )
        .unwrap()
        .events
}

// test.Editor changes the instance's own fields through update(); its
// guard vetoes a title of "forbidden", and it records what it is asked.
struct Editor {
    guards: Seen,
    changes: Seen,
    data: Seen,
}

const TITLE: &str = r#"{"type": "object", "additionalProperties": false, "required": ["title"], "properties": {"title": {"type": "string"}}}"#;

impl Behavior for Editor {
    type Config = Value;

    fn declaration(&self) -> &str {
        static DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
            let title: Value = serde_json::from_str(TITLE).unwrap();
            let none = json!({"type": "object", "additionalProperties": false});
            json!({
                "name": "test.Editor",
                "fields": [{"name": "renames"}],
                "operations": [
                    {"name": "rename", "paramsSchema": title, "resultSchema": {"type": "string"}, "writes": true},
                    {"name": "renameTwice", "paramsSchema": title, "resultSchema": true, "writes": true},
                    {"name": "dropNote", "paramsSchema": none, "resultSchema": true, "writes": true},
                    {"name": "setRenames", "paramsSchema": none, "resultSchema": true, "writes": true},
                    {"name": "setSize", "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"size": {}}}, "resultSchema": true, "writes": true},
                    {"name": "tryRename", "paramsSchema": title, "resultSchema": {"type": "string"}, "writes": true},
                    {"name": "renameThenFail", "paramsSchema": title, "resultSchema": true, "writes": true},
                    {"name": "check", "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"patch": {"type": "object"}}}, "resultSchema": true},
                    {"name": "renameInRead", "paramsSchema": title, "resultSchema": true},
                    {"name": "notAPatch", "paramsSchema": none, "resultSchema": true, "writes": true}
                ]
            })
            .to_string()
        });
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "renames")
                .column("renames", ColumnSpec::integer().not_null().default(0))],
            guard: Some(|editor, _, request| {
                editor.guards.lock().unwrap().push(request.to_json());
                if let GuardRequest::Update { after, .. } = request {
                    if after.get("title") == Some(&json!("forbidden")) {
                        return Ok(Some(Veto::from("that title is forbidden")));
                    }
                }
                Ok(None)
            }),
            operations: vec![
                ("rename", |editor, cx, params| {
                    let after = cx.update(&json!({"title": params["title"]}))?;
                    let renames = cx.columns().get()?.i64("renames")?;
                    cx.columns()
                        .set(&[("renames", SqlValue::Integer(renames + 1))])?;
                    editor
                        .data
                        .lock()
                        .unwrap()
                        .push(Value::Object((*cx.data()?).clone()));
                    Ok(after["title"].clone())
                }),
                ("renameTwice", |_, cx, params| {
                    cx.update(&json!({"title": "interim"}))?;
                    Ok(Value::Object(
                        (*cx.update(&json!({"title": params["title"]}))?).clone(),
                    ))
                }),
                ("dropNote", |_, cx, _| {
                    Ok(Value::Object((*cx.update(&json!({"note": null}))?).clone()))
                }),
                ("setRenames", |_, cx, _| {
                    Ok(Value::Object((*cx.update(&json!({"renames": 9}))?).clone()))
                }),
                ("setSize", |_, cx, params| {
                    Ok(Value::Object(
                        (*cx.update(&json!({"size": params["size"]}))?).clone(),
                    ))
                }),
                ("tryRename", |_, cx, params| {
                    // The savepoint takes the rename back with the call.
                    let _ = cx.call(
                        "test.Editor",
                        "renameThenFail",
                        &json!({"title": params["title"]}),
                    );
                    Ok(cx.data()?["title"].clone())
                }),
                ("renameThenFail", |_, cx, params| {
                    cx.update(&json!({"title": params["title"]}))?;
                    Err(Error::Internal("after the rename".into()))
                }),
                ("check", |_, cx, params| {
                    let patch = params.get("patch").cloned().unwrap_or(json!({}));
                    Ok(serde_json::to_value(cx.validate_update(&patch)?).unwrap())
                }),
                ("renameInRead", |_, cx, params| {
                    Ok(Value::Object(
                        (*cx.update(&json!({"title": params["title"]}))?).clone(),
                    ))
                }),
                ("notAPatch", |_, cx, _| {
                    Ok(Value::Object((*cx.update(&json!([1]))?).clone()))
                }),
            ],
            fields: vec![("renames", |_, view| {
                Ok(Some(Value::from(view.columns().get()?.i64("renames")?)))
            })],
            after_change: Some(|editor, _, change| {
                editor.changes.lock().unwrap().push(change.to_json());
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

// test.Lock refuses an update a behavior applies, and lets a caller's
// through.
struct Lock;

impl Behavior for Lock {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Lock"}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guard: Some(|_, _, request| {
                Ok(match request {
                    GuardRequest::Update {
                        caller: Some(caller),
                        ..
                    } => Some(Veto::from(format!("no update from {caller}").as_str())),
                    _ => None,
                })
            }),
            ..Hooks::default()
        }
    }
}

struct Editing {
    engine: Engine,
    guards: Seen,
    changes: Seen,
    data: Seen,
}

fn editing(target: &support::Target, behaviors: Value) -> Editing {
    let (guards, changes, data): (Seen, Seen, Seen) = Default::default();
    let engine = target.open(
        BehaviorSet::new()
            .with(Editor {
                guards: Arc::clone(&guards),
                changes: Arc::clone(&changes),
                data: Arc::clone(&data),
            })
            .with(Flag)
            .with(Lock),
    );
    support::publish_item_with(
        &engine,
        behaviors,
        json!([
            {"name": "note", "typeRef": {"name": "string"}},
            {"name": "size", "typeRef": {"name": "number"}, "validateMin": 1}
        ]),
    );
    create(&engine, "a", json!({"title": "Desk", "note": "oak"})).unwrap();
    guards.lock().unwrap().clear();
    changes.lock().unwrap().clear();
    Editing {
        engine,
        guards,
        changes,
        data,
    }
}

// replay applies an instance's events in order to its create's instance.
fn replay(engine: &Engine, id: &str) -> Value {
    let mut instance = json!({});
    for event in item_events(engine, id) {
        if event.kind == "create" {
            instance = event.change;
        } else {
            let patch = if event.kind == "operation" {
                event.change["patch"].clone()
            } else {
                event.change
            };
            instance = superschematic_engine::instances::patch::merge_patch(&instance, &patch);
        }
    }
    instance
}

#[test]
fn update_changes_the_own_fields_asks_every_guard_and_the_event_and_after_change_carry_it() {
    for target in targets("update_changes_the_own_fields") {
        let world = editing(&target, json!([{"name": "test.Editor"}]));
        let engine = &world.engine;
        assert_eq!(
            invoke(engine, "a", "rename", json!({"title": "Table"})).unwrap(),
            json!("Table")
        );
        let instance = get(engine, "a").unwrap();
        assert_eq!(
            fields_of(&instance),
            json!({"data": {"title": "Table", "note": "oak"}, "behaviors": {"test.Editor": {"renames": 1}}})
        );
        assert_eq!(instance.seq, 2);
        // The handler's context reads the fields as update() left them.
        assert_eq!(
            *world.data.lock().unwrap(),
            [json!({"title": "Table", "note": "oak"})]
        );
        assert_eq!(
            *world.guards.lock().unwrap(),
            [
                json!({"kind": "operation", "behavior": "test.Editor", "operation": "rename", "params": {"title": "Table"}, "writes": true}),
                json!({"kind": "update", "patch": {"title": "Table"}, "after": {"title": "Table", "note": "oak"}, "caller": "test.Editor"}),
            ]
        );
        assert_eq!(
            *world.changes.lock().unwrap(),
            [
                json!({"kind": "operation", "behavior": "test.Editor", "operation": "rename", "params": {"title": "Table"}, "before": {"title": "Desk", "note": "oak"}})
            ]
        );
        assert_eq!(
            item_events(engine, "a").last().unwrap().change,
            json!({"behavior": "test.Editor", "operation": "rename", "params": {"title": "Table"}, "patch": {"data": {"title": "Table"}, "behaviors": {"test.Editor": {"renames": 1}}}})
        );
        assert_eq!(replay(engine, "a"), fields_of(&instance));
    }
}

#[test]
fn two_updates_in_one_operation_are_one_change_and_a_patch_that_changes_nothing_writes_nothing() {
    for target in targets("two_updates_in_one_operation_are_one_change") {
        let world = editing(&target, json!([{"name": "test.Editor"}]));
        let engine = &world.engine;
        invoke(engine, "a", "renameTwice", json!({"title": "Table"})).unwrap();
        invoke(engine, "a", "dropNote", json!({})).unwrap();
        invoke(engine, "a", "dropNote", json!({})).unwrap();
        let events = item_events(engine, "a");
        assert_eq!(
            events[events.len() - 3..]
                .iter()
                .map(|event| event.change["patch"].clone())
                .collect::<Vec<_>>(),
            [
                json!({"data": {"title": "Table"}}),
                json!({"data": {"note": null}}),
                json!({})
            ]
        );
        assert_eq!(
            world
                .changes
                .lock()
                .unwrap()
                .iter()
                .map(|change| change.get("before").cloned())
                .collect::<Vec<_>>(),
            [
                Some(json!({"title": "Desk", "note": "oak"})),
                Some(json!({"title": "Table", "note": "oak"})),
                None
            ]
        );
        let fields =
            json!({"data": {"title": "Table"}, "behaviors": {"test.Editor": {"renames": 0}}});
        assert_eq!(fields_of(&get(engine, "a").unwrap()), fields);
        assert_eq!(replay(engine, "a"), fields);
    }
}

#[test]
fn update_refuses_a_behavior_fields_name_a_refused_result_and_a_veto_and_writes_nothing() {
    for target in targets("update_refuses_a_behavior_fields_name") {
        let world = editing(
            &target,
            json!([{"name": "test.Editor"}, {"name": "test.Flag"}]),
        );
        let engine = &world.engine;
        let unknown = refusal(invoke(engine, "a", "setRenames", json!({})));
        assert_eq!(
            validation_issues(&unknown),
            json!([{"path": "renames", "rule": "unknown", "message": "Item has no field renames"}])
        );
        let invalid = refusal(invoke(engine, "a", "setSize", json!({"size": 0})));
        assert_eq!(validation_issues(&invalid)[0]["path"], "size");
        assert_eq!(validation_issues(&invalid)[0]["rule"], "min");
        let (behavior, action, reason, ..) =
            veto(invoke(engine, "a", "rename", json!({"title": "forbidden"})));
        assert_eq!(
            (behavior.as_str(), action.as_str(), reason.as_str()),
            ("test.Editor", "update", "that title is forbidden")
        );
        // test.Flag holds a flagged instance still: it vetoes the operation.
        invoke(engine, "a", "flag", json!({"reason": "audit"})).unwrap();
        let (behavior, action, ..) = veto(invoke(engine, "a", "rename", json!({"title": "Table"})));
        assert_eq!(
            (behavior.as_str(), action.as_str()),
            ("test.Flag", "rename")
        );
        let instance = get(engine, "a").unwrap();
        assert_eq!(
            fields_of(&instance),
            json!({"data": {"title": "Desk", "note": "oak"}, "behaviors": {"test.Editor": {"renames": 0}, "test.Flag": {"flagged": true, "flagReason": "audit"}}})
        );
        assert_eq!(instance.seq, 2);
    }
}

#[test]
fn another_behaviors_guard_is_asked_for_the_update_and_sees_which_behavior_applies_it() {
    for target in targets("another_behaviors_guard_is_asked_for_the_update") {
        let world = editing(
            &target,
            json!([{"name": "test.Editor"}, {"name": "test.Lock"}]),
        );
        let engine = &world.engine;
        let (behavior, action, reason, ..) =
            veto(invoke(engine, "a", "rename", json!({"title": "Table"})));
        assert_eq!(
            (behavior.as_str(), action.as_str(), reason.as_str()),
            ("test.Lock", "update", "no update from test.Editor")
        );
        assert_eq!(get(engine, "a").unwrap().data["title"], "Desk");
        // A caller's own update names no behavior, and passes.
        assert_eq!(
            engine
                .instances()
                .update(
                    &alice(),
                    "Item",
                    "a",
                    &json!({"title": "Table"}),
                    WriteOptions::default()
                )
                .unwrap()
                .data["title"],
            "Table"
        );
    }
}

#[test]
fn a_called_operation_that_fails_takes_its_update_back_with_its_savepoint() {
    for target in targets("a_called_operation_that_fails_takes_its_update_back") {
        let world = editing(&target, json!([{"name": "test.Editor"}]));
        let engine = &world.engine;
        assert_eq!(
            invoke(engine, "a", "tryRename", json!({"title": "Table"})).unwrap(),
            json!("Desk")
        );
        assert_eq!(
            fields_of(&get(engine, "a").unwrap()),
            json!({"data": {"title": "Desk", "note": "oak"}, "behaviors": {"test.Editor": {"renames": 0}}})
        );
        let changes = world.changes.lock().unwrap();
        assert_eq!(changes.len(), 1);
        assert_eq!(changes[0].get("before"), None);
    }
}

#[test]
fn validate_update_reports_without_writing_and_a_read_only_operation_cannot_update() {
    for target in targets("validate_update_reports_without_writing") {
        let world = editing(&target, json!([{"name": "test.Editor"}]));
        let engine = &world.engine;
        assert_eq!(
            invoke(engine, "a", "check", json!({"patch": {"title": "Table"}})).unwrap(),
            json!([])
        );
        assert_eq!(
            invoke(
                engine,
                "a",
                "check",
                json!({"patch": {"size": 0, "renames": 1}})
            )
            .unwrap(),
            json!([
                {"path": "renames", "rule": "unknown", "message": "Item has no field renames"},
                {"path": "size", "rule": "min", "message": "size must be at least 1."}
            ])
        );
        let required = invoke(engine, "a", "check", json!({"patch": {"title": null}})).unwrap();
        assert_eq!(required[0]["path"], "title");
        assert_eq!(required[0]["rule"], "required");
        assert!(defect(invoke(
            engine,
            "a",
            "renameInRead",
            json!({"title": "Table"})
        ))
        .1
        .contains("read-only operation cannot update"));
        assert!(defect(invoke(engine, "a", "notAPatch", json!({})))
            .1
            .contains("a JSON object"));
        assert_eq!(
            fields_of(&get(engine, "a").unwrap()),
            json!({"data": {"title": "Desk", "note": "oak"}, "behaviors": {"test.Editor": {"renames": 0}}})
        );
    }
}

// test.Toggle holds a switch: set says it changed nothing when the switch
// is already where it is asked to be; the others say so when they wrote.
struct Toggle {
    changes: Seen,
}

impl Behavior for Toggle {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{
            "name": "test.Toggle",
            "fields": [{"name": "on"}],
            "operations": [
                {"name": "set", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["on"], "properties": {"on": {"type": "boolean"}}}, "resultSchema": true, "writes": true},
                {"name": "setAndClaim", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["on"], "properties": {"on": {"type": "boolean"}}}, "resultSchema": true, "writes": true},
                {"name": "retitleQuietly", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["title"], "properties": {"title": {"type": "string"}}}, "resultSchema": true, "writes": true},
                {"name": "relay", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["on"], "properties": {"on": {"type": "boolean"}}}, "resultSchema": true, "writes": true},
                {"name": "quietRelay", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["on"], "properties": {"on": {"type": "boolean"}}}, "resultSchema": true, "writes": true},
                {"name": "peek", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true}
            ]
        }"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "on")
                .column("on", ColumnSpec::integer().not_null().default(0))],
            operations: vec![
                ("set", |_, cx, params| {
                    let on = params["on"] == json!(true);
                    if (cx.columns().get()?.i64("on")? == 1) == on {
                        cx.unchanged();
                        return Ok(json!({"on": on, "changed": false}));
                    }
                    cx.columns()
                        .set(&[("on", SqlValue::Integer(i64::from(on)))])?;
                    Ok(json!({"on": on, "changed": true}))
                }),
                ("setAndClaim", |_, cx, params| {
                    cx.columns().set(&[(
                        "on",
                        SqlValue::Integer(i64::from(params["on"] == json!(true))),
                    )])?;
                    cx.unchanged();
                    Ok(json!({}))
                }),
                ("retitleQuietly", |_, cx, params| {
                    cx.update(&json!({"title": params["title"]}))?;
                    cx.unchanged();
                    Ok(cx.data()?["title"].clone())
                }),
                ("relay", |_, cx, params| {
                    cx.call("test.Toggle", "set", &json!({"on": params["on"]}))
                }),
                ("quietRelay", |_, cx, params| {
                    let result = cx.call("test.Toggle", "set", &json!({"on": params["on"]}))?;
                    cx.unchanged();
                    Ok(result)
                }),
                ("peek", |_, cx, _| {
                    cx.unchanged();
                    Ok(Value::Bool(cx.columns().get()?.i64("on")? == 1))
                }),
            ],
            fields: vec![("on", |_, view| {
                Ok(Some(Value::Bool(view.columns().get()?.i64("on")? == 1)))
            })],
            after_change: Some(|toggle, _, change| {
                toggle.changes.lock().unwrap().push(change.to_json());
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

fn toggling(target: &support::Target) -> (Engine, Seen) {
    let changes: Seen = Arc::default();
    let engine = target.open_with(
        BehaviorSet::new().with(Toggle {
            changes: Arc::clone(&changes),
        }),
        |options| options.clock = Clock::Manual(1_000),
    );
    support::publish_item(&engine, json!([{"name": "test.Toggle"}]));
    create(&engine, "a", json!({"title": "Lamp"})).unwrap();
    changes.lock().unwrap().clear();
    (engine, changes)
}

fn operate(engine: &Engine, who: &Principal, operation: &str, params: Value) -> Value {
    serde_json::to_value(
        engine
            .instances()
            .operate(
                who,
                "Item",
                "a",
                operation,
                &params,
                WriteOptions::default(),
            )
            .unwrap(),
    )
    .unwrap()
}

#[test]
fn unchanged_appends_no_event_and_keeps_seq_updated_at_and_updated_by_and_no_after_change_runs() {
    for target in targets("unchanged_appends_no_event") {
        let (engine, changes) = toggling(&target);
        engine.clock().set(2_000).unwrap();
        assert_eq!(
            operate(&engine, &alice(), "set", json!({"on": true})),
            json!({"result": {"on": true, "changed": true}, "seq": 2})
        );
        assert_eq!(changes.lock().unwrap().len(), 1);
        engine.clock().set(3_000).unwrap();
        assert_eq!(
            operate(&engine, &named("bob"), "set", json!({"on": true})),
            json!({"result": {"on": true, "changed": false}, "seq": 2})
        );
        let instance = get(&engine, "a").unwrap();
        assert_eq!(
            (
                instance.seq,
                instance.updated_at,
                instance.updated_by.as_str()
            ),
            (2, 2_000, "alice")
        );
        assert_eq!(item_events(&engine, "a").len(), 2);
        assert_eq!(changes.lock().unwrap().len(), 1);
    }
}

#[test]
fn unchanged_with_a_stale_expected_seq_is_still_refused_before_the_handler() {
    for target in targets("unchanged_with_a_stale_expected_seq") {
        let (engine, _) = toggling(&target);
        invoke(&engine, "a", "set", json!({"on": true})).unwrap();
        assert_eq!(
            code(invoke_with(
                &engine,
                "a",
                "set",
                json!({"on": true}),
                WriteOptions {
                    expected_seq: Some(1),
                    ..Default::default()
                }
            )),
            EngineErrorCode::SeqMismatch
        );
    }
}

#[test]
fn a_call_that_wrote_and_says_it_changed_nothing_is_a_behavior_error_and_rolls_back() {
    for target in targets("a_call_that_wrote_and_says_it_changed_nothing") {
        let (engine, _) = toggling(&target);
        assert!(
            defect(invoke(&engine, "a", "setAndClaim", json!({"on": true})))
                .1
                .contains("said it changed nothing")
        );
        assert_eq!(
            get(&engine, "a").unwrap().behaviors["test.Toggle"]["on"],
            false
        );
        assert_eq!(item_events(&engine, "a").len(), 1);
    }
}

#[test]
fn an_update_that_changed_the_own_fields_counts_as_a_write_and_one_that_changed_nothing_does_not() {
    for target in targets("an_update_that_changed_the_own_fields_counts_as_a_write") {
        let (engine, _) = toggling(&target);
        defect(invoke(
            &engine,
            "a",
            "retitleQuietly",
            json!({"title": "Desk lamp"}),
        ));
        assert_eq!(get(&engine, "a").unwrap().data["title"], "Lamp");
        assert_eq!(
            invoke(&engine, "a", "retitleQuietly", json!({"title": "Lamp"})).unwrap(),
            json!("Lamp")
        );
        assert_eq!(item_events(&engine, "a").len(), 1);
    }
}

#[test]
fn a_called_operation_that_changed_nothing_leaves_its_caller_to_say_so() {
    for target in targets("a_called_operation_that_changed_nothing") {
        let (engine, _) = toggling(&target);
        invoke(&engine, "a", "relay", json!({"on": false})).unwrap();
        assert_eq!(item_events(&engine, "a").len(), 2);
        assert_eq!(get(&engine, "a").unwrap().seq, 2);
        assert_eq!(
            operate(&engine, &alice(), "quietRelay", json!({"on": false})),
            json!({"result": {"on": false, "changed": false}, "seq": 2})
        );
        assert_eq!(item_events(&engine, "a").len(), 2);
        defect(invoke(&engine, "a", "quietRelay", json!({"on": true})));
        assert_eq!(
            get(&engine, "a").unwrap().behaviors["test.Toggle"]["on"],
            false
        );
        assert_eq!(
            operate(&engine, &alice(), "peek", json!({})),
            json!({"result": false, "seq": 2})
        );
    }
}

#[derive(Clone, Default)]
struct ShapeConfig {
    ban: Option<String>,
    permission: Option<String>,
    kind: Option<String>,
}

// test.Shape refuses a title that holds the word its config bans, unless
// the caller holds its permission, and holds extra to the type its config
// names.
struct Shape {
    seen: Seen,
}

impl Behavior for Shape {
    type Config = ShapeConfig;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Shape", "configSchema": {"type": "object", "additionalProperties": false, "properties": {"ban": {"type": "string"}, "permission": {"type": "string"}, "type": {"type": "string"}}}}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<ShapeConfig> {
        Ok(ShapeConfig {
            ban: raw["ban"].as_str().map(str::to_owned),
            permission: raw["permission"].as_str().map(str::to_owned),
            kind: raw["type"].as_str().map(str::to_owned),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            checked_types: Some(|_, config| config.kind.iter().cloned().collect()),
            validate: Some(|shape, cx, request| {
                let (data, recorded) = match request {
                    ValidationRequest::Create { data } => {
                        (*data, json!({"kind": "create", "data": data}))
                    }
                    ValidationRequest::Update {
                        before,
                        after,
                        caller,
                    } => {
                        let mut recorded =
                            json!({"kind": "update", "before": before, "after": after});
                        if let Some(caller) = caller {
                            recorded["caller"] = json!(caller);
                        }
                        (*after, recorded)
                    }
                };
                shape
                    .seen
                    .lock()
                    .unwrap()
                    .push(json!({"step": "validate", "request": recorded}));
                let mut issues = Vec::new();
                let config = cx.config();
                if let Some(ban) = &config.ban {
                    let title = data["title"].as_str().unwrap_or_default();
                    let excused = match &config.permission {
                        Some(permission) => cx.can(permission)?,
                        None => false,
                    };
                    if title.contains(ban.as_str()) && !excused {
                        issues.push(ValidationIssue::new(
                            "title",
                            "banned",
                            format!("title holds {ban}"),
                        ));
                    }
                }
                if let (Some(kind), Some(extra)) = (&config.kind, data.get("extra")) {
                    issues.extend(cx.check_type(kind, extra, "extra")?);
                }
                Ok(issues)
            }),
            guard: Some(|shape, _, request| {
                let kind = request.to_json()["kind"].clone();
                shape
                    .seen
                    .lock()
                    .unwrap()
                    .push(json!({"step": "guard", "kind": kind}));
                Ok(None)
            }),
            ..Hooks::default()
        }
    }
}

// test.Second refuses the title "two", after test.Shape in list order.
struct Second;

impl Behavior for Second {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Second"}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            validate: Some(|_, _, request| {
                let data = match request {
                    ValidationRequest::Create { data } => *data,
                    ValidationRequest::Update { after, .. } => *after,
                };
                Ok(
                    if data["title"].as_str().unwrap_or_default().contains("two") {
                        vec![ValidationIssue::new("title", "second", "title holds two")]
                    } else {
                        Vec::new()
                    },
                )
            }),
            ..Hooks::default()
        }
    }
}

// test.Writer writes the instance's fields through update(), reports what
// validateUpdate() says, and creates an Item.
struct Writer;

impl Behavior for Writer {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Writer", "operations": [
            {"name": "write", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["patch"], "properties": {"patch": {"type": "object"}}}, "resultSchema": true, "writes": true},
            {"name": "check", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["patch"], "properties": {"patch": {"type": "object"}}}, "resultSchema": true},
            {"name": "make", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["id", "title"], "properties": {"id": {"type": "string"}, "title": {"type": "string"}}}, "resultSchema": true, "writes": true}
        ]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![
                ("write", |_, cx, params| {
                    Ok(Value::Object((*cx.update(&params["patch"])?).clone()))
                }),
                ("check", |_, cx, params| {
                    Ok(
                        json!({"issues": cx.validate_update(&params["patch"])?, "title": cx.data()?["title"]}),
                    )
                }),
                ("make", |_, cx, params| {
                    let id = params["id"].as_str().unwrap_or_default();
                    match cx.instances().create(
                        "Item",
                        &json!({"title": params["title"]}),
                        Some(id),
                        None,
                    ) {
                        Ok(made) => Ok(Value::from(made.id)),
                        Err(Error::Engine(error))
                            if error.code() == EngineErrorCode::InvalidInstance =>
                        {
                            Ok(json!({"refused": error.details().unwrap()["issues"]}))
                        }
                        Err(error) => Err(error),
                    }
                }),
            ],
            ..Hooks::default()
        }
    }
}

// test.Defect's validate breaks the contract as its config says.
struct Defect;

impl Behavior for Defect {
    type Config = String;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Defect", "configSchema": {"type": "object", "additionalProperties": false, "required": ["mode"], "properties": {"mode": {"type": "string"}}}}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<String> {
        Ok(raw["mode"].as_str().unwrap_or_default().to_owned())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            checked_types: Some(|_, _| vec!["Inner".to_owned()]),
            validate: Some(|_, cx, _| match cx.config().as_str() {
                "shape" => Ok(vec![ValidationIssue::new("title", "", "no rule")]),
                "type" => cx.check_type("Shape", &json!({}), "extra"),
                "veto" => Err(Error::veto(
                    cx.behavior(),
                    "create",
                    cx.schema(),
                    cx.id(),
                    Veto {
                        reason: "no".into(),
                        code: Some("nope".into()),
                        details: None,
                    },
                )),
                _ => Ok(Vec::new()),
            }),
            ..Hooks::default()
        }
    }
}

fn shapes_document(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Item",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "extra", "typeRef": {"name": "Generic.JSON"}}
        ]),
        json!({"types": {
            "Shape": {"name": "Shape", "role": "EmbeddedStruct", "fields": [
                {"name": "size", "typeRef": {"name": "number"}, "required": true},
                {"name": "tags", "typeRef": {"name": "string", "isArray": true}},
                {"name": "inner", "typeRef": {"name": "Inner"}}
            ]},
            "Inner": {"name": "Inner", "role": "EmbeddedStruct", "fields": [{"name": "name", "typeRef": {"name": "string"}, "required": true}]}
        }}),
    );
    document["types"]["Item"]["behaviors"] = behaviors;
    document
}

struct Validating {
    engine: Engine,
    seen: Seen,
    hold: Hold,
}

fn validating(target: &support::Target) -> Validating {
    let seen: Seen = Arc::default();
    let hold = Hold::default();
    let engine = target.open(
        BehaviorSet::new()
            .with(Shape {
                seen: Arc::clone(&seen),
            })
            .with(Second)
            .with(Writer)
            .with(Defect)
            .with(hold.clone()),
    );
    Validating { engine, seen, hold }
}

fn steps(seen: &Seen) -> Vec<Value> {
    seen.lock()
        .unwrap()
        .iter()
        .map(|entry| entry["step"].clone())
        .collect()
}

#[test]
fn validate_refuses_a_create_the_live_version_accepts_before_any_guard_and_writes_nothing() {
    for target in targets("validate_refuses_a_create") {
        let world = validating(&target);
        let engine = &world.engine;
        publish(
            engine,
            &shapes_document(
                json!([{"name": "test.Shape", "config": {"ban": "secret"}}, {"name": "test.Second"}]),
            ),
            None,
        );
        let refused = refusal(create(engine, "i1", json!({"title": "a secret two"})));
        assert_eq!(refused.code(), EngineErrorCode::InvalidInstance);
        assert_eq!(
            validation_issues(&refused),
            json!([
                {"path": "title", "rule": "banned", "message": "title holds secret"},
                {"path": "title", "rule": "second", "message": "title holds two"}
            ])
        );
        assert_eq!(
            refused.message(),
            "Item in namespace default (version 1): title: title holds secret; title: title holds two"
        );
        assert_eq!(
            *world.seen.lock().unwrap(),
            [
                json!({"step": "validate", "request": {"kind": "create", "data": {"title": "a secret two"}}})
            ]
        );
        assert!(get(engine, "i1").is_none());
        world.seen.lock().unwrap().clear();
        create(engine, "i1", json!({"title": "plain"})).unwrap();
        assert_eq!(steps(&world.seen), [json!("validate"), json!("guard")]);
    }
}

#[test]
fn the_live_versions_issues_come_first_and_validate_is_asked_only_about_accepted_fields() {
    for target in targets("the_live_versions_issues_come_first") {
        let world = validating(&target);
        publish(
            &world.engine,
            &shapes_document(json!([{"name": "test.Shape", "config": {"ban": "secret"}}])),
            None,
        );
        let refused = refusal(world.engine.instances().create(
            &alice(),
            "Item",
            &json!({"title": 7}),
            Default::default(),
        ));
        assert_eq!(validation_issues(&refused)[0]["rule"], "type");
        assert!(world.seen.lock().unwrap().is_empty());
    }
}

#[test]
fn an_update_asks_validate_with_the_fields_before_and_after_unless_can_lets_the_caller_through() {
    for target in targets("an_update_asks_validate") {
        let world = validating(&target);
        let engine = &world.engine;
        publish(
            engine,
            &shapes_document(
                json!([{"name": "test.Shape", "config": {"ban": "secret", "permission": "items.edit"}}]),
            ),
            None,
        );
        create(engine, "i1", json!({"title": "plain", "extra": {"a": 1}})).unwrap();
        world.seen.lock().unwrap().clear();
        let refused = refusal(engine.instances().update(
            &alice(),
            "Item",
            "i1",
            &json!({"title": "the secret", "extra": {"b": 2}}),
            WriteOptions::default(),
        ));
        assert_eq!(
            validation_issues(&refused),
            json!([{"path": "title", "rule": "banned", "message": "title holds secret"}])
        );
        assert_eq!(
            *world.seen.lock().unwrap(),
            [
                json!({"step": "validate", "request": {"kind": "update", "before": {"title": "plain", "extra": {"a": 1}}, "after": {"title": "the secret", "extra": {"a": 1, "b": 2}}}})
            ]
        );
        assert_eq!(get(engine, "i1").unwrap().seq, 1);
        let editor = Principal::new("eve", ["items.edit"]);
        assert_eq!(
            engine
                .instances()
                .update(
                    &editor,
                    "Item",
                    "i1",
                    &json!({"title": "the secret"}),
                    WriteOptions::default()
                )
                .unwrap()
                .data["title"],
            "the secret"
        );
    }
}

#[test]
fn an_operations_update_asks_validate_with_its_behavior_as_caller_and_validate_update_reports_the_same(
) {
    for target in targets("an_operations_update_asks_validate") {
        let world = validating(&target);
        let engine = &world.engine;
        publish(
            engine,
            &shapes_document(
                json!([{"name": "test.Shape", "config": {"ban": "secret", "type": "Shape"}}, {"name": "test.Writer"}]),
            ),
            None,
        );
        create(engine, "i1", json!({"title": "plain"})).unwrap();
        world.seen.lock().unwrap().clear();
        let patch = json!({"title": "secret", "extra": {"size": "big"}});
        let refused = refusal(invoke(engine, "i1", "write", json!({"patch": patch})));
        let found = validation_issues(&refused);
        assert_eq!(
            found
                .as_array()
                .unwrap()
                .iter()
                .map(|issue| json!([issue["path"], issue["rule"]]))
                .collect::<Vec<_>>(),
            [json!(["title", "banned"]), json!(["extra.size", "type"])]
        );
        assert!(world
            .seen
            .lock()
            .unwrap()
            .iter()
            .any(|entry| entry["request"]["caller"] == "test.Writer"));
        world.seen.lock().unwrap().clear();
        assert_eq!(
            invoke(engine, "i1", "check", json!({"patch": patch})).unwrap(),
            json!({"issues": found, "title": "plain"})
        );
        assert_eq!(steps(&world.seen), [json!("guard"), json!("validate")]);
        assert_eq!(get(engine, "i1").unwrap().seq, 1);
        assert_eq!(
            invoke(
                engine,
                "i1",
                "write",
                json!({"patch": {"extra": {"size": 2}}})
            )
            .unwrap(),
            json!({"title": "plain", "extra": {"size": 2}})
        );
    }
}

#[test]
fn an_updates_preconditions_are_checked_with_its_arguments_and_judged_by_their_guard_after_every_validate(
) {
    for target in targets("an_updates_preconditions_are_checked_with_its_arguments") {
        let world = validating(&target);
        let engine = &world.engine;
        publish(
            engine,
            &shapes_document(
                json!([{"name": "test.Shape", "config": {"ban": "secret"}}, {"name": "test.Hold"}, {"name": "test.Writer"}]),
            ),
            None,
        );
        create(engine, "i1", json!({"title": "plain"})).unwrap();
        let at = |generation: i64| json!({"test.Hold": {"generation": generation}});
        let zero = at(0);
        invoke_with(
            engine,
            "i1",
            "advance",
            json!({}),
            WriteOptions {
                preconditions: Some(&zero),
                ..Default::default()
            },
        )
        .unwrap();
        world.seen.lock().unwrap().clear();
        world.hold.take();
        let malformed = json!({"test.Hold": {}});
        let refused = refusal(engine.instances().update(
            &alice(),
            "Item",
            "i1",
            &json!({"title": "secret"}),
            WriteOptions {
                preconditions: Some(&malformed),
                ..Default::default()
            },
        ));
        assert_eq!(
            (refused.code(), refused.name()),
            (EngineErrorCode::InvalidArgument, "PreconditionsError")
        );
        assert!(world.seen.lock().unwrap().is_empty() && world.hold.take().is_empty());
        let stale = WriteOptions {
            preconditions: Some(&zero),
            ..Default::default()
        };
        let banned = refusal(engine.instances().update(
            &alice(),
            "Item",
            "i1",
            &json!({"title": "secret"}),
            stale,
        ));
        assert_eq!(validation_issues(&banned)[0]["rule"], "banned");
        assert_eq!(steps(&world.seen), [json!("validate")]);
        assert!(world.hold.take().is_empty());
        world.seen.lock().unwrap().clear();
        let (behavior, _, _, veto_code, _) = veto(engine.instances().update(
            &alice(),
            "Item",
            "i1",
            &json!({"title": "fine"}),
            stale,
        ));
        assert_eq!(
            (behavior.as_str(), veto_code.as_deref()),
            ("test.Hold", Some("stale"))
        );
        assert_eq!(steps(&world.seen), [json!("validate"), json!("guard")]);
        assert_eq!(get(engine, "i1").unwrap().data["title"], "plain");
        world.seen.lock().unwrap().clear();
        world.hold.take();
        let one = at(1);
        invoke_with(
            engine,
            "i1",
            "write",
            json!({"patch": {"title": "better"}}),
            WriteOptions {
                preconditions: Some(&one),
                ..Default::default()
            },
        )
        .unwrap();
        assert_eq!(
            steps(&world.seen),
            [json!("guard"), json!("validate"), json!("guard")]
        );
        assert_eq!(
            world
                .hold
                .take()
                .iter()
                .map(|request| json!([request["kind"], request.get("precondition")]))
                .collect::<Vec<_>>(),
            [
                json!(["operation", {"generation": 1}]),
                json!(["update", null])
            ]
        );
        assert_eq!(get(engine, "i1").unwrap().data["title"], "better");
    }
}

#[test]
fn a_behaviors_instances_create_asks_validate_too() {
    for target in targets("a_behaviors_instances_create_asks_validate_too") {
        let world = validating(&target);
        let engine = &world.engine;
        publish(
            engine,
            &shapes_document(
                json!([{"name": "test.Shape", "config": {"ban": "secret"}}, {"name": "test.Writer"}]),
            ),
            None,
        );
        create(engine, "m1", json!({"title": "maker"})).unwrap();
        assert_eq!(
            invoke(
                engine,
                "m1",
                "make",
                json!({"id": "i1", "title": "my secret"})
            )
            .unwrap(),
            json!({"refused": [{"path": "title", "rule": "banned", "message": "title holds secret"}]})
        );
        assert!(get(engine, "i1").is_none());
        assert_eq!(
            invoke(engine, "m1", "make", json!({"id": "i2", "title": "open"})).unwrap(),
            json!("i2")
        );
    }
}

#[test]
fn check_type_holds_a_value_to_a_type_its_checked_types_names_as_a_field_of_the_type() {
    for target in targets("check_type_holds_a_value_to_a_type") {
        let world = validating(&target);
        let engine = &world.engine;
        publish(
            engine,
            &shapes_document(json!([{"name": "test.Shape", "config": {"type": "Shape"}}])),
            None,
        );
        let issues = |extra: Value| {
            let refused = refusal(engine.instances().create(
                &alice(),
                "Item",
                &json!({"title": "t", "extra": extra}),
                Default::default(),
            ));
            let mut found: Vec<(String, String)> = validation_issues(&refused)
                .as_array()
                .unwrap()
                .iter()
                .map(|issue| {
                    (
                        issue["path"].as_str().unwrap().to_owned(),
                        issue["rule"].as_str().unwrap().to_owned(),
                    )
                })
                .collect();
            found.sort();
            found
        };
        let pair = |path: &str, rule: &str| (path.to_owned(), rule.to_owned());
        assert_eq!(
            issues(
                json!({"size": "big", "tags": "x", "inner": {"name": 1, "more": true}, "other": 1})
            ),
            [
                pair("extra.inner.more", "unknown"),
                pair("extra.inner.name", "type"),
                pair("extra.other", "unknown"),
                pair("extra.size", "type"),
                pair("extra.tags", "type"),
            ]
        );
        assert_eq!(
            issues(json!({"inner": {}})),
            [
                pair("extra.inner.name", "required"),
                pair("extra.size", "required")
            ]
        );
        assert_eq!(
            validation_issues(&refusal(engine.instances().create(
                &alice(),
                "Item",
                &json!({"title": "t", "extra": "text"}),
                Default::default()
            ))),
            json!([{"path": "extra", "rule": "type", "message": "a Shape is a JSON object"}])
        );
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "t", "extra": {"size": 2, "tags": ["a"], "inner": {"name": "n"}}}),
                Default::default(),
            )
            .unwrap();
    }
}

#[test]
fn checked_types_names_types_of_the_document_and_check_type_reaches_only_those() {
    for target in targets("checked_types_names_types_of_the_document") {
        let world = validating(&target);
        let engine = &world.engine;
        for kind in ["Nope", "Item"] {
            let refused = refusal(engine.schemas().define(
                &alice(),
                shapes_document(json!([{"name": "test.Shape", "config": {"type": kind}}])),
                Default::default(),
            ));
            assert_eq!(
                issues_of(&refused),
                [issue("/types/Item/behaviors/0/config", &format!("type Item: behavior test.Shape checks values against {kind}, which is not a type of the document besides Item"))]
            );
        }
        let broken = |mode: &str| {
            publish(
                engine,
                &shapes_document(json!([{"name": "test.Defect", "config": {"mode": mode}}])),
                None,
            );
            defect(engine.instances().create(
                &alice(),
                "Item",
                &json!({"title": "t"}),
                Default::default(),
            ))
            .1
        };
        assert!(broken("shape").contains(
            "behavior test.Defect: validate returns a list of issues, each { path, rule, message }"
        ));
        assert!(broken("type").contains(
            "behavior test.Defect: checkType: Shape is not a type its checkedTypes names (Inner)"
        ));
        assert!(broken("veto").contains(
            "behavior test.Defect: a veto's code \"nope\" is not one its declaration lists (none)"
        ));
        assert!(engine
            .instances()
            .list(&alice(), "Item", Default::default())
            .unwrap()
            .items
            .is_empty());
    }
}

// test.Rogue's guard answers what a test sets, to a delete.
struct Rogue {
    answer: Arc<Mutex<Option<Veto>>>,
}

impl Behavior for Rogue {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Rogue", "vetoes": [{"code": "listed"}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guard: Some(|rogue, _, request| {
                Ok(match request {
                    GuardRequest::Delete { .. } => rogue.answer.lock().unwrap().clone(),
                    _ => None,
                })
            }),
            ..Hooks::default()
        }
    }
}

struct Refusing {
    engine: Engine,
    hold: Hold,
    answer: Arc<Mutex<Option<Veto>>>,
}

fn refusing(target: &support::Target, behaviors: Value) -> Refusing {
    let hold = Hold::default();
    let answer = Arc::new(Mutex::new(None));
    let engine = target.open(support::fixtures().with(hold.clone()).with(Rogue {
        answer: Arc::clone(&answer),
    }));
    support::publish_item(&engine, behaviors);
    for id in ["i1", "i2"] {
        create(&engine, id, json!({ "title": id })).unwrap();
    }
    Refusing {
        engine,
        hold,
        answer,
    }
}

fn with(preconditions: &Value) -> WriteOptions<'_> {
    WriteOptions {
        preconditions: Some(preconditions),
        ..Default::default()
    }
}

fn fenced(generation: i64) -> Value {
    json!({"test.Hold": {"generation": generation}})
}

#[test]
fn a_guards_veto_carries_the_code_its_declaration_lists_and_its_details() {
    for target in targets("a_guards_veto_carries_the_code") {
        let world = refusing(&target, json!([{"name": "test.Hold"}]));
        let engine = &world.engine;
        invoke(engine, "i1", "advance", json!({})).unwrap();
        let zero = fenced(0);
        let refused = refusal(engine.instances().update(
            &alice(),
            "Item",
            "i1",
            &json!({"title": "Late"}),
            WriteOptions {
                preconditions: Some(&zero),
                ..Default::default()
            },
        ));
        assert_eq!(refused.message(), "behavior test.Hold vetoes update of Item i1: generation 0 is stale: the instance is at 1");
        let (behavior, action, reason, veto_code, details) =
            veto::<superschematic_engine::InstanceRecord>(Err(Error::Engine(refused)));
        assert_eq!(
            (
                behavior.as_str(),
                action.as_str(),
                reason.as_str(),
                veto_code.as_deref(),
                details
            ),
            (
                "test.Hold",
                "update",
                "generation 0 is stale: the instance is at 1",
                Some("stale"),
                Some(json!({"generation": 0, "current": 1}))
            )
        );
    }
}

#[test]
fn a_handlers_veto_carries_its_code_and_an_unlisted_code_or_details_that_are_no_object_is_a_defect()
{
    for target in targets("a_handlers_veto_carries_its_code") {
        let world = refusing(&target, json!([{"name": "test.Hold"}]));
        let engine = &world.engine;
        let (_, _, reason, veto_code, details) = veto(invoke(
            engine,
            "i1",
            "refuse",
            json!({"code": "refused", "details": {"why": "asked"}}),
        ));
        assert_eq!(
            (veto_code.as_deref(), details, reason.as_str()),
            (
                Some("refused"),
                Some(json!({"why": "asked"})),
                "refused as asked"
            )
        );
        assert_eq!(veto(invoke(engine, "i1", "refuse", json!({}))).3, None);
        assert!(defect(invoke(engine, "i1", "refuse", json!({"code": "unlisted"})))
            .1
            .contains("behavior test.Hold: a veto's code \"unlisted\" is not one its declaration lists (stale, required, refused)"));
        assert!(defect(invoke(
            engine,
            "i1",
            "refuse",
            json!({"code": "refused", "details": [1]})
        ))
        .1
        .contains("a veto's details are a JSON object"));
    }
}

#[test]
fn a_guard_answers_nothing_or_a_veto_whose_code_its_declaration_lists() {
    for target in targets("a_guard_answers_nothing_or_a_veto") {
        let world = refusing(&target, json!([{"name": "test.Rogue"}]));
        let engine = &world.engine;
        let delete = || {
            engine
                .instances()
                .delete(&alice(), "Item", "i1", WriteOptions::default())
        };
        *world.answer.lock().unwrap() = Some(Veto::from("a plain reason"));
        let (_, _, reason, veto_code, details) = veto(delete());
        assert_eq!(
            (reason.as_str(), veto_code, details),
            ("a plain reason", None, None)
        );
        let mut at = Map::new();
        at.insert("at".into(), json!(1));
        *world.answer.lock().unwrap() = Some(Veto {
            reason: "listed".into(),
            code: Some("listed".into()),
            details: Some(at),
        });
        assert_eq!(veto(delete()).4, Some(json!({"at": 1})));
        for answer in [
            Veto {
                reason: "unlisted".into(),
                code: Some("other".into()),
                details: None,
            },
            Veto {
                reason: String::new(),
                code: None,
                details: None,
            },
        ] {
            *world.answer.lock().unwrap() = Some(answer);
            assert!(defect(delete()).1.starts_with("behavior test.Rogue: "));
        }
        *world.answer.lock().unwrap() = None;
        assert!(delete().unwrap());
    }
}

#[test]
fn registration_refuses_bad_veto_codes_and_a_precondition_schema_that_is_not_a_closed_object() {
    for target in targets("registration_refuses_bad_veto_codes") {
        struct Declared(String);
        impl Behavior for Declared {
            type Config = Value;
            fn declaration(&self) -> &str {
                &self.0
            }
            fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
                Ok(raw.clone())
            }
            fn hooks(&self) -> Hooks<Self> {
                Hooks::default()
            }
        }
        let engine = target.open(BehaviorSet::new());
        let refusal = |change: Value| {
            let mut declaration = json!({"name": "test.Hold"});
            for (key, value) in change.as_object().unwrap() {
                declaration[key] = value.clone();
            }
            usage(
                engine
                    .behaviors()
                    .register(Declared(declaration.to_string())),
            )
        };
        assert!(refusal(json!({"vetoes": [{"code": "Stale"}]}))
            .contains("veto code \"Stale\" is not lowercase snake case of at most 64 characters"));
        assert!(
            refusal(json!({"vetoes": [{"code": "stale"}, {"code": "stale"}]}))
                .contains("veto code stale is declared twice")
        );
        assert!(
            refusal(json!({"vetoes": [{"code": "stale", "status": 409}]}))
                .contains("vetoes[0] has the unknown key \"status\"")
        );
        assert!(refusal(json!({"vetoes": "stale"})).contains("vetoes is a list"));
        assert!(refusal(json!({"preconditionSchema": {"type": "integer"}}))
            .contains("preconditionSchema must be an object schema"));
        assert!(refusal(json!({"preconditionSchema": {"type": "object"}}))
            .contains("preconditionSchema must set \"additionalProperties\": false"));
        assert!(refusal(json!({"preconditionSchema": {"type": "object", "additionalProperties": false, "required": "generation"}})).contains("preconditionSchema does not compile"));
    }
}

#[test]
fn each_guard_gets_its_own_behaviors_entry_with_an_update_a_delete_and_an_operation() {
    for target in targets("each_guard_gets_its_own_behaviors_entry") {
        let world = refusing(
            &target,
            json!([{"name": "test.Counter"}, {"name": "test.Hold"}]),
        );
        let engine = &world.engine;
        let zero = fenced(0);
        engine
            .instances()
            .update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "Desk"}),
                with(&zero),
            )
            .unwrap();
        let update = world.hold.take();
        assert_eq!(
            (update[0]["kind"].clone(), update[0]["precondition"].clone()),
            (json!("update"), json!({"generation": 0}))
        );
        invoke_with(engine, "i1", "increment", json!({}), with(&zero)).unwrap();
        assert_eq!(
            world
                .hold
                .take()
                .iter()
                .map(|request| json!([request["kind"], request["precondition"]]))
                .collect::<Vec<_>>(),
            [json!(["operation", {"generation": 0}])]
        );
        invoke_with(engine, "i1", "advance", json!({}), with(&zero)).unwrap();
        let seq = get(engine, "i1").unwrap().seq;
        assert_eq!(
            veto(invoke_with(
                engine,
                "i1",
                "increment",
                json!({}),
                with(&zero)
            ))
            .3
            .as_deref(),
            Some("stale")
        );
        assert_eq!(
            veto(
                engine
                    .instances()
                    .delete(&alice(), "Item", "i1", with(&zero))
            )
            .3
            .as_deref(),
            Some("stale")
        );
        assert_eq!(get(engine, "i1").unwrap().seq, seq, "nothing was written");
        // A read-only operation is no write: the guard lets it through.
        assert_eq!(
            invoke_with(engine, "i1", "peek", json!({}), with(&zero)).unwrap(),
            json!({"generation": 1})
        );
        engine
            .instances()
            .update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "Lamp"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(world.hold.take().last().unwrap().get("precondition"), None);
        let one = fenced(1);
        assert!(engine
            .instances()
            .delete(&alice(), "Item", "i1", with(&one))
            .unwrap());
    }
}

#[test]
fn preconditions_are_refused_unless_each_names_a_behavior_that_declares_a_schema_which_accepts_the_entry(
) {
    for target in targets("preconditions_are_refused_unless") {
        let world = refusing(
            &target,
            json!([{"name": "test.Counter"}, {"name": "test.Hold"}]),
        );
        let engine = &world.engine;
        let issues = |preconditions: Value| {
            let refused = refusal(engine.instances().update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "X"}),
                WriteOptions {
                    preconditions: Some(&preconditions),
                    ..Default::default()
                },
            ));
            assert_eq!(refused.code(), EngineErrorCode::InvalidArgument);
            issues_of(&refused)
        };
        assert_eq!(
            issues(json!([])),
            [issue(
                "",
                "the preconditions are a JSON object of each behavior's entry by its name"
            )]
        );
        assert_eq!(
            issues(json!({"test.Nope": {}})),
            [issue("/test.Nope", "Item composes no behavior test.Nope")]
        );
        assert_eq!(
            issues(json!({"test.Counter": {"count": 0}})),
            [issue(
                "/test.Counter",
                "behavior test.Counter takes no precondition"
            )]
        );
        assert_eq!(
            issues(json!({"test.Hold": {}})),
            [issue(
                "/test.Hold",
                "must have required property 'generation'"
            )]
        );
        assert_eq!(
            issues(json!({"test.Hold": {"generation": -1, "at": 3}})),
            [
                issue("/test.Hold", "must NOT have additional properties: at"),
                issue("/test.Hold/generation", "must be >= 0")
            ]
        );
        let flag = json!({"test.Flag": {}});
        assert_eq!(
            issues_of(&refusal(engine.instances().delete(
                &alice(),
                "Item",
                "i1",
                WriteOptions {
                    preconditions: Some(&flag),
                    ..Default::default()
                }
            ))),
            [issue("/test.Flag", "Item composes no behavior test.Flag")]
        );
        let one = json!({"test.Hold": {"generation": "one"}});
        assert_eq!(
            issues_of(&refusal(invoke_with(
                engine,
                "i1",
                "advance",
                json!({}),
                WriteOptions {
                    preconditions: Some(&one),
                    ..Default::default()
                }
            )))[0]
                .0,
            "/test.Hold/generation"
        );
        assert!(world.hold.take().is_empty(), "no guard was asked");
        assert_eq!(get(engine, "i1").unwrap().data["title"], "i1");
    }
}

#[test]
fn a_request_a_behaviors_own_code_makes_carries_no_precondition_and_an_invoke_carries_the_ones_it_gives(
) {
    for target in targets("a_request_a_behaviors_own_code_makes") {
        let world = refusing(
            &target,
            json!([{"name": "test.Counter"}, {"name": "test.Tally"}, {"name": "test.Hold", "config": {"require": true}}]),
        );
        let engine = &world.engine;
        let zero = fenced(0);
        let with = WriteOptions {
            preconditions: Some(&zero),
            ..Default::default()
        };
        invoke_with(engine, "i1", "bump", json!({}), with).unwrap();
        assert_eq!(
            world
                .hold
                .take()
                .iter()
                .map(|request| json!([
                    request["operation"],
                    request.get("caller"),
                    request.get("precondition")
                ]))
                .collect::<Vec<_>>(),
            [
                json!(["bump", null, {"generation": 0}]),
                json!(["increment", "test.Tally", null])
            ]
        );
        assert_eq!(
            veto(invoke_with(
                engine,
                "i1",
                "forward",
                json!({"id": "i2"}),
                with
            ))
            .3
            .as_deref(),
            Some("required")
        );
        assert_eq!(
            veto(invoke_with(
                engine,
                "i1",
                "forward",
                json!({"id": "i2", "generation": 3}),
                with
            ))
            .3
            .as_deref(),
            Some("stale")
        );
        assert_eq!(
            invoke_with(
                engine,
                "i1",
                "forward",
                json!({"id": "i2", "generation": 0}),
                with
            )
            .unwrap(),
            json!({"generation": 1})
        );
        assert_eq!(
            get(engine, "i2").unwrap().behaviors["test.Hold"]["generation"],
            1
        );
        assert_eq!(
            veto(engine.instances().update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "Bare"}),
                WriteOptions::default()
            ))
            .3
            .as_deref(),
            Some("required")
        );
    }
}
