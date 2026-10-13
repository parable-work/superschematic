//! Instances, the cases of `test/instances.test.ts`: create, get, list,
//! update and delete on a schema's instance type, keyed by namespace,
//! schema and id, validated by the live version; on SQLite and, with the
//! variable set, on Postgres. A page size or a sequence JSON holds but
//! Rust's types do not (1.5) is the JSON calls' (`calls`) to refuse.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, GetOptions, Ids, InstanceRecord,
    ListOptions, NamespaceOptions, Principal, SchemaTarget, WriteOptions,
};
use support::{alice, code, count, order_document, refusal, schema_document, targets, Target};

fn bob() -> Principal {
    Principal::new("bob", Vec::<String>::new())
}

fn shipment_document() -> Value {
    schema_document(
        "Shipment",
        json!([
            {"name": "carrier", "typeRef": {"name": "string"}, "required": true},
            {"name": "address", "typeRef": {"name": "Address"}},
            {"name": "parcels", "typeRef": {"name": "number", "isArray": true}}
        ]),
        json!({"types": {"Address": {"name": "Address", "role": "EmbeddedStruct", "fields": [
            {"name": "street", "typeRef": {"name": "string"}},
            {"name": "city", "typeRef": {"name": "string"}, "required": true}
        ]}}}),
    )
}

fn publish(engine: &Engine, document: &Value, namespace: Option<&str>) {
    let record = engine
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
        .publish(&alice(), &record.name, SchemaTarget { namespace })
        .unwrap();
}

fn with_orders(
    target: &Target,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> Engine {
    let engine = target.open_with(configure);
    publish(&engine, &order_document(), None);
    engine
}

fn event_count(engine: &Engine) -> i64 {
    count(engine.db(), "SELECT COUNT(*) AS n FROM engine_events")
}

fn create(engine: &Engine, schema: &str, data: Value, id: &str) -> InstanceRecord {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                ..CreateOptions::default()
            },
        )
        .unwrap()
}

fn get(engine: &Engine, schema: &str, id: &str) -> Option<InstanceRecord> {
    engine
        .instances()
        .get(&alice(), schema, id, GetOptions::default())
        .unwrap()
}

fn in_namespace(namespace: &str) -> GetOptions<'_> {
    GetOptions {
        namespace: Some(namespace),
        value_refs: false,
    }
}

fn expected(seq: i64) -> WriteOptions<'static> {
    WriteOptions {
        expected_seq: Some(seq),
        ..WriteOptions::default()
    }
}

fn is_uuid4(id: &str) -> bool {
    let parts: Vec<&str> = id.split('-').collect();
    parts.iter().map(|part| part.len()).collect::<Vec<_>>() == [8, 4, 4, 4, 12]
        && id
            .chars()
            .all(|c| c == '-' || c.is_ascii_digit() || ('a'..='f').contains(&c))
        && parts[2].starts_with('4')
        && matches!(parts[3].chars().next(), Some('8' | '9' | 'a' | 'b'))
}

#[test]
fn create_validates_an_instance_stores_it_with_the_live_version_and_reads_it_back() {
    for target in targets("create_validates_an_instance") {
        let engine = with_orders(&target, |options| options.clock = Clock::Manual(5000));
        let created = engine
            .instances()
            .create(
                &alice(),
                "Order",
                &json!({"title": "Desk", "quantity": 2}),
                CreateOptions::default(),
            )
            .unwrap();
        assert!(is_uuid4(&created.id), "{}", created.id);
        assert_eq!(
            serde_json::to_value(&created).unwrap(),
            json!({
                "namespace": "default",
                "schema": "Order",
                "id": created.id,
                "schemaNamespace": "default",
                "version": 1,
                "seq": 1,
                "data": {"title": "Desk", "quantity": 2},
                // An Order composes no behavior, so it holds no behavior's fields.
                "behaviors": {},
                "createdAt": 5000,
                "createdBy": "alice",
                "updatedAt": 5000,
                "updatedBy": "alice"
            })
        );
        assert_eq!(get(&engine, "Order", &created.id), Some(created));
    }
}

