//! What a behavior reaches beyond its own instance, the instance store's
//! half of `src/behaviors/execution.ts`'s `Reach` (D16, amended): it reads
//! other instances, invokes their operations and creates new ones as the
//! chain's principal, asking the access policy each time, records the
//! references behaviors hold, asks the guards of the behaviors that refer
//! to an instance before it changes and runs their hooks after, each
//! reference's as far as it hears the change, and refuses a delete that
//! leaves a reference to the deleted instance behind.
//!
//! Every method runs in the call's one transaction (D58): a [`Reach`] holds
//! `&Tx`, the engine, the namespaces the call read and its [`Chain`], and a
//! nested write is a savepoint on the same transaction.

use serde_json::{Map, Value};

use superschematic_engine_storage::{Sql, SqlValue, Tx};

use super::references::{IncomingReference, Move, Reference, ReferenceSource, ReferenceTable};
use super::{
    check_id, existing, insert, live, read_record, row, run_operation, write_own, InstanceRecord,
};
use crate::access::SchemaAction;
use crate::behaviors::behavior::{GuardRequest, InstanceChange, InstanceFields};
use crate::behaviors::composition::Composition;
use crate::behaviors::declaration::OperationScope;
use crate::behaviors::execution::{check_params, check_preconditions, Chain, Execution, Own};
use crate::behaviors::registry::OperationSpec;
use crate::engine::Engine;
use crate::errors::{EngineError, EngineErrorCode, Error, Result};
use crate::events::{log_floor, log_head, EngineEvent};
use crate::namespaces::NamespaceView;
use crate::reads::Reads;
use crate::registry::document::check_schema_name;
use crate::registry::{SchemaRecord, VersionRuntime, Which};
use crate::values::refs_of;
use std::sync::Arc;

/// What a call's behaviors reach: the engine, the call's transaction, the
/// namespaces it read and its chain.
#[derive(Clone, Copy)]
pub(crate) struct Reach<'t, 'db> {
    pub engine: &'t Engine,
    pub tx: &'t Tx<'db>,
    pub view: &'t NamespaceView<'t>,
    pub chain: &'t Chain,
}

/// An operation of a schema's live version, found: the version, its
/// runtime, and the operation's position on the type.
pub(crate) struct FoundOperation {
    pub record: Arc<SchemaRecord>,
    pub runtime: Arc<VersionRuntime>,
    /// The behavior's position on the type.
    pub index: usize,
    /// The operation's position among its behavior's.
    pub operation: usize,
}

impl FoundOperation {
    pub fn bound(&self) -> &crate::behaviors::composition::BoundBehavior {
        &self.runtime.composition.behaviors[self.index]
    }

    pub fn spec(&self) -> &OperationSpec {
        &self.bound().behavior.operations[self.operation]
    }
}

impl<'t, 'db> Reach<'t, 'db> {
    fn namespace(&self) -> &str {
        &self.chain.namespace
    }

    /// The live version of a schema the namespace reaches, or `not_found`.
    pub fn live(&self, schema: &str) -> Result<Arc<SchemaRecord>> {
        live(
            &self.engine.catalog,
            self.tx,
            self.view,
            self.namespace(),
            schema,
        )
    }

    /// The runtime of a published version.
    pub fn runtime(&self, record: &SchemaRecord) -> Result<Arc<VersionRuntime>> {
        self.engine.catalog.runtime_of(self.tx, record)
    }

