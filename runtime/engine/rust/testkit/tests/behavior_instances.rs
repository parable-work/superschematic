//! Behaviors on instances, the cases of `test/behavior-instances.test.ts`:
//! initialization, the fields they add, which a read keeps under each
//! behavior's name, guards, operations and the calls between behaviors,
//! after-change hooks, events, the access policy and rollback; on SQLite
//! and, with the variable set, on Postgres. A handler that returns a
//! promise or a value that is no JSON has no Rust counterpart; a hook that
//! panics does, and is a BehaviorError.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, ConfigTarget, EngineErrorCode, Error, GetOptions, GuardRequest,
    Hooks, ListOptions, Policy, Result, WriteOptions,
};
use superschematic_engine_testkit::behaviors::{Counter, Tally};
use support::{
    alice, code, defect, events_of, fields_of, issue, issues_of, named, publish_item, refusal,
    targets, texts, validation_issues, veto, RecordingPolicy,
};

// test.Faulty: operations that misbehave.
struct Faulty;

impl Behavior for Faulty {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{
            "name": "test.Faulty",
            "fields": [{"name": "marks"}],
            "operations": [
                {"name": "markThenThrow", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true},
                {"name": "wrongResult", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": {"type": "integer"}},
                {"name": "writeInRead", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true},
                {"name": "callWriteInRead", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true},
                {"name": "recurse", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true},
                {"name": "setOther", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true},
                {"name": "panicAfterWriting", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}
            ]
        }"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "marks")
                .column("marks", ColumnSpec::integer().not_null().default(0))],
            operations: vec![
                ("markThenThrow", |_, cx, _| {
                    let marks = cx.columns().get()?.i64("marks")?;
                    cx.columns()
                        .set(&[("marks", SqlValue::Integer(marks + 1))])?;
                    Err(Error::Internal("the handler failed after writing".into()))
                }),
                ("wrongResult", |_, _, _| Ok(json!("one"))),
                ("writeInRead", |_, cx, _| {
                    cx.columns().set(&[("marks", SqlValue::Integer(99))])?;
                    Ok(Value::Null)
                }),
                ("callWriteInRead", |_, cx, _| {
                    cx.call("test.Faulty", "markThenThrow", &json!({}))
                }),
                ("recurse", |_, cx, _| {
                    cx.call("test.Faulty", "recurse", &json!({}))
                }),
                ("setOther", |_, cx, _| {
                    cx.columns().set(&[("count", SqlValue::Integer(5))])?;
                    Ok(Value::Null)
                }),
                ("panicAfterWriting", |_, cx, _| {
                    cx.columns().set(&[("marks", SqlValue::Integer(7))])?;
                    panic!("the handler fell over")
                }),
            ],
            fields: vec![("marks", |_, cx| {
                Ok(Some(Value::from(cx.columns().get()?.i64("marks")?)))
            })],
            ..Hooks::default()
        }
    }
}

// test.Caller: calls test.Faulty's failing operation and catches it.
struct Caller;

impl Behavior for Caller {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{
            "name": "test.Caller",
            "fields": [{"name": "calls"}],
            "operations": [{"name": "callFaulty", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true}]
        }"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "calls")
                .column("calls", ColumnSpec::integer().not_null().default(0))],
            operations: vec![("callFaulty", |_, cx, _| {
                let calls = cx.columns().get()?.i64("calls")?;
                cx.columns()
                    .set(&[("calls", SqlValue::Integer(calls + 1))])?;
                match cx.call("test.Faulty", "markThenThrow", &json!({})) {
                    Ok(_) => Ok(json!("not reached")),
                    Err(_) => Ok(json!("caught")),
                }
            })],
            fields: vec![("calls", |_, cx| {
                Ok(Some(Value::from(cx.columns().get()?.i64("calls")?)))
            })],
            ..Hooks::default()
        }
    }
}

// test.Watcher: records each operation request its guard is asked.
struct Watcher {
    seen: Arc<Mutex<Vec<Value>>>,
}

impl Behavior for Watcher {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Watcher"}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guard: Some(|watcher, _, request| {
                if let GuardRequest::Operation {
                    behavior,
                    operation,
                    params,
                    writes,
                    caller,
                    ..
                } = request
                {
                    watcher
                        .seen
                        .lock()
                        .unwrap()
                        .push(json!([behavior, operation, caller, params, writes]));
                }
                Ok(None)
            }),
            ..Hooks::default()
        }
    }
}

