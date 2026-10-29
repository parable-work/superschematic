//! The message record every operation returns (`comments-contract.md`,
//! "The message record"): a message's row with its reactions, and for a
//! top-level message its thread's replies, latest reply and participants,
//! and for a composing message its draft as its body, read for a page of
//! messages in four statements, however many it holds.

use std::collections::HashMap;

use serde_json::{Map, Value};

use superschematic_engine::behaviors::InstanceView;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::Result;

use super::storage::{drafts, keyed, placeholders, Message, MESSAGES, REACTIONS, STANDS};

/// The most participants a top-level record names.
const PARTICIPANTS: usize = 10;

// One emoji's reactions to a message.
struct Emoji {
    emoji: String,
    count: i64,
    mine: bool,
}

// A thread's members on its top-level record.
#[derive(Default)]
struct Thread {
    replies: i64,
    last_reply_at: Option<i64>,
    // (author, the id of the author's latest standing message), latest
    // first.
    participants: Vec<(String, i64)>,
}

/// The records of `messages`, in their order, `mine` computed for the
/// call's principal.
pub(super) fn records<C>(view: &InstanceView<'_, C>, messages: &[Message]) -> Result<Vec<Value>> {
    if messages.is_empty() {
        return Ok(Vec::new());
    }
    let reactions = reactions(view, messages)?;
    let mut threads = threads(view, messages)?;
    let composing: Vec<i64> = messages
        .iter()
        .filter(|message| message.composing())
        .map(|message| message.id)
        .collect();
    let mut drafts = drafts(view, &composing)?;
    Ok(messages
        .iter()
        .map(|message| {
            record(
                message,
                message
                    .composing()
                    .then(|| drafts.remove(&message.id).unwrap_or_default()),
                reactions
                    .get(&message.id)
                    .map(Vec::as_slice)
                    .unwrap_or_default(),
                message
                    .top_level()
                    .then(|| threads.remove(&message.id).unwrap_or_default()),
            )
        })
        .collect())
}

/// The record of one message.
pub(super) fn record_of<C>(view: &InstanceView<'_, C>, message: &Message) -> Result<Value> {
    Ok(records(view, std::slice::from_ref(message))?
        .pop()
        .unwrap_or(Value::Null))
}

// reactions reads the messages' reactions, by message, each message's
// emoji in the order of their first reaction still held.
fn reactions<C>(
    view: &InstanceView<'_, C>,
    messages: &[Message],
) -> Result<HashMap<i64, Vec<Emoji>>> {
    let table = view.sql().table(REACTIONS)?;
    let mut params = vec![SqlValue::from(view.principal().subject.as_str())];
    params.extend(keyed(
        view,
        messages.iter().map(|message| SqlValue::Integer(message.id)),
    ));
    let rows = view.sql().all(
        format!(
            "SELECT message, emoji, COUNT(*) AS count, MAX(CASE WHEN subject = ? THEN 1 ELSE 0 END) AS mine, MIN(reaction) AS first
             FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND message IN ({})
             GROUP BY message, emoji ORDER BY message, first",
            placeholders(messages.len())
        ),
        &params,
    )?;
    let mut out: HashMap<i64, Vec<Emoji>> = HashMap::new();
    for row in &rows {
        out.entry(row.i64("message")?).or_default().push(Emoji {
            emoji: row.text("emoji")?.to_owned(),
            count: row.i64("count")?,
            mine: row.i64("mine")? != 0,
        });
    }
    Ok(out)
}

