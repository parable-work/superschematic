//! Comments' writing operations, every one beside the instance (D77
//! primitive 1): post, edit, deleteMessage, purge, react, unreact, pin,
//! unpin, resolve and reopen (`comments-contract.md`, "Operations", as
//! batch 2 amends them; `drafts.rs` holds batch 2's own). Each
//! checks in the order the contract's table lists and stops at the first
//! refusal, writes only Comments' tables and its `count` column, and
//! returns what its event's `result` carries.

use serde_json::{json, Value};

use superschematic_engine::storage::SqlValue;
use superschematic_engine::{OperationCx, Result};

use super::checks::{
    check_action_data, check_blocks, check_body, composing, deleted, forbidden, holds, integer,
    invalid, moderates, not_a_thread, not_author, object, present, veto, MAX_EDITS, MAX_EMOJI,
};
use super::record::record_of;
use super::storage::{
    conversation, key, keyed, move_counts, placeholders, save_conversation, Message, Moves,
    CONVERSATIONS, DELETED, DRAFTS, EDITS, MESSAGES, MESSAGE_COLUMNS, POSTED, REACTIONS,
};
use super::CommentsConfig;

pub(super) type Cx<'a, 'b> = &'a OperationCx<'b, CommentsConfig>;

// compact writes a JSON array as stored: `[]` when absent.
pub(super) fn compact(value: Option<&Value>) -> String {
    value.map_or_else(|| "[]".to_owned(), superschematic_engine::json::stringify)
}

/// The record of a message after a write, read again.
pub(super) fn after(cx: Cx<'_, '_>, operation: &str, id: i64) -> Result<Value> {
    let message = present(cx, operation, id)?;
    record_of(cx, &message)
}

/// A retry: the message of the instance the caller made with `clientId`,
/// in any state, as it is now, when the call gives one.
pub(super) fn retried(cx: Cx<'_, '_>, client_id: Option<&str>) -> Result<Option<Value>> {
    let Some(client_id) = client_id else {
        return Ok(None);
    };
    let table = cx.sql().table(MESSAGES)?;
    let found = cx.sql().get(
        format!(
            "SELECT {MESSAGE_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND author = ? AND client_id = ?"
        ),
        &keyed(
            cx,
            [
                SqlValue::from(cx.principal().subject.as_str()),
                SqlValue::from(client_id),
            ],
        ),
    )?;
    match found {
        Some(row) => {
            cx.unchanged();
            Ok(Some(record_of(cx, &Message::of(&row)?)?))
        }
        None => Ok(None),
    }
}

/// The thread and quotes a post or a begin names: the thread present,
/// top-level and not composing; the message it quotes present, in the
/// thread, not deleted and not composing.
pub(super) fn check_reply(
    cx: Cx<'_, '_>,
    operation: &str,
    thread: Option<i64>,
    quotes: Option<i64>,
) -> Result<()> {
    let Some(thread) = thread else {
        return Ok(());
    };
    let top = present(cx, operation, thread)?;
    if !top.top_level() {
        return Err(not_a_thread(cx, operation, &top));
    }
    if top.composing() {
        return Err(composing(cx, operation, thread));
    }
    if let Some(quotes) = quotes {
        let quoted = present(cx, operation, quotes)?;
        if quoted.id != thread && quoted.thread != Some(thread) {
            return Err(veto(
                cx,
                operation,
                "not_in_thread",
                format!(
                    "message {quotes} is not in thread {thread}, which the {operation} replies in"
                ),
                json!({ "message": quotes, "thread": thread }),
            ));
        }
        if quoted.deleted() {
            return Err(deleted(cx, operation, quotes));
        }
        if quoted.composing() {
            return Err(composing(cx, operation, quotes));
        }
    }
    Ok(())
}

/// The id of the message a post or a begin makes: the conversation's next,
/// the row inserted when the instance has none.
pub(super) fn next_message(cx: Cx<'_, '_>) -> Result<(i64, bool, super::storage::Conversation)> {
    let found = conversation(cx)?;
    let existed = found.is_some();
    let mut conversation = found.unwrap_or_default();
    conversation.last_message += 1;
    Ok((conversation.last_message, existed, conversation))
}

/// The calling service's deployable, when a service calls.
pub(super) fn via<'a>(cx: Cx<'a, '_>) -> Option<&'a str> {
    cx.principal()
        .service
        .as_ref()
        .map(|service| service.deployable.as_str())
}

