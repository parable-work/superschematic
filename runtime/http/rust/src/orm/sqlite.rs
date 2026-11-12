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
use super::{
    row, values, wire, Bound, Column, Constraint, ConstraintKind, CoreCreate, CoreDelete,
    CoreUpdate, Definition, Dialect, Error, FieldKeys, FindOptions, Found, Marks, PageOptions,
    RowPage, Session, Table, UpsertOptions, UpsertStatus, Upserted, Values, WriteOptions,
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
            plan: Plan::new(self.definition, table, &self.session)?,
            table,
            connection: self.connection,
            rules: self.rules,
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
    plan: Plan<'a>,
    table: &'a Table,
    connection: &'a Connection,
    rules: Option<&'a dyn Rules>,
}

impl Repository<'_> {
    /// The live row whose key is `key`; [`Error::NotFound`] when there is
    /// none.
    pub fn get_one(&self, key: &Value) -> Result<Values, Error> {
        let rows = self.query(&self.plan.get_one(key)?)?;
        rows.into_iter().next().ok_or_else(|| self.not_found(key))
    }

    /// The first row `filter` finds, in the options' order, or none.
    pub fn find_one(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Option<Values>, Error> {
        let rows = self.query(&self.plan.find_one(filter, options)?)?;
        Ok(rows.into_iter().next())
    }

    /// The rows `filter` finds, in the options' order, a limit of them at
    /// a time, and how many it finds in all, unless the options skip the
    /// count.
    pub fn find_many(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Found<Values>, Error> {
        let (count, read) = self.plan.find_many(filter, options)?;
        let total = match count {
            Some(count) => Some(self.count(&count)?),
            None => None,
        };
        Ok(Found {
            rows: self.query(&read)?,
            total,
        })
    }

    /// A page of the rows `filter` finds, in the sort's order, after the
    /// key of the page before (D60, amended).
    pub fn find_page(
        &self,
        filter: Option<&Values>,
        options: &PageOptions,
    ) -> Result<RowPage<Values>, Error> {
        let page = self.plan.find_page(filter, options)?;
        let mut rows = self.query(&page.statement)?;
        if rows.is_empty() {
            if let Some(exists) = &page.key_exists {
                if self.count(exists)? == 0 {
                    return Err(Error::PageKey(format!(
                        "no {} row has the key's {}",
                        self.table.name, self.table.key
                    )));
                }
            }
        }
        let more = rows.len() > page.limit;
        rows.truncate(page.limit);
        let next = match rows.last() {
            Some(last) if more => last.get(&self.table.key).cloned(),
            _ => None,
        };
        Ok(RowPage { rows, next })
    }

    /// Inserts a row; a member left unset takes its column's default, or
    /// the schema's.
    pub fn create_one(&self, input: &Values) -> Result<Values, Error> {
        if self.table.behaviors.is_some() {
            let key = self
                .rules()?
                .create(self.connection, self.plan.core_create(input)?)?;
            return self.get_one(&Value::String(key));
        }
        self.returned(&self.plan.create_one(input)?)
    }

    /// Inserts the rows, in order.
    pub fn create_many(&self, inputs: &[Values]) -> Result<Vec<Values>, Error> {
        if self.table.behaviors.is_some() {
            return inputs.iter().map(|input| self.create_one(input)).collect();
        }
        if inputs.is_empty() {
            return Ok(Vec::new());
        }
        let mut rows = Vec::with_capacity(inputs.len());
        for statement in self.plan.create_many(inputs)? {
            rows.extend(self.query(&statement)?);
        }
        Ok(rows)
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
        if self.table.behaviors.is_some() {
            self.rules()?
                .update(
                    self.connection,
                    self.plan.core_update(key, update, options)?,
                )
                .map_err(|err| self.core_miss(err, key))?;
            return self.get_one(key);
        }
        self.plan.refuse_preconditions(options)?;
        let rows = self.query(&self.plan.update_one(key, update, options)?)?;
        match rows.into_iter().next() {
            Some(row) => Ok(row),
            None => Err(self.miss(key, options)?),
        }
    }

    /// Sets the members `update` sets on the live rows `filter` finds, and
    /// answers how many.
    pub fn update_many(&self, filter: Option<&Values>, update: &Values) -> Result<u64, Error> {
        self.refuse_behaviors("update_many")?;
        self.execute(&self.plan.update_many(filter, update)?)
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
        let plan = self
            .plan
            .upsert_one(input, on, update, options.only_if_changed)?;
        // SQLite's upsert does not say whether it inserted: a row held the
        // key before it, or none did, in the one writer's transaction.
        let existed = match &plan.before {
            Some(before) => !self.raw(before)?.is_empty(),
            None => false,
        };
        let rows = self.query(&plan.statement)?;
        let upserted = match rows.into_iter().next() {
            Some(row) => Upserted {
                row,
                status: if existed {
                    UpsertStatus::Updated
                } else {
                    UpsertStatus::Inserted
                },
            },
            None if !options.only_if_changed && plan.sets => {
                return Err(self.deleted(on));
            }
            None => {
                let held = plan.held.as_ref().ok_or_else(|| self.deleted(on))?;
                let row = self
                    .raw(held)?
                    .into_iter()
                    .next()
                    .filter(|row| self.live(row))
                    .ok_or_else(|| self.deleted(on))?;
                Upserted {
                    row: self.plan.open(row)?,
                    status: UpsertStatus::Unchanged,
                }
            }
        };
        self.resealed(upserted, update, &plan.sealing)
    }

    /// Deletes the live row whose key is `key`, stamping it on a
    /// soft-deletable table; [`Error::NotFound`] when there is none.
    pub fn delete_one(&self, key: &Value, options: &WriteOptions) -> Result<(), Error> {
        if self.table.behaviors.is_some() {
            return self
                .rules()?
                .delete(self.connection, self.plan.core_delete(key, options)?)
                .map_err(|err| self.core_miss(err, key));
        }
        self.plan.refuse_preconditions(options)?;
        match self.execute(&self.plan.delete_one(key, options)?)? {
            0 => Err(self.miss(key, options)?),
            _ => Ok(()),
        }
    }

    /// Removes the row whose key is `key`, deleted or not, whatever the
    /// table; [`Error::NotFound`] when there is none.
    pub fn hard_delete_one(&self, key: &Value) -> Result<(), Error> {
        self.refuse_behaviors("hard_delete_one")?;
        let options = WriteOptions::default();
        match self.execute(&self.plan.hard_delete_one(key, &options)?)? {
            0 => Err(self.not_found(key)),
            _ => Ok(()),
        }
    }

    fn rules(&self) -> Result<&dyn Rules, Error> {
        self.rules.ok_or_else(|| {
            Error::Definition(format!(
                "{} composes behaviors (D103), and the repositories were opened with nothing to run them: pass the types crate's rules",
                self.table.name
            ))
        })
    }

    fn refuse_behaviors(&self, method: &str) -> Result<(), Error> {
        if self.table.behaviors.is_none() {
            return Ok(());
        }
        Err(Error::Invalid(format!(
            "{method} of {}: a table that composes behaviors writes its rows through the core, a row at a time (D103)",
            self.table.name
        )))
    }

    fn not_found(&self, key: &Value) -> Error {
        Error::NotFound(format!("{} {} not found", self.table.name, text_of(key)))
    }

    fn deleted(&self, on: &[&str]) -> Error {
        Error::NotFound(format!(
            "the {} row that holds the key {} is deleted",
            self.table.name,
            on.join(",")
        ))
    }

    /// The core's not_found of the row as the repository's own.
    fn core_miss(&self, err: Error, key: &Value) -> Error {
        match err {
            Error::Refused(refusal) if refusal.code == "not_found" => self.not_found(key),
            other => other,
        }
    }

    /// Why a write of one row changed none: the row is not there, or, for
    /// a fenced write, it has moved from the version.
    fn miss(&self, key: &Value, options: &WriteOptions) -> Result<Error, Error> {
        let Some(expected) = options.expected_version else {
            return Ok(self.not_found(key));
        };
        let statement = self.plan.version_of(key)?;
        let held: Option<i64> = self.scalar(&statement)?;
        Ok(match held {
            None => self.not_found(key),
            Some(version) => Error::VersionConflict(format!(
                "{} {} is at version {version}, not {expected}",
                self.table.name,
                text_of(key)
            )),
        })
    }

    /// Whether a row a read by key returned is live.
    fn live(&self, row: &Values) -> bool {
        match self.table.audit("deletedAt") {
            Some(deleted_at) => row.get(&deleted_at.name).is_none_or(Value::is_null),
            None => true,
        }
    }

    /// `upserted`, with each encrypted field of the update the row it
    /// updated holds another value of set by a second statement, sealed
    /// under the row's key (D89). A row the upsert inserted holds the
    /// input's already.
    fn resealed(
        &self,
        upserted: Upserted<Values>,
        update: &Values,
        sealing: &Values,
    ) -> Result<Upserted<Values>, Error> {
        if upserted.status == UpsertStatus::Inserted || sealing.is_empty() {
            return Ok(upserted);
        }
        let mut change = Values::new();
        for (member, wanted) in sealing {
            let held = upserted.row.get(member).filter(|held| !held.is_null());
            let same = match held {
                None => wanted.is_null(),
                Some(held) => held == wanted,
            };
            if !same {
                change.insert(member.clone(), wanted.clone());
            }
        }
        if change.is_empty() {
            return Ok(upserted);
        }
        if let Some(updated_by) = self.table.audit("updatedBy") {
            if let Some(by) = update.get(&updated_by.name) {
                change.insert(updated_by.name.clone(), by.clone());
            }
        }
        let key = upserted
            .row
            .get(&self.table.key)
            .cloned()
            .unwrap_or(Value::Null);
        Ok(Upserted {
            row: self.update_one(&key, &change, &WriteOptions::default())?,
            status: UpsertStatus::Updated,
        })
    }

    /// Runs a statement that returns rows of the table, and reads them
    /// with their encrypted members opened.
    fn query(&self, statement: &Statement) -> Result<Vec<Values>, Error> {
        self.raw(statement)?
            .into_iter()
            .map(|row| self.plan.open(row))
            .collect()
    }

    /// Runs a statement that returns one row, the one a write wrote.
    fn returned(&self, statement: &Statement) -> Result<Values, Error> {
        self.query(statement)?.into_iter().next().ok_or_else(|| {
            Error::Database(format!(
                "{}: the statement returned no row",
                self.table.name
            ))
        })
    }

    /// Runs a statement that returns rows of the table, each column in
    /// the table's order, and reads them as the database holds them.
    fn raw(&self, statement: &Statement) -> Result<Vec<Values>, Error> {
        let context = || format!("{}: {}", self.table.name, statement.sql);
        let mut prepared = self
            .connection
            .prepare_cached(&statement.sql)
            .map_err(|err| failure(&context(), &err))?;
        let args = self.params(statement)?;
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
            for (i, column) in self.table.columns.iter().enumerate() {
                let cell = row.get_ref(i).map_err(|err| failure(&context(), &err))?;
                let value = member(cell, column).map_err(|err| {
                    Error::Definition(format!("{}.{}: {err}", self.table.name, column.name))
                })?;
                members.insert(column.name.clone(), value);
            }
            out.push(members);
        }
        Ok(out)
    }

    /// Runs a statement that changes rows, and answers how many.
    fn execute(&self, statement: &Statement) -> Result<u64, Error> {
        let context = || format!("{}: {}", self.table.name, statement.sql);
        let args = self.params(statement)?;
        let changed = self
            .connection
            .prepare_cached(&statement.sql)
            .and_then(|mut prepared| prepared.execute(params_from_iter(args)))
            .map_err(|err| failure(&context(), &err))?;
        Ok(changed as u64)
    }

    /// Runs a statement that reads one integer of one row, or none.
    fn scalar(&self, statement: &Statement) -> Result<Option<i64>, Error> {
        let context = || format!("{}: {}", self.table.name, statement.sql);
        let args = self.params(statement)?;
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

    /// Runs a statement that counts, or reads whether a row exists.
    fn count(&self, statement: &Statement) -> Result<u64, Error> {
        let n = self.scalar(statement)?.unwrap_or(0);
        Ok(u64::try_from(n).unwrap_or(0))
    }

    fn params(&self, statement: &Statement) -> Result<Vec<SqlValue>, Error> {
        statement
            .args
            .iter()
            .map(|bound| {
                param(bound).map_err(|err| Error::Invalid(format!("{}: {err}", self.table.name)))
            })
            .collect()
    }
}

/// A key as an error names it.
fn text_of(key: &Value) -> String {
    match key {
        Value::String(text) => text.clone(),
        other => other.to_string(),
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
