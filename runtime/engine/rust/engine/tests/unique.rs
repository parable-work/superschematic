//! Unique fields (@unique, @key and a unique @index) and lookup, the cases
//! of `test/unique.test.ts` that need no behavior: what define refuses, the
//! writes a unique index refuses (a create and an update), publish adding a
//! unique field the stored instances break and dropping one, a namespace's
//! own values, lookup by a value with a slash, and the value store, which
//! keeps an indexed field inline in the row; on SQLite and, with the
//! variable set, on Postgres.
//!
//! milestone 5: a behavior's update() and two creates in one call meeting
//! the index (test.Slugger). milestone 6: the describe document's lookup.

mod support;

use serde_json::{json, Value};

use superschematic_engine::storage::{Db, Dialect};
use superschematic_engine::{
    AccessPolicy, AccessRequest, CreateOptions, DefineOptions, Engine, EngineErrorCode, GetOptions,
    ListOptions, NamespaceOptions, Policy, Principal, ReadEvents, SchemaTarget, ValueOptions,
    WriteOptions,
};
use support::{alice, code, count, refusal, schema_document, targets, texts};

fn model_document(fields: Value) -> Value {
    let mut all = vec![
        json!({"name": "slug", "typeRef": {"name": "string"}, "unique": true}),
        json!({"name": "title", "typeRef": {"name": "string"}}),
    ];
    all.extend(fields.as_array().cloned().unwrap_or_default());
    schema_document("Model", Value::Array(all), json!({}))
}

fn publish(engine: &Engine, document: &Value, namespace: Option<&str>) {
    engine
        .schemas()
        .define(
            &alice(),
            document,
            DefineOptions {
                namespace,
                source: None,
            },
        )
        .unwrap();
    engine
        .schemas()
        .publish(
            &alice(),
            document["name"].as_str().unwrap(),
            SchemaTarget { namespace },
        )
        .unwrap();
}

fn create_in(
    engine: &Engine,
    data: Value,
    id: &str,
    namespace: Option<&str>,
) -> superschematic_engine::Result<superschematic_engine::InstanceRecord> {
    engine.instances().create(
        &alice(),
        "Model",
        &data,
        CreateOptions {
            id: Some(id),
            namespace,
            behaviors: None,
        },
    )
}

fn create(engine: &Engine, data: Value, id: &str) -> superschematic_engine::InstanceRecord {
    create_in(engine, data, id, None).unwrap()
}

fn update(
    engine: &Engine,
    id: &str,
    patch: Value,
) -> superschematic_engine::Result<superschematic_engine::InstanceRecord> {
    engine
        .instances()
        .update(&alice(), "Model", id, &patch, WriteOptions::default())
}

fn lookup(
    engine: &Engine,
    key: Value,
    namespace: Option<&str>,
) -> superschematic_engine::Result<Option<superschematic_engine::InstanceRecord>> {
    engine.instances().lookup(
        &alice(),
        "Model",
        &key,
        GetOptions {
            namespace,
            value_refs: false,
        },
    )
}

fn indexes_named(db: &Db, prefix: &str) -> Vec<String> {
    let sql = match db.dialect() {
        Dialect::Sqlite => format!("SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE '{prefix}%' ORDER BY name"),
        Dialect::Postgres => format!("SELECT indexname AS name FROM pg_catalog.pg_indexes WHERE schemaname = current_schema() AND indexname LIKE '{prefix}%' ORDER BY indexname"),
    };
    texts(db, &sql)
}

fn unique_indexes(engine: &Engine) -> Vec<String> {
    indexes_named(engine.db(), "engine_unique_")
}

