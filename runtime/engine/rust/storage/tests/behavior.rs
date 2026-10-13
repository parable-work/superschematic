//! A behavior's storage on both databases: its key and prefix, its
//! migrations (columns and indexes on the instances table, tables of its
//! own, objects of its own alone), and its SQL handle (`behavior-sql.test
//! .ts` and `behavior-own-columns.test.ts` at the storage's level): its
//! own tables, read-only reads, and the relation over the instances.

mod support;

use std::cell::Cell;

use superschematic_engine_storage::behavior::{
    assign_key, migration_set, prefix_of, stored_key, BehaviorMigration, BehaviorSql,
    InstanceRelation,
};
use superschematic_engine_storage::{
    engine_migrations, params, ColumnSpec, Db, Dialect, Error, IndexSpec, Lock, Sql, SqlMode,
    SqlValue, TableSpec, Tx,
};
use support::{targets, Target};

const SNEAKY: &str = "test.Sneaky";

// A migration's up(), as a case of the tests writes it.
type Up = fn(&BehaviorSql<'_, '_>) -> Result<(), Error>;
const PREFIX: &str = "bhv_test_sneaky__";

fn engine(target: &Target) -> Db {
    let db = target.open();
    db.migrate(&engine_migrations(|_| Ok(())), 1).unwrap();
    db
}

fn write<'db, T>(db: &'db Db, f: impl FnOnce(&Tx<'db>) -> Result<T, Error>) -> Result<T, Error> {
    db.write(Lock::Catalog, f)
}

fn tables(db: &Db, prefix: &str) -> Vec<String> {
    db.read(|tx| {
        tx.all(
            Sql::per(
                "SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE ? ORDER BY name",
                "SELECT tablename AS name FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename LIKE ? ORDER BY tablename",
            ),
            params![format!("{prefix}%")],
        )
    })
    .unwrap()
    .iter()
    .map(|row| row.text("name").unwrap().to_owned())
    // FTS5's shadow tables come with its virtual table.
    .filter(|name| !["_data", "_idx", "_content", "_docsize", "_config"].iter().any(|shadow| name.ends_with(shadow)))
    .collect()
}

// notes is a behavior's first migration: a table of its own, by spec, and
// on SQLite an fts5 table.
fn notes() -> BehaviorMigration {
    BehaviorMigration::new(1, "notes")
        .table(
            TableSpec::new("notes")
                .column("id", ColumnSpec::text())
                .column("body", ColumnSpec::text())
                .primary_key(&["id"]),
        )
        .up(|sql| {
            if sql.dialect() == Dialect::Sqlite {
                sql.run(
                    format!(
                        "CREATE VIRTUAL TABLE {} USING fts5(body)",
                        sql.table("search")?
                    ),
                    &[],
                )?;
            }
            Ok(())
        })
}

fn publish_sneaky(db: &Db, more: Vec<BehaviorMigration>) -> Result<(), Error> {
    let key = write(db, |tx| assign_key(tx, SNEAKY, 1))?;
    assert_eq!(prefix_of(&key), PREFIX);
    let mut migrations = vec![notes()];
    migrations.extend(more);
    db.migrate(&migration_set(SNEAKY, PREFIX, &migrations), 1)
        .map(|_| ())
}

#[test]
fn a_behavior_gets_one_key_for_the_life_of_the_database() {
    for target in targets("a_behavior_gets_one_key") {
        let db = engine(&target);
        write(&db, |tx| {
            assert_eq!(stored_key(tx, SNEAKY)?, None);
            assert_eq!(assign_key(tx, SNEAKY, 1)?, "test_sneaky");
            assert_eq!(assign_key(tx, SNEAKY, 2)?, "test_sneaky");
            assert_eq!(assign_key(tx, "test-sneaky", 3)?, "test_sneaky_2");
            assert_eq!(assign_key(tx, "TEST..sneaky", 4)?, "test_sneaky_3");
            assert_eq!(assign_key(tx, "...", 5)?, "b");
            assert_eq!(
                stored_key(tx, "test-sneaky")?.as_deref(),
                Some("test_sneaky_2")
            );
            Ok(())
        })
        .unwrap();
    }
}

