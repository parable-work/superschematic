//! Blueprint, children created with their parent (D16), the port of
//! `typescript/src/blueprint.ts`. A map of steps, by key, says which
//! children an instance has: one instance of the child schema per step,
//! linked to its parent through the child's `parentLink` and blocked,
//! through the child's Dependencies, by the children of the steps it comes
//! after. Stamping creates them all in one transaction, as the principal
//! that made the change: `instances().create` for each child, which gives
//! the child's Links its `parentLink` and copied links and its Dependencies
//! its blockers as create parameters, so a child holds them from its create
//! and is never claimable before its edges exist. Anything that fails
//! refuses the change, and the transaction rolls back whole. Each child's
//! id comes from the engine's id source, as any create's without one does.
//!
//! The steps are inline in the config (`steps`), stamped in after_change of
//! the instance's create, or kept as an instance (`from`): a link of the
//! type's Links config pinned to a revision of an instance whose field
//! holds a map of the same form, stamped when the link is first set (at the
//! create that gives it, or the first `link`), read from the revision the
//! link pins through Revisions' `getRevision`, as the principal. Once
//! stamped, the guard refuses moving the `from` link. Each refusal is a
//! veto with a code the declaration lists: `stamped` for the guard's, and
//! `no_revision`, `unreadable`, `invalid_steps`, `no_dependencies` and
//! `not_constant` for a stamp that cannot go ahead.
//!
//! A step's `when` holds when this instance's field equals a JSON value, or
//! is a list that includes one, compared as JSON; a step left out passes
//! the steps it came after on to the steps that come after it, so the chain
//! stays connected. Children are created in an order where each comes after
//! its blockers, ties in the map's order. Each child's fields are its key in
//! `keyField`, then `copyFields` from this instance (the ones it holds),
//! then the step's data.
//!
//! `parse_config` checks the config against this type and, when the schema
//! is defined or published, the child schema's live version and the
//! definition's (Links with `parentLink` pointing here, Revisions before
//! Blueprint when that link pins a revision, Constants over `keyField` and
//! every copied field, Dependencies for `after`, and not Blueprint). A
//! stamp checks Dependencies and Constants again against the child
//! schema's live version (D16, amended: a published version is not refused
//! for another schema's change).
//!
//! What was stamped is kept in Blueprint's own table: the `children` field
//! lists each step's key and its child's id, in the order they were
//! created, absent until the instance is stamped. Storage (D58): one table
//! and one column, every statement the same on both databases.
//!
//! configChange: any config may change; Blueprint can be added to a schema
//! that has instances and cannot be removed from one.

use std::collections::{HashMap, HashSet};

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, ConfigSchema, InstanceCx, InstanceView, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::json::{js_equal, js_keys, quote, stringify};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, ConfigTarget, EngineErrorCode, Error, GuardRequest, Hooks, InstanceChange, Result,
};
use superschematic_engine_behaviors::{link_many, link_pin, LinkPin};

/// The most steps a map holds: the most children one stamp creates.
pub const MAX_STEPS: usize = 500;

const NAME: &str = "Blueprint";
const LINKS_TARGETS: &str = "Links.targets";

/// The pattern a step key matches, as the TypeScript package writes it.
const STEP_KEY: &str = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$";

/// When a step is stamped: this instance's field equals a value, or is a
/// list that includes one.
#[derive(Clone, Debug, PartialEq)]
pub enum BlueprintWhen {
    Equals { field: String, value: Value },
    Includes { field: String, value: Value },
}

impl BlueprintWhen {
    pub fn field(&self) -> &str {
        match self {
            BlueprintWhen::Equals { field, .. } | BlueprintWhen::Includes { field, .. } => field,
        }
    }
}

/// One step of a map of steps, parsed.
#[derive(Clone, Debug, PartialEq)]
pub struct BlueprintStep {
    pub key: String,
    /// The keys of the steps whose children block this one's.
    pub after: Vec<String>,
    pub when: Option<BlueprintWhen>,
    /// The child's fields beside its key and the copied ones.
    pub data: Map<String, Value>,
}

/// Where a definition's steps are kept: a link of the type and the field of
/// its target that holds them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BlueprintFrom {
    pub link: String,
    pub field: String,
}

/// Blueprint's config, parsed.
#[derive(Clone, Debug, PartialEq)]
pub struct BlueprintConfig {
    pub schema: String,
    pub parent_link: String,
    pub key_field: String,
    /// The inline steps, in the config's order; `None` with `from`.
    pub steps: Option<Vec<BlueprintStep>>,
    pub from: Option<BlueprintFrom>,
    pub copy_fields: Vec<String>,
    pub copy_links: Vec<String>,
    /// This type's own fields, which a step's when may name.
    pub fields: Vec<String>,
    /// The ones that hold lists, which a when's includes reads.
    pub list_fields: Vec<String>,
}

