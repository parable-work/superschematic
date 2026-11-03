//! A behavior's namespace operations (D77, primitive 3): an operation
//! declared with `"scope": "namespace"` runs on the behavior's own tables
//! across every schema of a namespace that composes it, with no instance
//! and no schema. Its tables already hold every schema's rows, keyed by
//! namespace and schema (each behavior has one key per engine, "Storage"),
//! so "my mentions" or "what is unread" is one statement over them.
//!
//! A call (`engine.behaviors().invoke`, the `behaviors.invoke` call, `POST
//! /namespaces/{namespace}/behaviors/{behavior}/operations/{operation}` and
//! its MCP tool) is refused `not_found` for a behavior this engine does not
//! run and for an operation it does not declare with namespace scope. The
//! policy is asked a question of its own, `{ principal, action, namespace,
//! behavior, operation }` ([`crate::AccessRequest::Behavior`]), `write` or
//! `read` as the declaration says; then the parameters are held to the
//! operation's `paramsSchema` (`invalid_argument`). A writing one runs in a
//! write of its namespace, which an archived namespace refuses.
//!
//! The handler gets a [`NamespaceCx`]: the behavior, the namespace, the
//! principal, the call's time, `can()`, its SQL, and the namespace's
//! instances as the principal reaches them, for reads and read-only
//! operations only. Its SQL reads the behavior's tables, writes them in a
//! writing operation, and names `sql.readable()`, the schemas of the
//! namespace that compose the behavior and that the principal may read:
//! the policy is asked `read` once per such schema per call, and a schema
//! it refuses is left out of the relation, never refused. `configs()` gives
//! each composing schema's config of the behavior, asking nothing, for a
//! handler that decides by a schema's settings. It appends no
//! event and changes no instance: the SQL checks keep `engine_*` names out
//! of its statements as they do every behavior's, and a writing invoke, a
//! create, `update()` and `signal()` are a `BehaviorError`.
//!
//! A behavior no published schema composes has no storage yet; its first
//! namespace call creates it, as a publish would, so the call runs with
//! empty tables and an empty `readable()`.

use std::cell::OnceCell;
use std::sync::Arc;

use serde_json::{Map, Value};

use superschematic_engine_storage::behavior::{
    prefix_of, stored_key, BehaviorSql, ReadableRelation,
};
use superschematic_engine_storage::fulltext::{FullTextMatches, FullTextQuery};
use superschematic_engine_storage::{Dialect, Lock, Row, RunResult, Sql, SqlMode, SqlValue, Tx};

use super::context::Core;
use super::declaration::OperationScope;
use super::erased::ConfigBox;
use super::execution::{can, check_params, check_result, check_veto_code, values_load, Chain};
use super::registry::{ensure_storage, OperationSpec, RegisteredBehavior};
use super::Behaviors;
use crate::access::SchemaAction;
use crate::errors::{EngineError, EngineErrorCode, EngineErrorKind, Error, Result};
use crate::instances::reach::Reach;
use crate::instances::InstanceRecord;
use crate::namespaces::{NamespaceView, DEFAULT_NAMESPACE};
use crate::principal::{check_principal, Principal};
use crate::registry::Which;

/// One call of a namespace operation: `{ namespace?, behavior, operation,
/// params? }`, the namespace `default` when absent and the parameters `{}`.
#[derive(Clone, Copy, Debug)]
pub struct NamespaceCall<'a> {
    pub namespace: Option<&'a str>,
    pub behavior: &'a str,
    pub operation: &'a str,
    pub params: Option<&'a Value>,
}

