//! A behavior's storage: columns on the instances table and tables of its
//! own, all named by the engine (a port of
//! `runtime/engine/typescript/src/behaviors/storage.ts`).
//!
//! When a schema that composes a behavior is first published, the engine
//! records a key for it in `engine_behaviors` (its name, lowercased, with
//! each run of other characters made one `_`, and `_2`, `_3`, ... when a
//! key is taken), and every name the behavior owns is
//! `bhv_<key>__<its own name>`. A key has no `__` and does not end in `_`,
//! so one behavior's prefix never starts another's, and the table keeps
//! each behavior's key for the life of the database. A new key is at most
//! 40 bytes, so on Postgres, which cuts a name past 63 bytes
//! ([`crate::postgres_identifier`]), the prefix stays whole.
//!
//! The behavior's migrations run through the engine's ledger under its
//! name: each adds the columns it lists (`ALTER TABLE engine_instances ADD
//! COLUMN`), then the indexes it lists on them, then the tables it declares
//! ([`TableSpec`], named by the behavior and prefixed by the engine), and
//! runs its `up()` with SQL scoped to the behavior ([`BehaviorSql`]).
//! Afterwards every object the database's catalog gained (`sqlite_master`;
//! `pg_class` and `pg_trigger` in the engine's schema) must be a table or
//! index of its own, on a table of its own (or on Postgres a table's
//! sequence); anything else rolls the migration back.
//!
//! The engine keeps the names under `bhv_<key>___`, one `_` more than the
//! prefix, for what it names for the behavior: an index a migration lists
//! on the instances table is `bhv_<key>___index_<name>`, and the relation
//! over the instances a call's SQL reads (`sql.instances()`) is
//! `bhv_<key>___instances`. A name the behavior gives starts with a letter
//! after the prefix, so none of these collides with one of its tables, and
//! a migration may not name one ([`crate::sql_refusal`]). A namespace
//! operation's statements (D77) read `bhv_<key>___readable` instead: the
//! schemas of the call's namespace that compose the behavior and that the
//! call's principal may read ([`ReadableRelation`]), one row each, its
//! column `schema`, inlined as literals the same way.
//!
//! The relation is a common table expression the engine puts ahead of each
//! statement that names it ([`crate::with_relation`]), over
//! `engine_instances` for the call's namespace and schema, with the
//! instance columns of [`RELATION_COLUMNS`] and the behavior's own columns
//! under its own names. Before such a statement runs, the engine asks the
//! access policy for read on the schema as the call's principal
//! ([`InstanceRelation::allow`]). Neither database writes through a common
//! table expression, so an INSERT, UPDATE or DELETE that targets the
//! relation fails. A migration acts for no principal and gets no relation.

use std::collections::HashSet;
use std::sync::Arc;

use crate::checker::{sql_refusal, with_relation, SqlMode};
use crate::db::Tx;
use crate::ddl::{is_column_name, ColumnSpec, TableSpec};
use crate::dialect::{Dialect, Sql};
use crate::error::{Error, Result};
use crate::fulltext::{self, FullTextMatches, FullTextQuery};
use crate::ident::{identifier, quote_identifier, sql_string};
use crate::migrations::{Migration, MigrationSet};
use crate::value::{Row, RunResult, SqlValue};

/// The prefix of every name a behavior owns: `bhv_<key>__<name>`.
pub const STORAGE_PREFIX: &str = "bhv_";

/// The columns of the relation `sql.instances()` names, before the
/// behavior's own.
pub const RELATION_COLUMNS: &[&str] = &[
    "id",
    "seq",
    "version",
    "created_at",
    "created_by",
    "updated_at",
    "updated_by",
    "data",
];

/// The longest key the engine assigns a behavior, in bytes.
pub const MAX_KEY: usize = 40;

/// Whether `name` is a behavior's own name for a column, table or index:
/// `^[a-z][a-z0-9_]{0,62}$` (`LOCAL_NAME`).
pub fn is_local_name(name: &str) -> bool {
    is_column_name(name)
}

/// The prefix of a behavior's names under its key.
pub fn prefix_of(key: &str) -> String {
    format!("{STORAGE_PREFIX}{key}__")
}

/// The SQL name of the relation over the instances a behavior's SQL reads
/// (`sql.instances()`), before a database keeps it.
pub fn relation_name(prefix: &str) -> String {
    format!("{prefix}_instances")
}

/// The SQL name of the relation over the schemas a namespace operation's
/// statements may read (`sql.readable()`, D77), before a database keeps it.
pub fn readable_name(prefix: &str) -> String {
    format!("{prefix}_readable")
}

/// The SQL name of an index a behavior's migration lists on the instances
/// table, before a database keeps it.
pub fn index_name(prefix: &str, name: &str) -> String {
    format!("{prefix}_index_{name}")
}

