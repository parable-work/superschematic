//! Opening an engine: its migrations, from an empty storage and from the
//! storage each earlier version of the engine left (the cases of
//! `test/engine-migrations.test.ts` the registry can check), migration 10
//! building the indexes of unique fields from the stored schema documents,
//! a storage newer than the build, reopening, and the busy refusal; on
//! SQLite and, with the variable set, on Postgres.
//!
//! milestone 4b: the seeds' checks through instances and the event log
//! (an update of a seeded instance, its events read back with their causes
//! and value refs, a lookup by a unique field, a unique conflict, retention
//! reading from the floor). milestone 5: the checks that publish a schema
//! with a test behavior on the migrated storage and run its operations
//! (checkOperationEvents, test.Holder's references). milestone 6: the
//! runner's subscriptions on a migrated storage.

mod support;

use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use serde_json::{json, Value};

use superschematic_engine::json::{canonical_json, sha256_hex};
use superschematic_engine::storage::{
    engine_migrations, Db, Dialect, Lock, Migration, MigrationSet, Sql, SqlValue, Tx, ENGINE_OWNER,
};
use superschematic_engine::{
    Clock, DefineOptions, Engine, EngineErrorCode, Error, NamespaceOrigin, NamespaceRecord,
    NamespaceState, SchemaTarget,
};
use superschematic_schema_ir::schemafile::load_schema_file;
use support::{alice, code, count, order_document, refusal, schema_document, targets, texts, with};

const ANY: SchemaTarget<'static> = SchemaTarget { namespace: None };

const LATEST: usize = 12;

fn note_document() -> Value {
    schema_document(
        "Note",
        json!([{"name": "body", "typeRef": {"name": "string"}}]),
        json!({}),
    )
}

