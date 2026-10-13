//! Variants, the core's field typed by another field's value: the cases of
//! `test/core-variants.test.ts` (while kind holds a value the config lists,
//! result is held to that type as strictly as a field of it, unknown keys
//! refused at every depth; while kind holds another value or none, result
//! holds none; the config is held to the type and the document; a new
//! version keeps each value's type and may give a new value one; the types
//! it checks are held to the compatibility rule; the JSON Schema of each
//! variant), on SQLite and, with the variable set, on Postgres. The
//! describe document and the tools that carry the rules are milestone 6's:
//! here the rules are the behavior's own `instance_schema`, over a stand-in
//! for the describe document's type schemas.

mod support;

use serde_json::{json, Value};

use superschematic_engine::behaviors::InstanceSchemaForm;
use superschematic_engine::{
    Behavior, CreateOptions, DefineOptions, Engine, EngineError, EngineErrorKind, Error,
    SchemaTarget, ValidateOptions, WriteOptions,
};
use superschematic_engine_behaviors::{Variants, VariantsConfig};
use support::{alice, refusal, repository, targets};

/// The steps document the core binary loads with no extension linked
/// (`fixture-variants-json`), as the TypeScript tests' `stepsDocument()`
/// reads it: Step composes Constants, which keeps its kind, and Variants,
/// which types its result by its kind.
fn steps() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-variants-json/src/step.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the steps document"))
        .expect("JSON")
}

/// The document's Variants config, to change.
fn variants_of(document: &mut Value) -> &mut Value {
    document["types"]["Step"]["behaviors"]
        .as_array_mut()
        .expect("behaviors")
        .iter_mut()
        .find(|behavior| behavior["name"] == "Variants")
        .map(|behavior| &mut behavior["config"])
        .expect("Variants")
}

fn without(document: &mut Value, behavior: &str) {
    document["types"]["Step"]["behaviors"]
        .as_array_mut()
        .expect("behaviors")
        .retain(|composed| composed["name"] != behavior);
}

fn define(engine: &Engine, document: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) {
    define(engine, document).expect("define");
    engine
        .schemas()
        .publish(&alice(), "Step", SchemaTarget::default())
        .expect("publish");
}

fn create(engine: &Engine, data: Value, id: Option<&str>) -> Result<Value, Error> {
    engine
        .instances()
        .create(
            &alice(),
            "Step",
            &data,
            CreateOptions {
                id,
                ..Default::default()
            },
        )
        .map(|record| Value::Object(record.data))
}

fn update(engine: &Engine, id: &str, patch: Value) -> Result<Value, Error> {
    engine
        .instances()
        .update(&alice(), "Step", id, &patch, WriteOptions::default())
        .map(|record| Value::Object(record.data))
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
        other => panic!("expected an InstanceValidationError, got {other:?}: {error}"),
    }
}

/// The (path, rule) of each issue a write is refused with, sorted.
fn sorted(result: Result<Value, Error>) -> Vec<(String, String)> {
    let mut out: Vec<(String, String)> = issues(&refusal(result))
        .into_iter()
        .map(|(path, rule, _)| (path, rule))
        .collect();
    out.sort();
    out
}

fn pairs(expected: &[(&str, &str)]) -> Vec<(String, String)> {
    expected
        .iter()
        .map(|(path, rule)| ((*path).to_owned(), (*rule).to_owned()))
        .collect()
}

fn passed() -> Value {
    json!({"passed": true, "checks": [{"name": "lint", "ok": true}]})
}

