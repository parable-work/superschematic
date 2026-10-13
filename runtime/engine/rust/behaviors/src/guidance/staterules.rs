//! StateRules' guidance (`guidance/staterules.ts`): what each state
//! requires and freezes, who may change a frozen field, and what a create,
//! a transition, an update and a Revisions approval meet under the rules.

use superschematic_engine::behaviors::{
    BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance,
};

use super::{capital, list, refusal, sentences};
use crate::staterules::{StateRule, StateRulesConfig};

// rule_text says one state's rules as a sentence.
fn rule_text(state: &str, rule: &StateRule) -> String {
    let mut parts: Vec<String> = Vec::new();
    if !rule.required.is_empty() {
        parts.push(format!(
            "entering {state} needs {}, which {} set while it is {state}",
            list(&rule.required, "and"),
            if rule.required.len() == 1 {
                "stays"
            } else {
                "stay"
            }
        ));
    }
    if rule.frozen_all {
        parts.push(format!("no field changes while it is {state}"));
    } else if !rule.frozen.is_empty() {
        parts.push(format!(
            "{} {} not change while it is {state}",
            list(&rule.frozen, "and"),
            if rule.frozen.len() == 1 { "does" } else { "do" }
        ));
    }
    format!("{}.", capital(&parts.join("; ")))
}

pub(crate) fn guidance(config: &StateRulesConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let rules = &config.states;
    let entering: Vec<&(String, StateRule)> = rules
        .iter()
        .filter(|(_, rule)| !rule.required.is_empty())
        .collect();
    let freezing = rules.iter().any(|(_, rule)| !rule.frozen.is_empty());
    let create_requires: &[String] = config
        .initial
        .as_ref()
        .and_then(|initial| config.rule(initial))
        .map_or(&[], |rule| rule.required.as_slice());
    let who = match &config.permission {
        None => "Nobody changes a frozen field.".to_owned(),
        Some(permission) => {
            format!("Only a caller with permission {permission} changes a frozen field.")
        }
    };
    let entering_states: Vec<&str> = entering.iter().map(|(state, _)| state.as_str()).collect();
    let missing = refusal(
        "missing_fields",
        Some(&format!(
            "A move into {} while a field it requires is absent or null; details.details.fields lists them.",
            list(&entering_states, "or")
        )),
        "Update the instance to give the fields the state requires, then move it.",
    );
    let mut update_errors: Vec<GuidanceError> = Vec::new();
    if !entering.is_empty() {
        update_errors.push(refusal(
            "required_fields",
            Some("An update that clears a field the instance's state requires; details.details.fields lists them."),
            "Give the field a value instead of clearing it, or move the instance to a state that does not require it first.",
        ));
    }
    if freezing {
        let correction = match &config.permission {
            None => "Leave the field as it is, or move the instance to a state that does not freeze it first.".to_owned(),
            Some(permission) => format!(
                "Leave the field as it is, move the instance to a state that does not freeze it first, or ask a caller with permission {permission}."
            ),
        };
        update_errors.push(refusal(
            "frozen_fields",
            Some("An update that changes a field the instance's state freezes; details.details.fields lists them."),
            &correction,
        ));
    }
    // What an update leaves alone in each state: a field it requires, one
    // it freezes, or one it does both to, named once.
    let update_rules: Vec<String> = rules
        .iter()
        .map(|(state, rule)| {
            let same = !rule.frozen_all
                && rule.required.len() == rule.frozen.len()
                && rule
                    .required
                    .iter()
                    .all(|field| rule.frozen.contains(field));
            let parts: Vec<String> = if same {
                vec![format!("change or clear {}", list(&rule.required, "or"))]
            } else {
                let mut parts = Vec::new();
                if !rule.required.is_empty() {
                    parts.push(format!("clear {}", list(&rule.required, "or")));
                }
                if !rule.frozen.is_empty() {
                    parts.push(format!(
                        "change {}",
                        if rule.frozen_all {
                            "any field".to_owned()
                        } else {
                            list(&rule.frozen, "or")
                        }
                    ));
                }
                parts
            };
            format!("while it is {state}, do not {}", parts.join(" or "))
        })
        .collect();
    let permitted = match &config.permission {
        Some(permission) if freezing => format!(
            " (a caller with permission {permission} may change a frozen field{})",
            if entering.is_empty() {
                ""
            } else {
                ", but not clear a required one"
            }
        ),
        _ => String::new(),
    };
    let approves = target.operations.iter().any(|operation| {
        operation.name == "approve" && operation.behavior.as_deref() == Some("Revisions")
    });
    let mut operations = vec![(
        "update".to_owned(),
        OperationGuidance {
            do_not_use_when: Some(format!("{}{permitted}.", capital(&update_rules.join("; ")))),
            errors: update_errors.clone(),
            ..OperationGuidance::default()
        },
    )];
    if !entering.is_empty() {
        let before: Vec<String> = entering
            .iter()
            .map(|(state, rule)| {
                format!(
                    "{state} before {} {}",
                    list(&rule.required, "and"),
                    if rule.required.len() == 1 {
                        "holds a value"
                    } else {
                        "hold values"
                    }
                )
            })
            .collect();
        operations.push((
            "transition".to_owned(),
            OperationGuidance {
                do_not_use_when: Some(format!("Do not move into {}.", before.join(", or "))),
                errors: vec![missing],
                ..OperationGuidance::default()
            },
        ));
    }
    if !create_requires.is_empty() {
        let one = create_requires.len() == 1;
        operations.push((
            "create".to_owned(),
            OperationGuidance {
                use_when: Some(format!(
                    "Give {} at create: {}, the state a new {} starts in, requires {}, and a create without {} is refused as invalid_instance, with rule required at the field.",
                    list(create_requires, "and"),
                    config.initial.as_deref().unwrap_or_default(),
                    target.type_name,
                    if one { "it" } else { "them" },
                    if one { "it" } else { "them" }
                )),
                ..OperationGuidance::default()
            },
        ));
    }
    if approves {
        operations.push((
            "approve".to_owned(),
            OperationGuidance {
                do_not_use_when: Some("Do not approve a proposal whose patch breaks the rules of the instance's state: propose accepts it, and approve is refused.".to_owned()),
                errors: update_errors,
                ..OperationGuidance::default()
            },
        ));
    }
    let mut summary: Vec<Option<String>> = rules
        .iter()
        .map(|(state, rule)| Some(rule_text(state, rule)))
        .collect();
    summary.push(freezing.then_some(who));
    summary.push(Some(
        "A field is held when it is neither absent nor null.".to_owned(),
    ));
    BehaviorGuidance {
        summary: sentences(&summary),
        operations,
    }
}
