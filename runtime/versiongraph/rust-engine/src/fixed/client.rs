//! The seam the fixed layout runs its statements through, in either
//! dialect. The SQLite adapter's default binding, to rusqlite, is
//! `crate::sqlite::Rusqlite`.

use std::fmt;

use async_trait::async_trait;

/// A statement argument or a result column: one of SQLite's five storage
/// classes. The adapter binds texts, integers and `Null`, and reads texts,
/// integers and `Null` back; a column of another class is refused where it
/// reads one. On Postgres every parameter of the layout's statements is
/// `TEXT` or `BIGINT`, as its column or a cast makes it, and every column it
/// reads is `TEXT` or an integer, so a Postgres client binds a text as
/// text, an integer as `int8` and `Null` as a null of the parameter's type,
/// and reads `text` as `Text` and `int2`, `int4` and `int8` as `Int`.
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

/// A statement or transaction the database refused. `code` is SQLite's
/// extended result code when the driver gave one: 5 is `SQLITE_BUSY` and
/// 2067 `SQLITE_CONSTRAINT_UNIQUE`. On Postgres it is `None`: the layout's
/// statements there turn a taken name into no row rather than an error,
/// since an error aborts a Postgres transaction.
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

/// Runs the adapter's statements on one connection. The adapter asks it for
/// one transaction per engine operation. On SQLite `crate::sqlite::Rusqlite`
/// binds rusqlite; another driver implements the two traits itself, and
/// [`crate::fixed::HostClient`] binds a synchronous transaction a host
/// holds, in either dialect.
#[async_trait]
pub trait Client: Send + Sync {
    /// Begins one transaction. On a SQLite connection in autocommit mode it
    /// begins with `BEGIN IMMEDIATE`, which takes the file's write lock at once, so
    /// a transaction another connection holds makes it wait, up to the
    /// connection's busy timeout, and then fail with `SQLITE_BUSY`. Inside a
    /// transaction the caller holds on the connection it begins a savepoint,
    /// which the transaction's commit releases and its rollback rolls back
    /// to, leaving the caller's transaction open.
    async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError>;
    /// Runs one statement with no parameters on the connection, outside any
    /// transaction the client began, and returns its rows. The adapter
    /// calls it only on SQLite, when it binds the client or creates its
    /// tables: to turn the connection's foreign keys on and read them back,
    /// and to read SQLite's version.
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
/// for each, and a placeholder may appear more than once. In either
/// dialect the adapter writes them so; a Postgres client numbers them `$1`,
/// `$2`, ... A statement holds no `?` but its placeholders' outside quotes.
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
