//! Comments' storage: migration 1 as shipped (the instance column `count`
//! and the table `comments`), and migration 2, `conversation`, which
//! creates the conversation's four tables, moves v1's comments into the
//! first and drops `comments`; migration 3, `drafts`, which adds a
//! composing message's columns, their two indexes and the table of its
//! draft's chunks (batch 2); the row a message is read from, and the
//! counts a write keeps (`comments-contract.md`, "Storage", "Migration 3").
//!
//! Every statement is the same on SQLite and Postgres (D58): each
//! parameter's type is the column's it is compared with or written to.

use std::collections::HashMap;

use serde_json::Value;

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, IndexSpec, InstanceCx, InstanceView, TableSpec,
};
use superschematic_engine::storage::behavior::BehaviorSql;
use superschematic_engine::storage::{Row, SqlValue};
use superschematic_engine::Result;

/// The messages, by `(namespace, schema, id, message)`.
pub(super) const MESSAGES: &str = "comment_messages";
/// What each message held before each of its edits.
pub(super) const EDITS: &str = "comment_edits";
/// Who reacted to a message with which emoji, in the order they reacted.
pub(super) const REACTIONS: &str = "comment_reactions";
/// One row per instance that has had a message: its counters and counts.
pub(super) const CONVERSATIONS: &str = "comment_conversations";
/// A composing message's draft, one row per non-empty append.
pub(super) const DRAFTS: &str = "comment_drafts";

/// A message row's columns, as [`Message::of`] reads them.
pub(super) const MESSAGE_COLUMNS: &str = "message, thread, quotes, author, via, state, body, blocks, mentions, created_at, posted_at, edited_at, edits, pinned, client_id, resolved_by, resolved_at, draft_length, last_append_at";

/// The states a message is in.
pub(super) const COMPOSING: &str = "composing";
pub(super) const POSTED: &str = "posted";
pub(super) const INTERRUPTED: &str = "interrupted";
pub(super) const DELETED: &str = "deleted";

/// The condition on a message row that it stands: posted or interrupted.
pub(super) const STANDS: &str = "state IN ('posted', 'interrupted')";

/// Comments' migrations: 1 as shipped, then 2 and 3.
pub(super) fn migrations() -> Vec<BehaviorMigration> {
    vec![
        BehaviorMigration::new(1, "comments")
            .column("count", ColumnSpec::integer().not_null().default(0))
            .table(
                TableSpec::new("comments")
                    .column("namespace", ColumnSpec::text().not_null())
                    .column("schema", ColumnSpec::text().not_null())
                    .column("id", ColumnSpec::text().not_null())
                    .column("comment", ColumnSpec::integer().not_null())
                    .column("reply_to", ColumnSpec::integer())
                    .column("body", ColumnSpec::text().not_null())
                    .column("created_by", ColumnSpec::text().not_null())
                    .column("created_at", ColumnSpec::integer().not_null())
                    .primary_key(&["namespace", "schema", "id", "comment"]),
            ),
        BehaviorMigration::new(2, "conversation")
            .table(
                instance_columns(TableSpec::new(MESSAGES))
                    .column("message", ColumnSpec::integer().not_null())
                    .column("thread", ColumnSpec::integer())
                    .column("quotes", ColumnSpec::integer())
                    .column("author", ColumnSpec::text().not_null())
                    .column("via", ColumnSpec::text())
                    .column("state", ColumnSpec::text().not_null())
                    .column("body", ColumnSpec::text().not_null())
                    .column("blocks", ColumnSpec::text().not_null())
                    .column("mentions", ColumnSpec::text().not_null())
                    .column("created_at", ColumnSpec::integer().not_null())
                    .column("edited_at", ColumnSpec::integer())
                    .column("edits", ColumnSpec::integer().not_null().default(0))
                    .column("pinned", ColumnSpec::integer().not_null().default(0))
                    .column("client_id", ColumnSpec::text())
                    .column("resolved_by", ColumnSpec::text())
                    .column("resolved_at", ColumnSpec::integer())
                    .primary_key(&["namespace", "schema", "id", "message"])
                    .index(IndexSpec::new(
                        "comment_messages_thread",
                        &["namespace", "schema", "id", "thread", "message"],
                    ))
                    .index(
                        IndexSpec::new(
                            "comment_messages_client",
                            &["namespace", "schema", "id", "author", "client_id"],
                        )
                        .unique()
                        .filter("client_id IS NOT NULL"),
                    ),
            )
            .table(
                instance_columns(TableSpec::new(EDITS))
                    .column("message", ColumnSpec::integer().not_null())
                    .column("edit", ColumnSpec::integer().not_null())
                    .column("body", ColumnSpec::text().not_null())
                    .column("blocks", ColumnSpec::text().not_null())
                    .column("mentions", ColumnSpec::text().not_null())
                    .column("edited_by", ColumnSpec::text().not_null())
                    .column("edited_at", ColumnSpec::integer().not_null())
                    .primary_key(&["namespace", "schema", "id", "message", "edit"]),
            )
            .table(
                instance_columns(TableSpec::new(REACTIONS))
                    .column("message", ColumnSpec::integer().not_null())
                    .column("emoji", ColumnSpec::text().not_null())
                    .column("subject", ColumnSpec::text().not_null())
                    .column("reaction", ColumnSpec::integer().not_null())
                    .column("reacted_at", ColumnSpec::integer().not_null())
                    .primary_key(&["namespace", "schema", "id", "message", "emoji", "subject"])
                    .index(IndexSpec::new(
                        "comment_reactions_order",
                        &["namespace", "schema", "id", "message", "emoji", "reaction"],
                    )),
            )
            .table(
                instance_columns(TableSpec::new(CONVERSATIONS))
                    .column("last_message", ColumnSpec::integer().not_null())
                    .column("last_reaction", ColumnSpec::integer().not_null().default(0))
                    .column("last_message_at", ColumnSpec::integer())
                    .column("open_threads", ColumnSpec::integer().not_null().default(0))
                    .column(
                        "resolved_threads",
                        ColumnSpec::integer().not_null().default(0),
                    )
                    .primary_key(&["namespace", "schema", "id"]),
            )
            .up(move_comments),
        BehaviorMigration::new(3, "drafts")
            .table(
                instance_columns(TableSpec::new(DRAFTS))
                    .column("message", ColumnSpec::integer().not_null())
                    .column("chunk_offset", ColumnSpec::integer().not_null())
                    .column("text", ColumnSpec::text().not_null())
                    .primary_key(&["namespace", "schema", "id", "message", "chunk_offset"]),
            )
            .up(add_drafts),
    ]
}

