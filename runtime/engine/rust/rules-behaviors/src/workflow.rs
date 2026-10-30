//! Workflow, the core's state machine (D16), ported onto the pure core
//! (D103) from the engine's (`../behaviors/src/workflow.rs`): a status that
//! starts in the config's initial state and changes only through
//! `transition`, along a transition the config lists, which may name a
//! permission the caller must hold. A state that no transition leaves is
//! terminal.
//!
//! A terminal state has an outcome: success, failure or neutral. The
//! config's `outcomes` names it for the terminal states it lists, and every
//! other one is a success, so a config without outcomes means what it did
//! before outcomes existed. Workflow itself reads no outcome: Dependencies,
//! Rollups and Reactions read it from another type's config, through
//! [`state_outcome`], to tell a blocker or a child that finished well from
//! one that failed.
//!
//! Workflow owns two fields: `status`, the state the row is in and the
//! value a field anchor's field is, and `enteredAt`, when the status last
//! changed, the create's time and then each transition's, in epoch
//! milliseconds (D56). A use anchored on the type holds them under the use
//! id `Workflow`; a use anchored on a field (`status: Workflow<{ ... }>`)
//! holds them under the field's name, and guards and moves only its own
//! status, so a type may carry several. Where they live is the host's
//! layout, by the rule [`Declaration`] states: on a build-time project's
//! table, a field anchor's are the field's column and one it prefixes
//! (`status`, `status_entered_at`), and a type anchor's are prefixed with
//! the behavior's storage key (`workflow_status`, `workflow_entered_at`), so
//! an own field named `status` sits beside them. A row written before
//! `enteredAt` was kept has none until its next transition.
//!
//! Nothing else moves the status. A create or an update writes own fields
//! only, and the core refuses one that names a field anchor's field, which
//! is the status; another use writes only the fields it declares (the core
//! holds it to its declaration), so it moves the status by calling
//! `transition`, whose parameter check, guard and permission gate run for a
//! call as for a caller. `transition`'s parameter is `to` alone, checked as
//! its closed paramsSchema has it, so no alias reaches the guard or the
//! handler. On a field anchor, a refusal names the field (`"packed" is not a
//! state of Order.status`, `Order o1's fulfilment is already packed`).
//!
//! A transition's change set is the row's update with the use's two owned
//! fields set, and its result is `{ from, to }`. Workflow emits no outbox
//! event of its own: the event a host records for the command, whose patch
//! carries the new status and when it was entered, is the row write's, as
//! D103's outbox trigger writes one per change, whoever makes it, and as
//! the engine wrote one per command.
//!
//! The config is checked at define as the engine checked it: first as the
//! declaration's configSchema holds it, each issue at its path in the
//! schema loader's words ([`ConfigError::Schema`]), then as a state
//! machine, in Workflow's own ([`ConfigError::Rule`]). The core runs no JSON
//! Schema, so the schema's checks are written here, held to the
//! declaration file by this module's tests. A use that gives no config is
//! checked as `{}`, whose shape the schema refuses, so the core refuses it
//! for the config it lacks. Workflow takes no create parameters: its
//! declaration has no createParamsSchema, and the core refuses an entry a
//! create gives it.
//!
//! configChange: while the type has rows, a new version keeps every state
//! of the old config, since a row may be in any of them; with none it may
//! drop any. A changed config that drops a state reads whether the type has
//! rows. Transitions, their permissions, the initial state and outcomes may
//! change; outcomes, like transitions, are read when they are needed, so a
//! new version's apply at the next read. Workflow cannot be added to or
//! removed from a type that has rows: they would have no status, or lose
//! it. The core asks about an added or a removed use only when the type has
//! rows.
//!
//! What the engine kept beside the behavior is the host's now: a row's
//! sequence, version and timestamps, the filter on the status and its
//! index (the layout's), and the guidance a describe gives, which the core
//! has no surface for.

use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use superschematic_rules::{
    Behavior, ConfigCx, ConfigError, ConfigTarget, Cx, Declaration, GuardRequest, Hook, Refusal,
    Row, SchemaIssue, Veto, View,
};

use crate::declared;
use crate::schema::{self, Issues};

