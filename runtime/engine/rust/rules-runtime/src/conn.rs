//! The two connections a runtime database is served through: a SQLite
//! file, whose publish runs the Rust runner in this process on a
//! connection of its own (D116), and a Postgres database, whose publish
//! runs the Go runner, `superschematic-migrate`, beside it.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use rusqlite::types::{Value as SqlValue, ValueRef};
use rusqlite::{Connection, ToSql};
use serde_json::{Map, Value};
use superschematic_rules::{Code, Refusal};

use crate::db::Db;
use crate::values::{internal, real, Dialect};
use crate::Binaries;

/// The refusal of a plan the runner refused or a step it failed:
/// `conflict`, of the class `PlanApplyError`.
pub(crate) fn apply_refusal(text: impl std::fmt::Display) -> Refusal {
    Refusal {
        class: "PlanApplyError".to_owned(),
        ..Refusal::new(
            Code::Conflict,
            format!("the runner did not apply the plan: {text}"),
        )
    }
}

/// A runtime database's connection.
pub(crate) enum Conn {
    Sqlite(SqliteConn),
    #[cfg(feature = "postgres")]
    Postgres(PgConn),
}

impl Conn {
    fn db(&mut self) -> &mut dyn Db {
        match self {
            Conn::Sqlite(conn) => conn,
            #[cfg(feature = "postgres")]
            Conn::Postgres(conn) => conn,
        }
    }

    fn db_ref(&self) -> &dyn Db {
        match self {
            Conn::Sqlite(conn) => conn,
            #[cfg(feature = "postgres")]
            Conn::Postgres(conn) => conn,
        }
    }

    /// Holds every request of the database's servers off while a publish
    /// runs: on Postgres the session's advisory lock that a request's
    /// transaction takes too; on SQLite nothing, since each request checks
    /// for the plan in progress inside its `BEGIN IMMEDIATE`.
    pub(crate) fn lock_publish(&mut self) -> Result<(), Refusal> {
        match self {
            Conn::Sqlite(_) => Ok(()),
            #[cfg(feature = "postgres")]
            Conn::Postgres(conn) => {
                conn.batch(&format!("SELECT pg_advisory_lock({})", pg::LOCK_KEY))
            }
        }
    }

    pub(crate) fn unlock_publish(&mut self) -> Result<(), Refusal> {
        match self {
            Conn::Sqlite(_) => Ok(()),
            #[cfg(feature = "postgres")]
            Conn::Postgres(conn) => {
                conn.batch(&format!("SELECT pg_advisory_unlock({})", pg::LOCK_KEY))
            }
        }
    }

    /// Applies a plan, expand and contract back to back, through the
    /// environment's runner: never over the serving connection.
    pub(crate) fn apply_plan(
        &mut self,
        document: &[u8],
        plan: &superschematic_migrate::Plan,
        binaries: &Binaries,
    ) -> Result<(), Refusal> {
        match self {
            Conn::Sqlite(conn) => {
                let _ = (document, binaries);
                conn.apply_plan(plan)
            }
            #[cfg(feature = "postgres")]
            Conn::Postgres(conn) => {
                let _ = plan;
                conn.apply_plan(document, binaries)
            }
        }
    }
}

impl Db for Conn {
    fn dialect(&self) -> Dialect {
        self.db_ref().dialect()
    }

    fn batch(&mut self, sql: &str) -> Result<(), Refusal> {
        self.db().batch(sql)
    }

    fn query(&mut self, sql: &str, params: &[Value]) -> Result<Vec<Map<String, Value>>, Refusal> {
        self.db().query(sql, params)
    }

    fn execute(&mut self, sql: &str, params: &[Value]) -> Result<u64, Refusal> {
        self.db().execute(sql, params)
    }

    fn begin(&mut self) -> Result<(), Refusal> {
        self.db().begin()
    }

    fn commit(&mut self) -> Result<(), Refusal> {
        self.db().commit()
    }

    fn rollback(&mut self) -> Result<(), Refusal> {
        self.db().rollback()
    }

    fn has_table(&mut self, table: &str) -> Result<bool, Refusal> {
        self.db().has_table(table)
    }
}

/// A SQLite file's serving connection, and the file's path, which the
/// runner opens a connection of its own to.
pub(crate) struct SqliteConn {
    conn: Connection,
    path: PathBuf,
}