fn invoke(
    engine: &superschematic_engine::Engine,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value> {
    engine.instances().invoke(
        &alice(),
        "Item",
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn create(engine: &superschematic_engine::Engine, id: &str, title: &str) {
    engine
        .instances()
        .create(
            &alice(),
            "Item",
            &json!({ "title": title }),
            superschematic_engine::CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .unwrap();
}

fn get(engine: &superschematic_engine::Engine, id: &str) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .get(&alice(), "Item", id, GetOptions::default())
        .unwrap()
        .expect("the instance")
}

#[test]
fn create_initializes_each_behavior_and_reads_carry_their_fields_under_each_behaviors_name() {
    for target in targets("create_initializes_each_behavior") {
        let engine = target.open_with(support::fixtures(), |options| {
            options.clock = Clock::Manual(50);
        });
        publish_item(
            &engine,
            json!([{"name": "test.Counter", "config": {"start": 3}}, {"name": "test.Flag"}]),
        );
        let created = engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Desk"}),
                superschematic_engine::CreateOptions {
                    id: Some("i1"),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(
            Value::Object(created.data.clone()),
            json!({"title": "Desk"})
        );
        assert_eq!(
            Value::Object(created.behaviors.clone()),
            json!({"test.Counter": {"count": 3}, "test.Flag": {"flagged": false}})
        );
        assert_eq!(
            created.behaviors.keys().collect::<Vec<_>>(),
            ["test.Counter", "test.Flag"],
            "in the type's behavior order"
        );
        assert_eq!(fields_of(&get(&engine, "i1")), fields_of(&created));
        let page = engine
            .instances()
            .list(&alice(), "Item", ListOptions::default())
            .unwrap();
        assert_eq!(
            page.items.iter().map(fields_of).collect::<Vec<_>>(),
            [fields_of(&created)]
        );
        // The row holds only the type's own fields; the behaviors keep theirs.
        assert_eq!(
            texts(&engine, "SELECT data FROM engine_instances", &[]),
            ["{\"title\":\"Desk\"}"]
        );
        assert_eq!(
            events_of(&engine, "Item"),
            [json!({"kind": "create", "seq": 1, "change": fields_of(&created)})]
        );
    }
}

#[test]
fn a_behavior_fields_name_is_no_field_of_the_types_and_create_and_update_refuse_it() {
    for target in targets("a_behavior_fields_name_is_no_field_of_the_types") {
        let engine = target.open_fixtures();
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        create(&engine, "i1", "Desk");
        let unknown =
            json!({"path": "count", "rule": "unknown", "message": "Item has no field count"});
        let refused = refusal(engine.instances().create(
            &alice(),
            "Item",
            &json!({"title": "Lamp", "count": 5}),
            Default::default(),
        ));
        assert_eq!(validation_issues(&refused), json!([unknown]));
        for patch in [json!({"count": 5}), json!({"title": "Lamp", "count": 0})] {
            let error = refusal(engine.instances().update(
                &alice(),
                "Item",
                "i1",
                &patch,
                WriteOptions::default(),
            ));
            assert_eq!(error.code(), EngineErrorCode::InvalidInstance);
            assert_eq!(validation_issues(&error), json!([unknown]));
        }
        // A patch that removes it removes no own field: it changes nothing.
        assert_eq!(
            engine
                .instances()
                .update(
                    &alice(),
                    "Item",
                    "i1",
                    &json!({"count": null}),
                    WriteOptions::default()
                )
                .unwrap()
                .seq,
            1
        );
        assert_eq!(
            serde_json::to_value(
                engine
                    .schemas()
                    .validate(
                        &alice(),
                        "Item",
                        &json!({"title": "Lamp", "count": 1, "colour": "red"}),
                        Default::default()
                    )
                    .unwrap()
            )
            .unwrap(),
            json!([unknown, {"path": "colour", "rule": "unknown", "message": "Item has no field colour"}])
        );
        assert_eq!(
            fields_of(&get(&engine, "i1")),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 0}}})
        );
        assert_eq!(events_of(&engine, "Item").len(), 1);
    }
}

