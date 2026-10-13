//! Behaviors and creates, the cases of `test/behavior-create-params.test.ts`
//! (a declaration's createParamsSchema, the engine's checks of a create's
//! parameters, the create guard after the insert and before any
//! initialize, each initialize with its own parameters) and
//! `test/behavior-create.test.ts` (a behavior's `instances.create`, from an
//! operation, afterChange, initialize and a schema-level operation, as the
//! caller, in a savepoint, nesting like an invoke); on SQLite and, with the
//! variable set, on Postgres. The describe document, the tools, the routes
//! and the runner's creates are milestone 6's; a parameter that is no JSON
//! has no Rust counterpart.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, CreateOptions, DefineOptions, Engine, EngineError,
    EngineErrorCode, Error, GuardRequest, Hooks, Ids, InstanceChange, InstanceRecord,
    NamespaceOptions, Policy, ReadEvents, Result, SchemaTarget, WriteOptions,
};
use superschematic_engine_testkit::behaviors::{Counter, Hold};
use support::{
    alice, code, defect, fields_of, issue, issues_of, named, publish, publish_item, refusal,
    schema_document, targets, usage, veto, RecordingPolicy,
};

type Seen = Arc<Mutex<Vec<Value>>>;

fn row_json(row: &Row) -> Value {
    Value::Object(
        row.iter()
            .map(|(column, value)| {
                (
                    column.to_owned(),
                    match value {
                        SqlValue::Null => Value::Null,
                        SqlValue::Integer(n) => Value::from(*n),
                        SqlValue::Real(x) => Value::from(*x),
                        SqlValue::Text(text) => Value::from(text.as_str()),
                        SqlValue::Blob(_) => Value::from("<blob>"),
                    },
                )
            })
            .collect(),
    )
}

#[derive(Clone, Default)]
struct TagsConfig {
    refuse: Option<String>,
    code: Option<String>,
}

// test.Tags: records what its guard is asked at a create and what its
// initialize gets, and vetoes a create that gives the tag its config
// refuses; its initialize vetoes the tag late the same way.
struct Tags {
    seen: Seen,
}

impl Behavior for Tags {
    type Config = TagsConfig;

    fn declaration(&self) -> &str {
        r#"{
            "name": "test.Tags",
            "description": "Tags a create gives, which it keeps.",
            "configSchema": {"type": "object", "additionalProperties": false, "properties": {"refuse": {"type": "string"}, "code": {"type": "string"}}},
            "createParamsSchema": {"type": "object", "additionalProperties": false, "properties": {"tags": {"type": "array", "maxItems": 3, "items": {"type": "string", "minLength": 1}}}},
            "fields": [{"name": "tags", "description": "The tags its create gave."}],
            "vetoes": [{"code": "refused_tag", "description": "A create gives a tag its config refuses."}]
        }"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<TagsConfig> {
        Ok(TagsConfig {
            refuse: raw["refuse"].as_str().map(str::to_owned),
            code: raw["code"].as_str().map(str::to_owned),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "tags").column("tags", ColumnSpec::text())],
            guard: Some(|tags, view, request| {
                let GuardRequest::Create { behaviors, .. } = request else {
                    return Ok(None);
                };
                tags.seen.lock().unwrap().push(json!({
                    "step": "guard",
                    "columns": row_json(&view.columns().get()?),
                    "data": *view.data()?,
                    "request": request.to_json(),
                }));
                let given: Vec<&str> = behaviors
                    .get("test.Tags")
                    .and_then(|entry| entry["tags"].as_array())
                    .map(|list| list.iter().filter_map(Value::as_str).collect())
                    .unwrap_or_default();
                let config = view.config();
                Ok(match &config.refuse {
                    Some(refuse) if given.contains(&refuse.as_str()) => {
                        let mut details = Map::new();
                        details.insert("tag".into(), json!(refuse));
                        Some(Veto {
                            reason: format!("tag {refuse} is refused"),
                            code: Some(config.code.clone().unwrap_or_else(|| "refused_tag".into())),
                            details: Some(details),
                        })
                    }
                    _ => None,
                })
            }),
            initialize: Some(|tags, cx, params| {
                tags.seen
                    .lock()
                    .unwrap()
                    .push(json!({"step": "initialize", "params": params}));
                let given = params.get("tags").cloned().unwrap_or(json!([]));
                if given
                    .as_array()
                    .is_some_and(|list| list.contains(&json!("late")))
                {
                    return Err(Error::veto(
                        "test.Tags",
                        "create",
                        cx.schema(),
                        cx.id(),
                        Veto {
                            reason: "tag late is refused".into(),
                            code: Some(
                                cx.config()
                                    .code
                                    .clone()
                                    .unwrap_or_else(|| "refused_tag".into()),
                            ),
                            details: None,
                        },
                    ));
                }
                cx.columns()
                    .set(&[("tags", SqlValue::from(given.to_string()))])
            }),
            after_change: Some(|tags, cx, change| {
                if matches!(change, InstanceChange::Create) {
                    tags.seen.lock().unwrap().push(
                        json!({"step": "afterChange", "columns": row_json(&cx.columns().get()?)}),
                    );
                }
                Ok(())
            }),
            fields: vec![("tags", |_, view| {
                Ok(view
                    .columns()
                    .get()?
                    .opt_text("tags")?
                    .map(|stored| serde_json::from_str(stored).unwrap_or(Value::Null)))
            })],
            ..Hooks::default()
        }
    }
}

