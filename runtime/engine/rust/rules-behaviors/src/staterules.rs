//! StateRules, the own fields a state of a Workflow requires or freezes
//! (D56), ported onto the pure core (D103) from the engine's
//! (`../behaviors/src/staterules.rs`), with who may change a frozen field
//! (D104). A record's fields mean more as it moves on: an invoice is
//! drafted with any amount, sent only with a customer and an amount that
//! then stay, and paid with nothing left to change. The config gives, by
//! state of the Workflow, the type's own fields, by name, a row must hold
//! to enter the state and keeps while it is in it (`required`), the ones
//! that do not change while it is in it (`frozen`: a list, or `"all"` for
//! every own field), and the fields that hold a subject who may change
//! them all the same (`by`). A field is held when it is neither absent nor
//! null, since a merge patch removes a member with null: `""` and `[]` are
//! held.
//!
//! StateRules is held to D103's test of a behavior and fails it: it keeps
//! no state of its own, and its rules read the table's own fields by the
//! state of its Workflow; D103 names the fields a state requires or freezes
//! among step 5's declarative vocabulary. It is ported because D104 orders
//! it, with `by`, and the question is step 5's, which D104's status
//! records.
//!
//! Each rule sits where it reads what it needs:
//!
//! - A transition into a state is a request of Workflow's `transition` of
//!   the use the rules are of, whoever makes it: a caller, another use's
//!   `call()`, another row's `call_on()`, a Timers fire, a Reactions move.
//!   The guard reads the state from its `to` and the fields from the row as
//!   the run has them, and vetoes a move into a state that requires a field
//!   the row does not hold (`missing_fields`). A move Workflow's config
//!   refuses (the row has no status, or no transition leads from it to
//!   `to`) it lets through, so Workflow's own veto answers it whatever the
//!   list order, as Approvals' gate does.
//! - A create starts in the Workflow's initial state, which `parse_config`
//!   reads from the Workflow's config, so validate refuses a create whose
//!   initial state requires a field it does not give, with the rule
//!   `required` at the field (`invalid_instance`), before anything is
//!   stored.
//! - An update through the core is an update request. The guard reads the
//!   row's status only when a field a rule names changes, and compares the
//!   fields before with the ones after. One that clears a field the state
//!   requires is vetoed whoever asks (`required_fields`), and checked
//!   first, since no permission and no `by` lifts it; one that changes a
//!   field the state freezes is vetoed (`frozen_fields`) unless the caller
//!   holds the config's permission or, for a state with `by`, is the
//!   subject one of its `by` fields holds in the row before the update, so
//!   a patch that names the caller in a `by` field does not count. A `by`
//!   field is an own field that holds a subject, the audit field
//!   `createdBy` among them, read as Workflow's `by` reads one
//!   ([`crate::subjects`]); a `seat:` entry names a seat of Seats, which the
//!   core does not run yet (D104, step 3), and is refused at define, and so
//!   is a `by` in a state that freezes nothing.
//!
//! Each veto names the state and the fields in its details, in the order
//! the type lists its fields for `"all"` and the config's otherwise, and a
//! `frozen_fields` veto names the state's `by` when it has one. A value is
//! compared as JSON, as Constants compares one, so a change inside an
//! object or a list is a change.
//!
//! Which Workflow the rules are of is the config's `status`, the id of a
//! Workflow use of the type: the field it is anchored on, or `Workflow` for
//! one anchored on the type. A type with one Workflow implies it; one with
//! several names it, and one with none is refused. The engine read the
//! instance's one `Workflow`. On a table, the uses it implements come before
//! its field uses (`implements StateRules<..>` before `status:
//! Workflow<..>`), so StateRules' guard is asked before Workflow's: a move
//! the config allows whose `by` or permission the caller fails, into a
//! state whose rule is unmet, is `missing_fields`.
//!
//! The core has no write of own fields from an operation (the engine's
//! `update()`), so the update the rules hold is every own-field write
//! through the core, an upsert's update of the row its key holds among
//! them; a write around it (a bulk update, raw SQL) is held to none of
//! them, the escape hatch D103 keeps.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, each issue at its path in the
//! schema loader's words ([`ConfigError::Schema`]), then each state held to
//! the Workflow and each field to the type's own, in the engine's words
//! ([`ConfigError::Rule`]). The core runs no JSON Schema, so the schema's
//! checks are written here, held to the declaration file by this crate's
//! tests. Messages name the row `{type} {key}`, where the engine's named
//! `{schema} {id}`.
//!
//! configChange: any change of the rules is allowed, and the behavior can
//! be added to and removed from a type with rows: it keeps no state. A row
//! that predates a requirement keeps working until it would lose a field
//! the rule requires or enter the state.

