//! The contexts a behavior's hooks get (`src/behaviors/behavior.ts`), each
//! reaching only what the behavior may touch, layered as the TypeScript
//! interfaces are: [`Scope`] (the behavior, its config and the call: its
//! namespace, schema, version, principal and time, `can()`, `validate()`,
//! and the namespace's other instances and schemas) under
//! [`InstanceView`] (one instance: its own fields, the behavior's columns
//! and SQL to read, its references and values) under [`InstanceCx`] (a
//! write: columns and SQL that write, references and values that change,
//! `call()`) under [`OperationCx`] (an operation: `update()`,
//! `validate_update()`, `unchanged()`). Each derefs to the one below it.
//! [`ReferenceCx`], [`ValidationCx`], [`SchemaCx`], [`ReactionCx`],
//! [`ScheduleCx`] and [`PublishCx`] are the other hooks' contexts.
//!
//! A context borrows the engine's call (`Core`): the one transaction every
//! part of the call shares, borrowed for one statement at a time, so no
//! borrow is held while a hook runs. What a context may do in its moment
//! (a read-only operation's columns refuse to change, a deleted instance's
//! values to stow) is the call's to say, with the TypeScript engine's
//! words.

use std::ops::Deref;
use std::sync::Arc;

use serde_json::{Map, Value};

use superschematic_engine_storage::fulltext::{FullTextMatches, FullTextQuery};
use superschematic_engine_storage::{Dialect, Row, RunResult, Sql, SqlValue};

use super::behavior::{InstanceFields, StoredInstance, StowedObject};
use crate::errors::{Error, Result, ValidationIssue};
use crate::events::EngineEvent;
use crate::instances::references::{Hears, Reference};
use crate::instances::InstanceRecord;
use crate::principal::Principal;
use crate::UuidSource;

/// How deep `call()`, invokes, creates and reads of other instances may
/// nest together; deeper is a cycle.
pub const MAX_CALL_DEPTH: usize = 16;

/// The most ids one `get_many` reads.
pub const MAX_BATCH_READ: usize = 500;

/// What a hook's context reaches of the engine's call. Each kind of
/// context implements what it has; the rest is a defect of the engine's,
/// which the public types never reach.
pub(crate) trait Core {
    fn behavior(&self) -> &str;
    fn namespace(&self) -> &str;
    fn schema(&self) -> &str;
    fn version(&self) -> i64;
    fn now(&self) -> i64;
    fn principal(&self) -> &Principal;

    fn id(&self) -> &str {
        ""
    }
    fn uuids(&self) -> UuidSource {
        UuidSource::random()
    }
    fn can(&self, _permission: &str) -> Result<bool> {
        Err(unreachable_here("can()"))
    }
    fn validate_type(&self, _type_name: &str, _value: &Value) -> Result<Vec<ValidationIssue>> {
        Err(unreachable_here("validate()"))
    }
    fn check_type(
        &self,
        _type_name: &str,
        _value: &Value,
        _path: &str,
    ) -> Result<Vec<ValidationIssue>> {
        Err(unreachable_here("checkType()"))
    }

    fn read(
        &self,
        _schema: &str,
        _ids: &[&str],
        _fields: Option<&[&str]>,
        _what: &str,
    ) -> Result<Vec<(String, InstanceRecord)>> {
        Err(unreachable_here("instances"))
    }
    fn invoke(
        &self,
        _schema: &str,
        _id: &str,
        _operation: &str,
        _params: &Value,
        _preconditions: Option<&Value>,
    ) -> Result<Value> {
        Err(unreachable_here("instances.invoke"))
    }
    fn invoke_schema(&self, _schema: &str, _operation: &str, _params: &Value) -> Result<Value> {
        Err(unreachable_here("instances.invokeSchema"))
    }
    fn create(
        &self,
        _schema: &str,
        _data: &Value,
        _id: Option<&str>,
        _behaviors: Option<&Value>,
    ) -> Result<InstanceRecord> {
        Err(unreachable_here("instances.create"))
    }
    fn schema_config(&self, _schema: &str, _behavior: &str) -> Result<Option<Value>> {
        Err(unreachable_here("schemas.config"))
    }
    fn readable(&self, _schema: &str) -> Result<bool> {
        Err(unreachable_here("schemas.readable"))
    }

