//! The value store's behavior parts, the cases of `test/values.test.ts`
//! that need a behavior: a field reader, an operation's view and a read of
//! another instance get the value back; an operation's params and patch,
//! and a behavior's own field, keep a ref in the log; an operation that
//! writes only its columns neither loads nor rewrites a large field; and a
//! behavior's own rows hold values through values.stow and values.release;
//! on SQLite and, with the variable set, on Postgres. The engine's own
//! tests (`engine/tests/values.rs`) hold the cases that need no behavior;
//! before() puts the values back for the runner's reactions (milestone 6),
//! and Variants, Search and Revisions are core behaviors (milestones 8 to
//! 12).

mod support;

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec, TableSpec};
use superschematic_engine::json::{canonical_json, sha256_hex};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, CreateOptions, DriverError, Engine, EngineOptions, Error,
    GetOptions, Hooks, ListOptions, ReadEvents, Result, ValueDriver, ValueOptions, WriteOptions,
};
use superschematic_engine_testkit::behaviors::Counter;
use support::{alice, defect, publish, repository, targets, texts, Target};

/// The tests' threshold: the least an engine takes.
const THRESHOLD: u64 = 1024;

/// A VerifyResult of n checks: about 30 bytes of JSON each.
fn verify(n: usize, seed: &str) -> Value {
    let checks: Vec<Value> = (0..n)
        .map(|index| json!({"name": format!("{seed}-{index}"), "ok": index % 2 == 0}))
        .collect();
    json!({"passed": true, "checks": checks})
}

fn hash_of(value: &Value) -> String {
    sha256_hex(&canonical_json(value).unwrap())
}

fn ref_of(value: &Value) -> Value {
    json!({"$value": hash_of(value), "bytes": canonical_json(value).unwrap().len()})
}

fn checks_of(data: &Map<String, Value>) -> usize {
    data.get("result")
        .and_then(|result| result.get("checks"))
        .and_then(Value::as_array)
        .map_or(0, Vec::len)
}

// test.Shelf keeps documents in its own table through the value store,
// from a schema-level operation that appends no event, and reads the
// instance's own fields from a field reader, an operation and another
// instance; record writes a result with update(), as Retries' does.
struct Shelf;

static SHELF: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    json!({
        "name": "test.Shelf",
        "description": "Keeps documents through the value store and reads the instance.",
        "fields": [{"name": "seen", "description": "How many checks the result holds, as a field reader sees it."}],
        "operations": [
            {"name": "record", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["result"], "properties": {"result": {}}}, "resultSchema": true, "writes": true},
            {"name": "inspect", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true},
            {"name": "peek", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["id"], "properties": {"id": {"type": "string"}}}, "resultSchema": true},
            {"name": "keep", "scope": "schema", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["key", "doc"], "properties": {"key": {"type": "string"}, "doc": {}, "fail": {"type": "boolean"}}}, "resultSchema": true, "writes": true},
            {"name": "drop", "scope": "schema", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["key"], "properties": {"key": {"type": "string"}}}, "resultSchema": true, "writes": true},
            {"name": "take", "scope": "schema", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["key"], "properties": {"key": {"type": "string"}, "stow": {"type": "boolean"}}}, "resultSchema": true}
        ]
    })
    .to_string()
});

fn key_of(params: &Value) -> &str {
    params["key"].as_str().unwrap_or_default()
}

impl Behavior for Shelf {
    type Config = Value;

