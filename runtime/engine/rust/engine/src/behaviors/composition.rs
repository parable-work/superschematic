//! A type's behaviors, checked against the registered implementations and
//! bound to their configs, the port of `src/behaviors/composition.ts`. The
//! engine checks a schema's list at define and at publish as the
//! compiler's loader does, with the same wording, and refuses what the
//! loader would: a behavior listed twice, a config its configSchema
//! rejects, a requirement the type does not list, a conflict it does, and
//! two behaviors that add an operation of the same name. It refuses two
//! things more: a behavior with no implementation registered with this
//! engine, and a behavior on a type other than the instance type.
//!
//! A config passes `parse_config` after its configSchema. When the schema
//! is defined or published, `parse_config` also reaches the namespace's
//! other schemas (`ConfigTarget::schemas`); a published version composed
//! again to run it does not. Every time it reaches the document's other
//! types, and each type it reads there counts as reachable from the
//! instance type for the version. What it reads must not depend on the
//! other schemas, so a define or publish holds each config's reads with
//! them to its reads without them, and refuses one whose reads differ.
//!
//! `config_changes` is the behaviors' half of the compatibility rule: a new
//! version keeps a behavior's config unless the implementation allows the
//! change, and adds or removes a behavior on a schema with instances only
//! when the implementation opts in. `checked_types` and `read_types` name
//! the types the rule then holds as it holds a type a field reaches.

use std::cell::OnceCell;
use std::collections::BTreeSet;
use std::sync::Arc;

use serde_json::{Map, Value};

use super::behavior::ConfigChange;
use super::config::{
    other_types, ConfigBehaviorField, ConfigSchema, ConfigSchemas, ConfigTarget, ConfigTypes,
};
use super::erased::ConfigBox;
use super::registry::{detail, BehaviorRegistry, OperationSpec, RegisteredBehavior};
use crate::errors::{Error, Result, SchemaChange, SchemaIssue};
use crate::json::{self, compare_utf16};
use crate::registry::document::{
    field_type_issue, fields_of, json_key, reachable_types, type_of, types_of, SchemaModel,
};
use crate::registry::fields::field_type;

/// One behavior on the instance type, with its config.
#[derive(Clone)]
pub(crate) struct BoundBehavior {
    pub behavior: Arc<RegisteredBehavior>,
    /// Its position in the type's list.
    pub index: usize,
    /// The config as the schema holds it; `{}` when the type gives none.
    pub json: Value,
    /// What `parse_config` returned.
    pub config: ConfigBox,
    /// The types its validate checks values against.
    pub checked: Vec<String>,
    /// The types its `parse_config` read through `ConfigTarget::types`,
    /// sorted.
    pub read: Vec<String>,
}

/// The behaviors of a schema's instance type, in list order.
#[derive(Clone)]
pub(crate) struct Composition {
    pub type_name: String,
    pub behaviors: Vec<BoundBehavior>,
    /// Every type the version's checks cover: the instance type, the types
    /// its fields reach, and the types each behavior checks values against
    /// or read, with the types those reach.
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

    /// The position of a behavior on the type.
    pub fn position(&self, name: &str) -> Option<usize> {
        self.behaviors
            .iter()
            .position(|bound| bound.behavior.name == name)
    }

    /// A behavior operation on the type, by name, with its behavior.
    pub fn operation(&self, name: &str) -> Option<(&BoundBehavior, &OperationSpec)> {
        self.behaviors.iter().find_map(|bound| {
            bound
                .behavior
                .operation(name)
                .map(|operation| (bound, operation))
        })
    }

    /// Every behavior operation on the type, in list order.
    pub fn operations(&self) -> impl Iterator<Item = (&BoundBehavior, &OperationSpec)> {
        self.behaviors.iter().flat_map(|bound| {
            bound
                .behavior
                .operations
                .iter()
                .map(move |operation| (bound, operation))
        })
    }

