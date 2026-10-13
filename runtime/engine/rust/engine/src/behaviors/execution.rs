//! Running a type's behaviors for one call on one instance, the port of
//! `src/behaviors/execution.ts`: its guards, its initializers, an
//! operation and the operations it calls, its after-change hooks and its
//! field readers. The instance store makes one [`Execution`] per create,
//! read, update, delete or operation, inside the write transaction for a
//! write, and one per other instance a behavior reads or invokes. They
//! share the call's [`Chain`]: its principal, namespace and time, how deep
//! calls nest and which instances are being written.
//!
//! Every part of a call shares one transaction (D58): the store's `Reach`
//! holds `&Tx`, `&Engine` and the namespaces it read, a nested call is a
//! savepoint on the same transaction, and what a value driver leaves is
//! settled when the outer write ends. A hook gets a context ([`context`])
//! over a `Ctx`, which answers for one behavior in one moment: a guard's
//! view, a field reader's, initialize's and afterChange's writable
//! context, a read-only operation's, an operation's, a schema-level
//! operation's, a reaction's or a schedule's.
//!
//! A guard vetoes with a [`Veto`] whose code its declaration lists; a
//! handler, initialize and afterChange refuse with [`Error::veto`], held to
//! the same list (`declared_vetoes`). A call's preconditions, by behavior,
//! are checked against each behavior's preconditionSchema and handed to
//! that behavior's guard; a request a behavior's own code makes carries
//! none. Every write of an instance's own fields asks each behavior's
//! validate about the fields it would store once the live version accepts
//! them, before any guard.

use std::cell::{Cell, RefCell};
use std::sync::Arc;

use serde_json::{Map, Value};

use superschematic_engine_storage::behavior::{instance_column, BehaviorSql, InstanceRelation};
use superschematic_engine_storage::{
    quote_identifier, Dialect, Row, RunResult, Sql, SqlMode, SqlValue,
};

use super::behavior::{
    GuardRequest, InstanceChange, InstanceFields, StowedObject, ValidationRequest,
};
use super::composition::BoundBehavior;
use super::context::{Core, MAX_CALL_DEPTH};
use super::registry::{detail, issues, OperationSpec, RegisteredBehavior};
use crate::errors::{EngineError, Error, Result, SchemaIssue, ValidationIssue, Veto};
use crate::events::{EngineEvent, EventCause};
use crate::instances::patch::merge_patch;
use crate::instances::reach::Reach;
use crate::instances::references::{Hears, Reference, ReferenceSource, ReferenceTable};
use crate::instances::InstanceRecord;
use crate::json;
use crate::principal::Principal;
use crate::registry::{NormalizeMode, VersionRuntime};
use crate::values::{refs_of, refs_text, ValueHolder};

/// One engine call across every instance it reaches: who acts, in which
/// namespace, at what time, how deep calls nest, which instances have a
/// write running, and for the runner's work the cause the events it
/// writes record.
pub(crate) struct Chain {
    pub principal: Principal,
    pub namespace: String,
    /// The clock's time for the whole call.
    pub now: i64,
    pub cause: Option<EventCause>,
    depth: Cell<usize>,
    writes: RefCell<Vec<(String, String)>>,
}

impl Chain {
    pub fn new(principal: &Principal, namespace: &str, now: i64) -> Self {
        Chain {
            principal: principal.clone(),
            namespace: namespace.to_owned(),
            now,
            cause: None,
            depth: Cell::new(0),
            writes: RefCell::new(Vec::new()),
        }
    }

    /// The chain of the runner's work: a reaction to an event or a
    /// schedule's run, whose events record `cause`.
    pub fn caused(principal: &Principal, namespace: &str, now: i64, cause: EventCause) -> Self {
        Chain {
            cause: Some(cause),
            ..Chain::new(principal, namespace, now)
        }
    }

    /// Runs `f` one level deeper, refusing past [`MAX_CALL_DEPTH`] with a
    /// BehaviorError of `behavior`.
    pub fn nest<T>(&self, behavior: &str, what: &str, f: impl FnOnce() -> Result<T>) -> Result<T> {
        let depth = self.depth.get();
        if depth >= MAX_CALL_DEPTH {
            return Err(Error::behavior(
                behavior,
                format!("{what}: calls nest more than {MAX_CALL_DEPTH} deep"),
            ));
        }
        self.depth.set(depth + 1);
        let result = f();
        self.depth.set(depth);
        result
    }

    /// Whether an instance has a write running up the chain.
    pub fn writing(&self, schema: &str, id: &str) -> bool {
        self.writes
            .borrow()
            .iter()
            .any(|(each, which)| each == schema && which == id)
    }

    /// Runs `f` as the write of an instance: while it runs, invoking a
    /// writing operation of it is a cycle.
    pub fn write<T>(&self, schema: &str, id: &str, f: impl FnOnce() -> Result<T>) -> Result<T> {
        if self.writing(schema, id) {
            return f();
        }
        self.writes
            .borrow_mut()
            .push((schema.to_owned(), id.to_owned()));
        let result = f();
        self.writes
            .borrow_mut()
            .retain(|(each, which)| !(each == schema && which == id));
        result
    }
}

/// A call's preconditions, checked: each behavior's entry by its name.
pub(crate) type Preconditions = Vec<(String, Value)>;

/// The instance's own fields an execution starts with: a copy, or the row
/// it reads them from when a behavior first does.
pub(crate) enum Own {
    Loaded(Map<String, Value>),
    Row(Row),
    /// A schema-level execution, with no instance.
    None,
}

/// The behaviors of one version on one instance during a call, or on the
/// schema as a whole for a schema-level operation and the runner's work.
pub(crate) struct Execution<'t, 'db> {
    pub reach: Reach<'t, 'db>,
    pub runtime: Arc<VersionRuntime>,
    pub schema: String,
    pub version: i64,
    /// The instance's id; `""` for a schema-level execution.
    pub id: String,
    data: RefCell<Option<Arc<Map<String, Value>>>>,
    loader: Option<Row>,
    // The own fields before the call's first update(), once one has run.
    initial: RefCell<Option<Arc<Map<String, Value>>>>,
    // Each behavior's columns as the instance had them before its delete.
    deleted: RefCell<Option<Vec<Row>>>,
    /// False for a read and a read-only operation: nothing may write.
    writable: bool,
}

impl<'t, 'db> Execution<'t, 'db> {
    pub fn new(
        reach: Reach<'t, 'db>,
        runtime: Arc<VersionRuntime>,
        schema: &str,
        version: i64,
        id: &str,
        own: Own,
        writable: bool,
    ) -> Self {
        let (data, loader) = match own {
            Own::Loaded(data) => (Some(Arc::new(data)), None),
            Own::Row(row) => (None, Some(row)),
            Own::None => (Some(Arc::new(Map::new())), None),
        };
        Execution {
            reach,
            runtime,
            schema: schema.to_owned(),
            version,
            id: id.to_owned(),
            data: RefCell::new(data),
            loader,
            initial: RefCell::new(None),
            deleted: RefCell::new(None),
            writable,
        }
    }

    fn behaviors(&self) -> &[BoundBehavior] {
        &self.runtime.composition.behaviors
    }

