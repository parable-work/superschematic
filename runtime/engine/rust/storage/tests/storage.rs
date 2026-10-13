//! Transactions on both databases: `storage.test.ts`'s cases (the
//! connection's settings, rows and values, failures and their codes, the
//! busy timeout, commits, savepoints, the queued work and the write
//! counter), and what the Rust storage adds: one failure rule on both
//! databases, writer locks per namespace on Postgres, values coerced to
//! Postgres's types, and the commit notifier.

mod support;

use std::cell::RefCell;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::{mpsc, Arc, Barrier};
use std::thread;
use std::time::{Duration, Instant};

use superschematic_engine_storage::{
    params, ColumnSpec, Db, Dialect, Error, Lock, Row, Sql, SqlValue, TableSpec, Tx,
};
use support::targets;

type Result<T> = std::result::Result<T, Error>;

const NS: Lock<'static> = Lock::Namespace("default");

fn write<'db, T>(db: &'db Db, f: impl FnOnce(&Tx<'db>) -> Result<T>) -> Result<T> {
    db.write(NS, f)
}

fn notes(db: &Db) {
    write(db, |tx| {
        tx.create_table(
            &TableSpec::new("notes")
                .column("id", ColumnSpec::text())
                .primary_key(&["id"]),
        )
    })
    .expect("create notes");
}

fn ids(db: &Db) -> Vec<String> {
    db.read(|tx| tx.all("SELECT id FROM notes ORDER BY id", &[]))
        .expect("read notes")
        .iter()
        .map(|row| row.text("id").unwrap().to_owned())
        .collect()
}

fn values(row: &Row) -> Vec<(String, SqlValue)> {
    row.iter()
        .map(|(name, value)| (name.to_owned(), value.clone()))
        .collect()
}

#[cfg(feature = "sqlite")]
#[test]
fn sqlite_opens_in_wal_mode_with_foreign_keys_and_a_busy_timeout() {
    let target = support::Target::sqlite();
    let db = target.open();
    assert_eq!(db.dialect(), Dialect::Sqlite);
    let pragma = |db: &Db, name: &str| {
        write(db, |tx| tx.get(format!("PRAGMA {name}"), &[]))
            .unwrap()
            .unwrap()
            .values()[0]
            .clone()
    };
    assert_eq!(pragma(&db, "journal_mode"), SqlValue::from("wal"));
    assert_eq!(pragma(&db, "foreign_keys"), SqlValue::Integer(1));
    assert_eq!(pragma(&db, "busy_timeout"), SqlValue::Integer(5000));
    let other = target.open_with(Duration::from_millis(1234));
    assert_eq!(pragma(&other, "busy_timeout"), SqlValue::Integer(1234));
    let refused = Db::open_sqlite(
        target.sqlite_path().unwrap(),
        superschematic_engine_storage::SqliteOptions {
            busy_timeout: Duration::from_secs(u64::MAX / 2_000),
        },
    )
    .unwrap_err();
    assert!(
        refused.to_string().contains("busy_timeout must be at most"),
        "{refused}"
    );
}

#[cfg(feature = "sqlite")]
#[test]
fn sqlite_refuses_a_database_that_cannot_use_write_ahead_logging() {
    let error = Db::open_sqlite(":memory:", Default::default()).unwrap_err();
    assert!(
        error
            .to_string()
            .contains("would not use write-ahead logging"),
        "{error}"
    );
}

#[cfg(feature = "sqlite")]
#[test]
fn sqlite_enforces_foreign_keys_and_reopens_a_file_with_its_data() {
    let target = support::Target::sqlite();
    let db = target.open();
    let error = write(&db, |tx| {
        tx.exec(
            "CREATE TABLE catalogs (id TEXT PRIMARY KEY) STRICT;
             CREATE TABLE items (id TEXT PRIMARY KEY, catalog TEXT NOT NULL REFERENCES catalogs (id)) STRICT;",
        )?;
        tx.run("INSERT INTO items VALUES (?, ?)", params!["i1", "missing"])
    })
    .unwrap_err();
    // SQLITE_CONSTRAINT_FOREIGNKEY; the failed statement ended the write,
    // so the tables are gone with it.
    assert_eq!(error.sqlite_code(), Some(787), "{error}");
    write(&db, |tx| {
        tx.exec("CREATE TABLE notes (id TEXT PRIMARY KEY) STRICT; INSERT INTO notes VALUES ('n1')")
    })
    .unwrap();
    drop(db);
    assert_eq!(ids(&target.open()), ["n1"]);
}