#[test]
fn a_writing_operation_runs_in_the_transaction_and_appends_an_operation_event() {
    for target in targets("a_writing_operation_runs_in_the_transaction") {
        let engine = target.open_with(support::fixtures(), |options| {
            options.clock = Clock::Manual(9);
        });
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        create(&engine, "i1", "Desk");
        assert_eq!(
            invoke(&engine, "i1", "increment", json!({"by": 2})).unwrap(),
            json!({"count": 2})
        );
        assert_eq!(
            invoke(&engine, "i1", "increment", json!({})).unwrap(),
            json!({"count": 3})
        );
        let instance = get(&engine, "i1");
        assert_eq!(
            fields_of(&instance),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 3}}})
        );
        assert_eq!(instance.seq, 3);
        assert_eq!(
            events_of(&engine, "Item")[1..],
            [
                json!({"kind": "operation", "seq": 2, "change": {"behavior": "test.Counter", "operation": "increment", "params": {"by": 2}, "patch": {"behaviors": {"test.Counter": {"count": 2}}}}}),
                json!({"kind": "operation", "seq": 3, "change": {"behavior": "test.Counter", "operation": "increment", "params": {}, "patch": {"behaviors": {"test.Counter": {"count": 3}}}}}),
            ]
        );
        // operate gives the sequence after the call.
        let outcome = engine
            .instances()
            .operate(
                &alice(),
                "Item",
                "i1",
                "increment",
                &json!({}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!((outcome.result, outcome.seq), (json!({"count": 4}), 4));
        let read = engine
            .instances()
            .operate(
                &alice(),
                "Item",
                "i1",
                "history",
                &json!({}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!((read.result, read.seq), (json!([2, 1, 1]), 4));
    }
}

#[test]
fn a_read_only_operation_writes_nothing_and_appends_no_event() {
    for target in targets("a_read_only_operation_writes_nothing") {
        let engine = target.open_fixtures();
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        create(&engine, "i1", "Desk");
        invoke(&engine, "i1", "increment", json!({"by": 4})).unwrap();
        invoke(&engine, "i1", "increment", json!({})).unwrap();
        assert_eq!(
            invoke(&engine, "i1", "history", json!({})).unwrap(),
            json!([4, 1])
        );
        assert_eq!(events_of(&engine, "Item").len(), 3);
        assert_eq!(get(&engine, "i1").seq, 3);
    }
}

#[test]
fn parameters_are_checked_against_params_schema_before_anything_runs() {
    for target in targets("parameters_are_checked_against_params_schema") {
        let engine = target.open_fixtures();
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        create(&engine, "i1", "Desk");
        for (params, expected) in [
            (json!({"by": 0}), issue("/by", "must be >= 1")),
            (
                json!({"by": 1, "step": 2}),
                issue("", "must NOT have additional properties: step"),
            ),
            (json!({"by": "two"}), issue("/by", "must be integer")),
        ] {
            let error = refusal(invoke(&engine, "i1", "increment", params));
            assert_eq!(error.code(), EngineErrorCode::InvalidArgument);
            assert_eq!(error.name(), "OperationParamsError");
            assert_eq!(issues_of(&error), [expected]);
        }
        let missing = refusal(invoke(&engine, "i1", "decrement", json!({})));
        assert_eq!(missing.code(), EngineErrorCode::NotFound);
        assert!(
            missing
                .message()
                .contains("its behaviors' operations: increment, history"),
            "{missing}"
        );
        assert_eq!(
            code(invoke(&engine, "missing", "increment", json!({}))),
            EngineErrorCode::NotFound
        );
        assert_eq!(
            code(engine.instances().invoke(
                &alice(),
                "Order",
                "i1",
                "increment",
                &json!({}),
                WriteOptions::default()
            )),
            EngineErrorCode::NotFound
        );
        assert_eq!(get(&engine, "i1").behaviors["test.Counter"]["count"], 0);
    }
}

#[test]
fn parameters_issues_come_by_path_then_keyword_then_message_with_ajvs_messages() {
    for target in targets("parameters_issues_come_in_a_defined_order") {
        let engine = target.open_fixtures();
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        create(&engine, "i1", "Desk");
        let error = refusal(invoke(
            &engine,
            "i1",
            "increment",
            json!({"zeta": 1, "by": 0, "alpha": 2}),
        ));
        assert_eq!(
            issues_of(&error),
            [
                issue("", "must NOT have additional properties: alpha"),
                issue("", "must NOT have additional properties: zeta"),
                issue("/by", "must be >= 1"),
            ]
        );
    }
}

#[test]
fn a_guard_vetoes_an_update_a_delete_and_an_operation_and_the_first_veto_wins() {
    for target in targets("a_guard_vetoes_an_update_a_delete_and_an_operation") {
        let engine = target.open_fixtures();
        publish_item(
            &engine,
            json!([{"name": "test.Counter", "config": {"limit": 3}}, {"name": "test.Flag"}]),
        );
        create(&engine, "i1", "Desk");
        // The counter's own guard, on its own operation.
        let over = refusal(invoke(&engine, "i1", "increment", json!({"by": 4})));
        assert_eq!(over.code(), EngineErrorCode::Vetoed);
        assert_eq!(
            over.message(),
            "behavior test.Counter vetoes increment of Item i1: the count would pass its limit, 3"
        );
        let (behavior, action, reason, ..) =
            veto(invoke(&engine, "i1", "increment", json!({"by": 4})));
        assert_eq!(
            (behavior.as_str(), action.as_str(), reason.as_str()),
            (
                "test.Counter",
                "increment",
                "the count would pass its limit, 3"
            )
        );

        assert_eq!(
            invoke(&engine, "i1", "flag", json!({"reason": "audit"})).unwrap(),
            json!(true)
        );
        let events = events_of(&engine, "Item").len();
        assert_eq!(
            refusal(engine.instances().update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "Lamp"}),
                WriteOptions::default()
            ))
            .message(),
            "behavior test.Flag vetoes update of Item i1: it is flagged: audit"
        );
        assert_eq!(
            refusal(
                engine
                    .instances()
                    .delete(&alice(), "Item", "i1", WriteOptions::default())
            )
            .message(),
            "behavior test.Flag vetoes delete of Item i1: it is flagged: audit"
        );
        // Another behavior's guard on the counter's operation.
        assert_eq!(
            refusal(invoke(&engine, "i1", "increment", json!({}))).message(),
            "behavior test.Flag vetoes increment of Item i1: it is flagged: audit"
        );
        // The counter's guard comes first in list order.
        assert_eq!(
            veto(invoke(&engine, "i1", "increment", json!({"by": 4}))).0,
            "test.Counter"
        );
        assert_eq!(events_of(&engine, "Item").len(), events);
        assert_eq!(
            fields_of(&get(&engine, "i1")),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 0}, "test.Flag": {"flagged": true, "flagReason": "audit"}}})
        );
        // A patch that changes nothing is no update, and asks no guard.
        assert_eq!(
            engine
                .instances()
                .update(
                    &alice(),
                    "Item",
                    "i1",
                    &json!({"title": "Desk"}),
                    WriteOptions::default()
                )
                .unwrap()
                .seq,
            2
        );
        assert_eq!(
            invoke(&engine, "i1", "unflag", json!({})).unwrap(),
            json!(false)
        );
        assert_eq!(
            engine
                .instances()
                .update(
                    &alice(),
                    "Item",
                    "i1",
                    &json!({"title": "Lamp"}),
                    WriteOptions::default()
                )
                .unwrap()
                .data["title"],
            "Lamp"
        );
        assert!(engine
            .instances()
            .delete(&alice(), "Item", "i1", WriteOptions::default())
            .unwrap());
    }
}

