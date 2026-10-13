//! The schema registry, the cases of `test/registry.test.ts`: what a schema
//! document must be, define and publish, the reads, and the validator of a
//! version, on SQLite and, with the variable set, on Postgres. The case of
//! `test/normalize.test.ts` that needs no behavior is here too.
//!
//! milestone 5: none of these cases needs a behavior; the TypeScript cases
//! that register one (normalize.test.ts's test.Mailer) run once behaviors
//! do.

mod support;

use std::sync::Arc;

use serde_json::{json, Value};

use superschematic_engine::json::sha256_hex;
use superschematic_engine::storage::{Lock, SqlValue};
use superschematic_engine::{
    Clock, DefineOptions, EngineErrorCode, MetaSchema, PublishResult, SchemaSummary, SchemaTarget,
    ValidateOptions,
};
use superschematic_schema_ir::schemafile::load_schema_file;
use support::{
    alice, code, issue, issues, order_document, refusal, repository_file, schema_document, targets,
    with,
};

const ANY: SchemaTarget<'static> = SchemaTarget { namespace: None };
const LIVE: ValidateOptions<'static> = ValidateOptions {
    namespace: None,
    version: None,
};

fn define_options() -> DefineOptions<'static> {
    DefineOptions::default()
}

fn published(name: &str, version: i64) -> PublishResult {
    PublishResult {
        namespace: "default".into(),
        name: name.into(),
        version,
        published: true,
    }
}

// The meta-schema of a binary that registers two behaviors, acme.Audited
// and acme.Stock, which the Go data-form reader's parity corpus uses.
fn behavior_meta_schema() -> String {
    repository_file("runtime/schema/testdata/schema_file_parity.meta-schema.json")
}

#[test]
fn define_stores_a_draft_and_publish_makes_it_version_1() {
    for target in targets("define_stores_a_draft_and_publish_makes_it_version_1") {
        let engine = target.open_with(|options| options.clock = Clock::Manual(1000));
        let schemas = engine.schemas();
        let draft = schemas
            .define(&alice(), order_document(), define_options())
            .unwrap();
        assert_eq!(draft.version, None);
        assert_eq!(draft.namespace, "default");
        assert_eq!(draft.instance_type, "Order");
        assert_eq!(draft.defined_at, 1000);
        assert_eq!(draft.published_at, None);
        assert_eq!(schemas.live(&alice(), "Order", ANY).unwrap(), None);

        engine.clock().set(2000).unwrap();
        assert_eq!(
            schemas.publish(&alice(), "Order", ANY).unwrap(),
            published("Order", 1)
        );
        let live = schemas.live(&alice(), "Order", ANY).unwrap().unwrap();
        assert_eq!(live.version, Some(1));
        assert_eq!(live.defined_at, 1000);
        assert_eq!(live.published_at, Some(2000));
        assert_eq!(live.defined_by.as_deref(), Some("alice"));
        assert_eq!(live.published_by.as_deref(), Some("alice"));
        assert_eq!(schemas.draft(&alice(), "Order", ANY).unwrap(), None);
    }
}

#[test]
fn the_stored_document_is_the_loader_canonical_form_with_its_sha256() {
    for target in targets("the_stored_document_is_the_loader_canonical_form_with_its_sha256") {
        let engine = target.open();
        let text = serde_json::to_string_pretty(&order_document()).unwrap();
        let record = engine
            .schemas()
            .define(&alice(), text.as_str(), define_options())
            .unwrap();
        let loaded = load_schema_file(&text, "schema").unwrap();
        assert_eq!(record.canonical, loaded.canonical);
        assert_eq!(record.document, loaded.document);
        assert_eq!(record.hash, sha256_hex(&loaded.canonical));
        assert_eq!(
            engine
                .schemas()
                .define(&alice(), order_document(), define_options())
                .unwrap()
                .canonical,
            loaded.canonical
        );
    }
}

#[test]
fn a_name_has_one_draft_and_the_next_define_replaces_it() {
    for target in targets("a_name_has_one_draft_and_the_next_define_replaces_it") {
        let engine = target.open();
        engine
            .schemas()
            .define(&alice(), order_document(), define_options())
            .unwrap();
        let second = with(
            &order_document(),
            "description",
            json!("Orders placed in the shop."),
        );
        engine
            .schemas()
            .define(&alice(), &second, define_options())
            .unwrap();
        let draft = engine
            .schemas()
            .draft(&alice(), "Order", ANY)
            .unwrap()
            .unwrap();
        assert_eq!(draft.document["description"], "Orders placed in the shop.");
        assert_eq!(
            support::count(engine.db(), "SELECT COUNT(*) AS n FROM engine_schemas"),
            1
        );
    }
}

