//! Behaviors that reach other instances, the cases of
//! `test/behavior-reach.test.ts` with the behaviors of
//! `test/reach-fixtures.ts`: reads of other instances and of other
//! schemas' configs as the caller, invokes that run the target's guards,
//! afterChange and event in the caller's transaction, the depth and cycle
//! limits, references with their guards and hooks, the namespace rule,
//! schema-level operations, and parseConfig's view of the other behaviors
//! and, at define and publish, of the other schemas; on SQLite and, with
//! the variable set, on Postgres. A `hears` that is neither 'delete' nor
//! `{ path, crosses? }` is a value Rust's [`Hears`] cannot hold. test.Reader
//! is the test kit's, since the Dependencies scenarios compose it too; the
//! core's Links and Dependencies, which reach other instances the same way,
//! have tests of their own.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, Hears, OperationCx, Reference, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    AccessRequest, Behavior, BehaviorSet, ConfigTarget, CreateOptions, DefineOptions, Engine,
    EngineErrorCode, EngineOptions, Error, EventsAfter, GuardRequest, Hooks, InstanceChange,
    InstanceRecord, NamespaceOptions, Policy, Principal, ReadEvents, Result, SchemaTarget,
    WriteOptions,
};
use superschematic_engine_testkit::behaviors::Reader;
use support::{
    alice, code, defect, fields_of, fixtures, issues_of, named, publish, refusal, schema_document,
    targets, veto, RecordingPolicy, Target,
};

type Seen = Arc<Mutex<Vec<Value>>>;

fn text<'v>(params: &'v Value, key: &str) -> &'v str {
    params.get(key).and_then(Value::as_str).unwrap_or_default()
}

fn reference_json(reference: &Reference) -> Value {
    let mut out = json!({"schema": reference.schema, "id": reference.id, "key": reference.key});
    match &reference.hears {
        None => {}
        Some(Hears::Delete) => out["hears"] = json!("delete"),
        Some(Hears::Path { path, crosses }) => {
            let mut hears = json!({ "path": path });
            if let Some(crosses) = crosses {
                hears["crosses"] = if crosses.fract() == 0.0 && crosses.abs() < 9e15 {
                    json!(*crosses as i64)
                } else {
                    json!(crosses)
                };
            }
            out["hears"] = hears;
        }
    }
    out
}

fn instance_key<C>(cx: &OperationCx<'_, C>) -> [SqlValue; 3] {
    [
        SqlValue::from(cx.namespace()),
        SqlValue::from(cx.schema()),
        SqlValue::from(cx.id()),
    ]
}

#[derive(Clone, Debug, Default)]
struct HolderConfig {
    // The changes of a held instance its guard vetoes.
    veto: Vec<String>,
    // Leave the reference behind when a held instance is deleted.
    leave: bool,
}

// test.Holder holds references to other instances and notes what happens
// to them.
struct Holder;

static HOLDER: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let target = json!({"schema": {"type": "string", "minLength": 1}, "id": {"type": "string", "minLength": 1}});
    let object = |required: Value, properties: Value| json!({"type": "object", "additionalProperties": false, "required": required, "properties": properties});
    let with = |more: Value| {
        let mut out = target.clone();
        for (key, value) in more.as_object().unwrap() {
            out[key] = value.clone();
        }
        out
    };
    let no_params = json!({"type": "object", "additionalProperties": false});
    json!({
        "name": "test.Holder",
        "description": "Holds references to other instances and notes what happens to them.",
        "configSchema": {"type": "object", "additionalProperties": false, "properties": {
            "veto": {"type": "array", "items": {"enum": ["update", "delete", "operation"]}},
            "leave": {"type": "boolean"}
        }},
        "fields": [{"name": "held", "description": "What the instance holds: schema/id/key."}],
        "operations": [
            {"name": "hold", "paramsSchema": object(json!(["schema", "id"]), with(json!({"key": {"type": "string"}, "hears": {}}))), "resultSchema": true, "writes": true},
            {"name": "release", "paramsSchema": object(json!(["schema", "id"]), with(json!({"key": {"type": "string"}}))), "resultSchema": {"type": "boolean"}, "writes": true},
            {"name": "note", "paramsSchema": object(json!(["schema", "id", "kind"]), with(json!({"kind": {"type": "string"}}))), "resultSchema": true, "writes": true},
            {"name": "notes", "paramsSchema": no_params, "resultSchema": {"type": "array", "items": {"type": "string"}}},
            {"name": "holding", "paramsSchema": no_params, "resultSchema": {"type": "array"}},
            {"name": "holders", "scope": "schema", "paramsSchema": object(json!(["schema", "id"]), target.clone()), "resultSchema": {"type": "array", "items": {"type": "string"}}},
            {"name": "releaseAll", "scope": "schema", "paramsSchema": object(json!(["schema", "id"]), target.clone()), "resultSchema": {"type": "integer"}, "writes": true},
            {"name": "scribble", "scope": "schema", "paramsSchema": no_params, "resultSchema": true},
            {"name": "tidy", "scope": "schema", "paramsSchema": no_params, "resultSchema": {"type": "integer"}, "writes": true}
        ]
    })
    .to_string()
});

// hears_of reads what a hold's hears says. JSON has no number past the
// finite ones, so the string "Infinity" stands for one, to show what the
// engine says of it.
fn hears_of(hears: Option<&Value>) -> Option<Hears> {
    match hears? {
        Value::String(word) if word == "delete" => Some(Hears::Delete),
        given => Some(Hears::Path {
            path: text(given, "path").to_owned(),
            crosses: match given.get("crosses") {
                Some(Value::String(word)) if word == "Infinity" => Some(f64::INFINITY),
                other => other.and_then(Value::as_f64),
            },
        }),
    }
}

impl Behavior for Holder {
    type Config = HolderConfig;

