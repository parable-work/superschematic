//! The repositories of a database's tables (D79, D116): what a generated
//! types crate's `orm` module runs its tables through.
//!
//! A types crate carries each table as data: its columns, what each holds,
//! and its statements, which `sqlutil` writes once for the Go ORM, the
//! TypeScript repositories and these, on Postgres and, for a database that
//! lists the sqlite dialect, on SQLite. This module parses that definition
//! ([`Definition::parse`]), fills the statements in the order the Go ORM
//! fills them, and runs them: on SQLite through a `rusqlite::Connection`
//! its caller holds ([`sqlite`]), synchronously, in the caller's
//! transaction; on Postgres through a tokio-postgres client
//! ([`postgres`]), asynchronously.
//!
//! A row is a JSON object in the wire form its Rust type serializes as: a
//! member per column, a to-one relation's key as `<field>Id`, a UUID in
//! base62, a timestamp in RFC 3339. A repository stamps the acting user
//! into `createdBy`, `updatedBy` and `deletedBy` (D87), leaves a
//! soft-deletable table's deleted rows out, fences a write on `_version`
//! when asked, pages a column that may be null with its nulls last and
//! negates a filter with a null kept (D112), seals and opens an
//! `Encrypted<T>` column with the keys it was given (D85), and writes a
//! table that composes behaviors through the behaviors' core, which a
//! [`Rules`] its caller passes runs (D103).

mod plan;
#[cfg(feature = "sqlite")]
pub mod sqlite;

use std::collections::BTreeMap;
use std::fmt;
use std::time::SystemTime;

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::civil::from_days;

pub use plan::fill;

/// A row, a create's input, an update's and a filter, as the repositories
/// read them: a JSON object by member.
pub type Values = Map<String, Value>;

/// The dialect a definition's statements are written in.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Dialect {
    /// Postgres, whose parameters are `$n`.
    Postgres,
    /// SQLite, whose parameters are `?n`.
    Sqlite,
}

impl fmt::Display for Dialect {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            Dialect::Postgres => "postgres",
            Dialect::Sqlite => "sqlite",
        })
    }
}

/// A database's tables and the clauses their statements are written with,
/// in one dialect: what a types crate's `orm` module holds as JSON, with
/// the members of the TypeScript `OrmDefinition`.
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Definition {
    /// The dialect of every statement and clause.
    pub dialect: Dialect,
    /// The parts of statements written around a column.
    pub clauses: Clauses,
    /// The rows `find_many` and `find_page` read unless told.
    pub default_limit: u32,
    /// Who a repository opened as the system acts as (D87): the DB
    /// service's `outputs.sql.systemActor`, [`DEFAULT_SYSTEM_ACTOR`] unless
    /// set.
    #[serde(default)]
    pub system_actor: Option<SystemActor>,
    /// An update of a table with `updatedBy` needs an acting user, as a
    /// create of a table with `createdBy` does (D87).
    #[serde(default)]
    pub require_updated_by: bool,
    /// The tables, in the schema's order.
    pub tables: Vec<Table>,
}

/// A database's system actor (D87): a user's id and the name an app shows
/// for it.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
pub struct SystemActor {
    /// The id, hyphenated.
    pub id: String,
    /// The name.
    pub name: String,
}

/// The system actor of a database whose DB service names none: the fixed
/// id the generated Go APIs export as `SystemUserID`.
pub const DEFAULT_SYSTEM_ACTOR: &str = "00000000-0000-4000-8000-000000000000";

impl Definition {
    /// Reads a definition from the JSON a types crate holds.
    pub fn parse(json: &str) -> Result<Self, Error> {
        let definition: Definition = serde_json::from_str(json)
            .map_err(|err| Error::Definition(format!("the ORM's definition: {err}")))?;
        for table in &definition.tables {
            table.check()?;
        }
        Ok(definition)
    }

    /// The table of the type `name`.
    pub fn table(&self, name: &str) -> Result<&Table, Error> {
        self.tables
            .iter()
            .find(|table| table.name == name)
            .ok_or_else(|| Error::Definition(format!("the ORM has no table {name}")))
    }

