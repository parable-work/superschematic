//! A SQLite file as a server holds it (D116): what a Rust API's `Deps`
//! hold in a pool's place for a database on a SQLite file.
//!
//! The file opens in WAL mode with one writing connection and a few
//! reading ones. A write runs in an IMMEDIATE transaction on the writing
//! connection, so it waits its turn where a transaction that read first
//! would meet `SQLITE_BUSY`; one that waits past the busy timeout for
//! another process's write is [`Error::Busy`], a 503 its caller may try
//! again (D58). A read runs on a reading connection, beside the writer.
//! Each write that commits notifies the file's [`Wake`], which a worker of
//! the same process sleeps on between its drains (D116).

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, PoisonError, TryLockError, Weak};
use std::time::Duration;

use rusqlite::{Connection, OpenFlags, TransactionBehavior};

use super::sqlite::failure;
use super::Error;
use crate::worker::Wake;

/// How a [`Database`] opens its file.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DatabaseOptions {
    /// The reading connections, 1 or more.
    pub readers: usize,
    /// How long a statement waits for another connection's write lock.
    pub busy_timeout: Duration,
}

impl Default for DatabaseOptions {
    fn default() -> Self {
        Self {
            readers: 4,
            busy_timeout: crate::queue::sqlite::BUSY_TIMEOUT,
        }
    }
}

/// A SQLite file: one writing connection and a few reading ones.
pub struct Database {
    path: PathBuf,
    /// The writing connection; none while a [`HeldWrite`] holds it.
    writer: Mutex<Option<Connection>>,
    /// Told when the writing connection comes back, for a write that
    /// waits on a thread.
    returned: Condvar,
    /// Told when the writing connection comes back, for a write that
    /// waits on a task.
    freed: tokio::sync::Notify,
    busy_timeout: Duration,
    /// The held write and the task holding it: a write or a read of the
    /// same task while it holds one runs in it (#618).
    holder: Mutex<Option<(Runner, Weak<Held>)>>,
    /// Names each nested write's savepoint.
    savepoints: AtomicU64,
    readers: Vec<Mutex<Connection>>,
    next: AtomicUsize,
    /// What a sleeping worker of the process waits on, notified after each
    /// write the file commits (D116).
    wake: Arc<Wake>,
}

impl std::fmt::Debug for Database {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Database")
            .field("path", &self.path)
            .field("readers", &self.readers.len())
            .finish()
    }
}

/// Opens the file a stack's database field names, as a server's entrypoint
/// does (D57, D116): a URL of the form `sqlite:<path>`, which the local
/// and the embedded targets derive for a database on a SQLite file.
pub fn connect(database: &crate::stackconfig::Database) -> Result<Arc<Database>, Error> {
    let crate::stackconfig::Database::Url(url) = database else {
        return Err(Error::Definition(
            "the database is a Cloud SQL instance, and the API's database is a SQLite file: its DB service lists sqlite first".to_owned(),
        ));
    };
    let path = url
        .strip_prefix("sqlite://")
        .or_else(|| url.strip_prefix("sqlite:"))
        .filter(|path| !path.is_empty())
        .ok_or_else(|| {
            Error::Definition(
                "the database's URL is no sqlite:<path>, and the API's database is a SQLite file: its DB service lists sqlite first".to_owned(),
            )
        })?;
    Database::open(path).map(Arc::new)
}

/// A connection, whoever panicked holding it: a transaction a panic
/// unwound was rolled back as its guard dropped.
fn held(connection: &Mutex<Connection>) -> MutexGuard<'_, Connection> {
    connection.lock().unwrap_or_else(PoisonError::into_inner)
}

impl Database {
    /// Opens the file at `path`, which exists: a migration creates it
    /// before a server opens it.
    pub fn open(path: impl AsRef<Path>) -> Result<Self, Error> {
        Self::open_with(path, DatabaseOptions::default())
    }

