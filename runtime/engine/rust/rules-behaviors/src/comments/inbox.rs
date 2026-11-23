//! Comments' `inbox` (D77's step 3, `comments-contract.md`, "inbox"), an
//! operation of the namespace as a whole that reads (D77's primitive 3, the
//! core's [`superschematic_rules::NamespaceCx`]): the caller's mentions, the
//! threads it follows and the conversations it has read, across every type
//! that composes Comments and that the caller may read, newest first, each
//! kind's items merged by `(postedAt, rank)` and paged by a cursor over an
//! item's key, as the engine's statements order and page them.
//!
//! The core reads each type's rows with their messages, markers, follows and
//! deliveries through the read across a table, so the host answers it from
//! its layout with no statement of Comments' own: development mode promises
//! a table's answers, not its speed.

use serde_json::{json, Map, Value};
use superschematic_rules::{Hook, NamespaceCx, Refusal, SchemaIssue, SubRow, TableRef};

use super::follows::{markers, Delivery, Follow};
use super::messages::{drafts, Message};
use super::{DELIVERIES, DRAFTS, FOLLOWS, MESSAGES, NAME, READS};
use crate::paging::{base64url_decode, base64url_encode};

pub(super) const INBOX: &str = "inbox";

/// A page's size when `limit` is absent.
const PAGE: i64 = 50;
/// The longest excerpt, in code points.
const EXCERPT: usize = 200;

/// The three kinds of item, by rank: at one `postedAt`, mentions before
/// threads before conversations.
const KINDS: [(&str, i64); 3] = [("mention", 3), ("thread", 2), ("conversation", 1)];

fn rank_of(kind: &str) -> Option<i64> {
    KINDS
        .iter()
        .find(|(name, _)| *name == kind)
        .map(|(_, rank)| *rank)
}

// An item's key, which its cursor writes.
#[derive(Clone, Debug, PartialEq)]
struct Key {
    posted_at: i64,
    kind: String,
    schema: String,
    instance: String,
    message: i64,
}

impl Key {
    fn cursor(&self) -> String {
        let written = serde_json::to_string(&json!([
            self.posted_at,
            self.kind,
            self.schema,
            self.instance,
            self.message
        ]))
        .unwrap_or_default();
        base64url_encode(written.as_bytes())
    }

    // decode reads a cursor back: exactly an array of five, an integer, one
    // of the kinds, two strings and an integer >= 1.
    fn decode(cursor: &str) -> Option<Key> {
        let bytes = base64url_decode(cursor);
        let text = std::str::from_utf8(&bytes).ok()?;
        let value: Value = serde_json::from_str(text).ok()?;
        let [posted_at, kind, schema, instance, message] = value.as_array()?.as_slice() else {
            return None;
        };
        let kind = kind.as_str()?;
        rank_of(kind)?;
        let message = message.as_i64().filter(|message| *message >= 1)?;
        Some(Key {
            posted_at: posted_at.as_i64()?,
            kind: kind.to_owned(),
            schema: schema.as_str()?.to_owned(),
            instance: instance.as_str()?.to_owned(),
            message,
        })
    }

    // order is the key's place in its kind's list, greatest first: postedAt,
    // then the type, the row and the message, as the engine's statements
    // order them.
    fn order(&self) -> (i64, &str, &str, i64) {
        (self.posted_at, &self.schema, &self.instance, self.message)
    }
}

// One item of a kind's list.
struct Item {
    key: Key,
    rank: i64,
    unread: i64,
    value: Value,
}

/// The message's body with each run of white space one space, trimmed, and
/// when longer than 200 code points its first 199, a trailing space
/// removed, then an ellipsis.
pub(super) fn excerpt(body: &str) -> String {
    let collapsed = body
        .split(char::is_whitespace)
        .filter(|word| !word.is_empty())
        .collect::<Vec<_>>()
        .join(" ");
    if collapsed.chars().count() <= EXCERPT {
        return collapsed;
    }
    let mut cut: String = collapsed.chars().take(EXCERPT - 1).collect();
    if cut.ends_with(' ') {
        cut.pop();
    }
    cut.push('\u{2026}');
    cut
}

