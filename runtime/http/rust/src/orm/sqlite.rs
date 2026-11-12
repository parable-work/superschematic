//! The repositories on a SQLite file (D116): synchronous, over a
//! `rusqlite::Connection` the caller holds, in the caller's transaction.
//!
//! A write that takes more than one statement, an upsert and a write of a
//! table that composes behaviors, reads and then writes, so its caller
//! opens the transaction with `TransactionBehavior::Immediate`: SQLite has
//! one writer, and a transaction that reads first and writes after meets
//! `SQLITE_BUSY` where one that took the write lock at its start waits its
//! turn. A write that waits past the connection's busy timeout is
//! [`Error::Busy`], which its caller may try again (D58). A server on
//! tokio calls a repository on its blocking pool.
//!
//! A value crosses as the SQLite DDL stores it: a UUID as its lowercase
//! hyphenated text, a timestamp in RFC 3339 in UTC to the millisecond, the
//! form a column's default writes, so the two order as text; a boolean as
//! 1 or 0; a list and a JSON value as JSON text; a decimal through the
//! column's NUMERIC affinity, and back as its text.

use std::marker::PhantomData;

use rusqlite::types::{Value as SqlValue, ValueRef};
use rusqlite::{params_from_iter, Connection, ErrorCode};
use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::Value;

// The SQLite client the repositories run on, for a types crate's `orm`
// module and its callers to name without a version of their own.
pub use rusqlite;

use super::plan::{Plan, Statement};
use super::run::{finish, Client, Runner};
use super::{
    row, values, wire, Bound, Column, Constraint, ConstraintKind, CoreCreate, CoreDelete,
    CoreUpdate, Definition, Dialect, Error, FieldKeys, FindOptions, Found, Marks, PageOptions,
    RowPage, Session, Table, UpsertOptions, Upserted, Values, WriteOptions,
};
use crate::error::chain;
use crate::queue::sqlite::{column_value, member_value, utc_timestamp};
use crate::queue::QueueField;
use crate::uuid62::{parse_uuid, uuid_to_base62};

/// What runs a table's behaviors on a SQLite connection (D103): the
/// behaviors' core, through its adapter's `sqlite` module, which a
/// generated types crate binds. Each runs in the caller's transaction,
/// writes the row with the columns the command carries, and answers the
/// core's refusal as [`Error::Refused`].
pub trait Rules: Send + Sync {
    /// Creates a row, and answers its key in its wire form.
    fn create(&self, connection: &Connection, create: CoreCreate<'_>) -> Result<String, Error>;
    /// Updates the live row a command names.
    fn update(&self, connection: &Connection, update: CoreUpdate<'_>) -> Result<(), Error>;
    /// Deletes the live row a command names, or stamps it deleted.
    fn delete(&self, connection: &Connection, delete: CoreDelete<'_>) -> Result<(), Error>;
}

/// The repositories of a database on a SQLite connection, acting as a
/// user or as no one.
pub struct Repositories<'a> {
    connection: &'a Connection,
    definition: &'a Definition,
    session: Session<'a>,
    rules: Option<&'a dyn Rules>,
}

impl<'a> Repositories<'a> {
    /// The repositories of `definition`'s tables over `connection`, the
    /// caller's: a `rusqlite::Transaction` it opened, for a write. They
    /// act as no one until [`Repositories::acting`] names a user.
    pub fn new(connection: &'a Connection, definition: &'a Definition) -> Result<Self, Error> {
        if definition.dialect != Dialect::Sqlite {
            return Err(Error::Definition(format!(
                "the ORM's definition is {}'s, and the connection is SQLite's: build the types crate with its sqlite feature, from a DB service that lists the sqlite dialect",
                definition.dialect
            )));
        }
        Ok(Self {
            connection,
            definition,
            session: Session::default(),
            rules: None,
        })
    }

    /// The repositories acting as `user`, whose id a create, an update and
    /// a delete stamp (D87); `None` acts as no one.
    #[must_use]
    pub fn acting(mut self, user: Option<String>) -> Self {
        self.session.user = user;
        self
    }

    /// The repositories acting as the database's system actor (D87).
    #[must_use]
    pub fn as_system(mut self) -> Self {
        self.session.user = Some(self.definition.system_actor_id().to_owned());
        self
    }

    /// The repositories with the session's settings: who a behavior's
    /// command runs as, the keys of the encrypted fields and a clock.
    #[must_use]
    pub fn with_session(mut self, session: Session<'a>) -> Self {
        self.session = session;
        self
    }

    /// The repositories telling `trace` each statement's text as it runs.
    #[must_use]
    pub fn with_trace(mut self, trace: &'a (dyn Fn(&str) + Sync)) -> Self {
        self.session.trace = Some(trace);
        self
    }

    /// The repositories with the keys of the database's `Encrypted<T>`
    /// fields (D85).
    #[must_use]
    pub fn with_keys(mut self, keys: &'a dyn FieldKeys) -> Self {
        self.session.keys = Some(keys);
        self
    }

    /// The repositories with what runs the behaviors of the tables that
    /// compose them (D103).
    #[must_use]
    pub fn with_rules(mut self, rules: &'a dyn Rules) -> Self {
        self.rules = Some(rules);
        self
    }

    /// The connection the repositories run on.
    pub fn connection(&self) -> &'a Connection {
        self.connection
    }

    /// The session the repositories act in.
    pub fn session(&self) -> &Session<'a> {
        &self.session
    }