#[test]
fn each_publish_mints_the_next_version_and_every_version_stays_readable() {
    for target in targets("each_publish_mints_the_next_version_and_every_version_stays_readable") {
        let engine = target.open();
        for description in ["one", "two", "three"] {
            engine
                .schemas()
                .define(
                    &alice(),
                    with(&order_document(), "description", json!(description)),
                    define_options(),
                )
                .unwrap();
            engine.schemas().publish(&alice(), "Order", ANY).unwrap();
        }
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Order", ANY)
                .unwrap()
                .unwrap()
                .version,
            Some(3)
        );
        let descriptions: Vec<Value> = [1, 2, 3]
            .iter()
            .map(|version| {
                engine
                    .schemas()
                    .version(&alice(), "Order", *version, ANY)
                    .unwrap()
                    .unwrap()
                    .document["description"]
                    .clone()
            })
            .collect();
        assert_eq!(descriptions, [json!("one"), json!("two"), json!("three")]);
        assert_eq!(
            engine.schemas().version(&alice(), "Order", 4, ANY).unwrap(),
            None
        );
    }
}

#[test]
fn publishing_a_draft_identical_to_the_live_version_mints_nothing_and_drops_the_draft() {
    for target in targets(
        "publishing_a_draft_identical_to_the_live_version_mints_nothing_and_drops_the_draft",
    ) {
        let engine = target.open();
        engine
            .schemas()
            .define(&alice(), order_document(), define_options())
            .unwrap();
        engine.schemas().publish(&alice(), "Order", ANY).unwrap();
        let spaced = serde_json::to_string_pretty(&order_document()).unwrap();
        engine
            .schemas()
            .define(&alice(), spaced, define_options())
            .unwrap();
        assert_eq!(
            engine.schemas().publish(&alice(), "Order", ANY).unwrap(),
            PublishResult {
                published: false,
                ..published("Order", 1)
            }
        );
        assert_eq!(
            engine.schemas().draft(&alice(), "Order", ANY).unwrap(),
            None
        );
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Order", ANY)
                .unwrap()
                .unwrap()
                .version,
            Some(1)
        );
    }
}

#[test]
fn publish_without_a_draft_is_not_found() {
    for target in targets("publish_without_a_draft_is_not_found") {
        let engine = target.open();
        assert_eq!(
            code(engine.schemas().publish(&alice(), "Order", ANY)),
            EngineErrorCode::NotFound
        );
        engine
            .schemas()
            .define(&alice(), order_document(), define_options())
            .unwrap();
        engine.schemas().publish(&alice(), "Order", ANY).unwrap();
        assert!(refusal(engine.schemas().publish(&alice(), "Order", ANY))
            .message()
            .contains("schema Order has no draft in namespace default"));
    }
}

#[test]
fn list_reports_each_name_with_its_live_version_and_whether_it_has_a_draft() {
    for target in targets("list_reports_each_name_with_its_live_version_and_whether_it_has_a_draft")
    {
        let engine = target.open();
        let schemas = engine.schemas();
        schemas
            .define(&alice(), order_document(), define_options())
            .unwrap();
        schemas.publish(&alice(), "Order", ANY).unwrap();
        schemas
            .define(
                &alice(),
                with(&order_document(), "description", json!("next")),
                define_options(),
            )
            .unwrap();
        schemas
            .define(
                &alice(),
                schema_document(
                    "Note",
                    json!([{"name": "body", "typeRef": {"name": "string"}}]),
                    json!({}),
                ),
                define_options(),
            )
            .unwrap();
        let summary = |name: &str, live_version: Option<i64>| SchemaSummary {
            namespace: "default".into(),
            name: name.into(),
            live_version,
            has_draft: true,
        };
        assert_eq!(
            schemas.list(&alice(), ANY).unwrap(),
            [summary("Note", None), summary("Order", Some(1))]
        );
    }
}

#[test]
fn reads_check_their_arguments() {
    for target in targets("reads_check_their_arguments") {
        let engine = target.open();
        for version in [0, -1] {
            let refused = refusal(engine.schemas().version(&alice(), "Order", version, ANY));
            assert_eq!(refused.code(), EngineErrorCode::InvalidArgument);
            assert_eq!(
                refused.message(),
                format!("a schema version is a positive integer, got {version}")
            );
        }
        assert_eq!(
            code(engine.schemas().live(&alice(), "not a name", ANY)),
            EngineErrorCode::InvalidArgument
        );
        // A principal without a subject is refused before anything else.
        let nobody = superschematic_engine::Principal::new("", Vec::<String>::new());
        assert_eq!(
            refusal(engine.schemas().list(&nobody, ANY)).message(),
            "an engine call needs a principal with a subject"
        );
    }
}

#[test]
fn the_instance_type_is_the_type_named_like_the_schema_even_among_several() {
    for target in targets("the_instance_type_is_the_type_named_like_the_schema_even_among_several")
    {
        let engine = target.open();
        assert_eq!(
            engine
                .schemas()
                .define(&alice(), order_document(), define_options())
                .unwrap()
                .instance_type,
            "Order"
        );
    }
}

#[test]
fn the_instance_type_is_the_only_type_when_none_is_named_like_the_schema() {
    for target in targets("the_instance_type_is_the_only_type_when_none_is_named_like_the_schema") {
        let engine = target.open();
        let document = json!({"kind": "General", "name": "Catalog", "types": {"Item": {"name": "Item", "role": "EmbeddedStruct"}}});
        assert_eq!(
            engine
                .schemas()
                .define(&alice(), &document, define_options())
                .unwrap()
                .instance_type,
            "Item"
        );
    }
}