    /// Opens the file at `path` with `options`.
    pub fn open_with(path: impl AsRef<Path>, options: DatabaseOptions) -> Result<Self, Error> {
        let path = path.as_ref().to_path_buf();
        let context = format!("open {}", path.display());
        let connect = |flags: OpenFlags| -> Result<Connection, Error> {
            let connection =
                Connection::open_with_flags(&path, flags | OpenFlags::SQLITE_OPEN_NO_MUTEX)
                    .map_err(|err| failure(&context, &err))?;
            connection
                .busy_timeout(options.busy_timeout)
                .and_then(|()| connection.execute_batch("PRAGMA foreign_keys = ON"))
                .map_err(|err| failure(&context, &err))?;
            Ok(connection)
        };
        let writer = connect(OpenFlags::SQLITE_OPEN_READ_WRITE)?;
        // WAL lets the readers read beside the writer; the mode is the
        // file's, so the readers find it set.
        writer
            .query_row("PRAGMA journal_mode = WAL", [], |row| {
                row.get::<_, String>(0)
            })
            .map_err(|err| failure(&context, &err))?;
        let mut readers = Vec::with_capacity(options.readers.max(1));
        for _ in 0..options.readers.max(1) {
            readers.push(Mutex::new(connect(OpenFlags::SQLITE_OPEN_READ_ONLY)?));
        }
        Ok(Self {
            path,
            writer: Mutex::new(Some(writer)),
            returned: Condvar::new(),
            freed: tokio::sync::Notify::new(),
            busy_timeout: options.busy_timeout,
            holder: Mutex::new(None),
            savepoints: AtomicU64::new(0),
            readers,
            next: AtomicUsize::new(0),
            wake: Arc::new(Wake::new()),
        })
    }

    /// What wakes a worker of this process that sleeps between its drains
    /// (D116): notified after each write the file commits, a write to a
    /// table a behavior hears among them, so the worker runs it at once.
    pub fn wake(&self) -> &Arc<Wake> {
        &self.wake
    }

    /// The file.
    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Runs `work` in a transaction on the writing connection, which it
    /// commits when `work` answers and rolls back when it fails. The
    /// transaction takes the write lock as it begins.
    ///
    /// Called by the task that holds a [`HeldWrite`] of the file, it runs in
    /// that write instead, in a savepoint it releases when `work` answers
    /// and rolls back to when it fails: the task would wait on itself
    /// otherwise. Its rows commit with the held write.
    pub fn write<T, E>(&self, work: impl FnOnce(&Connection) -> Result<T, E>) -> Result<T, E>
    where
        E: From<Error>,
    {
        if let Some(held) = self.held_here() {
            let savepoint = self.savepoint();
            return held.with(|connection| in_savepoint(connection, &savepoint, &self.path, work));
        }
        let mut guard = self.writer()?;
        let context = || format!("write to {}", self.path.display());
        let writer = guard.as_mut().ok_or_else(|| self.busy())?;
        let transaction = writer
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .map_err(|err| failure(&context(), &err))?;
        let out = work(&transaction)?;
        transaction
            .commit()
            .map_err(|err| failure(&context(), &err))?;
        self.wake.notify();
        Ok(out)
    }

    /// Runs `work` on a reading connection, in a transaction of its own,
    /// so its statements read one state of the file.
    ///
    /// Called by the task that holds a [`HeldWrite`] of the file, it reads
    /// in that write, so it sees the write's rows.
    pub fn read<T, E>(&self, work: impl FnOnce(&Connection) -> Result<T, E>) -> Result<T, E>
    where
        E: From<Error>,
    {
        if let Some(held) = self.held_here() {
            return held.with(work);
        }
        let mut reader = self.reader();
        let context = || format!("read {}", self.path.display());
        let transaction = reader
            .transaction()
            .map_err(|err| failure(&context(), &err))?;
        let out = work(&transaction)?;
        transaction
            .commit()
            .map_err(|err| failure(&context(), &err))?;
        Ok(out)
    }

