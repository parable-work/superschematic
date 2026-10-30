//! The adapter over SQLite (D103), through rusqlite: every function takes
//! the connection the caller's transaction runs on (a
//! [`rusqlite::Transaction`] derefs to one) and runs the core's commands
//! on it, beside the caller's other writes. Each returns at once: a
//! connection's work is synchronous, so the host loop runs to its end
//! without an executor.
//!
//! SQLite takes no lock a rule across rows asks for (the store's locks are
//! fenced), and a caller's reads may come before the unit that writes, so
//! the unit begins with a check of each read the command made under a
//! lock: a row still at the version read or still absent, a table that
//! still has rows or still none. A change set applies in the savepoint
//! `rules_apply`, each write's changed rows checked and each check run as
//! `SELECT CASE WHEN (...) THEN 1 ELSE 0 END`; a check that fails or a row
//! that moved rolls back to it and releases it, and the command runs again
//! from fresh state.
//!
//! The caller's transaction is to begin IMMEDIATE
//! (`conn.transaction_with_behavior(TransactionBehavior::Immediate)`;
//! rusqlite's `transaction()` begins DEFERRED). It then holds SQLite's one
//! writer from its start, so two commands on one database run one after
//! the other and each reads what the one before it committed. A DEFERRED
//! transaction reads a snapshot another connection may write past before
//! the unit: its checks pass on that snapshot, and its write is refused
//! with `SQLITE_BUSY`, which refuses the command as `busy`, nothing
//! written, for the caller to roll back and try again.
//!
//! A value binds as its storage class: text, an integer, a real, a boolean
//! as 1 or 0, a timestamp as the text `Date.prototype.toISOString` writes,
//! a UUID as its hyphenated text, an object or an array as its JSON text.

use std::future::Future;
use std::pin::pin;
use std::task::{Context, Poll, Waker};

use rusqlite::types::{Value as SqlValue, ValueRef};
use rusqlite::{params_from_iter, Connection};
use serde_json::{Map, Value};

use crate::convert::{internal, Bind, Cell};
use crate::run::{self, Dialect, Statement, Store};
use crate::{Caller, Command, CreateOptions, Error, ExecuteOptions, Executed, Host, Owned, Row};

/// The savepoint a change set applies in.
const APPLY_SAVEPOINT: &str = "rules_apply";

/// Runs one command on a type of the layout to its outcome on `conn`:
/// answers its reads, takes its locks as checks in the unit that writes,
/// applies its change set whole with each row fenced on its version, and
/// runs it again from fresh state when a row moved or a read it made under
/// a lock changed, up to [`crate::MAX_ATTEMPTS`] times.
///
/// A command with no request id gets one of the host's, kept for every
/// run; a create with no id gets one when its key is a UUID or a string.
/// The core's refusal, a `busy` one after the last attempt or when another
/// connection's lock kept the unit from the database (`SQLITE_BUSY`), a
/// `conflict` for a create of a key the table holds, and an `internal` one
/// for a type the layout lacks come back as [`Error::Refused`]; any other
/// failure of the database as [`Error::Database`].
pub fn execute(
    host: &Host<'_>,
    conn: &Connection,
    type_name: &str,
    command: Command,
    options: &ExecuteOptions,
) -> Result<Executed, Error> {
    finish(run::execute(
        host,
        &SqliteStore { conn },
        type_name,
        command,
        options,
    ))
}

/// Runs an operation of a use on a row and returns its result; null
/// params are `{}`.
#[allow(clippy::too_many_arguments)]
pub fn run_operation(
    host: &Host<'_>,
    conn: &Connection,
    type_name: &str,
    key: &str,
    use_id: &str,
    operation: &str,
    params: Value,
    caller: Caller,
) -> Result<Value, Error> {
    let store = SqliteStore { conn };
    finish(run::run_operation(
        host, &store, type_name, key, use_id, operation, params, caller,
    ))
}

/// Creates a row through the core, its uses' create parameters,
/// validation, guards and initializers run, and returns its key: the
/// options' id, or one the adapter made.
pub fn run_create(
    host: &Host<'_>,
    conn: &Connection,
    type_name: &str,
    own: Map<String, Value>,
    caller: Caller,
    options: &CreateOptions,
) -> Result<String, Error> {
    finish(run::run_create(
        host,
        &SqliteStore { conn },
        type_name,
        own,
        caller,
        options,
    ))
}

