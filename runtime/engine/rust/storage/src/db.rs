//! The storage a running engine holds ([`Db`]) and a transaction on it
//! ([`Tx`]), a port of `runtime/engine/typescript/src/storage/storage.ts`
//! over both databases (D16, D58).
//!
//! Writes run in transactions. On SQLite a write begins with BEGIN
//! IMMEDIATE, which takes the write lock at once rather than at the first
//! write, so two writers queue on the busy timeout instead of failing
//! midway. On Postgres it runs at READ COMMITTED and takes advisory locks
//! that keep D16's one writer per namespace: its namespace's exclusively
//! and the catalog's shared, or for a change to the catalog the catalog's
//! exclusively; a lock that waits past the busy timeout (`lock_timeout`)
//! is [`Error::Busy`], as SQLite's `SQLITE_BUSY` is. It is not
//! SERIALIZABLE, which would retry a behavior's side effects.
//!
//! The engine is synchronous: behavior code runs inside the write
//! transaction and calls back into the engine, each nested call in a
//! savepoint ([`Tx::savepoint`]) that rolls back alone and leaves the
//! outer transaction running. Every part of a call shares one `&Tx`,
//! which borrows its connection for one statement at a time and reads
//! every row before it returns, so no borrow is held across a callback.
//!
//! Postgres ends a transaction at any failure until a savepoint around it
//! rolls back; the storage holds SQLite to the same rule, so code that
//! passes its tests on SQLite does not fail on Postgres. A statement whose
//! failure the caller handles (a unique conflict, a malformed search)
//! runs with [`Tx::try_run`], [`Tx::try_get`] or [`Tx::try_all`], each in
//! a savepoint of its own.
//!
//! [`Tx::changes`] counts the rows the transaction's statements wrote and
//! never goes down, as SQLite's `total_changes()` never does, so the
//! engine reads two counts around a call to tell whether it wrote
//! (`unchanged()`). Work that must wait for the commit, such as announcing
//! the events a write appended, is queued with [`Tx::after_commit`]: it
//! runs once the outermost transaction commits, and is dropped with the
//! transaction or savepoint that queued it when that one rolls back.

use std::cell::{Cell, RefCell};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::Arc;
use std::time::Duration;

use crate::conn::{Conn, Failure, Outcome};
use crate::ddl::TableSpec;
use crate::dialect::{Dialect, Sql};
use crate::error::{Error, Result};
use crate::ident::{catalog_lock_key, namespace_lock_key};
use crate::notifier::Notifier;
use crate::value::{Row, RunResult, SqlValue};

/// How long a statement waits for another writer's lock by default.
pub const DEFAULT_BUSY_TIMEOUT: Duration = Duration::from_millis(5000);

/// The writers a write transaction excludes. SQLite has one writer per
/// file, so every write waits for every other; on Postgres a write waits
/// only for the writers it names.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Lock<'a> {
    /// A write in one namespace: the namespace's other writers wait.
    Namespace(&'a str),
    /// A write in several namespaces, whose locks it takes in one order.
    Namespaces(&'a [&'a str]),
    /// A change to the catalog (a define, a publish, a change of
    /// namespaces, a migration): every other writer waits.
    Catalog,
}

