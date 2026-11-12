//! The repositories on Postgres (D116): asynchronous, over a
//! tokio-postgres client its caller holds, the statements the Go ORM and
//! the TypeScript repositories run (D79).
//!
//! The client is a deadpool connection of the server's pool or a
//! transaction on one, whose statements are prepared once a connection. A write
//! that takes more than one statement, an upsert that reads back a row it
//! left as it was and a write of a table that composes behaviors, runs in
//! the caller's transaction, so its statements see one state.
//!
//! A value crosses as the queue client's do (D65): each parameter as text,
//! which Postgres reads by the type it gives the parameter, and each
//! column back by its type, a UUID in base62 and a `NUMERIC` as its text.

use std::future::Future;
use std::marker::PhantomData;
use std::pin::Pin;
use std::sync::Arc;

use deadpool_postgres::GenericClient;
use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::Value;
use tokio_postgres::error::SqlState;
use tokio_postgres::types::{FromSql, ToSql, Type};
use tokio_postgres::Row;

use super::plan::{Plan, Statement};
use super::routes::{self, Routes, TableOperation, TableOutcome, TableRequest};
use super::run::{Client, Runner};
use super::{
    row, values, wire, Column, Constraint, ConstraintKind, CoreCreate, CoreDelete, CoreUpdate,
    Definition, Dialect, Error, FieldKeys, FindOptions, Found, PageOptions, RowPage, Session,
    Table, UpsertOptions, Upserted, Values, WriteOptions,
};
use crate::auth::Principal;
use crate::error::{chain, ApiError};
use crate::queue::codec::{numeric, param_text, Column as Cell, Text};
use crate::uuid62::{parse_uuid, uuid_to_base62};

// The Postgres clients the repositories run on, for a types crate's `orm`
// module and its callers to name without a version of their own.
pub use deadpool_postgres;
pub use tokio_postgres;

/// What a behavior's command answers with, once its work is done.
pub type CoreFuture<'a, T> = Pin<Box<dyn Future<Output = Result<T, Error>> + Send + 'a>>;

/// What runs a table's behaviors on a Postgres client (D103): the
/// behaviors' core, through its adapter's `postgres` module, which a
/// generated types crate binds for the client it runs on. Each runs in
/// the caller's transaction, writes the row with the columns the command
/// carries, and answers the core's refusal as [`Error::Refused`].
pub trait Rules<C>: Send + Sync {
    /// Creates a row, and answers its key in its wire form.
    fn create<'a>(&'a self, client: &'a C, create: CoreCreate<'a>) -> CoreFuture<'a, String>;
    /// Updates the live row a command names.
    fn update<'a>(&'a self, client: &'a C, update: CoreUpdate<'a>) -> CoreFuture<'a, ()>;
    /// Deletes the live row a command names, or stamps it deleted.
    fn delete<'a>(&'a self, client: &'a C, delete: CoreDelete<'a>) -> CoreFuture<'a, ()>;
}

/// The repositories of a database on a Postgres client, acting as a user
/// or as no one.
pub struct Repositories<'a, C> {
    client: &'a C,
    definition: &'a Definition,
    session: Session<'a>,
    rules: Option<&'a dyn Rules<C>>,
}

impl<'a, C: GenericClient + Sync> Repositories<'a, C> {
    /// The repositories of `definition`'s tables over `client`, the
    /// caller's: a connection of its pool, or a transaction on one. They
    /// act as no one until [`Repositories::acting`] names a user.
    pub fn new(client: &'a C, definition: &'a Definition) -> Result<Self, Error> {
        if definition.dialect != Dialect::Postgres {
            return Err(Error::Definition(format!(
                "the ORM's definition is {}'s, and the client is Postgres's",
                definition.dialect
            )));
        }
        Ok(Self {
            client,
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
    pub fn with_rules(mut self, rules: &'a dyn Rules<C>) -> Self {
        self.rules = Some(rules);
        self
    }

    /// The client the repositories run on.
    pub fn client(&self) -> &'a C {
        self.client
    }

    /// The session the repositories act in.
    pub fn session(&self) -> &Session<'a> {
        &self.session
    }

    /// The repository of the table type `name`.
    pub fn table(&self, name: &str) -> Result<Repository<'_, C>, Error> {
        let table = self.definition.table(name)?;
        Ok(Repository {
            client: PostgresClient {
                client: self.client,
                rules: self.rules,
            },
            plan: Plan::new(self.definition, table, &self.session)?,
            table,
        })
    }