/// Updates a row's own fields through the core with a merge patch; a patch
/// that names an owned field is refused (`invalid_instance`).
pub fn run_update(
    host: &Host<'_>,
    conn: &Connection,
    type_name: &str,
    key: &str,
    patch: Map<String, Value>,
    caller: Caller,
    options: &ExecuteOptions,
) -> Result<(), Error> {
    finish(run::run_update(
        host,
        &SqliteStore { conn },
        type_name,
        key,
        patch,
        caller,
        options,
    ))
}

/// Deletes a row through the core, its uses' guards run; the options'
/// `soft_delete` stamps it deleted in place of deleting it.
pub fn run_delete(
    host: &Host<'_>,
    conn: &Connection,
    type_name: &str,
    key: &str,
    caller: Caller,
    options: &ExecuteOptions,
) -> Result<(), Error> {
    finish(run::run_delete(
        host,
        &SqliteStore { conn },
        type_name,
        key,
        caller,
        options,
    ))
}

/// Runs a create of a row through the core to its change set and applies
/// nothing: what an upsert's insert binds. It returns the owned fields the
/// change set gives the row, or the core's refusal. A change set that
/// writes anything but the one row, or that read under a lock (which only
/// the unit that writes could check), refuses it as `internal`.
pub fn prepare_create(
    host: &Host<'_>,
    conn: &Connection,
    type_name: &str,
    own: Map<String, Value>,
    caller: Caller,
    options: &CreateOptions,
) -> Result<Owned, Error> {
    finish(run::prepare_create(
        host,
        &SqliteStore { conn },
        type_name,
        own,
        caller,
        options,
    ))
}

/// Reads a row of a type by its key as the core reads it: its own and
/// owned fields, each column with no value absent, and its version;
/// `None` when the table holds no such row, or none that is live.
pub fn read_row(
    host: &Host<'_>,
    conn: &Connection,
    type_name: &str,
    key: &str,
) -> Result<Option<Row>, Error> {
    finish(run::read_row(host, &SqliteStore { conn }, type_name, key))
}

/// Runs the host loop to its end. A SQLite store's every step returns at
/// once, so one poll finishes it; a future that waits all the same is
/// refused as internal rather than spun on.
pub(crate) fn finish<T>(work: impl Future<Output = Result<T, Error>>) -> Result<T, Error> {
    let mut work = pin!(work);
    match work.as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(out) => out,
        Poll::Pending => Err(internal("the SQLite store waited, which it never does").into()),
    }
}

/// The store over a connection.
pub(crate) struct SqliteStore<'c> {
    pub(crate) conn: &'c Connection,
}

impl SqliteStore<'_> {
    fn applied(&self, statement: &Statement) -> Result<bool, rusqlite::Error> {
        let values = statement.values.iter().map(sql_value);
        if statement.check {
            let text = format!("SELECT CASE WHEN ({}) THEN 1 ELSE 0 END", statement.text);
            let held: i64 = self
                .conn
                .prepare_cached(&text)?
                .query_row(params_from_iter(values), |row| row.get(0))?;
            return Ok(held == 1);
        }
        let changed = self
            .conn
            .prepare_cached(&statement.text)?
            .execute(params_from_iter(values))?;
        Ok(statement
            .expect
            .is_none_or(|expect| changed as u64 == expect))
    }

    /// Rolls a unit back to its savepoint and releases it; a rollback that
    /// fails is let go, the error that undid the unit being the one
    /// returned.
    fn undo(&self) {
        let undo =
            format!("ROLLBACK TO SAVEPOINT {APPLY_SAVEPOINT}; RELEASE SAVEPOINT {APPLY_SAVEPOINT}");
        let _ = self.conn.execute_batch(&undo);
    }
}