    /// The id the system acts as, hyphenated.
    pub fn system_actor_id(&self) -> &str {
        self.system_actor
            .as_ref()
            .map_or(DEFAULT_SYSTEM_ACTOR, |actor| actor.id.as_str())
    }
}

/// The parts of statements written around a column, `sqlutil.Clauses`:
/// format strings whose first `%s` is the column.
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
#[allow(missing_docs)]
pub struct Clauses {
    pub eq: String,
    pub ne: String,
    pub gt: String,
    pub gte: String,
    pub lt: String,
    pub lte: String,
    pub ilike: String,
    #[serde(rename = "in")]
    pub in_: String,
    pub none_of: String,
    pub is_null: String,
    pub is_not_null: String,
    pub search: String,
    pub exists: String,
    pub not_exists: String,
    pub set: String,
    pub set_null: String,
    pub page_key: String,
    pub placeholder: String,
    /// The value of a column a row of a batch insert leaves unset; empty
    /// on SQLite, whose VALUES take none, where a batch is inserted a row
    /// at a time.
    #[serde(default)]
    pub default: String,
    pub or: String,
    pub group: String,
    #[serde(rename = "where")]
    pub where_: String,
    pub and: String,
    pub order_by: String,
    pub order_term: String,
    pub limit: String,
    pub offset: String,
    pub changed: String,
    pub never: String,
    pub null: String,
}

/// A table's statements, `sqlutil.TableStatements`: format strings of `%s`
/// and `%d` alone. One a table has none of is empty.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase", default)]
#[allow(missing_docs)]
pub struct Statements {
    pub columns: String,
    pub qualified_columns: String,
    pub get_one: String,
    pub live: String,
    pub live_where: String,
    pub live_condition: String,
    pub find_one: String,
    pub find_many: String,
    pub count: String,
    pub page: String,
    pub page_after_key: String,
    pub page_after: String,
    pub page_after_nulls: String,
    pub page_order: String,
    pub page_order_nulls: String,
    pub page_tiebreak: String,
    pub page_key_exists: String,
    pub insert: String,
    pub insert_many: String,
    pub update: String,
    pub version_fence: String,
    pub returning: String,
    pub update_many: String,
    pub updated_at: String,
    pub updated_by: String,
    pub upsert: String,
    pub soft_delete: String,
    pub soft_delete_if_version: String,
    pub delete: String,
    pub delete_if_version: String,
    pub version: String,
}

/// A table, as a types crate's `orm` module describes it.
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Table {
    /// The table type.
    pub name: String,
    /// The table, quoted for SQL where it must be.
    pub table: String,
    /// The primary key's member.
    pub key: String,
    /// The columns, in the order every statement lists them.
    pub columns: Vec<Column>,
    /// The table's statements in the definition's dialect.
    pub statements: Statements,
    /// A table with `deletedAt`, which a delete stamps.
    #[serde(default)]
    pub soft_delete: bool,
    /// The search column, qualified, of a table with search fields (D62).
    #[serde(default)]
    pub search: Option<String>,
    /// The unique keys by name, their fields joined by `,`.
    #[serde(default)]
    pub unique_keys: BTreeMap<String, UniqueKey>,
    /// The to-one relations of other tables that point at this one.
    #[serde(default)]
    pub referenced_by: Vec<Reference>,
    /// The to-one relations a filter reaches through, in column order.
    #[serde(default)]
    pub relations: Vec<Relation>,
    /// Why the table has no repository in this dialect: a column SQLite's
    /// repositories do not hold, or a name SQLite reserves. A table with
    /// one carries no statements.
    #[serde(default)]
    pub unsupported: Option<String>,
    /// The table composes behaviors (D103): a create, an update and a
    /// delete of a row go through the behaviors' core, which the
    /// repository's [`Rules`] runs.
    #[serde(default)]
    pub behaviors: Option<Behaviors>,
}