#[test]
fn ids_come_from_the_id_source_or_the_caller() {
    for target in targets("ids_come_from_the_id_source_or_the_caller") {
        let engine = with_orders(&target, |options| {
            options.ids = Ids::Sequence {
                prefix: "order-".into(),
            }
        });
        let none = CreateOptions::default();
        let instances = engine.instances();
        assert_eq!(
            instances
                .create(&alice(), "Order", &json!({"title": "Desk"}), none)
                .unwrap()
                .id,
            "order-1"
        );
        assert_eq!(
            instances
                .create(&alice(), "Order", &json!({"title": "Lamp"}), none)
                .unwrap()
                .id,
            "order-2"
        );
        assert_eq!(
            create(&engine, "Order", json!({"title": "Chair"}), "chair").id,
            "chair"
        );
        assert_eq!(
            get(&engine, "Order", "chair").unwrap().data["title"],
            "Chair"
        );
    }
}

#[test]
fn an_id_is_checked_whoever_made_it() {
    for target in targets("an_id_is_checked_whoever_made_it") {
        let engine = with_orders(&target, |options| {
            options.ids = Ids::Sequence {
                prefix: "not an id".into(),
            }
        });
        for id in ["", "has space", "-leading", &"x".repeat(257)] {
            let refused = refusal(engine.instances().create(
                &alice(),
                "Order",
                &json!({"title": "Desk"}),
                CreateOptions {
                    id: Some(id),
                    ..CreateOptions::default()
                },
            ));
            assert_eq!(refused.code(), EngineErrorCode::InvalidArgument);
            assert_eq!(
                refused.message(),
                format!("instance id \"{id}\" must match ^[A-Za-z0-9][A-Za-z0-9._:-]{{0,255}}$")
            );
        }
        let refused = refusal(engine.instances().create(
            &alice(),
            "Order",
            &json!({"title": "Desk"}),
            CreateOptions::default(),
        ));
        assert!(
            refused.message().starts_with("instance id \"not an id"),
            "{refused}"
        );
    }
}

#[test]
fn a_duplicate_id_in_a_namespace_is_a_conflict_and_writes_nothing() {
    for target in targets("a_duplicate_id_in_a_namespace_is_a_conflict") {
        let engine = with_orders(&target, |_| {});
        create(&engine, "Order", json!({"title": "Desk"}), "fixed");
        let refused = refusal(engine.instances().create(
            &alice(),
            "Order",
            &json!({"title": "Lamp"}),
            CreateOptions {
                id: Some("fixed"),
                ..CreateOptions::default()
            },
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Conflict,
                "Order fixed already exists in namespace default"
            )
        );
        assert_eq!(
            get(&engine, "Order", "fixed").unwrap().data["title"],
            "Desk"
        );
        // The define, the publish and the first create.
        assert_eq!(event_count(&engine), 3);
    }
}

#[test]
fn an_id_in_another_namespace_or_schema_is_another_instance() {
    for target in targets("an_id_in_another_namespace_or_schema_is_another_instance") {
        let engine = target.open_with(|options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into()],
                shared: None,
            }
        });
        publish(&engine, &order_document(), None);
        publish(&engine, &order_document(), Some("east"));
        publish(&engine, &shipment_document(), None);
        create(&engine, "Order", json!({"title": "Desk"}), "o1");
        let east = engine
            .instances()
            .create(
                &alice(),
                "Order",
                &json!({"title": "Lamp"}),
                CreateOptions {
                    id: Some("o1"),
                    namespace: Some("east"),
                    behaviors: None,
                },
            )
            .unwrap();
        assert_eq!(east.seq, 1);
        create(&engine, "Shipment", json!({"carrier": "Post"}), "o1");
        assert_eq!(get(&engine, "Order", "o1").unwrap().data["title"], "Desk");
        let in_east = |engine: &Engine| {
            engine
                .instances()
                .get(&alice(), "Order", "o1", in_namespace("east"))
                .unwrap()
        };
        assert_eq!(in_east(&engine).unwrap().data["title"], "Lamp");
        assert!(engine
            .instances()
            .delete(&alice(), "Order", "o1", WriteOptions::default())
            .unwrap());
        assert_eq!(get(&engine, "Order", "o1"), None);
        assert_eq!(in_east(&engine).unwrap().data["title"], "Lamp");
    }
}

