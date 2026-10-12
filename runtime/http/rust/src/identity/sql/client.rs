//! The seam the SQL store runs its statements through. `TokioPostgres` (the
//! `identity-postgres` feature) and `Rusqlite` (the `identity-sqlite`
//! feature) bind it; another driver, or a pool, implements the two traits
//! itself.

use std::fmt;
use std::time::SystemTime;

use async_trait::async_trait;

/// The SQL a store writes: Postgres, over the tables `create.sql` creates,
/// or SQLite, over those `sqlite/create.sql` creates.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Dialect {
    /// `$n` placeholders, `TIMESTAMPTZ` timestamps, `TEXT[]` permissions.
    Postgres,
    /// `?` placeholders, timestamps as text to the millisecond in UTC, and
    /// permissions as a JSON array in text.
    Sqlite,
}

/// A statement argument or a result column. The store binds texts, times
/// and lists of texts; a binding sends each as the type the statement
/// gives its parameter, so a key reaches a `UUID` column and a login a
/// `CITEXT` one as written. A result column is a text (a Postgres UUID in
/// its hyphenated form), an integer, a boolean, a time, a list of texts,
/// or `Null`.
#[derive(Clone, Debug, PartialEq)]
pub enum SqlValue {
    Null,
    Text(String),
    Int(i64),
    Bool(bool),
    Time(SystemTime),
    TextList(Vec<String>),
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

/// A statement or transaction the database refused. `code` is the
/// database's code when the driver gave one: Postgres's SQLSTATE (`23505`
/// is a unique violation), or SQLite's extended result code (`2067`).
#[derive(Debug)]
pub struct ClientError {
    pub code: Option<String>,
    pub source: Box<dyn std::error::Error + Send + Sync>,
}

impl ClientError {
    /// An error with no code.
    pub fn new(source: impl Into<Box<dyn std::error::Error + Send + Sync>>) -> Self {
        ClientError {
            code: None,
            source: source.into(),
        }
    }

    /// Whether a unique index refused the row.
    pub fn is_unique_violation(&self) -> bool {
        matches!(self.code.as_deref(), Some("23505" | "2067" | "1555"))
    }
}

impl fmt::Display for ClientError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match &self.code {
            Some(code) => write!(f, "{} (code {code})", self.source),
            None => self.source.fmt(f),
        }
    }
}

impl std::error::Error for ClientError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.source.as_ref())
    }
}

/// Runs the store's statements: one at a time on their own, or in a
/// transaction. The statements are the dialect's own, placeholders
/// included.
#[async_trait]
pub trait Client: Send + Sync + 'static {
    /// The SQL the client speaks.
    fn dialect(&self) -> Dialect;
    /// Runs one statement on its own and returns its rows, each as its
    /// columns' values in the order the statement selects them.
    async fn query(&self, sql: &str, args: &[SqlValue]) -> Result<Vec<Vec<SqlValue>>, ClientError>;
    /// Runs one statement on its own and returns how many rows it changed.
    async fn execute(&self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError>;
    /// Begins a transaction. No statement another caller runs on its own
    /// joins it.
    async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError>;
}

/// Runs statements inside one transaction, and ends it. A transaction
/// dropped without either is rolled back before the client's next
/// statement.
#[async_trait]
pub trait Conn: Send {
    async fn query(
        &mut self,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, ClientError>;
    async fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError>;
    async fn commit(self: Box<Self>) -> Result<(), ClientError>;
    async fn rollback(self: Box<Self>) -> Result<(), ClientError>;
}

#[async_trait]
impl<C: Client + ?Sized> Client for std::sync::Arc<C> {
    fn dialect(&self) -> Dialect {
        (**self).dialect()
    }

    async fn query(&self, sql: &str, args: &[SqlValue]) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        (**self).query(sql, args).await
    }

    async fn execute(&self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
        (**self).execute(sql, args).await
    }