    fn declaration(&self) -> &str {
        &HOLDER
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<HolderConfig> {
        Ok(HolderConfig {
            veto: raw
                .get("veto")
                .and_then(Value::as_array)
                .map(|kinds| {
                    kinds
                        .iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                })
                .unwrap_or_default(),
            leave: raw.get("leave") == Some(&Value::Bool(true)),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "holds")
                .table(
                    TableSpec::new("holds")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("target_schema", ColumnSpec::text().not_null())
                        .column("target_id", ColumnSpec::text().not_null())
                        .column("key", ColumnSpec::text().not_null())
                        .primary_key(&[
                            "namespace",
                            "schema",
                            "id",
                            "target_schema",
                            "target_id",
                            "key",
                        ]),
                )
                .table(
                    TableSpec::new("notes")
                        .row_id("position")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("note", ColumnSpec::text().not_null()),
                )],
            operations: vec![
                ("hold", |_, cx, params| {
                    let key = params.get("key").and_then(Value::as_str).unwrap_or("");
                    cx.references().add(
                        text(params, "schema"),
                        text(params, "id"),
                        key,
                        hears_of(params.get("hears")),
                    )?;
                    let table = cx.sql().table("holds")?;
                    let [namespace, schema, id] = instance_key(cx);
                    cx.sql().run(
                        format!("INSERT INTO {table} (namespace, schema, id, target_schema, target_id, key) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING"),
                        &[namespace, schema, id, text(params, "schema").into(), text(params, "id").into(), key.into()],
                    )?;
                    Ok(Value::Null)
                }),
                ("release", |_, cx, params| {
                    let key = params.get("key").and_then(Value::as_str).unwrap_or("");
                    let table = cx.sql().table("holds")?;
                    let [namespace, schema, id] = instance_key(cx);
                    cx.sql().run(
                        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND target_schema = ? AND target_id = ? AND key = ?"),
                        &[namespace, schema, id, text(params, "schema").into(), text(params, "id").into(), key.into()],
                    )?;
                    Ok(Value::Bool(cx.references().remove(
                        text(params, "schema"),
                        text(params, "id"),
                        key,
                    )?))
                }),
                ("note", |_, cx, params| {
                    let table = cx.sql().table("notes")?;
                    let [namespace, schema, id] = instance_key(cx);
                    let note = format!(
                        "{} {} {}",
                        text(params, "kind"),
                        text(params, "schema"),
                        text(params, "id")
                    );
                    cx.sql().run(
                        format!(
                            "INSERT INTO {table} (namespace, schema, id, note) VALUES (?, ?, ?, ?)"
                        ),
                        &[namespace, schema, id, note.into()],
                    )?;
                    Ok(Value::Null)
                }),
                ("notes", |_, cx, _| {
                    let table = cx.sql().table("notes")?;
                    let rows = cx.sql().all(
                        format!("SELECT note FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY position"),
                        &instance_key(cx),
                    )?;
                    rows.iter()
                        .map(|row| Ok(Value::from(row.text("note")?)))
                        .collect::<Result<Vec<_>>>()
                        .map(Value::Array)
                }),
                ("holding", |_, cx, _| {
                    Ok(Value::Array(
                        cx.references().list()?.iter().map(reference_json).collect(),
                    ))
                }),
            ],
            schema_operations: vec![
                ("holders", |_, cx, params| {
                    let table = cx.sql().table("holds")?;
                    let rows = cx.sql().all(
                        format!("SELECT id FROM {table} WHERE namespace = ? AND schema = ? AND target_schema = ? AND target_id = ? ORDER BY id"),
                        &[cx.namespace().into(), cx.schema().into(), text(params, "schema").into(), text(params, "id").into()],
                    )?;
                    rows.iter()
                        .map(|row| Ok(Value::from(row.text("id")?)))
                        .collect::<Result<Vec<_>>>()
                        .map(Value::Array)
                }),
                ("releaseAll", |_, cx, params| {
                    let table = cx.sql().table("holds")?;
                    let rows = cx.sql().all(
                        format!("SELECT id, key FROM {table} WHERE namespace = ? AND schema = ? AND target_schema = ? AND target_id = ? ORDER BY id"),
                        &[cx.namespace().into(), cx.schema().into(), text(params, "schema").into(), text(params, "id").into()],
                    )?;
                    for row in &rows {
                        cx.instances().invoke(
                            cx.schema(),
                            row.text("id")?,
                            "release",
                            &json!({"schema": text(params, "schema"), "id": text(params, "id"), "key": row.text("key")?}),
                            None,
                        )?;
                    }
                    Ok(Value::from(rows.len()))
                }),
                ("scribble", |_, cx, _| {
                    let table = cx.sql().table("notes")?;
                    cx.sql().run(format!("DELETE FROM {table}"), &[])?;
                    Ok(Value::Null)
                }),
                ("tidy", |_, cx, _| {
                    let table = cx.sql().table("notes")?;
                    let ran = cx.sql().run(
                        format!("DELETE FROM {table} WHERE namespace = ? AND schema = ?"),
                        &[cx.namespace().into(), cx.schema().into()],
                    )?;
                    Ok(Value::from(ran.changes))
                }),
            ],
            fields: vec![("held", |_, view| {
                let held: Vec<Value> = view
                    .references()
                    .list()?
                    .iter()
                    .map(|reference| {
                        Value::from(format!(
                            "{}/{}/{}",
                            reference.schema, reference.id, reference.key
                        ))
                    })
                    .collect();
                Ok((!held.is_empty()).then_some(Value::Array(held)))
            })],
            guard_reference: Some(|_, view, reference, request| {
                let kind = match request {
                    GuardRequest::Create { .. } => "create",
                    GuardRequest::Update { .. } => "update",
                    GuardRequest::Delete { .. } => "delete",
                    GuardRequest::Operation { .. } => "operation",
                };
                if !view.config().veto.iter().any(|vetoed| vetoed == kind) {
                    return Ok(None);
                }
                let key = if reference.key.is_empty() {
                    "no key"
                } else {
                    reference.key.as_str()
                };
                Ok(Some(Veto::from(
                    format!("{} {} holds it ({key})", view.schema(), view.id()).as_str(),
                )))
            }),
            after_reference_change: Some(|_, cx, reference, change| {
                // Its own write changed what it holds: noting it now would
                // be a cycle.
                if cx.writing() {
                    return Ok(());
                }
                cx.instances().invoke(
                    cx.schema(),
                    cx.id(),
                    "note",
                    &json!({"schema": reference.schema, "id": reference.id, "kind": change.kind()}),
                    None,
                )?;
                if matches!(change, InstanceChange::Delete) && !cx.config().leave {
                    cx.instances().invoke(
                        cx.schema(),
                        cx.id(),
                        "release",
                        &json!({"schema": reference.schema, "id": reference.id, "key": reference.key}),
                        None,
                    )?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

fn reach_behaviors() -> BehaviorSet {
    fixtures().with(Reader).with(Holder)
}

fn open(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_with(reach_behaviors(), configure)
}

/// A policy that records every question and lets alice do anything; bob
/// gets what the rule allows.
fn recording(rule: impl Fn(&AccessRequest<'_>) -> bool + Send + Sync + 'static) -> RecordingPolicy {
    RecordingPolicy::new(move |request| request.principal().subject == "alice" || rule(request))
}

fn with_policy(policy: &RecordingPolicy) -> impl FnOnce(&mut EngineOptions) {
    let policy = policy.clone();
    move |options| options.policy = Some(Policy::custom(policy))
}

fn namespaces(names: &[&str], shared: Option<&str>) -> impl FnOnce(&mut EngineOptions) {
    let names: Vec<String> = names.iter().map(|&name| name.to_owned()).collect();
    let shared = shared.map(str::to_owned);
    move |options| options.namespaces = NamespaceOptions { names, shared }
}

/// A schema named name whose instance type has a title, an optional
/// partner, and the behaviors.
fn document(name: &str, behaviors: Value) -> Value {
    let mut document = schema_document(
        name,
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "partner", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    document["types"][name]["behaviors"] = behaviors;
    document
}

fn publish_named(engine: &Engine, name: &str, behaviors: Value, namespace: Option<&str>) {
    publish(engine, &document(name, behaviors), namespace);
}

fn make_in(engine: &Engine, schema: &str, id: &str, data: Value, namespace: Option<&str>) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                namespace,
                ..Default::default()
            },
        )
        .unwrap();
}

fn make(engine: &Engine, schema: &str, id: &str, title: &str) {
    make_in(engine, schema, id, json!({ "title": title }), None);
}

// A Note reads and holds; an Item counts to 5 and can be flagged.
fn world(target: &Target, configure: impl FnOnce(&mut EngineOptions), holder: Value) -> Engine {
    let engine = open(target, configure);
    publish_named(
        &engine,
        "Note",
        json!([{"name": "test.Reader"}, {"name": "test.Holder", "config": holder}]),
        None,
    );
    publish_named(
        &engine,
        "Item",
        json!([{"name": "test.Counter", "config": {"start": 1, "limit": 5}}, {"name": "test.Flag"}]),
        None,
    );
    make(&engine, "Note", "n1", "First");
    make(&engine, "Note", "n2", "Second");
    make(&engine, "Item", "i1", "Desk");
    make(&engine, "Item", "i2", "Lamp");
    engine
}

fn call_in(
    engine: &Engine,
    who: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
    namespace: Option<&str>,
) -> Result<Value> {
    engine.instances().invoke(
        who,
        schema,
        id,
        operation,
        &params,
        WriteOptions {
            namespace,
            ..Default::default()
        },
    )
}

fn call(
    engine: &Engine,
    who: &Principal,
    schema: &str,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value> {
    call_in(engine, who, schema, id, operation, params, None)
}

fn invoke_schema(
    engine: &Engine,
    who: &Principal,
    operation: &str,
    params: Value,
) -> Result<Value> {
    engine
        .instances()
        .invoke_schema(who, "Note", operation, &params, None)
}

fn get(engine: &Engine, schema: &str, id: &str) -> Option<InstanceRecord> {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .unwrap()
}

fn delete_in(engine: &Engine, who: &Principal, id: &str, namespace: Option<&str>) -> Result<bool> {
    engine.instances().delete(
        who,
        "Item",
        id,
        WriteOptions {
            namespace,
            ..Default::default()
        },
    )
}

fn delete(engine: &Engine, who: &Principal, id: &str) -> Result<bool> {
    delete_in(engine, who, id, None)
}

fn update(engine: &Engine, schema: &str, id: &str, patch: Value) -> Result<InstanceRecord> {
    engine
        .instances()
        .update(&alice(), schema, id, &patch, WriteOptions::default())
}

fn notes(engine: &Engine, id: &str) -> Value {
    call(engine, &alice(), "Note", id, "notes", json!({})).unwrap()
}

fn hold(engine: &Engine, id: &str, params: Value) {
    call(engine, &alice(), "Note", id, "hold", params).unwrap();
}

fn head(engine: &Engine) -> i64 {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                after: Some(EventsAfter::Head),
                ..Default::default()
            },
        )
        .unwrap()
        .next
}

fn events_after(engine: &Engine, after: i64) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                after: Some(EventsAfter::Cursor(after)),
                limit: Some(500),
                ..Default::default()
            },
        )
        .unwrap()
        .events
}

// The events after a cursor: kind, schema, instance, and the operation of
// an operation event or the actor of another.
fn events_of(engine: &Engine, after: i64) -> Vec<Value> {
    events_after(engine, after)
        .iter()
        .map(|event| {
            let last = if event.kind == "operation" {
                event.change["operation"].clone()
            } else {
                json!(event.actor)
            };
            json!([event.kind, event.schema, event.instance_id, last])
        })
        .collect()
}

fn count(engine: &Engine, id: &str) -> Value {
    get(engine, "Item", id).unwrap().behaviors["test.Counter"]["count"].clone()
}

fn pokes(engine: &Engine, id: &str) -> Value {
    get(engine, "Note", id).unwrap().behaviors["test.Reader"]["pokes"].clone()
}

#[test]
fn reads_another_instance_with_every_behavior_field_the_ones_named_or_none() {
    for target in targets("reads_another_instance") {
        let engine = world(&target, |_| {}, json!({}));
        let peek = |fields: Option<Value>| {
            let mut params = json!({"schema": "Item", "id": "i1"});
            if let Some(fields) = fields {
                params["fields"] = fields;
            }
            call(&engine, &alice(), "Note", "n1", "peek", params)
        };
        assert_eq!(
            peek(None).unwrap(),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 1}, "test.Flag": {"flagged": false}}})
        );
        // A read names a field by its qualified name; one no behavior of
        // the schema declares is left out, and a bare name is refused.
        assert_eq!(
            peek(Some(json!([
                "test.Counter.count",
                "test.Counter.noSuchField",
                "test.Ghost.count"
            ])))
            .unwrap(),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 1}}})
        );
        assert!(defect(peek(Some(json!(["count"]))))
            .1
            .contains("fields is a list of behavior fields by qualified name, <behavior>.<field> (Workflow.status)"));
        assert_eq!(
            peek(Some(json!([]))).unwrap(),
            json!({"data": {"title": "Desk"}, "behaviors": {}})
        );
        assert_eq!(
            call(
                &engine,
                &alice(),
                "Note",
                "n1",
                "peek",
                json!({"schema": "Item", "id": "gone"})
            )
            .unwrap(),
            Value::Null
        );
        let missing = refusal(call(
            &engine,
            &alice(),
            "Note",
            "n1",
            "peek",
            json!({"schema": "Nothing", "id": "x"}),
        ));
        assert_eq!(
            (missing.code(), missing.message()),
            (
                EngineErrorCode::NotFound,
                "schema Nothing has no live version in namespace default"
            )
        );
    }
}

