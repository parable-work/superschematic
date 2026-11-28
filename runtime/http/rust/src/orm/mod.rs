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
//! `Encrypted<T>` column with the keys it was given (D85), the
//! [`FieldKeyring`] of the DB service's key secret, whose envelopes are
//! the Go ORM's and the TypeScript repositories' byte for byte
//! ([`fieldcrypt`]), and writes a table that composes behaviors through
//! the behaviors' core (D103): a create, an update, a delete and an
//! operation of one of its behaviors, each run by the `Rules` of its
//! dialect, which a generated types crate binds to the core's adapter. An
//! upsert of such a table reads whether a row holds the key, then runs the
//! core's create or update; an update of many rows runs the core's update
//! of each; and a hard delete of a live row is the core's delete.
//!
//! Repositories opened for a caller ([`AccessCaller`], D156) keep every
//! read and write to what the caller sees, as the TypeScript repositories
//! do: an `@audience` table's rows whose effective audience holds the
//! caller ([`audience_conditions`]), and for a party or an agent run the
//! rows one of its grants reaches, through the relation paths its role
//! names. A row the caller does not see is the [`Error::NotFound`] of one
//! that is not there, and a create of a row its grants do not reach is
//! [`Error::AccessRefused`]. Repositories opened for no caller, a job's or
//! the system's, read every row.

pub mod agents;
pub mod calendar;
pub mod fieldcrypt;
pub mod file_processing;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub mod inbound;
pub mod notify;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub mod parties;
mod plan;
#[cfg(feature = "postgres")]
pub mod postgres;
pub mod routes;
pub mod rowfeatures;
mod run;
pub mod search_tables;
#[cfg(feature = "sqlite")]
pub mod sqlite;
#[cfg(feature = "sqlite")]
mod sqlite_file;
#[cfg(any(feature = "postgres", feature = "sqlite"))]
pub mod store;

use std::collections::BTreeMap;
use std::fmt;
use std::time::SystemTime;

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::Map;

use crate::auth::{ActingFor, GrantReach, Principal, PrincipalKind};
use crate::civil::from_days;
use crate::uuid62::parse_uuid;

pub use fieldcrypt::FieldKeyring;
pub use plan::fill;
// A value in its wire form, as a member of [`Values`] holds one, for a
// types crate's `orm` module to name without a serde_json of its own.
pub use serde_json::Value;

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
    /// The conditions of a JSON column's filter where they are not
    /// [`Clauses`]': SQLite's, which compare JSON values where its text
    /// would compare text (D116). Postgres's JSONB compares its values
    /// itself, and carries none.
    #[serde(default, rename = "jsonClauses")]
    pub json_clauses: Option<JsonClauses>,
    /// The rows `find_many` and `find_page` read unless told.
    pub default_limit: u32,
    /// The most parameters one statement binds in the dialect, by which
    /// `upsert_many` batches its rows (D89): 65535 on Postgres, 32766 on
    /// SQLite. Zero in a definition a compiler before it wrote.
    #[serde(default)]
    pub max_parameters: usize,
    /// Who a repository opened as the system acts as (D87): the DB
    /// service's `outputs.sql.systemActor`, [`DEFAULT_SYSTEM_ACTOR`] unless
    /// set.
    #[serde(default)]
    pub system_actor: Option<SystemActor>,
    /// An update of a table with `updatedBy` needs an acting user, as a
    /// create of a table with `createdBy` does (D87).
    #[serde(default)]
    pub require_updated_by: bool,
    /// The statement a hard delete of a versioned table follows, which
    /// sets the transaction's history actor to its first parameter, and
    /// the statement that clears it (D17, D32); Postgres's alone.
    #[serde(default, rename = "historyActorCTE")]
    pub history_actor_cte: String,
    #[serde(default)]
    pub clear_history_actor: String,
    /// The tables, in the schema's order.
    pub tables: Vec<Table>,
    /// The references of the database (D154): its `@references` tables,
    /// the tables a reference may point at, and the conversations whose
    /// ref blocks are references; none for a database with neither.
    #[serde(default)]
    pub references: Option<rowfeatures::RowReferencesDefinition>,
    /// The attachments of the database (D154): its `@attachments` tables;
    /// none for a database with none.
    #[serde(default)]
    pub attachments: Option<rowfeatures::AttachmentsDefinition>,
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

    /// The table that is `access` to per-record access (D156):
    /// [`ACCESS_GRANTS`], [`ACCESS_LOG`] or [`ACCESS_ROLES`].
    pub fn access_table(&self, access: &str) -> Option<&Table> {
        self.tables
            .iter()
            .find(|table| table.access.as_deref() == Some(access))
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
    /// An upsert of many rows' assignment of the column `%s` from the
    /// value the row it would have inserted holds in the column `%s`, and
    /// that value (D89); empty in a definition a compiler before
    /// `upsert_many` wrote, which refuses it.
    #[serde(default)]
    pub set_excluded: String,
    #[serde(default)]
    pub excluded: String,
}