use serde_json::{Map, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, Declaration, GuardRequest, Hook,
    ValidationIssue, ValidationRequest, Veto, View,
};

use crate::declared;
use crate::flow::{self, flow_of, Flow};
use crate::schema::{self, Issues};
use crate::subjects::{holds_subject, names_subject};
use crate::values::{equal, held};
use crate::workflow;

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "StateRules";

/// StateRules' declaration, the file the compiler registers and the engine
/// embeds, read in place: what a host describes the behavior with.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/StateRules.behavior.json");

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 3] = ["permission", "states", "status"];
const RULE_KEYS: [&str; 3] = ["by", "frozen", "required"];

// The bound the configSchema holds a rule's lists to.
const MAX_FIELDS: usize = 64;

// What a by entry that names a seat of Seats starts with (D104, step 3).
const SEAT_PREFIX: &str = "seat:";

/// One state's rules, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StateRule {
    /// The own fields a row must hold to enter the state, and keeps while
    /// it is in it.
    pub required: Vec<String>,
    /// The own fields that do not change while the row is in the state:
    /// every own field of the type, in its order, when frozen_all.
    pub frozen: Vec<String>,
    /// Whether the config freezes every own field (`"all"`).
    pub frozen_all: bool,
    /// The own fields that hold a subject who may change a frozen field
    /// (D104).
    pub by: Vec<String>,
}

/// StateRules' config, parsed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StateRulesConfig {
    /// By state, in the order the Workflow lists its states.
    pub states: Vec<(String, StateRule)>,
    /// The permission a caller needs to change a frozen field; nobody may
    /// when `None`.
    pub permission: Option<String>,
    /// The Workflow use the rules are of, its initial state and its
    /// transitions.
    pub status: String,
    pub initial: String,
    pub transitions: Vec<(String, String)>,
    /// Each field a by names, once, with its IR type.
    pub subject_types: Vec<(String, String)>,
}