    /// An operation of a schema's live version with a scope. Without one, a
    /// principal that may read the schema learns there is none, and one
    /// that may not is refused.
    pub fn find_operation(
        &self,
        schema: &str,
        operation: &str,
        scope: OperationScope,
    ) -> Result<FoundOperation> {
        let principal = &self.chain.principal;
        let namespace = self.namespace();
        let access = &self.engine.access;
        let Some(record) =
            self.engine
                .catalog
                .find(self.tx, self.view, schema, namespace, Which::Live)?
        else {
            access.require(principal, SchemaAction::Read, namespace, schema)?;
            return Err(EngineError::new(
                EngineErrorCode::NotFound,
                format!("schema {schema} has no live version in namespace {namespace}"),
            )
            .into());
        };
        let runtime = self.runtime(&record)?;
        let found = runtime
            .composition
            .behaviors
            .iter()
            .enumerate()
            .find_map(|(index, bound)| {
                bound
                    .behavior
                    .operations
                    .iter()
                    .position(|spec| spec.name == operation)
                    .map(|position| (index, position))
            });
        let schema_level = scope == OperationScope::Schema;
        match found {
            Some((index, position))
                if runtime.composition.behaviors[index].behavior.operations[position].scope
                    == scope =>
            {
                Ok(FoundOperation {
                    record,
                    runtime,
                    index,
                    operation: position,
                })
            }
            Some((index, position)) => {
                access.require(principal, SchemaAction::Read, namespace, schema)?;
                let name = &runtime.composition.behaviors[index].behavior.operations[position].name;
                Err(EngineError::new(
                    EngineErrorCode::NotFound,
                    if schema_level {
                        format!("{schema}'s {name} is an instance operation: call it on an instance")
                    } else {
                        format!("{schema}'s {name} is a schema-level operation: call it on the schema, with no instance")
                    },
                )
                .into())
            }
            None => {
                access.require(principal, SchemaAction::Read, namespace, schema)?;
                let known: Vec<&str> = runtime
                    .composition
                    .operations()
                    .filter(|(_, spec)| spec.scope == scope)
                    .map(|(_, spec)| spec.name.as_str())
                    .collect();
                let level = if schema_level { "schema-level " } else { "" };
                Err(EngineError::new(
                    EngineErrorCode::NotFound,
                    format!(
                        "schema {schema} has no {level}operation {operation} (its behaviors' {level}operations: {})",
                        if known.is_empty() { "none".to_owned() } else { known.join(", ") }
                    ),
                )
                .into())
            }
        }
    }

    /// Reads instances of a schema for a behavior, with the named behavior
    /// fields (every one when `None`): read is asked once.
    pub fn read(
        &self,
        schema: &str,
        ids: &[&str],
        fields: Option<&[&str]>,
    ) -> Result<Vec<(String, InstanceRecord)>> {
        check_schema_name(schema)?;
        self.engine.access.require(
            &self.chain.principal,
            SchemaAction::Read,
            self.namespace(),
            schema,
        )?;
        let record = self.live(schema)?;
        let runtime = self.runtime(&record)?;
        if ids.is_empty() {
            return Ok(Vec::new());
        }
        let rows = Reads::instances(self.tx, self.namespace(), schema, ids, &runtime)?;
        let mut found = Vec::new();
        for id in ids {
            for row in &rows {
                if row.text("id")? == *id {
                    found.push((
                        (*id).to_owned(),
                        read_record(self, &runtime, &record, row, fields, false)?,
                    ));
                }
            }
        }
        Ok(found)
    }

    /// Runs an instance operation a behavior invokes, as the chain's
    /// principal, with the preconditions it gives, in the chain's
    /// transaction: a savepoint for a writing one, which a write running
    /// up the chain on the same instance refuses as a cycle.
    #[allow(clippy::too_many_arguments)]
    pub fn invoke(
        &self,
        from: &str,
        schema: &str,
        id: &str,
        operation: &str,
        params: &Value,
        writes: bool,
        given: Option<&Value>,
    ) -> Result<Value> {
        check_schema_name(schema)?;
        let found = self.find_operation(schema, operation, OperationScope::Instance)?;
        let spec = found.spec();
        if spec.writes && !writes {
            return Err(Error::behavior(
                from,
                format!(
                    "a read cannot invoke {} of {schema}, which writes; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can",
                    spec.name
                ),
            ));
        }
        self.engine.access.require_operation(
            &self.chain.principal,
            if spec.writes {
                SchemaAction::Write
            } else {
                SchemaAction::Read
            },
            self.namespace(),
            schema,
            Some(&spec.name),
        )?;
        let checked = check_params(&found.bound().behavior, spec, params)?;
        let preconditions = check_preconditions(&found.runtime.composition, schema, given)?;
        if !spec.writes {
            let row = existing(self.tx, self.namespace(), schema, id)?;
            let execution = Execution::new(
                *self,
                Arc::clone(&found.runtime),
                &found.record.name,
                found.record.version.unwrap_or_default(),
                id,
                Own::Row(row),
                false,
            );
            return execution.invoke(found.bound(), spec, &checked, None, preconditions.as_ref());
        }
        if self.chain.writing(schema, id) {
            return Err(Error::behavior(
                from,
                format!(
                    "invoking {} of {schema} {id} is a cycle: a write of {schema} {id} is still running up this call",
                    spec.name
                ),
            ));
        }
        self.tx
            .savepoint(|_| run_operation(self, &found, id, &checked, None, preconditions.as_ref()))
            .map(|(result, _)| result)
    }