#[test]
fn get_of_a_missing_id_is_none() {
    for target in targets("get_of_a_missing_id_is_none") {
        let engine = with_orders(&target, |_| {});
        assert_eq!(get(&engine, "Order", "missing"), None);
    }
}

#[test]
fn a_schema_without_a_live_version_is_not_found_for_every_operation() {
    for target in targets("a_schema_without_a_live_version_is_not_found") {
        let engine = target.open();
        engine
            .schemas()
            .define(&alice(), order_document(), DefineOptions::default())
            .unwrap();
        for schema in ["Order", "Note"] {
            let instances = engine.instances();
            let data = json!({"title": "Desk"});
            let refusals = [
                refusal(instances.create(&alice(), schema, &data, CreateOptions::default())),
                refusal(instances.get(&alice(), schema, "o1", GetOptions::default())),
                refusal(instances.list(&alice(), schema, ListOptions::default())),
                refusal(instances.update(
                    &alice(),
                    schema,
                    "o1",
                    &json!({"title": "Lamp"}),
                    WriteOptions::default(),
                )),
                refusal(instances.delete(&alice(), schema, "o1", WriteOptions::default())),
            ];
            for refused in refusals {
                assert_eq!(refused.code(), EngineErrorCode::NotFound);
                assert_eq!(
                    refused.message(),
                    format!("schema {schema} has no live version in namespace default")
                );
            }
        }
    }
}

#[test]
fn a_refused_instance_names_each_issue_and_writes_nothing() {
    for target in targets("a_refused_instance_names_each_issue_and_writes_nothing") {
        let engine = with_orders(&target, |_| {});
        let refused = refusal(engine.instances().create(
            &alice(),
            "Order",
            &json!({"quantity": "two", "colour": "red"}),
            CreateOptions::default(),
        ));
        assert_eq!(refused.code(), EngineErrorCode::InvalidInstance);
        assert_eq!(refused.name(), "InstanceValidationError");
        assert_eq!(
            refused.details().unwrap(),
            json!({"issues": [
                {"path": "colour", "rule": "unknown", "message": "Order has no field colour"},
                {"path": "title", "rule": "required", "message": "title is required."},
                {"path": "quantity", "rule": "type", "message": "Generic_Int64 must be a number."}
            ]})
        );
        assert_eq!(
            refused.message(),
            "Order in namespace default (version 1): colour: Order has no field colour; title: title is required.; quantity: Generic_Int64 must be a number."
        );
        assert!(engine
            .instances()
            .list(&alice(), "Order", ListOptions::default())
            .unwrap()
            .items
            .is_empty());
        assert_eq!(event_count(&engine), 2);
    }
}