impl StateRulesConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then the Workflow it reads, each state held to that
    /// Workflow's and each field to the type's own, a rule's reason in the
    /// engine's words, which the core prefixes with the type and the
    /// behavior.
    pub fn parse(raw: &Value, target: &ConfigTarget<'_>) -> Result<StateRulesConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let flow = flow_of(target, raw.get("status").and_then(Value::as_str))?;
        let empty = Map::new();
        let rules = raw
            .get("states")
            .and_then(Value::as_object)
            .unwrap_or(&empty);
        let type_name = target.type_name();
        let fields = target.fields();
        let names = || -> String {
            let names: Vec<&str> = fields.iter().map(|field| field.name.as_str()).collect();
            if names.is_empty() {
                "none".to_owned()
            } else {
                names.join(", ")
            }
        };
        let field = |state: &str, rule: &str, name: &str| -> Result<(), ConfigError> {
            if fields.iter().any(|known| known.name == name) {
                return Ok(());
            }
            Err(format!(
                "states.{state}.{rule}: {name} is not a field of {type_name} (its fields: {})",
                names()
            )
            .into())
        };
        let mut subject_types: Vec<(String, String)> = Vec::new();
        for (state, rule) in rules {
            if flow.lacks(state) {
                return Err(format!(
                    "states: \"{state}\" is not a state of the type's Workflow ({})",
                    flow.listed()
                )
                .into());
            }
            for name in strings(rule.get("required")) {
                field(state, "required", &name)?;
            }
            if rule.get("frozen").is_some_and(Value::is_array) {
                for name in strings(rule.get("frozen")) {
                    field(state, "frozen", &name)?;
                }
            }
            let Some(by) = rule.get("by") else {
                continue;
            };
            if rule.get("frozen").is_none() {
                return Err(format!(
                    "states.{state}.by lists who may change a field {state} freezes, and {state} freezes none"
                )
                .into());
            }
            for entry in strings(Some(by)) {
                let named = format!("states.{state}.by names \"{entry}\"");
                if entry.starts_with(SEAT_PREFIX) {
                    return Err(format!(
                        "{named}, a seat of Seats, which the core does not run yet (D104, step 3)"
                    )
                    .into());
                }
                let Some(found) = fields.iter().find(|known| known.name == entry) else {
                    return Err(format!(
                        "{named}, which is not a field of {type_name} (its fields: {})",
                        names()
                    )
                    .into());
                };
                if !holds_subject(&found.type_name) {
                    return Err(format!(
                        "{named}, a {} field of {type_name}: by names a field that holds a subject (string, Identity.UUID or Identity.UserID)",
                        found.type_name
                    )
                    .into());
                }
                if !subject_types.iter().any(|(name, _)| *name == entry) {
                    subject_types.push((entry.clone(), found.type_name.clone()));
                }
            }
        }
        // The rules in the order the Workflow lists its states.
        let order: Vec<String> = if flow.states.is_empty() {
            rules.keys().cloned().collect()
        } else {
            flow.states.clone()
        };
        let mut states: Vec<(String, StateRule)> = Vec::new();
        for state in order {
            let Some(rule) = rules.get(&state) else {
                continue;
            };
            if states.iter().any(|(known, _)| *known == state) {
                continue;
            }
            let frozen_all = rule.get("frozen").and_then(Value::as_str) == Some("all");
            states.push((
                state,
                StateRule {
                    required: strings(rule.get("required")),
                    frozen: if frozen_all {
                        fields.iter().map(|field| field.name.clone()).collect()
                    } else {
                        strings(rule.get("frozen"))
                    },
                    frozen_all,
                    by: strings(rule.get("by")),
                },
            ));
        }
        let Flow {
            use_id,
            initial,
            transitions,
            ..
        } = flow;
        Ok(StateRulesConfig {
            states,
            permission: raw
                .get("permission")
                .and_then(Value::as_str)
                .map(str::to_owned),
            status: use_id,
            initial,
            transitions,
            subject_types,
        })
    }

    /// A state's rules; `None` when the config gives it none.
    pub fn rule(&self, state: &str) -> Option<&StateRule> {
        self.states
            .iter()
            .find(|(known, _)| known == state)
            .map(|(_, rule)| rule)
    }

    /// The IR type of a field a by names, as the type's spec gives it;
    /// `None` for a field no by names.
    pub fn subject_type(&self, field: &str) -> Option<&str> {
        self.subject_types
            .iter()
            .find(|(name, _)| name == field)
            .map(|(_, type_name)| type_name.as_str())
    }

    // leads says whether a transition of the Workflow leads from one state
    // to another.
    fn leads(&self, from: &str, to: &str) -> bool {
        flow::leads(&self.transitions, from, to)
    }
}

/// StateRules: what each state of a Workflow requires and freezes, and who
/// may change what it freezes.
#[derive(Clone, Copy, Debug, Default)]
pub struct StateRules;

impl Behavior for StateRules {
    type Config = StateRulesConfig;

    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        target: &ConfigTarget<'_>,
    ) -> Result<StateRulesConfig, ConfigError> {
        StateRulesConfig::parse(raw, target)
    }

    // Any change: StateRules keeps no state, and joins and leaves a type
    // with rows.
    fn config_change(
        &self,
        _cx: &ConfigCx<'_>,
        _before: Option<&StateRulesConfig>,
        _after: Option<&StateRulesConfig>,
    ) -> Hook<Option<String>> {
        Ok(None)
    }

    // A create starts in the initial state: what it requires, the create
    // gives. An update's rules read the status, which the guard reads.
    fn validate(
        &self,
        view: &View<'_, StateRulesConfig>,
        request: &ValidationRequest<'_>,
    ) -> Hook<Vec<ValidationIssue>> {
        let ValidationRequest::Create { own } = request else {
            return Ok(Vec::new());
        };
        let config = view.config();
        let initial = &config.initial;
        let Some(rule) = config.rule(initial) else {
            return Ok(Vec::new());
        };
        Ok(rule
            .required
            .iter()
            .filter(|field| !held(own, field))
            .map(|field| {
                ValidationIssue::new(
                    field.as_str(),
                    "required",
                    format!(
                        "{field} is required in {initial}, the state an instance of {} is created in",
                        view.type_name()
                    ),
                )
            })
            .collect())
    }

    fn guard(
        &self,
        view: &View<'_, StateRulesConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        match request {
            GuardRequest::Update { after, .. } => update_guard(view, after),
            _ => match request.operation_of(&view.config().status, workflow::TRANSITION) {
                Some(params) => transition_guard(view, params),
                None => Ok(None),
            },
        }
    }
}