    /// Whether any behavior declares a field.
    pub fn has_fields(&self) -> bool {
        self.behaviors
            .iter()
            .any(|bound| !bound.behavior.fields.is_empty())
    }
}

/// What composing a version found: its composition, or every issue.
pub(crate) type Composed = std::result::Result<Composition, Vec<SchemaIssue>>;

/// One behavior a type lists: its name and its config as the schema holds
/// it.
pub(crate) struct BehaviorRef<'d> {
    pub name: &'d str,
    pub config: Option<&'d Value>,
}

/// The behaviors a type lists, as the schema holds them.
pub(crate) fn refs_of<'d>(document: &'d Value, type_name: &str) -> Vec<BehaviorRef<'d>> {
    type_of(document, type_name)
        .and_then(|type_def| type_def.get("behaviors"))
        .and_then(Value::as_array)
        .map(|refs| {
            refs.iter()
                .map(|entry| BehaviorRef {
                    name: entry
                        .get("name")
                        .and_then(Value::as_str)
                        .unwrap_or_default(),
                    config: entry.get("config"),
                })
                .collect()
        })
        .unwrap_or_default()
}

/// What `parse_config` made of a config: the JSON, the config and the
/// types it read; or why the config is refused.
enum Parsed {
    Config {
        json: Value,
        config: ConfigBox,
        read: Vec<String>,
    },
    Problem(String),
}

