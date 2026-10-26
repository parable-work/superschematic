//! Comments' streamed replies (D77 step 2, `comments-contract.md`, "Batch
//! 2"): `begin` holds a message's place as composing, `appendDraft` adds a
//! chunk to its draft and sends the `draft` signal when its call commits,
//! `finish` posts it and `interrupt` stops it, keeping what was written;
//! and the schedule `interrupt`, which interrupts, as the runner, each
//! message whose author has appended nothing for `composeTimeoutMs`.
//!
//! Each is beside the instance, as batch 1's writes are; `appendDraft`
//! appends no event (`"log": false`), the others one each.

use serde_json::{json, Value};

use superschematic_engine::behaviors::ScheduleCx;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::Result;

use super::checks::{
    check_action_data, check_blocks, check_body, has_text, integer, invalid, moderates, not_author,
    not_composing, object, present, veto, MAX_APPEND_BYTES,
};
use super::storage::{
    keyed, move_counts, save_conversation, Message, Moves, COMPOSING, DELETED, DRAFTS, INTERRUPTED,
    MESSAGES, POSTED,
};
use super::writes::{after, check_reply, compact, next_message, retried, via, Cx};
use super::CommentsConfig;

/// How many idle messages the schedule reads at a time.
const IDLE_PAGE: i64 = 100;
/// The most idle messages one run of the schedule interrupts.
const MAX_IDLE: i64 = 1000;

pub(super) fn begin(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "begin";
    let thread = integer(params.get("thread"));
    let quotes = integer(params.get("quotes"));
    let client_id = params.get("clientId").and_then(Value::as_str);
    // 1: a retry of a begin (or a post) returns its message as it is now.
    if let Some(record) = retried(cx, client_id)? {
        return Ok(record);
    }
    if quotes.is_some() && thread.is_none() {
        return Err(invalid(OP, "/quotes", "needs thread"));
    }
    check_reply(cx, OP, thread, quotes)?;
    let subject = cx.principal().subject.as_str();
    let table = cx.sql().table(MESSAGES)?;
    let held = cx
        .sql()
        .get(
            format!(
                "SELECT COUNT(*) AS composing FROM {table}
                 WHERE namespace = ? AND schema = ? AND id = ? AND author = ? AND last_append_at IS NOT NULL"
            ),
            &keyed(cx, [SqlValue::from(subject)]),
        )?
        .map(|row| row.i64("composing"))
        .transpose()?
        .unwrap_or(0);
    let max = cx.config().max_drafts;
    if held >= max {
        return Err(veto(
            cx,
            OP,
            "too_many_drafts",
            format!(
                "{subject} is composing {held} messages on {} {}, and Comments allows {max} at once; finish or delete one first",
                cx.schema(),
                cx.id()
            ),
            json!({ "composing": held, "max": max }),
        ));
    }

    let (id, existed, conversation) = next_message(cx)?;
    let now = cx.now();
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, message, thread, quotes, author, via, state, body, blocks, mentions, created_at, posted_at, edited_at, edits, pinned, client_id, resolved_by, resolved_at, draft_length, last_append_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '[]', '[]', ?, NULL, NULL, 0, 0, ?, NULL, NULL, 0, ?)"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(id),
                thread.map_or(SqlValue::Null, SqlValue::Integer),
                quotes.map_or(SqlValue::Null, SqlValue::Integer),
                SqlValue::from(subject),
                via(cx).map_or(SqlValue::Null, SqlValue::from),
                SqlValue::from(COMPOSING),
                SqlValue::Integer(now),
                client_id.map_or(SqlValue::Null, SqlValue::from),
                SqlValue::Integer(now),
            ],
        ),
    )?;
    // No count moves: the message does not stand yet.
    save_conversation(cx, existed, &conversation)?;
    after(cx, OP, id)
}

// composing_message is the composing message an appendDraft, a finish or
// an interrupt names: present, and composing.
fn composing_message(cx: Cx<'_, '_>, operation: &str, params: &Value) -> Result<Message> {
    let message = present(
        cx,
        operation,
        integer(params.get("message")).unwrap_or_default(),
    )?;
    if !message.composing() {
        return Err(not_composing(cx, operation, &message));
    }
    Ok(message)
}