    /// The repository of the table type `name`, its rows, inputs, filters
    /// and keys as their Rust types: what a types crate's `orm` module
    /// names each table's repository as.
    pub fn typed<Row, Create, Update, Filter, Key>(
        &self,
        name: &str,
    ) -> Result<Typed<'_, C, Row, Create, Update, Filter, Key>, Error> {
        Ok(Typed {
            repository: self.table(name)?,
            types: PhantomData,
        })
    }
}

impl<C: GenericClient + Sync> Repositories<'_, C> {
    /// Runs a table operation (D106) on its table's repository, for the
    /// route's caller, and answers the row's view or a page of views. The
    /// repositories act as whom the caller of this names: a route's
    /// caller, by [`routes::acting`].
    pub async fn table_operation(
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
        routes.serve(request).await
    }
}

/// What runs a table's behaviors in a transaction of a server's pool.
pub type PoolRules = dyn for<'t> Rules<deadpool_postgres::Transaction<'t>>;

/// What a generated router serves its table operations from on Postgres
/// (D106, D116): the server's pool, the database's definition, and what
/// its repositories seal with and run behaviors through.
#[derive(Clone)]
pub struct Tables {
    pool: deadpool_postgres::Pool,
    definition: fn() -> Result<&'static Definition, Error>,
    keys: Option<Arc<dyn FieldKeys>>,
    rules: Option<Arc<PoolRules>>,
}

impl Tables {
    /// The tables in the database `pool` reaches, of the definition
    /// `definition` answers: a types crate's `orm::postgres_definition`.
    pub fn new(
        pool: deadpool_postgres::Pool,
        definition: fn() -> Result<&'static Definition, Error>,
    ) -> Self {
        Self {
            pool,
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
    pub fn with_rules(mut self, rules: Arc<PoolRules>) -> Self {
        self.rules = Some(rules);
        self
    }

    /// The pool the tables are reached through.
    pub fn pool(&self) -> &deadpool_postgres::Pool {
        &self.pool
    }

    /// Runs `operation` for `principal` in one transaction of the pool,
    /// which commits when the operation answers and rolls back when it
    /// refuses.
    pub async fn serve(
        &self,
        operation: &TableOperation,
        principal: Option<Principal>,
        request: TableRequest,
    ) -> Result<TableOutcome, ApiError> {
        let unreachable = |err: &dyn std::error::Error| {
            ApiError::service_unavailable(format!("the database is unreachable: {err}"))
        };
        let mut client = self.pool.get().await.map_err(|err| unreachable(&err))?;
        let transaction = client
            .transaction()
            .await
            .map_err(|err| unreachable(&err))?;
        let outcome = {
            let mut repositories = Repositories::new(&transaction, (self.definition)()?)?
                .acting(routes::acting(principal.as_ref()));
            if let Some(keys) = self.keys.as_deref() {
                repositories = repositories.with_keys(keys);
            }
            if let Some(rules) = self.rules.as_deref() {
                repositories = repositories.with_rules(rules);
            }
            repositories
                .table_operation(operation, principal.as_ref(), &request)
                .await?
        };
        transaction
            .commit()
            .await
            .map_err(|err| ApiError::internal(format!("commit: {}", chain(&err))))?;
        Ok(outcome)
    }
}

/// The repository of one table on a Postgres client.
pub struct Repository<'a, C> {
    client: PostgresClient<'a, C>,
    plan: Plan<'a>,
    table: &'a Table,
}

impl<C: GenericClient + Sync> Repository<'_, C> {
    fn runner(&self) -> Runner<'_, PostgresClient<'_, C>> {
        Runner {
            plan: self.plan.clone(),
            table: self.table,
            client: &self.client,
        }
    }

    /// The live row whose key is `key`; [`Error::NotFound`] when there is
    /// none.
    pub async fn get_one(&self, key: &Value) -> Result<Values, Error> {
        self.runner().get_one(key).await
    }

