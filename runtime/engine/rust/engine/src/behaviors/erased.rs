//! A behavior with its type erased, as the engine holds it: its config is
//! an `Arc<dyn Any + Send + Sync>` the hooks get back as `B::Config`, and
//! each hook is called through the context the engine builds, a hook the
//! behavior leaves absent answering `None`. A hook that panics is caught
//! here, where the engine calls it, and is a [`crate::BehaviorError`]: no
//! panic unwinds through the engine's own frames.

use std::any::Any;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::Arc;

use serde_json::Value;

use super::behavior::{
    Behavior, ConfigChange, DescribeTarget, Every, GuardRequest, Hooks, InstanceChange,
    ValidationRequest,
};
use super::config::ConfigTarget;
use super::context::{
    Core, InstanceCx, InstanceView, OperationCx, PublishCx, ReactionCx, ReferenceCx, ScheduleCx,
    SchemaCx, Scope, ValidationCx,
};
use crate::errors::{Error, Result, ValidationIssue, Veto};
use crate::events::EngineEvent;
use crate::instances::references::Reference;

/// A config as the engine holds it: what `parse_config` returned.
pub(crate) type ConfigBox = Arc<dyn Any + Send + Sync>;

/// Which hooks an implementation has, by name, and what else it declares
/// beside its declaration: what the registry checks and the engine asks
/// before it calls one.
#[derive(Clone, Debug, Default)]
pub(crate) struct HookSet {
    pub config_change: bool,
    pub after_config_change: bool,
    pub initialize: bool,
    pub guard: bool,
    pub validate: bool,
    pub checked_types: bool,
    // The tools ask the implementation itself (Erased::instance_schema,
    // Erased::guidance); the flags say which it has, for a reader.
    #[allow(dead_code)]
    pub instance_schema: bool,
    #[allow(dead_code)]
    pub guidance: bool,
    pub create_params_schema: bool,
    pub after_change: bool,
    pub guard_reference: bool,
    pub after_reference_change: bool,
    pub reactions: bool,
    pub watches: bool,
    pub operations: Vec<String>,
    pub schema_operations: Vec<String>,
    pub fields: Vec<String>,
    pub filters: Vec<(String, super::behavior::BehaviorFilter)>,
    /// Each schedule's name and its fixed interval (`None` for one the
    /// config gives).
    pub schedules: Vec<(String, Option<i64>)>,
    pub migrations: Vec<superschematic_engine_storage::behavior::BehaviorMigration>,
    /// What the host of a dynamic behavior found wrong with it that the
    /// engine cannot see; registration refuses it with these. Always empty
    /// for a Rust behavior.
    pub problems: Vec<String>,
}

/// A behavior with its config type erased.
pub(crate) trait Erased: Send + Sync {
    /// Its declared name, or a label for a declaration it cannot read.
    fn label(&self) -> String;
    fn declaration(&self) -> &str;
    fn hook_set(&self) -> HookSet;

    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<ConfigBox>;
    fn config_change(
        &self,
        before: Option<&ConfigBox>,
        after: Option<&ConfigBox>,
        change: &ConfigChange<'_>,
    ) -> Option<Result<Option<String>>>;
    fn checked_types(&self, config: &ConfigBox) -> Option<Result<Vec<String>>>;
    fn after_config_change(
        &self,
        core: &dyn Core,
        config: Option<&ConfigBox>,
        before: Option<&ConfigBox>,
    ) -> Option<Result<()>>;
    fn initialize(&self, core: &dyn Core, config: &ConfigBox, params: &Value)
        -> Option<Result<()>>;
    fn guard(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        request: &GuardRequest<'_>,
    ) -> Option<Result<Option<Veto>>>;
    fn validate(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        request: &ValidationRequest<'_>,
    ) -> Option<Result<Vec<ValidationIssue>>>;
    fn operation(
        &self,
        name: &str,
        core: &dyn Core,
        config: &ConfigBox,
        params: &Value,
    ) -> Option<Result<Value>>;
    fn schema_operation(
        &self,
        name: &str,
        core: &dyn Core,
        config: &ConfigBox,
        params: &Value,
    ) -> Option<Result<Value>>;
    fn field(
        &self,
        name: &str,
        core: &dyn Core,
        config: &ConfigBox,
    ) -> Option<Result<Option<Value>>>;
    fn after_change(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        change: &InstanceChange<'_>,
    ) -> Option<Result<()>>;
    fn guard_reference(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        reference: &Reference,
        request: &GuardRequest<'_>,
    ) -> Option<Result<Option<Veto>>>;
    fn after_reference_change(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        reference: &Reference,
        change: &InstanceChange<'_>,
    ) -> Option<Result<()>>;
    fn watches(&self, config: &ConfigBox, schema: &str) -> Option<Result<Option<Vec<String>>>>;
    fn react(&self, core: &dyn Core, config: &ConfigBox, event: &EngineEvent)
        -> Option<Result<()>>;
    fn every(&self, schedule: &str, config: &ConfigBox) -> Option<Result<Option<i64>>>;
    fn run_schedule(
        &self,
        schedule: &str,
        core: &dyn Core,
        config: &ConfigBox,
    ) -> Option<Result<()>>;
    fn create_params_schema(
        &self,
        config: &ConfigBox,
        target: &DescribeTarget,
    ) -> Option<Result<Value>>;
    /// What a behavior tells a caller about the type under its config.
    fn guidance(
        &self,
        config: &ConfigBox,
        target: &DescribeTarget,
    ) -> Option<Result<super::behavior::BehaviorGuidance>>;
    /// What validate holds the instance's own fields to, in a form, as JSON
    /// Schemas; `render` writes a type `checked_types` names.
    fn instance_schema(
        &self,
        config: &ConfigBox,
        form: super::behavior::InstanceSchemaForm,
        render: &dyn Fn(&str, bool) -> Value,
    ) -> Option<Result<Vec<Value>>>;
}

