//! What one transaction keeps of what it read (D58, amended): within a
//! call, a read after a write of the same instance sees the write, one
//! after a write that rolled back does not, one after a create sees the
//! instance; a list's page reads each instance's behaviors as a get does;
//! and in a host's transaction, a call sees what the host's own
//! statements wrote since the call before. On SQLite and, with the
//! variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, ConfigTarget, CreateOptions, Engine, Error, Hooks, InstanceRecord, ListOptions,
    Result,
};
use superschematic_engine_testkit::behaviors::Reader;
use support::{alice, fields_of, fixtures, publish, schema_document, targets, Target};

// test.Rereader reads an instance, changes it through another call in the
// same transaction, and reads it again.
struct Rereader;

impl Behavior for Rereader {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Rereader", "operations": [
            {"name": "reread", "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"schema": {"type": "string"}, "id": {"type": "string"}, "operation": {"type": "string"}, "field": {"type": "string"}}}, "resultSchema": true, "writes": true},
            {"name": "makeThenRead", "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"schema": {"type": "string"}, "id": {"type": "string"}}}, "resultSchema": true, "writes": true}
        ]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![
                ("reread", |_, cx, params| {
                    let schema = text(params, "schema");
                    let id = text(params, "id");
                    let field = text(params, "field");
                    let read = || -> Result<Value> {
                        let found = cx.instances().get(schema, id, Some(&[field]))?;
                        Ok(found
                            .as_ref()
                            .map_or(Value::Null, |record| value_of(record, field)))
                    };
                    let before = read()?;
                    let threw = cx
                        .instances()
                        .invoke(schema, id, text(params, "operation"), &json!({}), None)
                        .is_err();
                    Ok(json!({"before": before, "after": read()?, "threw": threw}))
                }),
                ("makeThenRead", |_, cx, params| {
                    let schema = text(params, "schema");
                    let id = text(params, "id");
                    let title = || -> Result<Value> {
                        let found = cx.instances().get(schema, id, Some(&[]))?;
                        Ok(found.map_or(Value::Null, |record| record.data["title"].clone()))
                    };
                    let before = title()?;
                    cx.instances()
                        .create(schema, &json!({"title": "Made"}), Some(id), None)?;
                    Ok(json!({"before": before, "after": title()?}))
                }),
            ],
            ..Hooks::default()
        }
    }
}

fn text<'v>(params: &'v Value, key: &str) -> &'v str {
    params.get(key).and_then(Value::as_str).unwrap_or_default()
}

// value_of is a qualified behavior field of a record: test.Counter.count.
fn value_of(record: &InstanceRecord, field: &str) -> Value {
    let (behavior, name) = field.rsplit_once('.').unwrap_or_default();
    record
        .behavior_field(behavior, name)
        .cloned()
        .unwrap_or(Value::Null)
}

fn document(name: &str, behaviors: Value) -> Value {
    let mut document = schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"][name]["behaviors"] = behaviors;
    document
}

fn make(engine: &Engine, schema: &str, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &json!({"title": id}),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .unwrap();
}

// A Watch rereads; an Item counts to 5; a Note is poked and fails after
// its write when asked.
fn world(target: &Target) -> Engine {
    let engine = target.open(fixtures().with(Rereader).with(Reader));
    publish(
        &engine,
        &document("Watch", json!([{"name": "test.Rereader"}])),
        None,
    );
    publish(
        &engine,
        &document(
            "Item",
            json!([{"name": "test.Counter", "config": {"start": 1, "limit": 5}}, {"name": "test.Tally"}]),
        ),
        None,
    );
    publish(
        &engine,
        &document("Note", json!([{"name": "test.Reader"}])),
        None,
    );
    make(&engine, "Watch", "w1");
    make(&engine, "Item", "i1");
    make(&engine, "Note", "n1");
    engine
}

fn reread(engine: &Engine, params: Value) -> Value {
    engine
        .instances()
        .invoke(
            &alice(),
            "Watch",
            "w1",
            "reread",
            &params,
            Default::default(),
        )
        .unwrap()
}