    fn declaration(&self) -> &str {
        &SHELF
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            migrations: vec![BehaviorMigration::new(1, "shelf").table(
                TableSpec::new("shelf")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("key", ColumnSpec::text().not_null())
                    .column("doc", ColumnSpec::text().not_null())
                    .column("value_refs", ColumnSpec::text())
                    .primary_key(&["namespace", "schema", "key"]),
            )],
            fields: vec![("seen", |_, view| Ok(Some(json!(checks_of(&*view.data()?)))))],
            operations: vec![
                ("record", |_, cx, params| {
                    cx.update(&json!({"result": params["result"]}))?;
                    Ok(Value::Null)
                }),
                ("inspect", |_, cx, _| Ok(json!(checks_of(&*cx.data()?)))),
                ("peek", |_, cx, params| {
                    let other = cx.instances().get(
                        cx.schema(),
                        params["id"].as_str().unwrap_or_default(),
                        None,
                    )?;
                    Ok(json!(other.map_or(0, |record| checks_of(&record.data))))
                }),
            ],
            schema_operations: vec![
                ("keep", |_, cx, params| {
                    let mut object = Map::new();
                    object.insert("doc".into(), params["doc"].clone());
                    let stowed = cx.values().stow(key_of(params), &object)?;
                    let table = cx.sql().table("shelf")?;
                    cx.sql().run(
                        format!("INSERT INTO {table} (namespace, schema, key, doc, value_refs) VALUES (?, ?, ?, ?, ?) ON CONFLICT (namespace, schema, key) DO UPDATE SET doc = excluded.doc, value_refs = excluded.value_refs"),
                        &[
                            cx.namespace().into(),
                            cx.schema().into(),
                            key_of(params).into(),
                            stowed.json.into(),
                            stowed.refs.map_or(SqlValue::Null, SqlValue::from),
                        ],
                    )?;
                    if params.get("fail") == Some(&Value::Bool(true)) {
                        return Err(Error::Internal("the keep fails after it stowed".into()));
                    }
                    Ok(Value::Null)
                }),
                ("drop", |_, cx, params| {
                    let table = cx.sql().table("shelf")?;
                    cx.sql().run(
                        format!(
                            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND key = ?"
                        ),
                        &[
                            cx.namespace().into(),
                            cx.schema().into(),
                            key_of(params).into(),
                        ],
                    )?;
                    cx.values().release(Some(key_of(params)))?;
                    Ok(Value::Null)
                }),
                ("take", |_, cx, params| {
                    if params.get("stow") == Some(&Value::Bool(true)) {
                        cx.values().stow(key_of(params), &Map::new())?;
                    }
                    let table = cx.sql().table("shelf")?;
                    let row = cx.sql().get(
                        format!("SELECT doc, value_refs FROM {table} WHERE namespace = ? AND schema = ? AND key = ?"),
                        &[cx.namespace().into(), cx.schema().into(), key_of(params).into()],
                    )?;
                    let Some(row) = row else {
                        return Ok(Value::Null);
                    };
                    let loaded = cx
                        .values()
                        .load(row.text("doc")?, row.opt_text("value_refs")?)?;
                    Ok(loaded.get("doc").cloned().unwrap_or(Value::Null))
                }),
            ],
            ..Hooks::default()
        }
    }
}

// test.Mirror reads the step's body back in a field of its own: a
// behavior's field as large as the own field it reads.
struct Mirror;

impl Behavior for Mirror {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Mirror", "fields": [{"name": "echo", "description": "The step's body, read back."}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            fields: vec![("echo", |_, view| Ok(view.data()?.get("body").cloned()))],
            ..Hooks::default()
        }
    }
}

/// The Variants fixture's Step without its behaviors (Variants is a core
/// behavior), with a body field and the behaviors given.
fn steps(behaviors: Value) -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-variants-json/src/step.schema.json");
    let mut document: Value =
        serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap();
    let step = document["types"]["Step"].as_object_mut().unwrap();
    step.insert("behaviors".into(), behaviors);
    step["fields"]
        .as_array_mut()
        .unwrap()
        .push(json!({"name": "body", "typeRef": {"name": "string"}}));
    document
}

fn open(
    target: &Target,
    behaviors: BehaviorSet,
    configure: impl FnOnce(&mut EngineOptions),
) -> Engine {
    target.open_with(behaviors, |options| {
        options.values = ValueOptions {
            threshold_bytes: Some(THRESHOLD),
            ..ValueOptions::default()
        };
        configure(options);
    })
}

fn shelved(target: &Target) -> Engine {
    let engine = open(target, BehaviorSet::new().with(Shelf), |_| {});
    publish(&engine, &steps(json!([{"name": "test.Shelf"}])), None);
    engine
}

