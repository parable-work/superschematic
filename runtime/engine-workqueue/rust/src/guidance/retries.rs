//! Retries' guidance (`typescript/src/guidance/retries.ts`): the failure
//! classes and their caps, the total, what exhaustion does, and the
//! refusals its guard adds once an instance is exhausted (Workflow's
//! transition, Lease's acquire, Queue's claim) and to updates of its
//! limits field.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};
use superschematic_engine::json::js_number;

use super::{list, refusal, said, sentences};
use crate::retries::{RetriesConfig, RetryClass};

/// What Retries tells a caller about the type under its config.
pub(crate) fn guidance(config: &RetriesConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let classes: Vec<String> = config
        .classes
        .iter()
        .map(|(name, class)| match class {
            RetryClass::Terminal => format!("{name} (terminal: one failure exhausts)"),
            RetryClass::Capped { attempts, .. } => format!(
                "{name} ({attempts} attempt{})",
                if *attempts == 1 { "" } else { "s" }
            ),
        })
        .collect();
    let names: Vec<&str> = config
        .classes
        .iter()
        .map(|(name, _)| name.as_str())
        .collect();
    let has = |name: &str| {
        target
            .operations
            .iter()
            .any(|operation| operation.name == name)
    };
    let exhausted_errors = || {
        vec![refusal(
            "exhausted",
            None,
            "None: the instance takes no more attempts; create new work instead.",
        )]
    };
    let exhausted = || -> OperationGuidance {
        said(
            None,
            Some("Do not use once behaviors.Retries.exhausted is true.".to_owned()),
            None,
            exhausted_errors(),
        )
    };
    let limits = config.limits_field.as_deref();
    let to = &config.exhausted_state;
    let mut operations = vec![
        (
            "recordAttempt".to_owned(),
            said(
                Some(sentences(&[
                    Some(format!(
                        "Use after each attempt: without failure it is a success; with failure, one of {}.",
                        list(&names, "or")
                    )),
                    config
                        .permission
                        .as_ref()
                        .map(|permission| format!("It needs permission {permission}.")),
                ])),
                None,
                Some("Returns the counts, whether the instance is exhausted or stuck, and the failure class's hint for the next attempt.".to_owned()),
                exhausted_errors(),
            ),
        ),
        (
            "transition".to_owned(),
            said(
                None,
                Some(format!(
                    "Do not move an exhausted instance anywhere but {to}."
                )),
                None,
                exhausted_errors(),
            ),
        ),
        (
            "list".to_owned(),
            said(
                Some(format!(
                    "where: {{ \"Retries.exhausted\": true }} lists the {} instances whose retries are exhausted, and false the ones that may run again.",
                    target.type_name
                )),
                None,
                None,
                Vec::new(),
            ),
        ),
    ];
    for name in ["acquire", "claim"] {
        if has(name) {
            operations.push((name.to_owned(), exhausted()));
        }
    }
    if let Some(limits) = limits {
        let mut errors = Vec::new();
        if config.leased {
            errors.push(refusal(
                "limits_fixed",
                None,
                "None: a worker never raises its own caps; an operator does.",
            ));
        }
        if config.limits_permission.is_none() {
            errors.push(refusal(
                "not_configured",
                None,
                &format!("None: {limits} does not change once the instance exists."),
            ));
        }
        operations.push((
            "update".to_owned(),
            said(
                None,
                Some(format!(
                    "Do not change {limits}{}{}.",
                    config
                        .limits_permission
                        .as_ref()
                        .map(|permission| format!(" without permission {permission}"))
                        .unwrap_or_default(),
                    if config.leased {
                        ", nor as the holder of its lease"
                    } else {
                        ""
                    }
                )),
                None,
                errors,
            ),
        ));
    }
    BehaviorGuidance {
        summary: sentences(&[
            Some(format!(
                "Failure classes: {}; at most {} counted failure{} in all{}.",
                list(&classes, "and"),
                config.total_attempts,
                if config.total_attempts == 1 { "" } else { "s" },
                limits
                    .map(|limits| format!(", or the caps the instance's {limits} holds"))
                    .unwrap_or_default()
            )),
            Some(format!(
                "Once exhausted, status moves from {} to {to}, and the {} takes no more attempts, moves only to {to}, and is not acquired or claimed.",
                list(&config.from, "or"),
                target.type_name
            )),
            config.stuck_after.map(|after| {
                format!("{after} failures in a row with the same signature exhaust it as stuck.")
            }),
            config.result_field.as_ref().map(|field| {
                format!(
                    "A kept attempt's result is written to {field}{}.",
                    config
                        .keep_best
                        .as_ref()
                        .map(|keep| format!(
                            "; a failure's is kept when its score beats the best by {}",
                            js_number(keep.min_delta)
                        ))
                        .unwrap_or_default()
                )
            }),
        ]),
        operations,
    }
}