#[test]
fn a_writing_operation_moves_the_sequence_so_a_stale_write_is_refused_before_any_guard() {
    for target in targets("a_writing_operation_moves_the_sequence") {
        let engine = target.open_fixtures();
        publish_item(
            &engine,
            json!([{"name": "test.Counter"}, {"name": "test.Flag"}]),
        );
        create(&engine, "i1", "Desk");
        invoke(&engine, "i1", "increment", json!({})).unwrap();
        assert_eq!(get(&engine, "i1").seq, 2);
        let at = |seq: i64| WriteOptions {
            expected_seq: Some(seq),
            ..Default::default()
        };
        assert_eq!(
            code(engine.instances().update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "Lamp"}),
                at(1)
            )),
            EngineErrorCode::SeqMismatch
        );
        assert_eq!(
            code(engine.instances().delete(&alice(), "Item", "i1", at(1))),
            EngineErrorCode::SeqMismatch
        );
        // An operation that changes no field still appends its event and
        // moves the sequence.
        assert_eq!(
            invoke(&engine, "i1", "unflag", json!({})).unwrap(),
            json!(false)
        );
        assert_eq!(
            events_of(&engine, "Item").last().unwrap(),
            &json!({"kind": "operation", "seq": 3, "change": {"behavior": "test.Flag", "operation": "unflag", "params": {}, "patch": {}}})
        );
        assert_eq!(
            code(engine.instances().update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "Lamp"}),
                at(2)
            )),
            EngineErrorCode::SeqMismatch
        );
        invoke(&engine, "i1", "flag", json!({"reason": "audit"})).unwrap();
        assert_eq!(
            code(engine.instances().update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "Lamp"}),
                at(4)
            )),
            EngineErrorCode::Vetoed
        );
        assert_eq!(
            code(engine.instances().delete(&alice(), "Item", "i1", at(4))),
            EngineErrorCode::Vetoed
        );
        assert_eq!(
            code(engine.instances().delete(&alice(), "Item", "i1", at(3))),
            EngineErrorCode::SeqMismatch
        );
        invoke(&engine, "i1", "unflag", json!({})).unwrap();
        assert_eq!(
            engine
                .instances()
                .update(&alice(), "Item", "i1", &json!({"title": "Lamp"}), at(5))
                .unwrap()
                .seq,
            6
        );
        assert!(engine
            .instances()
            .delete(&alice(), "Item", "i1", at(6))
            .unwrap());
        // An invoke that expects an old sequence is refused too.
        create(&engine, "i2", "Chair");
        assert_eq!(
            code(
                engine
                    .instances()
                    .invoke(&alice(), "Item", "i2", "increment", &json!({}), at(9))
            ),
            EngineErrorCode::SeqMismatch
        );
    }
}