// add_drafts adds a composing message's columns to comment_messages, and
// the two indexes on them, and sets every existing message's posted_at to
// its created_at: each was posted by a post, and stood from then.
fn add_drafts(sql: &BehaviorSql<'_, '_>) -> superschematic_engine::storage::Result<()> {
    let messages = sql.table(MESSAGES)?;
    for column in ["draft_length", "last_append_at", "posted_at"] {
        sql.run(
            format!(
                "ALTER TABLE {messages} ADD COLUMN {column} {}",
                ColumnSpec::integer().definition(sql.dialect())?
            ),
            &[],
        )?;
    }
    sql.run(
        format!(
            "CREATE INDEX {} ON {messages} (namespace, schema, id, message) WHERE last_append_at IS NOT NULL",
            sql.table("comment_messages_composing")?
        ),
        &[],
    )?;
    sql.run(
        format!(
            "CREATE INDEX {} ON {messages} (namespace, schema, last_append_at, id, message) WHERE last_append_at IS NOT NULL",
            sql.table("comment_messages_idle")?
        ),
        &[],
    )?;
    sql.run(format!("UPDATE {messages} SET posted_at = created_at"), &[])?;
    Ok(())
}

// instance_columns leads a table with the instance's key.
fn instance_columns(spec: TableSpec) -> TableSpec {
    spec.column("namespace", ColumnSpec::text().not_null())
        .column("schema", ColumnSpec::text().not_null())
        .column("id", ColumnSpec::text().not_null())
}

// How many of v1's comments the move reads at a time.
const MOVE_PAGE: i64 = 1000;

