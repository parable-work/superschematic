//! Blueprint, children created with their parent (D16, amended), ported
//! onto the pure core (D149) from the engine's
//! (`../../engine-workqueue/rust/src/blueprint.rs`). A table says
//! `implements Blueprint<Batch, { schema, parentLink, keyField, steps,
//! copyFields?, copyLinks? }>`. A map of steps, by key, says which children
//! a row has: one row of the child table (`schema`) per step, whose to-one
//! relation `parentLink` points at the row, whose `keyField` holds the
//! step's key, and which is blocked, through the child table's
//! Dependencies, by the children of the steps it comes after.
//!
//! The create of a row stamps its children in its after-change hook: each
//! one a create of the child table through that table's own create
//! ([`superschematic_rules::Cx::create_row`]: its validation, guards,
//! initializers and after-change hooks), with its blockers as Dependencies'
//! create parameters, so a child is blocked from its create and never
//! claimable before its edges exist. Every child is in the command's change
//! set, so a refusal of any of them refuses the parent's create, and
//! nothing is left. A child's key is a UUID, in its base62 wire form,
//! drawn from the command's request id ([`superschematic_rules::Cx::new_key`]),
//! so a run again makes the same keys.
//!
//! A step's `when` holds when the row's own field equals a JSON value, or
//! is a list that includes one, compared as JSON; a step left out passes
//! the steps it came after on to the steps that come after it, so the chain
//! stays connected. Each child's own fields are its key in `keyField`, then
//! `copyFields` from the row (the ones it holds), then the step's data,
//! then its relation to the row and the relations `copyLinks` names: this
//! table's to-one relations, each copied to the child table's relation of
//! the same name.
//!
//! Blueprint is held to D103's test of a behavior and fails it, as D149
//! admits: it holds no state once its rows exist. Its one field,
//! `children`, is what a read computes ([`Behavior::read_view`]): the rows
//! of the child table whose `parentLink` points at the row and whose
//! `keyField` holds a step of the template, each its step's key and its
//! key, in the order the template lists its steps (a step after
//! the steps it comes after, ties in the map's order), the order the stamp
//! creates them in; absent while none points at it. It recurs (a batch's
//! jobs, a build's steps, a release's checks), and D149 ports it because
//! `examples/engine-jobs` and the app D104 counted need it now.
//!
//! What changed from the engine, and why. Steps kept in a definition
//! (`from`) are read from the revision a pinned link records, and a table's
//! relation pins none, so a table's Blueprint takes inline steps alone and
//! the core refuses `from` at define; so a `copyLinks` link copies a
//! relation's key, never a pin. With no state, nothing is stamped twice and
//! nothing stays behind: Blueprint joins and leaves a table with rows, and
//! a row's delete leaves its children to their relation's rule (a foreign
//! key's `onDelete`). The engine's creator checks the child schema's
//! permissions at each child's create; on a table the access policy is the
//! route's (D103), so a child's create asks none.
//!
//! The config is checked at define, first as the declaration's
//! configSchema holds it, each issue at its path in the schema loader's
//! words ([`ConfigError::Schema`]), then against the table's own fields
//! and relations, in the engine's words where they apply
//! ([`ConfigError::Rule`]): each step's shape, each `after` a step of the
//! map, no cycle, `when` and `copyFields` own fields, `data` leaving the
//! key alone, `copyLinks` to-one relations other than the parent link.
//! What reads the child table (it exists and composes no Blueprint, its
//! relation `parentLink` points here, `keyField` is a string field,
//! Constants keeps `keyField` and every copied field, Dependencies for an
//! `after`, the fields a step's data and `copyFields` set, a `when`'s
//! `includes` over a list) is the build's to check
//! (`verify/behaviors_blueprint.go`) and the stamp's, since a type's define
//! check sees its own spec alone: the stamp vetoes `no_dependencies` and
//! `not_constant` as the engine did, and any other mismatch with the child
//! table's live version without a code.

use serde_json::{json, Map, Value};
use sha2::{Digest, Sha256};
use std::collections::{HashMap, HashSet};
use std::sync::OnceLock;
use superschematic_rules::{
    Behavior, Change, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, FieldSpec, Hook,
    Refusal, TypeSpec, Veto, View,
};

