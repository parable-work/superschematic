//! Comments' streamed replies (D77's step 2, `comments-contract.md`,
//! "Batch 2"), on the core's signals (D77's primitive 2): `begin` holds a
//! message's place as composing, `appendDraft` adds a chunk to its draft
//! and sends the `draft` signal, which the host delivers once the call
//! commits, `finish` posts it and `interrupt` stops it, keeping what was
//! written. Each is beside the row, as batch 1's writes are.
//!
//! The engine's runner swept idle drafts on a schedule; on a table each
//! composing message arms a deadline of its own, `idle.<message>`, at its
//! last append plus `composeTimeoutMs`, which every append moves and its
//! finish, interrupt, delete and purge clear. Its fire interrupts the
//! message as the runner, as the engine's schedule did, and moves no
//! version of the row: Comments writes beside its row alone.

use serde_json::{json, Map, Value};
use superschematic_rules::{Code, Cx, Hook};

use super::checks::{
    check_block_shapes, check_blocks, check_body, for_of, invalid, moderates, not_author,
    not_composing, present, veto, via_of, whole, MAX_APPEND_BYTES,
};
use super::follows;
use super::messages::{
    chunk_key, drafts_of, idle_deadline, message_key, messages_of, Conversation, Message, Talk,
    COMPOSING, DELETED, INTERRUPTED, POSTED,
};
use super::params::has_text;
use super::writes::{after, check_reply, listed, retried};
use super::{CommentsConfig, DRAFTS, MESSAGES};

pub(super) const BEGIN: &str = "begin";
pub(super) const APPEND_DRAFT: &str = "appendDraft";
pub(super) const FINISH: &str = "finish";
pub(super) const INTERRUPT: &str = "interrupt";

/// The draft signal's name.
pub(super) const DRAFT_SIGNAL: &str = "draft";

/// The prefix of a composing message's idle deadline.
pub(super) const IDLE: &str = "idle.";

type C<'a, 'r> = &'a Cx<'r, CommentsConfig>;

// arm_idle arms the message's idle deadline at `now + composeTimeoutMs`.
fn arm_idle(cx: C<'_, '_>, id: i64) -> Hook<()> {
    cx.arm(
        &idle_deadline(id),
        cx.now() + cx.config().compose_timeout_ms,
        json!({ "message": id }),
    )
}

/// Clears the idle deadline of each composing message of `messages` whose
/// id `ids` names.
pub(super) fn clear_idle(cx: C<'_, '_>, messages: &[Message], ids: &[i64]) -> Hook<()> {
    for message in messages
        .iter()
        .filter(|message| message.composing() && ids.contains(&message.id))
    {
        cx.clear(&idle_deadline(message.id))?;
    }
    Ok(())
}

/// Deletes the draft chunks of the messages.
pub(super) fn remove_drafts(cx: C<'_, '_>, ids: &[i64]) -> Hook<()> {
    for chunk in cx.sub_rows(DRAFTS)? {
        let message = chunk.value("message").and_then(whole).unwrap_or_default();
        if ids.contains(&message) {
            cx.delete_row(DRAFTS, &chunk.key)?;
        }
    }
    Ok(())
}

pub(super) fn begin(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let thread = params.get("thread").and_then(whole);
    let quotes = params.get("quotes").and_then(whole);
    let client_id = params.get("clientId").and_then(Value::as_str);
    let messages = messages_of(cx)?;
    // 1: a retry of a begin (or a post) returns its message as it is now.
    if let Some(record) = retried(cx, client_id)? {
        return Ok(record);
    }
    if quotes.is_some() && thread.is_none() {
        return Err(invalid(cx, BEGIN, "/quotes", "needs thread"));
    }
    check_reply(cx, &messages, thread, quotes)?;
    let subject = cx.caller().subject.clone();
    let held = messages
        .iter()
        .filter(|message| message.composing() && message.author == subject)
        .count() as i64;
    let max = cx.config().max_drafts;
    if held >= max {
        return Err(veto(
            cx,
            "too_many_drafts",
            format!(
                "{subject} is composing {held} messages on {} {}, and Comments allows {max} at once; finish or delete one first",
                cx.type_name(),
                cx.key().unwrap_or_default()
            ),
            json!({ "composing": held, "max": max }),
        ));
    }

    let mut conversation = Conversation::of(cx)?;
    conversation.last_message += 1;
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
    values.insert("state".into(), Value::from(COMPOSING));
    values.insert("body".into(), Value::from(""));
    values.insert("blocks".into(), Value::Array(Vec::new()));
    values.insert("mentions".into(), Value::Array(Vec::new()));
    values.insert("createdAt".into(), Value::from(now));
    values.insert("edits".into(), Value::from(0));
    values.insert("pinned".into(), Value::from(false));
    if let Some(client_id) = client_id {
        values.insert("clientId".into(), Value::from(client_id));
    }
    values.insert("draftLength".into(), Value::from(0));
    values.insert("lastAppendAt".into(), Value::from(now));
    cx.insert_row(MESSAGES, &message_key(id), values)?;
    // No count moves: the message does not stand yet.
    conversation.save(cx)?;
    arm_idle(cx, id)?;
    after(cx, id)
}

