//! A behavior whose hooks are known when it registers rather than when it
//! compiles (the `dynamic` feature): what a binding's bridge registers for
//! a behavior written in the host's language, as the Node-API test addon
//! does for one written in TypeScript (D58, milestone 7b). It is the
//! [`Behavior`](super::Behavior) trait with its hooks named at run time:
//! [`DynamicHooks`] says which it has, and each hook is a method that gets
//! the name of the operation, field or schedule it runs, with the same
//! contexts a Rust behavior's hooks get. The engine runs it as it runs any
//! other: registered, composed, checked and called inside the call's
//! transaction, a panic in a hook a [`crate::BehaviorError`].
//!
//! A host's hook that throws returns [`Error::Host`]: what it threw,
//! carried back to the host's binding as it was thrown.

use std::sync::Arc;

use serde_json::Value;

use super::behavior::{
    BehaviorFilter, BehaviorGuidance, ConfigChange, DescribeTarget, GuardRequest, InstanceChange,
    InstanceSchemaForm, ValidationRequest,
};
use super::config::ConfigTarget;
use super::context::{
    Core, InstanceCx, InstanceView, NamespaceCx, OperationCx, PublishCx, ReactionCx, ReferenceCx,
    ScheduleCx, SchemaCx, Scope, ValidationCx,
};
use super::erased::{ConfigBox, Erased, HookSet};
use crate::errors::{Error, Result, ValidationIssue, Veto};
use crate::events::EngineEvent;
use crate::instances::references::Reference;
use superschematic_engine_storage::behavior::BehaviorMigration;

/// Which hooks a dynamic behavior has, and what it declares beside its
/// declaration, as [`super::Hooks`] says it for a Rust one.
#[derive(Clone, Default)]
pub struct DynamicHooks {
    pub config_change: bool,
    pub after_config_change: bool,
    pub initialize: bool,
    pub guard: bool,
    pub validate: bool,
    pub checked_types: bool,
    pub instance_schema: bool,
    pub guidance: bool,
    pub create_params_schema: bool,
    pub after_change: bool,
    pub guard_reference: bool,
    pub after_reference_change: bool,
    /// Whether it has reactions, and whether they have `watches`.
    pub reactions: bool,
    pub watches: bool,
    /// The names of its instance operations, schema-level operations and
    /// fields, as it implements them (registration holds them to its
    /// declaration's).
    pub operations: Vec<String>,
    pub schema_operations: Vec<String>,
    /// The names of its namespace operations (D77).
    pub namespace_operations: Vec<String>,
    pub fields: Vec<String>,
    pub filters: Vec<(String, BehaviorFilter)>,
    /// Each schedule's name and its fixed interval; `None` for one whose
    /// interval the config gives ([`DynamicBehavior::every`]).
    pub schedules: Vec<(String, Option<i64>)>,
    pub migrations: Vec<BehaviorMigration>,
    /// What the host found wrong with the implementation that the engine
    /// cannot see (a hook that is no function, say): registration refuses
    /// it with these among its own problems.
    pub problems: Vec<String>,
}