    /// The first row `filter` finds, in the options' order, or none.
    pub async fn find_one(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Option<Values>, Error> {
        self.runner().find_one(filter, options).await
    }

    /// The rows `filter` finds, in the options' order, a limit of them at
    /// a time, and how many it finds in all, unless the options skip the
    /// count.
    pub async fn find_many(
        &self,
        filter: Option<&Values>,
        options: &FindOptions,
    ) -> Result<Found<Values>, Error> {
        self.runner().find_many(filter, options).await
    }

    /// A page of the rows `filter` finds, in the sort's order, after the
    /// key of the page before (D60, amended).
    pub async fn find_page(
        &self,
        filter: Option<&Values>,
        options: &PageOptions,
    ) -> Result<RowPage<Values>, Error> {
        self.runner().find_page(filter, options).await
    }

    /// Inserts a row; a member left unset takes its column's default, or
    /// the schema's.
    pub async fn create_one(&self, input: &Values) -> Result<Values, Error> {
        self.runner().create_one(input).await
    }

    /// Inserts the rows in one statement, DEFAULT for each column a row
    /// leaves unset that another sets.
    pub async fn create_many(&self, inputs: &[Values]) -> Result<Vec<Values>, Error> {
        self.runner().create_many(inputs).await
    }

    /// Sets the members `update` sets, null setting a column NULL, on the
    /// live row whose key is `key`; [`Error::NotFound`] when there is none,
    /// and [`Error::VersionConflict`] when the options fence the write on
    /// a version the row has moved from.
    pub async fn update_one(
        &self,
        key: &Value,
        update: &Values,
        options: &WriteOptions,
    ) -> Result<Values, Error> {
        self.runner().update_one(key, update, options).await
    }

    /// Sets the members `update` sets on the live rows `filter` finds, and
    /// answers how many.
    pub async fn update_many(
        &self,
        filter: Option<&Values>,
        update: &Values,
    ) -> Result<u64, Error> {
        self.runner().update_many(filter, update).await
    }

    /// Inserts `input`, or, when a row already holds its value of the
    /// unique key `on`, applies `update` to that row instead (D79, D89).
    /// On a soft-deletable table a deleted row that holds the key is left
    /// as it is, and the upsert is [`Error::NotFound`].
    pub async fn upsert_one(
        &self,
        input: &Values,
        on: &[&str],
        update: &Values,
        options: UpsertOptions,
    ) -> Result<Upserted<Values>, Error> {
        self.runner().upsert_one(input, on, update, options).await
    }

    /// Deletes the live row whose key is `key`, stamping it on a
    /// soft-deletable table; [`Error::NotFound`] when there is none.
    pub async fn delete_one(&self, key: &Value, options: &WriteOptions) -> Result<(), Error> {
        self.runner().delete_one(key, options).await
    }

    /// Removes the row whose key is `key`, deleted or not, whatever the
    /// table; [`Error::NotFound`] when there is none.
    pub async fn hard_delete_one(&self, key: &Value) -> Result<(), Error> {
        self.runner().hard_delete_one(key).await
    }
}

/// The Rust types a typed repository reads and writes, which it holds
/// none of.
type Types<Row, Create, Update, Filter, Key> =
    PhantomData<fn() -> (Row, Create, Update, Filter, Key)>;

/// The repository of one table on a Postgres client, over its Rust types:
/// `Row` a row, `Create` what a create sets, `Update` what an update sets,
/// `Filter` the rows a read or a write finds, and `Key` the primary key.
pub struct Typed<'a, C, Row, Create, Update, Filter, Key> {
    repository: Repository<'a, C>,
    types: Types<Row, Create, Update, Filter, Key>,
}