#[test]
fn define_refuses_unique_key_and_index_on_a_field_with_no_one_value_or_on_another_type() {
    for target in targets("define_refuses_unique_key_and_index") {
        let engine = target.open();
        let refused = |document: Value, wanted: &str| {
            let error = refusal(engine.schemas().define(
                &alice(),
                document,
                DefineOptions::default(),
            ));
            assert_eq!(error.name(), "SchemaDocumentError");
            assert!(error.message().contains(wanted), "{error}");
        };
        refused(
            schema_document("Model", json!([{"name": "tags", "typeRef": {"name": "string", "isArray": true}, "unique": true}]), json!({})),
            "/types/Model/fields/0/unique: field Model.tags is @unique, and a unique field holds a string, a number or a boolean",
        );
        refused(
            schema_document(
                "Model",
                json!([{"name": "body", "typeRef": {"name": "Generic.JSON"}, "key": true}]),
                json!({}),
            ),
            "field Model.body is @key, and a unique field holds a string, a number or a boolean",
        );
        refused(
            schema_document(
                "Model",
                json!([{"name": "address", "typeRef": {"name": "Address"}, "unique": true}]),
                json!({"types": {"Address": {"name": "Address", "role": "EmbeddedStruct", "fields": [{"name": "city", "typeRef": {"name": "string"}, "unique": true}]}}}),
            ),
            "field Address.city is @unique, and the engine enforces @unique on the instance type's own fields (Model) only",
        );
        let indexed = |indexes: Value| {
            let mut document = model_document(
                json!([{"name": "tags", "typeRef": {"name": "string", "isArray": true}}]),
            );
            document["types"]["Model"]["indexes"] = indexes;
            document
        };
        refused(
            indexed(json!([{"keys": ["slug", "owner"], "unique": true}])),
            "/types/Model/indexes/0/keys/1: the index names owner, which is not a field of Model",
        );
        refused(indexed(json!([{"keys": ["tags"]}])), "field Model.tags holds no string, number or boolean, and an index compares one value per field");
        refused(
            indexed(json!([{"keys": []}])),
            "an index lists at least one field",
        );
        refused(
            schema_document(
                "Model",
                json!([{"name": "slug", "typeRef": {"name": "string"}, "unique": true, "jsonTag": "a\"b"}]),
                json!({}),
            ),
            "holds a quote, a backslash or a control character",
        );
        // A field of the instance type that holds a string, a number, a
        // boolean or an enum's value takes them all.
        engine
            .schemas()
            .define(
                &alice(),
                schema_document(
                    "Model",
                    json!([
                        {"name": "slug", "typeRef": {"name": "string"}, "unique": true},
                        {"name": "rank", "typeRef": {"name": "Generic.Int64"}, "key": true},
                        {"name": "live", "typeRef": {"name": "boolean"}, "unique": true},
                        {"name": "kind", "typeRef": {"name": "Kind"}, "unique": true}
                    ]),
                    json!({"enums": {"Kind": {"name": "Kind", "values": [{"name": "A"}, {"name": "B"}]}}}),
                ),
                DefineOptions::default(),
            )
            .unwrap();
    }
}

#[test]
fn a_create_or_an_update_that_repeats_a_unique_value_is_a_conflict_absent_and_null_are_no_value() {
    for target in targets("a_create_or_an_update_that_repeats_a_unique_value_is_a_conflict") {
        let engine = target.open();
        publish(&engine, &model_document(json!([])), None);
        create(&engine, json!({"slug": "openai/gpt-5", "title": "A"}), "a");
        let refused = refusal(create_in(
            &engine,
            json!({"slug": "openai/gpt-5", "title": "B"}),
            "b",
            None,
        ));
        assert_eq!(
            (refused.code(), refused.name()),
            (EngineErrorCode::Conflict, "UniqueConflictError")
        );
        assert_eq!(refused.details().unwrap(), json!({"fields": ["slug"]}));
        assert_eq!(
            refused.message(),
            "Model in namespace default: another instance holds slug \"openai/gpt-5\", and it is unique"
        );
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Model", "b", GetOptions::default())
                .unwrap(),
            None
        );
        // Any number of instances lack it, absent or null.
        create(&engine, json!({"title": "C"}), "c");
        create(&engine, json!({"title": "D"}), "d");
        create(&engine, json!({"slug": null, "title": "E"}), "e");
        // An update to a value another holds is refused and writes nothing;
        // an update that keeps its own value, or removes it, is not.
        let before = engine.events().head().unwrap();
        assert_eq!(
            refusal(update(&engine, "c", json!({"slug": "openai/gpt-5"}))).name(),
            "UniqueConflictError"
        );
        assert_eq!(engine.events().head().unwrap(), before);
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Model", "c", GetOptions::default())
                .unwrap()
                .unwrap()
                .data
                .get("slug"),
            None
        );
        update(&engine, "a", json!({"slug": "openai/gpt-5", "title": "A2"})).unwrap();
        update(&engine, "a", json!({"slug": null})).unwrap();
        update(&engine, "c", json!({"slug": "openai/gpt-5"})).unwrap();
        // A delete frees the value.
        engine
            .instances()
            .delete(&alice(), "Model", "c", WriteOptions::default())
            .unwrap();
        create(&engine, json!({"slug": "openai/gpt-5"}), "f");
        assert_eq!(
            lookup(&engine, json!({"slug": "openai/gpt-5"}), None)
                .unwrap()
                .unwrap()
                .id,
            "f"
        );
    }
}