/// The behavior's name, as its declaration has it.
pub const NAME: &str = "Workflow";

/// The owned field that holds the state a row is in.
pub const STATUS: &str = "status";

/// The owned field that holds when the status last changed, in epoch
/// milliseconds.
pub const ENTERED_AT: &str = "enteredAt";

/// The operation that moves the status.
pub const TRANSITION: &str = "transition";

/// Workflow's declaration, the file the compiler registers and the engine
/// embeds, read in place: what a host describes the behavior with.
pub const DECLARATION: &str = include_str!("../../behaviors/declarations/Workflow.behavior.json");

// What the configSchema holds a state's name to, in its states and in the
// keys of its outcomes.
const STATE_PATTERN: &str = "^[A-Za-z][A-Za-z0-9_-]*$";
const STATE_MAX_LENGTH: usize = 64;

// The members the configSchema's closed objects take.
const CONFIG_KEYS: [&str; 4] = ["initial", "outcomes", "states", "transitions"];
const TRANSITION_KEYS: [&str; 3] = ["from", "permission", "to"];

// The outcomes, in the order the configSchema's enum lists them.
const OUTCOMES: [&str; 3] = ["success", "failure", "neutral"];

/// One transition of a Workflow config.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WorkflowTransition {
    pub from: String,
    pub to: String,
    /// The permission a caller needs to make it; absent, anyone who may
    /// write the row can.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub permission: Option<String>,
}

/// The outcome of a terminal state: how the work it ends went.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum WorkflowOutcome {
    Success,
    Failure,
    Neutral,
}

impl WorkflowOutcome {
    /// The outcome as a config writes it.
    pub fn as_str(self) -> &'static str {
        match self {
            WorkflowOutcome::Success => "success",
            WorkflowOutcome::Failure => "failure",
            WorkflowOutcome::Neutral => "neutral",
        }
    }

    /// The outcome a config's text names; `None` for another text.
    pub fn parse(text: &str) -> Option<Self> {
        match text {
            "success" => Some(WorkflowOutcome::Success),
            "failure" => Some(WorkflowOutcome::Failure),
            "neutral" => Some(WorkflowOutcome::Neutral),
            _ => None,
        }
    }
}

/// Workflow's config, parsed: the initial state filled in, and every
/// terminal state's outcome.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WorkflowConfig {
    pub states: Vec<String>,
    pub initial: String,
    pub transitions: Vec<WorkflowTransition>,
    /// The outcome of each terminal state, in the order of `states`.
    pub outcomes: Vec<(String, WorkflowOutcome)>,
}

// What the configSchema lets through.
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RawConfig {
    states: Vec<String>,
    #[serde(default)]
    initial: Option<String>,
    transitions: Vec<WorkflowTransition>,
    #[serde(default)]
    outcomes: Option<Map<String, Value>>,
}