// the draft's chunks concatenated, in order.
fn draft_of(cx: Cx<'_, '_>, id: i64) -> Result<String> {
    Ok(super::storage::drafts(cx, &[id])?
        .remove(&id)
        .unwrap_or_default())
}

pub(super) fn append_draft(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "appendDraft";
    let text = params
        .get("text")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let bytes = serde_json::to_vec(&Value::from(text))
        .map(|written| written.len())
        .unwrap_or(0);
    if bytes > MAX_APPEND_BYTES {
        return Err(invalid(
            OP,
            "/text",
            format!("is more than {MAX_APPEND_BYTES} bytes as a JSON string"),
        ));
    }
    let message = composing_message(cx, OP, params)?;
    if message.author != cx.principal().subject {
        return Err(not_author(cx, OP, &message));
    }
    let offset = message.draft_length.unwrap_or(0);
    let append = text.chars().count() as i64;
    let max = cx.config().max_body_length;
    if offset + append > max {
        return Err(veto(
            cx,
            OP,
            "draft_too_long",
            format!(
                "message {} has a draft of {offset} characters, and {append} more would pass maxBodyLength ({max})",
                message.id
            ),
            json!({ "message": message.id, "length": offset, "append": append, "maxBodyLength": max }),
        ));
    }

    let now = cx.now();
    if append > 0 {
        let drafts = cx.sql().table(DRAFTS)?;
        cx.sql().run(
            format!(
                "INSERT INTO {drafts} (namespace, schema, id, message, chunk_offset, text) VALUES (?, ?, ?, ?, ?, ?)"
            ),
            &keyed(
                cx,
                [
                    SqlValue::Integer(message.id),
                    SqlValue::Integer(offset),
                    SqlValue::from(text),
                ],
            ),
        )?;
    }
    let table = cx.sql().table(MESSAGES)?;
    cx.sql().run(
        format!(
            "UPDATE {table} SET draft_length = ?, last_append_at = ?
             WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &[SqlValue::Integer(offset + append), SqlValue::Integer(now)]
            .into_iter()
            .chain(keyed(cx, [SqlValue::Integer(message.id)]))
            .collect::<Vec<_>>(),
    )?;
    // An empty text keeps the message from being idle, and shows nothing.
    if append > 0 {
        cx.signal(
            "draft",
            &object(vec![
                ("message", Value::from(message.id)),
                ("offset", Value::from(offset)),
                ("text", Value::from(text)),
            ]),
        )?;
    }
    Ok(object(vec![
        ("message", Value::from(message.id)),
        ("offset", Value::from(offset)),
        ("length", Value::from(offset + append)),
    ]))
}

// stand moves a composing message out of composing: to `state` with
// `body`, its draft's chunks gone. A message that comes to stand takes
// posted_at = now and the conversation's next stand, moves the counts as a
// post does, and makes its deliveries and follows.
fn stand(
    cx: Cx<'_, '_>,
    message: &Message,
    state: &str,
    body: &str,
    blocks: Option<&Value>,
    mentions: Option<&Value>,
) -> Result<()> {
    let now = cx.now();
    let stands = state != DELETED;
    let drafts = cx.sql().table(DRAFTS)?;
    cx.sql().run(
        format!(
            "DELETE FROM {drafts} WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &keyed(cx, [SqlValue::Integer(message.id)]),
    )?;
    let mut conversation = super::storage::conversation(cx)?.unwrap_or_default();
    let stand = stands.then(|| {
        conversation.last_stand += 1;
        conversation.last_stand
    });
    let table = cx.sql().table(MESSAGES)?;
    cx.sql().run(
        format!(
            "UPDATE {table} SET state = ?, body = ?, blocks = ?, mentions = ?, posted_at = ?, stand = ?, draft_length = NULL, last_append_at = NULL
             WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &[
            SqlValue::from(state),
            SqlValue::from(body),
            SqlValue::from(compact(blocks)),
            SqlValue::from(compact(mentions)),
            if stands {
                SqlValue::Integer(now)
            } else {
                SqlValue::Null
            },
            stand.map_or(SqlValue::Null, SqlValue::Integer),
        ]
        .into_iter()
        .chain(keyed(cx, [SqlValue::Integer(message.id)]))
        .collect::<Vec<_>>(),
    )?;
    if let Some(stand) = stand {
        move_counts(
            cx,
            &mut conversation,
            &Moves::standing(message.top_level(), now),
        )?;
        save_conversation(cx, true, &conversation)?;
        super::follows::came_to_stand(
            cx,
            message.id,
            message.thread,
            &message.author,
            stand,
            mentions,
        )?;
    }
    Ok(())
}

pub(super) fn finish(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "finish";
    let body = params.get("body").and_then(Value::as_str);
    let blocks = params.get("blocks");
    let mentions = params.get("mentions");
    check_body(cx, OP, body)?;
    check_action_data(OP, blocks)?;
    let message = composing_message(cx, OP, params)?;
    if message.author != cx.principal().subject {
        return Err(not_author(cx, OP, &message));
    }
    let body = match body {
        Some(body) => body.to_owned(),
        None => {
            let draft = draft_of(cx, message.id)?;
            if !has_text(&draft) {
                return Err(invalid(OP, "/body", "is required: the draft has no text"));
            }
            check_body(cx, OP, Some(&draft))?;
            draft
        }
    };
    check_blocks(cx, OP, blocks, None)?;
    stand(cx, &message, POSTED, &body, blocks, mentions)?;
    after(cx, OP, message.id)
}

pub(super) fn interrupt(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "interrupt";
    let message = composing_message(cx, OP, params)?;
    let config = cx.config();
    let last_append_at = message.last_append_at.unwrap_or(message.created_at);
    let idle = cx.now() - last_append_at >= config.compose_timeout_ms;
    if message.author != cx.principal().subject && !idle && !moderates(cx)? {
        return Err(veto(
            cx,
            OP,
            "not_idle",
            format!(
                "message {} was appended to at {last_append_at}, less than composeTimeoutMs ({}) ago; only its author or a moderator interrupts it before then",
                message.id, config.compose_timeout_ms
            ),
            json!({
                "message": message.id,
                "author": message.author,
                "lastAppendAt": last_append_at,
                "composeTimeoutMs": config.compose_timeout_ms,
            }),
        ));
    }
    let draft = draft_of(cx, message.id)?;
    if has_text(&draft) {
        stand(cx, &message, INTERRUPTED, &draft, None, None)?;
    } else {
        stand(cx, &message, DELETED, "", None, None)?;
    }
    after(cx, OP, message.id)
}

/// The schedule `interrupt`: each idle message of the run's namespace and
/// schema, oldest append first, interrupted as the runner. A veto leaves
/// the message (its author appended or finished in between); any other
/// error fails the run.
pub(super) fn interrupt_idle(cx: &ScheduleCx<'_, CommentsConfig>) -> Result<()> {
    let table = cx.sql().table(MESSAGES)?;
    let due = cx.now() - cx.config().compose_timeout_ms;
    let mut past: Option<(i64, String, i64)> = None;
    let mut swept = 0;
    while swept < MAX_IDLE {
        let mut params = vec![
            SqlValue::from(cx.namespace()),
            SqlValue::from(cx.schema()),
            SqlValue::Integer(due),
        ];
        let keyset = match &past {
            Some((at, id, message)) => {
                params.extend([
                    SqlValue::Integer(*at),
                    SqlValue::from(id.as_str()),
                    SqlValue::Integer(*message),
                ]);
                " AND (last_append_at, id, message) > (?, ?, ?)"
            }
            None => "",
        };
        params.push(SqlValue::Integer(IDLE_PAGE.min(MAX_IDLE - swept)));
        let idle = cx.sql().all(
            format!(
                "SELECT id, message, last_append_at FROM {table}
                 WHERE namespace = ? AND schema = ? AND last_append_at IS NOT NULL
                   AND last_append_at <= ?{keyset}
                 ORDER BY last_append_at, id, message LIMIT ?"
            ),
            &params,
        )?;
        for row in &idle {
            match cx.instances().invoke(
                cx.schema(),
                row.text("id")?,
                "interrupt",
                &json!({ "message": row.i64("message")? }),
                None,
            ) {
                Ok(_) => {}
                Err(error) if error.is_veto() => {}
                Err(error) => return Err(error),
            }
        }
        swept += idle.len() as i64;
        if (idle.len() as i64) < IDLE_PAGE {
            return Ok(());
        }
        let last = idle.last().expect("a full page has a last row");
        past = Some((
            last.i64("last_append_at")?,
            last.text("id")?.to_owned(),
            last.i64("message")?,
        ));
    }
    Ok(())
}
