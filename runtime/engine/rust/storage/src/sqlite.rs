//! SQLite through rusqlite and the SQLite it bundles: one file, one
//! connection that writes it (D16), as `storage/storage.ts` holds one.
//!
//! Opening it sets the busy timeout, write-ahead logging and foreign keys,
//! which SQLite ignores inside a transaction, so they are set first. A
//! file that cannot use write-ahead logging (`:memory:`) is refused, as
//! the TypeScript engine refuses it. Statements are prepared once per text
//! and reused. A failure SQLite reports carries its extended result code:
//! `SQLITE_BUSY` is [`Error::Busy`], `SQLITE_CONSTRAINT_UNIQUE` and
//! `SQLITE_CONSTRAINT_PRIMARYKEY` are [`Error::Unique`].

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use rusqlite::types::{ToSqlOutput, ValueRef};
use rusqlite::{Connection, ToSql};

use crate::conn::{Conn, Failure, Outcome, Pool, RawConn};
use crate::db::{Backend, Db, DEFAULT_BUSY_TIMEOUT};
use crate::dialect::Dialect;
use crate::error::{
    DbCode, Error, Result, SQLITE_BUSY, SQLITE_CONSTRAINT_PRIMARYKEY, SQLITE_CONSTRAINT_UNIQUE,
};
use crate::notifier::Notifier;
use crate::value::SqlValue;

/// How a SQLite file opens.
#[derive(Clone, Debug)]
pub struct SqliteOptions {
    /// How long a statement waits for another connection's lock (default
    /// 5000 ms); SQLite takes it in whole milliseconds, up to 2^31 - 1.
    pub busy_timeout: Duration,
}

impl Default for SqliteOptions {
    fn default() -> Self {
        SqliteOptions {
            busy_timeout: DEFAULT_BUSY_TIMEOUT,
        }
    }
}

/// How many prepared statements a connection keeps for reuse.
const STATEMENT_CACHE: usize = 256;

impl Db {
    /// Opens (creating if absent) the SQLite file at `path`.
    pub fn open_sqlite(path: impl AsRef<Path>, options: SqliteOptions) -> Result<Db> {
        let path = path.as_ref().to_path_buf();
        let busy_ms = i32::try_from(options.busy_timeout.as_millis()).map_err(|_| {
            Error::usage(format!(
                "busy_timeout must be at most {} ms, got {} ms",
                i32::MAX,
                options.busy_timeout.as_millis()
            ))
        })?;
        let label = path.display().to_string();
        let first = open_connection(&path, &label, busy_ms)?;
        let connect_path = path.clone();
        let connect_label = label.clone();
        let pool = Pool::new(
            1,
            vec![first],
            Box::new(move || open_connection(&connect_path, &connect_label, busy_ms)),
            <SqliteConn as RawConn>::healthy,
        );
        Ok(Db::new(
            Box::new(SqliteBackend {
                label,
                pool,
                _path: path,
            }),
            options.busy_timeout,
            options.busy_timeout,
            Arc::new(Notifier::new()),
            Vec::new(),
        ))
    }
}

fn open_connection(path: &Path, label: &str, busy_ms: i32) -> Result<SqliteConn> {
    let conn = Connection::open(path).map_err(sqlite_error)?;
    conn.execute_batch(&format!("PRAGMA busy_timeout = {busy_ms}"))
        .map_err(sqlite_error)?;
    let mode: String = conn
        .query_row("PRAGMA journal_mode = WAL", [], |row| row.get(0))
        .map_err(sqlite_error)?;
    if !mode.eq_ignore_ascii_case("wal") {
        return Err(Error::usage(format!(
            "{label}: SQLite would not use write-ahead logging (journal mode {mode})"
        )));
    }
    conn.execute_batch("PRAGMA foreign_keys = ON")
        .map_err(sqlite_error)?;
    conn.set_prepared_statement_cache_capacity(STATEMENT_CACHE);
    Ok(SqliteConn { conn })
}

struct SqliteBackend {
    label: String,
    pool: Pool<SqliteConn>,
    _path: PathBuf,
}

impl Backend for SqliteBackend {
    fn dialect(&self) -> Dialect {
        Dialect::Sqlite
    }

    fn acquire(&self, timeout: Duration) -> Result<Box<dyn Conn + '_>> {
        Ok(Box::new(self.pool.get(timeout)?))
    }

    fn label(&self) -> &str {
        &self.label
    }

    fn postgres_schema(&self) -> Option<&str> {
        None
    }

    fn schemas(&self) -> &[String] {
        &[]
    }

    fn channel(&self) -> Option<&str> {
        None
    }

    // One process writes the file, so its runner holds the lease.
    fn try_lease(&self, _key: i64) -> Result<Option<crate::db::Lease>> {
        Ok(Some(crate::db::Lease::local()))
    }
}

struct SqliteConn {
    conn: Connection,
}