/// The conditions of a JSON column's filter, `sqlutil.JSONClauses`: `eq`
/// and `ne` of the column `%s` and its parameter's number `%d`, `in` and
/// `noneOf` of the column `%s` and the list `%s` of its parameters, each a
/// `value` of the parameter numbered `%d`.
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
#[allow(missing_docs)]
pub struct JsonClauses {
    pub eq: String,
    pub ne: String,
    #[serde(rename = "in")]
    pub in_: String,
    pub none_of: String,
    pub value: String,
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
    /// A table with an Encrypted<T> column's: the read of the rows another
    /// key sealed, and their reseal under the current one (D85).
    pub stale: String,
    pub reseal: String,
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
    /// A @versioned table whose hard deletes set the history actor.
    #[serde(default)]
    pub history_actor: bool,
    /// The table's @fenced fields, in the order declared (D119); none for
    /// most tables.
    #[serde(default)]
    pub fences: Vec<Fence>,
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
    /// repository's `Rules` runs.
    #[serde(default)]
    pub behaviors: Option<Behaviors>,
    /// What `merge_into` runs, for a table that implements Mergeable
    /// (D147); none for any other.
    #[serde(default)]
    pub merge: Option<Merge>,
    /// The one-to-many relations of the table (D90): a filter reaches
    /// through one to the rows that point back at the row, as a row rule's
    /// path and a grant's do (D156).
    #[serde(default)]
    pub many: Vec<Many>,
    /// An `@audience` table's resolved columns (D156), which a read for a
    /// caller compares the caller with.
    #[serde(default)]
    pub audience: Option<Audience>,
    /// What the table is to per-record access (D156): `grants`, the table
    /// of grants; `log`, the access log; `roles`, the UserRole table whose
    /// rows a grant's role names. None for every other.
    #[serde(default)]
    pub access: Option<String>,
    /// The table's `@calendarSource` declarations (D152), which
    /// [`calendar::read_calendar`] merges; none for most tables, and on
    /// Postgres alone (D188).
    #[serde(default)]
    pub calendar: Vec<calendar::CalendarSource>,
    /// A `@searchable` table's index (D155), which
    /// [`search_tables::search_tables`] ranks and
    /// [`search_tables::index_search`] writes; Postgres alone (D188).
    #[serde(default)]
    pub searchable: Option<search_tables::SearchableDefinition>,
    /// The table's `@event` declarations (D153), which
    /// [`notify::Notifier::raise`] holds an event to and whose payload and
    /// title it reads; none for most tables, and on Postgres alone (D191).
    #[serde(default)]
    pub events: Vec<EventDefinition>,
    /// What the table is to the notifier (D153): its `@notifier` part
    /// (`inbox`, `deliveries`, `routes`, `preferences`, `subscriptions`,
    /// `channelTrust`, `replies`, `dispatch`), or `channelIdentities` for
    /// the table of channel identities; none for every other.
    #[serde(default)]
    pub notifier: Option<String>,
    /// A `@party` table (D157), whose rows are principals of kind party:
    /// the field a party is named by. None for every other table.
    #[serde(default)]
    pub party: Option<PartyDefinition>,
    /// What the table is to the conversations (D157): `conversations` or
    /// `messages`; none for every other.
    #[serde(default)]
    pub conversations: Option<String>,
    /// What the table is to the agent orchestration (D158): `definitions`,
    /// `runs`, `requests`, `policies`, `approvals` or `controls`; none for
    /// every other.
    #[serde(default)]
    pub agents: Option<String>,
}

/// What a `@party` table says of its parties (D157).
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
pub struct PartyDefinition {
    /// The field a party is named by; none when it names none.
    #[serde(default)]
    pub title: Option<String>,
}

/// One `@event` of a table (D153): its kind, its default urgency, the
/// member a notice is titled by and the members its payload carries.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
pub struct EventDefinition {
    /// Dotted lower-camel words: `ticket.overdue`.
    pub kind: String,
    /// `urgent`, `normal`, `digest` or `record`.
    pub urgency: String,
    /// The member a notice of it is titled by; the kind and the key unless
    /// set.
    #[serde(default)]
    pub title: Option<String>,
    /// The members of the row its payload carries.
    #[serde(default)]
    pub payload: Vec<String>,
}

/// What `merge_into` of a table that implements Mergeable runs (D147),
/// written at build time from the schema's relations and unique indexes,
/// in the definition's dialect (sqlutil's `MergePlan`): each statement
/// takes the key of the row merged away first and the key of the row that
/// stays second, and one with an actor the acting user third.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Merge {
    /// Reads the two rows, locked where the dialect locks a read: a row
    /// per one it finds, true for the row merged away.
    pub lock: String,
    /// A step deletes rows of a @versioned table, whose tombstone records
    /// the acting user (Postgres alone).
    #[serde(default)]
    pub history_actor: bool,
    /// The merge stamps a row deleted, so it needs an acting user before it
    /// writes, as a soft delete does.
    #[serde(default)]
    pub requires_user: bool,
    /// The relations that reference the table, by source and field.
    #[serde(default)]
    pub steps: Vec<MergeStep>,
}

