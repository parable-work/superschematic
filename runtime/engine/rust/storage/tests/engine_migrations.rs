//! The engine's own migrations, from an empty database and from the
//! database each earlier version of the engine left (`engine-migrations
//! .test.ts`, at the storage's level: rows written as that version wrote
//! them must survive the later migrations), on both databases; and on
//! SQLite, the same tables as the TypeScript engine's statements make.

mod support;

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use superschematic_engine_storage::{
    engine_migrations, params, Db, Dialect, Error, Lock, MigrationSet, Row, Sql, SqlValue, Tx,
    ENGINE_OWNER,
};
use support::targets;

const NAMES: [&str; 12] = [
    "schema versions",
    "instances and the event log",
    "publish events by namespace",
    "operation events and behavior storage",
    "references between instances",
    "reactions, schedules and causes",
    "define events and calling services",
    "what a reference hears",
    "the value store",
    "unique fields and indexes",
    "namespaces a create makes",
    "event log retention",
];

const TABLES: [&str; 13] = [
    "engine_behaviors",
    "engine_event_bases",
    "engine_events",
    "engine_instances",
    "engine_log_floors",
    "engine_migrations",
    "engine_namespaces",
    "engine_payload_holders",
    "engine_payloads",
    "engine_references",
    "engine_schedules",
    "engine_schemas",
    "engine_subscriptions",
];

// The indexes and triggers of engine_events the engine relies on.
// Migration 7 rebuilds the table on SQLite, which drops its indexes and
// triggers.
const EVENT_LOG_OBJECTS: [&str; 6] = [
    "index engine_events_instance",
    "index engine_events_namespace",
    "index engine_events_publish",
    "index engine_events_schema",
    "trigger engine_events_no_delete",
    "trigger engine_events_no_update",
];

fn engine() -> MigrationSet {
    engine_migrations(|_| Ok(()))
}

fn slice(set: &MigrationSet, to: usize) -> MigrationSet {
    MigrationSet::new(set.owner.clone(), set.migrations[..to].to_vec())
}

fn tables(db: &Db) -> Vec<String> {
    texts(
        db,
        Sql::per(
            "SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'engine%' ORDER BY name",
            "SELECT tablename AS name FROM pg_catalog.pg_tables WHERE schemaname = current_schema() ORDER BY tablename",
        ),
    )
}

fn event_log_objects(db: &Db) -> Vec<String> {
    texts(
        db,
        Sql::per(
            "SELECT type || ' ' || name AS name FROM sqlite_master WHERE tbl_name = 'engine_events' AND type IN ('index', 'trigger') ORDER BY type, name",
            "SELECT name FROM (
               SELECT 'index ' || indexname AS name FROM pg_catalog.pg_indexes
               WHERE schemaname = current_schema() AND tablename = 'engine_events' AND indexname <> 'engine_events_pkey'
               UNION ALL
               SELECT 'trigger ' || tgname FROM pg_catalog.pg_trigger
               WHERE tgrelid = 'engine_events'::regclass AND NOT tgisinternal
             ) AS objects ORDER BY name",
        ),
    )
}

fn texts(db: &Db, sql: Sql<'_>) -> Vec<String> {
    db.read(|tx| tx.all(sql, &[]))
        .unwrap()
        .iter()
        .map(|row| row.text("name").unwrap().to_owned())
        .collect()
}

fn write<'db, T>(db: &'db Db, f: impl FnOnce(&Tx<'db>) -> Result<T, Error>) -> Result<T, Error> {
    db.write(Lock::Catalog, f)
}

