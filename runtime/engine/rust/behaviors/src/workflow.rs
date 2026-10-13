//! Workflow, the core's state machine (D16), the port of
//! `src/behaviors/core/workflow.ts`: a status that starts in the config's
//! initial state and changes only through `transition`, along a transition
//! the config lists, which may name a permission the caller must hold. A
//! state that no transition leaves is terminal.
//!
//! A terminal state has an outcome: success, failure or neutral. The
//! config's `outcomes` names it for the terminal states it lists, and every
//! other one is a success, so a config without outcomes means what it did
//! before outcomes existed. Workflow itself reads no outcome: Dependencies,
//! Rollups and Reactions read it from another schema's config, through
//! [`state_outcome`], to tell a blocker or a child that finished well from
//! one that failed.
//!
//! A read returns the status as `Workflow.status`, apart from the type's own
//! fields. A list filters on it (`where: { "Workflow.status": "doing" }`),
//! which Workflow's column holds and an index of its own serves.
//! `Workflow.enteredAt` is when the status last changed, the create's time
//! and then each transition's, in epoch milliseconds (D56). An instance
//! created before the column existed has none until its next transition,
//! since a behavior's migration reaches only its own storage, which never
//! held the time.
//!
//! Nothing else can move the status. It is Workflow's own column: a create
//! or an update writes own fields only, and another behavior has no handle
//! on the column (D16: a behavior writes only its own storage and reaches
//! another through its operations). `transition`'s parameter is `to` alone,
//! and its paramsSchema is closed, so no alias reaches the handler past the
//! guard, which checks every transition request, a caller's or another
//! behavior's `call()`, before any handler runs.
//!
//! configChange: while the schema has instances, a new version keeps every
//! state of the old config, since an instance may be in any of them; with
//! none it may drop any. Transitions, their permissions, the initial state
//! and outcomes may change; outcomes, like transitions, are read when they
//! are needed, so a new version's apply at the next read. Workflow cannot
//! be added to or removed from a schema that has instances: they would have
//! no status, or lose it.

use serde::Deserialize;
use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{
    BehaviorFilter, BehaviorMigration, ColumnSpec, ConfigChange, InstanceView,
};
use superschematic_engine::errors::{SchemaIssue, Veto};
use superschematic_engine::json::js_keys;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    json_config, Behavior, ConfigTarget, EngineError, EngineErrorCode, Error, GuardRequest, Hooks,
    Result,
};

const NAME: &str = "Workflow";

/// One transition of a Workflow config.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WorkflowTransition {
    pub from: String,
    pub to: String,
    /// The permission a caller needs to make it; absent, anyone who may
    /// write the instance can.
    #[serde(default)]
    pub permission: Option<String>,
}

/// The outcome of a terminal state: how the work it ends went.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
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

impl WorkflowConfig {
    /// Whether a state is terminal: one of its states that no transition
    /// leaves. A state the config does not list is not terminal.
    pub fn is_terminal(&self, state: &str) -> bool {
        self.states.iter().any(|known| known == state)
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

// The states and transitions of a config as a schema holds it.
fn listed<'c>(config: &'c Value, key: &str) -> impl Iterator<Item = &'c Value> {
    config
        .get(key)
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
}

/// Whether a state of a Workflow config, as a schema holds it
/// (`schemas().config(schema, "Workflow")` returns it), is terminal: one of
/// its states that no transition leaves (`isTerminalState`). A behavior or
/// a client asks it about any instance's status. A state the config does
/// not list is not terminal.
pub fn is_terminal_state(config: &Value, state: &str) -> bool {
    listed(config, "states").any(|known| known.as_str() == Some(state))
        && !listed(config, "transitions")
            .any(|transition| transition.get("from").and_then(Value::as_str) == Some(state))
}

/// The outcome of a terminal state of a Workflow config, as a schema holds
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

// What the configSchema lets through.
#[derive(Deserialize)]
struct RawConfig {
    states: Vec<String>,
    #[serde(default)]
    initial: Option<String>,
    transitions: Vec<WorkflowTransition>,
    #[serde(default)]
    outcomes: Option<Map<String, Value>>,
}