#[test]
fn a_result_is_held_to_the_type_its_kind_picks_and_an_unlisted_kind_holds_none() {
    for target in targets("variants_create") {
        let engine = target.open_core();
        publish(&engine, &steps());
        assert_eq!(
            create(
                &engine,
                json!({"title": "Check", "kind": "verify", "result": passed()}),
                Some("s1")
            )
            .expect("create")["result"],
            passed()
        );
        assert_eq!(
            sorted(create(
                &engine,
                json!({"title": "Check", "kind": "verify", "result": {"approved": true}}),
                None
            )),
            pairs(&[
                ("result.approved", "unknown"),
                ("result.passed", "required")
            ])
        );
        assert_eq!(
            sorted(create(
                &engine,
                json!({"title": "Check", "kind": "verify", "result": {"passed": true, "checks": [{"name": "lint", "ok": "yes", "by": "ci"}]}}),
                None
            )),
            pairs(&[
                ("result.checks[0].by", "unknown"),
                ("result.checks[0].ok", "type")
            ])
        );
        assert_eq!(
            sorted(create(
                &engine,
                json!({"title": "Look", "kind": "review", "result": passed()}),
                None
            )),
            pairs(&[
                ("result.approved", "required"),
                ("result.checks", "unknown"),
                ("result.passed", "unknown")
            ])
        );
        assert_eq!(
            issues(&refusal(create(
                &engine,
                json!({"title": "Check", "kind": "verify", "result": "done"}),
                None
            ))),
            [(
                "result".to_owned(),
                "type".to_owned(),
                "a VerifyResult is a JSON object".to_owned()
            )]
        );
        let unlisted = refusal(create(
            &engine,
            json!({"title": "Note", "kind": "note", "result": {"text": "hi"}}),
            None,
        ));
        assert_eq!(unlisted.code().as_str(), "invalid_instance");
        assert_eq!(
            issues(&unlisted),
            [(
                "result".to_owned(),
                "variant".to_owned(),
                "result holds a value while kind is \"note\"; result has a type only while kind is \"review\", \"verify\", and no value otherwise".to_owned()
            )]
        );
        // No result, or null, is none.
        create(
            &engine,
            json!({"title": "Note", "kind": "note"}),
            Some("s2"),
        )
        .expect("s2");
        create(
            &engine,
            json!({"title": "Note", "kind": "note", "result": null}),
            Some("s3"),
        )
        .expect("s3");
        create(
            &engine,
            json!({"title": "Look", "kind": "review"}),
            Some("s4"),
        )
        .expect("s4");
        assert_eq!(
            create(
                &engine,
                json!({"title": "Look", "kind": "review", "result": {"approved": false, "notes": "Too long."}}),
                None
            )
            .expect("create")["result"],
            json!({"approved": false, "notes": "Too long."})
        );
        // schemas.validate checks a value against the version's own rules;
        // a behavior's validate judges a write.
        assert!(engine
            .schemas()
            .validate(
                &alice(),
                "Step",
                &json!({"title": "Note", "kind": "note", "result": {"text": "hi"}}),
                ValidateOptions::default()
            )
            .expect("validate")
            .is_empty());
    }
}

#[test]
fn an_update_is_held_to_it_as_merged_and_a_new_kind_needs_a_result_of_its_type() {
    for target in targets("variants_update") {
        let engine = target.open_core();
        publish(&engine, &steps());
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": passed()}),
            Some("s1"),
        )
        .expect("s1");
        // A patch merges into the result; a list in it replaces the list.
        assert_eq!(
            sorted(update(
                &engine,
                "s1",
                json!({"result": {"checks": [{"name": "test", "ok": 1}]}})
            )),
            pairs(&[("result.checks[0].ok", "type")])
        );
        assert_eq!(
            update(&engine, "s1", json!({"result": {"passed": false}})).expect("update")["result"],
            json!({"passed": false, "checks": [{"name": "lint", "ok": true}]})
        );
        assert_eq!(
            sorted(update(&engine, "s1", json!({"result": {"passed": null}}))),
            pairs(&[("result.passed", "required")])
        );
        assert_eq!(
            update(&engine, "s1", json!({"result": null})).expect("update")["result"],
            Value::Null
        );
    }
    // Without Constants, kind may change, and the result goes with it.
    for target in targets("variants_update_kind") {
        let engine = target.open_core();
        let mut open = steps();
        without(&mut open, "Constants");
        publish(&engine, &open);
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": passed()}),
            Some("s1"),
        )
        .expect("s1");
        assert_eq!(
            sorted(update(&engine, "s1", json!({"kind": "review"}))),
            pairs(&[
                ("result.approved", "required"),
                ("result.checks", "unknown"),
                ("result.passed", "unknown")
            ])
        );
        assert_eq!(
            sorted(update(&engine, "s1", json!({"kind": "note"}))),
            pairs(&[("result", "variant")])
        );
        // The patch merges into the result, so the new kind's result
        // removes the old one's members.
        assert_eq!(
            sorted(update(
                &engine,
                "s1",
                json!({"kind": "review", "result": {"approved": true}})
            )),
            pairs(&[("result.checks", "unknown"), ("result.passed", "unknown")])
        );
        assert_eq!(
            update(
                &engine,
                "s1",
                json!({"kind": "review", "result": {"approved": true, "passed": null, "checks": null}})
            )
            .expect("update")["result"],
            json!({"approved": true})
        );
        assert_eq!(
            update(&engine, "s1", json!({"kind": "note", "result": null})).expect("update")["kind"],
            json!("note")
        );
    }
}