// move_comments moves v1's comments into comment_messages, a page at a
// time in ascending (namespace, schema, id, comment) order: a comment's
// thread is the root of its reply_to chain, and it quotes its reply_to when
// that is a reply. Then one conversation row per instance with comments,
// and v1's table goes.
fn move_comments(sql: &BehaviorSql<'_, '_>) -> superschematic_engine::storage::Result<()> {
    let comments = sql.table("comments")?;
    let messages = sql.table(MESSAGES)?;
    let conversations = sql.table(CONVERSATIONS)?;
    let mut instance: Option<(String, String, String)> = None;
    // Each comment of the instance being moved, by number: its thread's
    // root.
    let mut roots: HashMap<i64, i64> = HashMap::new();
    let mut last: Option<Vec<SqlValue>> = None;
    loop {
        let rows = match &last {
            None => sql.all(
                format!(
                    "SELECT namespace, schema, id, comment, reply_to, body, created_by, created_at FROM {comments}
                     ORDER BY namespace, schema, id, comment LIMIT ?"
                ),
                &[SqlValue::Integer(MOVE_PAGE)],
            )?,
            Some(after) => {
                let mut params = after.clone();
                params.push(SqlValue::Integer(MOVE_PAGE));
                sql.all(
                    format!(
                        "SELECT namespace, schema, id, comment, reply_to, body, created_by, created_at FROM {comments}
                         WHERE (namespace, schema, id, comment) > (?, ?, ?, ?)
                         ORDER BY namespace, schema, id, comment LIMIT ?"
                    ),
                    &params,
                )?
            }
        };
        for row in &rows {
            let key = (
                row.text("namespace")?.to_owned(),
                row.text("schema")?.to_owned(),
                row.text("id")?.to_owned(),
            );
            if instance.as_ref() != Some(&key) {
                instance = Some(key.clone());
                roots.clear();
            }
            let comment = row.i64("comment")?;
            let reply_to = row.opt_i64("reply_to")?;
            // A reply_to that names no comment (none can) makes the comment
            // top-level.
            let root = reply_to.and_then(|to| roots.get(&to).copied().map(|root| (to, root)));
            let (thread, quotes) = match root {
                None => (None, None),
                Some((to, root)) => (Some(root), (to != root).then_some(to)),
            };
            roots.insert(comment, thread.unwrap_or(comment));
            let (namespace, schema, id) = key;
            sql.run(
                format!(
                    "INSERT INTO {messages} (namespace, schema, id, message, thread, quotes, author, via, state, body, blocks, mentions, created_at, edited_at, edits, pinned, client_id, resolved_by, resolved_at)
                     VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, NULL, 0, 0, NULL, NULL, NULL)"
                ),
                &[
                    SqlValue::from(namespace),
                    SqlValue::from(schema),
                    SqlValue::from(id),
                    SqlValue::Integer(comment),
                    thread.map_or(SqlValue::Null, SqlValue::Integer),
                    quotes.map_or(SqlValue::Null, SqlValue::Integer),
                    SqlValue::from(row.text("created_by")?),
                    SqlValue::from(POSTED),
                    SqlValue::from(row.text("body")?),
                    SqlValue::from("[]"),
                    SqlValue::from("[]"),
                    SqlValue::Integer(row.i64("created_at")?),
                ],
            )?;
        }
        if (rows.len() as i64) < MOVE_PAGE {
            break;
        }
        let tail = rows.last().expect("a full page has a last row");
        last = Some(vec![
            SqlValue::from(tail.text("namespace")?),
            SqlValue::from(tail.text("schema")?),
            SqlValue::from(tail.text("id")?),
            SqlValue::Integer(tail.i64("comment")?),
        ]);
    }
    sql.run(
        format!(
            "INSERT INTO {conversations} (namespace, schema, id, last_message, last_reaction, last_message_at, open_threads, resolved_threads)
             SELECT namespace, schema, id, MAX(message), 0, MAX(created_at), COUNT(CASE WHEN thread IS NULL THEN 1 END), 0
             FROM {messages} GROUP BY namespace, schema, id"
        ),
        &[],
    )?;
    sql.run(format!("DROP TABLE {comments}"), &[])?;
    Ok(())
}

/// The behavior's rows of one instance, as its SQL names them.
pub(super) fn key<C>(view: &InstanceView<'_, C>) -> Vec<SqlValue> {
    vec![
        SqlValue::from(view.namespace()),
        SqlValue::from(view.schema()),
        SqlValue::from(view.id()),
    ]
}

/// [`key`] with more values after it.
pub(super) fn keyed<C>(
    view: &InstanceView<'_, C>,
    more: impl IntoIterator<Item = SqlValue>,
) -> Vec<SqlValue> {
    let mut values = key(view);
    values.extend(more);
    values
}

/// `?, ?, ?` for `count` values of an `IN` list.
pub(super) fn placeholders(count: usize) -> String {
    vec!["?"; count].join(", ")
}

