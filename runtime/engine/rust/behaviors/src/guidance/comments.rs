//! Comments' guidance: a conversation on each instance, top-level messages
//! and one level of replies, every write beside the instance, so it never
//! moves the instance's ETag and an active lease does not stop it. What it
//! says follows the config: the longest body, the edit window, the block
//! kinds allowed, who moderates, pins and resolves, and how long a
//! streamed message's author may pause and how many it may write at once.

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
    let composing = || {
        refusal(
            "composing",
            None,
            "Wait for the message's finish or interrupt event: a composing message is still being written.",
        )
    };
    let not_composing = || {
        refusal(
            "not_composing",
            None,
            "None: the message is posted, interrupted or deleted; read it with getMessage.",
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
    let mut edit_errors = vec![
        no_message(),
        deleted(),
        composing(),
        not_author("edits a message"),
    ];
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
    let reply_errors = vec![
        no_message(),
        not_a_thread(),
        refusal(
            "not_in_thread",
            None,
            "Quote the thread's top-level message or one of its replies.",
        ),
        refusal("deleted", None, "Quote a message that is not deleted."),
        refusal(
            "composing",
            None,
            "Reply in, or quote, a message once it is finished: a composing message is still being written.",
        ),
    ];
    let mut post_errors = reply_errors.clone();
    post_errors.extend(blocks_refused.clone());
    let mut begin_errors = reply_errors;
    begin_errors.push(refusal(
        "too_many_drafts",
        None,
        &format!(
            "Finish, interrupt or delete one of the caller's composing messages first; a caller composes at most {} at once.",
            config.max_drafts
        ),
    ));
    let mut finish_errors = vec![
        no_message(),
        not_composing(),
        refusal(
            "not_author",
            None,
            "Only the author who began the message finishes it.",
        ),
    ];
    finish_errors.extend(blocks_refused);
    let timeout = duration(config.compose_timeout_ms);
    let resolver = match config.resolve_permission.as_deref() {
        Some(permission) => format!("its author or a caller with permission {permission}"),
        None => "its author".to_owned(),
    };
    let pinner = config.pin_permission.as_deref().map(|permission| {
        format!(" It needs permission {permission}, else the call is forbidden.")
    });
    let summary = format!(
        "Each {type_name} holds a conversation: messages, each top-level or a reply in the thread of a top-level one (one level deep), with {body}, {kinds} and mentions. A message is posted whole with post, or streamed as it is written: begin, appendDraft, then finish; one whose author appends nothing for {timeout} is interrupted, keeping what was written. Every write is beside the instance, so its seq and ETag do not move and a lease held by another does not stop it. behaviors.Comments.messages counts the posted and interrupted messages, lastMessageAt says when the latest came to stand, threads counts the open and resolved threads, and composing names who is writing a message; none of them moves in an event, so read the instance for them."
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
                "begin".to_owned(),
                said(
                    Some(format!("Use to stream a message as it is written, as the caller: begin holds its place, top-level or with thread a reply, as composing; appendDraft adds text to it and finish posts it. Give clientId to make a retry safe, as for post. {BESIDE}")),
                    text("Do not use for a message written all at once; call post."),
                    text("Returns the message's record, state composing, with its id, which appendDraft, finish and interrupt name."),
                    begin_errors,
                ),
            ),
            (
                "appendDraft".to_owned(),
                said(
                    Some(format!("Use to add text to the draft of a message the caller began, in order; readers with the event stream open with signals receive it as a draft signal once the call commits. An empty text adds nothing and keeps the message from being interrupted: send one at least every {timeout} while working with nothing to show. It appends no event.")),
                    None,
                    text("Returns message, offset and length: the draft's length before the text and after it, in code points."),
                    vec![
                        no_message(),
                        not_composing(),
                        refusal("not_author", None, "Only the author who began the message appends to it."),
                        refusal("draft_too_long", None, &format!("Finish the message, or post the rest as another; a draft holds at most {} characters.", config.max_body_length)),
                    ],
                ),
            ),
            (
                "finish".to_owned(),
                said(
                    text("Use to post a composing message: its draft becomes its body, or body when given replaces it, with blocks and mentions as post takes them."),
                    None,
                    text("Returns the record, state posted."),
                    finish_errors,
                ),
            ),
            (
                "interrupt".to_owned(),
                said(
                    Some(format!("Use to stop a composing message, keeping the draft as its body, state interrupted, or deleting it when the draft has no text. Its author may at any time{}; anyone else once its author has appended nothing for {timeout}, as the engine's runner does.", moderator.map_or(String::new(), |permission| format!(", and a caller with permission {permission}")))),
                    text("Do not use to drop a draft; its author calls deleteMessage."),
                    text("Returns the record, state interrupted, or deleted when the draft had no text."),
                    vec![
                        no_message(),
                        not_composing(),
                        refusal("not_idle", None, "Wait until its author has appended nothing for composeTimeoutMs, or leave it to the runner."),
                    ],
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
                    text("Use to delete a message: its body, blocks, mentions, reactions and edits go, and its id and its place in the thread stay, as do its replies. Its author deletes a composing message to drop its draft."),
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
                        composing(),
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
                    vec![no_message(), deleted(), composing(), refusal("not_reacted", None, "None: the caller holds no reaction with that emoji.")],
                ),
            ),
            (
                "pin".to_owned(),
                said(
                    Some(format!("Use to pin a message, top-level or a reply.{}", pinner.clone().unwrap_or_default())),
                    None,
                    text("Returns the record, pinned."),
                    vec![no_message(), deleted(), composing(), refusal("already_pinned", None, "None: it is pinned already.")],
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
                    vec![no_message(), not_a_thread(), deleted(), composing(), refusal("not_author", None, &format!("Only {resolver} resolves the thread.")), refusal("already_resolved", None, "None: it is resolved already.")],
                ),
            ),
            (
                "reopen".to_owned(),
                said(
                    Some(format!("Use to reopen a resolved thread; {resolver} may.")),
                    None,
                    text("Returns the top-level record, resolved gone."),
                    vec![no_message(), not_a_thread(), deleted(), composing(), refusal("not_author", None, &format!("Only {resolver} reopens the thread.")), refusal("not_resolved", None, "None: it is open.")],
                ),
            ),
            (
                "listMessages".to_owned(),
                said(
                    text("Use to read the conversation a page at a time, in ascending id order: the top-level messages, or with thread a thread's replies and its top-level message as root. With no anchor it reads the latest page; before reads older, after newer, around a page about one message. A composing message is in its place, its body the draft so far."),
                    text("Do not page through it for one message; call getMessage."),
                    text("Returns items, before and after; pass before as before for the older page and after as after for the newer, each null at its end."),
                    vec![no_message(), not_a_thread()],
                ),
            ),
            (
                "getMessage".to_owned(),
                said(
                    text("Use to read one message's record by its id, a deleted one's included; a composing message's body is its draft so far, which draft signals extend."),
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