impl Store for SqliteStore<'_> {
    fn dialect(&self) -> Dialect {
        Dialect::Sqlite
    }

    fn fenced(&self) -> bool {
        true
    }

    async fn query(&self, text: &str, values: &[Bind]) -> Result<Vec<Vec<Cell>>, Error> {
        let mut statement = self.conn.prepare_cached(text)?;
        let width = statement.column_count();
        let mut rows = statement.query(params_from_iter(values.iter().map(sql_value)))?;
        let mut out = Vec::new();
        while let Some(row) = rows.next()? {
            let cells = (0..width)
                .map(|i| row.get_ref(i).map(cell))
                .collect::<Result<Vec<_>, _>>()?;
            out.push(cells);
        }
        Ok(out)
    }

    async fn apply(&self, statements: &[Statement]) -> Result<bool, Error> {
        if statements.is_empty() {
            return Ok(true);
        }
        self.conn
            .execute_batch(&format!("SAVEPOINT {APPLY_SAVEPOINT}"))?;
        for statement in statements {
            match self.applied(statement) {
                Ok(true) => {}
                Ok(false) => {
                    self.undo();
                    return Ok(false);
                }
                Err(error) => {
                    self.undo();
                    return Err(error.into());
                }
            }
        }
        self.conn
            .execute_batch(&format!("RELEASE SAVEPOINT {APPLY_SAVEPOINT}"))?;
        Ok(true)
    }

    async fn lock(&self, _key: &str) -> Result<(), Error> {
        // The unit that writes holds the lock instead: the loop checks in
        // it each read made under one.
        Ok(())
    }

    fn busy(&self, error: &Error) -> bool {
        busy(error)
    }
}

/// Whether an error is SQLite's `SQLITE_BUSY`, in any of its extended forms
/// (`SQLITE_BUSY_SNAPSHOT`, ...).
fn busy(error: &Error) -> bool {
    match error {
        Error::Database(error) => {
            error
                .downcast_ref::<rusqlite::Error>()
                .and_then(rusqlite::Error::sqlite_error_code)
                == Some(rusqlite::ErrorCode::DatabaseBusy)
        }
        Error::Refused(_) => false,
    }
}

/// A value as SQLite binds it.
fn sql_value(value: &Bind) -> SqlValue {
    match value {
        Bind::Null => SqlValue::Null,
        Bind::Text(text) => SqlValue::Text(text.clone()),
        Bind::Int(n) => SqlValue::Integer(*n),
        Bind::Number(number) => match number.as_i64() {
            Some(n) => SqlValue::Integer(n),
            None => number.as_f64().map_or(SqlValue::Null, SqlValue::Real),
        },
        Bind::Bool(flag) => SqlValue::Integer(i64::from(*flag)),
    }
}

/// A column's value as a cell.
fn cell(value: ValueRef<'_>) -> Cell {
    match value {
        ValueRef::Null => Cell::Null,
        ValueRef::Integer(n) => Cell::Int(n),
        ValueRef::Real(x) => Cell::Real(x),
        ValueRef::Text(text) => Cell::Text(String::from_utf8_lossy(text).into_owned()),
        ValueRef::Blob(bytes) => Cell::Blob(bytes.to_vec()),
    }
}

#[cfg(test)]
mod tests {
    //! The SQLite store on a database in memory: its unit, and the host loop
    //! over a store that moves the row under a command through the same
    //! connection, so a moved row, a moved fenced read and a soft delete
    //! made under a command are each deterministic.

    use std::cell::Cell as Count;

    use serde_json::json;

    use super::*;
    use crate::tests::{alice, both, db_layout, own, refusal, transition, KEY, KEY_COLUMN};
    use crate::{Action, Code, MAX_ATTEMPTS};

    fn open() -> Connection {
        let conn = Connection::open_in_memory().expect("SQLite opens");
        for ddl in crate::tests::ddl(false) {
            conn.execute_batch(&ddl).expect("the table is made");
        }
        conn
    }

    fn write(text: &str, values: Vec<Bind>) -> Statement {
        Statement {
            text: text.into(),
            values,
            expect: Some(1),
            check: false,
        }
    }

