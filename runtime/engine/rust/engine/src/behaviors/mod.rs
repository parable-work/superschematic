//! The behavior plug-in API (D16, D58), the port of
//! `src/behaviors/{behavior,declaration,registry,composition,execution,storage,publish,paging}.ts`
//! and `src/values/behavior.ts`.
//!
//! A behavior is a Rust type that implements [`Behavior`]: its declaration
//! (the JSON file the compiler registers), a typed config its
//! `parse_config` makes of the JSON a type gives it, and its [`Hooks`]. An
//! engine runs the behaviors its options name ([`BehaviorSet`]) and the
//! ones registered later ([`Behaviors::register`]); a schema whose instance
//! type composes one is checked against it at define and publish, its
//! storage created at the publish, and its hooks run with every call on the
//! type's instances, inside the call's transaction.

pub(crate) mod behavior;
pub(crate) mod composition;
pub(crate) mod config;
pub(crate) mod context;
pub(crate) mod declaration;
#[cfg(feature = "dynamic")]
pub(crate) mod dynamic;
pub(crate) mod erased;
pub(crate) mod execution;
pub mod paging;
pub(crate) mod publish;
pub(crate) mod registry;

pub use behavior::{
    json_config, AfterChangeFn, AfterConfigChangeFn, AfterReferenceChangeFn, Behavior,
    BehaviorFilter, BehaviorGuidance, BehaviorSet, CheckedTypesFn, ConfigChange, ConfigChangeFn,
    CreateParamsSchemaFn, DescribeTarget, DescribedTypeOperation, Every, EveryFn, FieldFn, GuardFn,
    GuardReferenceFn, GuardRequest, GuidanceError, GuidanceFn, Hooks, InitializeFn, InstanceChange,
    InstanceFields, InstanceSchemaFn, InstanceSchemaForm, OperationFn, OperationGuidance, ReactFn,
    Reactions, Schedule, ScheduleFn, SchemaOperationFn, StoredInstance, StowedObject, ValidateFn,
    ValidationRequest, WatchesFn, MIN_SCHEDULE_MS,
};
pub use config::{
    ConfigBehaviorField, ConfigFieldKind, ConfigSchema, ConfigSchemas, ConfigTarget, ConfigType,
    ConfigTypeField, ConfigTypes,
};
pub use context::{
    Columns, ColumnsReader, InstanceCx, InstanceView, InstancesHandle, OperationCx, PublishCx,
    ReactionCx, ReadFields, ReferenceCx, ReferenceReader, References, ScheduleCx, SchemaCx,
    SchemasHandle, Scope, SqlReader, SqlWriter, ValidationCx, ValueReader, Values, MAX_BATCH_READ,
    MAX_CALL_DEPTH,
};
pub use declaration::{
    check_declaration, create_params_schema_problem, is_behavior_name, is_veto_code,
    BehaviorDeclaration, FieldDeclaration, OperationDeclaration, OperationScope, VetoDeclaration,
    BEHAVIOR_NAME, BUILTIN_OPERATIONS, VETO_CODE,
};
#[cfg(feature = "dynamic")]
pub use dynamic::{DynamicBehavior, DynamicHooks};
/// Paging for a behavior's list operations (`page`, `pageRequest`).
pub use paging::{page, page_request, Page, PageRequest};
/// A behavior's own storage: its migrations, the column and table specs
/// they take, and the SQL its hooks run (the storage crate's).
pub use superschematic_engine_storage::behavior::{BehaviorMigration, RELATION_COLUMNS};
pub use superschematic_engine_storage::{ColumnSpec, ColumnType, IndexSpec, TableSpec};

pub use crate::instances::references::{Hears, Reference};

use serde_json::Value;

use crate::engine::Engine;
use crate::errors::Result;

/// The behaviors an engine runs (`engine.behaviors`).
pub struct Behaviors<'e> {
    pub(crate) engine: &'e Engine,
}

impl Behaviors<'_> {
    /// Registers an implementation besides the ones the engine opened with.
    /// One the engine cannot run is refused, naming every problem (the
    /// TypeScript engine's TypeError); a name registers once. A behavior
    /// whose storage exists has it brought up to its last migration.
    pub fn register<B: Behavior>(&self, behavior: B) -> Result<()> {
        self.engine.catalog.register(
            erased::erase(behavior),
            &self.engine.db,
            self.engine.clock.now(),
        )
    }

    /// Registers a behavior whose hooks are named at run time, as
    /// [`Behaviors::register`] registers a Rust one (the `dynamic`
    /// feature: a binding's bridge to a behavior in the host's language).
    #[cfg(feature = "dynamic")]
    pub fn register_dynamic<D: DynamicBehavior>(&self, behavior: D) -> Result<()> {
        self.engine.catalog.register(
            dynamic::erase_dynamic(behavior),
            &self.engine.db,
            self.engine.clock.now(),
        )
    }

    /// Registers every behavior of a set, in order.
    pub fn register_set(&self, set: BehaviorSet) -> Result<()> {
        for behavior in set.behaviors {
            self.engine
                .catalog
                .register(behavior, &self.engine.db, self.engine.clock.now())?;
        }
        Ok(())
    }

    /// Whether an implementation of a name is registered.
    pub fn has(&self, name: &str) -> bool {
        self.engine.catalog.registry().lookup(name).is_some()
    }

    /// The registered behaviors' names, sorted.
    pub fn names(&self) -> Vec<String> {
        self.engine.catalog.registry().names()
    }

    /// A registered behavior's declaration, as its JSON holds it.
    pub fn declaration(&self, name: &str) -> Option<Value> {
        self.engine
            .catalog
            .registry()
            .lookup(name)
            .map(|behavior| behavior.declaration.json.clone())
    }
}