    /// The instance's own fields as the call has left them, read from the
    /// row when first asked.
    pub fn data(&self) -> Result<Arc<Map<String, Value>>> {
        if let Some(data) = self.data.borrow().as_ref() {
            return Ok(Arc::clone(data));
        }
        let row = self
            .loader
            .as_ref()
            .ok_or_else(|| Error::internal("an execution with neither its fields nor its row"))?;
        let loaded = Arc::new(crate::instances::own_of(
            self.reach.engine,
            self.reach.tx,
            row,
        )?);
        *self.data.borrow_mut() = Some(Arc::clone(&loaded));
        Ok(loaded)
    }

    /// Replaces the own fields the contexts see, after an update.
    pub fn set_data(&self, data: Map<String, Value>) {
        *self.data.borrow_mut() = Some(Arc::new(data));
    }

    /// The own fields before the call when an update() in it changed them;
    /// `None` when none did.
    pub fn own_before(&self) -> Result<Option<Arc<Map<String, Value>>>> {
        let Some(initial) = self.initial.borrow().clone() else {
            return Ok(None);
        };
        let now = self.data()?;
        Ok((!json::js_equal(
            &Value::Object((*initial).clone()),
            &Value::Object((*now).clone()),
        ))
        .then_some(initial))
    }

    fn ctx<'e>(&'e self, index: usize, mode: Mode<'e>) -> Ctx<'e, 't, 'db> {
        Ctx {
            exec: self,
            index,
            mode,
        }
    }

    /// Asks every behavior's guard in list order, each with its own entry
    /// of the preconditions, then, for a request that writes, the
    /// guardReference of every behavior that refers to the instance; the
    /// first veto refuses.
    pub fn guard(
        &self,
        request: &GuardRequest<'_>,
        writes: bool,
        preconditions: Option<&Preconditions>,
    ) -> Result<()> {
        for (index, bound) in self.behaviors().iter().enumerate() {
            if !bound.behavior.hooks.guard {
                continue;
            }
            let precondition = preconditions.and_then(|entries| {
                entries
                    .iter()
                    .find(|(name, _)| *name == bound.behavior.name)
                    .map(|(_, entry)| entry)
            });
            let asked = match precondition {
                Some(entry) => request.with_precondition(Some(entry)),
                None => request.clone(),
            };
            let ctx = self.ctx(index, Mode::View { columns: None });
            let answer = bound
                .behavior
                .implementation
                .guard(&ctx, &bound.config, &asked)
                .unwrap_or(Ok(None))?;
            if let Some(veto) = veto_of(&bound.behavior, "a guard", answer)? {
                return Err(Error::veto(
                    &bound.behavior.name,
                    request.action(),
                    &self.schema,
                    &self.id,
                    veto,
                ));
            }
        }
        if writes {
            self.reach
                .guard_references(&self.schema, &self.id, request)?;
        }
        Ok(())
    }

    /// Runs every behavior's initialize in list order, each with its own
    /// entry of the create's parameters, `{}` when the create gives none.
    pub fn initialize(&self, params: &Map<String, Value>) -> Result<()> {
        let none = Value::Object(Map::new());
        for (index, bound) in self.behaviors().iter().enumerate() {
            if !bound.behavior.hooks.initialize {
                continue;
            }
            let own = params.get(&bound.behavior.name).unwrap_or(&none);
            let ctx = self.ctx(index, Mode::Instance { writable: true });
            declared_vetoes(
                self,
                bound
                    .behavior
                    .implementation
                    .initialize(&ctx, &bound.config, own)
                    .unwrap_or(Ok(())),
            )?;
        }
        Ok(())
    }

    /// Records every behavior's columns before the instance's row goes, for
    /// the contexts afterChange gets after a delete.
    pub fn deleting(&self) -> Result<()> {
        let mut rows = Vec::new();
        for index in 0..self.behaviors().len() {
            rows.push(
                self.ctx(index, Mode::View { columns: None })
                    .columns_get()?,
            );
        }
        *self.deleted.borrow_mut() = Some(rows);
        Ok(())
    }

    /// Runs every behavior's afterChange in list order.
    pub fn after_change(&self, change: &InstanceChange<'_>) -> Result<()> {
        for (index, bound) in self.behaviors().iter().enumerate() {
            if !bound.behavior.hooks.after_change {
                continue;
            }
            let ctx = self.ctx(index, Mode::Instance { writable: true });
            declared_vetoes(
                self,
                bound
                    .behavior
                    .implementation
                    .after_change(&ctx, &bound.config, change)
                    .unwrap_or(Ok(())),
            )?;
        }
        Ok(())
    }

    /// The behaviors' declared fields, every one or the ones named by
    /// qualified name: by behavior name, in the type's behavior order, an
    /// object of the fields that have a value, in declaration order. A
    /// behavior that declares none of the fields asked has no entry.
    pub fn fields(&self, only: Option<&[&str]>) -> Result<Map<String, Value>> {
        let mut out = Map::new();
        for (index, bound) in self.behaviors().iter().enumerate() {
            let name = &bound.behavior.name;
            let wanted: Vec<&String> = bound
                .behavior
                .fields
                .iter()
                .filter(|field| {
                    only.is_none_or(|only| only.contains(&format!("{name}.{field}").as_str()))
                })
                .collect();
            if wanted.is_empty() {
                continue;
            }
            // A read's field readers share the columns, read once.
            let columns = RefCell::new(None);
            let ctx = self.ctx(
                index,
                Mode::View {
                    columns: Some(&columns),
                },
            );
            let mut entry = Map::new();
            for field in wanted {
                let value = bound
                    .behavior
                    .implementation
                    .field(field, &ctx, &bound.config)
                    .unwrap_or(Ok(None))?;
                match value {
                    None | Some(Value::Null) => {}
                    Some(value) => {
                        entry.insert(field.clone(), value);
                    }
                }
            }
            out.insert(name.clone(), Value::Object(entry));
        }
        Ok(out)
    }

    /// Runs an operation and returns its result: every guard, with the
    /// caller's checked preconditions, its handler, its result against the
    /// resultSchema. `caller` names the behavior of a `call()`, which has
    /// no preconditions.
    pub fn invoke(
        &self,
        bound: &BoundBehavior,
        operation: &OperationSpec,
        params: &Value,
        caller: Option<&str>,
        preconditions: Option<&Preconditions>,
    ) -> Result<Value> {
        Ok(self
            .operate(bound, operation, params, caller, preconditions)?
            .0)
    }