/// What a repository reads of the behaviors a table composes (D103): the
/// columns their fields own, which only their operations change.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Behaviors {
    /// The members of the columns the table's behaviors own.
    #[serde(default)]
    pub owned: Vec<String>,
    /// The behaviors keep sub-tables or arm deadlines, which an upsert's
    /// insert would leave unwritten, so the table has no upserts.
    #[serde(default)]
    pub no_upsert: bool,
}

impl Table {
    fn check(&self) -> Result<(), Error> {
        if self.columns.iter().all(|column| column.name != self.key) {
            return Err(Error::Definition(format!(
                "table {} has no column for its key {}",
                self.name, self.key
            )));
        }
        Ok(())
    }

    /// The column of the member `name`.
    pub fn column(&self, name: &str) -> Option<&Column> {
        self.columns.iter().find(|column| column.name == name)
    }

    /// The key's column.
    pub fn key_column(&self) -> &Column {
        // `check` held the definition to having one.
        self.column(&self.key).unwrap_or(&self.columns[0])
    }

    /// The column that is the audit field `name`.
    pub fn audit(&self, name: &str) -> Option<&Column> {
        self.columns
            .iter()
            .find(|column| column.audit.as_deref() == Some(name))
    }

    /// The table's Encrypted<T> columns.
    pub fn encrypted(&self) -> impl Iterator<Item = &Column> {
        self.columns.iter().filter(|column| column.encrypted)
    }

    /// Whether the table's rows hold `_version`.
    pub fn versioned(&self) -> bool {
        self.columns.iter().any(|column| column.name == VERSION)
    }
}

/// The member a row holds its version under, on an `@optimistic` table.
pub const VERSION: &str = "_version";

/// A column of a table and what the repositories write and filter.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct Column {
    /// The row's member: the field, or `<field>Id` for a to-one relation's
    /// foreign key.
    pub name: String,
    /// The column, quoted for SQL where it must be.
    pub column: String,
    /// The schema field the column stores.
    pub field: String,
    /// The column qualified by its table, as a condition names it.
    pub qualified: String,
    /// The column holds its value as JSON.
    pub json: bool,
    /// The column holds a list.
    pub list: bool,
    /// The column, or each element of its list, holds a UUID.
    pub uuid: bool,
    /// The column, or each element, holds a timestamp.
    pub timestamp: bool,
    /// The column, or each element, holds a date.
    pub date: bool,
    /// The column, or each element, holds a NUMERIC, a decimal's text in a
    /// row.
    pub numeric: bool,
    /// The column holds a location.
    pub point: bool,
    /// The column holds a duration.
    pub interval: bool,
    /// The column, or each element, holds a boolean.
    pub boolean: bool,
    /// The primary key.
    pub key: bool,
    /// A column the database fills unless a write sets it.
    pub auto: bool,
    /// A column that takes no NULL.
    pub required: bool,
    /// Internal metadata, such as `_version`, which no write sets.
    pub internal: bool,
    /// An Encrypted<T> field's column, which holds its envelope (D85).
    pub encrypted: bool,
    /// The audit field the column is: `createdAt`, `updatedAt`,
    /// `createdBy`, `updatedBy`, `deletedAt` or `deletedBy`.
    pub audit: Option<String>,
    /// The target type of a to-one relation, whose key the column holds.
    pub relation: Option<String>,
    /// The schema's default, which a create that leaves the member unset
    /// writes.
    pub default: Option<Value>,
    /// The operations of the column's filter, in the order their
    /// conditions are written.
    pub filter: Vec<String>,
    /// A column `find_page` orders by.
    pub sortable: bool,
}

impl Column {
    /// Whether a null written to the column is the JSON null: a JSON
    /// column that takes no NULL.
    fn json_null(&self) -> bool {
        self.json && self.required
    }

    /// The column's name without the quotes SQL needs around it.
    pub fn unquoted(&self) -> &str {
        self.column.trim_matches('"')
    }
}

/// A unique key an upsert is keyed by.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct UniqueKey {
    /// The conflict target of an upsert on the key.
    pub conflict: String,
    /// The assignment of the update an upsert that sets nothing names.
    pub keep: String,
    /// The read of the rows that hold the keys of the tuples `%s`; empty
    /// for a key over an expression.
    pub rows: String,
    /// An expression key's read of the row that holds one input's value
    /// of it, its parameters the key's columns' values in order.
    pub held: String,
    /// A unique index over an expression.
    pub expression: bool,
}