#[test]
fn get_many_reads_a_batch_of_one_schema_with_one_policy_question_by_id() {
    for target in targets("get_many_reads_a_batch") {
        let policy = recording(|_| true);
        let engine = world(&target, with_policy(&policy), json!({}));
        policy.take();
        assert_eq!(
            call(&engine, &named("bob"), "Note", "n1", "peekMany", json!({"schema": "Item", "ids": ["i2", "gone", "i1", "i2"], "fields": ["test.Counter.count"]})).unwrap(),
            json!([
                ["i2", {"data": {"title": "Lamp"}, "behaviors": {"test.Counter": {"count": 1}}}],
                ["i1", {"data": {"title": "Desk"}, "behaviors": {"test.Counter": {"count": 1}}}]
            ])
        );
        assert_eq!(
            policy
                .take()
                .iter()
                .map(|asked| json!([asked["action"], asked["schema"], asked.get("operation")]))
                .collect::<Vec<_>>(),
            [
                json!(["read", "Note", "peekMany"]),
                json!(["read", "Item", null])
            ]
        );
        let too_many: Vec<String> = (0..501).map(|index| format!("i{index}")).collect();
        assert!(defect(call(
            &engine,
            &alice(),
            "Note",
            "n1",
            "peekMany",
            json!({"schema": "Item", "ids": too_many})
        ))
        .1
        .contains("instances.getMany reads at most 500 instances at once, not 501"));
    }
}

#[test]
fn each_read_asks_the_policy_for_read_on_the_schema_it_names_as_the_caller() {
    for target in targets("each_read_asks_the_policy") {
        let policy = recording(|request| request.schema() == Some("Note"));
        let engine = world(&target, with_policy(&policy), json!({}));
        let bob = named("bob");
        let peek = || {
            call(
                &engine,
                &bob,
                "Note",
                "n1",
                "peek",
                json!({"schema": "Item", "id": "i1"}),
            )
        };
        policy.take();
        let refused = refusal(peek());
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not read Item in namespace default"
            )
        );
        assert_eq!(
            policy.take().last().unwrap(),
            &json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Item"})
        );
        // The same read again asks again: nothing is remembered between reads.
        refusal(peek());
        assert_eq!(
            policy
                .take()
                .iter()
                .filter(|asked| asked["schema"] == "Item")
                .count(),
            1
        );
        assert_eq!(
            call(
                &engine,
                &bob,
                "Note",
                "n1",
                "peek",
                json!({"schema": "Note", "id": "n2", "fields": []})
            )
            .unwrap(),
            json!({"data": {"title": "Second"}, "behaviors": {}})
        );
    }
}

#[test]
fn a_field_reader_reads_as_the_caller() {
    for target in targets("a_field_reader_reads_as_the_caller") {
        let policy = recording(|request| request.schema() == Some("Note"));
        let engine = open(&target, with_policy(&policy));
        publish_named(
            &engine,
            "Item",
            json!([{"name": "test.Counter", "config": {"start": 1}}]),
            None,
        );
        publish_named(
            &engine,
            "Note",
            json!([{"name": "test.Reader", "config": {"partnerSchema": "Item"}}]),
            None,
        );
        make(&engine, "Item", "i1", "Desk");
        make_in(
            &engine,
            "Note",
            "n1",
            json!({"title": "First", "partner": "i1"}),
            None,
        );
        assert_eq!(
            get(&engine, "Note", "n1").unwrap().behaviors["test.Reader"]["echo"],
            "Desk"
        );
        let bob = named("bob");
        assert_eq!(
            code(
                engine
                    .instances()
                    .get(&bob, "Note", "n1", Default::default())
            ),
            EngineErrorCode::Forbidden
        );
        make(&engine, "Note", "n2", "Alone");
        let alone = engine
            .instances()
            .get(&bob, "Note", "n2", Default::default())
            .unwrap()
            .unwrap();
        assert_eq!(
            fields_of(&alone),
            json!({"data": {"title": "Alone"}, "behaviors": {"test.Reader": {"pokes": 0}}})
        );
    }
}

#[test]
fn reads_nest_at_most_16_deep_and_a_cycle_of_field_reads_ends_with_a_behavior_error() {
    for target in targets("reads_nest_at_most_16_deep") {
        let engine = world(&target, |_| {}, json!({}));
        update(&engine, "Note", "n1", json!({"partner": "n2"})).unwrap();
        assert_eq!(
            get(&engine, "Note", "n1").unwrap().behaviors["test.Reader"]["echo"],
            "Second"
        );
        assert_eq!(
            defect(update(&engine, "Note", "n2", json!({"partner": "n1"}))).1,
            "behavior test.Reader: instances.get: calls nest more than 16 deep"
        );
        assert_eq!(
            get(&engine, "Note", "n2").unwrap().data.get("partner"),
            None,
            "the update rolled back"
        );
    }
}

#[test]
fn schemas_config_reads_the_config_another_schema_gives_a_behavior_and_readable_asks_the_policy() {
    for target in targets("schemas_config_reads_the_config") {
        let policy = recording(|request| request.schema() == Some("Note"));
        let engine = world(&target, with_policy(&policy), json!({}));
        let config_of = |who: &Principal, schema: &str, behavior: &str| {
            call(
                &engine,
                who,
                "Note",
                "n1",
                "configOf",
                json!({"schema": schema, "behavior": behavior}),
            )
        };
        let bob = named("bob");
        assert_eq!(
            config_of(&alice(), "Item", "test.Counter").unwrap(),
            json!({"start": 1, "limit": 5})
        );
        assert_eq!(config_of(&alice(), "Item", "test.Flag").unwrap(), json!({}));
        assert_eq!(
            config_of(&alice(), "Item", "test.Holder").unwrap(),
            Value::Null
        );
        refusal(config_of(&alice(), "Nothing", "test.Flag"));
        policy.take();
        assert_eq!(config_of(&bob, "Note", "test.Holder").unwrap(), json!({}));
        assert_eq!(
            policy
                .take()
                .iter()
                .map(|asked| json!([asked["schema"], asked["operation"]]))
                .collect::<Vec<_>>(),
            [json!(["Note", "configOf"])],
            "its own schema asks nothing more"
        );
        assert_eq!(
            code(config_of(&bob, "Item", "test.Flag")),
            EngineErrorCode::Forbidden
        );
        let can_read = |who: &Principal, schema: &str| {
            call(
                &engine,
                who,
                "Note",
                "n1",
                "canRead",
                json!({ "schema": schema }),
            )
            .unwrap()
        };
        assert_eq!(can_read(&bob, "Item"), false);
        assert_eq!(can_read(&bob, "Note"), true);
        assert_eq!(can_read(&alice(), "Item"), true);
    }
}