/// A behavior whose hooks are named at run time. Each hook method is
/// called only when [`DynamicHooks`] says the behavior has it; the default
/// bodies are never reached.
#[allow(unused_variables)]
pub trait DynamicBehavior: Send + Sync + 'static {
    /// What its hooks get as config.
    type Config: Send + Sync + 'static;

    /// The declaration, as JSON text.
    fn declaration(&self) -> &str;

    /// Which hooks it has.
    fn hooks(&self) -> DynamicHooks;

    /// The config the hooks get, as [`super::Behavior::parse_config`].
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<Self::Config>;

    fn config_change(
        &self,
        before: Option<&Self::Config>,
        after: Option<&Self::Config>,
        change: &ConfigChange<'_>,
    ) -> Result<Option<String>> {
        Err(absent("configChange"))
    }

    fn checked_types(&self, config: &Self::Config) -> Result<Vec<String>> {
        Err(absent("checkedTypes"))
    }

    fn after_config_change(&self, cx: &PublishCx<'_, Self::Config>) -> Result<()> {
        Err(absent("afterConfigChange"))
    }

    fn initialize(&self, cx: &InstanceCx<'_, Self::Config>, params: &Value) -> Result<()> {
        Err(absent("initialize"))
    }

    fn guard(
        &self,
        view: &InstanceView<'_, Self::Config>,
        request: &GuardRequest<'_>,
    ) -> Result<Option<Veto>> {
        Err(absent("guard"))
    }

    fn validate(
        &self,
        cx: &ValidationCx<'_, Self::Config>,
        request: &ValidationRequest<'_>,
    ) -> Result<Vec<ValidationIssue>> {
        Err(absent("validate"))
    }

    fn instance_schema(
        &self,
        config: &Self::Config,
        form: InstanceSchemaForm,
        render: &dyn Fn(&str, bool) -> Value,
    ) -> Result<Vec<Value>> {
        Err(absent("instanceSchema"))
    }

    fn guidance(&self, config: &Self::Config, target: &DescribeTarget) -> Result<BehaviorGuidance> {
        Err(absent("guidance"))
    }

    fn create_params_schema(
        &self,
        config: &Self::Config,
        target: &DescribeTarget,
    ) -> Result<Value> {
        Err(absent("createParamsSchema"))
    }

    fn operation(
        &self,
        name: &str,
        cx: &OperationCx<'_, Self::Config>,
        params: &Value,
    ) -> Result<Value> {
        Err(absent("operations"))
    }

    fn schema_operation(
        &self,
        name: &str,
        cx: &SchemaCx<'_, Self::Config>,
        params: &Value,
    ) -> Result<Value> {
        Err(absent("schemaOperations"))
    }

    fn namespace_operation(
        &self,
        name: &str,
        cx: &NamespaceCx<'_, Self::Config>,
        params: &Value,
    ) -> Result<Value> {
        Err(absent("namespaceOperations"))
    }

    fn field(&self, name: &str, view: &InstanceView<'_, Self::Config>) -> Result<Option<Value>> {
        Err(absent("fields"))
    }

    fn after_change(
        &self,
        cx: &InstanceCx<'_, Self::Config>,
        change: &InstanceChange<'_>,
    ) -> Result<()> {
        Err(absent("afterChange"))
    }

    fn guard_reference(
        &self,
        view: &InstanceView<'_, Self::Config>,
        reference: &Reference,
        request: &GuardRequest<'_>,
    ) -> Result<Option<Veto>> {
        Err(absent("guardReference"))
    }

    fn after_reference_change(
        &self,
        cx: &ReferenceCx<'_, Self::Config>,
        reference: &Reference,
        change: &InstanceChange<'_>,
    ) -> Result<()> {
        Err(absent("afterReferenceChange"))
    }

    fn watches(&self, config: &Self::Config, schema: &str) -> Result<Option<Vec<String>>> {
        Err(absent("reactions.watches"))
    }

    fn react(&self, cx: &ReactionCx<'_, Self::Config>, event: &EngineEvent) -> Result<()> {
        Err(absent("reactions.react"))
    }

    /// The interval of a schedule whose interval the config gives.
    fn every(&self, schedule: &str, config: &Self::Config) -> Result<Option<i64>> {
        Err(absent("schedules everyMs"))
    }

    fn run_schedule(&self, schedule: &str, cx: &ScheduleCx<'_, Self::Config>) -> Result<()> {
        Err(absent("schedules run"))
    }
}

fn absent(hook: &str) -> Error {
    Error::internal(format!(
        "the engine called {hook} of a dynamic behavior that has none"
    ))
}

/// Erases a dynamic behavior's config type.
pub(crate) fn erase_dynamic<D: DynamicBehavior>(behavior: D) -> Arc<dyn Erased> {
    let hooks = behavior.hooks();
    // The declaration's name, read once, as erase reads it.
    let label = super::declaration::BehaviorDeclaration::parse(behavior.declaration())
        .map(|declaration| declaration.name)
        .unwrap_or_else(|_| "implementation".to_owned());
    Arc::new(Dynamic {
        behavior,
        hooks,
        label,
    })
}

struct Dynamic<D: DynamicBehavior> {
    behavior: D,
    hooks: DynamicHooks,
    label: String,
}

impl<D: DynamicBehavior> Dynamic<D> {
    fn label_or(&self) -> String {
        self.label.clone()
    }

    fn config<'c>(&self, config: &'c ConfigBox) -> Result<&'c D::Config> {
        config.downcast_ref::<D::Config>().ok_or_else(|| {
            Error::internal(format!(
                "behavior {}: a config of another behavior's type",
                self.label_or()
            ))
        })
    }

    // run calls one hook, catching a panic as a defect of the behavior's.
    fn run<T>(&self, hook: &str, call: impl FnOnce() -> Result<T>) -> Result<T> {
        super::erased::run_hook(&self.label, hook, call)
    }

    fn scope<'a>(core: &'a dyn Core, config: &'a D::Config) -> Scope<'a, D::Config> {
        Scope { core, config }
    }
}

impl<D: DynamicBehavior> Erased for Dynamic<D> {
    fn label(&self) -> String {
        self.label_or()
    }

