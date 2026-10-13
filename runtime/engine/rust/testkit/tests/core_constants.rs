//! Constants, the core's fields set at create: the cases of
//! `test/core-constants.test.ts` (an update that changes a listed field is
//! invalid_instance with the rule constant at the field, a caller's update
//! and an operation's `update()` alike, unless the caller holds the
//! config's permission; a field the create leaves absent stays absent;
//! absent and null are one; `validate_update()` reports it; the config
//! names the type's own fields, and any change to it is allowed, the
//! behavior's adding and removing included), on SQLite and, with the
//! variable set, on Postgres. The TypeScript file holds an operation's
//! `update()` to it through Revisions, not ported yet (wave 2); a test
//! behavior of this file calls `update()` and `validate_update()` instead.
//! The describe document is milestone 6's.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Behavior, ConfigTarget, CreateOptions, DefineOptions, Engine, EngineError, EngineErrorKind,
    Hooks, Principal, Result, SchemaTarget, ValidateOptions, WriteOptions,
};
use support::{alice, issue, issues_of, refusal, repository, schema_document, targets};

fn renamer() -> Principal {
    Principal::new("rita", ["steps.rename"])
}

/// A run: a key, a kind, a title, labels and an owner, whose behaviors are
/// the ones given.
fn run_document(behaviors: Value) -> Value {
    let mut document = schema_document(
        "runs",
        json!([
            {"name": "key", "typeRef": {"name": "string"}, "required": true},
            {"name": "kind", "typeRef": {"name": "string"}},
            {"name": "title", "typeRef": {"name": "string"}},
            {"name": "labels", "typeRef": {"name": "string", "isArray": true}},
            {"name": "owner", "typeRef": {"name": "Owner"}}
        ]),
        json!({"types": {"Owner": {"name": "Owner", "role": "EmbeddedStruct", "fields": [
            {"name": "team", "typeRef": {"name": "string"}, "required": true},
            {"name": "lead", "typeRef": {"name": "string"}}
        ]}}}),
    );
    document["types"]["runs"]["behaviors"] = behaviors;
    document
}

fn keep(fields: &[&str], permission: Option<&str>) -> Value {
    let mut config = json!({ "fields": fields });
    if let Some(permission) = permission {
        config["permission"] = json!(permission);
    }
    json!({"name": "Constants", "config": config})
}

fn define(engine: &Engine, document: &Value) -> Result<()> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) {
    define(engine, document).expect("define");
    let name = document["name"].as_str().expect("a name");
    engine
        .schemas()
        .publish(&alice(), name, SchemaTarget::default())
        .expect("publish");
}