/// A to-one relation of another table that points at a table: the filter
/// member that probes for it.
#[derive(Clone, Debug, Deserialize)]
pub struct Reference {
    /// The filter's member, `referencedBy<Source><Field>`.
    pub filter: String,
    /// The probe, the rows of the source that point at the row.
    pub probe: String,
    /// What leaves the source's deleted rows out of the probe.
    #[serde(default)]
    pub live: String,
}

/// A to-one relation of a table to another of its database (D90).
#[derive(Clone, Debug, Deserialize)]
pub struct Relation {
    /// The relation field, the member a filter reaches through.
    pub field: String,
    /// The target's table type.
    pub target: String,
    /// A filter through the relation: the foreign key in the keys of the
    /// target's rows of the WHERE `%s`.
    pub probe: String,
}

/// What a repository's read or write failed with.
#[derive(Debug)]
pub enum Error {
    /// A read or a write that names no live row.
    NotFound(String),
    /// A write that stamps the acting user, with none (D87).
    NoUser,
    /// `find_page`'s after key holds no key, or its row no longer exists
    /// in an order by another column (D60).
    PageKey(String),
    /// A write fenced on a version the row has moved from.
    VersionConflict(String),
    /// What the caller passed is no input of the table: a filter of a
    /// member it has none of, a create without a required member.
    Invalid(String),
    /// The database refused a row: a unique key another row holds, a
    /// foreign key that names no row, a column that takes no null, or a
    /// check.
    Constraint(Constraint),
    /// SQLite's writer was held past the busy timeout: the write may be
    /// tried again (D58).
    Busy(String),
    /// The behaviors' core refused the command (D103), as its [`Rules`]
    /// says it.
    Refused(Refusal),
    /// An `Encrypted<T>` column's value could not be sealed or opened.
    Encryption(String),
    /// The definition, or a row the database holds, is not what the
    /// repository reads.
    Definition(String),
    /// Any other failure of the database.
    Database(String),
}

/// A refusal of the behaviors' core (D16, D103): its code, its message and
/// the details a problem carries.
#[derive(Clone, Debug, PartialEq)]
pub struct Refusal {
    /// The refusal's code, `not_found`, `precondition_failed`, `conflict`.
    pub code: String,
    /// What it says.
    pub message: String,
    /// The details of the problem it is answered as.
    pub details: Value,
}

/// What kind of constraint refused a row.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ConstraintKind {
    /// A unique key another row holds.
    Unique,
    /// A foreign key that names no row, or a row others point at.
    ForeignKey,
    /// A column that takes no null.
    NotNull,
    /// A check constraint.
    Check,
}

/// A row the database refused.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Constraint {
    /// The kind of constraint.
    pub kind: ConstraintKind,
    /// The columns the database named, unquoted, when it named any.
    pub columns: Vec<String>,
    /// The database's message.
    pub message: String,
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::NotFound(message)
            | Error::PageKey(message)
            | Error::VersionConflict(message)
            | Error::Invalid(message)
            | Error::Busy(message)
            | Error::Encryption(message)
            | Error::Definition(message)
            | Error::Database(message) => f.write_str(message),
            Error::NoUser => f.write_str(
                "no acting user: the write stamps who made it; open the repositories as a user, or as the system",
            ),
            Error::Constraint(constraint) => f.write_str(&constraint.message),
            Error::Refused(refusal) => write!(f, "{}: {}", refusal.code, refusal.message),
        }
    }
}

impl std::error::Error for Error {}

fn invalid(message: impl Into<String>) -> Error {
    Error::Invalid(message.into())
}

/// The order of `find_one` and `find_many`: a column's member, ascending
/// or descending.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct OrderBy {
    /// The member.
    pub field: String,
    /// Descending.
    pub desc: bool,
}

impl OrderBy {
    /// Ascending by `field`.
    pub fn asc(field: impl Into<String>) -> Self {
        Self {
            field: field.into(),
            desc: false,
        }
    }

