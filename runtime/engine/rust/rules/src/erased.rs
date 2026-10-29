//! A behavior with its config type erased, so the core holds behaviors of
//! every config type in one list and a run holds each use's parsed config
//! as `dyn Any`. Each call downcasts the config back to the behavior's
//! type and hands the hook the typed context it expects.

use std::any::Any;

use serde_json::Value;

use crate::behavior::{
    Behavior, Change, ConfigError, ConfigTarget, Declaration, GuardRequest, Hook, ValidationRequest,
};
use crate::context::{ConfigCx, Cx, View};
use crate::refusal::{SchemaIssue, ValidationIssue, Veto};
use crate::run::{Frame, Run};

/// A use's parsed config, as a run holds it.
pub(crate) type AnyConfig = Box<dyn Any + Send + Sync>;

/// Where a hook runs: the run, the frame (a type, its uses and a row), the
/// use's position in the type's list, the action a veto refuses, and
/// whether the hook may write.
#[derive(Clone, Copy)]
pub(crate) struct Scope<'s> {
    pub run: &'s Run<'s>,
    pub frame: Frame<'s>,
    pub index: usize,
    pub action: &'s str,
    pub writable: bool,
}

pub(crate) trait Erased: Send + Sync {
    fn declaration(&self) -> Declaration;
    fn parse(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<AnyConfig, ConfigError>;
    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&(dyn Any + Send + Sync)>,
        after: Option<&(dyn Any + Send + Sync)>,
    ) -> Hook<Option<String>>;
    fn check_create_params(
        &self,
        config: &(dyn Any + Send + Sync),
        params: &Value,
    ) -> Option<Vec<SchemaIssue>>;
    fn check_params(
        &self,
        config: &(dyn Any + Send + Sync),
        operation: &str,
        params: &Value,
    ) -> Option<Vec<SchemaIssue>>;
    fn initialize(&self, scope: Scope<'_>, params: &Value) -> Hook<()>;
    fn guard(&self, scope: Scope<'_>, request: &GuardRequest<'_>) -> Hook<Option<Veto>>;
    fn validate(
        &self,
        scope: Scope<'_>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>>;
    fn operation(&self, scope: Scope<'_>, name: &str, params: &Value) -> Hook<Value>;
    fn deadline(&self, scope: Scope<'_>, name: &str, payload: &Value) -> Hook<()>;
    fn after_change(&self, scope: Scope<'_>, change: &Change<'_>) -> Hook<()>;
}

/// A typed behavior behind [`Erased`].
pub(crate) struct Typed<B>(pub B);

// typed downcasts one side of a config change to the behavior's config.
fn typed<'c, C: 'static>(
    cx: &ConfigCx<'_>,
    side: Option<&'c (dyn Any + Send + Sync)>,
) -> Hook<Option<&'c C>> {
    match side {
        None => Ok(None),
        Some(config) => config
            .downcast_ref::<C>()
            .map(Some)
            .ok_or_else(|| cx.defect("the core holds a config of another type")),
    }
}

impl<B: Behavior> Typed<B> {
    // view is the hook's read-only context, its config downcast; a config
    // of another type is a defect of the core's, refused as the behavior's.
    fn view<'s>(&self, scope: Scope<'s>) -> Hook<View<'s, B::Config>> {
        let config = scope.frame.uses[scope.index]
            .config
            .downcast_ref::<B::Config>()
            .ok_or_else(|| {
                scope.run.defect_of(
                    scope.frame,
                    scope.index,
                    "the core holds a config of another type for this use",
                )
            })?;
        Ok(View {
            run: scope.run,
            frame: scope.frame,
            index: scope.index,
            config,
            action: scope.action,
        })
    }

    fn cx<'s>(&self, scope: Scope<'s>) -> Hook<Cx<'s, B::Config>> {
        Ok(Cx {
            view: self.view(scope)?,
            writable: scope.writable,
        })
    }
}

impl<B: Behavior> Erased for Typed<B> {
    fn declaration(&self) -> Declaration {
        self.0.declaration()
    }

    fn parse(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<AnyConfig, ConfigError> {
        self.0
            .parse_config(raw, target)
            .map(|config| Box::new(config) as AnyConfig)
    }

    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&(dyn Any + Send + Sync)>,
        after: Option<&(dyn Any + Send + Sync)>,
    ) -> Hook<Option<String>> {
        self.0.config_change(
            cx,
            typed::<B::Config>(cx, before)?,
            typed::<B::Config>(cx, after)?,
        )
    }

    fn check_create_params(
        &self,
        config: &(dyn Any + Send + Sync),
        params: &Value,
    ) -> Option<Vec<SchemaIssue>> {
        let config = config.downcast_ref::<B::Config>()?;
        Some(self.0.check_create_params(config, params))
    }

    fn check_params(
        &self,
        config: &(dyn Any + Send + Sync),
        operation: &str,
        params: &Value,
    ) -> Option<Vec<SchemaIssue>> {
        let config = config.downcast_ref::<B::Config>()?;
        Some(self.0.check_params(config, operation, params))
    }

    fn initialize(&self, scope: Scope<'_>, params: &Value) -> Hook<()> {
        self.0.initialize(&self.cx(scope)?, params)
    }

    fn guard(&self, scope: Scope<'_>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        self.0.guard(&self.view(scope)?, request)
    }

    fn validate(
        &self,
        scope: Scope<'_>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>> {
        self.0.validate(&self.view(scope)?, request)
    }

    fn operation(&self, scope: Scope<'_>, name: &str, params: &Value) -> Hook<Value> {
        self.0.operation(&self.cx(scope)?, name, params)
    }

    fn deadline(&self, scope: Scope<'_>, name: &str, payload: &Value) -> Hook<()> {
        self.0.deadline(&self.cx(scope)?, name, payload)
    }

    fn after_change(&self, scope: Scope<'_>, change: &Change<'_>) -> Hook<()> {
        self.0.after_change(&self.cx(scope)?, change)
    }
}