#[test]
fn update_merges_a_patch_validates_the_result_and_records_who_and_when() {
    for target in targets("update_merges_a_patch") {
        let engine = with_orders(&target, |options| options.clock = Clock::Manual(1000));
        create(
            &engine,
            "Order",
            json!({"title": "Desk", "quantity": 1}),
            "o1",
        );
        engine.clock().set(2000).unwrap();
        let updated = engine
            .instances()
            .update(
                &bob(),
                "Order",
                "o1",
                &json!({"quantity": 3, "status": "shipped"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            Value::Object(updated.data.clone()),
            json!({"title": "Desk", "quantity": 3, "status": "shipped"})
        );
        assert_eq!(updated.seq, 2);
        assert_eq!(
            (
                updated.created_at,
                updated.created_by.as_str(),
                updated.updated_at,
                updated.updated_by.as_str()
            ),
            (1000, "alice", 2000, "bob")
        );
        assert_eq!(get(&engine, "Order", "o1"), Some(updated));
    }
}

#[test]
fn null_in_a_patch_removes_an_optional_field() {
    for target in targets("null_in_a_patch_removes_an_optional_field") {
        let engine = with_orders(&target, |_| {});
        create(
            &engine,
            "Order",
            json!({"title": "Desk", "quantity": 3}),
            "o1",
        );
        let updated = engine
            .instances()
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"quantity": null}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(Value::Object(updated.data), json!({"title": "Desk"}));
    }
}

#[test]
fn null_on_a_required_field_is_refused_and_writes_nothing() {
    for target in targets("null_on_a_required_field_is_refused") {
        let engine = with_orders(&target, |_| {});
        create(&engine, "Order", json!({"title": "Desk"}), "o1");
        let refused = refusal(engine.instances().update(
            &alice(),
            "Order",
            "o1",
            &json!({"title": null}),
            WriteOptions::default(),
        ));
        assert_eq!(refused.code(), EngineErrorCode::InvalidInstance);
        assert_eq!(refused.details().unwrap()["issues"][0]["path"], "title");
        let stored = get(&engine, "Order", "o1").unwrap();
        assert_eq!(
            (Value::Object(stored.data), stored.seq),
            (json!({"title": "Desk"}), 1)
        );
    }
}

#[test]
fn a_nested_object_merges_and_a_list_is_replaced() {
    for target in targets("a_nested_object_merges_and_a_list_is_replaced") {
        let engine = target.open();
        publish(&engine, &shipment_document(), None);
        create(
            &engine,
            "Shipment",
            json!({"carrier": "Post", "address": {"street": "1 Main St", "city": "Oslo"}, "parcels": [1, 2]}),
            "s1",
        );
        let updated = engine
            .instances()
            .update(
                &alice(),
                "Shipment",
                "s1",
                &json!({"address": {"city": "Bergen"}, "parcels": [3]}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            Value::Object(updated.data),
            json!({"carrier": "Post", "address": {"street": "1 Main St", "city": "Bergen"}, "parcels": [3]})
        );
        let cleared = engine
            .instances()
            .update(
                &alice(),
                "Shipment",
                "s1",
                &json!({"address": {"street": null}}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(cleared.data["address"], json!({"city": "Bergen"}));
        // The row holds what JSON.stringify writes.
        assert_eq!(
            support::texts(
                engine.db(),
                "SELECT data FROM engine_instances WHERE id = 's1'"
            ),
            [r#"{"carrier":"Post","address":{"city":"Bergen"},"parcels":[3]}"#]
        );
    }
}

#[test]
fn a_patch_that_changes_nothing_writes_nothing() {
    for target in targets("a_patch_that_changes_nothing_writes_nothing") {
        let engine = with_orders(&target, |options| options.clock = Clock::Manual(1000));
        create(
            &engine,
            "Order",
            json!({"title": "Desk", "quantity": 3}),
            "o1",
        );
        engine.clock().set(2000).unwrap();
        let same = engine
            .instances()
            .update(
                &bob(),
                "Order",
                "o1",
                &json!({"quantity": 3.0}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            (same.seq, same.updated_at, same.updated_by.as_str()),
            (1, 1000, "alice")
        );
        assert_eq!(event_count(&engine), 3);
    }
}

#[test]
fn a_patch_is_a_json_object_and_the_instance_must_exist() {
    for target in targets("a_patch_is_a_json_object_and_the_instance_must_exist") {
        let engine = with_orders(&target, |_| {});
        create(&engine, "Order", json!({"title": "Desk"}), "o1");
        for patch in [json!(["title"]), json!("Lamp"), json!(null)] {
            let refused = refusal(engine.instances().update(
                &alice(),
                "Order",
                "o1",
                &patch,
                WriteOptions::default(),
            ));
            assert_eq!(
                (refused.code(), refused.message()),
                (
                    EngineErrorCode::InvalidArgument,
                    "a merge patch of an instance is a JSON object"
                )
            );
        }
        let missing = refusal(engine.instances().update(
            &alice(),
            "Order",
            "o2",
            &json!({"title": "Lamp"}),
            WriteOptions::default(),
        ));
        assert_eq!(
            (missing.code(), missing.message()),
            (
                EngineErrorCode::NotFound,
                "Order o2 does not exist in namespace default"
            )
        );
    }
}

#[test]
fn delete_removes_the_instance_and_a_second_delete_finds_nothing() {
    for target in targets("delete_removes_the_instance") {
        let engine = with_orders(&target, |_| {});
        create(&engine, "Order", json!({"title": "Desk"}), "o1");
        let instances = engine.instances();
        assert!(instances
            .delete(&alice(), "Order", "o1", WriteOptions::default())
            .unwrap());
        assert_eq!(get(&engine, "Order", "o1"), None);
        assert!(!instances
            .delete(&alice(), "Order", "o1", WriteOptions::default())
            .unwrap());
        assert_eq!(event_count(&engine), 4);
    }
}

#[test]
fn update_and_delete_with_an_expected_sequence_write_only_while_the_instance_is_at_it() {
    for target in targets("update_and_delete_with_an_expected_sequence") {
        let engine = with_orders(&target, |_| {});
        let instances = engine.instances();
        create(&engine, "Order", json!({"title": "Desk"}), "o1");
        assert_eq!(
            instances
                .update(
                    &alice(),
                    "Order",
                    "o1",
                    &json!({"title": "Lamp"}),
                    expected(1)
                )
                .unwrap()
                .seq,
            2
        );

        let stale = refusal(instances.update(
            &alice(),
            "Order",
            "o1",
            &json!({"title": "Chair"}),
            expected(1),
        ));
        assert_eq!(
            (stale.code(), stale.message()),
            (
                EngineErrorCode::SeqMismatch,
                "Order o1 is no longer at sequence 1"
            )
        );
        assert_eq!(stale.status(), 412);
        // A precondition is checked before the patch: an invalid patch at a
        // stale sequence is still seq_mismatch.
        assert_eq!(
            code(instances.update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": null}),
                expected(1)
            )),
            EngineErrorCode::SeqMismatch
        );
        assert_eq!(
            code(instances.delete(&alice(), "Order", "o1", expected(1))),
            EngineErrorCode::SeqMismatch
        );
        // 0 matches no instance: sequences start at 1.
        assert_eq!(
            code(instances.delete(&alice(), "Order", "o1", expected(0))),
            EngineErrorCode::SeqMismatch
        );
        assert_eq!(
            (
                Value::Object(get(&engine, "Order", "o1").unwrap().data),
                event_count(&engine)
            ),
            (json!({"title": "Lamp"}), 4)
        );
        // A patch that changes nothing still needs the sequence.
        assert_eq!(
            instances
                .update(
                    &alice(),
                    "Order",
                    "o1",
                    &json!({"title": "Lamp"}),
                    expected(2)
                )
                .unwrap()
                .seq,
            2
        );
        assert!(instances
            .delete(&alice(), "Order", "o1", expected(2))
            .unwrap());
        // A missing instance is not_found for an update and false for a
        // delete, whatever the sequence.
        assert_eq!(
            code(instances.update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": "Desk"}),
                expected(3)
            )),
            EngineErrorCode::NotFound
        );
        assert!(!instances
            .delete(&alice(), "Order", "o1", expected(3))
            .unwrap());
        let refused = refusal(instances.update(&alice(), "Order", "o1", &json!({}), expected(-1)));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::InvalidArgument,
                "an expected sequence is a non-negative integer, got -1"
            )
        );
        assert_eq!(
            code(instances.delete(&alice(), "Order", "o1", expected(-1))),
            EngineErrorCode::InvalidArgument
        );
    }
}

#[test]
fn an_expected_sequence_is_checked_against_the_storage_inside_the_write_not_against_what_the_caller_read(
) {
    for target in targets("an_expected_sequence_is_checked_against_the_storage") {
        let first = target.open();
        publish(&first, &order_document(), None);
        create(&first, "Order", json!({"title": "Desk"}), "o1");
        let read = get(&first, "Order", "o1").unwrap();
        // A second engine (a connection, or a process on Postgres) writes
        // between the read and the update.
        let second = target.open();
        second
            .instances()
            .update(
                &bob(),
                "Order",
                "o1",
                &json!({"title": "Lamp"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            code(first.instances().update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": "Chair"}),
                expected(read.seq)
            )),
            EngineErrorCode::SeqMismatch
        );
        assert_eq!(
            Value::Object(get(&first, "Order", "o1").unwrap().data),
            json!({"title": "Lamp"})
        );
    }
}

#[test]
fn an_id_created_again_after_a_delete_continues_its_sequence() {
    for target in targets("an_id_created_again_after_a_delete") {
        let engine = with_orders(&target, |_| {});
        create(&engine, "Order", json!({"title": "Desk"}), "o1");
        engine
            .instances()
            .delete(&alice(), "Order", "o1", WriteOptions::default())
            .unwrap();
        assert_eq!(
            create(&engine, "Order", json!({"title": "Lamp"}), "o1").seq,
            3
        );
    }
}

#[test]
fn an_instance_keeps_the_version_it_was_written_with_until_it_is_written_again() {
    for target in targets("an_instance_keeps_the_version_it_was_written_with") {
        let engine = with_orders(&target, |_| {});
        create(
            &engine,
            "Order",
            json!({"title": "Desk", "quantity": 2}),
            "o1",
        );
        let mut next = order_document();
        next["types"]["Order"]["fields"]
            .as_array_mut()
            .unwrap()
            .push(json!({"name": "note", "typeRef": {"name": "string"}}));
        publish(&engine, &next, None);
        assert_eq!(get(&engine, "Order", "o1").unwrap().version, 1);
        let updated = engine
            .instances()
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"note": "fragile"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(updated.version, 2);
        let created = engine
            .instances()
            .create(
                &alice(),
                "Order",
                &json!({"title": "Lamp"}),
                CreateOptions::default(),
            )
            .unwrap();
        assert_eq!(created.version, 2);
    }
}

#[test]
fn a_version_that_drops_a_field_stored_instances_hold_is_refused_and_they_still_read() {
    for target in targets("a_version_that_drops_a_field_stored_instances_hold") {
        let engine = with_orders(&target, |_| {});
        create(
            &engine,
            "Order",
            json!({"title": "Desk", "quantity": 7}),
            "o1",
        );
        let mut dropped = order_document();
        dropped["types"]["Order"]["fields"]
            .as_array_mut()
            .unwrap()
            .retain(|field| field["name"] != "quantity");
        let refused = refusal(
            engine
                .schemas()
                .define(&alice(), dropped, DefineOptions::default()),
        );
        assert_eq!(refused.name(), "IncompatibleChangeError");
        assert_eq!(
            refused
                .changes()
                .iter()
                .map(|change| change.message.as_str())
                .collect::<Vec<_>>(),
            ["field Order.quantity is removed"]
        );
        assert_eq!(
            Value::Object(get(&engine, "Order", "o1").unwrap().data),
            json!({"title": "Desk", "quantity": 7})
        );
    }
}

#[test]
fn an_instance_of_a_shared_schema_lives_in_the_namespace_that_creates_it() {
    for target in targets("an_instance_of_a_shared_schema_lives_in_the_namespace_that_creates_it") {
        let engine = target.open_with(|options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into(), "west".into(), "common".into()],
                shared: Some("common".into()),
            }
        });
        publish(&engine, &shipment_document(), Some("common"));
        let created = engine
            .instances()
            .create(
                &alice(),
                "Shipment",
                &json!({"carrier": "Post"}),
                CreateOptions {
                    id: Some("s1"),
                    namespace: Some("east"),
                    behaviors: None,
                },
            )
            .unwrap();
        assert_eq!(
            (
                created.namespace.as_str(),
                created.schema_namespace.as_str(),
                created.version
            ),
            ("east", "common", 1)
        );
        assert_eq!(
            engine
                .instances()
                .get(&alice(), "Shipment", "s1", in_namespace("west"))
                .unwrap(),
            None
        );
        let list = |namespace| {
            engine
                .instances()
                .list(
                    &alice(),
                    "Shipment",
                    ListOptions {
                        namespace: Some(namespace),
                        ..ListOptions::default()
                    },
                )
                .unwrap()
                .items
                .into_iter()
                .map(|item| item.id)
                .collect::<Vec<_>>()
        };
        assert!(list("common").is_empty());
        assert_eq!(list("east"), ["s1"]);
    }
}