// threads reads the top-level messages' threads: their standing replies,
// the latest of them, and the authors of the thread's standing messages.
fn threads<C>(view: &InstanceView<'_, C>, messages: &[Message]) -> Result<HashMap<i64, Thread>> {
    let tops: Vec<i64> = messages
        .iter()
        .filter(|message| message.top_level())
        .map(|message| message.id)
        .collect();
    let mut out: HashMap<i64, Thread> = HashMap::new();
    if tops.is_empty() {
        return Ok(out);
    }
    let table = view.sql().table(MESSAGES)?;
    let ids = || tops.iter().map(|&id| SqlValue::Integer(id));
    let list = placeholders(tops.len());
    let replies = view.sql().all(
        format!(
            "SELECT thread, COUNT(*) AS replies, MAX(posted_at) AS latest FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND {STANDS} AND thread IN ({list})
             GROUP BY thread"
        ),
        &keyed(view, ids()),
    )?;
    for row in &replies {
        let thread = out.entry(row.i64("thread")?).or_default();
        thread.replies = row.i64("replies")?;
        thread.last_reply_at = row.opt_i64("latest")?;
    }
    let authors = view.sql().all(
        format!(
            "SELECT COALESCE(thread, message) AS root, author, MAX(message) AS latest FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND {STANDS}
               AND ((thread IS NULL AND message IN ({list})) OR thread IN ({list}))
             GROUP BY COALESCE(thread, message), author"
        ),
        &keyed(view, ids().chain(ids())),
    )?;
    for row in &authors {
        out.entry(row.i64("root")?)
            .or_default()
            .participants
            .push((row.text("author")?.to_owned(), row.i64("latest")?));
    }
    for thread in out.values_mut() {
        thread
            .participants
            .sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(&b.0)));
        thread.participants.truncate(PARTICIPANTS);
    }
    Ok(out)
}

// record writes the record's members in the contract's order, leaving out
// the absent ones; a composing message's body is its draft.
fn record(
    message: &Message,
    draft: Option<String>,
    reactions: &[Emoji],
    thread: Option<Thread>,
) -> Value {
    let mut out = Map::new();
    out.insert("id".into(), Value::from(message.id));
    if let Some(thread) = message.thread {
        out.insert("thread".into(), Value::from(thread));
    }
    if let Some(quotes) = message.quotes {
        out.insert("quotes".into(), Value::from(quotes));
    }
    out.insert("author".into(), Value::from(message.author.as_str()));
    if let Some(subject) = &message.for_subject {
        out.insert("for".into(), Value::from(subject.as_str()));
    }
    if let Some(via) = &message.via {
        out.insert("via".into(), Value::from(via.as_str()));
    }
    out.insert("state".into(), Value::from(message.state.as_str()));
    out.insert(
        "body".into(),
        Value::from(draft.unwrap_or_else(|| message.body.clone())),
    );
    out.insert("blocks".into(), message.blocks.clone());
    out.insert("mentions".into(), message.mentions.clone());
    let mut held = Map::new();
    for emoji in reactions {
        let mut one = Map::new();
        one.insert("count".into(), Value::from(emoji.count));
        one.insert("mine".into(), Value::from(emoji.mine));
        held.insert(emoji.emoji.clone(), Value::Object(one));
    }
    out.insert("reactions".into(), Value::Object(held));
    out.insert("createdAt".into(), Value::from(message.created_at));
    if let Some(at) = message.posted_at {
        out.insert("postedAt".into(), Value::from(at));
    }
    if let Some(stand) = message.stand {
        out.insert("stand".into(), Value::from(stand));
    }
    if let Some(at) = message.edited_at {
        out.insert("editedAt".into(), Value::from(at));
    }
    out.insert("edits".into(), Value::from(message.edits));
    out.insert("pinned".into(), Value::from(message.pinned));
    if let Some(thread) = thread {
        out.insert("replies".into(), Value::from(thread.replies));
        if let Some(at) = thread.last_reply_at {
            out.insert("lastReplyAt".into(), Value::from(at));
        }
        out.insert(
            "participants".into(),
            Value::Array(
                thread
                    .participants
                    .into_iter()
                    .map(|(author, _)| Value::from(author))
                    .collect(),
            ),
        );
        if let (Some(by), Some(at)) = (&message.resolved_by, message.resolved_at) {
            let mut resolved = Map::new();
            resolved.insert("by".into(), Value::from(by.as_str()));
            if let Some(subject) = &message.resolved_for {
                resolved.insert("for".into(), Value::from(subject.as_str()));
            }
            resolved.insert("at".into(), Value::from(at));
            out.insert("resolved".into(), Value::Object(resolved));
        }
    }
    Value::Object(out)
}
