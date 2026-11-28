//! Comments' writing operations, every one beside the row (D77's primitive
//! 1): post, edit, deleteMessage, purge, react, unreact, pin, unpin,
//! resolve and reopen (`comments-contract.md`, "Operations", as batches 2
//! to 5 amend them: a composing message refused, an interrupted one edited
//! as a posted one, the drafts, deliveries and follows a delete and a purge
//! drop). Each checks in the order the contract's table lists and stops at
//! the first refusal, writes only Comments' sub-tables and its
//! conversation's shared row, and returns the record after, as the
//! engine's event carried it.

use serde_json::{json, Map, Value};
use superschematic_rules::{Cx, Hook};

use super::checks::{
    check_block_shapes, check_blocks, check_body, composing, deleted, for_of, forbidden, holds,
    invalid, moderates, not_a_thread, not_author, object, present, veto, via_of, whole, MAX_EDITS,
    MAX_EMOJI,
};
use super::drafts::{clear_idle, remove_drafts};
use super::follows;
use super::messages::{
    edit_key, edits_of, message_key, messages_of, reaction_key, reactions_of, Conversation,
    Message, Talk, DELETED, POSTED,
};
use super::{CommentsConfig, EDITS, MESSAGES, REACTIONS};

pub(super) const POST: &str = "post";
pub(super) const EDIT: &str = "edit";
pub(super) const DELETE_MESSAGE: &str = "deleteMessage";
pub(super) const PURGE: &str = "purge";
pub(super) const REACT: &str = "react";
pub(super) const UNREACT: &str = "unreact";
pub(super) const PIN: &str = "pin";
pub(super) const UNPIN: &str = "unpin";
pub(super) const RESOLVE: &str = "resolve";
pub(super) const REOPEN: &str = "reopen";

type C<'a, 'r> = &'a Cx<'r, CommentsConfig>;

/// The record of a message after the run's writes, read again.
pub(super) fn after(cx: C<'_, '_>, id: i64) -> Hook<Value> {
    let talk = Talk::of(cx)?;
    let messages = &talk.messages;
    let message = present(cx, messages, id)?;
    Ok(talk.record(&message, &cx.caller().subject))
}

/// A list member as a write keeps it: as given, `[]` when absent.
pub(super) fn listed(value: Option<&Value>) -> Value {
    value.cloned().unwrap_or_else(|| Value::Array(Vec::new()))
}

/// A retry of a post or a begin: the record, as it is now, of the present
/// message of any state its caller posted or began with the clientId, which
/// writes nothing; `None` for none.
pub(super) fn retried(cx: C<'_, '_>, client_id: Option<&str>) -> Hook<Option<Value>> {
    let Some(client_id) = client_id else {
        return Ok(None);
    };
    let talk = Talk::of(cx)?;
    let subject = &cx.caller().subject;
    Ok(talk
        .messages
        .iter()
        .find(|message| {
            message.author == *subject && message.client_id.as_deref() == Some(client_id)
        })
        .map(|found| talk.record(found, subject)))
}

/// The thread and quotes a post or a begin names: the thread present,
/// top-level and not composing; the message it quotes present, in the
/// thread, not deleted and not composing.
pub(super) fn check_reply(
    cx: C<'_, '_>,
    messages: &[Message],
    thread: Option<i64>,
    quotes: Option<i64>,
) -> Hook<()> {
    let Some(thread) = thread else {
        return Ok(());
    };
    let top = present(cx, messages, thread)?;
    if !top.top_level() {
        return Err(not_a_thread(cx, &top));
    }
    if top.composing() {
        return Err(composing(cx, thread));
    }
    if let Some(quotes) = quotes {
        let quoted = present(cx, messages, quotes)?;
        if quoted.id != thread && quoted.thread != Some(thread) {
            return Err(veto(
                cx,
                "not_in_thread",
                format!("message {quotes} is not in thread {thread}, which the post replies in"),
                json!({ "message": quotes, "thread": thread }),
            ));
        }
        if quoted.deleted() {
            return Err(deleted(cx, quotes));
        }
        if quoted.composing() {
            return Err(composing(cx, quotes));
        }
    }
    Ok(())
}