#[test]
fn from_an_empty_database() {
    for target in targets("from_an_empty_database") {
        let db = target.open();
        let calls = Arc::new(AtomicUsize::new(0));
        let counted = {
            let calls = Arc::clone(&calls);
            engine_migrations(move |_| {
                calls.fetch_add(1, Ordering::SeqCst);
                Ok(())
            })
        };
        let result = db.migrate(&counted, 7).unwrap();
        assert_eq!(result.applied, (1..=12).collect::<Vec<_>>());
        assert_eq!(
            calls.load(Ordering::SeqCst),
            1,
            "migration 10's function ran once"
        );
        let ledger = db.applied_migrations(ENGINE_OWNER).unwrap();
        assert_eq!(
            ledger
                .iter()
                .map(|row| row.name.as_str())
                .collect::<Vec<_>>(),
            NAMES
        );
        assert!(ledger.iter().all(|row| row.applied_at == 7));
        assert_eq!(tables(&db), TABLES);
        assert_eq!(event_log_objects(&db), EVENT_LOG_OBJECTS);
        // Reopened, there is nothing to do.
        assert!(target
            .open()
            .migrate(&counted, 8)
            .unwrap()
            .applied
            .is_empty());
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        check_event_log(&db, target.dialect);
    }
}

// check_event_log writes events and holds the log to its rules: append
// only, but for what retention prunes at or before a namespace's floor; a
// define has no instance and no version, and only a define lacks one.
fn check_event_log(db: &Db, dialect: Dialect) {
    let insert = "INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)";
    write(db, |tx| {
        tx.run(
            insert,
            params![
                "define",
                "default",
                "Order",
                None::<String>,
                None::<i64>,
                None::<i64>,
                "alice",
                1
            ],
        )?;
        tx.run(
            insert,
            params![
                "publish",
                "default",
                "Order",
                None::<String>,
                None::<i64>,
                1,
                "alice",
                2
            ],
        )?;
        tx.run(
            insert,
            params!["create", "default", "Order", "o1", 1, 1, "alice", 3],
        )?;
        tx.run(
            insert,
            params!["operation", "other", "Order", "o1", 1, 1, "alice", 4],
        )
    })
    .unwrap();
    let refused = |sql: &str, values: &[SqlValue]| {
        write(db, |tx| tx.run(sql, values)).unwrap_err().to_string()
    };
    assert!(refused("DELETE FROM engine_events", &[]).contains("engine_events is append-only"));
    assert!(refused("UPDATE engine_events SET actor = 'mallory'", &[])
        .contains("engine_events is append-only"));
    let check = match dialect {
        Dialect::Sqlite => "CHECK constraint failed",
        Dialect::Postgres => "violates check constraint",
    };
    for row in [
        params![
            "define",
            "default",
            "Order",
            None::<String>,
            None::<i64>,
            1,
            "mallory",
            1
        ],
        params![
            "define",
            "default",
            "Order",
            "o9",
            1,
            None::<i64>,
            "mallory",
            1
        ],
        params![
            "publish",
            "default",
            "Order",
            None::<String>,
            None::<i64>,
            None::<i64>,
            "mallory",
            1
        ],
        params![
            "create",
            "default",
            "Order",
            "o9",
            1,
            None::<i64>,
            "mallory",
            1
        ],
        params!["created", "default", "Order", "o9", 1, 1, "mallory", 1],
    ] {
        let error = refused(insert, row);
        assert!(error.contains(check), "{row:?}: {error}");
    }
    // Retention moves a namespace's floor first; then what is at or before
    // it may go, and nothing after it or in another namespace.
    write(db, |tx| {
        tx.run(
            "INSERT INTO engine_log_floors (namespace, floor) VALUES (?, ?)",
            params!["default", 2],
        )?;
        assert_eq!(
            tx.run(
                "DELETE FROM engine_events WHERE namespace = ? AND cursor <= ?",
                params!["default", 2]
            )?
            .changes,
            2
        );
        Ok(())
    })
    .unwrap();
    assert!(
        refused("DELETE FROM engine_events WHERE namespace = 'default'", &[])
            .contains("append-only")
    );
    assert!(
        refused("DELETE FROM engine_events WHERE namespace = 'other'", &[]).contains("append-only")
    );
}