#[test]
fn a_call_to_another_behaviors_operation_runs_every_guard_of_the_type_first() {
    for target in targets("a_call_to_another_behaviors_operation_runs_every_guard") {
        let engine = target.open_fixtures();
        publish_item(
            &engine,
            json!([{"name": "test.Counter", "config": {"limit": 2}}, {"name": "test.Tally"}]),
        );
        create(&engine, "i1", "Desk");
        assert_eq!(
            invoke(&engine, "i1", "bump", json!({"times": 2})).unwrap(),
            json!({"count": 2})
        );
        // The third increment passes the counter's limit: its guard vetoes
        // the call, and the bump with it.
        assert_eq!(
            refusal(invoke(&engine, "i1", "bump", json!({}))).message(),
            "behavior test.Counter vetoes increment of Item i1: the count would pass its limit, 2"
        );
        assert_eq!(
            invoke(&engine, "i1", "history", json!({})).unwrap(),
            json!([1, 1])
        );
        assert_eq!(
            events_of(&engine, "Item").last().unwrap()["change"],
            json!({"behavior": "test.Tally", "operation": "bump", "params": {"times": 2}, "patch": {"behaviors": {"test.Counter": {"count": 2}}}})
        );
        // A caller that catches the veto goes on; its own writes stay.
        assert_eq!(
            invoke(&engine, "i1", "tryBump", json!({})).unwrap(),
            json!({"vetoed": true})
        );
        assert_eq!(
            fields_of(&get(&engine, "i1")),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 2}, "test.Tally": {"edits": 100}}})
        );
    }
}

#[test]
fn a_guard_sees_who_called_the_callers_behavior_for_a_call_and_whether_it_writes() {
    for target in targets("a_guard_sees_who_called") {
        let seen = Arc::new(Mutex::new(Vec::new()));
        let engine = target.open(
            BehaviorSet::new()
                .with(Counter::default())
                .with(Watcher {
                    seen: Arc::clone(&seen),
                })
                .with(Tally),
        );
        publish_item(
            &engine,
            json!([{"name": "test.Watcher"}, {"name": "test.Counter"}, {"name": "test.Tally"}]),
        );
        create(&engine, "i1", "Desk");
        invoke(&engine, "i1", "bump", json!({})).unwrap();
        invoke(&engine, "i1", "history", json!({})).unwrap();
        assert_eq!(
            *seen.lock().unwrap(),
            [
                json!(["test.Tally", "bump", null, {}, true]),
                json!(["test.Counter", "increment", "test.Tally", {"by": 1}, true]),
                json!(["test.Counter", "history", null, {}, false]),
            ]
        );
    }
}