/// What a transaction is.
#[derive(Clone, Copy, Debug)]
pub(crate) enum TxKind<'a> {
    Read,
    /// A read that begins once the writers a lock names have finished
    /// ([`Db::read_settled`]).
    Settled(Lock<'a>),
    Write(Lock<'a>),
    /// A migration: a catalog write whose locks wait up to `timeout`.
    Migrate {
        timeout: Duration,
    },
}

/// What a backend keeps about its database.
pub(crate) trait Backend: Send + Sync {
    fn dialect(&self) -> Dialect;
    /// A connection from the pool, waiting up to `timeout` for one.
    fn acquire(&self, timeout: Duration) -> Result<Box<dyn Conn + '_>>;
    /// The database as messages name it: SQLite's file, Postgres's schema.
    fn label(&self) -> &str;
    /// The engine's Postgres schema; `None` on SQLite.
    fn postgres_schema(&self) -> Option<&str>;
    /// The schemas the Postgres database holds, which a behavior may not
    /// qualify a name by; empty on SQLite.
    fn schemas(&self) -> &[String];
    /// The Postgres channel commits notify; `None` on SQLite.
    fn channel(&self) -> Option<&str>;
    /// Takes the lease `key` names for this process's session, or `None`
    /// when another session holds it ([`Db::try_lease`]).
    fn try_lease(&self, key: i64) -> Result<Option<Lease>>;
}

/// What holds a lease: on Postgres the connection whose session holds the
/// advisory lock, which lets go of it when it closes.
pub(crate) trait LeaseHolder: Send {
    /// Whether the lease is still held: on Postgres, whether the session's
    /// connection still answers, since a lost connection lost the lock.
    fn held(&mut self) -> bool;
}

/// A lease of the database's that one process holds at a time (D58): on
/// Postgres a session's advisory lock on a connection of its own, so the
/// engine's runner works in one process of a server's instances and
/// another takes over once its holder stops, crashes or loses the
/// connection; on SQLite, which one process writes, always granted. It
/// lets go when dropped.
pub struct Lease {
    holder: Option<Box<dyn LeaseHolder>>,
}

impl std::fmt::Debug for Lease {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Lease")
            .field("session", &self.holder.is_some())
            .finish()
    }
}

impl Lease {
    #[cfg_attr(not(feature = "sqlite"), allow(dead_code))]
    pub(crate) fn local() -> Lease {
        Lease { holder: None }
    }

    #[cfg_attr(not(feature = "postgres"), allow(dead_code))]
    pub(crate) fn of(holder: Box<dyn LeaseHolder>) -> Lease {
        Lease {
            holder: Some(holder),
        }
    }

    /// Whether the lease is still held; once it is not, take another
    /// ([`Db::try_lease`]).
    pub fn held(&mut self) -> bool {
        self.holder.as_mut().is_none_or(|holder| holder.held())
    }
}

pub(crate) struct Inner {
    backend: Box<dyn Backend>,
    busy_timeout: Duration,
    /// How long a migration waits for the locks it needs.
    migration_timeout: Duration,
    notifier: Arc<Notifier>,
    /// What runs as the database closes (the Postgres listener's stop).
    closers: Vec<Box<dyn FnOnce() + Send + Sync>>,
}

impl Drop for Inner {
    fn drop(&mut self) {
        for close in self.closers.drain(..) {
            close();
        }
    }
}

/// The engine's storage: a SQLite file or a schema of a Postgres
/// database, behind one synchronous API. It is `Send + Sync` and cheap to
/// clone; a call runs on one thread, and calls on different threads wait
/// for the connections they need.
#[derive(Clone)]
pub struct Db {
    inner: Arc<Inner>,
}

impl std::fmt::Debug for Db {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Db")
            .field("dialect", &self.dialect())
            .field("label", &self.label())
            .finish_non_exhaustive()
    }
}

thread_local! {
    // The databases this thread holds a transaction on, by the address of
    // their state: a second transaction on one of them would wait for the
    // first (SQLite's one connection, Postgres's writer lock) forever.
    static OPEN: RefCell<Vec<usize>> = const { RefCell::new(Vec::new()) };
}

// Holds a database's place in OPEN until dropped.
struct Opened(usize);

impl Opened {
    fn enter(id: usize) -> Result<Opened> {
        OPEN.with(|open| {
            let mut open = open.borrow_mut();
            if open.contains(&id) {
                return Err(Error::usage(
                    "this thread already holds a transaction on this database; a nested call runs in a savepoint of it (Tx::savepoint)",
                ));
            }
            open.push(id);
            Ok(Opened(id))
        })
    }
}

impl Drop for Opened {
    fn drop(&mut self) {
        OPEN.with(|open| open.borrow_mut().retain(|&id| id != self.0));
    }
}

impl Db {
    #[cfg_attr(not(any(feature = "sqlite", feature = "postgres")), allow(dead_code))]
    pub(crate) fn new(
        backend: Box<dyn Backend>,
        busy_timeout: Duration,
        migration_timeout: Duration,
        notifier: Arc<Notifier>,
        closers: Vec<Box<dyn FnOnce() + Send + Sync>>,
    ) -> Db {
        Db {
            inner: Arc::new(Inner {
                backend,
                busy_timeout,
                migration_timeout,
                notifier,
                closers,
            }),
        }
    }