    /// Descending by `field`.
    pub fn desc(field: impl Into<String>) -> Self {
        Self {
            field: field.into(),
            desc: true,
        }
    }
}

/// The options of `find_one` and `find_many`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct FindOptions {
    /// The order, by each column in turn.
    pub order_by: Vec<OrderBy>,
    /// At most this many rows, the definition's default unless set;
    /// `find_many` alone.
    pub limit: Option<u32>,
    /// Skips this many rows first; `find_many` alone.
    pub offset: Option<u32>,
    /// Reads soft-deleted rows too.
    pub include_deleted: bool,
    /// Reads the rows without counting them first: `find_many`'s total is
    /// then `None` (D89).
    pub skip_count: bool,
}

/// The rows `find_many` read, and how many its filter finds in all unless
/// the options skipped the count.
#[derive(Clone, Debug, PartialEq)]
pub struct Found<R> {
    /// The rows.
    pub rows: Vec<R>,
    /// How many rows the filter finds.
    pub total: Option<u64>,
}

/// The options of `find_page`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct PageOptions {
    /// The sort field's member; the key unless set.
    pub sort: Option<String>,
    /// Descending.
    pub desc: bool,
    /// The key of the last row of the page before, in its wire form; the
    /// first page without it.
    pub after: Option<Value>,
    /// The rows a page holds; the definition's default unless set.
    pub limit: Option<u32>,
    /// Reads soft-deleted rows too.
    pub include_deleted: bool,
}

/// A page of rows in `find_page`'s order, and the key of its last row when
/// more follow.
#[derive(Clone, Debug, PartialEq)]
pub struct RowPage<R> {
    /// The rows.
    pub rows: Vec<R>,
    /// The key of the last row, in its wire form, when more rows follow.
    pub next: Option<Value>,
}

/// What a write of one row takes beside its key and fields.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct WriteOptions {
    /// Fences the write on the row's `_version`: a row that has moved from
    /// it is [`Error::VersionConflict`].
    pub expected_version: Option<i64>,
    /// The caller's preconditions (D16, D104), each use's entry by its use
    /// id, which the behaviors' core checks; a table that composes no
    /// behaviors refuses any.
    pub preconditions: Option<Values>,
}

/// The options of `upsert_one`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct UpsertOptions {
    /// Updates the row that holds the key only when a field the update
    /// sets differs from what the row holds (D89).
    pub only_if_changed: bool,
}

/// What an upsert did with a row (D89).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum UpsertStatus {
    /// It inserted the row.
    Inserted,
    /// It updated the row that held the key.
    Updated,
    /// It left that row as it was.
    Unchanged,
}

/// A row an upsert wrote or found, and what it did with it.
#[derive(Clone, Debug, PartialEq)]
pub struct Upserted<R> {
    /// The row.
    pub row: R,
    /// What the upsert did.
    pub status: UpsertStatus,
}

/// The conditions of a column that compares for equality alone: a UUID, a
/// boolean, JSON. Each member set is one condition, all of them ANDed.
/// `ne` and `not_in` keep the rows `eq` and `in_` leave out, a row whose
/// column is NULL among them (D112).
#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Equals<T> {
    /// The column is the value.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub eq: Option<T>,
    /// The column is not the value, or is null.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ne: Option<T>,
    /// The column is one of the values; no condition when empty.
    #[serde(rename = "in", skip_serializing_if = "Vec::is_empty")]
    pub in_: Vec<T>,
    /// The column is none of the values, or is null; no condition when
    /// empty.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub not_in: Vec<T>,
    /// The column is null, or is not.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub is_null: Option<bool>,
}

/// The conditions of a column that orders: a number, a date, a timestamp,
/// a decimal. `between` is `[min, max]`, both included.
#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
#[allow(missing_docs)]
pub struct Ordered<T> {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub eq: Option<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ne: Option<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub gt: Option<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub gte: Option<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub lt: Option<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub lte: Option<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub between: Option<(T, T)>,
    #[serde(rename = "in", skip_serializing_if = "Vec::is_empty")]
    pub in_: Vec<T>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub not_in: Vec<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub is_null: Option<bool>,
}