/// What the inbox's parameters break of its paramsSchema.
pub(super) fn params_issues(params: &Value) -> Vec<SchemaIssue> {
    let mut issues = Vec::new();
    let Some(given) = params.as_object() else {
        return vec![SchemaIssue::new("", "must be object")];
    };
    for key in given.keys() {
        if !["kinds", "unread", "limit", "cursor"].contains(&key.as_str()) {
            issues.push(SchemaIssue::new(
                "",
                format!("must NOT have additional properties: {key}"),
            ));
        }
    }
    if let Some(kinds) = given.get("kinds") {
        match kinds.as_array() {
            None => issues.push(SchemaIssue::new("/kinds", "must be array")),
            Some(items) => {
                if items.is_empty() {
                    issues.push(SchemaIssue::new(
                        "/kinds",
                        "must NOT have fewer than 1 items",
                    ));
                }
                for (index, item) in items.iter().enumerate() {
                    if !item.as_str().is_some_and(|kind| rank_of(kind).is_some()) {
                        issues.push(SchemaIssue::new(
                            format!("/kinds/{index}"),
                            "must be equal to one of the allowed values",
                        ));
                    }
                }
            }
        }
    }
    if given
        .get("unread")
        .is_some_and(|unread| !unread.is_boolean())
    {
        issues.push(SchemaIssue::new("/unread", "must be boolean"));
    }
    if let Some(limit) = given.get("limit") {
        match limit.as_i64() {
            None => issues.push(SchemaIssue::new("/limit", "must be integer")),
            Some(limit) if limit < 1 => issues.push(SchemaIssue::new("/limit", "must be >= 1")),
            Some(limit) if limit > 100 => {
                issues.push(SchemaIssue::new("/limit", "must be <= 100"));
            }
            Some(_) => {}
        }
    }
    if given
        .get("cursor")
        .is_some_and(|cursor| !cursor.is_string())
    {
        issues.push(SchemaIssue::new("/cursor", "must be string"));
    }
    issues
}

// One row's conversation, as the read across its table gave it.
struct Conversation<'r> {
    schema: &'r str,
    key: &'r str,
    messages: Vec<Message>,
    markers: std::collections::BTreeMap<String, i64>,
    follows: Vec<Follow>,
    deliveries: Vec<Delivery>,
}

impl Conversation<'_> {
    // standing is the message of an id, when it stands.
    fn standing(&self, id: i64) -> Option<&Message> {
        self.messages
            .iter()
            .find(|message| message.id == id && message.stands() && message.stand.is_some())
    }

    // latest is the standing message of another's with the greatest stand
    // in a listing: a thread's replies, or the top-level messages.
    fn latest(&self, thread: Option<i64>, subject: &str) -> Option<&Message> {
        self.messages
            .iter()
            .filter(|message| {
                message.thread == thread
                    && message.stands()
                    && message.stand.is_some()
                    && message.author != subject
            })
            .max_by_key(|message| message.stand)
    }

    // above is how many of others' standing messages of a listing stand
    // above a marker.
    fn above(&self, thread: Option<i64>, marker: i64, subject: &str) -> i64 {
        self.messages
            .iter()
            .filter(|message| {
                message.thread == thread
                    && message.stands()
                    && message.author != subject
                    && message.stand.is_some_and(|stand| stand > marker)
            })
            .count() as i64
    }
}