#[test]
fn after_change_runs_after_an_update_and_its_field_changes_are_in_the_update_event() {
    for target in targets("after_change_runs_after_an_update") {
        let engine = target.open_fixtures();
        publish_item(
            &engine,
            json!([{"name": "test.Counter"}, {"name": "test.Tally"}]),
        );
        create(&engine, "i1", "Desk");
        let updated = engine
            .instances()
            .update(
                &alice(),
                "Item",
                "i1",
                &json!({"title": "Lamp"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            fields_of(&updated),
            json!({"data": {"title": "Lamp"}, "behaviors": {"test.Counter": {"count": 0}, "test.Tally": {"edits": 1}}})
        );
        assert_eq!(
            events_of(&engine, "Item").last().unwrap(),
            &json!({"kind": "update", "seq": 2, "change": {"data": {"title": "Lamp"}, "behaviors": {"test.Tally": {"edits": 1}}}})
        );
    }
}

#[test]
fn a_delete_runs_guards_before_and_after_change_after_which_cleans_up_the_behaviors_tables() {
    for target in targets("a_delete_runs_guards_before_and_after_change_after") {
        let engine = target.open_fixtures();
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        create(&engine, "i1", "Desk");
        invoke(&engine, "i1", "increment", json!({})).unwrap();
        let history = "SELECT COUNT(*) AS n FROM bhv_test_counter__history";
        assert_eq!(support::count(&engine, history), 1);
        assert!(engine
            .instances()
            .delete(&alice(), "Item", "i1", WriteOptions::default())
            .unwrap());
        assert_eq!(support::count(&engine, history), 0);
        // A re-created instance starts over.
        create(&engine, "i1", "Desk");
        assert_eq!(get(&engine, "i1").behaviors["test.Counter"]["count"], 0);
    }
}

#[test]
fn a_handler_that_fails_rolls_everything_back() {
    for target in targets("a_handler_that_fails_rolls_everything_back") {
        let engine = target.open(BehaviorSet::new().with(Faulty).with(Caller));
        publish_item(
            &engine,
            json!([{"name": "test.Faulty"}, {"name": "test.Caller"}]),
        );
        create(&engine, "i1", "Desk");
        match invoke(&engine, "i1", "markThenThrow", json!({})) {
            Err(Error::Internal(message)) => {
                assert_eq!(message, "the handler failed after writing")
            }
            other => panic!("expected the handler's error, got {other:?}"),
        }
        assert_eq!(
            fields_of(&get(&engine, "i1")),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Faulty": {"marks": 0}, "test.Caller": {"calls": 0}}})
        );
        assert_eq!(events_of(&engine, "Item").len(), 1);
        // A called operation that fails rolls back alone when its caller
        // catches.
        assert_eq!(
            invoke(&engine, "i1", "callFaulty", json!({})).unwrap(),
            json!("caught")
        );
        assert_eq!(
            fields_of(&get(&engine, "i1")),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Faulty": {"marks": 0}, "test.Caller": {"calls": 1}}})
        );
    }
}

#[test]
fn defects_in_behavior_code_are_behavior_errors_and_roll_back() {
    for target in targets("defects_in_behavior_code_are_behavior_errors") {
        let engine = target.open(BehaviorSet::new().with(Counter::default()).with(Faulty));
        publish_item(
            &engine,
            json!([{"name": "test.Counter"}, {"name": "test.Faulty"}]),
        );
        create(&engine, "i1", "Desk");
        for (operation, expected) in [
            (
                "wrongResult",
                "operation wrongResult returned a result its resultSchema refuses: must be integer",
            ),
            ("writeInRead", "a read-only operation cannot set columns"),
            (
                "callWriteInRead",
                "a read-only operation cannot call test.Faulty.markThenThrow, which writes",
            ),
            ("recurse", "operation recurse: calls nest more than 16 deep"),
            ("setOther", "it has no column count; its columns are marks"),
            (
                "panicAfterWriting",
                "operation panicAfterWriting panicked: the handler fell over",
            ),
        ] {
            let (behavior, message) = defect(invoke(&engine, "i1", operation, json!({})));
            assert_eq!(behavior, "test.Faulty");
            assert!(message.contains(expected), "{operation}: {message}");
        }
        assert_eq!(
            fields_of(&get(&engine, "i1")),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 0}, "test.Faulty": {"marks": 0}}})
        );
        assert_eq!(events_of(&engine, "Item").len(), 1);
        // A panic a hook raised is the behavior's: the engine goes on.
        assert!(!engine.poisoned());
    }
}