#[test]
fn a_schema_without_an_instance_type_is_refused_naming_the_types_it_has() {
    for target in targets("a_schema_without_an_instance_type_is_refused_naming_the_types_it_has") {
        let engine = target.open();
        let document = json!({
            "kind": "General",
            "name": "Catalog",
            "types": {"Item": {"name": "Item", "role": "EmbeddedStruct"}, "Shelf": {"name": "Shelf", "role": "EmbeddedStruct"}}
        });
        assert_eq!(
            issues(engine.schemas().define(&alice(), &document, define_options())),
            [issue("/types", "no instance type: declare a type named Catalog or exactly one type (found: Item, Shelf)")]
        );
        assert!(engine.schemas().list(&alice(), ANY).unwrap().is_empty());
    }
}

#[test]
fn only_a_general_document_with_a_name_is_a_schema() {
    for target in targets("only_a_general_document_with_a_name_is_a_schema") {
        let engine = target.open();
        let define = |document: &Value| {
            engine
                .schemas()
                .define(&alice(), document, define_options())
        };
        assert_eq!(
            issues(define(&with(&order_document(), "kind", json!("DB")))),
            [issue(
                "/kind",
                "a schema is a document of kind General, not DB"
            )]
        );
        let enum_file =
            json!({"kind": "Enum", "name": "OrderStatus", "values": [{"name": "OPEN"}]});
        assert_eq!(
            issues(define(&enum_file))
                .into_iter()
                .map(|(_, message)| message)
                .collect::<Vec<_>>(),
            [
                "a schema is a document of kind General; this file has no kind",
                "a schema needs a name"
            ]
        );
        let mut nameless = order_document();
        nameless.as_object_mut().unwrap().remove("name");
        assert_eq!(
            issues(define(&nameless)),
            [issue("/name", "a schema needs a name")]
        );
        let spaced = schema_document(
            "Order Line",
            json!([{"name": "sku", "typeRef": {"name": "string"}}]),
            json!({}),
        );
        assert!(issues(define(&spaced))[0]
            .1
            .contains("schema name \"Order Line\" must match"));
    }
}

#[test]
fn imports_and_operation_sets_are_refused() {
    for target in targets("imports_and_operation_sets_are_refused") {
        let engine = target.open();
        let define = |document: &Value| {
            engine
                .schemas()
                .define(&alice(), document, define_options())
        };
        let imports = with(
            &order_document(),
            "imports",
            json!([{"from": "../shared", "package": "shared", "types": ["Money"]}]),
        );
        assert_eq!(
            issues(define(&imports))
                .into_iter()
                .map(|(path, _)| path)
                .collect::<Vec<_>>(),
            ["/imports"]
        );
        let operations = with(
            &order_document(),
            "operationSets",
            json!([{"name": "OrderOps", "operations": [{"name": "getOrder", "typeRef": {"name": "Order"}}]}]),
        );
        assert_eq!(
            issues(define(&operations))
                .into_iter()
                .map(|(path, _)| path)
                .collect::<Vec<_>>(),
            ["/operationSets"]
        );
    }
}

#[test]
fn field_types_the_schema_runtime_cannot_validate_are_refused() {
    for target in targets("field_types_the_schema_runtime_cannot_validate_are_refused") {
        let engine = target.open();
        let document = schema_document(
            "Order",
            json!([
                {"name": "payment", "typeRef": {"name": "Payment"}},
                {"name": "labels", "typeRef": {"name": "string", "isMap": true}},
                {"name": "owner", "typeRef": {"name": "Customer"}}
            ]),
            json!({
                "unions": {"Payment": {"name": "Payment", "types": ["Card", "Cash"]}},
                "types": {
                    "Card": {"name": "Card", "role": "EmbeddedStruct"},
                    "Cash": {"name": "Cash", "role": "EmbeddedStruct"}
                }
            }),
        );
        assert_eq!(
            issues(engine.schemas().define(&alice(), &document, define_options())),
            [
                issue("/types/Order/fields/0/typeRef", "field Order.payment has the union type Payment, which the schema runtime does not validate"),
                issue("/types/Order/fields/1/typeRef", "field Order.labels is a map, which the schema runtime does not validate"),
                issue("/types/Order/fields/2/typeRef", "field Order.owner has type Customer, which is not a primitive, a scalar, or an enum or type of this schema"),
            ]
        );
    }
}