fn sqlite_failed(error: rusqlite::Error) -> Refusal {
    if let rusqlite::Error::SqliteFailure(failure, _) = &error {
        match failure.code {
            rusqlite::ErrorCode::DatabaseBusy | rusqlite::ErrorCode::DatabaseLocked => {
                return Refusal::new(
                    Code::Busy,
                    format!("the runtime database is busy with another writer: {error}"),
                )
            }
            rusqlite::ErrorCode::ConstraintViolation => {
                return Refusal::new(
                    Code::Conflict,
                    format!("the runtime database refused the write: {error}"),
                )
            }
            _ => {}
        }
    }
    internal(format!("the database failed: {error}"))
}

fn sqlite_param(value: &Value) -> SqlValue {
    match value {
        Value::Null => SqlValue::Null,
        Value::Bool(flag) => SqlValue::Integer(i64::from(*flag)),
        Value::Number(n) => match n.as_i64() {
            Some(i) => SqlValue::Integer(i),
            None => SqlValue::Real(n.as_f64().unwrap_or_default()),
        },
        Value::String(text) => SqlValue::Text(text.clone()),
        other => SqlValue::Text(other.to_string()),
    }
}

fn sqlite_value(value: ValueRef<'_>) -> Value {
    match value {
        ValueRef::Null => Value::Null,
        ValueRef::Integer(i) => Value::from(i),
        ValueRef::Real(x) => real(x),
        ValueRef::Text(bytes) => Value::String(String::from_utf8_lossy(bytes).into_owned()),
        ValueRef::Blob(bytes) => {
            Value::String(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
        }
    }
}

/// The functions a list's filter calls on SQLite ([`crate::query`]): the
/// handler's own `ilike` of a text anywhere, and its equality of two JSON
/// values, each deterministic, so the planner may use them in an index's
/// expression too.
fn register_functions(conn: &Connection) -> rusqlite::Result<()> {
    use rusqlite::functions::FunctionFlags;
    let flags = FunctionFlags::SQLITE_UTF8 | FunctionFlags::SQLITE_DETERMINISTIC;
    conn.create_scalar_function(crate::query::ILIKE, 2, flags, |cx| {
        let text: Option<String> = cx.get(0)?;
        let pattern: Option<String> = cx.get(1)?;
        Ok(match (text, pattern) {
            (Some(text), Some(pattern)) => i64::from(superschematic_rules_handler::values::ilike(
                &text,
                &format!("%{pattern}%"),
            )),
            _ => 0,
        })
    })?;
    conn.create_scalar_function(crate::query::JSON_SAME, 2, flags, |cx| {
        let held: Option<String> = cx.get(0)?;
        let given: Option<String> = cx.get(1)?;
        let read = |text: Option<String>| -> Option<Value> {
            text.and_then(|text| serde_json::from_str(&text).ok())
        };
        Ok(match (read(held), read(given)) {
            (Some(held), Some(given)) => {
                i64::from(superschematic_rules_handler::values::same(&held, &given))
            }
            _ => 0,
        })
    })
}

impl SqliteConn {
    /// The serving connection to a SQLite file, with foreign keys on, as
    /// the plan's tables expect them, and the functions a list's filter
    /// calls.
    pub(crate) fn open(path: &Path) -> Result<Self, Refusal> {
        let conn = Connection::open(path).map_err(sqlite_failed)?;
        conn.busy_timeout(std::time::Duration::from_secs(5))
            .map_err(sqlite_failed)?;
        conn.execute_batch("PRAGMA foreign_keys = ON")
            .map_err(sqlite_failed)?;
        register_functions(&conn).map_err(sqlite_failed)?;
        Ok(SqliteConn {
            conn,
            path: path.to_owned(),
        })
    }

    /// The serving connection, for a test's or an operator's own SQL.
    pub(crate) fn connection(&self) -> &Connection {
        &self.conn
    }

    fn apply_plan(&mut self, plan: &superschematic_migrate::Plan) -> Result<(), Refusal> {
        let conn = Connection::open(&self.path).map_err(sqlite_failed)?;
        let mut runner = superschematic_migrate::Runner::new(&conn).map_err(apply_refusal)?;
        runner
            .apply(plan, superschematic_migrate::Phase::All)
            .map_err(apply_refusal)?;
        Ok(())
    }
}

impl Db for SqliteConn {
    fn dialect(&self) -> Dialect {
        Dialect::Sqlite
    }

    fn batch(&mut self, sql: &str) -> Result<(), Refusal> {
        self.conn.execute_batch(sql).map_err(sqlite_failed)
    }

    fn query(&mut self, sql: &str, params: &[Value]) -> Result<Vec<Map<String, Value>>, Refusal> {
        // Prepared afresh: a `SELECT *` a publish's plan has since given
        // other columns would keep the old statement's column names.
        let mut statement = self.conn.prepare(sql).map_err(sqlite_failed)?;
        let names: Vec<String> = statement
            .column_names()
            .into_iter()
            .map(str::to_owned)
            .collect();
        let bound: Vec<SqlValue> = params.iter().map(sqlite_param).collect();
        let refs: Vec<&dyn ToSql> = bound.iter().map(|value| value as &dyn ToSql).collect();
        let mut rows = statement.query(refs.as_slice()).map_err(sqlite_failed)?;
        let mut out = Vec::new();
        while let Some(row) = rows.next().map_err(sqlite_failed)? {
            let mut columns = Map::new();
            for (i, name) in names.iter().enumerate() {
                columns.insert(
                    name.clone(),
                    sqlite_value(row.get_ref(i).map_err(sqlite_failed)?),
                );
            }
            out.push(columns);
        }
        Ok(out)
    }

    fn execute(&mut self, sql: &str, params: &[Value]) -> Result<u64, Refusal> {
        let bound: Vec<SqlValue> = params.iter().map(sqlite_param).collect();
        let refs: Vec<&dyn ToSql> = bound.iter().map(|value| value as &dyn ToSql).collect();
        let mut statement = self.conn.prepare_cached(sql).map_err(sqlite_failed)?;
        statement
            .execute(refs.as_slice())
            .map(|changed| changed as u64)
            .map_err(sqlite_failed)
    }

    fn begin(&mut self) -> Result<(), Refusal> {
        self.batch("BEGIN IMMEDIATE")
    }

    fn commit(&mut self) -> Result<(), Refusal> {
        self.batch("COMMIT")
    }

    fn rollback(&mut self) -> Result<(), Refusal> {
        if self.conn.is_autocommit() {
            return Ok(());
        }
        self.batch("ROLLBACK")
    }

    fn has_table(&mut self, table: &str) -> Result<bool, Refusal> {
        let rows = self.query(
            "SELECT count(*) AS n FROM sqlite_master WHERE type = 'table' AND name = ?1",
            &[Value::String(table.to_owned())],
        )?;
        Ok(rows
            .first()
            .and_then(|row| row.get("n"))
            .and_then(Value::as_i64)
            .unwrap_or(0)
            > 0)
    }
}

/// Runs a binary with a document on its stdin and returns its exit status,
/// stdout and stderr.
pub(crate) fn run_binary(
    binary: &Path,
    args: &[&str],
    stdin: &[u8],
) -> Result<(bool, Vec<u8>, String), Refusal> {
    let mut child = Command::new(binary)
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| {
            internal(format!(
                "cannot run {} (set it with Binaries, or put it on PATH): {error}",
                binary.display()
            ))
        })?;
    if let Some(mut input) = child.stdin.take() {
        input
            .write_all(stdin)
            .map_err(|error| internal(format!("cannot write to {}: {error}", binary.display())))?;
    }
    let output = child
        .wait_with_output()
        .map_err(|error| internal(format!("{} did not finish: {error}", binary.display())))?;
    Ok((
        output.status.success(),
        output.stdout,
        String::from_utf8_lossy(&output.stderr).trim().to_owned(),
    ))
}