/// Checks every type's behaviors and binds the instance type's: the
/// composition, or every issue found. `schemas`, given when the schema is
/// defined or published, is what `parse_config` reaches of the namespace's
/// other schemas; `alone` is the same document composed with none in reach,
/// whose reads each behavior's must match.
pub(crate) fn compose(
    target: &SchemaModel,
    registry: &BehaviorRegistry,
    schemas: Option<&dyn ConfigSchemas>,
    alone: Option<&Composition>,
) -> Result<Composed> {
    let mut issues = Vec::new();
    let mut type_names: Vec<&String> = types_of(&target.document)
        .map(|types| types.keys().collect())
        .unwrap_or_default();
    type_names.sort_by(|a, b| compare_utf16(a, b));
    for type_name in type_names {
        if *type_name == target.instance_type {
            continue;
        }
        for (index, entry) in refs_of(&target.document, type_name).into_iter().enumerate() {
            issues.push(SchemaIssue::new(
                format!("{}/behaviors/{index}", json::pointer(&["types", type_name.as_str()])),
                format!(
                    "type {type_name}: behavior {} composes on the instance type, {}; {type_name} is a nested type, which has no instances",
                    entry.name, target.instance_type
                ),
            ));
        }
    }

    let instance_type = &target.instance_type;
    let type_path = json::pointer(&["types", instance_type.as_str()]);
    let refs = refs_of(&target.document, instance_type);
    let listed: Vec<&str> = refs.iter().map(|entry| entry.name).collect();
    let type_names = other_types(target);

    let mut bound: Vec<BoundBehavior> = Vec::new();
    let mut seen: Vec<&str> = Vec::new();
    for (index, entry) in refs.iter().enumerate() {
        let path = format!("{type_path}/behaviors/{index}");
        if seen.contains(&entry.name) {
            issues.push(SchemaIssue::new(
                path,
                format!("type {instance_type} lists behavior {} twice", entry.name),
            ));
            continue;
        }
        seen.push(entry.name);
        let Some(behavior) = registry.lookup(entry.name) else {
            issues.push(SchemaIssue::new(
                path,
                format!(
                    "behavior {} on type {instance_type}: no implementation registered",
                    entry.name
                ),
            ));
            continue;
        };
        let (json, config, read) =
            match parse_config(behavior, entry.config, target, registry, schemas)? {
                Parsed::Problem(problem) => {
                    issues.push(SchemaIssue::new(
                        format!("{path}/config"),
                        format!("type {instance_type}: {problem}"),
                    ));
                    continue;
                }
                Parsed::Config { json, config, read } => (json, config, read),
            };
        if schemas.is_some() {
            let alone_read = alone
                .and_then(|alone| alone.bound(&behavior.name))
                .map(|bound| bound.read.as_slice());
            if let Some(apart) = reads_apart(behavior, &read, alone_read)? {
                issues.push(SchemaIssue::new(
                    format!("{path}/config"),
                    format!("type {instance_type}: {apart}"),
                ));
                continue;
            }
        }
        let checked = checked_types_of(behavior, &config)?;
        let unknown: Vec<&str> = checked
            .iter()
            .filter(|name| !type_names.contains(name))
            .map(String::as_str)
            .collect();
        if !unknown.is_empty() {
            issues.push(SchemaIssue::new(
                format!("{path}/config"),
                format!(
                    "type {instance_type}: behavior {} checks values against {}, which {} of the document besides {instance_type}",
                    entry.name,
                    unknown.join(", "),
                    if unknown.len() == 1 { "is not a type" } else { "are not types" }
                ),
            ));
            continue;
        }
        bound.push(BoundBehavior {
            behavior: Arc::clone(behavior),
            index,
            json,
            config,
            checked,
            read,
        });
    }

    // A value checked against a type, or kept as a value of a type a
    // behavior read, is held to its fields as an instance's field is, so
    // those types, and the ones they reach, hold only field types the
    // schema runtime validates.
    let from_instance: BTreeSet<String> = reachable_types(&target.document, instance_type)
        .into_iter()
        .collect();
    let mut behaviors_only: BTreeSet<String> = BTreeSet::new();
    for each in &bound {
        for root in each.checked.iter().chain(&each.read) {
            for type_name in reachable_types(&target.document, root) {
                if !from_instance.contains(&type_name) {
                    behaviors_only.insert(type_name);
                }
            }
        }
    }
    let mut only: Vec<&String> = behaviors_only.iter().collect();
    only.sort_by(|a, b| compare_utf16(a, b));
    for type_name in only {
        for (index, field) in fields_of(&target.document, type_name).iter().enumerate() {
            if let Some(issue) = field_type_issue(&target.document, type_name, field) {
                issues.push(SchemaIssue::new(
                    format!(
                        "{}/fields/{index}/typeRef",
                        json::pointer(&["types", type_name.as_str()])
                    ),
                    issue,
                ));
            }
        }
    }

    let mut operation_owner: Vec<(&str, &str)> = Vec::new();
    for each in &bound {
        let path = format!("{type_path}/behaviors/{}", each.index);
        let name = each.behavior.name.as_str();
        for required in &each.behavior.declaration.requires {
            if !listed.contains(&required.as_str()) {
                issues.push(SchemaIssue::new(
                    path.clone(),
                    format!("type {instance_type}: behavior {name} requires behavior {required}, which the type does not list"),
                ));
            }
        }
        for conflict in &each.behavior.declaration.conflicts {
            if listed.contains(&conflict.as_str()) {
                issues.push(SchemaIssue::new(
                    path.clone(),
                    format!("type {instance_type}: behavior {name} conflicts with behavior {conflict}, which the type also lists"),
                ));
            }
        }
        for operation in &each.behavior.operations {
            match operation_owner
                .iter()
                .find(|(owned, _)| *owned == operation.name)
            {
                Some((_, previous)) => issues.push(SchemaIssue::new(
                    path.clone(),
                    format!(
                        "type {instance_type}: behaviors {previous} and {name} both add operation {}",
                        operation.name
                    ),
                )),
                None => operation_owner.push((&operation.name, name)),
            }
        }
    }
    if !issues.is_empty() {
        return Ok(Err(issues));
    }
    let mut covered = from_instance;
    covered.extend(behaviors_only);
    Ok(Ok(Composition {
        type_name: instance_type.clone(),
        behaviors: bound,
        covered,
    }))
}