/// The name of a behavior's column on `engine_instances` as `dialect`
/// keeps it.
pub fn instance_column(dialect: Dialect, prefix: &str, column: &str) -> String {
    identifier(dialect, &format!("{prefix}{column}")).into_owned()
}

/// A behavior's key, or `None` when no schema that composes it has been
/// published.
pub fn stored_key(tx: &Tx<'_>, name: &str) -> Result<Option<String>> {
    let row = tx.get(
        "SELECT key FROM engine_behaviors WHERE name = ?",
        &[SqlValue::from(name)],
    )?;
    row.map(|row| row.text("key").map(str::to_owned))
        .transpose()
}

/// A behavior's key, recording a new one at `now` (epoch milliseconds).
/// Call it in a write that holds the catalog's lock.
pub fn assign_key(tx: &Tx<'_>, name: &str, now: i64) -> Result<String> {
    if let Some(key) = stored_key(tx, name)? {
        return Ok(key);
    }
    let base = key_base(name);
    let mut key = base.clone();
    let mut n = 2;
    while tx
        .get(
            "SELECT 1 AS taken FROM engine_behaviors WHERE key = ?",
            &[SqlValue::from(key.as_str())],
        )?
        .is_some()
    {
        key = format!("{base}_{n}");
        n += 1;
    }
    tx.run(
        "INSERT INTO engine_behaviors (name, key, created_at) VALUES (?, ?, ?)",
        &[
            SqlValue::from(name),
            SqlValue::from(key.as_str()),
            SqlValue::Integer(now),
        ],
    )?;
    Ok(key)
}

// key_base is a behavior's name lowercased, each run of other characters
// one `_`, none leading or trailing, at most MAX_KEY bytes; `b` when
// nothing is left.
fn key_base(name: &str) -> String {
    let mut slug = String::with_capacity(name.len());
    for c in name.chars().flat_map(char::to_lowercase) {
        if c.is_ascii_lowercase() || c.is_ascii_digit() {
            slug.push(c);
        } else if !slug.ends_with('_') {
            slug.push('_');
        }
    }
    slug.truncate(MAX_KEY);
    let trimmed = slug.trim_matches('_');
    if trimmed.is_empty() {
        "b".to_owned()
    } else {
        trimmed.to_owned()
    }
}

/// What is wrong with a column a behavior's migration adds to the
/// instances table, as `columnProblems` says it.
pub fn column_problems(name: &str, spec: &ColumnSpec, dialect: Dialect) -> Vec<String> {
    let at = format!("column {name}");
    if !is_local_name(name) {
        return vec![format!(
            "{at}: a column name matches ^[a-z][a-z0-9_]{{0,62}}$"
        )];
    }
    let mut problems = Vec::new();
    if let Err(error) = spec.ty.sql(dialect) {
        problems.push(format!("{at}: {error}"));
    }
    if spec.check.is_some() {
        problems.push(format!(
            "{at}: a column on the instances table takes a type, NOT NULL and a default"
        ));
    }
    let default = spec.default.as_ref().filter(|value| !value.is_null());
    if spec.not_null && default.is_none() {
        problems.push(format!(
            "{at} is NOT NULL, so it needs a default for the instances that exist"
        ));
    }
    if let Some(value) = default {
        if !spec.ty.fits(value) {
            problems.push(format!(
                "{at}: a default of {value} does not fit type {}",
                spec.ty.name()
            ));
        }
    }
    problems
}

/// What a behavior's migration runs after its columns, indexes and tables.
pub type BehaviorUp = Arc<dyn Fn(&BehaviorSql<'_, '_>) -> Result<()> + Send + Sync>;

/// One forward step of a behavior's storage, recorded in the engine's
/// ledger under the behavior's name. A shipped migration is never edited;
/// a change is a new migration at the end of the list.
#[derive(Clone)]
pub struct BehaviorMigration {
    /// 1, 2, 3, ... in order.
    pub version: u32,
    pub name: String,
    /// Columns this step adds to the instances table, by the behavior's
    /// own name for each.
    pub columns: Vec<(String, ColumnSpec)>,
    /// Indexes this step adds to the instances table over the behavior's
    /// own columns, by its own name for each: the columns, by its own
    /// names, that this step or an earlier one adds, in index order. The
    /// engine names each index and leads it with the instance's namespace
    /// and schema, so a statement on the relation that filters or orders by
    /// the columns reads the index rather than every instance.
    pub indexes: Vec<(String, Vec<String>)>,
    /// Tables this step creates, by the behavior's own names for them and
    /// their indexes.
    pub tables: Vec<TableSpec>,
    /// DDL and data changes on the behavior's own tables, in the migrate
    /// mode of [`BehaviorSql`].
    pub up: Option<BehaviorUp>,
}