fn ids(engine: &Engine, options: ListOptions<'_>) -> (Vec<String>, Option<String>) {
    let page = engine.instances().list(&alice(), "Order", options).unwrap();
    (
        page.items.into_iter().map(|item| item.id).collect(),
        page.next,
    )
}

fn page(limit: i64, cursor: Option<&str>) -> ListOptions<'_> {
    ListOptions {
        limit: Some(limit),
        cursor,
        ..ListOptions::default()
    }
}

#[test]
fn list_pages_through_instances_in_creation_order() {
    for target in targets("list_pages_through_instances_in_creation_order") {
        let engine = with_orders(&target, |_| {});
        for id in ["g", "a", "f", "b", "e", "c", "d"] {
            create(&engine, "Order", json!({"title": id}), id);
        }
        let (first, next) = ids(&engine, page(3, None));
        assert_eq!(first, ["g", "a", "f"]);
        let (second, next) = ids(&engine, page(3, next.as_deref()));
        assert_eq!(second, ["b", "e", "c"]);
        assert_eq!(
            ids(&engine, page(3, next.as_deref())),
            (vec!["d".to_owned()], None)
        );
    }
}

#[test]
fn a_page_holds_50_by_default_and_at_most_500() {
    for target in targets("a_page_holds_50_by_default_and_at_most_500") {
        let engine = with_orders(&target, |_| {});
        for index in 0..51 {
            create(
                &engine,
                "Order",
                json!({"title": format!("order {index}")}),
                &format!("o{index}"),
            );
        }
        let (items, next) = ids(&engine, ListOptions::default());
        assert_eq!(items.len(), 50);
        assert_eq!(
            ids(
                &engine,
                ListOptions {
                    cursor: next.as_deref(),
                    ..ListOptions::default()
                }
            ),
            (vec!["o50".to_owned()], None)
        );
        assert_eq!(ids(&engine, page(500, None)).0.len(), 51);
        for limit in [0, 501] {
            let refused = refusal(
                engine
                    .instances()
                    .list(&alice(), "Order", page(limit, None)),
            );
            assert_eq!(
                (refused.code(), refused.message()),
                (
                    EngineErrorCode::InvalidArgument,
                    format!("a page size is an integer from 1 to 500, got {limit}").as_str()
                )
            );
        }
    }
}