/// The conditions of a column of text. `ilike` keeps the rows that contain
/// the text, in any case; the text is a LIKE pattern's, so the caller
/// escapes a `%`, a `_` and a backslash it means literally.
#[derive(Clone, Debug, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
#[allow(missing_docs)]
pub struct Text<T> {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub eq: Option<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ne: Option<T>,
    #[serde(rename = "in", skip_serializing_if = "Vec::is_empty")]
    pub in_: Vec<T>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub not_in: Vec<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ilike: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub is_null: Option<bool>,
}

// Default by hand: a derive would ask it of T, which a filter holds none of
// until set.
impl<T> Default for Equals<T> {
    fn default() -> Self {
        Self {
            eq: None,
            ne: None,
            in_: Vec::new(),
            not_in: Vec::new(),
            is_null: None,
        }
    }
}

impl<T> Default for Ordered<T> {
    fn default() -> Self {
        Self {
            eq: None,
            ne: None,
            gt: None,
            gte: None,
            lt: None,
            lte: None,
            between: None,
            in_: Vec::new(),
            not_in: Vec::new(),
            is_null: None,
        }
    }
}

impl<T> Default for Text<T> {
    fn default() -> Self {
        Self {
            eq: None,
            ne: None,
            in_: Vec::new(),
            not_in: Vec::new(),
            ilike: None,
            is_null: None,
        }
    }
}

impl<T> Equals<T> {
    /// The rows whose column is `value`.
    pub fn eq(value: T) -> Self {
        Self {
            eq: Some(value),
            ..Self::default()
        }
    }
}

impl<T> Ordered<T> {
    /// The rows whose column is `value`.
    pub fn eq(value: T) -> Self {
        Self {
            eq: Some(value),
            ..Self::default()
        }
    }
}

impl<T> Text<T> {
    /// The rows whose column is `value`.
    pub fn eq(value: T) -> Self {
        Self {
            eq: Some(value),
            ..Self::default()
        }
    }
}

// A route's typed filter (D106, D112) is a repository's: its `contains`,
// which matches the text itself, is `ilike` with the pattern's characters
// escaped.
impl<T> From<crate::filter::Equals<T>> for Equals<T> {
    fn from(filter: crate::filter::Equals<T>) -> Self {
        Self {
            eq: filter.eq,
            ne: filter.ne,
            in_: filter.in_,
            not_in: filter.not_in,
            is_null: filter.is_null,
        }
    }
}

impl<T> From<crate::filter::Ordered<T>> for Ordered<T> {
    fn from(filter: crate::filter::Ordered<T>) -> Self {
        Self {
            eq: filter.eq,
            ne: filter.ne,
            gt: filter.gt,
            gte: filter.gte,
            lt: filter.lt,
            lte: filter.lte,
            between: None,
            in_: filter.in_,
            not_in: filter.not_in,
            is_null: filter.is_null,
        }
    }
}

impl<T> From<crate::filter::Text<T>> for Text<T> {
    fn from(filter: crate::filter::Text<T>) -> Self {
        Self {
            eq: filter.eq,
            ne: filter.ne,
            in_: filter.in_,
            not_in: filter.not_in,
            ilike: filter.contains.as_deref().map(escape_like),
            is_null: filter.is_null,
        }
    }
}

/// `text` with the characters a LIKE pattern reads, `%`, `_` and the
/// backslash that escapes them, escaped: the pattern of a filter's
/// `contains`, which matches the text itself.
pub fn escape_like(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for c in text.chars() {
        if matches!(c, '%' | '_' | '\\') {
            out.push('\\');
        }
        out.push(c);
    }
    out
}

/// A probe for the rows another table's relation points from.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ReferencedBy {
    /// Keeps the rows some row points at, or those none does.
    pub exists: bool,
    /// Counts soft-deleted rows of the other table too.
    pub include_deleted: bool,
}