impl BehaviorMigration {
    /// A step that adds nothing yet.
    pub fn new(version: u32, name: impl Into<String>) -> Self {
        BehaviorMigration {
            version,
            name: name.into(),
            columns: Vec::new(),
            indexes: Vec::new(),
            tables: Vec::new(),
            up: None,
        }
    }

    /// The step, adding a column to the instances table.
    pub fn column(mut self, name: impl Into<String>, spec: ColumnSpec) -> Self {
        self.columns.push((name.into(), spec));
        self
    }

    /// The step, adding an index over the behavior's columns.
    pub fn index(mut self, name: impl Into<String>, columns: &[&str]) -> Self {
        self.indexes.push((
            name.into(),
            columns.iter().map(|&column| column.to_owned()).collect(),
        ));
        self
    }

    /// The step, creating a table of the behavior's own.
    pub fn table(mut self, spec: TableSpec) -> Self {
        self.tables.push(spec);
        self
    }

    /// The step, running `up` last.
    pub fn up(
        mut self,
        up: impl Fn(&BehaviorSql<'_, '_>) -> Result<()> + Send + Sync + 'static,
    ) -> Self {
        self.up = Some(Arc::new(up));
        self
    }
}

impl std::fmt::Debug for BehaviorMigration {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("BehaviorMigration")
            .field("version", &self.version)
            .field("name", &self.name)
            .field("columns", &self.columns)
            .field("indexes", &self.indexes)
            .field("tables", &self.tables)
            .finish_non_exhaustive()
    }
}

/// A behavior's migrations, wrapped for the engine's ledger under the
/// behavior's name. The registry checks them first (their versions, names,
/// columns and indexes); each step adds its columns, indexes and tables,
/// runs its `up()`, and checks what the database gained.
pub fn migration_set(
    behavior: &str,
    prefix: &str,
    migrations: &[BehaviorMigration],
) -> MigrationSet {
    let set = migrations
        .iter()
        .map(|migration| {
            let behavior = behavior.to_owned();
            let prefix = prefix.to_owned();
            let step = migration.clone();
            Migration::new(migration.version, migration.name.clone(), move |tx| {
                run_step(tx, &behavior, &prefix, &step)
            })
        })
        .collect();
    MigrationSet::new(behavior, set)
}

fn run_step(tx: &Tx<'_>, behavior: &str, prefix: &str, step: &BehaviorMigration) -> Result<()> {
    let dialect = tx.dialect();
    for (column, spec) in &step.columns {
        let problems = column_problems(column, spec, dialect);
        if !problems.is_empty() {
            return Err(Error::behavior(behavior, problems.join("; ")));
        }
        tx.exec(format!(
            "ALTER TABLE engine_instances ADD COLUMN {} {}",
            quote_identifier(&instance_column(dialect, prefix, column)),
            spec.definition(dialect)?
        ))?;
    }
    // The names and columns were checked when the behavior registered: its
    // own names, of its own columns.
    for (index, columns) in &step.indexes {
        let columns: Vec<String> = columns
            .iter()
            .map(|column| quote_identifier(&instance_column(dialect, prefix, column)))
            .collect();
        tx.exec(format!(
            "CREATE INDEX {} ON engine_instances (namespace, schema, {})",
            quote_identifier(&identifier(dialect, &index_name(prefix, index))),
            columns.join(", ")
        ))?;
    }
    if step.tables.is_empty() && step.up.is_none() {
        return Ok(());
    }
    let before: HashSet<String> = objects(tx)?
        .into_iter()
        .map(|object| object.key())
        .collect();
    let sql = BehaviorSql::new(tx, behavior, prefix, SqlMode::Migrate, None);
    for spec in &step.tables {
        sql.create_table(spec)?;
    }
    if let Some(up) = &step.up {
        up(&sql)?;
    }
    for object in objects(tx)? {
        if !before.contains(&object.key()) {
            check_created(behavior, prefix, step, &object)?;
        }
    }
    Ok(())
}

/// The instances a call's relation reads (`sql.instances()`): one
/// schema's in one namespace, as one principal.
pub struct InstanceRelation<'a> {
    pub namespace: String,
    pub schema: String,
    /// The behavior's own columns, by its own names.
    pub columns: Vec<String>,
    /// Asks the access policy for read on the schema as the call's
    /// principal; an error (forbidden) refuses the statement.
    pub allow: Box<dyn Fn() -> Result<()> + 'a>,
}

/// The schemas a namespace operation's statements may read (D77,
/// `sql.readable()`): those of the call's namespace that compose the
/// behavior and that the call's principal may read. `schemas` asks the
/// access policy, once a call, the first time a statement names the
/// relation; an error refuses the statement.
pub struct ReadableRelation<'a> {
    pub schemas: Box<dyn Fn() -> Result<Vec<String>> + 'a>,
}