#[test]
fn rows_are_plain_data_and_a_missing_row_is_none() {
    for target in targets("rows_are_plain_data_and_a_missing_row_is_none") {
        let db = target.open();
        write(&db, |tx| {
            tx.create_table(
                &TableSpec::new("notes")
                    .column("id", ColumnSpec::text())
                    .column("body", ColumnSpec::text())
                    .primary_key(&["id"]),
            )?;
            tx.run("INSERT INTO notes VALUES (?, ?)", params!["n1", "hello"])
        })
        .unwrap();
        db.read(|tx| {
            let row = tx
                .get("SELECT id, body FROM notes WHERE id = ?", params!["n1"])?
                .unwrap();
            assert_eq!(
                values(&row),
                [
                    ("id".to_owned(), SqlValue::from("n1")),
                    ("body".to_owned(), SqlValue::from("hello"))
                ]
            );
            assert_eq!(
                tx.get("SELECT id FROM notes WHERE id = ?", params!["n2"])?,
                None
            );
            assert_eq!(tx.all("SELECT id FROM notes", &[])?.len(), 1);
            Ok::<_, Error>(())
        })
        .unwrap();
    }
}

#[test]
fn binds_text_numbers_bytes_and_null_and_refuses_what_is_not_finite() {
    for target in targets("binds_text_numbers_bytes_and_null") {
        let db = target.open();
        write(&db, |tx| {
            tx.create_table(
                &TableSpec::new("cells")
                    .row_id("n")
                    .column("t", ColumnSpec::text())
                    .column("i", ColumnSpec::integer())
                    .column("r", ColumnSpec::real())
                    .column("b", ColumnSpec::blob()),
            )?;
            tx.run(
                "INSERT INTO cells (t, i, r, b) VALUES (?, ?, ?, ?)",
                params!["x", 42_i64, 1.5, vec![1_u8, 2, 3]],
            )?;
            tx.run(
                "INSERT INTO cells (t, i, r, b) VALUES (?, ?, ?, ?)",
                params![None::<String>, 7, None::<f64>, None::<Vec<u8>>],
            )
        })
        .unwrap();
        let rows = db
            .read(|tx| tx.all("SELECT t, i, r, b FROM cells ORDER BY n", &[]))
            .unwrap();
        assert_eq!(
            rows[0].values(),
            [
                SqlValue::from("x"),
                SqlValue::Integer(42),
                SqlValue::Real(1.5),
                SqlValue::Blob(vec![1, 2, 3])
            ]
        );
        assert_eq!(
            rows[1].values(),
            [
                SqlValue::Null,
                SqlValue::Integer(7),
                SqlValue::Null,
                SqlValue::Null
            ]
        );
        for value in [f64::NAN, f64::INFINITY, f64::NEG_INFINITY] {
            let refused = write(&db, |tx| {
                let error = tx
                    .run("INSERT INTO cells (r) VALUES (?)", params![value])
                    .unwrap_err();
                assert!(
                    error.to_string().contains("bind a finite number"),
                    "{error}"
                );
                // Refused before it ran, the statement leaves the write going.
                tx.get("SELECT COUNT(*) AS n FROM cells", &[])
            });
            assert_eq!(refused.unwrap().unwrap().i64("n").unwrap(), 2);
        }
        let error = write(&db, |tx| {
            tx.run("INSERT INTO cells (t) VALUES (?)", params!["a", "b"])
        })
        .unwrap_err();
        assert!(
            error
                .to_string()
                .contains("binds 1 parameters and was given 2"),
            "{error}"
        );
    }
}

#[test]
fn failures_carry_the_databases_code() {
    for target in targets("failures_carry_the_databases_code") {
        let db = target.open();
        write(&db, |tx| {
            tx.create_table(
                &TableSpec::new("skus")
                    .column("code", ColumnSpec::text())
                    .unique(&["code"]),
            )?;
            tx.run("INSERT INTO skus VALUES (?)", params!["A-1"])
        })
        .unwrap();
        let error = write(&db, |tx| {
            tx.run("INSERT INTO skus VALUES (?)", params!["A-1"])
        })
        .unwrap_err();
        assert!(error.is_unique(), "{error:?}");
        let missing = write(&db, |tx| tx.all("SELECT * FROM nowhere", &[])).unwrap_err();
        match target.dialect {
            Dialect::Sqlite => {
                assert_eq!(error.sqlite_code(), Some(2067));
                assert!(
                    error.to_string().contains("UNIQUE constraint failed"),
                    "{error}"
                );
                assert_eq!(missing.sqlite_code(), Some(1));
            }
            Dialect::Postgres => {
                assert_eq!(error.sqlstate(), Some("23505"));
                assert!(
                    matches!(&error, Error::Unique { index: Some(index), .. } if index == "skus_code_key"),
                    "{error:?}"
                );
                assert_eq!(missing.sqlstate(), Some("42P01"));
            }
        }
    }
}