impl<'a, C, Row, Create, Update, Filter, Key> Typed<'a, C, Row, Create, Update, Filter, Key>
where
    C: GenericClient + Sync,
    Row: DeserializeOwned,
    Create: Serialize + Sync,
    Update: Serialize + Sync,
    Filter: Serialize + Sync,
    Key: Serialize + Sync,
{
    /// The repository over JSON objects, which this one reads and writes
    /// through.
    pub fn untyped(&self) -> &Repository<'a, C> {
        &self.repository
    }

    fn filter(filter: Option<&Filter>) -> Result<Option<Values>, Error> {
        filter.map(values).transpose()
    }

    /// The live row whose key is `key`; [`Error::NotFound`] when there is
    /// none.
    pub async fn get_one(&self, key: &Key) -> Result<Row, Error> {
        row(self.repository.get_one(&wire(key)?).await?)
    }

    /// The first row `filter` finds, in the options' order, or none.
    pub async fn find_one(
        &self,
        filter: Option<&Filter>,
        options: &FindOptions,
    ) -> Result<Option<Row>, Error> {
        let filter = Self::filter(filter)?;
        self.repository
            .find_one(filter.as_ref(), options)
            .await?
            .map(row)
            .transpose()
    }

    /// The rows `filter` finds, in the options' order, a limit of them at
    /// a time, and how many it finds in all, unless the options skip the
    /// count.
    pub async fn find_many(
        &self,
        filter: Option<&Filter>,
        options: &FindOptions,
    ) -> Result<Found<Row>, Error> {
        let filter = Self::filter(filter)?;
        let found = self.repository.find_many(filter.as_ref(), options).await?;
        Ok(Found {
            rows: found.rows.into_iter().map(row).collect::<Result<_, _>>()?,
            total: found.total,
        })
    }

    /// A page of the rows `filter` finds, in the sort's order, after the
    /// key of the page before (D60, amended).
    pub async fn find_page(
        &self,
        filter: Option<&Filter>,
        options: &PageOptions,
    ) -> Result<RowPage<Row>, Error> {
        let filter = Self::filter(filter)?;
        let page = self.repository.find_page(filter.as_ref(), options).await?;
        Ok(RowPage {
            rows: page.rows.into_iter().map(row).collect::<Result<_, _>>()?,
            next: page.next,
        })
    }

    /// Inserts a row; a member left unset takes its column's default, or
    /// the schema's.
    pub async fn create_one(&self, input: &Create) -> Result<Row, Error> {
        row(self.repository.create_one(&values(input)?).await?)
    }

    /// Inserts the rows in one statement.
    pub async fn create_many(&self, inputs: &[Create]) -> Result<Vec<Row>, Error> {
        let inputs = inputs.iter().map(values).collect::<Result<Vec<_>, _>>()?;
        self.repository
            .create_many(&inputs)
            .await?
            .into_iter()
            .map(row)
            .collect()
    }

    /// Sets the members `update` sets on the live row whose key is `key`.
    pub async fn update_one(
        &self,
        key: &Key,
        update: &Update,
        options: &WriteOptions,
    ) -> Result<Row, Error> {
        row(self
            .repository
            .update_one(&wire(key)?, &values(update)?, options)
            .await?)
    }

    /// Sets the members `update` sets on the live rows `filter` finds, and
    /// answers how many.
    pub async fn update_many(
        &self,
        filter: Option<&Filter>,
        update: &Update,
    ) -> Result<u64, Error> {
        let filter = Self::filter(filter)?;
        self.repository
            .update_many(filter.as_ref(), &values(update)?)
            .await
    }

    /// Inserts `input`, or, when a row already holds its value of the
    /// unique key `on`, applies `update` to that row instead.
    pub async fn upsert_one(
        &self,
        input: &Create,
        on: &[&str],
        update: &Update,
        options: UpsertOptions,
    ) -> Result<Upserted<Row>, Error> {
        let upserted = self
            .repository
            .upsert_one(&values(input)?, on, &values(update)?, options)
            .await?;
        Ok(Upserted {
            row: row(upserted.row)?,
            status: upserted.status,
        })
    }

    /// Deletes the live row whose key is `key`, stamping it on a
    /// soft-deletable table.
    pub async fn delete_one(&self, key: &Key, options: &WriteOptions) -> Result<(), Error> {
        self.repository.delete_one(&wire(key)?, options).await
    }

    /// Removes the row whose key is `key`, deleted or not.
    pub async fn hard_delete_one(&self, key: &Key) -> Result<(), Error> {
        self.repository.hard_delete_one(&wire(key)?).await
    }
}

/// A Postgres client as a repository's runner drives it.
struct PostgresClient<'a, C> {
    client: &'a C,
    rules: Option<&'a dyn Rules<C>>,
}

impl<C: GenericClient + Sync> PostgresClient<'_, C> {
    fn rules(&self, table: &str) -> Result<&dyn Rules<C>, Error> {
        self.rules.ok_or_else(|| {
            Error::Definition(format!(
                "{table} composes behaviors (D103), and the repositories were opened with nothing to run them: pass the types crate's rules"
            ))
        })
    }

    /// Prepares the statement, once a connection, binds each value as the
    /// text of the type Postgres gave its parameter, and runs `run`.
    async fn bound(&self, table: &Table, statement: &Statement) -> Result<Vec<Row>, Error> {
        let context = || format!("{}: {}", table.name, statement.sql);
        let prepared = self
            .client
            .prepare_cached(&statement.sql)
            .await
            .map_err(|err| failure(&context(), &err))?;
        let params = params(table, statement, prepared.params())?;
        let refs: Vec<&(dyn ToSql + Sync)> =
            params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
        self.client
            .query(&prepared, &refs)
            .await
            .map_err(|err| failure(&context(), &err))
    }
}

