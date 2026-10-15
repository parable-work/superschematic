//! Assignment's guidance (`typescript/src/guidance/assignment.ts`): who
//! may assign whom, that an assigned instance's lease and claim are its
//! assignee's alone, which it adds to Lease's acquire and Queue's claim
//! when the type composes them, and the filter a list takes on the
//! assignee.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{refusal, said, sentences};
use crate::assignment::AssignmentConfig;

fn taken() -> OperationGuidance {
    said(
        None,
        Some("Do not use on an instance assigned to another principal.".to_owned()),
        None,
        vec![refusal(
            "assigned_to_another",
            None,
            "Take work assigned to you or to no one, or have it unassigned first.",
        )],
    )
}

/// What Assignment tells a caller about the type under its config.
pub(crate) fn guidance(config: &AssignmentConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let permission = config.permission.as_deref();
    let has = |name: &str| {
        target
            .operations
            .iter()
            .any(|operation| operation.name == name)
    };
    let not_configured = || {
        refusal(
            "not_configured",
            None,
            "None: only the assignee or an unassigned caller acts on its own assignment here.",
        )
    };
    let with_unconfigured = |first| {
        let mut errors = vec![first];
        if permission.is_none() {
            errors.push(not_configured());
        }
        errors
    };
    let mut operations = vec![
        (
            "assign".to_owned(),
            said(
                Some(match permission {
                    None => "Use to assign an unassigned instance to yourself.".to_owned(),
                    Some(permission) => format!("Use to assign an unassigned instance to yourself, or, with permission {permission}, any instance to another principal."),
                }),
                None,
                Some("Returns the assignee, when and by whom it was assigned.".to_owned()),
                with_unconfigured(refusal(
                    "already_assigned",
                    None,
                    "None: the instance is already assigned to that principal.",
                )),
            ),
        ),
        (
            "unassign".to_owned(),
            said(
                Some(match permission {
                    None => "Use as the assignee to give the assignment up.".to_owned(),
                    Some(permission) => format!("Use as the assignee to give the assignment up, or with permission {permission} to unassign another principal."),
                }),
                None,
                Some("Returns the assignee it had.".to_owned()),
                with_unconfigured(refusal(
                    "not_assigned",
                    None,
                    "None: the instance is not assigned.",
                )),
            ),
        ),
        (
            "list".to_owned(),
            said(
                Some(format!(
                    "where: {{ \"Assignment.assignee\": <subject> }} lists the {} instances assigned to a principal, \"Assignment.assignee\": null the unassigned ones, and [null, <subject>] both.",
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
            operations.push((name.to_owned(), taken()));
        }
    }
    BehaviorGuidance {
        summary: sentences(&[
            Some(format!(
                "Each {} is assigned to one principal or none; while it is assigned, only its assignee acquires its lease or claims it.",
                target.type_name
            )),
            Some(match permission {
                None => "A principal assigns an unassigned instance to itself and unassigns itself; the config names no permission for anything else.".to_owned(),
                Some(permission) => format!("A principal assigns an unassigned instance to itself and unassigns itself; assigning another principal, reassigning and unassigning another need permission {permission}."),
            }),
        ]),
        operations,
    }
}