pub(super) fn post(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let subject = cx.caller().subject.clone();
    let body = params
        .get("body")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let blocks = params.get("blocks");
    let mentions = params.get("mentions");
    let thread = params.get("thread").and_then(whole);
    let quotes = params.get("quotes").and_then(whole);
    let client_id = params.get("clientId").and_then(Value::as_str);
    let messages = messages_of(cx)?;
    // 1: a retry of a post (or a begin) returns its message as it is now,
    // and writes nothing.
    if let Some(record) = retried(cx, client_id)? {
        return Ok(record);
    }
    check_body(cx, POST, Some(body))?;
    if quotes.is_some() && thread.is_none() {
        return Err(invalid(cx, POST, "/quotes", "needs thread"));
    }
    check_block_shapes(cx, POST, blocks)?;
    check_reply(cx, &messages, thread, quotes)?;
    check_blocks(cx, blocks, None)?;

    let mut conversation = Conversation::of(cx)?;
    conversation.last_message += 1;
    conversation.last_stand += 1;
    let id = conversation.last_message;
    let now = cx.now();
    let mut values = Map::new();
    if let Some(thread) = thread {
        values.insert("thread".into(), Value::from(thread));
    }
    if let Some(quotes) = quotes {
        values.insert("quotes".into(), Value::from(quotes));
    }
    values.insert("author".into(), Value::from(subject.as_str()));
    if let Some(acted_for) = for_of(cx) {
        values.insert("forSubject".into(), Value::from(acted_for));
    }
    if let Some(via) = via_of(cx) {
        values.insert("via".into(), Value::from(via));
    }
    values.insert("state".into(), Value::from(POSTED));
    values.insert("body".into(), Value::from(body));
    values.insert("blocks".into(), listed(blocks));
    values.insert("mentions".into(), listed(mentions));
    values.insert("createdAt".into(), Value::from(now));
    values.insert("postedAt".into(), Value::from(now));
    values.insert("stand".into(), Value::from(conversation.last_stand));
    values.insert("edits".into(), Value::from(0));
    values.insert("pinned".into(), Value::from(false));
    if let Some(client_id) = client_id {
        values.insert("clientId".into(), Value::from(client_id));
    }
    let stand = conversation.last_stand;
    cx.insert_row(MESSAGES, &message_key(id), values)?;
    conversation.save(cx)?;
    let posted = messages_of(cx)?
        .into_iter()
        .find(|message| message.id == id)
        .ok_or_else(|| cx.defect("a post's message is not in the run"))?;
    follows::came_to_stand(cx, &posted, stand)?;
    after(cx, id)
}