// item is an item of a kind for a message of a conversation.
fn item(kind: &str, at: &Conversation<'_>, message: &Message, unread: i64) -> Item {
    let key = Key {
        posted_at: message.posted_at.unwrap_or(message.created_at),
        kind: kind.to_owned(),
        schema: at.schema.to_owned(),
        instance: at.key.to_owned(),
        message: message.id,
    };
    let mut out = Map::new();
    out.insert("kind".into(), Value::from(kind));
    out.insert("schema".into(), Value::from(at.schema));
    out.insert("instanceId".into(), Value::from(at.key));
    out.insert("message".into(), Value::from(message.id));
    if kind != "conversation" {
        if let Some(thread) = message.thread {
            out.insert("thread".into(), Value::from(thread));
        }
    }
    out.insert("author".into(), Value::from(message.author.as_str()));
    if let Some(acted_for) = &message.for_subject {
        out.insert("for".into(), Value::from(acted_for.as_str()));
    }
    out.insert("postedAt".into(), Value::from(key.posted_at));
    out.insert("excerpt".into(), Value::from(excerpt(&message.body)));
    out.insert("unread".into(), Value::from(unread));
    Item {
        rank: rank_of(kind).unwrap_or(0),
        key,
        unread,
        value: Value::Object(out),
    }
}

// items is a conversation's items of a kind for the subject.
fn items(kind: &str, at: &Conversation<'_>, subject: &str) -> Vec<Item> {
    let mut out = Vec::new();
    match kind {
        "mention" => {
            for delivery in at
                .deliveries
                .iter()
                .filter(|delivery| delivery.subject == subject && delivery.mention)
            {
                let Some(message) = at.standing(delivery.message) else {
                    continue;
                };
                let marker = match message.thread {
                    None => at.markers.get(subject).copied(),
                    Some(thread) => at
                        .follows
                        .iter()
                        .find(|row| row.thread == thread && row.subject == subject)
                        .map(|row| row.read_up_to),
                };
                let unread = i64::from(message.stand.unwrap_or(0) > marker.unwrap_or(0));
                out.push(item(kind, at, message, unread));
            }
        }
        "thread" => {
            for follow in at
                .follows
                .iter()
                .filter(|row| row.subject == subject && row.followed)
            {
                let Some(message) = at.latest(Some(follow.thread), subject) else {
                    continue;
                };
                let unread = at.above(Some(follow.thread), follow.read_up_to, subject);
                out.push(item(kind, at, message, unread));
            }
        }
        _ => {
            let Some(marker) = at.markers.get(subject).copied() else {
                return out;
            };
            if let Some(message) = at.latest(None, subject) {
                let unread = at.above(None, marker, subject);
                out.push(item(kind, at, message, unread));
            }
        }
    }
    out
}