/// A message's row.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct Message {
    pub id: i64,
    pub thread: Option<i64>,
    pub quotes: Option<i64>,
    pub author: String,
    pub via: Option<String>,
    pub state: String,
    pub body: String,
    /// The JSON array as stored.
    pub blocks: Value,
    pub mentions: Value,
    pub created_at: i64,
    /// When it came to stand; `None` while composing and on a message that
    /// never stood.
    pub posted_at: Option<i64>,
    pub edited_at: Option<i64>,
    pub edits: i64,
    pub pinned: bool,
    pub resolved_by: Option<String>,
    pub resolved_at: Option<i64>,
    /// The draft's length in code points, while composing.
    pub draft_length: Option<i64>,
    /// The `now` of its begin or latest appendDraft, while composing.
    pub last_append_at: Option<i64>,
}

impl Message {
    pub fn of(row: &Row) -> Result<Self> {
        Ok(Message {
            id: row.i64("message")?,
            thread: row.opt_i64("thread")?,
            quotes: row.opt_i64("quotes")?,
            author: row.text("author")?.to_owned(),
            via: row.opt_text("via")?.map(str::to_owned),
            state: row.text("state")?.to_owned(),
            body: row.text("body")?.to_owned(),
            blocks: json_array(row.text("blocks")?),
            mentions: json_array(row.text("mentions")?),
            created_at: row.i64("created_at")?,
            posted_at: row.opt_i64("posted_at")?,
            edited_at: row.opt_i64("edited_at")?,
            edits: row.i64("edits")?,
            pinned: row.i64("pinned")? != 0,
            resolved_by: row.opt_text("resolved_by")?.map(str::to_owned),
            resolved_at: row.opt_i64("resolved_at")?,
            draft_length: row.opt_i64("draft_length")?,
            last_append_at: row.opt_i64("last_append_at")?,
        })
    }

    /// Whether it stands: posted or interrupted.
    pub fn stands(&self) -> bool {
        self.state == POSTED || self.state == INTERRUPTED
    }

    pub fn composing(&self) -> bool {
        self.state == COMPOSING
    }

    pub fn deleted(&self) -> bool {
        self.state == DELETED
    }

    pub fn top_level(&self) -> bool {
        self.thread.is_none()
    }
}

// json_array reads a stored array, which the behavior wrote.
pub(super) fn json_array(text: &str) -> Value {
    serde_json::from_str(text).unwrap_or_else(|_| Value::Array(Vec::new()))
}

/// A present message of the instance, `None` when it has none with the id
/// (never posted, or purged).
pub(super) fn message<C>(view: &InstanceView<'_, C>, id: i64) -> Result<Option<Message>> {
    let table = view.sql().table(MESSAGES)?;
    view.sql()
        .get(
            format!(
                "SELECT {MESSAGE_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
            ),
            &keyed(view, [SqlValue::Integer(id)]),
        )?
        .as_ref()
        .map(Message::of)
        .transpose()
}

/// The instance's conversation row's counters and counts.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(super) struct Conversation {
    pub last_message: i64,
    pub last_reaction: i64,
    pub last_message_at: Option<i64>,
    pub open_threads: i64,
    pub resolved_threads: i64,
}

/// The instance's conversation row, `None` before its first message.
pub(super) fn conversation<C>(view: &InstanceView<'_, C>) -> Result<Option<Conversation>> {
    let table = view.sql().table(CONVERSATIONS)?;
    view.sql()
        .get(
            format!(
                "SELECT last_message, last_reaction, last_message_at, open_threads, resolved_threads FROM {table}
                 WHERE namespace = ? AND schema = ? AND id = ?"
            ),
            &key(view),
        )?
        .map(|row| {
            Ok(Conversation {
                last_message: row.i64("last_message")?,
                last_reaction: row.i64("last_reaction")?,
                last_message_at: row.opt_i64("last_message_at")?,
                open_threads: row.i64("open_threads")?,
                resolved_threads: row.i64("resolved_threads")?,
            })
        })
        .transpose()
}

/// Writes the instance's conversation row, inserting it when `existed` is
/// false.
pub(super) fn save_conversation<C>(
    cx: &InstanceCx<'_, C>,
    existed: bool,
    conversation: &Conversation,
) -> Result<()> {
    let table = cx.sql().table(CONVERSATIONS)?;
    let values = [
        SqlValue::Integer(conversation.last_message),
        SqlValue::Integer(conversation.last_reaction),
        conversation
            .last_message_at
            .map_or(SqlValue::Null, SqlValue::Integer),
        SqlValue::Integer(conversation.open_threads),
        SqlValue::Integer(conversation.resolved_threads),
    ];
    if existed {
        let mut params = values.to_vec();
        params.extend(key(cx));
        cx.sql().run(
            format!(
                "UPDATE {table} SET last_message = ?, last_reaction = ?, last_message_at = ?, open_threads = ?, resolved_threads = ?
                 WHERE namespace = ? AND schema = ? AND id = ?"
            ),
            &params,
        )?;
    } else {
        cx.sql().run(
            format!(
                "INSERT INTO {table} (namespace, schema, id, last_message, last_reaction, last_message_at, open_threads, resolved_threads)
                 VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
            ),
            &keyed(cx, values),
        )?;
    }
    Ok(())
}