impl Behaviors<'_> {
    /// Calls a behavior's namespace operation (D77) as `principal` and
    /// returns its result (the module's comment).
    pub fn invoke(&self, principal: &Principal, call: &NamespaceCall<'_>) -> Result<Value> {
        check_principal(principal)?;
        let engine = self.engine;
        let registry = engine.catalog.registry();
        let behavior = Arc::clone(registry.lookup(call.behavior).ok_or_else(|| {
            EngineError::new(
                EngineErrorCode::NotFound,
                format!("this engine runs no behavior {}", call.behavior),
            )
        })?);
        let index = namespace_operation(&behavior, call.operation)?;
        let spec = &behavior.operations[index];
        let empty = Value::Object(Map::new());
        let params = call
            .params
            .filter(|params| !params.is_null())
            .unwrap_or(&empty);
        // A read-only operation runs in a read, a writing one in a write of
        // its namespace, each checked again there; the first call of a
        // behavior with no storage creates it first, once the call is
        // allowed.
        let start = |tx: &Tx<'_>, run: bool| -> Result<Started> {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(call.namespace)?;
            engine.access.require_behavior(
                principal,
                action_of(spec),
                &namespace,
                &behavior.name,
                &spec.name,
            )?;
            if spec.writes {
                view.require_writable(&namespace)?;
            }
            let checked = check_params(&behavior, spec, params)?;
            let Some(key) = stored_key(tx, &behavior.name)? else {
                return Ok(Started::NoStorage);
            };
            if !run {
                return Ok(Started::Write);
            }
            let chain = Chain::new(principal, &namespace, engine.clock.now());
            let reach = Reach {
                engine,
                tx,
                view: &view,
                chain: &chain,
            };
            run_operation(&reach, &behavior, spec, &prefix_of(&key), &checked).map(Started::Ran)
        };
        let mut started = engine.read(|tx| start(tx, !spec.writes))?;
        if matches!(started, Started::NoStorage) {
            engine.write(Lock::Catalog, |tx| {
                ensure_storage(tx, &behavior, engine.clock.now()).map(drop)
            })?;
            started = if spec.writes {
                Started::Write
            } else {
                engine.read(|tx| start(tx, true))?
            };
        }
        match started {
            Started::Ran(result) => Ok(result),
            Started::Write => engine.write(
                Lock::Namespace(call.namespace.unwrap_or(DEFAULT_NAMESPACE)),
                |tx| match start(tx, true)? {
                    Started::Ran(result) => Ok(result),
                    _ => Err(Error::internal(format!(
                        "behavior {} lost its storage during a call",
                        behavior.name
                    ))),
                },
            ),
            Started::NoStorage => Err(Error::internal(format!(
                "behavior {} has no storage after the engine created it",
                behavior.name
            ))),
        }
    }
}

// What a namespace call's checks found: its result, a writing operation to
// run in a write, or a behavior with no storage yet.
enum Started {
    Ran(Value),
    Write,
    NoStorage,
}

fn action_of(spec: &OperationSpec) -> SchemaAction {
    if spec.writes {
        SchemaAction::Write
    } else {
        SchemaAction::Read
    }
}

// namespace_operation finds a behavior's namespace operation by name, or
// says why there is none: an operation of another scope, or none at all.
fn namespace_operation(behavior: &RegisteredBehavior, operation: &str) -> Result<usize> {
    let name = &behavior.name;
    match behavior
        .operations
        .iter()
        .position(|spec| spec.name == operation)
    {
        Some(index) if behavior.operations[index].scope == OperationScope::Namespace => Ok(index),
        Some(index) => Err(EngineError::new(
            EngineErrorCode::NotFound,
            if behavior.operations[index].scope == OperationScope::Schema {
                format!("{name}'s {operation} is a schema-level operation: call it on a schema that composes {name}")
            } else {
                format!("{name}'s {operation} is an instance operation: call it on an instance of a schema that composes {name}")
            },
        )
        .into()),
        None => {
            let known: Vec<&str> = behavior
                .operations
                .iter()
                .filter(|spec| spec.scope == OperationScope::Namespace)
                .map(|spec| spec.name.as_str())
                .collect();
            Err(EngineError::new(
                EngineErrorCode::NotFound,
                format!(
                    "behavior {name} has no namespace operation {operation} (its namespace operations: {})",
                    if known.is_empty() { "none".to_owned() } else { known.join(", ") }
                ),
            )
            .into())
        }
    }
}