impl WorkflowConfig {
    /// Parses and checks a config as a type gives it: as the declaration's
    /// configSchema holds it, each issue at its path in the schema loader's
    /// words, then as a state machine, a rule's reason in the engine's
    /// words, which the core prefixes with the type and the behavior.
    pub fn parse(raw: &Value) -> Result<WorkflowConfig, ConfigError> {
        let issues = config_issues(raw);
        if !issues.is_empty() {
            return Err(ConfigError::Schema(issues.into_issues()));
        }
        let raw: RawConfig =
            serde_json::from_value(raw.clone()).map_err(|error| error.to_string())?;
        let states = raw.states;
        let has = |state: &str| states.iter().any(|known| known == state);
        let all = states.join(", ");
        let initial = raw
            .initial
            .unwrap_or_else(|| states.first().cloned().unwrap_or_default());
        if !has(&initial) {
            return Err(format!(
                "the initial state \"{initial}\" is not one of its states ({all})"
            )
            .into());
        }
        let mut seen: Vec<(&str, &str)> = Vec::new();
        for WorkflowTransition { from, to, .. } in &raw.transitions {
            for state in [from, to] {
                if !has(state) {
                    return Err(format!(
                        "the transition from \"{from}\" to \"{to}\" names \"{state}\", which is not one of its states ({all})"
                    ).into());
                }
            }
            if from == to {
                return Err(
                    format!("a transition from \"{from}\" to itself changes nothing").into(),
                );
            }
            if seen.contains(&(from.as_str(), to.as_str())) {
                return Err(
                    format!("the transition from \"{from}\" to \"{to}\" is listed twice").into(),
                );
            }
            seen.push((from, to));
        }
        let mut config = WorkflowConfig {
            states: states.clone(),
            initial,
            transitions: raw.transitions,
            outcomes: Vec::new(),
        };
        let declared = raw.outcomes.unwrap_or_default();
        for state in declared.keys() {
            if !has(state) {
                return Err(format!(
                    "outcomes names \"{state}\", which is not one of its states ({all})"
                )
                .into());
            }
            if !config.is_terminal(state) {
                return Err(format!(
                    "outcomes names \"{state}\", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome"
                ).into());
            }
        }
        // A state no transition reaches stays allowed: a new version keeps
        // every state a row may be in, including one it no longer enters.
        config.outcomes = states
            .iter()
            .filter(|state| config.is_terminal(state))
            .map(|state| {
                let named = declared
                    .get(state.as_str())
                    .and_then(Value::as_str)
                    .and_then(WorkflowOutcome::parse);
                (state.clone(), named.unwrap_or(WorkflowOutcome::Success))
            })
            .collect();
        Ok(config)
    }

    /// Whether a state is one of the config's.
    pub fn has_state(&self, state: &str) -> bool {
        self.states.iter().any(|known| known == state)
    }

    /// Whether a state is terminal: one of its states that no transition
    /// leaves. A state the config does not list is not terminal.
    pub fn is_terminal(&self, state: &str) -> bool {
        self.has_state(state)
            && !self
                .transitions
                .iter()
                .any(|transition| transition.from == state)
    }

    /// The outcome of a terminal state; `None` for any other state.
    pub fn outcome(&self, state: &str) -> Option<WorkflowOutcome> {
        self.outcomes
            .iter()
            .find(|(terminal, _)| terminal == state)
            .map(|(_, outcome)| *outcome)
    }

    /// The transitions that leave a state, in the config's order.
    pub fn moves(&self, from: &str) -> Vec<&WorkflowTransition> {
        self.transitions
            .iter()
            .filter(|transition| transition.from == from)
            .collect()
    }

    // targets lists the states a transition leads to from one.
    fn targets(&self, from: &str) -> Vec<&str> {
        self.moves(from)
            .into_iter()
            .map(|transition| transition.to.as_str())
            .collect()
    }
}

// The states and transitions of a config as a type holds it.
fn listed<'c>(config: &'c Value, key: &str) -> impl Iterator<Item = &'c Value> {
    config
        .get(key)
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
}

/// Whether a state of a Workflow config, as a type holds it, is terminal:
/// one of its states that no transition leaves (`isTerminalState`). A
/// behavior or a client asks it about any row's status. A state the config
/// does not list is not terminal.
pub fn is_terminal_state(config: &Value, state: &str) -> bool {
    listed(config, "states").any(|known| known.as_str() == Some(state))
        && !listed(config, "transitions")
            .any(|transition| transition.get("from").and_then(Value::as_str) == Some(state))
}

/// The outcome of a terminal state of a Workflow config, as a type holds
/// it (`stateOutcome`): the one its outcomes names, or success. A state
/// that is not terminal has none.
pub fn state_outcome(config: &Value, state: &str) -> Option<WorkflowOutcome> {
    if !is_terminal_state(config, state) {
        return None;
    }
    Some(
        config
            .get("outcomes")
            .and_then(|outcomes| outcomes.get(state))
            .and_then(Value::as_str)
            .and_then(WorkflowOutcome::parse)
            .unwrap_or(WorkflowOutcome::Success),
    )
}

/// The status of a row a host read, under a Workflow use's id (`Workflow`
/// on a type anchor, the field's name on a field anchor): what another
/// behavior reads of a row it does not run on. `None` when it has none.
pub fn status_of<'r>(row: &'r Row, use_id: &str) -> Option<&'r str> {
    row.owned(use_id, STATUS).and_then(Value::as_str)
}