// composing_message is the composing message an appendDraft, a finish or
// an interrupt names: present, and composing.
fn composing_message(cx: C<'_, '_>, params: &Value) -> Hook<Message> {
    let messages = messages_of(cx)?;
    let message = present(
        cx,
        &messages,
        params.get("message").and_then(whole).unwrap_or_default(),
    )?;
    if !message.composing() {
        return Err(not_composing(cx, &message));
    }
    Ok(message)
}

pub(super) fn append_draft(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let text = params
        .get("text")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let bytes = serde_json::to_vec(&Value::from(text)).map_or(0, |written| written.len());
    if bytes > MAX_APPEND_BYTES {
        return Err(invalid(
            cx,
            APPEND_DRAFT,
            "/text",
            format!("is more than {MAX_APPEND_BYTES} bytes as a JSON string"),
        ));
    }
    let message = composing_message(cx, params)?;
    if message.author != cx.caller().subject {
        return Err(not_author(cx, &message));
    }
    let offset = message.draft_length.unwrap_or(0);
    let append = text.chars().count() as i64;
    let max = cx.config().max_body_length;
    if offset + append > max {
        return Err(veto(
            cx,
            "draft_too_long",
            format!(
                "message {} has a draft of {offset} characters, and {append} more would pass maxBodyLength ({max})",
                message.id
            ),
            json!({ "message": message.id, "length": offset, "append": append, "maxBodyLength": max }),
        ));
    }
    if append > 0 {
        let mut chunk = Map::new();
        chunk.insert("message".into(), Value::from(message.id));
        chunk.insert("chunkOffset".into(), Value::from(offset));
        chunk.insert("text".into(), Value::from(text));
        cx.insert_row(DRAFTS, &chunk_key(message.id, offset), chunk)?;
    }
    let mut values = Map::new();
    values.insert("draftLength".into(), Value::from(offset + append));
    values.insert("lastAppendAt".into(), Value::from(cx.now()));
    cx.update_row(MESSAGES, &message_key(message.id), values)?;
    // Every append moves the idle deadline, an empty one too: it keeps the
    // message from being idle, for an author at work with nothing to show.
    arm_idle(cx, message.id)?;
    // The append and the conversation's other writes are held apart.
    Conversation::of(cx)?.save(cx)?;
    if append > 0 {
        let mut data = Map::new();
        data.insert("message".into(), Value::from(message.id));
        data.insert("offset".into(), Value::from(offset));
        data.insert("text".into(), Value::from(text));
        cx.signal(DRAFT_SIGNAL, Value::Object(data))?;
    }
    let mut result = Map::new();
    result.insert("message".into(), Value::from(message.id));
    result.insert("offset".into(), Value::from(offset));
    result.insert("length".into(), Value::from(offset + append));
    Ok(Value::Object(result))
}