    /// The repository of the table type `name`.
    pub fn table(&self, name: &str) -> Result<Repository<'_>, Error> {
        let table = self.definition.table(name)?;
        Ok(Repository {
            client: SqliteClient {
                connection: self.connection,
                rules: self.rules,
            },
            plan: Plan::new(self.definition, table, &self.session)?,
            table,
        })
    }
}

impl<'a> Repositories<'a> {
    /// The repository of the table type `name`, its rows, inputs, filters
    /// and keys as their Rust types: what a types crate's `orm` module
    /// names each table's repository as.
    pub fn typed<Row, Create, Update, Filter, Key>(
        &self,
        name: &str,
    ) -> Result<Typed<'_, Row, Create, Update, Filter, Key>, Error> {
        Ok(Typed {
            repository: self.table(name)?,
            types: PhantomData,
        })
    }
}

/// The Rust types a typed repository reads and writes, which it holds
/// none of.
type Types<Row, Create, Update, Filter, Key> =
    PhantomData<fn() -> (Row, Create, Update, Filter, Key)>;

/// The repository of one table on a SQLite connection, over its Rust
/// types: `Row` a row, `Create` what a create sets, `Update` what an
/// update sets, `Filter` the rows a read or a write finds, and `Key` the
/// primary key.
pub struct Typed<'a, Row, Create, Update, Filter, Key> {
    repository: Repository<'a>,
    types: Types<Row, Create, Update, Filter, Key>,
}

