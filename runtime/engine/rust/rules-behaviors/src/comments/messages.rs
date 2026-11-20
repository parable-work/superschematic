//! A message as the sub-table `messages` holds it, the record every
//! operation returns for one (`comments-contract.md`, "The message
//! record", as batches 2 and 3 amend it: `postedAt` and `stand`), the
//! fields a read computes from them, and the conversation's high-water
//! marks in the shared table `conversations`.

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{Cx, Hook, SubRow, View};

use super::checks::whole;
use super::{
    CommentsConfig, CONVERSATIONS, EDITS, LAST_MESSAGE_AT, MESSAGES, MESSAGES_FIELD, REACTIONS,
    THREADS,
};

/// A message posted and standing.
pub(super) const POSTED: &str = "posted";
/// A message deleted, which keeps its place.
pub(super) const DELETED: &str = "deleted";

/// The most participants a top-level record names.
const PARTICIPANTS: usize = 10;

/// The width of a message's id and a reaction's number in their keys.
const KEY_WIDTH: usize = 10;

/// One message of the conversation.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct Message {
    pub id: i64,
    pub thread: Option<i64>,
    pub quotes: Option<i64>,
    pub author: String,
    pub state: String,
    pub body: String,
    pub blocks: Value,
    pub mentions: Value,
    pub created_at: i64,
    pub posted_at: Option<i64>,
    pub stand: Option<i64>,
    pub edited_at: Option<i64>,
    pub edits: i64,
    pub pinned: bool,
    pub client_id: Option<String>,
    pub resolved_by: Option<String>,
    pub resolved_at: Option<i64>,
}

impl Message {
    /// The message a sub-table row holds; `None` for a key that is no id.
    pub fn of(row: &SubRow) -> Option<Message> {
        let id: i64 = row.key.parse().ok()?;
        let int = |name: &str| row.value(name).and_then(whole);
        let text = |name: &str| row.value(name).and_then(Value::as_str).map(str::to_owned);
        Some(Message {
            id,
            thread: int("thread"),
            quotes: int("quotes"),
            author: text("author").unwrap_or_default(),
            state: text("state").unwrap_or_else(|| POSTED.to_owned()),
            body: text("body").unwrap_or_default(),
            blocks: list(row.value("blocks")),
            mentions: list(row.value("mentions")),
            created_at: int("createdAt").unwrap_or_default(),
            posted_at: int("postedAt"),
            stand: int("stand"),
            edited_at: int("editedAt"),
            edits: int("edits").unwrap_or_default(),
            pinned: row.value("pinned").and_then(Value::as_bool) == Some(true),
            client_id: text("clientId"),
            resolved_by: text("resolvedBy"),
            resolved_at: int("resolvedAt"),
        })
    }

    /// Whether it is top-level, a thread of its own.
    pub fn top_level(&self) -> bool {
        self.thread.is_none()
    }

    /// Whether it is deleted.
    pub fn deleted(&self) -> bool {
        self.state == DELETED
    }

    /// Whether it stands: posted, holding text its author wrote, which the
    /// counts count.
    pub fn stands(&self) -> bool {
        self.state == POSTED
    }

    /// Whether its thread is resolved.
    pub fn resolved(&self) -> bool {
        self.resolved_by.is_some() && self.resolved_at.is_some()
    }
}

/// A list member as a sub-table row holds it: its JSON, or JSON text a
/// store gave back as a string; `[]` when absent.
fn list(value: Option<&Value>) -> Value {
    match value {
        Some(Value::Array(items)) => Value::Array(items.clone()),
        Some(Value::String(text)) => serde_json::from_str(text)
            .ok()
            .filter(Value::is_array)
            .unwrap_or_else(|| Value::Array(Vec::new())),
        _ => Value::Array(Vec::new()),
    }
}

/// One reaction a subject holds on a message.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct Reaction {
    pub number: i64,
    pub message: i64,
    pub emoji: String,
    pub subject: String,
    pub at: i64,
}

impl Reaction {
    /// The reaction a sub-table row holds; `None` for a key that is no
    /// number.
    pub fn of(row: &SubRow) -> Option<Reaction> {
        Some(Reaction {
            number: row.key.parse().ok()?,
            message: row.value("message").and_then(whole).unwrap_or_default(),
            emoji: row
                .value("emoji")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            subject: row
                .value("subject")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            at: row.value("at").and_then(whole).unwrap_or_default(),
        })
    }
}