#[test]
fn reads_stay_in_the_namespace_and_a_schema_the_shared_namespace_holds_is_looked_up_there() {
    for target in targets("reads_stay_in_the_namespace") {
        let engine = open(
            &target,
            namespaces(&["east", "west", "library"], Some("library")),
        );
        publish_named(
            &engine,
            "Item",
            json!([{"name": "test.Counter", "config": {"start": 1}}]),
            Some("library"),
        );
        for namespace in ["east", "west"] {
            publish_named(
                &engine,
                "Note",
                json!([{"name": "test.Reader"}, {"name": "test.Holder"}]),
                Some(namespace),
            );
            make_in(
                &engine,
                "Note",
                "n1",
                json!({ "title": namespace }),
                Some(namespace),
            );
        }
        make_in(
            &engine,
            "Item",
            "i1",
            json!({"title": "East desk"}),
            Some("east"),
        );
        let peek = |namespace: &str| {
            call_in(
                &engine,
                &alice(),
                "Note",
                "n1",
                "peek",
                json!({"schema": "Item", "id": "i1"}),
                Some(namespace),
            )
        };
        assert_eq!(
            peek("east").unwrap(),
            json!({"data": {"title": "East desk"}, "behaviors": {"test.Counter": {"count": 1}}})
        );
        assert_eq!(peek("west").unwrap(), Value::Null);
        let refused = refusal(call_in(
            &engine,
            &alice(),
            "Note",
            "n1",
            "hold",
            json!({"schema": "Item", "id": "i1"}),
            Some("west"),
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::NotFound,
                "Item i1 does not exist in namespace west"
            )
        );
    }
}

#[test]
fn an_invoke_runs_the_targets_guards_handler_after_change_and_event_as_the_caller_in_its_transaction(
) {
    for target in targets("an_invoke_runs_the_targets_guards") {
        let policy = recording(|_| true);
        let engine = world(&target, with_policy(&policy), json!({}));
        let from = head(&engine);
        policy.take();
        assert_eq!(
            call(
                &engine,
                &named("bob"),
                "Note",
                "n1",
                "poke",
                json!({"schema": "Item", "id": "i1", "operation": "increment", "params": {"by": 2}})
            )
            .unwrap(),
            json!({"count": 3})
        );
        assert_eq!(
            policy
                .take()
                .iter()
                .map(|asked| json!([
                    asked["principal"],
                    asked["action"],
                    asked["schema"],
                    asked["operation"]
                ]))
                .collect::<Vec<_>>(),
            [
                json!(["bob", "write", "Note", "poke"]),
                json!(["bob", "write", "Item", "increment"])
            ]
        );
        let item = get(&engine, "Item", "i1").unwrap();
        assert_eq!(
            (
                item.behaviors["test.Counter"]["count"].clone(),
                item.seq,
                item.updated_by.as_str()
            ),
            (json!(3), 2, "bob")
        );
        // The target's event first, as its operation finished first; both are bob's.
        assert_eq!(
            events_of(&engine, from),
            [
                json!(["operation", "Item", "i1", "increment"]),
                json!(["operation", "Note", "n1", "poke"])
            ]
        );
        assert_eq!(
            events_after(&engine, from)
                .iter()
                .map(|event| json!([event.actor, event.change["patch"]]))
                .collect::<Vec<_>>(),
            [
                json!(["bob", {"behaviors": {"test.Counter": {"count": 3}}}]),
                json!(["bob", {"behaviors": {"test.Reader": {"pokes": 1}}}])
            ]
        );
    }
}

#[test]
fn the_target_refuses_as_it_would_refuse_a_caller_its_guards_its_parameters_its_policy() {
    for target in targets("the_target_refuses_as_it_would") {
        let policy = recording(|request| request.schema() == Some("Note"));
        let engine = world(&target, with_policy(&policy), json!({}));
        let poke = |who: &Principal, operation: &str, params: Value| {
            call(
                &engine,
                who,
                "Note",
                "n1",
                "poke",
                json!({"schema": "Item", "id": "i1", "operation": operation, "params": params}),
            )
        };
        let limit = refusal(poke(&alice(), "increment", json!({"by": 5})));
        assert_eq!(
            limit.message(),
            "behavior test.Counter vetoes increment of Item i1: the count would pass its limit, 5"
        );
        let (behavior, action, ..) = veto::<Value>(Err(Error::Engine(limit)));
        assert_eq!(
            (behavior.as_str(), action.as_str()),
            ("test.Counter", "increment")
        );
        call(
            &engine,
            &alice(),
            "Item",
            "i1",
            "flag",
            json!({"reason": "broken leg"}),
        )
        .unwrap();
        let (behavior, _, reason, ..) = veto(poke(&alice(), "increment", json!({})));
        assert_eq!(
            (behavior.as_str(), reason.as_str()),
            ("test.Flag", "it is flagged: broken leg")
        );
        call(&engine, &alice(), "Item", "i1", "unflag", json!({})).unwrap();
        assert_eq!(
            code(poke(&alice(), "increment", json!({"by": 0}))),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            refusal(poke(&named("bob"), "increment", json!({}))).message(),
            "bob may not call increment (write) on Item in namespace default"
        );
        assert_eq!(
            pokes(&engine, "n1"),
            0,
            "every refusal rolled the poke back"
        );
        assert_eq!(count(&engine, "i1"), 1);
    }
}

#[test]
fn a_failure_rolls_back_every_instance_the_call_changed_and_one_the_behavior_catches_the_invoked_operation_alone(
) {
    for target in targets("a_failure_rolls_back_every_instance") {
        let engine = world(&target, |_| {}, json!({}));
        let from = head(&engine);
        refusal(call(
            &engine,
            &alice(),
            "Note",
            "n1",
            "poke",
            json!({"schema": "Item", "id": "i1", "operation": "increment", "fail": true}),
        ));
        assert_eq!(count(&engine, "i1"), 1);
        assert!(events_of(&engine, from).is_empty());
        assert_eq!(
            call(
                &engine,
                &alice(),
                "Note",
                "n1",
                "pokeCatch",
                json!({"schema": "Note", "id": "n2", "operation": "failAfterWrite"})
            )
            .unwrap(),
            json!({"threw": "EngineError"})
        );
        assert_eq!(
            (
                pokes(&engine, "n1"),
                pokes(&engine, "n2"),
                get(&engine, "Note", "n2").unwrap().seq
            ),
            (json!(1), json!(0), 1)
        );
        assert_eq!(
            events_of(&engine, from),
            [json!(["operation", "Note", "n1", "pokeCatch"])]
        );
    }
}

#[test]
fn a_guard_and_a_read_only_operation_reach_read_only_operations_only() {
    for target in targets("a_guard_and_a_read_only_operation") {
        let engine = world(&target, |_| {}, json!({}));
        call(&engine, &alice(), "Item", "i1", "increment", json!({})).unwrap();
        let reach = |operation: &str, of: &str| {
            call(
                &engine,
                &alice(),
                "Note",
                "n1",
                operation,
                json!({"schema": "Item", "id": "i1", "operation": of}),
            )
        };
        assert_eq!(reach("pokeRead", "history").unwrap(), json!([1]));
        assert_eq!(
            defect(reach("pokeRead", "increment")).1,
            "behavior test.Reader: a read cannot invoke increment of Item, which writes; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can"
        );
        assert_eq!(reach("guarded", "history").unwrap(), json!("ran"));
        defect(reach("guarded", "increment"));
        assert_eq!(count(&engine, "i1"), 2);
    }
}