    /// The writing connection, waiting up to the busy timeout while a
    /// [`HeldWrite`] holds it.
    fn writer(&self) -> Result<MutexGuard<'_, Option<Connection>>, Error> {
        let (guard, _) = self
            .returned
            .wait_timeout_while(
                self.writer.lock().unwrap_or_else(PoisonError::into_inner),
                self.busy_timeout,
                |writer| writer.is_none(),
            )
            .unwrap_or_else(PoisonError::into_inner);
        if guard.is_none() {
            return Err(self.busy());
        }
        Ok(guard)
    }

    fn busy(&self) -> Error {
        Error::Busy(format!(
            "write to {}: the writing connection was held past the busy timeout",
            self.path.display()
        ))
    }

    /// Begins a write that a task holds across its awaits (#618): an
    /// IMMEDIATE transaction on the writing connection, which the
    /// [`HeldWrite`] holds until it commits or drops, and which every other
    /// write waits for, a task's without blocking its thread. A
    /// transaction of the notifier, the inbound path or the agent
    /// dispatcher runs in one, its statements ready at once, so it holds
    /// the file no longer than its statements and its callbacks take.
    ///
    /// Begun by the task that holds one already, it is a savepoint of that
    /// write, released at its commit and rolled back to at its drop.
    pub async fn begin_async(self: &Arc<Self>) -> Result<HeldWrite, Error> {
        if let Some(held) = self.held_here() {
            let savepoint = self.savepoint();
            held.with(|connection| {
                connection
                    .execute_batch(&format!("SAVEPOINT {savepoint}"))
                    .map_err(|err| failure(&format!("write to {}", self.path.display()), &err))
            })?;
            return Ok(HeldWrite {
                database: Arc::clone(self),
                held,
                savepoint: Some(savepoint),
                done: false,
            });
        }
        let connection = loop {
            let notified = self.freed.notified();
            let mut notified = std::pin::pin!(notified);
            // Registered before the connection is looked for, so a return
            // between the two is not missed.
            notified.as_mut().enable();
            let taken = self
                .writer
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .take();
            if let Some(connection) = taken {
                break connection;
            }
            if tokio::time::timeout(self.busy_timeout, notified)
                .await
                .is_err()
            {
                return Err(self.busy());
            }
        };
        if let Err(err) = connection.execute_batch("BEGIN IMMEDIATE") {
            self.give_back(connection);
            return Err(failure(&format!("write to {}", self.path.display()), &err));
        }
        let held = Arc::new(Held {
            connection: Mutex::new(Some(connection)),
        });
        *self.holder.lock().unwrap_or_else(PoisonError::into_inner) =
            Some((Runner::current(), Arc::downgrade(&held)));
        Ok(HeldWrite {
            database: Arc::clone(self),
            held,
            savepoint: None,
            done: false,
        })
    }

    /// The held write of the calling task, when it holds one.
    fn held_here(&self) -> Option<Arc<Held>> {
        let runner = Runner::current();
        let holder = self.holder.lock().unwrap_or_else(PoisonError::into_inner);
        match holder.as_ref() {
            Some((holding, held)) if *holding == runner => held.upgrade(),
            _ => None,
        }
    }

    fn savepoint(&self) -> String {
        format!(
            "held_write_{}",
            self.savepoints.fetch_add(1, Ordering::Relaxed)
        )
    }

    fn give_back(&self, connection: Connection) {
        *self.writer.lock().unwrap_or_else(PoisonError::into_inner) = Some(connection);
        self.returned.notify_all();
        self.freed.notify_waiters();
    }

    /// A reading connection: one that is free, or the next in turn.
    fn reader(&self) -> MutexGuard<'_, Connection> {
        for reader in &self.readers {
            match reader.try_lock() {
                Ok(guard) => return guard,
                Err(TryLockError::Poisoned(poisoned)) => return poisoned.into_inner(),
                Err(TryLockError::WouldBlock) => {}
            }
        }
        let next = self.next.fetch_add(1, Ordering::Relaxed) % self.readers.len();
        held(&self.readers[next])
    }

    /// [`Database::write`] off the caller's thread when the caller runs on
    /// tokio, on its blocking pool, and on the caller's thread when it
    /// runs on none: a host that polls a request's future itself.
    pub async fn write_async<T, E>(
        self: &Arc<Self>,
        work: impl FnOnce(&Connection) -> Result<T, E> + Send + 'static,
    ) -> Result<T, E>
    where
        T: Send + 'static,
        E: From<Error> + Send + 'static,
    {
        let database = Arc::clone(self);
        off_thread(move || database.write(work)).await
    }

    /// [`Database::read`], off the caller's thread as
    /// [`Database::write_async`] is.
    pub async fn read_async<T, E>(
        self: &Arc<Self>,
        work: impl FnOnce(&Connection) -> Result<T, E> + Send + 'static,
    ) -> Result<T, E>
    where
        T: Send + 'static,
        E: From<Error> + Send + 'static,
    {
        let database = Arc::clone(self);
        off_thread(move || database.read(work)).await
    }
}

