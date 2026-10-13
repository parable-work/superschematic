//! Test hosts: a synchronous transaction over plain rusqlite and over the
//! synchronous `postgres` crate, each a `fixed::HostTx` as D58's engine's
//! transaction will be, which runs each engine operation in a savepoint of
//! its own; and a Postgres schema of its own for the fixed layout, over the
//! synchronous client.
//!
//! The synchronous `postgres` client blocks on a runtime of its own, so a
//! test that uses it runs on no tokio runtime: a plain `#[test]`.

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use postgres::types::{IsNull, ToSql, Type};
use superschematic_versiongraph_engine::fixed::{ClientError, HostTx, SqlValue};

/// The savepoint each operation runs in.
const SAVEPOINT: &str = "vg_test_host";

/// A transaction on a rusqlite connection, as a host holds one: each
/// statement runs on the connection, and each engine operation in a
/// savepoint, so one that fails rolls back alone and the host's
/// transaction goes on.
#[cfg(feature = "rusqlite")]
pub struct RusqliteHost<'c> {
    pub connection: &'c rusqlite::Connection,
}

#[cfg(feature = "rusqlite")]
impl RusqliteHost<'_> {
    fn failed(error: rusqlite::Error) -> ClientError {
        let code = match &error {
            rusqlite::Error::SqliteFailure(failure, _) => Some(failure.extended_code),
            _ => None,
        };
        ClientError {
            code,
            source: Box::new(error),
        }
    }

    fn bound(args: &[SqlValue]) -> Vec<rusqlite::types::Value> {
        use rusqlite::types::Value;
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
}

#[cfg(feature = "rusqlite")]
impl HostTx for RusqliteHost<'_> {
    fn query(&mut self, sql: &str, args: &[SqlValue]) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        use rusqlite::types::ValueRef;
        let mut statement = self.connection.prepare_cached(sql).map_err(Self::failed)?;
        let columns = statement.column_count();
        let mut rows = statement
            .query(rusqlite::params_from_iter(Self::bound(args)))
            .map_err(Self::failed)?;
        let mut out = Vec::new();
        while let Some(row) = rows.next().map_err(Self::failed)? {
            let mut cells = Vec::with_capacity(columns);
            for i in 0..columns {
                cells.push(match row.get_ref(i).map_err(Self::failed)? {
                    ValueRef::Null => SqlValue::Null,
                    ValueRef::Integer(n) => SqlValue::Int(n),
                    ValueRef::Real(r) => SqlValue::Real(r),
                    ValueRef::Text(bytes) => {
                        SqlValue::Text(String::from_utf8(bytes.to_vec()).map_err(ClientError::new)?)
                    }
                    ValueRef::Blob(bytes) => SqlValue::Blob(bytes.to_vec()),
                });
            }
            out.push(cells);
        }
        Ok(out)
    }

    fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
        let mut statement = self.connection.prepare_cached(sql).map_err(Self::failed)?;
        let changed = statement
            .execute(rusqlite::params_from_iter(Self::bound(args)))
            .map_err(Self::failed)?;
        Ok(u64::try_from(changed).unwrap_or(u64::MAX))
    }

    fn begin(&mut self) -> Result<(), ClientError> {
        self.connection
            .execute_batch(&format!("SAVEPOINT {SAVEPOINT}"))
            .map_err(Self::failed)
    }

    fn commit(&mut self) -> Result<(), ClientError> {
        self.connection
            .execute_batch(&format!("RELEASE {SAVEPOINT}"))
            .map_err(Self::failed)
    }

    fn rollback(&mut self) -> Result<(), ClientError> {
        self.connection
            .execute_batch(&format!("ROLLBACK TO {SAVEPOINT}; RELEASE {SAVEPOINT}"))
            .map_err(Self::failed)
    }
}

