//! Comments' `inbox` (D77 step 3, `comments-contract.md`, "inbox"), a
//! namespace operation that reads: the caller's mentions, the threads it
//! follows and the conversations it has read, across every schema of the
//! namespace that composes Comments and that the caller may read, newest
//! first, one statement per kind merged by `(postedAt, rank)`, and paged by
//! a cursor over an item's key.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::paging::{base64url_decode, base64url_encode};
use superschematic_engine::behaviors::NamespaceCx;
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::Result;

use super::checks::{integer, invalid};
use super::storage::{DELIVERIES, FOLLOWS, MESSAGES, READS};
use super::CommentsConfig;

type Ncx<'a, 'b> = &'a NamespaceCx<'b, CommentsConfig>;

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

    // decode reads a cursor back: exactly an array of five, an integer,
    // one of the kinds, two strings and an integer >= 1.
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
}

// One item as its statement read it.
struct Item {
    key: Key,
    rank: i64,
    value: Value,
}

/// The message's body with each run of white space one space, trimmed,
/// and when longer than 200 code points its first 199, a trailing space
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

pub(super) fn inbox(cx: Ncx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "inbox";
    let after = match params.get("cursor").and_then(Value::as_str) {
        None => None,
        Some(cursor) => Some(
            Key::decode(cursor)
                .ok_or_else(|| invalid(OP, "/cursor", "is not a cursor this operation returned"))?,
        ),
    };
    let limit = integer(params.get("limit")).unwrap_or(PAGE);
    let unread = params.get("unread").and_then(Value::as_bool);
    let asked: Vec<&str> = match params.get("kinds").and_then(Value::as_array) {
        Some(kinds) => KINDS
            .iter()
            .map(|(kind, _)| *kind)
            .filter(|kind| kinds.iter().any(|asked| asked.as_str() == Some(kind)))
            .collect(),
        None => KINDS.iter().map(|(kind, _)| *kind).collect(),
    };
    let mut lists = Vec::new();
    for kind in asked {
        lists.push(list(cx, kind, unread, after.as_ref(), limit)?);
    }
    let held: usize = lists.iter().map(Vec::len).sum();
    let limit = usize::try_from(limit).unwrap_or(0);
    // Merge by (postedAt, rank) descending, each list in its own order.
    let mut lists: Vec<_> = lists
        .into_iter()
        .map(|list| list.into_iter().peekable())
        .collect();
    let mut taken: Vec<Item> = Vec::new();
    while taken.len() < limit {
        let mut best: Option<(usize, (i64, i64))> = None;
        for (at, list) in lists.iter_mut().enumerate() {
            if let Some(item) = list.peek() {
                let order = (item.key.posted_at, item.rank);
                if best.is_none_or(|(_, held)| order > held) {
                    best = Some((at, order));
                }
            }
        }
        let Some(item) = best.and_then(|(at, _)| lists[at].next()) else {
            break;
        };
        taken.push(item);
    }
    let next = if held > limit {
        taken.last().map(|item| item.key.cursor())
    } else {
        None
    };
    let mut out = Map::new();
    out.insert(
        "items".into(),
        Value::Array(taken.into_iter().map(|item| item.value).collect()),
    );
    out.insert("next".into(), next.map_or(Value::Null, Value::String));
    Ok(Value::Object(out))
}

