//! What a behavior learns of its schema and its caller, the cases of
//! `test/behavior-types.test.ts` (ConfigTarget.types, the document checks
//! and compatibility rule over the types parse_config reads, and a
//! context's validate(type, value)), `test/behavior-publish.test.ts`
//! (afterConfigChange, and parse_config's field schemas) and
//! `test/behavior-permissions.test.ts` (can() through the permission
//! matcher); on SQLite and, with the variable set, on Postgres. A read of
//! ConfigTarget.types after parse_config returns is one Rust's borrow of
//! the target refuses at compile time, as it refuses a hook that is not a
//! function, a matcher that returns a promise or anything but a boolean,
//! and a hook that returns a promise.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, ConfigType, PublishCx, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    has_any_permission, Behavior, BehaviorSet, ConfigTarget, CreateOptions, DefineOptions, Engine,
    EngineErrorKind, Error, GuardRequest, Hooks, NamespaceOptions, PermissionMatcher, Principal,
    PrincipalService, Result, SchemaTarget, ValidationIssue, WriteOptions,
};
use support::{
    alice, defect, issues_of, item_document, publish, refusal, schema_document, tables_of, targets,
    texts, veto, Target,
};

type Seen = Arc<Mutex<Vec<Value>>>;

fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .map(|list| {
            list.iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

fn type_json(read: &ConfigType) -> Value {
    json!({
        "name": read.name,
        "fields": read.fields.iter().map(|field| {
            let mut out = json!({"key": field.key, "type": field.type_name, "kind": field.kind.as_str()});
            if let Some(json_type) = &field.json_type {
                out["jsonType"] = json!(json_type);
            }
            out["depth"] = json!(field.depth);
            out["optional"] = json!(field.optional);
            out
        }).collect::<Vec<_>>()
    })
}

fn issues_json(issues: Vec<ValidationIssue>) -> Value {
    serde_json::to_value(issues).unwrap()
}

#[derive(Clone, Debug, Default)]
struct KindsConfig {
    // The types parse_config read, as Branches reads its kinds' types.
    kinds: Value,
    // A value afterConfigChange checks against a type, when given.
    probe: Option<(String, Value)>,
}

// test.Kinds reads the types its config names through ConfigTarget.types
// and keeps each one's fields in its parsed config; its operations check a
// value against a type with validate.
#[derive(Clone, Default)]
struct Kinds {
    // What each parse_config was given: whether other schemas were in
    // reach, and the type names.
    targets: Seen,
    // What afterConfigChange checked with the version being published.
    published: Seen,
    // Types parse_config also reads while other schemas are in reach.
    with_schemas: Arc<Mutex<Vec<String>>>,
}

static KINDS: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
    let check = json!({"type": "object", "additionalProperties": false, "required": ["type", "value"], "properties": {"type": {"type": "string"}, "value": {}}});
    let names = json!({"type": "array", "items": {"type": "string"}});
    json!({
        "name": "test.Kinds",
        "configSchema": {"type": "object", "additionalProperties": false, "properties": {
            "types": names, "withSchemas": names, "withoutSchemas": names, "probe": check
        }},
        "operations": [
            {"name": "kinds", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true},
            {"name": "check", "paramsSchema": check, "resultSchema": true},
            {"name": "checkSchema", "scope": "schema", "paramsSchema": check, "resultSchema": true}
        ]
    })
    .to_string()
});

impl Behavior for Kinds {
    type Config = KindsConfig;