#[test]
fn a_behaviors_migration_adds_columns_indexes_and_tables_of_its_own() {
    for target in targets("a_behaviors_migration_adds_columns") {
        let db = engine(&target);
        let prefix = prefix_of(&write(&db, |tx| assign_key(tx, "test.Counter", 1)).unwrap());
        let migrations = [
            BehaviorMigration::new(1, "count")
                .column("count", ColumnSpec::integer().not_null().default(0))
                .column("label", ColumnSpec::text())
                .index("by_count", &["count"])
                .table(
                    TableSpec::new("history")
                        .row_id("row")
                        .column("at", ColumnSpec::integer().not_null())
                        .column("note", ColumnSpec::text())
                        .index(IndexSpec::new("history_by_at", &["at"])),
                )
                .up(|sql| {
                    sql.run(
                        format!(
                            "INSERT INTO {} (at, note) VALUES (?, ?)",
                            sql.table("history")?
                        ),
                        params![1, "made"],
                    )?;
                    Ok(())
                }),
            BehaviorMigration::new(2, "score").column("score", ColumnSpec::real().default(0.5)),
        ];
        let set = migration_set("test.Counter", &prefix, &migrations);
        assert_eq!(db.migrate(&set, 1).unwrap().applied, [1, 2]);
        assert_eq!(db.migrate(&set, 1).unwrap().applied, Vec::<u32>::new());
        assert_eq!(tables(&db, &prefix), ["bhv_test_counter__history"]);
        write(&db, |tx| {
            tx.run(
                "INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
                 VALUES ('default', 'Item', 'i1', 'default', 1, 1, '{}', 1, 'alice', 1, 'alice')",
                &[],
            )?;
            let row = tx
                .get(
                    "SELECT bhv_test_counter__count AS count, bhv_test_counter__label AS label, bhv_test_counter__score AS score FROM engine_instances",
                    &[],
                )?
                .unwrap();
            assert_eq!(row.values(), [SqlValue::Integer(0), SqlValue::Null, SqlValue::Real(0.5)]);
            let history = tx.get("SELECT at, note FROM bhv_test_counter__history", &[])?.unwrap();
            assert_eq!(history.values(), [SqlValue::Integer(1), SqlValue::from("made")]);
            Ok(())
        })
        .unwrap();
        // The index leads with the namespace and schema, under the engine's
        // names for the behavior.
        let index = db
            .read(|tx| {
                tx.get(
                    Sql::per(
                        "SELECT sql AS definition FROM sqlite_master WHERE type = 'index' AND name = 'bhv_test_counter___index_by_count'",
                        "SELECT indexdef AS definition FROM pg_catalog.pg_indexes WHERE schemaname = current_schema() AND indexname = 'bhv_test_counter___index_by_count'",
                    ),
                    &[],
                )
            })
            .unwrap()
            .expect("the index");
        let definition = index.text("definition").unwrap();
        assert!(
            definition.contains("(namespace, schema, \"bhv_test_counter__count\")")
                || definition.contains("(namespace, schema, bhv_test_counter__count)"),
            "{definition}"
        );
        assert_eq!(
            db.applied_migrations("test.Counter")
                .unwrap()
                .iter()
                .map(|row| row.name.as_str())
                .collect::<Vec<_>>(),
            ["count", "score"]
        );
    }
}

