//! Reactions' guidance (`guidance/reactions.ts`): its rules, each what sets
//! it off and the move it makes, which the runner makes after a change
//! commits (D16, amended; D56). It adds no operation, so it gives a
//! summary, and tells a caller of `transition` and of the writes that can
//! set a rule off that the status may move again on its own: for a changes
//! rule, a create and an update when its field is an own field, and the
//! writing operations of the behavior whose field it is.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget};
use superschematic_engine::json::stringify;

use super::{list, said};
use crate::reactions::{ReactionsConfig, ReactionsRule, ReactionsWhen};
use crate::workflow::WorkflowOutcome;

// outcomes names a rule's outcomes as a sentence lists them.
fn outcomes(listed: &[WorkflowOutcome]) -> String {
    let names: Vec<&str> = listed.iter().map(|outcome| outcome.as_str()).collect();
    list(&names, "or")
}

// when says what sets one rule off.
fn when(rule: &ReactionsRule) -> String {
    match &rule.when {
        ReactionsWhen::Enters(state) => format!("when it enters {state}"),
        ReactionsWhen::Changes(changes) => format!(
            "when {} changes{}{}",
            changes.field,
            changes
                .from
                .as_ref()
                .map_or(String::new(), |from| format!(" from {}", stringify(from))),
            changes
                .to
                .as_ref()
                .map_or(String::new(), |to| format!(" to {}", stringify(to)))
        ),
        ReactionsWhen::AllTerminal(terminal) => format!(
            "when every {} linking here through {} is in a terminal state{}",
            terminal.schema,
            terminal.link,
            terminal
                .outcomes
                .as_deref()
                .map_or(String::new(), |listed| format!(
                    " with outcome {}",
                    outcomes(listed)
                ))
        ),
        ReactionsWhen::AnyTerminal(terminal) => format!(
            "when a {} linking here through {} ends with outcome {}",
            terminal.schema,
            terminal.link,
            outcomes(terminal.outcomes.as_deref().unwrap_or_default())
        ),
        ReactionsWhen::Holds(rollup) => format!("when rollup {rollup} comes to hold"),
        ReactionsWhen::Revised { link } => {
            format!("when the target of {link} gains a revision or a release")
        }
    }
}

// then says the move one rule makes.
fn then(rule: &ReactionsRule) -> String {
    match &rule.then.link {
        None => format!("to {}", rule.then.transition),
        Some(link) => format!(
            "the instance its {link} points to, to {}",
            rule.then.transition
        ),
    }
}

// moving lists the operations of the type that can move a changes rule's
// field: a create and an update for an own field, the writing operations
// of the behavior whose field it is.
fn moving(config: &ReactionsConfig, target: &DescribeTarget) -> Vec<String> {
    let mut names: Vec<String> = Vec::new();
    for rule in &config.rules {
        let ReactionsWhen::Changes(changes) = &rule.when else {
            continue;
        };
        let behavior = config
            .field(&changes.field)
            .and_then(|field| field.behavior.as_deref());
        for operation in &target.operations {
            let moves = match behavior {
                None => operation.name == "create" || operation.name == "update",
                Some(behavior) => {
                    operation.behavior.as_deref() == Some(behavior) && operation.writes
                }
            };
            if moves && !names.contains(&operation.name) {
                names.push(operation.name.clone());
            }
        }
    }
    names
}

pub(crate) fn guidance(config: &ReactionsConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let rules: Vec<String> = config
        .rules
        .iter()
        .map(|rule| format!("{}, it moves {}", when(rule), then(rule)))
        .collect();
    let success = format!(
        "Reactions' rules on {} may move it, or the instances it links to, again after the commit.",
        target.type_name
    );
    let mut operations = vec!["transition".to_owned()];
    operations.extend(
        moving(config, target)
            .into_iter()
            .filter(|name| name != "transition"),
    );
    BehaviorGuidance {
        summary: format!(
            "After a change commits, the runner moves statuses by rules, through transition and its guards: {}. A move a guard vetoes, or that no transition allows, is left undone.",
            rules.join("; ")
        ),
        operations: operations
            .into_iter()
            .map(|name| (name, said(None, None, Some(success.clone()), Vec::new())))
            .collect(),
    }
}