/// When a row's status under a Workflow use's id last changed, in epoch
/// milliseconds; `None` for a row written before Workflow kept it.
pub fn entered_at_of(row: &Row, use_id: &str) -> Option<i64> {
    row.owned(use_id, ENTERED_AT).and_then(Value::as_i64)
}

/// Workflow: the state machine on a row.
#[derive(Clone, Copy, Debug, Default)]
pub struct Workflow;

impl Behavior for Workflow {
    type Config = WorkflowConfig;

    // The declaration file names the value field, the status (D103).
    fn declaration(&self) -> Declaration {
        declared::declaration(DECLARATION)
    }

    fn parse_config(
        &self,
        raw: &Value,
        _target: &ConfigTarget<'_>,
    ) -> Result<WorkflowConfig, ConfigError> {
        WorkflowConfig::parse(raw)
    }

    fn config_change(
        &self,
        cx: &ConfigCx<'_>,
        before: Option<&WorkflowConfig>,
        after: Option<&WorkflowConfig>,
    ) -> Hook<Option<String>> {
        let Some(before) = before else {
            return Ok(Some(
                "the instances that exist have no status to start from".to_owned(),
            ));
        };
        let Some(after) = after else {
            return Ok(Some("the instances would lose their status".to_owned()));
        };
        let gone: Vec<String> = before
            .states
            .iter()
            .filter(|state| !after.has_state(state))
            .map(|state| format!("\"{state}\""))
            .collect();
        // No row is in a state the new config drops.
        if gone.is_empty() || !cx.has_rows()? {
            return Ok(None);
        }
        Ok(Some(format!(
            "an instance may be in {}, which the new config drops",
            gone.join(", ")
        )))
    }

    fn check_params(
        &self,
        _config: &WorkflowConfig,
        operation: &str,
        params: &Value,
    ) -> Vec<SchemaIssue> {
        if operation != TRANSITION {
            return Vec::new();
        }
        transition_issues(params).into_issues()
    }

    fn initialize(&self, cx: &Cx<'_, WorkflowConfig>, _params: &Value) -> Hook<()> {
        cx.set(STATUS, cx.config().initial.as_str())?;
        cx.set(ENTERED_AT, cx.now())
    }

    // guard checks every transition request of its own use, a caller's or
    // another use's call(), before any handler runs: a state the config
    // lacks is a bad parameter, a move the config does not list a veto,
    // and a move whose permission the caller lacks forbidden.
    fn guard(
        &self,
        view: &View<'_, WorkflowConfig>,
        request: &GuardRequest<'_>,
    ) -> Hook<Option<Veto>> {
        let Some(params) = request.operation_of(view.use_id(), TRANSITION) else {
            return Ok(None);
        };
        let config = view.config();
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        if !config.has_state(to) {
            return Err(Refusal::operation_params(
                view.behavior(),
                view.use_id(),
                TRANSITION,
                vec![SchemaIssue::new(
                    "/to",
                    format!(
                        "\"{to}\" is not a state of {} ({})",
                        subject(view),
                        config.states.join(", ")
                    ),
                )],
            )
            .into());
        }
        let Some(from) = status(view) else {
            let missing = view.field().unwrap_or(STATUS);
            return Ok(Some(
                Veto::new(format!("{} has no {missing}", row(view))).with_code("no_status"),
            ));
        };
        let mut details = Map::new();
        details.insert("from".into(), Value::from(from.as_str()));
        details.insert("to".into(), Value::from(to));
        if from == to {
            return Ok(Some(
                Veto::new(format!("{} is already {to}", target(view)))
                    .with_code("already_in_state")
                    .with_details(details),
            ));
        }
        let Some(found) = config
            .moves(&from)
            .into_iter()
            .find(|transition| transition.to == to)
        else {
            let next = config.targets(&from);
            if next.is_empty() {
                return Ok(Some(
                    Veto::new(format!(
                        "{from} is a terminal state: no transition leaves it"
                    ))
                    .with_code("terminal_state")
                    .with_details(details),
                ));
            }
            let reason = format!(
                "no transition leads from {from} to {to}; from {from} it can move to {}",
                next.join(", ")
            );
            details.insert(
                "allowed".into(),
                Value::Array(next.into_iter().map(Value::from).collect()),
            );
            return Ok(Some(
                Veto::new(reason)
                    .with_code("transition_not_allowed")
                    .with_details(details),
            ));
        };
        if let Some(permission) = &found.permission {
            if !view.can(permission)? {
                return Err(view.forbidden(format!(
                    "{} may not move {} from {from} to {to}: the transition needs permission {permission}",
                    view.caller().subject,
                    target(view)
                )));
            }
        }
        Ok(None)
    }

