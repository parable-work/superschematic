//! A type's behaviors, checked against the registered implementations and
//! bound to their configs: the part of `src/behaviors/composition.ts` the
//! registry needs before any behavior runs (D16, D58).
//!
//! The engine checks a schema's list at define and at publish as the
//! compiler's loader does, with the same wording, and refuses a behavior on
//! a type other than the instance type (only the instance type has
//! instances), a behavior listed twice, and a behavior with no
//! implementation registered with this engine.
//!
//! Milestone 5 brings the `Behavior` trait and the core's behaviors: a
//! config parsed by its implementation, requirements and conflicts,
//! operations of one name, the types a behavior checks or reads, and each
//! implementation's half of the compatibility rule. Until then no
//! implementation is registered, so a schema whose instance type composes a
//! behavior is refused at define, and a stored version that composes one is
//! `unavailable`, as the TypeScript engine answers for a behavior it has no
//! implementation of.

use std::collections::BTreeSet;

use serde_json::{Map, Value};

use crate::errors::{SchemaChange, SchemaIssue};
use crate::json::{self, compare_utf16};
use crate::registry::document::{reachable_types, type_of, types_of, SchemaModel};

/// A behavior implementation registered with an engine, as the registry
/// reads it: its name and the fields it adds, each read under the
/// behavior's name.
#[derive(Clone, Debug)]
#[cfg_attr(not(all(test, feature = "sqlite")), allow(dead_code))] // milestone 5 registers implementations
pub(crate) struct RegisteredBehavior {
    pub name: String,
    pub fields: Vec<String>,
}

/// The behavior implementations an engine runs, by name.
#[derive(Clone, Debug, Default)]
pub(crate) struct BehaviorRegistry {
    behaviors: Vec<RegisteredBehavior>,
}

impl BehaviorRegistry {
    /// The implementation registered under a name.
    pub fn lookup(&self, name: &str) -> Option<&RegisteredBehavior> {
        self.behaviors.iter().find(|behavior| behavior.name == name)
    }

    /// Registers an implementation (the tests' stand-in for milestone 5's
    /// registration).
    #[cfg(all(test, feature = "sqlite"))]
    pub fn register(&mut self, behavior: RegisteredBehavior) {
        self.behaviors.push(behavior);
    }
}

/// One behavior on the instance type, with its config.
#[derive(Clone, Debug)]
pub(crate) struct BoundBehavior {
    pub behavior: RegisteredBehavior,
    /// The config as the schema holds it; `{}` when the type gives none.
    pub json: Value,
}

/// The behaviors of a schema's instance type, in list order.
#[derive(Clone, Debug)]
pub(crate) struct Composition {
    pub behaviors: Vec<BoundBehavior>,
    /// Every type the version's checks cover: the instance type and the
    /// types its fields reach.
    #[allow(dead_code)] // milestone 5 adds the types a behavior covers
    pub covered: BTreeSet<String>,
}

impl Composition {
    /// The behavior that adds a field, by its qualified name,
    /// `<behavior>.<field>`.
    pub fn field_owner(&self, qualified: &str) -> Option<&str> {
        self.behaviors
            .iter()
            .find(|bound| {
                bound
                    .behavior
                    .fields
                    .iter()
                    .any(|field| format!("{}.{field}", bound.behavior.name) == qualified)
            })
            .map(|bound| bound.behavior.name.as_str())
    }

    /// The entry of a behavior on the type.
    pub fn bound(&self, name: &str) -> Option<&BoundBehavior> {
        self.behaviors
            .iter()
            .find(|bound| bound.behavior.name == name)
    }
}

/// The behaviors a type lists, as the schema holds them: each entry's name
/// and config.
fn refs_of<'d>(document: &'d Value, type_name: &str) -> Vec<(&'d str, Option<&'d Value>)> {
    type_of(document, type_name)
        .and_then(|type_def| type_def.get("behaviors"))
        .and_then(Value::as_array)
        .map(|refs| {
            refs.iter()
                .map(|entry| {
                    (
                        entry
                            .get("name")
                            .and_then(Value::as_str)
                            .unwrap_or_default(),
                        entry.get("config"),
                    )
                })
                .collect()
        })
        .unwrap_or_default()
}