    /// Which database this is.
    pub fn dialect(&self) -> Dialect {
        self.inner.backend.dialect()
    }

    /// How long a statement waits for another writer's lock.
    pub fn busy_timeout(&self) -> Duration {
        self.inner.busy_timeout
    }

    /// The database as messages name it: SQLite's file, or Postgres's
    /// schema.
    pub fn label(&self) -> &str {
        self.inner.backend.label()
    }

    /// The engine's Postgres schema; `None` on SQLite.
    pub fn postgres_schema(&self) -> Option<&str> {
        self.inner.backend.postgres_schema()
    }

    /// The schemas the Postgres database held when the storage opened,
    /// lowercase, which the checker refuses as a name's qualifier; empty
    /// on SQLite.
    pub fn schemas(&self) -> &[String] {
        self.inner.backend.schemas()
    }

    /// What wakes waiters when a transaction that notified commits.
    pub fn notifier(&self) -> &Arc<Notifier> {
        &self.inner.notifier
    }

    /// Whether this thread holds a transaction on the database: a call that
    /// opens one of its own (the runner's pass) is refused inside it.
    pub fn in_transaction(&self) -> bool {
        let id = Arc::as_ptr(&self.inner) as usize;
        OPEN.with(|open| open.borrow().contains(&id))
    }

    /// Takes the lease the key's parts name ([`crate::lock_key`] of
    /// `lease`, the engine's Postgres schema and `parts`) for this process,
    /// without waiting: `None` while another session holds it. On Postgres
    /// it opens a connection of its own, outside the pool, whose session
    /// holds `pg_try_advisory_lock` until the lease drops; on SQLite it is
    /// always granted.
    pub fn try_lease(&self, parts: &[&str]) -> Result<Option<Lease>> {
        let schema = self.postgres_schema().unwrap_or_default();
        let mut named = vec!["lease", schema];
        named.extend_from_slice(parts);
        self.inner.backend.try_lease(crate::lock_key(&named))
    }

    /// Runs `f` in a write transaction that excludes the writers `lock`
    /// names, commits when it returns `Ok` and rolls back when it returns
    /// `Err` or panics. A transaction this thread already holds on the
    /// database is refused: a nested call runs in [`Tx::savepoint`].
    pub fn write<'db, T, E, F>(&'db self, lock: Lock<'_>, f: F) -> Result<T, E>
    where
        E: From<Error>,
        F: FnOnce(&Tx<'db>) -> Result<T, E>,
    {
        self.transaction(TxKind::Write(lock), f)
    }

    /// Runs `f` in a read-only transaction that sees one snapshot of the
    /// database: SQLite's deferred transaction, Postgres's REPEATABLE READ
    /// READ ONLY. A statement that writes is refused.
    pub fn read<'db, T, E, F>(&'db self, f: F) -> Result<T, E>
    where
        E: From<Error>,
        F: FnOnce(&Tx<'db>) -> Result<T, E>,
    {
        self.transaction(TxKind::Read, f)
    }

    /// Runs `f` in a read-only transaction that begins once the writers
    /// `lock` names have finished: on Postgres it takes their advisory
    /// locks shared (so it waits for a writer that holds one, and readers
    /// do not wait for each other) and reads at READ COMMITTED, so each
    /// statement sees what they committed; on SQLite, whose one writer
    /// commits before the next begins, it is [`Db::read`]. A cursor of an
    /// identity read in it is one that no write in flight in those
    /// namespaces drew a lower value than, since a writer draws its values
    /// under its lock: what the log's head needs on Postgres, where a
    /// write's identity is drawn before it commits. A lock that waits past
    /// the busy timeout is [`Error::Busy`].
    pub fn read_settled<'db, T, E, F>(&'db self, lock: Lock<'_>, f: F) -> Result<T, E>
    where
        E: From<Error>,
        F: FnOnce(&Tx<'db>) -> Result<T, E>,
    {
        self.transaction(TxKind::Settled(lock), f)
    }

    fn transaction<'db, T, E, F>(&'db self, kind: TxKind<'_>, f: F) -> Result<T, E>
    where
        E: From<Error>,
        F: FnOnce(&Tx<'db>) -> Result<T, E>,
    {
        let _opened = Opened::enter(Arc::as_ptr(&self.inner) as usize)?;
        let conn = self.inner.backend.acquire(self.inner.busy_timeout)?;
        let tx = Tx::begin(&self.inner, conn, kind).map_err(|(error, _)| error)?;
        let result = f(&tx);
        tx.finish(result).0
    }

    /// A connection of the pool for work that spans transactions (the
    /// migrations at start), with this thread's place held.
    pub(crate) fn session(&self) -> Result<Session<'_>> {
        let opened = Opened::enter(Arc::as_ptr(&self.inner) as usize)?;
        let conn = self.inner.backend.acquire(self.inner.busy_timeout)?;
        Ok(Session {
            inner: &self.inner,
            conn: Some(conn),
            _opened: opened,
        })
    }
}

/// A connection held across transactions.
pub(crate) struct Session<'db> {
    inner: &'db Inner,
    conn: Option<Box<dyn Conn + 'db>>,
    _opened: Opened,
}

impl<'db> Session<'db> {
    pub fn dialect(&self) -> Dialect {
        self.inner.backend.dialect()
    }