/// One relation that references a Mergeable table (D147).
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MergeStep {
    /// What the counts name the step's rows by.
    pub source: String,
    /// What holds the reference.
    pub field: String,
    /// On a relation of the table to itself, reads whether the row that
    /// stays references the row merged away through it.
    #[serde(default)]
    pub self_reference: Option<String>,
    /// Run first, counted nowhere: an Aliased use's insert of the name of
    /// the row merged away as an alias of the row that stays.
    #[serde(default)]
    pub before: Vec<MergeStatement>,
    /// Each deletes the rows a unique key would refuse once repointed.
    #[serde(default)]
    pub collisions: Vec<MergeStatement>,
    /// Repoints every row that references the row merged away.
    pub repoint: MergeStatement,
    /// The rows the step writes as per-record access holds them (D156);
    /// none for a step whose rows are the merged rows' own (an Aliased
    /// use's, and the pairs of a `@manyToMany` the table declares).
    #[serde(default)]
    pub reach: Option<MergeReach>,
}

/// The code of [`Error::Merge`] for a merge opened for a caller (D156)
/// that would repoint or delete a row of another table the caller may not
/// write.
pub const MERGE_OUT_OF_REACH: &str = "out_of_reach";

/// The rows a merge's step writes as per-record access holds them (D156):
/// a merge opened for a caller refuses, before it writes, when one of them
/// is a row the caller may not write.
#[derive(Clone, Debug, Default, Deserialize)]
pub struct MergeReach {
    /// The table type whose access holds the rows.
    #[serde(rename = "type")]
    pub table: String,
    /// Reads one of them, `SELECT 1 FROM` the table `WHERE` a condition
    /// that takes the key of the row merged away first, which the
    /// conditions of the caller's write of the table's rows are added to.
    pub rows: String,
}

/// One statement of a merge (D147): its SQL, and how it takes the acting
/// user, `uuid` for a uuid column, `text` for another, none when it takes
/// none.
#[derive(Clone, Debug, Default, Deserialize)]
pub struct MergeStatement {
    /// The statement.
    pub sql: String,
    /// How it takes the acting user.
    #[serde(default)]
    pub actor: Option<String>,
}

/// What a merge did to the tables that referenced the row merged away
/// (D147), each by its type's name: the rows it repointed to the row that
/// stays, and the rows it deleted because a unique index of their table
/// would have refused them once repointed. No row is in both. The row
/// merged away is in neither.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct MergeResult {
    /// The rows repointed, by table.
    pub repointed: BTreeMap<String, u64>,
    /// The rows deleted, by table.
    pub deleted: BTreeMap<String, u64>,
}

/// A one-to-many relation of a table (D90, D156): the rows of another
/// table whose to-one relation points back at the row.
#[derive(Clone, Debug, Deserialize)]
pub struct Many {
    /// The relation's member, which a filter reaches through.
    pub name: String,
    /// The other table's type.
    pub target: String,
    /// The other table's member that holds the foreign key, `<field>Id`.
    pub column: String,
}

/// What a read of an `@audience` table compares its caller with (D156):
/// the row's effective audience, null for everyone or the ids of the
/// principals it is visible to, and its effective agent access, each a
/// column qualified by its table. On a projection, `probe` is the view's
/// key in the keys of its source's rows of the WHERE `%s`.
#[derive(Clone, Debug, Deserialize)]
pub struct Audience {
    /// The effective audience's column: a `UUID[]` on Postgres, a JSON
    /// array of the ids' text on SQLite.
    pub effective: String,
    /// The effective agent access's column: `followRole`, `readOnly` or
    /// `none`.
    pub agents: String,
    /// A projection's probe of its source's rows.
    #[serde(default)]
    pub probe: Option<String>,
}

/// What [`Table::access`] names: the table of grants (D156).
pub const ACCESS_GRANTS: &str = "grants";
/// What [`Table::access`] names: the access log (D156).
pub const ACCESS_LOG: &str = "log";
/// What [`Table::access`] names: the UserRole table a grant's role is a
/// row of (D156).
pub const ACCESS_ROLES: &str = "roles";

/// A fence of a table, a @fenced field (D119): a counter every write of a
/// column it covers sets to the stored value plus one, which a trigger
/// holds every writer to. An update, an update of many rows and an
/// upsert's update add its bump to their assignments
/// (`plan::fence_bumps`); a soft delete carries it in its statement.
#[derive(Clone, Debug, Deserialize)]
pub struct Fence {
    /// The fence's column, as a statement names it.
    pub column: String,
    /// The columns a write of which advances the fence, as a statement
    /// names them; none for a fence that counts every update of the row.
    #[serde(default)]
    pub covers: Vec<String>,
    /// The assignment that advances the fence by one.
    pub bump: String,
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
    /// The operations on a row that write nothing, by the member they sit
    /// under (`comments`: `listMessages`), as the core declares them: a
    /// caller who sees the row may run one of them, and every other
    /// operation needs a caller who may write it (D156).
    #[serde(default)]
    pub reads: BTreeMap<String, Vec<String>>,
    /// The operations of the table as a whole whose answer names one of
    /// its rows (Aliased's `resolve`), which the repositories keep to their
    /// caller (D156): the core reads every row.
    #[serde(default)]
    pub answers: Vec<AnsweredRow>,
}