// test.Reason needs a reason at every create.
struct Reason {
    seen: Seen,
}

impl Behavior for Reason {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Reason", "createParamsSchema": {"type": "object", "additionalProperties": false, "required": ["reason"], "properties": {"reason": {"type": "string"}}}}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            initialize: Some(|reason, _, params| {
                reason
                    .seen
                    .lock()
                    .unwrap()
                    .push(json!({"step": "reason", "params": params}));
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

// test.Maker creates an Item from its operation, with parameters for the
// new instance's behaviors.
struct Maker;

impl Behavior for Maker {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Maker", "operations": [{"name": "make", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["id"], "properties": {"id": {"type": "string"}, "tags": {"type": "array"}, "catch": {"type": "boolean"}}}, "resultSchema": true, "writes": true}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![("make", |_, cx, params| {
                let id = params["id"].as_str().unwrap_or_default();
                let behaviors = json!({"test.Tags": {"tags": params.get("tags")}});
                match cx.instances().create(
                    "Item",
                    &json!({ "title": id }),
                    Some(id),
                    Some(&behaviors),
                ) {
                    Ok(made) => Ok(
                        json!({"id": made.id, "tags": made.behaviors["test.Tags"]["tags"], "frozen": true}),
                    ),
                    Err(Error::Engine(error))
                        if params["catch"] == json!(true)
                            && error.name() == "CreateParamsError" =>
                    {
                        Ok(json!({"refused": error.details().unwrap()["issues"]}))
                    }
                    Err(error) => Err(error),
                }
            })],
            ..Hooks::default()
        }
    }
}

struct World {
    engine: Engine,
    seen: Seen,
}

fn params_world(target: &support::Target) -> World {
    let seen: Seen = Arc::default();
    let engine = target.open(
        support::fixtures()
            .with(Tags {
                seen: Arc::clone(&seen),
            })
            .with(Reason {
                seen: Arc::clone(&seen),
            })
            .with(Maker)
            .with(Hold::default()),
    );
    World { engine, seen }
}

fn create_with(engine: &Engine, id: &str, behaviors: Option<&Value>) -> Result<InstanceRecord> {
    engine.instances().create(
        &alice(),
        "Item",
        &json!({"title": "Desk"}),
        CreateOptions {
            id: Some(id),
            behaviors,
            ..Default::default()
        },
    )
}

#[test]
fn a_declarations_create_params_schema_is_an_object_schema_that_checks_every_key_it_admits() {
    for target in targets("a_declarations_create_params_schema") {
        let world = params_world(&target);
        struct Open(String);
        impl Behavior for Open {
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
        let register = |name: &str, schema: Value| {
            world.engine.behaviors().register(Open(
                json!({"name": name, "createParamsSchema": schema}).to_string(),
            ))
        };
        for schema in [
            json!({"type": "array"}),
            json!(true),
            json!({"type": "object"}),
            json!({"type": "object", "additionalProperties": true}),
        ] {
            assert!(usage(register("test.Open", schema)).contains("createParamsSchema must"));
        }
        assert!(usage(register(
            "test.Odd",
            json!({"type": "object", "additionalProperties": false, "minProperties": "x"})
        ))
        .contains("createParamsSchema does not compile"));
        register(
            "test.Keyed",
            json!({"type": "object", "additionalProperties": {"type": "string"}}),
        )
        .unwrap();
        assert_eq!(
            world.engine.behaviors().declaration("test.Keyed").unwrap()["createParamsSchema"],
            json!({"type": "object", "additionalProperties": {"type": "string"}})
        );
    }
}

#[test]
fn a_create_asks_every_guard_after_the_insert_and_before_any_initialize_then_hands_each_initialize_its_own(
) {
    for target in targets("a_create_asks_every_guard_after_the_insert") {
        let World { engine, seen } = params_world(&target);
        publish_item(
            &engine,
            json!([{"name": "test.Tags"}, {"name": "test.Counter", "config": {"start": 2}}, {"name": "test.Flag"}]),
        );
        let created = create_with(
            &engine,
            "i1",
            Some(&json!({"test.Tags": {"tags": ["a", "b"]}})),
        )
        .unwrap();
        assert_eq!(
            fields_of(&created),
            json!({"data": {"title": "Desk"}, "behaviors": {"test.Tags": {"tags": ["a", "b"]}, "test.Counter": {"count": 2}, "test.Flag": {"flagged": false}}})
        );
        assert_eq!(
            std::mem::take(&mut *seen.lock().unwrap()),
            [
                json!({"step": "guard", "columns": {"tags": null}, "data": {"title": "Desk"}, "request": {"kind": "create", "data": {"title": "Desk"}, "behaviors": {"test.Tags": {"tags": ["a", "b"]}}}}),
                json!({"step": "initialize", "params": {"tags": ["a", "b"]}}),
                json!({"step": "afterChange", "columns": {"tags": "[\"a\",\"b\"]"}}),
            ]
        );
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Item"),
                    instance_id: Some("i1"),
                    ..Default::default()
                },
            )
            .unwrap()
            .events;
        assert_eq!(
            events
                .iter()
                .map(|event| json!([event.kind, event.seq, event.change]))
                .collect::<Vec<_>>(),
            [
                json!(["create", 1, {"data": {"title": "Desk"}, "behaviors": {"test.Tags": {"tags": ["a", "b"]}, "test.Counter": {"count": 2}, "test.Flag": {"flagged": false}}}])
            ]
        );
        // A create that gives a behavior nothing hands its initialize {}.
        assert_eq!(
            create_with(&engine, "i2", None).unwrap().behaviors["test.Tags"]["tags"],
            json!([])
        );
        let steps: Vec<Value> = seen
            .lock()
            .unwrap()
            .iter()
            .map(|entry| match entry["step"].as_str() {
                Some("guard") => entry["request"]["behaviors"].clone(),
                Some("initialize") => entry["params"].clone(),
                _ => entry["columns"].clone(),
            })
            .collect();
        assert_eq!(steps, [json!({}), json!({}), json!({"tags": "[]"})]);
    }
}