#[test]
fn the_config_is_held_to_the_type_and_the_document() {
    for target in targets("variants_config") {
        let engine = target.open_core();
        let refused = |change: &dyn Fn(&mut Value)| -> String {
            let mut document = steps();
            change(&mut document);
            let error = refusal(define(&engine, &document));
            assert_eq!(error.name(), "SchemaDocumentError");
            error
                .issues()
                .iter()
                .map(|issue| issue.message.clone())
                .collect::<Vec<_>>()
                .join("; ")
        };
        let has = |message: String, part: &str| {
            assert!(message.contains(part), "{message} lacks {part}");
        };
        assert_eq!(
            refused(&|document| variants_of(document)["field"] = json!("nope")),
            "type Step: behavior Variants config: field: nope is not a field of Step (its fields: title, kind, result)"
        );
        has(
            refused(&|document| variants_of(document)["by"] = json!("nope")),
            "by: nope is not a field of Step",
        );
        has(
            refused(&|document| variants_of(document)["field"] = json!("kind")),
            "field and by both name kind: a field cannot be typed by its own value",
        );
        has(
            refused(&|document| variants_of(document)["field"] = json!("title")),
            "field: Step.title is not an open JSON object; Variants types a Generic.JSON field, or one of a scalar whose values are JSON objects",
        );
        for type_ref in [
            json!({"name": "VerifyResult"}),
            json!({"name": "Generic.JSON", "isArray": true}),
        ] {
            has(
                refused(&|document| {
                    document["types"]["Step"]["fields"]
                        .as_array_mut()
                        .expect("fields")
                        .push(json!({"name": "other", "typeRef": type_ref}));
                    variants_of(document)["field"] = json!("other");
                }),
                "field: Step.other is not an open JSON object",
            );
        }
        for (name, type_name) in [("meta", "Generic.JSON"), ("rank", "number")] {
            has(
                refused(&|document| {
                    document["types"]["Step"]["fields"]
                        .as_array_mut()
                        .expect("fields")
                        .push(json!({"name": name, "typeRef": {"name": type_name}}));
                    variants_of(document)["by"] = json!(name);
                }),
                &format!("by: Step.{name} is not a string or enum field"),
            );
        }
        for type_name in ["Nope", "Step"] {
            has(
                refused(&|document| variants_of(document)["types"]["verify"] = json!(type_name)),
                &format!("types: \"verify\" names {type_name}, which is not a type of the schema document besides Step (its types: Check, ReviewResult, VerifyResult)"),
            );
        }
        has(
            refused(&|document| {
                variants_of(document)["types"]["deploy"] = json!("VerifyResult");
            }),
            "types: \"deploy\" is not a value of kind (its values: verify, review, note)",
        );
        // A type it checks holds only field types the schema runtime
        // validates.
        assert_eq!(
            refused(&|document| {
                document["types"]["Tally"] = json!({"name": "Tally", "role": "EmbeddedStruct", "fields": [{"name": "counts", "typeRef": {"name": "number", "isMap": true}}]});
                variants_of(document)["types"]["verify"] = json!("Tally");
            }),
            "field Tally.counts is a map, which the schema runtime does not validate"
        );
        // A string field picks by any value.
        let mut by_title = steps();
        variants_of(&mut by_title)["by"] = json!("title");
        variants_of(&mut by_title)["types"] = json!({"Check": "VerifyResult"});
        publish(&engine, &by_title);
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": passed()}),
            None,
        )
        .expect("create");
        assert_eq!(
            sorted(create(
                &engine,
                json!({"title": "Other", "kind": "verify", "result": passed()}),
                None
            )),
            pairs(&[("result", "variant")])
        );
    }
}