    /// [`Execution::invoke`], saying also whether the handler said it
    /// changed nothing (`unchanged()`), which the engine has held to the
    /// rows the call wrote: none.
    pub fn operate(
        &self,
        bound: &BoundBehavior,
        operation: &OperationSpec,
        params: &Value,
        caller: Option<&str>,
        preconditions: Option<&Preconditions>,
    ) -> Result<(Value, bool)> {
        let behavior = &bound.behavior.name;
        self.reach.chain.nest(
            behavior,
            &format!("operation {}", operation.name),
            || {
                let request = GuardRequest::Operation {
                    behavior,
                    operation: &operation.name,
                    params,
                    writes: operation.writes,
                    caller,
                    precondition: None,
                };
                self.guard(
                    &request,
                    operation.writes,
                    if caller.is_none() { preconditions } else { None },
                )?;
                let index = self
                    .runtime
                    .composition
                    .position(behavior)
                    .ok_or_else(|| Error::internal("an operation's behavior is not on the type"))?;
                let said = Cell::new(false);
                let before = if operation.writes {
                    self.reach.tx.changes()
                } else {
                    0
                };
                let ctx = self.ctx(
                    index,
                    Mode::Operation {
                        writable: operation.writes && self.writable,
                        said: &said,
                    },
                );
                let result = declared_vetoes(
                    self,
                    bound
                        .behavior
                        .implementation
                        .operation(&operation.name, &ctx, &bound.config, params)
                        .unwrap_or_else(|| {
                            Err(Error::internal(format!(
                                "behavior {behavior} has no handler for operation {}",
                                operation.name
                            )))
                        }),
                )?;
                let checked = check_result(&bound.behavior, operation, result)?;
                if said.get() && operation.writes && self.reach.tx.changes() != before {
                    return Err(Error::behavior(
                        behavior,
                        format!(
                            "operation {} said it changed nothing (unchanged()), and it wrote: a call that writes appends its event",
                            operation.name
                        ),
                    ));
                }
                Ok((checked, said.get() && operation.writes))
            },
        )
    }

    /// Runs a schema-level operation's handler and checks its result:
    /// `params` are checked already.
    pub fn invoke_schema_operation(
        &self,
        bound: &BoundBehavior,
        operation: &OperationSpec,
        params: &Value,
    ) -> Result<Value> {
        let behavior = &bound.behavior.name;
        self.reach
            .chain
            .nest(behavior, &format!("operation {}", operation.name), || {
                let index =
                    self.runtime.composition.position(behavior).ok_or_else(|| {
                        Error::internal("an operation's behavior is not on the type")
                    })?;
                let ctx = self.ctx(
                    index,
                    Mode::Schema {
                        writes: operation.writes,
                    },
                );
                let result = declared_vetoes(
                    self,
                    bound
                        .behavior
                        .implementation
                        .schema_operation(&operation.name, &ctx, &bound.config, params)
                        .unwrap_or_else(|| {
                            Err(Error::internal(format!(
                                "behavior {behavior} has no handler for operation {}",
                                operation.name
                            )))
                        }),
                )?;
                check_result(&bound.behavior, operation, result)
            })
    }

    /// Hands one event to a behavior's reactions, as the runner's work.
    pub fn react(&self, index: usize, event: &EngineEvent) -> Result<()> {
        let bound = &self.behaviors()[index];
        self.reach.chain.nest(&bound.behavior.name, "react", || {
            let ctx = self.ctx(index, Mode::Reaction);
            bound
                .behavior
                .implementation
                .react(&ctx, &bound.config, event)
                .unwrap_or(Ok(()))
        })
    }

    /// Runs one schedule of a behavior once, as the runner's work.
    pub fn schedule(&self, index: usize, name: &str, previous: Option<i64>) -> Result<()> {
        let bound = &self.behaviors()[index];
        self.reach
            .chain
            .nest(&bound.behavior.name, &format!("schedule {name}"), || {
                let ctx = self.ctx(index, Mode::Schedule { name, previous });
                bound
                    .behavior
                    .implementation
                    .run_schedule(name, &ctx, &bound.config)
                    .unwrap_or(Ok(()))
            })
    }

    /// The guardReference of a behavior on this instance about one of its
    /// references.
    pub fn guard_reference(
        &self,
        index: usize,
        reference: &Reference,
        request: &GuardRequest<'_>,
    ) -> Result<Option<Veto>> {
        let bound = &self.behaviors()[index];
        let ctx = self.ctx(index, Mode::View { columns: None });
        let answer = bound
            .behavior
            .implementation
            .guard_reference(&ctx, &bound.config, reference, request)
            .unwrap_or(Ok(None))?;
        veto_of(&bound.behavior, "guardReference", answer)
    }

    /// The afterReferenceChange of a behavior on this instance about one of
    /// its references.
    pub fn after_reference_change(
        &self,
        index: usize,
        reference: &Reference,
        change: &InstanceChange<'_>,
    ) -> Result<()> {
        let bound = &self.behaviors()[index];
        let ctx = self.ctx(index, Mode::Reference);
        bound
            .behavior
            .implementation
            .after_reference_change(&ctx, &bound.config, reference, change)
            .unwrap_or(Ok(()))
    }

    // call runs another behavior's operation on this instance for a
    // context of `from`: its parameters, every guard, its handler, in a
    // savepoint when it may write.
    fn call(
        &self,
        from: &RegisteredBehavior,
        writable: bool,
        behavior: &str,
        name: &str,
        params: &Value,
    ) -> Result<Value> {
        if self.deleted.borrow().is_some() {
            return Err(Error::behavior(
                &from.name,
                "the instance is deleted; call() runs no operation on it",
            ));
        }
        let found = self
            .runtime
            .composition
            .operation(name)
            .filter(|(bound, _)| bound.behavior.name == behavior);
        let Some((bound, operation)) = found else {
            return Err(EngineError::new(
                crate::errors::EngineErrorCode::NotFound,
                format!(
                    "{} has no operation {name} of behavior {behavior}",
                    self.schema
                ),
            )
            .into());
        };
        if operation.scope == super::declaration::OperationScope::Schema {
            return Err(EngineError::new(
                crate::errors::EngineErrorCode::NotFound,
                format!(
                    "{}'s {name} is a schema-level operation; call() runs an operation of this instance",
                    self.schema
                ),
            )
            .into());
        }
        if operation.writes && !writable {
            return Err(Error::behavior(
                &from.name,
                format!("a read-only operation cannot call {behavior}.{name}, which writes"),
            ));
        }
        let checked = check_params(&bound.behavior, operation, params)?;
        if !writable {
            return self.invoke(bound, operation, &checked, Some(&from.name), None);
        }
        // The savepoint rolls back an update() the called operation made
        // with the rest of it, so the fields the contexts see go back too.
        let data = self.data.borrow().clone();
        let initial = self.initial.borrow().clone();
        let result = self
            .reach
            .tx
            .savepoint(|_| self.invoke(bound, operation, &checked, Some(&from.name), None));
        if result.is_err() {
            *self.data.borrow_mut() = data;
            *self.initial.borrow_mut() = initial;
        }
        result
    }

    // update applies a merge patch to the instance's own fields for an
    // operation of `from`: the checks and guards of an update, without the
    // policy, which allowed the operation, and without an event.
    fn update(
        &self,
        from: &RegisteredBehavior,
        writable: bool,
        patch: &Value,
    ) -> Result<Arc<Map<String, Value>>> {
        if self.deleted.borrow().is_some() {
            return Err(Error::behavior(
                &from.name,
                "the instance is deleted; update() changes nothing",
            ));
        }
        if !writable {
            return Err(Error::behavior(
                &from.name,
                "a read-only operation cannot update the instance",
            ));
        }
        let (copy, merged, issues) = self.merge(from, patch)?;
        if !issues.is_empty() {
            return Err(EngineError::instance_validation(
                &self.reach.chain.namespace,
                &self.schema,
                self.version,
                issues,
            )
            .into());
        }
        let current = self.data()?;
        if json::js_equal(
            &Value::Object(merged.clone()),
            &Value::Object((*current).clone()),
        ) {
            return Ok(current);
        }
        self.guard(
            &GuardRequest::Update {
                patch: &copy,
                after: &merged,
                caller: Some(&from.name),
                precondition: None,
            },
            true,
            None,
        )?;
        self.reach.write_own(
            &self.schema,
            &self.id,
            &merged,
            &copy.keys().cloned().collect(),
        )?;
        if self.initial.borrow().is_none() {
            *self.initial.borrow_mut() = Some(current);
        }
        self.set_data(merged);
        self.data()
    }