#[test]
fn a_document_the_loader_refuses_is_refused_with_its_issues_and_nothing_is_written() {
    for target in
        targets("a_document_the_loader_refuses_is_refused_with_its_issues_and_nothing_is_written")
    {
        let engine = target.open();
        let mut unknown_key = order_document();
        unknown_key["types"]["Order"]["colour"] = json!("red");
        let found = issues(
            engine
                .schemas()
                .define(&alice(), &unknown_key, define_options()),
        );
        assert_eq!(found[0], issue("/types/Order", "unknown key \"colour\""));
        let syntax = refusal(engine.schemas().define(
            &alice(),
            "{\"kind\": \"General\",",
            define_options(),
        ));
        assert_eq!(syntax.code(), EngineErrorCode::InvalidSchema);
        assert_eq!(syntax.name(), "SchemaDocumentError");
        let sourced = refusal(engine.schemas().define(
            &alice(),
            "{}",
            DefineOptions {
                source: Some("orders.schema.json"),
                ..DefineOptions::default()
            },
        ));
        assert!(
            sourced.message().starts_with("orders.schema.json: "),
            "{sourced}"
        );
        assert!(engine.schemas().list(&alice(), ANY).unwrap().is_empty());
        // Nothing was written, not even an event.
        assert_eq!(
            support::count(engine.db(), "SELECT COUNT(*) AS n FROM engine_events"),
            0
        );
    }
}

#[test]
fn a_behavior_the_loader_accepts_is_refused_without_an_implementation_and_on_a_nested_type() {
    for target in targets(
        "a_behavior_the_loader_accepts_is_refused_without_an_implementation_and_on_a_nested_type",
    ) {
        let engine = target.open_with(|options| {
            options.meta_schema = Some(MetaSchema::Text(behavior_meta_schema()))
        });
        let mut document = order_document();
        document["types"]["Order"]["behaviors"] = json!([{"name": "acme.Audited"}]);
        document["types"]["OrderLine"]["behaviors"] =
            json!([{"name": "acme.Stock", "config": {"aisles": 3}}]);
        assert_eq!(
            issues(engine.schemas().define(&alice(), &document, define_options())),
            [
                issue("/types/OrderLine/behaviors/0", "type OrderLine: behavior acme.Stock composes on the instance type, Order; OrderLine is a nested type, which has no instances"),
                issue("/types/Order/behaviors/0", "behavior acme.Audited on type Order: no implementation registered"),
            ]
        );
        assert!(engine.schemas().list(&alice(), ANY).unwrap().is_empty());
        // Without the behavior, the deployment's meta-schema loads the
        // document.
        assert_eq!(
            engine
                .schemas()
                .define(&alice(), order_document(), define_options())
                .unwrap()
                .name,
            "Order"
        );
    }
}

fn blob(name: &str, type_mappings: Option<Value>, rules: Value) -> Value {
    let mut scalar = json!({"name": "Acme.Blob", "languagePrimitive": "object"});
    if let Value::Object(rules) = rules {
        scalar.as_object_mut().unwrap().extend(rules);
    }
    if let Some(type_mappings) = type_mappings {
        scalar["typeMappings"] = type_mappings;
    }
    schema_document(
        name,
        json!([{"name": "payload", "typeRef": {"name": "Acme.Blob"}}]),
        json!({"scalars": {"Acme.Blob": scalar}}),
    )
}

fn issue_paths_and_rules(
    issues: &[superschematic_engine::ValidationIssue],
) -> Vec<(String, String)> {
    issues
        .iter()
        .map(|issue| (issue.path.clone(), issue.rule.clone()))
        .collect()
}

#[test]
fn an_object_scalar_the_document_declares_says_which_json_it_holds_or_define_and_publish_refuse_it()
{
    for target in targets("an_object_scalar_the_document_declares_says_which_json_it_holds_or_define_and_publish_refuse_it") {
        let engine = target.open();
        let schemas = engine.schemas();
        let refused = vec![issue(
            "/scalars/Acme.Blob",
            "scalar Acme.Blob has language primitive object but no json_schema type mapping of object, array or any to say which JSON it holds: add typeMappings: { json_schema: object } (or array or any; JSONSchemaType in a catalog row), use the catalog's Generic.JSON for free-form JSON, or model a value with known fields as a nested object type",
        )];
        assert_eq!(issues(schemas.define(&alice(), blob("Reading", None, json!({})), define_options())), refused);
        assert_eq!(
            issues(schemas.define(
                &alice(),
                blob("Reading", Some(json!({"json_schema": "string", "sql": "JSONB"})), json!({})),
                define_options()
            )),
            refused
        );
        // A length is a rule on a string, so with one the runtime does not
        // hold the value to a JSON object.
        assert_eq!(
            issues(schemas.define(
                &alice(),
                blob("Reading", Some(json!({"json_schema": "object"})), json!({"maxLength": 10})),
                define_options()
            )),
            [issue(
                "/scalars/Acme.Blob",
                "scalar Acme.Blob has language primitive object and json_schema type mapping object, but its maxLength 10 is a rule on a string, so the validators check its values as strings: drop the maxLength"
            )]
        );
        assert!(schemas.list(&alice(), ANY).unwrap().is_empty());

        // A draft an engine stored before the rule is refused at publish,
        // which loads it again.
        let canonical = load_schema_file(&blob("Reading", None, json!({})).to_string(), "schema")
            .unwrap()
            .canonical;
        engine
            .db()
            .write(Lock::Catalog, |tx| {
                tx.run(
                    "INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, defined_by) VALUES (?, ?, 0, ?, ?, ?, ?)",
                    &[
                        SqlValue::from("default"),
                        SqlValue::from("Reading"),
                        SqlValue::from(canonical.as_str()),
                        SqlValue::from(sha256_hex(&canonical)),
                        SqlValue::Integer(1),
                        SqlValue::from("alice"),
                    ],
                )
            })
            .unwrap();
        assert_eq!(issues(schemas.publish(&alice(), "Reading", ANY)), refused);
        assert_eq!(schemas.live(&alice(), "Reading", ANY).unwrap(), None);

        // With the mapping, the schema runtime holds the value to that JSON.
        for (name, json_type, value, wrong) in [
            ("ObjectBlob", "object", json!({"a": [1, "b"]}), Some(json!([1]))),
            ("ArrayBlob", "array", json!([1, "b"]), Some(json!({"a": 1}))),
            ("AnyBlob", "any", json!("text"), None),
        ] {
            schemas
                .define(&alice(), blob(name, Some(json!({"json_schema": json_type})), json!({})), define_options())
                .unwrap();
            assert_eq!(schemas.publish(&alice(), name, ANY).unwrap().version, 1, "{name}");
            assert!(schemas.validate(&alice(), name, &json!({"payload": value}), LIVE).unwrap().is_empty(), "{name}");
            if let Some(wrong) = wrong {
                assert_eq!(
                    issue_paths_and_rules(&schemas.validate(&alice(), name, &json!({"payload": wrong}), LIVE).unwrap()),
                    [("payload".to_owned(), "type".to_owned())],
                    "{name}"
                );
            }
        }
    }
}

