//! The Workflow a config of StateRules or Approvals reads (D104): the
//! Workflow use the config's `status` names, by its id (the field it is
//! anchored on, or `Workflow` for one anchored on the type), or the type's
//! one Workflow when it names none, as Timers resolves its own. A type with
//! no Workflow, and one with several whose config names none, is refused at
//! define. The engine read the instance's one `Workflow`.
//!
//! Reconcile reads a Workflow's transitions as paths (D104): the fewest
//! transitions from a state to another ([`path`]) and every state a state
//! reaches ([`reachable`]).

use std::collections::VecDeque;

use serde_json::Value;
use superschematic_rules::{ConfigError, ConfigTarget, UseSpec};

use crate::workflow;

/// The Workflow a config reads (D104): its use's id, states, initial state
/// and transitions, as the type's uses hold its config. A Workflow config
/// of a shape Workflow refuses (no list of states that are strings) reads
/// as no states, and the checks against them pass: Workflow refuses it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Flow {
    pub use_id: String,
    pub states: Vec<String>,
    pub initial: String,
    pub transitions: Vec<(String, String)>,
}

impl Flow {
    pub fn has_state(&self, state: &str) -> bool {
        self.states.iter().any(|known| known == state)
    }

    /// Whether a check against the states refuses a state: the flow reads
    /// states, and the state is none of them.
    pub fn lacks(&self, state: &str) -> bool {
        !self.states.is_empty() && !self.has_state(state)
    }

    /// The states, as a refusal lists them.
    pub fn listed(&self) -> String {
        self.states.join(", ")
    }
}

/// Whether a transition of a Workflow's, by its `from` and `to`, leads
/// from one state to another.
pub(crate) fn leads(transitions: &[(String, String)], from: &str, to: &str) -> bool {
    transitions
        .iter()
        .any(|(source, target)| source == from && target == to)
}

/// The states after `from` along the fewest transitions that lead to `to`
/// (D104): breadth first, each state's transitions in the config's order,
/// so of two paths of one length the one whose first differing step the
/// config lists first; `Some(vec![])` when `from` is `to`, `None` when no
/// transitions lead there.
pub(crate) fn path(transitions: &[(String, String)], from: &str, to: &str) -> Option<Vec<String>> {
    if from == to {
        return Some(Vec::new());
    }
    // Each state reached, with the index of the one it was reached from.
    let mut reached: Vec<(&str, Option<usize>)> = vec![(from, None)];
    let mut queue: VecDeque<usize> = VecDeque::from([0]);
    while let Some(at) = queue.pop_front() {
        let state = reached[at].0;
        for (_, target) in transitions.iter().filter(|(source, _)| source == state) {
            if reached.iter().any(|(known, _)| known == target) {
                continue;
            }
            reached.push((target, Some(at)));
            if target == to {
                let mut steps = Vec::new();
                let mut back = Some(reached.len() - 1);
                while let Some(index) = back {
                    let (state, parent) = reached[index];
                    if parent.is_some() {
                        steps.push(state.to_owned());
                    }
                    back = parent;
                }
                steps.reverse();
                return Some(steps);
            }
            queue.push_back(reached.len() - 1);
        }
    }
    None
}

/// Every state the transitions lead to from a state, itself included,
/// breadth first from it.
pub(crate) fn reachable(transitions: &[(String, String)], from: &str) -> Vec<String> {
    let mut reached: Vec<String> = vec![from.to_owned()];
    let mut next = 0;
    while next < reached.len() {
        let state = reached[next].clone();
        next += 1;
        for (_, target) in transitions.iter().filter(|(source, _)| *source == state) {
            if !reached.contains(target) {
                reached.push(target.clone());
            }
        }
    }
    reached
}