    fn data(&self) -> Result<Arc<Map<String, Value>>> {
        Err(unreachable_here("data"))
    }
    fn columns_get(&self) -> Result<Row> {
        Err(unreachable_here("columns"))
    }
    fn columns_set(&self, _values: &[(&str, SqlValue)]) -> Result<()> {
        Err(unreachable_here("columns.set"))
    }

    fn dialect(&self) -> Dialect;
    fn sql_table(&self, name: &str) -> Result<String>;
    fn sql_instances(&self) -> Result<String> {
        Err(unreachable_here("sql.instances()"))
    }
    fn sql_get(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<Option<Row>>;
    fn sql_all(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<Vec<Row>>;
    fn sql_explain(&self, _sql: &Sql<'_>, _params: &[SqlValue]) -> Result<Vec<String>> {
        Err(unreachable_here("sql.explain"))
    }
    fn sql_run(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<RunResult>;
    fn sql_fulltext(&self, _query: &FullTextQuery<'_>) -> Result<FullTextMatches> {
        Err(unreachable_here("sql.fulltext"))
    }

    fn references_list(&self) -> Result<Vec<Reference>> {
        Err(unreachable_here("references"))
    }
    fn references_add(
        &self,
        _schema: &str,
        _id: &str,
        _key: &str,
        _hears: Option<Hears>,
    ) -> Result<()> {
        Err(unreachable_here("references.add"))
    }
    fn references_remove(&self, _schema: &str, _id: &str, _key: &str) -> Result<bool> {
        Err(unreachable_here("references.remove"))
    }

    fn values_load(&self, json: &str, refs: Option<&str>) -> Result<Map<String, Value>>;
    fn values_stow(&self, _key: &str, _object: &Map<String, Value>) -> Result<StowedObject> {
        Err(unreachable_here("values.stow"))
    }
    fn values_release(&self, _key: Option<&str>) -> Result<()> {
        Err(unreachable_here("values.release"))
    }

    fn call(&self, _behavior: &str, _operation: &str, _params: &Value) -> Result<Value> {
        Err(unreachable_here("call()"))
    }
    fn update(
        &self,
        _patch: &Value,
        _attribution: Option<&Value>,
    ) -> Result<Arc<Map<String, Value>>> {
        Err(unreachable_here("update()"))
    }
    fn validate_update(&self, _patch: &Value) -> Result<Vec<ValidationIssue>> {
        Err(unreachable_here("validateUpdate()"))
    }
    fn unchanged(&self) {}
    fn writing(&self) -> bool {
        false
    }
    fn before(&self, _event: &EngineEvent) -> Result<Option<InstanceFields>> {
        Err(unreachable_here("before()"))
    }
    fn each_instance(&self, _visit: &mut dyn FnMut(StoredInstance) -> Result<()>) -> Result<()> {
        Err(unreachable_here("eachInstance"))
    }
    fn schedule(&self) -> (&str, Option<i64>) {
        ("", None)
    }
}

fn unreachable_here(what: &str) -> Error {
    Error::internal(format!("{what} is not part of this context"))
}

/// What every hook's context has: the behavior, its config and the call.
pub struct Scope<'a, C> {
    pub(crate) core: &'a dyn Core,
    pub(crate) config: &'a C,
}

impl<C> Clone for Scope<'_, C> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<C> Copy for Scope<'_, C> {}

impl<'a, C> Scope<'a, C> {
    /// The behavior's name.
    pub fn behavior(&self) -> &'a str {
        self.core.behavior()
    }

    /// The type's config of the behavior, as `parse_config` returned it.
    pub fn config(&self) -> &'a C {
        self.config
    }

