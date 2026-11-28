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

use std::future::Future;
use std::marker::PhantomData;
use std::pin::pin;
use std::sync::Arc;
use std::task::{Context, Poll, Waker};

use rusqlite::types::{Value as SqlValue, ValueRef};
use rusqlite::{params_from_iter, Connection, ErrorCode};
use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::Value;

pub use super::sqlite_file::{connect, Database, DatabaseOptions, HeldWrite};

// The SQLite client the repositories run on, for a types crate's `orm`
// module and its callers to name without a version of their own.
pub use rusqlite;

use super::plan::{Plan, Statement};
use super::routes::{self, Routes, TableOperation, TableOutcome, TableRequest};
use super::run::{finish, Client, Runner};
use super::{
    row, values, wire, AccessCaller, Bound, Caller, Column, Constraint, ConstraintKind, CoreCreate,
    CoreDelete, CoreOperation, CoreTableOperation, CoreUpdate, CreateOptions, Definition, Dialect,
    Error, FieldKeys, FindOptions, Found, Marks, PageOptions, RowPage, Session, Table,
    UpsertManyOptions, UpsertOptions, Upserted, Values, WriteOptions,
};
use crate::auth::Principal;
use crate::error::{chain, ApiError};
use crate::queue::sqlite::{column_value, member_value, utc_timestamp};
use crate::queue::QueueField;
use crate::uuid62::{parse_uuid, uuid_to_base62};