#[test]
fn a_scalar_whose_map_key_is_not_its_name_is_refused_before_its_json_is_judged() {
    for target in
        targets("a_scalar_whose_map_key_is_not_its_name_is_refused_before_its_json_is_judged")
    {
        let engine = target.open();
        let document = schema_document(
            "Reading",
            json!([{"name": "payload", "typeRef": {"name": "Acme.Blob"}}]),
            json!({"scalars": {"Acme.Blob": {"name": "Acme.Other", "languagePrimitive": "object", "typeMappings": {"json_schema": "object"}}}}),
        );
        assert_eq!(
            issues(
                engine
                    .schemas()
                    .define(&alice(), &document, define_options())
            ),
            [issue(
                "/scalars/Acme.Blob",
                "scalar map key \"Acme.Blob\" does not match the definition name \"Acme.Other\""
            )]
        );
        assert!(engine.schemas().list(&alice(), ANY).unwrap().is_empty());
    }
}

#[test]
fn an_extension_json_scalar_as_format_to_json_writes_it_is_defined_and_published() {
    for target in
        targets("an_extension_json_scalar_as_format_to_json_writes_it_is_defined_and_published")
    {
        // cli/testdata/format/ext-json-scalar.schema.json is what format
        // --to=json writes of a TypeScript service whose extension catalog
        // gives Ext.Doc the Object primitive and the json_schema mapping
        // object. The engine knows only the builtin catalog, so it reads
        // Ext.Doc as written.
        let engine = target.open();
        let text = repository_file("cli/testdata/format/ext-json-scalar.schema.json");
        engine
            .schemas()
            .define(&alice(), text, define_options())
            .unwrap();
        assert_eq!(
            engine
                .schemas()
                .publish(&alice(), "ext-json-scalar", ANY)
                .unwrap()
                .version,
            1
        );
        assert!(engine
            .schemas()
            .validate(
                &alice(),
                "ext-json-scalar",
                &json!({"payload": {"a": 1}, "meta": [1, "b"]}),
                LIVE
            )
            .unwrap()
            .is_empty());
        assert_eq!(
            issue_paths_and_rules(
                &engine
                    .schemas()
                    .validate(
                        &alice(),
                        "ext-json-scalar",
                        &json!({"payload": [1], "meta": 7}),
                        LIVE
                    )
                    .unwrap()
            ),
            [("payload".to_owned(), "type".to_owned())]
        );
    }
}

#[test]
fn with_the_core_meta_schema_the_loader_itself_refuses_a_behavior_the_core_does_not_declare() {
    for target in targets(
        "with_the_core_meta_schema_the_loader_itself_refuses_a_behavior_the_core_does_not_declare",
    ) {
        let engine = target.open();
        let mut document = order_document();
        document["types"]["Order"]["behaviors"] = json!([{"name": "acme.Audited"}]);
        let found = issues(
            engine
                .schemas()
                .define(&alice(), &document, define_options()),
        );
        assert_eq!(found.len(), 1, "{found:?}");
        assert_eq!(found[0].0, "/types/Order/behaviors/0/name");
        // The core's behaviors, as the meta-schema this build carries lists
        // them.
        assert!(
            found[0]
                .1
                .starts_with("must be one of \"Approvals\", \"Assignment\", \"Blueprint\""),
            "{}",
            found[0].1
        );
        assert!(found[0].1.ends_with("\"Workflow\""), "{}", found[0].1);
    }
}

fn publish_orders(engine: &superschematic_engine::Engine) {
    engine
        .schemas()
        .define(&alice(), order_document(), define_options())
        .unwrap();
    engine.schemas().publish(&alice(), "Order", ANY).unwrap();
}

