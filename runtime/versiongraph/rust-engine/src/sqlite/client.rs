//! The seam the SQLite adapter runs its statements through, and its default
//! binding to rusqlite.

use std::fmt;

use async_trait::async_trait;

/// A statement argument or a result column: one of SQLite's five storage
/// classes. The adapter binds texts, integers and `Null`, and reads texts,
/// integers and `Null` back; a column of another class is refused where it
/// reads one.
#[derive(Debug, Clone, PartialEq)]
pub enum SqlValue {
    Null,
    Int(i64),
    Real(f64),
    Text(String),
    Blob(Vec<u8>),
}

impl From<&str> for SqlValue {
    fn from(value: &str) -> Self {
        SqlValue::Text(value.to_owned())
    }
}

impl From<String> for SqlValue {
    fn from(value: String) -> Self {
        SqlValue::Text(value)
    }
}

impl From<i64> for SqlValue {
    fn from(value: i64) -> Self {
        SqlValue::Int(value)
    }
}

impl<T: Into<SqlValue>> From<Option<T>> for SqlValue {
    fn from(value: Option<T>) -> Self {
        value.map_or(SqlValue::Null, Into::into)
    }
}

/// A statement or transaction SQLite refused. `code` is SQLite's extended
/// result code when the driver gave one: 5 is `SQLITE_BUSY` and 2067
/// `SQLITE_CONSTRAINT_UNIQUE`.
#[derive(Debug)]
pub struct ClientError {
    pub code: Option<i32>,
    pub source: Box<dyn std::error::Error + Send + Sync>,
}

impl ClientError {
    /// An error with no result code.
    pub fn new(source: impl Into<Box<dyn std::error::Error + Send + Sync>>) -> Self {
        ClientError {
            code: None,
            source: source.into(),
        }
    }
}

impl fmt::Display for ClientError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self.code {
            Some(code) => write!(f, "{} (SQLite result code {code})", self.source),
            None => self.source.fmt(f),
        }
    }
}

impl std::error::Error for ClientError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.source.as_ref())
    }
}

/// Runs the adapter's statements on one SQLite connection. The adapter asks
/// it for one transaction per engine operation. `Rusqlite` binds rusqlite;
/// another driver implements the two traits itself.
#[async_trait]
pub trait Client: Send + Sync {
    /// Begins one transaction. On a connection in autocommit mode it begins
    /// with `BEGIN IMMEDIATE`, which takes the file's write lock at once, so
    /// a transaction another connection holds makes it wait, up to the
    /// connection's busy timeout, and then fail with `SQLITE_BUSY`. Inside a
    /// transaction the caller holds on the connection it begins a savepoint,
    /// which the transaction's commit releases and its rollback rolls back
    /// to, leaving the caller's transaction open.
    async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError>;
    /// Runs one statement with no parameters on the connection, outside any
    /// transaction the client began, and returns its rows. The adapter
    /// calls it only when it binds the client or creates its tables: to
    /// turn the connection's foreign keys on and read them back, and to read
    /// SQLite's version.
    async fn exec(&self, sql: &str) -> Result<Vec<Vec<SqlValue>>, ClientError>;
}

/// A client shared, so that several adapters (one per graph of a file, say)
/// bind one connection; their transactions take turns on it.
#[async_trait]
impl<C: Client + ?Sized> Client for std::sync::Arc<C> {
    async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
        (**self).begin().await
    }

    async fn exec(&self, sql: &str) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        (**self).exec(sql).await
    }
}

/// Runs statements inside one transaction, and ends it. A statement's
/// numbered placeholders (`?1`, `?2`, ...) take its arguments in order, one
/// for each.
#[async_trait]
pub trait Conn: Send {
    /// Runs a statement and returns its rows, each as its columns' values,
    /// in the order the statement selects them.
    async fn query(
        &mut self,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, ClientError>;
    /// Runs a statement that returns no rows, and returns how many rows it
    /// changed.
    async fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError>;
    /// Commits the transaction.
    async fn commit(self: Box<Self>) -> Result<(), ClientError>;
    /// Rolls the transaction back.
    async fn rollback(self: Box<Self>) -> Result<(), ClientError>;
}

#[cfg(feature = "rusqlite")]
pub use rusqlite_binding::Rusqlite;

#[cfg(feature = "rusqlite")]
mod rusqlite_binding {
    use std::ops::DerefMut;

    use async_trait::async_trait;
    use rusqlite::types::{Value, ValueRef};
    use tokio::sync::{Mutex, MutexGuard};