// run_operation runs the handler in the call's transaction and holds what
// it returns to the declaration: a veto's code, and the result.
fn run_operation(
    reach: &Reach<'_, '_>,
    behavior: &RegisteredBehavior,
    spec: &OperationSpec,
    prefix: &str,
    checked: &Value,
) -> Result<Value> {
    let core = NamespaceCore {
        reach: *reach,
        behavior,
        prefix,
        writes: spec.writes,
        composing: OnceCell::new(),
        readable: OnceCell::new(),
    };
    let result = reach
        .chain
        .nest(&behavior.name, &format!("operation {}", spec.name), || {
            behavior
                .implementation
                .namespace_operation(&spec.name, &core, checked)
                .unwrap_or_else(|| {
                    Err(Error::internal(format!(
                        "behavior {} has no handler for operation {}",
                        behavior.name, spec.name
                    )))
                })
        });
    if let Err(Error::Engine(error)) = &result {
        if let EngineErrorKind::BehaviorVeto {
            behavior: vetoing,
            veto_code,
            ..
        } = error.kind()
        {
            if *vetoing == behavior.name {
                check_veto_code(behavior, veto_code.as_deref())?;
            }
        }
    }
    check_result(behavior, spec, result?)
}

// The schemas of a namespace call that compose the behavior, by name, each
// with the config it gives the behavior: every live schema the namespace
// reaches whose version composes it. A schema whose behaviors this engine
// cannot run is left out, as the tools document leaves it out.
fn composing_schemas(reach: &Reach<'_, '_>, behavior: &str) -> Result<Vec<(String, ConfigBox)>> {
    let engine = reach.engine;
    let namespace = reach.chain.namespace.as_str();
    let mut out = Vec::new();
    for summary in engine.catalog.list(reach.tx, reach.view, namespace)? {
        if summary.live_version.is_none() {
            continue;
        }
        let Some(record) =
            engine
                .catalog
                .find(reach.tx, reach.view, &summary.name, namespace, Which::Live)?
        else {
            continue;
        };
        let runtime = match engine.catalog.runtime_of(reach.tx, &record) {
            Ok(runtime) => runtime,
            Err(error) if error.code() == Some(EngineErrorCode::Unavailable) => continue,
            Err(error) => return Err(error),
        };
        if let Some(bound) = runtime.composition.bound(behavior) {
            out.push((summary.name, Arc::clone(&bound.config)));
        }
    }
    Ok(out)
}

// A namespace operation's context over the call's transaction.
struct NamespaceCore<'a, 't, 'db> {
    reach: Reach<'t, 'db>,
    behavior: &'a RegisteredBehavior,
    prefix: &'a str,
    writes: bool,
    // The schemas that compose the behavior, with their configs, read once
    // a call.
    composing: OnceCell<Vec<(String, ConfigBox)>>,
    // The schemas `sql.readable()` holds, asked of the policy once a call.
    readable: OnceCell<Vec<String>>,
}