#[test]
fn a_unique_index_is_per_namespace_and_a_shared_schema_holds_each_namespace_to_its_own() {
    for target in targets("a_unique_index_is_per_namespace") {
        let engine = target.open_with(|options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into(), "west".into(), "common".into()],
                shared: Some("common".into()),
            }
        });
        publish(&engine, &model_document(json!([])), Some("common"));
        create_in(&engine, json!({"slug": "same"}), "e1", Some("east")).unwrap();
        create_in(&engine, json!({"slug": "same"}), "w1", Some("west")).unwrap();
        assert_eq!(
            code(create_in(
                &engine,
                json!({"slug": "same"}),
                "e2",
                Some("east")
            )),
            EngineErrorCode::Conflict
        );
        assert_eq!(
            lookup(&engine, json!({"slug": "same"}), Some("west"))
                .unwrap()
                .unwrap()
                .id,
            "w1"
        );
        assert_eq!(
            lookup(&engine, json!({"slug": "same"}), Some("east"))
                .unwrap()
                .unwrap()
                .id,
            "e1"
        );
    }
}

#[test]
fn key_is_unique_and_a_unique_index_makes_its_fields_unique_together() {
    for target in targets("key_is_unique_and_a_unique_index_makes_its_fields_unique_together") {
        let engine = target.open();
        let mut document = schema_document(
            "Model",
            json!([
                {"name": "code", "typeRef": {"name": "string"}, "key": true},
                {"name": "source", "typeRef": {"name": "string"}},
                {"name": "externalId", "typeRef": {"name": "Generic.Int64"}}
            ]),
            json!({}),
        );
        document["types"]["Model"]["indexes"] =
            json!([{"keys": ["source", "externalId"], "unique": true}, {"keys": ["source"]}]);
        publish(&engine, &document, None);
        create(
            &engine,
            json!({"code": "x", "source": "crm", "externalId": 1}),
            "a",
        );
        create(
            &engine,
            json!({"code": "y", "source": "crm", "externalId": 2}),
            "b",
        );
        create(
            &engine,
            json!({"code": "z", "source": "erp", "externalId": 1}),
            "c",
        );
        // One key missing leaves the instance out of the index.
        create(&engine, json!({"code": "w", "source": "crm"}), "d");
        create(&engine, json!({"code": "v", "source": "crm"}), "e");
        let none = CreateOptions::default();
        assert_eq!(
            refusal(
                engine
                    .instances()
                    .create(&alice(), "Model", &json!({"code": "x"}), none)
            )
            .details()
            .unwrap(),
            json!({"fields": ["code"]})
        );
        let refused = refusal(engine.instances().create(
            &alice(),
            "Model",
            &json!({"code": "u", "source": "crm", "externalId": 2}),
            none,
        ));
        assert_eq!(
            refused.details().unwrap(),
            json!({"fields": ["source", "externalId"]})
        );
        assert!(refused.message().contains("another instance holds source \"crm\" and externalId 2, and together they are unique"), "{refused}");
        assert_eq!(
            lookup(&engine, json!({"externalId": 1, "source": "erp"}), None)
                .unwrap()
                .unwrap()
                .id,
            "c"
        );
        assert_eq!(
            lookup(&engine, json!({"code": "y"}), None)
                .unwrap()
                .unwrap()
                .id,
            "b"
        );
        assert_eq!(
            refusal(lookup(&engine, json!({"source": "crm"}), None)).message(),
            "a lookup key names the fields of one unique index of Model (code; source and externalId), not source"
        );
        // Two unique indexes and a plain one, which the unique one's keys
        // do not repeat.
        assert_eq!(unique_indexes(&engine).len(), 2);
        assert_eq!(indexes_named(engine.db(), "engine_index_").len(), 1);
    }
}

// alice may do everything; anyone else anything but read.
struct Deny;

impl AccessPolicy for Deny {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        request.principal().subject == "alice" || request.action() != "read"
    }
}