#[test]
fn invoking_a_writing_operation_of_an_instance_whose_write_runs_up_the_call_is_a_cycle() {
    for target in targets("invoking_a_writing_operation_is_a_cycle") {
        let engine = world(&target, |_| {}, json!({}));
        let poke = |params: Value| call(&engine, &alice(), "Note", "n1", "poke", params);
        assert_eq!(
            defect(poke(json!({"schema": "Note", "id": "n1", "operation": "failAfterWrite"}))).1,
            "behavior test.Reader: invoking failAfterWrite of Note n1 is a cycle: a write of Note n1 is still running up this call"
        );
        assert!(defect(poke(json!({"schema": "Note", "id": "n2", "operation": "poke", "params": {"schema": "Note", "id": "n1", "operation": "failAfterWrite"}})))
            .1
            .contains("invoking failAfterWrite of Note n1 is a cycle"));
        // A read-only operation of an instance being written runs: it reads
        // what the call has written so far.
        assert_eq!(
            poke(json!({"schema": "Note", "id": "n2", "operation": "pokeRead", "params": {"schema": "Note", "id": "n1", "operation": "peek", "params": {"schema": "Note", "id": "n1", "fields": ["test.Reader.pokes"]}}})).unwrap(),
            json!({"data": {"title": "First"}, "behaviors": {"test.Reader": {"pokes": 1}}})
        );
    }
}

fn chain(from: usize, to: usize) -> Value {
    if from == to {
        json!({"schema": "Item", "id": "i1", "operation": "increment"})
    } else {
        json!({"schema": "Note", "id": format!("n{}", from + 1), "operation": "poke", "params": chain(from + 1, to)})
    }
}

#[test]
fn invokes_nest_at_most_16_deep() {
    for target in targets("invokes_nest_at_most_16_deep") {
        let engine = world(&target, |_| {}, json!({}));
        for index in 3..=18 {
            make(
                &engine,
                "Note",
                &format!("n{index}"),
                &format!("Note {index}"),
            );
        }
        assert_eq!(
            call(&engine, &alice(), "Note", "n1", "poke", chain(1, 14)).unwrap(),
            json!({"count": 2})
        );
        let deep = defect(call(&engine, &alice(), "Note", "n1", "poke", chain(1, 16))).1;
        assert!(
            deep.contains("operation poke: calls nest more than 16 deep")
                || deep.contains("operation increment: calls nest more than 16 deep"),
            "{deep}"
        );
        assert_eq!(count(&engine, "i1"), 2);
    }
}

#[test]
fn add_asks_read_on_the_target_and_needs_it_to_exist_list_keeps_the_order_remove_says_whether_there_was_one(
) {
    for target in targets("add_asks_read_on_the_target") {
        let policy = recording(|request| request.schema() == Some("Note"));
        let engine = world(&target, with_policy(&policy), json!({}));
        let gone = refusal(call(
            &engine,
            &alice(),
            "Note",
            "n1",
            "hold",
            json!({"schema": "Item", "id": "gone"}),
        ));
        assert_eq!(
            (gone.code(), gone.message()),
            (
                EngineErrorCode::NotFound,
                "Item gone does not exist in namespace default"
            )
        );
        assert_eq!(
            code(call(
                &engine,
                &named("bob"),
                "Note",
                "n1",
                "hold",
                json!({"schema": "Item", "id": "i1"})
            )),
            EngineErrorCode::Forbidden
        );
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i2", "key": "b"}),
        );
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "key": "a"}),
        );
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "key": "a"}),
        );
        hold(&engine, "n1", json!({"schema": "Note", "id": "n2"}));
        let holding = |id: &str| call(&engine, &alice(), "Note", id, "holding", json!({})).unwrap();
        assert_eq!(
            holding("n1"),
            json!([
                {"schema": "Item", "id": "i2", "key": "b"},
                {"schema": "Item", "id": "i1", "key": "a"},
                {"schema": "Note", "id": "n2", "key": ""}
            ])
        );
        assert_eq!(
            holding("n2"),
            json!([]),
            "a behavior's references are its instance's own"
        );
        let release = || {
            call(
                &engine,
                &alice(),
                "Note",
                "n1",
                "release",
                json!({"schema": "Item", "id": "i2", "key": "b"}),
            )
            .unwrap()
        };
        assert_eq!(release(), true);
        assert_eq!(release(), false);
        assert_eq!(
            get(&engine, "Note", "n1").unwrap().behaviors["test.Holder"]["held"],
            json!(["Item/i1/a", "Note/n2/"])
        );
    }
}

#[test]
fn a_referencing_behaviors_guard_vetoes_the_changes_of_a_held_instance_it_names_whoever_the_caller()
{
    for target in targets("a_referencing_behaviors_guard_vetoes") {
        let policy = recording(|request| request.schema() == Some("Item"));
        let engine = world(
            &target,
            with_policy(&policy),
            json!({"veto": ["delete", "operation"]}),
        );
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "key": "desk"}),
        );
        let bob = named("bob");
        let deleted = refusal(delete(&engine, &bob, "i1"));
        assert_eq!(
            deleted.message(),
            "behavior test.Holder vetoes delete of Item i1: Note n1 holds it (desk)"
        );
        let (behavior, action, ..) = veto::<bool>(Err(Error::Engine(deleted)));
        assert_eq!(
            (behavior.as_str(), action.as_str()),
            ("test.Holder", "delete")
        );
        assert_eq!(
            veto(call(&engine, &bob, "Item", "i1", "increment", json!({}))).1,
            "increment"
        );
        // An update is not in its list (its hook then notes it, as alice
        // may), and a read-only operation is never asked.
        update(&engine, "Item", "i1", json!({"title": "Oak desk"})).unwrap();
        assert_eq!(
            call(&engine, &bob, "Item", "i1", "history", json!({})).unwrap(),
            json!([])
        );
        assert_eq!(
            get(&engine, "Item", "i1").unwrap().data["title"],
            "Oak desk"
        );
    }
}

#[test]
fn after_a_change_of_a_held_instance_the_hook_runs_on_the_holder_through_its_own_operation_and_event(
) {
    for target in targets("after_a_change_of_a_held_instance") {
        let engine = world(&target, |_| {}, json!({}));
        hold(&engine, "n1", json!({"schema": "Item", "id": "i1"}));
        hold(&engine, "n2", json!({"schema": "Item", "id": "i1"}));
        let from = head(&engine);
        update(&engine, "Item", "i1", json!({"title": "Oak desk"})).unwrap();
        call(&engine, &alice(), "Item", "i1", "increment", json!({})).unwrap();
        call(&engine, &alice(), "Item", "i1", "history", json!({})).unwrap();
        assert_eq!(
            notes(&engine, "n1"),
            json!(["update Item i1", "operation Item i1"])
        );
        assert_eq!(
            events_of(&engine, from),
            [
                json!(["update", "Item", "i1", "alice"]),
                json!(["operation", "Note", "n1", "note"]),
                json!(["operation", "Note", "n2", "note"]),
                json!(["operation", "Item", "i1", "increment"]),
                json!(["operation", "Note", "n1", "note"]),
                json!(["operation", "Note", "n2", "note"]),
            ]
        );
    }
}

#[test]
fn a_holder_whose_own_write_changes_what_it_holds_hears_it_with_writing_set() {
    for target in targets("a_holder_whose_own_write_changes_what_it_holds") {
        let engine = world(&target, |_| {}, json!({}));
        hold(&engine, "n1", json!({"schema": "Item", "id": "i1"}));
        hold(&engine, "n2", json!({"schema": "Item", "id": "i1"}));
        let from = head(&engine);
        // n1's poke increments i1 while n1's own write runs: n1's hook sees
        // writing and invokes nothing, which would be a cycle; n2's notes it.
        call(
            &engine,
            &alice(),
            "Note",
            "n1",
            "poke",
            json!({"schema": "Item", "id": "i1", "operation": "increment"}),
        )
        .unwrap();
        assert_eq!(notes(&engine, "n1"), json!([]));
        assert_eq!(notes(&engine, "n2"), json!(["operation Item i1"]));
        assert_eq!(
            events_of(&engine, from),
            [
                json!(["operation", "Item", "i1", "increment"]),
                json!(["operation", "Note", "n2", "note"]),
                json!(["operation", "Note", "n1", "poke"]),
            ]
        );
    }
}

#[test]
fn a_delete_no_guard_vetoes_runs_the_hooks_which_remove_the_references_through_an_operation() {
    for target in targets("a_delete_no_guard_vetoes_runs_the_hooks") {
        let engine = world(&target, |_| {}, json!({}));
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "key": "a"}),
        );
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "key": "b"}),
        );
        let from = head(&engine);
        assert!(delete(&engine, &alice(), "i1").unwrap());
        assert_eq!(
            events_of(&engine, from),
            [
                json!(["delete", "Item", "i1", "alice"]),
                json!(["operation", "Note", "n1", "note"]),
                json!(["operation", "Note", "n1", "release"]),
                json!(["operation", "Note", "n1", "note"]),
                json!(["operation", "Note", "n1", "release"]),
            ]
        );
        assert_eq!(
            call(&engine, &alice(), "Note", "n1", "holding", json!({})).unwrap(),
            json!([])
        );
        // A new instance with the id is no one's.
        make(&engine, "Item", "i1", "New desk");
        delete(&engine, &alice(), "i1").unwrap();
        assert_eq!(
            notes(&engine, "n1"),
            json!(["delete Item i1", "delete Item i1"])
        );
    }
}