    fn title(conn: &Connection, table: &str) -> (String, i64) {
        conn.query_row(
            &format!("SELECT title, _version FROM \"{table}\" WHERE id = ?1"),
            [KEY_COLUMN],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .expect("the row reads")
    }

    fn released(conn: &Connection) -> bool {
        conn.execute_batch("RELEASE SAVEPOINT rules_apply").is_err()
    }

    #[test]
    fn a_unit_applies_in_a_savepoint_it_releases_and_one_that_fails_rolls_back_leaving_the_transaction_going_on(
    ) {
        let conn = open();
        conn.execute_batch("BEGIN").expect("a transaction");
        conn.execute("INSERT INTO toy (id, title) VALUES (?1, 'a')", [KEY_COLUMN])
            .expect("a row");
        let store = SqliteStore { conn: &conn };
        let retitle = |to: &str, id: &str| {
            write(
                &format!("UPDATE toy SET title = '{to}' WHERE id = ?1"),
                vec![Bind::Text(id.into())],
            )
        };

        assert!(finish(store.apply(&[retitle("b", KEY_COLUMN)])).expect("applied"));
        assert!(released(&conn));
        assert_eq!(title(&conn, "toy"), ("b".into(), 2));

        // A write that changes another number of rows than it expects.
        assert!(
            !finish(store.apply(&[retitle("c", KEY_COLUMN), retitle("c", "none")])).expect("moved")
        );
        assert!(released(&conn));
        assert_eq!(title(&conn, "toy").0, "b");

        // A statement the database refuses.
        let failed = finish(store.apply(&[
            retitle("d", KEY_COLUMN),
            write("INSERT INTO nowhere VALUES (1)", Vec::new()),
        ]));
        assert!(matches!(failed, Err(Error::Database(_))), "{failed:?}");
        assert!(released(&conn));
        assert_eq!(title(&conn, "toy").0, "b");

        // A check that is false, then one that holds.
        let found = |id: &str| Statement {
            text: "EXISTS (SELECT 1 FROM toy WHERE id = ?1)".into(),
            values: vec![Bind::Text(id.into())],
            expect: None,
            check: true,
        };
        assert!(!finish(store.apply(&[retitle("e", KEY_COLUMN), found("none")])).expect("moved"));
        assert_eq!(title(&conn, "toy").0, "b");
        assert!(
            finish(store.apply(&[found(KEY_COLUMN), retitle("f", KEY_COLUMN)])).expect("applied")
        );
        assert_eq!(title(&conn, "toy").0, "f");

        // An empty unit applies with nothing to do, and the transaction is
        // the caller's still.
        assert!(finish(store.apply(&[])).expect("applied"));
        assert!(!conn.is_autocommit());
        conn.execute_batch("COMMIT").expect("the caller commits");
    }

    /// A store whose first `moves` applies each find the row moved first,
    /// by a write through the same connection, as when another command's
    /// write lands between the read and the apply.
    struct Moving<'c> {
        inner: SqliteStore<'c>,
        write: String,
        values: Vec<Bind>,
        moves: usize,
        applies: Count<usize>,
    }

    impl<'c> Moving<'c> {
        fn new(conn: &'c Connection, write: &str, values: Vec<Bind>, moves: usize) -> Self {
            Moving {
                inner: SqliteStore { conn },
                write: write.into(),
                values,
                moves,
                applies: Count::new(0),
            }
        }
    }

    impl Store for Moving<'_> {
        fn dialect(&self) -> Dialect {
            self.inner.dialect()
        }

        fn fenced(&self) -> bool {
            self.inner.fenced()
        }

        async fn query(&self, text: &str, values: &[Bind]) -> Result<Vec<Vec<Cell>>, Error> {
            self.inner.query(text, values).await
        }

        async fn apply(&self, statements: &[Statement]) -> Result<bool, Error> {
            if self.applies.get() < self.moves {
                let values = self.values.iter().map(sql_value);
                self.inner
                    .conn
                    .execute(&self.write, params_from_iter(values))?;
            }
            self.applies.set(self.applies.get() + 1);
            self.inner.apply(statements).await
        }