    fn declaration(&self) -> &str {
        &KINDS
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<KindsConfig> {
        let with = target.schemas().is_some();
        self.targets
            .lock()
            .unwrap()
            .push(json!({"schemas": with, "names": target.types().names()}));
        let mut reading = strings(raw.get("types"));
        reading.extend(strings(raw.get(if with {
            "withSchemas"
        } else {
            "withoutSchemas"
        })));
        if with {
            reading.extend(self.with_schemas.lock().unwrap().iter().cloned());
        }
        let mut read = Map::new();
        for name in reading {
            let Some(found) = target.types().get(&name) else {
                return Err(Error::config(format!(
                    "{name} is not a type of {} besides {} (its types: {})",
                    target.schema(),
                    target.type_name(),
                    target.types().names().join(", ")
                )));
            };
            read.insert(name, type_json(&found));
        }
        Ok(KindsConfig {
            kinds: Value::Object(read),
            probe: raw.get("probe").map(|probe| {
                (
                    probe["type"].as_str().unwrap_or_default().to_owned(),
                    probe["value"].clone(),
                )
            }),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            after_config_change: Some(|kinds, cx| {
                if let Some((type_name, value)) =
                    cx.config().and_then(|config| config.probe.as_ref())
                {
                    let issues = cx.validate(type_name, value)?;
                    kinds.published.lock().unwrap().push(issues_json(issues));
                }
                Ok(())
            }),
            operations: vec![
                ("kinds", |_, cx, _| Ok(cx.config().kinds.clone())),
                ("check", |_, cx, params| {
                    Ok(issues_json(cx.validate(
                        params["type"].as_str().unwrap_or_default(),
                        &params["value"],
                    )?))
                }),
            ],
            schema_operations: vec![("checkSchema", |_, cx, params| {
                Ok(issues_json(cx.validate(
                    params["type"].as_str().unwrap_or_default(),
                    &params["value"],
                )?))
            })],
            ..Hooks::default()
        }
    }
}

// A Kitchen whose instance type reaches Meta through a field, beside types
// no field reaches: a Recipe with a field of every kind and its Steps, a
// Loose type with a map field, an Odd one with a field of a type the
// document lacks, a Wrapper around a Loose, and a Spare.
fn kitchen(config: Value) -> Value {
    let mut document = schema_document(
        "Kitchen",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "meta", "typeRef": {"name": "Meta"}}
        ]),
        json!({
            "enums": {"Unit": {"name": "Unit", "values": [{"name": "GRAM", "serializedAs": "g"}, {"name": "CUP", "serializedAs": "cup"}]}},
            "types": {
                "Meta": {"name": "Meta", "role": "EmbeddedStruct", "fields": [{"name": "source", "typeRef": {"name": "string"}, "required": true}]},
                "Recipe": {"name": "Recipe", "role": "EmbeddedStruct", "fields": [
                    {"name": "title", "typeRef": {"name": "string"}, "required": true},
                    {"name": "servings", "typeRef": {"name": "Int"}, "jsonTag": "serves"},
                    {"name": "contact", "typeRef": {"name": "Contact.Email"}},
                    {"name": "unit", "typeRef": {"name": "Unit"}},
                    {"name": "steps", "typeRef": {"name": "Step", "isArray": true}},
                    {"name": "grid", "typeRef": {"name": "number", "isArray": true, "isArrayOfArrays": true}}
                ]},
                "Step": {"name": "Step", "role": "EmbeddedStruct", "fields": [
                    {"name": "name", "typeRef": {"name": "string"}, "required": true},
                    {"name": "minutes", "typeRef": {"name": "number"}, "validateMin": 1}
                ]},
                "Loose": {"name": "Loose", "role": "EmbeddedStruct", "fields": [{"name": "extras", "typeRef": {"name": "string", "isMap": true}}]},
                "Odd": {"name": "Odd", "role": "EmbeddedStruct", "fields": [{"name": "part", "typeRef": {"name": "Nope"}}]},
                "Wrapper": {"name": "Wrapper", "role": "EmbeddedStruct", "fields": [{"name": "inner", "typeRef": {"name": "Loose"}}]},
                "Spare": {"name": "Spare", "role": "EmbeddedStruct", "fields": [{"name": "note", "typeRef": {"name": "string"}}]}
            }
        }),
    );
    document["types"]["Kitchen"]["behaviors"] = json!([{"name": "test.Kinds", "config": config}]);
    document
}

fn open_kinds(target: &Target) -> (Engine, Kinds) {
    let kinds = Kinds::default();
    (target.open(BehaviorSet::new().with(kinds.clone())), kinds)
}

fn define(engine: &Engine, document: &Value) -> Result<superschematic_engine::SchemaRecord> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
}

fn publish_kitchen(engine: &Engine, document: &Value) {
    publish(engine, document, None);
}

fn make_kitchen(engine: &Engine) {
    engine
        .instances()
        .create(
            &alice(),
            "Kitchen",
            &json!({"title": "Home"}),
            CreateOptions {
                id: Some("k1"),
                ..Default::default()
            },
        )
        .unwrap();
}

fn kitchen_call(engine: &Engine, operation: &str, params: Value) -> Result<Value> {
    engine.instances().invoke(
        &alice(),
        "Kitchen",
        "k1",
        operation,
        &params,
        WriteOptions::default(),
    )
}

const CONFIG_AT: &str = "/types/Kitchen/behaviors/0/config";

#[test]
fn parse_config_reads_each_other_types_fields_json_key_type_kind_list_depth_and_whether_it_is_optional(
) {
    for target in targets("parse_config_reads_each_other_types_fields") {
        let (engine, kinds) = open_kinds(&target);
        publish_kitchen(&engine, &kitchen(json!({"types": ["Recipe"]})));
        make_kitchen(&engine);
        assert_eq!(
            kitchen_call(&engine, "kinds", json!({})).unwrap(),
            json!({"Recipe": {"name": "Recipe", "fields": [
                {"key": "title", "type": "string", "kind": "primitive", "depth": 0, "optional": false},
                {"key": "serves", "type": "Int", "kind": "primitive", "depth": 0, "optional": true},
                {"key": "contact", "type": "Contact.Email", "kind": "scalar", "jsonType": "string", "depth": 0, "optional": true},
                {"key": "unit", "type": "Unit", "kind": "enum", "depth": 0, "optional": true},
                {"key": "steps", "type": "Step", "kind": "type", "depth": 1, "optional": true},
                {"key": "grid", "type": "number", "kind": "primitive", "depth": 2, "optional": true}
            ]}})
        );
        // The names are the types besides the instance type, sorted. A
        // version composed again to run it has no other schema in reach.
        let seen = kinds.targets.lock().unwrap();
        assert_eq!(
            *seen.last().unwrap(),
            json!({"schemas": false, "names": ["Loose", "Meta", "Odd", "Recipe", "Spare", "Step", "Wrapper"]})
        );
        assert!(seen.iter().any(|target| target["schemas"] == true));
    }
}

