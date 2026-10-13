//! What a behavior implementation is (D16, D58): the code an engine runs
//! for one declared behavior, the port of `src/behaviors/behavior.ts`. A
//! behavior is a Rust type that implements [`Behavior`]: its declaration,
//! the JSON file the compiler registers; a config typed in Rust, parsed
//! from the JSON a type gives it; and its [`Hooks`], each optional, since
//! an absent hook means something of its own (no `config_change` allows
//! only an identical config). A project's engine is a value its build
//! assembles ([`BehaviorSet`]), not a registry the linker collects.
//!
//! Every hook is synchronous and runs inside the engine's write
//! transaction, or in a read, and gets a context that reaches only what
//! the behavior may touch (`context`): the instance's own fields, the
//! behavior's own columns and tables, the read-only relation over the
//! instances of the call's schema, the principal, the version and the
//! clock's time for the call, `can(permission)`, `validate(type, value)`,
//! the namespace's other instances and schemas as the principal reaches
//! them, references, the value store, and in a write `call()` and, in a
//! writing operation, `update()`. A hook returns a [`Result`]: a veto is
//! [`Error::veto`], a config the behavior refuses [`Error::config`], a
//! defect in its own code [`Error::behavior`]; an error rolls the call
//! back. A hook that panics is a [`crate::BehaviorError`] too, caught where
//! the engine calls it.

use std::sync::Arc;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use super::config::ConfigTarget;
use super::context::{
    InstanceCx, InstanceView, OperationCx, PublishCx, ReactionCx, ReferenceCx, ScheduleCx,
    SchemaCx, ValidationCx,
};
use crate::errors::{Error, Result, ValidationIssue, Veto};
use crate::events::EngineEvent;
use crate::instances::references::Reference;
use superschematic_engine_storage::behavior::BehaviorMigration;

/// A behavior implementation: a Rust type with its declaration, a typed
/// config and its hooks. An extension's behavior is a crate that
/// implements it, compiled into the project's engine.
pub trait Behavior: Send + Sync + 'static {
    /// What the behavior's hooks get as config: what [`Behavior::parse_config`]
    /// makes of the JSON a type gives it.
    type Config: Send + Sync + 'static;

    /// The declaration the compiler registers, as its JSON file holds it.
    fn declaration(&self) -> &str;

    /// Checks a config its configSchema accepted, beyond what JSON Schema
    /// says, and returns the value the hooks get. Refuse it with
    /// [`Error::config`]; the schema is refused at that config. The engine
    /// calls it whenever it composes a version (at define and publish, when
    /// it runs a published version and when it checks a new version against
    /// the live one), so it returns the same value for the same config and
    /// document. The types it reads through `target.types()` are held by the
    /// version's checks and the compatibility rule, so which it reads
    /// depends only on the config and the document, never on
    /// `target.schemas()`. A behavior whose config is its JSON returns
    /// `raw.clone()`; [`json_config`] reads a serde type.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<Self::Config>;

    /// Its hooks, migrations, filters, reactions and schedules.
    fn hooks(&self) -> Hooks<Self>
    where
        Self: Sized;
}

/// A config read as a serde type: what a behavior's `parse_config` returns
/// for a config whose configSchema says all there is to say. A config the
/// type cannot hold is refused as a config ([`Error::config`]).
pub fn json_config<T: serde::de::DeserializeOwned>(raw: &Value) -> Result<T> {
    serde_json::from_value(raw.clone()).map_err(|error| Error::config(error.to_string()))
}

/// What configChange knows of the schema a new version changes, beside
/// the two configs: whether an instance of it exists, in any namespace that
/// reads the version, read only when the behavior asks. On an added or a
/// removed behavior it is always true.
pub struct ConfigChange<'a> {
    pub(crate) instances: &'a dyn Fn() -> Result<bool>,
}

impl ConfigChange<'_> {
    /// Whether the schema has instances.
    pub fn instances(&self) -> Result<bool> {
        (self.instances)()
    }
}