    pub fn namespace(&self) -> &'a str {
        self.core.namespace()
    }

    pub fn schema(&self) -> &'a str {
        self.core.schema()
    }

    /// The live schema version the call runs with.
    pub fn version(&self) -> i64 {
        self.core.version()
    }

    /// Who the call acts for.
    pub fn principal(&self) -> &'a Principal {
        self.core.principal()
    }

    /// The engine clock's time for the call, in epoch milliseconds: one
    /// value for the whole call.
    pub fn now(&self) -> i64 {
        self.core.now()
    }

    /// Whether the principal holds a permission, as the engine's
    /// permission matcher answers; a service standing in for an end user
    /// holds none (D37).
    pub fn can(&self, permission: &str) -> Result<bool> {
        self.core.can(permission)
    }

    /// Other instances of the namespace, as the principal reaches them.
    pub fn instances(&self) -> InstancesHandle<'a> {
        InstancesHandle { core: self.core }
    }

    /// The configs of the behaviors other schemas compose.
    pub fn schemas(&self) -> SchemasHandle<'a> {
        SchemasHandle { core: self.core }
    }

    /// Checks a value against a type of the schema besides its instance
    /// type that the version's checks cover, as a nested value of the type
    /// is checked in an instance: every issue, none when it holds.
    pub fn validate(&self, type_name: &str, value: &Value) -> Result<Vec<ValidationIssue>> {
        self.core.validate_type(type_name, value)
    }

    /// Which database the call runs on.
    pub fn dialect(&self) -> Dialect {
        self.core.dialect()
    }

    /// Where the version 4 UUIDs the behavior makes of its own come from:
    /// the engine's seeded generator when it has one, random ones
    /// otherwise (D58).
    pub fn uuids(&self) -> UuidSource {
        self.core.uuids()
    }
}

/// How much of another instance a read returns: the behavior fields by
/// qualified name, every one when `None`, none for an empty list.
pub type ReadFields<'f> = Option<&'f [&'f str]>;

/// The namespace's instances as the call's principal reaches them. Each
/// read asks the access policy for read on the schema it names, and each
/// invoke what `instances.invoke` asks; reads, invokes, creates and
/// `call()` nest at most [`MAX_CALL_DEPTH`] deep together.
#[derive(Clone, Copy)]
pub struct InstancesHandle<'a> {
    core: &'a dyn Core,
}

impl InstancesHandle<'_> {
    /// An instance: its record with the behavior fields named (every one
    /// when `fields` is `None`); `None` when the namespace has none with
    /// the id. Inside a write it reads the instance as the call has left it.
    pub fn get(
        &self,
        schema: &str,
        id: &str,
        fields: ReadFields<'_>,
    ) -> Result<Option<InstanceRecord>> {
        if id.is_empty() {
            return Err(self.name_refusal("instances.get", "id"));
        }
        Ok(self
            .core
            .read(schema, &[id], fields, "instances.get")?
            .into_iter()
            .next()
            .map(|(_, record)| record))
    }

    /// The instances of one schema with the ids, at most [`MAX_BATCH_READ`],
    /// in one query and one policy question, by id, without the ids that
    /// have none.
    pub fn get_many(
        &self,
        schema: &str,
        ids: &[&str],
        fields: ReadFields<'_>,
    ) -> Result<Vec<(String, InstanceRecord)>> {
        let mut unique: Vec<&str> = Vec::new();
        for id in ids {
            if !unique.contains(id) {
                unique.push(id);
            }
        }
        if unique.len() > MAX_BATCH_READ {
            return Err(Error::behavior(
                self.core.behavior(),
                format!(
                    "instances.getMany reads at most {MAX_BATCH_READ} instances at once, not {}",
                    unique.len()
                ),
            ));
        }
        self.core.read(schema, &unique, fields, "instances.getMany")
    }

    /// Runs an instance operation of an instance as its caller would: the
    /// parameters, every guard, the handler, its result, and for a writing
    /// one the instance's afterChange, seq and event, in this call's
    /// transaction, in a savepoint. `preconditions` are checked and handed
    /// to its guards as a caller's are.
    pub fn invoke(
        &self,
        schema: &str,
        id: &str,
        operation: &str,
        params: &Value,
        preconditions: Option<&Value>,
    ) -> Result<Value> {
        for (name, value) in [("schema", schema), ("id", id), ("operation", operation)] {
            if value.is_empty() {
                return Err(self.name_refusal("instances.invoke", name));
            }
        }
        self.core
            .invoke(schema, id, operation, params, preconditions)
    }

    /// Runs a schema-level operation of a schema, as
    /// `engine.instances().invoke_schema` would.
    pub fn invoke_schema(&self, schema: &str, operation: &str, params: &Value) -> Result<Value> {
        for (name, value) in [("schema", schema), ("operation", operation)] {
            if value.is_empty() {
                return Err(self.name_refusal("instances.invokeSchema", name));
            }
        }
        self.core.invoke_schema(schema, operation, params)
    }

    /// Creates an instance of a schema, the namespace's own, as
    /// `engine.instances().create` would for the call's principal, in this
    /// call's transaction, in a savepoint. `behaviors` is the create's
    /// parameters by behavior name; `id` the engine's id source's when
    /// absent.
    pub fn create(
        &self,
        schema: &str,
        data: &Value,
        id: Option<&str>,
        behaviors: Option<&Value>,
    ) -> Result<InstanceRecord> {
        if schema.is_empty() {
            return Err(self.name_refusal("instances.create", "schema"));
        }
        if id.is_some_and(str::is_empty) {
            return Err(self.name_refusal("instances.create", "id"));
        }
        if !data.is_object() {
            return Err(Error::behavior(
                self.core.behavior(),
                "instances.create takes the instance's own fields: a JSON object",
            ));
        }
        if behaviors.is_some_and(|given| !given.is_object()) {
            return Err(Error::behavior(
                self.core.behavior(),
                "instances.create takes behaviors, the create's parameters by behavior name: a JSON object",
            ));
        }
        self.core.create(schema, data, id, behaviors)
    }

    fn name_refusal(&self, what: &str, name: &str) -> Error {
        Error::behavior(
            self.core.behavior(),
            format!(
                "{what} takes {}: a non-empty string",
                if name == "id" {
                    "an instance id".to_owned()
                } else {
                    format!("a {name} name")
                }
            ),
        )
    }
}