#[test]
fn a_guards_veto_refuses_the_create_and_leaves_nothing_of_it() {
    for target in targets("a_guards_veto_refuses_the_create") {
        let World { engine, seen } = params_world(&target);
        publish_item(
            &engine,
            json!([{"name": "test.Counter"}, {"name": "test.Tags", "config": {"refuse": "x"}}]),
        );
        let refused = refusal(create_with(
            &engine,
            "i1",
            Some(&json!({"test.Tags": {"tags": ["a", "x"]}})),
        ));
        assert_eq!(refused.code(), EngineErrorCode::Vetoed);
        assert_eq!(
            refused.message(),
            "behavior test.Tags vetoes create of Item i1: tag x is refused"
        );
        let (behavior, action, _, veto_code, details) = veto(create_with(
            &engine,
            "i1",
            Some(&json!({"test.Tags": {"tags": ["a", "x"]}})),
        ));
        assert_eq!(
            (
                behavior.as_str(),
                action.as_str(),
                veto_code.as_deref(),
                details
            ),
            (
                "test.Tags",
                "create",
                Some("refused_tag"),
                Some(json!({"tag": "x"}))
            )
        );
        assert!(seen
            .lock()
            .unwrap()
            .iter()
            .all(|entry| entry["step"] == "guard"));
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Item", "i1", Default::default())
                .unwrap(),
            None
        );
        assert!(support::events_of(&engine, "Item").is_empty());
        assert_eq!(
            create_with(&engine, "i1", Some(&json!({"test.Tags": {"tags": ["a"]}})))
                .unwrap()
                .seq,
            1
        );
    }
}

#[test]
fn a_veto_initialize_raises_refuses_too_and_an_unlisted_code_is_a_behavior_error() {
    for target in targets("a_veto_initialize_raises_refuses_too") {
        let listed = params_world(&target);
        publish_item(
            &listed.engine,
            json!([{"name": "test.Tags", "config": {"refuse": "x"}}]),
        );
        let (behavior, action, reason, veto_code, _) = veto(create_with(
            &listed.engine,
            "i1",
            Some(&json!({"test.Tags": {"tags": ["late"]}})),
        ));
        assert_eq!(
            (
                behavior.as_str(),
                action.as_str(),
                reason.as_str(),
                veto_code.as_deref()
            ),
            (
                "test.Tags",
                "create",
                "tag late is refused",
                Some("refused_tag")
            )
        );
        assert_eq!(
            listed
                .engine
                .instances()
                .get(&alice(), "Item", "i1", Default::default())
                .unwrap(),
            None
        );
        drop(listed);
        for unlisted_target in targets("unlisted") {
            if unlisted_target.dialect != target.dialect {
                continue;
            }
            let unlisted = params_world(&unlisted_target);
            publish_item(
                &unlisted.engine,
                json!([{"name": "test.Tags", "config": {"refuse": "x", "code": "unlisted"}}]),
            );
            for given in [json!(["x"]), json!(["late"])] {
                let (_, message) = defect(create_with(
                    &unlisted.engine,
                    "i1",
                    Some(&json!({"test.Tags": {"tags": given}})),
                ));
                assert!(
                    message.contains(
                        "a veto's code \"unlisted\" is not one its declaration lists (refused_tag)"
                    ),
                    "{message}"
                );
            }
            assert!(unlisted
                .engine
                .instances()
                .list(&alice(), "Item", Default::default())
                .unwrap()
                .items
                .is_empty());
        }
    }
}