/// The SQL handle a behavior gets: its own tables, in one mode, and with a
/// relation, its own columns across the instances of the call's schema, or
/// in a namespace operation the schemas it may read. Every statement
/// passes [`sql_refusal`] first.
pub struct BehaviorSql<'t, 'db> {
    tx: &'t Tx<'db>,
    behavior: &'t str,
    prefix: &'t str,
    mode: SqlMode,
    relation: Option<&'t InstanceRelation<'t>>,
    readable: Option<&'t ReadableRelation<'t>>,
}

impl<'t, 'db> BehaviorSql<'t, 'db> {
    /// The SQL of `behavior`, whose names start `prefix`, in `mode` on
    /// `tx`; `relation` is what `instances()` reads, `None` for a call with
    /// no principal (a migration, afterConfigChange).
    pub fn new(
        tx: &'t Tx<'db>,
        behavior: &'t str,
        prefix: &'t str,
        mode: SqlMode,
        relation: Option<&'t InstanceRelation<'t>>,
    ) -> Self {
        BehaviorSql {
            tx,
            behavior,
            prefix,
            mode,
            relation,
            readable: None,
        }
    }

    /// The SQL of a namespace operation (D77): no relation over one
    /// schema's instances, and `readable()` over the schemas it may read.
    pub fn namespace(
        tx: &'t Tx<'db>,
        behavior: &'t str,
        prefix: &'t str,
        mode: SqlMode,
        readable: &'t ReadableRelation<'t>,
    ) -> Self {
        BehaviorSql {
            readable: Some(readable),
            ..BehaviorSql::new(tx, behavior, prefix, mode, None)
        }
    }

    /// Which database the statements run on, for a migration whose
    /// statements differ.
    pub fn dialect(&self) -> Dialect {
        self.tx.dialect()
    }

    /// What the statements may do.
    pub fn mode(&self) -> SqlMode {
        self.mode
    }

    /// The SQL name of one of the behavior's tables, by its own name for
    /// it.
    pub fn table(&self, name: &str) -> Result<String> {
        if !is_local_name(name) {
            return Err(Error::behavior(
                self.behavior,
                format!("table name {name:?} must match ^[a-z][a-z0-9_]{{0,62}}$"),
            ));
        }
        Ok(identifier(self.dialect(), &format!("{}{name}", self.prefix)).into_owned())
    }

    /// The SQL name of the relation over the instances of the call's
    /// schema. It refuses where a statement on the relation would: in a
    /// call with no principal, and for a behavior with a column the
    /// relation cannot hold under its own name.
    pub fn instances(&self) -> Result<String> {
        self.definition()?;
        Ok(self.relation_name())
    }

    /// The SQL name of the relation over the schemas a namespace
    /// operation may read, one row each, its column `schema` (D77). It
    /// refuses anywhere else.
    pub fn readable(&self) -> Result<String> {
        if self.readable.is_none() {
            return Err(Error::behavior(
                self.behavior,
                "sql.readable() is a namespace operation's: it names the schemas of the call's namespace that compose the behavior and that the principal may read",
            ));
        }
        Ok(self.readable_name())
    }

    /// Runs one statement and returns its first row.
    pub fn get<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Option<Row>> {
        let prepared = self.prepare(&sql.into())?;
        self.tx.get(prepared, params)
    }

    /// Runs one statement and returns every row.
    pub fn all<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Vec<Row>> {
        let prepared = self.prepare(&sql.into())?;
        self.tx.all(prepared, params)
    }

    /// The plan the database makes for a statement the behavior may run,
    /// as it runs it, the relation's definition merged in: SQLite's
    /// `EXPLAIN QUERY PLAN` details, or the lines of Postgres's `EXPLAIN`.
    /// What a behavior's author reads to see a filter on its columns reach
    /// the indexes its migrations list. It is checked as the statement is,
    /// and names of the relation ask read as the statement would; nothing
    /// runs.
    pub fn explain<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Vec<String>> {
        let prepared = self.prepare(&sql.into())?;
        let (statement, column) = match self.dialect() {
            Dialect::Sqlite => (format!("EXPLAIN QUERY PLAN {prepared}"), 3),
            Dialect::Postgres => (format!("EXPLAIN {prepared}"), 0),
        };
        Ok(self
            .tx
            .all(statement, params)?
            .iter()
            .map(|row| match row.values().get(column) {
                Some(SqlValue::Text(text)) => text.clone(),
                Some(other) => other.to_string(),
                None => String::new(),
            })
            .collect())
    }

    /// Runs one INSERT, UPDATE, DELETE or REPLACE (or a read).
    pub fn run<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<RunResult> {
        self.writable()?;
        let prepared = self.prepare(&sql.into())?;
        self.tx.run(prepared, params)
    }