/// The key of a message's row: its id in ten digits, zero-padded.
pub(super) fn message_key(id: i64) -> String {
    format!("{id:0KEY_WIDTH$}")
}

/// The key of a reaction's row: its number in ten digits, zero-padded.
pub(super) fn reaction_key(number: i64) -> String {
    format!("{number:0KEY_WIDTH$}")
}

/// The key of an edit's row: the message's id and the edit's number, each
/// in ten digits, joined by a dot.
pub(super) fn edit_key(message: i64, number: i64) -> String {
    format!("{message:0KEY_WIDTH$}.{number:0KEY_WIDTH$}")
}

/// The conversation's messages, as the run has them, in id order.
pub(super) fn messages_of(view: &View<'_, CommentsConfig>) -> Hook<Vec<Message>> {
    Ok(view
        .sub_rows(MESSAGES)?
        .iter()
        .filter_map(Message::of)
        .collect())
}

/// The conversation's reactions, as the run has them, in number order.
pub(super) fn reactions_of(view: &View<'_, CommentsConfig>) -> Hook<Vec<Reaction>> {
    Ok(view
        .sub_rows(REACTIONS)?
        .iter()
        .filter_map(Reaction::of)
        .collect())
}

/// The conversation's edits, as the run has them, in key order: each
/// message's by number.
pub(super) fn edits_of(view: &View<'_, CommentsConfig>) -> Hook<Vec<SubRow>> {
    view.sub_rows(EDITS)
}

/// The conversation's high-water marks, and the version of its shared row
/// a write of it is held to; `None` before the conversation's first write.
#[derive(Clone, Debug, Default, PartialEq)]
pub(super) struct Conversation {
    pub version: Option<i64>,
    pub last_message: i64,
    pub last_reaction: i64,
    pub last_stand: i64,
}

impl Conversation {
    /// The conversation of the row the run is on, as its shared row holds
    /// it, each mark at least what the row's own messages and reactions
    /// reach.
    pub fn of(cx: &Cx<'_, CommentsConfig>) -> Hook<Conversation> {
        let key = cx.key().unwrap_or_default();
        let held = cx.shared_row(CONVERSATIONS, key)?;
        let mark = |name: &str| {
            held.as_ref()
                .and_then(|row| row.value(name))
                .and_then(whole)
                .unwrap_or(0)
        };
        let messages = messages_of(cx)?;
        let reactions = reactions_of(cx)?;
        Ok(Conversation {
            version: held.as_ref().map(|row| row.version),
            last_message: mark("lastMessage")
                .max(messages.iter().map(|message| message.id).max().unwrap_or(0)),
            last_reaction: mark("lastReaction").max(
                reactions
                    .iter()
                    .map(|reaction| reaction.number)
                    .max()
                    .unwrap_or(0),
            ),
            last_stand: mark("lastStand").max(
                messages
                    .iter()
                    .filter_map(|message| message.stand)
                    .max()
                    .unwrap_or(0),
            ),
        })
    }

    /// Writes the marks to the conversation's shared row, held to the
    /// version it was read at, which every writing operation does so that
    /// two writes of one conversation are held apart.
    pub fn save(&self, cx: &Cx<'_, CommentsConfig>) -> Hook<()> {
        let key = cx.key().unwrap_or_default();
        let mut values = Map::new();
        values.insert("lastMessage".into(), Value::from(self.last_message));
        values.insert("lastReaction".into(), Value::from(self.last_reaction));
        values.insert("lastStand".into(), Value::from(self.last_stand));
        match self.version {
            Some(version) => cx.update_shared(CONVERSATIONS, key, version, values),
            None => cx.insert_shared(CONVERSATIONS, key, values),
        }
    }
}

/// The three fields a read computes: how many messages stand, the latest
/// standing one's postedAt (absent while none stands), and the standing
/// top-level messages, open and resolved.
pub(super) fn fields(messages: &[Message]) -> Map<String, Value> {
    let standing: Vec<&Message> = messages.iter().filter(|m| m.stands()).collect();
    let mut fields = Map::new();
    fields.insert(MESSAGES_FIELD.into(), Value::from(standing.len()));
    if let Some(at) = standing.iter().filter_map(|m| m.posted_at).max() {
        fields.insert(LAST_MESSAGE_AT.into(), Value::from(at));
    }
    let tops: Vec<&&Message> = standing.iter().filter(|m| m.top_level()).collect();
    let resolved = tops.iter().filter(|m| m.resolved()).count();
    let mut threads = Map::new();
    threads.insert("open".into(), Value::from(tops.len() - resolved));
    threads.insert("resolved".into(), Value::from(resolved));
    fields.insert(THREADS.into(), Value::Object(threads));
    fields
}