// A Model whose slug is @unique, which version 9 stored and ignored.
fn model_document() -> Value {
    schema_document(
        "Model",
        json!([
            {"name": "slug", "typeRef": {"name": "string"}, "unique": true},
            {"name": "title", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    )
}

/// A document's canonical text, as the loader writes it, and its hash.
fn canonical(document: &Value) -> (String, String) {
    let text = load_schema_file(&document.to_string(), "schema")
        .unwrap()
        .canonical;
    let hash = sha256_hex(&text);
    (text, hash)
}

/// The engine's first `to` migrations, with migration 10 doing nothing (a
/// seed's storage holds no published schema before it).
fn migrations_to(to: usize) -> MigrationSet {
    let all = engine_migrations(|_| Ok(()));
    MigrationSet::new(all.owner.clone(), all.migrations[..to].to_vec())
}

fn values(list: &[Value]) -> Vec<SqlValue> {
    list.iter()
        .map(|value| match value {
            Value::Null => SqlValue::Null,
            Value::Number(number) => SqlValue::Integer(number.as_i64().unwrap()),
            Value::String(text) => SqlValue::from(text.as_str()),
            other => panic!("no SQL value for {other}"),
        })
        .collect()
}

/// A row's values from a JSON list.
fn row(list: Value) -> Vec<Value> {
    match list {
        Value::Array(items) => items,
        other => vec![other],
    }
}

fn run(tx: &Tx<'_>, sql: &str, params: &[Value]) {
    tx.run(sql, &values(params))
        .unwrap_or_else(|error| panic!("{sql}: {error}"));
}

const INSERT_SCHEMA: &str = "INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, defined_by, published_at, published_by)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)";
const INSERT_INSTANCE: &str = "INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)";
const INSERT_EVENT: &str =
    "INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)";

// The rows each version of the engine wrote, as it wrote them.
fn seed(tx: &Tx<'_>, version: usize) {
    let (order, order_hash) = canonical(&order_document());
    let order_row = || {
        row(json!([
            "default", "Order", 1, order, order_hash, 100, "alice", 200, "alice"
        ]))
    };
    match version {
        0 => {}
        // Version 1 stored schemas without actors, and had no instances or
        // events.
        1 => {
            let (note, note_hash) = canonical(&note_document());
            run(
                tx,
                "INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, published_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
                &[json!("default"), json!("Order"), json!(1), json!(order), json!(order_hash), json!(100), json!(200)],
            );
            run(
                tx,
                "INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, published_at) VALUES (?, ?, ?, ?, ?, ?, NULL)",
                &[json!("default"), json!("Note"), json!(0), json!(note), json!(note_hash), json!(300)],
            );
        }
        // Version 2 added actors, instances and the event log; version 3
        // indexed the publish events alone; version 4 added operation
        // events and behavior storage; version 5 references; their rows are
        // version 2's.
        2..=5 => {
            run(tx, INSERT_SCHEMA, &order_row());
            run(
                tx,
                INSERT_INSTANCE,
                &row(json!(["default", "Order", "o1", "default", 1, 1, "{\"title\":\"Desk\"}", 300, "alice", 300, "alice"])),
            );
            run(tx, INSERT_EVENT, &row(json!(["publish", "default", "Order", null, null, 1, "alice", 200, order])));
            run(tx, INSERT_EVENT, &row(json!(["create", "default", "Order", "o1", 1, 1, "alice", 300, "{\"title\":\"Desk\"}"])));
        }
        // Version 6 added subscriptions, schedules and the causes of the
        // runner's events. It had no define events and recorded no
        // service.
        6 => {
            run(tx, INSERT_SCHEMA, &order_row());
            run(
                tx,
                INSERT_INSTANCE,
                &row(json!(["default", "Order", "o1", "default", 1, 2, "{\"title\":\"Lamp\"}", 300, "alice", 400, "runner"])),
            );
            let insert = "INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change, cause_behavior, cause_event, depth)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)";
            run(tx, insert, &row(json!(["publish", "default", "Order", null, null, 1, "alice", 200, order, null, null, 0])));
            run(tx, insert, &row(json!(["create", "default", "Order", "o1", 1, 1, "alice", 300, "{\"title\":\"Desk\"}", null, null, 0])));
            run(tx, insert, &row(json!(["update", "default", "Order", "o1", 2, 1, "runner", 400, "{\"title\":\"Lamp\"}", "test.Ledger", 2, 1])));
            run(
                tx,
                "INSERT INTO engine_subscriptions (behavior, namespace, schema, cursor) VALUES (?, ?, ?, ?)",
                &[json!("test.Ledger"), json!("default"), json!("Order"), json!(3)],
            );
        }
        // Version 7 recorded references that hear every change.
        7 => run(
            tx,
            "INSERT INTO engine_references (namespace, target_schema, target_id, source_schema, source_id, behavior, key) VALUES (?, ?, ?, ?, ?, ?, ?)",
            &[json!("default"), json!("Item"), json!("i1"), json!("Note"), json!("n1"), json!("test.Holder"), json!("desk")],
        ),
        // Version 8 kept every field inline, however large.
        8 => {
            let data = json!({"title": "Bulk", "lines": bulk_lines()}).to_string();
            run(tx, INSERT_SCHEMA, &order_row());
            run(
                tx,
                INSERT_INSTANCE,
                &row(json!(["default", "Order", "o1", "default", 1, 1, data, 300, "alice", 300, "alice"])),
            );
            run(tx, INSERT_EVENT, &row(json!(["publish", "default", "Order", null, null, 1, "alice", 200, order])));
            run(tx, INSERT_EVENT, &row(json!(["create", "default", "Order", "o1", 1, 1, "alice", 300, data])));
        }
        // Version 9 ignored @unique, and kept a long slug by hash in the
        // row.
        9 => write_models(tx, &[]),
        // Version 10 had no namespaces but the configured ones, kept every
        // event, and found where a subscription starts from its publish
        // event.
        10 => {
            run(tx, INSERT_SCHEMA, &order_row());
            run(
                tx,
                INSERT_INSTANCE,
                &row(json!(["default", "Order", "o1", "default", 1, 2, "{\"title\":\"Lamp\"}", 300, "alice", 400, "alice"])),
            );
            run(tx, INSERT_EVENT, &row(json!(["publish", "default", "Order", null, null, 1, "alice", 200, order])));
            run(tx, INSERT_EVENT, &row(json!(["create", "default", "Order", "o1", 1, 1, "alice", 300, "{\"title\":\"Desk\"}"])));
            run(tx, INSERT_EVENT, &row(json!(["update", "default", "Order", "o1", 2, 1, "alice", 400, "{\"title\":\"Lamp\"}"])));
        }
        // Version 11 kept the namespaces a create made, and every event.
        11 => {
            run(
                tx,
                "INSERT INTO engine_namespaces (name, created_at, created_by, archived_at, archived_by) VALUES (?, ?, ?, ?, ?)",
                &[json!("acme"), json!(100), json!("alice"), json!(200), json!("bob")],
            );
            run(
                tx,
                "INSERT INTO engine_namespaces (name, created_at, created_by) VALUES (?, ?, ?)",
                &[json!("beta"), json!(150), json!("alice")],
            );
            run(
                tx,
                INSERT_SCHEMA,
                &row(json!(["beta", "Order", 1, order, order_hash, 160, "alice", 170, "alice"])),
            );
            run(tx, INSERT_EVENT, &row(json!(["publish", "beta", "Order", null, null, 1, "alice", 170, order])));
        }
        _ => panic!("add a seed for engine storage version {version}"),
    }
}

/// bulk_lines is an order's lines whose JSON is past the default
/// threshold, 64 KiB.
fn bulk_lines() -> Value {
    Value::Array(
        (0..3000)
            .map(|index| json!({"sku": format!("SKU-{index:05}"), "count": index + 1}))
            .collect(),
    )
}

/// long_slug is a slug a version 9 engine with a 1 KiB threshold stored by
/// hash.
fn long_slug() -> String {
    format!("models/{}", "x".repeat(2000))
}

// write_models writes version 1 of Model and its instances, as version 9
// wrote them: a by its slug inline, b by its slug's ref, and the extra
// rows given.
fn write_models(tx: &Tx<'_>, rows: &[(&str, &str)]) {
    let (model, model_hash) = canonical(&model_document());
    run(
        tx,
        INSERT_SCHEMA,
        &row(json!([
            "default", "Model", 1, model, model_hash, 100, "alice", 200, "alice"
        ])),
    );
    let slug_json = canonical_json(&json!(long_slug())).unwrap();
    let hash = sha256_hex(&slug_json);
    let reference = json!({"slug": {"$value": hash, "bytes": slug_json.len()}}).to_string();
    run(
        tx,
        "INSERT INTO engine_payloads (hash, value, bytes) VALUES (?, ?, ?)",
        &[json!(hash), json!(slug_json), json!(slug_json.len())],
    );
    let instance = "INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by, value_refs)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)";
    let event = "INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change, value_refs) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)";
    let holder = "INSERT INTO engine_payload_holders (hash, namespace, schema, holder, id, key) VALUES (?, ?, ?, ?, ?, ?)";
    run(
        tx,
        event,
        &row(json!([
            "publish", "default", "Model", null, null, 1, "alice", 200, model, null
        ])),
    );
    run(
        tx,
        instance,
        &row(json!([
            "default",
            "Model",
            "a",
            "default",
            1,
            1,
            "{\"slug\":\"models/plain\"}",
            300,
            "alice",
            300,
            "alice",
            null
        ])),
    );
    run(
        tx,
        event,
        &row(json!([
            "create",
            "default",
            "Model",
            "a",
            1,
            1,
            "alice",
            300,
            "{\"slug\":\"models/plain\"}",
            null
        ])),
    );
    run(
        tx,
        instance,
        &row(json!([
            "default",
            "Model",
            "b",
            "default",
            1,
            1,
            reference,
            300,
            "alice",
            300,
            "alice",
            "[\"/slug\"]"
        ])),
    );
    let created = tx
        .get(
            "INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change, value_refs) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING cursor",
            &values(&row(json!(["create", "default", "Model", "b", 1, 1, "alice", 300, reference, "[\"/slug\"]"]))),
        )
        .unwrap()
        .unwrap()
        .i64("cursor")
        .unwrap();
    run(
        tx,
        holder,
        &row(json!([hash, "default", "Model", "instance", "b", ""])),
    );
    run(
        tx,
        holder,
        &row(json!([
            hash,
            "default",
            "Model",
            "event",
            "b",
            created.to_string()
        ])),
    );
    for (id, data) in rows {
        run(
            tx,
            instance,
            &row(json!([
                "default", "Model", id, "default", 1, 1, data, 300, "alice", 300, "alice", null
            ])),
        );
    }
}

// Each seed's rows, read through the engine after the migrations.
fn check(engine: &Engine, version: usize) {
    match version {
        0 => check_empty(engine),
        1 => check_version_1(engine),
        2..=5 | 8 => check_log(engine, version),
        6 => check_version_6(engine),
        7 => check_version_7(engine),
        9 => check_version_9(engine),
        10 => check_version_10(engine),
        11 => check_version_11(engine),
        _ => panic!("add a check for engine storage version {version}"),
    }
}

fn check_empty(engine: &Engine) {
    assert!(engine.schemas().list(&alice(), ANY).unwrap().is_empty());
}

fn check_version_1(engine: &Engine) {
    let db = engine.db();
    let order = engine
        .schemas()
        .live(&alice(), "Order", ANY)
        .unwrap()
        .unwrap();
    assert_eq!(order.version, Some(1));
    assert_eq!(order.defined_at, 100);
    assert_eq!(order.defined_by, None);
    assert_eq!(order.published_by, None);
    assert_eq!(
        engine
            .schemas()
            .draft(&alice(), "Note", ANY)
            .unwrap()
            .unwrap()
            .defined_by,
        None
    );
    assert_eq!(count(db, "SELECT COUNT(*) AS n FROM engine_events"), 0);
    assert_eq!(
        engine
            .schemas()
            .publish(&alice(), "Note", ANY)
            .unwrap()
            .version,
        1
    );
    assert_eq!(
        texts(
            db,
            "SELECT kind || ' ' || schema FROM engine_events ORDER BY cursor"
        ),
        ["publish Note"]
    );
}

fn check_log(engine: &Engine, version: usize) {
    let db = engine.db();
    assert_eq!(
        texts(
            db,
            "SELECT CAST(cursor AS TEXT) || ' ' || kind FROM engine_events ORDER BY cursor"
        ),
        ["1 publish", "2 create"]
    );
    assert_eq!(
        engine
            .schemas()
            .live(&alice(), "Order", ANY)
            .unwrap()
            .unwrap()
            .defined_by
            .as_deref(),
        Some("alice")
    );
    if version == 8 {
        // The row reads as it was written, inline.
        let data = texts(db, "SELECT data FROM engine_instances WHERE id = 'o1'");
        let stored: Value = serde_json::from_str(&data[0]).unwrap();
        assert_eq!(stored["lines"], bulk_lines());
    }
}

fn check_version_6(engine: &Engine) {
    let db = engine.db();
    assert_eq!(
            texts(
                db,
                "SELECT CAST(cursor AS TEXT) || ' ' || kind || ' ' || COALESCE(cause_behavior, '-') || ' ' || COALESCE(service, '-') FROM engine_events ORDER BY cursor"
            ),
            ["1 publish - -", "2 create - -", "3 update test.Ledger -"]
        );
    let draft = engine
        .schemas()
        .define(
            &alice(),
            with(&order_document(), "description", json!("a draft")),
            DefineOptions::default(),
        )
        .unwrap();
    assert_eq!(
            texts(db, "SELECT CAST(cursor AS TEXT) || ' ' || kind || ' ' || COALESCE(CAST(version AS TEXT), 'null') FROM engine_events WHERE cursor > 3"),
            ["4 define null"]
        );
    assert_eq!(
        texts(db, "SELECT change FROM engine_events WHERE cursor = 4"),
        [format!("{{\"hash\":\"{}\"}}", draft.hash)]
    );
}

fn check_version_7(engine: &Engine) {
    let db = engine.db();
    assert_eq!(
        count(
            db,
            "SELECT COUNT(*) AS n FROM engine_references WHERE hears IS NULL AND crosses IS NULL"
        ),
        1
    );
}

fn check_version_9(engine: &Engine) {
    let db = engine.db();
    // Migration 10 indexed the slug and put b's back inline in its
    // row.
    let row = db
        .read(|tx| {
            tx.get(
                "SELECT data, value_refs FROM engine_instances WHERE id = 'b'",
                &[],
            )
            .map(Option::unwrap)
        })
        .unwrap();
    let data: Value = serde_json::from_str(row.text("data").unwrap()).unwrap();
    assert_eq!(data, json!({"slug": long_slug()}));
    assert_eq!(row.opt_text("value_refs").unwrap(), None);
    assert_eq!(
        count(
            db,
            "SELECT COUNT(*) AS n FROM engine_payload_holders WHERE holder = 'instance'"
        ),
        0,
        "b's row holds no value; its create event still does"
    );
    assert_eq!(
        count(
            db,
            "SELECT COUNT(*) AS n FROM engine_payload_holders WHERE holder = 'event'"
        ),
        1
    );
    assert_eq!(count(db, "SELECT COUNT(*) AS n FROM engine_payloads"), 1);
    // The unique index refuses a second slug models/plain.
    let duplicate = db.write(Lock::Namespace("default"), |tx| {
            tx.run(
                "INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
                 VALUES ('default', 'Model', 'c', 'default', 1, 1, '{\"slug\":\"models/plain\"}', 1, 'alice', 1, 'alice')",
                &[],
            )
        });
    assert!(
        matches!(&duplicate, Err(error) if error.is_unique()),
        "{duplicate:?}"
    );
    assert_eq!(unique_indexes(db), 1);
}

fn check_version_10(engine: &Engine) {
    let db = engine.db();
    // The version keeps its publish's cursor, which the migration
    // read from the log.
    assert_eq!(
        count(
            db,
            "SELECT published_cursor FROM engine_schemas WHERE name = 'Order' AND version = 1"
        ),
        1
    );
    // The namespaces are the configured ones, and a create makes
    // one.
    let listed: Vec<(String, NamespaceOrigin)> = engine
        .namespaces()
        .list(&alice())
        .unwrap()
        .into_iter()
        .map(|record| (record.name, record.origin))
        .collect();
    assert_eq!(
        listed,
        [("default".to_owned(), NamespaceOrigin::Configured)]
    );
    assert_eq!(
        engine.namespaces().create(&alice(), "acme").unwrap().origin,
        NamespaceOrigin::Created
    );
}

fn check_version_11(engine: &Engine) {
    let db = engine.db();
    assert_eq!(
        engine.namespaces().names().unwrap(),
        ["default", "acme", "beta"]
    );
    assert_eq!(
        engine.namespaces().get(&alice(), "acme").unwrap(),
        NamespaceRecord {
            name: "acme".into(),
            origin: NamespaceOrigin::Created,
            shared: false,
            state: NamespaceState::Archived,
            created_at: Some(100),
            created_by: Some("alice".into()),
            archived_at: Some(200),
            archived_by: Some("bob".into()),
        }
    );
    assert_eq!(
            count(db, "SELECT published_cursor FROM engine_schemas WHERE namespace = 'beta' AND name = 'Order'"),
            1
        );
    let acme = DefineOptions {
        namespace: Some("acme"),
        source: None,
    };
    assert_eq!(
        code(engine.schemas().define(&alice(), order_document(), acme)),
        EngineErrorCode::NamespaceArchived
    );
    assert_eq!(
        engine
            .namespaces()
            .unarchive(&alice(), "acme")
            .unwrap()
            .state,
        NamespaceState::Active
    );
    assert_eq!(
        engine
            .schemas()
            .define(&alice(), order_document(), acme)
            .unwrap()
            .namespace,
        "acme"
    );
}

fn unique_indexes(db: &Db) -> i64 {
    db.read(|tx| {
        let row = tx.get(
            Sql::per(
                "SELECT COUNT(*) AS n FROM sqlite_master WHERE type = 'index' AND name LIKE 'engine_unique_%'",
                "SELECT COUNT(*) AS n FROM pg_catalog.pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'engine_unique_%'",
            ),
            &[],
        )?;
        row.unwrap().i64("n")
    })
    .unwrap()
}

// The indexes and triggers of engine_events the engine relies on.
fn event_log_objects(db: &Db) -> Vec<String> {
    let sql = match db.dialect() {
        Dialect::Sqlite => "SELECT type || ' ' || name FROM sqlite_master WHERE tbl_name = 'engine_events' AND type IN ('index', 'trigger') ORDER BY type, name",
        Dialect::Postgres => "SELECT 'index ' || indexname FROM pg_catalog.pg_indexes WHERE schemaname = current_schema() AND tablename = 'engine_events' AND indexname <> 'engine_events_pkey'
             UNION ALL SELECT 'trigger ' || trigger_name FROM information_schema.triggers WHERE trigger_schema = current_schema() AND event_object_table = 'engine_events' GROUP BY trigger_name
             ORDER BY 1",
    };
    texts(db, sql)
}

const EVENT_LOG_OBJECTS: [&str; 6] = [
    "index engine_events_instance",
    "index engine_events_namespace",
    "index engine_events_publish",
    "index engine_events_schema",
    "trigger engine_events_no_delete",
    "trigger engine_events_no_update",
];

fn ledger(db: &Db) -> Vec<u32> {
    db.applied_migrations(ENGINE_OWNER)
        .unwrap()
        .into_iter()
        .map(|row| row.version)
        .collect()
}

#[test]
fn from_each_earlier_version() {
    for from in 0..LATEST {
        for target in targets(&format!("from_each_earlier_version {from}")) {
            {
                let db = target.db();
                db.migrate(&migrations_to(from), 0).unwrap();
                db.write(Lock::Catalog, |tx| {
                    seed(tx, from);
                    Ok::<_, superschematic_engine::storage::Error>(())
                })
                .unwrap();
            }
            let engine = target.open();
            assert_eq!(
                ledger(engine.db()),
                (1..=12).collect::<Vec<u32>>(),
                "from {from}"
            );
            check(&engine, from);
            assert_eq!(
                event_log_objects(engine.db()),
                EVENT_LOG_OBJECTS,
                "from {from}"
            );
            engine
                .schemas()
                .define(
                    &alice(),
                    with(
                        &order_document(),
                        "description",
                        json!("after the migration"),
                    ),
                    DefineOptions::default(),
                )
                .unwrap();
            assert!(
                engine
                    .schemas()
                    .publish(&alice(), "Order", ANY)
                    .unwrap()
                    .published,
                "from {from}"
            );
            // The log now holds an event whatever the seed wrote, so its
            // triggers fire.
            for statement in [
                "DELETE FROM engine_events",
                "UPDATE engine_events SET actor = 'mallory'",
            ] {
                let refused = engine
                    .db()
                    .write(Lock::Catalog, |tx| tx.run(statement, &[]));
                assert!(
                    refused.as_ref().is_err_and(|error| error
                        .to_string()
                        .contains("engine_events is append-only")),
                    "from {from}: {statement}: {refused:?}"
                );
            }
            // A define has no instance and no version, and only a define
            // lacks a version.
            let insert = "INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)";
            for row in [
                json!(["define", "default", "Order", null, null, 1, "mallory", 1]),
                json!(["define", "default", "Order", "o9", 1, null, "mallory", 1]),
                json!(["publish", "default", "Order", null, null, null, "mallory", 1]),
                json!(["create", "default", "Order", "o9", 1, null, "mallory", 1]),
            ] {
                let refused = engine.db().write(Lock::Catalog, |tx| {
                    tx.run(insert, &values(row.as_array().unwrap()))
                });
                let message = refused
                    .as_ref()
                    .map_err(ToString::to_string)
                    .err()
                    .unwrap_or_default();
                assert!(
                    message.contains("CHECK constraint failed")
                        || message.contains("violates check constraint"),
                    "from {from}: {row}: {message}"
                );
            }
        }
    }
}

#[test]
fn from_an_empty_storage_every_migration_runs_and_reopening_runs_none() {
    for target in targets("from_an_empty_storage_every_migration_runs_and_reopening_runs_none") {
        let engine = target.open_with(|options| options.clock = Clock::Manual(42));
        assert_eq!(ledger(engine.db()), (1..=12).collect::<Vec<u32>>());
        let stamps = engine.db().applied_migrations(ENGINE_OWNER).unwrap();
        assert!(stamps.iter().all(|row| row.applied_at == 42));
        drop(engine);
        let again = target.open_with(|options| options.clock = Clock::Manual(99));
        assert!(again
            .db()
            .applied_migrations(ENGINE_OWNER)
            .unwrap()
            .iter()
            .all(|row| row.applied_at == 42));
    }
}

#[test]
fn migration_10_refuses_a_storage_whose_instances_break_a_unique_field_the_engine_ignored_before() {
    for target in targets("migration_10_refuses_a_storage_whose_instances_break_a_unique_field_the_engine_ignored_before") {
        {
            let db = target.db();
            db.migrate(&migrations_to(9), 0).unwrap();
            db.write(Lock::Catalog, |tx| {
                write_models(tx, &[("c", "{\"slug\":\"models/plain\"}"), ("d", "{\"slug\":\"models/plain\"}")]);
                Ok::<_, superschematic_engine::storage::Error>(())
            })
            .unwrap();
        }
        let refused = Engine::open(target.options()).unwrap_err();
        assert!(matches!(refused, Error::Storage(_)), "{refused:?}");
        assert_eq!(
            refused.to_string(),
            "engine migration 10: schema Model in namespace default has unique fields its stored instances break: field Model.slug becomes unique, and 3 instances in namespace default hold slug \"models/plain\". Make the values distinct, or publish a version without the unique field, with the engine before this one"
        );
        // Nothing of the migration stays: the storage is still at version
        // 9, its row b still holds its slug by hash, and no index is made.
        let db = target.db();
        assert_eq!(ledger(&db), (1..=9).collect::<Vec<u32>>());
        assert_eq!(texts(&db, "SELECT value_refs FROM engine_instances WHERE id = 'b'"), ["[\"/slug\"]"]);
        assert_eq!(unique_indexes(&db), 0);
    }
}

#[test]
fn an_engine_whose_storage_is_newer_than_the_build_refuses_to_open_it() {
    for target in targets("an_engine_whose_storage_is_newer_than_the_build_refuses_to_open_it") {
        {
            let db = target.db();
            let mut later = engine_migrations(|_| Ok(()));
            later
                .migrations
                .push(Migration::new(13, "from a later build", |_| Ok(())));
            db.migrate(&later, 0).unwrap();
        }
        let refused = Engine::open(target.options()).unwrap_err();
        assert!(
            refused
                .to_string()
                .contains("migrations do not run backwards"),
            "{refused}"
        );
    }
}

#[test]
fn reopening_an_engine_keeps_its_schemas_and_events() {
    for target in targets("reopening_an_engine_keeps_its_schemas_and_events") {
        let first = target.open();
        first
            .schemas()
            .define(&alice(), order_document(), DefineOptions::default())
            .unwrap();
        first.schemas().publish(&alice(), "Order", ANY).unwrap();
        drop(first);
        let second = target.open();
        assert_eq!(
            second
                .schemas()
                .live(&alice(), "Order", ANY)
                .unwrap()
                .unwrap()
                .version,
            Some(1)
        );
        assert_eq!(
            texts(
                second.db(),
                "SELECT kind FROM engine_events ORDER BY cursor"
            ),
            ["define", "publish"]
        );
        // The publish event holds the version's canonical document, and the
        // version its cursor.
        let record = second
            .schemas()
            .live(&alice(), "Order", ANY)
            .unwrap()
            .unwrap();
        assert_eq!(
            texts(
                second.db(),
                "SELECT change FROM engine_events WHERE kind = 'publish'"
            ),
            [record.canonical]
        );
        assert_eq!(
            count(
                second.db(),
                "SELECT published_cursor FROM engine_schemas WHERE version = 1"
            ),
            2
        );
    }
}

#[test]
fn an_engine_opens_on_a_storage_the_host_opened() {
    for target in targets("an_engine_opens_on_a_storage_the_host_opened") {
        let mut options = superschematic_engine::EngineOptions::new(
            superschematic_engine::Storage::Db(target.db()),
        );
        options.policy = Some(superschematic_engine::Policy::allow_all());
        let engine = Engine::open(options).unwrap();
        assert_eq!(engine.dialect(), target.dialect);
        assert_eq!(ledger(engine.db()), (1..=12).collect::<Vec<u32>>());
    }
}

#[test]
fn ids_are_uuids_or_a_sequence() {
    for target in targets("ids_are_uuids_or_a_sequence") {
        let engine = target.open();
        let id = engine.new_id().unwrap();
        assert_eq!(id.len(), 36);
        assert_eq!(id.chars().nth(14), Some('4'));
        assert_ne!(engine.new_id().unwrap(), id);
        drop(engine);
        let counted = target.open_with(|options| {
            options.ids = superschematic_engine::Ids::Sequence {
                prefix: "order-".into(),
            }
        });
        assert_eq!(
            (counted.new_id().unwrap(), counted.new_id().unwrap()),
            ("order-1".to_owned(), "order-2".to_owned())
        );
        drop(counted);
        // Two engines given one counter draw the recorder's one sequence.
        let drawn = superschematic_engine::IdCounter::new();
        let seeded = |drawn: &superschematic_engine::IdCounter| {
            let drawn = drawn.clone();
            target.open_with(move |options| {
                options.ids = superschematic_engine::Ids::Seeded {
                    prefix: superschematic_engine::SCENARIO_SEED.into(),
                    drawn,
                }
            })
        };
        let (first, second) = (seeded(&drawn), seeded(&drawn));
        assert_eq!(
            first.new_id().unwrap(),
            "81af85d2-eaea-4aa9-9131-9573fd47ad9a"
        );
        assert_eq!(
            second.new_id().unwrap(),
            "adb623a0-36b0-44f2-8f7d-79ba3d38cb0c"
        );
        assert_eq!(drawn.drawn(), 2);
        drop((first, second));
        // Queued ids are drawn in order, and an empty queue refuses.
        let queue = superschematic_engine::IdQueue::new();
        let queued = {
            let queue = queue.clone();
            target.open_with(move |options| options.ids = superschematic_engine::Ids::Queue(queue))
        };
        queue.push("given-1");
        queue.push("given-2");
        assert_eq!(
            (queued.new_id().unwrap(), queued.new_id().unwrap()),
            ("given-1".to_owned(), "given-2".to_owned())
        );
        assert!(matches!(queued.new_id(), Err(Error::Usage(_))));
    }
}

// The busy refusal: a writer that holds the catalog's lock (SQLite's one
// writer) past the busy timeout makes a define wait, then refuse with
// `busy`, on both databases (D58).
#[test]
fn a_lock_held_past_the_busy_timeout_is_the_refusal_busy() {
    for target in targets("a_lock_held_past_the_busy_timeout_is_the_refusal_busy") {
        let engine = Engine::open({
            let mut options = target.options();
            options.storage = target.storage_with(Duration::from_millis(100));
            options
        })
        .unwrap();
        let holder = target.db();
        let (locked, wait) = mpsc::channel();
        let (done, release) = mpsc::channel::<()>();
        let hold = thread::spawn(move || {
            holder
                .write(Lock::Catalog, |_tx| {
                    locked.send(()).unwrap();
                    let _ = release.recv_timeout(Duration::from_secs(10));
                    Ok::<_, superschematic_engine::storage::Error>(())
                })
                .unwrap();
        });
        wait.recv().unwrap();
        let refused = refusal(engine.schemas().define(
            &alice(),
            order_document(),
            DefineOptions::default(),
        ));
        done.send(()).unwrap();
        hold.join().unwrap();
        assert_eq!(refused.code(), EngineErrorCode::Busy, "{refused}");
        assert_eq!(refused.status(), 503);
        assert!(
            refused
                .message()
                .starts_with("the engine's storage is busy"),
            "{refused}"
        );
        // Nothing was written; once the lock is free the define goes on.
        assert!(engine.schemas().list(&alice(), ANY).unwrap().is_empty());
        engine
            .schemas()
            .define(&alice(), order_document(), DefineOptions::default())
            .unwrap();
    }
}