    // merge applies a behavior's merge patch to a copy of the own fields
    // and lists what update() would refuse: what the live version refuses
    // in the result, then what the behaviors' validate refuses in it.
    fn merge(&self, from: &RegisteredBehavior, patch: &Value) -> Result<Merged> {
        if !patch.is_object() {
            return Err(Error::behavior(
                &from.name,
                "update() takes a JSON merge patch of the instance: a JSON object",
            ));
        }
        // The patch's values as the version stores them; it fills no
        // default.
        let normalized = self
            .runtime
            .validator
            .normalize(patch, NormalizeMode::Patch);
        let value = normalized.value.as_object().cloned().unwrap_or_default();
        let current = self.data()?;
        let merged = match merge_patch(
            &Value::Object((*current).clone()),
            &Value::Object(value.clone()),
        ) {
            Value::Object(merged) => merged,
            _ => Map::new(),
        };
        let own = self
            .runtime
            .validator
            .validate(&Value::Object(merged.clone()));
        if !own.is_empty() || !normalized.issues.is_empty() {
            let issues = if own.is_empty() {
                normalized.issues
            } else {
                own
            };
            return Ok((value, merged, issues));
        }
        let issues = validation_issues(
            &self.reach,
            &self.runtime,
            &self.schema,
            self.version,
            &self.id,
            &ValidationRequest::Update {
                before: &current,
                after: &merged,
                caller: Some(&from.name),
            },
        )?;
        Ok((value, merged, issues))
    }
}

/// What update() would store: the patch as the version stores it, the
/// own fields after it, and what would refuse them.
type Merged = (Map<String, Value>, Map<String, Value>, Vec<ValidationIssue>);

/// What a context may do in its moment.
#[derive(Clone, Copy)]
enum Mode<'e> {
    /// A guard's view, a field reader's (whose columns are read once), and
    /// guardReference's.
    View {
        columns: Option<&'e RefCell<Option<Row>>>,
    },
    /// afterReferenceChange's: a view whose invokes write.
    Reference,
    /// initialize's, afterChange's, and a read-only operation's (not
    /// writable).
    Instance { writable: bool },
    /// An operation handler's.
    Operation {
        writable: bool,
        said: &'e Cell<bool>,
    },
    /// A schema-level operation's.
    Schema { writes: bool },
    /// A reaction's.
    Reaction,
    /// A schedule run's.
    Schedule {
        name: &'e str,
        previous: Option<i64>,
    },
}

/// One behavior's context in one moment of an execution.
struct Ctx<'e, 't, 'db> {
    exec: &'e Execution<'t, 'db>,
    index: usize,
    mode: Mode<'e>,
}

impl Ctx<'_, '_, '_> {
    fn bound(&self) -> &BoundBehavior {
        &self.exec.behaviors()[self.index]
    }

    fn name(&self) -> &str {
        &self.bound().behavior.name
    }

    fn prefix(&self) -> Result<&str> {
        self.exec
            .runtime
            .prefixes
            .get(self.index)
            .map(String::as_str)
            .ok_or_else(|| {
                Error::internal(format!(
                    "behavior {} has no storage in this database",
                    self.name()
                ))
            })
    }

    // invoke_writes is whether instances.invoke and create may write.
    fn invoke_writes(&self) -> bool {
        match self.mode {
            Mode::View { .. } => false,
            Mode::Reference | Mode::Reaction | Mode::Schedule { .. } => true,
            Mode::Instance { writable } | Mode::Operation { writable, .. } => {
                writable && self.exec.writable
            }
            Mode::Schema { writes } => writes,
        }
    }

    // writable is whether the instance's columns, references and values
    // may change.
    fn writable(&self) -> bool {
        match self.mode {
            Mode::Instance { writable } | Mode::Operation { writable, .. } => {
                writable && self.exec.writable
            }
            _ => false,
        }
    }

    fn sql_mode(&self) -> SqlMode {
        match self.mode {
            Mode::View { .. } | Mode::Reference | Mode::Reaction => SqlMode::Read,
            Mode::Instance { .. } | Mode::Operation { .. } => {
                if self.writable() {
                    SqlMode::Write
                } else {
                    SqlMode::Read
                }
            }
            Mode::Schema { writes } => {
                if writes {
                    SqlMode::Write
                } else {
                    SqlMode::Read
                }
            }
            Mode::Schedule { .. } => SqlMode::Write,
        }
    }

    // value_refusal is why stow or release refuses in this moment.
    fn value_refusal(&self, what: &str) -> Option<String> {
        if what == "stow" && self.exec.deleted.borrow().is_some() {
            return Some("the instance is deleted; values.stow stores nothing for it".to_owned());
        }
        let who = match self.mode {
            Mode::View { .. } | Mode::Reference => "a read",
            Mode::Instance { .. } | Mode::Operation { .. } if !self.writable() => {
                "a read-only operation"
            }
            Mode::Schema { writes: false } => "a read-only operation",
            Mode::Reaction => "a reaction",
            _ => return None,
        };
        Some(format!(
            "{who} cannot change what the value store holds (values.{what})"
        ))
    }

    fn with_sql<T>(
        &self,
        run: impl FnOnce(&BehaviorSql<'_, '_>) -> superschematic_engine_storage::Result<T>,
    ) -> Result<T> {
        let exec = self.exec;
        let bound = self.bound();
        let prefix = self.prefix()?.to_owned();
        let schema = exec.schema.clone();
        let reach = exec.reach;
        let relation = InstanceRelation {
            namespace: exec.reach.chain.namespace.clone(),
            schema: exec.schema.clone(),
            columns: bound.behavior.columns.clone(),
            allow: Box::new(move || reach.allow_read(&schema).map_err(Into::into)),
        };
        let sql = BehaviorSql::new(
            exec.reach.tx,
            &bound.behavior.name,
            &prefix,
            self.sql_mode(),
            Some(&relation),
        );
        Ok(run(&sql)?)
    }

    fn source(&self) -> ReferenceSource {
        ReferenceSource {
            schema: self.exec.schema.clone(),
            id: self.exec.id.clone(),
            behavior: self.name().to_owned(),
        }
    }

    // reference_writing refuses add and remove after a delete and in a
    // moment that does not write.
    fn reference_writing(&self, what: &str) -> Result<()> {
        if self.exec.deleted.borrow().is_some() {
            return Err(Error::behavior(
                self.name(),
                format!("the instance is deleted; references.{what} changes nothing, and the engine drops its references"),
            ));
        }
        if !self.writable() {
            return Err(Error::behavior(
                self.name(),
                format!("a read-only operation cannot {what} a reference"),
            ));
        }
        Ok(())
    }

    fn values_holder<'h>(&'h self, key: &'h str) -> ValueHolder<'h> {
        ValueHolder {
            namespace: &self.exec.reach.chain.namespace,
            schema: &self.exec.schema,
            holder: self.name(),
            id: &self.exec.id,
            key,
        }
    }
}