#[test]
fn migration_10_that_fails_leaves_the_database_at_version_9() {
    for target in targets("migration_10_that_fails") {
        let db = target.open();
        db.migrate(&slice(&engine(), 9), 1).unwrap();
        let clashing = engine_migrations(|tx| {
            tx.exec("CREATE TABLE engine_half_built (id TEXT)")?;
            Err(Error::external(
                "engine migration 10: schema Model in namespace default has unique fields its stored instances break",
            ))
        });
        let error = db.migrate(&clashing, 2).unwrap_err();
        assert!(matches!(error, Error::External(_)), "{error:?}");
        assert!(error.to_string().starts_with("engine migration 10:"));
        let ledger = db.applied_migrations(ENGINE_OWNER).unwrap();
        assert_eq!(ledger.len(), 9);
        assert!(!tables(&db).contains(&"engine_half_built".to_owned()));
        assert_eq!(db.migrate(&engine(), 3).unwrap().applied, [10, 11, 12]);
    }
}

#[test]
fn a_database_newer_than_the_build_is_refused() {
    for target in targets("a_database_newer_than_the_build") {
        let db = target.open();
        let mut later = engine();
        later
            .migrations
            .push(superschematic_engine_storage::Migration::new(
                13,
                "from a later build",
                |_| Ok(()),
            ));
        db.migrate(&later, 1).unwrap();
        let error = db.migrate(&engine(), 1).unwrap_err();
        assert!(
            error
                .to_string()
                .contains("migrations do not run backwards"),
            "{error}"
        );
    }
}

#[test]
fn from_each_earlier_version() {
    for from in 0..NAMES.len() {
        for target in targets("from_each_earlier_version") {
            let db = target.open();
            db.migrate(&slice(&engine(), from), 1).unwrap();
            write(&db, |tx| seed(tx, from)).unwrap();
            let applied = db.migrate(&engine(), 2).unwrap().applied;
            assert_eq!(
                applied,
                ((from as u32 + 1)..=12).collect::<Vec<_>>(),
                "from {from}"
            );
            assert_eq!(event_log_objects(&db), EVENT_LOG_OBJECTS, "from {from}");
            check(&db, from);
        }
    }
}