    async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
        (**self).begin().await
    }
}

#[cfg(feature = "identity-postgres")]
pub use postgres_binding::TokioPostgres;

#[cfg(feature = "identity-postgres")]
mod postgres_binding {
    use std::collections::HashMap;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::Mutex;
    use std::time::SystemTime;

    use async_trait::async_trait;
    use tokio::sync::{RwLock, RwLockReadGuard, RwLockWriteGuard};
    use tokio_postgres::types::{Kind, ToSql, Type};
    use tokio_postgres::Statement;

    use super::{Client, ClientError, Conn, Dialect, SqlValue};

    /// The Postgres [`Client`] over one tokio-postgres connection. A
    /// statement on its own runs beside the others, pipelined on the
    /// connection; a transaction holds the connection alone until it ends,
    /// and statements on their own wait for it. A service whose writes
    /// should not wait on each other binds a pool through [`Client`].
    pub struct TokioPostgres {
        session: RwLock<Session>,
        statements: Mutex<HashMap<String, Statement>>,
    }

    struct Session {
        client: tokio_postgres::Client,
        // A transaction the binding began and never ended, because the
        // future that held it was dropped. The next statement rolls it
        // back first.
        open: AtomicBool,
    }

    impl TokioPostgres {
        /// Binds a connected client. Drive its connection (the future
        /// `tokio_postgres::connect` returns beside it) on a task of its
        /// own.
        pub fn new(client: tokio_postgres::Client) -> Self {
            TokioPostgres {
                session: RwLock::new(Session {
                    client,
                    open: AtomicBool::new(false),
                }),
                statements: Mutex::new(HashMap::new()),
            }
        }

        /// The connection, once no transaction holds it.
        async fn shared(&self) -> Result<RwLockReadGuard<'_, Session>, ClientError> {
            loop {
                let session = self.session.read().await;
                if !session.open.load(Ordering::SeqCst) {
                    return Ok(session);
                }
                drop(session);
                let session = self.session.write().await;
                roll_back_abandoned(&session).await?;
            }
        }

        async fn statement(
            &self,
            client: &tokio_postgres::Client,
            sql: &str,
        ) -> Result<Statement, ClientError> {
            if let Some(statement) = self.cached(sql) {
                return Ok(statement);
            }
            let statement = client.prepare(sql).await.map_err(client_error)?;
            if let Ok(mut cache) = self.statements.lock() {
                cache.insert(sql.to_owned(), statement.clone());
            }
            Ok(statement)
        }

        fn cached(&self, sql: &str) -> Option<Statement> {
            self.statements.lock().ok()?.get(sql).cloned()
        }

        async fn rows(
            &self,
            client: &tokio_postgres::Client,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            let statement = self.statement(client, sql).await?;
            let values = typed(&statement, args)?;
            let params: Vec<&(dyn ToSql + Sync)> = values
                .iter()
                .map(|v| v.as_ref() as &(dyn ToSql + Sync))
                .collect();
            let rows = client
                .query(&statement, &params)
                .await
                .map_err(client_error)?;
            rows.iter()
                .map(|row| (0..row.len()).map(|i| cell(row, i)).collect())
                .collect()
        }

        async fn run(
            &self,
            client: &tokio_postgres::Client,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<u64, ClientError> {
            let statement = self.statement(client, sql).await?;
            let values = typed(&statement, args)?;
            let params: Vec<&(dyn ToSql + Sync)> = values
                .iter()
                .map(|v| v.as_ref() as &(dyn ToSql + Sync))
                .collect();
            client
                .execute(&statement, &params)
                .await
                .map_err(client_error)
        }
    }

    async fn roll_back_abandoned(session: &Session) -> Result<(), ClientError> {
        if session.open.load(Ordering::SeqCst) {
            session
                .client
                .batch_execute("ROLLBACK")
                .await
                .map_err(client_error)?;
            session.open.store(false, Ordering::SeqCst);
        }
        Ok(())
    }