/// How a write moves the counts the fields read: standing messages, and
/// standing top-level ones open and resolved.
#[derive(Clone, Copy, Debug, Default)]
pub(super) struct Moves {
    pub messages: i64,
    pub open: i64,
    pub resolved: i64,
    /// The `postedAt` of a message that came to stand: a post's, a
    /// finish's or an interrupt's `now`.
    pub posted_at: Option<i64>,
    /// Whether a standing message left, so the greatest `postedAt` is read
    /// again.
    pub left: bool,
}

impl Moves {
    /// What a message coming to stand at `now` moves: a post, a finish, an
    /// interrupt that keeps text.
    pub fn standing(top_level: bool, now: i64) -> Self {
        Moves {
            messages: 1,
            open: i64::from(top_level),
            posted_at: Some(now),
            ..Moves::default()
        }
    }

    /// What a standing message leaving moves.
    pub fn leaving(&mut self, message: &Message) {
        if !message.stands() {
            return;
        }
        self.messages -= 1;
        self.left = true;
        if message.top_level() {
            if message.resolved_at.is_some() {
                self.resolved -= 1;
            } else {
                self.open -= 1;
            }
        }
    }
}

/// Moves the counts: the instance column `count`, and the conversation
/// row's `last_message_at`, `open_threads` and `resolved_threads`.
pub(super) fn move_counts<C>(
    cx: &InstanceCx<'_, C>,
    conversation: &mut Conversation,
    moves: &Moves,
) -> Result<()> {
    if moves.messages != 0 {
        let count = cx.columns().get()?.i64("count")?;
        cx.columns()
            .set(&[("count", SqlValue::Integer(count + moves.messages))])?;
    }
    conversation.open_threads += moves.open;
    conversation.resolved_threads += moves.resolved;
    if moves.left {
        let table = cx.sql().table(MESSAGES)?;
        conversation.last_message_at = cx
            .sql()
            .get(
                format!(
                    "SELECT MAX(posted_at) AS latest FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND {STANDS}"
                ),
                &key(cx),
            )?
            .map(|row| row.opt_i64("latest"))
            .transpose()?
            .flatten();
    }
    if let Some(at) = moves.posted_at {
        conversation.last_message_at =
            Some(conversation.last_message_at.map_or(at, |was| was.max(at)));
    }
    Ok(())
}

/// The subjects with a composing message on the instance, each once, by
/// the least id of its composing messages: the `composing` field.
pub(super) fn composing<C>(view: &InstanceView<'_, C>) -> Result<Vec<String>> {
    let table = view.sql().table(MESSAGES)?;
    view.sql()
        .all(
            format!(
                "SELECT author, MIN(message) AS first FROM {table}
                 WHERE namespace = ? AND schema = ? AND id = ? AND last_append_at IS NOT NULL
                 GROUP BY author ORDER BY first"
            ),
            &key(view),
        )?
        .iter()
        .map(|row| Ok(row.text("author")?.to_owned()))
        .collect()
}

/// The drafts of the composing messages among `ids`, by message: each its
/// chunks concatenated in `chunk_offset` order.
pub(super) fn drafts<C>(view: &InstanceView<'_, C>, ids: &[i64]) -> Result<HashMap<i64, String>> {
    let mut out: HashMap<i64, String> = HashMap::new();
    if ids.is_empty() {
        return Ok(out);
    }
    let table = view.sql().table(DRAFTS)?;
    let rows = view.sql().all(
        format!(
            "SELECT message, text FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND message IN ({})
             ORDER BY message, chunk_offset",
            placeholders(ids.len())
        ),
        &keyed(view, ids.iter().map(|&id| SqlValue::Integer(id))),
    )?;
    for row in &rows {
        out.entry(row.i64("message")?)
            .or_default()
            .push_str(row.text("text")?);
    }
    Ok(out)
}