#[test]
fn a_migration_creates_only_tables_and_indexes_of_its_own() {
    let both: Vec<(&str, Up, &str)> = vec![
        (
            "trigger",
            |sql| {
                sql.run(
                    format!("CREATE TRIGGER {} AFTER INSERT ON {} BEGIN SELECT 1; END", sql.table("t")?, sql.table("notes")?),
                    &[],
                )
                .map(|_| ())
            },
            "SQL refused: the SQL holds more than one statement",
        ),
        ("view", |sql| sql.run(format!("CREATE VIEW {} AS SELECT 1", sql.table("v")?), &[]).map(|_| ()), "not CREATE VIEW"),
        ("temp", |sql| sql.run(format!("CREATE TEMP TABLE {} (x TEXT)", sql.table("t")?), &[]).map(|_| ()), "not CREATE TEMP TABLE"),
        ("engine", |sql| sql.run("ALTER TABLE engine_instances ADD COLUMN x TEXT", &[]).map(|_| ()), "it names engine_instances"),
        (
            "rename",
            |sql| sql.run(format!("ALTER TABLE {} RENAME TO notes", sql.table("notes")?), &[]).map(|_| ()),
            "migration 2 \"rename\" created table notes; a behavior creates only tables and indexes named bhv_test_sneaky__*, with sql.table(name)",
        ),
        ("pragma", |sql| sql.run("PRAGMA writable_schema = ON", &[]).map(|_| ()), "not PRAGMA"),
        (
            "table name",
            |sql| sql.table("Notes").map(|_| ()),
            "table name \"Notes\" must match ^[a-z][a-z0-9_]{0,62}$",
        ),
        (
            "bad spec",
            |sql| sql.create_table(&TableSpec::new("extra")),
            "table bhv_test_sneaky__extra has no columns",
        ),
    ];
    for target in targets("a_migration_creates_only_tables_and_indexes_of_its_own") {
        let mut cases = both.clone();
        match target.dialect {
            Dialect::Sqlite => cases.extend([
                (
                    "rtree",
                    (|sql: &BehaviorSql<'_, '_>| {
                        sql.run(format!("CREATE VIRTUAL TABLE {} USING rtree(id, lo, hi)", sql.table("boxes")?), &[]).map(|_| ())
                    }) as Up,
                    "a migration creates a virtual table only with the fts5 module, not rtree",
                ),
                (
                    "content",
                    |sql| {
                        sql.run(
                            format!("CREATE VIRTUAL TABLE {} USING fts5(data, content='engine_instances')", sql.table("mirror")?),
                            &[],
                        )
                        .map(|_| ())
                    },
                    "it names engine_instances",
                ),
            ]),
            Dialect::Postgres => cases.extend([
                (
                    "virtual",
                    (|sql: &BehaviorSql<'_, '_>| {
                        sql.run(format!("CREATE VIRTUAL TABLE {} USING fts5(body)", sql.table("search")?), &[]).map(|_| ())
                    }) as Up,
                    "Postgres has no virtual tables",
                ),
                (
                    "function",
                    |sql| {
                        sql.run("CREATE FUNCTION bhv_test_sneaky__f() RETURNS int LANGUAGE sql AS 'SELECT 1'", &[])
                            .map(|_| ())
                    },
                    "not CREATE FUNCTION",
                ),
                (
                    "public",
                    |sql| sql.run(format!("CREATE TABLE public.{} (x TEXT)", sql.table("t")?), &[]).map(|_| ()),
                    "an object of schema public",
                ),
                (
                    "moved",
                    |sql| sql.run(format!("ALTER TABLE {} SET SCHEMA public", sql.table("notes")?), &[]).map(|_| ()),
                    "may not move a table to another schema",
                ),
            ]),
        }
        for (name, up, message) in cases {
            let db = engine(&target);
            let error =
                publish_sneaky(&db, vec![BehaviorMigration::new(2, name).up(up)]).unwrap_err();
            assert!(
                matches!(&error, Error::Behavior { behavior, .. } if behavior == SNEAKY),
                "{name}: {error:?}"
            );
            assert!(error.to_string().contains(message), "{name}: {error}");
            // Migration 1 stands; migration 2 left nothing.
            assert_eq!(tables(&db, "notes"), Vec::<String>::new(), "{name}");
            assert_eq!(db.applied_migrations(SNEAKY).unwrap().len(), 1, "{name}");
            if target.dialect == Dialect::Postgres {
                // Leave the next case an empty public schema.
                let _ = write(&db, |tx| {
                    tx.exec("DROP TABLE IF EXISTS public.bhv_test_sneaky__t, public.bhv_test_sneaky__notes")
                });
            }
        }
    }
}