    /// [`BehaviorSql::get`] in a savepoint of its own, so its failure
    /// leaves the call running.
    pub fn try_get<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Option<Row>> {
        let prepared = self.prepare(&sql.into())?;
        self.tx.try_get(prepared, params)
    }

    /// [`BehaviorSql::all`] in a savepoint of its own.
    pub fn try_all<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Vec<Row>> {
        let prepared = self.prepare(&sql.into())?;
        self.tx.try_all(prepared, params)
    }

    /// [`BehaviorSql::run`] in a savepoint of its own: a unique conflict
    /// comes back as [`Error::Unique`] and the call goes on.
    pub fn try_run<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<RunResult> {
        self.writable()?;
        let prepared = self.prepare(&sql.into())?;
        self.tx.try_run(prepared, params)
    }

    /// Creates a table of the behavior's own in a migration: `spec` names
    /// it and its indexes by the behavior's own names.
    pub fn create_table(&self, spec: &TableSpec) -> Result<()> {
        if self.mode != SqlMode::Migrate {
            return Err(Error::behavior(
                self.behavior,
                "a table is created in a migration",
            ));
        }
        let mut names = vec![spec.name.as_str()];
        names.extend(spec.indexes.iter().map(|index| index.name.as_str()));
        for name in names {
            self.table(name)?;
        }
        let prefixed = spec.renamed(|name| format!("{}{name}", self.prefix));
        let statements = prefixed
            .statements(self.dialect())
            .map_err(|error| Error::behavior(self.behavior, error.to_string()))?;
        for statement in statements {
            self.run(statement, &[])?;
        }
        Ok(())
    }

    /// Creates a full-text index of the behavior's own in a migration
    /// (D58, [`crate::fulltext`]): `columns` of text keyed by an integer
    /// `rowid`, which the behavior writes and deletes with plain SQL on
    /// both databases (`INSERT INTO <index> (rowid, <columns>) ...`) and
    /// matches with [`BehaviorSql::fulltext`]. On SQLite it is the FTS5
    /// table the TypeScript engine creates, through the checker; on
    /// Postgres the engine writes its table, its generated vectors, their
    /// GIN index and its text search configuration itself, creating the
    /// `unaccent` extension when the database lacks it
    /// ([`fulltext::Unaccent::Required`]).
    pub fn create_fulltext(&self, name: &str, columns: &[&str]) -> Result<()> {
        self.create_fulltext_with(name, columns, fulltext::Unaccent::Required)
    }

    /// [`BehaviorSql::create_fulltext`], saying whether a Postgres index
    /// needs `unaccent`: with [`fulltext::Unaccent::IfAvailable`] a
    /// database that lacks it and refuses the engine its create gets an
    /// index that folds case alone, and the migration goes on.
    pub fn create_fulltext_with(
        &self,
        name: &str,
        columns: &[&str],
        unaccent: fulltext::Unaccent,
    ) -> Result<()> {
        if self.mode != SqlMode::Migrate {
            return Err(Error::behavior(
                self.behavior,
                "a full-text index is created in a migration",
            ));
        }
        let table = self.table(name)?;
        let problems = fulltext::column_problems(columns);
        if !problems.is_empty() {
            return Err(Error::behavior(self.behavior, problems.join("; ")));
        }
        match self.dialect() {
            Dialect::Sqlite => {
                self.run(fulltext::sqlite_create(&table, columns), &[])?;
            }
            Dialect::Postgres => {
                let config = fulltext::config_name(self.prefix, name);
                match unaccent {
                    fulltext::Unaccent::Required => {
                        let extension = self.unaccent_schema()?.map_err(|failure| {
                            Error::behavior(
                                self.behavior,
                                format!(
                                    "full-text search on Postgres needs the unaccent extension, which the database does not have and the engine could not create in schema {} ({failure}); have an administrator run CREATE EXTENSION unaccent",
                                    fulltext::EXTENSION_SCHEMA,
                                ),
                            )
                        })?;
                        for statement in
                            fulltext::postgres_create(&table, &config, &extension, columns)
                        {
                            self.tx.exec(statement)?;
                        }
                    }
                    fulltext::Unaccent::IfAvailable => {
                        self.tx.exec(fulltext::postgres_configuration(&config))?;
                        // unaccent where the database has it or the engine
                        // may create it, and the role may use it; else
                        // simple alone. Each try in a savepoint, so a
                        // refusal leaves the migration running. Only a
                        // refusal falls back: any other error (a lock or
                        // statement timeout, a lost connection) is the
                        // migration's, and fails it, rather than leave an
                        // index made without unaccent for good.
                        let mapped = match self.unaccent_schema()? {
                            Ok(extension) => match self.tx.savepoint(|tx| {
                                tx.exec(fulltext::postgres_mapping(&config, Some(&extension)))
                            }) {
                                Ok(_) => true,
                                Err(error) if fulltext::unaccent_unavailable(&error) => false,
                                Err(error) => return Err(error),
                            },
                            Err(_) => false,
                        };
                        if !mapped {
                            self.tx.exec(fulltext::postgres_mapping(&config, None))?;
                        }
                        for statement in fulltext::postgres_tables(&table, &config, columns) {
                            self.tx.exec(statement)?;
                        }
                    }
                }
            }
        }
        Ok(())
    }

