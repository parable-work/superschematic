//! The status moves the work-queue behaviors make through Workflow (D16):
//! the config rule each holds to the type's Workflow (`checkMove`, which
//! Lease's onExpiry and escalate and Presence's onMissed and onBeat
//! share, with the same messages), the instance's status as the caller
//! reads it, and the move itself, a `call()` of Workflow's transition
//! whose guards run, a veto leaving the status as it is; and the JSON
//! types of a field a config names, which their checks read.

use serde_json::{json, Value};

use superschematic_engine::behaviors::InstanceView;
use superschematic_engine::{ConfigTarget, Error, InstanceCx, Result};

/// A move of the status a config makes: to `transition`, from one of
/// `from`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StatusMove {
    pub transition: String,
    pub from: Vec<String>,
}

impl StatusMove {
    /// A move as the configSchema lets it through: `{ transition, from }`.
    pub(crate) fn parse(raw: &Value) -> StatusMove {
        StatusMove {
            transition: raw
                .get("transition")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            from: strings(raw.get("from")),
        }
    }
}

/// A Workflow config as the schema holds it: its states and transitions.
pub(crate) struct Flow {
    pub(crate) states: Vec<String>,
    pub(crate) transitions: Vec<(String, String)>,
}

// strings reads a list of strings the configSchema let through.
pub(crate) fn strings(value: Option<&Value>) -> Vec<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
}

/// The type's Workflow config, when its states and transitions are lists:
/// one of another shape is Workflow's own checks' to refuse, and is
/// reported there.
pub(crate) fn flow_of(target: &ConfigTarget<'_>) -> Option<Flow> {
    let raw = target.configs().get("Workflow")?;
    let states = raw.get("states")?.as_array()?;
    let transitions = raw.get("transitions")?.as_array()?;
    Some(Flow {
        states: states
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect(),
        transitions: transitions
            .iter()
            .filter_map(|edge| {
                Some((
                    edge.get("from")?.as_str()?.to_owned(),
                    edge.get("to")?.as_str()?.to_owned(),
                ))
            })
            .collect(),
    })
}

/// Holds a status move of the config to the type's Workflow (`checkMove`):
/// every state one of its states, and a transition from each from state to
/// the target.
pub(crate) fn check_move(flow: &Flow, at: &str, rule: &StatusMove) -> Result<()> {
    let states = flow.states.join(", ");
    let to = rule.transition.as_str();
    if !flow.states.iter().any(|state| state == to) {
        return Err(Error::config(format!(
            "{at}.transition \"{to}\" is not a state of the type's Workflow ({states})"
        )));
    }
    for from in &rule.from {
        if !flow.states.contains(from) {
            return Err(Error::config(format!(
                "{at}.from names \"{from}\", which is not a state of the type's Workflow ({states})"
            )));
        }
        if from == to {
            return Err(Error::config(format!(
                "{at} moves the status from \"{from}\" to itself"
            )));
        }
        if !flow
            .transitions
            .iter()
            .any(|(source, target)| source == from && target == to)
        {
            return Err(Error::config(format!(
                "{at}: no transition of the type's Workflow leads from \"{from}\" to \"{to}\""
            )));
        }
    }
    Ok(())
}

/// Holds the config's status moves to the type: each one needs Workflow on
/// it, and its states and transitions.
pub(crate) fn check_moves(
    target: &ConfigTarget<'_>,
    rules: &[(&str, Option<&StatusMove>)],
) -> Result<()> {
    let flow = flow_of(target);
    for (at, rule) in rules {
        let Some(rule) = rule else {
            continue;
        };
        if !target.behaviors().iter().any(|name| name == "Workflow") {
            return Err(Error::config(format!(
                "{at} moves the status through Workflow, which the type does not list"
            )));
        }
        if let Some(flow) = &flow {
            check_move(flow, at, rule)?;
        }
    }
    Ok(())
}

/// The instance's Workflow status, read as the caller; `None` without one.
pub(crate) fn status_of<C>(view: &InstanceView<'_, C>) -> Result<Option<String>> {
    let record = view
        .instances()
        .get(view.schema(), view.id(), Some(&["Workflow.status"]))?;
    Ok(record
        .as_ref()
        .and_then(|record| record.behavior_field("Workflow", "status"))
        .and_then(Value::as_str)
        .map(str::to_owned))
}

/// Moves the status through Workflow's transition, as the caller. A
/// guard's veto leaves it as it is; anything else, a permission the caller
/// lacks say, fails the call.
pub(crate) fn transition<C>(cx: &InstanceCx<'_, C>, to: &str) -> Result<()> {
    match cx.call("Workflow", "transition", &json!({ "to": to })) {
        Ok(_) => Ok(()),
        Err(error) if error.is_veto() => Ok(()),
        Err(error) => Err(error),
    }
}

/// Moves the status with a rule when it is one of the rule's from states.
pub(crate) fn apply<C>(
    cx: &InstanceCx<'_, C>,
    rule: Option<&StatusMove>,
    status: Option<&str>,
) -> Result<()> {
    match (rule, status) {
        (Some(rule), Some(status)) if rule.from.iter().any(|from| from == status) => {
            transition(cx, &rule.transition)
        }
        _ => Ok(()),
    }
}

/// The JSON types a field's JSON Schema takes, as the TypeScript checks
/// read `fieldSchemas[field].type`: a list, or the one type (`null` for
/// none).
pub(crate) fn field_types(target: &ConfigTarget<'_>, field: &str) -> Vec<Value> {
    match target
        .field_schemas()
        .get(field)
        .and_then(|schema| schema.get("type"))
    {
        Some(Value::Array(types)) => types.clone(),
        Some(one) => vec![one.clone()],
        None => vec![Value::Null],
    }
}

/// Whether a field's JSON Schema takes `kind` and nothing but it and null.
pub(crate) fn only_types(target: &ConfigTarget<'_>, field: &str, kind: &str) -> bool {
    let types = field_types(target, field);
    types.iter().any(|one| one.as_str() == Some(kind))
        && types
            .iter()
            .all(|one| matches!(one.as_str(), Some(name) if name == kind || name == "null"))
}