#[test]
fn the_engine_refuses_parameters_a_type_does_not_take_or_a_schema_refuses_each_at_a_pointer() {
    for target in targets("the_engine_refuses_parameters") {
        let World { engine, seen } = params_world(&target);
        publish_item(
            &engine,
            json!([{"name": "test.Tags"}, {"name": "test.Counter"}, {"name": "test.Reason"}]),
        );
        let issues =
            |behaviors: Value| issues_of(&refusal(create_with(&engine, "i1", Some(&behaviors))));
        let mut given = json!({"test.Reason": {"reason": "stock"}});
        given["test.Nope"] = json!({});
        assert_eq!(
            issues(given),
            [issue("/behaviors/test.Nope", "Item does not compose behavior test.Nope (its behaviors that take create parameters: test.Tags, test.Reason)")]
        );
        assert_eq!(
            issues(json!({"test.Reason": {"reason": "stock"}, "test.Counter": {}})),
            [issue(
                "/behaviors/test.Counter",
                "behavior test.Counter takes no create parameters"
            )]
        );
        assert_eq!(
            issues(
                json!({"test.Reason": {"reason": "stock"}, "test.Tags": {"tags": ["", "b", "c", "d"]}})
            ),
            [
                issue(
                    "/behaviors/test.Tags/tags",
                    "must NOT have more than 3 items"
                ),
                issue(
                    "/behaviors/test.Tags/tags/0",
                    "must NOT have fewer than 1 characters"
                ),
            ]
        );
        assert_eq!(
            issues(json!(["test.Tags"])),
            [issue(
                "/behaviors",
                "behaviors is the create's parameters by behavior name: a JSON object"
            )]
        );
        // A behavior whose createParamsSchema refuses {} needs an entry at
        // every create.
        assert_eq!(
            issues(json!({})),
            [issue(
                "/behaviors/test.Reason",
                "must have required property 'reason'"
            )]
        );
        // Every issue at once, across behaviors.
        let all = refusal(engine.instances().create(
            &alice(),
            "Item",
            &json!({"title": "Desk"}),
            CreateOptions {
                behaviors: Some(&json!({"test.Nope": {}, "test.Tags": {"tags": "a"}})),
                ..Default::default()
            },
        ));
        assert_eq!(
            issues_of(&all)
                .into_iter()
                .map(|(path, _)| path)
                .collect::<Vec<_>>(),
            [
                "/behaviors/test.Nope",
                "/behaviors/test.Tags/tags",
                "/behaviors/test.Reason"
            ]
        );
        assert_eq!(all.code(), EngineErrorCode::InvalidArgument);
        assert!(all.message().starts_with(
            "create of Item: /behaviors/test.Nope: Item does not compose behavior test.Nope"
        ));
        assert!(engine
            .instances()
            .list(&alice(), "Item", Default::default())
            .unwrap()
            .items
            .is_empty());
        assert!(seen.lock().unwrap().is_empty());
        create_with(
            &engine,
            "i1",
            Some(&json!({"test.Reason": {"reason": "stock"}})),
        )
        .unwrap();
        assert!(seen
            .lock()
            .unwrap()
            .contains(&json!({"step": "reason", "params": {"reason": "stock"}})));
    }
}

#[test]
fn a_behaviors_instances_create_gives_the_new_instances_behaviors_their_parameters() {
    for target in targets("a_behaviors_instances_create_gives_parameters") {
        let World { engine, .. } = params_world(&target);
        publish_item(
            &engine,
            json!([{"name": "test.Tags"}, {"name": "test.Maker"}]),
        );
        engine
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Maker"}),
                CreateOptions {
                    id: Some("m1"),
                    ..Default::default()
                },
            )
            .unwrap();
        let make = |params: Value| {
            engine.instances().invoke(
                &alice(),
                "Item",
                "m1",
                "make",
                &params,
                WriteOptions::default(),
            )
        };
        assert_eq!(
            make(json!({"id": "i1", "tags": ["made"]})).unwrap(),
            json!({"id": "i1", "tags": ["made"], "frozen": true})
        );
        // A refusal the behavior catches leaves nothing of the create.
        assert_eq!(
            make(json!({"id": "i2", "tags": [""], "catch": true})).unwrap(),
            json!({"refused": [{"path": "/behaviors/test.Tags/tags/0", "message": "must NOT have fewer than 1 characters"}]})
        );
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Item", "i2", Default::default())
                .unwrap(),
            None
        );
        assert_eq!(
            code(make(json!({"id": "i2", "tags": [""]}))),
            EngineErrorCode::InvalidArgument
        );
        assert_eq!(
            engine
                .instances()
                .list(&alice(), "Item", Default::default())
                .unwrap()
                .items
                .iter()
                .map(|item| item.id.as_str())
                .collect::<Vec<_>>(),
            ["m1", "i1"]
        );
    }
}

#[derive(Clone, Default)]
struct SpawnerConfig {
    children: Option<(String, i64)>,
}

// test.Spawner: creates instances from its operations and its afterChange.
struct Spawner {
    field_creates: Arc<Mutex<bool>>,
}

fn summary(record: &InstanceRecord) -> Value {
    json!({
        "id": record.id,
        "namespace": record.namespace,
        "schema": record.schema,
        "schemaNamespace": record.schema_namespace,
        "seq": record.seq,
        "createdBy": record.created_by,
        "data": record.data,
        "behaviors": record.behaviors,
        "frozen": true,
    })
}

impl Behavior for Spawner {
    type Config = SpawnerConfig;