/// What a guard is asked to allow. The instance before the change is the
/// view's data; for a create, the new instance's own fields. `precondition`
/// is the guard's own behavior's entry of the caller's preconditions,
/// checked against its preconditionSchema; absent when the caller sent none
/// for it, and always for a create and a request a behavior's code makes.
#[derive(Clone, Debug, PartialEq)]
pub enum GuardRequest<'a> {
    Create {
        /// The new instance's own fields.
        data: &'a Map<String, Value>,
        /// The create's parameters, by behavior name, as it gives them.
        behaviors: &'a Map<String, Value>,
    },
    Update {
        patch: &'a Map<String, Value>,
        after: &'a Map<String, Value>,
        /// The behavior whose operation applied it with update(); `None`
        /// for a caller's update.
        caller: Option<&'a str>,
        precondition: Option<&'a Value>,
    },
    Delete {
        precondition: Option<&'a Value>,
    },
    Operation {
        /// The behavior whose operation it is.
        behavior: &'a str,
        operation: &'a str,
        /// The parameters, as the handler gets them.
        params: &'a Value,
        /// Whether the operation writes, as its declaration says.
        writes: bool,
        /// The behavior whose code made the call, for a call(); `None` for
        /// a caller's.
        caller: Option<&'a str>,
        precondition: Option<&'a Value>,
    },
}

impl<'a> GuardRequest<'a> {
    /// `create`, `update`, `delete`, or the operation's name: what a veto
    /// refuses.
    pub fn action(&self) -> &'a str {
        match self {
            GuardRequest::Create { .. } => "create",
            GuardRequest::Update { .. } => "update",
            GuardRequest::Delete { .. } => "delete",
            GuardRequest::Operation { operation, .. } => operation,
        }
    }

    /// The guard's own entry of the caller's preconditions.
    pub fn precondition(&self) -> Option<&'a Value> {
        match self {
            GuardRequest::Create { .. } => None,
            GuardRequest::Update { precondition, .. }
            | GuardRequest::Delete { precondition }
            | GuardRequest::Operation { precondition, .. } => *precondition,
        }
    }

    /// The behavior whose code made the request; `None` for a caller's.
    pub fn caller(&self) -> Option<&'a str> {
        match self {
            GuardRequest::Update { caller, .. } | GuardRequest::Operation { caller, .. } => *caller,
            _ => None,
        }
    }

    /// The request with a precondition: a behavior's own entry.
    pub(crate) fn with_precondition(&self, entry: Option<&'a Value>) -> GuardRequest<'a> {
        let mut out = self.clone();
        match &mut out {
            GuardRequest::Create { .. } => {}
            GuardRequest::Update { precondition, .. }
            | GuardRequest::Delete { precondition }
            | GuardRequest::Operation { precondition, .. } => *precondition = entry,
        }
        out
    }

    /// The request as the TypeScript engine's object holds it, for a
    /// behavior that records what it is asked.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        let mut put = |key: &str, value: Value| {
            out.insert(key.to_owned(), value);
        };
        match self {
            GuardRequest::Create { data, behaviors } => {
                put("kind", Value::from("create"));
                put("data", Value::Object((*data).clone()));
                put("behaviors", Value::Object((*behaviors).clone()));
            }
            GuardRequest::Update {
                patch,
                after,
                caller,
                precondition,
            } => {
                put("kind", Value::from("update"));
                put("patch", Value::Object((*patch).clone()));
                put("after", Value::Object((*after).clone()));
                if let Some(caller) = caller {
                    put("caller", Value::from(*caller));
                }
                if let Some(precondition) = precondition {
                    put("precondition", (*precondition).clone());
                }
            }
            GuardRequest::Delete { precondition } => {
                put("kind", Value::from("delete"));
                if let Some(precondition) = precondition {
                    put("precondition", (*precondition).clone());
                }
            }
            GuardRequest::Operation {
                behavior,
                operation,
                params,
                writes,
                caller,
                precondition,
            } => {
                put("kind", Value::from("operation"));
                put("behavior", Value::from(*behavior));
                put("operation", Value::from(*operation));
                put("params", (*params).clone());
                put("writes", Value::Bool(*writes));
                if let Some(caller) = caller {
                    put("caller", Value::from(*caller));
                }
                if let Some(precondition) = precondition {
                    put("precondition", (*precondition).clone());
                }
            }
        }
        Value::Object(out)
    }
}