/// Calls one hook of a behavior, catching a panic as a defect of the
/// behavior's.
pub(crate) fn run_hook<T>(label: &str, hook: &str, call: impl FnOnce() -> Result<T>) -> Result<T> {
    match catch_unwind(AssertUnwindSafe(call)) {
        Ok(result) => result,
        Err(payload) => {
            let message = payload
                .downcast_ref::<&str>()
                .map(|text| (*text).to_owned())
                .or_else(|| payload.downcast_ref::<String>().cloned())
                .unwrap_or_else(|| "a panic".to_owned());
            Err(Error::behavior(
                label,
                format!("{hook} panicked: {message}"),
            ))
        }
    }
}

/// Erases a behavior's config type.
pub(crate) fn erase<B: Behavior>(behavior: B) -> Arc<dyn Erased> {
    let hooks = behavior.hooks();
    Arc::new(Implementation { behavior, hooks })
}

struct Implementation<B: Behavior> {
    behavior: B,
    hooks: Hooks<B>,
}

impl<B: Behavior> Implementation<B> {
    fn label_or(&self) -> String {
        super::declaration::BehaviorDeclaration::parse(self.behavior.declaration())
            .map(|declaration| declaration.name)
            .unwrap_or_else(|_| "implementation".to_owned())
    }

    fn config<'c>(&self, config: &'c ConfigBox) -> Result<&'c B::Config> {
        config.downcast_ref::<B::Config>().ok_or_else(|| {
            Error::internal(format!(
                "behavior {}: a config of another behavior's type",
                self.label_or()
            ))
        })
    }

    // run calls one hook, catching a panic as a defect of the behavior's.
    fn run<T>(&self, hook: &str, call: impl FnOnce() -> Result<T>) -> Result<T> {
        run_hook(&self.label_or(), hook, call)
    }

    fn scope<'a>(core: &'a dyn Core, config: &'a B::Config) -> Scope<'a, B::Config> {
        Scope { core, config }
    }
}

impl<B: Behavior> Erased for Implementation<B> {
    fn label(&self) -> String {
        self.label_or()
    }

    fn declaration(&self) -> &str {
        self.behavior.declaration()
    }