#[test]
fn a_scalar_field_carries_the_json_type_of_the_scalars_values_and_a_catalog_scalars_is_the_catalogs(
) {
    for target in targets("a_scalar_field_carries_the_json_type") {
        let (engine, _) = open_kinds(&target);
        let mut document = kitchen(json!({"types": ["Measure"]}));
        document["scalars"] = json!({
            "Grams": {"name": "Grams", "languagePrimitive": "number", "typeMappings": {"json_schema": "integer"}},
            "Ratio": {"name": "Ratio", "languagePrimitive": "number"},
            "Flag": {"name": "Flag", "languagePrimitive": "boolean"},
            "Code": {"name": "Code", "languagePrimitive": "string", "pattern": "^[A-Z]+$"},
            // The form format --to=json writes declares a catalog scalar by
            // its name and primitive; the catalog's row decides it.
            "Identity.UUID": {"name": "Identity.UUID", "languagePrimitive": "object"}
        });
        document["types"]["Measure"] = json!({"name": "Measure", "role": "EmbeddedStruct", "fields": [
            {"name": "grams", "typeRef": {"name": "Grams"}},
            {"name": "ratio", "typeRef": {"name": "Ratio", "isArray": true}},
            {"name": "flag", "typeRef": {"name": "Flag"}},
            {"name": "code", "typeRef": {"name": "Code"}},
            {"name": "id", "typeRef": {"name": "Identity.UUID"}},
            {"name": "extra", "typeRef": {"name": "Generic.JSON"}}
        ]});
        publish_kitchen(&engine, &document);
        make_kitchen(&engine);
        let read = kitchen_call(&engine, "kinds", json!({})).unwrap();
        assert_eq!(
            read["Measure"]["fields"]
                .as_array()
                .unwrap()
                .iter()
                .map(|field| json!([field["key"], field["jsonType"]]))
                .collect::<Vec<_>>(),
            [
                json!(["grams", "integer"]),
                json!(["ratio", "number"]),
                json!(["flag", "boolean"]),
                json!(["code", "string"]),
                json!(["id", "string"]),
                json!(["extra", "any"]),
            ]
        );
    }
}

#[test]
fn the_instance_type_an_enum_and_a_name_the_document_lacks_are_no_type_to_read() {
    for target in targets("the_instance_type_an_enum_and_a_name") {
        let (engine, _) = open_kinds(&target);
        for name in ["Kitchen", "Unit", "Nope"] {
            let refused = refusal(define(&engine, &kitchen(json!({ "types": [name] }))));
            let issues = issues_of(&refused);
            assert_eq!(
                issues
                    .iter()
                    .map(|(path, _)| path.as_str())
                    .collect::<Vec<_>>(),
                [CONFIG_AT]
            );
            assert!(
                issues[0].1.contains(&format!(
                    "behavior test.Kinds config: {name} is not a type of Kitchen besides Kitchen"
                )),
                "{}",
                issues[0].1
            );
        }
    }
}

#[test]
fn a_type_parse_config_reads_gets_the_document_checks_and_so_do_the_types_it_reaches() {
    for target in targets("a_type_parse_config_reads_gets_the_document_checks") {
        let (engine, _) = open_kinds(&target);
        // Loose's map and Odd's missing type pass while nothing reads them.
        define(&engine, &kitchen(json!({"types": ["Recipe", "Spare"]}))).unwrap();
        assert_eq!(
            issues_of(&refusal(define(
                &engine,
                &kitchen(json!({"types": ["Loose"]}))
            ))),
            [(
                "/types/Loose/fields/0/typeRef".to_owned(),
                "field Loose.extras is a map, which the schema runtime does not validate"
                    .to_owned()
            )]
        );
        assert_eq!(
            issues_of(&refusal(define(&engine, &kitchen(json!({"types": ["Odd"]}))))),
            [(
                "/types/Odd/fields/0/typeRef".to_owned(),
                "field Odd.part has type Nope, which is not a primitive, a scalar, or an enum or type of this schema".to_owned()
            )]
        );
        // A type read reaches Loose through its field.
        assert_eq!(
            issues_of(&refusal(define(
                &engine,
                &kitchen(json!({"types": ["Wrapper"]}))
            )))
            .into_iter()
            .map(|(path, _)| path)
            .collect::<Vec<_>>(),
            ["/types/Loose/fields/0/typeRef"]
        );
    }
}

#[test]
fn what_parse_config_reads_may_not_depend_on_the_other_schemas() {
    for target in targets("what_parse_config_reads_may_not_depend") {
        let (engine, kinds) = open_kinds(&target);
        for (config, reads) in [
            (
                json!({"types": ["Spare"], "withSchemas": ["Recipe"]}),
                "Recipe through ConfigTarget.types only while other schemas",
            ),
            (
                json!({"withoutSchemas": ["Recipe", "Step"]}),
                "Recipe, Step through ConfigTarget.types only when no other schemas",
            ),
            // Reads that differ both ways name each side.
            (
                json!({"withSchemas": ["Recipe"], "withoutSchemas": ["Step"]}),
                "Recipe through ConfigTarget.types only while other schemas are in reach (ConfigTarget.schemas), and Step only when none are; ",
            ),
        ] {
            let issues = issues_of(&refusal(define(&engine, &kitchen(config))));
            assert_eq!(issues.iter().map(|(path, _)| path.as_str()).collect::<Vec<_>>(), [CONFIG_AT]);
            let expected = format!("type Kitchen: behavior test.Kinds config: parseConfig reads {reads}");
            assert!(issues[0].1.starts_with(&expected), "{}", issues[0].1);
        }
        // The same types with and without them stand.
        define(
            &engine,
            &kitchen(
                json!({"types": ["Recipe"], "withSchemas": ["Spare"], "withoutSchemas": ["Spare"]}),
            ),
        )
        .unwrap();
        // A publish composes the draft again, with the other schemas in
        // reach and without, and refuses it as a define does.
        define(&engine, &kitchen(json!({"types": ["Recipe"]}))).unwrap();
        *kinds.with_schemas.lock().unwrap() = vec!["Spare".to_owned()];
        let at_publish = issues_of(&refusal(engine.schemas().publish(
            &alice(),
            "Kitchen",
            SchemaTarget::default(),
        )));
        assert_eq!(
            at_publish
                .iter()
                .map(|(path, _)| path.as_str())
                .collect::<Vec<_>>(),
            [CONFIG_AT]
        );
        assert!(at_publish[0].1.contains(
            "parseConfig reads Spare through ConfigTarget.types only while other schemas"
        ));
        assert!(engine
            .schemas()
            .live(&alice(), "Kitchen", SchemaTarget::default())
            .unwrap()
            .is_none());
        kinds.with_schemas.lock().unwrap().clear();
        engine
            .schemas()
            .publish(&alice(), "Kitchen", SchemaTarget::default())
            .unwrap();
    }
}