impl Core for Ctx<'_, '_, '_> {
    fn behavior(&self) -> &str {
        self.name()
    }

    fn namespace(&self) -> &str {
        &self.exec.reach.chain.namespace
    }

    fn schema(&self) -> &str {
        &self.exec.schema
    }

    fn version(&self) -> i64 {
        self.exec.version
    }

    fn now(&self) -> i64 {
        self.exec.reach.chain.now
    }

    fn principal(&self) -> &Principal {
        &self.exec.reach.chain.principal
    }

    fn id(&self) -> &str {
        &self.exec.id
    }

    fn uuids(&self) -> crate::UuidSource {
        self.exec.reach.engine.uuids()
    }

    fn can(&self, permission: &str) -> Result<bool> {
        can(&self.exec.reach, self.name(), permission)
    }

    fn validate_type(&self, type_name: &str, value: &Value) -> Result<Vec<ValidationIssue>> {
        type_check(&self.exec.runtime, self.name(), type_name, value)
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
        self.exec.reach.chain.nest(self.name(), what, || {
            self.exec.reach.read(schema, ids, fields)
        })
    }

    fn invoke(
        &self,
        schema: &str,
        id: &str,
        operation: &str,
        params: &Value,
        preconditions: Option<&Value>,
    ) -> Result<Value> {
        self.exec.reach.invoke(
            self.name(),
            schema,
            id,
            operation,
            params,
            self.invoke_writes(),
            preconditions,
        )
    }

    fn invoke_schema(&self, schema: &str, operation: &str, params: &Value) -> Result<Value> {
        self.exec
            .reach
            .invoke_schema(self.name(), schema, operation, params, self.invoke_writes())
    }

    fn create(
        &self,
        schema: &str,
        data: &Value,
        id: Option<&str>,
        behaviors: Option<&Value>,
    ) -> Result<InstanceRecord> {
        self.exec
            .reach
            .chain
            .nest(self.name(), "instances.create", || {
                self.exec.reach.create(
                    self.name(),
                    schema,
                    data,
                    id,
                    behaviors,
                    self.invoke_writes(),
                )
            })
    }

    fn schema_config(&self, schema: &str, behavior: &str) -> Result<Option<Value>> {
        for (what, value) in [("schema", schema), ("behavior", behavior)] {
            if value.is_empty() {
                return Err(Error::behavior(
                    self.name(),
                    format!("schemas.config takes a {what} name: a non-empty string"),
                ));
            }
        }
        self.exec.reach.config(&self.exec.schema, schema, behavior)
    }

    fn readable(&self, schema: &str) -> Result<bool> {
        if schema.is_empty() {
            return Err(Error::behavior(
                self.name(),
                "schemas.readable takes a schema name: a non-empty string",
            ));
        }
        self.exec.reach.readable(schema)
    }

    fn data(&self) -> Result<Arc<Map<String, Value>>> {
        self.exec.data()
    }

    fn columns_get(&self) -> Result<Row> {
        if let Some(rows) = self.exec.deleted.borrow().as_ref() {
            return Ok(rows[self.index].clone());
        }
        if let Mode::View {
            columns: Some(cache),
        } = self.mode
        {
            if let Some(row) = cache.borrow().as_ref() {
                return Ok(row.clone());
            }
        }
        let row = instance_columns(self)?;
        if let Mode::View {
            columns: Some(cache),
        } = self.mode
        {
            *cache.borrow_mut() = Some(row.clone());
        }
        Ok(row)
    }

    fn columns_set(&self, values: &[(&str, SqlValue)]) -> Result<()> {
        let name = self.name();
        if self.exec.deleted.borrow().is_some() {
            return Err(Error::behavior(
                name,
                "the instance is deleted; its columns cannot change",
            ));
        }
        match self.mode {
            Mode::Instance { .. } | Mode::Operation { .. } if self.writable() => {}
            Mode::Instance { .. } | Mode::Operation { .. } => {
                return Err(Error::behavior(
                    name,
                    "a read-only operation cannot set columns",
                ))
            }
            _ => return Err(Error::behavior(name, "a read cannot set columns")),
        }
        let columns = &self.bound().behavior.columns;
        for (column, _) in values {
            if !columns.iter().any(|known| known == column) {
                return Err(Error::behavior(
                    name,
                    format!(
                        "it has no column {column}; its columns are {}",
                        if columns.is_empty() {
                            "none".to_owned()
                        } else {
                            columns.join(", ")
                        }
                    ),
                ));
            }
        }
        if values.is_empty() {
            return Ok(());
        }
        let dialect = self.exec.reach.tx.dialect();
        let prefix = self.prefix()?;
        let set: Vec<String> = values
            .iter()
            .map(|(column, _)| {
                format!(
                    "{} = ?",
                    quote_identifier(&instance_column(dialect, prefix, column))
                )
            })
            .collect();
        let mut params: Vec<SqlValue> = values.iter().map(|(_, value)| value.clone()).collect();
        params.extend([
            SqlValue::from(self.exec.reach.chain.namespace.as_str()),
            SqlValue::from(self.exec.schema.as_str()),
            SqlValue::from(self.exec.id.as_str()),
        ]);
        self.exec.reach.tx.run(
            format!(
                "UPDATE engine_instances SET {} WHERE namespace = ? AND schema = ? AND id = ?",
                set.join(", ")
            ),
            &params,
        )?;
        Ok(())
    }

    fn dialect(&self) -> Dialect {
        self.exec.reach.tx.dialect()
    }

    fn sql_table(&self, name: &str) -> Result<String> {
        self.with_sql(|sql| sql.table(name))
    }

    fn sql_instances(&self) -> Result<String> {
        self.with_sql(|sql| sql.instances())
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

    fn sql_run(&self, sql: &Sql<'_>, params: &[SqlValue], tried: bool) -> Result<RunResult> {
        self.with_sql(|handle| {
            if tried {
                handle.try_run(sql, params)
            } else {
                handle.run(sql, params)
            }
        })
    }

    fn references_list(&self) -> Result<Vec<Reference>> {
        ReferenceTable::from(
            self.exec.reach.tx,
            &self.exec.reach.chain.namespace,
            &self.source(),
        )
    }

    fn references_add(
        &self,
        schema: &str,
        id: &str,
        key: &str,
        hears: Option<Hears>,
    ) -> Result<()> {
        self.reference_writing("add")?;
        let target = reference(self.name(), schema, id, key, hears)?;
        self.exec.reach.add_reference(&self.source(), &target)
    }

    fn references_remove(&self, schema: &str, id: &str, key: &str) -> Result<bool> {
        self.reference_writing("remove")?;
        let target = reference(self.name(), schema, id, key, None)?;
        ReferenceTable::remove(
            self.exec.reach.tx,
            &self.exec.reach.chain.namespace,
            &self.source(),
            &target,
        )
    }

    fn values_load(&self, json: &str, refs: Option<&str>) -> Result<Map<String, Value>> {
        values_load(&self.exec.reach, self.name(), json, refs)
    }

    fn values_stow(&self, key: &str, object: &Map<String, Value>) -> Result<StowedObject> {
        if let Some(reason) = self.value_refusal("stow") {
            return Err(Error::behavior(self.name(), reason));
        }
        values_stow(
            &self.exec.reach,
            self.name(),
            &self.values_holder(key),
            object,
        )
    }

    fn values_release(&self, key: Option<&str>) -> Result<()> {
        if let Some(reason) = self.value_refusal("release") {
            return Err(Error::behavior(self.name(), reason));
        }
        values_release(&self.exec.reach, self.name(), &self.values_holder(""), key)
    }

    fn call(&self, behavior: &str, operation: &str, params: &Value) -> Result<Value> {
        match self.mode {
            Mode::Instance { .. } | Mode::Operation { .. } => self.exec.call(
                &self.bound().behavior,
                self.writable(),
                behavior,
                operation,
                params,
            ),
            _ => Err(Error::internal("call() is not part of this context")),
        }
    }

    fn update(&self, patch: &Value) -> Result<Arc<Map<String, Value>>> {
        self.exec
            .update(&self.bound().behavior, self.writable(), patch)
    }

    fn validate_update(&self, patch: &Value) -> Result<Vec<ValidationIssue>> {
        Ok(self.exec.merge(&self.bound().behavior, patch)?.2)
    }

    fn unchanged(&self) {
        if let Mode::Operation { said, .. } = self.mode {
            said.set(true);
        }
    }

    fn writing(&self) -> bool {
        self.exec
            .reach
            .chain
            .writing(&self.exec.schema, &self.exec.id)
    }

    fn before(&self, event: &EngineEvent) -> Result<Option<InstanceFields>> {
        self.exec.reach.before(self.name(), event)
    }

    fn schedule(&self) -> (&str, Option<i64>) {
        match self.mode {
            Mode::Schedule { name, previous } => (name, previous),
            _ => ("", None),
        }
    }
}