    fn declaration(&self) -> &str {
        r#"{
            "name": "test.Spawner",
            "description": "Creates instances from its operations and its afterChange.",
            "configSchema": {"type": "object", "additionalProperties": false, "properties": {"children": {"type": "object", "additionalProperties": false, "required": ["schema", "count"], "properties": {"schema": {"type": "string"}, "count": {"type": "integer", "minimum": 1}}}}},
            "fields": [{"name": "spawned", "description": "How many creates its spawn operation asked for."}, {"name": "probe", "description": "Tries a create when the test sets it."}],
            "operations": [
                {"name": "spawn", "description": "Creates an instance.", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["schema", "data"], "properties": {"schema": {"type": "string"}, "data": {"type": "object"}, "id": {"type": "string"}, "catch": {"type": "boolean"}}}, "resultSchema": true, "writes": true},
                {"name": "spawnRead", "description": "Tries a create from a read-only operation.", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["schema", "data"], "properties": {"schema": {"type": "string"}, "data": {"type": "object"}, "id": {"type": "string"}, "catch": {"type": "boolean"}}}, "resultSchema": true},
                {"name": "guarded", "description": "Its guard tries a create before it runs.", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["schema", "data"], "properties": {"schema": {"type": "string"}, "data": {"type": "object"}, "id": {"type": "string"}, "catch": {"type": "boolean"}}}, "resultSchema": true, "writes": true},
                {"name": "spawnMany", "description": "Creates count instances of a schema, from no instance.", "scope": "schema", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["schema", "count"], "properties": {"schema": {"type": "string"}, "count": {"type": "integer", "minimum": 1}}}, "resultSchema": true, "writes": true},
                {"name": "spawnPeek", "description": "Tries a create from a read-only schema-level operation.", "scope": "schema", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["schema", "data"], "properties": {"schema": {"type": "string"}, "data": {"type": "object"}, "id": {"type": "string"}, "catch": {"type": "boolean"}}}, "resultSchema": true}
            ]
        }"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<SpawnerConfig> {
        Ok(SpawnerConfig {
            children: raw.get("children").map(|children| {
                (
                    children["schema"].as_str().unwrap_or_default().to_owned(),
                    children["count"].as_i64().unwrap_or(0),
                )
            }),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "spawned")
                .column("spawned", ColumnSpec::integer().not_null().default(0))],
            guard: Some(|_, view, request| {
                if let GuardRequest::Operation {
                    operation: "guarded",
                    params,
                    ..
                } = request
                {
                    view.instances().create(
                        params["schema"].as_str().unwrap_or_default(),
                        &params["data"],
                        params["id"].as_str(),
                        None,
                    )?;
                }
                Ok(None)
            }),
            operations: vec![
                ("spawn", |_, cx, params| {
                    let spawned = cx.columns().get()?.i64("spawned")?;
                    cx.columns()
                        .set(&[("spawned", SqlValue::Integer(spawned + 1))])?;
                    let created = cx.instances().create(
                        params["schema"].as_str().unwrap_or_default(),
                        &params["data"],
                        params["id"].as_str(),
                        None,
                    );
                    if params["catch"] != json!(true) {
                        return Ok(summary(&created?));
                    }
                    Ok(match created {
                        Ok(record) => summary(&record),
                        Err(Error::Engine(error)) => {
                            json!({"threw": error.name(), "message": error.message()})
                        }
                        Err(error) => json!({"threw": "Error", "message": error.to_string()}),
                    })
                }),
                ("spawnRead", |_, cx, params| {
                    Ok(summary(&cx.instances().create(
                        params["schema"].as_str().unwrap_or_default(),
                        &params["data"],
                        params["id"].as_str(),
                        None,
                    )?))
                }),
                ("guarded", |_, _, _| Ok(json!("ran"))),
            ],
            schema_operations: vec![
                ("spawnMany", |_, cx, params| {
                    let mut ids = Vec::new();
                    for n in 1..=params["count"].as_i64().unwrap_or(0) {
                        ids.push(Value::from(
                            cx.instances()
                                .create(
                                    params["schema"].as_str().unwrap_or_default(),
                                    &json!({ "title": format!("batch {n}") }),
                                    None,
                                    None,
                                )?
                                .id,
                        ));
                    }
                    Ok(Value::Array(ids))
                }),
                ("spawnPeek", |_, cx, params| {
                    Ok(summary(&cx.instances().create(
                        params["schema"].as_str().unwrap_or_default(),
                        &params["data"],
                        params["id"].as_str(),
                        None,
                    )?))
                }),
            ],
            fields: vec![
                ("spawned", |_, view| {
                    Ok(Some(Value::from(view.columns().get()?.i64("spawned")?)))
                }),
                ("probe", |spawner, view| {
                    if !*spawner.field_creates.lock().unwrap() {
                        return Ok(None);
                    }
                    Ok(Some(Value::from(
                        view.instances()
                            .create("Task", &json!({"title": "from a field"}), None, None)?
                            .id,
                    )))
                }),
            ],
            after_change: Some(|_, cx, change| {
                let Some((schema, count)) = cx.config().children.clone() else {
                    return Ok(());
                };
                if !matches!(change, InstanceChange::Create) {
                    return Ok(());
                }
                let title = cx.data()?["title"].as_str().unwrap_or_default().to_owned();
                for n in 1..=count {
                    cx.instances().create(
                        &schema,
                        &json!({ "title": format!("{title} {n}") }),
                        Some(&format!("{}-{n}", cx.id())),
                        None,
                    )?;
                }
                Ok(())
            }),
            ..Hooks::default()
        }
    }
}

// test.Child marks a task born in afterChange; its initialize refuses a
// title that starts with boom and, for a depth, creates a task one less
// deep under the task's id.
struct Child;

impl Behavior for Child {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Child", "description": "Marks new tasks, and nests them.", "fields": [{"name": "born", "description": "Whether afterChange saw the create."}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "born")
                .column("born", ColumnSpec::integer().not_null().default(0))],
            initialize: Some(|_, cx, _| {
                let data = cx.data()?;
                let title = data["title"].as_str().unwrap_or_default();
                if title.starts_with("boom") {
                    return Err(
                        EngineError::invalid_argument(format!("{title} refuses to start")).into(),
                    );
                }
                if let Some(depth) = data.get("depth").and_then(Value::as_i64) {
                    if depth > 0 {
                        cx.instances().create(
                            cx.schema(),
                            &json!({"title": "nest", "depth": depth - 1}),
                            Some(&format!("{}.{}", cx.id(), depth - 1)),
                            None,
                        )?;
                    }
                }
                Ok(())
            }),
            after_change: Some(|_, cx, change| {
                if matches!(change, InstanceChange::Create) {
                    cx.columns().set(&[("born", SqlValue::Integer(1))])?;
                }
                Ok(())
            }),
            fields: vec![("born", |_, view| {
                Ok(Some(Value::from(view.columns().get()?.i64("born")?)))
            })],
            ..Hooks::default()
        }
    }
}

