//! Workflow's guidance (`guidance/workflow.ts`): the states, where an
//! instance starts, which moves leave each state and which of them need a
//! permission, and the outcome of each terminal state, so a caller of
//! transition knows which `to` it may name from where the instance is.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{capital, list, refusal, said, sentences};
use crate::workflow::{WorkflowConfig, WorkflowOutcome};

pub(crate) fn guidance(config: &WorkflowConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let moves = |state: &str| config.moves(state);
    let terminal: Vec<&str> = config
        .states
        .iter()
        .map(String::as_str)
        .filter(|state| moves(state).is_empty())
        .collect();
    let leaving: Vec<&str> = config
        .states
        .iter()
        .map(String::as_str)
        .filter(|state| !moves(state).is_empty())
        .collect();
    let outcomes: Vec<String> = terminal
        .iter()
        .map(|state| {
            format!(
                "{state} ({})",
                config
                    .outcome(state)
                    .unwrap_or(WorkflowOutcome::Success)
                    .as_str()
            )
        })
        .collect();
    let routes: Vec<String> = leaving
        .iter()
        .map(|state| {
            let to: Vec<&str> = moves(state)
                .iter()
                .map(|transition| transition.to.as_str())
                .collect();
            format!("from {state} to {}", list(&to, "or"))
        })
        .collect();
    // The moves each permission guards, by permission in the config's order.
    let mut guarded: Vec<(&str, Vec<String>)> = Vec::new();
    for transition in &config.transitions {
        if let Some(permission) = &transition.permission {
            let edge = format!("{} to {}", transition.from, transition.to);
            match guarded.iter_mut().find(|(known, _)| known == permission) {
                Some((_, edges)) => edges.push(edge),
                None => guarded.push((permission, vec![edge])),
            }
        }
    }
    let permissions: Vec<String> = guarded
        .iter()
        .map(|(permission, edges)| match edges.as_slice() {
            [one] => format!("the move {one} needs permission {permission}"),
            _ => format!(
                "the moves {} need permission {permission}",
                list(edges, "and")
            ),
        })
        .collect();
    let mut errors = vec![
        refusal(
            "transition_not_allowed",
            Some("No transition leads from the instance's status to the state named; details.allowed lists the states it can move to."),
            "Read status, then name a state details.allowed lists, or move through one of them first.",
        ),
        refusal(
            "already_in_state",
            None,
            "Read status: the instance is already there, so nothing needs to move.",
        ),
    ];
    if !terminal.is_empty() {
        errors.push(refusal(
            "terminal_state",
            None,
            "None: the work has ended. Create a new instance to start again.",
        ));
    }
    errors.push(refusal(
        "no_status",
        None,
        "None: an instance without a status cannot move.",
    ));
    BehaviorGuidance {
        summary: sentences(&[
            Some(format!(
                "Its status is one of {}; a new {} starts in {}, and status moves only by transition.",
                list(&config.states, "and"),
                target.type_name,
                config.initial
            )),
            Some(if terminal.is_empty() {
                "No state is terminal: a transition leaves every state.".to_owned()
            } else {
                format!("Terminal, with their outcomes: {}.", list(&outcomes, "and"))
            }),
        ]),
        operations: vec![
            (
                "transition".to_owned(),
                said(
                    Some(sentences(&[
                        (!leaving.is_empty())
                            .then(|| format!("Use to move status {}.", routes.join("; "))),
                        (!permissions.is_empty()).then(|| {
                            format!(
                                "{}; without it the call is forbidden.",
                                capital(&permissions.join("; "))
                            )
                        }),
                    ])),
                    Some(sentences(&[
                        (!terminal.is_empty()).then(|| {
                            format!(
                                "Do not use from {}: no transition leaves a terminal state.",
                                list(&terminal, "or")
                            )
                        }),
                        Some("Do not use to stay where the instance is.".to_owned()),
                    ])),
                    Some("Returns from and to; status holds to from then on, and enteredAt the time of the move.".to_owned()),
                    errors,
                ),
            ),
            (
                "create".to_owned(),
                OperationGuidance {
                    success: Some(format!("A new instance's status is {}.", config.initial)),
                    ..OperationGuidance::default()
                },
            ),
            (
                "list".to_owned(),
                OperationGuidance {
                    use_when: Some(format!(
                        "where: {{ \"Workflow.status\": <state> }} lists the instances in one of {}, and a list of states the instances in any of them.",
                        list(&config.states, "or")
                    )),
                    ..OperationGuidance::default()
                },
            ),
            (
                "update".to_owned(),
                OperationGuidance {
                    do_not_use_when: Some("Do not use to move Workflow's status, which behaviors.Workflow holds; call transition.".to_owned()),
                    ..OperationGuidance::default()
                },
            ),
        ],
    }
}