        async fn lock(&self, key: &str) -> Result<(), Error> {
            self.inner.lock(key).await
        }
    }

    fn create(host: &Host<'_>, conn: &Connection, type_name: &str) {
        let options = CreateOptions {
            id: Some(KEY.into()),
            ..CreateOptions::default()
        };
        run_create(
            host,
            conn,
            type_name,
            own(json!({"title": "Desk"})),
            alice(),
            &options,
        )
        .expect("created");
    }

    #[test]
    fn a_row_that_moved_runs_the_command_again_from_fresh_state_and_one_that_keeps_moving_is_busy_with_nothing_written(
    ) {
        let (core, layout) = (both(), db_layout());
        let host = Host::new(&core, &layout).with_clock(|| 5000);
        let conn = open();
        create(&host, &conn, "Order");
        let bump =
            r#"UPDATE "order" SET title = title || '!', _version = _version + 1 WHERE id = ?1"#;
        let key = vec![Bind::Text(KEY_COLUMN.into())];

        let once = Moving::new(&conn, bump, key.clone(), 1);
        let executed = finish(run::execute(
            &host,
            &once,
            "Order",
            transition("paid"),
            &ExecuteOptions::default(),
        ))
        .expect("applied");
        assert_eq!(executed.attempts, 2);
        assert_eq!(executed.result, json!({"from": "draft", "to": "paid"}));
        // The other write and the command's: both kept.
        assert_eq!(title(&conn, "order"), ("Desk!".into(), 3));

        let always = Moving::new(&conn, bump, key, MAX_ATTEMPTS);
        let mut ship = transition("shipped");
        ship.caller = Caller::new("sam", ["orders.ship"]);
        let refused = refusal(finish(run::execute(
            &host,
            &always,
            "Order",
            ship,
            &ExecuteOptions::default(),
        )));
        assert_eq!((refused.code, refused.status), (Code::Busy, 503));
        assert!(
            refused
                .message
                .contains("it ran 8 times and nothing was written"),
            "{}",
            refused.message
        );
        let status: String = conn
            .query_row(
                r#"SELECT status FROM "order" WHERE id = ?1"#,
                [KEY_COLUMN],
                |row| row.get(0),
            )
            .expect("the row reads");
        assert_eq!(status, "paid");
        assert_eq!(
            title(&conn, "order"),
            (
                format!("Desk{}", "!".repeat(1 + MAX_ATTEMPTS)),
                3 + MAX_ATTEMPTS as i64
            )
        );
    }

    #[test]
    fn a_read_made_under_a_lock_that_moved_before_the_unit_runs_the_command_again_over_the_other_write(
    ) {
        let (core, layout) = (both(), db_layout());
        let host = Host::new(&core, &layout);
        let conn = open();
        // The toy's create decides under the lock Stock/s1 whether its table
        // has rows; another's row lands between that read and the unit.
        let other = Moving::new(
            &conn,
            "INSERT INTO toy (id, title) VALUES ('other', 'Other')",
            Vec::new(),
            1,
        );
        let create = Command::new(
            alice(),
            1000,
            Action::Create {
                id: Some(KEY.into()),
                own: own(json!({"title": "Desk"})),
                params: Map::new(),
            },
        );
        let executed = finish(run::execute(
            &host,
            &other,
            "Toy",
            create,
            &ExecuteOptions::default(),
        ))
        .expect("applied");
        assert_eq!(executed.attempts, 2);
        let seen: String = conn
            .query_row(
                "SELECT toy_seen FROM toy WHERE id = ?1",
                [KEY_COLUMN],
                |row| row.get(0),
            )
            .expect("the row reads");
        assert_eq!(seen, "after");
    }

    /// A SQLite file of a test's own under the temporary directory, removed
    /// with its WAL files when dropped.
    struct File(std::path::PathBuf);

    impl File {
        fn new() -> Self {
            static FILES: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
            let nanos = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|since| since.as_nanos())
                .unwrap_or(0);
            let n = FILES.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            File(
                std::env::temp_dir()
                    .join(format!("rules_rs_{}_{nanos}_{n}.db", std::process::id())),
            )
        }

        /// A connection to the file that waits up to ten seconds for a lock.
        fn open(&self) -> Connection {
            let conn = Connection::open(&self.0).expect("SQLite opens");
            conn.busy_timeout(std::time::Duration::from_secs(10))
                .expect("a busy timeout");
            conn
        }
    }

    impl Drop for File {
        fn drop(&mut self) {
            for suffix in ["", "-wal", "-shm"] {
                let _ = std::fs::remove_file(format!("{}{suffix}", self.0.display()));
            }
        }
    }

    /// A store that runs `before` once, ahead of its first unit.
    struct Before<'c, F> {
        inner: SqliteStore<'c>,
        before: std::cell::RefCell<Option<F>>,
    }

    impl<F: FnOnce()> Store for Before<'_, F> {
        fn dialect(&self) -> Dialect {
            self.inner.dialect()
        }

        fn fenced(&self) -> bool {
            self.inner.fenced()
        }

        async fn query(&self, text: &str, values: &[Bind]) -> Result<Vec<Vec<Cell>>, Error> {
            self.inner.query(text, values).await
        }

        async fn apply(&self, statements: &[Statement]) -> Result<bool, Error> {
            let before = self.before.borrow_mut().take();
            if let Some(before) = before {
                before();
            }
            self.inner.apply(statements).await
        }

        async fn lock(&self, key: &str) -> Result<(), Error> {
            self.inner.lock(key).await
        }

        fn busy(&self, error: &Error) -> bool {
            self.inner.busy(error)
        }
    }

    /// A transaction begun DEFERRED, rusqlite's default, reads a snapshot
    /// another connection can commit past before its unit (in WAL mode):
    /// the unit's checks pass on that snapshot, and its write is refused
    /// with `SQLITE_BUSY_SNAPSHOT`. The command is refused as `busy`,
    /// nothing of it written, and not with the database's error.
    #[test]
    fn a_command_in_a_deferred_transaction_another_connection_committed_past_is_busy_with_nothing_written(
    ) {
        let (core, layout) = (both(), db_layout());
        let host = Host::new(&core, &layout);
        let file = File::new();
        let conn = file.open();
        conn.pragma_update(None, "journal_mode", "WAL")
            .expect("WAL mode");
        for ddl in crate::tests::ddl(false) {
            conn.execute_batch(&ddl).expect("the table is made");
        }
        let other = file.open();
        // The toy's create reads under its lock whether its table has rows.
        let create = |id: Option<&str>| {
            Command::new(
                alice(),
                1000,
                Action::Create {
                    id: id.map(str::to_owned),
                    own: own(json!({"title": "Desk"})),
                    params: Map::new(),
                },
            )
        };
        conn.execute_batch("BEGIN DEFERRED").expect("a transaction");
        // After the command's reads and before its unit, another's create
        // runs whole on the other connection and commits.
        let store = Before {
            inner: SqliteStore { conn: &conn },
            before: std::cell::RefCell::new(Some(|| {
                let tx = other.unchecked_transaction().expect("a transaction");
                finish(run::execute(
                    &host,
                    &SqliteStore { conn: &tx },
                    "Toy",
                    create(None),
                    &ExecuteOptions::default(),
                ))
                .expect("the other's create applies");
                tx.commit().expect("the other commits");
            })),
        };
        let refused = refusal(finish(run::execute(
            &host,
            &store,
            "Toy",
            create(Some(KEY)),
            &ExecuteOptions::default(),
        )));
        assert_eq!((refused.code, refused.status), (Code::Busy, 503));
        assert!(
            refused
                .message
                .contains("it ran 1 times and nothing was written"),
            "{}",
            refused.message
        );
        conn.execute_batch("ROLLBACK")
            .expect("the caller rolls back");
        let ids: Vec<String> = conn
            .prepare("SELECT id FROM toy")
            .expect("the read prepares")
            .query_map([], |row| row.get(0))
            .expect("the read runs")
            .collect::<Result<_, _>>()
            .expect("the ids read");
        assert_eq!(ids.len(), 1);
        assert_ne!(ids[0], KEY_COLUMN);
    }

    #[test]
    fn a_soft_deletable_tables_deleted_row_is_not_found_and_a_soft_delete_made_under_a_command_moves_it_to_not_found(
    ) {
        let (core, layout) = (both(), db_layout());
        let host = Host::new(&core, &layout);
        let conn = open();
        create(&host, &conn, "Ticket");
        let soft = "UPDATE ticket SET deleted_at = ?1, _version = _version + 1 WHERE id = ?2";
        let values = vec![
            Bind::Text("2026-10-30T00:00:00.000Z".into()),
            Bind::Text(KEY_COLUMN.into()),
        ];
        let deleting = Moving::new(&conn, soft, values, 1);
        let command =
            crate::tests::command(crate::tests::invoke("transition", json!({"to": "paid"})));
        let refused = refusal(finish(run::execute(
            &host,
            &deleting,
            "Ticket",
            command,
            &ExecuteOptions::default(),
        )));
        assert_eq!(refused.code, Code::NotFound);
        assert_eq!(deleting.applies.get(), 1);
        let (status, version): (String, i64) = conn
            .query_row(
                "SELECT status, _version FROM ticket WHERE id = ?1",
                [KEY_COLUMN],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .expect("the row reads");
        assert_eq!((status.as_str(), version), ("draft", 2));
        assert!(read_row(&host, &conn, "Ticket", KEY)
            .expect("read")
            .is_none());
        let refused = refusal(run_operation(
            &host,
            &conn,
            "Ticket",
            KEY,
            "status",
            "transition",
            json!({"to": "paid"}),
            alice(),
        ));
        assert_eq!(refused.code, Code::NotFound);
    }
}