fn task_document(name: &str, behaviors: Value) -> Value {
    let mut document = schema_document(
        name,
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "depth", "typeRef": {"name": "Generic.Int64"}}
        ]),
        json!({}),
    );
    document["types"][name]["behaviors"] = behaviors;
    document
}

fn task_behaviors() -> Value {
    json!([{"name": "test.Counter", "config": {"start": 7}}, {"name": "test.Child"}])
}

fn task(title: &str, extra: Value) -> Value {
    let mut data = json!({ "title": title });
    if let Value::Object(extra) = extra {
        data.as_object_mut().unwrap().extend(extra);
    }
    json!({"data": data, "behaviors": {"test.Counter": {"count": 7}, "test.Child": {"born": 1}}})
}

struct Spawning {
    engine: Engine,
    field_creates: Arc<Mutex<bool>>,
}

fn open_spawning(
    target: &support::Target,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> Spawning {
    let field_creates = Arc::new(Mutex::new(false));
    let engine = target.open_with(
        BehaviorSet::new()
            .with(Counter::default())
            .with(Spawner {
                field_creates: Arc::clone(&field_creates),
            })
            .with(Child),
        |options| {
            options.ids = Ids::Sequence {
                prefix: "gen-".into(),
            };
            configure(options);
        },
    );
    Spawning {
        engine,
        field_creates,
    }
}

// A Note spawns; a Task counts from 7 and is born.
fn spawning_world(
    target: &support::Target,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> Spawning {
    let world = open_spawning(target, configure);
    publish(
        &world.engine,
        &task_document("Note", json!([{"name": "test.Spawner"}])),
        None,
    );
    publish(
        &world.engine,
        &task_document("Task", task_behaviors()),
        None,
    );
    world
        .engine
        .instances()
        .create(
            &alice(),
            "Note",
            &json!({"title": "First"}),
            CreateOptions {
                id: Some("n1"),
                ..Default::default()
            },
        )
        .unwrap();
    world
}

fn head(engine: &Engine) -> i64 {
    engine.events().head().unwrap()
}

fn events_after(engine: &Engine, after: i64) -> Vec<Value> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                after: Some(superschematic_engine::EventsAfter::Cursor(after)),
                ..Default::default()
            },
        )
        .unwrap()
        .events
        .into_iter()
        .map(|event| {
            let who = if event.kind == "operation" {
                event.change["operation"].clone()
            } else {
                Value::from(event.actor.as_str())
            };
            json!([event.kind, event.schema, event.instance_id, who])
        })
        .collect()
}

fn tasks(engine: &Engine) -> Vec<String> {
    engine
        .instances()
        .list(&alice(), "Task", Default::default())
        .unwrap()
        .items
        .into_iter()
        .map(|item| item.id)
        .collect()
}

fn spawned(engine: &Engine) -> Value {
    engine
        .instances()
        .get(&alice(), "Note", "n1", Default::default())
        .unwrap()
        .unwrap()
        .behaviors["test.Spawner"]["spawned"]
        .clone()
}

fn spawn(engine: &Engine, who: &superschematic_engine::Principal, params: Value) -> Result<Value> {
    engine
        .instances()
        .invoke(who, "Note", "n1", "spawn", &params, WriteOptions::default())
}