#[test]
fn an_instance_created_or_deleted_while_a_client_pages_moves_no_other_instance() {
    for target in targets("an_instance_created_or_deleted_while_a_client_pages") {
        let engine = with_orders(&target, |_| {});
        for id in ["a", "b", "c", "d", "e"] {
            create(&engine, "Order", json!({"title": id}), id);
        }
        let (first, next) = ids(&engine, page(2, None));
        assert_eq!(first, ["a", "b"]);
        engine
            .instances()
            .delete(&alice(), "Order", "a", WriteOptions::default())
            .unwrap();
        engine
            .instances()
            .delete(&alice(), "Order", "c", WriteOptions::default())
            .unwrap();
        create(&engine, "Order", json!({"title": "f"}), "f");
        create(&engine, "Order", json!({"title": "again"}), "a");
        let (second, next) = ids(&engine, page(2, next.as_deref()));
        assert_eq!(second, ["d", "e"]);
        assert_eq!(
            ids(&engine, page(2, next.as_deref())),
            (vec!["f".to_owned(), "a".to_owned()], None)
        );
    }
}

#[test]
fn a_list_stays_inside_its_namespace_and_schema() {
    for target in targets("a_list_stays_inside_its_namespace_and_schema") {
        let engine = target.open_with(|options| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into()],
                shared: None,
            }
        });
        publish(&engine, &order_document(), None);
        publish(&engine, &order_document(), Some("east"));
        publish(&engine, &shipment_document(), None);
        create(&engine, "Order", json!({"title": "Desk"}), "here");
        engine
            .instances()
            .create(
                &alice(),
                "Order",
                &json!({"title": "Lamp"}),
                CreateOptions {
                    id: Some("there"),
                    namespace: Some("east"),
                    behaviors: None,
                },
            )
            .unwrap();
        create(&engine, "Shipment", json!({"carrier": "Post"}), "elsewhere");
        assert_eq!(ids(&engine, ListOptions::default()).0, ["here"]);
        assert_eq!(
            ids(
                &engine,
                ListOptions {
                    namespace: Some("east"),
                    ..ListOptions::default()
                }
            )
            .0,
            ["there"]
        );
    }
}