/// An operation of a table as a whole whose answer names one of the
/// table's rows (D156): under `row`, an object whose `key` is the row's
/// key in its wire form, or null for none. Opened for a caller, the
/// repositories answer null there for a row the caller does not see, as
/// for an answer of no row.
#[derive(Clone, Debug, Default, Deserialize)]
pub struct AnsweredRow {
    /// The member the use's operations sit under.
    pub member: String,
    /// The operation.
    pub operation: String,
    /// The member of the answer that holds the row.
    pub row: String,
}

impl Behaviors {
    /// Whether `operation` under `member` writes: every operation the
    /// definition does not name a read, so one written before it named
    /// them needs a caller who may write the row.
    pub fn writes(&self, member: &str, operation: &str) -> bool {
        !self
            .reads
            .get(member)
            .is_some_and(|reads| reads.iter().any(|read| read == operation))
    }
}

impl Table {
    /// The relation `name` of the table, to-one or one-to-many, and the
    /// type of the table it reaches.
    pub fn relation_target(&self, name: &str) -> Option<&str> {
        self.relations
            .iter()
            .find(|relation| relation.field == name)
            .map(|relation| relation.target.as_str())
            .or_else(|| {
                self.many
                    .iter()
                    .find(|many| many.name == name)
                    .map(|many| many.target.as_str())
            })
    }

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
    /// The behaviors' core refused the command (D103), as its `Rules`
    /// says it. Boxed, so a result that may hold one stays small.
    Refused(Box<Refusal>),
    /// A merge the repository refuses before it writes (D147): of a row
    /// into itself (`self`), into a row that references the one merged
    /// away through a relation of the table to itself (`self_reference`),
    /// or, opened for a caller (D156), one that would repoint or delete a
    /// row of another table the caller may not write (`out_of_reach`),
    /// which a table operation answers 403.
    Merge {
        /// `self`, `self_reference` or `out_of_reach`.
        code: &'static str,
        /// What it says.
        message: String,
    },
    /// A party's or an agent run's write that leaves a row its grants do
    /// not reach (D156): a create under a target no grant names. A table
    /// operation answers it 403, and its transaction keeps nothing.
    AccessRefused(String),
    /// An `Encrypted<T>` column's value could not be sealed or opened.
    Encryption(String),
    /// The definition, or a row the database holds, is not what the
    /// repository reads.
    Definition(String),
    /// Any other failure of the database.
    Database(String),
}

/// A refusal of the behaviors' core (D16, D103): its code, its status, its
/// message and the details a problem carries.
#[derive(Clone, Debug, PartialEq)]
pub struct Refusal {
    /// The refusal's code, `not_found`, `forbidden`, `vetoed`.
    pub code: String,
    /// The HTTP status the code answers with, the core's for each.
    pub status: u16,
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
            | Error::AccessRefused(message)
            | Error::Encryption(message)
            | Error::Definition(message)
            | Error::Database(message) => f.write_str(message),
            Error::NoUser => f.write_str(
                "no acting user: the write stamps who made it; open the repositories as a user, or as the system",
            ),
            Error::Constraint(constraint) => f.write_str(&constraint.message),
            Error::Refused(refusal) => write!(f, "{}: {}", refusal.code, refusal.message),
            Error::Merge { message, .. } => f.write_str(message),
        }
    }
}

impl std::error::Error for Error {}

impl Error {
    /// A refusal of the behaviors' core (D103) as a repository's error:
    /// its code, the status the code answers with, its message and the
    /// details its problem carries. The core's `busy`, a row that kept
    /// moving or a writer held past its timeout, is [`Error::Busy`], which
    /// the caller may try again.
    pub fn refused(
        code: &str,
        status: u16,
        message: impl Into<String>,
        details: Option<Values>,
    ) -> Self {
        let message = message.into();
        if code == "busy" {
            return Error::Busy(message);
        }
        Error::Refused(Box::new(Refusal {
            code: code.to_owned(),
            status,
            message,
            details: details.map_or(Value::Null, Value::Object),
        }))
    }
}

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