/// Checks every type's behaviors and binds the instance type's: the
/// composition, or every issue found.
pub(crate) fn compose(
    target: &SchemaModel,
    registry: &BehaviorRegistry,
) -> Result<Composition, Vec<SchemaIssue>> {
    let mut issues = Vec::new();
    let mut type_names: Vec<&String> = types_of(&target.document)
        .map(|types| types.keys().collect())
        .unwrap_or_default();
    type_names.sort_by(|a, b| compare_utf16(a, b));
    for type_name in type_names {
        if *type_name == target.instance_type {
            continue;
        }
        for (index, (name, _)) in refs_of(&target.document, type_name).into_iter().enumerate() {
            issues.push(SchemaIssue::new(
                format!("{}/behaviors/{index}", json::pointer(&["types", type_name.as_str()])),
                format!(
                    "type {type_name}: behavior {name} composes on the instance type, {}; {type_name} is a nested type, which has no instances",
                    target.instance_type
                ),
            ));
        }
    }

    let type_path = json::pointer(&["types", target.instance_type.as_str()]);
    let mut bound = Vec::new();
    let mut seen: Vec<&str> = Vec::new();
    for (index, (name, config)) in refs_of(&target.document, &target.instance_type)
        .into_iter()
        .enumerate()
    {
        let path = format!("{type_path}/behaviors/{index}");
        if seen.contains(&name) {
            issues.push(SchemaIssue::new(
                path,
                format!("type {} lists behavior {name} twice", target.instance_type),
            ));
            continue;
        }
        seen.push(name);
        let Some(behavior) = registry.lookup(name) else {
            issues.push(SchemaIssue::new(
                path,
                format!(
                    "behavior {name} on type {}: no implementation registered",
                    target.instance_type
                ),
            ));
            continue;
        };
        bound.push(BoundBehavior {
            behavior: behavior.clone(),
            json: config.cloned().unwrap_or_else(|| Value::Object(Map::new())),
        });
    }
    if !issues.is_empty() {
        return Err(issues);
    }
    Ok(Composition {
        behaviors: bound,
        covered: reachable_types(&target.document, &target.instance_type)
            .into_iter()
            .collect(),
    })
}

/// What a new version does to the instance type's behaviors that the rule
/// refuses: a config changed, or a behavior added to or removed from a
/// schema with instances, each refused unless its implementation allows
/// it. `has_instances` is asked at most once, and only when a behavior is
/// added or removed. With no implementation registered, none allows a
/// change (milestone 5 asks each implementation's `configChange`).
pub(crate) fn config_changes(
    before: &SchemaModel,
    after: &SchemaModel,
    registry: &BehaviorRegistry,
    mut has_instances: impl FnMut() -> crate::errors::Result<bool>,
) -> crate::errors::Result<Vec<SchemaChange>> {
    let mut changes = Vec::new();
    let type_name = &after.instance_type;
    let before_refs = refs_of(&before.document, &before.instance_type);
    let after_refs = refs_of(&after.document, type_name);
    let mut instances: Option<bool> = None;
    let mut populated = || -> crate::errors::Result<bool> {
        if let Some(known) = instances {
            return Ok(known);
        }
        let known = has_instances()?;
        instances = Some(known);
        Ok(known)
    };
    let empty = Value::Object(Map::new());
    let decide = |name: &str, refusal: &str| -> Option<String> {
        match registry.lookup(name) {
            None => Some("no implementation is registered to allow it".to_owned()),
            // Milestone 5 asks the implementation's configChange.
            Some(_) => Some(refusal.to_owned()),
        }
    };

    for (name, config) in &after_refs {
        let path = format!("{type_name}.behaviors.{name}");
        if let Some((_, earlier)) = before_refs.iter().find(|(candidate, _)| candidate == name) {
            let earlier = earlier.unwrap_or(&empty);
            let config = config.unwrap_or(&empty);
            if json::js_equal(earlier, config) {
                continue;
            }
            if let Some(reason) = decide(name, "it allows no config change") {
                changes.push(SchemaChange::new(
                    path,
                    format!(
                        "behavior {name} on type {type_name} cannot change its config from {} to {}: {reason}",
                        json::stringify(earlier),
                        json::stringify(config)
                    ),
                ));
            }
        } else if populated()? {
            if let Some(reason) = decide(name, "it cannot be added to a schema that has instances")
            {
                changes.push(SchemaChange::new(
                    path,
                    format!("behavior {name} cannot be added to type {type_name}, which has instances: {reason}"),
                ));
            }
        }
    }
    for (name, _) in &before_refs {
        if after_refs.iter().any(|(candidate, _)| candidate == name) || !populated()? {
            continue;
        }
        if let Some(reason) = decide(
            name,
            "it cannot be removed from a schema that has instances",
        ) {
            changes.push(SchemaChange::new(
                format!("{type_name}.behaviors.{name}"),
                format!("behavior {name} cannot be removed from type {type_name}, which has instances: {reason}"),
            ));
        }
    }
    Ok(changes)
}