pub(super) fn post(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "post";
    let subject = cx.principal().subject.as_str();
    let body = params
        .get("body")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let blocks = params.get("blocks");
    let mentions = params.get("mentions");
    let thread = integer(params.get("thread"));
    let quotes = integer(params.get("quotes"));
    let client_id = params.get("clientId").and_then(Value::as_str);
    let table = cx.sql().table(MESSAGES)?;
    // 1: a retry of a post (or a begin) returns its message as it is now.
    if let Some(record) = retried(cx, client_id)? {
        return Ok(record);
    }
    check_body(cx, OP, Some(body))?;
    if quotes.is_some() && thread.is_none() {
        return Err(invalid(OP, "/quotes", "needs thread"));
    }
    check_action_data(OP, blocks)?;
    check_reply(cx, OP, thread, quotes)?;
    check_blocks(cx, OP, blocks, None)?;

    let (id, existed, mut conversation) = next_message(cx)?;
    let now = cx.now();
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, message, thread, quotes, author, via, state, body, blocks, mentions, created_at, posted_at, edited_at, edits, pinned, client_id, resolved_by, resolved_at, draft_length, last_append_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 0, 0, ?, NULL, NULL, NULL, NULL)"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(id),
                thread.map_or(SqlValue::Null, SqlValue::Integer),
                quotes.map_or(SqlValue::Null, SqlValue::Integer),
                SqlValue::from(subject),
                via(cx).map_or(SqlValue::Null, SqlValue::from),
                SqlValue::from(POSTED),
                SqlValue::from(body),
                SqlValue::from(compact(blocks)),
                SqlValue::from(compact(mentions)),
                SqlValue::Integer(now),
                SqlValue::Integer(now),
                client_id.map_or(SqlValue::Null, SqlValue::from),
            ],
        ),
    )?;
    let moves = Moves::standing(thread.is_none(), now);
    move_counts(cx, &mut conversation, &moves)?;
    save_conversation(cx, existed, &conversation)?;
    after(cx, OP, id)
}

pub(super) fn edit(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "edit";
    let id = integer(params.get("message")).unwrap_or_default();
    let body = params.get("body").and_then(Value::as_str);
    let blocks = params.get("blocks");
    let mentions = params.get("mentions");
    if body.is_none() && blocks.is_none() && mentions.is_none() {
        return Err(invalid(OP, "", "names none of body, blocks and mentions"));
    }
    check_body(cx, OP, body)?;
    check_action_data(OP, blocks)?;
    let message = present(cx, OP, id)?;
    if message.deleted() {
        return Err(deleted(cx, OP, id));
    }
    if message.composing() {
        return Err(composing(cx, OP, id));
    }
    let subject = cx.principal().subject.as_str();
    let mut moderator: Option<bool> = None;
    let mut moderating = || -> Result<bool> {
        if let Some(known) = moderator {
            return Ok(known);
        }
        let known = moderates(cx)?;
        moderator = Some(known);
        Ok(known)
    };
    if message.author != subject && !moderating()? {
        return Err(not_author(cx, OP, &message));
    }
    if let Some(window) = cx.config().edit_window_ms {
        // A standing message has a postedAt; its createdAt stands in for
        // one that has none, which no write leaves.
        let posted_at = message.posted_at.unwrap_or(message.created_at);
        if cx.now() - posted_at > window && !moderating()? {
            return Err(veto(
                cx,
                OP,
                "edit_window_passed",
                format!(
                    "message {id} was posted at {posted_at}, more than editWindowMs ({window}) ago"
                ),
                json!({ "message": id, "postedAt": posted_at, "editWindowMs": window }),
            ));
        }
    }
    if message.edits >= MAX_EDITS {
        return Err(veto(
            cx,
            OP,
            "too_many_edits",
            format!("message {id} has had {MAX_EDITS} edits, the most a message has"),
            json!({ "message": id, "max": MAX_EDITS }),
        ));
    }
    if blocks.is_some() {
        check_blocks(cx, OP, blocks, Some(&message.blocks))?;
    }

    let now = cx.now();
    let edits = cx.sql().table(EDITS)?;
    cx.sql().run(
        format!(
            "INSERT INTO {edits} (namespace, schema, id, message, edit, body, blocks, mentions, edited_by, edited_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(id),
                SqlValue::Integer(message.edits + 1),
                SqlValue::from(message.body.as_str()),
                SqlValue::from(compact(Some(&message.blocks))),
                SqlValue::from(compact(Some(&message.mentions))),
                SqlValue::from(subject),
                SqlValue::Integer(now),
            ],
        ),
    )?;
    // An interrupted message its author edits is posted: the author has
    // said what it meant. A moderator's edit of another's leaves it.
    let state = if message.author == subject {
        POSTED
    } else {
        message.state.as_str()
    };
    let table = cx.sql().table(MESSAGES)?;
    cx.sql().run(
        format!(
            "UPDATE {table} SET state = ?, body = ?, blocks = ?, mentions = ?, edited_at = ?, edits = ?
             WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &[
            SqlValue::from(state),
            SqlValue::from(body.unwrap_or(message.body.as_str())),
            SqlValue::from(compact(Some(blocks.unwrap_or(&message.blocks)))),
            SqlValue::from(compact(Some(mentions.unwrap_or(&message.mentions)))),
            SqlValue::Integer(now),
            SqlValue::Integer(message.edits + 1),
        ]
        .into_iter()
        .chain(keyed(cx, [SqlValue::Integer(id)]))
        .collect::<Vec<_>>(),
    )?;
    after(cx, OP, id)
}