    pub fn label(&self) -> &str {
        self.inner.backend.label()
    }

    pub fn postgres_schema(&self) -> Option<&str> {
        self.inner.backend.postgres_schema()
    }

    /// How long a migration waits for the locks it needs.
    pub fn migration_timeout(&self) -> Duration {
        self.inner.migration_timeout
    }

    /// Runs transaction control on the session's connection, outside any
    /// transaction (a lock of the session).
    pub fn control(&mut self, sql: &str) -> Result<()> {
        match self.conn.as_mut() {
            Some(conn) => conn.control(sql),
            None => Err(closed()),
        }
    }

    /// Drops the connection rather than returning it to the pool.
    pub fn discard(&mut self) {
        if let Some(conn) = self.conn.as_mut() {
            conn.discard();
        }
    }

    /// Runs `f` in a transaction of `kind` on the session's connection.
    pub fn transaction<T, E, F>(&mut self, kind: TxKind<'_>, f: F) -> Result<T, E>
    where
        E: From<Error>,
        F: FnOnce(&Tx<'db>) -> Result<T, E>,
    {
        let conn = self.conn.take().ok_or_else(closed)?;
        let tx = match Tx::begin(self.inner, conn, kind) {
            Ok(tx) => tx,
            Err((error, conn)) => {
                self.conn = Some(conn);
                return Err(error.into());
            }
        };
        let result = f(&tx);
        let (result, conn) = tx.finish(result);
        self.conn = conn;
        result
    }
}

fn closed() -> Error {
    Error::usage("the transaction's connection is gone: it failed or panicked")
}

/// A transaction: every statement of a call, and the savepoints of its
/// nested calls. It is shared by reference (`&Tx`) and used on one thread.
pub struct Tx<'db> {
    inner: &'db Inner,
    conn: RefCell<Option<Box<dyn Conn + 'db>>>,
    read_only: bool,
    /// Open savepoints.
    depth: Cell<usize>,
    changes: Cell<u64>,
    /// The failure that ended the transaction, until a savepoint around it
    /// rolls back.
    aborted: RefCell<Option<String>>,
    committed: RefCell<Vec<Box<dyn FnOnce() + 'db>>>,
    ended: RefCell<Vec<Box<dyn FnOnce() + 'db>>>,
    /// Whether it holds the catalog's lock exclusively (Postgres).
    catalog: bool,
    /// The namespaces whose locks it holds (Postgres).
    namespaces: RefCell<Vec<String>>,
    done: Cell<bool>,
}

impl<'db> Tx<'db> {
    // begin begins a transaction of kind on conn; on failure it hands the
    // connection back, which may hold a lock of its session.
    fn begin(
        inner: &'db Inner,
        mut conn: Box<dyn Conn + 'db>,
        kind: TxKind<'_>,
    ) -> Result<Tx<'db>, (Error, Box<dyn Conn + 'db>)> {
        let backend = inner.backend.as_ref();
        let mut namespaces = Vec::new();
        let sql = match backend.dialect() {
            Dialect::Sqlite => match kind {
                TxKind::Read | TxKind::Settled(_) => "BEGIN".to_owned(),
                TxKind::Write(_) | TxKind::Migrate { .. } => "BEGIN IMMEDIATE".to_owned(),
            },
            Dialect::Postgres => {
                let schema = backend.postgres_schema().unwrap_or_default();
                let (timeout, locks) = match kind {
                    TxKind::Read => (inner.busy_timeout, String::new()),
                    TxKind::Settled(lock) => (inner.busy_timeout, settled_locks(schema, lock)),
                    TxKind::Write(Lock::Catalog) => (
                        inner.busy_timeout,
                        format!("pg_advisory_xact_lock({})", catalog_lock_key(schema)),
                    ),
                    TxKind::Migrate { timeout } => (
                        timeout,
                        format!("pg_advisory_xact_lock({})", catalog_lock_key(schema)),
                    ),
                    TxKind::Write(Lock::Namespace(namespace)) => {
                        namespaces.push(namespace.to_owned());
                        (inner.busy_timeout, writer_locks(schema, &namespaces))
                    }
                    TxKind::Write(Lock::Namespaces(list)) => {
                        namespaces.extend(list.iter().map(|&namespace| namespace.to_owned()));
                        (inner.busy_timeout, writer_locks(schema, &namespaces))
                    }
                };
                let isolation = match kind {
                    TxKind::Read => "REPEATABLE READ READ ONLY",
                    TxKind::Settled(_) => "READ COMMITTED READ ONLY",
                    _ => "READ COMMITTED",
                };
                let mut sql = format!(
                    "BEGIN ISOLATION LEVEL {isolation}; SET LOCAL lock_timeout = '{}ms'",
                    lock_timeout_ms(timeout)
                );
                if !locks.is_empty() {
                    sql.push_str("; SELECT ");
                    sql.push_str(&locks);
                }
                sql
            }
        };
        if let Err(error) = conn.control(&sql) {
            // A lock that timed out leaves Postgres's transaction open and
            // ended; SQLite's BEGIN that failed left none.
            if backend.dialect() == Dialect::Postgres && conn.control("ROLLBACK").is_err() {
                conn.discard();
            }
            return Err((error, conn));
        }
        Ok(Tx {
            inner,
            conn: RefCell::new(Some(conn)),
            read_only: matches!(kind, TxKind::Read | TxKind::Settled(_)),
            depth: Cell::new(0),
            changes: Cell::new(0),
            aborted: RefCell::new(None),
            committed: RefCell::new(Vec::new()),
            ended: RefCell::new(Vec::new()),
            catalog: matches!(kind, TxKind::Write(Lock::Catalog) | TxKind::Migrate { .. }),
            namespaces: RefCell::new(namespaces),
            done: Cell::new(false),
        })
    }