    fn client_error(error: tokio_postgres::Error) -> ClientError {
        ClientError {
            code: error.code().map(|code| code.code().to_owned()),
            source: Box::new(error),
        }
    }

    type Param = Box<dyn ToSql + Sync + Send>;

    /// Each argument as the type its parameter has in the statement.
    fn typed(statement: &Statement, args: &[SqlValue]) -> Result<Vec<Param>, ClientError> {
        let types = statement.params();
        if types.len() != args.len() {
            return Err(ClientError::new(format!(
                "the statement takes {} arguments, not {}",
                types.len(),
                args.len()
            )));
        }
        args.iter()
            .zip(types)
            .map(|(arg, ty)| param(arg, ty))
            .collect()
    }

    fn param(arg: &SqlValue, ty: &Type) -> Result<Param, ClientError> {
        let is_time = matches!(*ty, Type::TIMESTAMPTZ | Type::TIMESTAMP);
        Ok(match arg {
            SqlValue::Null if *ty == Type::UUID => Box::new(None::<uuid::Uuid>),
            SqlValue::Null if is_time => Box::new(None::<SystemTime>),
            SqlValue::Null if matches!(ty.kind(), Kind::Array(_)) => Box::new(None::<Vec<String>>),
            SqlValue::Null => Box::new(None::<String>),
            SqlValue::Text(text) if *ty == Type::UUID => Box::new(
                uuid::Uuid::parse_str(text)
                    .map_err(|err| ClientError::new(format!("{text:?} is not a UUID: {err}")))?,
            ),
            SqlValue::Text(text) => Box::new(text.clone()),
            SqlValue::Int(n) => match *ty {
                Type::INT2 => Box::new(i16::try_from(*n).map_err(ClientError::new)?),
                Type::INT4 => Box::new(i32::try_from(*n).map_err(ClientError::new)?),
                _ => Box::new(*n),
            },
            SqlValue::Bool(b) => Box::new(*b),
            SqlValue::Time(t) => Box::new(*t),
            SqlValue::TextList(list) => Box::new(list.clone()),
        })
    }

    fn cell(row: &tokio_postgres::Row, i: usize) -> Result<SqlValue, ClientError> {
        let ty = row.columns()[i].type_();
        let value = match *ty {
            Type::BOOL => row
                .try_get::<_, Option<bool>>(i)
                .map(|v| v.map_or(SqlValue::Null, SqlValue::Bool)),
            Type::INT2 => row
                .try_get::<_, Option<i16>>(i)
                .map(|v| v.map_or(SqlValue::Null, |n| SqlValue::Int(n.into()))),
            Type::INT4 => row
                .try_get::<_, Option<i32>>(i)
                .map(|v| v.map_or(SqlValue::Null, |n| SqlValue::Int(n.into()))),
            Type::INT8 => row
                .try_get::<_, Option<i64>>(i)
                .map(|v| v.map_or(SqlValue::Null, SqlValue::Int)),
            Type::UUID => row.try_get::<_, Option<uuid::Uuid>>(i).map(|v| {
                v.map_or(SqlValue::Null, |u| {
                    SqlValue::Text(u.hyphenated().to_string())
                })
            }),
            Type::TIMESTAMPTZ | Type::TIMESTAMP => row
                .try_get::<_, Option<SystemTime>>(i)
                .map(|v| v.map_or(SqlValue::Null, SqlValue::Time)),
            _ if matches!(ty.kind(), Kind::Array(_)) => row
                .try_get::<_, Option<Vec<String>>>(i)
                .map(|v| v.map_or(SqlValue::Null, SqlValue::TextList)),
            _ => row
                .try_get::<_, Option<String>>(i)
                .map(|v| v.map_or(SqlValue::Null, SqlValue::Text)),
        };
        value.map_err(client_error)
    }