    /// Matches an FTS5 expression over one of the behavior's full-text
    /// indexes (D58, [`crate::fulltext`]): the rows of its `keys` table the
    /// index rows match, best first, each with its snippets. A caller's
    /// expression the index does not take comes back as a
    /// [`fulltext::QueryRefusal`], checked before it runs on Postgres and
    /// run in a savepoint on SQLite, so the call goes on either way.
    pub fn fulltext(&self, query: &FullTextQuery<'_>) -> Result<FullTextMatches> {
        let problems = fulltext::query_problems(query);
        if !problems.is_empty() {
            return Err(Error::behavior(self.behavior, problems.join("; ")));
        }
        let dialect = self.dialect();
        let index = self.table(query.index)?;
        let keys = self.table(query.keys)?;
        let config = fulltext::config_name(self.prefix, query.index);
        // A match kept to the readable schemas is a namespace operation's:
        // readable() refuses anywhere else.
        let readable = match query.readable {
            Some(_) => Some(self.readable()?),
            None => None,
        };
        let names = fulltext::Names {
            index: &index,
            keys: &keys,
            config: &config,
            readable: readable.as_deref(),
        };
        let expression = match dialect {
            Dialect::Sqlite => query.expression.to_owned(),
            Dialect::Postgres => match fulltext::tsquery(query.expression) {
                Ok(translated) => translated,
                Err(refusal) if query.caller => return Ok(Err(refusal)),
                Err(refusal) => {
                    return Err(fulltext::own_expression_refused(self.behavior, &refusal))
                }
            },
        };
        let (sql, params) = fulltext::match_statement(dialect, &names, query, &expression);
        let rows = if dialect == Dialect::Sqlite && query.caller {
            match self.try_all(sql, &params) {
                Ok(rows) => rows,
                Err(Error::Database { message, .. })
                    if fulltext::is_sqlite_query_error(&message) =>
                {
                    return Ok(Err(fulltext::QueryRefusal { message }));
                }
                Err(error) => return Err(error),
            }
        } else {
            self.all(sql, &params)?
        };
        Ok(Ok(fulltext::hits(dialect, query, rows)))
    }