/// Runs `work` on tokio's blocking pool when a tokio runtime runs the
/// caller, and where it stands otherwise.
async fn off_thread<T, E>(work: impl FnOnce() -> Result<T, E> + Send + 'static) -> Result<T, E>
where
    T: Send + 'static,
    E: From<Error> + Send + 'static,
{
    if tokio::runtime::Handle::try_current().is_err() {
        return work();
    }
    match tokio::task::spawn_blocking(work).await {
        Ok(out) => out,
        Err(err) => {
            Err(Error::Database(format!("the database's work did not finish: {err}")).into())
        }
    }
}

/// Runs `work` on `connection` in the savepoint `name`, released when it
/// answers and rolled back to when it fails.
fn in_savepoint<T, E: From<Error>>(
    connection: &Connection,
    name: &str,
    path: &Path,
    work: impl FnOnce(&Connection) -> Result<T, E>,
) -> Result<T, E> {
    let context = || format!("write to {}", path.display());
    connection
        .execute_batch(&format!("SAVEPOINT {name}"))
        .map_err(|err| failure(&context(), &err))?;
    match work(connection) {
        Ok(out) => {
            connection
                .execute_batch(&format!("RELEASE {name}"))
                .map_err(|err| failure(&context(), &err))?;
            Ok(out)
        }
        Err(err) => {
            let _ = connection.execute_batch(&format!("ROLLBACK TO {name}; RELEASE {name}"));
            Err(err)
        }
    }
}

/// What runs a held write's statements: a tokio task, or a thread's own
/// future that no task runs (a runtime's `block_on`), which stays on its
/// thread.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Runner {
    Task(tokio::task::Id),
    Thread(std::thread::ThreadId),
}

impl Runner {
    fn current() -> Self {
        match tokio::task::try_id() {
            Some(task) => Runner::Task(task),
            None => Runner::Thread(std::thread::current().id()),
        }
    }
}

/// The writing connection a held write took, in its transaction.
struct Held {
    connection: Mutex<Option<Connection>>,
}

impl Held {
    fn with<T, E: From<Error>>(
        &self,
        work: impl FnOnce(&Connection) -> Result<T, E>,
    ) -> Result<T, E> {
        let guard = self
            .connection
            .lock()
            .unwrap_or_else(PoisonError::into_inner);
        let connection = guard
            .as_ref()
            .ok_or_else(|| Error::Database("the held write has ended".to_owned()))?;
        work(connection)
    }
}

/// A write a task holds across its awaits ([`Database::begin_async`]): the
/// writing connection in an IMMEDIATE transaction, which
/// [`HeldWrite::commit`] commits and a drop rolls back, giving the
/// connection back either way; or, begun by the task that holds one
/// already, a savepoint of it.
pub struct HeldWrite {
    database: Arc<Database>,
    held: Arc<Held>,
    savepoint: Option<String>,
    done: bool,
}

impl std::fmt::Debug for HeldWrite {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("HeldWrite")
            .field("path", &self.database.path)
            .field("savepoint", &self.savepoint)
            .finish_non_exhaustive()
    }
}

impl HeldWrite {
    /// Runs `work` on the transaction's connection.
    pub fn with<T, E: From<Error>>(
        &self,
        work: impl FnOnce(&Connection) -> Result<T, E>,
    ) -> Result<T, E> {
        self.held.with(work)
    }

    /// The file the write is to.
    pub fn database(&self) -> &Arc<Database> {
        &self.database
    }

    /// Commits the transaction, gives the connection back and notifies
    /// the file's wake; a savepoint is released.
    pub fn commit(mut self) -> Result<(), Error> {
        self.done = true;
        let context = || format!("commit to {}", self.database.path.display());
        if let Some(savepoint) = &self.savepoint {
            return self.held.with(|connection| {
                connection
                    .execute_batch(&format!("RELEASE {savepoint}"))
                    .map_err(|err| failure(&context(), &err))
            });
        }
        let taken = self
            .held
            .connection
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .take();
        let Some(connection) = taken else {
            return Ok(());
        };
        let done = connection.execute_batch("COMMIT");
        if done.is_err() {
            let _ = connection.execute_batch("ROLLBACK");
        }
        self.release(connection);
        done.map_err(|err| failure(&context(), &err))?;
        self.database.wake.notify();
        Ok(())
    }

    fn release(&self, connection: Connection) {
        {
            let mut holder = self
                .database
                .holder
                .lock()
                .unwrap_or_else(PoisonError::into_inner);
            if holder
                .as_ref()
                .is_some_and(|(_, held)| held.ptr_eq(&Arc::downgrade(&self.held)))
            {
                *holder = None;
            }
        }
        self.database.give_back(connection);
    }
}

impl Drop for HeldWrite {
    fn drop(&mut self) {
        if self.done {
            return;
        }
        if let Some(savepoint) = &self.savepoint {
            let _ = self.held.with(|connection| {
                connection
                    .execute_batch(&format!("ROLLBACK TO {savepoint}; RELEASE {savepoint}"))
                    .map_err(|err| failure("rollback", &err))
            });
            return;
        }
        let taken = self
            .held
            .connection
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .take();
        if let Some(connection) = taken {
            let _ = connection.execute_batch("ROLLBACK");
            self.release(connection);
        }
    }
}