/// What runs a table's behaviors on a SQLite connection (D103): the
/// behaviors' core, through its adapter's `sqlite` module, which a
/// generated types crate binds (its `orm::sqlite::Rules`). Each runs in
/// the caller's transaction, writes the row with the columns the command
/// carries ([`core_columns`]), and answers the core's refusal as
/// [`Error::Refused`].
pub trait Rules: Send + Sync {
    /// Creates a row, and answers its key in its wire form.
    fn create(&self, connection: &Connection, create: CoreCreate<'_>) -> Result<String, Error>;
    /// Updates the live row a command names.
    fn update(&self, connection: &Connection, update: CoreUpdate<'_>) -> Result<(), Error>;
    /// Deletes the live row a command names, or stamps it deleted.
    fn delete(&self, connection: &Connection, delete: CoreDelete<'_>) -> Result<(), Error>;
    /// Runs an operation of one of the table's behaviors on the live row
    /// a command names, and answers its result.
    fn operation(
        &self,
        connection: &Connection,
        operation: CoreOperation<'_>,
    ) -> Result<Value, Error>;
    /// Runs an operation of one of the table's behaviors on the table as a
    /// whole (D104), and answers its result. Rules written before they
    /// could run one refuse it.
    fn table_operation(
        &self,
        connection: &Connection,
        operation: CoreTableOperation<'_>,
    ) -> Result<Value, Error> {
        let _ = connection;
        Err(super::no_table_operations(&operation))
    }
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

    /// The repositories acting as the database's system actor (D87),
    /// which reads every row (D156).
    #[must_use]
    pub fn as_system(mut self) -> Self {
        self.session.user = Some(self.definition.system_actor_id().to_owned());
        self.session.access = None;
        self
    }

    /// The repositories keeping every read and write to what `caller` sees
    /// (D156): an `@audience` table's rows whose audience, a JSON array a
    /// trigger keeps, holds the caller (D182, #611), and for a party or an
    /// agent run the rows its grants reach.
    #[must_use]
    pub fn with_access(mut self, caller: AccessCaller) -> Self {
        self.session.access = Some(caller);
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

    /// The repositories running each behavior's command as `caller`, whose
    /// permissions the core's guards read (D103); the acting user, holding
    /// none, unless set.
    #[must_use]
    pub fn with_caller(mut self, caller: Caller) -> Self {
        self.session.caller = Some(caller);
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

    /// The definition of the database the repositories serve.
    pub fn definition(&self) -> &'a Definition {
        self.definition
    }

    /// The references of the database's rows (D154, #611), as the
    /// repositories read: an [`Error::Invalid`] for a database that keeps
    /// none.
    pub fn references(&self) -> Result<super::rowfeatures::sqlite::References<'_, 'a>, Error> {
        super::rowfeatures::sqlite::References::new(self)
    }

    /// The files of the database's rows (D154, #611), as the repositories
    /// read: an [`Error::Invalid`] for a database that holds none.
    pub fn attachments(&self) -> Result<super::rowfeatures::sqlite::Attachments<'_, 'a>, Error> {
        super::rowfeatures::sqlite::Attachments::new(self)
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

impl Repositories<'_> {
    /// Runs a table operation (D106) on its table's repository, for the
    /// route's caller, and answers the row's view or a page of views. The
    /// repositories act as whom the caller of this names: a route's
    /// caller, by [`routes::acting`].
    pub fn table_operation(
        &self,
        operation: &TableOperation,
        principal: Option<&Principal>,
        request: &TableRequest,
    ) -> Result<TableOutcome, ApiError> {
        let repository = self.table(&operation.table)?;
        let routes = Routes {
            runner: repository.runner(),
            operation,
            principal,
        };
        let mut work = pin!(routes.serve(request));
        match work.as_mut().poll(&mut Context::from_waker(Waker::noop())) {
            Poll::Ready(out) => out,
            Poll::Pending => Err(ApiError::internal(
                "the SQLite repository waited, which it never does",
            )),
        }
    }
}

/// What a generated router serves its table operations from on a SQLite
/// file (D106, D116): the file, the database's definition, and what its
/// repositories seal with and run behaviors through.
#[derive(Clone)]
pub struct Tables {
    database: Arc<Database>,
    definition: fn() -> Result<&'static Definition, Error>,
    keys: Option<Arc<dyn FieldKeys>>,
    rules: Option<Arc<dyn Rules>>,
}

impl Tables {
    /// The tables in the file `database` holds, of the definition
    /// `definition` answers: a types crate's `orm::sqlite_definition`.
    pub fn new(
        database: Arc<Database>,
        definition: fn() -> Result<&'static Definition, Error>,
    ) -> Self {
        Self {
            database,
            definition,
            keys: None,
            rules: None,
        }
    }

    /// The tables with the keys of the database's `Encrypted<T>` fields.
    #[must_use]
    pub fn with_keys(mut self, keys: Arc<dyn FieldKeys>) -> Self {
        self.keys = Some(keys);
        self
    }

    /// The tables with what runs the behaviors of the tables that compose
    /// them.
    #[must_use]
    pub fn with_rules(mut self, rules: Arc<dyn Rules>) -> Self {
        self.rules = Some(rules);
        self
    }

    /// The file the tables are in.
    pub fn database(&self) -> &Arc<Database> {
        &self.database
    }

    /// The keys of the database's `Encrypted<T>` fields, when given.
    pub fn keys(&self) -> Option<&Arc<dyn FieldKeys>> {
        self.keys.as_ref()
    }

    /// What runs the behaviors of the tables that compose them, when
    /// given.
    pub fn rules(&self) -> Option<&Arc<dyn Rules>> {
        self.rules.as_ref()
    }

    /// The definition of the database's tables.
    pub fn definition(&self) -> Result<&'static Definition, Error> {
        (self.definition)()
    }

    /// Runs `operation` for `principal` in one transaction of the file: a
    /// write's on the writing connection, which commits when the operation
    /// answers and rolls back when it refuses, and a read's on a reading
    /// one, but a read that may reveal an `Encrypted<T>` value, whose
    /// access-log row it writes (D156). On tokio the work runs on its
    /// blocking pool.
    pub async fn serve(
        &self,
        operation: &'static TableOperation,
        principal: Option<Principal>,
        request: TableRequest,
    ) -> Result<TableOutcome, ApiError> {
        let tables = self.clone();
        let work = move |connection: &Connection| {
            // The route's caller acts, and runs a behavior's command with
            // the permissions it holds, as the TypeScript server's table
            // routes bind it.
            // Every read and write keeps to what the caller sees (D156).
            let mut repositories = Repositories::new(connection, (tables.definition)()?)?
                .acting(routes::acting(principal.as_ref()))
                .with_caller(Caller::of(principal.as_ref()))
                .with_access(AccessCaller::of(principal.as_ref()));
            if let Some(keys) = tables.keys.as_deref() {
                repositories = repositories.with_keys(keys);
            }
            if let Some(rules) = tables.rules.as_deref() {
                repositories = repositories.with_rules(rules);
            }
            repositories.table_operation(operation, principal.as_ref(), &request)
        };
        // A read that may reveal a value writes its access-log row (D156),
        // which a reading connection, opened read-only, cannot.
        if operation.writes() || !operation.reveal.is_empty() {
            self.database.write_async(work).await
        } else {
            self.database.read_async(work).await
        }
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

    /// Inserts a row as `create_one` does, with each use's create
    /// parameters, by its member (#729).
    pub fn create_one_with(&self, input: &Create, options: &CreateOptions) -> Result<Row, Error> {
        row(self.repository.create_one_with(&values(input)?, options)?)
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

    /// Inserts each of `inputs`, or, when a row already holds its value of
    /// the unique key `on`, sets on that row each field `fields` names, by
    /// its member or its field, to the value the input would have inserted
    /// (D89); each input's row and what the upsert did with it, in order.
    pub fn upsert_many(
        &self,
        inputs: &[Create],
        on: &[&str],
        fields: &[&str],
        options: UpsertManyOptions,
    ) -> Result<Vec<Upserted<Row>>, Error> {
        let inputs = inputs.iter().map(values).collect::<Result<Vec<_>, _>>()?;
        self.repository
            .upsert_many(&inputs, on, fields, options)?
            .into_iter()
            .map(|upserted| {
                Ok(Upserted {
                    row: row(upserted.row)?,
                    status: upserted.status,
                })
            })
            .collect()
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

    /// Folds the row `from` into the row `into` (D147), as the untyped
    /// repository's `merge_into` does.
    pub fn merge_into(&self, from: &Key, into: &Key) -> Result<super::MergeResult, Error> {
        self.repository.merge_into(&wire(from)?, &wire(into)?)
    }

    /// Runs `operation` of the behavior under `member` on the live row
    /// whose key is `key`, through the behaviors' core (D103), with
    /// `params`, and answers its result as `R`.
    pub fn operation<P, R>(
        &self,
        member: &str,
        operation: &str,
        key: &Key,
        params: &P,
        options: &WriteOptions,
    ) -> Result<R, Error>
    where
        P: Serialize + ?Sized,
        R: DeserializeOwned,
    {
        let result =
            self.repository
                .operation(member, operation, &wire(key)?, &wire(params)?, options)?;
        serde_json::from_value(result)
            .map_err(|err| Error::Definition(format!("the result of {member}.{operation}: {err}")))
    }

    /// Runs `operation` of the behavior under `member` on the table as a
    /// whole, through the behaviors' core (D104), with `params`, and
    /// answers its result as `R`.
    pub fn table_operation<P, R>(
        &self,
        member: &str,
        operation: &str,
        params: &P,
    ) -> Result<R, Error>
    where
        P: Serialize + ?Sized,
        R: DeserializeOwned,
    {
        let result = self
            .repository
            .table_operation(member, operation, &wire(params)?)?;
        serde_json::from_value(result)
            .map_err(|err| Error::Definition(format!("the result of {member}.{operation}: {err}")))
    }

    /// Reseals under the current field key every envelope another key
    /// sealed, `batch` rows at a time, deleted rows among them, and answers
    /// how many rows it resealed (D85).
    pub fn reencrypt(&self, batch: Option<u32>) -> Result<u64, Error> {
        self.repository.reencrypt(batch)
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

    /// How many live rows `filter` finds, read with no row's value, so no
    /// encrypted field is opened (D156).
    pub fn count(&self, filter: Option<&Values>) -> Result<u64, Error> {
        finish(self.runner().count_of(filter))
    }

    /// Inserts a row; a member left unset takes its column's default, or
    /// the schema's.
    pub fn create_one(&self, input: &Values) -> Result<Values, Error> {
        finish(self.runner().create_one(input))
    }

    /// Inserts a row as `create_one` does, with each use's create
    /// parameters, by its member (#729): the behaviors' core checks them
    /// and each use's initializer runs in the create's command.
    pub fn create_one_with(
        &self,
        input: &Values,
        options: &CreateOptions,
    ) -> Result<Values, Error> {
        finish(self.runner().create_one_with(input, options))
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
    /// answers how many. On a table that composes behaviors it reads the
    /// rows and runs the core's update of each, in the key's order, so each
    /// behavior's guards hold every row (D103); a refusal of any is the
    /// call's, and the caller's transaction rolls back the rows before it.
    pub fn update_many(&self, filter: Option<&Values>, update: &Values) -> Result<u64, Error> {
        finish(self.runner().update_many(filter, update))
    }

    /// Inserts `input`, or, when a row already holds its value of the
    /// unique key `on`, applies `update` to that row instead (D79, D89).
    /// On a soft-deletable table a deleted row that holds the key is left
    /// as it is, and the upsert is [`Error::NotFound`]. On a table that
    /// composes behaviors it reads whether a row holds the key, then runs
    /// the core's create of `input` or the core's update of that row
    /// (D103), and an update that sets no field writes nothing (D105).
    pub fn upsert_one(
        &self,
        input: &Values,
        on: &[&str],
        update: &Values,
        options: UpsertOptions,
    ) -> Result<Upserted<Values>, Error> {
        finish(self.runner().upsert_one(input, on, update, options))
    }

    /// Inserts each of `inputs`, or, when a row already holds its value of
    /// the unique key `on`, sets on that row each field `fields` names to
    /// the value the input would have inserted (D89), and answers each
    /// input's row and what the upsert did with it, in order. A batch's
    /// rows are as many statements as the sets of columns they set, since
    /// SQLite's VALUES take no DEFAULT, after one read of the rows that
    /// hold their keys, which says which rows each inserted; the caller's
    /// transaction, `TransactionBehavior::Immediate`, holds them together.
    /// Two inputs of one batch that hold one key are refused, as Postgres
    /// refuses them, and a table that composes behaviors upserts a row at
    /// a time with [`Repository::upsert_one`].
    pub fn upsert_many(
        &self,
        inputs: &[Values],
        on: &[&str],
        fields: &[&str],
        options: UpsertManyOptions,
    ) -> Result<Vec<Upserted<Values>>, Error> {
        finish(self.runner().upsert_many(inputs, on, fields, options))
    }

    /// Deletes the live row whose key is `key`, stamping it on a
    /// soft-deletable table; [`Error::NotFound`] when there is none.
    pub fn delete_one(&self, key: &Value, options: &WriteOptions) -> Result<(), Error> {
        finish(self.runner().delete_one(key, options))
    }

    /// Removes the row whose key is `key`, deleted or not, whatever the
    /// table; [`Error::NotFound`] when there is none. On a table that
    /// composes behaviors a live row is removed by the core's delete, with
    /// its sub-table rows and its queues' messages (D103).
    pub fn hard_delete_one(&self, key: &Value) -> Result<(), Error> {
        finish(self.runner().hard_delete_one(key))
    }

    /// Folds the row `from` into the row `into` (D147), in a savepoint: of
    /// the caller's transaction, or the merge's own when it opened none,
    /// for SQLite's one writer holds the database once the merge writes.
    /// What it does and refuses is the Postgres repository's `merge_into`'s.
    pub fn merge_into(&self, from: &Value, into: &Value) -> Result<super::MergeResult, Error> {
        finish(self.runner().merge_into(from, into))
    }

    /// Runs `operation` of the behavior under `member`, the use's field
    /// or the behavior's own member, on the live row whose key is `key`,
    /// through the behaviors' core (D103): a Workflow's `transition` of
    /// `status`, say, with its parameters in their wire form. It answers
    /// the operation's result; the core's refusal is [`Error::Refused`],
    /// and a key no live row holds [`Error::NotFound`], as is, opened for a
    /// caller (D156), a row the caller may not write, or for an operation
    /// that writes nothing does not see.
    pub fn operation(
        &self,
        member: &str,
        operation: &str,
        key: &Value,
        params: &Value,
        options: &WriteOptions,
    ) -> Result<Value, Error> {
        finish(
            self.runner()
                .operation(member, operation, key, params, options),
        )
    }

    /// Runs `operation` of the behavior under `member` on the table as a
    /// whole, a command without a row, through the behaviors' core (D104):
    /// a Pool's `pick`, say. It answers the operation's result; the core's
    /// refusal is [`Error::Refused`].
    pub fn table_operation(
        &self,
        member: &str,
        operation: &str,
        params: &Value,
    ) -> Result<Value, Error> {
        finish(self.runner().table_operation(member, operation, params))
    }

    /// Reseals under the current field key every envelope of the table's
    /// `Encrypted<T>` columns another key of the keyring sealed, `batch`
    /// rows at a time (the definition's default limit unless set), deleted
    /// rows among them, and answers how many rows it resealed (D85). Run it
    /// after a rotation puts a new key first, before the old one leaves the
    /// secret. A row a write changes between the read and the reseal keeps
    /// the write's envelope.
    pub fn reencrypt(&self, batch: Option<u32>) -> Result<u64, Error> {
        finish(self.runner().reencrypt(batch))
    }

    /// The conditions that keep a read of the table's rows to what the
    /// repositories' caller sees (D156), each value appended to `args` and
    /// written `?n` by its place there, each column qualified by the
    /// table: the Postgres `Repository::conditions` on SQLite, what a
    /// search across tables holds a table's rows to with an `EXISTS` back
    /// to the table (D155, D186, #615).
    pub fn conditions(&self, args: &mut Vec<Bound>) -> Result<Vec<String>, Error> {
        self.plan.read_conditions(self.table, args)
    }
}

/// Runs `sql` on `connection` with `args` bound as SQLite holds each
/// ([`param`]), and answers each row's cells in order: text a string, an
/// integer or a real a number, NULL null. What a read across tables runs
/// beside the repositories (D152, D155, #615): a calendar's occurrences
/// and a search's ranking.
pub fn query_rows(
    connection: &Connection,
    sql: &str,
    args: &[Bound],
) -> Result<Vec<Vec<Value>>, Error> {
    let params = args
        .iter()
        .map(|bound| param(bound).map_err(|err| Error::Invalid(format!("{sql}: {err}"))))
        .collect::<Result<Vec<_>, _>>()?;
    let mut prepared = connection
        .prepare_cached(sql)
        .map_err(|err| failure(sql, &err))?;
    let columns = prepared.column_count();
    let mut rows = prepared
        .query(params_from_iter(params))
        .map_err(|err| failure(sql, &err))?;
    let mut out = Vec::new();
    while let Some(row) = rows.next().map_err(|err| failure(sql, &err))? {
        let mut cells = Vec::with_capacity(columns);
        for i in 0..columns {
            let cell = row.get_ref(i).map_err(|err| failure(sql, &err))?;
            cells.push(
                member(cell, &Column::default())
                    .map_err(|err| Error::Definition(format!("{sql}: {err}")))?,
            );
        }
        out.push(cells);
    }
    Ok(out)
}

/// Runs `sql`, a statement that changes rows, on `connection` with `args`
/// bound as [`query_rows`] binds them, and answers how many it changed.
pub fn execute_rows(connection: &Connection, sql: &str, args: &[Bound]) -> Result<u64, Error> {
    let params = args
        .iter()
        .map(|bound| param(bound).map_err(|err| Error::Invalid(format!("{sql}: {err}"))))
        .collect::<Result<Vec<_>, _>>()?;
    let changed = connection
        .prepare_cached(sql)
        .and_then(|mut prepared| prepared.execute(params_from_iter(params)))
        .map_err(|err| failure(sql, &err))?;
    Ok(changed as u64)
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
                "{table} composes behaviors (D103), and the repositories were opened with nothing to run them: open them through the types crate's orm::sqlite::Repositories, which binds the behaviors' core, or pass with_rules a core of your own"
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

    async fn cells(
        &self,
        table: &Table,
        statement: &Statement,
        columns: &[&Column],
    ) -> Result<Vec<Vec<Value>>, Error> {
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
            let mut cells = Vec::with_capacity(columns.len());
            for (i, column) in columns.iter().enumerate() {
                let cell = row.get_ref(i).map_err(|err| failure(&context(), &err))?;
                cells.push(member(cell, column).map_err(|err| {
                    Error::Definition(format!("{}.{}: {err}", table.name, column.name))
                })?);
            }
            out.push(cells);
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
        self.rules(&create.table.name)?
            .create(self.connection, create)
    }

    async fn core_update(&self, update: CoreUpdate<'_>) -> Result<(), Error> {
        self.rules(&update.table.name)?
            .update(self.connection, update)
    }

    async fn core_delete(&self, delete: CoreDelete<'_>) -> Result<(), Error> {
        self.rules(&delete.table.name)?
            .delete(self.connection, delete)
    }

    async fn core_operation(&self, operation: CoreOperation<'_>) -> Result<Value, Error> {
        self.rules(&operation.table.name)?
            .operation(self.connection, operation)
    }

    async fn core_table_operation(
        &self,
        operation: CoreTableOperation<'_>,
    ) -> Result<Value, Error> {
        self.rules(&operation.table.name)?
            .table_operation(self.connection, operation)
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

/// The columns a command through the behaviors' core writes of its own
/// row (D103), as the core's adapter binds them on SQLite: each value the
/// JSON scalar of what the repository's own statement would bind, a UUID
/// its hyphenated text, a timestamp its text to the millisecond, a boolean
/// 1 or 0, a list and a JSON value their JSON text, and NULL null.
pub fn core_columns(table: &Table, columns: &[(String, Option<Bound>)]) -> Result<Values, Error> {
    let mut out = Values::new();
    for (column, bound) in columns {
        let refused = |err: String| Error::Invalid(format!("{}: {column}: {err}", table.name));
        let value = match bound {
            None => Value::Null,
            Some(bound) => match param(bound).map_err(refused)? {
                SqlValue::Null => Value::Null,
                SqlValue::Integer(n) => Value::from(n),
                SqlValue::Real(x) => serde_json::Number::from_f64(x)
                    .map(Value::Number)
                    .ok_or_else(|| refused(format!("{x} is no number a column holds")))?,
                SqlValue::Text(text) => Value::String(text),
                SqlValue::Blob(_) => {
                    return Err(refused("a blob is no value of a table's column".to_owned()))
                }
            },
        };
        out.insert(column.clone(), value);
    }
    Ok(out)
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

    /// A table with createdAt and updatedAt, its statements as sqlutil
    /// writes them on SQLite.
    const NOTE: &str = r#"{
  "dialect": "sqlite",
  "clauses": {
    "eq": "%s = ?%d", "ne": "%s IS DISTINCT FROM ?%d", "gt": "%s > ?%d", "gte": "%s >= ?%d",
    "lt": "%s < ?%d", "lte": "%s <= ?%d", "ilike": "%s LIKE ?%d ESCAPE '\\'", "in": "%s IN (%s)",
    "noneOf": "(%s IN (%s)) IS NOT TRUE", "isNull": "%s IS NULL", "isNotNull": "%s IS NOT NULL",
    "search": "%s LIKE ?%d ESCAPE '\\'", "exists": "EXISTS (%s)", "notExists": "NOT EXISTS (%s)",
    "set": "%s = ?%d", "setNull": "%s = NULL", "pageKey": "page_key.%s", "placeholder": "?%d",
    "default": "", "or": " OR ", "group": "(%s)", "where": " WHERE ", "and": " AND ",
    "orderBy": " ORDER BY ", "orderTerm": "%s %s", "limit": " LIMIT %d", "offset": " OFFSET %d",
    "changed": "(%s) IS DISTINCT FROM (%s)", "never": "FALSE", "null": "NULL"
  },
  "defaultLimit": 100,
  "tables": [{
    "name": "Note",
    "table": "note",
    "key": "id",
    "columns": [
      {"name": "id", "column": "id", "field": "id", "qualified": "note.id", "key": true, "auto": true, "required": true},
      {"name": "title", "column": "title", "field": "title", "qualified": "note.title", "required": true, "filter": ["eq"]},
      {"name": "createdAt", "column": "created_at", "field": "createdAt", "qualified": "note.created_at", "timestamp": true, "audit": "createdAt", "required": true},
      {"name": "updatedAt", "column": "updated_at", "field": "updatedAt", "qualified": "note.updated_at", "timestamp": true, "audit": "updatedAt", "required": true}
    ],
    "statements": {
      "columns": "\"id\", \"title\", \"created_at\", \"updated_at\"",
      "getOne": "SELECT \"id\", \"title\", \"created_at\", \"updated_at\" FROM note WHERE id = ?1",
      "findMany": "SELECT %s FROM note%s%s%s",
      "count": "SELECT COUNT(*) FROM note%s",
      "insert": "INSERT INTO note (%s) VALUES (%s) RETURNING %s",
      "update": "UPDATE note SET %s WHERE id = ?%d",
      "returning": " RETURNING \"id\", \"title\", \"created_at\", \"updated_at\"",
      "updateMany": "UPDATE note SET %s%s",
      "updatedAt": "updated_at = strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now')",
      "updatedBy": "updated_by = ?%d",
      "delete": "DELETE FROM note WHERE id = ?1"
    }
  }]
}"#;

    /// A row's times are the database's clock's alone: an update stamps
    /// updated_at from SQLite's clock, as the insert's default stamps
    /// created_at, so a server whose clock (the session's) runs an hour
    /// behind the database's stamps no row earlier than its insert.
    #[test]
    fn an_update_stamps_updated_at_from_the_database_clock() {
        let definition = Definition::parse(NOTE).expect("the definition");
        let connection = Connection::open_in_memory().expect("open");
        connection
            .execute_batch(
                "CREATE TABLE note (id INTEGER PRIMARY KEY, title TEXT NOT NULL, \
                 created_at TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL, \
                 updated_at TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL)",
            )
            .expect("create the table");
        let behind = std::time::SystemTime::now() - std::time::Duration::from_secs(60 * 60);
        let orm = Repositories::new(&connection, &definition)
            .expect("the repositories")
            .with_session(Session {
                now: Some(behind),
                ..Session::default()
            });
        let notes = orm.table("Note").expect("the table");
        let input = |title: &str| {
            let mut values = Values::new();
            values.insert("title".to_owned(), Value::from(title));
            values
        };
        let at = |row: &Values, member: &str| row[member].as_str().expect("a time").to_owned();
        let created = notes.create_one(&input("Draft")).expect("create the note");
        assert_eq!(at(&created, "updatedAt"), at(&created, "createdAt"));
        let updated = notes
            .update_one(&created["id"], &input("Final"), &WriteOptions::default())
            .expect("update the note");
        assert!(
            at(&updated, "updatedAt") >= at(&created, "createdAt"),
            "updated at {}, before its insert at {}",
            at(&updated, "updatedAt"),
            at(&created, "createdAt")
        );
        assert_eq!(
            notes
                .update_many(None, &input("Filed"))
                .expect("update the notes"),
            1
        );
        let filed = notes.get_one(&created["id"]).expect("read the note");
        assert!(at(&filed, "updatedAt") >= at(&updated, "updatedAt"));
        assert_eq!(at(&filed, "createdAt"), at(&created, "createdAt"));
    }

    /// A table keyed by text, with the statements a reach reads.
    const MEMO: &str = r#"{
  "dialect": "sqlite",
  "clauses": {
    "eq": "%s = ?%d", "ne": "%s IS DISTINCT FROM ?%d", "gt": "%s > ?%d", "gte": "%s >= ?%d",
    "lt": "%s < ?%d", "lte": "%s <= ?%d", "ilike": "%s LIKE ?%d ESCAPE '\\'", "in": "%s IN (%s)",
    "noneOf": "(%s IN (%s)) IS NOT TRUE", "isNull": "%s IS NULL", "isNotNull": "%s IS NOT NULL",
    "search": "%s LIKE ?%d ESCAPE '\\'", "exists": "EXISTS (%s)", "notExists": "NOT EXISTS (%s)",
    "set": "%s = ?%d", "setNull": "%s = NULL", "pageKey": "page_key.%s", "placeholder": "?%d",
    "default": "", "or": " OR ", "group": "(%s)", "where": " WHERE ", "and": " AND ",
    "orderBy": " ORDER BY ", "orderTerm": "%s %s", "limit": " LIMIT %d", "offset": " OFFSET %d",
    "changed": "(%s) IS DISTINCT FROM (%s)", "never": "FALSE", "null": "NULL"
  },
  "defaultLimit": 100,
  "tables": [{
    "name": "Memo",
    "table": "memo",
    "key": "id",
    "columns": [
      {"name": "id", "column": "id", "field": "id", "qualified": "memo.id", "key": true, "required": true, "filter": ["eq", "in"]},
      {"name": "title", "column": "title", "field": "title", "qualified": "memo.title", "required": true}
    ],
    "statements": {
      "columns": "\"id\", \"title\"",
      "getOne": "SELECT \"id\", \"title\" FROM memo WHERE id = ?1",
      "findOne": "SELECT %s FROM memo%s%s LIMIT 1",
      "findMany": "SELECT %s FROM memo%s%s%s",
      "count": "SELECT COUNT(*) FROM memo%s",
      "insert": "INSERT INTO memo (%s) VALUES (%s) RETURNING %s",
      "update": "UPDATE memo SET %s WHERE id = ?%d",
      "returning": " RETURNING \"id\", \"title\"",
      "updateMany": "UPDATE memo SET %s%s",
      "delete": "DELETE FROM memo WHERE id = ?1"
    }
  }]
}"#;

    /// A batch create by a caller with grants checks the rows it made in
    /// one read of their keys (D156), and is refused as a row at a time
    /// was when one of them is a row no grant reaches.
    #[test]
    fn a_batch_create_reads_the_reach_of_its_rows_at_once_and_refuses_one_unreached() {
        let definition = Definition::parse(MEMO).expect("the definition");
        let connection = Connection::open_in_memory().expect("open");
        connection
            .execute_batch("CREATE TABLE memo (id TEXT PRIMARY KEY, title TEXT NOT NULL)")
            .expect("create the table");
        let grant = |target: &str| crate::auth::GrantReach {
            id: format!("g-{target}"),
            target_type: "Memo".to_owned(),
            target_id: target.to_owned(),
            reaches: Default::default(),
        };
        let statements = std::sync::Mutex::new(Vec::<String>::new());
        let trace = |sql: &str| statements.lock().unwrap().push(sql.to_owned());
        let orm = Repositories::new(&connection, &definition)
            .expect("the repositories")
            .with_session(Session {
                trace: Some(&trace),
                access: Some(AccessCaller {
                    subject: Some("party".to_owned()),
                    grants: Some(vec![grant("m1"), grant("m2"), grant("m3")]),
                    ..AccessCaller::default()
                }),
                ..Session::default()
            });
        let memos = orm.table("Memo").expect("the table");
        let input = |id: &str| {
            let mut values = Values::new();
            values.insert("id".to_owned(), Value::from(id));
            values.insert("title".to_owned(), Value::from(id));
            values
        };
        let reads = |statements: &std::sync::Mutex<Vec<String>>| {
            statements
                .lock()
                .unwrap()
                .drain(..)
                .filter(|sql| sql.starts_with("SELECT"))
                .collect::<Vec<_>>()
        };
        let created = memos
            .create_many(&[input("m1"), input("m2")])
            .expect("the grants reach both");
        assert_eq!(created.len(), 2);
        assert_eq!(
            reads(&statements),
            ["SELECT \"id\", \"title\" FROM memo WHERE memo.id IN (?1, ?2) AND (memo.id = ?3 OR memo.id = ?4 OR memo.id = ?5)"]
        );
        let refused = memos.create_many(&[input("m3"), input("m4")]);
        assert!(
            matches!(&refused, Err(Error::AccessRefused(message)) if message == "Memo: the row is not one the caller's grants reach"),
            "{refused:?}"
        );
        assert_eq!(reads(&statements).len(), 1);
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