#[test]
fn a_unique_index_failure_names_the_index() {
    for target in targets("a_unique_index_failure_names_the_index") {
        let db = target.open();
        write(&db, |tx| {
            tx.exec("CREATE TABLE things (doc TEXT NOT NULL)")?;
            tx.exec(Sql::per(
                "CREATE UNIQUE INDEX engine_unique_slug ON things (json_extract(doc, '$.slug'))",
                "CREATE UNIQUE INDEX engine_unique_slug ON things (((doc::jsonb) ->> 'slug'))",
            ))?;
            tx.run("INSERT INTO things VALUES (?)", params![r#"{"slug":"a"}"#])
        })
        .unwrap();
        let error = write(&db, |tx| {
            tx.run("INSERT INTO things VALUES (?)", params![r#"{"slug":"a"}"#])
        })
        .unwrap_err();
        assert!(
            matches!(&error, Error::Unique { index: Some(index), .. } if index == "engine_unique_slug"),
            "{error:?}"
        );
    }
}

#[test]
fn a_unique_conflict_tried_in_a_savepoint_leaves_the_write_running() {
    for target in targets("a_unique_conflict_tried_in_a_savepoint") {
        let db = target.open();
        notes(&db);
        write(&db, |tx| {
            tx.run("INSERT INTO notes VALUES (?)", params!["n1"])?;
            let error = tx
                .try_run("INSERT INTO notes VALUES (?)", params!["n1"])
                .unwrap_err();
            assert!(error.is_unique(), "{error:?}");
            // ON CONFLICT DO NOTHING and a count say the same without a failure.
            let skipped = tx.run(
                "INSERT INTO notes VALUES (?) ON CONFLICT DO NOTHING",
                params!["n1"],
            )?;
            assert_eq!(skipped.changes, 0);
            tx.run("INSERT INTO notes VALUES (?)", params!["n2"])?;
            assert!(tx.try_get("SELECT nope FROM notes", &[]).is_err());
            assert_eq!(tx.try_all("SELECT id FROM notes", &[])?.len(), 2);
            Ok(())
        })
        .unwrap();
        assert_eq!(ids(&db), ["n1", "n2"]);
    }
}

#[test]
fn a_failure_ends_the_write_on_both_databases_until_a_savepoint_rolls_back() {
    for target in targets("a_failure_ends_the_write") {
        let db = target.open();
        notes(&db);
        // A failure the caller ignores ends the write: the next statement is
        // refused, and the write rolls back though its function returns Ok.
        let error = write(&db, |tx| {
            tx.run("INSERT INTO notes VALUES (?)", params!["n1"])?;
            assert!(tx
                .run("INSERT INTO notes VALUES (?)", params!["n1"])
                .is_err());
            let next = tx.get("SELECT 1 AS one", &[]).unwrap_err();
            assert!(matches!(next, Error::Aborted { .. }), "{next:?}");
            assert!(tx.savepoint(|_| Ok::<_, Error>(())).is_err());
            Ok(())
        })
        .unwrap_err();
        assert!(
            matches!(&error, Error::Aborted { cause } if !cause.is_empty()),
            "{error:?}"
        );
        assert!(ids(&db).is_empty());
        // Inside a savepoint, the failure rolls back with the savepoint and
        // the write goes on.
        write(&db, |tx| {
            tx.run("INSERT INTO notes VALUES (?)", params!["kept"])?;
            let inner = tx.savepoint(|tx| {
                tx.run("INSERT INTO notes VALUES (?)", params!["dropped"])?;
                let _ = tx.run("INSERT INTO notes VALUES (?)", params!["kept"]);
                Ok::<_, Error>("swallowed")
            });
            assert!(matches!(inner, Err(Error::Aborted { .. })), "{inner:?}");
            tx.run("INSERT INTO notes VALUES (?)", params!["after"])?;
            Ok(())
        })
        .unwrap();
        assert_eq!(ids(&db), ["after", "kept"]);
    }
}

#[test]
fn a_second_writer_waits_for_the_busy_timeout_then_is_busy() {
    for target in targets("a_second_writer_waits") {
        let first = target.open();
        let second = target.open_with(Duration::from_millis(50));
        write(&first, |tx| {
            tx.create_table(&TableSpec::new("counters").column("n", ColumnSpec::integer()))
        })
        .unwrap();
        write(&first, |tx| {
            tx.run("INSERT INTO counters VALUES (?)", params![1])?;
            let started = Instant::now();
            let error = write(&second, |tx| {
                tx.run("INSERT INTO counters VALUES (?)", params![2])
            })
            .unwrap_err();
            assert!(error.is_busy(), "{error:?}");
            assert!(started.elapsed() >= Duration::from_millis(40));
            match target.dialect {
                Dialect::Sqlite => assert_eq!(error.sqlite_code(), Some(5)),
                Dialect::Postgres => assert_eq!(error.sqlstate(), Some("55P03")),
            }
            Ok(())
        })
        .unwrap();
        let rows = second
            .read(|tx| tx.all("SELECT n FROM counters", &[]))
            .unwrap();
        assert_eq!(
            rows.iter()
                .map(|row| row.i64("n").unwrap())
                .collect::<Vec<_>>(),
            [1]
        );
    }
}

#[test]
fn a_transaction_commits_on_ok_and_rolls_back_on_err() {
    for target in targets("a_transaction_commits_on_ok") {
        let db = target.open();
        notes(&db);
        assert_eq!(
            write(&db, |tx| Ok(tx
                .run("INSERT INTO notes VALUES (?)", params!["kept"])?
                .changes))
            .unwrap(),
            1
        );
        let refused = write(&db, |tx| -> Result<()> {
            tx.run("INSERT INTO notes VALUES (?)", params!["dropped"])?;
            Err(Error::Value {
                message: "refused".into(),
            })
        });
        assert!(matches!(refused, Err(Error::Value { .. })), "{refused:?}");
        assert_eq!(ids(&db), ["kept"]);
        // A panic rolls back too, and the storage stays usable.
        let panicked = catch_unwind(AssertUnwindSafe(|| {
            let _ = write(&db, |tx| -> Result<()> {
                tx.run("INSERT INTO notes VALUES (?)", params!["panicked"])?;
                panic!("a behavior's bug");
            });
        }));
        assert!(panicked.is_err());
        assert_eq!(ids(&db), ["kept"]);
    }
}

#[test]
fn a_nested_transaction_is_a_savepoint_that_rolls_back_alone() {
    for target in targets("a_nested_transaction_is_a_savepoint") {
        let db = target.open();
        notes(&db);
        write(&db, |tx| {
            tx.run("INSERT INTO notes VALUES (?)", params!["outer"])?;
            let inner: Result<()> = tx.savepoint(|tx| {
                tx.run("INSERT INTO notes VALUES (?)", params!["inner"])?;
                assert_eq!(tx.depth(), 1);
                tx.savepoint(|tx| {
                    assert_eq!(tx.depth(), 2);
                    tx.run("INSERT INTO notes VALUES (?)", params!["innermost"])
                })?;
                Err(Error::Value {
                    message: "inner refused".into(),
                })
            });
            assert!(inner.is_err());
            assert_eq!(tx.depth(), 0);
            tx.savepoint(|tx| tx.run("INSERT INTO notes VALUES (?)", params!["second inner"]))?;
            // A panic in a savepoint that the caller catches rolls back the
            // savepoint alone.
            let caught = catch_unwind(AssertUnwindSafe(|| {
                let _ = tx.savepoint(|tx| -> Result<()> {
                    tx.run("INSERT INTO notes VALUES (?)", params!["panicked"])?;
                    panic!("a nested call's bug");
                });
            }));
            assert!(caught.is_err());
            assert_eq!(tx.depth(), 0);
            tx.run("INSERT INTO notes VALUES (?)", params!["last"])?;
            Ok(())
        })
        .unwrap();
        assert_eq!(ids(&db), ["last", "outer", "second inner"]);
    }
}

#[test]
fn after_commit_runs_once_the_outermost_commits_and_never_for_what_rolls_back() {
    for target in targets("after_commit_runs_once") {
        let db = target.open();
        notes(&db);
        let ran = RefCell::new(Vec::<&str>::new());
        write(&db, |tx| {
            tx.after_commit(|| ran.borrow_mut().push("outer"));
            tx.savepoint(|tx| {
                tx.after_commit(|| ran.borrow_mut().push("inner kept"));
                Ok::<_, Error>(())
            })?;
            let dropped: Result<()> = tx.savepoint(|tx| {
                tx.after_commit(|| ran.borrow_mut().push("inner dropped"));
                Err(Error::Value {
                    message: "inner refused".into(),
                })
            });
            assert!(dropped.is_err());
            tx.after_commit(|| panic!("a failing hook"));
            tx.after_commit(|| ran.borrow_mut().push("after a failing hook"));
            assert!(ran.borrow().is_empty());
            Ok(())
        })
        .unwrap();
        assert_eq!(
            *ran.borrow(),
            ["outer", "inner kept", "after a failing hook"]
        );
        let _ = write(&db, |tx| -> Result<()> {
            tx.after_commit(|| ran.borrow_mut().push("rolled back"));
            Err(Error::Value {
                message: "refused".into(),
            })
        });
        write(&db, |tx| {
            tx.run("INSERT INTO notes VALUES (?)", params!["n1"])
        })
        .unwrap();
        assert_eq!(
            *ran.borrow(),
            ["outer", "inner kept", "after a failing hook"]
        );
    }
}

#[test]
fn after_transaction_runs_once_the_outermost_ends_and_a_savepoint_drops_none() {
    for target in targets("after_transaction_runs_once") {
        let db = target.open();
        let ran = RefCell::new(Vec::<&str>::new());
        write(&db, |tx| {
            tx.after_commit(|| ran.borrow_mut().push("committed"));
            let dropped: Result<()> = tx.savepoint(|tx| {
                tx.after_transaction(|| {
                    ran.borrow_mut()
                        .push("queued in a savepoint that rolled back")
                });
                Err(Error::Value {
                    message: "inner refused".into(),
                })
            });
            assert!(dropped.is_err());
            assert!(ran.borrow().is_empty());
            Ok(())
        })
        .unwrap();
        assert_eq!(
            *ran.borrow(),
            ["committed", "queued in a savepoint that rolled back"]
        );
        let _ = write(&db, |tx| -> Result<()> {
            tx.after_transaction(|| ran.borrow_mut().push("rolled back"));
            Err(Error::Value {
                message: "refused".into(),
            })
        });
        assert_eq!(ran.borrow().last(), Some(&"rolled back"));
    }
}

#[test]
fn the_write_counter_counts_rows_written_and_never_goes_down() {
    for target in targets("the_write_counter") {
        let db = target.open();
        notes(&db);
        write(&db, |tx| {
            assert_eq!(tx.changes(), 0);
            tx.run("INSERT INTO notes VALUES (?)", params!["n1"])?;
            assert_eq!(tx.changes(), 1);
            tx.all("SELECT id FROM notes", &[])?;
            assert_eq!(tx.changes(), 1, "a read writes no row");
            let rolled: Result<()> = tx.savepoint(|tx| {
                tx.run("INSERT INTO notes VALUES (?), (?)", params!["n2", "n3"])?;
                Err(Error::Value {
                    message: "refused".into(),
                })
            });
            assert!(rolled.is_err());
            assert_eq!(tx.changes(), 3, "rolled back rows still count");
            let returned = tx.all("INSERT INTO notes VALUES (?) RETURNING id", params!["n4"])?;
            assert_eq!(returned.len(), 1);
            assert_eq!(tx.changes(), 4, "a write that returns rows counts its rows");
            tx.run(
                "UPDATE notes SET id = id || '!' WHERE id <> ?",
                params!["none"],
            )?;
            assert_eq!(tx.changes(), 6);
            tx.run("DELETE FROM notes WHERE id = ?", params!["absent"])?;
            tx.get("WITH x AS (SELECT 1 AS one) SELECT one FROM x", &[])?;
            assert_eq!(tx.changes(), 6, "nothing written, nothing counted");
            tx.exec("CREATE TABLE more (id TEXT)")?;
            assert_eq!(tx.changes(), 6);
            Ok(())
        })
        .unwrap();
    }
}

#[test]
fn a_nested_transaction_on_one_thread_is_refused_and_a_read_does_not_write() {
    for target in targets("a_nested_transaction_on_one_thread") {
        let db = target.open();
        notes(&db);
        write(&db, |_| {
            let nested = write(&db, |_| Ok(())).unwrap_err();
            assert!(
                nested
                    .to_string()
                    .contains("a nested call runs in a savepoint"),
                "{nested}"
            );
            let read = db.read(|_| Ok::<_, Error>(())).unwrap_err();
            assert!(matches!(read, Error::Usage { .. }));
            Ok(())
        })
        .unwrap();
        let error = db
            .read(|tx| tx.run("INSERT INTO notes VALUES (?)", params!["n1"]))
            .unwrap_err();
        assert!(!error.is_unique(), "{error:?}");
        assert!(db.read(|tx| tx.notify("x")).is_err());
        assert!(ids(&db).is_empty());
        // Another thread has transactions of its own.
        let db2 = db.clone();
        thread::spawn(move || {
            write(&db2, |tx| {
                tx.run("INSERT INTO notes VALUES (?)", params!["n1"])
            })
            .unwrap()
        })
        .join()
        .unwrap();
        assert_eq!(ids(&db), ["n1"]);
    }
}

#[test]
fn the_notifier_wakes_a_waiter_when_a_write_that_notified_commits() {
    for target in targets("the_notifier_wakes_a_waiter") {
        let db = target.open_listening();
        notes(&db);
        let notifier = Arc::clone(db.notifier());
        // Let a listener's first connection move the epoch first.
        if target.dialect == Dialect::Postgres {
            wait_listening(&db);
        }
        let start = notifier.epoch();
        let waiter = {
            let notifier = Arc::clone(&notifier);
            thread::spawn(move || notifier.wait(start, Duration::from_secs(10)))
        };
        write(&db, |tx| {
            tx.run("INSERT INTO notes VALUES (?)", params!["n1"])?;
            tx.notify("default")
        })
        .unwrap();
        assert!(waiter.join().unwrap() > start);
        // Nothing moves it for a write that rolled back, or a savepoint.
        let settled = settle(&notifier);
        let _ = write(&db, |tx| -> Result<()> {
            tx.notify("default")?;
            Err(Error::Value {
                message: "refused".into(),
            })
        });
        write(&db, |tx| {
            let dropped: Result<()> = tx.savepoint(|tx| {
                tx.notify("default")?;
                Err(Error::Value {
                    message: "refused".into(),
                })
            });
            assert!(dropped.is_err());
            Ok(())
        })
        .unwrap();
        assert_eq!(notifier.wait(settled, Duration::from_millis(300)), settled);
    }
}

// settle waits until no notification has arrived for a moment, and returns
// the epoch then: a Postgres listener delivers a commit's notification
// after the commit's own wakeup.
fn settle(notifier: &superschematic_engine_storage::Notifier) -> u64 {
    let mut epoch = notifier.epoch();
    loop {
        let next = notifier.wait(epoch, Duration::from_millis(300));
        if next == epoch {
            return epoch;
        }
        epoch = next;
    }
}

fn wait_listening(db: &Db) {
    let deadline = Instant::now() + Duration::from_secs(10);
    while !db.notifier().listening() {
        assert!(Instant::now() < deadline, "the listener did not connect");
        thread::sleep(Duration::from_millis(10));
    }
}

#[test]
fn postgres_writers_in_one_namespace_take_turns_and_other_namespaces_do_not_wait() {
    let Some(target) = support::postgres("postgres_writers_take_turns") else {
        return;
    };
    let first = target.open();
    let second = target.open();
    notes(&first);
    // The same namespace: the second writer waits for the first's commit,
    // then sees its row.
    let (held, hold) = (Arc::new(Barrier::new(2)), Duration::from_millis(300));
    let holder = {
        let (db, held) = (first.clone(), Arc::clone(&held));
        thread::spawn(move || {
            db.write(Lock::Namespace("a"), |tx| {
                tx.run("INSERT INTO notes VALUES (?)", params!["first"])?;
                held.wait();
                thread::sleep(hold);
                Ok::<_, Error>(Instant::now())
            })
            .unwrap()
        })
    };
    held.wait();
    let seen = second
        .write(Lock::Namespace("a"), |tx| {
            let committed = Instant::now();
            let rows = tx.all("SELECT id FROM notes", &[])?;
            Ok::<_, Error>((committed, rows.len()))
        })
        .unwrap();
    let released = holder.join().unwrap();
    assert!(
        seen.0 >= released,
        "the second writer ran before the first committed"
    );
    assert_eq!(seen.1, 1, "it sees the first writer's row");

    // Another namespace: the second writer runs while the first holds its
    // lock (the first waits for it, so a wait would time out as busy).
    let (tx_done, rx_done) = mpsc::channel();
    let holder = {
        let db = first.clone();
        thread::spawn(move || {
            db.write(Lock::Namespace("a"), |_| {
                rx_done
                    .recv_timeout(Duration::from_secs(10))
                    .expect("the other namespace's write");
                Ok::<_, Error>(())
            })
        })
    };
    thread::sleep(Duration::from_millis(100));
    second
        .write(Lock::Namespaces(&["b", "c"]), |tx| {
            tx.run("INSERT INTO notes VALUES (?)", params!["b"])
        })
        .unwrap();
    tx_done.send(()).unwrap();
    holder.join().unwrap().unwrap();

    // The catalog's lock waits for every writer, and every writer for it.
    let short = target.open_with(Duration::from_millis(100));
    first
        .write(Lock::Namespace("a"), |_| {
            let error = short
                .write(Lock::Catalog, |_| Ok::<_, Error>(()))
                .unwrap_err();
            assert!(error.is_busy(), "{error:?}");
            Ok::<_, Error>(())
        })
        .unwrap();
    first
        .write(Lock::Catalog, |_| {
            let error = short
                .write(Lock::Namespace("z"), |_| Ok::<_, Error>(()))
                .unwrap_err();
            assert!(error.is_busy(), "{error:?}");
            Ok::<_, Error>(())
        })
        .unwrap();
    // A lock taken mid-write waits as the first did.
    first
        .write(Lock::Namespace("a"), |_| {
            let error = short
                .write(Lock::Namespace("b"), |tx| tx.lock_namespace("a"))
                .unwrap_err();
            assert!(error.is_busy(), "{error:?}");
            Ok::<_, Error>(())
        })
        .unwrap();
}

#[test]
fn postgres_notifies_every_process_listening() {
    let Some(target) = support::postgres("postgres_notifies_every_process") else {
        return;
    };
    let writer = target.open();
    let listener = target.open_listening();
    notes(&writer);
    wait_listening(&listener);
    let notifier = Arc::clone(listener.notifier());
    let start = settle(&notifier);
    let waiter = {
        let notifier = Arc::clone(&notifier);
        thread::spawn(move || notifier.wait(start, Duration::from_secs(10)))
    };
    write(&writer, |tx| {
        tx.run("INSERT INTO notes VALUES (?)", params!["n1"])?;
        tx.notify("default")
    })
    .unwrap();
    assert!(
        waiter.join().unwrap() > start,
        "the other process's commit woke it"
    );
    let settled = settle(&notifier);
    let _ = write(&writer, |tx| -> Result<()> {
        tx.notify("default")?;
        Err(Error::Value {
            message: "refused".into(),
        })
    });
    assert_eq!(
        notifier.wait(settled, Duration::from_millis(300)),
        settled,
        "a rollback notifies no one"
    );
}

#[test]
fn postgres_coerces_each_value_to_its_parameters_type() {
    let Some(target) = support::postgres("postgres_coerces_each_value") else {
        return;
    };
    let db = target.open();
    write(&db, |tx| {
        tx.exec(
            "CREATE TABLE typed (i BIGINT, s SMALLINT, d DOUBLE PRECISION, n NUMERIC, b BOOLEAN, j JSONB, u UUID, t TEXT)",
        )?;
        // An integral real binds to an integer, text to a number in its
        // text form, an integer to a boolean as 0 or 1.
        tx.run(
            "INSERT INTO typed VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
            params![3.0, "7", 2, 1.25, 1, r#"{"a": [1, 2]}"#, "6f1a3c1e-3f43-4a52-9c39-0d1b8f7e6a01", 4.5],
        )?;
        let row = tx
            .get("SELECT i, s, d, n, b, j, u, t, i + s AS sum, NULL::bigint AS none FROM typed", &[])?
            .unwrap();
        assert_eq!(
            row.values(),
            [
                SqlValue::Integer(3),
                SqlValue::Integer(7),
                SqlValue::Real(2.0),
                SqlValue::Real(1.25),
                SqlValue::Integer(1),
                SqlValue::from(r#"{"a": [1, 2]}"#),
                SqlValue::from("6f1a3c1e-3f43-4a52-9c39-0d1b8f7e6a01"),
                SqlValue::from("4.5"),
                SqlValue::Integer(10),
                SqlValue::Null,
            ]
        );
        // SUM of integers is NUMERIC with no fraction, AVG one with a fraction.
        tx.run("INSERT INTO typed (i) VALUES (?)", params![4])?;
        let row = tx.get("SELECT SUM(i) AS total, AVG(i) AS mean, COUNT(*) AS n FROM typed", &[])?.unwrap();
        assert_eq!(row.values(), [SqlValue::Integer(7), SqlValue::Real(3.5), SqlValue::Integer(2)]);
        // pg_notify and the advisory lock functions return void, read as null.
        assert_eq!(
            tx.get("SELECT pg_advisory_xact_lock(1) AS locked", &[])?.unwrap().values(),
            [SqlValue::Null]
        );
        // A value that cannot bind is refused before it runs, and the write
        // goes on.
        let error = tx.run("INSERT INTO typed (i) VALUES (?)", params![3.5]).unwrap_err();
        assert!(matches!(error, Error::Value { .. }), "{error:?}");
        assert!(error.to_string().contains("which Postgres types int8"), "{error}");
        let error = tx.run("INSERT INTO typed (b) VALUES (?)", params![2]).unwrap_err();
        assert!(error.to_string().contains("a boolean binds 0 or 1"), "{error}");
        // A column type the storage does not read names the cast.
        let error = tx.try_get("SELECT now() AS at", &[]).unwrap_err();
        assert!(error.to_string().contains("cast it in the statement"), "{error}");
        // ? in a string or a comment is no placeholder; a cast follows one.
        let row = tx
            .get("SELECT '?' AS q, ?::text AS a /* ? */, $$?$$ AS d, ?::bigint + 1 AS b", params!["x", 41])?
            .unwrap();
        assert_eq!(
            row.values(),
            [SqlValue::from("?"), SqlValue::from("x"), SqlValue::from("?"), SqlValue::Integer(42)]
        );
        Ok::<_, Error>(())
    })
    .unwrap();
}

#[test]
fn postgres_keeps_a_long_name_whole_with_a_hash() {
    let Some(target) = support::postgres("postgres_keeps_a_long_name_whole") else {
        return;
    };
    let db = target.open();
    let long = format!("bhv_{}__notes", "k".repeat(60));
    let twin = format!("bhv_{}__notez", "k".repeat(60));
    write(&db, |tx| {
        for name in [&long, &twin] {
            tx.create_table(&TableSpec::new(name.clone()).column("id", ColumnSpec::text()))?;
        }
        let stored = superschematic_engine_storage::postgres_identifier(&long);
        tx.run(format!("INSERT INTO \"{stored}\" VALUES (?)"), params!["n1"])?;
        let tables = tx.all(
            "SELECT relname FROM pg_catalog.pg_class WHERE relnamespace = current_schema()::regnamespace AND relkind = 'r' ORDER BY relname",
            &[],
        )?;
        assert_eq!(tables.len(), 2, "two tables, not one cut to 63 bytes");
        assert!(tables.iter().all(|row| row.text("relname").unwrap().len() == 63));
        Ok::<_, Error>(())
    })
    .unwrap();
}

#[test]
fn postgres_searches_the_engines_schema_alone() {
    let Some(target) = support::postgres("postgres_searches_the_engines_schema") else {
        return;
    };
    let db = target.open();
    assert_eq!(db.postgres_schema(), target.schema());
    assert!(db.schemas().iter().any(|schema| schema == "public"));
    assert!(db
        .schemas()
        .iter()
        .any(|schema| Some(schema.as_str()) == target.schema()));
    let path = db
        .read(|tx| {
            tx.get(
                "SELECT current_schema() AS s, current_setting('search_path') AS p",
                &[],
            )
        })
        .unwrap()
        .unwrap();
    assert_eq!(path.opt_text("s").unwrap(), target.schema());
    assert_eq!(path.opt_text("p").unwrap(), target.schema());
    // A second storage on the schema opens it as it is.
    let again = target.open();
    assert_eq!(
        again.label(),
        format!("Postgres schema {}", target.schema().unwrap())
    );
}

// A server that takes the socket and never answers: a port a proxy
// forwards to a database not up yet. Each connection it accepts is held
// open, silent, until the test ends.
#[cfg(feature = "postgres")]
fn silent_server() -> (std::net::SocketAddr, mpsc::Sender<()>) {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind a port");
    let address = listener.local_addr().expect("the port's address");
    listener
        .set_nonblocking(true)
        .expect("a listener that polls");
    let (stop, stopped) = mpsc::channel::<()>();
    thread::spawn(move || {
        let mut held = Vec::new();
        loop {
            match listener.accept() {
                Ok((socket, _)) => held.push(socket),
                Err(_) => thread::sleep(Duration::from_millis(10)),
            }
            if stopped.try_recv().is_ok() {
                return;
            }
        }
    });
    (address, stop)
}

#[cfg(feature = "postgres")]
#[test]
fn a_postgres_connect_is_bounded_by_the_connect_timeout_end_to_end() {
    use superschematic_engine_storage::PostgresOptions;
    let options = || PostgresOptions {
        connect_timeout: Duration::from_millis(300),
        listen: false,
        ..PostgresOptions::default()
    };
    // A server that answers nothing: tokio-postgres bounds the socket
    // alone, and the storage the whole connect.
    let (address, stop) = silent_server();
    let started = Instant::now();
    let refused = Db::open_postgres(
        &format!(
            "postgres://postgres@{}:{}/postgres?sslmode=disable",
            address.ip(),
            address.port()
        ),
        options(),
    )
    .expect_err("a server that never answers is refused");
    assert!(
        matches!(refused, Error::Connection { ref message } if message.contains("connect timeout (300 ms)")),
        "{refused:?}"
    );
    assert!(
        started.elapsed() < Duration::from_secs(5),
        "the connect waited {:?}",
        started.elapsed()
    );
    let _ = stop.send(());

    // A port nothing listens on is refused at once.
    let closed = std::net::TcpListener::bind("127.0.0.1:0")
        .expect("bind a port")
        .local_addr()
        .expect("the port's address");
    let started = Instant::now();
    let refused = Db::open_postgres(
        &format!(
            "postgres://postgres@127.0.0.1:{}/postgres?sslmode=disable",
            closed.port()
        ),
        options(),
    )
    .expect_err("a closed port is refused");
    assert!(matches!(refused, Error::Connection { .. }), "{refused:?}");
    assert!(started.elapsed() < Duration::from_secs(5));

    // An address nothing routes to waits for the timeout, no longer. The
    // URL's own connect_timeout (whole seconds) wins over the option.
    let started = Instant::now();
    let refused = Db::open_postgres(
        "postgres://postgres@10.255.255.1:5432/postgres?sslmode=disable&connect_timeout=1",
        options(),
    )
    .expect_err("an unroutable address is refused");
    assert!(matches!(refused, Error::Connection { .. }), "{refused:?}");
    assert!(
        started.elapsed() < Duration::from_secs(5),
        "the connect waited {:?}",
        started.elapsed()
    );
}

#[test]
fn a_lease_is_one_processs_at_a_time_and_frees_when_it_drops() {
    for target in targets("a_lease_is_one_processs") {
        let first = target.open();
        let second = target.open();
        let mut held = first
            .try_lease(&["runner"])
            .unwrap()
            .expect("the first process takes the lease");
        assert!(held.held());
        if target.dialect == Dialect::Sqlite {
            // One process writes a SQLite file: its every engine holds it.
            assert!(second.try_lease(&["runner"]).unwrap().is_some());
            continue;
        }
        assert!(
            second.try_lease(&["runner"]).unwrap().is_none(),
            "a second process waits while the first holds it"
        );
        assert!(
            second.try_lease(&["other"]).unwrap().is_some(),
            "another lease is another lock"
        );
        drop(held);
        let mut taken = second
            .try_lease(&["runner"])
            .unwrap()
            .expect("once it drops, another process takes it");
        assert!(taken.held());
        assert!(first.try_lease(&["runner"]).unwrap().is_none());
    }
}