fn create(engine: &Engine, id: &str, data: Value) {
    engine
        .instances()
        .create(
            &alice(),
            "Step",
            &data,
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .unwrap();
}

fn invoke(engine: &Engine, id: &str, operation: &str, params: Value) -> Result<Value> {
    engine.instances().invoke(
        &alice(),
        "Step",
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn invoke_schema(engine: &Engine, operation: &str, params: Value) -> Result<Value> {
    engine
        .instances()
        .invoke_schema(&alice(), "Step", operation, &params, None)
}

fn get(engine: &Engine, id: &str, value_refs: bool) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .get(
            &alice(),
            "Step",
            id,
            GetOptions {
                value_refs,
                ..Default::default()
            },
        )
        .unwrap()
        .unwrap()
}

/// The values engine_payloads holds, by hash.
fn payloads(engine: &Engine) -> Vec<String> {
    let mut hashes = texts(engine, "SELECT hash FROM engine_payloads", &[]);
    hashes.sort();
    hashes
}

fn hashes(values: &[&Value]) -> Vec<String> {
    let mut hashes: Vec<String> = values.iter().map(|value| hash_of(value)).collect();
    hashes.sort();
    hashes
}

/// Who holds a value: namespace, schema, holder, id and key, sorted.
fn holders(engine: &Engine, value: &Value) -> Vec<String> {
    let mut held = texts(
        engine,
        "SELECT namespace || ' ' || schema || ' ' || holder || ' ' || id || ' ' || key FROM engine_payload_holders WHERE hash = ?",
        &[SqlValue::from(hash_of(value))],
    );
    held.sort();
    held
}

fn step_events(engine: &Engine, id: &str) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Step"),
                instance_id: Some(id),
                ..Default::default()
            },
        )
        .unwrap()
        .events
}