impl<'a, Row, Create, Update, Filter, Key> Typed<'a, Row, Create, Update, Filter, Key>
where
    Row: DeserializeOwned,
    Create: Serialize,
    Update: Serialize,
    Filter: Serialize,
    Key: Serialize,
{
    /// The repository over JSON objects, which this one reads and writes
    /// through.
    pub fn untyped(&self) -> &Repository<'a> {
        &self.repository
    }

    fn filter(filter: Option<&Filter>) -> Result<Option<Values>, Error> {
        filter.map(values).transpose()
    }

    /// The live row whose key is `key`; [`Error::NotFound`] when there is
    /// none.
    pub fn get_one(&self, key: &Key) -> Result<Row, Error> {
        row(self.repository.get_one(&wire(key)?)?)
    }

    /// The first row `filter` finds, in the options' order, or none.
    pub fn find_one(
        &self,
        filter: Option<&Filter>,
        options: &FindOptions,
    ) -> Result<Option<Row>, Error> {
        let filter = Self::filter(filter)?;
        self.repository
            .find_one(filter.as_ref(), options)?
            .map(row)
            .transpose()
    }

    /// The rows `filter` finds, in the options' order, a limit of them at
    /// a time, and how many it finds in all, unless the options skip the
    /// count.
    pub fn find_many(
        &self,
        filter: Option<&Filter>,
        options: &FindOptions,
    ) -> Result<Found<Row>, Error> {
        let filter = Self::filter(filter)?;
        let found = self.repository.find_many(filter.as_ref(), options)?;
        Ok(Found {
            rows: found.rows.into_iter().map(row).collect::<Result<_, _>>()?,
            total: found.total,
        })
    }

    /// A page of the rows `filter` finds, in the sort's order, after the
    /// key of the page before (D60, amended).
    pub fn find_page(
        &self,
        filter: Option<&Filter>,
        options: &PageOptions,
    ) -> Result<RowPage<Row>, Error> {
        let filter = Self::filter(filter)?;
        let page = self.repository.find_page(filter.as_ref(), options)?;
        Ok(RowPage {
            rows: page.rows.into_iter().map(row).collect::<Result<_, _>>()?,
            next: page.next,
        })
    }

    /// Inserts a row; a member left unset takes its column's default, or
    /// the schema's.
    pub fn create_one(&self, input: &Create) -> Result<Row, Error> {
        row(self.repository.create_one(&values(input)?)?)
    }

    /// Inserts the rows, in order.
    pub fn create_many(&self, inputs: &[Create]) -> Result<Vec<Row>, Error> {
        let inputs = inputs.iter().map(values).collect::<Result<Vec<_>, _>>()?;
        self.repository
            .create_many(&inputs)?
            .into_iter()
            .map(row)
            .collect()
    }

    /// Sets the members `update` sets on the live row whose key is `key`.
    pub fn update_one(
        &self,
        key: &Key,
        update: &Update,
        options: &WriteOptions,
    ) -> Result<Row, Error> {
        row(self
            .repository
            .update_one(&wire(key)?, &values(update)?, options)?)
    }

    /// Sets the members `update` sets on the live rows `filter` finds, and
    /// answers how many.
    pub fn update_many(&self, filter: Option<&Filter>, update: &Update) -> Result<u64, Error> {
        let filter = Self::filter(filter)?;
        self.repository
            .update_many(filter.as_ref(), &values(update)?)
    }

    /// Inserts `input`, or, when a row already holds its value of the
    /// unique key `on`, applies `update` to that row instead.
    pub fn upsert_one(
        &self,
        input: &Create,
        on: &[&str],
        update: &Update,
        options: UpsertOptions,
    ) -> Result<Upserted<Row>, Error> {
        let upserted =
            self.repository
                .upsert_one(&values(input)?, on, &values(update)?, options)?;
        Ok(Upserted {
            row: row(upserted.row)?,
            status: upserted.status,
        })
    }

    /// Deletes the live row whose key is `key`, stamping it on a
    /// soft-deletable table.
    pub fn delete_one(&self, key: &Key, options: &WriteOptions) -> Result<(), Error> {
        self.repository.delete_one(&wire(key)?, options)
    }

    /// Removes the row whose key is `key`, deleted or not.
    pub fn hard_delete_one(&self, key: &Key) -> Result<(), Error> {
        self.repository.hard_delete_one(&wire(key)?)
    }
}

/// The repository of one table on a SQLite connection.
pub struct Repository<'a> {
    client: SqliteClient<'a>,
    plan: Plan<'a>,
    table: &'a Table,
}