    /// Runs a schema-level operation a behavior invokes, as the chain's
    /// principal, in the chain's transaction; from a read, only a read-only
    /// one.
    pub fn invoke_schema(
        &self,
        from: &str,
        schema: &str,
        operation: &str,
        params: &Value,
        writes: bool,
    ) -> Result<Value> {
        check_schema_name(schema)?;
        let found = self.find_operation(schema, operation, OperationScope::Schema)?;
        let spec = found.spec();
        if spec.writes && !writes {
            return Err(Error::behavior(
                from,
                format!(
                    "a read cannot invoke {} of {schema}, which writes; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can",
                    spec.name
                ),
            ));
        }
        self.engine.access.require_operation(
            &self.chain.principal,
            if spec.writes {
                SchemaAction::Write
            } else {
                SchemaAction::Read
            },
            self.namespace(),
            schema,
            Some(&spec.name),
        )?;
        let checked = check_params(&found.bound().behavior, spec, params)?;
        self.run_schema_operation(&found, &checked)
    }

    /// Runs a schema-level operation's handler, in a savepoint for a
    /// writing one.
    pub fn run_schema_operation(&self, found: &FoundOperation, checked: &Value) -> Result<Value> {
        let run = || {
            Execution::new(
                *self,
                Arc::clone(&found.runtime),
                &found.record.name,
                found.record.version.unwrap_or_default(),
                "",
                Own::None,
                found.spec().writes,
            )
            .invoke_schema_operation(found.bound(), found.spec(), checked)
        };
        if found.spec().writes {
            self.tx.savepoint(|_| run())
        } else {
            run()
        }
    }

    /// Creates an instance a behavior asks for, as the chain's principal,
    /// who needs write on the schema, in a savepoint. A read cannot create,
    /// and neither can a call up which the same instance is being written.
    pub fn create(
        &self,
        from: &str,
        schema: &str,
        data: &Value,
        id: Option<&str>,
        behaviors: Option<&Value>,
        writes: bool,
    ) -> Result<InstanceRecord> {
        if !writes {
            return Err(Error::behavior(
                from,
                format!("a read cannot create an instance of {schema}; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can"),
            ));
        }
        check_schema_name(schema)?;
        self.engine.access.require(
            &self.chain.principal,
            SchemaAction::Write,
            self.namespace(),
            schema,
        )?;
        let instance_id = match id {
            Some(id) => id.to_owned(),
            None => self.engine.new_id()?,
        };
        check_id(&instance_id)?;
        if self.chain.writing(schema, &instance_id) {
            return Err(Error::behavior(
                from,
                format!("creating {schema} {instance_id} is a cycle: a write of {schema} {instance_id} is still running up this call"),
            ));
        }
        self.tx
            .savepoint(|_| insert(self, schema, &instance_id, data, behaviors))
    }

    /// Asks read on a schema, as a read of its instances does.
    pub fn allow_read(&self, schema: &str) -> Result<()> {
        check_schema_name(schema)?;
        Ok(self.engine.access.require(
            &self.chain.principal,
            SchemaAction::Read,
            self.namespace(),
            schema,
        )?)
    }