/// Moves a composing message out of composing: to `state` with `body`, its
/// draft's chunks and its idle deadline gone. A message that comes to stand
/// takes `postedAt = now` and the conversation's next stand, and makes its
/// deliveries and follows.
fn stand(
    cx: C<'_, '_>,
    message: &Message,
    state: &str,
    body: &str,
    blocks: Option<&Value>,
    mentions: Option<&Value>,
) -> Hook<()> {
    let now = cx.now();
    let stands = state != DELETED;
    remove_drafts(cx, &[message.id])?;
    cx.clear(&idle_deadline(message.id))?;
    let mut conversation = Conversation::of(cx)?;
    let stand = stands.then(|| {
        conversation.last_stand += 1;
        conversation.last_stand
    });
    let mut values = Map::new();
    values.insert("state".into(), Value::from(state));
    values.insert("body".into(), Value::from(body));
    values.insert("blocks".into(), listed(blocks));
    values.insert("mentions".into(), listed(mentions));
    values.insert(
        "postedAt".into(),
        if stands {
            Value::from(now)
        } else {
            Value::Null
        },
    );
    values.insert("stand".into(), stand.map_or(Value::Null, Value::from));
    values.insert("draftLength".into(), Value::Null);
    values.insert("lastAppendAt".into(), Value::Null);
    cx.update_row(MESSAGES, &message_key(message.id), values)?;
    conversation.save(cx)?;
    if let Some(stand) = stand {
        let stood = Message {
            state: state.to_owned(),
            body: body.to_owned(),
            mentions: listed(mentions),
            stand: Some(stand),
            posted_at: Some(now),
            ..message.clone()
        };
        follows::came_to_stand(cx, &stood, stand)?;
    }
    Ok(())
}

pub(super) fn finish(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let body = params.get("body").and_then(Value::as_str);
    let blocks = params.get("blocks");
    let mentions = params.get("mentions");
    check_body(cx, FINISH, body)?;
    check_block_shapes(cx, FINISH, blocks)?;
    let message = composing_message(cx, params)?;
    if message.author != cx.caller().subject {
        return Err(not_author(cx, &message));
    }
    let body = match body {
        Some(body) => body.to_owned(),
        None => {
            let draft = drafts_of(cx)?.remove(&message.id).unwrap_or_default();
            if !has_text(&draft) {
                return Err(invalid(
                    cx,
                    FINISH,
                    "/body",
                    "is required: the draft has no text",
                ));
            }
            check_body(cx, FINISH, Some(&draft))?;
            draft
        }
    };
    check_blocks(cx, blocks, None)?;
    stand(cx, &message, POSTED, &body, blocks, mentions)?;
    after(cx, message.id)
}

pub(super) fn interrupt(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let message = composing_message(cx, params)?;
    let timeout = cx.config().compose_timeout_ms;
    let last_append_at = message.last_append_at.unwrap_or(message.created_at);
    let idle = cx.now() - last_append_at >= timeout;
    if message.author != cx.caller().subject && !idle && !moderates(cx)? {
        return Err(veto(
            cx,
            "not_idle",
            format!(
                "message {} was appended to at {last_append_at}, less than composeTimeoutMs ({timeout}) ago; only its author or a moderator interrupts it before then",
                message.id
            ),
            json!({
                "message": message.id,
                "author": message.author,
                "lastAppendAt": last_append_at,
                "composeTimeoutMs": timeout,
            }),
        ));
    }
    let draft = drafts_of(cx)?.remove(&message.id).unwrap_or_default();
    if has_text(&draft) {
        stand(cx, &message, INTERRUPTED, &draft, None, None)?;
    } else {
        stand(cx, &message, DELETED, "", None, None)?;
    }
    after(cx, message.id)
}

/// A composing message's idle deadline fell due: it is interrupted as the
/// runner, as the engine's schedule did. The deadline's name names the
/// message, and so does its payload. A veto leaves it, its author having
/// appended or finished in between; the core runs a deadline still armed at
/// its time alone, so that is rare.
pub(super) fn idle(cx: C<'_, '_>, name: &str, payload: &Value) -> Hook<()> {
    let named = name
        .strip_prefix(IDLE)
        .and_then(|digits| digits.parse::<i64>().ok());
    let Some(id) = named.or_else(|| payload.get("message").and_then(whole)) else {
        return Ok(());
    };
    let still = Talk::of(cx)?.find(id).is_some_and(Message::composing);
    if !still {
        return Ok(());
    }
    match cx.call(cx.use_id(), INTERRUPT, &json!({ "message": id })) {
        Ok(_) => Ok(()),
        Err(stop)
            if stop
                .refusal()
                .is_some_and(|refusal| refusal.code == Code::Vetoed) =>
        {
            Ok(())
        }
        Err(stop) => Err(stop),
    }
}