#[test]
fn a_new_version_keeps_field_by_and_each_values_type_and_may_give_another_value_one() {
    for target in targets("variants_versions") {
        let engine = target.open_core();
        let mut document = steps();
        publish(&engine, &document);
        create(
            &engine,
            json!({"title": "Check", "kind": "verify", "result": passed()}),
            Some("s1"),
        )
        .expect("s1");
        create(
            &engine,
            json!({"title": "Note", "kind": "note"}),
            Some("s2"),
        )
        .expect("s2");
        let change = |edit: &dyn Fn(&mut Value)| -> String {
            let mut next = document.clone();
            edit(&mut next);
            let error = refusal(define(&engine, &next));
            assert_eq!(error.name(), "IncompatibleChangeError");
            error.message().to_owned()
        };
        let has = |message: String, parts: &[&str]| {
            for part in parts {
                assert!(message.contains(part), "{message} lacks {part}");
            }
        };
        has(
            change(&|next| {
                variants_of(next)["types"]
                    .as_object_mut()
                    .expect("types")
                    .shift_remove("review");
            }),
            &[
                "behavior Variants on type Step cannot change its config",
                ": types keeps \"review\": an instance whose kind is \"review\" may hold a ReviewResult, which a version without it refuses",
            ],
        );
        has(
            change(&|next| variants_of(next)["types"]["verify"] = json!("ReviewResult")),
            &["types: \"verify\" stays VerifyResult: an instance's result was checked against it, not against ReviewResult"],
        );
        has(
            change(&|next| variants_of(next)["by"] = json!("title")),
            &["field and by stay result and kind"],
        );

        // A note gains a type: no stored note holds a result.
        document["types"]["NoteResult"] = json!({"name": "NoteResult", "role": "EmbeddedStruct", "fields": [{"name": "text", "typeRef": {"name": "string"}, "required": true}]});
        variants_of(&mut document)["types"]["note"] = json!("NoteResult");
        publish(&engine, &document);
        assert_eq!(
            update(&engine, "s2", json!({"result": {"text": "hi"}})).expect("update")["result"],
            json!({"text": "hi"})
        );

        // Off: result is open JSON again. On again: refused, the stored
        // results were never checked.
        let mut plain = document.clone();
        without(&mut plain, "Variants");
        publish(&engine, &plain);
        create(
            &engine,
            json!({"title": "Note", "kind": "note", "result": {"anything": 1}}),
            None,
        )
        .expect("create");
        let error = refusal(define(&engine, &document));
        assert!(
            error.message().contains("behavior Variants cannot be added to type Step, which has instances: the instances already hold values of result that no type checked"),
            "{}",
            error.message()
        );
    }
}

#[test]
fn with_no_instance_field_by_and_each_type_may_change() {
    for target in targets("variants_empty") {
        let engine = target.open_core();
        let document = steps();
        publish(&engine, &document);
        let mut next = document.clone();
        variants_of(&mut next)["types"]
            .as_object_mut()
            .expect("types")
            .shift_remove("review");
        variants_of(&mut next)["types"]["verify"] = json!("ReviewResult");
        variants_of(&mut next)["by"] = json!("title");
        publish(&engine, &next);
        create(
            &engine,
            json!({"title": "verify", "kind": "note", "result": {"approved": true}}),
            Some("s1"),
        )
        .expect("s1");
        let error = refusal(define(&engine, &document));
        assert!(
            error
                .message()
                .contains("field and by stay result and title"),
            "{}",
            error.message()
        );
    }
}

#[test]
fn a_type_it_checks_is_held_to_the_compatibility_rule_and_a_type_nothing_reaches_stays_free() {
    for target in targets("variants_compat") {
        let engine = target.open_core();
        let mut document = steps();
        document["types"]["Spare"] = json!({"name": "Spare", "role": "EmbeddedStruct", "fields": [{"name": "note", "typeRef": {"name": "string"}}]});
        publish(&engine, &document);
        let change = |edit: &dyn Fn(&mut Value)| -> Vec<String> {
            let mut next = document.clone();
            edit(&mut next);
            match define(&engine, &next) {
                Ok(()) => Vec::new(),
                Err(Error::Engine(error)) if error.name() == "IncompatibleChangeError" => error
                    .changes()
                    .iter()
                    .map(|change| change.message.clone())
                    .collect(),
                Err(other) => panic!("{other}"),
            }
        };
        let push = |next: &mut Value, type_name: &str, field: Value| {
            next["types"][type_name]["fields"]
                .as_array_mut()
                .expect("fields")
                .push(field);
        };
        assert_eq!(
            change(&|next| push(
                next,
                "VerifyResult",
                json!({"name": "score", "typeRef": {"name": "number"}, "required": true})
            )),
            ["field VerifyResult.score is added as required"]
        );
        assert_eq!(
            change(&|next| next["types"]["Check"]["fields"][1] =
                json!({"name": "ok", "typeRef": {"name": "string"}, "required": true})),
            ["field Check.ok changes type from boolean to string"]
        );
        assert_eq!(
            change(&|next| {
                next["types"]["ReviewResult"]["fields"]
                    .as_array_mut()
                    .expect("fields")
                    .pop();
            }),
            ["field ReviewResult.notes is removed"]
        );
        // A new optional field, or any change to a type nothing reaches, is
        // allowed.
        assert!(change(&|next| push(
            next,
            "VerifyResult",
            json!({"name": "score", "typeRef": {"name": "number"}})
        ))
        .is_empty());
        assert!(change(&|next| push(
            next,
            "Spare",
            json!({"name": "size", "typeRef": {"name": "number"}, "required": true})
        ))
        .is_empty());
        // A version without Variants checks VerifyResult no more, so it may
        // change it.
        assert!(change(&|next| {
            without(next, "Variants");
            push(
                next,
                "VerifyResult",
                json!({"name": "score", "typeRef": {"name": "number"}, "required": true}),
            );
        })
        .is_empty());
    }
}