/// The schemas the namespace reaches, as the call's principal may read
/// them.
#[derive(Clone, Copy)]
pub struct SchemasHandle<'a> {
    core: &'a dyn Core,
}

impl SchemasHandle<'_> {
    /// The config of a behavior a schema's live version composes, as the
    /// schema holds it (`{}` when it gives none); `None` when it does not
    /// compose the behavior. Asks read on the schema, unless it is the
    /// call's own; `not_found` when the schema has no live version.
    pub fn config(&self, schema: &str, behavior: &str) -> Result<Option<Value>> {
        self.core.schema_config(schema, behavior)
    }

    /// Whether the principal may read a schema.
    pub fn readable(&self, schema: &str) -> Result<bool> {
        self.core.readable(schema)
    }
}

/// The behavior's own columns on one instance, to read.
#[derive(Clone, Copy)]
pub struct ColumnsReader<'a> {
    core: &'a dyn Core,
}

impl ColumnsReader<'_> {
    /// Every column the behavior's migrations add, by its own name for it.
    pub fn get(&self) -> Result<Row> {
        self.core.columns_get()
    }
}

/// The behavior's own columns on one instance, to read and write.
#[derive(Clone, Copy)]
pub struct Columns<'a> {
    core: &'a dyn Core,
}

impl Columns<'_> {
    /// Every column the behavior's migrations add, by its own name for it.
    pub fn get(&self) -> Result<Row> {
        self.core.columns_get()
    }

    /// Sets some of the behavior's columns; a name it did not declare is
    /// refused.
    pub fn set(&self, values: &[(&str, SqlValue)]) -> Result<()> {
        self.core.columns_set(values)
    }
}

/// SQL on the behavior's own tables, and the relation over the instances
/// of the call's schema, to read. Each call runs one statement, which the
/// engine checks first (`sql_refusal`).
#[derive(Clone, Copy)]
pub struct SqlReader<'a> {
    core: &'a dyn Core,
}