    /// Whether the principal may read a schema.
    pub fn readable(&self, schema: &str) -> Result<bool> {
        check_schema_name(schema)?;
        Ok(self.engine.access.allows(
            &self.chain.principal,
            SchemaAction::Read,
            self.namespace(),
            schema,
        ))
    }

    /// The instance of an event as the log had it just before the event,
    /// `{ data, behaviors }`, folded from its events before it (from the
    /// base retention kept, when it pruned some); asks read on its schema.
    /// A beside operation's event (D77) has no sequence, and the instance
    /// before it is the one its sequenced events before it in the log
    /// leave.
    pub fn before(&self, from: &str, event: &EngineEvent) -> Result<Option<InstanceFields>> {
        let Some(instance_id) = &event.instance_id else {
            return Err(Error::behavior(
                from,
                "before() takes an instance event of the log",
            ));
        };
        if event.seq.is_none() && !event.beside {
            return Err(Error::behavior(
                from,
                "before() takes an instance event of the log",
            ));
        }
        let namespace = self.namespace();
        if event.namespace != namespace {
            return Err(Error::behavior(
                from,
                format!(
                    "before() reads the events of namespace {namespace}, not {}",
                    event.namespace
                ),
            ));
        }
        check_schema_name(&event.schema)?;
        self.engine.access.require(
            &self.chain.principal,
            SchemaAction::Read,
            namespace,
            &event.schema,
        )?;
        let key = [
            SqlValue::from(namespace),
            SqlValue::from(event.schema.as_str()),
            SqlValue::from(instance_id.as_str()),
        ];
        let base = self.tx.get(
            "SELECT seq, data, value_refs FROM engine_event_bases WHERE namespace = ? AND schema = ? AND instance_id = ?",
            &key,
        )?;
        let mut data: Option<Value> = None;
        if let Some(base) = &base {
            let pruned = match event.seq {
                Some(seq) => seq <= base.i64("seq")?,
                None => event.cursor <= log_floor(self.tx, self.view, namespace, false)?,
            };
            if pruned {
                let floor = log_floor(self.tx, self.view, namespace, false)?;
                return Err(EngineError::cursor_expired(
                    namespace,
                    event.cursor,
                    floor,
                    log_head(self.tx)?,
                )
                .into());
            }
            if let Some(text) = base.opt_text("data")? {
                let stored: Value = serde_json::from_str(text).map_err(|error| {
                    Error::internal(format!("an event base is not JSON: {error}"))
                })?;
                data = Some(self.engine.values.fill(
                    self.tx,
                    &stored,
                    &refs_of(base.opt_text("value_refs")?)?,
                )?);
            }
        }
        let mut params = key.to_vec();
        let rows = match event.seq {
            Some(seq) => {
                params.push(SqlValue::Integer(seq));
                self.tx.all(
                    Sql::per(
                        "SELECT kind, change, value_refs FROM engine_events INDEXED BY engine_events_instance
                 WHERE namespace = ? AND schema = ? AND instance_id = ? AND seq < ? ORDER BY seq",
                        "SELECT kind, change, value_refs FROM engine_events
                 WHERE namespace = ? AND schema = ? AND instance_id = ? AND seq < ? ORDER BY seq",
                    ),
                    &params,
                )?
            }
            None => {
                params.push(SqlValue::Integer(event.cursor));
                self.tx.all(
                    Sql::per(
                        "SELECT kind, change, value_refs FROM engine_events INDEXED BY engine_events_instance_cursor
                 WHERE namespace = ? AND schema = ? AND instance_id = ? AND cursor < ? AND seq IS NOT NULL ORDER BY cursor",
                        "SELECT kind, change, value_refs FROM engine_events
                 WHERE namespace = ? AND schema = ? AND instance_id = ? AND cursor < ? AND seq IS NOT NULL ORDER BY cursor",
                    ),
                    &params,
                )?
            }
        };
        for row in rows {
            let change = match row.opt_text("change")? {
                None => Value::Null,
                Some(text) => {
                    let stored: Value = serde_json::from_str(text).map_err(|error| {
                        Error::internal(format!("an event's change is not JSON: {error}"))
                    })?;
                    self.engine.values.fill(
                        self.tx,
                        &stored,
                        &refs_of(row.opt_text("value_refs")?)?,
                    )?
                }
            };
            data = fold_event(data, row.text("kind")?, change);
        }
        Ok(data.map(|folded| InstanceFields {
            data: folded
                .get("data")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default(),
            behaviors: folded
                .get("behaviors")
                .and_then(Value::as_object)
                .cloned()
                .unwrap_or_default(),
        }))
    }