/// What changed, for afterChange and afterReferenceChange.
#[derive(Clone, Debug, PartialEq)]
pub enum InstanceChange<'a> {
    Create,
    Update {
        patch: &'a Map<String, Value>,
        /// The own fields before the update.
        before: &'a Map<String, Value>,
    },
    Delete,
    Operation {
        behavior: &'a str,
        operation: &'a str,
        params: &'a Value,
        /// The own fields before the operation, when it changed them with
        /// update(); `None` when they did not change.
        before: Option<&'a Map<String, Value>>,
    },
}

impl InstanceChange<'_> {
    /// `create`, `update`, `delete` or `operation`.
    pub fn kind(&self) -> &'static str {
        match self {
            InstanceChange::Create => "create",
            InstanceChange::Update { .. } => "update",
            InstanceChange::Delete => "delete",
            InstanceChange::Operation { .. } => "operation",
        }
    }

    /// The change as the TypeScript engine's object holds it.
    pub fn to_json(&self) -> Value {
        let mut out = Map::new();
        out.insert("kind".into(), Value::from(self.kind()));
        match self {
            InstanceChange::Update { patch, before } => {
                out.insert("patch".into(), Value::Object((*patch).clone()));
                out.insert("before".into(), Value::Object((*before).clone()));
            }
            InstanceChange::Operation {
                behavior,
                operation,
                params,
                before,
            } => {
                out.insert("behavior".into(), Value::from(*behavior));
                out.insert("operation".into(), Value::from(*operation));
                out.insert("params".into(), (*params).clone());
                if let Some(before) = before {
                    out.insert("before".into(), Value::Object((*before).clone()));
                }
            }
            InstanceChange::Create | InstanceChange::Delete => {}
        }
        Value::Object(out)
    }
}

/// What a behavior's validate is asked to judge: the instance's own fields
/// a write would store, which the live version accepts.
#[derive(Clone, Debug, PartialEq)]
pub enum ValidationRequest<'a> {
    Create {
        data: &'a Map<String, Value>,
    },
    Update {
        before: &'a Map<String, Value>,
        after: &'a Map<String, Value>,
        /// The behavior whose operation applies it with update(); `None`
        /// for a caller's update.
        caller: Option<&'a str>,
    },
}

/// An instance's fields as a read returns them, and as the log holds them:
/// its own fields in `data`, its behaviors' in `behaviors`, by behavior
/// name. A reaction's `before()` returns one.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct InstanceFields {
    pub data: Map<String, Value>,
    pub behaviors: Map<String, Value>,
}

/// What `values.stow` returns: what the behavior's row keeps.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StowedObject {
    /// The object as JSON text, a ref in place of each top-level member the
    /// value store holds.
    pub json: String,
    /// The JSON pointers to those members, as JSON text; `None` when the
    /// store holds none.
    pub refs: Option<String>,
}

/// One stored instance, as afterConfigChange visits it.
#[derive(Clone, Debug, PartialEq)]
pub struct StoredInstance {
    pub id: String,
    /// Its own fields, the ones the value store holds put back.
    pub data: Map<String, Value>,
}

/// A field of the behavior's a list may filter on (list's `where`), or a
/// member of one whose value is an object (`<field>.<member>`): its value is
/// one of the behavior's columns, which `where` compares in SQL, a null
/// column holding no value.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BehaviorFilter {
    /// The column that holds the field's value, by the behavior's own name.
    pub column: String,
    /// The field's JSON type: `string`, `number`, `integer` or `boolean`.
    pub kind: String,
    /// What the value is, for the list tool's `where`.
    pub description: Option<String>,
}

impl BehaviorFilter {
    pub fn new(column: &str, kind: &str) -> Self {
        BehaviorFilter {
            column: column.to_owned(),
            kind: kind.to_owned(),
            description: None,
        }
    }