/// What a new version does to the instance type's behaviors that the rule
/// refuses. `has_instances` is asked at most once, and only when a behavior
/// is added or removed or a `config_change` reads it.
pub(crate) fn config_changes(
    before: &SchemaModel,
    after: &SchemaModel,
    registry: &BehaviorRegistry,
    has_instances: &dyn Fn() -> Result<bool>,
) -> Result<Vec<SchemaChange>> {
    let mut changes = Vec::new();
    let type_name = &after.instance_type;
    let before_refs = refs_of(&before.document, &before.instance_type);
    let after_refs = refs_of(&after.document, type_name);
    let known: OnceCell<bool> = OnceCell::new();
    let populated = || -> Result<bool> {
        if let Some(known) = known.get() {
            return Ok(*known);
        }
        let found = has_instances()?;
        let _ = known.set(found);
        Ok(found)
    };
    let empty = Value::Object(Map::new());
    for entry in &after_refs {
        let path = format!("{type_name}.behaviors.{}", entry.name);
        let behavior = registry.lookup(entry.name);
        if let Some(earlier) = before_refs
            .iter()
            .find(|candidate| candidate.name == entry.name)
        {
            let was = earlier.config.unwrap_or(&empty);
            let now = entry.config.unwrap_or(&empty);
            if json::js_equal(was, now) {
                continue;
            }
            if let Some(reason) = decide(
                behavior,
                registry,
                Some((earlier.config, before)),
                Some((entry.config, after)),
                "it allows no config change",
                &populated,
            )? {
                changes.push(SchemaChange::new(
                    path,
                    format!(
                        "behavior {} on type {type_name} cannot change its config from {} to {}: {reason}",
                        entry.name,
                        json::stringify(was),
                        json::stringify(now)
                    ),
                ));
            }
        } else if populated()? {
            if let Some(reason) = decide(
                behavior,
                registry,
                None,
                Some((entry.config, after)),
                "it cannot be added to a schema that has instances",
                &populated,
            )? {
                changes.push(SchemaChange::new(
                    path,
                    format!(
                        "behavior {} cannot be added to type {type_name}, which has instances: {reason}",
                        entry.name
                    ),
                ));
            }
        }
    }
    for entry in &before_refs {
        if after_refs
            .iter()
            .any(|candidate| candidate.name == entry.name)
            || !populated()?
        {
            continue;
        }
        if let Some(reason) = decide(
            registry.lookup(entry.name),
            registry,
            Some((entry.config, before)),
            None,
            "it cannot be removed from a schema that has instances",
            &populated,
        )? {
            changes.push(SchemaChange::new(
                format!("{type_name}.behaviors.{}", entry.name),
                format!(
                    "behavior {} cannot be removed from type {type_name}, which has instances: {reason}",
                    entry.name
                ),
            ));
        }
    }
    Ok(changes)
}

/// The types of the document whose values a behavior's validate checks
/// under both versions' configs, for each behavior both versions compose,
/// sorted: the compatibility rule holds a new version to each. A config
/// either version's implementation refuses counts as naming none.
pub(crate) fn checked_types(
    before: &SchemaModel,
    after: &SchemaModel,
    registry: &BehaviorRegistry,
) -> Result<Vec<String>> {
    let before_refs = refs_of(&before.document, &before.instance_type);
    let after_refs = refs_of(&after.document, &after.instance_type);
    let named = |behavior: &Arc<RegisteredBehavior>,
                 config: Option<&Value>,
                 target: &SchemaModel|
     -> Result<Vec<String>> {
        match parse_config(behavior, config, target, registry, None)? {
            Parsed::Problem(_) => Ok(Vec::new()),
            Parsed::Config { config, .. } => checked_types_of(behavior, &config),
        }
    };
    let mut carried: BTreeSet<String> = BTreeSet::new();
    for entry in &before_refs {
        let Some(behavior) = registry.lookup(entry.name) else {
            continue;
        };
        let Some(later) = after_refs
            .iter()
            .find(|candidate| candidate.name == entry.name)
        else {
            continue;
        };
        if !behavior.hooks.checked_types {
            continue;
        }
        let kept = named(behavior, later.config, after)?;
        for name in named(behavior, entry.config, before)? {
            if kept.contains(&name) {
                carried.insert(name);
            }
        }
    }
    let mut out: Vec<String> = carried.into_iter().collect();
    out.sort_by(|a, b| compare_utf16(a, b));
    Ok(out)
}