/// Each value a statement binds, as the text of the type Postgres gave its
/// parameter.
fn params(table: &Table, statement: &Statement, types: &[Type]) -> Result<Vec<Text>, Error> {
    if types.len() != statement.args.len() {
        return Err(Error::Definition(format!(
            "{}: the statement takes {} values, and the repository binds {}: {}",
            table.name,
            types.len(),
            statement.args.len(),
            statement.sql
        )));
    }
    statement
        .args
        .iter()
        .zip(types)
        .map(|(bound, ty)| {
            param_text(&bound.value, ty, bound.marks.json)
                .map(Text)
                .map_err(|err| Error::Invalid(format!("{}: {err}", table.name)))
        })
        .collect()
}

impl<C: GenericClient + Sync> Client for PostgresClient<'_, C> {
    async fn rows(
        &self,
        table: &Table,
        statement: &Statement,
        extra: usize,
    ) -> Result<Vec<(Values, Vec<Value>)>, Error> {
        let rows = self.bound(table, statement).await?;
        let mut out = Vec::with_capacity(rows.len());
        for row in &rows {
            let mut members = Values::new();
            for (i, column) in table.columns.iter().enumerate() {
                let value = cell(row, i, column).map_err(|err| {
                    Error::Definition(format!("{}.{}: {err}", table.name, column.name))
                })?;
                members.insert(column.name.clone(), value);
            }
            let mut extras = Vec::with_capacity(extra);
            for i in table.columns.len()..table.columns.len() + extra {
                extras.push(
                    cell(row, i, &Column::default())
                        .map_err(|err| Error::Definition(format!("{}: {err}", table.name)))?,
                );
            }
            out.push((members, extras));
        }
        Ok(out)
    }

    async fn execute(&self, table: &Table, statement: &Statement) -> Result<u64, Error> {
        let context = || format!("{}: {}", table.name, statement.sql);
        let prepared = self
            .client
            .prepare_cached(&statement.sql)
            .await
            .map_err(|err| failure(&context(), &err))?;
        let params = params(table, statement, prepared.params())?;
        let refs: Vec<&(dyn ToSql + Sync)> =
            params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
        self.client
            .execute(&prepared, &refs)
            .await
            .map_err(|err| failure(&context(), &err))
    }

    async fn scalar(&self, table: &Table, statement: &Statement) -> Result<Option<i64>, Error> {
        let rows = self.bound(table, statement).await?;
        let Some(row) = rows.first() else {
            return Ok(None);
        };
        let value = cell(row, 0, &Column::default())
            .map_err(|err| Error::Definition(format!("{}: {err}", table.name)))?;
        match value {
            Value::Bool(held) => Ok(Some(i64::from(held))),
            Value::Number(number) => number.as_i64().map(Some).ok_or_else(|| {
                Error::Definition(format!("{}: {number} is no integer", table.name))
            }),
            other => Err(Error::Definition(format!(
                "{}: the statement read {other}, no integer: {}",
                table.name, statement.sql
            ))),
        }
    }

    async fn core_create(&self, create: CoreCreate<'_>) -> Result<String, Error> {
        self.rules(create.table)?.create(self.client, create).await
    }

    async fn core_update(&self, update: CoreUpdate<'_>) -> Result<(), Error> {
        self.rules(update.table)?.update(self.client, update).await
    }

    async fn core_delete(&self, delete: CoreDelete<'_>) -> Result<(), Error> {
        self.rules(delete.table)?.delete(self.client, delete).await
    }
}

/// A `NUMERIC` as its digits' text, which a decimal's row member holds,
/// where the queue client reads it as a number.
struct Decimal(Option<String>);

impl<'a> FromSql<'a> for Decimal {
    fn from_sql(
        _ty: &Type,
        raw: &'a [u8],
    ) -> Result<Self, Box<dyn std::error::Error + Sync + Send>> {
        numeric(raw).map(|text| Decimal(Some(text)))
    }

    fn from_sql_null(_ty: &Type) -> Result<Self, Box<dyn std::error::Error + Sync + Send>> {
        Ok(Decimal(None))
    }