#[test]
fn lookup_reads_the_instance_a_value_names_a_slash_and_all_as_a_reader_of_the_schema() {
    for target in targets("lookup_reads_the_instance_a_value_names") {
        let engine = target.open_with(|options| options.policy = Some(Policy::custom(Deny)));
        publish(
            &engine,
            &model_document(json!([{"name": "rank", "typeRef": {"name": "number"}}])),
            None,
        );
        create(
            &engine,
            json!({"slug": "models/openai/gpt-5", "title": "GPT", "rank": 1}),
            "a",
        );
        let found = lookup(&engine, json!({"slug": "models/openai/gpt-5"}), None)
            .unwrap()
            .unwrap();
        assert_eq!(
            (found.id.as_str(), Value::Object(found.data)),
            (
                "a",
                json!({"slug": "models/openai/gpt-5", "title": "GPT", "rank": 1})
            )
        );
        assert_eq!(
            lookup(&engine, json!({"slug": "models/openai"}), None).unwrap(),
            None
        );
        let bob = Principal::new("bob", Vec::<String>::new());
        assert_eq!(
            code(engine.instances().lookup(
                &bob,
                "Model",
                &json!({"slug": "models/openai/gpt-5"}),
                GetOptions::default()
            )),
            EngineErrorCode::Forbidden
        );
        for (key, message) in [
            (json!({"title": "GPT"}), "a lookup key names the fields of one unique index of Model (slug), not title"),
            (json!({"slug": 1}), "key.slug is a string, not 1"),
            (json!({"slug": null}), "key.slug is a string, not null"),
            (json!({"slug": "a", "title": "b"}), "a lookup key names the fields of one unique index of Model (slug), not slug, title"),
            (json!("a"), "a lookup key is a JSON object of the values of one unique index of Model: slug"),
        ] {
            let refused = refusal(lookup(&engine, key, None));
            assert_eq!((refused.code(), refused.message()), (EngineErrorCode::InvalidArgument, message));
        }
        publish(
            &engine,
            &schema_document(
                "Note",
                json!([{"name": "body", "typeRef": {"name": "string"}}]),
                json!({}),
            ),
            None,
        );
        assert_eq!(
            refusal(engine.instances().lookup(
                &alice(),
                "Note",
                &json!({"body": "x"}),
                GetOptions::default()
            ))
            .message(),
            "Note has no unique field to look an instance up by; find one with list and where"
        );
    }
}

#[test]
fn publish_refuses_a_unique_field_the_stored_instances_break_and_creates_its_index_once_they_do_not(
) {
    for target in targets("publish_refuses_a_unique_field_the_stored_instances_break") {
        let engine = target.open();
        let plain = schema_document(
            "Model",
            json!([{"name": "slug", "typeRef": {"name": "string"}}, {"name": "title", "typeRef": {"name": "string"}}]),
            json!({}),
        );
        publish(&engine, &plain, None);
        create(&engine, json!({"slug": "dup", "title": "A"}), "a");
        create(&engine, json!({"slug": "dup", "title": "B"}), "b");
        create(&engine, json!({"title": "C"}), "c");
        // define refuses it while the instances share a value.
        let refused = refusal(engine.schemas().define(
            &alice(),
            model_document(json!([])),
            DefineOptions::default(),
        ));
        assert_eq!(refused.name(), "IncompatibleChangeError");
        assert_eq!(
            refused.details().unwrap(),
            json!({"changes": [{"path": "Model.slug", "message": "field Model.slug becomes unique, and 2 instances in namespace default hold slug \"dup\""}]})
        );
        assert!(refused.message().contains("Make the values distinct first"));
        // A draft defined before they did is refused at publish, which
        // leaves the live version and the storage as they were.
        update(&engine, "b", json!({"slug": "other"})).unwrap();
        engine
            .schemas()
            .define(
                &alice(),
                model_document(json!([])),
                DefineOptions::default(),
            )
            .unwrap();
        update(&engine, "b", json!({"slug": "dup"})).unwrap();
        assert_eq!(
            refusal(
                engine
                    .schemas()
                    .publish(&alice(), "Model", SchemaTarget::default())
            )
            .name(),
            "IncompatibleChangeError"
        );
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Model", SchemaTarget::default())
                .unwrap()
                .unwrap()
                .version,
            Some(1)
        );
        assert!(unique_indexes(&engine).is_empty());
        update(&engine, "b", json!({"slug": "other"})).unwrap();
        assert_eq!(
            engine
                .schemas()
                .publish(&alice(), "Model", SchemaTarget::default())
                .unwrap()
                .version,
            2
        );
        assert_eq!(unique_indexes(&engine).len(), 1);
        assert_eq!(
            refusal(update(&engine, "b", json!({"slug": "dup"}))).name(),
            "UniqueConflictError"
        );
        // A version without the field's @unique drops the index.
        publish(&engine, &plain, None);
        assert!(unique_indexes(&engine).is_empty());
        update(&engine, "b", json!({"slug": "dup"})).unwrap();
        let filter = json!({"slug": "dup"});
        assert_eq!(
            engine
                .instances()
                .list(
                    &alice(),
                    "Model",
                    ListOptions {
                        filter: Some(&filter),
                        ..ListOptions::default()
                    }
                )
                .unwrap()
                .items
                .len(),
            2
        );
        assert!(refusal(lookup(&engine, json!({"slug": "dup"}), None))
            .message()
            .contains("no unique field"));
    }
}