#[test]
fn a_behavior_reads_and_writes_its_own_tables() {
    for target in targets("a_behavior_reads_and_writes_its_own_tables") {
        let db = engine(&target);
        publish_sneaky(&db, vec![]).unwrap();
        let mut expected = vec!["bhv_test_sneaky__notes"];
        if target.dialect == Dialect::Sqlite {
            expected.push("bhv_test_sneaky__search");
        }
        assert_eq!(tables(&db, PREFIX), expected);
        write(&db, |tx| {
            let sql = BehaviorSql::new(tx, SNEAKY, PREFIX, SqlMode::Write, None);
            assert_eq!(sql.run("INSERT INTO bhv_test_sneaky__notes (id, body) VALUES ('n1', 'a desk'); ", &[])?.changes, 1);
            assert_eq!(
                sql.all("SELECT body FROM bhv_test_sneaky__notes -- engine_instances is only a comment", &[])?[0].text("body")?,
                "a desk"
            );
            let rows = sql.all("WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 3) SELECT i FROM n", &[])?;
            assert_eq!(rows.iter().map(|row| row.i64("i").unwrap()).collect::<Vec<_>>(), [1, 2, 3]);
            // Data that looks like a reserved name goes in as a parameter.
            assert_eq!(sql.run("UPDATE bhv_test_sneaky__notes SET body = ?", params!["engine_instances"])?.changes, 1);
            // A unique conflict, tried, leaves the call going.
            let conflict = sql.try_run("INSERT INTO bhv_test_sneaky__notes (id) VALUES (?)", params!["n1"]).unwrap_err();
            assert!(conflict.is_unique(), "{conflict:?}");
            assert_eq!(sql.try_all("SELECT id FROM bhv_test_sneaky__notes", &[])?.len(), 1);
            if tx.dialect() == Dialect::Sqlite {
                sql.run("INSERT INTO \"BHV_TEST_SNEAKY__SEARCH\" (body) VALUES (?)", params!["a walnut desk"])?;
                let found = sql.all(
                    "/* engine_events */ SELECT body FROM bhv_test_sneaky__search WHERE bhv_test_sneaky__search MATCH ?",
                    params!["walnut"],
                )?;
                assert_eq!(found[0].text("body")?, "a walnut desk");
            }
            // What is outside the behavior's storage is refused before it runs.
            let refused = sql.all("SELECT data FROM engine_instances", &[]).unwrap_err();
            assert_eq!(
                refused.to_string(),
                "behavior test.Sneaky: SQL refused: it names engine_instances, which is not the behavior's own storage (bhv_test_sneaky__*): SELECT data FROM engine_instances"
            );
            // A read cannot write.
            let read = BehaviorSql::new(tx, SNEAKY, PREFIX, SqlMode::Read, None);
            assert_eq!(
                read.run("SELECT 1", &[]).unwrap_err().to_string(),
                "behavior test.Sneaky: a read cannot run a statement that writes; run() is for writes"
            );
            assert!(read.all("DELETE FROM bhv_test_sneaky__notes RETURNING id", &[]).unwrap_err().to_string().contains("not DELETE"));
            assert!(read.create_table(&TableSpec::new("x").column("a", ColumnSpec::text())).is_err());
            assert_eq!(read.all("VALUES (1)", &[])?[0].values(), [SqlValue::Integer(1)]);
            Ok(())
        })
        .unwrap();
    }
}

#[test]
fn a_table_name_is_the_behaviors_own_name_for_it() {
    for target in targets("a_table_name_is_the_behaviors_own") {
        let db = target.open();
        db.read(|tx| {
            let sql = BehaviorSql::new(tx, "test.Probe", "bhv_test_probe__", SqlMode::Read, None);
            assert_eq!(sql.table("notes")?, "bhv_test_probe__notes");
            for name in ["Notes", "../x", "a b", ""] {
                let error = sql.table(name).unwrap_err().to_string();
                assert!(error.contains("must match"), "{name}: {error}");
            }
            let long = sql.table(&"n".repeat(63))?;
            match tx.dialect() {
                Dialect::Sqlite => assert_eq!(long.len(), 16 + 63),
                Dialect::Postgres => {
                    assert_eq!(long.len(), 63);
                    assert!(long.starts_with("bhv_test_probe__"));
                }
            }
            Ok::<_, Error>(())
        })
        .unwrap();
    }
}