impl SqlReader<'_> {
    /// The SQL name of one of the behavior's tables, by its own name for it.
    pub fn table(&self, name: &str) -> Result<String> {
        self.core.sql_table(name)
    }

    /// The SQL name of the read-only relation over the instances of the
    /// call's schema in its namespace (`id`, `seq`, `version`,
    /// `created_at`, `created_by`, `updated_at`, `updated_by`, `data`, then
    /// the behavior's columns under its own names). Each statement that
    /// names it asks read on the schema, as the principal.
    pub fn instances(&self) -> Result<String> {
        self.core.sql_instances()
    }

    pub fn get<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Option<Row>> {
        self.core.sql_get(&sql.into(), params, false)
    }

    pub fn all<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Vec<Row>> {
        self.core.sql_all(&sql.into(), params, false)
    }

    /// The plan the database makes for a statement this reader may run,
    /// as it would run it: SQLite's `EXPLAIN QUERY PLAN` details, or the
    /// lines of Postgres's `EXPLAIN`. It shows whether a filter on the
    /// behavior's columns across the relation reaches the indexes its
    /// migrations list. Nothing runs; the statement is checked, and asks
    /// read for the relation, as it would be.
    pub fn explain<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Vec<String>> {
        self.core.sql_explain(&sql.into(), params)
    }

    /// [`SqlReader::get`] in a savepoint of its own, so its failure leaves
    /// the call running (D58: Postgres ends a transaction at a failure).
    pub fn try_get<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Option<Row>> {
        self.core.sql_get(&sql.into(), params, true)
    }

    /// [`SqlReader::all`] in a savepoint of its own.
    pub fn try_all<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<Vec<Row>> {
        self.core.sql_all(&sql.into(), params, true)
    }

    /// Matches an FTS5 expression over one of the behavior's full-text
    /// indexes, which a migration of its own created with
    /// `create_fulltext` (D58: full-text search is the engine's own, FTS5
    /// on SQLite and a `tsvector` with a GIN index on Postgres): the rows
    /// of its keys table the index rows match, best first, each with a
    /// snippet per column asked for. A caller's expression the index does
    /// not take comes back as the inner error, and the call goes on; the
    /// order and the snippets differ between the databases
    /// (`superschematic_engine_storage::fulltext`).
    pub fn fulltext(&self, query: &FullTextQuery<'_>) -> Result<FullTextMatches> {
        self.core.sql_fulltext(query)
    }

    /// Which database the statements run on.
    pub fn dialect(&self) -> Dialect {
        self.core.dialect()
    }
}

/// SQL on the behavior's own tables that writes, and the relation over the
/// instances to read.
#[derive(Clone, Copy)]
pub struct SqlWriter<'a> {
    reader: SqlReader<'a>,
}

impl<'a> Deref for SqlWriter<'a> {
    type Target = SqlReader<'a>;

    fn deref(&self) -> &SqlReader<'a> {
        &self.reader
    }
}

impl SqlWriter<'_> {
    /// Runs one INSERT, UPDATE, DELETE or REPLACE (or a read).
    pub fn run<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<RunResult> {
        self.reader.core.sql_run(&sql.into(), params, false)
    }

    /// [`SqlWriter::run`] in a savepoint of its own: a unique conflict comes
    /// back as an error and the call goes on.
    pub fn try_run<'s>(&self, sql: impl Into<Sql<'s>>, params: &[SqlValue]) -> Result<RunResult> {
        self.reader.core.sql_run(&sql.into(), params, true)
    }
}

/// The references a behavior recorded from its instance.
#[derive(Clone, Copy)]
pub struct ReferenceReader<'a> {
    core: &'a dyn Core,
}

impl ReferenceReader<'_> {
    /// Every reference, in the order recorded.
    pub fn list(&self) -> Result<Vec<Reference>> {
        self.core.references_list()
    }
}

/// Records and removes the behavior's references from its instance, so
/// the engine finds the instance when a referenced one changes or goes.
#[derive(Clone, Copy)]
pub struct References<'a> {
    core: &'a dyn Core,
}

impl References<'_> {
    /// Every reference, in the order recorded.
    pub fn list(&self) -> Result<Vec<Reference>> {
        self.core.references_list()
    }

    /// Records a reference to an instance of the namespace, under a key the
    /// behavior gives (`""` for none), hearing every change of it or what
    /// `hears` says. It asks read on the schema and refuses an instance
    /// that does not exist (`not_found`).
    pub fn add(&self, schema: &str, id: &str, key: &str, hears: Option<Hears>) -> Result<()> {
        self.core.references_add(schema, id, key, hears)
    }

    /// Removes a reference; false when there was none.
    pub fn remove(&self, schema: &str, id: &str, key: &str) -> Result<bool> {
        self.core.references_remove(schema, id, key)
    }
}

/// What the behavior's rows keep in the value store, to read.
#[derive(Clone, Copy)]
pub struct ValueReader<'a> {
    core: &'a dyn Core,
}