/// Workflow: the state machine on an instance.
#[derive(Clone, Copy, Debug, Default)]
pub struct Workflow;

impl Behavior for Workflow {
    type Config = WorkflowConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/Workflow.behavior.json")
    }

    // The configSchema holds the shape; this holds the states together.
    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<WorkflowConfig> {
        let raw: RawConfig = json_config(raw)?;
        let states = raw.states;
        let has = |state: &str| states.iter().any(|known| known == state);
        let all = states.join(", ");
        let initial = raw
            .initial
            .unwrap_or_else(|| states.first().cloned().unwrap_or_default());
        if !has(&initial) {
            return Err(Error::config(format!(
                "the initial state \"{initial}\" is not one of its states ({all})"
            )));
        }
        let mut seen: Vec<(&str, &str)> = Vec::new();
        for WorkflowTransition { from, to, .. } in &raw.transitions {
            for state in [from, to] {
                if !has(state) {
                    return Err(Error::config(format!(
                        "the transition from \"{from}\" to \"{to}\" names \"{state}\", which is not one of its states ({all})"
                    )));
                }
            }
            if from == to {
                return Err(Error::config(format!(
                    "a transition from \"{from}\" to itself changes nothing"
                )));
            }
            if seen.contains(&(from, to)) {
                return Err(Error::config(format!(
                    "the transition from \"{from}\" to \"{to}\" is listed twice"
                )));
            }
            seen.push((from, to));
        }
        let mut config = WorkflowConfig {
            states: states.clone(),
            initial,
            transitions: raw.transitions.clone(),
            outcomes: Vec::new(),
        };
        let declared = raw.outcomes.unwrap_or_default();
        for state in js_keys(&declared) {
            if !has(state) {
                return Err(Error::config(format!(
                    "outcomes names \"{state}\", which is not one of its states ({all})"
                )));
            }
            if !config.is_terminal(state) {
                return Err(Error::config(format!(
                    "outcomes names \"{state}\", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome"
                )));
            }
        }
        // A state no transition reaches stays allowed: a new version keeps
        // every state an instance may be in, including one it no longer
        // enters.
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

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(config_change),
            guidance: Some(|_, config, target| crate::guidance::workflow::guidance(config, target)),
            // The index lets a list that filters on status read only the
            // instances in the states it names, in creation order.
            migrations: vec![
                BehaviorMigration::new(1, "status").column("status", ColumnSpec::text()),
                BehaviorMigration::new(2, "status index").index("status", &["status"]),
                BehaviorMigration::new(3, "entered at").column("entered_at", ColumnSpec::integer()),
            ],
            filters: vec![("status", BehaviorFilter::new("status", "string"))],
            initialize: Some(|_, cx, _| {
                cx.columns().set(&[
                    ("status", SqlValue::from(cx.config().initial.as_str())),
                    ("entered_at", SqlValue::Integer(cx.now())),
                ])
            }),
            guard: Some(guard),
            operations: vec![("transition", |_, cx, params| {
                let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
                let from = match cx.columns().get()?.get("status") {
                    Some(SqlValue::Text(status)) => status.clone(),
                    _ => "null".to_owned(),
                };
                if !cx
                    .config()
                    .moves(&from)
                    .iter()
                    .any(|transition| transition.to == to)
                {
                    return Err(Error::behavior(
                        NAME,
                        format!(
                            "transition from {from} to {to} reached its handler without its guard"
                        ),
                    ));
                }
                cx.columns().set(&[
                    ("status", SqlValue::from(to)),
                    ("entered_at", SqlValue::Integer(cx.now())),
                ])?;
                Ok(json!({ "from": from, "to": to }))
            })],
            fields: vec![
                ("status", |_, view| {
                    Ok(view.columns().get()?.opt_text("status")?.map(Value::from))
                }),
                ("enteredAt", |_, view| {
                    Ok(view
                        .columns()
                        .get()?
                        .opt_i64("entered_at")?
                        .map(Value::from))
                }),
            ],
            ..Hooks::default()
        }
    }
}