// next is the Kitchen at a config, changed.
fn next(config: Value, change: impl FnOnce(&mut Value)) -> Value {
    let mut document = kitchen(config);
    change(&mut document);
    document
}

fn changes(engine: &Engine, document: &Value) -> Vec<(String, String)> {
    let refused = refusal(define(engine, document));
    assert!(
        matches!(refused.kind(), EngineErrorKind::IncompatibleChange { .. }),
        "{refused}"
    );
    refused
        .changes()
        .iter()
        .map(|change| (change.path.clone(), change.message.clone()))
        .collect()
}

fn field<'d>(document: &'d mut Value, type_name: &str, name: &str) -> &'d mut Value {
    document["types"][type_name]["fields"]
        .as_array_mut()
        .unwrap()
        .iter_mut()
        .find(|field| field["name"] == name)
        .unwrap()
}

fn push_field(document: &mut Value, type_name: &str, field: Value) {
    document["types"][type_name]["fields"]
        .as_array_mut()
        .unwrap()
        .push(field);
}

#[test]
fn a_new_version_keeps_the_fields_of_a_type_the_live_version_read_and_of_the_types_it_reaches() {
    for target in targets("a_new_version_keeps_the_fields_of_a_type_read") {
        let (engine, _) = open_kinds(&target);
        let recipe = || json!({"types": ["Recipe"]});
        publish_kitchen(&engine, &kitchen(recipe()));
        assert_eq!(
            changes(
                &engine,
                &next(recipe(), |doc| push_field(
                    doc,
                    "Recipe",
                    json!({"name": "cuisine", "typeRef": {"name": "string"}, "required": true})
                ))
            ),
            [(
                "Recipe.cuisine".to_owned(),
                "field Recipe.cuisine is added as required".to_owned()
            )]
        );
        assert_eq!(
            changes(
                &engine,
                &next(recipe(), |doc| field(doc, "Step", "minutes")
                    ["validateMin"] = json!(2))
            ),
            [(
                "Step.minutes".to_owned(),
                "field Step.minutes raises min from 1 to 2".to_owned()
            )]
        );
        // An optional field is a compatible change, as on the instance type.
        define(
            &engine,
            &next(recipe(), |doc| {
                push_field(
                    doc,
                    "Recipe",
                    json!({"name": "cuisine", "typeRef": {"name": "string"}}),
                )
            }),
        )
        .unwrap();
    }
}

#[test]
fn the_live_version_holds_the_types_it_read_even_where_the_new_version_reads_them_no_longer() {
    for target in targets("the_live_version_holds_the_types_it_read") {
        let (engine, _) = open_kinds(&target);
        publish_kitchen(&engine, &kitchen(json!({"types": ["Recipe"]})));
        assert_eq!(
            changes(
                &engine,
                &next(json!({"types": []}), |doc| {
                    doc["types"].as_object_mut().unwrap().remove("Recipe");
                })
            ),
            [("Recipe".to_owned(), "type Recipe is removed".to_owned())]
        );
        assert_eq!(
            changes(
                &engine,
                &next(json!({"types": []}), |doc| field(doc, "Recipe", "title")
                    ["typeRef"] =
                    json!({"name": "number"}))
            ),
            [(
                "Recipe.title".to_owned(),
                "field Recipe.title changes type from string to number".to_owned()
            )]
        );
    }
}

#[test]
fn a_type_no_live_behavior_read_changes_freely_even_one_the_new_version_starts_to_read() {
    for target in targets("a_type_no_live_behavior_read_changes_freely") {
        let (engine, _) = open_kinds(&target);
        publish_kitchen(&engine, &kitchen(json!({"types": []})));
        define(
            &engine,
            &next(json!({"types": []}), |doc| {
                push_field(
                    doc,
                    "Recipe",
                    json!({"name": "cuisine", "typeRef": {"name": "string"}, "required": true}),
                )
            }),
        )
        .unwrap();
        define(
            &engine,
            &next(json!({"types": ["Recipe"]}), |doc| {
                field(doc, "Recipe", "title")["typeRef"] = json!({"name": "number"})
            }),
        )
        .unwrap();
        define(
            &engine,
            &next(json!({"types": []}), |doc| {
                doc["types"].as_object_mut().unwrap().remove("Spare");
            }),
        )
        .unwrap();
    }
}

fn rules(issues: &Value) -> Vec<(String, String)> {
    let mut out: Vec<(String, String)> = issues
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
    out.sort();
    out
}

fn pairs(list: &[(&str, &str)]) -> Vec<(String, String)> {
    list.iter()
        .map(|(path, rule)| ((*path).to_owned(), (*rule).to_owned()))
        .collect()
}