#[test]
fn an_indexed_field_stays_inline_in_the_row_whatever_its_length_and_a_row_that_held_one_by_hash_takes_it_back_at_publish(
) {
    for target in targets("an_indexed_field_stays_inline_in_the_row") {
        let engine = target.open_with(|options| {
            options.values = ValueOptions {
                threshold_bytes: Some(1024),
                ..ValueOptions::default()
            }
        });
        let long = |seed: &str| format!("{seed}/{}", "x".repeat(2000));
        publish(
            &engine,
            &schema_document(
                "Model",
                json!([{"name": "slug", "typeRef": {"name": "string"}}, {"name": "body", "typeRef": {"name": "string"}}]),
                json!({}),
            ),
            None,
        );
        create(
            &engine,
            json!({"slug": long("a"), "body": long("body")}),
            "a",
        );
        let row = |engine: &Engine| -> (Value, String) {
            let data: Value = serde_json::from_str(
                &texts(
                    engine.db(),
                    "SELECT data FROM engine_instances WHERE id = 'a'",
                )[0],
            )
            .unwrap();
            (
                data,
                texts(
                    engine.db(),
                    "SELECT value_refs FROM engine_instances WHERE id = 'a'",
                )[0]
                .clone(),
            )
        };
        assert_eq!(row(&engine).1, r#"["/slug","/body"]"#);
        // A filter on a field the row holds by hash matches it by its hash.
        let filter = json!({"slug": long("a")});
        let listed = engine
            .instances()
            .list(
                &alice(),
                "Model",
                ListOptions {
                    filter: Some(&filter),
                    ..ListOptions::default()
                },
            )
            .unwrap();
        assert_eq!(
            listed
                .items
                .iter()
                .map(|item| item.id.as_str())
                .collect::<Vec<_>>(),
            ["a"]
        );
        let holders = |engine: &Engine| {
            count(
                engine.db(),
                "SELECT COUNT(*) AS n FROM engine_payload_holders WHERE holder = 'instance'",
            )
        };
        assert_eq!(holders(&engine), 2);
        publish(
            &engine,
            &model_document(json!([{"name": "body", "typeRef": {"name": "string"}}])),
            None,
        );
        let (data, refs) = row(&engine);
        assert_eq!(
            (refs.as_str(), data["slug"].as_str().unwrap()),
            (r#"["/body"]"#, long("a").as_str())
        );
        assert_eq!(holders(&engine), 1);
        assert_eq!(
            lookup(&engine, json!({"slug": long("a")}), None)
                .unwrap()
                .unwrap()
                .id,
            "a"
        );
        // A create keeps it inline; its event stores it by hash.
        let created = create(&engine, json!({"slug": long("b"), "body": long("b")}), "b");
        assert_eq!(
            texts(
                engine.db(),
                "SELECT value_refs FROM engine_instances WHERE id = 'b'"
            ),
            [r#"["/body"]"#]
        );
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Model"),
                    instance_id: Some("b"),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events;
        assert_eq!(
            events[0].value_refs.as_deref(),
            Some(&["/data/slug".to_owned(), "/data/body".to_owned()][..])
        );
        assert_eq!(
            lookup(&engine, json!({"slug": long("b")}), None)
                .unwrap()
                .unwrap()
                .id,
            created.id
        );
        assert_eq!(
            code(create_in(&engine, json!({"slug": long("b")}), "c", None)),
            EngineErrorCode::Conflict
        );
    }
}