/// The options of `upsert_many` (D89).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct UpsertManyOptions {
    /// Updates a row that holds an input's key only when a field the
    /// update sets differs from what the row holds.
    pub only_if_changed: bool,
    /// The most rows one statement writes; none is as many as the
    /// dialect binds the parameters of. More rows than a batch take a
    /// statement each, in the caller's transaction.
    pub batch: Option<usize>,
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
/// value into the envelope its column holds, and opens one. A server's
/// are the [`FieldKeyring`] of the DB service's key secret.
pub trait FieldKeys: Send + Sync {
    /// The envelope of `plaintext` in `cell`, under the current key.
    fn seal(&self, cell: FieldCell<'_>, plaintext: &str) -> Result<String, String>;
    /// The plaintext of `envelope`, which a key of the ring sealed for
    /// `cell`.
    fn open(&self, cell: FieldCell<'_>, envelope: &str) -> Result<String, String>;
    /// The start of every envelope the current key seals, as a LIKE
    /// pattern, by which a reencrypt reads the rows another key sealed;
    /// none for keys that cannot say, which reseal nothing.
    fn current_prefix(&self) -> Option<String> {
        None
    }
    /// Whether `envelope` was sealed under the current key.
    fn is_current(&self, envelope: &str) -> bool {
        let _ = envelope;
        false
    }
}

/// Who a behavior's command runs as (D103): the subject and the
/// permissions the core's guards read.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Caller {
    /// The caller's subject.
    pub subject: String,
    /// The permissions the caller holds.
    pub permissions: Vec<String>,
    /// A service standing in for an end user (D37), which holds no
    /// permission.
    pub stands_in: bool,
    /// What the caller is (D156): `user`, `party`, `agent` or
    /// `extension`; `None` for a user.
    pub kind: Option<String>,
    /// The subject the caller acts for (D77, D156).
    pub acting_for: Option<String>,
    /// The kind of the subject it acts for; `None` for a user.
    pub acting_for_kind: Option<String>,
    /// The Agent whose run the caller is (D158, D200).
    pub agent: Option<String>,
    /// How its authenticator verified it for the call (D200).
    pub assurance: Option<crate::auth::Assurance>,
}

impl Caller {
    /// The caller a route's principal is: its subject and the permissions
    /// it holds, each once, its kind and whom it acts for, each left out for
    /// a user, its run's Agent and its assurance (D200); no one, an empty
    /// subject, without one.
    pub fn of(principal: Option<&crate::auth::Principal>) -> Self {
        let Some(principal) = principal else {
            return Self::default();
        };
        let mut permissions: Vec<String> = Vec::new();
        for permission in &principal.permissions {
            if !permissions.contains(permission) {
                permissions.push(permission.clone());
            }
        }
        let named = |kind: crate::auth::PrincipalKind| {
            (kind != crate::auth::PrincipalKind::User).then(|| kind.as_str().to_owned())
        };
        Self {
            subject: principal.subject.clone(),
            permissions,
            stands_in: false,
            kind: named(principal.kind),
            acting_for: principal.acting_for.as_ref().map(|a| a.subject.clone()),
            acting_for_kind: principal
                .acting_for
                .as_ref()
                .and_then(|a| a.kind)
                .and_then(named),
            agent: principal.agent().map(str::to_owned),
            assurance: principal.assurance.clone(),
        }
    }
}

/// The columns a command through the core writes of its own row, each by
/// its unquoted name with the value its statement binds, `None` for NULL:
/// what the repository encoded of its input, its audit stamps among them,
/// which the core's write of the row binds in place of its own fields.
pub type CoreColumns = Vec<(String, Option<Bound>)>;

/// A create of a row of a table that composes behaviors, as the core takes
/// it (D103).
#[derive(Clone, Debug)]
pub struct CoreCreate<'a> {
    /// The table.
    pub table: &'a Table,
    /// The key the create names, in its wire form, when it names one.
    pub key: Option<String>,
    /// The fields the core reads, in their wire form, by unquoted column:
    /// every column the input names but an encrypted one, whose plaintext
    /// the core has no use for. What runs the command hands the core the
    /// ones the behaviors' layout names, the row's own and any owned field
    /// the input names, which the core refuses.
    pub fields: Values,
    /// The own columns as the insert binds them, by unquoted column.
    pub columns: CoreColumns,
    /// Who the command runs as.
    pub caller: Caller,
}

/// An update of a row through the core (D103).
#[derive(Clone, Debug)]
pub struct CoreUpdate<'a> {
    /// The table.
    pub table: &'a Table,
    /// The row's key, in its wire form.
    pub key: String,
    /// The fields the update sets, in their wire form, by unquoted column,
    /// as a create's are.
    pub fields: Values,
    /// The own columns as the update binds them, by unquoted column, a
    /// `None` setting NULL, with `updated_by`.
    pub columns: CoreColumns,
    /// The unquoted columns the core's update of the row sets to the
    /// database's clock: `updated_at`, which its insert's default stamped
    /// from that clock too.
    pub now: Vec<String>,
    /// The caller's preconditions.
    pub preconditions: Option<&'a Values>,
    /// Who the command runs as.
    pub caller: Caller,
}

/// A delete of a row through the core (D103).
#[derive(Clone, Debug)]
pub struct CoreDelete<'a> {
    /// The table.
    pub table: &'a Table,
    /// The row's key, in its wire form.
    pub key: String,
    /// The stamps of a soft delete, by unquoted column, which the core
    /// writes in place of deleting the row; `None` for a table without
    /// `deletedAt`.
    pub soft_delete: Option<CoreColumns>,
    /// The caller's preconditions.
    pub preconditions: Option<&'a Values>,
    /// Who the command runs as.
    pub caller: Caller,
}