impl Repository<'_> {
    fn runner(&self) -> Runner<'_, SqliteClient<'_>> {
        Runner {
            plan: self.plan.clone(),
            table: self.table,
            client: &self.client,
        }
    }

    /// The live row whose key is `key`; [`Error::NotFound`] when there is
    /// none.
    pub fn get_one(&self, key: &Value) -> Result<Values, Error> {
        finish(self.runner().get_one(key))
    }

    /// The first row `filter` finds, in the options' order, or none.
    pub fn find_one(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Option<Values>, Error> {
        finish(self.runner().find_one(filter, options))
    }

    /// The rows `filter` finds, in the options' order, a limit of them at
    /// a time, and how many it finds in all, unless the options skip the
    /// count.
    pub fn find_many(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Found<Values>, Error> {
        finish(self.runner().find_many(filter, options))
    }

    /// A page of the rows `filter` finds, in the sort's order, after the
    /// key of the page before (D60, amended).
    pub fn find_page(
        &self,
        filter: Option<&Values>,
        options: &PageOptions,
    ) -> Result<RowPage<Values>, Error> {
        finish(self.runner().find_page(filter, options))
    }

    /// Inserts a row; a member left unset takes its column's default, or
    /// the schema's.
    pub fn create_one(&self, input: &Values) -> Result<Values, Error> {
        finish(self.runner().create_one(input))
    }

    /// Inserts the rows, in order, a statement a row: a VALUES list on
    /// SQLite takes no DEFAULT for a column one row sets and another
    /// leaves.
    pub fn create_many(&self, inputs: &[Values]) -> Result<Vec<Values>, Error> {
        finish(self.runner().create_many(inputs))
    }

    /// Sets the members `update` sets, null setting a column NULL, on the
    /// live row whose key is `key`; [`Error::NotFound`] when there is none,
    /// and [`Error::VersionConflict`] when the options fence the write on
    /// a version the row has moved from.
    pub fn update_one(
        &self,
        key: &Value,
        update: &Values,
        options: &WriteOptions,
    ) -> Result<Values, Error> {
        finish(self.runner().update_one(key, update, options))
    }

    /// Sets the members `update` sets on the live rows `filter` finds, and
    /// answers how many.
    pub fn update_many(&self, filter: Option<&Values>, update: &Values) -> Result<u64, Error> {
        finish(self.runner().update_many(filter, update))
    }

    /// Inserts `input`, or, when a row already holds its value of the
    /// unique key `on`, applies `update` to that row instead (D79, D89).
    /// On a soft-deletable table a deleted row that holds the key is left
    /// as it is, and the upsert is [`Error::NotFound`].
    pub fn upsert_one(
        &self,
        input: &Values,
        on: &[&str],
        update: &Values,
        options: UpsertOptions,
    ) -> Result<Upserted<Values>, Error> {
        finish(self.runner().upsert_one(input, on, update, options))
    }

    /// Deletes the live row whose key is `key`, stamping it on a
    /// soft-deletable table; [`Error::NotFound`] when there is none.
    pub fn delete_one(&self, key: &Value, options: &WriteOptions) -> Result<(), Error> {
        finish(self.runner().delete_one(key, options))
    }

    /// Removes the row whose key is `key`, deleted or not, whatever the
    /// table; [`Error::NotFound`] when there is none.
    pub fn hard_delete_one(&self, key: &Value) -> Result<(), Error> {
        finish(self.runner().hard_delete_one(key))
    }
}

/// A SQLite connection as a repository's runner drives it: each statement
/// runs as it is called, so each future is ready when first polled.
struct SqliteClient<'a> {
    connection: &'a Connection,
    rules: Option<&'a dyn Rules>,
}

impl SqliteClient<'_> {
    fn rules(&self, table: &str) -> Result<&dyn Rules, Error> {
        self.rules.ok_or_else(|| {
            Error::Definition(format!(
                "{table} composes behaviors (D103), and the repositories were opened with nothing to run them: pass the types crate's rules"
            ))
        })
    }

    fn params(table: &Table, statement: &Statement) -> Result<Vec<SqlValue>, Error> {
        statement
            .args
            .iter()
            .map(|bound| {
                param(bound).map_err(|err| Error::Invalid(format!("{}: {err}", table.name)))
            })
            .collect()
    }
}