#[test]
fn the_policy_is_asked_for_write_or_read_as_the_operation_declares_with_its_name() {
    for target in targets("the_policy_is_asked_for_write_or_read") {
        let policy = RecordingPolicy::new(|request| {
            request.principal().subject == "alice" || request.action() == "read"
        });
        let recorded = policy.clone();
        let engine = target.open_with(support::fixtures(), |options| {
            options.policy = Some(Policy::custom(policy));
        });
        publish_item(&engine, json!([{"name": "test.Counter"}]));
        create(&engine, "i1", "Desk");
        let reader = named("reader");
        for (principal, operation, action) in [
            (alice(), "increment", "write"),
            (alice(), "history", "read"),
            (reader.clone(), "history", "read"),
            (reader.clone(), "increment", "write"),
        ] {
            recorded.take();
            if let Err(error) = engine.instances().invoke(
                &principal,
                "Item",
                "i1",
                operation,
                &json!({}),
                WriteOptions::default(),
            ) {
                let error = refusal::<()>(Err(error));
                assert_eq!(error.code(), EngineErrorCode::Forbidden);
                assert_eq!(
                    error.message(),
                    "reader may not call increment (write) on Item in namespace default"
                );
            }
            assert_eq!(
                recorded.take(),
                [
                    json!({"principal": principal.subject, "action": action, "namespace": "default", "schema": "Item", "operation": operation})
                ]
            );
        }
        assert_eq!(get(&engine, "i1").behaviors["test.Counter"]["count"], 1);
        // An operation the schema lacks is not_found to a reader, and
        // forbidden to a principal that may not read.
        recorded.take();
        assert_eq!(
            code(engine.instances().invoke(
                &reader,
                "Item",
                "i1",
                "decrement",
                &json!({}),
                WriteOptions::default()
            )),
            EngineErrorCode::NotFound
        );
        assert_eq!(
            recorded
                .take()
                .iter()
                .map(|question| question["action"].clone())
                .collect::<Vec<_>>(),
            [json!("read")]
        );
        drop(engine);
        let nobody = target.open_with(support::fixtures(), |options| {
            options.policy = Some(Policy::custom(RecordingPolicy::new(|request| {
                matches!(request.action(), "define" | "publish")
            })));
        });
        publish_item(&nobody, json!([{"name": "test.Counter"}]));
        assert_eq!(
            code(nobody.instances().invoke(
                &alice(),
                "Item",
                "i1",
                "decrement",
                &json!({}),
                WriteOptions::default()
            )),
            EngineErrorCode::Forbidden
        );
    }
}

// test.Probe: what an operation's context holds.
struct Probe {
    seen: Arc<Mutex<Vec<Value>>>,
}

impl Behavior for Probe {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Probe", "operations": [{"name": "probe", "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"tags": {"type": "array"}}}, "resultSchema": true}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("probe", |probe, cx, params| {
                probe.seen.lock().unwrap().push(json!([
                    cx.data()?.get("title"),
                    params["tags"],
                    cx.principal().subject,
                    cx.now(),
                    cx.config(),
                    cx.version(),
                    cx.id()
                ]));
                Ok(Value::Null)
            })],
            ..Hooks::default()
        }
    }
}

#[test]
fn behavior_functions_see_the_data_and_parameters_the_principal_and_one_clock_reading() {
    for target in targets("behavior_functions_see_the_data") {
        let seen = Arc::new(Mutex::new(Vec::new()));
        let engine = target.open_with(
            BehaviorSet::new().with(Probe {
                seen: Arc::clone(&seen),
            }),
            |options| options.clock = Clock::Manual(100),
        );
        publish_item(&engine, json!([{"name": "test.Probe"}]));
        create(&engine, "i1", "Desk");
        engine.clock().script([100, 101, 102]).unwrap();
        invoke(&engine, "i1", "probe", json!({"tags": ["a"]})).unwrap();
        assert_eq!(
            *seen.lock().unwrap(),
            [json!(["Desk", ["a"], "alice", 100, {}, 1, "i1"])]
        );
        let _ = Policy::allow_all();
    }
}