    use super::{Client, ClientError, Conn, SqlValue};

    /// The savepoint a transaction inside the caller's is.
    const SAVEPOINT: &str = "superschematic_versiongraph";

    /// The default [`Client`]: rusqlite over one connection, with the SQLite
    /// rusqlite bundles. Each engine operation runs in a transaction of its
    /// own on the connection, or in a savepoint of a transaction the caller
    /// holds there ([`Rusqlite::connection`]), and operations on one binding
    /// take turns.
    ///
    /// The connection sits behind an async mutex, and rusqlite's calls are
    /// synchronous: each statement blocks the executor thread that runs it
    /// until SQLite returns, and a `BEGIN IMMEDIATE` that another
    /// connection's transaction keeps from the write lock blocks it for up to
    /// the connection's busy timeout (rusqlite's default is 5 seconds). An
    /// engine operation awaits nothing else while its transaction is open,
    /// so it runs to its end once begun; but where the waiting connection
    /// and the transaction it waits for share one thread, the transaction
    /// cannot end until the wait fails with `SQLITE_BUSY`. One connection
    /// per file, as D16's one writer has it, never waits so.
    pub struct Rusqlite {
        session: Mutex<Session>,
    }

    /// The connection, and the transaction the binding began on it and has
    /// not ended.
    struct Session {
        connection: rusqlite::Connection,
        open: Option<Open>,
    }

    /// How the binding began its transaction.
    #[derive(Debug, Clone, Copy)]
    enum Open {
        /// `BEGIN IMMEDIATE`, on a connection in autocommit mode.
        Transaction,
        /// A savepoint, inside a transaction the caller holds.
        Savepoint,
    }

    /// How many prepared statements the binding keeps on its connection:
    /// room for every statement the adapter runs (about 35 under one name
    /// function), where rusqlite keeps 16 unless told otherwise.
    const STATEMENT_CACHE: usize = 64;

    impl Rusqlite {
        /// Binds an open connection, keeping up to 64 of the adapter's
        /// prepared statements on it.
        pub fn new(connection: rusqlite::Connection) -> Self {
            connection.set_prepared_statement_cache_capacity(STATEMENT_CACHE);
            Rusqlite {
                session: Mutex::new(Session {
                    connection,
                    open: None,
                }),
            }
        }

        /// The bound connection, once no operation holds it: for a caller's
        /// own statements, and for a transaction the caller holds around
        /// engine operations, each of which then runs as a savepoint inside
        /// it. The binding's transactions take turns with it, so drop it
        /// before an engine operation over this binding, which waits for it.
        pub async fn connection(&self) -> impl DerefMut<Target = rusqlite::Connection> + '_ {
            MutexGuard::map(self.session.lock().await, |session| {
                // A transaction a dropped operation left, which its drop
                // could not roll back, ends before the caller's statements.
                let _ = session.settle();
                &mut session.connection
            })
        }

        /// The bound connection, given back.
        pub fn into_inner(self) -> rusqlite::Connection {
            self.session.into_inner().connection
        }
    }

    fn client_error(error: rusqlite::Error) -> ClientError {
        let code = match &error {
            rusqlite::Error::SqliteFailure(failure, _) => Some(failure.extended_code),
            // A statement SQLite could not prepare, with where it stopped.
            rusqlite::Error::SqlInputError { error, .. } => Some(error.extended_code),
            _ => None,
        };
        ClientError {
            code,
            source: Box::new(error),
        }
    }

    fn bound(args: &[SqlValue]) -> Vec<Value> {
        args.iter()
            .map(|arg| match arg {
                SqlValue::Null => Value::Null,
                SqlValue::Int(n) => Value::Integer(*n),
                SqlValue::Real(r) => Value::Real(*r),
                SqlValue::Text(s) => Value::Text(s.clone()),
                SqlValue::Blob(b) => Value::Blob(b.clone()),
            })
            .collect()
    }

    fn cell(value: ValueRef<'_>) -> Result<SqlValue, ClientError> {
        Ok(match value {
            ValueRef::Null => SqlValue::Null,
            ValueRef::Integer(n) => SqlValue::Int(n),
            ValueRef::Real(r) => SqlValue::Real(r),
            ValueRef::Text(bytes) => SqlValue::Text(
                String::from_utf8(bytes.to_vec())
                    .map_err(|e| ClientError::new(format!("a text column is not UTF-8: {e}")))?,
            ),
            ValueRef::Blob(bytes) => SqlValue::Blob(bytes.to_vec()),
        })
    }

