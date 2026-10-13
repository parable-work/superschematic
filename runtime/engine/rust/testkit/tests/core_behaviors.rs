//! The core's behaviors with no extension: the cases of
//! `test/core-behaviors.test.ts` that compose only the behaviors ported so
//! far (an engine opened with them registers them and no one else can take
//! their names; the steps document the core binary builds), on SQLite and,
//! with the variable set, on Postgres. The other documents compose
//! behaviors of waves 2 to 4.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Behavior, BehaviorSet, ConfigTarget, CreateOptions, DefineOptions, Engine, Error, Hooks,
    Principal, Result, SchemaTarget, WriteOptions,
};
use support::{alice, refusal, repository, targets, usage};

fn writer() -> Principal {
    Principal::new("wes", Vec::<String>::new())
}

// An implementation that takes Workflow's name.
struct Impostor;

impl Behavior for Impostor {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "Workflow"}"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks::default()
    }
}

#[test]
fn an_engine_opened_with_the_core_registers_it_and_no_one_else_can_take_its_names() {
    for target in targets("core_names") {
        let engine = target.open_core();
        let mut ported: Vec<&str> = superschematic_engine_behaviors::NAMES.to_vec();
        ported.sort_unstable();
        assert_eq!(engine.behaviors().names(), ported);
        let fields: Vec<Value> = engine
            .behaviors()
            .declaration("Workflow")
            .expect("Workflow's declaration")["fields"]
            .as_array()
            .expect("fields")
            .iter()
            .map(|field| field["name"].clone())
            .collect();
        assert_eq!(fields, [json!("status"), json!("enteredAt")]);
        assert_eq!(
            usage(engine.behaviors().register(Impostor)),
            "behavior Workflow is already registered with this engine"
        );
        let opened = Engine::open({
            let mut options = target.core_options();
            options.behaviors =
                std::mem::take(&mut options.behaviors).extend(BehaviorSet::new().with(Impostor));
            options
        });
        assert!(
            usage(opened).contains("already registered"),
            "an open that names a core behavior again"
        );
    }
}

/// The steps document the core binary loads with no extension linked
/// (`fixture-variants-json`).
fn steps() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-variants-json/src/step.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the steps document"))
        .expect("JSON")
}

#[test]
fn it_runs_the_steps_document_a_steps_kind_stays_and_its_result_has_the_kinds_shape() {
    for target in targets("core_steps") {
        let engine = target.open_core();
        engine
            .schemas()
            .define(&alice(), steps(), DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(&alice(), "Step", SchemaTarget::default())
            .expect("publish");
        let check = engine
            .instances()
            .create(
                &writer(),
                "Step",
                &json!({"title": "Lint", "kind": "verify", "result": {"passed": true, "checks": [{"name": "eslint", "ok": true}]}}),
                CreateOptions::default(),
            )
            .expect("create");
        assert_eq!(check.seq, 1);
        let update = |patch: Value| -> Result<Value, Error> {
            engine
                .instances()
                .update(
                    &writer(),
                    "Step",
                    &check.id,
                    &patch,
                    WriteOptions::default(),
                )
                .map(|record| Value::Object(record.data))
        };
        assert_eq!(
            refusal(update(json!({"kind": "note", "result": null}))).message(),
            "Step in namespace default (version 1): kind: kind is a constant of Step: its create sets it and nothing changes it after"
        );
        assert_eq!(
            refusal(update(json!({"result": {"passed": "yes"}})))
                .code()
                .as_str(),
            "invalid_instance"
        );
        assert_eq!(
            refusal(engine.instances().create(
                &writer(),
                "Step",
                &json!({"title": "Idea", "kind": "note", "result": {"passed": true}}),
                CreateOptions::default(),
            ))
            .code()
            .as_str(),
            "invalid_instance"
        );
        assert_eq!(
            update(json!({"result": {"passed": false}})).expect("update"),
            json!({"title": "Lint", "kind": "verify", "result": {"passed": false, "checks": [{"name": "eslint", "ok": true}]}})
        );
    }
}