#[test]
fn validate_holds_a_value_to_a_type_as_a_nested_value() {
    for target in targets("validate_holds_a_value_to_a_type") {
        let (engine, _) = open_kinds(&target);
        publish_kitchen(&engine, &kitchen(json!({"types": ["Recipe"]})));
        make_kitchen(&engine);
        let check = |type_name: &str, value: Value| {
            kitchen_call(&engine, "check", json!({"type": type_name, "value": value})).unwrap()
        };
        assert_eq!(
            check(
                "Recipe",
                json!({"title": "Bread", "serves": 4, "contact": "baker@example.com", "unit": "g", "steps": [{"name": "knead", "minutes": 10}], "grid": [[1, 2], [3]]})
            ),
            json!([])
        );
        assert_eq!(
            rules(&check(
                "Recipe",
                json!({
                    "title": 7,
                    "serves": "four",
                    "contact": "not an email",
                    "unit": "ounce",
                    "steps": [{"minutes": 0, "extra": true}, "knead"],
                    "grid": [1],
                    "other": 1
                })
            )),
            pairs(&[
                ("contact", "pattern"),
                ("grid[0]", "type"),
                ("other", "unknown"),
                ("serves", "type"),
                ("steps[0].extra", "unknown"),
                ("steps[0].minutes", "min"),
                ("steps[0].name", "required"),
                ("steps[1]", "type"),
                ("title", "type"),
                ("unit", "enum"),
            ])
        );
        assert_eq!(
            check("Recipe", json!("Bread")),
            json!([{"path": "", "rule": "type", "message": "a Recipe is a JSON object"}])
        );
        assert_eq!(
            rules(&check("Step", json!({"name": "rest", "later": {"at": 1}}))),
            pairs(&[("later", "unknown")])
        );
    }
}

#[test]
fn validate_reaches_a_type_its_fields_reach_one_parse_config_read_and_the_types_those_reach_only() {
    for target in targets("validate_reaches_a_type_its_fields_reach") {
        let (engine, _) = open_kinds(&target);
        publish_kitchen(&engine, &kitchen(json!({"types": ["Recipe"]})));
        make_kitchen(&engine);
        let check = |type_name: &str| {
            kitchen_call(&engine, "check", json!({"type": type_name, "value": {}}))
        };
        assert_eq!(
            rules(&check("Meta").unwrap()),
            pairs(&[("source", "required")])
        );
        for type_name in ["Spare", "Kitchen", "Unit", "Nope"] {
            let (_, message) = defect(check(type_name));
            assert!(
                message.starts_with(&format!("behavior test.Kinds: validate: {type_name} is not a type the version checks besides Kitchen"))
                    && message.ends_with("(Meta, Recipe, Step)"),
                "{message}"
            );
        }
    }
}

#[test]
fn a_schema_level_operation_and_after_config_change_validate_too_the_latter_with_the_version_being_published(
) {
    for target in targets("a_schema_level_operation_and_after_config_change_validate") {
        let (engine, kinds) = open_kinds(&target);
        publish_kitchen(
            &engine,
            &kitchen(
                json!({"types": ["Recipe"], "probe": {"type": "Recipe", "value": {"title": 1}}}),
            ),
        );
        assert_eq!(
            rules(&kinds.published.lock().unwrap()[0]),
            pairs(&[("title", "type")])
        );
        assert_eq!(
            rules(
                &engine
                    .instances()
                    .invoke_schema(
                        &alice(),
                        "Kitchen",
                        "checkSchema",
                        &json!({"type": "Step", "value": {"name": 1}}),
                        None
                    )
                    .unwrap()
            ),
            pairs(&[("name", "type")])
        );
        // The next version makes Recipe's cuisine an optional field, which
        // the hook's value may then hold.
        let mut document = kitchen(
            json!({"types": ["Recipe"], "probe": {"type": "Recipe", "value": {"title": "Bread", "cuisine": "rye"}}}),
        );
        push_field(
            &mut document,
            "Recipe",
            json!({"name": "cuisine", "typeRef": {"name": "string"}}),
        );
        publish_kitchen(&engine, &document);
        assert_eq!(*kinds.published.lock().unwrap().last().unwrap(), json!([]));
    }
}

#[derive(Clone, Debug, PartialEq)]
struct RecorderConfig {
    tag: String,
}

type PublishHook = fn(&PublishCx<'_, RecorderConfig>) -> Result<()>;

// test.Recorder records each afterConfigChange and writes what it visits to
// its own table; its hook, when given, runs after that.
#[derive(Clone)]
struct Recorder {
    calls: Seen,
    hook: Option<PublishHook>,
}

fn config_json(config: Option<&RecorderConfig>) -> Value {
    config.map_or(Value::Null, |config| json!({"tag": config.tag}))
}

impl Behavior for Recorder {
    type Config = RecorderConfig;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Recorder", "configSchema": {"type": "object", "additionalProperties": false, "properties": {"tag": {"type": "string"}}}}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<RecorderConfig> {
        Ok(RecorderConfig {
            tag: raw
                .get("tag")
                .and_then(Value::as_str)
                .unwrap_or("none")
                .to_owned(),
        })
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, _, _, _| Ok(None)),
            migrations: vec![BehaviorMigration::new(1, "seen").table(
                TableSpec::new("seen")
                    .row_id("position")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("title", ColumnSpec::text())
                    .column("tag", ColumnSpec::text()),
            )],
            after_config_change: Some(|recorder, cx| {
                let mut visited = Vec::new();
                let table = cx.sql().table("seen")?;
                cx.each_instance(|instance| {
                    visited.push(json!(instance.id));
                    cx.sql().run(
                        format!(
                            "INSERT INTO {table} (namespace, id, title, tag) VALUES (?, ?, ?, ?)"
                        ),
                        &[
                            cx.namespace().into(),
                            instance.id.as_str().into(),
                            instance
                                .data
                                .get("title")
                                .and_then(Value::as_str)
                                .unwrap_or_default()
                                .into(),
                            cx.config()
                                .map_or(SqlValue::Null, |config| config.tag.as_str().into()),
                        ],
                    )?;
                    Ok(())
                })?;
                recorder.calls.lock().unwrap().push(json!({
                    "namespace": cx.namespace(),
                    "schema": cx.schema(),
                    "version": cx.version(),
                    "before": config_json(cx.before()),
                    "config": config_json(cx.config()),
                    "visited": visited,
                }));
                match recorder.hook {
                    Some(hook) => hook(cx),
                    None => Ok(()),
                }
            }),
            ..Hooks::default()
        }
    }
}