    /// Which database the transaction runs on.
    pub fn dialect(&self) -> Dialect {
        self.inner.backend.dialect()
    }

    /// The engine's Postgres schema; `None` on SQLite.
    pub fn postgres_schema(&self) -> Option<&'db str> {
        self.inner.backend.postgres_schema()
    }

    /// The schemas the Postgres database held when the storage opened.
    pub fn schemas(&self) -> &'db [String] {
        self.inner.backend.schemas()
    }

    /// The database as messages name it.
    pub fn label(&self) -> &'db str {
        self.inner.backend.label()
    }

    /// Whether the transaction only reads.
    pub fn read_only(&self) -> bool {
        self.read_only
    }

    /// How many savepoints are open: 0 in the transaction itself.
    pub fn depth(&self) -> usize {
        self.depth.get()
    }

    /// How many rows the transaction's statements (run, get and all, and
    /// their try_ forms) have inserted, updated or deleted, rolled back
    /// ones included: two readings around a call that agree say it wrote
    /// no row. Statements run with [`Tx::exec`] are not counted.
    pub fn changes(&self) -> u64 {
        self.changes.get()
    }

    /// Runs one statement that writes, and says how many rows it wrote.
    pub fn run<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<RunResult> {
        let outcome = self.statement(&sql.into(), params)?;
        Ok(RunResult {
            changes: outcome.changes,
            last_insert_rowid: outcome.last_insert_rowid,
        })
    }

    /// Runs one statement and returns its first row, or `None`.
    pub fn get<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Option<Row>> {
        let outcome = self.statement(&sql.into(), params)?;
        let columns = outcome.columns;
        Ok(outcome
            .rows
            .into_iter()
            .next()
            .map(|values| Row::new(columns, values)))
    }

    /// Runs one statement and returns every row.
    pub fn all<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Vec<Row>> {
        let outcome = self.statement(&sql.into(), params)?;
        let columns = outcome.columns;
        Ok(outcome
            .rows
            .into_iter()
            .map(|values| Row::new(Arc::clone(&columns), values))
            .collect())
    }

    /// [`Tx::run`] in a savepoint of its own, so its failure leaves the
    /// transaction running: a unique conflict comes back as
    /// [`Error::Unique`] and the next statement runs.
    pub fn try_run<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<RunResult> {
        let sql = sql.into();
        self.savepoint(|tx| tx.run(&sql, params))
    }

    /// [`Tx::get`] in a savepoint of its own.
    pub fn try_get<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Option<Row>> {
        let sql = sql.into();
        self.savepoint(|tx| tx.get(&sql, params))
    }

    /// [`Tx::all`] in a savepoint of its own.
    pub fn try_all<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Vec<Row>> {
        let sql = sql.into();
        self.savepoint(|tx| tx.all(&sql, params))
    }

    /// Runs statements with no parameters, one after another: DDL and a
    /// migration's statements. The rows they write are not counted.
    pub fn exec<'s>(&self, sql: impl Into<Sql<'s>>) -> Result<()> {
        self.usable()?;
        let sql = sql.into();
        let result = self.with_conn(|conn| conn.batch(sql.text(self.dialect())))?;
        result.inspect_err(|error| self.abort(error))
    }

    /// Creates the table `spec` declares, and its indexes, in the
    /// database's own types.
    pub fn create_table(&self, spec: &TableSpec) -> Result<()> {
        for statement in spec.statements(self.dialect())? {
            self.exec(statement)?;
        }
        Ok(())
    }

    /// Runs `f` in a savepoint: it is released when `f` returns `Ok`, and
    /// rolled back alone when `f` returns `Err` or panics, with the
    /// [`Tx::after_commit`] work queued inside it, leaving the transaction
    /// running. A statement inside that failed without `f` returning its
    /// error rolls the savepoint back too, as [`Error::Aborted`].
    pub fn savepoint<T, E, F>(&self, f: F) -> Result<T, E>
    where
        E: From<Error>,
        F: FnOnce(&Self) -> Result<T, E>,
    {
        self.usable()?;
        let depth = self.depth.get();
        let name = format!("engine_savepoint_{}", depth + 1);
        self.control(&format!("SAVEPOINT {name}"))?;
        self.depth.set(depth + 1);
        let mut savepoint = Savepoint {
            tx: self,
            name,
            depth,
            queued: self.committed.borrow().len(),
            armed: true,
        };
        let result = f(self);
        savepoint.armed = false;
        self.depth.set(depth);
        match result {
            Ok(value) if self.aborted.borrow().is_none() => {
                self.control(&format!("RELEASE {}", savepoint.name))?;
                Ok(value)
            }
            Ok(_) => {
                let cause = self.aborted.borrow_mut().take().unwrap_or_default();
                savepoint.roll_back();
                Err(Error::Aborted { cause }.into())
            }
            Err(error) => {
                savepoint.roll_back();
                Err(error)
            }
        }
    }

    /// Queues `work` to run once the outermost transaction commits. It is
    /// dropped when the transaction or savepoint that queued it rolls
    /// back. A panic in it is caught: the transaction has committed, and
    /// what waited for it cannot undo that.
    pub fn after_commit(&self, work: impl FnOnce() + 'db) {
        self.committed.borrow_mut().push(Box::new(work));
    }

    /// Queues `work` to run once the outermost transaction ends, committed
    /// or rolled back, after any [`Tx::after_commit`] work. A savepoint's
    /// rollback does not drop it. What a write did outside the database,
    /// which no rollback undoes, is settled this way.
    pub fn after_transaction(&self, work: impl FnOnce() + 'db) {
        self.ended.borrow_mut().push(Box::new(work));
    }

    /// Wakes the waiters on the storage's [`Notifier`] once the outermost
    /// transaction commits, in this process and, on Postgres, in every
    /// process listening to the engine's channel, `topic` the payload. A
    /// savepoint that rolls back drops it.
    pub fn notify(&self, topic: &str) -> Result<()> {
        if self.read_only {
            return Err(Error::usage("a read-only transaction cannot notify"));
        }
        if let Some(channel) = self.inner.backend.channel() {
            self.run(
                "SELECT pg_notify(?, ?)",
                &[SqlValue::from(channel), SqlValue::from(topic)],
            )?;
        }
        let notifier = Arc::clone(&self.inner.notifier);
        self.after_commit(move || {
            notifier.bump();
        });
        Ok(())
    }

    /// Takes namespace `namespace`'s writer lock too, waiting up to the
    /// busy timeout. A no-op on SQLite, whose one writer excludes every
    /// other, and in a transaction that holds the catalog's lock.
    pub fn lock_namespace(&self, namespace: &str) -> Result<()> {
        if self.read_only {
            return Err(Error::usage("a read-only transaction takes no writer lock"));
        }
        if self.dialect() != Dialect::Postgres
            || self.catalog
            || self
                .namespaces
                .borrow()
                .iter()
                .any(|held| held == namespace)
        {
            return Ok(());
        }
        let schema = self.postgres_schema().unwrap_or_default();
        self.control(&format!(
            "SELECT pg_advisory_xact_lock({})",
            namespace_lock_key(schema, namespace)
        ))?;
        self.namespaces.borrow_mut().push(namespace.to_owned());
        Ok(())
    }

    fn usable(&self) -> Result<()> {
        match self.aborted.borrow().as_ref() {
            Some(cause) => Err(Error::Aborted {
                cause: cause.clone(),
            }),
            None => Ok(()),
        }
    }

    fn abort(&self, error: &Error) {
        let mut aborted = self.aborted.borrow_mut();
        if aborted.is_none() {
            *aborted = Some(error.to_string());
        }
    }

    // with_conn lends the connection to f for one call. A connection that
    // is lent already (a statement inside a statement) is refused rather
    // than panicking; nothing in the storage lends it across a callback.
    fn with_conn<R>(&self, f: impl FnOnce(&mut dyn Conn) -> R) -> Result<R> {
        let mut slot = self.conn.try_borrow_mut().map_err(|_| {
            Error::usage("a statement ran while another on the same transaction was running")
        })?;
        let conn = slot.as_mut().ok_or_else(closed)?;
        Ok(f(conn.as_mut()))
    }

    // control runs transaction control; a failure ends the transaction.
    fn control(&self, sql: &str) -> Result<()> {
        self.with_conn(|conn| conn.control(sql))?
            .inspect_err(|error| self.abort(error))
    }

    fn statement(&self, sql: &Sql<'_>, params: &[SqlValue]) -> Result<Outcome> {
        self.usable()?;
        for (index, value) in params.iter().enumerate() {
            if let SqlValue::Real(x) = value {
                if !x.is_finite() {
                    return Err(Error::Value {
                        message: format!(
                            "SQL parameter {} is {x}: bind a finite number",
                            index + 1
                        ),
                    });
                }
            }
        }
        let text = sql.text(self.dialect());
        match self.with_conn(|conn| conn.statement(text, params, self.read_only))? {
            Ok(outcome) => {
                self.changes.set(self.changes.get() + outcome.changes);
                Ok(outcome)
            }
            Err(Failure { error, reached }) => {
                if reached {
                    self.abort(&error);
                }
                Err(error)
            }
        }
    }

    // finish commits on Ok and rolls back otherwise, runs the queued work,
    // and returns the connection unless it failed.
    fn finish<T, E: From<Error>>(
        self,
        result: Result<T, E>,
    ) -> (Result<T, E>, Option<Box<dyn Conn + 'db>>) {
        self.done.set(true);
        let result = match result {
            Ok(value) if self.aborted.borrow().is_none() => match self.control("COMMIT") {
                Ok(()) => {
                    for work in self.committed.take() {
                        let _ = catch_unwind(AssertUnwindSafe(work));
                    }
                    Ok(value)
                }
                Err(error) => {
                    self.roll_back();
                    Err(error.into())
                }
            },
            Ok(_) => {
                let cause = self.aborted.borrow_mut().take().unwrap_or_default();
                self.roll_back();
                Err(Error::Aborted { cause }.into())
            }
            Err(error) => {
                self.roll_back();
                Err(error)
            }
        };
        self.settle();
        (result, self.conn.borrow_mut().take())
    }

    // roll_back ends the transaction; a connection that cannot is dropped.
    // A failed statement can end the transaction on its own (SQLite rolls
    // back on some errors), so a failing rollback does not hide the error
    // that caused it.
    fn roll_back(&self) {
        self.committed.borrow_mut().clear();
        let _ = self.with_conn(|conn| {
            if conn.control("ROLLBACK").is_err() && self.dialect() == Dialect::Postgres {
                conn.discard();
            }
        });
    }

    // settle runs the work queued for the end of the outermost transaction.
    fn settle(&self) {
        for work in self.ended.take() {
            let _ = catch_unwind(AssertUnwindSafe(work));
        }
    }
}

