//! The seam between a transaction and a database: one connection, which
//! runs transaction control, batches and single statements, and a pool of
//! them. A [`crate::Tx`] holds one connection for its life and runs every
//! statement through it, so its logic (savepoints, the write counter, the
//! work queued for the commit) is written once for both databases.

// Built with no database feature, the storage opens nothing, and nothing
// pools a connection.
#![cfg_attr(not(any(feature = "sqlite", feature = "postgres")), allow(dead_code))]

use std::ops::{Deref, DerefMut};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, PoisonError};
use std::time::{Duration, Instant};

use crate::error::{Error, Result};
use crate::value::SqlValue;

/// What one statement returned.
pub(crate) struct Outcome {
    pub columns: Arc<[String]>,
    pub rows: Vec<Vec<SqlValue>>,
    /// The rows it inserted, updated or deleted.
    pub changes: u64,
    pub last_insert_rowid: Option<i64>,
}

/// A statement's failure, and whether the database ran (or tried to run)
/// it: a failure the database saw ends a Postgres transaction, and the
/// storage holds SQLite to the same rule; one the storage raised before
/// sending the statement (a parameter that does not bind) does not.
pub(crate) struct Failure {
    pub error: Error,
    pub reached: bool,
}

impl Failure {
    /// A failure the database saw.
    pub fn reached(error: Error) -> Self {
        Failure {
            error,
            reached: true,
        }
    }

    /// A failure the storage raised before the statement left it.
    pub fn refused(error: Error) -> Self {
        Failure {
            error,
            reached: false,
        }
    }
}

/// One database connection.
pub(crate) trait RawConn: Send {
    /// Runs transaction control (BEGIN, SAVEPOINT, COMMIT, a lock).
    fn control(&mut self, sql: &str) -> Result<()>;
    /// Runs statements with no parameters: DDL, a migration's statements.
    fn batch(&mut self, sql: &str) -> Result<()>;
    /// Runs one statement and reads every row it returns.
    fn statement(
        &mut self,
        sql: &str,
        params: &[SqlValue],
        read_only: bool,
    ) -> Result<Outcome, Failure>;
    /// Whether the connection can go back to the pool: false once the
    /// server closed it.
    fn healthy(&self) -> bool {
        true
    }
}

/// A pooled connection as a transaction holds it.
pub(crate) trait Conn {
    fn control(&mut self, sql: &str) -> Result<()>;
    fn batch(&mut self, sql: &str) -> Result<()>;
    fn statement(
        &mut self,
        sql: &str,
        params: &[SqlValue],
        read_only: bool,
    ) -> Result<Outcome, Failure>;
    /// Drops the connection rather than returning it to the pool: it may
    /// be mid-transaction after a panic, or hold a lock of its session.
    fn discard(&mut self);
}

impl<C: RawConn> Conn for Pooled<'_, C> {
    fn control(&mut self, sql: &str) -> Result<()> {
        self.deref_mut().control(sql)
    }

    fn batch(&mut self, sql: &str) -> Result<()> {
        self.deref_mut().batch(sql)
    }

    fn statement(
        &mut self,
        sql: &str,
        params: &[SqlValue],
        read_only: bool,
    ) -> Result<Outcome, Failure> {
        self.deref_mut().statement(sql, params, read_only)
    }

    fn discard(&mut self) {
        self.broken = true;
    }
}

type Connect<C> = Box<dyn Fn() -> Result<C> + Send + Sync>;

/// Whether a connection can go back to the pool.
pub(crate) type Healthy<C> = fn(&C) -> bool;

/// A pool of up to `max` connections, opened as they are needed.
pub(crate) struct Pool<C> {
    state: Mutex<State<C>>,
    freed: Condvar,
    max: usize,
    connect: Connect<C>,
    healthy: Healthy<C>,
}

struct State<C> {
    idle: Vec<C>,
    /// Connections open, idle or in use.
    open: usize,
}

