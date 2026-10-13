//! Comments' guidance (`guidance/comments.ts`): a thread on each instance,
//! which a caller adds to and pages through. It takes no config, so its
//! text is the same on every type.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::said;

pub(crate) fn guidance(target: &DescribeTarget) -> BehaviorGuidance {
    BehaviorGuidance {
        summary: format!(
            "Each {} holds a thread of comments, each one a reply to another of its comments or not; commentCount counts them.",
            target.type_name
        ),
        operations: vec![
            (
                "comment".to_owned(),
                said(
                    Some("Use to add a comment to the instance, or with replyTo a reply to one of the instance's comments.".to_owned()),
                    Some("Do not use to change the instance's fields; call update.".to_owned()),
                    Some("Returns the comment with its id, which a reply names as replyTo; commentCount grows by one.".to_owned()),
                    Vec::new(),
                ),
            ),
            (
                "listComments".to_owned(),
                OperationGuidance {
                    use_when: Some("Use to read the instance's comments, oldest first, a page at a time.".to_owned()),
                    success: Some("Returns items and next; pass next as cursor for the page after, until it is null.".to_owned()),
                    ..OperationGuidance::default()
                },
            ),
        ],
    }
}