    fn declaration(&self) -> &str {
        self.behavior.declaration()
    }

    fn hook_set(&self) -> HookSet {
        let hooks = &self.hooks;
        HookSet {
            config_change: hooks.config_change,
            after_config_change: hooks.after_config_change,
            initialize: hooks.initialize,
            guard: hooks.guard,
            validate: hooks.validate,
            checked_types: hooks.checked_types,
            instance_schema: hooks.instance_schema,
            guidance: hooks.guidance,
            create_params_schema: hooks.create_params_schema,
            after_change: hooks.after_change,
            guard_reference: hooks.guard_reference,
            after_reference_change: hooks.after_reference_change,
            reactions: hooks.reactions,
            watches: hooks.reactions && hooks.watches,
            operations: hooks.operations.clone(),
            schema_operations: hooks.schema_operations.clone(),
            namespace_operations: hooks.namespace_operations.clone(),
            fields: hooks.fields.clone(),
            filters: hooks.filters.clone(),
            schedules: hooks.schedules.clone(),
            migrations: hooks.migrations.clone(),
            problems: hooks.problems.clone(),
        }
    }

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<ConfigBox> {
        let config = self.run("parseConfig", || self.behavior.parse_config(raw, target))?;
        Ok(Arc::new(config))
    }

    fn config_change(
        &self,
        before: Option<&ConfigBox>,
        after: Option<&ConfigBox>,
        change: &ConfigChange<'_>,
    ) -> Option<Result<Option<String>>> {
        if !self.hooks.config_change {
            return None;
        }
        Some((|| {
            let before = before.map(|config| self.config(config)).transpose()?;
            let after = after.map(|config| self.config(config)).transpose()?;
            self.run("configChange", || {
                self.behavior.config_change(before, after, change)
            })
        })())
    }

    fn checked_types(&self, config: &ConfigBox) -> Option<Result<Vec<String>>> {
        if !self.hooks.checked_types {
            return None;
        }
        Some(
            self.config(config).and_then(|config| {
                self.run("checkedTypes", || self.behavior.checked_types(config))
            }),
        )
    }

    fn after_config_change(
        &self,
        core: &dyn Core,
        config: Option<&ConfigBox>,
        before: Option<&ConfigBox>,
    ) -> Option<Result<()>> {
        if !self.hooks.after_config_change {
            return None;
        }
        Some((|| {
            let config = config.map(|config| self.config(config)).transpose()?;
            let before = before.map(|config| self.config(config)).transpose()?;
            let cx = PublishCx {
                core,
                config,
                before,
            };
            self.run("afterConfigChange", || {
                self.behavior.after_config_change(&cx)
            })
        })())
    }