pub(super) fn inbox(cx: &NamespaceCx<'_>, params: &Value) -> Hook<Value> {
    let after = match params.get("cursor").and_then(Value::as_str) {
        None => None,
        Some(cursor) => Some(Key::decode(cursor).ok_or_else(|| {
            Refusal::operation_params(
                NAME,
                NAME,
                INBOX,
                vec![SchemaIssue::new(
                    "/cursor",
                    "is not a cursor this operation returned",
                )],
            )
        })?),
    };
    let limit =
        usize::try_from(params.get("limit").and_then(Value::as_i64).unwrap_or(PAGE)).unwrap_or(0);
    let unread = params.get("unread").and_then(Value::as_bool);
    let asked: Vec<&str> = match params.get("kinds").and_then(Value::as_array) {
        Some(kinds) => KINDS
            .iter()
            .map(|(kind, _)| *kind)
            .filter(|kind| kinds.iter().any(|asked| asked.as_str() == Some(kind)))
            .collect(),
        None => KINDS.iter().map(|(kind, _)| *kind).collect(),
    };
    let subject = cx.caller().subject.clone();
    // Every row of every type that composes Comments and the caller may
    // read, with its conversation's tables, a page at a time.
    let mut lists: Vec<Vec<Item>> = asked.iter().map(|_| Vec::new()).collect();
    let mut waiting = None;
    for (spec, entry) in cx.uses()? {
        let use_id = entry.id();
        let tables: Vec<TableRef> = [MESSAGES, DRAFTS, READS, FOLLOWS, DELIVERIES]
            .into_iter()
            .map(|table| TableRef::new(use_id, table))
            .collect();
        let mut after_key: Option<String> = None;
        loop {
            let page = match cx.read_rows(&spec.name, &Map::new(), &tables, after_key.as_deref()) {
                Ok(page) => page,
                // Every type's page is asked for in one round.
                Err(stop) if stop.is_waiting() => {
                    waiting = Some(stop);
                    break;
                }
                Err(stop) => return Err(stop),
            };
            for row in &page.rows {
                let table = |name: &str| -> &[SubRow] { row.table(use_id, name) };
                let held = drafts(table(DRAFTS));
                let messages = table(MESSAGES)
                    .iter()
                    .filter_map(Message::of)
                    .map(|mut message| {
                        if let Some(draft) = held.get(&message.id) {
                            message.body.clone_from(draft);
                        }
                        message
                    })
                    .collect();
                let at = Conversation {
                    schema: &spec.name,
                    key: &row.key,
                    messages,
                    markers: markers(table(READS)),
                    follows: table(FOLLOWS).iter().map(Follow::of).collect(),
                    deliveries: table(DELIVERIES).iter().map(Delivery::of).collect(),
                };
                for (list, kind) in lists.iter_mut().zip(&asked) {
                    list.extend(items(kind, &at, &subject));
                }
            }
            if !page.more {
                break;
            }
            after_key = page.rows.last().map(|row| row.key.clone());
        }
    }
    if let Some(stop) = waiting {
        return Err(stop);
    }
    // Each kind's list in its statement's order, filtered by unread and the
    // cursor, greatest first.
    let lists: Vec<Vec<Item>> = lists
        .into_iter()
        .map(|mut list| {
            list.retain(|item| match unread {
                Some(true) => item.unread > 0,
                Some(false) => item.unread == 0,
                None => true,
            });
            if let Some(after) = &after {
                let cursor_rank = rank_of(&after.kind).unwrap_or(0);
                list.retain(|item| {
                    if item.rank < cursor_rank {
                        item.key.posted_at <= after.posted_at
                    } else if item.rank > cursor_rank {
                        item.key.posted_at < after.posted_at
                    } else {
                        item.key.order() < after.order()
                    }
                });
            }
            list.sort_by(|a, b| b.key.order().cmp(&a.key.order()));
            list
        })
        .collect();
    let held: usize = lists.iter().map(|list| list.len().min(limit + 1)).sum();
    // Merge the lists by (postedAt, rank), each keeping its own order.
    let mut taken: Vec<&Item> = Vec::new();
    let mut heads: Vec<usize> = vec![0; lists.len()];
    while taken.len() < limit {
        let mut best: Option<(usize, (i64, i64))> = None;
        for (at, list) in lists.iter().enumerate() {
            if let Some(item) = list.get(heads[at]) {
                let order = (item.key.posted_at, item.rank);
                if best.is_none_or(|(_, held)| order > held) {
                    best = Some((at, order));
                }
            }
        }
        let Some((at, _)) = best else {
            break;
        };
        taken.push(&lists[at][heads[at]]);
        heads[at] += 1;
    }
    let next = if held > limit {
        taken.last().map(|item| item.key.cursor())
    } else {
        None
    };
    let mut out = Map::new();
    out.insert(
        "items".into(),
        Value::Array(taken.iter().map(|item| item.value.clone()).collect()),
    );
    out.insert("next".into(), next.map_or(Value::Null, Value::String));
    Ok(Value::Object(out))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_excerpt_folds_white_space_and_cuts_at_200_code_points() {
        assert_eq!(
            excerpt("Look,   carol\nand agent:ada."),
            "Look, carol and agent:ada."
        );
        let long = "a ".repeat(150);
        let cut = excerpt(&long);
        assert_eq!(cut.chars().count(), 200);
        assert!(cut.ends_with('\u{2026}'));
    }

    #[test]
    fn a_cursor_reads_back_what_it_wrote_and_nothing_else() {
        let key = Key {
            posted_at: 2000,
            kind: "mention".into(),
            schema: "Review".into(),
            instance: "r1".into(),
            message: 2,
        };
        assert_eq!(key.cursor(), "WzIwMDAsIm1lbnRpb24iLCJSZXZpZXciLCJyMSIsMl0");
        assert_eq!(Key::decode(&key.cursor()), Some(key));
        assert_eq!(Key::decode("nope"), None);
    }
}