    #[async_trait]
    impl Client for TokioPostgres {
        fn dialect(&self) -> Dialect {
            Dialect::Postgres
        }

        async fn query(
            &self,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            let session = self.shared().await?;
            self.rows(&session.client, sql, args).await
        }

        async fn execute(&self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
            let session = self.shared().await?;
            self.run(&session.client, sql, args).await
        }

        async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
            let session = self.session.write().await;
            roll_back_abandoned(&session).await?;
            session
                .client
                .batch_execute("BEGIN")
                .await
                .map_err(client_error)?;
            session.open.store(true, Ordering::SeqCst);
            Ok(Box::new(Transaction {
                binding: self,
                session,
            }))
        }
    }

    struct Transaction<'a> {
        binding: &'a TokioPostgres,
        session: RwLockWriteGuard<'a, Session>,
    }

    impl Transaction<'_> {
        async fn end(&self, sql: &str) -> Result<(), ClientError> {
            let result = self.session.client.batch_execute(sql).await;
            // A failed COMMIT ends the transaction too.
            self.session.open.store(false, Ordering::SeqCst);
            result.map_err(client_error)
        }
    }

    #[async_trait]
    impl Conn for Transaction<'_> {
        async fn query(
            &mut self,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            self.binding.rows(&self.session.client, sql, args).await
        }

        async fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
            self.binding.run(&self.session.client, sql, args).await
        }

        async fn commit(self: Box<Self>) -> Result<(), ClientError> {
            self.end("COMMIT").await
        }

        async fn rollback(self: Box<Self>) -> Result<(), ClientError> {
            self.end("ROLLBACK").await
        }
    }
}

#[cfg(feature = "identity-sqlite")]
pub use sqlite_binding::Rusqlite;

#[cfg(feature = "identity-sqlite")]
mod sqlite_binding {
    use async_trait::async_trait;
    use rusqlite::types::{Value, ValueRef};
    use tokio::sync::{Mutex, MutexGuard};

    use super::{Client, ClientError, Conn, Dialect, SqlValue};
    use crate::identity::time::format_sqlite;

    /// The SQLite [`Client`] over one rusqlite connection, with the SQLite
    /// rusqlite bundles. Statements and transactions take turns on the
    /// connection, as one writer holds a SQLite file.
    ///
    /// rusqlite's calls are synchronous, as the version graph's SQLite
    /// adapter has them: a statement blocks the executor thread that runs
    /// it until SQLite returns. The store's statements are lookups by a key
    /// or a unique index and writes of a row or a user's sessions, which
    /// return in microseconds, and no transaction awaits anything but its
    /// own statements; a `BEGIN IMMEDIATE` that another connection's
    /// transaction keeps from the write lock waits up to the connection's
    /// busy timeout. One connection per file, as the store holds it, never
    /// waits so.
    pub struct Rusqlite {
        session: Mutex<Session>,
    }

    struct Session {
        connection: rusqlite::Connection,
        // A transaction the binding began and never ended, because the
        // future that held it was dropped. The next statement rolls it
        // back first.
        open: bool,
    }

    /// How many prepared statements the binding keeps: room for every
    /// statement the store runs.
    const STATEMENT_CACHE: usize = 64;

    impl Rusqlite {
        /// Binds an open connection and turns its foreign keys on, so
        /// deleting a user deletes their sessions, credential and grants.
        pub fn new(connection: rusqlite::Connection) -> Result<Self, ClientError> {
            connection.set_prepared_statement_cache_capacity(STATEMENT_CACHE);
            connection
                .execute_batch("PRAGMA foreign_keys = ON")
                .map_err(client_error)?;
            Ok(Rusqlite {
                session: Mutex::new(Session {
                    connection,
                    open: false,
                }),
            })
        }