fn create(engine: &Engine, schema: &str, data: Value, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

fn update(
    engine: &Engine,
    principal: &Principal,
    id: &str,
    patch: Value,
) -> Result<superschematic_engine::InstanceRecord> {
    engine
        .instances()
        .update(principal, "runs", id, &patch, WriteOptions::default())
}

fn issues(error: &EngineError) -> Vec<(String, String, String)> {
    match error.kind() {
        EngineErrorKind::InstanceValidation { issues } => issues
            .iter()
            .map(|issue| {
                (
                    issue.path.clone(),
                    issue.rule.clone(),
                    issue.message.clone(),
                )
            })
            .collect(),
        other => panic!("expected an InstanceValidationError, got {other:?}"),
    }
}

/// The (path, rule) of each issue an update is refused with.
fn refused(
    engine: &Engine,
    principal: &Principal,
    id: &str,
    patch: Value,
) -> Vec<(String, String)> {
    let error = refusal(update(engine, principal, id, patch));
    assert_eq!(error.code().as_str(), "invalid_instance");
    issues(&error)
        .into_iter()
        .map(|(path, rule, _)| (path, rule))
        .collect()
}

fn constant(path: &str) -> (String, String) {
    (path.to_owned(), "constant".to_owned())
}

#[test]
fn an_update_that_changes_a_listed_field_is_refused_at_the_field_whoever_makes_it() {
    for target in targets("constants_update") {
        let engine = target.open_core();
        publish(
            &engine,
            &run_document(json!([keep(&["key", "labels", "owner"], None)])),
        );
        create(
            &engine,
            "runs",
            json!({"key": "r1", "title": "First", "labels": ["a"], "owner": {"team": "core"}}),
            "r1",
        );
        let error = refusal(update(&engine, &alice(), "r1", json!({"key": "r2"})));
        assert_eq!(
            issues(&error),
            [(
                "key".to_owned(),
                "constant".to_owned(),
                "key is a constant of runs: its create sets it and nothing changes it after"
                    .to_owned()
            )]
        );
        // A change inside a list or an object is a change; every one is an
        // issue.
        assert_eq!(
            refused(
                &engine,
                &alice(),
                "r1",
                json!({"labels": ["a", "b"], "owner": {"lead": "ann"}, "key": "r9"})
            ),
            [constant("key"), constant("labels"), constant("owner")]
        );
        assert_eq!(
            refused(&engine, &renamer(), "r1", json!({"labels": null})),
            [constant("labels")]
        );
        // The same value is no change, and the fields it does not list stay
        // open.
        let updated = update(
            &engine,
            &alice(),
            "r1",
            json!({"key": "r1", "owner": {"team": "core"}, "title": "Renamed"}),
        )
        .expect("update");
        assert_eq!(
            (updated.seq, updated.data["title"].clone()),
            (2, json!("Renamed"))
        );
        assert_eq!(
            Value::Object(
                engine
                    .instances()
                    .get(&alice(), "runs", "r1", Default::default())
                    .expect("get")
                    .expect("r1")
                    .data
            ),
            json!({"key": "r1", "title": "Renamed", "labels": ["a"], "owner": {"team": "core"}})
        );
    }
}

#[test]
fn a_field_the_create_leaves_absent_stays_absent_and_absent_and_null_are_one() {
    for target in targets("constants_absent") {
        let engine = target.open_core();
        publish(
            &engine,
            &run_document(json!([keep(&["key", "kind"], None)])),
        );
        create(&engine, "runs", json!({"key": "r1"}), "r1");
        assert_eq!(
            refused(&engine, &alice(), "r1", json!({"kind": "build"})),
            [constant("kind")]
        );
        create(&engine, "runs", json!({"key": "r2", "kind": null}), "r2");
        assert_eq!(
            refused(&engine, &alice(), "r2", json!({"kind": "build"})),
            [constant("kind")]
        );
        // Removing a null, or setting one where there is none, changes
        // nothing.
        assert_eq!(
            update(&engine, &alice(), "r2", json!({"kind": null}))
                .expect("update")
                .data
                .get("kind"),
            None
        );
        assert_eq!(
            update(&engine, &alice(), "r1", json!({"kind": null, "title": "x"}))
                .expect("update")
                .data["title"],
            json!("x")
        );
    }
}

#[test]
fn a_caller_with_the_configs_permission_may_change_them_and_no_other_caller_may() {
    for target in targets("constants_permission") {
        let engine = target.open_core();
        publish(
            &engine,
            &run_document(json!([keep(&["key", "kind"], Some("steps.rename"))])),
        );
        create(&engine, "runs", json!({"key": "r1"}), "r1");
        assert_eq!(
            issues(&refusal(update(&engine, &alice(), "r1", json!({"key": "r2"})))),
            [(
                "key".to_owned(),
                "constant".to_owned(),
                "key is a constant of runs: its create sets it and nothing changes it after; only a caller with steps.rename may change it".to_owned()
            )]
        );
        assert_eq!(
            Value::Object(
                update(
                    &engine,
                    &renamer(),
                    "r1",
                    json!({"key": "r2", "kind": "build"})
                )
                .expect("rename")
                .data
            ),
            json!({"key": "r2", "kind": "build"})
        );
        assert_eq!(
            Value::Object(
                update(&engine, &renamer(), "r1", json!({"kind": null}))
                    .expect("remove")
                    .data
            ),
            json!({"key": "r2"})
        );
    }
}

// test.Retitle changes an instance through update() and asks
// validate_update() what update() would refuse, as Revisions' propose and
// approve do.
struct Retitle;

impl Behavior for Retitle {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{
            "name": "test.Retitle",
            "operations": [
                {"name": "retitle", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["patch"], "properties": {"patch": {"type": "object"}}}, "resultSchema": true, "writes": true},
                {"name": "check", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["patch"], "properties": {"patch": {"type": "object"}}}, "resultSchema": true}
            ]
        }"#
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![
                ("retitle", |_, cx, params| {
                    let fields = cx.update(&params["patch"])?;
                    Ok(Value::Object((*fields).clone()))
                }),
                ("check", |_, cx, params| {
                    let issues = cx.validate_update(&params["patch"])?;
                    Ok(Value::Array(
                        issues
                            .into_iter()
                            .map(|issue| json!([issue.path, issue.rule]))
                            .collect(),
                    ))
                }),
            ],
            ..Hooks::default()
        }
    }
}

