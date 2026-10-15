//! A behavior whose hooks are named at run time (the `dynamic` feature),
//! as a binding's bridge registers one written in the host's language:
//! registered with the problems its host found, composed, its operations,
//! fields and guard called by name with the contexts a Rust behavior gets,
//! and what its host's hook threw carried back as the host's error.
#![cfg(feature = "dynamic")]

mod support;

use serde_json::{json, Value};

use superschematic_engine::behaviors::{ConfigTarget, DynamicBehavior, DynamicHooks};
use superschematic_engine::storage::{
    behavior::BehaviorMigration, ColumnSpec, ColumnType, SqlValue,
};
use superschematic_engine::{
    DefineOptions, Error, GuardRequest, HostError, InstanceView, MetaSchema, OperationCx,
    SchemaTarget, Veto,
};
use support::{alice, repository_file, targets};

const DECLARATION: &str = r#"{
  "name": "test.Dyn",
  "description": "Counts bumps; a host's hook throws on boom.",
  "fields": [{ "name": "bumps" }],
  "operations": [
    { "name": "bump", "paramsSchema": { "type": "object", "additionalProperties": false }, "resultSchema": { "type": "integer" }, "writes": true },
    { "name": "boom", "paramsSchema": { "type": "object", "additionalProperties": false }, "resultSchema": true, "writes": true }
  ]
}"#;

struct Dyn {
    problems: Vec<String>,
}

impl DynamicBehavior for Dyn {
    type Config = Value;

    fn declaration(&self) -> &str {
        DECLARATION
    }

    fn hooks(&self) -> DynamicHooks {
        DynamicHooks {
            guard: true,
            operations: vec!["bump".into(), "boom".into()],
            fields: vec!["bumps".into()],
            migrations: vec![BehaviorMigration::new(1, "bumps").column(
                "bumps",
                ColumnSpec {
                    not_null: true,
                    default: Some(SqlValue::Integer(0)),
                    ..ColumnSpec::new(ColumnType::Integer)
                },
            )],
            problems: self.problems.clone(),
            ..DynamicHooks::default()
        }
    }

    fn parse_config(
        &self,
        raw: &Value,
        _target: &ConfigTarget<'_>,
    ) -> superschematic_engine::Result<Value> {
        Ok(raw.clone())
    }

    fn guard(
        &self,
        view: &InstanceView<'_, Value>,
        request: &GuardRequest<'_>,
    ) -> superschematic_engine::Result<Option<Veto>> {
        let blocked = view.data()?.get("title") == Some(&json!("blocked"));
        Ok((blocked && request.action() == "bump").then(|| Veto::from("it is blocked")))
    }

    fn operation(
        &self,
        name: &str,
        cx: &OperationCx<'_, Value>,
        _params: &Value,
    ) -> superschematic_engine::Result<Value> {
        match name {
            "bump" => {
                let bumps = cx.columns().get()?.i64("bumps")? + 1;
                cx.columns().set(&[("bumps", SqlValue::Integer(bumps))])?;
                Ok(json!(bumps))
            }
            _ => Err(Error::Host(HostError {
                token: 7,
                message: "boom".into(),
                described: "Error: boom".into(),
            })),
        }
    }

    fn field(
        &self,
        _name: &str,
        view: &InstanceView<'_, Value>,
    ) -> superschematic_engine::Result<Option<Value>> {
        Ok(Some(json!(view.columns().get()?.i64("bumps")?)))
    }
}

fn open_meta_schema() -> Value {
    let mut document: Value =
        serde_json::from_str(&repository_file("ir/typescript/schema-file.json")).unwrap();
    for (pointer, key) in [
        ("/$defs/TypeDef/properties/behaviors", "maxItems"),
        ("/$defs/BehaviorRef/properties/name", "enum"),
        ("/$defs/BehaviorRef", "allOf"),
    ] {
        document
            .pointer_mut(pointer)
            .and_then(Value::as_object_mut)
            .unwrap()
            .shift_remove(key);
    }
    document
}

#[test]
fn a_dynamic_behavior_runs_by_name_and_carries_its_hosts_error_back() {
    for target in targets("a_dynamic_behavior_runs_by_name") {
        let engine = target.open_with(|options| {
            options.meta_schema = Some(MetaSchema::Document(open_meta_schema()));
            options.behaviors.push_dynamic(Dyn {
                problems: Vec::new(),
            });
        });
        let alice = alice();
        let document = json!({
            "kind": "General",
            "name": "Item",
            "types": {"Item": {"name": "Item", "role": "EmbeddedStruct", "behaviors": [{"name": "test.Dyn"}], "fields": [
                {"name": "title", "typeRef": {"name": "string"}, "required": true}
            ]}}
        });
        engine
            .schemas()
            .define(&alice, &document, DefineOptions::default())
            .unwrap();
        engine
            .schemas()
            .publish(&alice, "Item", SchemaTarget::default())
            .unwrap();
        let instances = engine.instances();
        for (id, title) in [("i1", "Desk"), ("i2", "blocked")] {
            superschematic_engine::calls::call(
                &engine,
                "instances.create",
                Some(&alice),
                &[json!("Item"), json!({"title": title}), json!({"id": id})],
            )
            .unwrap();
        }
        let invoke = |id: &str, operation: &str| {
            superschematic_engine::calls::call(
                &engine,
                "instances.invoke",
                Some(&alice),
                &[json!("Item"), json!(id), json!(operation), json!({})],
            )
        };
        assert_eq!(invoke("i1", "bump").unwrap(), Some(json!(1)));
        assert_eq!(invoke("i1", "bump").unwrap(), Some(json!(2)));
        let record = instances
            .get(&alice, "Item", "i1", Default::default())
            .unwrap()
            .unwrap();
        assert_eq!(record.behaviors.get("test.Dyn"), Some(&json!({"bumps": 2})));
        assert!(invoke("i2", "bump").unwrap_err().is_veto());
        match invoke("i1", "boom") {
            Err(Error::Host(thrown)) => assert_eq!(
                (thrown.token, thrown.described.as_str()),
                (7, "Error: boom")
            ),
            other => panic!("expected the host's error, got {other:?}"),
        }
        // The host's problems refuse it with the engine's own.
        let refused = engine.behaviors().register_dynamic(Dyn {
            problems: vec!["guard is a function".into()],
        });
        let message = refused.unwrap_err().to_string();
        assert!(message.contains("guard is a function"), "{message}");
    }
}