#[test]
fn a_cursor_is_one_the_engine_returned() {
    for target in targets("a_cursor_is_one_the_engine_returned") {
        let engine = with_orders(&target, |_| {});
        // The last is base64url of `after:-1`.
        for cursor in ["", "garbage", "YWZ0ZXI6LTE"] {
            let refused = refusal(engine.instances().list(
                &alice(),
                "Order",
                ListOptions {
                    cursor: Some(cursor),
                    ..ListOptions::default()
                },
            ));
            assert_eq!(
                (refused.code(), refused.message()),
                (
                    EngineErrorCode::InvalidArgument,
                    "the list cursor is not one this engine returned"
                )
            );
        }
    }
}

#[test]
fn a_create_names_no_behavior_the_type_does_not_compose() {
    for target in targets("a_create_names_no_behavior_the_type_does_not_compose") {
        let engine = with_orders(&target, |_| {});
        let behaviors = json!({"Workflow": {}});
        let refused = refusal(engine.instances().create(
            &alice(),
            "Order",
            &json!({"title": "Desk"}),
            CreateOptions {
                behaviors: Some(&behaviors),
                ..CreateOptions::default()
            },
        ));
        assert_eq!(refused.name(), "CreateParamsError");
        assert_eq!(
            refused.message(),
            "create of Order: /behaviors/Workflow: Order does not compose behavior Workflow (its behaviors that take create parameters: none)"
        );
        let preconditions = json!({"Lease": {"token": 7}});
        create(&engine, "Order", json!({"title": "Desk"}), "o1");
        let refused = refusal(engine.instances().update(
            &alice(),
            "Order",
            "o1",
            &json!({"title": "Lamp"}),
            WriteOptions {
                preconditions: Some(&preconditions),
                ..WriteOptions::default()
            },
        ));
        assert_eq!(refused.name(), "PreconditionsError");
        assert_eq!(
            refused.message(),
            "preconditions of Order: /Lease: Order composes no behavior Lease"
        );
    }
}