#[test]
fn an_operation_creates_one_as_its_caller_would() {
    for target in targets("an_operation_creates_one_as_its_caller_would") {
        let Spawning { engine, .. } = spawning_world(&target, |_| {});
        let from = head(&engine);
        let bob = named("bob");
        assert_eq!(
            spawn(
                &engine,
                &bob,
                json!({"schema": "Task", "data": {"title": "Paint"}, "id": "t1"})
            )
            .unwrap(),
            json!({"id": "t1", "namespace": "default", "schema": "Task", "schemaNamespace": "default", "seq": 1, "createdBy": "bob", "data": {"title": "Paint"}, "behaviors": {"test.Counter": {"count": 7}, "test.Child": {"born": 1}}, "frozen": true})
        );
        assert_eq!(
            fields_of(
                &engine
                    .instances()
                    .get(&alice(), "Task", "t1", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            task("Paint", json!({}))
        );
        assert_eq!(
            events_after(&engine, from),
            [
                json!(["create", "Task", "t1", "bob"]),
                json!(["operation", "Note", "n1", "spawn"])
            ]
        );
        // Without an id, the engine's id source names it.
        assert_eq!(
            spawn(
                &engine,
                &alice(),
                json!({"schema": "Task", "data": {"title": "Sand"}})
            )
            .unwrap()["id"],
            "gen-1"
        );
        assert_eq!(spawned(&engine), 2);
    }
}

#[test]
fn a_create_the_engine_refuses_refuses_the_call() {
    for target in targets("a_create_the_engine_refuses_refuses_the_call") {
        let Spawning { engine, .. } = spawning_world(&target, |_| {});
        spawn(
            &engine,
            &alice(),
            json!({"schema": "Task", "data": {"title": "Paint"}, "id": "t1"}),
        )
        .unwrap();
        let from = head(&engine);
        let try_spawn = |data: Value, id: Option<&str>| {
            let mut params = json!({"schema": "Task", "data": data});
            if let Some(id) = id {
                params["id"] = json!(id);
            }
            spawn(&engine, &alice(), params)
        };
        assert_eq!(
            code(try_spawn(json!({}), None)),
            EngineErrorCode::InvalidInstance
        );
        let unknown = refusal(try_spawn(json!({"title": "Paint", "count": 3}), None));
        assert_eq!(
            support::validation_issues(&unknown),
            json!([{"path": "count", "rule": "unknown", "message": "Task has no field count"}])
        );
        let taken = refusal(try_spawn(json!({"title": "Again"}), Some("t1")));
        assert_eq!(
            (taken.code(), taken.message()),
            (
                EngineErrorCode::Conflict,
                "Task t1 already exists in namespace default"
            )
        );
        assert_eq!(
            code(try_spawn(json!({"title": "Odd"}), Some("not an id"))),
            EngineErrorCode::InvalidArgument
        );
        let missing = refusal(spawn(
            &engine,
            &alice(),
            json!({"schema": "Nothing", "data": {"title": "x"}}),
        ));
        assert_eq!(
            (missing.code(), missing.message()),
            (
                EngineErrorCode::NotFound,
                "schema Nothing has no live version in namespace default"
            )
        );
        assert!(events_after(&engine, from).is_empty());
        assert_eq!(spawned(&engine), 1);
    }
}

#[test]
fn after_change_creates_a_new_instances_children_which_commit_and_roll_back_with_it() {
    for target in targets("after_change_creates_a_new_instances_children") {
        let Spawning { engine, .. } = spawning_world(&target, |_| {});
        publish(
            &engine,
            &task_document(
                "Folder",
                json!([{"name": "test.Spawner", "config": {"children": {"schema": "Task", "count": 2}}}]),
            ),
            None,
        );
        let from = head(&engine);
        engine
            .instances()
            .create(
                &alice(),
                "Folder",
                &json!({"title": "Kitchen"}),
                CreateOptions {
                    id: Some("f1"),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(
            events_after(&engine, from),
            [
                json!(["create", "Task", "f1-1", "alice"]),
                json!(["create", "Task", "f1-2", "alice"]),
                json!(["create", "Folder", "f1", "alice"]),
            ]
        );
        assert_eq!(
            fields_of(
                &engine
                    .instances()
                    .get(&alice(), "Task", "f1-2", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            task("Kitchen 2", json!({}))
        );
        let failing = refusal(engine.instances().create(
            &alice(),
            "Folder",
            &json!({"title": "boom"}),
            CreateOptions {
                id: Some("f2"),
                ..Default::default()
            },
        ));
        assert_eq!(
            (failing.code(), failing.message()),
            (EngineErrorCode::InvalidArgument, "boom 1 refuses to start")
        );
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Folder", "f2", Default::default())
                .unwrap(),
            None
        );
        assert_eq!(tasks(&engine), ["f1-1", "f1-2"]);
    }
}

#[test]
fn initialize_creates_instances_that_nest_and_creates_count_toward_the_nesting_depth() {
    for target in targets("initialize_creates_instances_that_nest") {
        let Spawning { engine, .. } = spawning_world(&target, |_| {});
        engine
            .instances()
            .create(
                &alice(),
                "Task",
                &json!({"title": "nest", "depth": 3}),
                CreateOptions {
                    id: Some("t0"),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(tasks(&engine), ["t0", "t0.2", "t0.2.1", "t0.2.1.0"]);
        assert_eq!(
            fields_of(
                &engine
                    .instances()
                    .get(&alice(), "Task", "t0.2.1.0", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            task("nest", json!({"depth": 0}))
        );
        let (_, message) = defect(engine.instances().create(
            &alice(),
            "Task",
            &json!({"title": "nest", "depth": 20}),
            CreateOptions {
                id: Some("d0"),
                ..Default::default()
            },
        ));
        assert_eq!(
            message,
            "behavior test.Child: instances.create: calls nest more than 16 deep"
        );
        assert_eq!(tasks(&engine).len(), 4);
    }
}

#[test]
fn a_guard_a_field_reader_and_read_only_operations_cannot_create_and_a_writing_schema_level_one_can(
) {
    for target in targets("a_guard_a_field_reader_and_read_only_operations_cannot_create") {
        let Spawning {
            engine,
            field_creates,
        } = spawning_world(&target, |_| {});
        let params = json!({"schema": "Task", "data": {"title": "Paint"}});
        let refused = "behavior test.Spawner: a read cannot create an instance of Task; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can";
        assert_eq!(
            defect(engine.instances().invoke(
                &alice(),
                "Note",
                "n1",
                "spawnRead",
                &params,
                WriteOptions::default()
            ))
            .1,
            refused
        );
        assert_eq!(
            defect(
                engine
                    .instances()
                    .invoke_schema(&alice(), "Note", "spawnPeek", &params, None)
            )
            .1,
            refused
        );
        assert_eq!(
            defect(engine.instances().invoke(
                &alice(),
                "Note",
                "n1",
                "guarded",
                &params,
                WriteOptions::default()
            ))
            .1,
            refused
        );
        *field_creates.lock().unwrap() = true;
        assert_eq!(
            defect(
                engine
                    .instances()
                    .get(&alice(), "Note", "n1", Default::default())
            )
            .1,
            refused
        );
        *field_creates.lock().unwrap() = false;
        assert!(tasks(&engine).is_empty());
        assert_eq!(
            engine
                .instances()
                .invoke_schema(
                    &alice(),
                    "Note",
                    "spawnMany",
                    &json!({"schema": "Task", "count": 2}),
                    None
                )
                .unwrap(),
            json!(["gen-1", "gen-2"])
        );
        assert_eq!(
            fields_of(
                &engine
                    .instances()
                    .get(&alice(), "Task", "gen-2", Default::default())
                    .unwrap()
                    .unwrap()
            ),
            task("batch 2", json!({}))
        );
    }
}

#[test]
fn a_failure_the_behavior_catches_rolls_back_the_create_alone() {
    for target in targets("a_failure_the_behavior_catches_rolls_back_the_create_alone") {
        let Spawning { engine, .. } = spawning_world(&target, |_| {});
        let from = head(&engine);
        assert_eq!(
            spawn(
                &engine,
                &alice(),
                json!({"schema": "Task", "data": {"title": "boom"}, "id": "b1", "catch": true})
            )
            .unwrap(),
            json!({"threw": "EngineError", "message": "boom refuses to start"})
        );
        assert_eq!(spawned(&engine), 1);
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Task", "b1", Default::default())
                .unwrap(),
            None
        );
        assert_eq!(
            events_after(&engine, from),
            [json!(["operation", "Note", "n1", "spawn"])]
        );
        let after = head(&engine);
        let failure = refusal(spawn(
            &engine,
            &alice(),
            json!({"schema": "Task", "data": {"title": "boom"}, "id": "b1"}),
        ));
        assert_eq!(failure.message(), "boom refuses to start");
        assert_eq!(spawned(&engine), 1);
        assert!(events_after(&engine, after).is_empty());
        assert!(tasks(&engine).is_empty());
    }
}

#[test]
fn it_asks_the_policy_for_write_on_the_schema_as_the_caller() {
    for target in targets("it_asks_the_policy_for_write_on_the_schema") {
        let policy = RecordingPolicy::new(|request| {
            request.principal().subject == "alice"
                || !(request.action() == "write" && request.schema() == Some("Task"))
        });
        let recorded = policy.clone();
        let Spawning { engine, .. } = spawning_world(&target, |options| {
            options.policy = Some(Policy::custom(policy));
        });
        recorded.take();
        let bob = named("bob");
        let refused = refusal(spawn(
            &engine,
            &bob,
            json!({"schema": "Task", "data": {"title": "Paint"}, "id": "t1"}),
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not write Task in namespace default"
            )
        );
        assert_eq!(
            recorded.take(),
            [
                json!({"principal": "bob", "action": "write", "namespace": "default", "schema": "Note", "operation": "spawn"}),
                json!({"principal": "bob", "action": "write", "namespace": "default", "schema": "Task"}),
            ]
        );
        assert_eq!(spawned(&engine), 0);
        assert_eq!(
            spawn(
                &engine,
                &alice(),
                json!({"schema": "Task", "data": {"title": "Paint"}, "id": "t1"})
            )
            .unwrap()["createdBy"],
            "alice"
        );
    }
}

#[test]
fn creating_an_instance_whose_write_runs_up_the_call_is_a_cycle() {
    for target in targets("creating_an_instance_whose_write_runs_up_the_call") {
        let Spawning { engine, .. } = spawning_world(&target, |_| {});
        assert_eq!(
            defect(spawn(&engine, &alice(), json!({"schema": "Note", "data": {"title": "Again"}, "id": "n1"}))).1,
            "behavior test.Spawner: creating Note n1 is a cycle: a write of Note n1 is still running up this call"
        );
        assert_eq!(
            spawn(
                &engine,
                &alice(),
                json!({"schema": "Note", "data": {"title": "Second"}, "id": "n2"})
            )
            .unwrap()["id"],
            "n2"
        );
    }
}

#[test]
fn a_schema_is_looked_up_in_the_namespace_then_the_shared_one() {
    for target in targets("a_schema_is_looked_up_in_the_namespace_then_the_shared_one") {
        let Spawning { engine, .. } = open_spawning(&target, |options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into(), "west".into(), "library".into()],
                shared: Some("library".into()),
            };
        });
        publish(
            &engine,
            &task_document("Task", task_behaviors()),
            Some("library"),
        );
        publish(
            &engine,
            &task_document("Note", json!([{"name": "test.Spawner"}])),
            Some("east"),
        );
        engine
            .instances()
            .create(
                &alice(),
                "Note",
                &json!({"title": "East"}),
                CreateOptions {
                    id: Some("n1"),
                    namespace: Some("east"),
                    ..Default::default()
                },
            )
            .unwrap();
        let made = engine
            .instances()
            .invoke(
                &alice(),
                "Note",
                "n1",
                "spawn",
                &json!({"schema": "Task", "data": {"title": "Paint"}, "id": "t1"}),
                WriteOptions {
                    namespace: Some("east"),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(
            (made["namespace"].clone(), made["schemaNamespace"].clone()),
            (json!("east"), json!("library"))
        );
        assert_eq!(
            engine
                .instances()
                .get(
                    &alice(),
                    "Task",
                    "t1",
                    superschematic_engine::GetOptions {
                        namespace: Some("west"),
                        ..Default::default()
                    }
                )
                .unwrap(),
            None
        );
        let _ = (DefineOptions::default(), SchemaTarget::default());
    }
}