use crate::ajv;
use crate::declared;
use crate::derived::js_keys;
use crate::schema;
use crate::values;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Blueprint";

/// Blueprint's declaration, the file the compiler registers and the
/// engine embeds, read in place.
pub const DECLARATION: &str =
    include_str!("../../../../engine-workqueue/rust/declarations/Blueprint.behavior.json");

/// The field a read shows what the row's children are in.
pub const CHILDREN: &str = "children";

/// The most steps a map holds: the most children one stamp creates.
pub const MAX_STEPS: usize = 500;

/// The label of a child's key ([`superschematic_rules::Cx::new_key`]).
const CHILD_KEY: &str = "child";

/// The pattern a step key matches, as the TypeScript package writes it.
const STEP_KEY: &str = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$";

// The behaviors a stamp reads of the child table.
const CONSTANTS: &str = "Constants";
const DEPENDENCIES: &str = "Dependencies";

fn file() -> &'static Value {
    static FILE: OnceLock<Value> = OnceLock::new();
    FILE.get_or_init(|| ajv::file(DECLARATION))
}

/// When a step is stamped: the row's own field equals a value, or is a list
/// that includes one.
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
    /// The child's own fields beside its key and the copied ones.
    pub data: Map<String, Value>,
}

/// A relation `copyLinks` names: its name and the own field of this table
/// that holds its key.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CopiedLink {
    pub name: String,
    pub field: String,
}

/// Blueprint's config, parsed.
#[derive(Clone, Debug, PartialEq)]
pub struct BlueprintConfig {
    /// The child table.
    pub schema: String,
    /// The child table's to-one relation to this table.
    pub parent_link: String,
    /// The child table's string field a child holds its step's key in.
    pub key_field: String,
    /// The steps, in the map's order.
    pub steps: Vec<BlueprintStep>,
    /// The steps' keys in the order the template lists them: each after the
    /// steps it comes after, ties in the map's order.
    pub order: Vec<String>,
    pub copy_fields: Vec<String>,
    pub copy_links: Vec<CopiedLink>,
}

impl BlueprintConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then against the table's own fields and relations, each a
    /// rule's reason which the core prefixes with the type and the
    /// behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<BlueprintConfig, ConfigError> {
        let issues = ajv::config_issues(file(), raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues));
        }
        if raw.get("from").is_some() {
            return Err(
                "from reads its steps from the revision a pinned link records, and a table's relation pins none: a table's Blueprint takes inline steps (D149)"
                    .into(),
            );
        }
        let text = |name: &str| {
            raw.get(name)
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned()
        };
        let parent_link = text("parentLink");
        let key_field = text("keyField");
        // A host that gives no fields gives a type whose fields no rule
        // holds a config to.
        let given = !target.fields().is_empty();
        let fields: Vec<&str> = target
            .fields()
            .iter()
            .filter(|field| field.relation.is_none())
            .map(|field| field.name.as_str())
            .collect();
        let copy_fields = strings(raw.get("copyFields"));
        if given {
            for field in &copy_fields {
                if !fields.contains(&field.as_str()) {
                    return Err(format!(
                        "copyFields: {field} is not a field of {} (its fields: {})",
                        target.type_name(),
                        fields.join(", ")
                    )
                    .into());
                }
            }
        }
        let mut copy_links = Vec::new();
        for name in strings(raw.get("copyLinks")) {
            if name == parent_link {
                return Err(format!(
                    "copyLinks names {name}, the link each child points at its parent through"
                )
                .into());
            }
            let field = relation_of(target.fields(), &name);
            match field {
                Some(field) => copy_links.push(CopiedLink {
                    name,
                    field: field.name.clone(),
                }),
                None if given => {
                    return Err(format!(
                        "copyLinks: {} has no to-one relation {name}",
                        target.type_name()
                    )
                    .into())
                }
                None => copy_links.push(CopiedLink {
                    field: format!("{name}Id"),
                    name,
                }),
            }
        }
        let steps = parse_steps(raw.get("steps").unwrap_or(&Value::Null))
            .and_then(|steps| {
                check_steps(
                    &key_field,
                    given.then_some(fields.as_slice()),
                    target.type_name(),
                    &steps,
                )
                .map(|()| steps)
            })
            .map_err(|StepsError(message)| -> ConfigError { format!("steps: {message}").into() })?;
        let order = ordered(&steps);
        Ok(BlueprintConfig {
            schema: text("schema"),
            parent_link,
            key_field,
            steps,
            order,
            copy_fields,
            copy_links,
        })
    }

    /// The fields a stamp sets in every child and the child table's
    /// Constants must keep: `keyField`, then each copied field.
    fn constant_fields(&self) -> Vec<String> {
        let mut constant = vec![self.key_field.clone()];
        constant.extend(self.copy_fields.iter().cloned());
        constant
    }
}