/// The cell an `Encrypted<T>` column's envelope is bound to (D85): the
/// table type, the schema field and the row's key, a UUID hyphenated.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FieldCell<'a> {
    /// The table type.
    pub table: &'a str,
    /// The schema field.
    pub field: &'a str,
    /// The row's key.
    pub row: &'a str,
}

/// The keys of a database's `Encrypted<T>` fields (D85): what seals a
/// value into the envelope its column holds, and opens one.
pub trait FieldKeys: Send + Sync {
    /// The envelope of `plaintext` in `cell`, under the current key.
    fn seal(&self, cell: FieldCell<'_>, plaintext: &str) -> Result<String, String>;
    /// The plaintext of `envelope`, which a key of the ring sealed for
    /// `cell`.
    fn open(&self, cell: FieldCell<'_>, envelope: &str) -> Result<String, String>;
}

/// Who a behavior's command runs as (D103): the subject and the
/// permissions the core's guards read.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Caller {
    /// The caller's subject.
    pub subject: String,
    /// The permissions the caller holds.
    pub permissions: Vec<String>,
}

/// A create of a row of a table that composes behaviors, as the core takes
/// it (D103).
#[derive(Clone, Debug, PartialEq)]
pub struct CoreCreate<'a> {
    /// The table type.
    pub table: &'a str,
    /// The key the create names, in its wire form, when it names one.
    pub key: Option<String>,
    /// The fields the core reads, by member: the row's own and any owned
    /// field the input names, which the core refuses.
    pub fields: Values,
    /// The own columns as the insert binds them, by unquoted column.
    pub columns: Vec<(String, Bound)>,
    /// Who the command runs as.
    pub caller: Caller,
}

/// An update of a row through the core (D103).
#[derive(Clone, Debug, PartialEq)]
pub struct CoreUpdate<'a> {
    /// The table type.
    pub table: &'a str,
    /// The row's key, in its wire form.
    pub key: String,
    /// The fields the update sets, by member, as the core reads them.
    pub fields: Values,
    /// The own columns as the update binds them, by unquoted column, a
    /// `None` setting NULL, with `updated_at` and `updated_by`.
    pub columns: Vec<(String, Option<Bound>)>,
    /// The caller's preconditions.
    pub preconditions: Option<&'a Values>,
    /// Who the command runs as.
    pub caller: Caller,
}

/// A delete of a row through the core (D103).
#[derive(Clone, Debug, PartialEq)]
pub struct CoreDelete<'a> {
    /// The table type.
    pub table: &'a str,
    /// The row's key, in its wire form.
    pub key: String,
    /// The stamps of a soft delete, by unquoted column, which the core
    /// writes in place of deleting the row; `None` for a table without
    /// `deletedAt`.
    pub soft_delete: Option<Vec<(String, Bound)>>,
    /// The caller's preconditions.
    pub preconditions: Option<&'a Values>,
    /// Who the command runs as.
    pub caller: Caller,
}

/// A value a statement binds, and what its column holds: what a
/// repository hands its dialect's client, and the core its own columns.
#[derive(Clone, Debug, PartialEq)]
pub struct Bound {
    /// The value in its wire form.
    pub value: Value,
    /// What the column holds.
    pub marks: Marks,
}

/// What a column holds, as a value bound to it is written.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[allow(missing_docs)]
pub struct Marks {
    pub json: bool,
    pub list: bool,
    pub uuid: bool,
    pub timestamp: bool,
    pub interval: bool,
    pub boolean: bool,
    pub numeric: bool,
}

impl Marks {
    /// A text parameter, bound as it is: a pattern.
    pub const TEXT: Marks = Marks {
        json: false,
        list: false,
        uuid: false,
        timestamp: false,
        interval: false,
        boolean: false,
        numeric: false,
    };

    /// A timestamp.
    pub const TIMESTAMP: Marks = Marks {
        timestamp: true,
        ..Marks::TEXT
    };

    fn of(column: &Column) -> Self {
        Marks {
            json: column.json,
            list: column.list,
            uuid: column.uuid,
            timestamp: column.timestamp,
            interval: column.interval,
            boolean: column.boolean,
            numeric: column.numeric,
        }
    }
}