    fn hook_set(&self) -> HookSet {
        let hooks = &self.hooks;
        HookSet {
            config_change: hooks.config_change.is_some(),
            after_config_change: hooks.after_config_change.is_some(),
            initialize: hooks.initialize.is_some(),
            guard: hooks.guard.is_some(),
            validate: hooks.validate.is_some(),
            checked_types: hooks.checked_types.is_some(),
            instance_schema: hooks.instance_schema.is_some(),
            guidance: hooks.guidance.is_some(),
            create_params_schema: hooks.create_params_schema.is_some(),
            after_change: hooks.after_change.is_some(),
            guard_reference: hooks.guard_reference.is_some(),
            after_reference_change: hooks.after_reference_change.is_some(),
            reactions: hooks.reactions.is_some(),
            watches: hooks
                .reactions
                .as_ref()
                .is_some_and(|reactions| reactions.watches.is_some()),
            operations: hooks
                .operations
                .iter()
                .map(|(name, _)| (*name).to_owned())
                .collect(),
            schema_operations: hooks
                .schema_operations
                .iter()
                .map(|(name, _)| (*name).to_owned())
                .collect(),
            fields: hooks
                .fields
                .iter()
                .map(|(name, _)| (*name).to_owned())
                .collect(),
            filters: hooks
                .filters
                .iter()
                .map(|(name, filter)| ((*name).to_owned(), filter.clone()))
                .collect(),
            schedules: hooks
                .schedules
                .iter()
                .map(|(name, schedule)| {
                    (
                        (*name).to_owned(),
                        match schedule.every {
                            Every::Ms(ms) => Some(ms),
                            Every::Config(_) => None,
                        },
                    )
                })
                .collect(),
            migrations: hooks.migrations.clone(),
            problems: Vec::new(),
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
        let hook = self.hooks.config_change?;
        Some((|| {
            let before = before.map(|config| self.config(config)).transpose()?;
            let after = after.map(|config| self.config(config)).transpose()?;
            self.run("configChange", || {
                hook(&self.behavior, before, after, change)
            })
        })())
    }

    fn checked_types(&self, config: &ConfigBox) -> Option<Result<Vec<String>>> {
        let hook = self.hooks.checked_types?;
        Some(
            self.config(config)
                .and_then(|config| self.run("checkedTypes", || Ok(hook(&self.behavior, config)))),
        )
    }

    fn after_config_change(
        &self,
        core: &dyn Core,
        config: Option<&ConfigBox>,
        before: Option<&ConfigBox>,
    ) -> Option<Result<()>> {
        let hook = self.hooks.after_config_change?;
        Some((|| {
            let config = config.map(|config| self.config(config)).transpose()?;
            let before = before.map(|config| self.config(config)).transpose()?;
            let cx = PublishCx {
                core,
                config,
                before,
            };
            self.run("afterConfigChange", || hook(&self.behavior, &cx))
        })())
    }

    fn initialize(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        params: &Value,
    ) -> Option<Result<()>> {
        let hook = self.hooks.initialize?;
        Some(self.config(config).and_then(|config| {
            let cx = InstanceCx {
                view: InstanceView {
                    scope: Self::scope(core, config),
                },
            };
            self.run("initialize", || hook(&self.behavior, &cx, params))
        }))
    }

    fn guard(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        request: &GuardRequest<'_>,
    ) -> Option<Result<Option<Veto>>> {
        let hook = self.hooks.guard?;
        Some(self.config(config).and_then(|config| {
            let view = InstanceView {
                scope: Self::scope(core, config),
            };
            self.run("guard", || hook(&self.behavior, &view, request))
        }))
    }

    fn validate(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        request: &ValidationRequest<'_>,
    ) -> Option<Result<Vec<ValidationIssue>>> {
        let hook = self.hooks.validate?;
        Some(self.config(config).and_then(|config| {
            let cx = ValidationCx { core, config };
            self.run("validate", || hook(&self.behavior, &cx, request))
        }))
    }

    fn operation(
        &self,
        name: &str,
        core: &dyn Core,
        config: &ConfigBox,
        params: &Value,
    ) -> Option<Result<Value>> {
        let (_, handler) = self
            .hooks
            .operations
            .iter()
            .find(|(declared, _)| *declared == name)?;
        Some(self.config(config).and_then(|config| {
            let cx = OperationCx {
                cx: InstanceCx {
                    view: InstanceView {
                        scope: Self::scope(core, config),
                    },
                },
            };
            self.run(&format!("operation {name}"), || {
                handler(&self.behavior, &cx, params)
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
        let (_, handler) = self
            .hooks
            .schema_operations
            .iter()
            .find(|(declared, _)| *declared == name)?;
        Some(self.config(config).and_then(|config| {
            let cx = SchemaCx {
                scope: Self::scope(core, config),
            };
            self.run(&format!("operation {name}"), || {
                handler(&self.behavior, &cx, params)
            })
        }))
    }

    fn field(
        &self,
        name: &str,
        core: &dyn Core,
        config: &ConfigBox,
    ) -> Option<Result<Option<Value>>> {
        let (_, reader) = self
            .hooks
            .fields
            .iter()
            .find(|(declared, _)| *declared == name)?;
        Some(self.config(config).and_then(|config| {
            let view = InstanceView {
                scope: Self::scope(core, config),
            };
            self.run(&format!("field {name}"), || reader(&self.behavior, &view))
        }))
    }

    fn after_change(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        change: &InstanceChange<'_>,
    ) -> Option<Result<()>> {
        let hook = self.hooks.after_change?;
        Some(self.config(config).and_then(|config| {
            let cx = InstanceCx {
                view: InstanceView {
                    scope: Self::scope(core, config),
                },
            };
            self.run("afterChange", || hook(&self.behavior, &cx, change))
        }))
    }

    fn guard_reference(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        reference: &Reference,
        request: &GuardRequest<'_>,
    ) -> Option<Result<Option<Veto>>> {
        let hook = self.hooks.guard_reference?;
        Some(self.config(config).and_then(|config| {
            let view = InstanceView {
                scope: Self::scope(core, config),
            };
            self.run("guardReference", || {
                hook(&self.behavior, &view, reference, request)
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
        let hook = self.hooks.after_reference_change?;
        Some(self.config(config).and_then(|config| {
            let cx = ReferenceCx {
                view: InstanceView {
                    scope: Self::scope(core, config),
                },
            };
            self.run("afterReferenceChange", || {
                hook(&self.behavior, &cx, reference, change)
            })
        }))
    }

    fn watches(&self, config: &ConfigBox, schema: &str) -> Option<Result<Option<Vec<String>>>> {
        let hook = self.hooks.reactions.as_ref()?.watches?;
        Some(self.config(config).and_then(|config| {
            self.run("reactions.watches", || hook(&self.behavior, config, schema))
        }))
    }

    fn react(
        &self,
        core: &dyn Core,
        config: &ConfigBox,
        event: &EngineEvent,
    ) -> Option<Result<()>> {
        let react = self.hooks.reactions.as_ref()?.react;
        Some(self.config(config).and_then(|config| {
            let cx = ReactionCx {
                scope: Self::scope(core, config),
            };
            self.run("react", || react(&self.behavior, &cx, event))
        }))
    }

    fn every(&self, schedule: &str, config: &ConfigBox) -> Option<Result<Option<i64>>> {
        let (_, found) = self
            .hooks
            .schedules
            .iter()
            .find(|(name, _)| *name == schedule)?;
        Some(match found.every {
            Every::Ms(ms) => Ok(Some(ms)),
            Every::Config(every) => self.config(config).and_then(|config| {
                self.run(&format!("schedule {schedule} everyMs"), || {
                    every(&self.behavior, config)
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
        let (_, found) = self
            .hooks
            .schedules
            .iter()
            .find(|(name, _)| *name == schedule)?;
        let run = found.run;
        Some(self.config(config).and_then(|config| {
            let cx = ScheduleCx {
                scope: Self::scope(core, config),
            };
            self.run(&format!("schedule {schedule}"), || run(&self.behavior, &cx))
        }))
    }

    fn create_params_schema(
        &self,
        config: &ConfigBox,
        target: &DescribeTarget,
    ) -> Option<Result<Value>> {
        let hook = self.hooks.create_params_schema?;
        Some(self.config(config).and_then(|config| {
            self.run("createParamsSchema", || {
                Ok(hook(&self.behavior, config, target))
            })
        }))
    }

    fn guidance(
        &self,
        config: &ConfigBox,
        target: &DescribeTarget,
    ) -> Option<Result<super::behavior::BehaviorGuidance>> {
        let hook = self.hooks.guidance?;
        Some(
            self.config(config).and_then(|config| {
                self.run("guidance", || Ok(hook(&self.behavior, config, target)))
            }),
        )
    }

    fn instance_schema(
        &self,
        config: &ConfigBox,
        form: super::behavior::InstanceSchemaForm,
        render: &dyn Fn(&str, bool) -> Value,
    ) -> Option<Result<Vec<Value>>> {
        let hook = self.hooks.instance_schema?;
        Some(self.config(config).and_then(|config| {
            self.run("instanceSchema", || {
                Ok(hook(&self.behavior, config, form, render))
            })
        }))
    }
}