#[test]
fn an_operations_update_is_held_to_it_too_and_validate_update_reports_it() {
    for target in targets("constants_operation") {
        let engine = target.open_with(
            superschematic_engine_behaviors::core().with(Retitle),
            |_| {},
        );
        publish(
            &engine,
            &run_document(json!([{"name": "test.Retitle"}, keep(&["title"], None)])),
        );
        create(&engine, "runs", json!({"key": "r1", "title": "Plan"}), "r1");
        let invoke = |operation: &str, patch: Value| -> Result<Value> {
            engine.instances().invoke(
                &alice(),
                "runs",
                "r1",
                operation,
                &json!({ "patch": patch }),
                WriteOptions::default(),
            )
        };
        assert_eq!(
            invoke("check", json!({"title": "Plan C", "kind": "x"})).expect("check"),
            json!([["title", "constant"]])
        );
        let error = refusal(invoke("retitle", json!({"title": "Plan C", "kind": "x"})));
        assert_eq!(
            issues(&error)
                .into_iter()
                .map(|(path, rule, _)| (path, rule))
                .collect::<Vec<_>>(),
            [constant("title")]
        );
        assert_eq!(
            invoke("retitle", json!({"kind": "plan"})).expect("retitle")["kind"],
            json!("plan")
        );
        assert_eq!(
            invoke("check", json!({"kind": "x"})).expect("check"),
            json!([])
        );
    }
}

#[test]
fn its_fields_are_the_types_own_and_a_new_version_may_change_add_or_remove_it() {
    for target in targets("constants_versions") {
        let engine = target.open_core();
        let refusals = |fields: &[&str]| {
            issues_of(&refusal(define(
                &engine,
                &run_document(json!([keep(fields, None)])),
            )))
        };
        assert_eq!(
            refusals(&["key", "nope"]),
            [issue(
                "/types/runs/behaviors/0/config",
                "type runs: behavior Constants config: fields: nope is not a field of runs (its fields: key, kind, title, labels, owner)"
            )]
        );
        assert_eq!(
            refusals(&[]),
            [
                issue("/types/runs/behaviors/0", "must match \"then\" schema"),
                issue(
                    "/types/runs/behaviors/0/config/fields",
                    "must NOT have fewer than 1 items"
                )
            ]
        );
        // status is Workflow's, not the type's own.
        let flow = json!({"name": "Workflow", "config": {"states": ["open", "closed"], "transitions": [{"from": "open", "to": "closed"}]}});
        let error = refusal(define(
            &engine,
            &run_document(json!([flow, keep(&["status"], None)])),
        ));
        assert!(
            error
                .message()
                .contains("fields: status is not a field of runs"),
            "{}",
            error.message()
        );

        publish(&engine, &run_document(json!([])));
        create(&engine, "runs", json!({"key": "r1", "kind": "build"}), "r1");
        publish(&engine, &run_document(json!([keep(&["key"], None)])));
        assert_eq!(
            refused(&engine, &alice(), "r1", json!({"key": "r2"})),
            [constant("key")]
        );
        publish(
            &engine,
            &run_document(json!([keep(&["key", "kind"], Some("steps.rename"))])),
        );
        assert_eq!(
            refused(&engine, &alice(), "r1", json!({"kind": "test"})),
            [constant("kind")]
        );
        publish(&engine, &run_document(json!([keep(&["kind"], None)])));
        assert_eq!(
            update(&engine, &alice(), "r1", json!({"key": "r2"}))
                .expect("update")
                .data["key"],
            json!("r2")
        );
        publish(&engine, &run_document(json!([])));
        assert_eq!(
            update(&engine, &alice(), "r1", json!({"kind": "test"}))
                .expect("update")
                .data["kind"],
            json!("test")
        );
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "runs", SchemaTarget::default())
                .expect("live")
                .expect("a live version")
                .version,
            Some(5)
        );
    }
}

/// The steps document the core binary loads with no extension linked
/// (`fixture-variants-json`), as the TypeScript tests' `stepsDocument()`
/// reads it.
fn steps_document() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-variants-json/src/step.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the steps document"))
        .expect("JSON")
}

#[test]
fn the_document_the_core_binary_builds_keeps_a_steps_kind() {
    for target in targets("constants_steps") {
        let engine = target.open_core();
        publish(&engine, &steps_document());
        create(
            &engine,
            "Step",
            json!({"title": "Check", "kind": "verify"}),
            "s1",
        );
        let error = refusal(engine.instances().update(
            &alice(),
            "Step",
            "s1",
            &json!({"kind": "note"}),
            WriteOptions::default(),
        ));
        assert_eq!(
            issues(&error)
                .into_iter()
                .map(|(path, rule, _)| (path, rule))
                .collect::<Vec<_>>(),
            [constant("kind")]
        );
        let composed: Vec<String> = engine
            .schemas()
            .behaviors(&alice(), "Step", ValidateOptions::default())
            .expect("behaviors")
            .into_iter()
            .map(|behavior| behavior.name)
            .collect();
        assert_eq!(composed, ["Constants", "Variants"]);
    }
}