/// A map of steps that breaks a rule; its message says which.
struct StepsError(String);

fn steps_error(message: impl Into<String>) -> StepsError {
    StepsError(message.into())
}

// is_step_key is whether a key matches STEP_KEY.
fn is_step_key(key: &str) -> bool {
    let mut chars = key.chars();
    let Some(first) = chars.next() else {
        return false;
    };
    first.is_ascii_alphanumeric()
        && key.chars().count() <= 128
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | ':' | '-'))
}

// parse_steps reads a map of steps: its shape, and that each after names
// another step of the map. It is the one reader of inline steps and of a
// map a definition holds, so both follow one set of rules.
fn parse_steps(raw: &Value) -> std::result::Result<Vec<BlueprintStep>, StepsError> {
    let Some(map) = raw.as_object() else {
        return Err(steps_error(
            "a map of steps is a JSON object of steps by key",
        ));
    };
    let keys = js_keys(map);
    if keys.is_empty() || keys.len() > MAX_STEPS {
        return Err(steps_error(format!(
            "a map of steps holds 1 to {MAX_STEPS} steps, not {}",
            keys.len()
        )));
    }
    let mut steps = Vec::new();
    for key in keys {
        if !is_step_key(key) {
            return Err(steps_error(format!(
                "step key {} must match {STEP_KEY}",
                quote(key)
            )));
        }
        let Some(step) = map[key].as_object() else {
            return Err(steps_error(format!("step {key} is not a JSON object")));
        };
        let extra: Vec<&str> = js_keys(step)
            .into_iter()
            .filter(|name| !matches!(*name, "after" | "when" | "data"))
            .collect();
        if !extra.is_empty() {
            return Err(steps_error(format!(
                "step {key} has {}; a step has after, when and data",
                extra.join(", ")
            )));
        }
        let after = match step.get("after") {
            None | Some(Value::Null) => Vec::new(),
            Some(Value::Array(items)) => {
                let mut after: Vec<String> = Vec::new();
                for item in items {
                    match item.as_str() {
                        Some(other) if !after.iter().any(|seen| seen == other) => {
                            after.push(other.to_owned());
                        }
                        _ => {
                            return Err(steps_error(format!(
                                "step {key}: after is a list of distinct step keys"
                            )))
                        }
                    }
                }
                after
            }
            Some(_) => {
                return Err(steps_error(format!(
                    "step {key}: after is a list of distinct step keys"
                )))
            }
        };
        for other in &after {
            if other == key {
                return Err(steps_error(format!("step {key} comes after itself")));
            }
            if !map.contains_key(other) {
                return Err(steps_error(format!(
                    "step {key} comes after {other}, which is not a step of the map"
                )));
            }
        }
        let data = match step.get("data") {
            None | Some(Value::Null) => Map::new(),
            Some(Value::Object(data)) => js_keys(data)
                .into_iter()
                .map(|name| (name.to_owned(), data[name].clone()))
                .collect(),
            Some(_) => {
                return Err(steps_error(format!(
                    "step {key}: data is a JSON object of the child's fields"
                )))
            }
        };
        let when = match step.get("when") {
            None => None,
            Some(raw) => Some(parse_when(key, raw)?),
        };
        steps.push(BlueprintStep {
            key: key.to_owned(),
            after,
            when,
            data,
        });
    }
    Ok(steps)
}

fn parse_when(key: &str, raw: &Value) -> std::result::Result<BlueprintWhen, StepsError> {
    let field = raw
        .as_object()
        .and_then(|when| when.get("field"))
        .and_then(Value::as_str)
        .filter(|field| !field.is_empty());
    let (Some(when), Some(field)) = (raw.as_object(), field) else {
        return Err(steps_error(format!(
            "step {key}: when is an object with a field and one of equals and includes"
        )));
    };
    let extra: Vec<&str> = js_keys(when)
        .into_iter()
        .filter(|name| !matches!(*name, "field" | "equals" | "includes"))
        .collect();
    if !extra.is_empty() {
        return Err(steps_error(format!(
            "step {key}: when has {}; it has a field and one of equals and includes",
            extra.join(", ")
        )));
    }
    match (when.get("equals"), when.get("includes")) {
        (Some(value), None) => Ok(BlueprintWhen::Equals {
            field: field.to_owned(),
            value: value.clone(),
        }),
        (None, Some(value)) => Ok(BlueprintWhen::Includes {
            field: field.to_owned(),
            value: value.clone(),
        }),
        _ => Err(steps_error(format!(
            "step {key}: when has one of equals and includes"
        ))),
    }
}