    /// The config a schema's live version gives a behavior, as the schema
    /// holds it; read is asked unless the schema is the call's own.
    pub fn config(&self, own: &str, schema: &str, behavior: &str) -> Result<Option<Value>> {
        check_schema_name(schema)?;
        if schema != own {
            self.engine.access.require(
                &self.chain.principal,
                SchemaAction::Read,
                self.namespace(),
                schema,
            )?;
        }
        let record = self.live(schema)?;
        Ok(
            crate::behaviors::composition::refs_of(&record.document, &record.instance_type)
                .into_iter()
                .find(|entry| entry.name == behavior)
                .map(|entry| {
                    entry
                        .config
                        .cloned()
                        .unwrap_or_else(|| Value::Object(Map::new()))
                }),
        )
    }

    /// Records a behavior's reference to an instance of the namespace,
    /// which must exist as the principal reads it.
    pub fn add_reference(&self, source: &ReferenceSource, target: &Reference) -> Result<()> {
        check_schema_name(&target.schema)?;
        self.engine.access.require(
            &self.chain.principal,
            SchemaAction::Read,
            self.namespace(),
            &target.schema,
        )?;
        if row(self.tx, self.namespace(), &target.schema, &target.id)?.is_none() {
            return Err(EngineError::new(
                EngineErrorCode::NotFound,
                format!(
                    "{} {} does not exist in namespace {}",
                    target.schema,
                    target.id,
                    self.namespace()
                ),
            )
            .into());
        }
        ReferenceTable::add(self.tx, self.namespace(), source, target)
    }

    /// Asks the guardReference of each behavior that refers to an instance,
    /// on its own instance, whoever the caller: before a delete every
    /// reference, before another change the ones that hear every change.
    pub fn guard_references(
        &self,
        schema: &str,
        id: &str,
        request: &GuardRequest<'_>,
    ) -> Result<()> {
        let asked = if matches!(request, GuardRequest::Delete { .. }) {
            ReferenceTable::to(self.tx, self.namespace(), schema, id)?
        } else {
            ReferenceTable::guarding(self.tx, self.namespace(), schema, id)?
        };
        for incoming in asked {
            let Some((execution, index)) = self.source(&incoming)? else {
                continue;
            };
            if !execution.runtime.composition.behaviors[index]
                .behavior
                .hooks
                .guard_reference
            {
                continue;
            }
            let target = Reference {
                schema: schema.to_owned(),
                id: id.to_owned(),
                key: incoming.key.clone(),
                hears: incoming.hears.clone(),
            };
            let veto = self.chain.nest(&incoming.behavior, "guardReference", || {
                execution.guard_reference(index, &target, request)
            })?;
            if let Some(veto) = veto {
                return Err(Error::veto(
                    &incoming.behavior,
                    request.action(),
                    schema,
                    id,
                    veto,
                ));
            }
        }
        Ok(())
    }