pub(super) fn edit(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let id = params.get("message").and_then(whole).unwrap_or_default();
    let body = params.get("body").and_then(Value::as_str);
    let blocks = params.get("blocks");
    let mentions = params.get("mentions");
    if body.is_none() && blocks.is_none() && mentions.is_none() {
        return Err(invalid(
            cx,
            EDIT,
            "",
            "names none of body, blocks and mentions",
        ));
    }
    check_body(cx, EDIT, body)?;
    check_block_shapes(cx, EDIT, blocks)?;
    let messages = messages_of(cx)?;
    let message = present(cx, &messages, id)?;
    if message.deleted() {
        return Err(deleted(cx, id));
    }
    if message.composing() {
        return Err(composing(cx, id));
    }
    let subject = cx.caller().subject.clone();
    // Whether the caller moderates, asked once and only when it decides.
    let mut moderator: Option<bool> = None;
    let mut moderating = || -> Hook<bool> {
        if let Some(known) = moderator {
            return Ok(known);
        }
        let known = moderates(cx)?;
        moderator = Some(known);
        Ok(known)
    };
    if message.author != subject && !moderating()? {
        return Err(not_author(cx, &message));
    }
    if let Some(window) = cx.config().edit_window_ms {
        let posted_at = message.posted_at.unwrap_or(message.created_at);
        if cx.now() - posted_at > window && !moderating()? {
            return Err(veto(
                cx,
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
            "too_many_edits",
            format!("message {id} has had {MAX_EDITS} edits, the most a message has"),
            json!({ "message": id, "max": MAX_EDITS }),
        ));
    }
    if blocks.is_some() {
        check_blocks(cx, blocks, Some(&message.blocks))?;
    }

    let now = cx.now();
    let number = message.edits + 1;
    let mut kept = Map::new();
    kept.insert("message".into(), Value::from(id));
    kept.insert("number".into(), Value::from(number));
    kept.insert("body".into(), Value::from(message.body.as_str()));
    kept.insert("blocks".into(), message.blocks.clone());
    kept.insert("mentions".into(), message.mentions.clone());
    kept.insert("by".into(), Value::from(subject.as_str()));
    if let Some(acted_for) = for_of(cx) {
        kept.insert("forSubject".into(), Value::from(acted_for));
    }
    kept.insert("at".into(), Value::from(now));
    cx.insert_row(EDITS, &edit_key(id, number), kept)?;
    let mut values = Map::new();
    // An interrupted message its author edits is posted: the author has
    // said what it meant. A moderator's edit of another's leaves it.
    if message.author == subject {
        values.insert("state".into(), Value::from(POSTED));
    }
    if let Some(body) = body {
        values.insert("body".into(), Value::from(body));
    }
    if let Some(blocks) = blocks {
        values.insert("blocks".into(), blocks.clone());
    }
    if let Some(mentions) = mentions {
        values.insert("mentions".into(), mentions.clone());
    }
    values.insert("editedAt".into(), Value::from(now));
    values.insert("edits".into(), Value::from(number));
    cx.update_row(MESSAGES, &message_key(id), values)?;
    Conversation::of(cx)?.save(cx)?;
    if let Some(mentions) = mentions {
        follows::edited_mentions(cx, &message, mentions, body)?;
    }
    after(cx, id)
}

/// Deletes the edits, reactions and draft chunks of the messages, and
/// clears the idle deadlines of the composing ones.
fn remove_edits_and_reactions(cx: C<'_, '_>, messages: &[Message], ids: &[i64]) -> Hook<()> {
    remove_drafts(cx, ids)?;
    clear_idle(cx, messages, ids)?;
    for edit in edits_of(cx)? {
        let edited = edit.value("message").and_then(whole).unwrap_or_default();
        if ids.contains(&edited) {
            cx.delete_row(EDITS, &edit.key)?;
        }
    }
    for reaction in reactions_of(cx)? {
        if ids.contains(&reaction.message) {
            cx.delete_row(REACTIONS, &reaction_key(reaction.number))?;
        }
    }
    Ok(())
}

pub(super) fn delete_message(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let id = params.get("message").and_then(whole).unwrap_or_default();
    let messages = messages_of(cx)?;
    let message = present(cx, &messages, id)?;
    if message.deleted() {
        return Err(deleted(cx, id));
    }
    if message.author != cx.caller().subject && !moderates(cx)? {
        return Err(not_author(cx, &message));
    }
    remove_edits_and_reactions(cx, &messages, &[id])?;
    follows::removed(cx, &[id], &[])?;
    // It keeps its id, thread, quotes, author, createdAt, postedAt, stand,
    // clientId (so a retry of its post returns it) and a top-level
    // message's resolve; a composing message leaves composing.
    let mut values = Map::new();
    values.insert("state".into(), Value::from(DELETED));
    values.insert("body".into(), Value::from(""));
    values.insert("blocks".into(), Value::Array(Vec::new()));
    values.insert("mentions".into(), Value::Array(Vec::new()));
    values.insert("editedAt".into(), Value::Null);
    values.insert("edits".into(), Value::from(0));
    values.insert("pinned".into(), Value::from(false));
    values.insert("draftLength".into(), Value::Null);
    values.insert("lastAppendAt".into(), Value::Null);
    cx.update_row(MESSAGES, &message_key(id), values)?;
    Conversation::of(cx)?.save(cx)?;
    after(cx, id)
}

pub(super) fn purge(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let id = params.get("message").and_then(whole).unwrap_or_default();
    let Some(permission) = cx.config().moderate_permission.clone() else {
        return Err(veto(
            cx,
            "not_configured",
            format!(
                "Comments on {} names no moderatePermission, so nobody purges",
                cx.type_name()
            ),
            json!({ "permission": "moderatePermission" }),
        ));
    };
    if !cx.can(&permission)? {
        return Err(forbidden(cx, PURGE, &permission));
    }
    let messages = messages_of(cx)?;
    present(cx, &messages, id)?;
    // The message, and for a top-level one every reply of its thread: a
    // reply's id is no reply's thread.
    let ids: Vec<i64> = messages
        .iter()
        .filter(|message| message.id == id || message.thread == Some(id))
        .map(|message| message.id)
        .collect();
    remove_edits_and_reactions(cx, &messages, &ids)?;
    let top = messages
        .iter()
        .find(|message| message.id == id)
        .is_some_and(Message::top_level);
    let threads: Vec<i64> = if top { vec![id] } else { Vec::new() };
    follows::removed(cx, &ids, &threads)?;
    for gone in &ids {
        cx.delete_row(MESSAGES, &message_key(*gone))?;
    }
    Conversation::of(cx)?.save(cx)?;
    Ok(object(vec![
        ("message", Value::from(id)),
        (
            "purged",
            Value::Array(ids.into_iter().map(Value::from).collect()),
        ),
    ]))
}

// reactable is the message a reaction names, present and not deleted, and
// its emoji.
fn reactable(cx: C<'_, '_>, params: &Value) -> Hook<(Message, String)> {
    let id = params.get("message").and_then(whole).unwrap_or_default();
    let emoji = params
        .get("emoji")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    let messages = messages_of(cx)?;
    let message = present(cx, &messages, id)?;
    if message.deleted() {
        return Err(deleted(cx, id));
    }
    if message.composing() {
        return Err(composing(cx, id));
    }
    Ok((message, emoji))
}

pub(super) fn react(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let (message, emoji) = reactable(cx, params)?;
    let id = message.id;
    let subject = cx.caller().subject.clone();
    let held: Vec<_> = reactions_of(cx)?
        .into_iter()
        .filter(|reaction| reaction.message == id)
        .collect();
    if held
        .iter()
        .any(|reaction| reaction.emoji == emoji && reaction.subject == subject)
    {
        return Err(veto(
            cx,
            "already_reacted",
            format!("{subject} has reacted to message {id} with {emoji} already"),
            json!({ "message": id, "emoji": emoji }),
        ));
    }
    let mut distinct: Vec<&str> = held
        .iter()
        .map(|reaction| reaction.emoji.as_str())
        .collect();
    distinct.sort_unstable();
    distinct.dedup();
    if distinct.len() >= MAX_EMOJI && !distinct.contains(&emoji.as_str()) {
        return Err(veto(
            cx,
            "too_many_reactions",
            format!("message {id} has reactions with {MAX_EMOJI} emoji, the most a message holds"),
            json!({ "message": id, "max": MAX_EMOJI }),
        ));
    }
    let mut conversation = Conversation::of(cx)?;
    conversation.last_reaction += 1;
    let mut values = Map::new();
    values.insert("message".into(), Value::from(id));
    values.insert("emoji".into(), Value::from(emoji.as_str()));
    values.insert("subject".into(), Value::from(subject.as_str()));
    if let Some(acted_for) = for_of(cx) {
        values.insert("forSubject".into(), Value::from(acted_for));
    }
    values.insert("at".into(), Value::from(cx.now()));
    cx.insert_row(REACTIONS, &reaction_key(conversation.last_reaction), values)?;
    conversation.save(cx)?;
    after(cx, id)
}

pub(super) fn unreact(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let (message, emoji) = reactable(cx, params)?;
    let id = message.id;
    let subject = cx.caller().subject.clone();
    let Some(mine) = reactions_of(cx)?.into_iter().find(|reaction| {
        reaction.message == id && reaction.emoji == emoji && reaction.subject == subject
    }) else {
        return Err(veto(
            cx,
            "not_reacted",
            format!("{subject} has not reacted to message {id} with {emoji}"),
            json!({ "message": id, "emoji": emoji }),
        ));
    };
    cx.delete_row(REACTIONS, &reaction_key(mine.number))?;
    Conversation::of(cx)?.save(cx)?;
    after(cx, id)
}

/// pin and unpin: the permission the config names, the message, and
/// whether it is pinned already (pin) or not (unpin, which covers a
/// deleted message, never pinned).
pub(super) fn pin(cx: C<'_, '_>, params: &Value, pinned: bool) -> Hook<Value> {
    let operation = if pinned { PIN } else { UNPIN };
    if let Some(permission) = cx.config().pin_permission.clone() {
        if !holds(cx, Some(&permission))? {
            return Err(forbidden(cx, operation, &permission));
        }
    }
    let id = params.get("message").and_then(whole).unwrap_or_default();
    let messages = messages_of(cx)?;
    let message = present(cx, &messages, id)?;
    if pinned {
        if message.deleted() {
            return Err(deleted(cx, id));
        }
        if message.composing() {
            return Err(composing(cx, id));
        }
        if message.pinned {
            return Err(veto(
                cx,
                "already_pinned",
                format!("message {id} is pinned already"),
                json!({ "message": id }),
            ));
        }
    } else if !message.pinned {
        return Err(veto(
            cx,
            "not_pinned",
            format!("message {id} is not pinned"),
            json!({ "message": id }),
        ));
    }
    let mut values = Map::new();
    values.insert("pinned".into(), Value::from(pinned));
    cx.update_row(MESSAGES, &message_key(id), values)?;
    Conversation::of(cx)?.save(cx)?;
    after(cx, id)
}

/// resolve and reopen: the thread present, top-level and not deleted, the
/// caller its author or one who resolves, and the thread not resolved
/// (resolve) or resolved (reopen).
pub(super) fn resolve(cx: C<'_, '_>, params: &Value, resolved: bool) -> Hook<Value> {
    let id = params.get("thread").and_then(whole).unwrap_or_default();
    let messages = messages_of(cx)?;
    let top = present(cx, &messages, id)?;
    if !top.top_level() {
        return Err(not_a_thread(cx, &top));
    }
    if top.deleted() {
        return Err(deleted(cx, id));
    }
    if top.composing() {
        return Err(composing(cx, id));
    }
    if top.author != cx.caller().subject && !holds(cx, cx.config().resolve_permission.as_deref())? {
        return Err(not_author(cx, &top));
    }
    match (&top.resolved_by, top.resolved_at, resolved) {
        (Some(by), Some(at), true) => {
            return Err(veto(
                cx,
                "already_resolved",
                format!("thread {id} was resolved by {by} at {at}"),
                json!({ "thread": id, "by": by, "at": at }),
            ));
        }
        (_, None, false) | (None, _, false) => {
            return Err(veto(
                cx,
                "not_resolved",
                format!("thread {id} is not resolved"),
                json!({ "thread": id }),
            ));
        }
        _ => {}
    }
    let mut values = Map::new();
    if resolved {
        values.insert(
            "resolvedBy".into(),
            Value::from(cx.caller().subject.as_str()),
        );
        values.insert(
            "resolvedFor".into(),
            for_of(cx).map_or(Value::Null, Value::from),
        );
        values.insert("resolvedAt".into(), Value::from(cx.now()));
    } else {
        values.insert("resolvedBy".into(), Value::Null);
        values.insert("resolvedFor".into(), Value::Null);
        values.insert("resolvedAt".into(), Value::Null);
    }
    cx.update_row(MESSAGES, &message_key(id), values)?;
    Conversation::of(cx)?.save(cx)?;
    after(cx, id)
}