fn recording(
    target: &Target,
    hook: Option<PublishHook>,
    namespaces: NamespaceOptions,
) -> (Engine, Seen) {
    let calls: Seen = Arc::default();
    let engine = target.open_with(
        BehaviorSet::new().with(Recorder {
            calls: Arc::clone(&calls),
            hook,
        }),
        |options| options.namespaces = namespaces,
    );
    (engine, calls)
}

fn publish_recorder(
    engine: &Engine,
    behaviors: Value,
    namespace: Option<&str>,
    description: Option<&str>,
) -> i64 {
    let mut document = item_document(behaviors, json!([]));
    if let Some(description) = description {
        document["description"] = json!(description);
    }
    engine
        .schemas()
        .define(
            &alice(),
            &document,
            DefineOptions {
                namespace,
                source: None,
            },
        )
        .unwrap();
    engine
        .schemas()
        .publish(&alice(), "Item", SchemaTarget { namespace })
        .unwrap()
        .version
}

fn create_in(engine: &Engine, id: &str, title: &str, namespace: Option<&str>) {
    engine
        .instances()
        .create(
            &alice(),
            "Item",
            &json!({ "title": title }),
            CreateOptions {
                id: Some(id),
                namespace,
                ..Default::default()
            },
        )
        .unwrap();
}

#[test]
fn after_config_change_runs_for_a_first_version_a_changed_config_and_a_removal_and_not_for_an_unchanged_one(
) {
    for target in targets("after_config_change_runs_for_a_first_version") {
        let (engine, calls) = recording(
            &target,
            None,
            NamespaceOptions {
                names: vec!["east".into()],
                shared: None,
            },
        );
        let recorder = || json!([{"name": "test.Recorder", "config": {"tag": "a"}}]);
        assert_eq!(publish_recorder(&engine, recorder(), None, None), 1);
        assert_eq!(
            *calls.lock().unwrap(),
            [
                json!({"namespace": "default", "schema": "Item", "version": 1, "before": null, "config": {"tag": "a"}, "visited": []})
            ]
        );
        create_in(&engine, "i1", "Desk", None);
        create_in(&engine, "i2", "Lamp", None);
        assert_eq!(
            publish_recorder(&engine, recorder(), None, Some("the same config")),
            2
        );
        assert_eq!(
            calls.lock().unwrap().len(),
            1,
            "an unchanged config runs nothing"
        );
        assert_eq!(
            publish_recorder(
                &engine,
                json!([{"name": "test.Recorder", "config": {"tag": "b"}}]),
                None,
                None
            ),
            3
        );
        assert_eq!(publish_recorder(&engine, json!([]), None, None), 4);
        assert_eq!(
            publish_recorder(&engine, json!([{"name": "test.Recorder"}]), None, None),
            5
        );
        assert_eq!(
            calls.lock().unwrap()[1..],
            [
                json!({"namespace": "default", "schema": "Item", "version": 3, "before": {"tag": "a"}, "config": {"tag": "b"}, "visited": ["i1", "i2"]}),
                json!({"namespace": "default", "schema": "Item", "version": 4, "before": {"tag": "b"}, "config": null, "visited": ["i1", "i2"]}),
                json!({"namespace": "default", "schema": "Item", "version": 5, "before": null, "config": {"tag": "none"}, "visited": ["i1", "i2"]}),
            ]
        );
        // east's own Item is its own: a publish there covers east alone.
        publish_recorder(&engine, recorder(), Some("east"), None);
        assert_eq!(
            *calls.lock().unwrap().last().unwrap(),
            json!({"namespace": "east", "schema": "Item", "version": 1, "before": null, "config": {"tag": "a"}, "visited": []})
        );
    }
}

#[test]
fn after_config_change_covers_every_namespace_for_a_schema_of_the_shared_namespace_in_creation_order(
) {
    for target in targets("after_config_change_covers_every_namespace") {
        let (engine, calls) = recording(
            &target,
            None,
            NamespaceOptions {
                names: vec!["shared".into(), "east".into()],
                shared: Some("shared".into()),
            },
        );
        publish_recorder(&engine, json!([]), Some("shared"), None);
        let ids: Vec<String> = (0..501).map(|n| format!("e{n:03}")).collect();
        for id in ids.iter().rev() {
            create_in(&engine, id, id, Some("east"));
        }
        create_in(&engine, "s1", "Shared", Some("shared"));
        publish_recorder(
            &engine,
            json!([{"name": "test.Recorder", "config": {"tag": "x"}}]),
            Some("shared"),
            None,
        );
        let calls = calls.lock().unwrap();
        assert_eq!(
            calls
                .iter()
                .map(|call| json!([
                    call["namespace"],
                    call["version"],
                    call["visited"].as_array().unwrap().len()
                ]))
                .collect::<Vec<_>>(),
            [
                json!(["default", 2, 0]),
                json!(["shared", 2, 1]),
                json!(["east", 2, 501])
            ]
        );
        assert_eq!(
            calls[2]["visited"],
            json!(ids.iter().rev().collect::<Vec<_>>()),
            "every instance once, 500 at a time, in creation order"
        );
        assert_eq!(
            texts(&engine, "SELECT COUNT(*) FROM bhv_test_recorder__seen", &[]),
            ["502"]
        );
    }
}