#[cfg(feature = "postgres")]
pub(crate) use pg::PgConn;

#[cfg(feature = "postgres")]
mod pg {
    use std::error::Error as StdError;

    use bytes::BytesMut;
    use postgres::types::{to_sql_checked, Format, IsNull, ToSql, Type};
    use postgres::{Client, NoTls};
    use serde_json::{Map, Value};
    use superschematic_rules::{Code, Refusal};

    use super::{apply_refusal, run_binary};
    use crate::db::Db;
    use crate::values::{internal, Dialect};
    use crate::Binaries;

    /// The advisory lock a request's transaction and a publish take: one
    /// per schema of a database, whose requests and publishes it
    /// serializes.
    pub(crate) const LOCK_KEY: &str = "hashtext('superschematic_runtime:' || current_schema())";

    /// A Postgres database's serving connection, and its URL, which the Go
    /// runner connects to.
    pub(crate) struct PgConn {
        client: Client,
        url: String,
    }

    fn failed(error: postgres::Error) -> Refusal {
        if let Some(db) = error.as_db_error() {
            let code = db.code().code();
            if code == "23505" || code == "23503" || code == "23514" || code == "23502" {
                return Refusal::new(
                    Code::Conflict,
                    format!("the runtime database refused the write: {}", db.message()),
                );
            }
            if code == "40001" || code == "40P01" || code == "55P03" {
                return Refusal::new(
                    Code::Busy,
                    format!("the runtime database is busy: {}", db.message()),
                );
            }
            return internal(format!("the database failed: {}", db.message()));
        }
        internal(format!("the database failed: {error}"))
    }