    /// Runs the afterReferenceChange of each behavior that refers to an
    /// instance, after its change: after a delete every reference, after
    /// another change the ones that hear the values it moved. After a
    /// delete, a reference left behind by a behavior its source still
    /// composes refuses the delete; one a behavior the source no longer
    /// composes left is dropped.
    pub fn referenced(
        &self,
        schema: &str,
        id: &str,
        change: &InstanceChange<'_>,
        moved: &[Move],
    ) -> Result<()> {
        let namespace = self.namespace();
        let deleted = matches!(change, InstanceChange::Delete);
        let hearing = if deleted {
            ReferenceTable::to(self.tx, namespace, schema, id)?
        } else {
            ReferenceTable::hearing(self.tx, namespace, schema, id, moved)?
        };
        for incoming in hearing {
            let target = Reference {
                schema: schema.to_owned(),
                id: id.to_owned(),
                key: incoming.key.clone(),
                hears: incoming.hears.clone(),
            };
            let source = ReferenceSource {
                schema: incoming.schema.clone(),
                id: incoming.id.clone(),
                behavior: incoming.behavior.clone(),
            };
            // An earlier hook may have removed this reference.
            if !ReferenceTable::has(self.tx, namespace, &source, &target)? {
                continue;
            }
            let Some((execution, index)) = self.source(&incoming)? else {
                continue;
            };
            if !execution.runtime.composition.behaviors[index]
                .behavior
                .hooks
                .after_reference_change
            {
                continue;
            }
            self.chain
                .nest(&incoming.behavior, "afterReferenceChange", || {
                    execution.after_reference_change(index, &target, change)
                })?;
        }
        if !deleted {
            return Ok(());
        }
        for incoming in ReferenceTable::to(self.tx, namespace, schema, id)? {
            if self.source(&incoming)?.is_none() {
                ReferenceTable::drop_one(self.tx, namespace, schema, id, &incoming)?;
                continue;
            }
            return Err(Error::behavior(
                &incoming.behavior,
                format!(
                    "{} {} still refers to {schema} {id}, which is deleted: afterReferenceChange must remove the reference, through an operation of {} it invokes",
                    incoming.schema, incoming.id, incoming.schema
                ),
            ));
        }
        Ok(())
    }

    // source is the instance a reference starts at, read for its behavior's
    // guard or hook; None when its live version no longer composes the
    // behavior, or the instance is gone.
    fn source(&self, incoming: &IncomingReference) -> Result<Option<(Execution<'t, 'db>, usize)>> {
        let Some(record) = self.engine.catalog.find(
            self.tx,
            self.view,
            &incoming.schema,
            self.namespace(),
            Which::Live,
        )?
        else {
            return Ok(None);
        };
        let runtime = self.runtime(&record)?;
        let Some(index) = runtime.composition.position(&incoming.behavior) else {
            return Ok(None);
        };
        let Some(found) = row(self.tx, self.namespace(), &incoming.schema, &incoming.id)? else {
            return Ok(None);
        };
        Ok(Some((
            Execution::new(
                *self,
                runtime,
                &record.name,
                record.version.unwrap_or_default(),
                &incoming.id,
                Own::Row(found),
                false,
            ),
            index,
        )))
    }

    /// Stores an instance's own fields after an operation's update(), each
    /// large member in the value store; a member whose key is not in
    /// `changed` keeps the ref the row holds.
    pub fn write_own(
        &self,
        schema: &str,
        id: &str,
        data: &Map<String, Value>,
        changed: &std::collections::BTreeSet<String>,
    ) -> Result<()> {
        let record = self.live(schema)?;
        let runtime = self.runtime(&record)?;
        write_own(
            self.engine,
            self.tx,
            &runtime,
            self.namespace(),
            schema,
            id,
            data,
            changed,
        )
    }

    /// The composition of a schema's live version.
    #[allow(dead_code)] // the runner (milestone 6) reads it
    pub fn composition(&self, schema: &str) -> Result<Composition> {
        Ok(self.runtime(&*self.live(schema)?)?.composition.clone())
    }
}

/// An instance as the log held it after one more of its events: a create
/// is the instance, an update and an operation merge their patch into it,
/// a delete leaves nothing.
pub(crate) fn fold_event(data: Option<Value>, kind: &str, change: Value) -> Option<Value> {
    let base = || data.clone().unwrap_or_else(|| Value::Object(Map::new()));
    match kind {
        "create" => Some(change),
        "update" => Some(super::patch::merge_patch(&base(), &change)),
        "operation" => Some(super::patch::merge_patch(
            &base(),
            change.get("patch").unwrap_or(&Value::Null),
        )),
        _ => None,
    }
}