#[test]
fn a_valid_instance_has_no_issues() {
    for target in targets("a_valid_instance_has_no_issues") {
        let engine = target.open();
        publish_orders(&engine);
        let order = json!({"title": "Desk", "quantity": 2, "status": "open", "lines": [{"sku": "D-1", "count": 1}]});
        assert!(engine
            .schemas()
            .validate(&alice(), "Order", &order, LIVE)
            .unwrap()
            .is_empty());
    }
}

#[test]
fn field_rules_come_from_the_schema_runtime_with_nested_paths() {
    for target in targets("field_rules_come_from_the_schema_runtime_with_nested_paths") {
        let engine = target.open();
        publish_orders(&engine);
        let found = engine
            .schemas()
            .validate(
                &alice(),
                "Order",
                &json!({"title": 7, "quantity": "two", "status": "lost", "lines": [{"count": 0}, null, {"sku": "SKU-TOO-LONG-1"}]}),
                LIVE,
            )
            .unwrap();
        let expected: Vec<(String, String)> = [
            ("title", "type"),
            ("quantity", "type"),
            ("status", "enum"),
            ("lines[0].sku", "required"),
            ("lines[0].count", "min"),
            ("lines[1]", "required"),
            ("lines[2].sku", "maxLength"),
        ]
        .iter()
        .map(|(path, rule)| ((*path).to_owned(), (*rule).to_owned()))
        .collect();
        assert_eq!(issue_paths_and_rules(&found), expected);
    }
}

#[test]
fn an_undeclared_key_is_refused_at_every_level() {
    for target in targets("an_undeclared_key_is_refused_at_every_level") {
        let engine = target.open();
        publish_orders(&engine);
        let found = engine
            .schemas()
            .validate(
                &alice(),
                "Order",
                &json!({"title": "Desk", "colour": "red", "lines": [{"sku": "D-1", "weight": 3}]}),
                LIVE,
            )
            .unwrap();
        assert_eq!(
            found,
            [
                superschematic_engine::ValidationIssue::new(
                    "colour",
                    "unknown",
                    "Order has no field colour"
                ),
                superschematic_engine::ValidationIssue::new(
                    "lines[0].weight",
                    "unknown",
                    "OrderLine has no field weight"
                ),
            ]
        );
    }
}

#[test]
fn a_value_of_the_wrong_shape_is_one_issue_the_schema_runtime_type_rule() {
    for target in targets("a_value_of_the_wrong_shape_is_one_issue_the_schema_runtime_type_rule") {
        let engine = target.open();
        publish_orders(&engine);
        engine
            .schemas()
            .define(
                &alice(),
                schema_document(
                    "Shipment",
                    json!([
                        {"name": "tags", "typeRef": {"name": "string", "isArray": true}, "required": true},
                        {"name": "address", "typeRef": {"name": "Address"}}
                    ]),
                    json!({"types": {"Address": {"name": "Address", "role": "EmbeddedStruct", "fields": [{"name": "city", "typeRef": {"name": "string"}}]}}}),
                ),
                define_options(),
            )
            .unwrap();
        engine.schemas().publish(&alice(), "Shipment", ANY).unwrap();
        let found = |name: &str, value: Value| -> Vec<(String, String, String)> {
            engine
                .schemas()
                .validate(&alice(), name, &value, LIVE)
                .unwrap()
                .into_iter()
                .map(|issue| (issue.path, issue.rule, issue.message))
                .collect()
        };
        let one = |path: &str, message: &str| {
            vec![(path.to_owned(), "type".to_owned(), message.to_owned())]
        };
        // An optional list given a non-list.
        assert_eq!(
            found("Order", json!({"title": "Desk", "lines": {"sku": "D-1"}})),
            one("lines", "expected an array")
        );
        // A required list given a non-list.
        assert_eq!(
            found("Shipment", json!({"tags": "fragile"})),
            one("tags", "expected an array")
        );
        // An object-typed field given a string.
        assert_eq!(
            found("Shipment", json!({"tags": [], "address": "Main St"})),
            one("address", "expected an object")
        );
        // A list element of an object type given a number.
        assert_eq!(
            found("Order", json!({"title": "Desk", "lines": [7]})),
            one("lines[0]", "expected an object")
        );
    }
}