// check_acyclic refuses a map whose after edges form a cycle, naming it.
fn check_acyclic(steps: &[BlueprintStep]) -> std::result::Result<(), StepsError> {
    fn visit<'s>(
        key: &'s str,
        by_key: &HashMap<&str, &'s BlueprintStep>,
        done: &mut HashSet<&'s str>,
        path: &mut Vec<&'s str>,
    ) -> std::result::Result<(), StepsError> {
        if done.contains(key) {
            return Ok(());
        }
        if let Some(at) = path.iter().position(|seen| *seen == key) {
            let mut cycle: Vec<&str> = path[at..].to_vec();
            cycle.push(key);
            return Err(steps_error(format!(
                "the steps form a cycle: {}",
                cycle.join(" -> ")
            )));
        }
        path.push(key);
        if let Some(step) = by_key.get(key) {
            for other in &step.after {
                visit(other, by_key, done, path)?;
            }
        }
        path.pop();
        done.insert(key);
        Ok(())
    }
    let by_key: HashMap<&str, &BlueprintStep> =
        steps.iter().map(|step| (step.key.as_str(), step)).collect();
    let mut done = HashSet::new();
    let mut path = Vec::new();
    for step in steps {
        visit(&step.key, &by_key, &mut done, &mut path)?;
    }
    Ok(())
}

// check_steps holds a parsed map to this type and the config: when names a
// field of the type, a list for includes, and data leaves the key alone.
fn check_steps(
    key_field: &str,
    fields: &[String],
    list_fields: &[String],
    type_name: &str,
    steps: &[BlueprintStep],
) -> std::result::Result<(), StepsError> {
    for step in steps {
        if let Some(when) = &step.when {
            let field = when.field();
            if !fields.iter().any(|known| known == field) {
                return Err(steps_error(format!(
                    "step {}: when names {field}, which is not a field of {type_name} (its fields: {})",
                    step.key,
                    fields.join(", ")
                )));
            }
            if matches!(when, BlueprintWhen::Includes { .. })
                && !list_fields.iter().any(|known| known == field)
            {
                return Err(steps_error(format!(
                    "step {}: when includes reads {field} as a list, and {type_name}'s {field} is not one",
                    step.key
                )));
            }
        }
        if step.data.contains_key(key_field) {
            return Err(steps_error(format!(
                "step {}: data sets {key_field}, which holds the step's key",
                step.key
            )));
        }
    }
    check_acyclic(steps)
}

// holds reports whether a step's when holds for this instance's fields.
fn holds(when: Option<&BlueprintWhen>, data: &Map<String, Value>) -> bool {
    match when {
        None => true,
        Some(BlueprintWhen::Equals { field, value }) => {
            data.get(field).is_some_and(|held| js_equal(held, value))
        }
        Some(BlueprintWhen::Includes { field, value }) => data
            .get(field)
            .and_then(Value::as_array)
            .is_some_and(|items| items.iter().any(|item| js_equal(item, value))),
    }
}

// unique keeps the first of each key, in order (`[...new Set(keys)]`).
fn unique(keys: Vec<String>) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for key in keys {
        if !out.contains(&key) {
            out.push(key);
        }
    }
    out
}

/// The graph a stamp creates for an instance's fields: the steps whose when
/// holds, each after the included steps it comes after and, in place of a
/// step left out, the ones that step comes after, transitively. They come
/// in an order where each step follows the ones it comes after, ties in the
/// map's order. The map is acyclic.
fn effective_steps(steps: &[BlueprintStep], data: &Map<String, Value>) -> Vec<BlueprintStep> {
    let by_key: HashMap<&str, &BlueprintStep> =
        steps.iter().map(|step| (step.key.as_str(), step)).collect();
    let included: HashSet<&str> = steps
        .iter()
        .filter(|step| holds(step.when.as_ref(), data))
        .map(|step| step.key.as_str())
        .collect();
    // through is what a step contributes to the after of a step that comes
    // after it: itself when it is included, else what it comes after.
    fn through(
        key: &str,
        by_key: &HashMap<&str, &BlueprintStep>,
        included: &HashSet<&str>,
        passed: &mut HashMap<String, Vec<String>>,
    ) -> Vec<String> {
        if included.contains(key) {
            return vec![key.to_owned()];
        }
        if let Some(keys) = passed.get(key) {
            return keys.clone();
        }
        let mut keys = Vec::new();
        if let Some(step) = by_key.get(key) {
            for other in &step.after {
                keys.extend(through(other, by_key, included, passed));
            }
        }
        let keys = unique(keys);
        passed.insert(key.to_owned(), keys.clone());
        keys
    }
    let mut passed: HashMap<String, Vec<String>> = HashMap::new();
    let plan: Vec<BlueprintStep> = steps
        .iter()
        .filter(|step| included.contains(step.key.as_str()))
        .map(|step| {
            let mut after = Vec::new();
            for other in &step.after {
                after.extend(through(other, &by_key, &included, &mut passed));
            }
            BlueprintStep {
                after: unique(after),
                ..step.clone()
            }
        })
        .collect();
    let mut ordered: Vec<BlueprintStep> = Vec::new();
    let mut placed: HashSet<String> = HashSet::new();
    while ordered.len() < plan.len() {
        let Some(next) = plan.iter().find(|step| {
            !placed.contains(&step.key) && step.after.iter().all(|key| placed.contains(key))
        }) else {
            // An acyclic map always has one; nothing more can be placed.
            break;
        };
        placed.insert(next.key.clone());
        ordered.push(next.clone());
    }
    ordered
}