/// Deletes the edits, reactions and draft chunks of the messages.
pub(super) fn remove_edits_and_reactions(cx: Cx<'_, '_>, ids: &[i64]) -> Result<()> {
    for table in [EDITS, REACTIONS, DRAFTS] {
        let table = cx.sql().table(table)?;
        cx.sql().run(
            format!(
                "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND message IN ({})",
                placeholders(ids.len())
            ),
            &keyed(cx, ids.iter().map(|&id| SqlValue::Integer(id))),
        )?;
    }
    Ok(())
}

pub(super) fn delete_message(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "deleteMessage";
    let id = integer(params.get("message")).unwrap_or_default();
    let message = present(cx, OP, id)?;
    if message.deleted() {
        return Err(deleted(cx, OP, id));
    }
    if message.author != cx.principal().subject && !moderates(cx)? {
        return Err(not_author(cx, OP, &message));
    }
    remove_edits_and_reactions(cx, &[id])?;
    let table = cx.sql().table(MESSAGES)?;
    // It keeps posted_at, as it keeps created_at; a composing message
    // leaves composing.
    cx.sql().run(
        format!(
            "UPDATE {table} SET state = ?, body = '', blocks = '[]', mentions = '[]', edited_at = NULL, edits = 0, pinned = 0, draft_length = NULL, last_append_at = NULL
             WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &[SqlValue::from(DELETED)]
            .into_iter()
            .chain(keyed(cx, [SqlValue::Integer(id)]))
            .collect::<Vec<_>>(),
    )?;
    let mut moves = Moves::default();
    moves.leaving(&message);
    let mut conversation = conversation(cx)?.unwrap_or_default();
    move_counts(cx, &mut conversation, &moves)?;
    save_conversation(cx, true, &conversation)?;
    after(cx, OP, id)
}

pub(super) fn purge(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "purge";
    let id = integer(params.get("message")).unwrap_or_default();
    let Some(permission) = cx.config().moderate_permission.as_deref() else {
        return Err(veto(
            cx,
            OP,
            "not_configured",
            format!(
                "Comments on {} names no moderatePermission, so nobody purges",
                cx.schema()
            ),
            json!({ "permission": "moderatePermission" }),
        ));
    };
    if !cx.can(permission)? {
        return Err(forbidden(cx, OP, permission));
    }
    present(cx, OP, id)?;
    // The message, and for a top-level one every reply of its thread: a
    // reply's id is no reply's thread.
    let table = cx.sql().table(MESSAGES)?;
    let rows = cx.sql().all(
        format!(
            "SELECT {MESSAGE_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND (message = ? OR thread = ?)
             ORDER BY message"
        ),
        &keyed(cx, [SqlValue::Integer(id), SqlValue::Integer(id)]),
    )?;
    let gone = rows.iter().map(Message::of).collect::<Result<Vec<_>>>()?;
    let ids: Vec<i64> = gone.iter().map(|message| message.id).collect();
    remove_edits_and_reactions(cx, &ids)?;
    cx.sql().run(
        format!(
            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND message IN ({})",
            placeholders(ids.len())
        ),
        &keyed(cx, ids.iter().map(|&id| SqlValue::Integer(id))),
    )?;
    let mut moves = Moves::default();
    for message in &gone {
        moves.leaving(message);
    }
    let mut conversation = conversation(cx)?.unwrap_or_default();
    move_counts(cx, &mut conversation, &moves)?;
    save_conversation(cx, true, &conversation)?;
    Ok(object(vec![
        ("message", Value::from(id)),
        (
            "purged",
            Value::Array(ids.into_iter().map(Value::from).collect()),
        ),
    ]))
}

// reacted is whether the caller holds a reaction with the emoji on the
// message.
fn reacted(cx: Cx<'_, '_>, id: i64, emoji: &str) -> Result<bool> {
    let table = cx.sql().table(REACTIONS)?;
    Ok(cx
        .sql()
        .get(
            format!(
                "SELECT 1 AS held FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND emoji = ? AND subject = ?"
            ),
            &keyed(
                cx,
                [
                    SqlValue::Integer(id),
                    SqlValue::from(emoji),
                    SqlValue::from(cx.principal().subject.as_str()),
                ],
            ),
        )?
        .is_some())
}

// reactable is the message a reaction names: present and not deleted.
fn reactable(cx: Cx<'_, '_>, operation: &str, params: &Value) -> Result<(i64, String)> {
    let id = integer(params.get("message")).unwrap_or_default();
    let emoji = params
        .get("emoji")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    let message = present(cx, operation, id)?;
    if message.deleted() {
        return Err(deleted(cx, operation, id));
    }
    if message.composing() {
        return Err(composing(cx, operation, id));
    }
    Ok((id, emoji))
}

pub(super) fn react(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "react";
    let (id, emoji) = reactable(cx, OP, params)?;
    if reacted(cx, id, &emoji)? {
        return Err(veto(
            cx,
            OP,
            "already_reacted",
            format!(
                "{} has reacted to message {id} with {emoji} already",
                cx.principal().subject
            ),
            json!({ "message": id, "emoji": emoji }),
        ));
    }
    let table = cx.sql().table(REACTIONS)?;
    let held = cx.sql().get(
        format!(
            "SELECT COUNT(DISTINCT emoji) AS emoji, COUNT(CASE WHEN emoji = ? THEN 1 END) AS this FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &[SqlValue::from(emoji.as_str())]
            .into_iter()
            .chain(keyed(cx, [SqlValue::Integer(id)]))
            .collect::<Vec<_>>(),
    )?;
    let (distinct, this) = match held {
        Some(row) => (row.i64("emoji")?, row.i64("this")?),
        None => (0, 0),
    };
    if distinct >= MAX_EMOJI && this == 0 {
        return Err(veto(
            cx,
            OP,
            "too_many_reactions",
            format!("message {id} has reactions with {MAX_EMOJI} emoji, the most a message holds"),
            json!({ "message": id, "max": MAX_EMOJI }),
        ));
    }
    let found = conversation(cx)?;
    let existed = found.is_some();
    let mut conversation = found.unwrap_or_default();
    conversation.last_reaction += 1;
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, message, emoji, subject, reaction, reacted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(id),
                SqlValue::from(emoji.as_str()),
                SqlValue::from(cx.principal().subject.as_str()),
                SqlValue::Integer(conversation.last_reaction),
                SqlValue::Integer(cx.now()),
            ],
        ),
    )?;
    save_conversation(cx, existed, &conversation)?;
    after(cx, OP, id)
}