#[test]
fn after_config_change_reaches_its_own_tables_only_and_a_failure_refuses_the_publish_and_takes_its_writes_back(
) {
    let hooks: [(PublishHook, &str); 2] = [
        (
            |cx| {
                cx.sql()
                    .run("DELETE FROM engine_instances", &[])
                    .map(|_| ())
            },
            "SQL refused: it names engine_instances",
        ),
        (
            |_| Err(Error::Internal("the rebuild failed".into())),
            "the rebuild failed",
        ),
    ];
    for (hook, message) in hooks {
        for target in targets("after_config_change_reaches_its_own_tables_only") {
            let (engine, calls) = recording(&target, Some(hook), NamespaceOptions::default());
            publish_recorder(&engine, json!([]), None, None);
            create_in(&engine, "i1", "Desk", None);
            engine
                .schemas()
                .define(
                    &alice(),
                    item_document(json!([{"name": "test.Recorder"}]), json!([])),
                    DefineOptions::default(),
                )
                .unwrap();
            let error = engine
                .schemas()
                .publish(&alice(), "Item", SchemaTarget::default())
                .unwrap_err();
            assert!(error.to_string().contains(message), "{error}");
            assert_eq!(calls.lock().unwrap().len(), 1, "the hook ran");
            assert_eq!(
                engine
                    .schemas()
                    .live(&alice(), "Item", SchemaTarget::default())
                    .unwrap()
                    .unwrap()
                    .version,
                Some(1),
                "no version was minted"
            );
            assert_eq!(
                tables_of(&engine, "bhv_test_recorder__"),
                Vec::<String>::new(),
                "its storage was rolled back with what it wrote"
            );
            assert_eq!(
                engine
                    .instances()
                    .get(&alice(), "Item", "i1", Default::default())
                    .unwrap()
                    .unwrap()
                    .data,
                *json!({"title": "Desk"}).as_object().unwrap()
            );
        }
    }
}

// test.Probe notes the field schemas parse_config is given.
struct Probe {
    seen: Seen,
}

impl Behavior for Probe {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "test.Probe"}"#
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<Value> {
        self.seen
            .lock()
            .unwrap()
            .push(Value::Object(target.field_schemas().clone()));
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks::default()
    }
}

#[test]
fn parse_config_gets_the_json_schema_of_each_of_the_types_own_fields_by_json_key() {
    for target in targets("parse_config_gets_the_json_schema_of_each_field") {
        let seen: Seen = Arc::default();
        let engine = target.open(BehaviorSet::new().with(Probe {
            seen: Arc::clone(&seen),
        }));
        engine
            .schemas()
            .define(
                &alice(),
                item_document(
                    json!([{"name": "test.Probe"}]),
                    json!([
                        {"name": "note", "typeRef": {"name": "string"}, "jsonTag": "memo"},
                        {"name": "email", "typeRef": {"name": "Contact.Email"}},
                        {"name": "count", "typeRef": {"name": "Generic.Int64"}},
                        {"name": "tags", "typeRef": {"name": "string", "isArray": true}}
                    ]),
                ),
                DefineOptions::default(),
            )
            .unwrap();
        let seen = seen.lock().unwrap();
        let schemas = seen.last().unwrap().as_object().unwrap();
        assert_eq!(
            schemas.keys().map(String::as_str).collect::<Vec<_>>(),
            ["title", "memo", "email", "count", "tags"]
        );
        assert_eq!(schemas["title"]["type"], "string");
        assert_eq!(schemas["memo"]["type"], json!(["string", "null"]));
        assert_eq!(
            (
                schemas["email"]["type"].clone(),
                schemas["email"]["format"].clone()
            ),
            (json!(["string", "null"]), json!("email"))
        );
        assert_eq!(schemas["count"]["type"], json!(["integer", "null"]));
        assert_eq!(schemas["tags"]["type"], json!(["array", "null"]));
    }
}

// test.Gate opens an instance for a caller who holds the permission its
// config names; its guard asks can() and vetoes everyone else.
struct Gate;

impl Behavior for Gate {
    type Config = String;

    fn declaration(&self) -> &str {
        r#"{
            "name": "test.Gate",
            "configSchema": {"type": "object", "additionalProperties": false, "required": ["permission"], "properties": {"permission": {"type": "string"}}},
            "fields": [{"name": "opened"}],
            "operations": [
                {"name": "open", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": {"type": "boolean"}, "writes": true},
                {"name": "askEmpty", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": {"type": "boolean"}}
            ]
        }"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<String> {
        Ok(raw["permission"].as_str().unwrap_or_default().to_owned())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "opened")
                .column("opened", ColumnSpec::integer().not_null().default(0))],
            guard: Some(|_, view, request| {
                if let GuardRequest::Operation {
                    operation: "open", ..
                } = request
                {
                    if !view.can(view.config())? {
                        return Ok(Some(Veto::from(
                            format!("opening it needs {}", view.config()).as_str(),
                        )));
                    }
                }
                Ok(None)
            }),
            operations: vec![
                ("open", |_, cx, _| {
                    cx.columns().set(&[("opened", SqlValue::Integer(1))])?;
                    Ok(json!(true))
                }),
                ("askEmpty", |_, cx, _| Ok(json!(cx.can("")?))),
            ],
            fields: vec![("opened", |_, view| {
                Ok(Some(json!(view.columns().get()?.i64("opened")? == 1)))
            })],
            ..Hooks::default()
        }
    }
}

fn holding(permissions: &[&str]) -> Principal {
    Principal::new("bob", permissions.iter().copied())
}