#[test]
fn a_reference_a_hook_leaves_behind_refuses_the_delete_which_rolls_back_with_everything_the_hooks_did(
) {
    for target in targets("a_reference_a_hook_leaves_behind") {
        let engine = world(&target, |_| {}, json!({"leave": true}));
        hold(&engine, "n1", json!({"schema": "Item", "id": "i1"}));
        let from = head(&engine);
        assert_eq!(
            defect(delete(&engine, &alice(), "i1")).1,
            "behavior test.Holder: Note n1 still refers to Item i1, which is deleted: afterReferenceChange must remove the reference, through an operation of Note it invokes"
        );
        assert_eq!(get(&engine, "Item", "i1").unwrap().data["title"], "Desk");
        assert_eq!(notes(&engine, "n1"), json!([]));
        assert!(events_of(&engine, from).is_empty());
    }
}

#[test]
fn the_hooks_act_as_the_caller_and_one_who_may_not_write_the_holder_cannot_delete_what_it_holds() {
    for target in targets("the_hooks_act_as_the_caller") {
        let policy =
            recording(|request| request.schema() == Some("Item") || request.action() == "read");
        let engine = world(&target, with_policy(&policy), json!({}));
        hold(&engine, "n1", json!({"schema": "Item", "id": "i1"}));
        let bob = named("bob");
        let refused = refusal(delete(&engine, &bob, "i1"));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not call note (write) on Note in namespace default"
            )
        );
        assert!(get(&engine, "Item", "i1").is_some());
        assert!(delete(&engine, &bob, "i2").unwrap());
    }
}

#[test]
fn deleting_the_holder_drops_its_references_and_a_reference_to_itself_is_never_asked() {
    for target in targets("deleting_the_holder_drops_its_references") {
        let engine = world(&target, |_| {}, json!({"leave": true, "veto": ["delete"]}));
        hold(&engine, "n1", json!({"schema": "Item", "id": "i1"}));
        hold(&engine, "n1", json!({"schema": "Note", "id": "n1"}));
        assert!(engine
            .instances()
            .delete(&alice(), "Note", "n1", WriteOptions::default())
            .unwrap());
        assert!(delete(&engine, &alice(), "i1").unwrap());
    }
}

#[test]
fn references_stay_in_their_namespace() {
    for target in targets("references_stay_in_their_namespace") {
        let engine = open(&target, namespaces(&["east", "west"], None));
        for namespace in ["east", "west"] {
            publish_named(
                &engine,
                "Note",
                json!([{"name": "test.Reader"}, {"name": "test.Holder", "config": {"veto": ["delete"]}}]),
                Some(namespace),
            );
            publish_named(
                &engine,
                "Item",
                json!([{"name": "test.Counter", "config": {"start": 1}}]),
                Some(namespace),
            );
            make_in(
                &engine,
                "Note",
                "n1",
                json!({ "title": namespace }),
                Some(namespace),
            );
            make_in(
                &engine,
                "Item",
                "i1",
                json!({ "title": namespace }),
                Some(namespace),
            );
        }
        call_in(
            &engine,
            &alice(),
            "Note",
            "n1",
            "hold",
            json!({"schema": "Item", "id": "i1"}),
            Some("east"),
        )
        .unwrap();
        assert!(delete_in(&engine, &alice(), "i1", Some("west")).unwrap());
        assert_eq!(
            veto(delete_in(&engine, &alice(), "i1", Some("east"))).0,
            "test.Holder"
        );
    }
}

const COUNT: &str = "/behaviors/test.Counter/count";

#[test]
fn one_that_hears_a_value_runs_its_hook_when_a_change_moves_it_with_crosses_across_the_number_and_at_the_delete(
) {
    for target in targets("one_that_hears_a_value") {
        let engine = world(&target, |_| {}, json!({}));
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "hears": {"path": COUNT, "crosses": 3}}),
        );
        hold(
            &engine,
            "n2",
            json!({"schema": "Item", "id": "i1", "hears": {"path": COUNT}}),
        );
        assert_eq!(
            call(&engine, &alice(), "Note", "n1", "holding", json!({})).unwrap(),
            json!([{"schema": "Item", "id": "i1", "key": "", "hears": {"path": COUNT, "crosses": 3}}])
        );
        // The count starts at 1. A change that leaves it is heard by neither.
        update(&engine, "Item", "i1", json!({"title": "Oak desk"})).unwrap();
        call(
            &engine,
            &alice(),
            "Item",
            "i1",
            "flag",
            json!({"reason": "checking"}),
        )
        .unwrap();
        call(&engine, &alice(), "Item", "i1", "unflag", json!({})).unwrap();
        assert_eq!(
            (notes(&engine, "n1"), notes(&engine, "n2")),
            (json!([]), json!([]))
        );
        // 1 to 2 moves it; 2 to 3 moves it across 3; 3 to 4 on the same side.
        for _ in 2..=4 {
            call(&engine, &alice(), "Item", "i1", "increment", json!({})).unwrap();
        }
        assert_eq!(notes(&engine, "n1"), json!(["operation Item i1"]));
        assert_eq!(
            notes(&engine, "n2"),
            json!([
                "operation Item i1",
                "operation Item i1",
                "operation Item i1"
            ])
        );
        // Every reference hears the delete, and must let go of it.
        delete(&engine, &alice(), "i1").unwrap();
        for id in ["n1", "n2"] {
            assert_eq!(
                notes(&engine, id).as_array().unwrap().last().unwrap(),
                "delete Item i1"
            );
        }
        assert_eq!(
            call(&engine, &alice(), "Note", "n1", "holding", json!({})).unwrap(),
            json!([])
        );
    }
}

#[test]
fn a_value_that_is_not_a_number_is_a_side_of_its_own_and_becoming_one_crosses_every_number() {
    for target in targets("a_value_that_is_not_a_number") {
        let engine = open(&target, |_| {});
        publish_named(
            &engine,
            "Note",
            json!([{"name": "test.Reader"}, {"name": "test.Holder"}]),
            None,
        );
        let sized = schema_document(
            "Box",
            json!([
                {"name": "title", "typeRef": {"name": "string"}, "required": true},
                {"name": "size", "typeRef": {"name": "Generic.Int64"}}
            ]),
            json!({}),
        );
        publish(&engine, &sized, None);
        make(&engine, "Box", "b1", "Crate");
        make(&engine, "Note", "n1", "Low");
        make(&engine, "Note", "n2", "High");
        hold(
            &engine,
            "n1",
            json!({"schema": "Box", "id": "b1", "hears": {"path": "/data/size", "crosses": 1}}),
        );
        hold(
            &engine,
            "n2",
            json!({"schema": "Box", "id": "b1", "hears": {"path": "/data/size", "crosses": 100}}),
        );
        for size in [json!(50), json!(60), Value::Null] {
            update(&engine, "Box", "b1", json!({ "size": size })).unwrap();
        }
        assert_eq!(
            notes(&engine, "n1"),
            json!(["update Box b1", "update Box b1"])
        );
        assert_eq!(
            notes(&engine, "n2"),
            json!(["update Box b1", "update Box b1"])
        );
    }
}

#[test]
fn one_that_hears_less_than_every_change_is_asked_by_guard_reference_before_a_delete_only() {
    for target in targets("one_that_hears_less_than_every_change") {
        let engine = world(
            &target,
            |_| {},
            json!({"veto": ["update", "delete", "operation"]}),
        );
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "hears": "delete"}),
        );
        hold(
            &engine,
            "n2",
            json!({"schema": "Item", "id": "i2", "hears": {"path": COUNT}}),
        );
        update(&engine, "Item", "i1", json!({"title": "Oak desk"})).unwrap();
        call(&engine, &alice(), "Item", "i1", "increment", json!({})).unwrap();
        call(&engine, &alice(), "Item", "i2", "increment", json!({})).unwrap();
        // 'delete' hears nothing else; a value's move runs the hook, which
        // notes it.
        assert_eq!(
            (notes(&engine, "n1"), notes(&engine, "n2")),
            (json!([]), json!(["operation Item i2"]))
        );
        for id in ["i1", "i2"] {
            let (behavior, action, ..) = veto(delete(&engine, &alice(), id));
            assert_eq!(
                (behavior.as_str(), action.as_str()),
                ("test.Holder", "delete")
            );
        }
    }
}