// transition_guard holds a move of the Workflow the rules are of into a
// state to what the state requires, from the row as the run has it. A move
// Workflow's config refuses is Workflow's to refuse.
fn transition_guard(view: &View<'_, StateRulesConfig>, params: &Value) -> Hook<Option<Veto>> {
    let config = view.config();
    let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
    let Some(rule) = config.rule(to) else {
        return Ok(None);
    };
    let own = view.own();
    let missing: Vec<&str> = rule
        .required
        .iter()
        .map(String::as_str)
        .filter(|field| !held(&own, field))
        .collect();
    if missing.is_empty() {
        return Ok(None);
    }
    let Some(status) = status_of(view) else {
        return Ok(None);
    };
    if !config.leads(&status, to) {
        return Ok(None);
    }
    Ok(Some(veto(
        "missing_fields",
        format!(
            "{} cannot move to {to} without {}, which {to} requires",
            row(view),
            list(&missing, "and")
        ),
        to,
        &missing,
        None,
    )))
}

// update_guard holds an update to the rules of the state the row is in: no
// required field it held cleared, then no frozen field changed but by a
// caller with the permission or the subject a by field of the row before
// the update holds.
fn update_guard(
    view: &View<'_, StateRulesConfig>,
    after: &Map<String, Value>,
) -> Hook<Option<Veto>> {
    let config = view.config();
    let before = view.own();
    let changes = |field: &str| !equal(before.get(field), after.get(field));
    // The status is read only when a field some rule names changes.
    let named = config.states.iter().any(|(_, rule)| {
        rule.frozen.iter().any(|field| changes(field))
            || rule.required.iter().any(|field| changes(field))
    });
    if !named {
        return Ok(None);
    }
    let Some(state) = status_of(view) else {
        return Ok(None);
    };
    let Some(rule) = config.rule(&state) else {
        return Ok(None);
    };
    let cleared: Vec<&str> = rule
        .required
        .iter()
        .map(String::as_str)
        .filter(|field| held(&before, field) && !held(after, field))
        .collect();
    if !cleared.is_empty() {
        let keeps = if cleared.len() == 1 {
            "it keeps its value"
        } else {
            "they keep their values"
        };
        return Ok(Some(veto(
            "required_fields",
            format!(
                "{} is {state}, which requires {}: {keeps} while it is {state}",
                row(view),
                list(&cleared, "and")
            ),
            &state,
            &cleared,
            None,
        )));
    }
    let frozen: Vec<&str> = rule
        .frozen
        .iter()
        .map(String::as_str)
        .filter(|field| changes(field))
        .collect();
    if frozen.is_empty() {
        return Ok(None);
    }
    // The subject a by field holds before the update changes what the
    // state freezes (D104); a patch that names the caller does not count.
    let subject = view.caller().subject.as_str();
    let named_by = rule.by.iter().any(|field| {
        let type_name = config.subject_type(field).unwrap_or("string");
        names_subject(type_name, before.get(field), subject)
    });
    if named_by {
        return Ok(None);
    }
    if let Some(permission) = &config.permission {
        if view.can(permission)? {
            return Ok(None);
        }
    }
    let one = frozen.len() == 1;
    let them = if one { "it" } else { "them" };
    let unless = match (rule.by.is_empty(), &config.permission) {
        (true, None) => String::new(),
        (true, Some(permission)) => format!("; only a caller with {permission} may change {them}"),
        (false, None) => format!(
            "; only the subject {} holds may change {them}",
            either(&rule.by)
        ),
        (false, Some(permission)) => format!(
            "; only the subject {} holds, or a caller with {permission}, may change {them}",
            either(&rule.by)
        ),
    };
    Ok(Some(veto(
        "frozen_fields",
        format!(
            "{} is {state}, which freezes {}: {} not change while it is {state}{unless}",
            row(view),
            list(&frozen, "and"),
            if one { "it does" } else { "they do" }
        ),
        &state,
        &frozen,
        (!rule.by.is_empty()).then_some(rule.by.as_slice()),
    )))
}

