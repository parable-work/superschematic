//! The connection a runtime store reads and writes through, one per
//! dialect: what SQL it takes and how it binds and reads values.

use serde_json::{Map, Value};
use superschematic_rules::Refusal;

use crate::values::Dialect;

/// A connection to a runtime database.
pub(crate) trait Db {
    fn dialect(&self) -> Dialect;

    /// Runs statements with no parameters and no rows.
    fn batch(&mut self, sql: &str) -> Result<(), Refusal>;

    /// Runs a query: each row a JSON object by column, on Postgres each
    /// value as `to_jsonb` gives it, and on SQLite each value its storage
    /// class's JSON (text a string, an integer or a real a number, a blob
    /// its hex).
    fn query(&mut self, sql: &str, params: &[Value]) -> Result<Vec<Map<String, Value>>, Refusal>;

    /// Runs a write: the rows it changed.
    fn execute(&mut self, sql: &str, params: &[Value]) -> Result<u64, Refusal>;

    /// Begins a request's transaction, holding the database's one writer
    /// from its start: `BEGIN IMMEDIATE` on SQLite, an advisory lock on
    /// Postgres.
    fn begin(&mut self) -> Result<(), Refusal>;

    fn commit(&mut self) -> Result<(), Refusal>;

    fn rollback(&mut self) -> Result<(), Refusal>;

    /// Whether a table exists.
    fn has_table(&mut self, table: &str) -> Result<bool, Refusal>;
}

/// An identifier quoted as Postgres and SQLite both read one.
pub(crate) fn quote(identifier: &str) -> String {
    format!("\"{}\"", identifier.replace('"', "\"\""))
}

/// The nth placeholder of a dialect: `?n` on SQLite, `$n` on Postgres.
pub(crate) fn placeholder(dialect: Dialect, n: usize) -> String {
    match dialect {
        Dialect::Sqlite => format!("?{n}"),
        Dialect::Postgres => format!("${n}"),
    }
}

/// A time in epoch milliseconds as a statement writes it into a queue's
/// `due_at`: SQLite's ISO text, as the queue's default writes it, or a
/// Postgres `TIMESTAMPTZ`.
pub(crate) fn timestamp(dialect: Dialect, n: usize) -> String {
    match dialect {
        Dialect::Sqlite => format!("strftime('%Y-%m-%dT%H:%M:%fZ', ?{n} / 1000.0, 'unixepoch')"),
        Dialect::Postgres => format!("to_timestamp(${n}::bigint / 1000.0)"),
    }
}

/// A JSON parameter as a statement writes it into a queue's JSON column:
/// SQLite's text, Postgres's `JSONB`.
pub(crate) fn json_param(dialect: Dialect, n: usize) -> String {
    match dialect {
        Dialect::Sqlite => format!("?{n}"),
        Dialect::Postgres => format!("${n}::jsonb"),
    }
}

/// The rows of a table where its columns hold values, as a JSON object by
/// column each.
pub(crate) fn select_rows(
    db: &mut dyn Db,
    table: &str,
    conds: &[(&str, Value)],
) -> Result<Vec<Map<String, Value>>, Refusal> {
    select_live(db, table, conds, None)
}

/// The rows of a table where its columns hold values and, when a column is
/// named, that column holds NULL: a soft-deletable table's live rows.
pub(crate) fn select_live(
    db: &mut dyn Db,
    table: &str,
    conds: &[(&str, Value)],
    live: Option<&str>,
) -> Result<Vec<Map<String, Value>>, Refusal> {
    let dialect = db.dialect();
    let mut params = Vec::with_capacity(conds.len());
    let mut wheres = Vec::with_capacity(conds.len() + 1);
    for (i, (column, value)) in conds.iter().enumerate() {
        wheres.push(format!(
            "t.{} = {}",
            quote(column),
            placeholder(dialect, i + 1)
        ));
        params.push(value.clone());
    }
    if let Some(live) = live {
        wheres.push(format!("t.{} IS NULL", quote(live)));
    }
    let condition = if wheres.is_empty() {
        String::new()
    } else {
        format!(" WHERE {}", wheres.join(" AND "))
    };
    db.query(
        &format!("SELECT * FROM {} AS t{condition}", quote(table)),
        &params,
    )
}

/// A NULL of a table's row type, which `jsonb_populate_record` reads a
/// row's values into each column's type through, taken from the relation
/// itself: the type of the name alone may be a built-in one that the
/// catalog finds first (`line`, `point`, `box`), which has no columns.
fn row_type(table: &str) -> String {
    format!("(SELECT t FROM {table} AS t WHERE false)")
}