#[test]
fn a_read_after_a_write_in_the_same_call_sees_the_write_and_none_that_rolled_back() {
    for target in targets("a_read_after_a_write_in_the_same_call") {
        let engine = world(&target);
        assert_eq!(
            reread(
                &engine,
                json!({"schema": "Item", "id": "i1", "operation": "increment", "field": "test.Counter.count"})
            ),
            json!({"before": 1, "after": 2, "threw": false}),
            "the columns a nested call wrote"
        );
        assert_eq!(
            reread(
                &engine,
                json!({"schema": "Item", "id": "i1", "operation": "bump", "field": "test.Counter.count"})
            ),
            json!({"before": 2, "after": 3, "threw": false}),
            "the columns a call nested in a nested call wrote"
        );
        assert_eq!(
            reread(
                &engine,
                json!({"schema": "Note", "id": "n1", "operation": "failAfterWrite", "field": "test.Reader.pokes"})
            ),
            json!({"before": 0, "after": 0, "threw": true}),
            "a write whose savepoint rolled back"
        );
        assert_eq!(
            engine
                .instances()
                .invoke(
                    &alice(),
                    "Watch",
                    "w1",
                    "makeThenRead",
                    &json!({"schema": "Item", "id": "i2"}),
                    Default::default()
                )
                .unwrap(),
            json!({"before": null, "after": "Made"}),
            "an instance a nested call created, which a read before it found missing"
        );
    }
}

#[test]
fn a_lists_page_reads_each_instances_behaviors_as_a_get_does() {
    for target in targets("a_lists_page_reads_each_instances_behaviors") {
        let engine = world(&target);
        for id in ["i2", "i3", "i4"] {
            make(&engine, "Item", id);
        }
        for (id, times) in [("i2", 1), ("i3", 3)] {
            engine
                .instances()
                .invoke(
                    &alice(),
                    "Item",
                    id,
                    "bump",
                    &json!({"times": times}),
                    Default::default(),
                )
                .unwrap();
        }
        let page = engine
            .instances()
            .list(
                &alice(),
                "Item",
                ListOptions {
                    limit: Some(3),
                    ..Default::default()
                },
            )
            .unwrap();
        let listed: Vec<Value> = page.items.iter().map(fields_of).collect();
        let got: Vec<Value> = ["i1", "i2", "i3"]
            .iter()
            .map(|id| {
                fields_of(
                    &engine
                        .instances()
                        .get(&alice(), "Item", id, Default::default())
                        .unwrap()
                        .unwrap(),
                )
            })
            .collect();
        assert_eq!(listed, got);
        assert_eq!(
            listed[2],
            json!({"data": {"title": "i3"}, "behaviors": {"test.Counter": {"count": 4}, "test.Tally": {"edits": 0}}})
        );
    }
}

#[test]
fn a_call_in_a_hosts_transaction_sees_what_the_hosts_statements_wrote_since_the_call_before() {
    for target in targets("a_call_in_a_hosts_transaction_sees") {
        let engine = world(&target);
        let title = |engine: &Engine| {
            engine
                .instances()
                .get(&alice(), "Item", "i1", Default::default())
                .unwrap()
                .unwrap()
                .data["title"]
                .clone()
        };
        engine
            .db()
            .hold(|tx| {
                assert_eq!(title(&engine), json!("i1"));
                tx.run(
                    "UPDATE engine_instances SET data = ? WHERE schema = ? AND id = ?",
                    &[
                        SqlValue::from(r#"{"title":"Renamed by the host"}"#),
                        SqlValue::from("Item"),
                        SqlValue::from("i1"),
                    ],
                )?;
                assert_eq!(title(&engine), json!("Renamed by the host"));
                Ok::<_, Error>(())
            })
            .unwrap();
        assert_eq!(title(&engine), json!("Renamed by the host"));
    }
}