impl ValueReader<'_> {
    /// The object a behavior's row keeps, from the JSON text and the refs
    /// `stow` returned, each ref back to its value.
    pub fn load(&self, json: &str, refs: Option<&str>) -> Result<Map<String, Value>> {
        self.core.values_load(json, refs)
    }
}

/// The engine's value store for the objects the behavior keeps in its own
/// tables (D16, amended: a large value is stored once).
#[derive(Clone, Copy)]
pub struct Values<'a> {
    core: &'a dyn Core,
}

impl Values<'_> {
    /// The object a behavior's row keeps, each ref back to its value.
    pub fn load(&self, json: &str, refs: Option<&str>) -> Result<Map<String, Value>> {
        self.core.values_load(json, refs)
    }

    /// Stores an object for the behavior's row `key`, each top-level
    /// member longer than the threshold once by hash: the row keeps what it
    /// returns, and no longer what it held before.
    pub fn stow(&self, key: &str, object: &Map<String, Value>) -> Result<StowedObject> {
        self.core.values_stow(key, object)
    }

    /// Drops what the behavior's row `key` holds, or with no key every
    /// row's of the context's instance.
    pub fn release(&self, key: Option<&str>) -> Result<()> {
        self.core.values_release(key)
    }
}

/// A read of one instance: a guard's view and a field reader's.
pub struct InstanceView<'a, C> {
    pub(crate) scope: Scope<'a, C>,
}

impl<'a, C> Deref for InstanceView<'a, C> {
    type Target = Scope<'a, C>;

    fn deref(&self) -> &Scope<'a, C> {
        &self.scope
    }
}

impl<'a, C> InstanceView<'a, C> {
    pub fn id(&self) -> &'a str {
        self.scope.core.id()
    }

    /// The instance's own fields, without any behavior's: in an operation,
    /// as its `update()` calls leave them.
    pub fn data(&self) -> Result<Arc<Map<String, Value>>> {
        self.scope.core.data()
    }

    pub fn columns(&self) -> ColumnsReader<'a> {
        ColumnsReader {
            core: self.scope.core,
        }
    }

    pub fn sql(&self) -> SqlReader<'a> {
        SqlReader {
            core: self.scope.core,
        }
    }

    /// The references the behavior recorded from the instance.
    pub fn references(&self) -> ReferenceReader<'a> {
        ReferenceReader {
            core: self.scope.core,
        }
    }

    /// What the behavior's rows keep in the value store.
    pub fn values(&self) -> ValueReader<'a> {
        ValueReader {
            core: self.scope.core,
        }
    }
}

/// afterReferenceChange's context: a view of the referencing instance
/// whose `instances().invoke` also runs writing operations.
pub struct ReferenceCx<'a, C> {
    pub(crate) view: InstanceView<'a, C>,
}

impl<'a, C> Deref for ReferenceCx<'a, C> {
    type Target = InstanceView<'a, C>;

    fn deref(&self) -> &InstanceView<'a, C> {
        &self.view
    }
}

impl<C> ReferenceCx<'_, C> {
    /// Whether a write of the referencing instance is running up this call:
    /// invoking one of its writing operations now is a cycle.
    pub fn writing(&self) -> bool {
        self.view.scope.core.writing()
    }
}

/// A write to one instance: initialize, afterChange and a writing call. In
/// a read-only operation, `set()` and `run()` refuse and `call()` reaches
/// only read-only operations. After a delete, columns hold the values the
/// instance had, and `set()` and `call()` refuse.
pub struct InstanceCx<'a, C> {
    pub(crate) view: InstanceView<'a, C>,
}

impl<'a, C> Deref for InstanceCx<'a, C> {
    type Target = InstanceView<'a, C>;

    fn deref(&self) -> &InstanceView<'a, C> {
        &self.view
    }
}

impl<'a, C> InstanceCx<'a, C> {
    pub fn columns(&self) -> Columns<'a> {
        Columns {
            core: self.view.scope.core,
        }
    }

    pub fn sql(&self) -> SqlWriter<'a> {
        SqlWriter {
            reader: SqlReader {
                core: self.view.scope.core,
            },
        }
    }

    pub fn references(&self) -> References<'a> {
        References {
            core: self.view.scope.core,
        }
    }

    pub fn values(&self) -> Values<'a> {
        Values {
            core: self.view.scope.core,
        }
    }

    /// Calls an operation of a behavior the type composes, on this
    /// instance: its parameters checked, every guard in list order, then
    /// its handler, in a savepoint that rolls back alone when it fails.
    pub fn call(&self, behavior: &str, operation: &str, params: &Value) -> Result<Value> {
        self.view.scope.core.call(behavior, operation, params)
    }
}

