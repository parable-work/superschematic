//! The status moves the work-queue behaviors make through Workflow (D16),
//! ported from the engine's (`runtime/engine-workqueue/rust/src/moves.rs`):
//! the config rule each holds to the type's Workflow (`checkMove`, which
//! Lease's onExpiry and escalate, Presence's onMissed and onBeat, Budget's
//! escalate and Retries' then share, with the engine's messages), and the
//! move itself, a `call()` of Workflow's transition whose guards run, a
//! veto leaving the status as it is.
//!
//! Which Workflow a config's moves read is the config's `status`, the id of
//! a Workflow use of the type, or the type's one Workflow when it names none
//! (D104), as [`crate::flow`] resolves it for StateRules and Approvals; a
//! type with no Workflow has none to move, which a config with a move is
//! refused for. The engine moved the instance's one `Workflow`.

use serde_json::{json, Value};
use superschematic_rules::{Code, ConfigError, ConfigTarget, Cx, Hook};

use crate::flow::{flow_of, leads, Flow};
use crate::workflow;

/// A move of the status a config makes (D16): to `transition`, from one of
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

    /// Whether the move moves the status from a state: one of its from.
    pub fn moves_from(&self, status: &str) -> bool {
        self.from.iter().any(|from| from == status)
    }
}

/// The Workflow a config's moves read (D104): the use `named` names (in
/// flow.rs's words when none has the id), else the type's one; `Ok(None)`
/// for a type with no Workflow and no `named`; several and no `named` are
/// refused in flow.rs's words.
pub(crate) fn flow_for(
    target: &ConfigTarget<'_>,
    named: Option<&str>,
) -> Result<Option<Flow>, ConfigError> {
    let any = target
        .uses()
        .iter()
        .any(|entry| entry.behavior == workflow::NAME);
    if named.is_none() && !any {
        return Ok(None);
    }
    flow_of(target, named).map(Some)
}

/// Holds a move of the config to the Workflow (`checkMove`), in the
/// engine's words: its transition a state, each from state a state, not the
/// transition itself, and a transition of the Workflow from each to it. A
/// Workflow config of a shape Workflow refuses reads as no states, and the
/// check passes: Workflow refuses it.
pub(crate) fn check_move(flow: &Flow, at: &str, rule: &StatusMove) -> Result<(), ConfigError> {
    if flow.states.is_empty() {
        return Ok(());
    }
    let states = flow.listed();
    let to = rule.transition.as_str();
    if !flow.has_state(to) {
        return Err(format!(
            "{at}.transition \"{to}\" is not a state of the type's Workflow ({states})"
        )
        .into());
    }
    for from in &rule.from {
        if !flow.has_state(from) {
            return Err(format!(
                "{at}.from names \"{from}\", which is not a state of the type's Workflow ({states})"
            )
            .into());
        }
        if from == to {
            return Err(format!("{at} moves the status from \"{from}\" to itself").into());
        }
        if !leads(&flow.transitions, from, to) {
            return Err(format!(
                "{at}: no transition of the type's Workflow leads from \"{from}\" to \"{to}\""
            )
            .into());
        }
    }
    Ok(())
}

/// A move given on a type with no Workflow: the engine's words, which name
/// Workflow on a table too.
pub(crate) fn no_workflow(at: &str) -> ConfigError {
    format!("{at} moves the status through Workflow, which the type does not list").into()
}

/// Moves the status through the Workflow use's transition (`Cx::call`, so
/// every use's guards run) when `status` is one of the rule's from states:
/// `Ok(true)` moved, `Ok(false)` not one of them, or a guard's veto, which
/// leaves the status as it is (the refused call is undone); any other
/// refusal, a permission the caller lacks say, is passed on.
pub(crate) fn apply<C>(
    cx: &Cx<'_, C>,
    use_id: &str,
    rule: Option<&StatusMove>,
    status: Option<&str>,
) -> Hook<bool> {
    let (Some(rule), Some(status)) = (rule, status) else {
        return Ok(false);
    };
    if !rule.moves_from(status) {
        return Ok(false);
    }
    match cx.call(
        use_id,
        workflow::TRANSITION,
        &json!({ "to": rule.transition }),
    ) {
        Ok(_) => Ok(true),
        Err(stop) if stop.refusal().map(|refusal| refusal.code) == Some(Code::Vetoed) => Ok(false),
        Err(stop) => Err(stop),
    }
}