impl Drop for Tx<'_> {
    fn drop(&mut self) {
        if self.done.get() {
            return;
        }
        // The function panicked: roll back, and drop the connection, which
        // may be anywhere.
        self.roll_back();
        let _ = self.with_conn(|conn| conn.discard());
        self.settle();
    }
}

// A savepoint's state while its function runs. Dropped armed, as a panic
// unwinds through it, it rolls the savepoint back.
struct Savepoint<'t, 'db> {
    tx: &'t Tx<'db>,
    name: String,
    depth: usize,
    queued: usize,
    armed: bool,
}

impl Savepoint<'_, '_> {
    // roll_back rolls the savepoint back and releases it, drops the work
    // queued inside it, and clears the failure that happened inside it.
    fn roll_back(&self) {
        let tx = self.tx;
        tx.committed.borrow_mut().truncate(self.queued);
        tx.aborted.borrow_mut().take();
        let sql = format!("ROLLBACK TO {name}; RELEASE {name}", name = self.name);
        if let Err(error) = tx.control(&sql) {
            tx.abort(&error);
        }
    }
}

impl Drop for Savepoint<'_, '_> {
    fn drop(&mut self) {
        if self.armed {
            self.tx.depth.set(self.depth);
            self.roll_back();
        }
    }
}

fn lock_timeout_ms(timeout: Duration) -> u128 {
    // Postgres reads 0 as no timeout; SQLite's 0 fails at once.
    timeout.as_millis().max(1)
}