#[test]
fn the_engine_accepts_what_each_variant_allows() {
    for target in targets("variants_samples") {
        let engine = target.open_core();
        publish(&engine, &steps());
        let samples = [
            (
                json!({"title": "t", "kind": "verify", "result": passed()}),
                true,
            ),
            (
                json!({"title": "t", "kind": "verify", "result": {"approved": true}}),
                false,
            ),
            (
                json!({"title": "t", "kind": "verify", "result": {"passed": true, "checks": [{"name": "lint", "ok": true, "by": "ci"}]}}),
                false,
            ),
            (
                json!({"title": "t", "kind": "review", "result": {"approved": true}}),
                true,
            ),
            (
                json!({"title": "t", "kind": "note", "result": {"text": "hi"}}),
                false,
            ),
            (json!({"title": "t", "kind": "note"}), true),
            (
                json!({"title": "t", "kind": "verify", "result": null}),
                true,
            ),
        ];
        for (data, accepted) in samples {
            match create(&engine, data.clone(), None) {
                Ok(_) => assert!(accepted, "{data}"),
                Err(Error::Engine(error)) if error.name() == "InstanceValidationError" => {
                    assert!(!accepted, "{data}: {error}");
                }
                Err(other) => panic!("{data}: {other}"),
            }
        }
    }
}

#[test]
fn its_instance_schema_is_an_if_then_per_value_and_one_for_every_other_value() {
    let config = VariantsConfig {
        field: "result".to_owned(),
        by: "kind".to_owned(),
        types: vec![
            ("review".to_owned(), "ReviewResult".to_owned()),
            ("verify".to_owned(), "VerifyResult".to_owned()),
        ],
        required: false,
    };
    let instance_schema = Variants.hooks().instance_schema.expect("instance_schema");
    // A stand-in for the describe document's type schema.
    let type_schema = |name: &str, nullable: bool| json!({ "type": name, "nullable": nullable });
    let rules = instance_schema(
        &Variants,
        &config,
        InstanceSchemaForm::Instance,
        &type_schema,
    );
    assert_eq!(
        rules,
        [
            json!({
                "description": "While kind is \"review\", result is a ReviewResult",
                "if": {"properties": {"kind": {"const": "review"}}, "required": ["kind"]},
                "then": {"properties": {"result": {"type": "ReviewResult", "nullable": true}}}
            }),
            json!({
                "description": "While kind is \"verify\", result is a VerifyResult",
                "if": {"properties": {"kind": {"const": "verify"}}, "required": ["kind"]},
                "then": {"properties": {"result": {"type": "VerifyResult", "nullable": true}}}
            }),
            json!({
                "description": "While kind holds another value or none, result holds none",
                "if": {"not": {"properties": {"kind": {"enum": ["review", "verify"]}}, "required": ["kind"]}},
                "then": {"properties": {"result": {"type": "null"}}}
            }),
        ]
    );
    // A patch's if needs kind in the patch, since a patch that leaves kind
    // alone does not show it.
    let patch = instance_schema(&Variants, &config, InstanceSchemaForm::Patch, &type_schema);
    assert_eq!(patch[1]["if"], rules[1]["if"]);
    assert_eq!(
        patch[2]["if"],
        json!({"properties": {"kind": {"not": {"enum": ["review", "verify"]}}}, "required": ["kind"]})
    );
    // A required field's type is not nullable.
    let required = VariantsConfig {
        required: true,
        ..config
    };
    assert_eq!(
        instance_schema(
            &Variants,
            &required,
            InstanceSchemaForm::Instance,
            &type_schema
        )[0]["then"]["properties"]["result"]["nullable"],
        json!(false)
    );
}