impl Client for SqliteClient<'_> {
    async fn rows(
        &self,
        table: &Table,
        statement: &Statement,
        extra: usize,
    ) -> Result<Vec<(Values, Vec<Value>)>, Error> {
        let context = || format!("{}: {}", table.name, statement.sql);
        let mut prepared = self
            .connection
            .prepare_cached(&statement.sql)
            .map_err(|err| failure(&context(), &err))?;
        let args = Self::params(table, statement)?;
        let mut rows = prepared
            .query(params_from_iter(args))
            .map_err(|err| failure(&context(), &err))?;
        let mut out = Vec::new();
        loop {
            let row = match rows.next() {
                Ok(Some(row)) => row,
                Ok(None) => break,
                Err(err) => return Err(failure(&context(), &err)),
            };
            let mut members = Values::new();
            for (i, column) in table.columns.iter().enumerate() {
                let cell = row.get_ref(i).map_err(|err| failure(&context(), &err))?;
                let value = member(cell, column).map_err(|err| {
                    Error::Definition(format!("{}.{}: {err}", table.name, column.name))
                })?;
                members.insert(column.name.clone(), value);
            }
            let mut extras = Vec::with_capacity(extra);
            for i in table.columns.len()..table.columns.len() + extra {
                let cell = row.get_ref(i).map_err(|err| failure(&context(), &err))?;
                extras.push(member(cell, &Column::default()).map_err(Error::Definition)?);
            }
            out.push((members, extras));
        }
        Ok(out)
    }

    async fn execute(&self, table: &Table, statement: &Statement) -> Result<u64, Error> {
        let context = || format!("{}: {}", table.name, statement.sql);
        let args = Self::params(table, statement)?;
        let changed = self
            .connection
            .prepare_cached(&statement.sql)
            .and_then(|mut prepared| prepared.execute(params_from_iter(args)))
            .map_err(|err| failure(&context(), &err))?;
        Ok(changed as u64)
    }

    async fn scalar(&self, table: &Table, statement: &Statement) -> Result<Option<i64>, Error> {
        let context = || format!("{}: {}", table.name, statement.sql);
        let args = Self::params(table, statement)?;
        let mut prepared = self
            .connection
            .prepare_cached(&statement.sql)
            .map_err(|err| failure(&context(), &err))?;
        let mut rows = prepared
            .query(params_from_iter(args))
            .map_err(|err| failure(&context(), &err))?;
        match rows.next() {
            Ok(Some(row)) => row
                .get(0)
                .map(Some)
                .map_err(|err| failure(&context(), &err)),
            Ok(None) => Ok(None),
            Err(err) => Err(failure(&context(), &err)),
        }
    }

    async fn core_create(&self, create: CoreCreate<'_>) -> Result<String, Error> {
        self.rules(create.table)?.create(self.connection, create)
    }

    async fn core_update(&self, update: CoreUpdate<'_>) -> Result<(), Error> {
        self.rules(update.table)?.update(self.connection, update)
    }

    async fn core_delete(&self, delete: CoreDelete<'_>) -> Result<(), Error> {
        self.rules(delete.table)?.delete(self.connection, delete)
    }
}

/// The marks of a value as the queue client's conversion reads them: a
/// column's, without a name.
fn field(marks: Marks) -> QueueField {
    QueueField {
        name: String::new(),
        column: String::new(),
        json: marks.json,
        shape: None,
        list: marks.list,
        default: None,
        point: false,
        interval: marks.interval,
        uuid: marks.uuid,
        timestamp: marks.timestamp,
        boolean: marks.boolean,
    }
}

/// A value a statement binds, as SQLite holds it.
pub fn param(bound: &Bound) -> Result<SqlValue, String> {
    let marks = bound.marks;
    if marks.timestamp && !marks.json && !marks.list {
        return match &bound.value {
            Value::Null => Ok(SqlValue::Null),
            Value::String(text) => Ok(SqlValue::Text(millis(&utc_timestamp(text)?))),
            other => Err(format!("{other} is no timestamp")),
        };
    }
    if marks.numeric && !marks.json && !marks.list {
        // A decimal's text, which the column's NUMERIC affinity reads as
        // the number it writes.
        return match &bound.value {
            Value::Null => Ok(SqlValue::Null),
            Value::String(text) => Ok(SqlValue::Text(text.clone())),
            Value::Number(number) => Ok(SqlValue::Text(number.to_string())),
            other => Err(format!("{other} is no decimal")),
        };
    }
    column_value(&bound.value, &field(marks))
}

/// A timestamp in UTC with exactly three digits of fraction, the form a
/// SQLite column's default writes: a longer fraction is truncated, as the
/// column would hold the default's.
fn millis(utc: &str) -> String {
    let body = utc.strip_suffix('Z').unwrap_or(utc);
    let (whole, fraction) = body.split_once('.').unwrap_or((body, ""));
    let mut digits: String = fraction.chars().take(3).collect();
    while digits.len() < 3 {
        digits.push('0');
    }
    format!("{whole}.{digits}Z")
}

