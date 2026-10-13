//! The migration ledger: `storage.test.ts`'s migrations cases on both
//! databases, and engines that start together migrating once.

mod support;

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Barrier};
use std::thread;
use std::time::Duration;

use superschematic_engine_storage::{
    params, AppliedMigration, ColumnSpec, Db, Dialect, Error, Lock, Migration, MigrationResult,
    MigrationSet, Sql, TableSpec,
};
use support::targets;

fn catalogs() -> Migration {
    Migration::new(1, "catalogs", |tx| {
        tx.create_table(
            &TableSpec::new("catalogs")
                .column("id", ColumnSpec::text())
                .primary_key(&["id"]),
        )
    })
}

fn catalog_titles() -> Migration {
    Migration::sql(
        2,
        "catalog titles",
        Sql::same("ALTER TABLE catalogs ADD COLUMN title TEXT"),
    )
}

fn set(owner: &str, migrations: Vec<Migration>) -> MigrationSet {
    MigrationSet::new(owner, migrations)
}

fn versions(db: &Db, owner: &str) -> Vec<u32> {
    db.applied_migrations(owner)
        .unwrap()
        .iter()
        .map(|row| row.version)
        .collect()
}

fn has_table(db: &Db, name: &str) -> bool {
    db.read(|tx| {
        tx.get(
            Sql::per(
                "SELECT 1 AS present FROM sqlite_master WHERE type = 'table' AND name = ?",
                "SELECT 1 AS present FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename = ?",
            ),
            params![name],
        )
    })
    .unwrap()
    .is_some()
}

#[test]
fn applies_every_migration_to_an_empty_database_then_nothing() {
    for target in targets("applies_every_migration") {
        let db = target.open();
        let all = set("catalog", vec![catalogs(), catalog_titles()]);
        assert_eq!(
            db.migrate(&all, 1000).unwrap(),
            MigrationResult {
                owner: "catalog".into(),
                from: 0,
                to: 2,
                applied: vec![1, 2]
            }
        );
        assert_eq!(
            db.applied_migrations("catalog").unwrap(),
            [
                AppliedMigration {
                    owner: "catalog".into(),
                    version: 1,
                    name: "catalogs".into(),
                    applied_at: 1000
                },
                AppliedMigration {
                    owner: "catalog".into(),
                    version: 2,
                    name: "catalog titles".into(),
                    applied_at: 1000
                },
            ]
        );
        assert_eq!(
            db.migrate(&all, 2000).unwrap(),
            MigrationResult {
                owner: "catalog".into(),
                from: 2,
                to: 2,
                applied: vec![]
            }
        );
        assert!(db.applied_migrations("nobody").unwrap().is_empty());
    }
}

#[test]
fn an_empty_database_has_no_ledger_rows() {
    for target in targets("an_empty_database_has_no_ledger_rows") {
        assert!(target
            .open()
            .applied_migrations("engine")
            .unwrap()
            .is_empty());
    }
}

#[test]
fn applies_only_what_the_database_lacks_keeping_its_rows() {
    for target in targets("applies_only_what_the_database_lacks") {
        let db = target.open();
        db.migrate(&set("catalog", vec![catalogs()]), 1).unwrap();
        db.write(Lock::Catalog, |tx| {
            tx.run("INSERT INTO catalogs (id) VALUES (?)", params!["spring"])
        })
        .unwrap();
        assert_eq!(
            db.migrate(&set("catalog", vec![catalogs(), catalog_titles()]), 2)
                .unwrap()
                .applied,
            [2]
        );
        let rows = db
            .read(|tx| tx.all("SELECT id, title FROM catalogs", &[]))
            .unwrap();
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].text("id").unwrap(), "spring");
        assert_eq!(rows[0].opt_text("title").unwrap(), None);
    }
}

#[test]
fn owners_keep_separate_records_so_another_owner_can_add_columns_to_a_table_it_does_not_own() {
    for target in targets("owners_keep_separate_records") {
        let db = target.open();
        db.migrate(&set("catalog", vec![catalogs()]), 1).unwrap();
        let featured = set(
            "catalog.Featured",
            vec![Migration::sql(
                1,
                "featured flag and picks",
                Sql::per(
                    "ALTER TABLE catalogs ADD COLUMN featured INTEGER NOT NULL DEFAULT 0;
                     CREATE TABLE catalog_featured_picks (catalog TEXT NOT NULL REFERENCES catalogs (id)) STRICT;",
                    "ALTER TABLE catalogs ADD COLUMN featured BIGINT NOT NULL DEFAULT 0;
                     CREATE TABLE catalog_featured_picks (catalog TEXT NOT NULL REFERENCES catalogs (id));",
                ),
            )],
        );
        assert_eq!(db.migrate(&featured, 1).unwrap().applied, [1]);
        assert_eq!(
            db.migrate(&set("catalog", vec![catalogs(), catalog_titles()]), 1)
                .unwrap()
                .applied,
            [2]
        );
        assert_eq!(versions(&db, "catalog.Featured"), [1]);
        assert_eq!(versions(&db, "catalog"), [1, 2]);
    }
}