/// Who a repository acts as, the keys of its encrypted fields, and the
/// clock it stamps with: what a database's repositories share.
#[derive(Clone, Default)]
pub struct Session<'a> {
    /// The acting user's id, in either form of a UUID, or none (D87).
    pub user: Option<String>,
    /// The keys of the database's `Encrypted<T>` fields (D85).
    pub keys: Option<&'a dyn FieldKeys>,
    /// Who a behavior's command runs as, the acting user holding no
    /// permissions unless set (D103).
    pub caller: Option<Caller>,
    /// The time a write stamps, the system's unless set: a test's clock.
    pub now: Option<SystemTime>,
}

impl fmt::Debug for Session<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Session")
            .field("user", &self.user)
            .field("keys", &self.keys.is_some())
            .field("caller", &self.caller)
            .finish()
    }
}

impl Session<'_> {
    fn now(&self) -> String {
        rfc3339_millis(self.now.unwrap_or_else(SystemTime::now))
    }

    fn caller(&self) -> Caller {
        self.caller.clone().unwrap_or_else(|| Caller {
            subject: self.user.clone().unwrap_or_default(),
            permissions: Vec::new(),
        })
    }
}

/// A time in RFC 3339, in UTC, to the millisecond: the form a SQLite
/// column's default writes, so the times a repository stamps order with
/// the ones the database stamped.
pub fn rfc3339_millis(time: SystemTime) -> String {
    let (seconds, millis) = match time.duration_since(SystemTime::UNIX_EPOCH) {
        Ok(since) => (
            i64::try_from(since.as_secs()).unwrap_or(i64::MAX),
            i64::from(since.subsec_millis()),
        ),
        Err(before) => {
            let before = before.duration();
            let whole = i64::try_from(before.as_secs()).unwrap_or(i64::MAX);
            match i64::from(before.subsec_millis()) {
                0 => (-whole, 0),
                millis => (-whole - 1, 1000 - millis),
            }
        }
    };
    let (year, month, day) = from_days(seconds.div_euclid(86_400));
    let of_day = seconds.rem_euclid(86_400);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{millis:03}Z",
        of_day / 3600,
        of_day % 3600 / 60,
        of_day % 60
    )
}

/// `value` as the JSON object a repository reads: a create's input, an
/// update or a filter.
pub fn values<T: Serialize + ?Sized>(value: &T) -> Result<Values, Error> {
    match serde_json::to_value(value) {
        Ok(Value::Object(members)) => Ok(members),
        Ok(other) => Err(invalid(format!("{other} is no object"))),
        Err(err) => Err(invalid(err.to_string())),
    }
}

/// A row as its Rust type.
pub fn row<T: DeserializeOwned>(row: Values) -> Result<T, Error> {
    serde_json::from_value(Value::Object(row))
        .map_err(|err| Error::Definition(format!("a row the database holds: {err}")))
}

/// A value in its wire form, as a key or a version a repository takes.
pub fn wire<T: Serialize + ?Sized>(value: &T) -> Result<Value, Error> {
    serde_json::to_value(value).map_err(|err| invalid(err.to_string()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn a_time_is_rfc_3339_to_the_millisecond() {
        let at = SystemTime::UNIX_EPOCH + Duration::from_millis(1_791_979_200_125);
        assert_eq!(rfc3339_millis(at), "2026-10-14T12:00:00.125Z");
        assert_eq!(
            rfc3339_millis(SystemTime::UNIX_EPOCH),
            "1970-01-01T00:00:00.000Z"
        );
        let before = SystemTime::UNIX_EPOCH - Duration::from_millis(1);
        assert_eq!(rfc3339_millis(before), "1969-12-31T23:59:59.999Z");
    }

    #[test]
    fn a_contains_filter_matches_its_text_itself() {
        assert_eq!(escape_like(r"50%_off\"), r"50\%\_off\\");
        let text: Text<String> = crate::filter::Text {
            contains: Some("a_b".to_owned()),
            ..Default::default()
        }
        .into();
        assert_eq!(text.ilike.as_deref(), Some(r"a\_b"));
    }
}