/// A column's value as its row's member: a UUID in base62, a decimal as
/// its text, and the rest as a queue's message reads a column.
fn member(cell: ValueRef<'_>, column: &Column) -> Result<Value, String> {
    if matches!(cell, ValueRef::Null) {
        return Ok(Value::Null);
    }
    let scalar = !column.json && !column.list;
    if column.numeric && scalar {
        return match cell {
            ValueRef::Integer(i) => Ok(Value::String(i.to_string())),
            ValueRef::Real(f) => Ok(Value::String(f.to_string())),
            ValueRef::Text(bytes) => std::str::from_utf8(bytes)
                .map(|text| Value::String(text.to_owned()))
                .map_err(|err| err.to_string()),
            ValueRef::Blob(_) | ValueRef::Null => Err("the column holds no decimal".to_owned()),
        };
    }
    let value = member_value(cell, &field(Marks::of(column)))?.unwrap_or(Value::Null);
    match value {
        Value::String(text) if column.uuid && scalar => parse_uuid(&text)
            .map(|uuid| Value::String(uuid_to_base62(uuid)))
            .ok_or_else(|| format!("the column holds {text:?}, no UUID")),
        Value::Array(elements) if column.uuid && column.list && !column.json => elements
            .into_iter()
            .map(|element| match element {
                Value::String(text) => parse_uuid(&text)
                    .map(|uuid| Value::String(uuid_to_base62(uuid)))
                    .ok_or_else(|| format!("the column holds {text:?}, no UUID")),
                other => Ok(other),
            })
            .collect::<Result<Vec<_>, _>>()
            .map(Value::Array),
        other => Ok(other),
    }
}

/// A rusqlite error as a repository's: the constraint that refused a row,
/// the busy writer, or the database's failure.
pub fn failure(context: &str, err: &rusqlite::Error) -> Error {
    if let rusqlite::Error::SqliteFailure(code, message) = err {
        let message = message.clone().unwrap_or_else(|| code.to_string());
        match code.code {
            ErrorCode::DatabaseBusy | ErrorCode::DatabaseLocked => {
                return Error::Busy(format!("{context}: {message}"));
            }
            ErrorCode::ConstraintViolation => {
                let kind = match code.extended_code {
                    rusqlite::ffi::SQLITE_CONSTRAINT_UNIQUE
                    | rusqlite::ffi::SQLITE_CONSTRAINT_PRIMARYKEY => Some(ConstraintKind::Unique),
                    rusqlite::ffi::SQLITE_CONSTRAINT_FOREIGNKEY => Some(ConstraintKind::ForeignKey),
                    rusqlite::ffi::SQLITE_CONSTRAINT_NOTNULL => Some(ConstraintKind::NotNull),
                    rusqlite::ffi::SQLITE_CONSTRAINT_CHECK => Some(ConstraintKind::Check),
                    _ => None,
                };
                if let Some(kind) = kind {
                    return Error::Constraint(Constraint {
                        kind,
                        columns: constraint_columns(&message),
                        message,
                    });
                }
            }
            _ => {}
        }
    }
    Error::Database(format!("{context}: {}", chain(err)))
}

/// The columns SQLite's message of a constraint names, `UNIQUE constraint
/// failed: entry.account_id, entry.external_id`, without their table;
/// none for a message that names none, a foreign key's or a check's.
fn constraint_columns(message: &str) -> Vec<String> {
    let Some((head, list)) = message.split_once(": ") else {
        return Vec::new();
    };
    if !head.starts_with("UNIQUE") && !head.starts_with("NOT NULL") {
        return Vec::new();
    }
    list.split(", ")
        .map(|name| name.rsplit('.').next().unwrap_or(name).to_owned())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_timestamp_is_bound_to_the_millisecond() {
        let bound = |text: &str| Bound {
            value: Value::String(text.to_owned()),
            marks: Marks::TIMESTAMP,
        };
        assert_eq!(
            param(&bound("2026-10-11T09:15:00+02:00")).unwrap(),
            SqlValue::Text("2026-10-11T07:15:00.000Z".into())
        );
        assert_eq!(
            param(&bound("2026-10-11T09:15:00.5Z")).unwrap(),
            SqlValue::Text("2026-10-11T09:15:00.500Z".into())
        );
        assert_eq!(
            param(&bound("2026-10-11T09:15:00.123456Z")).unwrap(),
            SqlValue::Text("2026-10-11T09:15:00.123Z".into())
        );
        assert!(param(&bound("yesterday")).is_err());
    }

    #[test]
    fn a_constraint_names_its_columns() {
        assert_eq!(
            constraint_columns("UNIQUE constraint failed: entry.account_id, entry.external_id"),
            vec!["account_id", "external_id"]
        );
        assert_eq!(
            constraint_columns("NOT NULL constraint failed: entry.memo"),
            vec!["memo"]
        );
        assert!(constraint_columns("FOREIGN KEY constraint failed").is_empty());
    }
}