/// The types of the document the behaviors of a version read through
/// `ConfigTarget::types` when their `parse_config` runs on it with no other
/// schema in reach, sorted: the compatibility rule holds a new version to
/// each. A behavior with no implementation, and a refused config, read
/// none.
pub(crate) fn read_types(
    version: &SchemaModel,
    registry: &BehaviorRegistry,
) -> Result<Vec<String>> {
    let mut read: BTreeSet<String> = BTreeSet::new();
    let mut seen: Vec<&str> = Vec::new();
    for entry in refs_of(&version.document, &version.instance_type) {
        let Some(behavior) = registry.lookup(entry.name) else {
            continue;
        };
        if seen.contains(&entry.name) {
            continue;
        }
        seen.push(entry.name);
        if let Parsed::Config { read: names, .. } =
            parse_config(behavior, entry.config, version, registry, None)?
        {
            read.extend(names);
        }
    }
    let mut out: Vec<String> = read.into_iter().collect();
    out.sort_by(|a, b| compare_utf16(a, b));
    Ok(out)
}

/// A behavior whose config a published version adds, removes or changes.
pub(crate) struct ConfigTransition {
    pub behavior: Arc<RegisteredBehavior>,
    /// The parsed config of the version replaced; `None` when it did not
    /// compose the behavior.
    pub before: Option<ConfigBox>,
    /// The parsed config of the version published; `None` when it no
    /// longer composes the behavior.
    pub after: Option<ConfigBox>,
}

/// The instance type's behaviors whose config a published version adds,
/// removes or changes against the version it replaces, in the new
/// version's list order, then the removed ones in the old order. A removed
/// behavior with no implementation is left out; an old config its
/// implementation no longer parses counts as none.
pub(crate) fn config_transitions(
    before: Option<&SchemaModel>,
    after: &SchemaModel,
    registry: &BehaviorRegistry,
) -> Result<Vec<ConfigTransition>> {
    let before_refs = before
        .map(|before| refs_of(&before.document, &before.instance_type))
        .unwrap_or_default();
    let after_refs = refs_of(&after.document, &after.instance_type);
    let parsed = |behavior: &Arc<RegisteredBehavior>,
                  config: Option<&Value>,
                  target: &SchemaModel|
     -> Result<Option<ConfigBox>> {
        Ok(
            match parse_config(behavior, config, target, registry, None)? {
                Parsed::Problem(_) => None,
                Parsed::Config { config, .. } => Some(config),
            },
        )
    };
    let empty = Value::Object(Map::new());
    let mut transitions = Vec::new();
    for entry in &after_refs {
        let Some(behavior) = registry.lookup(entry.name) else {
            continue;
        };
        let earlier = before_refs
            .iter()
            .find(|candidate| candidate.name == entry.name);
        if let Some(earlier) = earlier {
            if json::js_equal(
                earlier.config.unwrap_or(&empty),
                entry.config.unwrap_or(&empty),
            ) {
                continue;
            }
        }
        transitions.push(ConfigTransition {
            behavior: Arc::clone(behavior),
            before: match (earlier, before) {
                (Some(earlier), Some(before)) => parsed(behavior, earlier.config, before)?,
                _ => None,
            },
            after: parsed(behavior, entry.config, after)?,
        });
    }
    if let Some(before) = before {
        for entry in &before_refs {
            let Some(behavior) = registry.lookup(entry.name) else {
                continue;
            };
            if after_refs
                .iter()
                .any(|candidate| candidate.name == entry.name)
            {
                continue;
            }
            transitions.push(ConfigTransition {
                behavior: Arc::clone(behavior),
                before: parsed(behavior, entry.config, before)?,
                after: None,
            });
        }
    }
    Ok(transitions)
}