    // unaccent_schema is the schema the database's unaccent extension lives
    // in, created in fulltext::EXTENSION_SCHEMA when it has none. Each
    // create runs in a savepoint: another engine creating it at the same
    // moment fails it as a duplicate, and leaves it there.
    // unaccent_schema is the schema `unaccent` lives in, created in
    // EXTENSION_SCHEMA when the database lacks it; `Ok(Err(failure))` when
    // the database refuses the engine its create
    // (fulltext::unaccent_unavailable), and any other error the
    // migration's.
    fn unaccent_schema(&self) -> Result<std::result::Result<String, String>> {
        let found = |tx: &Tx<'_>| -> Result<Option<String>> {
            tx.get(fulltext::UNACCENT_SCHEMA, &[])?
                .map(|row| row.text("schema").map(str::to_owned))
                .transpose()
        };
        if let Some(schema) = found(self.tx)? {
            return Ok(Ok(schema));
        }
        let mut failure = None;
        for statement in fulltext::unaccent_create() {
            if let Err(error) = self.tx.savepoint(|tx| tx.exec(statement.as_str())) {
                let duplicate = matches!(error.sqlstate(), Some("23505" | "42P06" | "42710"));
                if duplicate {
                    continue;
                }
                if !fulltext::unaccent_unavailable(&error) {
                    return Err(error);
                }
                failure = Some(error.to_string());
                break;
            }
        }
        Ok(match (found(self.tx)?, failure) {
            (Some(schema), _) => Ok(schema),
            (None, failure) => Err(failure.unwrap_or_else(|| "it is still missing".to_owned())),
        })
    }

    fn writable(&self) -> Result<()> {
        if self.mode == SqlMode::Read {
            return Err(Error::behavior(
                self.behavior,
                "a read cannot run a statement that writes; run() is for writes",
            ));
        }
        Ok(())
    }

    fn relation_name(&self) -> String {
        identifier(self.dialect(), &relation_name(self.prefix)).into_owned()
    }

    fn readable_name(&self) -> String {
        identifier(self.dialect(), &readable_name(self.prefix)).into_owned()
    }

    // readable_definition is what follows `<readable> AS`: one row per
    // schema, each a string literal, as the relation over the instances
    // inlines its own; no row when the principal may read none.
    fn readable_definition(&self, readable: &ReadableRelation<'_>) -> Result<String> {
        let schemas = (readable.schemas)()?;
        if schemas.is_empty() {
            return Ok("(SELECT CAST(NULL AS TEXT) AS schema WHERE 1 = 0)".to_owned());
        }
        Ok(format!(
            "({})",
            schemas
                .iter()
                .map(|schema| format!("SELECT CAST({} AS TEXT) AS schema", sql_string(schema)))
                .collect::<Vec<_>>()
                .join(" UNION ALL ")
        ))
    }

    // prepare checks the behavior's statement and returns what runs: the
    // statement, with the relation's definition ahead of it when it names
    // the relation, once the access policy has allowed the read.
    fn prepare(&self, sql: &Sql<'_>) -> Result<String> {
        let dialect = self.dialect();
        let text = sql.text(dialect);
        if let Some(refusal) = sql_refusal(text, self.prefix, self.mode, dialect, self.tx.schemas())
        {
            let shown: String = text.trim().chars().take(200).collect();
            return Err(Error::behavior(
                self.behavior,
                format!("SQL refused: {refusal}: {shown}"),
            ));
        }
        let readable = match self.readable {
            None => None,
            Some(readable) => with_relation(text, dialect, &self.readable_name(), || {
                self.readable_definition(readable)
            })?,
        };
        let text = readable.as_deref().unwrap_or(text);
        let Some(relation) = self.relation else {
            return Ok(text.to_owned());
        };
        match with_relation(text, dialect, &self.relation_name(), || self.definition())? {
            None => Ok(text.to_owned()),
            Some(prepared) => {
                (relation.allow)()?;
                Ok(prepared)
            }
        }
    }

    // definition is what follows `<relation> AS` in the expression the
    // engine puts ahead of a statement that names the relation.
    //
    // The namespace and the schema are SQL string literals, not
    // parameters: a parameter in the expression would take the first
    // position ahead of the statement's own, so every positional ? the
    // behavior wrote would bind the wrong value. Both values are names the
    // engine checked, and sql_string doubles a quote all the same.
    //
    // NOT MATERIALIZED makes both databases fold the expression into the
    // statement as they would a view, so a filter on the behavior's columns
    // reaches the indexes its migrations list.
    fn definition(&self) -> Result<String> {
        let Some(relation) = self.relation else {
            return Err(Error::behavior(
                self.behavior,
                "sql.instances() reads as a principal, and a migration and afterConfigChange act for none; they read the instances with eachInstance",
            ));
        };
        let clash: Vec<&str> = relation
            .columns
            .iter()
            .map(String::as_str)
            .filter(|column| RELATION_COLUMNS.contains(column))
            .collect();
        if !clash.is_empty() {
            return Err(Error::behavior(
                self.behavior,
                format!(
                    "sql.instances(): its column {} shares a name with the relation's own columns ({})",
                    clash.join(", "),
                    RELATION_COLUMNS.join(", ")
                ),
            ));
        }
        let dialect = self.dialect();
        let own: String = relation
            .columns
            .iter()
            .map(|column| {
                format!(
                    ", {} AS {}",
                    quote_identifier(&instance_column(dialect, self.prefix, column)),
                    quote_identifier(column)
                )
            })
            .collect();
        Ok(format!(
            "NOT MATERIALIZED (SELECT {}{own} FROM engine_instances WHERE namespace = {} AND schema = {})",
            RELATION_COLUMNS.join(", "),
            sql_string(&relation.namespace),
            sql_string(&relation.schema)
        ))
    }
}

/// An object the database's catalog lists.
struct SchemaObject {
    kind: String,
    name: String,
    table: String,
}

impl SchemaObject {
    fn key(&self) -> String {
        format!("{}\u{0}{}", self.kind, self.name)
    }
}