/// Whether a state is terminal in a Flow: one of its states that no
/// transition leaves.
pub(crate) fn is_terminal(flow: &Flow, state: &str) -> bool {
    flow.has_state(state) && !flow.transitions.iter().any(|(source, _)| source == state)
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

#[cfg(test)]
mod tests {
    use super::*;
    use superschematic_rules::{TypeSpec, UseSpec};

    fn flow() -> Flow {
        Flow {
            use_id: "Workflow".into(),
            states: vec!["queued".into(), "running".into(), "done".into()],
            initial: "queued".into(),
            transitions: vec![
                ("queued".into(), "running".into()),
                ("running".into(), "queued".into()),
                ("running".into(), "done".into()),
            ],
        }
    }

    fn rule(transition: &str, from: &[&str]) -> StatusMove {
        StatusMove {
            transition: transition.into(),
            from: from.iter().map(|state| (*state).to_owned()).collect(),
        }
    }

    #[test]
    fn a_move_is_held_to_the_workflow_in_the_engines_words() {
        let flow = flow();
        assert_eq!(
            check_move(&flow, "onExpiry", &rule("queued", &["running"])),
            Ok(())
        );
        let refused = |rule: StatusMove| {
            check_move(&flow, "onExpiry", &rule)
                .unwrap_err()
                .to_string()
        };
        assert_eq!(
            refused(rule("paused", &["running"])),
            "onExpiry.transition \"paused\" is not a state of the type's Workflow (queued, running, done)"
        );
        assert_eq!(
            refused(rule("queued", &["lost"])),
            "onExpiry.from names \"lost\", which is not a state of the type's Workflow (queued, running, done)"
        );
        assert_eq!(
            refused(rule("queued", &["queued"])),
            "onExpiry moves the status from \"queued\" to itself"
        );
        assert_eq!(
            refused(rule("queued", &["done"])),
            "onExpiry: no transition of the type's Workflow leads from \"done\" to \"queued\""
        );
        assert_eq!(
            no_workflow("escalate").to_string(),
            "escalate moves the status through Workflow, which the type does not list"
        );
        assert!(is_terminal(&flow, "done"));
        assert!(!is_terminal(&flow, "running"));
        assert!(!is_terminal(&flow, "lost"));
        assert_eq!(
            StatusMove::parse(&json!({"transition": "queued", "from": ["running"]})),
            rule("queued", &["running"])
        );
    }

    #[test]
    fn the_workflow_is_the_named_one_or_the_types_one() {
        let config = json!({"states": ["a", "b"], "transitions": [{"from": "a", "to": "b"}]});
        let none = TypeSpec::new("Job").with(UseSpec::on_type("Lease", None));
        assert_eq!(flow_for(&ConfigTarget::new(&none, 0), None), Ok(None));
        assert_eq!(
            flow_for(&ConfigTarget::new(&none, 0), Some("phase"))
                .unwrap_err()
                .to_string(),
            "status \"phase\" is not a Workflow of the type (its Workflows: none)"
        );
        let one = TypeSpec::new("Job")
            .with(UseSpec::on_type("Lease", None))
            .with(UseSpec::on_field("status", "Workflow", config.clone()));
        let found = flow_for(&ConfigTarget::new(&one, 0), None).unwrap();
        assert_eq!(found.map(|flow| flow.use_id), Some("status".to_owned()));
        let two = one
            .clone()
            .with(UseSpec::on_field("phase", "Workflow", config));
        assert_eq!(
            flow_for(&ConfigTarget::new(&two, 0), None)
                .unwrap_err()
                .to_string(),
            "it reads the status of Workflow, and the type has several (status, phase): status names the field whose Workflow it reads"
        );
        let named = flow_for(&ConfigTarget::new(&two, 0), Some("phase")).unwrap();
        assert_eq!(named.map(|flow| flow.use_id), Some("phase".to_owned()));
    }
}