    fn operation(&self, cx: &Cx<'_, WorkflowConfig>, name: &str, params: &Value) -> Hook<Value> {
        if name != TRANSITION {
            return Err(cx.defect(format!("it declares no operation {name}")));
        }
        let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
        let from = status(cx).unwrap_or_else(|| "null".to_owned());
        if !cx
            .config()
            .moves(&from)
            .iter()
            .any(|transition| transition.to == to)
        {
            return Err(cx.defect(format!(
                "transition from {from} to {to} reached its handler without its guard"
            )));
        }
        cx.set(STATUS, to)?;
        cx.set(ENTERED_AT, cx.now())?;
        Ok(json!({ "from": from, "to": to }))
    }
}

// status is the use's status as the run has it; `None` when it has none.
fn status<C>(view: &View<'_, C>) -> Option<String> {
    view.get(STATUS)
        .and_then(|status| status.as_str().map(str::to_owned))
}

// row names the row a refusal is about: its type and its key.
fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// target names what a refusal is about: the row, and on a field anchor the
// field (`Order o1's fulfilment`).
fn target<C>(view: &View<'_, C>) -> String {
    match view.field() {
        Some(field) => format!("{}'s {field}", row(view)),
        None => row(view),
    }
}

// subject names whose states a refusal lists: the type, and on a field
// anchor the field (`Order.fulfilment`).
fn subject<C>(view: &View<'_, C>) -> String {
    match view.field() {
        Some(field) => format!("{}.{field}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

// config_issues lists what a config breaks of the declaration's
// configSchema, in the schema loader's words.
fn config_issues(raw: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(config) = raw.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in config
        .keys()
        .filter(|key| !CONFIG_KEYS.contains(&key.as_str()))
    {
        issues.push(
            "",
            "additionalProperties",
            format!("unknown key {}", schema::quoted(key)),
        );
    }
    for key in ["states", "transitions"] {
        if !config.contains_key(key) {
            issues.push(
                "",
                "required",
                format!("must have required property '{key}'"),
            );
        }
    }
    if config
        .get("initial")
        .is_some_and(|initial| !initial.is_string())
    {
        issues.push("/initial", "type", "must be string");
    }
    if let Some(states) = config.get("states") {
        state_issues(states, &mut issues);
    }
    if let Some(transitions) = config.get("transitions") {
        transition_list_issues(transitions, &mut issues);
    }
    if let Some(outcomes) = config.get("outcomes") {
        outcome_issues(outcomes, &mut issues);
    }
    issues
}

fn state_issues(states: &Value, issues: &mut Issues) {
    let Some(items) = states.as_array() else {
        issues.push("/states", "type", "must be array");
        return;
    };
    if items.is_empty() {
        issues.push("/states", "minItems", "must NOT have fewer than 1 items");
    }
    for (i, item) in items.iter().enumerate() {
        let at = format!("/states/{i}");
        match item.as_str() {
            None => issues.push(at, "type", "must be string"),
            Some(name) => {
                for (keyword, message) in name_issues(name) {
                    issues.push(at.clone(), keyword, message);
                }
            }
        }
    }
    if let Some((j, i)) = schema::duplicate(items) {
        issues.push(
            "/states",
            "uniqueItems",
            format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
        );
    }
}

fn transition_list_issues(transitions: &Value, issues: &mut Issues) {
    let Some(items) = transitions.as_array() else {
        issues.push("/transitions", "type", "must be array");
        return;
    };
    for (i, item) in items.iter().enumerate() {
        let at = format!("/transitions/{i}");
        let Some(transition) = item.as_object() else {
            issues.push(at, "type", "must be object");
            continue;
        };
        for key in transition
            .keys()
            .filter(|key| !TRANSITION_KEYS.contains(&key.as_str()))
        {
            issues.push(
                at.clone(),
                "additionalProperties",
                format!("unknown key {}", schema::quoted(key)),
            );
        }
        for key in ["from", "to"] {
            match transition.get(key) {
                None => issues.push(
                    at.clone(),
                    "required",
                    format!("must have required property '{key}'"),
                ),
                Some(value) if !value.is_string() => {
                    issues.push(schema::member(&at, key), "type", "must be string");
                }
                Some(_) => {}
            }
        }
        match transition.get("permission") {
            Some(Value::String(permission)) if schema::length(permission) < 1 => issues.push(
                schema::member(&at, "permission"),
                "minLength",
                "must NOT have fewer than 1 characters",
            ),
            Some(Value::String(_)) | None => {}
            Some(_) => issues.push(schema::member(&at, "permission"), "type", "must be string"),
        }
    }
}

fn outcome_issues(outcomes: &Value, issues: &mut Issues) {
    let Some(named) = outcomes.as_object() else {
        issues.push("/outcomes", "type", "must be object");
        return;
    };
    if named.is_empty() {
        issues.push(
            "/outcomes",
            "minProperties",
            "must NOT have fewer than 1 properties",
        );
    }
    for (state, outcome) in named {
        // A key that is no state's name fails the object's propertyNames,
        // which ajv reports at the object, after the name's own failures.
        let broken = name_issues(state);
        if !broken.is_empty() {
            for (keyword, message) in broken {
                issues.push("/outcomes", keyword, message);
            }
            issues.push("/outcomes", "propertyNames", "property name must be valid");
        }
        if !outcome
            .as_str()
            .is_some_and(|text| OUTCOMES.contains(&text))
        {
            issues.push(
                schema::member("/outcomes", state),
                "enum",
                format!("must be one of {}", OUTCOMES.map(schema::quoted).join(", ")),
            );
        }
    }
}

// name_issues lists what a state's name breaks of the configSchema: its
// length and its pattern, each a keyword of its own.
fn name_issues(name: &str) -> Vec<(&'static str, String)> {
    let mut broken = Vec::new();
    if schema::length(name) > STATE_MAX_LENGTH {
        broken.push((
            "maxLength",
            format!("must NOT have more than {STATE_MAX_LENGTH} characters"),
        ));
    }
    if !is_state_name(name) {
        broken.push(("pattern", format!("must match pattern \"{STATE_PATTERN}\"")));
    }
    broken
}

// is_state_name matches STATE_PATTERN: a letter, then letters, digits,
// underscores and hyphens, ASCII alone.
fn is_state_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars
        .next()
        .is_some_and(|first| first.is_ascii_alphabetic())
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
}

// transition_issues lists what transition's parameters break of its
// closed paramsSchema, in the behavior registry's words.
fn transition_issues(params: &Value) -> Issues {
    let mut issues = Issues::default();
    let Some(given) = params.as_object() else {
        issues.push("", "type", "must be object");
        return issues;
    };
    for key in given.keys().filter(|key| key.as_str() != "to") {
        issues.push(
            "",
            "additionalProperties",
            format!("must NOT have additional properties: {key}"),
        );
    }
    match given.get("to") {
        None => issues.push("", "required", "must have required property 'to'"),
        Some(Value::String(to)) if schema::length(to) < 1 => {
            issues.push("/to", "minLength", "must NOT have fewer than 1 characters");
        }
        Some(Value::String(_)) => {}
        Some(_) => issues.push("/to", "type", "must be string"),
    }
    issues
}

#[cfg(test)]
mod tests {
    use super::*;

    fn order_flow() -> Value {
        json!({
            "states": ["draft", "open", "shipped", "cancelled"],
            "transitions": [
                {"from": "draft", "to": "open"},
                {"from": "draft", "to": "cancelled"},
                {"from": "open", "to": "shipped", "permission": "orders.ship"},
                {"from": "open", "to": "cancelled"}
            ]
        })
    }

    fn file() -> Value {
        serde_json::from_str(DECLARATION).expect("the declaration file parses")
    }

    // undescribed is a schema with every description left out, at any
    // depth: what the checks implement, and nothing else.
    fn undescribed(schema: &Value) -> Value {
        match schema {
            Value::Object(members) => Value::Object(
                members
                    .iter()
                    .filter(|(key, _)| key.as_str() != "description")
                    .map(|(key, member)| (key.clone(), undescribed(member)))
                    .collect(),
            ),
            Value::Array(items) => Value::Array(items.iter().map(undescribed).collect()),
            other => other.clone(),
        }
    }

    #[test]
    fn a_terminal_state_is_one_no_transition_leaves() {
        let flow = order_flow();
        assert!(is_terminal_state(&flow, "shipped"));
        assert!(is_terminal_state(&flow, "cancelled"));
        assert!(!is_terminal_state(&flow, "open"));
        assert!(!is_terminal_state(&flow, "lost"));
        let config = WorkflowConfig::parse(&flow).expect("a state machine");
        assert!(config.is_terminal("shipped"));
        assert!(!config.is_terminal("draft"));
        assert!(!config.is_terminal("lost"));
    }

    #[test]
    fn a_terminal_state_has_the_outcome_outcomes_names_or_success() {
        let mut flow = order_flow();
        flow["outcomes"] = json!({"cancelled": "failure"});
        assert_eq!(
            state_outcome(&flow, "cancelled"),
            Some(WorkflowOutcome::Failure)
        );
        assert_eq!(
            state_outcome(&flow, "shipped"),
            Some(WorkflowOutcome::Success)
        );
        assert_eq!(state_outcome(&flow, "open"), None);
        assert_eq!(state_outcome(&flow, "lost"), None);
        // A config without outcomes means what it did before them.
        assert_eq!(
            state_outcome(&order_flow(), "cancelled"),
            Some(WorkflowOutcome::Success)
        );
        // A state named like an Object member is a state like any other.
        let named = json!({"states": ["open", "constructor"], "transitions": [{"from": "open", "to": "constructor"}], "outcomes": {}});
        assert_eq!(
            state_outcome(&named, "constructor"),
            Some(WorkflowOutcome::Success)
        );
        // The parsed config agrees, for every terminal state.
        let config = WorkflowConfig::parse(&flow).expect("a state machine");
        assert_eq!(
            config.outcomes,
            vec![
                ("shipped".to_owned(), WorkflowOutcome::Success),
                ("cancelled".to_owned(), WorkflowOutcome::Failure)
            ]
        );
        assert_eq!(config.outcome("open"), None);
    }

    #[test]
    fn a_parsed_config_fills_in_its_initial_state_and_lists_moves() {
        let config = WorkflowConfig::parse(&order_flow()).expect("a state machine");
        assert_eq!(config.initial, "draft");
        assert_eq!(config.targets("open"), vec!["shipped", "cancelled"]);
        assert_eq!(
            config.moves("open")[0].permission.as_deref(),
            Some("orders.ship")
        );
        assert!(config.moves("shipped").is_empty());
        let mut later = order_flow();
        later["initial"] = json!("open");
        assert_eq!(
            WorkflowConfig::parse(&later)
                .expect("a state machine")
                .initial,
            "open"
        );
    }

    #[test]
    fn an_outcome_reads_and_writes_as_a_config_does() {
        for outcome in [
            WorkflowOutcome::Success,
            WorkflowOutcome::Failure,
            WorkflowOutcome::Neutral,
        ] {
            assert_eq!(WorkflowOutcome::parse(outcome.as_str()), Some(outcome));
            assert_eq!(
                serde_json::to_value(outcome).expect("an outcome"),
                outcome.as_str()
            );
        }
        assert_eq!(WorkflowOutcome::parse("aborted"), None);
    }

    #[test]
    fn a_state_name_matches_the_pattern() {
        for name in ["draft", "in-review", "step_2", "A"] {
            assert!(is_state_name(name), "{name}");
        }
        for name in ["", "in review", "2nd", "_x", "é"] {
            assert!(!is_state_name(name), "{name}");
        }
    }

    /// The checks this module writes by hand are the declaration's
    /// configSchema and paramsSchema, keyword for keyword: each schema, its
    /// descriptions left out, is exactly the one they implement, so a
    /// keyword the file gains fails this test until the checks take it.
    #[test]
    fn the_checks_are_the_declaration_files_schemas() {
        let file = file();
        let name = json!({"maxLength": STATE_MAX_LENGTH, "pattern": STATE_PATTERN});
        let mut state = name.clone();
        state["type"] = json!("string");
        assert_eq!(
            undescribed(&file["configSchema"]),
            json!({
                "additionalProperties": false,
                "properties": {
                    "initial": {"type": "string"},
                    "outcomes": {
                        "additionalProperties": {"enum": OUTCOMES},
                        "minProperties": 1,
                        "propertyNames": name,
                        "type": "object"
                    },
                    "states": {
                        "items": state,
                        "minItems": 1,
                        "type": "array",
                        "uniqueItems": true
                    },
                    "transitions": {
                        "items": {
                            "additionalProperties": false,
                            "properties": {
                                "from": {"type": "string"},
                                "permission": {"minLength": 1, "type": "string"},
                                "to": {"type": "string"}
                            },
                            "required": ["from", "to"],
                            "type": "object"
                        },
                        "type": "array"
                    }
                },
                "required": ["states", "transitions"],
                "type": "object"
            })
        );
        let mut properties: Vec<&String> = file["configSchema"]["properties"]
            .as_object()
            .expect("properties")
            .keys()
            .collect();
        properties.sort();
        assert_eq!(properties, CONFIG_KEYS);
        let transition = &file["configSchema"]["properties"]["transitions"]["items"];
        let mut members: Vec<&String> = transition["properties"]
            .as_object()
            .expect("properties")
            .keys()
            .collect();
        members.sort();
        assert_eq!(members, TRANSITION_KEYS);
        let operations = file["operations"].as_array().expect("operations");
        assert_eq!(operations.len(), 1);
        assert_eq!(operations[0]["name"], json!(TRANSITION));
        assert_eq!(
            undescribed(&operations[0]["paramsSchema"]),
            json!({
                "additionalProperties": false,
                "properties": {"to": {"minLength": 1, "type": "string"}},
                "required": ["to"],
                "type": "object"
            })
        );
        assert_eq!(file.get("createParamsSchema"), None);
    }

    #[test]
    fn the_declaration_is_the_files() {
        assert_eq!(
            Workflow.declaration(),
            Declaration::new(NAME)
                .value(STATUS)
                .field(ENTERED_AT)
                .operation(TRANSITION, true)
                .veto("already_in_state")
                .veto("terminal_state")
                .veto("transition_not_allowed")
                .veto("no_status")
        );
    }

    #[test]
    fn transitions_parameters_are_held_to_its_paramsschema() {
        // The issues as the core lists them: `<pointer>: <message>`, joined.
        let reason = |params: Value| {
            ConfigError::Schema(transition_issues(&params).into_issues()).to_string()
        };
        assert_eq!(
            reason(json!({"status": "open"})),
            "must NOT have additional properties: status; must have required property 'to'"
        );
        assert_eq!(
            reason(json!({"to": ""})),
            "/to: must NOT have fewer than 1 characters"
        );
        assert_eq!(reason(json!({"to": 5})), "/to: must be string");
        assert_eq!(reason(json!("open")), "must be object");
        assert_eq!(reason(json!({"to": "open"})), "");
    }

    #[test]
    fn a_status_is_read_from_a_row_by_use_id() {
        let mut row = Row {
            version: 3,
            ..Row::default()
        };
        row.owned.insert(
            "fulfilment".into(),
            json!({"status": "packed", "enteredAt": 1_000_000})
                .as_object()
                .cloned()
                .expect("an object"),
        );
        assert_eq!(status_of(&row, "fulfilment"), Some("packed"));
        assert_eq!(entered_at_of(&row, "fulfilment"), Some(1_000_000));
        assert_eq!(status_of(&row, NAME), None);
        assert_eq!(entered_at_of(&row, NAME), None);
    }
}