    pub fn described(mut self, description: &str) -> Self {
        self.description = Some(description.to_owned());
        self
    }
}

/// Which instance schema `instance_schema` describes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum InstanceSchemaForm {
    /// An instance's or a create's data.
    Instance,
    /// An update's merge patch.
    Patch,
}

/// One operation of a schema's instance type, as a behavior's guidance
/// reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DescribedTypeOperation {
    pub name: String,
    /// The behavior that adds it; `None` for the built-in ones.
    pub behavior: Option<String>,
    pub writes: bool,
    pub scope: super::declaration::OperationScope,
}

/// The type a config is described on, for guidance and
/// createParamsSchema (milestone 6's describe document).
#[derive(Clone, Debug, PartialEq)]
pub struct DescribeTarget {
    pub schema: String,
    pub type_name: String,
    pub behaviors: Vec<String>,
    pub configs: Map<String, Value>,
    pub operations: Vec<DescribedTypeOperation>,
}

/// One refusal an operation can meet, as its guidance lists it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct GuidanceError {
    pub code: String,
    pub description: Option<String>,
    pub common_correction: String,
}

/// What a behavior says about one operation under its config.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct OperationGuidance {
    pub use_when: Option<String>,
    pub do_not_use_when: Option<String>,
    pub success: Option<String>,
    pub errors: Vec<GuidanceError>,
}

/// What a behavior tells a caller about a type under its config.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BehaviorGuidance {
    pub summary: String,
    /// By operation name.
    pub operations: Vec<(String, OperationGuidance)>,
}