impl RawConn for SqliteConn {
    fn control(&mut self, sql: &str) -> Result<()> {
        self.conn.execute_batch(sql).map_err(sqlite_error)
    }

    fn batch(&mut self, sql: &str) -> Result<()> {
        self.conn.execute_batch(sql).map_err(sqlite_error)
    }

    fn statement(
        &mut self,
        sql: &str,
        params: &[SqlValue],
        read_only: bool,
    ) -> Result<Outcome, Failure> {
        // total_changes() counts every row a statement writes, a RETURNING
        // statement's and its triggers' included; changes() would repeat
        // the last write's count after a SELECT.
        let before = self.conn.total_changes();
        let mut statement = self
            .conn
            .prepare_cached(sql)
            .map_err(|error| Failure::reached(sqlite_error(error)))?;
        if read_only && !statement.readonly() {
            return Err(Failure::refused(Error::usage(format!(
                "a read-only transaction cannot run a statement that writes: {}",
                sql.trim()
            ))));
        }
        if statement.parameter_count() != params.len() {
            return Err(Failure::refused(Error::usage(format!(
                "the statement binds {} parameters and was given {}: {}",
                statement.parameter_count(),
                params.len(),
                sql.trim()
            ))));
        }
        let columns: Arc<[String]> = statement
            .column_names()
            .into_iter()
            .map(str::to_owned)
            .collect();
        let width = columns.len();
        let mut rows = statement
            .query(rusqlite::params_from_iter(params))
            .map_err(|error| Failure::reached(sqlite_error(error)))?;
        let mut read = Vec::new();
        while let Some(row) = rows
            .next()
            .map_err(|error| Failure::reached(sqlite_error(error)))?
        {
            let mut values = Vec::with_capacity(width);
            for index in 0..width {
                let value = row
                    .get_ref(index)
                    .map_err(|error| Failure::reached(sqlite_error(error)))?;
                values.push(value_of(value));
            }
            read.push(values);
        }
        drop(rows);
        drop(statement);
        Ok(Outcome {
            columns,
            rows: read,
            changes: self.conn.total_changes() - before,
            last_insert_rowid: Some(self.conn.last_insert_rowid()),
        })
    }
}

impl ToSql for SqlValue {
    fn to_sql(&self) -> rusqlite::Result<ToSqlOutput<'_>> {
        Ok(ToSqlOutput::Borrowed(match self {
            SqlValue::Null => ValueRef::Null,
            SqlValue::Integer(value) => ValueRef::Integer(*value),
            SqlValue::Real(value) => ValueRef::Real(*value),
            SqlValue::Text(value) => ValueRef::Text(value.as_bytes()),
            SqlValue::Blob(value) => ValueRef::Blob(value),
        }))
    }
}

fn value_of(value: ValueRef<'_>) -> SqlValue {
    match value {
        ValueRef::Null => SqlValue::Null,
        ValueRef::Integer(value) => SqlValue::Integer(value),
        ValueRef::Real(value) => SqlValue::Real(value),
        ValueRef::Text(text) => SqlValue::Text(String::from_utf8_lossy(text).into_owned()),
        ValueRef::Blob(bytes) => SqlValue::Blob(bytes.to_vec()),
    }
}

/// A rusqlite failure as the storage's error.
fn sqlite_error(error: rusqlite::Error) -> Error {
    match error {
        rusqlite::Error::SqliteFailure(failure, message) => {
            let code = failure.extended_code;
            let message = message.unwrap_or_else(|| failure.to_string());
            if code & 0xff == SQLITE_BUSY {
                Error::Busy {
                    code: Some(DbCode::Sqlite(code)),
                    message,
                }
            } else if code == SQLITE_CONSTRAINT_UNIQUE || code == SQLITE_CONSTRAINT_PRIMARYKEY {
                Error::Unique {
                    index: sqlite_failed_index(&message),
                    code: DbCode::Sqlite(code),
                    message,
                }
            } else {
                Error::Database {
                    code: DbCode::Sqlite(code),
                    message,
                }
            }
        }
        other => Error::Value {
            message: other.to_string(),
        },
    }
}

/// The index a SQLite unique constraint failure names, or `None` for a
/// column's or a primary key's (`UNIQUE constraint failed: t.a, t.b`),
/// as `failedIndex` reads it (`instances/indexes.ts`).
fn sqlite_failed_index(message: &str) -> Option<String> {
    let rest = message.split("UNIQUE constraint failed: index '").nth(1)?;
    let name = rest.split('\'').next()?;
    let valid = !name.is_empty() && name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_');
    valid.then(|| name.to_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_unique_failure_names_its_index() {
        assert_eq!(
            sqlite_failed_index("UNIQUE constraint failed: index 'engine_unique_ab12'"),
            Some("engine_unique_ab12".to_owned())
        );
        assert_eq!(
            sqlite_failed_index("UNIQUE constraint failed: skus.code"),
            None
        );
    }
}