fn objects(tx: &Tx<'_>) -> Result<Vec<SchemaObject>> {
    let rows = tx.all(
        Sql::per(
            "SELECT type, name, tbl_name FROM sqlite_master",
            // Every relation of the engine's schema, an index with its
            // table and a sequence with the table that owns it, and every
            // trigger on one of them.
            "SELECT CASE c.relkind
                      WHEN 'r' THEN 'table' WHEN 'p' THEN 'table'
                      WHEN 'i' THEN 'index' WHEN 'I' THEN 'index'
                      WHEN 'S' THEN 'sequence'
                      WHEN 'v' THEN 'view' WHEN 'm' THEN 'view'
                      ELSE 'relation ' || c.relkind::text
                    END AS type,
                    c.relname::text AS name,
                    COALESCE(t.relname, owner.relname, c.relname)::text AS tbl_name
             FROM pg_catalog.pg_class AS c
             LEFT JOIN pg_catalog.pg_index AS i ON i.indexrelid = c.oid
             LEFT JOIN pg_catalog.pg_class AS t ON t.oid = i.indrelid
             LEFT JOIN pg_catalog.pg_depend AS d
               ON c.relkind = 'S' AND d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = c.oid
              AND d.refclassid = 'pg_catalog.pg_class'::regclass AND d.deptype IN ('a', 'i')
             LEFT JOIN pg_catalog.pg_class AS owner ON owner.oid = d.refobjid
             WHERE c.relnamespace = current_schema()::regnamespace
             UNION ALL
             SELECT 'trigger', g.tgname::text, c.relname::text
             FROM pg_catalog.pg_trigger AS g
             JOIN pg_catalog.pg_class AS c ON c.oid = g.tgrelid
             WHERE c.relnamespace = current_schema()::regnamespace AND NOT g.tgisinternal",
        ),
        &[],
    )?;
    rows.iter()
        .map(|row| {
            Ok(SchemaObject {
                kind: row.text("type")?.to_owned(),
                name: row.text("name")?.to_owned(),
                table: row.text("tbl_name")?.to_owned(),
            })
        })
        .collect()
}

// check_created holds a new object to the behavior's own names: a table,
// or an index (its own, or the one the database makes for a UNIQUE or
// PRIMARY KEY constraint) on a table of its own, or on Postgres a table's
// sequence. A name under the engine's for the behavior (bhv_<key>___) is
// not its own.
fn check_created(
    behavior: &str,
    prefix: &str,
    step: &BehaviorMigration,
    object: &SchemaObject,
) -> Result<()> {
    let prefix_lower = prefix.to_lowercase();
    let engine_names = format!("{prefix_lower}_");
    let own = |name: &str| {
        let name = name.to_lowercase();
        name.starts_with(&prefix_lower) && !name.starts_with(&engine_names)
    };
    let named = if object.kind == "index" && object.name.starts_with("sqlite_autoindex_") {
        own(&object.table)
    } else {
        own(&object.name)
    };
    let kind_allowed = matches!(object.kind.as_str(), "table" | "index" | "sequence");
    if kind_allowed && named && own(&object.table) {
        return Ok(());
    }
    Err(Error::behavior(
        behavior,
        format!(
            "migration {} \"{}\" created {} {}; a behavior creates only tables and indexes named {prefix}*, with sql.table(name)",
            step.version, step.name, object.kind, object.name
        ),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_key_is_the_name_lowercased_with_runs_of_other_characters_one_underscore() {
        assert_eq!(key_base("test.Sneaky"), "test_sneaky");
        assert_eq!(key_base("__Weird--Name__"), "weird_name");
        assert_eq!(key_base("workqueue.Assignment"), "workqueue_assignment");
        assert_eq!(key_base("..."), "b");
        assert_eq!(key_base("Ünïcode"), "n_code");
        let long = key_base(&format!("{}.Behavior", "x".repeat(60)));
        assert_eq!(long.len(), MAX_KEY);
        assert_eq!(key_base(&format!("{}.y", "a".repeat(39))), "a".repeat(39));
    }

    #[test]
    fn instance_columns_take_a_type_not_null_and_a_default() {
        let fine = ColumnSpec::integer().not_null().default(0);
        assert!(column_problems("count", &fine, Dialect::Sqlite).is_empty());
        assert_eq!(
            column_problems("Count", &fine, Dialect::Sqlite),
            ["column Count: a column name matches ^[a-z][a-z0-9_]{0,62}$"]
        );
        assert_eq!(
            column_problems("count", &ColumnSpec::integer().not_null(), Dialect::Sqlite),
            ["column count is NOT NULL, so it needs a default for the instances that exist"]
        );
        assert_eq!(
            column_problems(
                "count",
                &ColumnSpec::integer().default(1.5),
                Dialect::Sqlite
            ),
            ["column count: a default of 1.5 does not fit type integer"]
        );
        assert_eq!(
            column_problems(
                "big",
                &ColumnSpec::integer().default(1_i64 << 53),
                Dialect::Sqlite
            ),
            ["column big: a default of 9007199254740992 does not fit type integer"]
        );
        assert!(column_problems(
            "v",
            &ColumnSpec::new(crate::ddl::ColumnType::Any).default("x"),
            Dialect::Sqlite
        )
        .is_empty());
        assert_eq!(
            column_problems(
                "v",
                &ColumnSpec::new(crate::ddl::ColumnType::Any),
                Dialect::Postgres
            )
            .len(),
            1
        );
        assert_eq!(
            column_problems("v", &ColumnSpec::text().check("v <> ''"), Dialect::Sqlite).len(),
            1
        );
    }
}