        /// The connection, with any abandoned transaction rolled back.
        async fn ready(&self) -> Result<MutexGuard<'_, Session>, ClientError> {
            let mut session = self.session.lock().await;
            if session.open {
                session
                    .connection
                    .execute_batch("ROLLBACK")
                    .map_err(client_error)?;
                session.open = false;
            }
            Ok(session)
        }
    }

    fn client_error(error: rusqlite::Error) -> ClientError {
        let code = match &error {
            rusqlite::Error::SqliteFailure(failure, _) => Some(failure.extended_code.to_string()),
            _ => None,
        };
        ClientError {
            code,
            source: Box::new(error),
        }
    }

    fn bind(args: &[SqlValue]) -> Vec<Value> {
        args.iter()
            .map(|arg| match arg {
                SqlValue::Null => Value::Null,
                SqlValue::Text(text) => Value::Text(text.clone()),
                SqlValue::Int(n) => Value::Integer(*n),
                SqlValue::Bool(b) => Value::Integer(i64::from(*b)),
                SqlValue::Time(t) => Value::Text(format_sqlite(*t)),
                SqlValue::TextList(list) => {
                    Value::Text(serde_json::to_string(list).unwrap_or_else(|_| "[]".to_owned()))
                }
            })
            .collect()
    }

    fn rows(
        connection: &rusqlite::Connection,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        let mut statement = connection.prepare_cached(sql).map_err(client_error)?;
        let columns = statement.column_count();
        let mut found = statement
            .query(rusqlite::params_from_iter(bind(args)))
            .map_err(client_error)?;
        let mut out = Vec::new();
        while let Some(row) = found.next().map_err(client_error)? {
            let mut values = Vec::with_capacity(columns);
            for i in 0..columns {
                values.push(match row.get_ref(i).map_err(client_error)? {
                    ValueRef::Null => SqlValue::Null,
                    ValueRef::Integer(n) => SqlValue::Int(n),
                    ValueRef::Real(x) => SqlValue::Text(x.to_string()),
                    ValueRef::Text(text) | ValueRef::Blob(text) => {
                        SqlValue::Text(String::from_utf8_lossy(text).into_owned())
                    }
                });
            }
            out.push(values);
        }
        Ok(out)
    }

    fn run(
        connection: &rusqlite::Connection,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<u64, ClientError> {
        let mut statement = connection.prepare_cached(sql).map_err(client_error)?;
        let changed = statement
            .execute(rusqlite::params_from_iter(bind(args)))
            .map_err(client_error)?;
        Ok(u64::try_from(changed).unwrap_or(u64::MAX))
    }

    #[async_trait]
    impl Client for Rusqlite {
        fn dialect(&self) -> Dialect {
            Dialect::Sqlite
        }

        async fn query(
            &self,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            let session = self.ready().await?;
            rows(&session.connection, sql, args)
        }

        async fn execute(&self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
            let session = self.ready().await?;
            run(&session.connection, sql, args)
        }

        async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
            let mut session = self.ready().await?;
            session
                .connection
                .execute_batch("BEGIN IMMEDIATE")
                .map_err(client_error)?;
            session.open = true;
            Ok(Box::new(Transaction { session }))
        }
    }

    struct Transaction<'a> {
        session: MutexGuard<'a, Session>,
    }

    impl Transaction<'_> {
        fn end(&mut self, sql: &str) -> Result<(), ClientError> {
            let result = self.session.connection.execute_batch(sql);
            if result.is_ok() || self.session.connection.is_autocommit() {
                self.session.open = false;
            }
            result.map_err(client_error)
        }
    }

    #[async_trait]
    impl Conn for Transaction<'_> {
        async fn query(
            &mut self,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            rows(&self.session.connection, sql, args)
        }

        async fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
            run(&self.session.connection, sql, args)
        }

        async fn commit(mut self: Box<Self>) -> Result<(), ClientError> {
            self.end("COMMIT")
        }

        async fn rollback(mut self: Box<Self>) -> Result<(), ClientError> {
            self.end("ROLLBACK")
        }
    }
}