/// An instance operation's handler: its result is checked against its
/// resultSchema.
pub type OperationFn<B> =
    fn(&B, &OperationCx<'_, <B as Behavior>::Config>, &Value) -> Result<Value>;
/// A schema-level operation's handler.
pub type SchemaOperationFn<B> =
    fn(&B, &SchemaCx<'_, <B as Behavior>::Config>, &Value) -> Result<Value>;
/// Reads one declared field: a JSON value, or `None` (or null) for none.
pub type FieldFn<B> = fn(&B, &InstanceView<'_, <B as Behavior>::Config>) -> Result<Option<Value>>;
/// May veto a change: a [`Veto`] to refuse, `None` to allow.
pub type GuardFn<B> =
    fn(&B, &InstanceView<'_, <B as Behavior>::Config>, &GuardRequest<'_>) -> Result<Option<Veto>>;
/// Whether a new version may change the config: a reason to refuse, or
/// `None` to allow. `before` is `None` for an added behavior, `after` for a
/// removed one.
pub type ConfigChangeFn<B> = fn(
    &B,
    Option<&<B as Behavior>::Config>,
    Option<&<B as Behavior>::Config>,
    &ConfigChange<'_>,
) -> Result<Option<String>>;

/// Brings the behavior's own storage in line with a published config.
pub type AfterConfigChangeFn<B> = fn(&B, &PublishCx<'_, <B as Behavior>::Config>) -> Result<()>;
/// Sets up a behavior's state for a new instance, with its create
/// parameters.
pub type InitializeFn<B> = fn(&B, &InstanceCx<'_, <B as Behavior>::Config>, &Value) -> Result<()>;
/// Judges the own fields a write would store.
pub type ValidateFn<B> = fn(
    &B,
    &ValidationCx<'_, <B as Behavior>::Config>,
    &ValidationRequest<'_>,
) -> Result<Vec<ValidationIssue>>;
/// The types a behavior's validate checks values against under a config.
pub type CheckedTypesFn<B> = fn(&B, &<B as Behavior>::Config) -> Vec<String>;
/// What validate holds the own fields to, as JSON Schemas.
pub type InstanceSchemaFn<B> = fn(
    &B,
    &<B as Behavior>::Config,
    InstanceSchemaForm,
    &dyn Fn(&str, bool) -> Value,
) -> Vec<Value>;
/// What a behavior tells a caller about the type under its config.
pub type GuidanceFn<B> = fn(&B, &<B as Behavior>::Config, &DescribeTarget) -> BehaviorGuidance;
/// The create parameters a behavior takes under its config.
pub type CreateParamsSchemaFn<B> = fn(&B, &<B as Behavior>::Config, &DescribeTarget) -> Value;
/// Runs after a change of the instance.
pub type AfterChangeFn<B> =
    fn(&B, &InstanceCx<'_, <B as Behavior>::Config>, &InstanceChange<'_>) -> Result<()>;
/// May veto a change of an instance the behavior's instance refers to.
pub type GuardReferenceFn<B> = fn(
    &B,
    &InstanceView<'_, <B as Behavior>::Config>,
    &Reference,
    &GuardRequest<'_>,
) -> Result<Option<Veto>>;
/// Runs after a change of an instance the behavior's instance refers to.
pub type AfterReferenceChangeFn<B> = fn(
    &B,
    &ReferenceCx<'_, <B as Behavior>::Config>,
    &Reference,
    &InstanceChange<'_>,
) -> Result<()>;
/// The schemas besides its own whose events a behavior's reactions hear on
/// a schema; `None` turns them off there.
pub type WatchesFn<B> = fn(&B, &<B as Behavior>::Config, &str) -> Result<Option<Vec<String>>>;
/// Handles one committed event.
pub type ReactFn<B> = fn(&B, &ReactionCx<'_, <B as Behavior>::Config>, &EngineEvent) -> Result<()>;
/// A schedule's interval on a schema, for its config; `None` turns it off.
pub type EveryFn<B> = fn(&B, &<B as Behavior>::Config) -> Result<Option<i64>>;
/// One run of a schedule.
pub type ScheduleFn<B> = fn(&B, &ScheduleCx<'_, <B as Behavior>::Config>) -> Result<()>;

/// A behavior's reactions: after each commit the engine's runner hands the
/// events of the instances of a schema that composes it, and of the
/// schemas `watches` names, to `react`, one at a time in log order, per
/// namespace (the runner is milestone 6).
pub struct Reactions<B: Behavior> {
    /// The schemas besides its own whose events the reactions on a schema
    /// hear, for the schema's config; its own alone when absent. `None`
    /// turns them off on the schema.
    pub watches: Option<WatchesFn<B>>,
    pub react: ReactFn<B>,
}

/// How often a schedule runs.
pub enum Every<B: Behavior> {
    /// An interval in milliseconds, at least [`MIN_SCHEDULE_MS`].
    Ms(i64),
    /// An interval each schema's config gives, or `None` to turn the
    /// schedule off there.
    Config(EveryFn<B>),
}

/// The shortest interval a schedule takes, in milliseconds.
pub const MIN_SCHEDULE_MS: i64 = 1000;

/// Work the runner runs on each schema that composes the behavior, once an
/// interval.
pub struct Schedule<B: Behavior> {
    pub every: Every<B>,
    pub run: ScheduleFn<B>,
}

/// A behavior's hooks, each optional: an absent one keeps its TypeScript
/// meaning. Build one with `..Hooks::default()`.
pub struct Hooks<B: Behavior> {
    /// Whether a new version may change the config. Absent, only an
    /// identical config is allowed, and the behavior can be neither added
    /// to nor removed from a schema that has instances.
    pub config_change: Option<ConfigChangeFn<B>>,
    /// Brings the behavior's own storage in line with a published config,
    /// in the publish's transaction, once for each namespace whose
    /// instances the schema serves.
    pub after_config_change: Option<AfterConfigChangeFn<B>>,
    /// The storage it owns, created when a schema that composes it is
    /// published.
    pub migrations: Vec<BehaviorMigration>,
    /// Sets up its state for a new instance, once every guard allowed the
    /// create, with its own entry of the create's parameters (`{}` when the
    /// create gives none).
    pub initialize: Option<InitializeFn<B>>,
    /// May veto a create, an update, a delete or an operation of any
    /// behavior on the type; every guard runs in list order and the first
    /// veto wins.
    pub guard: Option<GuardFn<B>>,
    /// Judges the instance's own fields a create or an update would store.
    pub validate: Option<ValidateFn<B>>,
    /// The types of the document whose values validate checks under the
    /// config.
    pub checked_types: Option<CheckedTypesFn<B>>,
    /// What validate holds the instance's own fields to, as JSON Schemas
    /// (milestone 6's describe document); the function renders a type
    /// `checked_types` names, nullable or not.
    pub instance_schema: Option<InstanceSchemaFn<B>>,
    /// What the behavior tells a caller about the type under its config
    /// (milestone 6's describe document).
    pub guidance: Option<GuidanceFn<B>>,
    /// The create parameters it takes under the config, narrower than its
    /// declaration's createParamsSchema (milestone 6's describe document).
    pub create_params_schema: Option<CreateParamsSchemaFn<B>>,
    /// A handler per declared instance operation, by name.
    pub operations: Vec<(&'static str, OperationFn<B>)>,
    /// A handler per declared schema-level operation, by name.
    pub schema_operations: Vec<(&'static str, SchemaOperationFn<B>)>,
    /// A reader per declared field, by name.
    pub fields: Vec<(&'static str, FieldFn<B>)>,
    /// What a list may filter on, by a declared field's name or
    /// `<field>.<member>`.
    pub filters: Vec<(&'static str, BehaviorFilter)>,
    /// Runs after a create (after every initialize), an update, a delete or
    /// a writing operation a caller called, in list order.
    pub after_change: Option<AfterChangeFn<B>>,
    /// May veto an update, a delete or a writing operation of an instance
    /// the behavior's instance refers to.
    pub guard_reference: Option<GuardReferenceFn<B>>,
    /// Runs after a change of an instance the behavior's instance refers
    /// to, for each reference that hears it. After a delete it must remove
    /// the reference, through an operation of the referencing instance.
    pub after_reference_change: Option<AfterReferenceChangeFn<B>>,
    /// Reactions to committed events, which the runner runs.
    pub reactions: Option<Reactions<B>>,
    /// Timed work by name (camelCase), which the runner runs.
    pub schedules: Vec<(&'static str, Schedule<B>)>,
}

impl<B: Behavior> Default for Hooks<B> {
    fn default() -> Self {
        Hooks {
            config_change: None,
            after_config_change: None,
            migrations: Vec::new(),
            initialize: None,
            guard: None,
            validate: None,
            checked_types: None,
            instance_schema: None,
            guidance: None,
            create_params_schema: None,
            operations: Vec::new(),
            schema_operations: Vec::new(),
            fields: Vec::new(),
            filters: Vec::new(),
            after_change: None,
            guard_reference: None,
            after_reference_change: None,
            reactions: None,
            schedules: Vec::new(),
        }
    }
}

/// The behaviors an engine runs besides the core's: a value a project's
/// build assembles and passes in `EngineOptions::behaviors`.
#[derive(Clone, Default)]
pub struct BehaviorSet {
    pub(crate) behaviors: Vec<Arc<dyn super::erased::Erased>>,
}

impl std::fmt::Debug for BehaviorSet {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_list()
            .entries(self.behaviors.iter().map(|behavior| behavior.label()))
            .finish()
    }
}

impl BehaviorSet {
    pub fn new() -> Self {
        Self::default()
    }

    /// The set with a behavior added after the ones before it.
    pub fn with<B: Behavior>(mut self, behavior: B) -> Self {
        self.push(behavior);
        self
    }

    /// Adds a behavior after the ones before it.
    pub fn push<B: Behavior>(&mut self, behavior: B) {
        self.behaviors.push(super::erased::erase(behavior));
    }

    /// The set with every behavior of another after its own.
    pub fn extend(mut self, other: BehaviorSet) -> Self {
        self.behaviors.extend(other.behaviors);
        self
    }

    pub fn len(&self) -> usize {
        self.behaviors.len()
    }

    pub fn is_empty(&self) -> bool {
        self.behaviors.is_empty()
    }
}