/// An operation handler's context: a writing one can change the instance's
/// own fields as `instances.update` does.
pub struct OperationCx<'a, C> {
    pub(crate) cx: InstanceCx<'a, C>,
}

impl<'a, C> Deref for OperationCx<'a, C> {
    type Target = InstanceCx<'a, C>;

    fn deref(&self) -> &InstanceCx<'a, C> {
        &self.cx
    }
}

impl<C> OperationCx<'_, C> {
    /// Applies a JSON merge patch to the instance's own fields with the
    /// checks of an update, every guard asked with an update request whose
    /// caller is this behavior, and returns the fields after it. No event
    /// is appended: the operation's carries the change.
    pub fn update(&self, patch: &Value) -> Result<Arc<Map<String, Value>>> {
        self.cx.view.scope.core.update(patch, None)
    }

    /// `update(patch)`, saying where the values of the fields it writes
    /// came from (D16, amended): `attribution` is an object of entries by
    /// top-level own field, each `{ confidence?, source? }`, and names
    /// only fields the patch writes. `confidence` is a finite number from
    /// 0 to 1; `source` a string of at most 200 characters. The engine
    /// adds this behavior's name to each entry, and the operation's change
    /// and event carry the entries of the fields the call changed. A
    /// malformed attribution is a `BehaviorError`.
    pub fn update_attributed(
        &self,
        patch: &Value,
        attribution: &Value,
    ) -> Result<Arc<Map<String, Value>>> {
        self.cx.view.scope.core.update(patch, Some(attribution))
    }

    /// What `update(patch)` would refuse the patch for, without writing or
    /// asking a guard; empty when it would validate.
    pub fn validate_update(&self, patch: &Value) -> Result<Vec<ValidationIssue>> {
        self.cx.view.scope.core.validate_update(patch)
    }

    /// Says this call changed nothing: the engine appends no event and the
    /// instance keeps its seq; a call that wrote and says so is a defect.
    pub fn unchanged(&self) {
        self.cx.view.scope.core.unchanged();
    }
}

/// validate's context: the behavior, its config and the call. It reaches
/// no storage and no other instance.
pub struct ValidationCx<'a, C> {
    pub(crate) core: &'a dyn Core,
    pub(crate) config: &'a C,
}

impl<'a, C> ValidationCx<'a, C> {
    pub fn behavior(&self) -> &'a str {
        self.core.behavior()
    }

    pub fn config(&self) -> &'a C {
        self.config
    }

    pub fn namespace(&self) -> &'a str {
        self.core.namespace()
    }

    pub fn schema(&self) -> &'a str {
        self.core.schema()
    }

    pub fn version(&self) -> i64 {
        self.core.version()
    }

    pub fn id(&self) -> &'a str {
        self.core.id()
    }

    pub fn principal(&self) -> &'a Principal {
        self.core.principal()
    }

    pub fn now(&self) -> i64 {
        self.core.now()
    }

    /// Whether the principal holds a permission, as [`Scope::can`] answers.
    pub fn can(&self, permission: &str) -> Result<bool> {
        self.core.can(permission)
    }

    /// The issues of a value as a value of a type the behavior's
    /// `checked_types` names, each under `path`.
    pub fn check_type(
        &self,
        type_name: &str,
        value: &Value,
        path: &str,
    ) -> Result<Vec<ValidationIssue>> {
        self.core.check_type(type_name, value, path)
    }
}

/// A schema-level operation's context: the schema as a whole, with no
/// instance. A writing one's SQL writes the behavior's own tables.
pub struct SchemaCx<'a, C> {
    pub(crate) scope: Scope<'a, C>,
}

impl<'a, C> Deref for SchemaCx<'a, C> {
    type Target = Scope<'a, C>;

    fn deref(&self) -> &Scope<'a, C> {
        &self.scope
    }
}