impl NamespaceCore<'_, '_, '_> {
    fn name(&self) -> &str {
        &self.behavior.name
    }

    fn composing(&self) -> Result<&[(String, ConfigBox)]> {
        if let Some(known) = self.composing.get() {
            return Ok(known);
        }
        let schemas = composing_schemas(&self.reach, self.name())?;
        Ok(self.composing.get_or_init(|| schemas))
    }

    // readable_schemas are the composing schemas the principal may read,
    // the policy asked read once each a call; a refused one is left out.
    fn readable_schemas(&self) -> Result<Vec<String>> {
        if let Some(known) = self.readable.get() {
            return Ok(known.clone());
        }
        let engine = self.reach.engine;
        let namespace = self.reach.chain.namespace.as_str();
        let schemas: Vec<String> = self
            .composing()?
            .iter()
            .filter(|(schema, _)| {
                engine.access.allows(
                    &self.reach.chain.principal,
                    SchemaAction::Read,
                    namespace,
                    schema,
                )
            })
            .map(|(schema, _)| schema.clone())
            .collect();
        Ok(self.readable.get_or_init(|| schemas).clone())
    }

    fn with_sql<T>(
        &self,
        run: impl FnOnce(&BehaviorSql<'_, '_>) -> superschematic_engine_storage::Result<T>,
    ) -> Result<T> {
        let readable = ReadableRelation {
            schemas: Box::new(|| self.readable_schemas().map_err(Into::into)),
        };
        let sql = BehaviorSql::namespace(
            self.reach.tx,
            self.name(),
            self.prefix,
            if self.writes {
                SqlMode::Write
            } else {
                SqlMode::Read
            },
            &readable,
        );
        Ok(run(&sql)?)
    }

    // refuse is a BehaviorError for what a namespace operation cannot do.
    fn refuse(&self, what: &str) -> Error {
        Error::behavior(self.name(), format!("a namespace operation {what}"))
    }

    // read_only refuses an invoke of an operation that writes, found in the
    // scope it names.
    fn read_only(&self, schema: &str, operation: &str, scope: OperationScope) -> Result<()> {
        let found = self.reach.find_operation(schema, operation, scope)?;
        if found.spec().writes {
            return Err(self.refuse(&format!(
                "cannot invoke {operation} of {schema}, which writes: it changes no instance, and writes only its behavior's own tables"
            )));
        }
        Ok(())
    }
}