/// A statement's `?` placeholders numbered `$1`, `$2`, ... in order, as a
/// host on Postgres numbers a behavior's (D58): quoted text and quoted
/// names keep what they hold.
pub fn dollar_placeholders(sql: &str) -> String {
    let mut out = String::with_capacity(sql.len() + 8);
    let mut n = 0;
    let mut quote: Option<char> = None;
    for c in sql.chars() {
        match (quote, c) {
            // A doubled quote closes and reopens, which comes to the same.
            (Some(q), _) if c == q => quote = None,
            (Some(_), _) => {}
            (None, '\'' | '"') => quote = Some(c),
            (None, '?') => {
                n += 1;
                out.push_str(&format!("${n}"));
                continue;
            }
            (None, _) => {}
        }
        out.push(c);
    }
    out
}

/// A null of whatever type Postgres gives its parameter.
#[derive(Debug)]
struct Null;

impl ToSql for Null {
    fn to_sql(
        &self,
        _: &Type,
        _: &mut postgres::types::private::BytesMut,
    ) -> Result<IsNull, Box<dyn std::error::Error + Sync + Send>> {
        Ok(IsNull::Yes)
    }

    fn accepts(_: &Type) -> bool {
        true
    }

    postgres::types::to_sql_checked!();
}

/// A transaction of the synchronous `postgres` client, as a host holds one:
/// each statement's `?` placeholders numbered `$1` in order, and each engine
/// operation in a savepoint, so one that fails rolls back alone and the
/// host's transaction goes on.
///
/// It binds as the fixed layout says a Postgres client may: a text as
/// text, an integer as `int8`, `Null` as a null of the parameter's type.
/// It refuses a statement with a parameter of any type but `text` or
/// `bigint`, and a column of any type but text or an integer, so the
/// layout's Postgres statements are held to what `SqlValue` promises.
pub struct PostgresHost<'t, 'c> {
    pub transaction: &'t mut postgres::Transaction<'c>,
}

impl PostgresHost<'_, '_> {
    fn failed(error: postgres::Error) -> ClientError {
        ClientError::new(error)
    }

    fn prepared(&mut self, sql: &str) -> Result<postgres::Statement, ClientError> {
        let numbered = dollar_placeholders(sql);
        let statement = self.transaction.prepare(&numbered).map_err(Self::failed)?;
        for (i, ty) in statement.params().iter().enumerate() {
            if *ty != Type::TEXT && *ty != Type::INT8 {
                return Err(ClientError::new(format!(
                    "parameter ${} is {ty}, not text or bigint: {numbered}",
                    i + 1
                )));
            }
        }
        Ok(statement)
    }

    fn params(args: &[SqlValue]) -> Result<Vec<Box<dyn ToSql + Sync>>, ClientError> {
        args.iter()
            .map(|arg| -> Result<Box<dyn ToSql + Sync>, ClientError> {
                Ok(match arg {
                    SqlValue::Null => Box::new(Null),
                    SqlValue::Int(n) => Box::new(*n),
                    SqlValue::Text(s) => Box::new(s.clone()),
                    other => {
                        return Err(ClientError::new(format!(
                            "the fixed layout binds no {other:?}"
                        )))
                    }
                })
            })
            .collect()
    }
}