pub(super) fn unreact(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "unreact";
    let (id, emoji) = reactable(cx, OP, params)?;
    if !reacted(cx, id, &emoji)? {
        return Err(veto(
            cx,
            OP,
            "not_reacted",
            format!(
                "{} has not reacted to message {id} with {emoji}",
                cx.principal().subject
            ),
            json!({ "message": id, "emoji": emoji }),
        ));
    }
    let table = cx.sql().table(REACTIONS)?;
    cx.sql().run(
        format!(
            "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND emoji = ? AND subject = ?"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(id),
                SqlValue::from(emoji.as_str()),
                SqlValue::from(cx.principal().subject.as_str()),
            ],
        ),
    )?;
    after(cx, OP, id)
}

// set_pinned pins or unpins a message.
fn set_pinned(cx: Cx<'_, '_>, id: i64, pinned: bool) -> Result<()> {
    let table = cx.sql().table(MESSAGES)?;
    cx.sql().run(
        format!(
            "UPDATE {table} SET pinned = ? WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &[SqlValue::Integer(i64::from(pinned))]
            .into_iter()
            .chain(keyed(cx, [SqlValue::Integer(id)]))
            .collect::<Vec<_>>(),
    )?;
    Ok(())
}

// pinnable checks pin's and unpin's permission, and finds the message.
fn pinnable(cx: Cx<'_, '_>, operation: &str, params: &Value) -> Result<Message> {
    if let Some(permission) = cx.config().pin_permission.as_deref() {
        if !holds(cx, Some(permission))? {
            return Err(forbidden(cx, operation, permission));
        }
    }
    present(
        cx,
        operation,
        integer(params.get("message")).unwrap_or_default(),
    )
}

pub(super) fn pin(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "pin";
    let message = pinnable(cx, OP, params)?;
    if message.deleted() {
        return Err(deleted(cx, OP, message.id));
    }
    if message.composing() {
        return Err(composing(cx, OP, message.id));
    }
    if message.pinned {
        return Err(veto(
            cx,
            OP,
            "already_pinned",
            format!("message {} is pinned already", message.id),
            json!({ "message": message.id }),
        ));
    }
    set_pinned(cx, message.id, true)?;
    after(cx, OP, message.id)
}

pub(super) fn unpin(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "unpin";
    let message = pinnable(cx, OP, params)?;
    if !message.pinned {
        return Err(veto(
            cx,
            OP,
            "not_pinned",
            format!("message {} is not pinned", message.id),
            json!({ "message": message.id }),
        ));
    }
    set_pinned(cx, message.id, false)?;
    after(cx, OP, message.id)
}

// thread_for checks resolve's and reopen's thread: present, top-level, not
// deleted, not composing, and the caller its author or one who resolves.
fn thread_for(cx: Cx<'_, '_>, operation: &str, params: &Value) -> Result<Message> {
    let id = integer(params.get("thread")).unwrap_or_default();
    let top = present(cx, operation, id)?;
    if !top.top_level() {
        return Err(not_a_thread(cx, operation, &top));
    }
    if top.deleted() {
        return Err(deleted(cx, operation, id));
    }
    if top.composing() {
        return Err(composing(cx, operation, id));
    }
    if top.author != cx.principal().subject
        && !holds(cx, cx.config().resolve_permission.as_deref())?
    {
        return Err(not_author(cx, operation, &top));
    }
    Ok(top)
}

// set_resolved resolves a thread as the caller now, or reopens it, and
// moves its count.
fn set_resolved(cx: Cx<'_, '_>, top: &Message, resolved: bool) -> Result<()> {
    let table = cx.sql().table(MESSAGES)?;
    let (by, at) = if resolved {
        (
            SqlValue::from(cx.principal().subject.as_str()),
            SqlValue::Integer(cx.now()),
        )
    } else {
        (SqlValue::Null, SqlValue::Null)
    };
    cx.sql().run(
        format!(
            "UPDATE {table} SET resolved_by = ?, resolved_at = ? WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &[by, at]
            .into_iter()
            .chain(keyed(cx, [SqlValue::Integer(top.id)]))
            .collect::<Vec<_>>(),
    )?;
    let step = if resolved { 1 } else { -1 };
    let moves = Moves {
        open: -step,
        resolved: step,
        ..Moves::default()
    };
    let mut conversation = conversation(cx)?.unwrap_or_default();
    move_counts(cx, &mut conversation, &moves)?;
    save_conversation(cx, true, &conversation)
}

pub(super) fn resolve(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "resolve";
    let top = thread_for(cx, OP, params)?;
    if let (Some(by), Some(at)) = (&top.resolved_by, top.resolved_at) {
        return Err(veto(
            cx,
            OP,
            "already_resolved",
            format!("thread {} was resolved by {by} at {at}", top.id),
            json!({ "thread": top.id, "by": by, "at": at }),
        ));
    }
    set_resolved(cx, &top, true)?;
    after(cx, OP, top.id)
}

pub(super) fn reopen(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "reopen";
    let top = thread_for(cx, OP, params)?;
    if top.resolved_at.is_none() {
        return Err(veto(
            cx,
            OP,
            "not_resolved",
            format!("thread {} is not resolved", top.id),
            json!({ "thread": top.id }),
        ));
    }
    set_resolved(cx, &top, false)?;
    after(cx, OP, top.id)
}

/// Deletes the instance's messages, edits, reactions and drafts, and empties its
/// conversation row but for its high-water marks: an instance re-created
/// with the id numbers its messages and reactions on from them, so no id
/// is reused across the lives one instance's events span.
pub(super) fn delete_conversation(
    cx: &superschematic_engine::behaviors::InstanceCx<'_, CommentsConfig>,
) -> Result<()> {
    for table in [MESSAGES, EDITS, REACTIONS, DRAFTS] {
        let table = cx.sql().table(table)?;
        cx.sql().run(
            format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
            &key(cx),
        )?;
    }
    let table = cx.sql().table(CONVERSATIONS)?;
    cx.sql().run(
        format!(
            "UPDATE {table} SET last_message_at = NULL, open_threads = 0, resolved_threads = 0
             WHERE namespace = ? AND schema = ? AND id = ?"
        ),
        &key(cx),
    )?;
    Ok(())
}