/// A field of a table as the behaviors' layout names it (D103): the name
/// the core gives it, its unquoted column, and whether the core holds it
/// as epoch milliseconds, a timestamp.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct CoreField<'a> {
    /// The field, as the core names it.
    pub field: &'a str,
    /// Its column, unquoted.
    pub column: &'a str,
    /// The core holds it as epoch milliseconds.
    pub epoch_millis: bool,
}

/// The fields of a command as the core reads them (D103), by the names
/// the layout gives them: each of `layout`'s fields, the table's own and
/// its behaviors' owned ones, that `fields` holds by its column, a
/// timestamp as epoch milliseconds and any other value as it is, as the
/// TypeScript repositories hand the core an input's.
pub fn core_own<'a>(
    table: &Table,
    fields: &Values,
    layout: impl IntoIterator<Item = CoreField<'a>>,
) -> Result<Values, Error> {
    let mut own = Values::new();
    for field in layout {
        let Some(value) = fields.get(field.column) else {
            continue;
        };
        let value = match value {
            Value::String(text) if field.epoch_millis => {
                Value::from(epoch_millis(text).ok_or_else(|| {
                    invalid(format!(
                        "{}.{}: {text:?} is no RFC 3339 timestamp",
                        table.name, field.field
                    ))
                })?)
            }
            other => other.clone(),
        };
        own.insert(field.field.to_owned(), value);
    }
    Ok(own)
}

/// An operation of one of a table's behaviors on a row, through the core
/// (D103): a Workflow's `transition`, a Timers' `set`.
#[derive(Clone, Debug)]
pub struct CoreOperation<'a> {
    /// The table.
    pub table: &'a Table,
    /// The row's key, in its wire form.
    pub key: String,
    /// The member the use's operations sit under: its field, or the
    /// behavior's own for a use the table implements (`timers`).
    pub member: &'a str,
    /// The operation, as the behavior declares it.
    pub operation: &'a str,
    /// The operation's parameters, in their wire form.
    pub params: Value,
    /// The stamps the operation's write of the row binds, by unquoted
    /// column: `updated_by` when the repositories act as a user.
    pub columns: CoreColumns,
    /// The unquoted columns the operation's write of the row sets to the
    /// database's clock: `updated_at`.
    pub now: Vec<String>,
    /// The caller's preconditions.
    pub preconditions: Option<&'a Values>,
    /// Who the command runs as.
    pub caller: Caller,
}

/// An operation of one of a table's behaviors on the table as a whole
/// (D104), a command without a row, through the core: a Pool's `pick`. It
/// takes no preconditions, which are a row's.
#[derive(Clone, Debug)]
pub struct CoreTableOperation<'a> {
    /// The table.
    pub table: &'a Table,
    /// The member the use's operations sit under.
    pub member: &'a str,
    /// The operation, as the behavior declares it.
    pub operation: &'a str,
    /// The operation's parameters, in their wire form.
    pub params: Value,
    /// Who the command runs as.
    pub caller: Caller,
}

/// The refusal of an operation of a table as a whole by a dialect's
/// `Rules` that run none: a binding written before they could (D104).
#[allow(dead_code)]
pub(crate) fn no_table_operations(operation: &CoreTableOperation<'_>) -> Error {
    Error::Definition(format!(
        "{}.{}.{} is an operation of the table as a whole (D104), and the repositories' rules run none: regenerate the types crate, whose rules run them",
        operation.table.name, operation.member, operation.operation
    ))
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
    /// What is told each statement's text as it runs: a test that holds
    /// the repositories to the Go ORM's statements, or a log.
    pub trace: Option<&'a (dyn Fn(&str) + Sync)>,
    /// Whom the reads and writes keep to (D156): the caller of the
    /// operation the repositories serve, the anonymous one for a route
    /// with no principal. `None` reads every row, as a job and the system
    /// do.
    pub access: Option<AccessCaller>,
}

/// The caller of an operation as the reads hold it (D156): its subject,
/// its kind and whom it acts for, and the grants it calls through, or no
/// subject for an operation with no principal, which sees the rows
/// everyone may. The TypeScript runtime's `AccessCaller`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AccessCaller {
    /// The caller's subject; none for an anonymous caller.
    pub subject: Option<String>,
    /// What the caller is.
    pub kind: PrincipalKind,
    /// Whom the caller acts for.
    pub acting_for: Option<ActingFor>,
    /// The grants the caller calls through: a caller with grants reaches,
    /// of a table, only the rows one of them reaches.
    pub grants: Option<Vec<GrantReach>>,
}

impl AccessCaller {
    /// The anonymous caller: an operation with no principal.
    pub fn anonymous() -> Self {
        Self::default()
    }

    /// The access caller of a route's principal, or the anonymous one for
    /// none.
    pub fn of(principal: Option<&Principal>) -> Self {
        let Some(principal) = principal else {
            return Self::anonymous();
        };
        Self {
            subject: Some(principal.subject.clone()),
            kind: principal.kind,
            acting_for: principal.acting_for.clone(),
            grants: principal.grants.clone(),
        }
    }