/// A schema as `parse_config` sees another one: its instance type, the
/// JSON type of each own field, and its behaviors with their configs.
pub(crate) fn config_schema_of(target: &SchemaModel) -> ConfigSchema {
    let mut fields = Map::new();
    for field in fields_of(&target.document, &target.instance_type) {
        fields.insert(
            json_key(field).to_owned(),
            Value::from(field_type(&target.document, field)),
        );
    }
    let refs = refs_of(&target.document, &target.instance_type);
    ConfigSchema {
        schema: target.name.clone(),
        type_name: target.instance_type.clone(),
        fields,
        behaviors: refs.iter().map(|entry| entry.name.to_owned()).collect(),
        configs: configs_of(&refs),
    }
}

fn configs_of(refs: &[BehaviorRef<'_>]) -> Map<String, Value> {
    let mut configs = Map::new();
    for entry in refs {
        if !configs.contains_key(entry.name) {
            configs.insert(
                entry.name.to_owned(),
                entry
                    .config
                    .cloned()
                    .unwrap_or_else(|| Value::Object(Map::new())),
            );
        }
    }
    configs
}

// behavior_fields_of is ConfigTarget::behavior_fields: the fields each
// listed behavior's declaration gives, by name, first listing first; a
// behavior with no implementation has no entry.
fn behavior_fields_of(
    refs: &[BehaviorRef<'_>],
    registry: &BehaviorRegistry,
) -> Vec<(String, Vec<ConfigBehaviorField>)> {
    let mut out: Vec<(String, Vec<ConfigBehaviorField>)> = Vec::new();
    for entry in refs {
        let Some(behavior) = registry.lookup(entry.name) else {
            continue;
        };
        if out.iter().any(|(name, _)| name == entry.name) {
            continue;
        }
        out.push((
            entry.name.to_owned(),
            behavior
                .declaration
                .fields
                .iter()
                .map(|field| ConfigBehaviorField {
                    name: field.name.clone(),
                    volatile: field.volatile,
                })
                .collect(),
        ));
    }
    out
}

// checked_types_of asks an implementation which types its validate checks
// values against under a parsed config, each once; none without the hook.
fn checked_types_of(behavior: &RegisteredBehavior, config: &ConfigBox) -> Result<Vec<String>> {
    let Some(names) = behavior.implementation.checked_types(config) else {
        return Ok(Vec::new());
    };
    let mut unique: Vec<String> = Vec::new();
    for name in names? {
        if !unique.contains(&name) {
            unique.push(name);
        }
    }
    Ok(unique)
}

// reads_apart says why a config whose parse_config read other types with
// the namespace's other schemas in reach than without them cannot stand.
fn reads_apart(
    behavior: &RegisteredBehavior,
    read: &[String],
    alone_read: Option<&[String]>,
) -> Result<Option<String>> {
    let Some(alone_read) = alone_read else {
        return Err(Error::internal(format!(
            "behavior {}: no composition with no other schema in reach to hold its reads to",
            behavior.name
        )));
    };
    let mut only_with: Vec<&str> = read
        .iter()
        .filter(|name| !alone_read.contains(name))
        .map(String::as_str)
        .collect();
    only_with.sort_by(|a, b| compare_utf16(a, b));
    let mut only_without: Vec<&str> = alone_read
        .iter()
        .filter(|name| !read.contains(name))
        .map(String::as_str)
        .collect();
    only_without.sort_by(|a, b| compare_utf16(a, b));
    if only_with.is_empty() && only_without.is_empty() {
        return Ok(None);
    }
    let reads = if only_with.is_empty() {
        format!(
            "{} through ConfigTarget.types only when no other schemas are in reach (ConfigTarget.schemas)",
            only_without.join(", ")
        )
    } else {
        format!(
            "{} through ConfigTarget.types only while other schemas are in reach (ConfigTarget.schemas){}",
            only_with.join(", "),
            if only_without.is_empty() {
                String::new()
            } else {
                format!(", and {} only when none are", only_without.join(", "))
            }
        )
    };
    Ok(Some(format!(
        "behavior {} config: parseConfig reads {reads}; what it reads there depends only on the config and the document, since a version runs and is checked against the next with no other schema in reach",
        behavior.name
    )))
}

// decide asks an implementation's config_change; None allows. A side is
// the config as the schema holds it with the version it is in.
fn decide(
    behavior: Option<&Arc<RegisteredBehavior>>,
    registry: &BehaviorRegistry,
    before: Option<(Option<&Value>, &SchemaModel)>,
    after: Option<(Option<&Value>, &SchemaModel)>,
    refusal: &str,
    populated: &dyn Fn() -> Result<bool>,
) -> Result<Option<String>> {
    let Some(behavior) = behavior else {
        return Ok(Some(
            "no implementation is registered to allow it".to_owned(),
        ));
    };
    if !behavior.hooks.config_change {
        return Ok(Some(refusal.to_owned()));
    }
    let mut sides: Vec<Option<ConfigBox>> = Vec::new();
    for side in [before, after] {
        sides.push(match side {
            None => None,
            Some((config, target)) => {
                match parse_config(behavior, config, target, registry, None)? {
                    Parsed::Problem(problem) => return Ok(Some(problem)),
                    Parsed::Config { config, .. } => Some(config),
                }
            }
        });
    }
    let change = ConfigChange {
        instances: populated,
    };
    let answer = behavior
        .implementation
        .config_change(sides[0].as_ref(), sides[1].as_ref(), &change)
        .unwrap_or(Ok(None))?;
    match answer {
        None => Ok(None),
        Some(reason) if reason.is_empty() => Err(Error::behavior(
            &behavior.name,
            "configChange returns a reason (a non-empty string) to refuse, or undefined to allow",
        )),
        Some(reason) => Ok(Some(reason)),
    }
}

// parse_config checks a config against the behavior's configSchema, then
// its parse_config, on the type the target composes, with schemas in reach
// when given. An absent config is checked as {}.
fn parse_config(
    behavior: &RegisteredBehavior,
    raw: Option<&Value>,
    target: &SchemaModel,
    registry: &BehaviorRegistry,
    schemas: Option<&dyn ConfigSchemas>,
) -> Result<Parsed> {
    let json = raw
        .map(json::js_copy)
        .unwrap_or_else(|| Value::Object(Map::new()));
    match &behavior.config {
        None => {
            if !json.as_object().is_some_and(Map::is_empty) {
                return Ok(Parsed::Problem(format!(
                    "behavior {} takes no config",
                    behavior.name
                )));
            }
        }
        Some(schema) => {
            if !schema.is_valid(&json) {
                return Ok(Parsed::Problem(format!(
                    "behavior {} config: {}",
                    behavior.name,
                    detail(schema, &json)
                )));
            }
        }
    }
    let refs = refs_of(&target.document, &target.instance_type);
    let config_target = ConfigTarget {
        model: target,
        fields: fields_of(&target.document, &target.instance_type)
            .iter()
            .map(|field| json_key(field).to_owned())
            .collect(),
        field_schemas: OnceCell::new(),
        types: ConfigTypes::new(target),
        behaviors: refs.iter().map(|entry| entry.name.to_owned()).collect(),
        behavior_fields: behavior_fields_of(&refs, registry),
        configs: configs_of(&refs),
        schemas,
    };
    match behavior.implementation.parse_config(&json, &config_target) {
        Ok(config) => Ok(Parsed::Config {
            json,
            config,
            read: config_target.types.read(),
        }),
        Err(Error::Config(message)) => Ok(Parsed::Problem(format!(
            "behavior {} config: {message}",
            behavior.name
        ))),
        Err(other) => Err(other),
    }
}
