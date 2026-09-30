//! The seam the Postgres adapter runs its statements through, and its
//! default binding to tokio-postgres.

use std::fmt;

use async_trait::async_trait;

/// A statement argument or a result column. Arguments are texts, integers,
/// booleans, lists of texts and of integers, and `Null`; a statement casts
/// each to its column's type. A result column is a text, an integer or a
/// boolean, or `Null`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SqlValue {
    Text(String),
    Int(i64),
    Bool(bool),
    TextList(Vec<String>),
    IntList(Vec<i64>),
    Null,
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

impl From<bool> for SqlValue {
    fn from(value: bool) -> Self {
        SqlValue::Bool(value)
    }
}

/// A statement or transaction the database refused. `code` is its SQLSTATE
/// when the database gave one (`23505` is a unique violation).
#[derive(Debug)]
pub struct ClientError {
    pub code: Option<String>,
    pub source: Box<dyn std::error::Error + Send + Sync>,
}

impl ClientError {
    /// An error with no SQLSTATE.
    pub fn new(source: impl Into<Box<dyn std::error::Error + Send + Sync>>) -> Self {
        ClientError {
            code: None,
            source: source.into(),
        }
    }
}

impl fmt::Display for ClientError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match &self.code {
            Some(code) => write!(f, "{} (SQLSTATE {code})", self.source),
            None => self.source.fmt(f),
        }
    }
}

impl std::error::Error for ClientError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.source.as_ref())
    }
}

/// Runs the adapter's statements. The adapter asks it for one transaction
/// per engine operation. [`TokioPostgres`] binds tokio-postgres; another
/// driver implements the two traits itself.
#[async_trait]
pub trait Client: Send + Sync {
    /// Begins one transaction.
    async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError>;
}

/// Runs statements inside one transaction, and ends it.
#[async_trait]
pub trait Conn: Send {
    /// Runs a statement and returns its rows, each as its columns' values.
    async fn query(
        &mut self,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, ClientError>;
    /// Runs a statement and returns how many rows it affected.
    async fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError>;
    /// Commits the transaction.
    async fn commit(self: Box<Self>) -> Result<(), ClientError>;
    /// Rolls the transaction back.
    async fn rollback(self: Box<Self>) -> Result<(), ClientError>;
}

#[cfg(feature = "tokio-postgres")]
pub use tokio_binding::TokioPostgres;

#[cfg(feature = "tokio-postgres")]
mod tokio_binding {
    use async_trait::async_trait;
    use tokio::sync::{Mutex, MutexGuard};
    use tokio_postgres::types::{ToSql, Type};

    use super::{Client, ClientError, Conn, SqlValue};

    /// The default [`Client`]: tokio-postgres over one connection. Each
    /// engine operation runs in a transaction of its own on the connection,
    /// and operations on one binding take turns. A service that runs
    /// operations side by side binds a pool through [`Client`], or builds a
    /// binding per connection.
    pub struct TokioPostgres {
        session: Mutex<Session>,
    }

    struct Session {
        client: tokio_postgres::Client,
        // A transaction the binding began and never ended, because the
        // operation that held it was dropped. The next begin rolls it back.
        open: bool,
    }

    impl TokioPostgres {
        /// Binds a connected client. Drive its connection (the future
        /// `tokio_postgres::connect` returns beside it) on a task of its own.
        pub fn new(client: tokio_postgres::Client) -> Self {
            TokioPostgres {
                session: Mutex::new(Session {
                    client,
                    open: false,
                }),
            }
        }

        /// The bound client, once no operation holds it.
        pub async fn client(&self) -> impl std::ops::Deref<Target = tokio_postgres::Client> + '_ {
            MutexGuard::map(self.session.lock().await, |session| &mut session.client)
        }
    }

    fn client_error(error: tokio_postgres::Error) -> ClientError {
        ClientError {
            code: error.code().map(|code| code.code().to_owned()),
            source: Box::new(error),
        }
    }

    #[async_trait]
    impl Client for TokioPostgres {
        async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
            let mut session = self.session.lock().await;
            if session.open {
                session
                    .client
                    .batch_execute("ROLLBACK")
                    .await
                    .map_err(client_error)?;
                session.open = false;
            }
            session
                .client
                .batch_execute("BEGIN")
                .await
                .map_err(client_error)?;
            session.open = true;
            Ok(Box::new(TokioConn { session }))
        }
    }

    struct TokioConn<'a> {
        session: MutexGuard<'a, Session>,
    }

    /// The statement's arguments with the type each is sent as: a text as
    /// `text`, which the statement casts, so a hyphenated UUID or JSON text
    /// reaches Postgres as written.
    fn typed(args: &[SqlValue]) -> (Vec<Type>, Vec<Box<dyn ToSql + Sync + Send>>) {
        let mut types = Vec::with_capacity(args.len());
        let mut values: Vec<Box<dyn ToSql + Sync + Send>> = Vec::with_capacity(args.len());
        for arg in args {
            let (ty, value): (Type, Box<dyn ToSql + Sync + Send>) = match arg {
                SqlValue::Text(s) => (Type::TEXT, Box::new(s.clone())),
                SqlValue::Int(n) => (Type::INT8, Box::new(*n)),
                SqlValue::Bool(b) => (Type::BOOL, Box::new(*b)),
                SqlValue::TextList(list) => (Type::TEXT_ARRAY, Box::new(list.clone())),
                SqlValue::IntList(list) => (Type::INT8_ARRAY, Box::new(list.clone())),
                SqlValue::Null => (Type::TEXT, Box::new(None::<String>)),
            };
            types.push(ty);
            values.push(value);
        }
        (types, values)
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
            _ => row
                .try_get::<_, Option<String>>(i)
                .map(|v| v.map_or(SqlValue::Null, SqlValue::Text)),
        };
        value.map_err(client_error)
    }

    #[async_trait]
    impl Conn for TokioConn<'_> {
        async fn query(
            &mut self,
            sql: &str,
            args: &[SqlValue],
        ) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            let (types, values) = typed(args);
            let client = &self.session.client;
            let statement = client
                .prepare_typed(sql, &types)
                .await
                .map_err(client_error)?;
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

        async fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
            let (types, values) = typed(args);
            let client = &self.session.client;
            let statement = client
                .prepare_typed(sql, &types)
                .await
                .map_err(client_error)?;
            let params: Vec<&(dyn ToSql + Sync)> = values
                .iter()
                .map(|v| v.as_ref() as &(dyn ToSql + Sync))
                .collect();
            client
                .execute(&statement, &params)
                .await
                .map_err(client_error)
        }

        async fn commit(mut self: Box<Self>) -> Result<(), ClientError> {
            let result = self.session.client.batch_execute("COMMIT").await;
            // A failed COMMIT ends the transaction too.
            self.session.open = false;
            result.map_err(client_error)
        }

        async fn rollback(mut self: Box<Self>) -> Result<(), ClientError> {
            let result = self.session.client.batch_execute("ROLLBACK").await;
            self.session.open = false;
            result.map_err(client_error)
        }
    }
}