    fn initialize(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        params: &Value,
    ) -> Option<Result<()>> {
        if !self.hooks.initialize {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let cx = InstanceCx {
                view: InstanceView {
                    scope: Self::scope(core, config),
                },
            };
            self.run("initialize", || self.behavior.initialize(&cx, params))
        }))
    }

    fn guard(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        request: &GuardRequest<'_>,
    ) -> Option<Result<Option<Veto>>> {
        if !self.hooks.guard {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let view = InstanceView {
                scope: Self::scope(core, config),
            };
            self.run("guard", || self.behavior.guard(&view, request))
        }))
    }

    fn validate(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        request: &ValidationRequest<'_>,
    ) -> Option<Result<Vec<ValidationIssue>>> {
        if !self.hooks.validate {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let cx = ValidationCx { core, config };
            self.run("validate", || self.behavior.validate(&cx, request))
        }))
    }

    fn operation(
        &self,
        name: &str,
        core: &dyn Core,
        config: &ConfigBox,
        params: &Value,
    ) -> Option<Result<Value>> {
        if !self
            .hooks
            .operations
            .iter()
            .any(|declared| declared == name)
        {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let cx = OperationCx {
                cx: InstanceCx {
                    view: InstanceView {
                        scope: Self::scope(core, config),
                    },
                },
            };
            self.run(&format!("operation {name}"), || {
                self.behavior.operation(name, &cx, params)
            })
        }))
    }

    fn schema_operation(
        &self,
        name: &str,
        core: &dyn Core,
        config: &ConfigBox,
        params: &Value,
    ) -> Option<Result<Value>> {
        if !self
            .hooks
            .schema_operations
            .iter()
            .any(|declared| declared == name)
        {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let cx = SchemaCx {
                scope: Self::scope(core, config),
            };
            self.run(&format!("operation {name}"), || {
                self.behavior.schema_operation(name, &cx, params)
            })
        }))
    }

    fn namespace_operation(
        &self,
        name: &str,
        core: &dyn Core,
        params: &Value,
    ) -> Option<Result<Value>> {
        if !self
            .hooks
            .namespace_operations
            .iter()
            .any(|declared| declared == name)
        {
            return None;
        }
        let cx = NamespaceCx {
            core,
            config: std::marker::PhantomData,
        };
        Some(self.run(&format!("operation {name}"), || {
            self.behavior.namespace_operation(name, &cx, params)
        }))
    }

    fn field(
        &self,
        name: &str,
        core: &dyn Core,
        config: &ConfigBox,
    ) -> Option<Result<Option<Value>>> {
        if !self.hooks.fields.iter().any(|declared| declared == name) {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let view = InstanceView {
                scope: Self::scope(core, config),
            };
            self.run(&format!("field {name}"), || {
                self.behavior.field(name, &view)
            })
        }))
    }

    fn after_change(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        change: &InstanceChange<'_>,
    ) -> Option<Result<()>> {
        if !self.hooks.after_change {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let cx = InstanceCx {
                view: InstanceView {
                    scope: Self::scope(core, config),
                },
            };
            self.run("afterChange", || self.behavior.after_change(&cx, change))
        }))
    }

    fn guard_reference(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        reference: &Reference,
        request: &GuardRequest<'_>,
    ) -> Option<Result<Option<Veto>>> {
        if !self.hooks.guard_reference {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let view = InstanceView {
                scope: Self::scope(core, config),
            };
            self.run("guardReference", || {
                self.behavior.guard_reference(&view, reference, request)
            })
        }))
    }

    fn after_reference_change(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        reference: &Reference,
        change: &InstanceChange<'_>,
    ) -> Option<Result<()>> {
        if !self.hooks.after_reference_change {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let cx = ReferenceCx {
                view: InstanceView {
                    scope: Self::scope(core, config),
                },
            };
            self.run("afterReferenceChange", || {
                self.behavior.after_reference_change(&cx, reference, change)
            })
        }))
    }

    fn watches(&self, config: &ConfigBox, schema: &str) -> Option<Result<Option<Vec<String>>>> {
        if !(self.hooks.reactions && self.hooks.watches) {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            self.run("reactions.watches", || {
                self.behavior.watches(config, schema)
            })
        }))
    }

    fn react(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        event: &EngineEvent,
    ) -> Option<Result<()>> {
        if !self.hooks.reactions {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            let cx = ReactionCx {
                scope: Self::scope(core, config),
            };
            self.run("react", || self.behavior.react(&cx, event))
        }))
    }

    fn every(&self, schedule: &str, config: &ConfigBox) -> Option<Result<Option<i64>>> {
        let (_, fixed) = self
            .hooks
            .schedules
            .iter()
            .find(|(name, _)| name == schedule)?;
        Some(match fixed {
            Some(ms) => Ok(Some(*ms)),
            None => self.config(config).and_then(|config| {
                self.run(&format!("schedule {schedule} everyMs"), || {
                    self.behavior.every(schedule, config)
                })
            }),
        })
    }

    fn run_schedule(
        &self,
        schedule: &str,
        core: &dyn Core,
        config: &ConfigBox,
    ) -> Option<Result<()>> {
        self.hooks
            .schedules
            .iter()
            .find(|(name, _)| name == schedule)?;
        Some(self.config(config).and_then(|config| {
            let cx = ScheduleCx {
                scope: Self::scope(core, config),
            };
            self.run(&format!("schedule {schedule}"), || {
                self.behavior.run_schedule(schedule, &cx)
            })
        }))
    }

    fn create_params_schema(
        &self,
        config: &ConfigBox,
        target: &DescribeTarget,
    ) -> Option<Result<Value>> {
        if !self.hooks.create_params_schema {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            self.run("createParamsSchema", || {
                self.behavior.create_params_schema(config, target)
            })
        }))
    }

    fn guidance(
        &self,
        config: &ConfigBox,
        target: &DescribeTarget,
    ) -> Option<Result<BehaviorGuidance>> {
        if !self.hooks.guidance {
            return None;
        }
        Some(
            self.config(config)
                .and_then(|config| self.run("guidance", || self.behavior.guidance(config, target))),
        )
    }

    fn instance_schema(
        &self,
        config: &ConfigBox,
        form: InstanceSchemaForm,
        render: &dyn Fn(&str, bool) -> Value,
    ) -> Option<Result<Vec<Value>>> {
        if !self.hooks.instance_schema {
            return None;
        }
        Some(self.config(config).and_then(|config| {
            self.run("instanceSchema", || {
                self.behavior.instance_schema(config, form, render)
            })
        }))
    }
}
