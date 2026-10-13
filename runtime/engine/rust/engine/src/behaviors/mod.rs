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
/// Paging for a behavior's list operations (`page`, `pageRequest`).
pub use paging::{page, page_request, Page, PageRequest};
/// A behavior's own storage: its migrations, the column and table specs
/// they take, and the SQL its hooks run (the storage crate's).
pub use superschematic_engine_storage::behavior::{BehaviorMigration, RELATION_COLUMNS};
pub use superschematic_engine_storage::{ColumnSpec, ColumnType, IndexSpec, TableSpec};

pub use crate::instances::references::{Hears, Reference};

use serde_json::Value;

use crate::engine::Engine;
use crate::errors::{Error, Result};
use crate::principal::Principal;

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

    /// Runs one schedule of a behavior once on a schema's live version in a
    /// namespace (`default` when `None`), as the runner's work runs it: as
    /// `principal`, at the engine clock's time, in a write of the
    /// namespace, the events it writes recording the schedule as their
    /// cause at depth 1, and `previous` as when its last run committed. A
    /// failure rolls the run back and is returned; the runner (milestone 6)
    /// records it, backs off and runs the schedule again.
    ///
    /// A test's entry point to a behavior's schedule until the runner
    /// drives schedules: it keeps no record of the run, and neither asks
    /// whether the schedule is due nor skips an archived namespace.
    #[doc(hidden)]
    pub fn run_schedule(
        &self,
        principal: &Principal,
        namespace: Option<&str>,
        schema: &str,
        behavior: &str,
        schedule: &str,
        previous: Option<i64>,
    ) -> Result<()> {
        use crate::behaviors::execution::{Chain, Execution, Own};
        use crate::events::EventCause;
        use crate::instances::reach::Reach;
        use crate::namespaces::NamespaceView;

        let engine = self.engine;
        crate::principal::check_principal(principal)?;
        let lock = superschematic_engine_storage::Lock::Namespace(
            namespace.unwrap_or(crate::namespaces::DEFAULT_NAMESPACE),
        );
        engine.write(lock, |tx| {
            let view = NamespaceView::load(&engine.namespaces, tx)?;
            let namespace = view.resolve(namespace)?;
            let record = crate::instances::live(&engine.catalog, tx, &view, &namespace, schema)?;
            let runtime = engine.catalog.runtime_of(tx, &record)?;
            let index = runtime
                .composition
                .position(behavior)
                .filter(|&index| {
                    runtime.composition.behaviors[index]
                        .behavior
                        .hooks
                        .schedules
                        .iter()
                        .any(|(name, _)| name == schedule)
                })
                .ok_or_else(|| {
                    Error::Usage(format!(
                        "schema {schema} composes no behavior {behavior} with a schedule {schedule}"
                    ))
                })?;
            let mut chain = Chain::new(principal, &namespace, engine.clock.now());
            chain.cause = Some(EventCause {
                behavior: behavior.to_owned(),
                event: None,
                schedule: Some(schedule.to_owned()),
                depth: 1,
            });
            let reach = Reach {
                engine,
                tx,
                view: &view,
                chain: &chain,
            };
            Execution::new(
                reach,
                std::sync::Arc::clone(&runtime),
                schema,
                record.version.unwrap_or_default(),
                "",
                Own::None,
                true,
            )
            .schedule(index, schedule, previous)
        })
    }
}