// status_of is the status of the Workflow the rules are of, as the run has
// it; `None` when the row has none.
fn status_of(view: &View<'_, StateRulesConfig>) -> Option<String> {
    view.get_of(&view.config().status, workflow::STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

fn veto(code: &str, reason: String, state: &str, fields: &[&str], by: Option<&[String]>) -> Veto {
    let mut details = Map::new();
    details.insert("state".into(), Value::from(state));
    details.insert(
        "fields".into(),
        Value::Array(fields.iter().map(|field| Value::from(*field)).collect()),
    );
    if let Some(by) = by {
        details.insert(
            "by".into(),
            Value::Array(by.iter().map(|field| Value::from(field.as_str())).collect()),
        );
    }
    Veto::new(reason).with_code(code).with_details(details)
}

// list joins names as a sentence does: `a`; `a and b`; `a, b and c`.
fn list(names: &[&str], conjunction: &str) -> String {
    match names {
        [] => String::new(),
        [one] => (*one).to_owned(),
        [rest @ .., last] => format!("{} {conjunction} {last}", rest.join(", ")),
    }
}

// either lists the fields a by names as a refusal says them: `reviewer`,
// `reviewer or createdBy`, `a, b or c`.
fn either(fields: &[String]) -> String {
    match fields {
        [] => String::new(),
        [one] => one.clone(),
        [rest @ .., last] => format!("{} or {last}", rest.join(", ")),
    }
}

// row names the row a refusal is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// strings reads a list of strings the configSchema let through.
fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
}

// ---- the configSchema, in ajv's words ----

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    unknown_keys("", config, &CONFIG_KEYS, &mut issues);
    if !config.contains_key("states") {
        issues.push("", "required", "must have required property 'states'");
    }
    for key in ["permission", "status"] {
        if let Some(value) = config.get(key) {
            schema::text_issues(&schema::member("", key), value, &mut issues);
        }
    }
    if let Some(states) = config.get("states") {
        states_issues(states, &mut issues);
    }
    issues
}

fn states_issues(states: &Value, issues: &mut Issues) {
    let at = "/states";
    let Some(named) = states.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    for (state, rule) in named {
        // A key that is no state's name fails the object's propertyNames,
        // which ajv reports at the object, after the name's own failures.
        let broken = schema::name_issues(state);
        if !broken.is_empty() {
            for (keyword, message) in broken {
                issues.push(at, keyword, message);
            }
            issues.push(at, "propertyNames", "property name must be valid");
        }
        rule_issues(&schema::member(at, state), rule, issues);
    }
}

fn rule_issues(at: &str, rule: &Value, issues: &mut Issues) {
    let Some(members) = rule.as_object() else {
        issues.push(at, "type", "must be object");
        return;
    };
    unknown_keys(at, members, &RULE_KEYS, issues);
    if members.is_empty() {
        issues.push(at, "minProperties", "must NOT have fewer than 1 properties");
    }
    if let Some(required) = members.get("required") {
        field_list_issues(
            &schema::member(at, "required"),
            required,
            Some(MAX_FIELDS),
            issues,
        );
    }
    if let Some(frozen) = members.get("frozen") {
        frozen_issues(&schema::member(at, "frozen"), frozen, issues);
    }
    if let Some(by) = members.get("by") {
        field_list_issues(&schema::member(at, "by"), by, None, issues);
    }
}

// frozen_issues lists what frozen breaks of its oneOf, as ajv reports it:
// nothing when exactly one branch holds (a list of fields, or "all"); else
// every branch's issues and the oneOf's own.
fn frozen_issues(at: &str, frozen: &Value, issues: &mut Issues) {
    let mut listed = Issues::default();
    field_list_issues(at, frozen, Some(MAX_FIELDS), &mut listed);
    let mut all = Issues::default();
    if frozen != &Value::from("all") {
        all.push(at, "const", "must be equal to constant");
    }
    if listed.is_empty() != all.is_empty() {
        return;
    }
    issues.extend(listed);
    issues.extend(all);
    issues.push(at, "oneOf", "must match exactly one schema in oneOf");
}

// field_list_issues lists what a list of fields breaks of `{ type: array,
// minItems: 1, maxItems, uniqueItems: true, items: { type: string,
// minLength: 1 } }`.
fn field_list_issues(at: &str, value: &Value, max: Option<usize>, issues: &mut Issues) {
    let Some(items) = value.as_array() else {
        issues.push(at, "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push(at, "minItems", "must NOT have fewer than 1 items");
    }
    if let Some(max) = max.filter(|max| items.len() > *max) {
        issues.push(
            at,
            "maxItems",
            format!("must NOT have more than {max} items"),
        );
    }
    for (i, item) in items.iter().enumerate() {
        schema::text_issues(&format!("{at}/{i}"), item, issues);
    }
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            at,
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

// unknown_keys lists the members a closed object does not take, in the
// schema loader's words.
fn unknown_keys(at: &str, members: &Map<String, Value>, known: &[&str], issues: &mut Issues) {
    for key in members.keys().filter(|key| !known.contains(&key.as_str())) {
        issues.push(
            at,
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
}
