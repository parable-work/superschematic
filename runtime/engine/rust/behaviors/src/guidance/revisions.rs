//! Revisions' guidance (`guidance/revisions.ts`): every change of the own
//! fields records a revision, which getRevision reads by number, and with a
//! review step a change may be proposed, with evidence, and settled by a
//! caller with the review permission, while pendingProposals counts the
//! ones waiting. Without one, the review operations are refused, which
//! their guidance says.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{refusal, said};
use crate::revisions::RevisionsConfig;

const PAGE: &str =
    "Returns items and next; pass next as cursor for the page after, until it is null.";

pub(crate) fn guidance(config: &RevisionsConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let type_name = &target.type_name;
    let refused = |what: &str| {
        said(
            None,
            Some(format!(
                "Do not use: {type_name} has no review step, so {what} is refused (no_review)."
            )),
            None,
            vec![refusal(
                "no_review",
                None,
                "None on this type: change the instance with update instead.",
            )],
        )
    };
    let pending = || {
        refusal(
            "not_pending",
            None,
            "Read the proposal with listProposals: someone has already settled it.",
        )
    };
    let review = config
        .review
        .as_ref()
        .map(|review| review.permission.as_str());
    let summary = match review {
        None => format!(
            "Every change of the own fields of a {type_name} records a numbered revision of them; behaviors.Revisions.revision holds the latest. There is no review step."
        ),
        Some(permission) => format!(
            "Every change of the own fields of a {type_name} records a numbered revision of them; behaviors.Revisions.revision holds the latest. A change may also be proposed, and a caller with permission {permission} approves or rejects it; behaviors.Revisions.pendingProposals counts the proposals waiting."
        ),
    };
    let text = |text: &str| Some(text.to_owned());
    BehaviorGuidance {
        summary,
        operations: vec![
            (
                "listRevisions".to_owned(),
                said(
                    text("Use to read the instance's revisions, oldest first: each its number and the own fields as that change left them."),
                    text("Do not page through them for one revision; call getRevision with its number."),
                    text(PAGE),
                    Vec::new(),
                ),
            ),
            (
                "getRevision".to_owned(),
                said(
                    text("Use to read one revision by its number, the one a pinned link or a proposal names say: the own fields as that change left them."),
                    None,
                    text("Returns the revision; a number the instance has not reached is not_found."),
                    Vec::new(),
                ),
            ),
            (
                "propose".to_owned(),
                match review {
                    None => refused("a proposal"),
                    Some(permission) => said(
                        Some(format!("Use to propose a change of the own fields, as a JSON merge patch, for a caller with permission {permission} to approve. evidence cites instances the proposer may read, each optionally at a revision it has had.")),
                        text("Do not use to change the instance at once; call update."),
                        text("Returns the pending proposal with its id and its evidence; the instance does not change until it is approved."),
                        Vec::new(),
                    ),
                },
            ),
            (
                "approve".to_owned(),
                match review {
                    None => refused("an approval"),
                    Some(permission) => said(
                        Some(format!("Use to apply a pending proposal; it needs permission {permission}, else the call is forbidden.")),
                        None,
                        text("Returns the proposal, approved, with the revision its patch made."),
                        vec![pending()],
                    ),
                },
            ),
            (
                "reject".to_owned(),
                match review {
                    None => refused("a rejection"),
                    Some(permission) => said(
                        Some(format!("Use to turn a pending proposal down, with an optional reason; it needs permission {permission}, else the call is forbidden.")),
                        None,
                        text("Returns the proposal, rejected; the instance does not change."),
                        vec![pending()],
                    ),
                },
            ),
            (
                "listProposals".to_owned(),
                match review {
                    None => refused("listing proposals"),
                    Some(_) => said(
                        text("Use to read the instance's proposals, oldest first, each with the evidence it cites; with state, only the pending, approved or rejected ones."),
                        None,
                        text(PAGE),
                        Vec::new(),
                    ),
                },
            ),
            (
                "update".to_owned(),
                OperationGuidance {
                    success: text("A change of the own fields records the next revision."),
                    ..OperationGuidance::default()
                },
            ),
        ],
    }
}