impl Core for NamespaceCore<'_, '_, '_> {
    fn behavior(&self) -> &str {
        self.name()
    }

    fn namespace(&self) -> &str {
        &self.reach.chain.namespace
    }

    fn schema(&self) -> &str {
        ""
    }

    fn version(&self) -> i64 {
        0
    }

    fn now(&self) -> i64 {
        self.reach.chain.now
    }

    fn principal(&self) -> &Principal {
        &self.reach.chain.principal
    }

    fn uuids(&self) -> crate::UuidSource {
        self.reach.engine.uuids()
    }

    fn can(&self, permission: &str) -> Result<bool> {
        can(&self.reach, self.name(), permission)
    }

    fn read(
        &self,
        schema: &str,
        ids: &[&str],
        fields: Option<&[&str]>,
        what: &str,
    ) -> Result<Vec<(String, InstanceRecord)>> {
        if schema.is_empty() {
            return Err(Error::behavior(
                self.name(),
                format!("{what} takes a schema name: a non-empty string"),
            ));
        }
        if fields.is_some_and(|fields| fields.iter().any(|field| !field.contains('.'))) {
            return Err(Error::behavior(
                self.name(),
                format!("{what}: fields is a list of behavior fields by qualified name, <behavior>.<field> (Workflow.status)"),
            ));
        }
        self.reach
            .chain
            .nest(self.name(), what, || self.reach.read(schema, ids, fields))
    }

    fn invoke(
        &self,
        schema: &str,
        id: &str,
        operation: &str,
        params: &Value,
        preconditions: Option<&Value>,
    ) -> Result<Value> {
        self.read_only(schema, operation, OperationScope::Instance)?;
        self.reach.chain.nest(self.name(), "instances.invoke", || {
            self.reach.invoke(
                self.name(),
                schema,
                id,
                operation,
                params,
                false,
                preconditions,
            )
        })
    }

    fn invoke_schema(&self, schema: &str, operation: &str, params: &Value) -> Result<Value> {
        self.read_only(schema, operation, OperationScope::Schema)?;
        self.reach
            .chain
            .nest(self.name(), "instances.invokeSchema", || {
                self.reach
                    .invoke_schema(self.name(), schema, operation, params, false)
            })
    }

    fn create(
        &self,
        schema: &str,
        _data: &Value,
        _id: Option<&str>,
        _behaviors: Option<&Value>,
    ) -> Result<InstanceRecord> {
        Err(self.refuse(&format!(
            "cannot create an instance of {schema}: it writes only its behavior's own tables"
        )))
    }

    fn schema_config(&self, _schema: &str, _behavior: &str) -> Result<Option<Value>> {
        Err(self
            .refuse("has no schema of its own, so no schemas.config(); its rows name their schema"))
    }

    fn readable(&self, schema: &str) -> Result<bool> {
        if schema.is_empty() {
            return Err(Error::behavior(
                self.name(),
                "schemas.readable takes a schema name: a non-empty string",
            ));
        }
        self.reach.readable(schema)
    }

    fn data(&self) -> Result<Arc<Map<String, Value>>> {
        Err(self.refuse("has no instance, so no data"))
    }

    fn columns_get(&self) -> Result<Row> {
        Err(self.refuse("has no instance, so no columns"))
    }

    fn columns_set(&self, _values: &[(&str, SqlValue)]) -> Result<()> {
        Err(self.refuse("has no instance, so no columns"))
    }

    fn dialect(&self) -> Dialect {
        self.reach.tx.dialect()
    }

    fn sql_table(&self, name: &str) -> Result<String> {
        self.with_sql(|handle| handle.table(name))
    }

    fn sql_instances(&self) -> Result<String> {
        Err(self.refuse(
            "has no schema, so no sql.instances(): join sql.readable() for the schemas the principal may read",
        ))
    }

    fn namespace_configs(&self) -> Result<&[(String, ConfigBox)]> {
        self.composing()
    }

    fn sql_readable(&self) -> Result<String> {
        self.with_sql(|handle| handle.readable())
    }

    fn sql_get(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<Option<Row>> {
        self.with_sql(|handle| {
            if tried {
                handle.try_get(sql, params)
            } else {
                handle.get(sql, params)
            }
        })
    }

    fn sql_all(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<Vec<Row>> {
        self.with_sql(|handle| {
            if tried {
                handle.try_all(sql, params)
            } else {
                handle.all(sql, params)
            }
        })
    }

    fn sql_explain(&self, sql: &Sql<'_>, params: &[SqlValue]) -> Result<Vec<String>> {
        self.with_sql(|handle| handle.explain(sql, params))
    }

    fn sql_run(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<RunResult> {
        if !self.writes {
            return Err(self.refuse(
                "that does not write cannot run a statement that writes; its declaration says \"writes\": true for run()",
            ));
        }
        self.with_sql(|handle| {
            if tried {
                handle.try_run(sql, params)
            } else {
                handle.run(sql, params)
            }
        })
    }

    fn sql_fulltext(&self, query: &FullTextQuery<'_>) -> Result<FullTextMatches> {
        self.with_sql(|handle| handle.fulltext(query))
    }

    fn values_load(&self, json: &str, refs: Option<&str>) -> Result<Map<String, Value>> {
        values_load(&self.reach, self.name(), json, refs)
    }

    fn values_stow(
        &self,
        _key: &str,
        _object: &Map<String, Value>,
    ) -> Result<super::behavior::StowedObject> {
        Err(self.refuse("cannot change what the value store holds (values.stow)"))
    }

    fn values_release(&self, _key: Option<&str>) -> Result<()> {
        Err(self.refuse("cannot change what the value store holds (values.release)"))
    }

    fn call(&self, behavior: &str, operation: &str, _params: &Value) -> Result<Value> {
        Err(self.refuse(&format!(
            "has no instance, so it cannot call() {behavior}.{operation}"
        )))
    }

    fn update(
        &self,
        _patch: &Value,
        _attribution: Option<&Value>,
    ) -> Result<Arc<Map<String, Value>>> {
        Err(self.refuse("has no instance to update()"))
    }

    fn validate_update(&self, _patch: &Value) -> Result<Vec<crate::errors::ValidationIssue>> {
        Err(self.refuse("has no instance to validateUpdate()"))
    }

    fn signal(&self, name: &str, _data: &Value) -> Result<()> {
        Err(self.refuse(&format!(
            "sends no signal ({name}): a signal is about one instance, and it has none"
        )))
    }
}