// instances writes three instances: two of Item in the default namespace,
// one of Item in another, and one of another schema, with the behavior's
// column count.
fn instances(db: &Db) {
    let key = write(db, |tx| assign_key(tx, SNEAKY, 1)).unwrap();
    assert_eq!(prefix_of(&key), PREFIX);
    let migrations = [
        notes(),
        BehaviorMigration::new(2, "count")
            .column("count", ColumnSpec::integer().not_null().default(0)),
    ];
    db.migrate(&migration_set(SNEAKY, PREFIX, &migrations), 1)
        .unwrap();
    write(db, |tx| {
        for (namespace, schema, id, count) in [
            ("default", "Item", "i1", 3),
            ("default", "Item", "i2", 5),
            ("other", "Item", "i3", 7),
            ("default", "Order", "o1", 9),
        ] {
            tx.run(
                "INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by, bhv_test_sneaky__count)
                 VALUES (?, ?, ?, ?, 1, 1, ?, 10, 'alice', 20, 'bob', ?)",
                params![namespace, schema, id, namespace, format!(r#"{{"title":"{id}"}}"#), count],
            )?;
        }
        tx.run("INSERT INTO bhv_test_sneaky__notes (id, body) VALUES (?, ?)", params!["i2", "noted"])
    })
    .unwrap();
}

#[test]
fn the_relation_holds_the_calls_schemas_instances_with_the_behaviors_columns() {
    for target in targets("the_relation_holds_the_calls_schemas_instances") {
        let db = engine(&target);
        instances(&db);
        let asked = Cell::new(0);
        let relation = InstanceRelation {
            namespace: "default".into(),
            schema: "Item".into(),
            columns: vec!["count".into()],
            allow: Box::new(|| {
                asked.set(asked.get() + 1);
                Ok(())
            }),
        };
        write(&db, |tx| {
            let sql = BehaviorSql::new(tx, SNEAKY, PREFIX, SqlMode::Write, Some(&relation));
            let name = sql.instances()?;
            assert_eq!(name, "bhv_test_sneaky___instances");
            let rows = sql.all(format!("SELECT * FROM {name} ORDER BY id"), &[])?;
            assert_eq!(
                rows[0].columns(),
                ["id", "seq", "version", "created_at", "created_by", "updated_at", "updated_by", "data", "count"]
            );
            assert_eq!(
                rows.iter().map(|row| (row.text("id").unwrap().to_owned(), row.i64("count").unwrap())).collect::<Vec<_>>(),
                [("i1".to_owned(), 3), ("i2".to_owned(), 5)]
            );
            assert_eq!(rows[0].text("data")?, r#"{"title":"i1"}"#);
            assert_eq!(asked.get(), 1, "one read asked once");
            // It joins the behavior's tables and merges into WITH and WITH
            // RECURSIVE, and the statement's own parameters bind as written.
            let joined = sql.all(
                "SELECT i.id, n.body FROM bhv_test_sneaky___instances AS i JOIN bhv_test_sneaky__notes AS n ON n.id = i.id WHERE i.count > ?",
                params![4],
            )?;
            assert_eq!(joined.len(), 1);
            assert_eq!(joined[0].text("body")?, "noted");
            let with = sql.all(
                "WITH big AS (SELECT id FROM bhv_test_sneaky___instances WHERE count >= ?) SELECT id FROM big WHERE id <> ?",
                params![3, "i1"],
            )?;
            assert_eq!(with.iter().map(|row| row.text("id").unwrap()).collect::<Vec<_>>(), ["i2"]);
            let recursive = sql.all(
                "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?) SELECT COUNT(*) AS c FROM n, \"bhv_test_sneaky___instances\"",
                params![2],
            )?;
            assert_eq!(recursive[0].i64("c")?, 4);
            assert_eq!(asked.get(), 4);
            // A statement that does not name it asks nothing.
            sql.all("SELECT id FROM bhv_test_sneaky__notes", &[])?;
            assert_eq!(asked.get(), 4);
            // Nothing writes through it.
            assert!(sql.try_run("UPDATE bhv_test_sneaky___instances SET count = 1", &[]).is_err());
            assert!(sql.try_run("DELETE FROM bhv_test_sneaky___instances", &[]).is_err());
            Ok(())
        })
        .unwrap();
        // A refusal of the read is the statement's error, and nothing runs.
        let refusing = InstanceRelation {
            namespace: "default".into(),
            schema: "Item".into(),
            columns: vec!["count".into()],
            allow: Box::new(|| Err(Error::external("forbidden: read Item"))),
        };
        write(&db, |tx| {
            let sql = BehaviorSql::new(tx, SNEAKY, PREFIX, SqlMode::Read, Some(&refusing));
            let error = sql
                .all("SELECT id FROM bhv_test_sneaky___instances", &[])
                .unwrap_err();
            assert_eq!(error.to_string(), "forbidden: read Item");
            Ok(())
        })
        .unwrap();
    }
}

#[test]
fn without_a_principal_or_with_a_clashing_column_there_is_no_relation() {
    for target in targets("without_a_principal") {
        let db = engine(&target);
        instances(&db);
        write(&db, |tx| {
            let none = BehaviorSql::new(tx, SNEAKY, PREFIX, SqlMode::Write, None);
            assert_eq!(
                none.instances().unwrap_err().to_string(),
                "behavior test.Sneaky: sql.instances() reads as a principal, and a migration and afterConfigChange act for none; they read the instances with eachInstance"
            );
            // Without a relation, the name is just a name no table has.
            assert!(none.try_all("SELECT id FROM bhv_test_sneaky___instances", &[]).is_err());
            let clashing = InstanceRelation {
                namespace: "default".into(),
                schema: "Item".into(),
                columns: vec!["data".into(), "count".into()],
                allow: Box::new(|| Ok(())),
            };
            let sql = BehaviorSql::new(tx, SNEAKY, PREFIX, SqlMode::Write, Some(&clashing));
            let expected = "behavior test.Sneaky: sql.instances(): its column data shares a name with the relation's own columns (id, seq, version, created_at, created_by, updated_at, updated_by, data)";
            assert_eq!(sql.instances().unwrap_err().to_string(), expected);
            assert_eq!(sql.all("SELECT id FROM bhv_test_sneaky___instances", &[]).unwrap_err().to_string(), expected);
            // Its other statements run.
            assert_eq!(sql.all("SELECT id FROM bhv_test_sneaky__notes", &[])?.len(), 1);
            Ok(())
        })
        .unwrap();
    }
}