// instance_columns reads one behavior's columns of the execution's
// instance, under its own names.
fn instance_columns(ctx: &Ctx<'_, '_, '_>) -> Result<Row> {
    let columns = &ctx.bound().behavior.columns;
    if columns.is_empty() {
        return Ok(Row::new(Arc::from(Vec::<String>::new()), Vec::new()));
    }
    let dialect = ctx.exec.reach.tx.dialect();
    let prefix = ctx.prefix()?;
    let listed: Vec<String> = columns
        .iter()
        .map(|column| {
            format!(
                "{} AS {}",
                quote_identifier(&instance_column(dialect, prefix, column)),
                quote_identifier(column)
            )
        })
        .collect();
    let row = ctx.exec.reach.tx.get(
        format!(
            "SELECT {} FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?",
            listed.join(", ")
        ),
        &[
            SqlValue::from(ctx.exec.reach.chain.namespace.as_str()),
            SqlValue::from(ctx.exec.schema.as_str()),
            SqlValue::from(ctx.exec.id.as_str()),
        ],
    )?;
    row.ok_or_else(|| {
        Error::behavior(
            ctx.name(),
            format!("{} {} does not exist", ctx.exec.schema, ctx.exec.id),
        )
    })
}

/// A context's `can()`: whether the principal holds one permission, as the
/// engine's permission matcher answers. A service standing in for an end
/// user holds none (D37), so it is never asked for one.
fn can(reach: &Reach<'_, '_>, behavior: &str, permission: &str) -> Result<bool> {
    if permission.is_empty() {
        return Err(Error::behavior(
            behavior,
            "can() takes a permission: a non-empty string",
        ));
    }
    let principal = &reach.chain.principal;
    if principal.stands_in() {
        return Ok(false);
    }
    Ok(reach
        .engine
        .matcher
        .matches(&principal.permissions, &[permission.to_owned()]))
}

/// A context's `validate(type, value)`: the version's validator holds a
/// value to a type its checks cover besides the instance type.
pub(crate) fn type_check(
    runtime: &VersionRuntime,
    behavior: &str,
    type_name: &str,
    value: &Value,
) -> Result<Vec<ValidationIssue>> {
    let composition = &runtime.composition;
    if type_name == composition.type_name || !composition.covered.contains(type_name) {
        let mut covered: Vec<&String> = composition
            .covered
            .iter()
            .filter(|name| **name != composition.type_name)
            .collect();
        covered.sort_by(|a, b| json::compare_utf16(a, b));
        let listed: Vec<&str> = covered.iter().map(|name| name.as_str()).collect();
        return Err(Error::behavior(
            behavior,
            format!(
                "validate: {type_name} is not a type the version checks besides {}, one its fields reach, a behavior's checkedTypes names or a behavior's parseConfig read through ConfigTarget.types ({})",
                composition.type_name,
                if listed.is_empty() { "none".to_owned() } else { listed.join(", ") }
            ),
        ));
    }
    runtime.validator.validate_type(type_name, value, "")
}

// reference checks a reference a behavior names: its schema and id, and
// what it hears, 'delete' or a JSON pointer into what a read returns of
// the target with a finite number to cross.
fn reference(
    behavior: &str,
    schema: &str,
    id: &str,
    key: &str,
    hears: Option<Hears>,
) -> Result<Reference> {
    if schema.is_empty() {
        return Err(Error::behavior(
            behavior,
            "references takes a schema name: a non-empty string",
        ));
    }
    if id.is_empty() {
        return Err(Error::behavior(
            behavior,
            "references takes an instance id: a non-empty string",
        ));
    }
    if let Some(Hears::Path { path, crosses }) = &hears {
        if !is_target_pointer(path) {
            return Err(Error::behavior(
                behavior,
                format!(
                    "references.add: hears is 'delete' or {{ path, crosses? }}, its path a JSON pointer into the target as a read returns it, /data/<field> or /behaviors/<behavior>/<field>, not {}",
                    json::quote(path)
                ),
            ));
        }
        // JSON.stringify writes NaN and the infinities as null.
        if crosses.is_some_and(|crosses| !crosses.is_finite()) {
            return Err(Error::behavior(
                behavior,
                "references.add: hears is 'delete' or { path, crosses? }, its crosses a finite number, not null",
            ));
        }
    }
    Ok(Reference {
        schema: schema.to_owned(),
        id: id.to_owned(),
        key: key.to_owned(),
        hears,
    })
}

// is_target_pointer: a JSON pointer, /data/<field>... or
// /behaviors/<behavior>/<field>..., with something after the part's slash
// (`^\/(data|behaviors\/([^~/]|~[01])+)\/.` in TypeScript).
fn is_target_pointer(path: &str) -> bool {
    let well_formed = path.starts_with('/')
        && path[1..].split('/').all(|token| {
            let mut chars = token.chars().peekable();
            while let Some(c) = chars.next() {
                if c == '~' && !matches!(chars.next(), Some('0' | '1')) {
                    return false;
                }
            }
            true
        });
    if !well_formed {
        return false;
    }
    let tokens: Vec<&str> = path[1..].split('/').collect();
    match tokens.first() {
        Some(&"data") => tokens.len() >= 2 && !tokens[1].is_empty() || tokens.len() > 2,
        Some(&"behaviors") => {
            tokens.len() >= 3
                && !tokens[1].is_empty()
                && (tokens.len() > 3 || !tokens[2].is_empty())
        }
        _ => false,
    }
}