// settled_locks is what a settled read selects: the catalog's lock shared,
// then each namespace's shared, in key order, as a writer takes them.
fn settled_locks(schema: &str, lock: Lock<'_>) -> String {
    let mut keys: Vec<i64> = match lock {
        Lock::Catalog => Vec::new(),
        Lock::Namespace(namespace) => vec![namespace_lock_key(schema, namespace)],
        Lock::Namespaces(list) => list
            .iter()
            .map(|namespace| namespace_lock_key(schema, namespace))
            .collect(),
    };
    keys.sort_unstable();
    keys.dedup();
    let mut locks = vec![format!(
        "pg_advisory_xact_lock_shared({})",
        catalog_lock_key(schema)
    )];
    locks.extend(
        keys.iter()
            .map(|key| format!("pg_advisory_xact_lock_shared({key})")),
    );
    locks.join(", ")
}

// writer_locks is what a namespace write selects: the catalog's lock
// shared, then each namespace's exclusively, in key order, so two writers
// that lock the same namespaces take them in one order.
fn writer_locks(schema: &str, namespaces: &[String]) -> String {
    let mut keys: Vec<i64> = namespaces
        .iter()
        .map(|namespace| namespace_lock_key(schema, namespace))
        .collect();
    keys.sort_unstable();
    keys.dedup();
    let mut locks = vec![format!(
        "pg_advisory_xact_lock_shared({})",
        catalog_lock_key(schema)
    )];
    locks.extend(
        keys.iter()
            .map(|key| format!("pg_advisory_xact_lock({key})")),
    );
    locks.join(", ")
}