fn gated(target: &Target, matcher: Option<Arc<dyn PermissionMatcher>>) -> Engine {
    let engine = target.open_with(BehaviorSet::new().with(Gate), |options| {
        options.permission_matcher = matcher
    });
    support::publish_item(
        &engine,
        json!([{"name": "test.Gate", "config": {"permission": "orders.approve"}}]),
    );
    engine
}

fn open_gate(engine: &Engine, who: &Principal, id: &str) -> Result<Value> {
    engine
        .instances()
        .invoke(who, "Item", id, "open", &json!({}), WriteOptions::default())
}

fn opened(engine: &Engine, id: &str) -> Value {
    engine
        .instances()
        .get(&alice(), "Item", id, Default::default())
        .unwrap()
        .unwrap()
        .behaviors["test.Gate"]["opened"]
        .clone()
}

fn gate_item(engine: &Engine, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            "Item",
            &json!({"title": "Desk"}),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .unwrap();
}

#[test]
fn the_default_matcher_is_the_http_runtimes_a_granted_permission_covers_itself_and_what_nests_under_it(
) {
    for target in targets("the_default_matcher_is_the_http_runtimes") {
        let engine = gated(&target, None);
        let cases: [(&[&str], bool); 7] = [
            (&["orders.approve"], true),
            (&["orders"], true),
            (&["reports", "orders"], true),
            (&["order"], false),
            (&["orders.approve.all"], false),
            (&["*"], false),
            (&[], false),
        ];
        for (index, (permissions, allowed)) in cases.into_iter().enumerate() {
            let held: Vec<String> = permissions
                .iter()
                .map(|&permission| permission.to_owned())
                .collect();
            assert_eq!(
                has_any_permission(&held, &["orders.approve".to_owned()]),
                allowed,
                "{permissions:?}"
            );
            let id = format!("i{index}");
            gate_item(&engine, &id);
            if allowed {
                assert_eq!(
                    open_gate(&engine, &holding(permissions), &id).unwrap(),
                    json!(true),
                    "{permissions:?}"
                );
                assert_eq!(opened(&engine, &id), true);
            } else {
                assert_eq!(
                    veto(open_gate(&engine, &holding(permissions), &id)).2,
                    "opening it needs orders.approve",
                    "{permissions:?}"
                );
                assert_eq!(opened(&engine, &id), false);
            }
        }
    }
}

/// What a matcher is asked: the permissions held, and the ones required.
type Question = (Vec<String>, Vec<String>);

// A matcher that records what it is asked, and lets root through.
#[derive(Clone, Default)]
struct Asked {
    asked: Arc<Mutex<Vec<Question>>>,
    all: bool,
}

impl PermissionMatcher for Asked {
    fn matches(&self, held: &[String], required: &[String]) -> bool {
        self.asked
            .lock()
            .unwrap()
            .push((held.to_vec(), required.to_vec()));
        self.all || held.iter().any(|permission| permission == "root")
    }
}

#[test]
fn a_deployments_matcher_decides_instead_with_the_principals_permissions_and_the_one_asked() {
    for target in targets("a_deployments_matcher_decides_instead") {
        let matcher = Asked::default();
        let engine = gated(&target, Some(Arc::new(matcher.clone())));
        gate_item(&engine, "a");
        gate_item(&engine, "b");
        assert_eq!(
            open_gate(&engine, &holding(&["root"]), "a").unwrap(),
            json!(true)
        );
        veto(open_gate(&engine, &holding(&["orders.approve"]), "b"));
        let strings = |list: &[&str]| list.iter().map(|&item| item.to_owned()).collect::<Vec<_>>();
        assert_eq!(
            *matcher.asked.lock().unwrap(),
            [
                (strings(&["root"]), strings(&["orders.approve"])),
                (strings(&["orders.approve"]), strings(&["orders.approve"])),
            ]
        );
    }
}

#[test]
fn a_check_with_no_permission_is_a_behavior_error() {
    for target in targets("a_check_with_no_permission") {
        let engine = gated(&target, None);
        gate_item(&engine, "a");
        assert!(defect(engine.instances().invoke(
            &alice(),
            "Item",
            "a",
            "askEmpty",
            &json!({}),
            WriteOptions::default()
        ))
        .1
        .contains("can() takes a permission"));
    }
}

#[test]
fn a_service_standing_in_holds_no_permission_whatever_the_matcher_says_and_one_acting_for_an_end_user_holds_the_end_users(
) {
    for target in targets("a_service_standing_in_holds_no_permission") {
        let generous = Asked {
            all: true,
            ..Asked::default()
        };
        let engine = gated(&target, Some(Arc::new(generous.clone())));
        gate_item(&engine, "a");
        let worker = Principal::service("worker", &[], "sa-1");
        assert_eq!(
            veto(open_gate(&engine, &worker, "a")).2,
            "opening it needs orders.approve"
        );
        assert!(
            generous.asked.lock().unwrap().is_empty(),
            "the matcher is not asked for a service standing in"
        );

        // The same storage, with the default matcher.
        drop(engine);
        let engine = gated(&target, None);
        gate_item(&engine, "c");
        gate_item(&engine, "d");
        let service = PrincipalService {
            deployable: "worker".into(),
            serves: Vec::new(),
            subject: "sa-1".into(),
            stands_in: false,
        };
        assert_eq!(
            open_gate(
                &engine,
                &holding(&["orders"]).with_service(service.clone()),
                "c"
            )
            .unwrap(),
            json!(true)
        );
        veto(open_gate(&engine, &holding(&[]).with_service(service), "d"));
    }
}