#[test]
fn an_instance_is_a_json_object_of_json_values() {
    for target in targets("an_instance_is_a_json_object_of_json_values") {
        let engine = target.open();
        publish_orders(&engine);
        assert_eq!(
            engine
                .schemas()
                .validate(&alice(), "Order", &json!(["Desk"]), LIVE)
                .unwrap(),
            [superschematic_engine::ValidationIssue::new(
                "",
                "type",
                "an instance is a JSON object"
            )]
        );
        // A number past the double range is Infinity to JavaScript, which
        // is no JSON number.
        let infinite: Value =
            serde_json::from_str(r#"{"title": "Desk", "quantity": 1e400}"#).unwrap();
        assert_eq!(
            engine
                .schemas()
                .validate(&alice(), "Order", &infinite, LIVE)
                .unwrap(),
            [superschematic_engine::ValidationIssue::new(
                "quantity",
                "type",
                "Infinity is not a JSON number"
            )]
        );
    }
}

#[test]
fn a_value_validates_against_the_live_version_or_a_given_one() {
    for target in targets("a_value_validates_against_the_live_version_or_a_given_one") {
        let engine = target.open();
        publish_orders(&engine);
        let mut next = order_document();
        next["types"]["Order"]["fields"]
            .as_array_mut()
            .unwrap()
            .push(json!({"name": "note", "typeRef": {"name": "string"}}));
        engine
            .schemas()
            .define(&alice(), &next, define_options())
            .unwrap();
        engine.schemas().publish(&alice(), "Order", ANY).unwrap();
        let value = json!({"title": "Desk", "note": "fragile"});
        assert!(engine
            .schemas()
            .validate(&alice(), "Order", &value, LIVE)
            .unwrap()
            .is_empty());
        let first = ValidateOptions {
            version: Some(1),
            ..LIVE
        };
        let rules: Vec<String> = engine
            .schemas()
            .validate(&alice(), "Order", &value, first)
            .unwrap()
            .into_iter()
            .map(|issue| issue.rule)
            .collect();
        assert_eq!(rules, ["unknown"]);
    }
}

#[test]
fn the_validator_of_a_version_is_built_once() {
    for target in targets("the_validator_of_a_version_is_built_once") {
        let engine = target.open();
        publish_orders(&engine);
        let first = ValidateOptions {
            version: Some(1),
            ..LIVE
        };
        let validator = engine.schemas().validator(&alice(), "Order", LIVE).unwrap();
        assert!(Arc::ptr_eq(
            &engine
                .schemas()
                .validator(&alice(), "Order", first)
                .unwrap(),
            &validator
        ));
        assert_eq!(validator.model().instance_type, "Order");
        engine
            .schemas()
            .define(
                &alice(),
                with(&order_document(), "description", json!("next")),
                define_options(),
            )
            .unwrap();
        engine.schemas().publish(&alice(), "Order", ANY).unwrap();
        assert!(!Arc::ptr_eq(
            &engine.schemas().validator(&alice(), "Order", LIVE).unwrap(),
            &validator
        ));
        assert!(Arc::ptr_eq(
            &engine
                .schemas()
                .validator(&alice(), "Order", first)
                .unwrap(),
            &validator
        ));
    }
}

#[test]
fn a_catalog_scalar_the_document_declares_validates_as_the_catalog_has_it() {
    for target in targets("a_catalog_scalar_the_document_declares_validates_as_the_catalog_has_it")
    {
        let engine = target.open();
        let declared = schema_document(
            "Reading",
            json!([
                {"name": "meta", "typeRef": {"name": "Generic.JSON"}},
                {"name": "count", "typeRef": {"name": "Generic.Int64"}}
            ]),
            json!({"scalars": {
                "Generic.JSON": {"name": "Generic.JSON", "languagePrimitive": "object", "typeMappings": {"sql": "JSONB"}},
                "Generic.Int64": {"name": "Generic.Int64", "languagePrimitive": "number"}
            }}),
        );
        engine
            .schemas()
            .define(&alice(), &declared, define_options())
            .unwrap();
        engine.schemas().publish(&alice(), "Reading", ANY).unwrap();
        assert!(engine
            .schemas()
            .validate(
                &alice(),
                "Reading",
                &json!({"meta": {"a": [1, "b"]}, "count": 2}),
                LIVE
            )
            .unwrap()
            .is_empty());
        let paths: Vec<String> = engine
            .schemas()
            .validate(&alice(), "Reading", &json!({"count": 1.5}), LIVE)
            .unwrap()
            .into_iter()
            .map(|issue| issue.path)
            .collect();
        assert_eq!(paths, ["count"]);
        // A version that stops declaring them changes no scalar.
        engine
            .schemas()
            .define(
                &alice(),
                with(&declared, "scalars", json!({})),
                define_options(),
            )
            .unwrap();
        assert_eq!(
            engine
                .schemas()
                .publish(&alice(), "Reading", ANY)
                .unwrap()
                .version,
            2
        );
    }
}

#[test]
fn validating_a_schema_with_no_live_version_is_not_found() {
    for target in targets("validating_a_schema_with_no_live_version_is_not_found") {
        let engine = target.open();
        engine
            .schemas()
            .define(&alice(), order_document(), define_options())
            .unwrap();
        let refused = refusal(
            engine
                .schemas()
                .validate(&alice(), "Order", &json!({}), LIVE),
        );
        assert_eq!(refused.code(), EngineErrorCode::NotFound);
        assert_eq!(
            refused.message(),
            "schema Order has no live version in namespace default"
        );
        assert_eq!(
            code(
                engine
                    .schemas()
                    .validate(&alice(), "Note", &json!({}), LIVE)
            ),
            EngineErrorCode::NotFound
        );
        let refused = refusal(engine.schemas().validate(
            &alice(),
            "Order",
            &json!({}),
            ValidateOptions {
                version: Some(2),
                ..LIVE
            },
        ));
        assert_eq!(
            refused.message(),
            "schema Order has no version 2 in namespace default"
        );
    }
}

// normalize.test.ts, without the behavior its document composes: the
// value as a create stores it, its defaults filled.
fn contact_document() -> Value {
    schema_document(
        "Contact",
        json!([
            {"name": "name", "typeRef": {"name": "string"}, "required": true},
            {"name": "email", "typeRef": {"name": "Contact.Email"}, "unique": true},
            {"name": "color", "typeRef": {"name": "Design.Color"}},
            {"name": "labels", "typeRef": {"name": "Generic.StringMap"}},
            {"name": "seenAt", "typeRef": {"name": "Temporal.DateTime"}},
            {"name": "score", "typeRef": {"name": "number"}, "default": "2.5"},
            {"name": "active", "typeRef": {"name": "boolean"}, "default": "true"},
            {"name": "tier", "typeRef": {"name": "Tier"}, "default": "free"},
            {"name": "address", "typeRef": {"name": "Address"}}
        ]),
        json!({
            "enums": {"Tier": {"name": "Tier", "values": [{"name": "FREE", "serializedAs": "free"}, {"name": "PAID", "serializedAs": "paid"}]}},
            "types": {
                "Address": {
                    "name": "Address",
                    "role": "EmbeddedStruct",
                    "fields": [
                        {"name": "city", "typeRef": {"name": "string"}, "required": true},
                        {"name": "country", "typeRef": {"name": "string"}, "default": "NZ"},
                        {"name": "contact", "typeRef": {"name": "Contact.Email"}}
                    ]
                }
            }
        }),
    )
}

#[test]
fn schemas_validate_answers_as_a_create_would_the_value_as_stored_its_defaults_filled() {
    for target in targets(
        "schemas_validate_answers_as_a_create_would_the_value_as_stored_its_defaults_filled",
    ) {
        let engine = target.open();
        engine
            .schemas()
            .define(&alice(), contact_document(), define_options())
            .unwrap();
        engine.schemas().publish(&alice(), "Contact", ANY).unwrap();
        assert!(engine
            .schemas()
            .validate(
                &alice(),
                "Contact",
                &json!({"name": "Ada", "color": "red"}),
                LIVE
            )
            .unwrap()
            .is_empty());
        let mut paths: Vec<String> = engine
            .schemas()
            .validate(
                &alice(),
                "Contact",
                &json!({"name": "Ada", "color": "not a color at all"}),
                LIVE,
            )
            .unwrap()
            .into_iter()
            .map(|issue| issue.path)
            .collect();
        paths.dedup();
        assert_eq!(paths, ["color"]);

        // The validator normalizes as a write stores: each scalar in its
        // canonical form, at every depth, the defaults filled on a create.
        let validator = engine
            .schemas()
            .validator(&alice(), "Contact", LIVE)
            .unwrap();
        let normalized = validator.normalize(
            &json!({"name": "Ada", "email": "Ada@Example.COM", "color": "red", "labels": "{\"b\":\"2\",\"a\":\"1\"}", "address": {"city": "Wellington", "contact": "Office@Example.COM"}}),
            superschematic_engine::NormalizeMode::Create,
        );
        assert!(normalized.issues.is_empty(), "{:?}", normalized.issues);
        assert_eq!(
            normalized.value,
            json!({
                "name": "Ada",
                "email": "ada@example.com",
                "color": "#FF0000FF",
                "labels": {"a": "1", "b": "2"},
                "address": {"city": "Wellington", "contact": "office@example.com", "country": "NZ"},
                "score": 2.5,
                "active": true,
                "tier": "free"
            })
        );
        // A patch fills no default.
        assert_eq!(
            validator
                .normalize(
                    &json!({"color": "#abc", "score": null, "address": {"city": "Auckland"}}),
                    superschematic_engine::NormalizeMode::Patch
                )
                .value,
            json!({"color": "#AABBCCFF", "score": null, "address": {"city": "Auckland"}})
        );
        assert_eq!(
            validator.normalize_field("email", &json!("ADA@example.com")),
            json!("ada@example.com")
        );
    }
}

#[test]
fn capabilities_answer_for_each_live_schema() {
    for target in targets("capabilities_answer_for_each_live_schema") {
        let engine = target.open();
        publish_orders(&engine);
        engine
            .schemas()
            .define(
                &alice(),
                schema_document(
                    "Note",
                    json!([{"name": "body", "typeRef": {"name": "string"}}]),
                    json!({}),
                ),
                define_options(),
            )
            .unwrap();
        // A draft that was never published has no live version.
        assert_eq!(
            engine.schemas().capabilities(&alice(), ANY).unwrap(),
            [superschematic_engine::SchemaCapabilities {
                name: "Order".into(),
                read: true,
                write: true,
                define: true,
                publish: true
            }]
        );
        assert!(engine
            .schemas()
            .behaviors(&alice(), "Order", LIVE)
            .unwrap()
            .is_empty());
    }
}