    /// The ids an audience admits the caller by (D156): its subject, and
    /// for an agent run whom it acts for, each a UUID, hyphenated; a
    /// subject that is no UUID admits by none.
    pub fn audience_ids(&self) -> Vec<String> {
        let acting_for = match self.kind {
            PrincipalKind::Agent => self.acting_for.as_ref().map(|a| a.subject.as_str()),
            _ => None,
        };
        [self.subject.as_deref(), acting_for]
            .into_iter()
            .flatten()
            .filter_map(parse_uuid)
            .map(|uuid| uuid.hyphenated().to_string())
            .collect()
    }
}

/// The conditions that keep a read, or with `write` a write, of an
/// `@audience` table to what `caller` may see (D156), each value appended
/// to `args`: the reusable audience predicate a read of rows across tables
/// (a search, a calendar's feed, references, notifications) applies as a
/// repository does, written as the TypeScript runtime's
/// `audienceConditions` writes it. The rows whose effective audience is
/// everyone or holds the caller's id, or for an agent run its own or the
/// id of whom it acts for, and whose agent access lets an agent run read
/// them, or with `write` write them. A caller with no id, an anonymous one
/// or one whose subject is no UUID, sees the rows everyone may. A
/// projection's probe holds the view to its source's rows that do.
/// On Postgres an audience is a `UUID[]` compared by array overlap; on
/// SQLite it is a JSON array of the ids' hyphenated text, which a trigger
/// keeps and `json_each` reads (D182, #611), sqlutil's
/// `SQLiteAudienceContains`.
pub fn audience_conditions(
    definition: &Definition,
    audience: &Audience,
    caller: &AccessCaller,
    args: &mut Vec<Bound>,
    write: bool,
) -> Result<Vec<String>, Error> {
    let ids = caller.audience_ids();
    let mut parts = Vec::new();
    if ids.is_empty() {
        parts.push(format!("{} IS NULL", audience.effective));
    } else {
        let mut placeholders = Vec::with_capacity(ids.len());
        for id in ids {
            args.push(Bound {
                value: Value::String(id),
                marks: Marks {
                    uuid: true,
                    ..Marks::TEXT
                },
            });
            placeholders.push(plan::fill(&definition.clauses.placeholder, &[&args.len()])?);
        }
        let ids = placeholders.join(", ");
        let effective = &audience.effective;
        parts.push(match definition.dialect {
            Dialect::Postgres => {
                format!("({effective} IS NULL OR {effective} && ARRAY[{ids}]::uuid[])")
            }
            Dialect::Sqlite => format!(
                "({effective} IS NULL OR EXISTS (SELECT 1 FROM json_each({effective}) WHERE json_each.value IN ({ids})))"
            ),
        });
    }
    if caller.kind == PrincipalKind::Agent {
        parts.push(if write {
            format!("{} = 'followRole'", audience.agents)
        } else {
            format!("{} <> 'none'", audience.agents)
        });
    }
    let text = parts.join(" AND ");
    Ok(vec![match &audience.probe {
        Some(probe) => plan::fill(probe, &[&format!(" WHERE {text}")])?,
        None => text,
    }])
}

impl fmt::Debug for Session<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Session")
            .field("user", &self.user)
            .field("keys", &self.keys.is_some())
            .field("caller", &self.caller)
            .field("access", &self.access)
            .finish()
    }
}