/// A value store load for a behavior's row.
pub(crate) fn values_load(
    reach: &Reach<'_, '_>,
    behavior: &str,
    text: &str,
    refs: Option<&str>,
) -> Result<Map<String, Value>> {
    let stored: Value = serde_json::from_str(text).map_err(|_| {
        Error::behavior(
            behavior,
            "values.load takes the JSON text stow returned, and its refs",
        )
    })?;
    match reach
        .engine
        .values
        .fill(reach.tx, &stored, &refs_of(refs)?)?
    {
        Value::Object(object) => Ok(object),
        _ => Err(Error::behavior(
            behavior,
            "values.load takes the JSON text stow returned, and its refs",
        )),
    }
}

/// A value store stow for a behavior's row.
pub(crate) fn values_stow(
    reach: &Reach<'_, '_>,
    behavior: &str,
    holder: &ValueHolder<'_>,
    object: &Map<String, Value>,
) -> Result<StowedObject> {
    if holder.key.is_empty() {
        return Err(Error::behavior(
            behavior,
            "values.stow takes the key of the behavior's row: a non-empty string",
        ));
    }
    let store = &reach.engine.values;
    let none = std::collections::BTreeSet::new();
    let stowed = store.stow(reach.tx, object, "", &none, &none)?;
    store.hold(reach.tx, holder, &stowed.hashes)?;
    Ok(StowedObject {
        json: json::stringify(&Value::Object(stowed.value)),
        refs: refs_text(&stowed.refs),
    })
}

/// A value store release of a behavior's row, or of every row of its
/// instance.
pub(crate) fn values_release(
    reach: &Reach<'_, '_>,
    behavior: &str,
    holder: &ValueHolder<'_>,
    key: Option<&str>,
) -> Result<()> {
    if key == Some("") {
        return Err(Error::behavior(
            behavior,
            "values.release takes the key of the behavior's row: a non-empty string",
        ));
    }
    reach.engine.values.release(
        reach.tx,
        holder.namespace,
        holder.schema,
        holder.holder,
        holder.id,
        key,
    )
}

/// Reads a guard's answer: `None` to allow, or a veto whose reason is text
/// and whose code the behavior's declaration lists.
pub(crate) fn veto_of(
    behavior: &RegisteredBehavior,
    what: &str,
    answer: Option<Veto>,
) -> Result<Option<Veto>> {
    let Some(veto) = answer else {
        return Ok(None);
    };
    if veto.reason.is_empty() {
        return Err(Error::behavior(
            &behavior.name,
            format!("{what} returns a reason (a non-empty string) or {{ reason, code?, details? }} to veto, or undefined to allow"),
        ));
    }
    check_veto_code(behavior, veto.code.as_deref())?;
    Ok(Some(veto))
}

// check_veto_code holds a veto's code to one its declaration lists.
fn check_veto_code(behavior: &RegisteredBehavior, code: Option<&str>) -> Result<()> {
    let Some(code) = code else {
        return Ok(());
    };
    if behavior.veto_codes.iter().any(|known| known == code) {
        return Ok(());
    }
    Err(Error::behavior(
        &behavior.name,
        format!(
            "a veto's code {} is not one its declaration lists ({})",
            json::quote(code),
            if behavior.veto_codes.is_empty() {
                "none".to_owned()
            } else {
                behavior.veto_codes.join(", ")
            }
        ),
    ))
}

/// Holds a veto a behavior of the composition refused with to the codes
/// its declaration lists: one it does not list is a BehaviorError. A veto
/// of another schema's behavior, from an invoke, was held where it was
/// raised.
pub(crate) fn declared_vetoes<T>(exec: &Execution<'_, '_>, result: Result<T>) -> Result<T> {
    if let Err(Error::Engine(error)) = &result {
        if let crate::errors::EngineErrorKind::BehaviorVeto {
            behavior,
            veto_code,
            veto_details,
            ..
        } = error.kind()
        {
            if veto_code.is_some() || veto_details.is_some() {
                if let Some(bound) = exec.runtime.composition.bound(behavior) {
                    check_veto_code(&bound.behavior, veto_code.as_deref())?;
                }
            }
        }
    }
    result
}

// check_result holds an operation's result to its resultSchema.
fn check_result(
    behavior: &RegisteredBehavior,
    operation: &OperationSpec,
    result: Value,
) -> Result<Value> {
    if !operation.result.is_valid(&result) {
        return Err(Error::behavior(
            &behavior.name,
            format!(
                "operation {} returned a result its resultSchema refuses: {}",
                operation.name,
                detail(&operation.result, &result)
            ),
        ));
    }
    Ok(result)
}

/// Checks an operation's parameters against its paramsSchema: the object
/// every guard and the handler get.
pub(crate) fn check_params(
    behavior: &RegisteredBehavior,
    operation: &OperationSpec,
    params: &Value,
) -> Result<Value, EngineError> {
    if !operation.params.is_valid(params) {
        return Err(EngineError::operation_params(
            &behavior.name,
            &operation.name,
            issues(operation.params.errors(params)),
        ));
    }
    Ok(params.clone())
}

/// Asks every behavior's validate, in list order, about the own fields a
/// write would store, and returns their issues together.
pub(crate) fn validation_issues(
    reach: &Reach<'_, '_>,
    runtime: &Arc<VersionRuntime>,
    schema: &str,
    version: i64,
    id: &str,
    request: &ValidationRequest<'_>,
) -> Result<Vec<ValidationIssue>> {
    let mut out = Vec::new();
    for bound in &runtime.composition.behaviors {
        if !bound.behavior.hooks.validate {
            continue;
        }
        let core = ValidationCore {
            reach,
            runtime,
            bound,
            schema,
            version,
            id,
        };
        let answer = bound
            .behavior
            .implementation
            .validate(&core, &bound.config, request)
            .unwrap_or(Ok(Vec::new()));
        // A veto validate raises is held to the declaration as a handler's.
        let answer = match &answer {
            Err(Error::Engine(error)) => match error.kind() {
                crate::errors::EngineErrorKind::BehaviorVeto {
                    behavior,
                    veto_code: Some(code),
                    ..
                } => match runtime.composition.bound(behavior) {
                    Some(owner) => {
                        check_veto_code(&owner.behavior, Some(code))?;
                        answer
                    }
                    None => answer,
                },
                _ => answer,
            },
            _ => answer,
        }?;
        for issue in answer {
            if issue.rule.is_empty() || issue.message.is_empty() {
                return Err(Error::behavior(
                    &bound.behavior.name,
                    "validate returns a list of issues, each { path, rule, message } with a rule and a message, or nothing to accept",
                ));
            }
            out.push(issue);
        }
    }
    Ok(out)
}

// validate's context: the behavior, its config, the call, can() and
// checkType().
struct ValidationCore<'a, 't, 'db> {
    reach: &'a Reach<'t, 'db>,
    runtime: &'a VersionRuntime,
    bound: &'a BoundBehavior,
    schema: &'a str,
    version: i64,
    id: &'a str,
}

