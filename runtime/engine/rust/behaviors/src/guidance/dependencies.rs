//! Dependencies' guidance (`guidance/dependencies.ts`): which schemas a
//! blocker comes from, which Workflow moves wait on blockers and which
//! outcomes finish one, and the narrowed create parameters: the blocker
//! schemas the config allows, and a blocker's schema required when the
//! type's own schema is not among them.

use serde_json::{json, Value};

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{list, refusal, said, sentences};
use crate::dependencies::DependenciesConfig;

const PAGE: &str =
    "Returns items and next; pass next as cursor for the page after, until it is null.";

// from says where a blocker comes from, and what an absent schema means.
fn from(config: &DependenciesConfig, target: &DescribeTarget) -> String {
    if config.schemas.contains(&target.schema) {
        format!(
            "an instance of {} (the instance's own schema when schema is absent)",
            list(&config.schemas, "or")
        )
    } else {
        format!(
            "an instance of {}, named by schema",
            list(&config.schemas, "or")
        )
    }
}

pub(crate) fn guidance(config: &DependenciesConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let gated = &config.gated_states;
    let outcomes: Vec<&str> = config
        .satisfied_by
        .iter()
        .map(|outcome| outcome.as_str())
        .collect();
    let finishes = format!(
        "a terminal state of its own Workflow whose outcome is {}",
        list(&outcomes, "or")
    );
    let from = from(config, target);
    let mut edge_errors = vec![
        refusal(
            "already_blocking",
            None,
            "Leave it: that instance already blocks this one.",
        ),
        refusal(
            "cycle",
            None,
            "Remove the edge that makes the other instance wait on this one first, or leave this edge out.",
        ),
    ];
    if !config.terminal_gated_states.is_empty() {
        edge_errors.push(refusal(
            "gated",
            None,
            "Add only finished blockers to an instance in a gated state no transition leaves, or none.",
        ));
    }
    let mut operations = vec![
        (
            "addBlocker".to_owned(),
            said(
                Some(sentences(&[
                    Some(format!("Use to make {from} block this one.")),
                    (!gated.is_empty()).then(|| {
                        format!(
                            "It holds up the transition into {} until it finishes.",
                            list(gated, "or")
                        )
                    }),
                ])),
                Some("Do not use at create; give the blockers in behaviors.Dependencies.blockers instead.".to_owned()),
                Some("Returns the blocker, with its status and whether it is open; blocked is true while one is.".to_owned()),
                edge_errors.clone(),
            ),
        ),
        (
            "removeBlocker".to_owned(),
            said(
                Some("Use to stop an instance blocking this one, as when a blocker failed or was cancelled and the work goes on without it.".to_owned()),
                None,
                Some("Returns the blocker it removed; blocked is false once no open blocker is left.".to_owned()),
                Vec::new(),
            ),
        ),
        (
            "listBlockers".to_owned(),
            said(
                Some("Use to read the instance's blockers, each with its status and whether it is open.".to_owned()),
                None,
                Some(PAGE.to_owned()),
                Vec::new(),
            ),
        ),
        (
            "listDependents".to_owned(),
            said(
                Some("Use to read the instances this one blocks, in schemas the caller may read.".to_owned()),
                None,
                Some(PAGE.to_owned()),
                Vec::new(),
            ),
        ),
        (
            "create".to_owned(),
            OperationGuidance {
                use_when: Some(format!(
                    "behaviors.Dependencies.blockers gives the instance its blockers from the create, at most 500, each {from}."
                )),
                errors: edge_errors,
                ..OperationGuidance::default()
            },
        ),
    ];
    if !gated.is_empty() {
        operations.push((
            "transition".to_owned(),
            OperationGuidance {
                do_not_use_when: Some(format!(
                    "Do not move into {} while blocked is true.",
                    list(gated, "or")
                )),
                errors: vec![refusal(
                    "blocked",
                    Some(&format!(
                        "A move into {} while a blocker is open; details.details.blockers lists the open ones.",
                        list(gated, "or")
                    )),
                    "Wait for the open blockers to finish, or remove the ones the work goes on without, then try again.",
                )],
                ..OperationGuidance::default()
            },
        ));
    }
    BehaviorGuidance {
        summary: sentences(&[
            Some(format!(
                "Each {} waits on blockers, each {from}.",
                target.type_name
            )),
            Some(if gated.is_empty() {
                "No state is gated, so blockers hold up no transition; blocked says whether one is open.".to_owned()
            } else {
                format!(
                    "A transition into {} waits until every blocker has finished: its status is {finishes}. blocked says whether one is open.",
                    list(gated, "or")
                )
            }),
        ]),
        operations,
    }
}

/// Dependencies' create parameters under its config
/// (`dependenciesCreateParams`): each blocker's schema one the config
/// lists, and required when the type's own schema is not among them, which
/// an absent schema names.
pub(crate) fn create_params(config: &DependenciesConfig, target: &DescribeTarget) -> Value {
    let own = config.schemas.contains(&target.schema);
    json!({
        "type": "object",
        "additionalProperties": false,
        "properties": {
            "blockers": {
                "description": format!(
                    "The instance's blockers from its create, each held to addBlocker's rules: an instance of {}.",
                    list(&config.schemas, "or")
                ),
                "type": "array",
                "maxItems": 500,
                "items": {
                    "type": "object",
                    "additionalProperties": false,
                    "required": if own { json!(["id"]) } else { json!(["id", "schema"]) },
                    "properties": {
                        "schema": {
                            "description": if own {
                                format!("The blocker's schema; {} when absent.", target.schema)
                            } else {
                                "The blocker's schema.".to_owned()
                            },
                            "type": "string",
                            "enum": config.schemas
                        },
                        "id": {
                            "description": "The blocker's id.",
                            "type": "string",
                            "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
                        }
                    }
                }
            }
        }
    })
}