impl Session<'_> {
    fn traced(&self, sql: &str) {
        if let Some(trace) = self.trace {
            trace(sql);
        }
    }

    fn now(&self) -> String {
        rfc3339_millis(self.now.unwrap_or_else(SystemTime::now))
    }

    fn caller(&self) -> Caller {
        self.caller.clone().unwrap_or_else(|| Caller {
            subject: self.user.clone().unwrap_or_default(),
            ..Caller::default()
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

/// A timestamp's RFC 3339 text as epoch milliseconds, the form the
/// behaviors' core holds one in (D103), a fraction past the millisecond
/// truncated; none for text that is no timestamp.
pub fn epoch_millis(text: &str) -> Option<i64> {
    let b = text.as_bytes();
    if b.len() < 20
        || b[4] != b'-'
        || b[7] != b'-'
        || !matches!(b[10], b'T' | b't' | b' ')
        || b[13] != b':'
        || b[16] != b':'
    {
        return None;
    }
    let number = |range: std::ops::Range<usize>| -> Option<i64> {
        let digits = text.get(range)?;
        if digits.is_empty() || !digits.bytes().all(|c| c.is_ascii_digit()) {
            return None;
        }
        digits.parse().ok()
    };
    let (year, month, day) = (number(0..4)?, number(5..7)?, number(8..10)?);
    let (hour, minute, second) = (number(11..13)?, number(14..16)?, number(17..19)?);
    if !(1..=12).contains(&month)
        || !(1..=31).contains(&day)
        || hour > 23
        || minute > 59
        || second > 60
    {
        return None;
    }
    let mut rest = text.get(19..)?;
    let mut millis = 0;
    if let Some(after) = rest.strip_prefix('.') {
        let digits = after.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 {
            return None;
        }
        let mut fraction: String = after[..digits].chars().take(3).collect();
        while fraction.len() < 3 {
            fraction.push('0');
        }
        millis = fraction.parse().ok()?;
        rest = &after[digits..];
    }
    let offset = match rest.as_bytes() {
        [b'Z' | b'z'] => 0,
        [sign @ (b'+' | b'-'), h1, h2, b':', m1, m2] | [sign @ (b'+' | b'-'), h1, h2, m1, m2] => {
            let digits = [*h1, *h2, *m1, *m2];
            if !digits.iter().all(u8::is_ascii_digit) {
                return None;
            }
            let value = |pair: &[u8]| i64::from(pair[0] - b'0') * 10 + i64::from(pair[1] - b'0');
            let minutes = value(&digits[..2]) * 60 + value(&digits[2..]);
            if *sign == b'+' {
                minutes * 60
            } else {
                -minutes * 60
            }
        }
        _ => return None,
    };
    let seconds =
        crate::civil::to_days(year, month, day) * 86_400 + hour * 3600 + minute * 60 + second
            - offset;
    Some(seconds * 1000 + millis)
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

    // D200: an agent run reaches the core as its kind, whom it acts for,
    // its Agent from its agent claim, and its authenticator's assurance; a
    // user's kind and an agent claim on a user are left out.
    #[test]
    fn a_caller_carries_its_kind_whom_it_acts_for_its_agent_and_its_assurance() {
        let mut run = Principal::new("run-7", ["changes.approve", "changes.approve"])
            .of_kind(crate::auth::PrincipalKind::Agent)
            .acting_for(ActingFor::user("ann"))
            .with_assurance(crate::auth::Assurance {
                signed_in_at: Some(1_000),
                user_verified: true,
            });
        run.claims
            .insert("agent".to_owned(), Value::from("reviewer"));
        let caller = Caller::of(Some(&run));
        assert_eq!(caller.permissions, ["changes.approve"]);
        assert_eq!(caller.kind.as_deref(), Some("agent"));
        assert_eq!(caller.acting_for.as_deref(), Some("ann"));
        assert_eq!(caller.acting_for_kind, None);
        assert_eq!(caller.agent.as_deref(), Some("reviewer"));
        assert_eq!(
            caller.assurance,
            Some(crate::auth::Assurance {
                signed_in_at: Some(1_000),
                user_verified: true
            })
        );
        let mut user = Principal::new("kim", Vec::<String>::new());
        user.claims.insert("agent".to_owned(), Value::from("x"));
        let caller = Caller::of(Some(&user));
        assert_eq!(
            (caller.kind, caller.agent, caller.acting_for),
            (None, None, None)
        );
    }

    #[test]
    fn an_audience_admits_a_caller_by_its_id_and_an_agent_run_by_whom_it_acts_for() {
        let user = "11111111-1111-4111-8111-111111111111";
        let owner = "22222222-2222-4222-8222-222222222222";
        let principal = Principal::new(user, Vec::<String>::new());
        assert_eq!(AccessCaller::of(Some(&principal)).audience_ids(), [user]);
        // A subject that is no UUID, and no caller, admit by none.
        let service = Principal::new("service:billing", Vec::<String>::new());
        assert!(AccessCaller::of(Some(&service)).audience_ids().is_empty());
        assert!(AccessCaller::of(None).audience_ids().is_empty());
        // An agent run by its own id and whom it acts for; any other kind
        // by its own alone.
        let run = principal
            .clone()
            .of_kind(PrincipalKind::Agent)
            .acting_for(ActingFor::user(owner));
        assert_eq!(AccessCaller::of(Some(&run)).audience_ids(), [user, owner]);
        let party = principal
            .of_kind(PrincipalKind::Party)
            .acting_for(ActingFor::user(owner));
        assert_eq!(AccessCaller::of(Some(&party)).audience_ids(), [user]);
    }

    #[test]
    fn a_behavior_operation_writes_unless_the_definition_names_it_a_read() {
        // A caller who sees a row runs a read; every other operation, and
        // every one of a definition that names none, needs write reach
        // (D156).
        let behaviors: Behaviors =
            serde_json::from_str(r#"{"owned": [], "reads": {"comments": ["listMessages"]}}"#)
                .unwrap();
        assert!(!behaviors.writes("comments", "listMessages"));
        assert!(behaviors.writes("comments", "post"));
        assert!(behaviors.writes("status", "listMessages"));
        let before: Behaviors = serde_json::from_str(r#"{"owned": []}"#).unwrap();
        assert!(before.writes("comments", "listMessages"));
    }

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
    fn a_timestamp_is_the_cores_epoch_milliseconds() {
        assert_eq!(
            epoch_millis("2026-10-14T12:00:00.125Z"),
            Some(1_791_979_200_125)
        );
        assert_eq!(
            epoch_millis("2026-10-14T14:00:00+02:00"),
            Some(1_791_979_200_000)
        );
        assert_eq!(epoch_millis("1969-12-31T23:59:59.999Z"), Some(-1));
        assert_eq!(epoch_millis("yesterday"), None);
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