#[test]
fn a_field_reader_an_operation_and_a_read_of_another_instance_get_the_value_back() {
    for target in targets("a_field_reader_an_operation_and_a_read_get_the_value_back") {
        let engine = shelved(&target);
        let result = verify(60, "check");
        create(
            &engine,
            "s1",
            json!({"title": "Big", "kind": "verify", "result": result}),
        );
        create(
            &engine,
            "s2",
            json!({"title": "Small", "kind": "verify", "result": verify(2, "check")}),
        );
        let got = get(&engine, "s1", false);
        assert_eq!(got.data["result"], result);
        assert_eq!(got.behaviors["test.Shelf"]["seen"], 60);
        assert_eq!(
            invoke(&engine, "s1", "inspect", json!({})).unwrap(),
            json!(60)
        );
        assert_eq!(
            invoke(&engine, "s2", "peek", json!({"id": "s1"})).unwrap(),
            json!(60)
        );
        // A read that asks for refs gets them, and the field reader still
        // read the value.
        let refs = engine
            .instances()
            .list(
                &alice(),
                "Step",
                ListOptions {
                    value_refs: true,
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(refs.items[0].data["result"], ref_of(&result));
        assert_eq!(refs.items[0].behaviors["test.Shelf"]["seen"], 60);
        let one = get(&engine, "s1", true);
        assert_eq!(one.data["result"], ref_of(&result));
        assert_eq!(one.behaviors["test.Shelf"]["seen"], 60);
    }
}

#[test]
fn the_log_keeps_refs_of_an_operations_params_and_patch_and_stores_their_value_once() {
    for target in targets("the_log_keeps_refs_of_an_operations_params_and_patch") {
        let engine = shelved(&target);
        // third differs from second in every member, so the operation's
        // patch of the result is the whole of third, as its params are.
        let (first, second) = (verify(60, "a"), verify(60, "b"));
        let mut third = verify(60, "c");
        third["passed"] = json!(false);
        create(
            &engine,
            "s1",
            json!({"title": "Check", "kind": "verify", "result": first}),
        );
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({ "result": second }),
                WriteOptions::default(),
            )
            .unwrap();
        invoke(&engine, "s1", "record", json!({ "result": third })).unwrap();
        let events = step_events(&engine, "s1");
        assert_eq!(
            events[0].change,
            json!({"data": {"title": "Check", "kind": "verify", "result": ref_of(&first)}, "behaviors": {"test.Shelf": {"seen": 60}}})
        );
        assert_eq!(events[0].value_refs, Some(vec!["/data/result".to_owned()]));
        assert_eq!(
            events[1].change,
            json!({"data": {"result": ref_of(&second)}})
        );
        assert_eq!(
            events[2].change,
            json!({"behavior": "test.Shelf", "operation": "record", "params": {"result": ref_of(&third)}, "patch": {"data": {"result": ref_of(&third)}}})
        );
        assert_eq!(
            events[2].value_refs,
            Some(vec![
                "/params/result".to_owned(),
                "/patch/data/result".to_owned()
            ])
        );
        let longest = texts(
            &engine,
            "SELECT MAX(LENGTH(change)) FROM engine_events WHERE schema = 'Step' AND instance_id IS NOT NULL",
            &[],
        );
        assert!(
            longest[0].parse::<usize>().unwrap() < 400,
            "the longest change holds {} characters",
            longest[0]
        );
        // The value the operation's params and patch name is stored once.
        assert_eq!(payloads(&engine), hashes(&[&first, &second, &third]));
    }
}

#[test]
fn a_behaviors_large_field_is_stowed_on_its_own_in_the_log_at_a_pointer_under_the_behaviors_name() {
    for target in targets("a_behaviors_large_field_is_stowed_on_its_own") {
        let engine = open(&target, BehaviorSet::new().with(Mirror), |_| {});
        publish(&engine, &steps(json!([{"name": "test.Mirror"}])), None);
        let (first, second) = (
            json!("a".repeat(THRESHOLD as usize)),
            json!("b".repeat(THRESHOLD as usize)),
        );
        create(
            &engine,
            "s1",
            json!({"title": "Long", "kind": "note", "body": first}),
        );
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({ "body": second }),
                WriteOptions::default(),
            )
            .unwrap();
        let events = step_events(&engine, "s1");
        assert_eq!(
            events[0].change,
            json!({"data": {"title": "Long", "kind": "note", "body": ref_of(&first)}, "behaviors": {"test.Mirror": {"echo": ref_of(&first)}}})
        );
        let pointers = Some(vec![
            "/data/body".to_owned(),
            "/behaviors/test.Mirror/echo".to_owned(),
        ]);
        assert_eq!(events[0].value_refs, pointers);
        assert_eq!(
            events[1].change,
            json!({"data": {"body": ref_of(&second)}, "behaviors": {"test.Mirror": {"echo": ref_of(&second)}}})
        );
        assert_eq!(events[1].value_refs, pointers);
        // Each value is stored once, though the row and the behavior's
        // field both name it.
        assert_eq!(payloads(&engine), hashes(&[&first, &second]));
        assert_eq!(
            get(&engine, "s1", false).behaviors["test.Mirror"]["echo"],
            second
        );
    }
}

/// A value driver over a map that counts its calls, outside the engine's
/// transactions, as a driver over other storage is.
#[derive(Default)]
struct MapDriver {
    values: Mutex<BTreeMap<String, String>>,
    reads: AtomicUsize,
    writes: AtomicUsize,
}

impl ValueDriver for MapDriver {
    fn read(&self, hash: &str) -> std::result::Result<Option<String>, DriverError> {
        self.reads.fetch_add(1, Ordering::SeqCst);
        Ok(self.values.lock().unwrap().get(hash).cloned())
    }

    fn write(&self, hash: &str, json: &str) -> std::result::Result<(), DriverError> {
        self.writes.fetch_add(1, Ordering::SeqCst);
        self.values
            .lock()
            .unwrap()
            .entry(hash.to_owned())
            .or_insert_with(|| json.to_owned());
        Ok(())
    }

    fn remove(&self, hash: &str) -> std::result::Result<(), DriverError> {
        self.values.lock().unwrap().remove(hash);
        Ok(())
    }
}