fn config_change(
    _: &Workflow,
    before: Option<&WorkflowConfig>,
    after: Option<&WorkflowConfig>,
    change: &ConfigChange<'_>,
) -> Result<Option<String>> {
    let Some(before) = before else {
        return Ok(Some(
            "the instances that exist have no status to start from".to_owned(),
        ));
    };
    let Some(after) = after else {
        return Ok(Some("the instances would lose their status".to_owned()));
    };
    // No instance is in a state the new config drops.
    if !change.instances()? {
        return Ok(None);
    }
    let gone: Vec<String> = before
        .states
        .iter()
        .filter(|state| !after.states.contains(state))
        .map(|state| format!("\"{state}\""))
        .collect();
    Ok((!gone.is_empty()).then(|| {
        format!(
            "an instance may be in {}, which the new config drops",
            gone.join(", ")
        )
    }))
}

// guard checks every transition request, a caller's or another behavior's
// call(), before any handler runs: a state the config lacks is a bad
// parameter, a move the config does not list a veto, and a move whose
// permission the caller lacks forbidden.
fn guard(
    _: &Workflow,
    view: &InstanceView<'_, WorkflowConfig>,
    request: &GuardRequest<'_>,
) -> Result<Option<Veto>> {
    let GuardRequest::Operation {
        behavior: NAME,
        operation: "transition",
        params,
        ..
    } = request
    else {
        return Ok(None);
    };
    let config = view.config();
    let to = params.get("to").and_then(Value::as_str).unwrap_or_default();
    if !config.states.iter().any(|state| state == to) {
        return Err(EngineError::operation_params(
            NAME,
            "transition",
            vec![SchemaIssue::new(
                "/to",
                format!(
                    "\"{to}\" is not a state of {} ({})",
                    view.schema(),
                    config.states.join(", ")
                ),
            )],
        )
        .into());
    }
    let columns = view.columns().get()?;
    let Some(SqlValue::Text(from)) = columns.get("status") else {
        return Ok(Some(veto(
            format!("{} {} has no status", view.schema(), view.id()),
            "no_status",
            None,
        )));
    };
    let mut details = Map::new();
    details.insert("from".into(), Value::from(from.as_str()));
    details.insert("to".into(), Value::from(to));
    if from == to {
        return Ok(Some(veto(
            format!("{} {} is already {to}", view.schema(), view.id()),
            "already_in_state",
            Some(details),
        )));
    }
    let Some(found) = config
        .moves(from)
        .into_iter()
        .find(|transition| transition.to == to)
    else {
        let next = config.targets(from);
        if next.is_empty() {
            return Ok(Some(veto(
                format!("{from} is a terminal state: no transition leaves it"),
                "terminal_state",
                Some(details),
            )));
        }
        let reason = format!(
            "no transition leads from {from} to {to}; from {from} it can move to {}",
            next.join(", ")
        );
        details.insert(
            "allowed".into(),
            Value::Array(next.into_iter().map(Value::from).collect()),
        );
        return Ok(Some(veto(reason, "transition_not_allowed", Some(details))));
    };
    if let Some(permission) = &found.permission {
        if !view.can(permission)? {
            return Err(EngineError::new(
                EngineErrorCode::Forbidden,
                format!(
                    "{} may not move {} {} from {from} to {to}: the transition needs permission {permission}",
                    view.principal().subject,
                    view.schema(),
                    view.id()
                ),
            )
            .into());
        }
    }
    Ok(None)
}

fn veto(reason: String, code: &str, details: Option<Map<String, Value>>) -> Veto {
    Veto {
        reason,
        code: Some(code.to_owned()),
        details,
    }
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

    #[test]
    fn a_terminal_state_is_one_no_transition_leaves() {
        let flow = order_flow();
        assert!(is_terminal_state(&flow, "shipped"));
        assert!(is_terminal_state(&flow, "cancelled"));
        assert!(!is_terminal_state(&flow, "open"));
        assert!(!is_terminal_state(&flow, "lost"));
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
    }
}