    impl Session {
        fn batch(&self, sql: &str) -> Result<(), ClientError> {
            self.connection.execute_batch(sql).map_err(client_error)
        }

        fn query(&self, sql: &str, args: &[SqlValue]) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            let mut statement = self.connection.prepare_cached(sql).map_err(client_error)?;
            let columns = statement.column_count();
            let mut rows = statement
                .query(rusqlite::params_from_iter(bound(args)))
                .map_err(client_error)?;
            let mut out = Vec::new();
            while let Some(row) = rows.next().map_err(client_error)? {
                let mut cells = Vec::with_capacity(columns);
                for i in 0..columns {
                    cells.push(cell(row.get_ref(i).map_err(client_error)?)?);
                }
                out.push(cells);
            }
            Ok(out)
        }

        fn execute(&self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
            let mut statement = self.connection.prepare_cached(sql).map_err(client_error)?;
            let changed = statement
                .execute(rusqlite::params_from_iter(bound(args)))
                .map_err(client_error)?;
            Ok(u64::try_from(changed).unwrap_or(u64::MAX))
        }

        /// Begins a transaction: `BEGIN IMMEDIATE` on a connection in
        /// autocommit mode, else a savepoint inside the caller's.
        fn begin(&mut self) -> Result<(), ClientError> {
            self.settle()?;
            let open = if self.connection.is_autocommit() {
                self.batch("BEGIN IMMEDIATE")?;
                Open::Transaction
            } else {
                self.batch(&format!("SAVEPOINT {SAVEPOINT}"))?;
                Open::Savepoint
            };
            self.open = Some(open);
            Ok(())
        }

        /// Rolls back a transaction the binding began and a dropped
        /// operation left open.
        fn settle(&mut self) -> Result<(), ClientError> {
            match self.open {
                Some(open) => self.rollback(open),
                None => Ok(()),
            }
        }

        fn commit(&mut self, open: Open) -> Result<(), ClientError> {
            let sql = match open {
                Open::Transaction => "COMMIT".to_owned(),
                Open::Savepoint => format!("RELEASE {SAVEPOINT}"),
            };
            match self.batch(&sql) {
                Ok(()) => {
                    self.open = None;
                    Ok(())
                }
                Err(error) => {
                    // The commit's error is the one to report.
                    let _ = self.rollback(open);
                    Err(error)
                }
            }
        }

        fn rollback(&mut self, open: Open) -> Result<(), ClientError> {
            // SQLite ends a transaction itself on some failures (a full disk,
            // say), and then there is nothing left to roll back.
            let result = if self.connection.is_autocommit() {
                Ok(())
            } else {
                match open {
                    Open::Transaction => self.batch("ROLLBACK"),
                    Open::Savepoint => {
                        self.batch(&format!("ROLLBACK TO {SAVEPOINT}; RELEASE {SAVEPOINT}"))
                    }
                }
            };
            if result.is_ok() {
                self.open = None;
            }
            result
        }
    }

    #[async_trait]
    impl Client for Rusqlite {
        async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
            let mut session = self.session.lock().await;
            session.begin()?;
            Ok(Box::new(RusqliteConn {
                session,
                ended: false,
            }))
        }

        async fn exec(&self, sql: &str) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            let mut session = self.session.lock().await;
            session.settle()?;
            session.query(sql, &[])
        }
    }

    struct RusqliteConn<'a> {
        session: MutexGuard<'a, Session>,
        ended: bool,
    }

    /// An operation dropped before it committed or rolled back, as a
    /// cancelled future is, rolls its transaction back at once, which frees
    /// the file's write lock for other connections; a rollback that fails
    /// here is tried again when the binding next begins.
    impl Drop for RusqliteConn<'_> {
        fn drop(&mut self) {
            if !self.ended {
                let _ = self.session.settle();
            }
        }
    }

    #[async_trait]
    impl Conn for RusqliteConn<'_> {
        async fn query(
            &mut self,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            self.session.query(sql, args)
        }

        async fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
            self.session.execute(sql, args)
        }

        async fn commit(mut self: Box<Self>) -> Result<(), ClientError> {
            self.ended = true;
            match self.session.open {
                Some(open) => self.session.commit(open),
                None => Ok(()),
            }
        }

        async fn rollback(mut self: Box<Self>) -> Result<(), ClientError> {
            self.ended = true;
            self.session.settle()
        }
    }
}
