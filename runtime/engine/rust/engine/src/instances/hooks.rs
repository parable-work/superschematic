//! Where a write reaches its instance's behaviors: the extension points of
//! the instance store's write path, which milestone 5 fills with the
//! behavior API (`src/behaviors/execution.ts`).
//!
//! Until then no implementation is registered, so a live version that
//! composes a behavior is `unavailable` before any write reaches here, and
//! every composition the store sees is empty: each point checks what the
//! TypeScript engine checks of a call's arguments against the composition
//! (a create's parameters, an update's preconditions) and otherwise does
//! nothing. The store calls each one where the TypeScript engine does, in
//! the same order, so milestone 5 changes what they do and not the path:
//!
//! - [`Chain`]: the call a write belongs to (its principal, namespace,
//!   time and cause), which milestone 5 extends with the instances being
//!   written up the call, its nesting and its depth;
//! - [`check_create_params`] and [`check_preconditions`]: a call's
//!   parameters for the type's behaviors, by behavior name;
//! - [`Execution`]: the behaviors on one instance during a write, its
//!   `validate`, `guard`, `initialize` and `afterChange` hooks and its
//!   field readers;
//! - [`Execution::guard_references`] and [`Execution::referenced`]: the
//!   behaviors of other instances that refer to the one written.

use serde_json::{Map, Value};

use crate::composition::Composition;
use crate::errors::{EngineError, Result, SchemaIssue, ValidationIssue};
use crate::events::EventCause;
use crate::instances::references::Move;
use crate::json;
use crate::principal::Principal;

/// The call a write belongs to: who makes it, in which namespace, when
/// (read once per call, so every write of one call has one time), and, for
/// the runner's work, what caused it.
pub(crate) struct Chain<'p> {
    pub principal: &'p Principal,
    pub namespace: String,
    pub now: i64,
    pub cause: Option<EventCause>,
}

impl<'p> Chain<'p> {
    pub fn new(principal: &'p Principal, namespace: &str, now: i64) -> Self {
        Chain {
            principal,
            namespace: namespace.to_owned(),
            now,
            cause: None,
        }
    }
}

/// A create's parameters for the type's behaviors, by behavior name, each
/// checked against its behavior's createParamsSchema (milestone 5); a name
/// the type does not compose, or whose behavior takes none, is refused
/// (CreateParamsError).
pub(crate) fn check_create_params(
    composition: &Composition,
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
    let mut issues = Vec::new();
    for name in json::js_keys(given) {
        let at = format!("/behaviors{}", json::pointer(&[name]));
        if composition.bound(name).is_none() {
            // Milestone 5 lists the behaviors that declare createParams.
            issues.push(SchemaIssue::new(
                at,
                format!(
                    "{schema} does not compose behavior {name} (its behaviors that take create parameters: none)"
                ),
            ));
            continue;
        }
        issues.push(SchemaIssue::new(
            at,
            format!("behavior {name} takes no create parameters"),
        ));
    }
    if !issues.is_empty() {
        return Err(EngineError::create_params(schema, issues));
    }
    Ok(Map::new())
}

/// An update's, a delete's or an operation's preconditions by behavior:
/// each entry names a behavior the type composes that declares a
/// preconditionSchema and holds what it accepts (milestone 5), or the call
/// is refused (PreconditionsError).
pub(crate) fn check_preconditions(
    composition: &Composition,
    schema: &str,
    preconditions: Option<&Value>,
) -> Result<Option<Map<String, Value>>, EngineError> {
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
    let mut issues = Vec::new();
    for name in json::js_keys(given) {
        let at = json::pointer(&[name]);
        if composition.bound(name).is_none() {
            issues.push(SchemaIssue::new(
                at,
                format!("{schema} composes no behavior {name}"),
            ));
            continue;
        }
        issues.push(SchemaIssue::new(
            at,
            format!("behavior {name} takes no precondition"),
        ));
    }
    if !issues.is_empty() {
        return Err(EngineError::preconditions(schema, issues));
    }
    Ok(None)
}

/// What a write asks its behaviors' `validate`, `guard` and `afterChange`
/// about.
#[allow(dead_code)] // milestone 5's hooks read them
pub(crate) enum InstanceChange<'c> {
    Create {
        data: &'c Map<String, Value>,
        params: &'c Map<String, Value>,
    },
    Update {
        patch: &'c Map<String, Value>,
        before: &'c Map<String, Value>,
        after: &'c Map<String, Value>,
    },
    Delete,
}

/// The behaviors on one instance during a write or a read: milestone 5's
/// `Execution`, over the version's composition.
pub(crate) struct Execution<'c> {
    composition: &'c Composition,
}

impl<'c> Execution<'c> {
    pub fn new(composition: &'c Composition) -> Self {
        Execution { composition }
    }

    /// Each behavior's `validate` about the own fields a write would store,
    /// once the live version accepts them; their issues refuse the write as
    /// the live version's do.
    pub fn validation_issues(&self, _change: &InstanceChange<'_>) -> Vec<ValidationIssue> {
        Vec::new()
    }

    /// Every behavior's `guard`, with its entry of the preconditions; with
    /// `references`, then the guards of the behaviors that refer to the
    /// instance ([`Execution::guard_references`]). A veto refuses the write.
    pub fn guard(
        &self,
        _change: &InstanceChange<'_>,
        references: bool,
        _preconditions: Option<&Map<String, Value>>,
    ) -> Result<()> {
        if references {
            self.guard_references(_change)?;
        }
        Ok(())
    }

    /// Each behavior's `initialize` with its create parameters.
    pub fn initialize(&self, _params: &Map<String, Value>) -> Result<()> {
        Ok(())
    }

    /// Before a delete: each behavior drops what it keeps of the instance.
    pub fn deleting(&self) -> Result<()> {
        Ok(())
    }

    /// Each behavior's `afterChange`, after the instance's row changed.
    pub fn after_change(&self, _change: &InstanceChange<'_>) -> Result<()> {
        Ok(())
    }

    /// The behaviors' fields, by behavior name, in the type's behavior
    /// order: each an object of the fields the behavior declares that have
    /// a value. A behavior that declares none has no entry.
    pub fn fields(&self) -> Map<String, Value> {
        debug_assert!(
            self.composition.behaviors.is_empty(),
            "milestone 5 reads behavior fields"
        );
        Map::new()
    }

    /// The `guardReference` of each behavior that refers to the instance,
    /// before it changes: before a delete every reference, before another
    /// change the ones that hear every change.
    pub fn guard_references(&self, _change: &InstanceChange<'_>) -> Result<()> {
        Ok(())
    }

    /// The `afterReferenceChange` of each behavior that refers to the
    /// instance, after it changed, each as far as it hears the values the
    /// change moved; after a delete, a reference left behind by a behavior
    /// its source still composes refuses the delete.
    pub fn referenced(&self, _change: &InstanceChange<'_>, _moved: &[Move]) -> Result<()> {
        Ok(())
    }
}
