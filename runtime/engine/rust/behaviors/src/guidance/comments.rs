//! Comments' guidance: a conversation on each instance, top-level messages
//! and one level of replies, every write beside the instance, so it never
//! moves the instance's ETag and an active lease does not stop it. What it
//! says follows the config: the longest body, the edit window, the block
//! kinds allowed, and who moderates, pins and resolves.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{duration, list, refusal, said};
use crate::comments::CommentsConfig;

const BESIDE: &str = "It is beside the instance: the instance's seq and ETag do not move, and a lease held by another does not stop it.";

pub(crate) fn guidance(config: &CommentsConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let type_name = &target.type_name;
    let text = |text: &str| Some(text.to_owned());
    let no_message = || {
        refusal(
            "no_message",
            None,
            "Read the conversation with listMessages for the ids it has; a purged message is gone.",
        )
    };
    let deleted = || {
        refusal(
            "deleted",
            None,
            "None: a deleted message takes no edit, reaction or pin.",
        )
    };
    let not_a_thread = || {
        refusal(
            "not_a_thread",
            None,
            "Name the top-level message, the reply's thread, instead.",
        )
    };
    let moderator = config.moderate_permission.as_deref();
    let not_author = |what: &str| {
        refusal(
            "not_author",
            None,
            &match moderator {
                Some(permission) => {
                    format!("Only the author {what}, or a caller with permission {permission}.")
                }
                None => format!("Only the author {what}."),
            },
        )
    };
    let kinds = match &config.blocks {
        None => "ref and action blocks".to_owned(),
        Some(kinds) if kinds.is_empty() => "no blocks".to_owned(),
        Some(kinds) => format!("{} blocks", list(kinds, "and")),
    };
    let body = format!(
        "a Markdown body of at most {} characters",
        config.max_body_length
    );
    let mut blocks_refused = vec![refusal(
        "ref_not_found",
        None,
        "Refer to an instance that exists in the namespace and that the caller may read.",
    )];
    if config.blocks.is_some() {
        blocks_refused.insert(
            0,
            refusal(
                "block_not_allowed",
                None,
                &format!("Give only {kinds}; the config allows no other."),
            ),
        );
    }
    let window = config.edit_window_ms.map(|ms| {
        format!(
            " An author edits a message for {} after posting it{}.",
            duration(ms),
            moderator.map_or(String::new(), |permission| format!(
                "; a caller with permission {permission} at any time"
            ))
        )
    });
    let mut edit_errors = vec![no_message(), deleted(), not_author("edits a message")];
    if config.edit_window_ms.is_some() {
        edit_errors.push(refusal(
            "edit_window_passed",
            None,
            "None: the message is past its edit window; post a new one.",
        ));
    }
    edit_errors.push(refusal(
        "too_many_edits",
        None,
        "None: the message has had 500 edits; post a new one.",
    ));
    edit_errors.extend(blocks_refused.clone());
    let mut post_errors = vec![
        no_message(),
        not_a_thread(),
        refusal(
            "not_in_thread",
            None,
            "Quote the thread's top-level message or one of its replies.",
        ),
        refusal("deleted", None, "Quote a message that is not deleted."),
    ];
    post_errors.extend(blocks_refused);
    let resolver = match config.resolve_permission.as_deref() {
        Some(permission) => format!("its author or a caller with permission {permission}"),
        None => "its author".to_owned(),
    };
    let pinner = config.pin_permission.as_deref().map(|permission| {
        format!(" It needs permission {permission}, else the call is forbidden.")
    });
    let summary = format!(
        "Each {type_name} holds a conversation: messages, each top-level or a reply in the thread of a top-level one (one level deep), with {body}, {kinds} and mentions. Every write is beside the instance, so its seq and ETag do not move and a lease held by another does not stop it. behaviors.Comments.messages counts the posted messages, lastMessageAt says when the latest was posted, and threads counts the open and resolved threads; none of them moves in an event, so read the instance for them."
    );
    BehaviorGuidance {
        summary,
        operations: vec![
            (
                "post".to_owned(),
                said(
                    Some(format!("Use to post a message as the caller, top-level or with thread a reply in a top-level message's thread; quotes names a message of that thread. Give clientId to make a retry safe: a post with a clientId the caller has used returns that message and posts nothing. {BESIDE}")),
                    text("Do not use to change the instance's fields; call update."),
                    text("Returns the message's record with its id, which a reply names as thread."),
                    post_errors,
                ),
            ),
            (
                "edit".to_owned(),
                said(
                    Some(format!("Use to replace a message's body, blocks or mentions; the members not given stay.{}", window.clone().unwrap_or_default())),
                    None,
                    text("Returns the record after the edit; listEdits reads what it held before."),
                    edit_errors,
                ),
            ),
            (
                "deleteMessage".to_owned(),
                said(
                    text("Use to delete a message: its body, blocks, mentions, reactions and edits go, and its id and its place in the thread stay, as do its replies."),
                    text("Do not use to delete the instance; call delete."),
                    text("Returns the record, state deleted."),
                    vec![no_message(), refusal("deleted", None, "None: it is deleted already."), not_author("deletes a message")],
                ),
            ),
            (
                "purge".to_owned(),
                match moderator {
                    None => said(
                        None,
                        Some(format!("Do not use: Comments on {type_name} names no moderatePermission, so nobody purges (not_configured).")),
                        None,
                        vec![refusal("not_configured", None, "None on this type: delete the message with deleteMessage instead.")],
                    ),
                    Some(permission) => said(
                        Some(format!("Use to remove a message for good, and a top-level message's whole thread with it; it needs permission {permission}, else the call is forbidden.")),
                        text("Do not use to take a message down while keeping its place; call deleteMessage."),
                        text("Returns message and purged, every id removed; the ids are not reused."),
                        vec![no_message()],
                    ),
                },
            ),
            (
                "react".to_owned(),
                said(
                    text("Use to react to a message with an emoji, a name or the character itself, as the caller."),
                    None,
                    text("Returns the record; reactions holds each emoji's count and whether the caller is one of them (mine)."),
                    vec![
                        no_message(),
                        deleted(),
                        refusal("already_reacted", None, "None: the caller has reacted with it already."),
                        refusal("too_many_reactions", None, "React with one of the 20 emoji the message holds."),
                    ],
                ),
            ),
            (
                "unreact".to_owned(),
                said(
                    text("Use to take back the caller's reaction to a message."),
                    None,
                    text("Returns the record."),
                    vec![no_message(), deleted(), refusal("not_reacted", None, "None: the caller holds no reaction with that emoji.")],
                ),
            ),
            (
                "pin".to_owned(),
                said(
                    Some(format!("Use to pin a message, top-level or a reply.{}", pinner.clone().unwrap_or_default())),
                    None,
                    text("Returns the record, pinned."),
                    vec![no_message(), deleted(), refusal("already_pinned", None, "None: it is pinned already.")],
                ),
            ),
            (
                "unpin".to_owned(),
                said(
                    Some(format!("Use to unpin a pinned message.{}", pinner.unwrap_or_default())),
                    None,
                    text("Returns the record."),
                    vec![no_message(), refusal("not_pinned", None, "None: it is not pinned.")],
                ),
            ),
            (
                "resolve".to_owned(),
                said(
                    Some(format!("Use to resolve a thread, named by its top-level message; {resolver} may. A reply to it later leaves it resolved.")),
                    None,
                    text("Returns the top-level record with resolved, who and when."),
                    vec![no_message(), not_a_thread(), deleted(), refusal("not_author", None, &format!("Only {resolver} resolves the thread.")), refusal("already_resolved", None, "None: it is resolved already.")],
                ),
            ),
            (
                "reopen".to_owned(),
                said(
                    Some(format!("Use to reopen a resolved thread; {resolver} may.")),
                    None,
                    text("Returns the top-level record, resolved gone."),
                    vec![no_message(), not_a_thread(), deleted(), refusal("not_author", None, &format!("Only {resolver} reopens the thread.")), refusal("not_resolved", None, "None: it is open.")],
                ),
            ),
            (
                "listMessages".to_owned(),
                said(
                    text("Use to read the conversation a page at a time, in ascending id order: the top-level messages, or with thread a thread's replies and its top-level message as root. With no anchor it reads the latest page; before reads older, after newer, around a page about one message."),
                    text("Do not page through it for one message; call getMessage."),
                    text("Returns items, before and after; pass before as before for the older page and after as after for the newer, each null at its end."),
                    vec![no_message(), not_a_thread()],
                ),
            ),
            (
                "getMessage".to_owned(),
                said(
                    text("Use to read one message's record by its id, a deleted one's included."),
                    None,
                    None,
                    vec![no_message()],
                ),
            ),
            (
                "listEdits".to_owned(),
                OperationGuidance {
                    use_when: text("Use to read what a message held before each of its edits, oldest first, with who made each edit and when."),
                    success: text("Returns items and next; pass next as cursor for the page after, until it is null."),
                    ..OperationGuidance::default()
                },
            ),
            (
                "listReactions".to_owned(),
                OperationGuidance {
                    use_when: text("Use to read who reacted to a message with one emoji, in the order they reacted."),
                    success: text("Returns items and next; pass next as cursor for the page after, until it is null."),
                    ..OperationGuidance::default()
                },
            ),
        ],
    }
}