/// Inserts a row of columns: on Postgres through `jsonb_populate_record`,
/// which reads each value into its column's type.
pub(crate) fn insert_row(
    db: &mut dyn Db,
    table: &str,
    columns: &Map<String, Value>,
) -> Result<u64, Refusal> {
    let names: Vec<String> = columns.keys().map(|name| quote(name)).collect();
    match db.dialect() {
        Dialect::Sqlite => {
            let placeholders: Vec<String> =
                (1..=columns.len()).map(|n| format!("?{n}")).collect();
            let params: Vec<Value> = columns.values().cloned().collect();
            db.execute(
                &format!(
                    "INSERT INTO {} ({}) VALUES ({})",
                    quote(table),
                    names.join(", "),
                    placeholders.join(", ")
                ),
                &params,
            )
        }
        Dialect::Postgres => db.execute(
            &format!(
                "INSERT INTO {table} ({names}) SELECT {names} FROM jsonb_populate_record({row}, $1::jsonb)",
                table = quote(table),
                row = row_type(&quote(table)),
                names = names.join(", ")
            ),
            &[Value::Object(columns.clone())],
        ),
    }
}

/// Sets columns of the rows where columns hold values, and moves a version
/// column on by one when one is named: the rows it changed.
pub(crate) fn update_rows(
    db: &mut dyn Db,
    table: &str,
    set: &Map<String, Value>,
    bump: Option<&str>,
    conds: &[(&str, Value)],
) -> Result<u64, Refusal> {
    update_live(db, table, set, bump, conds, None)
}

/// [`update_rows`] of the rows whose `live` column, when one is named,
/// holds NULL: a soft-deletable table's live rows.
pub(crate) fn update_live(
    db: &mut dyn Db,
    table: &str,
    set: &Map<String, Value>,
    bump: Option<&str>,
    conds: &[(&str, Value)],
    live: Option<&str>,
) -> Result<u64, Refusal> {
    let dialect = db.dialect();
    let target = quote(table);
    match dialect {
        Dialect::Sqlite => {
            let mut params: Vec<Value> = Vec::new();
            let mut sets: Vec<String> = Vec::new();
            for (column, value) in set {
                params.push(value.clone());
                sets.push(format!("{} = ?{}", quote(column), params.len()));
            }
            if let Some(version) = bump {
                sets.push(format!("{v} = {v} + 1", v = quote(version)));
            }
            let mut wheres = Vec::new();
            for (column, value) in conds {
                params.push(value.clone());
                wheres.push(format!("{} = ?{}", quote(column), params.len()));
            }
            if let Some(live) = live {
                wheres.push(format!("{} IS NULL", quote(live)));
            }
            if sets.is_empty() {
                return Ok(0);
            }
            db.execute(
                &format!(
                    "UPDATE {target} SET {} WHERE {}",
                    sets.join(", "),
                    wheres.join(" AND ")
                ),
                &params,
            )
        }
        Dialect::Postgres => {
            let mut params: Vec<Value> = vec![Value::Object(set.clone())];
            let mut sets: Vec<String> = set
                .keys()
                .map(|column| format!("{c} = r.{c}", c = quote(column)))
                .collect();
            if let Some(version) = bump {
                sets.push(format!("{v} = {target}.{v} + 1", v = quote(version)));
            }
            let mut wheres = Vec::new();
            for (column, value) in conds {
                params.push(value.clone());
                wheres.push(format!("{target}.{} = ${}", quote(column), params.len()));
            }
            if let Some(live) = live {
                wheres.push(format!("{target}.{} IS NULL", quote(live)));
            }
            if sets.is_empty() {
                return Ok(0);
            }
            db.execute(
                &format!(
                    "UPDATE {target} SET {} FROM jsonb_populate_record({}, $1::jsonb) AS r WHERE {}",
                    sets.join(", "),
                    row_type(&target),
                    wheres.join(" AND ")
                ),
                &params,
            )
        }
    }
}

/// Deletes the rows where columns hold values: the rows it deleted.
pub(crate) fn delete_rows(
    db: &mut dyn Db,
    table: &str,
    conds: &[(&str, Value)],
) -> Result<u64, Refusal> {
    let dialect = db.dialect();
    let mut wheres = Vec::new();
    let mut params = Vec::new();
    for (i, (column, value)) in conds.iter().enumerate() {
        wheres.push(format!(
            "{} = {}",
            quote(column),
            placeholder(dialect, i + 1)
        ));
        params.push(value.clone());
    }
    db.execute(
        &format!(
            "DELETE FROM {} WHERE {}",
            quote(table),
            wheres.join(" AND ")
        ),
        &params,
    )
}