// relation_of is the own field that holds a to-one relation's key, by the
// relation's name.
fn relation_of<'f>(fields: &'f [FieldSpec], name: &str) -> Option<&'f FieldSpec> {
    fields.iter().find(|field| {
        field
            .relation
            .as_ref()
            .is_some_and(|relation| relation.name == name)
    })
}

// strings reads a config's list of strings.
fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
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
// another step of the map, in the engine's words.
fn parse_steps(raw: &Value) -> Result<Vec<BlueprintStep>, StepsError> {
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
                schema::quoted(key)
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
        let listed = || steps_error(format!("step {key}: after is a list of distinct step keys"));
        let after = match step.get("after") {
            None | Some(Value::Null) => Vec::new(),
            Some(Value::Array(items)) => {
                let mut after: Vec<String> = Vec::new();
                for item in items {
                    match item.as_str() {
                        Some(other) if !after.iter().any(|seen| seen == other) => {
                            after.push(other.to_owned());
                        }
                        _ => return Err(listed()),
                    }
                }
                after
            }
            Some(_) => return Err(listed()),
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

fn parse_when(key: &str, raw: &Value) -> Result<BlueprintWhen, StepsError> {
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
fn check_acyclic(steps: &[BlueprintStep]) -> Result<(), StepsError> {
    fn visit<'s>(
        key: &'s str,
        by_key: &HashMap<&str, &'s BlueprintStep>,
        done: &mut HashSet<&'s str>,
        path: &mut Vec<&'s str>,
    ) -> Result<(), StepsError> {
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

// check_steps holds a parsed map to this type and the config: a when names
// a field of the type, and data leaves the key alone. `fields` is `None`
// for a host that gives no fields. Whether an includes reads a list is the
// build's, since a type's spec does not say which fields are lists.
fn check_steps(
    key_field: &str,
    fields: Option<&[&str]>,
    type_name: &str,
    steps: &[BlueprintStep],
) -> Result<(), StepsError> {
    for step in steps {
        if let (Some(when), Some(fields)) = (&step.when, fields) {
            let field = when.field();
            if !fields.contains(&field) {
                return Err(steps_error(format!(
                    "step {}: when names {field}, which is not a field of {type_name} (its fields: {})",
                    step.key,
                    fields.join(", ")
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

// ordered is the steps' keys in the order the template lists them: the
// first step in the map's order whose steps before it are placed, then the
// next, so each comes after the steps it comes after. The map is acyclic.
fn ordered(steps: &[BlueprintStep]) -> Vec<String> {
    let mut placed: Vec<String> = Vec::new();
    while placed.len() < steps.len() {
        let Some(next) = steps.iter().find(|step| {
            !placed.contains(&step.key) && step.after.iter().all(|key| placed.contains(key))
        }) else {
            break;
        };
        placed.push(next.key.clone());
    }
    placed
}

// holds reports whether a step's when holds for the row's own fields.
fn holds(when: Option<&BlueprintWhen>, own: &Map<String, Value>) -> bool {
    match when {
        None => true,
        Some(BlueprintWhen::Equals { field, value }) => {
            values::equal(own.get(field), Some(value)) && own.contains_key(field)
        }
        Some(BlueprintWhen::Includes { field, value }) => own
            .get(field)
            .and_then(Value::as_array)
            .is_some_and(|items| {
                items
                    .iter()
                    .any(|item| values::equal(Some(item), Some(value)))
            }),
    }
}

// unique keeps the first of each key, in order.
fn unique(keys: Vec<String>) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for key in keys {
        if !out.contains(&key) {
            out.push(key);
        }
    }
    out
}

/// The children a stamp creates for a row's own fields: the steps whose
/// when holds, each after the included steps it comes after and, in place
/// of a step left out, the ones that step comes after, transitively, in the
/// template's order ([`BlueprintConfig::order`]), which places each after
/// the steps it comes after.
fn plan(config: &BlueprintConfig, own: &Map<String, Value>) -> Vec<BlueprintStep> {
    let by_key: HashMap<&str, &BlueprintStep> = config
        .steps
        .iter()
        .map(|step| (step.key.as_str(), step))
        .collect();
    let included: HashSet<&str> = config
        .steps
        .iter()
        .filter(|step| holds(step.when.as_ref(), own))
        .map(|step| step.key.as_str())
        .collect();
    // through is what a step gives the after of a step that comes after
    // it: itself when it is included, else what it comes after.
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
    config
        .order
        .iter()
        .filter(|key| included.contains(key.as_str()))
        .filter_map(|key| by_key.get(key.as_str()))
        .map(|step| {
            let mut after = Vec::new();
            for other in &step.after {
                after.extend(through(other, &by_key, &included, &mut passed));
            }
            BlueprintStep {
                after: unique(after),
                ..(*step).clone()
            }
        })
        .collect()
}

/// A child's key: a UUID drawn from a key the run made
/// ([`superschematic_rules::Cx::new_key`]), the first sixteen bytes of its
/// SHA-256 marked as a version 8 UUID of the RFC 9562 variant, in its
/// base62 wire form, which a table keyed by a UUID and one keyed by a
/// string both take.
pub fn child_key(made: &str) -> String {
    let digest = Sha256::digest(made.as_bytes());
    let mut bytes = [0_u8; 16];
    bytes.copy_from_slice(&digest[..16]);
    bytes[6] = (bytes[6] & 0x0f) | 0x80;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    crate::subjects::uuid_wire(u128::from_be_bytes(bytes))
}

// child_spec is the child table's live spec, held to what a stamp needs of
// it: it exists, composes no Blueprint, and its relation parentLink points
// at this table. Each mismatch is a veto without a code.
fn child_spec<'r>(cx: &Cx<'r, BlueprintConfig>) -> Hook<(&'r TypeSpec, String)> {
    let config = cx.config();
    let at = format!("schema {}", config.schema);
    let Some(child) = cx.read_spec(&config.schema)? else {
        return Err(cx.veto(Veto::new(format!("{at} does not exist"))));
    };
    if child.uses.iter().any(|entry| entry.behavior == NAME) {
        return Err(cx.veto(Veto::new(format!(
            "{at} composes Blueprint: a child cannot stamp children of its own"
        ))));
    }
    let Some(field) = child.relation(&config.parent_link) else {
        return Err(cx.veto(Veto::new(format!(
            "{at} has no to-one relation {}, through which its children point at their parent",
            config.parent_link
        ))));
    };
    let target = field
        .relation
        .as_ref()
        .map(|relation| relation.type_name.as_str())
        .unwrap_or_default();
    if target != cx.type_name() {
        return Err(cx.veto(Veto::new(format!(
            "{at}'s relation {} points at {target}, not {}",
            config.parent_link,
            cx.type_name()
        ))));
    }
    Ok((child, field.name.clone()))
}

// checked holds the template to the child table's live version, as the
// engine's stamp held it: Dependencies for an after (no_dependencies),
// Constants over keyField and every copied field (not_constant), and the
// fields each child is given.
fn checked(
    cx: &Cx<'_, BlueprintConfig>,
    child: &TypeSpec,
    plan: &[BlueprintStep],
) -> Hook<Option<String>> {
    let config = cx.config();
    let dependencies = child
        .uses
        .iter()
        .find(|entry| entry.behavior == DEPENDENCIES)
        .map(|entry| entry.id().to_owned());
    if plan.iter().any(|step| !step.after.is_empty()) && dependencies.is_none() {
        return Err(cx.veto(
            Veto::new(format!(
                "{} does not compose Dependencies, so a step's after cannot block its child",
                config.schema
            ))
            .with_code("no_dependencies"),
        ));
    }
    // The child table's live version may have dropped what the build
    // required of its Constants since this table was built.
    let constants = child
        .uses
        .iter()
        .find(|entry| entry.behavior == CONSTANTS)
        .map(|entry| strings(entry.config_or_empty().get("fields")));
    let constant = config.constant_fields();
    let loose: Vec<String> = constant
        .iter()
        .filter(|field| !constants.as_deref().unwrap_or_default().contains(field))
        .cloned()
        .collect();
    if !loose.is_empty() {
        let mut details = Map::new();
        details.insert("fields".into(), json!(loose));
        return Err(cx.veto(
            Veto::new(match constants {
                None => format!(
                    "{} does not compose Constants, so the fields each stamp sets in a child could change after it",
                    config.schema
                ),
                Some(_) => format!(
                    "{}'s Constants does not list {}, which each stamp sets and nothing may change after",
                    config.schema,
                    loose.join(", ")
                ),
            })
            .with_code("not_constant")
            .with_details(details),
        ));
    }
    // A host that gives the child no fields gives none to hold a child to.
    if child.fields.is_empty() {
        return Ok(dependencies);
    }
    if child
        .field(&config.key_field)
        .is_none_or(|field| field.type_name != "string")
    {
        return Err(cx.veto(Veto::new(format!(
            "keyField {} is not a string field of {}",
            config.key_field, config.schema
        ))));
    }
    for field in &config.copy_fields {
        if child.field(field).is_none() {
            return Err(cx.veto(Veto::new(format!(
                "copyFields: {field} is not a field of {}",
                config.schema
            ))));
        }
    }
    for step in plan {
        for field in step.data.keys() {
            if child.field(field).is_none() {
                return Err(cx.veto(Veto::new(format!(
                    "step {}: data sets {field}, which is not a field of {}",
                    step.key, config.schema
                ))));
            }
        }
    }
    Ok(dependencies)
}

// stamp creates the row's children: each through the child table's own
// create, with its relation to the row, the copied fields and relations,
// and its blockers as Dependencies' create parameters, in the plan's
// order.
fn stamp(cx: &Cx<'_, BlueprintConfig>) -> Hook<()> {
    let config = cx.config();
    let own = cx.own();
    let plan = plan(config, &own);
    if plan.is_empty() {
        return Ok(());
    }
    let Some(parent) = cx.key().map(str::to_owned) else {
        return Err(Refusal::invalid_argument(format!(
            "a create of {} names no key, and Blueprint links its children to it by its key",
            cx.type_name()
        ))
        .into());
    };
    let (child, parent_field) = child_spec(cx)?;
    let dependencies = checked(cx, child, &plan)?;
    let mut links: Vec<(String, Value)> = Vec::new();
    for link in &config.copy_links {
        let Some(value) = own.get(&link.field).filter(|value| !value.is_null()) else {
            continue;
        };
        let Some(field) = child.relation(&link.name) else {
            return Err(cx.veto(Veto::new(format!(
                "copyLinks: schema {} has no to-one relation {}",
                config.schema, link.name
            ))));
        };
        links.push((field.name.clone(), value.clone()));
    }
    let mut copied = Map::new();
    for field in &config.copy_fields {
        if let Some(value) = own.get(field).filter(|value| !value.is_null()) {
            copied.insert(field.clone(), value.clone());
        }
    }
    let mut keys: HashMap<String, String> = HashMap::new();
    for step in &plan {
        let key = child_key(&cx.new_key(CHILD_KEY)?);
        let mut fields = Map::new();
        fields.insert(config.key_field.clone(), Value::from(step.key.as_str()));
        for (name, value) in copied.iter().chain(&step.data) {
            fields.insert(name.clone(), value.clone());
        }
        fields.insert(parent_field.clone(), Value::from(parent.as_str()));
        for (name, value) in &links {
            fields.insert(name.clone(), value.clone());
        }
        let mut params = Map::new();
        if let (Some(use_id), false) = (&dependencies, step.after.is_empty()) {
            let blockers: Vec<Value> = step
                .after
                .iter()
                .map(|blocker| json!({ "id": keys.get(blocker) }))
                .collect();
            params.insert(use_id.clone(), json!({ "blockers": blockers }));
        }
        cx.create_row(&config.schema, &key, &fields, &params)?;
        keys.insert(step.key.clone(), key);
    }
    Ok(())
}

// children reads the rows of the child table that point at the row through
// parentLink, page by page, whose keyField holds a step of the template:
// each its step's key and its key, in the template's order, two of one
// step by their keys. None when the child table has no such relation to
// this table.
fn children(view: &View<'_, BlueprintConfig>) -> Hook<Vec<Value>> {
    let config = view.config();
    let Some(key) = view.key() else {
        return Ok(Vec::new());
    };
    let Some(child) = view.read_spec(&config.schema)? else {
        return Ok(Vec::new());
    };
    let Some(field) = child.relation(&config.parent_link) else {
        return Ok(Vec::new());
    };
    if field
        .relation
        .as_ref()
        .is_none_or(|relation| relation.type_name != view.type_name())
    {
        return Ok(Vec::new());
    }
    let field = field.name.clone();
    let mut found: Vec<(usize, String, String)> = Vec::new();
    let mut after: Option<String> = None;
    loop {
        let page = view.read_linked(&config.schema, &field, key, after.as_deref())?;
        for row in &page.rows {
            let step = row
                .row
                .own
                .get(&config.key_field)
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned();
            // A row whose key is no step of the template is no child of
            // its, made by another create that points it here.
            if let Some(position) = config.order.iter().position(|known| *known == step) {
                found.push((position, step, row.key.clone()));
            }
        }
        match (page.more, page.rows.last()) {
            (true, Some(last)) => after = Some(last.key.clone()),
            _ => break,
        }
    }
    found.sort_by(|a, b| (a.0, &a.2).cmp(&(b.0, &b.2)));
    Ok(found
        .into_iter()
        .map(|(_, step, id)| json!({ "key": step, "id": id }))
        .collect())
}

/// Blueprint: children created with their parent.
#[derive(Clone, Copy, Debug, Default)]
pub struct Blueprint;

impl Behavior for Blueprint {
    type Config = BlueprintConfig;

    // The declaration file declares children, computed at a read, and the
    // stamp's veto codes; the from link's, stamped and the rest, a table's
    // Blueprint does not reach.
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<BlueprintConfig, ConfigError> {
        BlueprintConfig::parse(raw, target)
    }

    // It keeps no state, so any change is allowed, and it joins and leaves
    // a table with rows.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&BlueprintConfig>,
        _after: Option<&BlueprintConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    // A create stamps the row's children, once every initializer ran.
    fn after_change(&self, cx: &Cx<'_, BlueprintConfig>, change: &Change<'_>) -> Hook<()> {
        match change {
            Change::Create => stamp(cx),
            _ => Ok(()),
        }
    }

    // A read shows the rows that point at the row as its children.
    fn read_view(&self, view: &View<'_, BlueprintConfig>) -> Hook<Map<String, Value>> {
        let mut fields = Map::new();
        let listed = children(view)?;
        if !listed.is_empty() {
            fields.insert(CHILDREN.into(), Value::Array(listed));
        }
        Ok(fields)
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

    #[test]
    fn a_childs_key_is_a_uuid_drawn_from_the_key_the_run_made() {
        let key = child_key("request-1:Blueprint:child:1");
        assert_eq!(key, child_key("request-1:Blueprint:child:1"));
        assert_ne!(key, child_key("request-1:Blueprint:child:2"));
        assert!(key.chars().all(|c| c.is_ascii_alphanumeric()) && key.len() <= 22);
    }

    #[test]
    fn the_template_orders_each_step_after_the_ones_it_comes_after() {
        let steps = parse_steps(&json!({
            "review": { "after": ["build"] },
            "build": { "after": ["design"] },
            "design": {}
        }))
        .unwrap_or_default();
        assert_eq!(ordered(&steps), ["design", "build", "review"]);
    }
}