impl<C: Send> Pool<C> {
    /// A pool of up to `max` connections that `connect` opens, holding
    /// `idle` already; a connection `healthy` refuses is closed rather
    /// than returned.
    pub fn new(max: usize, idle: Vec<C>, connect: Connect<C>, healthy: Healthy<C>) -> Self {
        let open = idle.len();
        Pool {
            state: Mutex::new(State { idle, open }),
            freed: Condvar::new(),
            max,
            connect,
            healthy,
        }
    }

    /// The most connections the pool opens.
    pub fn max(&self) -> usize {
        self.max
    }

    fn lock(&self) -> MutexGuard<'_, State<C>> {
        // Nothing panics while holding the lock, so a poisoned one is whole.
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// An idle connection, a new one while fewer than `max` are open, or
    /// the first one freed within `timeout`; past it, busy.
    pub fn get(&self, timeout: Duration) -> Result<Pooled<'_, C>> {
        let deadline = Instant::now() + timeout;
        let mut state = self.lock();
        loop {
            if let Some(conn) = state.idle.pop() {
                return Ok(Pooled::new(self, conn));
            }
            if state.open < self.max {
                state.open += 1;
                drop(state);
                return match (self.connect)() {
                    Ok(conn) => Ok(Pooled::new(self, conn)),
                    Err(error) => {
                        self.put(None);
                        Err(error)
                    }
                };
            }
            let now = Instant::now();
            if now >= deadline {
                return Err(Error::Busy {
                    code: None,
                    message: format!(
                        "every one of the storage's {} connections stayed in use past the busy timeout ({} ms)",
                        self.max,
                        timeout.as_millis()
                    ),
                });
            }
            state = self
                .freed
                .wait_timeout(state, deadline - now)
                .unwrap_or_else(PoisonError::into_inner)
                .0;
        }
    }

    /// Returns a connection to the pool, or with `None` counts one closed.
    fn put(&self, conn: Option<C>) {
        let mut state = self.lock();
        match conn {
            Some(conn) => state.idle.push(conn),
            None => state.open -= 1,
        }
        drop(state);
        self.freed.notify_one();
    }
}

/// A connection taken from a pool, returned when dropped.
pub(crate) struct Pooled<'p, C: Send> {
    pool: &'p Pool<C>,
    conn: Option<C>,
    broken: bool,
}

impl<'p, C: Send> Pooled<'p, C> {
    fn new(pool: &'p Pool<C>, conn: C) -> Self {
        Pooled {
            pool,
            conn: Some(conn),
            broken: false,
        }
    }
}

impl<C: Send> Deref for Pooled<'_, C> {
    type Target = C;

    fn deref(&self) -> &C {
        self.conn
            .as_ref()
            .expect("a pooled connection is present until dropped")
    }
}

impl<C: Send> DerefMut for Pooled<'_, C> {
    fn deref_mut(&mut self) -> &mut C {
        self.conn
            .as_mut()
            .expect("a pooled connection is present until dropped")
    }
}

impl<C: Send> Drop for Pooled<'_, C> {
    fn drop(&mut self) {
        let healthy = self.pool.healthy;
        let conn = self
            .conn
            .take()
            .filter(|conn| !self.broken && healthy(conn));
        self.pool.put(conn);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    #[test]
    fn a_pool_opens_up_to_its_max_and_waits_for_one_freed() {
        let opened = Arc::new(AtomicUsize::new(0));
        let counter = Arc::clone(&opened);
        let pool = Pool::new(
            2,
            Vec::new(),
            Box::new(move || Ok(counter.fetch_add(1, Ordering::SeqCst))),
            |_| true,
        );
        let first = pool.get(Duration::ZERO).unwrap();
        let second = pool.get(Duration::ZERO).unwrap();
        assert_eq!((*first, *second), (0, 1));
        assert!(matches!(pool.get(Duration::from_millis(10)), Err(error) if error.is_busy()));
        drop(first);
        assert_eq!(*pool.get(Duration::ZERO).unwrap(), 0, "the freed one");
        let mut broken = pool.get(Duration::ZERO).unwrap();
        broken.broken = true;
        drop(broken);
        drop(second);
        assert_eq!(*pool.get(Duration::ZERO).unwrap(), 1);
        assert_eq!(opened.load(Ordering::SeqCst), 2);
    }
}