#[test]
fn an_operation_that_writes_only_its_columns_neither_loads_nor_rewrites_the_large_field() {
    for target in targets("an_operation_that_writes_only_its_columns") {
        let driver = Arc::new(MapDriver::default());
        let engine = target.open_with(BehaviorSet::new().with(Counter::default()), |options| {
            options.values = ValueOptions {
                threshold_bytes: Some(THRESHOLD),
                driver: Some(Arc::clone(&driver) as Arc<dyn ValueDriver>),
                cache_bytes: Some(0),
                ..ValueOptions::default()
            };
        });
        publish(&engine, &steps(json!([{"name": "test.Counter"}])), None);
        let result = verify(60, "check");
        create(
            &engine,
            "s1",
            json!({"title": "Check", "kind": "verify", "result": result}),
        );
        let counts = || {
            (
                driver.reads.load(Ordering::SeqCst),
                driver.writes.load(Ordering::SeqCst),
            )
        };
        let stored = texts(
            &engine,
            "SELECT data FROM engine_instances WHERE schema = 'Step' AND id = 's1'",
            &[],
        );
        assert_eq!(counts(), (0, 1));
        for _ in 0..3 {
            invoke(&engine, "s1", "increment", json!({})).unwrap();
        }
        assert_eq!(
            texts(
                &engine,
                "SELECT data FROM engine_instances WHERE schema = 'Step' AND id = 's1'",
                &[]
            ),
            stored
        );
        assert_eq!(counts(), (0, 1));
        let got = get(&engine, "s1", false);
        assert_eq!(got.behaviors["test.Counter"]["count"], 3);
        assert_eq!(got.data["result"], result);
    }
}

#[test]
fn a_value_goes_when_its_last_holder_does_and_a_behaviors_row_holds_one_through_stow_and_release() {
    for target in targets("a_value_goes_when_its_last_holder_does") {
        let engine = shelved(&target);
        let (x, y, z) = (verify(60, "x"), verify(60, "y"), verify(60, "z"));
        let keep = |key: &str, doc: &Value, fail: bool| {
            invoke_schema(
                &engine,
                "keep",
                json!({"key": key, "doc": doc, "fail": fail}),
            )
        };
        let drop = |key: &str| invoke_schema(&engine, "drop", json!({ "key": key })).unwrap();
        let take = |key: &str| invoke_schema(&engine, "take", json!({ "key": key })).unwrap();
        keep("a", &x, false).unwrap();
        keep("b", &x, false).unwrap();
        assert_eq!(payloads(&engine), hashes(&[&x]));
        assert_eq!(
            holders(&engine, &x),
            ["default Step test.Shelf  a", "default Step test.Shelf  b"]
        );
        assert_eq!(take("a"), x);
        drop("a");
        assert_eq!(payloads(&engine), hashes(&[&x]));
        drop("b");
        assert!(payloads(&engine).is_empty());
        // Stowing again for a key drops what it held before.
        keep("a", &x, false).unwrap();
        keep("a", &y, false).unwrap();
        assert_eq!(payloads(&engine), hashes(&[&y]));
        // A write that fails leaves what it stowed and dropped as it was.
        let failed = keep("a", &z, true).unwrap_err();
        assert!(
            failed
                .to_string()
                .contains("the keep fails after it stowed"),
            "{failed}"
        );
        assert_eq!(payloads(&engine), hashes(&[&y]));
        assert_eq!(take("a"), y);
        // A read-only operation stows nothing.
        defect(invoke_schema(
            &engine,
            "take",
            json!({"key": "a", "stow": true}),
        ));
        // An instance's old value stays while an event holds it.
        create(
            &engine,
            "s1",
            json!({"title": "Check", "kind": "verify", "result": x}),
        );
        engine
            .instances()
            .update(
                &alice(),
                "Step",
                "s1",
                &json!({ "result": z }),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(payloads(&engine), hashes(&[&x, &y, &z]));
        let holder = |held: Vec<String>| {
            held.iter()
                .map(|row| row.split(' ').nth(2).unwrap().to_owned())
                .collect::<Vec<_>>()
        };
        assert_eq!(holder(holders(&engine, &x)), ["event"]);
        engine
            .instances()
            .delete(&alice(), "Step", "s1", WriteOptions::default())
            .unwrap();
        assert_eq!(holder(holders(&engine, &z)), ["event"]);
        assert_eq!(payloads(&engine), hashes(&[&x, &y, &z]));
    }
}