/// The Workflow the config's `status` names, or the type's one Workflow.
pub(crate) fn flow_of(target: &ConfigTarget<'_>, named: Option<&str>) -> Result<Flow, ConfigError> {
    let workflows: Vec<&UseSpec> = target
        .uses()
        .iter()
        .filter(|entry| entry.behavior == workflow::NAME)
        .collect();
    let ids = || -> String {
        let ids: Vec<&str> = workflows.iter().map(|entry| entry.id()).collect();
        if ids.is_empty() {
            "none".to_owned()
        } else {
            ids.join(", ")
        }
    };
    let entry = match named {
        Some(named) => match workflows.iter().find(|entry| entry.id() == named) {
            Some(entry) => *entry,
            None => {
                return Err(format!(
                    "status \"{named}\" is not a Workflow of the type (its Workflows: {})",
                    ids()
                )
                .into())
            }
        },
        None => match workflows.as_slice() {
            [] => {
                return Err(
                    "it reads the status of Workflow, which the type does not list".into(),
                )
            }
            [entry] => *entry,
            _ => {
                return Err(format!(
                    "it reads the status of Workflow, and the type has several ({}): status names the field whose Workflow it reads",
                    ids()
                )
                .into())
            }
        },
    };
    Ok(read(entry.id(), &entry.config_or_empty()))
}

// read reads a Workflow config as the type holds it; one of another shape
// is Workflow's to refuse, and reads as no states.
fn read(use_id: &str, raw: &Value) -> Flow {
    let states: Vec<String> = match raw.get("states").and_then(Value::as_array) {
        Some(states) if states.iter().all(Value::is_string) => states
            .iter()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect(),
        _ => Vec::new(),
    };
    let initial = match raw.get("initial") {
        Some(Value::String(initial)) => initial.clone(),
        _ => states.first().cloned().unwrap_or_default(),
    };
    let transitions = raw
        .get("transitions")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|edge| {
            Some((
                edge.get("from")?.as_str()?.to_owned(),
                edge.get("to")?.as_str()?.to_owned(),
            ))
        })
        .collect();
    Flow {
        use_id: use_id.to_owned(),
        states,
        initial,
        transitions,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn edges(pairs: &[(&str, &str)]) -> Vec<(String, String)> {
        pairs
            .iter()
            .map(|(from, to)| ((*from).to_owned(), (*to).to_owned()))
            .collect()
    }

    fn states(names: &[&str]) -> Option<Vec<String>> {
        Some(names.iter().map(|name| (*name).to_owned()).collect())
    }

    #[test]
    fn a_path_is_the_fewest_transitions_its_tie_broken_by_the_configs_order() {
        let diamond = edges(&[
            ("draft", "left"),
            ("draft", "right"),
            ("left", "done"),
            ("right", "done"),
            ("done", "draft"),
        ]);
        assert_eq!(path(&diamond, "draft", "done"), states(&["left", "done"]));
        assert_eq!(path(&diamond, "done", "right"), states(&["draft", "right"]));
        assert_eq!(path(&diamond, "left", "left"), states(&[]));
        // Listed the other way, the other path is the first.
        let swapped = edges(&[
            ("draft", "right"),
            ("draft", "left"),
            ("left", "done"),
            ("right", "done"),
        ]);
        assert_eq!(path(&swapped, "draft", "done"), states(&["right", "done"]));
        // A shorter path wins over one the config lists first.
        let short = edges(&[("a", "b"), ("b", "c"), ("c", "d"), ("a", "d")]);
        assert_eq!(path(&short, "a", "d"), states(&["d"]));
    }

    #[test]
    fn no_path_leads_out_of_a_terminal_state_and_a_cycle_ends() {
        let flow = edges(&[
            ("pending", "running"),
            ("running", "pending"),
            ("running", "failed"),
        ]);
        assert_eq!(path(&flow, "failed", "running"), None);
        assert_eq!(path(&flow, "pending", "lost"), None);
        assert_eq!(
            path(&flow, "pending", "failed"),
            states(&["running", "failed"])
        );
        assert_eq!(
            reachable(&flow, "pending"),
            ["pending", "running", "failed"]
        );
        assert_eq!(reachable(&flow, "failed"), ["failed"]);
        assert_eq!(reachable(&[], "lost"), ["lost"]);
    }
}