    fn accepts(ty: &Type) -> bool {
        *ty == Type::NUMERIC
    }
}

/// Column `i` of `row` as its row's member: a UUID in base62, a decimal
/// as its text, and the rest as a queue's message reads a column.
fn cell(row: &Row, i: usize, column: &Column) -> Result<Value, String> {
    let ty = row
        .columns()
        .get(i)
        .map(tokio_postgres::Column::type_)
        .ok_or_else(|| format!("the statement returned no column {i}"))?;
    if *ty == Type::NUMERIC {
        let Decimal(text) = row.try_get(i).map_err(|err| chain(&err))?;
        return Ok(text.map_or(Value::Null, Value::String));
    }
    let value = row
        .try_get::<_, Option<Cell>>(i)
        .map_err(|err| chain(&err))?
        .map_or(Value::Null, |Cell(value)| value);
    let base62 = |text: String| {
        parse_uuid(&text)
            .map(|uuid| Value::String(uuid_to_base62(uuid)))
            .ok_or_else(|| format!("the column holds {text:?}, no UUID"))
    };
    match value {
        Value::String(text) if *ty == Type::UUID => base62(text),
        Value::Array(elements) if *ty == Type::UUID_ARRAY && !column.json => elements
            .into_iter()
            .map(|element| match element {
                Value::String(text) => base62(text),
                other => Ok(other),
            })
            .collect::<Result<Vec<_>, _>>()
            .map(Value::Array),
        other => Ok(other),
    }
}

/// A tokio-postgres error as a repository's: the constraint that refused
/// a row, or the database's failure.
pub fn failure(context: &str, err: &tokio_postgres::Error) -> Error {
    if let Some(refused) = err.as_db_error() {
        let kind = match *refused.code() {
            SqlState::UNIQUE_VIOLATION => Some(ConstraintKind::Unique),
            SqlState::FOREIGN_KEY_VIOLATION => Some(ConstraintKind::ForeignKey),
            SqlState::NOT_NULL_VIOLATION => Some(ConstraintKind::NotNull),
            SqlState::CHECK_VIOLATION => Some(ConstraintKind::Check),
            _ => None,
        };
        if let Some(kind) = kind {
            let columns = match (kind, refused.column()) {
                (ConstraintKind::NotNull, Some(column)) => vec![column.to_owned()],
                _ => key_columns(refused.detail().unwrap_or_default()),
            };
            return Error::Constraint(Constraint {
                kind,
                columns,
                message: refused.message().to_owned(),
            });
        }
    }
    Error::Database(format!("{context}: {}", chain(err)))
}

/// The columns Postgres's detail of a key names, `Key (account_id,
/// external_id)=(...) already exists.`; none for a detail that names none,
/// or names an expression.
fn key_columns(detail: &str) -> Vec<String> {
    let Some(list) = detail
        .strip_prefix("Key (")
        .and_then(|rest| rest.split_once(")=("))
        .map(|(list, _)| list)
    else {
        return Vec::new();
    };
    let columns: Vec<String> = list
        .split(", ")
        .map(|name| name.trim_matches('"').to_owned())
        .collect();
    let plain = |name: &String| {
        !name.is_empty() && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
    };
    if columns.iter().all(plain) {
        columns
    } else {
        Vec::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A server on tokio awaits a repository on any of its threads.
    #[test]
    fn a_repository_future_is_send() {
        fn send<T: Send>(_: T) {}
        fn check(repository: &Repository<'_, deadpool_postgres::Transaction<'_>>, key: &Value) {
            let update = Values::new();
            send(repository.get_one(key));
            send(repository.find_page(None, &PageOptions::default()));
            send(repository.update_one(key, &update, &WriteOptions::default()));
            send(repository.upsert_one(&update, &["id"], &update, UpsertOptions::default()));
            send(repository.hard_delete_one(key));
        }
        let _ = check;
    }

    #[test]
    fn a_key_violation_names_its_columns() {
        assert_eq!(
            key_columns("Key (account_id, external_id)=(a, b) already exists."),
            vec!["account_id", "external_id"]
        );
        assert_eq!(
            key_columns("Key (account_id)=(a) is not present in table \"account\"."),
            vec!["account_id"]
        );
        assert!(key_columns("Key (lower(code))=(k1) already exists.").is_empty());
        assert!(key_columns("").is_empty());
    }
}