impl HostTx for PostgresHost<'_, '_> {
    fn query(&mut self, sql: &str, args: &[SqlValue]) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        let statement = self.prepared(sql)?;
        let params = Self::params(args)?;
        let refs: Vec<&(dyn ToSql + Sync)> = params.iter().map(AsRef::as_ref).collect();
        let rows = self
            .transaction
            .query(&statement, &refs)
            .map_err(Self::failed)?;
        rows.iter()
            .map(|row| {
                row.columns()
                    .iter()
                    .enumerate()
                    .map(|(i, column)| {
                        let ty = column.type_();
                        let cell = if *ty == Type::INT8 {
                            row.try_get::<_, Option<i64>>(i)
                                .map(|v| v.map(SqlValue::Int))
                        } else if *ty == Type::INT4 {
                            row.try_get::<_, Option<i32>>(i)
                                .map(|v| v.map(|n| SqlValue::Int(n.into())))
                        } else if *ty == Type::INT2 {
                            row.try_get::<_, Option<i16>>(i)
                                .map(|v| v.map(|n| SqlValue::Int(n.into())))
                        } else if *ty == Type::TEXT || *ty == Type::VARCHAR {
                            row.try_get::<_, Option<String>>(i)
                                .map(|v| v.map(SqlValue::Text))
                        } else {
                            return Err(ClientError::new(format!(
                                "column {} is {ty}, not text or an integer",
                                column.name()
                            )));
                        };
                        cell.map(|v| v.unwrap_or(SqlValue::Null))
                            .map_err(Self::failed)
                    })
                    .collect()
            })
            .collect()
    }

    fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
        let statement = self.prepared(sql)?;
        let params = Self::params(args)?;
        let refs: Vec<&(dyn ToSql + Sync)> = params.iter().map(AsRef::as_ref).collect();
        self.transaction
            .execute(&statement, &refs)
            .map_err(Self::failed)
    }

    fn begin(&mut self) -> Result<(), ClientError> {
        self.transaction
            .batch_execute(&format!("SAVEPOINT {SAVEPOINT}"))
            .map_err(Self::failed)
    }

    fn commit(&mut self) -> Result<(), ClientError> {
        self.transaction
            .batch_execute(&format!("RELEASE SAVEPOINT {SAVEPOINT}"))
            .map_err(Self::failed)
    }

    fn rollback(&mut self) -> Result<(), ClientError> {
        self.transaction
            .batch_execute(&format!(
                "ROLLBACK TO SAVEPOINT {SAVEPOINT}; RELEASE SAVEPOINT {SAVEPOINT}"
            ))
            .map_err(Self::failed)
    }
}

/// A schema of its own for the fixed layout on Postgres, over the
/// synchronous client, dropped with the value. It holds nothing until a
/// test creates the layout in it.
pub struct PostgresSchema {
    pub dsn: String,
    pub name: String,
}

impl PostgresSchema {
    /// Creates a schema named after `prefix`.
    pub fn create(dsn: &str, prefix: &str) -> PostgresSchema {
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let n = NEXT.fetch_add(1, Ordering::Relaxed);
        let name = format!("{prefix}_{nanos}_{}_{n}", std::process::id());
        let mut admin =
            postgres::Client::connect(dsn, postgres::NoTls).expect("connect to Postgres");
        admin
            .batch_execute(&format!("CREATE SCHEMA {name}"))
            .expect("create the schema");
        PostgresSchema {
            dsn: dsn.to_owned(),
            name,
        }
    }

    /// A new connection whose search path is the schema alone.
    pub fn connect(&self) -> postgres::Client {
        let mut config: postgres::Config = self.dsn.parse().expect("parse the database URL");
        config.options(&format!("-c search_path={}", self.name));
        config
            .connect(postgres::NoTls)
            .expect("connect to Postgres")
    }
}

/// Drops the schema and everything in it, when the test ends and when it
/// panics, giving up on a lock it waits on past a few seconds. It runs on a
/// thread of its own, where no runtime keeps the synchronous client from
/// blocking.
impl Drop for PostgresSchema {
    fn drop(&mut self) {
        let statement = format!(
            "SET lock_timeout = '5s'; DROP SCHEMA IF EXISTS {} CASCADE",
            self.name
        );
        let dsn = self.dsn.clone();
        let dropped = std::thread::spawn(move || {
            postgres::Client::connect(&dsn, postgres::NoTls)
                .and_then(|mut admin| admin.batch_execute(&statement))
        })
        .join();
        if !matches!(dropped, Ok(Ok(()))) {
            eprintln!("could not drop schema {}: {dropped:?}", self.name);
        }
    }
}