#[test]
fn recording_one_again_records_what_it_hears_now_and_keeps_its_place() {
    for target in targets("recording_one_again") {
        let engine = world(&target, |_| {}, json!({}));
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "key": "a"}),
        );
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i2", "key": "b"}),
        );
        hold(
            &engine,
            "n1",
            json!({"schema": "Item", "id": "i1", "key": "a", "hears": "delete"}),
        );
        assert_eq!(
            call(&engine, &alice(), "Note", "n1", "holding", json!({})).unwrap(),
            json!([
                {"schema": "Item", "id": "i1", "key": "a", "hears": "delete"},
                {"schema": "Item", "id": "i2", "key": "b"}
            ])
        );
        call(&engine, &alice(), "Item", "i1", "increment", json!({})).unwrap();
        call(&engine, &alice(), "Item", "i2", "increment", json!({})).unwrap();
        assert_eq!(notes(&engine, "n1"), json!(["operation Item i2"]));
    }
}

#[test]
fn add_refuses_a_path_that_is_no_pointer_into_data_or_behaviors_and_a_crosses_that_is_not_finite() {
    for target in targets("add_refuses_a_path_that_is_no_pointer") {
        let engine = world(&target, |_| {}, json!({}));
        let pointer = |path: &str| {
            format!(
                "its path a JSON pointer into the target as a read returns it, /data/<field> or /behaviors/<behavior>/<field>, not {}",
                serde_json::to_string(path).unwrap()
            )
        };
        let mut cases: Vec<(Value, String)> = [
            "count",
            "",
            "/behaviors/test.Counter/count/~2",
            // A pointer into neither part, or to a part or a behavior whole.
            "/count",
            "/data",
            "/behaviors/test.Counter",
            "/behaviors/test.Counter/",
        ]
        .into_iter()
        .map(|path| (json!({ "path": path }), pointer(path)))
        .collect();
        cases.push((
            json!({"path": COUNT, "crosses": "Infinity"}),
            "its crosses a finite number, not null".to_owned(),
        ));
        for (hears, what) in cases {
            assert_eq!(
                defect(call(&engine, &alice(), "Note", "n1", "hold", json!({"schema": "Item", "id": "i1", "hears": hears}))).1,
                format!("behavior test.Holder: references.add: hears is 'delete' or {{ path, crosses? }}, {what}")
            );
        }
    }
}

#[test]
fn a_change_runs_the_hooks_of_the_references_whose_number_it_crosses_out_of_many_on_one_value() {
    for target in targets("a_change_runs_the_hooks_whose_number_it_crosses") {
        let engine = world(&target, |_| {}, json!({}));
        for at in 1..=60 {
            let id = format!("h{at}");
            make(&engine, "Note", &id, &format!("n{at}"));
            hold(
                &engine,
                &id,
                json!({"schema": "Item", "id": "i1", "hears": {"path": COUNT, "crosses": f64::from(at) + 0.5}}),
            );
        }
        let from = head(&engine);
        call(&engine, &alice(), "Item", "i1", "increment", json!({})).unwrap();
        call(&engine, &alice(), "Item", "i1", "increment", json!({})).unwrap();
        // 1 to 2 crosses 1.5 alone, 2 to 3 crosses 2.5 alone.
        assert_eq!(
            events_of(&engine, from),
            [
                json!(["operation", "Item", "i1", "increment"]),
                json!(["operation", "Note", "h1", "note"]),
                json!(["operation", "Item", "i1", "increment"]),
                json!(["operation", "Note", "h2", "note"]),
            ]
        );
    }
}

#[test]
fn a_schema_level_operation_runs_with_no_instance_and_the_routes_do_not_cross() {
    for target in targets("a_schema_level_operation_runs_with_no_instance") {
        let policy = recording(|request| request.action() == "read");
        let engine = world(&target, with_policy(&policy), json!({}));
        hold(&engine, "n2", json!({"schema": "Item", "id": "i1"}));
        hold(&engine, "n1", json!({"schema": "Item", "id": "i1"}));
        policy.take();
        let bob = named("bob");
        let held = json!({"schema": "Item", "id": "i1"});
        assert_eq!(
            invoke_schema(&engine, &bob, "holders", held.clone()).unwrap(),
            json!(["n1", "n2"])
        );
        assert_eq!(
            policy.take(),
            [
                json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Note", "operation": "holders"})
            ]
        );
        let instance = refusal(call(
            &engine,
            &alice(),
            "Note",
            "n1",
            "holders",
            held.clone(),
        ));
        assert_eq!(
            (instance.code(), instance.message()),
            (EngineErrorCode::NotFound, "Note's holders is a schema-level operation: call it on the schema, with no instance")
        );
        let schema = refusal(invoke_schema(&engine, &alice(), "hold", held.clone()));
        assert_eq!(
            (schema.code(), schema.message()),
            (
                EngineErrorCode::NotFound,
                "Note's hold is an instance operation: call it on an instance"
            )
        );
        assert_eq!(
            refusal(invoke_schema(&engine, &alice(), "nothing", json!({}))).message(),
            "schema Note has no schema-level operation nothing (its behaviors' schema-level operations: holders, releaseAll, scribble, tidy)"
        );
        assert_eq!(
            code(invoke_schema(
                &engine,
                &alice(),
                "holders",
                json!({"schema": "Item"})
            )),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            code(invoke_schema(&engine, &bob, "releaseAll", held)),
            EngineErrorCode::Forbidden
        );
    }
}

#[test]
fn a_writing_schema_level_operation_changes_instances_through_the_operations_it_invokes() {
    for target in targets("a_writing_schema_level_operation_changes_instances") {
        let engine = world(&target, |_| {}, json!({}));
        hold(&engine, "n1", json!({"schema": "Item", "id": "i1"}));
        hold(&engine, "n2", json!({"schema": "Item", "id": "i1"}));
        let from = head(&engine);
        let held = json!({"schema": "Item", "id": "i1"});
        assert_eq!(
            invoke_schema(&engine, &alice(), "releaseAll", held.clone()).unwrap(),
            json!(2)
        );
        assert_eq!(
            events_of(&engine, from),
            [
                json!(["operation", "Note", "n1", "release"]),
                json!(["operation", "Note", "n2", "release"])
            ]
        );
        assert_eq!(
            invoke_schema(&engine, &alice(), "holders", held).unwrap(),
            json!([])
        );
    }
}

#[test]
fn a_writing_schema_level_operation_writes_its_behaviors_own_tables_with_no_event_and_a_read_only_one_cannot(
) {
    for target in targets("a_writing_schema_level_operation_writes_its_tables") {
        let engine = world(&target, |_| {}, json!({}));
        for id in ["n1", "n2"] {
            call(
                &engine,
                &alice(),
                "Note",
                id,
                "note",
                json!({"kind": "update", "schema": "Item", "id": "i1"}),
            )
            .unwrap();
        }
        assert_eq!(
            defect(invoke_schema(&engine, &alice(), "scribble", json!({}))).1,
            "behavior test.Holder: a read cannot run a statement that writes; run() is for writes"
        );
        let from = head(&engine);
        let seq = get(&engine, "Note", "n1").unwrap().seq;
        assert_eq!(
            invoke_schema(&engine, &alice(), "tidy", json!({})).unwrap(),
            json!(2)
        );
        assert!(events_of(&engine, from).is_empty());
        assert_eq!(get(&engine, "Note", "n1").unwrap().seq, seq);
        assert_eq!(notes(&engine, "n1"), json!([]));
    }
}

