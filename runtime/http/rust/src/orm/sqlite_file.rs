//! A SQLite file as a server holds it (D116): what a Rust API's `Deps`
//! hold in a pool's place for a database on a SQLite file.
//!
//! The file opens in WAL mode with one writing connection and a few
//! reading ones. A write runs in an IMMEDIATE transaction on the writing
//! connection, so it waits its turn where a transaction that read first
//! would meet `SQLITE_BUSY`; one that waits past the busy timeout for
//! another process's write is [`Error::Busy`], a 503 its caller may try
//! again (D58). A read runs on a reading connection, beside the writer.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError, TryLockError};
use std::time::Duration;

use rusqlite::{Connection, OpenFlags, TransactionBehavior};

use super::sqlite::failure;
use super::Error;

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
    writer: Mutex<Connection>,
    readers: Vec<Mutex<Connection>>,
    next: AtomicUsize,
}

impl std::fmt::Debug for Database {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Database")
            .field("path", &self.path)
            .field("readers", &self.readers.len())
            .finish()
    }
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
            writer: Mutex::new(writer),
            readers,
            next: AtomicUsize::new(0),
        })
    }

    /// The file.
    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Runs `work` in a transaction on the writing connection, which it
    /// commits when `work` answers and rolls back when it fails. The
    /// transaction takes the write lock as it begins.
    pub fn write<T, E>(&self, work: impl FnOnce(&Connection) -> Result<T, E>) -> Result<T, E>
    where
        E: From<Error>,
    {
        let mut writer = held(&self.writer);
        let context = || format!("write to {}", self.path.display());
        let transaction = writer
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .map_err(|err| failure(&context(), &err))?;
        let out = work(&transaction)?;
        transaction
            .commit()
            .map_err(|err| failure(&context(), &err))?;
        Ok(out)
    }

    /// Runs `work` on a reading connection, in a transaction of its own,
    /// so its statements read one state of the file.
    pub fn read<T, E>(&self, work: impl FnOnce(&Connection) -> Result<T, E>) -> Result<T, E>
    where
        E: From<Error>,
    {
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