// list reads one kind's items: its statement, after the cursor, with the
// unread filter, `limit + 1` of them.
fn list(
    cx: Ncx<'_, '_>,
    kind: &str,
    unread: Option<bool>,
    after: Option<&Key>,
    limit: i64,
) -> Result<Vec<Item>> {
    let sql = cx.sql();
    let readable = sql.readable()?;
    let messages = sql.table(MESSAGES)?;
    let stands = "IN ('posted', 'interrupted')";
    let inner = match kind {
        "mention" => {
            let deliveries = sql.table(DELIVERIES)?;
            let reads = sql.table(READS)?;
            let follows = sql.table(FOLLOWS)?;
            format!(
                "SELECT d.schema, d.id, d.message, m.thread, m.author, m.for_subject, m.posted_at, m.body,
                        CASE WHEN m.stand > COALESCE(CASE WHEN m.thread IS NULL THEN r.up_to ELSE f.read_up_to END, 0)
                             THEN 1 ELSE 0 END AS unread
                   FROM {deliveries} d
                   JOIN {readable} k ON k.schema = d.schema
                   JOIN {messages} m
                     ON m.namespace = d.namespace AND m.schema = d.schema AND m.id = d.id AND m.message = d.message
                   LEFT JOIN {reads} r
                     ON r.namespace = d.namespace AND r.schema = d.schema AND r.id = d.id AND r.subject = d.subject
                   LEFT JOIN {follows} f
                     ON f.namespace = d.namespace AND f.schema = d.schema AND f.id = d.id
                    AND f.thread = m.thread AND f.subject = d.subject
                  WHERE d.namespace = ? AND d.subject = ? AND d.mention = 1
                    AND m.state {stands} AND m.stand IS NOT NULL"
            )
        }
        "thread" => {
            let follows = sql.table(FOLLOWS)?;
            format!(
                "SELECT f.schema, f.id, f.thread, m.message, m.author, m.for_subject, m.posted_at, m.body,
                        (SELECT COUNT(*) FROM {messages} u
                          WHERE u.namespace = f.namespace AND u.schema = f.schema AND u.id = f.id
                            AND u.thread = f.thread AND u.stand IS NOT NULL AND u.stand > f.read_up_to
                            AND u.state {stands} AND u.author <> f.subject) AS unread
                   FROM {follows} f
                   JOIN {readable} k ON k.schema = f.schema
                   JOIN {messages} m
                     ON m.namespace = f.namespace AND m.schema = f.schema AND m.id = f.id
                    AND m.message = (SELECT l.message FROM {messages} l
                                      WHERE l.namespace = f.namespace AND l.schema = f.schema AND l.id = f.id
                                        AND l.thread = f.thread AND l.stand IS NOT NULL
                                        AND l.state {stands} AND l.author <> f.subject
                                      ORDER BY l.stand DESC LIMIT 1)
                  WHERE f.namespace = ? AND f.subject = ? AND f.followed = 1"
            )
        }
        _ => {
            let reads = sql.table(READS)?;
            format!(
                "SELECT r.schema, r.id, m.message, m.author, m.for_subject, m.posted_at, m.body,
                        (SELECT COUNT(*) FROM {messages} u
                          WHERE u.namespace = r.namespace AND u.schema = r.schema AND u.id = r.id
                            AND u.thread IS NULL AND u.stand IS NOT NULL AND u.stand > r.up_to
                            AND u.state {stands} AND u.author <> r.subject) AS unread
                   FROM {reads} r
                   JOIN {readable} k ON k.schema = r.schema
                   JOIN {messages} m
                     ON m.namespace = r.namespace AND m.schema = r.schema AND m.id = r.id
                    AND m.message = (SELECT l.message FROM {messages} l
                                      WHERE l.namespace = r.namespace AND l.schema = r.schema AND l.id = r.id
                                        AND l.thread IS NULL AND l.stand IS NOT NULL
                                        AND l.state {stands} AND l.author <> r.subject
                                      ORDER BY l.stand DESC LIMIT 1)
                  WHERE r.namespace = ? AND r.subject = ?"
            )
        }
    };
    let rank = rank_of(kind).unwrap_or(0);
    let mut params = vec![
        SqlValue::from(cx.namespace()),
        SqlValue::from(cx.principal().subject.as_str()),
    ];
    let mut conditions: Vec<&str> = Vec::new();
    match unread {
        Some(true) => conditions.push("x.unread > 0"),
        Some(false) => conditions.push("x.unread = 0"),
        None => {}
    }
    if let Some(after) = after {
        let cursor_rank = rank_of(&after.kind).unwrap_or(0);
        if rank < cursor_rank {
            conditions.push("x.posted_at <= ?");
            params.push(SqlValue::Integer(after.posted_at));
        } else if rank > cursor_rank {
            conditions.push("x.posted_at < ?");
            params.push(SqlValue::Integer(after.posted_at));
        } else {
            conditions.push("(x.posted_at, x.schema, x.id, x.message) < (?, ?, ?, ?)");
            params.extend([
                SqlValue::Integer(after.posted_at),
                SqlValue::from(after.schema.as_str()),
                SqlValue::from(after.instance.as_str()),
                SqlValue::Integer(after.message),
            ]);
        }
    }
    params.push(SqlValue::Integer(limit + 1));
    let filter = if conditions.is_empty() {
        String::new()
    } else {
        format!(" WHERE {}", conditions.join(" AND "))
    };
    let rows = sql.all(
        format!(
            "SELECT * FROM ({inner}) x{filter}
              ORDER BY x.posted_at DESC, x.schema DESC, x.id DESC, x.message DESC
              LIMIT ?"
        ),
        &params,
    )?;
    rows.iter().map(|row| item(kind, rank, row)).collect()
}

fn item(kind: &str, rank: i64, row: &Row) -> Result<Item> {
    let key = Key {
        posted_at: row.i64("posted_at")?,
        kind: kind.to_owned(),
        schema: row.text("schema")?.to_owned(),
        instance: row.text("id")?.to_owned(),
        message: row.i64("message")?,
    };
    let thread = match kind {
        "conversation" => None,
        _ => row.opt_i64("thread")?,
    };
    let mut out = Map::new();
    out.insert("kind".into(), Value::from(kind));
    out.insert("schema".into(), Value::from(key.schema.as_str()));
    out.insert("instanceId".into(), Value::from(key.instance.as_str()));
    out.insert("message".into(), Value::from(key.message));
    if let Some(thread) = thread {
        out.insert("thread".into(), Value::from(thread));
    }
    out.insert("author".into(), Value::from(row.text("author")?));
    if let Some(subject) = row.opt_text("for_subject")? {
        out.insert("for".into(), Value::from(subject));
    }
    out.insert("postedAt".into(), Value::from(key.posted_at));
    out.insert("excerpt".into(), Value::from(excerpt(row.text("body")?)));
    out.insert("unread".into(), Value::from(row.i64("unread")?));
    Ok(Item {
        key,
        rank,
        value: Value::Object(out),
    })
}