#[test]
fn a_behavior_invokes_schema_level_operations_as_the_caller_a_read_the_read_only_ones_a_write_writing_ones_too(
) {
    for target in targets("a_behavior_invokes_schema_level_operations") {
        let policy = recording(|request| request.action() == "read");
        let engine = world(&target, with_policy(&policy), json!({}));
        hold(&engine, "n2", json!({"schema": "Item", "id": "i1"}));
        policy.take();
        let holders = json!({"schema": "Note", "operation": "holders", "params": {"schema": "Item", "id": "i1"}});
        assert_eq!(
            call(&engine, &named("bob"), "Note", "n1", "peekSchema", holders).unwrap(),
            json!(["n2"])
        );
        assert_eq!(
            policy.take(),
            [
                json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Note", "operation": "peekSchema"}),
                json!({"principal": "bob", "action": "read", "namespace": "default", "schema": "Note", "operation": "holders"}),
            ]
        );
        let release_all = json!({"schema": "Note", "operation": "releaseAll", "params": {"schema": "Item", "id": "i1"}});
        assert_eq!(
            defect(call(&engine, &alice(), "Note", "n1", "peekSchema", release_all.clone())).1,
            "behavior test.Reader: a read cannot invoke releaseAll of Note, which writes; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can"
        );
        let from = head(&engine);
        assert_eq!(
            call(&engine, &alice(), "Note", "n1", "pokeSchema", release_all).unwrap(),
            json!(1)
        );
        assert_eq!(
            events_of(&engine, from),
            [
                json!(["operation", "Note", "n2", "release"]),
                json!(["operation", "Note", "n1", "pokeSchema"])
            ]
        );
        assert_eq!(
            get(&engine, "Note", "n2").unwrap().behaviors["test.Holder"],
            json!({})
        );
        let scope = refusal(call(
            &engine,
            &alice(),
            "Note",
            "n1",
            "peekSchema",
            json!({"schema": "Note", "operation": "notes"}),
        ));
        assert_eq!(
            (scope.code(), scope.message()),
            (
                EngineErrorCode::NotFound,
                "Note's notes is an instance operation: call it on an instance"
            )
        );
        assert_eq!(
            code(call(
                &engine,
                &alice(),
                "Note",
                "n1",
                "peekSchema",
                json!({"schema": "Note", "operation": "holders"})
            )),
            EngineErrorCode::InvalidArgument
        );
    }
}

// test.Peer names another schema, which parse_config reads when the schema
// is defined or published, and notes each read.
struct Peer {
    seen: Seen,
}

impl Behavior for Peer {
    type Config = String;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Peer", "configSchema": {"type": "object", "additionalProperties": false, "required": ["peer"], "properties": {"peer": {"type": "string"}}}}"#
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<String> {
        let name = text(raw, "peer").to_owned();
        let Some(schemas) = target.schemas() else {
            self.seen.lock().unwrap().push(json!("without schemas"));
            return Ok(name);
        };
        let Some(found) = schemas.get(&name)? else {
            return Err(Error::config(format!("{name} has no live version")));
        };
        self.seen.lock().unwrap().push(json!({
            "schema": found.schema,
            "type": found.type_name,
            "fields": found.fields,
            "behaviors": found.behaviors,
            "configs": found.configs,
        }));
        Ok(name)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks::default()
    }
}

#[test]
fn at_define_and_publish_parse_config_reaches_the_other_schemas_as_the_caller_reads_them() {
    for target in targets("parse_config_reaches_the_other_schemas") {
        let policy =
            recording(|request| request.action() != "read" || request.schema() != Some("Item"));
        let seen: Seen = Arc::default();
        let engine = target.open_with(
            reach_behaviors().with(Peer {
                seen: Arc::clone(&seen),
            }),
            with_policy(&policy),
        );
        let peering = |name: &str, of: &str| {
            document(name, json!([{"name": "test.Peer", "config": {"peer": of}}]))
        };
        let define = |who: &Principal, document: &Value| {
            engine
                .schemas()
                .define(who, document, DefineOptions::default())
        };
        let refused = refusal(define(&alice(), &peering("Other", "Item")));
        assert!(
            issues_of(&refused).iter().any(|(_, message)| message
                .contains("behavior test.Peer config: Item has no live version")),
            "{refused}"
        );
        publish_named(
            &engine,
            "Item",
            json!([{"name": "test.Counter", "config": {"start": 0}}]),
            None,
        );
        seen.lock().unwrap().clear();
        policy.take();
        define(&alice(), &peering("Other", "Item")).unwrap();
        assert_eq!(
            *seen.lock().unwrap(),
            [
                json!("without schemas"),
                json!({"schema": "Item", "type": "Item", "fields": {"title": "string", "partner": "string"}, "behaviors": ["test.Counter"], "configs": {"test.Counter": {"start": 0}}}),
            ]
        );
        assert_eq!(
            policy
                .take()
                .iter()
                .map(|asked| json!([asked["principal"], asked["action"], asked["schema"]]))
                .collect::<Vec<_>>(),
            [
                json!(["alice", "define", "Other"]),
                json!(["alice", "read", "Item"])
            ]
        );
        let refused = refusal(define(&named("bob"), &peering("Other", "Item")));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not read Item in namespace default"
            )
        );
        seen.lock().unwrap().clear();
        define(&alice(), &peering("Self", "Self")).unwrap();
        assert_eq!(
            seen.lock().unwrap()[1]["behaviors"],
            json!(["test.Peer"]),
            "its own name, which has no live version yet"
        );
        seen.lock().unwrap().clear();
        engine
            .schemas()
            .publish(&alice(), "Other", SchemaTarget::default())
            .unwrap();
        assert_eq!(
            seen.lock().unwrap()[1]["schema"],
            "Item",
            "publish reads it again"
        );
        seen.lock().unwrap().clear();
        engine
            .instances()
            .create(
                &alice(),
                "Other",
                &json!({"title": "o1"}),
                CreateOptions::default(),
            )
            .unwrap();
        assert!(
            seen.lock()
                .unwrap()
                .iter()
                .all(|read| read == "without schemas"),
            "running a published version reads no other schema"
        );
    }
}

// test.Looker notes the fields parse_config is told each behavior of the
// type declares.
struct Looker {
    seen: Seen,
}

impl Behavior for Looker {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Looker", "fields": [{"name": "glance", "volatile": true}]}"#
    }

    fn parse_config(&self, _: &Value, target: &ConfigTarget<'_>) -> Result<Value> {
        let mut fields = serde_json::Map::new();
        for behavior in target.behaviors() {
            let declared = target.behavior_fields(behavior).unwrap_or_default();
            fields.insert(
                behavior.clone(),
                declared
                    .iter()
                    .map(|field| json!({"name": field.name, "volatile": field.volatile}))
                    .collect(),
            );
        }
        *self.seen.lock().unwrap() = vec![Value::Object(fields)];
        Ok(json!({}))
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            fields: vec![("glance", |_, _| Ok(None))],
            ..Hooks::default()
        }
    }
}

#[test]
fn parse_config_gets_every_behaviors_declared_fields_each_saying_whether_it_is_volatile() {
    for target in targets("parse_config_gets_every_behaviors_declared_fields") {
        let seen: Seen = Arc::default();
        let engine = target.open(reach_behaviors().with(Looker {
            seen: Arc::clone(&seen),
        }));
        engine
            .schemas()
            .define(
                &alice(),
                document("Other", json!([{"name": "test.Counter", "config": {"start": 0}}, {"name": "test.Flag"}, {"name": "test.Looker"}])),
                DefineOptions::default(),
            )
            .unwrap();
        assert_eq!(
            seen.lock().unwrap()[0],
            json!({
                "test.Counter": [{"name": "count", "volatile": false}],
                "test.Flag": [{"name": "flagged", "volatile": false}, {"name": "flagReason", "volatile": false}],
                "test.Looker": [{"name": "glance", "volatile": true}]
            })
        );
    }
}

// test.NeedsLimit reads test.Counter's config from the type's configs.
struct NeedsLimit;

impl Behavior for NeedsLimit {
    type Config = i64;

    fn declaration(&self) -> &str {
        r#"{"name": "test.NeedsLimit", "requires": ["test.Counter"]}"#
    }

    fn parse_config(&self, _: &Value, target: &ConfigTarget<'_>) -> Result<i64> {
        target
            .configs()
            .get("test.Counter")
            .and_then(|counter| counter.get("limit"))
            .and_then(Value::as_i64)
            .ok_or_else(|| {
                Error::config(format!(
                    "it needs test.Counter to set a limit (the type lists {})",
                    target.behaviors().join(", ")
                ))
            })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks::default()
    }
}

#[test]
fn parse_config_gets_the_config_of_every_behavior_the_type_lists_as_the_schema_holds_it() {
    for target in targets("parse_config_gets_the_config_of_every_behavior") {
        let engine = target.open(reach_behaviors().with(NeedsLimit));
        publish_named(
            &engine,
            "Item",
            json!([{"name": "test.Counter", "config": {"start": 0, "limit": 3}}, {"name": "test.NeedsLimit"}]),
            None,
        );
        let refused = refusal(engine.schemas().define(
            &alice(),
            document("Other", json!([{"name": "test.Counter", "config": {"start": 0}}, {"name": "test.NeedsLimit"}])),
            DefineOptions::default(),
        ));
        assert!(
            issues_of(&refused).iter().any(|(_, message)| message.contains(
                "behavior test.NeedsLimit config: it needs test.Counter to set a limit (the type lists test.Counter, test.NeedsLimit)"
            )),
            "{refused}"
        );
    }
}