fn json_types(schema: Option<&Value>) -> Vec<String> {
    let declared = schema.and_then(|schema| schema.get("type"));
    let listed: Vec<&Value> = match declared {
        Some(Value::Array(types)) => types.iter().collect(),
        Some(one) => vec![one],
        None => Vec::new(),
    };
    listed
        .into_iter()
        .filter_map(Value::as_str)
        .filter(|one| *one != "null")
        .map(str::to_owned)
        .collect()
}

// link_of is a link of a Links config, as the schema holds it.
fn link_of<'c>(config: Option<&'c Value>, name: &str) -> Option<&'c Value> {
    config?.get("links")?.as_object()?.get(name)
}

fn link_names(config: Option<&Value>) -> Vec<String> {
    config
        .and_then(|config| config.get("links"))
        .and_then(Value::as_object)
        .map(|links| js_keys(links).into_iter().map(str::to_owned).collect())
        .unwrap_or_default()
}

// js_text is String(value) of a config's member: its text, or undefined.
fn js_text(value: Option<&Value>) -> String {
    match value {
        None => "undefined".to_owned(),
        Some(Value::String(text)) => text.clone(),
        Some(other) => stringify(other),
    }
}

fn config_error(message: impl Into<String>) -> Error {
    Error::config(message)
}

