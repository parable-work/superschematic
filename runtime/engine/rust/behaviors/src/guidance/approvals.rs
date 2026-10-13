//! Approvals' guidance (`guidance/approvals.ts`): which moves of the
//! type's Workflow wait for how many sign-offs by callers with which
//! permission, whether the creator may sign, and what consumes a sign-off,
//! so a caller signs before the move and again after a change; Workflow's
//! transition and update hear the same.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{capital, list, plural, refusal, said, sentences};
use crate::approvals::ApprovalsConfig;

pub(crate) fn guidance(config: &ApprovalsConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let states: Vec<&str> = config
        .gates
        .iter()
        .map(|(state, _)| state.as_str())
        .collect();
    let needs: Vec<String> = config
        .gates
        .iter()
        .map(|(state, gate)| {
            format!(
                "a move into {state}{} waits for {} by callers with permission {}",
                gate.from
                    .as_ref()
                    .map_or(String::new(), |from| format!(" from {}", list(from, "or"))),
                plural(gate.count, "sign-off"),
                gate.permission
            )
        })
        .collect();
    let excluding: Vec<&str> = config
        .gates
        .iter()
        .filter(|(_, gate)| gate.exclude_creator)
        .map(|(state, _)| state.as_str())
        .collect();
    let page = "Returns items and next; pass next as cursor for the page after, until it is null.";
    let no_gate = || {
        refusal(
            "no_gate",
            None,
            &format!("Name a gated state: {}.", list(&states, "or")),
        )
    };
    let creator = if excluding.len() == states.len() {
        Some(format!(
            "The principal who created a {} cannot sign off on it.",
            target.type_name
        ))
    } else if !excluding.is_empty() {
        Some(format!(
            "The principal who created a {} cannot sign off on it for {}.",
            target.type_name,
            list(&excluding, "or")
        ))
    } else {
        None
    };
    let consumed = if config.reset_on_change {
        "A change of the own fields consumes every sign-off, and entering a gated state consumes its own, so each is signed again after."
    } else {
        "Entering a gated state consumes its sign-offs, so a return to it is signed again; a change of the own fields keeps them."
    };
    let mut sign_off_errors = vec![
        no_gate(),
        refusal(
            "in_state",
            None,
            "Wait until the instance has left the state; a sign-off counts toward the next move into it.",
        ),
    ];
    if !excluding.is_empty() {
        sign_off_errors.push(refusal(
            "creator_cannot_sign",
            None,
            "Ask another principal with the permission to sign off.",
        ));
    }
    sign_off_errors.push(refusal(
        "already_signed",
        None,
        "Leave it: the caller has signed off for the state already.",
    ));
    let mut operations = vec![
        (
            "signOff".to_owned(),
            said(
                Some(format!(
                    "Use to sign off for {}, as the caller; it needs the gate's permission, else the call is forbidden.",
                    list(&states, "or")
                )),
                Some("Do not use while the instance is in the state, or to sign twice.".to_owned()),
                Some("Returns the sign-off, the subjects whose sign-offs for the state count (signedBy) and the count the gate needs.".to_owned()),
                sign_off_errors,
            ),
        ),
        (
            "withdrawSignOff".to_owned(),
            said(
                Some("Use to take back the caller's own sign-off for a gated state before the move it waits for.".to_owned()),
                None,
                Some("Returns the sign-off withdrawn; it no longer counts.".to_owned()),
                vec![
                    no_gate(),
                    refusal(
                        "not_signed",
                        None,
                        "None: the caller has no sign-off for the state that counts.",
                    ),
                ],
            ),
        ),
        (
            "listSignOffs".to_owned(),
            said(
                Some("Use to read the instance's sign-offs, oldest first, the consumed ones with consumedAt; with state, only those for it.".to_owned()),
                Some("Do not page through them for the ones that count; read behaviors.Approvals.gates.".to_owned()),
                Some(page.to_owned()),
                Vec::new(),
            ),
        ),
        (
            "transition".to_owned(),
            said(
                None,
                Some(format!(
                    "Read behaviors.Approvals.gates before a move: {}.",
                    needs.join("; ")
                )),
                None,
                vec![refusal(
                    "approvals_missing",
                    Some("A move into a gated state with fewer sign-offs than its count; details.details gives the state, the count it requires and the sign-offs it has."),
                    "Ask callers with the gate's permission to sign off, then try again.",
                )],
            ),
        ),
    ];
    if config.reset_on_change {
        operations.push((
            "update".to_owned(),
            OperationGuidance {
                success: Some("A change of the own fields consumes every sign-off: each gate is signed again before its move.".to_owned()),
                ..OperationGuidance::default()
            },
        ));
    }
    BehaviorGuidance {
        summary: sentences(&[
            Some(format!("{}.", capital(&needs.join("; ")))),
            creator,
            Some("behaviors.Approvals.gates lists the sign-offs that count, by state.".to_owned()),
            Some(consumed.to_owned()),
        ]),
        operations,
    }
}