impl Core for ValidationCore<'_, '_, '_> {
    fn behavior(&self) -> &str {
        &self.bound.behavior.name
    }

    fn namespace(&self) -> &str {
        &self.reach.chain.namespace
    }

    fn schema(&self) -> &str {
        self.schema
    }

    fn version(&self) -> i64 {
        self.version
    }

    fn now(&self) -> i64 {
        self.reach.chain.now
    }

    fn principal(&self) -> &Principal {
        &self.reach.chain.principal
    }

    fn id(&self) -> &str {
        self.id
    }

    fn can(&self, permission: &str) -> Result<bool> {
        can(self.reach, &self.bound.behavior.name, permission)
    }

    fn check_type(
        &self,
        type_name: &str,
        value: &Value,
        path: &str,
    ) -> Result<Vec<ValidationIssue>> {
        let checked = &self.bound.checked;
        if !checked.iter().any(|name| name == type_name) {
            return Err(Error::behavior(
                &self.bound.behavior.name,
                format!(
                    "checkType: {type_name} is not a type its checkedTypes names ({})",
                    if checked.is_empty() {
                        "none".to_owned()
                    } else {
                        checked.join(", ")
                    }
                ),
            ));
        }
        self.runtime.validator.validate_type(type_name, value, path)
    }

    fn dialect(&self) -> Dialect {
        self.reach.tx.dialect()
    }

    fn sql_table(&self, _name: &str) -> Result<String> {
        Err(Error::internal("validate's context reaches no storage"))
    }

    fn sql_get(&self, _sql: &Sql<'_>, _params: &[SqlValue], _tried: bool) -> Result<Option<Row>> {
        Err(Error::internal("validate's context reaches no storage"))
    }

    fn sql_all(&self, _sql: &Sql<'_>, _params: &[SqlValue], _tried: bool) -> Result<Vec<Row>> {
        Err(Error::internal("validate's context reaches no storage"))
    }

    fn sql_run(&self, _sql: &Sql<'_>, _params: &[SqlValue], _tried: bool) -> Result<RunResult> {
        Err(Error::internal("validate's context reaches no storage"))
    }

    fn values_load(&self, _json: &str, _refs: Option<&str>) -> Result<Map<String, Value>> {
        Err(Error::internal("validate's context reaches no storage"))
    }
}

/// Checks the parameters a create gives the type's behaviors: a JSON
/// object by behavior name, each entry for a behavior the type composes
/// that declares a createParamsSchema, which accepts it; a behavior with a
/// createParamsSchema the create gives nothing must accept `{}`.
pub(crate) fn check_create_params(
    composition: &super::composition::Composition,
    schema: &str,
    behaviors: Option<&Value>,
) -> Result<Map<String, Value>, EngineError> {
    let empty = Value::Object(Map::new());
    let Value::Object(given) = behaviors.unwrap_or(&empty) else {
        return Err(EngineError::create_params(
            schema,
            vec![SchemaIssue::new(
                "/behaviors",
                "behaviors is the create's parameters by behavior name: a JSON object",
            )],
        ));
    };
    let mut found: Vec<SchemaIssue> = Vec::new();
    let mut out = Map::new();
    let check =
        |bound: &BoundBehavior, at: &str, entry: &Value, found: &mut Vec<SchemaIssue>| {
            if let Some(validate) = &bound.behavior.create_params {
                if !validate.is_valid(entry) {
                    found.extend(issues(validate.errors(entry)).into_iter().map(|issue| {
                        SchemaIssue::new(format!("{at}{}", issue.path), issue.message)
                    }));
                }
            }
        };
    for name in json::js_keys(given) {
        let entry = &given[name];
        let at = format!("/behaviors{}", json::pointer(&[name]));
        let Some(bound) = composition.bound(name) else {
            let taking: Vec<&str> = composition
                .behaviors
                .iter()
                .filter(|candidate| candidate.behavior.create_params.is_some())
                .map(|candidate| candidate.behavior.name.as_str())
                .collect();
            found.push(SchemaIssue::new(
                at,
                format!(
                    "{schema} does not compose behavior {name} (its behaviors that take create parameters: {})",
                    if taking.is_empty() { "none".to_owned() } else { taking.join(", ") }
                ),
            ));
            continue;
        };
        if bound.behavior.create_params.is_none() {
            found.push(SchemaIssue::new(
                at,
                format!("behavior {name} takes no create parameters"),
            ));
            continue;
        }
        check(bound, &at, entry, &mut found);
        out.insert(name.to_owned(), entry.clone());
    }
    for bound in &composition.behaviors {
        let name = &bound.behavior.name;
        if bound.behavior.create_params.is_some() && !given.contains_key(name) {
            check(
                bound,
                &format!("/behaviors{}", json::pointer(&[name.as_str()])),
                &empty,
                &mut found,
            );
        }
    }
    if !found.is_empty() {
        return Err(EngineError::create_params(schema, found));
    }
    Ok(out)
}

/// Checks a call's preconditions against the type's behaviors: a JSON
/// object whose every member names a behavior the type composes that
/// declares a preconditionSchema, with an entry its schema accepts. `None`,
/// or no entry, is no preconditions.
pub(crate) fn check_preconditions(
    composition: &super::composition::Composition,
    schema: &str,
    preconditions: Option<&Value>,
) -> Result<Option<Preconditions>, EngineError> {
    let Some(preconditions) = preconditions else {
        return Ok(None);
    };
    let Value::Object(given) = preconditions else {
        return Err(EngineError::preconditions(
            schema,
            vec![SchemaIssue::new(
                "",
                "the preconditions are a JSON object of each behavior's entry by its name",
            )],
        ));
    };
    let mut found = Vec::new();
    let mut checked: Preconditions = Vec::new();
    for name in json::js_keys(given) {
        let entry = &given[name];
        let at = json::pointer(&[name]);
        let Some(bound) = composition.bound(name) else {
            found.push(SchemaIssue::new(
                at,
                format!("{schema} composes no behavior {name}"),
            ));
            continue;
        };
        let Some(validate) = &bound.behavior.precondition else {
            found.push(SchemaIssue::new(
                at,
                format!("behavior {name} takes no precondition"),
            ));
            continue;
        };
        if !validate.is_valid(entry) {
            found.extend(
                issues(validate.errors(entry))
                    .into_iter()
                    .map(|issue| SchemaIssue::new(format!("{at}{}", issue.path), issue.message)),
            );
            continue;
        }
        checked.push((name.to_owned(), entry.clone()));
    }
    if !found.is_empty() {
        return Err(EngineError::preconditions(schema, found));
    }
    Ok((!checked.is_empty()).then_some(checked))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_target_pointer_reaches_data_or_a_behaviors_field() {
        for path in [
            "/data/title",
            "/data/lines/0",
            "/behaviors/Workflow/status",
            "/data/a~1b",
            "/data//",
            "/behaviors/Workflow//",
        ] {
            assert!(is_target_pointer(path), "{path}");
        }
        for path in [
            "",
            "data/title",
            "/data",
            "/behaviors/Workflow",
            "/behaviors/Workflow/",
            "/behaviors//status",
            "/data/",
            "/other/x",
            "/data/~2",
        ] {
            assert!(!is_target_pointer(path), "{path}");
        }
    }
}