    /// A parameter sent as text, which Postgres reads into whatever type it
    /// meets, or NULL.
    #[derive(Debug)]
    struct Text(Option<String>);

    impl ToSql for Text {
        fn to_sql(
            &self,
            _ty: &Type,
            out: &mut BytesMut,
        ) -> Result<IsNull, Box<dyn StdError + Sync + Send>> {
            match &self.0 {
                Some(text) => {
                    out.extend_from_slice(text.as_bytes());
                    Ok(IsNull::No)
                }
                None => Ok(IsNull::Yes),
            }
        }

        fn accepts(_ty: &Type) -> bool {
            true
        }

        fn encode_format(&self, _ty: &Type) -> Format {
            Format::Text
        }

        to_sql_checked!();
    }

    fn text(value: &Value) -> Text {
        Text(match value {
            Value::Null => None,
            Value::String(text) => Some(text.clone()),
            other => Some(other.to_string()),
        })
    }

    impl PgConn {
        pub(crate) fn open(url: &str) -> Result<Self, Refusal> {
            let client = Client::connect(url, NoTls).map_err(failed)?;
            Ok(PgConn {
                client,
                url: url.to_owned(),
            })
        }

        pub(crate) fn client(&mut self) -> &mut Client {
            &mut self.client
        }

        pub(crate) fn apply_plan(
            &mut self,
            document: &[u8],
            binaries: &Binaries,
        ) -> Result<(), Refusal> {
            let (ok, stdout, stderr) = run_binary(
                &binaries.migrate,
                &["apply", "--plan", "-", "--database-url", &self.url],
                document,
            )?;
            if ok {
                return Ok(());
            }
            let said = if stderr.is_empty() {
                String::from_utf8_lossy(&stdout).trim().to_owned()
            } else {
                stderr
            };
            Err(apply_refusal(said))
        }
    }

    impl Db for PgConn {
        fn dialect(&self) -> Dialect {
            Dialect::Postgres
        }

        fn batch(&mut self, sql: &str) -> Result<(), Refusal> {
            self.client.batch_execute(sql).map_err(failed)
        }

        fn query(
            &mut self,
            sql: &str,
            params: &[Value],
        ) -> Result<Vec<Map<String, Value>>, Refusal> {
            let texts: Vec<Text> = params.iter().map(text).collect();
            let refs: Vec<&(dyn ToSql + Sync)> = texts
                .iter()
                .map(|value| value as &(dyn ToSql + Sync))
                .collect();
            let wrapped = format!("SELECT to_jsonb(q)::text FROM ({sql}) AS q");
            let rows = self.client.query(&wrapped, &refs).map_err(failed)?;
            let mut out = Vec::with_capacity(rows.len());
            for row in rows {
                let text: String = row.try_get(0).map_err(failed)?;
                match serde_json::from_str::<Value>(&text) {
                    Ok(Value::Object(columns)) => out.push(columns),
                    _ => return Err(internal(format!("a row read as no object: {text}"))),
                }
            }
            Ok(out)
        }

        fn execute(&mut self, sql: &str, params: &[Value]) -> Result<u64, Refusal> {
            let texts: Vec<Text> = params.iter().map(text).collect();
            let refs: Vec<&(dyn ToSql + Sync)> = texts
                .iter()
                .map(|value| value as &(dyn ToSql + Sync))
                .collect();
            self.client.execute(sql, &refs).map_err(failed)
        }

        fn begin(&mut self) -> Result<(), Refusal> {
            self.batch(&format!("BEGIN; SELECT pg_advisory_xact_lock({LOCK_KEY})"))
        }

        fn commit(&mut self) -> Result<(), Refusal> {
            self.batch("COMMIT")
        }

        fn rollback(&mut self) -> Result<(), Refusal> {
            self.batch("ROLLBACK")
        }

        fn has_table(&mut self, table: &str) -> Result<bool, Refusal> {
            let rows = self.query(
                "SELECT to_regclass($1::text) IS NOT NULL AS found",
                &[Value::String(table.to_owned())],
            )?;
            Ok(rows
                .first()
                .and_then(|row| row.get("found"))
                .and_then(Value::as_bool)
                .unwrap_or(false))
        }
    }
}