// check_child holds the config to the child schema's live version, as the
// caller who defines or publishes reads it.
fn check_child(
    config: &BlueprintConfig,
    target: &ConfigTarget<'_>,
    child: Option<ConfigSchema>,
) -> Result<()> {
    let at = format!("schema {}", config.schema);
    let Some(child) = child else {
        return Err(config_error(format!(
            "{at} has no live version; publish it first"
        )));
    };
    let composes = |name: &str| child.behaviors.iter().any(|known| known == name);
    if composes(NAME) {
        return Err(config_error(format!(
            "{at} composes Blueprint: a child cannot stamp children of its own"
        )));
    }
    if !composes("Links") {
        return Err(config_error(format!(
            "{at} does not compose Links, so its children cannot link to their parent through {}",
            config.parent_link
        )));
    }
    let child_links = child.configs.get("Links");
    let Some(parent) = link_of(child_links, &config.parent_link) else {
        return Err(config_error(format!(
            "{at} has no link {} (its links: {})",
            config.parent_link,
            link_names(child_links).join(", ")
        )));
    };
    if parent.get("schema").and_then(Value::as_str) != Some(target.schema()) {
        return Err(config_error(format!(
            "{at}'s link {} points at {}, not {}",
            config.parent_link,
            js_text(parent.get("schema")),
            target.schema()
        )));
    }
    if link_many(parent) {
        return Err(config_error(format!(
            "{at}'s link {} holds a list of targets, and a child links to its one parent through it",
            config.parent_link
        )));
    }
    // A parentLink pinned to a revision pins the parent's first revision,
    // which Revisions records in its after_change of the create: before
    // Blueprint's, in list order. One pinned to a release would find none.
    match link_pin(parent) {
        Some(LinkPin::Release) => {
            return Err(config_error(format!(
                "{at}'s link {} pins a release, which {} has none of when it stamps its children",
                config.parent_link,
                target.type_name()
            )));
        }
        Some(LinkPin::Revision) => {
            let before: Vec<&String> = target
                .behaviors()
                .iter()
                .take_while(|name| *name != NAME)
                .collect();
            if !before.iter().any(|name| *name == "Revisions") {
                return Err(config_error(format!(
                    "{at}'s link {} is pinned, so {} lists Revisions before Blueprint, to have a revision to pin",
                    config.parent_link,
                    target.type_name()
                )));
            }
        }
        None => {}
    }
    if config
        .steps
        .iter()
        .flatten()
        .any(|step| !step.after.is_empty())
    {
        if !composes("Dependencies") {
            return Err(config_error(format!(
                "{at} does not compose Dependencies, so a step's after cannot block its child"
            )));
        }
        let schemas: Vec<String> = match child
            .configs
            .get("Dependencies")
            .and_then(|config| config.get("schemas"))
            .and_then(Value::as_array)
        {
            Some(listed) => listed
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect(),
            None => vec![config.schema.clone()],
        };
        if !schemas.contains(&config.schema) {
            return Err(config_error(format!(
                "{at}'s Dependencies takes blockers of {}, not of {}, which its siblings are",
                schemas.join(", "),
                config.schema
            )));
        }
    }
    if child.fields.get(&config.key_field).and_then(Value::as_str) != Some("string") {
        return Err(config_error(format!(
            "keyField {} is not a string field of {}",
            config.key_field, child.type_name
        )));
    }
    for field in &config.copy_fields {
        let Some(kind) = child.fields.get(field) else {
            return Err(config_error(format!(
                "copyFields: {field} is not a field of {}",
                child.type_name
            )));
        };
        let kind = kind.as_str().unwrap_or_default();
        let own = json_types(target.field_schemas().get(field));
        if kind != "any" && !own.is_empty() && !own.iter().any(|one| one == kind) {
            return Err(config_error(format!(
                "copyFields: {}'s {field} holds {}, and {}'s holds {kind}",
                target.type_name(),
                own.join(" or "),
                child.type_name
            )));
        }
    }
    let mut constant = vec![config.key_field.clone()];
    constant.extend(config.copy_fields.iter().cloned());
    if !composes("Constants") {
        return Err(config_error(format!(
            "{at} does not compose Constants, so the fields each stamp sets in a child could change after it: compose Constants with fields {}",
            constant.join(", ")
        )));
    }
    let kept = strings(
        child
            .configs
            .get("Constants")
            .and_then(|config| config.get("fields")),
    )
    .unwrap_or_default();
    let loose: Vec<&String> = constant
        .iter()
        .filter(|field| !kept.contains(field))
        .collect();
    if !loose.is_empty() {
        return Err(config_error(format!(
            "{at}'s Constants does not list {}, which each stamp sets and nothing may change after",
            loose
                .iter()
                .map(|field| field.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        )));
    }
    for step in config.steps.iter().flatten() {
        for field in step.data.keys() {
            if !child.fields.contains_key(field) {
                return Err(config_error(format!(
                    "step {}: data sets {field}, which is not a field of {}",
                    step.key, child.type_name
                )));
            }
        }
    }
    for name in &config.copy_links {
        let own = link_of(target.configs().get("Links"), name);
        let Some(copy) = link_of(child_links, name) else {
            return Err(config_error(format!("copyLinks: {at} has no link {name}")));
        };
        if copy.get("schema") != own.and_then(|own| own.get("schema")) {
            return Err(config_error(format!(
                "copyLinks: {at}'s link {name} points at {}, and this type's at {}",
                js_text(copy.get("schema")),
                js_text(own.and_then(|own| own.get("schema")))
            )));
        }
        if link_many(copy) {
            return Err(config_error(format!(
                "copyLinks: {at}'s link {name} holds a list of targets, and a stamp copies one"
            )));
        }
    }
    Ok(())
}

// check_source holds from to the schema its link points at: a live version
// that composes Revisions, with the field that holds the map.
fn check_source(from: &BlueprintFrom, schema: &str, source: Option<ConfigSchema>) -> Result<()> {
    let Some(source) = source else {
        return Err(config_error(format!(
            "from: schema {schema}, which link {} points at, has no live version; publish it first",
            from.link
        )));
    };
    if !source.behaviors.iter().any(|name| name == "Revisions") {
        return Err(config_error(format!(
            "from: {schema} does not compose Revisions, so link {} cannot pin a revision of its steps",
            from.link
        )));
    }
    let Some(kind) = source.fields.get(&from.field) else {
        return Err(config_error(format!(
            "from: {schema} has no field {}",
            from.field
        )));
    };
    let kind = kind.as_str().unwrap_or_default();
    if kind != "object" && kind != "any" {
        return Err(config_error(format!(
            "from: {schema}'s {} holds {kind}, not a map of steps",
            from.field
        )));
    }
    Ok(())
}

fn strings(value: Option<&Value>) -> Option<Vec<String>> {
    value.and_then(Value::as_array).map(|items| {
        items
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect()
    })
}

fn vetoed<C>(
    view: &InstanceView<'_, C>,
    operation: &str,
    reason: String,
    code: &str,
    details: Option<Value>,
) -> Error {
    Error::veto(
        NAME,
        operation,
        view.schema(),
        view.id(),
        Veto {
            reason,
            code: Some(code.to_owned()),
            details: match details {
                Some(Value::Object(details)) => Some(details),
                _ => None,
            },
        },
    )
}

fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

fn stamped<C>(view: &InstanceView<'_, C>) -> Result<bool> {
    Ok(view
        .columns()
        .get()?
        .get("stamped_at")
        .is_some_and(|at| !at.is_null()))
}

// links_of reads this instance's Links targets field, as the caller: the
// links that hold one target. A list, which parse_config refuses for every
// link Blueprint reads, is no link here.
fn links_of(cx: &InstanceCx<'_, BlueprintConfig>) -> Result<Map<String, Value>> {
    let record = cx
        .instances()
        .get(cx.schema(), cx.id(), Some(&[LINKS_TARGETS]))?;
    let links = record
        .as_ref()
        .and_then(|record| record.behavior_field("Links", "targets"))
        .and_then(Value::as_object);
    Ok(links
        .map(|links| {
            js_keys(links)
                .into_iter()
                .filter(|name| !links[*name].is_array())
                .map(|name| (name.to_owned(), links[name].clone()))
                .collect()
        })
        .unwrap_or_default())
}

// revision_data reads one revision of an instance's own fields through
// Revisions' getRevision, as the caller; `None` when the instance has no
// such revision.
fn revision_data(
    cx: &InstanceCx<'_, BlueprintConfig>,
    schema: &str,
    id: &str,
    revision: &Value,
) -> Result<Option<Value>> {
    match cx.instances().invoke(
        schema,
        id,
        "getRevision",
        &json!({ "revision": revision }),
        None,
    ) {
        Ok(found) => Ok(Some(found.get("data").cloned().unwrap_or(Value::Null))),
        Err(error) if error.code() == Some(EngineErrorCode::NotFound) => Ok(None),
        Err(error) => Err(error),
    }
}

// stamp creates the children of the steps for this instance, each with its
// link to it, the copied links and its edges as create parameters, and
// records them.
fn stamp(
    cx: &InstanceCx<'_, BlueprintConfig>,
    steps: &[BlueprintStep],
    operation: &str,
) -> Result<()> {
    let config = cx.config();
    let data = cx.data()?;
    let plan = effective_steps(steps, &data);
    if plan.iter().any(|step| !step.after.is_empty())
        && cx
            .schemas()
            .config(&config.schema, "Dependencies")?
            .is_none()
    {
        return Err(vetoed(
            cx,
            operation,
            format!(
                "{} does not compose Dependencies, so a step's after cannot block its child",
                config.schema
            ),
            "no_dependencies",
            None,
        ));
    }
    // The child schema's live version may have dropped what parse_config
    // required of its Constants since this schema was published.
    let mut constant = vec![config.key_field.clone()];
    constant.extend(config.copy_fields.iter().cloned());
    let kept = strings(
        cx.schemas()
            .config(&config.schema, "Constants")?
            .as_ref()
            .and_then(|constants| constants.get("fields")),
    );
    let loose: Vec<String> = constant
        .iter()
        .filter(|field| !kept.as_deref().unwrap_or_default().contains(field))
        .cloned()
        .collect();
    if !loose.is_empty() {
        return Err(vetoed(
            cx,
            operation,
            match kept {
                None => format!(
                    "{} does not compose Constants, so the fields each stamp sets in a child could change after it",
                    config.schema
                ),
                Some(_) => format!(
                    "{}'s Constants does not list {}, which each stamp sets and nothing may change after",
                    config.schema,
                    loose.join(", ")
                ),
            },
            "not_constant",
            Some(json!({ "fields": loose })),
        ));
    }
    let mut copied = Map::new();
    for field in &config.copy_fields {
        if let Some(value) = data.get(field) {
            copied.insert(field.clone(), value.clone());
        }
    }
    let held = if config.copy_links.is_empty() {
        Map::new()
    } else {
        links_of(cx)?
    };
    let child_links = if config.copy_links.is_empty() {
        None
    } else {
        cx.schemas().config(&config.schema, "Links")?
    };
    let table = cx.sql().table("children")?;
    let mut ids: HashMap<String, String> = HashMap::new();
    for (position, step) in plan.iter().enumerate() {
        let mut links = Map::new();
        links.insert(config.parent_link.clone(), Value::from(cx.id()));
        for name in &config.copy_links {
            let Some(link) = held.get(name) else {
                continue;
            };
            // The child keeps this instance's pin where its own link pins
            // the same kind.
            let pin = link_of(child_links.as_ref(), name).and_then(link_pin);
            let value = pin.and_then(|pin| link.get(pin.as_str()));
            let id = link.get("id").cloned().unwrap_or(Value::Null);
            let entry = match (pin, value) {
                (Some(pin), Some(value)) => {
                    let mut entry = Map::new();
                    entry.insert("id".into(), id);
                    entry.insert(pin.as_str().to_owned(), value.clone());
                    Value::Object(entry)
                }
                _ => id,
            };
            links.insert(name.clone(), entry);
        }
        let mut behaviors = Map::new();
        behaviors.insert("Links".into(), Value::Object(links));
        if !step.after.is_empty() {
            let blockers: Vec<Value> = step
                .after
                .iter()
                .map(|blocker| json!({ "id": ids.get(blocker) }))
                .collect();
            behaviors.insert("Dependencies".into(), json!({ "blockers": blockers }));
        }
        let mut fields = Map::new();
        fields.insert(config.key_field.clone(), Value::from(step.key.as_str()));
        for (name, value) in copied.iter().chain(&step.data) {
            fields.insert(name.clone(), value.clone());
        }
        let child = cx.instances().create(
            &config.schema,
            &Value::Object(fields),
            None,
            Some(&Value::Object(behaviors)),
        )?;
        ids.insert(step.key.clone(), child.id.clone());
        let mut values = key(cx);
        values.extend([
            SqlValue::from(i64::try_from(position).unwrap_or(i64::MAX)),
            SqlValue::from(step.key.as_str()),
            SqlValue::from(child.id.as_str()),
        ]);
        cx.sql().run(
            format!("INSERT INTO {table} (namespace, schema, id, position, step, child_id) VALUES (?, ?, ?, ?, ?, ?)"),
            &values,
        )?;
    }
    cx.columns()
        .set(&[("stamped_at", SqlValue::from(cx.now()))])
}

// stamp_from stamps the map the from link's pinned revision holds, which it
// holds to the rules inline steps follow; an invalid one refuses the change
// that stamps: the create that gives the link, or the link.
fn stamp_from(
    cx: &InstanceCx<'_, BlueprintConfig>,
    from: &BlueprintFrom,
    operation: &str,
) -> Result<()> {
    let links = links_of(cx)?;
    let link = links.get(&from.link);
    let revision = link.and_then(|link| link.get("revision"));
    let (Some(link), Some(revision)) = (link, revision) else {
        return Err(vetoed(
            cx,
            operation,
            format!(
                "link {} records no revision to read its steps from",
                from.link
            ),
            "no_revision",
            None,
        ));
    };
    let schema = link
        .get("schema")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let id = link.get("id").and_then(Value::as_str).unwrap_or_default();
    let source = format!("{schema} {id} revision {}", stringify(revision));
    let Some(data) = revision_data(cx, schema, id, revision)? else {
        return Err(vetoed(
            cx,
            operation,
            format!("{source} cannot be read"),
            "unreadable",
            None,
        ));
    };
    let config = cx.config();
    let parsed = match data.get(&from.field) {
        None => Err(steps_error(format!("it has no {}", from.field))),
        Some(raw) => parse_steps(raw).and_then(|steps| {
            check_steps(
                &config.key_field,
                &config.fields,
                &config.list_fields,
                cx.schema(),
                &steps,
            )
            .map(|()| steps)
        }),
    };
    let steps = parsed.map_err(|StepsError(message)| {
        vetoed(
            cx,
            operation,
            format!("the steps of {source} are invalid: {message}"),
            "invalid_steps",
            None,
        )
    })?;
    stamp(cx, &steps, operation)
}

/// Blueprint: children created with their parent.
#[derive(Clone, Copy, Debug, Default)]
pub struct Blueprint;

impl Behavior for Blueprint {
    type Config = BlueprintConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Blueprint.behavior.json")
    }

    // The configSchema holds the shape; this holds the config to this type,
    // the child schema and the definition's.
    fn parse_config(&self, raw: &Value, target: &ConfigTarget<'_>) -> Result<BlueprintConfig> {
        let text = |name: &str| {
            raw.get(name)
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned()
        };
        let list_fields: Vec<String> = target
            .fields()
            .iter()
            .filter(|field| {
                json_types(target.field_schemas().get(field.as_str()))
                    .iter()
                    .any(|one| one == "array")
            })
            .cloned()
            .collect();
        let copy_fields = strings(raw.get("copyFields")).unwrap_or_default();
        let copy_links = strings(raw.get("copyLinks")).unwrap_or_default();
        let parent_link = text("parentLink");
        let key_field = text("keyField");
        for field in &copy_fields {
            if !target.fields().contains(field) {
                return Err(config_error(format!(
                    "copyFields: {field} is not a field of {} (its fields: {})",
                    target.type_name(),
                    target.fields().join(", ")
                )));
            }
        }
        let from = raw.get("from").map(|from| BlueprintFrom {
            link: from
                .get("link")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            field: from
                .get("field")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
        });
        let links = target.configs().get("Links");
        if (from.is_some() || !copy_links.is_empty())
            && !target.behaviors().iter().any(|name| name == "Links")
        {
            return Err(config_error(format!(
                "{} reads links of the type's Links, which the type does not list",
                if from.is_some() { "from" } else { "copyLinks" }
            )));
        }
        if let Some(from) = &from {
            let Some(link) = link_of(links, &from.link) else {
                return Err(config_error(format!(
                    "from: the type's Links has no link {}",
                    from.link
                )));
            };
            match link_pin(link) {
                Some(LinkPin::Revision) => {}
                Some(LinkPin::Release) => {
                    return Err(config_error(format!(
                        "from: link {} pins a release, not the revision of the fields its steps are read from",
                        from.link
                    )))
                }
                None => {
                    return Err(config_error(format!(
                        "from: link {} is not pinned, so it cannot hold the revision its steps are read from",
                        from.link
                    )))
                }
            }
            if link_many(link) {
                return Err(config_error(format!(
                    "from: link {} holds a list of targets, and the steps are read from one",
                    from.link
                )));
            }
            if let Some(schemas) = target.schemas() {
                let schema = js_text(link.get("schema"));
                check_source(from, &schema, schemas.get(&schema)?)?;
            }
        }
        for name in &copy_links {
            if *name == parent_link {
                return Err(config_error(format!(
                    "copyLinks names {name}, the link each child points at its parent through"
                )));
            }
            let Some(copied) = link_of(links, name) else {
                return Err(config_error(format!(
                    "copyLinks: the type's Links has no link {name}"
                )));
            };
            if link_many(copied) {
                return Err(config_error(format!(
                    "copyLinks: link {name} holds a list of targets, and a stamp copies one"
                )));
            }
        }
        let fields: Vec<String> = target.fields().to_vec();
        let steps = match raw.get("steps") {
            None => None,
            Some(given) => Some(
                parse_steps(given)
                    .and_then(|steps| {
                        check_steps(
                            &key_field,
                            &fields,
                            &list_fields,
                            target.type_name(),
                            &steps,
                        )
                        .map(|()| steps)
                    })
                    .map_err(|StepsError(message)| config_error(format!("steps: {message}")))?,
            ),
        };
        let config = BlueprintConfig {
            schema: text("schema"),
            parent_link,
            key_field,
            steps,
            from,
            copy_fields,
            copy_links,
            fields,
            list_fields,
        };
        if let Some(schemas) = target.schemas() {
            check_child(&config, target, schemas.get(&config.schema)?)?;
        }
        Ok(config)
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            guidance: Some(|_, config, target| {
                crate::guidance::blueprint::guidance(config, target)
            }),
            config_change: Some(|_, before, after, _| {
                Ok((before.is_some() && after.is_none()).then(|| {
                    "the record of what its instances stamped would stay behind".to_owned()
                }))
            }),
            migrations: vec![BehaviorMigration::new(1, "blueprint")
                .column("stamped_at", ColumnSpec::integer())
                .table(
                    TableSpec::new("children")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("position", ColumnSpec::integer().not_null())
                        .column("step", ColumnSpec::text().not_null())
                        .column("child_id", ColumnSpec::text().not_null())
                        .primary_key(&["namespace", "schema", "id", "position"]),
                )],
            // Once stamped from a definition, the link that pins it stays.
            guard: Some(|_, view, request| {
                let Some(from) = &view.config().from else {
                    return Ok(None);
                };
                let GuardRequest::Operation {
                    behavior: "Links",
                    operation: "link",
                    params,
                    ..
                } = request
                else {
                    return Ok(None);
                };
                if params.get("name").and_then(Value::as_str) != Some(from.link.as_str())
                    || !stamped(view)?
                {
                    return Ok(None);
                }
                Ok(Some(Veto {
                    reason: format!(
                        "its children were stamped from the revision its link {} pins, so the link cannot move",
                        from.link
                    ),
                    code: Some("stamped".to_owned()),
                    details: None,
                }))
            }),
            fields: vec![("children", |_, view| {
                if !stamped(view)? {
                    return Ok(None);
                }
                let table = view.sql().table("children")?;
                let rows = view.sql().all(
                    format!("SELECT step, child_id FROM {table} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY position"),
                    &key(view),
                )?;
                let children = rows
                    .iter()
                    .map(|row| Ok(json!({ "key": row.text("step")?, "id": row.text("child_id")? })))
                    .collect::<Result<Vec<_>>>()?;
                Ok(Some(Value::Array(children)))
            })],
            after_change: Some(|_, cx, change| {
                let config = cx.config();
                match change {
                    InstanceChange::Create => {
                        if let Some(steps) = &config.steps {
                            stamp(cx, steps, "create")
                        } else if let Some(from) = &config.from {
                            if links_of(cx)?.contains_key(&from.link) {
                                stamp_from(cx, from, "create")
                            } else {
                                Ok(())
                            }
                        } else {
                            Ok(())
                        }
                    }
                    InstanceChange::Operation {
                        behavior: "Links",
                        operation: "link",
                        params,
                        ..
                    } => match &config.from {
                        Some(from)
                            if params.get("name").and_then(Value::as_str)
                                == Some(from.link.as_str())
                                && !stamped(cx)? =>
                        {
                            stamp_from(cx, from, "link")
                        }
                        _ => Ok(()),
                    },
                    InstanceChange::Delete => {
                        let table = cx.sql().table("children")?;
                        cx.sql().run(
                            format!(
                                "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
                            ),
                            &key(cx),
                        )?;
                        Ok(())
                    }
                    _ => Ok(()),
                }
            }),
            ..Hooks::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_step_key_matches_the_typescript_pattern() {
        for key in ["a", "Z9", "build.v2", "a:b-c_d", &"x".repeat(128)] {
            assert!(is_step_key(key), "{key}");
        }
        for key in ["", "a b", "-a", ".a", "a/b", &"x".repeat(129)] {
            assert!(!is_step_key(key), "{key}");
        }
    }
}