// seed writes rows into a database at version `version`, as that version
// wrote them: each version's columns and no later one's.
fn seed(tx: &Tx<'_>, version: usize) -> Result<(), Error> {
    if version == 0 {
        return Ok(());
    }
    tx.run(
        "INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, published_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
        params!["default", "Order", 1, r#"{"kind":"General","name":"Order"}"#, "h1", 100, 200],
    )?;
    tx.run(
        "INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, published_at) VALUES (?, ?, ?, ?, ?, ?, NULL)",
        params!["default", "Note", 0, r#"{"kind":"General","name":"Note"}"#, "h2", 300],
    )?;
    if version < 2 {
        return Ok(());
    }
    tx.run(
        "UPDATE engine_schemas SET defined_by = ?, published_by = ? WHERE name = ?",
        params!["alice", "alice", "Order"],
    )?;
    tx.run(
        "INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        params!["default", "Order", "o1", "default", 1, 1, r#"{"title":"Desk"}"#, 300, "alice", 300, "alice"],
    )?;
    let event = "INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)";
    tx.run(
        event,
        params![
            "publish",
            "default",
            "Order",
            None::<String>,
            None::<i64>,
            1,
            "alice",
            200,
            r#"{"kind":"General"}"#
        ],
    )?;
    tx.run(
        event,
        params![
            "create",
            "default",
            "Order",
            "o1",
            1,
            1,
            "alice",
            300,
            r#"{"title":"Desk"}"#
        ],
    )?;
    if version >= 3 {
        tx.run(
            event,
            params![
                "update",
                "default",
                "Order",
                "o1",
                2,
                1,
                "alice",
                400,
                r#"{"title":"Lamp"}"#
            ],
        )?;
        tx.run(
            "UPDATE engine_instances SET seq = 2, data = ? WHERE id = 'o1'",
            params![r#"{"title":"Lamp"}"#],
        )?;
    }
    if version >= 4 {
        tx.run(
            "INSERT INTO engine_behaviors (name, key, created_at) VALUES (?, ?, ?)",
            params!["test.Counter", "test_counter", 500],
        )?;
        tx.run(
            event,
            params![
                "operation",
                "default",
                "Order",
                "o1",
                3,
                1,
                "alice",
                500,
                r#"{"name":"increment"}"#
            ],
        )?;
    }
    if version >= 5 {
        tx.run(
            "INSERT INTO engine_references (namespace, target_schema, target_id, source_schema, source_id, behavior, key) VALUES (?, ?, ?, ?, ?, ?, ?)",
            params!["default", "Order", "o1", "Item", "i1", "test.Holder", ""],
        )?;
    }
    if version >= 6 {
        tx.run(
            "INSERT INTO engine_subscriptions (behavior, namespace, schema, cursor) VALUES (?, ?, ?, ?)",
            params!["test.Ledger", "default", "Order", 2],
        )?;
        tx.run(
            "INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change, cause_behavior, cause_event, depth)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            params!["update", "default", "Order", "o1", 4, 1, "runner", 600, r#"{"marked":true}"#, "test.Ledger", 2, 1],
        )?;
    }
    if version >= 7 {
        tx.run(
            "INSERT INTO engine_events (kind, namespace, schema, actor, at, change, service) VALUES (?, ?, ?, ?, ?, ?, ?)",
            params!["define", "default", "Note", "alice", 700, r#"{"kind":"General"}"#, "svc"],
        )?;
    }
    if version >= 8 {
        tx.run(
            "UPDATE engine_references SET hears = ?, crosses = ?",
            params!["title", 1.5],
        )?;
    }
    if version >= 9 {
        tx.run(
            "INSERT INTO engine_payloads (hash, value, bytes) VALUES (?, ?, ?)",
            params!["p1", r#""big""#, 5],
        )?;
        tx.run(
            "INSERT INTO engine_payload_holders (hash, namespace, schema, holder, id, key) VALUES (?, ?, ?, ?, ?, ?)",
            params!["p1", "default", "Order", "instance", "o1", ""],
        )?;
        tx.run(
            "UPDATE engine_instances SET value_refs = ? WHERE id = 'o1'",
            params![r#"["/body"]"#],
        )?;
    }
    if version >= 11 {
        tx.run(
            "INSERT INTO engine_namespaces (name, created_at, created_by) VALUES (?, ?, ?)",
            params!["shop", 800, "alice"],
        )?;
    }
    Ok(())
}

fn rows(db: &Db, sql: &str) -> Vec<Row> {
    db.read(|tx| tx.all(sql, &[])).unwrap()
}

// check reads, after the later migrations, the rows seed wrote at
// `version`, and writes on.
fn check(db: &Db, version: usize) {
    check_schemas(db, version);
    let logged = check_events(db, version);
    check_rows(db, version);
    // The log's cursors carry on from the seed's, and the rebuilt CHECKs
    // admit a define and an operation.
    let next = write(db, |tx| {
        tx.run(
            "INSERT INTO engine_events (kind, namespace, schema, actor, at) VALUES (?, ?, ?, ?, ?)",
            params!["define", "default", "Order", "alice", 900],
        )?;
        tx.get("SELECT MAX(cursor) AS last FROM engine_events", &[])
    })
    .unwrap()
    .unwrap();
    assert_eq!(next.i64("last").unwrap(), logged + 1, "from {version}");
}

fn check_schemas(db: &Db, version: usize) {
    let schemas = rows(
        db,
        "SELECT name, version, defined_by, published_cursor FROM engine_schemas ORDER BY name",
    );
    if version == 0 {
        assert!(schemas.is_empty());
        return;
    }
    assert_eq!(schemas.len(), 2, "from {version}");
    let order = &schemas[1];
    assert_eq!(order.text("name").unwrap(), "Order");
    // A version-1 database recorded no actors and no publish events.
    let (actor, cursor) = if version >= 2 {
        (Some("alice"), Some(1))
    } else {
        (None, None)
    };
    assert_eq!(
        order.opt_text("defined_by").unwrap(),
        actor,
        "from {version}"
    );
    assert_eq!(
        order.opt_i64("published_cursor").unwrap(),
        cursor,
        "from {version}"
    );
}

// check_events checks the seed's events, their cursors kept, and returns
// how many there are.
fn check_events(db: &Db, version: usize) -> i64 {
    let events = rows(
        db,
        "SELECT cursor, kind, seq, depth, service FROM engine_events ORDER BY cursor",
    );
    let expected: Vec<(i64, &str)> = [
        (2, "publish"),
        (2, "create"),
        (3, "update"),
        (4, "operation"),
        (6, "update"),
        (7, "define"),
    ]
    .into_iter()
    .filter(|(since, _)| version >= *since)
    .enumerate()
    .map(|(index, (_, kind))| (index as i64 + 1, kind))
    .collect();
    assert_eq!(
        events
            .iter()
            .map(|row| (row.i64("cursor").unwrap(), row.text("kind").unwrap()))
            .collect::<Vec<_>>(),
        expected,
        "from {version}: the cursors survive"
    );
    if version >= 6 {
        assert_eq!(events[4].i64("depth").unwrap(), 1);
    }
    if version >= 7 {
        assert_eq!(events[5].opt_text("service").unwrap(), Some("svc"));
    }
    expected.len() as i64
}

fn check_rows(db: &Db, version: usize) {
    let instances = rows(db, "SELECT id, seq, data, value_refs FROM engine_instances");
    if version >= 2 {
        assert_eq!(instances.len(), 1);
        let seq = if version >= 3 { 2 } else { 1 };
        assert_eq!(instances[0].i64("seq").unwrap(), seq);
        let refs = (version >= 9).then_some(r#"["/body"]"#);
        assert_eq!(instances[0].opt_text("value_refs").unwrap(), refs);
    }
    let counts = |table: &str| {
        rows(db, &format!("SELECT COUNT(*) AS n FROM {table}"))[0]
            .i64("n")
            .unwrap()
    };
    assert_eq!(counts("engine_behaviors"), i64::from(version >= 4));
    assert_eq!(counts("engine_references"), i64::from(version >= 5));
    assert_eq!(counts("engine_subscriptions"), i64::from(version >= 6));
    assert_eq!(counts("engine_payloads"), i64::from(version >= 9));
    assert_eq!(counts("engine_namespaces"), i64::from(version >= 11));
    if version >= 8 {
        let reference = &rows(db, "SELECT hears, crosses FROM engine_references")[0];
        assert_eq!(reference.opt_text("hears").unwrap(), Some("title"));
        assert_eq!(reference.opt_f64("crosses").unwrap(), Some(1.5));
    }
}

/// The TypeScript engine's migrations, if its source is still beside the
/// Rust engine's (it goes at the cutover, D58).
#[cfg(feature = "sqlite")]
fn typescript_migrations() -> Option<String> {
    let path =
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../typescript/src/migrations.ts");
    std::fs::read_to_string(path).ok()
}

// The SQL of each `storage.exec(`...`)` in the TypeScript engine's
// migrations, in order: every migration's but the tenth, which builds
// indexes in code.
#[cfg(feature = "sqlite")]
fn typescript_statements(source: &str) -> Vec<String> {
    source
        .split("storage.exec(`")
        .skip(1)
        .map(|rest| {
            rest.split("`)")
                .next()
                .expect("a closing backtick")
                .to_owned()
        })
        .collect()
}

#[cfg(feature = "sqlite")]
#[test]
fn sqlite_makes_the_tables_the_typescript_engines_statements_make() {
    let Some(source) = typescript_migrations() else {
        eprintln!("the TypeScript engine's migrations are gone; nothing to compare");
        return;
    };
    let statements = typescript_statements(&source);
    assert_eq!(statements.len(), 11);
    let theirs = support::Target::sqlite();
    let theirs_db = theirs.open();
    for statement in &statements {
        write(&theirs_db, |tx| tx.exec(statement.as_str())).unwrap();
    }
    let ours = support::Target::sqlite();
    let ours_db = ours.open();
    ours_db.migrate(&engine(), 1).unwrap();
    let schema = |db: &Db| {
        rows(
            db,
            "SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT IN ('engine_migrations', 'sqlite_sequence') AND name NOT LIKE 'sqlite_autoindex_engine_migrations%' ORDER BY type, name",
        )
        .iter()
        .map(|row| row.values().to_vec())
        .collect::<Vec<_>>()
    };
    assert_eq!(
        schema(&ours_db),
        schema(&theirs_db),
        "the same objects, from the same statement text"
    );
}