/// The record of a message, `mine` computed for `subject`: its members in
/// the contract's order, absent ones left out; a top-level message's
/// thread members from the conversation's messages.
pub(super) fn record(
    message: &Message,
    messages: &[Message],
    reactions: &[Reaction],
    subject: &str,
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
    out.insert("state".into(), Value::from(message.state.as_str()));
    out.insert("body".into(), Value::from(message.body.as_str()));
    out.insert("blocks".into(), message.blocks.clone());
    out.insert("mentions".into(), message.mentions.clone());
    out.insert(
        "reactions".into(),
        Value::Object(reactions_record(message.id, reactions, subject)),
    );
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
    if message.top_level() {
        let replies: Vec<&Message> = messages
            .iter()
            .filter(|reply| reply.thread == Some(message.id) && reply.stands())
            .collect();
        out.insert("replies".into(), Value::from(replies.len()));
        if let Some(at) = replies.iter().filter_map(|reply| reply.posted_at).max() {
            out.insert("lastReplyAt".into(), Value::from(at));
        }
        out.insert(
            "participants".into(),
            Value::Array(
                participants(message, &replies)
                    .into_iter()
                    .map(Value::from)
                    .collect(),
            ),
        );
        if let (Some(by), Some(at)) = (&message.resolved_by, message.resolved_at) {
            let mut resolved = Map::new();
            resolved.insert("by".into(), Value::from(by.as_str()));
            resolved.insert("at".into(), Value::from(at));
            out.insert("resolved".into(), Value::Object(resolved));
        }
    }
    Value::Object(out)
}

// reactions_record is a message's reactions by emoji, `{ count, mine }`,
// in the order of each emoji's first reaction still held.
fn reactions_record(message: i64, reactions: &[Reaction], subject: &str) -> Map<String, Value> {
    // By emoji: its first reaction's number, how many and whether mine.
    let mut held: BTreeMap<&str, (i64, i64, bool)> = BTreeMap::new();
    for reaction in reactions.iter().filter(|r| r.message == message) {
        let entry = held
            .entry(reaction.emoji.as_str())
            .or_insert((reaction.number, 0, false));
        entry.0 = entry.0.min(reaction.number);
        entry.1 += 1;
        entry.2 = entry.2 || reaction.subject == subject;
    }
    let mut ordered: Vec<(&str, (i64, i64, bool))> = held.into_iter().collect();
    ordered.sort_by_key(|(_, (first, ..))| *first);
    let mut out = Map::new();
    for (emoji, (_, count, mine)) in ordered {
        let mut one = Map::new();
        one.insert("count".into(), Value::from(count));
        one.insert("mine".into(), Value::from(mine));
        out.insert(emoji.to_owned(), Value::Object(one));
    }
    out
}

// participants is the distinct authors of a thread's standing messages, by
// the id of each one's latest, greatest first (an author's name breaking a
// tie), at most ten. A deleted top-level message's author is in it only by
// a standing reply.
fn participants(top: &Message, replies: &[&Message]) -> Vec<String> {
    let mut latest: BTreeMap<&str, i64> = BTreeMap::new();
    let standing = top.stands().then_some(top).into_iter();
    for message in standing.chain(replies.iter().copied()) {
        let entry = latest.entry(message.author.as_str()).or_insert(message.id);
        *entry = (*entry).max(message.id);
    }
    let mut ordered: Vec<(&str, i64)> = latest.into_iter().collect();
    ordered.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(b.0)));
    ordered.truncate(PARTICIPANTS);
    ordered
        .into_iter()
        .map(|(author, _)| author.to_owned())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_keys_order_is_its_numbers() {
        assert_eq!(message_key(1), "0000000001");
        assert_eq!(reaction_key(42), "0000000042");
        assert_eq!(edit_key(3, 2), "0000000003.0000000002");
        assert!(edit_key(2, 10) < edit_key(3, 1));
    }

    #[test]
    fn a_list_member_reads_its_json_or_its_text() {
        assert_eq!(
            list(Some(&serde_json::json!(["a"]))),
            serde_json::json!(["a"])
        );
        assert_eq!(
            list(Some(&serde_json::json!("[\"a\"]"))),
            serde_json::json!(["a"])
        );
        assert_eq!(list(None), serde_json::json!([]));
        assert_eq!(list(Some(&serde_json::json!("x"))), serde_json::json!([]));
    }
}