#[test]
fn a_database_ahead_of_the_build_is_refused() {
    for target in targets("a_database_ahead_of_the_build") {
        let db = target.open();
        db.migrate(&set("catalog", vec![catalogs(), catalog_titles()]), 1)
            .unwrap();
        let error = db
            .migrate(&set("catalog", vec![catalogs()]), 1)
            .unwrap_err();
        assert!(matches!(error, Error::Migration { .. }), "{error:?}");
        assert!(
            error.to_string().ends_with(
                "catalog storage is at migration 2, newer than this build's 1; migrations do not run backwards"
            ),
            "{error}"
        );
        assert!(error.to_string().starts_with(db.label()), "{error}");
    }
}

#[test]
fn a_migration_recorded_under_another_name_is_refused() {
    for target in targets("a_migration_recorded_under_another_name") {
        let db = target.open();
        db.migrate(&set("catalog", vec![catalogs()]), 1).unwrap();
        let renamed = Migration::new(1, "catalog table", |_| Ok(()));
        let error = db.migrate(&set("catalog", vec![renamed]), 1).unwrap_err();
        let holder = match target.dialect {
            Dialect::Sqlite => "the file",
            Dialect::Postgres => "the schema",
        };
        assert!(
            error.to_string().contains(&format!(
                "migration 1 is \"catalog table\" in this build, but {holder} records migration 1 \"catalogs\""
            )),
            "{error}"
        );
    }
}

#[test]
fn a_failing_migration_leaves_neither_its_changes_nor_its_record() {
    for target in targets("a_failing_migration") {
        let db = target.open();
        let failing = Migration::new(2, "half done", |tx| {
            tx.exec("CREATE TABLE half_done (id TEXT)")?;
            Err(Error::external("migration failed"))
        });
        let error = db
            .migrate(&set("catalog", vec![catalogs(), failing]), 1)
            .unwrap_err();
        assert_eq!(error.to_string(), "migration failed");
        assert_eq!(versions(&db, "catalog"), [1]);
        assert!(has_table(&db, "catalogs"));
        assert!(!has_table(&db, "half_done"));
    }
}

#[test]
fn a_set_must_be_numbered_one_two_three_and_have_an_owner_name() {
    for target in targets("a_set_must_be_numbered") {
        let db = target.open();
        let refused =
            |migrations: MigrationSet| db.migrate(&migrations, 1).unwrap_err().to_string();
        assert!(refused(set("catalog", vec![catalog_titles()])).contains("numbered 1, 2, 3"));
        assert!(refused(set("catalog", vec![catalogs(), catalogs()]))
            .contains("position 2 holds version 1"));
        assert!(refused(set("", vec![catalogs()])).contains("migration owner \"\" must match"));
        assert!(
            refused(set("catalog", vec![Migration::new(1, "", |_| Ok(()))]))
                .contains("needs a name")
        );
        // Nothing ran, not even the ledger.
        assert!(!has_table(&db, "engine_migrations"));
    }
}

#[test]
fn engines_that_start_together_migrate_once() {
    for target in targets("engines_that_start_together") {
        let runs = Arc::new(AtomicUsize::new(0));
        let step = |version: u32| {
            let runs = Arc::clone(&runs);
            Migration::new(version, format!("step {version}"), move |tx| {
                runs.fetch_add(1, Ordering::SeqCst);
                thread::sleep(Duration::from_millis(50));
                tx.exec(format!("CREATE TABLE step_{version} (id TEXT)"))
            })
        };
        let steps = set("racer", vec![step(1), step(2)]);
        let engines: Vec<Db> = (0..4).map(|_| target.open()).collect();
        let start = Arc::new(Barrier::new(engines.len()));
        let results: Vec<MigrationResult> = engines
            .into_iter()
            .map(|db| {
                let (steps, start) = (steps.clone(), Arc::clone(&start));
                thread::spawn(move || {
                    start.wait();
                    db.migrate(&steps, 1).unwrap()
                })
            })
            .collect::<Vec<_>>()
            .into_iter()
            .map(|handle| handle.join().unwrap())
            .collect();
        assert_eq!(runs.load(Ordering::SeqCst), 2, "each migration ran once");
        let applied: usize = results.iter().map(|result| result.applied.len()).sum();
        assert_eq!(applied, 2);
        assert!(results.iter().all(|result| result.to == 2));
        assert_eq!(versions(&target.open(), "racer"), [1, 2]);
    }
}