impl<'a, C> SchemaCx<'a, C> {
    pub fn sql(&self) -> SqlWriter<'a> {
        SqlWriter {
            reader: SqlReader {
                core: self.scope.core,
            },
        }
    }

    /// The value store for the behavior's rows of the schema (instance
    /// `""`); a read-only operation's `stow` refuses.
    pub fn values(&self) -> Values<'a> {
        Values {
            core: self.scope.core,
        }
    }
}

/// A reaction's context (the runner is milestone 6).
pub struct ReactionCx<'a, C> {
    pub(crate) scope: Scope<'a, C>,
}

impl<'a, C> Deref for ReactionCx<'a, C> {
    type Target = Scope<'a, C>;

    fn deref(&self) -> &Scope<'a, C> {
        &self.scope
    }
}

impl<'a, C> ReactionCx<'a, C> {
    pub fn sql(&self) -> SqlReader<'a> {
        SqlReader {
            core: self.scope.core,
        }
    }

    pub fn values(&self) -> ValueReader<'a> {
        ValueReader {
            core: self.scope.core,
        }
    }

    /// The instance of an event as the log had it just before the event,
    /// as a read returns it; `None` when the event is its create. Asks read
    /// on the event's schema.
    pub fn before(&self, event: &EngineEvent) -> Result<Option<InstanceFields>> {
        self.scope.core.before(event)
    }
}

/// A schedule run's context (the runner is milestone 6).
pub struct ScheduleCx<'a, C> {
    pub(crate) scope: Scope<'a, C>,
}

impl<'a, C> Deref for ScheduleCx<'a, C> {
    type Target = Scope<'a, C>;

    fn deref(&self) -> &Scope<'a, C> {
        &self.scope
    }
}

impl<'a, C> ScheduleCx<'a, C> {
    pub fn sql(&self) -> SqlWriter<'a> {
        SqlWriter {
            reader: SqlReader {
                core: self.scope.core,
            },
        }
    }

    pub fn values(&self) -> Values<'a> {
        Values {
            core: self.scope.core,
        }
    }

    /// The schedule's name.
    pub fn schedule(&self) -> &'a str {
        self.scope.core.schedule().0
    }

    /// When its previous run committed; `None` before its first.
    pub fn previous(&self) -> Option<i64> {
        self.scope.core.schedule().1
    }
}

/// afterConfigChange's context: the schema's instances in one namespace,
/// in the publish's transaction. It has no principal and asks no policy.
pub struct PublishCx<'a, C> {
    pub(crate) core: &'a dyn Core,
    pub(crate) config: Option<&'a C>,
    pub(crate) before: Option<&'a C>,
}

impl<'a, C> PublishCx<'a, C> {
    pub fn behavior(&self) -> &'a str {
        self.core.behavior()
    }

    /// The config the published version gives the behavior; `None` when it
    /// no longer composes it.
    pub fn config(&self) -> Option<&'a C> {
        self.config
    }

    /// The config the version it replaces gave; `None` when that one did
    /// not compose it, or there was none.
    pub fn before(&self) -> Option<&'a C> {
        self.before
    }

    /// The namespace whose instances this call covers.
    pub fn namespace(&self) -> &'a str {
        self.core.namespace()
    }

    pub fn schema(&self) -> &'a str {
        self.core.schema()
    }

    /// The version being published.
    pub fn version(&self) -> i64 {
        self.core.version()
    }

    pub fn now(&self) -> i64 {
        self.core.now()
    }

    /// SQL on the behavior's own tables, writes included; not the relation
    /// over the instances.
    pub fn sql(&self) -> SqlWriter<'a> {
        SqlWriter {
            reader: SqlReader { core: self.core },
        }
    }

    /// The value store for the behavior's rows of the schema in the
    /// namespace (instance `""`).
    pub fn values(&self) -> Values<'a> {
        Values { core: self.core }
    }

    /// Visits every instance of the schema in the namespace, in creation
    /// order, 500 at a time, each with its own fields' values.
    pub fn each_instance(&self, mut visit: impl FnMut(StoredInstance) -> Result<()>) -> Result<()> {
        self.core.each_instance(&mut visit)
    }

    /// Checks a value against another type of the schema with the version
    /// being published.
    pub fn validate(&self, type_name: &str, value: &Value) -> Result<Vec<ValidationIssue>> {
        self.core.validate_type(type_name, value)
    }
}
