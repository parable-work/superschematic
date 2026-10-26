//! Comments' read state and follows (D77 step 3, `comments-contract.md`,
//! "Batch 3"): each message's stand, which it takes when it comes to stand;
//! the deliveries a standing message makes for its mentions and its
//! thread's followers; who follows without asking (the author, the
//! mentioned); `markRead` and `follow`, which write only the caller's own
//! rows, append no event and ask read; the `unread` field and
//! `listMessages`' `readUpTo`; and what `edit`, `deleteMessage`, `purge`
//! and the instance's delete do to the deliveries and follows.

use std::collections::BTreeMap;

use serde_json::Value;

use superschematic_engine::behaviors::{InstanceCx, InstanceView};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::Result;

use super::checks::{integer, not_a_thread, object, present};
use super::storage::{
    conversation, key, keyed, placeholders, Message, DELIVERIES, FOLLOWS, MESSAGES, READS, STANDS,
};
use super::writes::Cx;
use super::CommentsConfig;

/// A subject that names a group: it gets its deliveries as given, and
/// follows nothing.
const ROLE: &str = "role:";

/// The distinct subjects of a `mentions` array, in their order.
pub(super) fn subjects(mentions: Option<&Value>) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for subject in mentions
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
    {
        if !out.iter().any(|seen| seen == subject) {
            out.push(subject.to_owned());
        }
    }
    out
}

// A subject's row on a thread.
struct Follow {
    followed: bool,
    read_up_to: i64,
}

fn follow_row<C>(view: &InstanceView<'_, C>, thread: i64, subject: &str) -> Result<Option<Follow>> {
    let table = view.sql().table(FOLLOWS)?;
    view.sql()
        .get(
            format!(
                "SELECT followed, read_up_to FROM {table}
                 WHERE namespace = ? AND schema = ? AND id = ? AND thread = ? AND subject = ?"
            ),
            &keyed(view, [SqlValue::Integer(thread), SqlValue::from(subject)]),
        )?
        .map(|row| {
            Ok(Follow {
                followed: row.i64("followed")? != 0,
                read_up_to: row.i64("read_up_to")?,
            })
        })
        .transpose()
}

// write_follow inserts the subject's row on the thread, or updates the one
// it has (`existed`), with `updated_at = now`.
fn write_follow<C>(
    cx: &InstanceCx<'_, C>,
    existed: bool,
    thread: i64,
    subject: &str,
    followed: bool,
    read_up_to: i64,
) -> Result<()> {
    let table = cx.sql().table(FOLLOWS)?;
    let values = [
        SqlValue::Integer(i64::from(followed)),
        SqlValue::Integer(read_up_to),
        SqlValue::Integer(cx.now()),
    ];
    if existed {
        let mut params = values.to_vec();
        params.extend(keyed(
            cx,
            [SqlValue::Integer(thread), SqlValue::from(subject)],
        ));
        cx.sql().run(
            format!(
                "UPDATE {table} SET followed = ?, read_up_to = ?, updated_at = ?
                 WHERE namespace = ? AND schema = ? AND id = ? AND thread = ? AND subject = ?"
            ),
            &params,
        )?;
    } else {
        cx.sql().run(
            format!(
                "INSERT INTO {table} (namespace, schema, id, thread, subject, followed, read_up_to, updated_at)
                 VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
            ),
            &keyed(
                cx,
                [SqlValue::Integer(thread), SqlValue::from(subject)]
                    .into_iter()
                    .chain(values),
            ),
        )?;
    }
    Ok(())
}

/// The subject's conversation marker on the instance, `None` when it has
/// none.
pub(super) fn conversation_marker<C>(
    view: &InstanceView<'_, C>,
    subject: &str,
) -> Result<Option<i64>> {
    let table = view.sql().table(READS)?;
    Ok(view
        .sql()
        .get(
            format!(
                "SELECT up_to FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND subject = ?"
            ),
            &keyed(view, [SqlValue::from(subject)]),
        )?
        .map(|row| row.i64("up_to"))
        .transpose()?)
}

// move_conversation_marker moves the subject's conversation marker up to
// `to`, never back, the row inserted at `to` when absent.
fn move_conversation_marker<C>(cx: &InstanceCx<'_, C>, subject: &str, to: i64) -> Result<i64> {
    let table = cx.sql().table(READS)?;
    let now = SqlValue::Integer(cx.now());
    match conversation_marker(cx, subject)? {
        Some(was) => {
            let up_to = was.max(to);
            let mut params = vec![SqlValue::Integer(up_to), now];
            params.extend(keyed(cx, [SqlValue::from(subject)]));
            cx.sql().run(
                format!(
                    "UPDATE {table} SET up_to = ?, read_at = ?
                     WHERE namespace = ? AND schema = ? AND id = ? AND subject = ?"
                ),
                &params,
            )?;
            Ok(up_to)
        }
        None => {
            cx.sql().run(
                format!(
                    "INSERT INTO {table} (namespace, schema, id, subject, up_to, read_at) VALUES (?, ?, ?, ?, ?, ?)"
                ),
                &keyed(cx, [SqlValue::from(subject), SqlValue::Integer(to), now]),
            )?;
            Ok(to)
        }
    }
}

/// The subject's thread marker on the thread, `None` when it has no row.
pub(super) fn thread_marker<C>(
    view: &InstanceView<'_, C>,
    thread: i64,
    subject: &str,
) -> Result<Option<i64>> {
    Ok(follow_row(view, thread, subject)?.map(|row| row.read_up_to))
}

/// The `unread` field: the reader's count of others' standing top-level
/// messages whose stand is above its conversation marker; 0 with none.
pub(super) fn unread<C>(view: &InstanceView<'_, C>) -> Result<i64> {
    let subject = view.principal().subject.as_str();
    let messages = view.sql().table(MESSAGES)?;
    let reads = view.sql().table(READS)?;
    let mut params = vec![SqlValue::from(subject)];
    params.extend(key(view));
    params.push(SqlValue::from(subject));
    Ok(view
        .sql()
        .get(
            format!(
                "SELECT COUNT(*) AS unread
                   FROM {messages} u
                   JOIN {reads} r
                     ON r.namespace = u.namespace AND r.schema = u.schema AND r.id = u.id AND r.subject = ?
                  WHERE u.namespace = ? AND u.schema = ? AND u.id = ? AND u.thread IS NULL
                    AND u.stand IS NOT NULL AND u.stand > r.up_to
                    AND u.{STANDS} AND u.author <> ?"
            ),
            &params,
        )?
        .map(|row| row.i64("unread"))
        .transpose()?
        .unwrap_or(0))
}

// thread_unread is how many of others' standing replies in the thread
// stand above `marker`.
fn thread_unread<C>(
    view: &InstanceView<'_, C>,
    thread: i64,
    marker: i64,
    subject: &str,
) -> Result<i64> {
    let messages = view.sql().table(MESSAGES)?;
    Ok(view
        .sql()
        .get(
            format!(
                "SELECT COUNT(*) AS unread FROM {messages}
                  WHERE namespace = ? AND schema = ? AND id = ? AND thread = ?
                    AND stand IS NOT NULL AND stand > ? AND {STANDS} AND author <> ?"
            ),
            &keyed(
                view,
                [
                    SqlValue::Integer(thread),
                    SqlValue::Integer(marker),
                    SqlValue::from(subject),
                ],
            ),
        )?
        .map(|row| row.i64("unread"))
        .transpose()?
        .unwrap_or(0))
}

/// `listMessages`' `readUpTo`: the caller's conversation marker, or with
/// `thread` its thread marker there; `None` when it has none.
pub(super) fn read_up_to<C>(
    view: &InstanceView<'_, C>,
    thread: Option<i64>,
) -> Result<Option<i64>> {
    let subject = view.principal().subject.as_str();
    match thread {
        None => conversation_marker(view, subject),
        Some(thread) => thread_marker(view, thread, subject),
    }
}

// insert_delivery inserts a pending delivery of the message to the
// subject, due now.
fn insert_delivery(
    cx: Cx<'_, '_>,
    message: i64,
    subject: &str,
    mention: bool,
    reply: bool,
) -> Result<()> {
    let table = cx.sql().table(DELIVERIES)?;
    let now = cx.now();
    cx.sql().run(
        format!(
            "INSERT INTO {table} (namespace, schema, id, message, subject, mention, reply, state, due_at, token, claimed_by, attempts, error, created_at, settled_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, NULL, NULL, 0, NULL, ?, NULL)"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(message),
                SqlValue::from(subject),
                SqlValue::Integer(i64::from(mention)),
                SqlValue::Integer(i64::from(reply)),
                SqlValue::Integer(now),
                SqlValue::Integer(now),
            ],
        ),
    )?;
    Ok(())
}

// follow_on_mention is a mention's follow of the thread: absent, inserted
// following at `watermark`; not following, following again with its marker
// moved up to `watermark`; following, left.
fn follow_on_mention(cx: Cx<'_, '_>, thread: i64, subject: &str, watermark: i64) -> Result<()> {
    match follow_row(cx, thread, subject)? {
        None => write_follow(cx, false, thread, subject, true, watermark),
        Some(row) if !row.followed => write_follow(
            cx,
            true,
            thread,
            subject,
            true,
            row.read_up_to.max(watermark),
        ),
        Some(_) => Ok(()),
    }
}

// The thread a message is in: its thread, or itself when top-level.
fn thread_of(id: i64, thread: Option<i64>) -> i64 {
    thread.unwrap_or(id)
}

// The thread marker a mention starts at: the message's stand for a
// top-level message, one below it for a reply, so the reply counts.
fn mention_watermark(thread: Option<i64>, stand: i64) -> i64 {
    if thread.is_some() {
        stand - 1
    } else {
        stand
    }
}

/// What a message that has come to stand at `stand` writes, after its own
/// writes: a delivery to each subject it mentions and each follower of its
/// thread but its author, then the author's follow and marker and the
/// mentioned subjects' follows ("Deliveries", "Who follows").
pub(super) fn came_to_stand(
    cx: Cx<'_, '_>,
    id: i64,
    thread: Option<i64>,
    author: &str,
    stand: i64,
    mentions: Option<&Value>,
) -> Result<()> {
    // 1: the thread's followers, read before the writes below.
    let followers: Vec<String> = match thread {
        None => Vec::new(),
        Some(thread) => {
            let table = cx.sql().table(FOLLOWS)?;
            cx.sql()
                .all(
                    format!(
                        "SELECT subject FROM {table}
                         WHERE namespace = ? AND schema = ? AND id = ? AND thread = ? AND followed = 1
                         ORDER BY subject"
                    ),
                    &keyed(cx, [SqlValue::Integer(thread)]),
                )?
                .iter()
                .map(|row| Ok(row.text("subject")?.to_owned()))
                .collect::<Result<_>>()?
        }
    };
    // 2: the mentioned, as written.
    let mentioned = subjects(mentions);
    // 3: one delivery per recipient, with each reason that holds.
    let mut recipients: BTreeMap<&str, (bool, bool)> = BTreeMap::new();
    for subject in &mentioned {
        recipients.entry(subject.as_str()).or_default().0 = true;
    }
    for subject in &followers {
        recipients.entry(subject.as_str()).or_default().1 = true;
    }
    recipients.remove(author);
    for (subject, (mention, reply)) in &recipients {
        insert_delivery(cx, id, subject, *mention, *reply)?;
    }
    // 4: the author follows, and reads up to its own message; the
    // mentioned follow.
    let followed = thread_of(id, thread);
    match follow_row(cx, followed, author)? {
        None => write_follow(cx, false, followed, author, true, stand)?,
        Some(row) => write_follow(cx, true, followed, author, true, row.read_up_to.max(stand))?,
    }
    if thread.is_none() {
        move_conversation_marker(cx, author, stand)?;
    }
    let watermark = mention_watermark(thread, stand);
    for subject in &mentioned {
        if subject != author && !subject.starts_with(ROLE) {
            follow_on_mention(cx, followed, subject, watermark)?;
        }
    }
    Ok(())
}

// A delivery's state and reasons, as an edit reads them.
struct Delivery {
    state: String,
    reply: bool,
}

fn delivery(cx: Cx<'_, '_>, message: i64, subject: &str) -> Result<Option<Delivery>> {
    let table = cx.sql().table(DELIVERIES)?;
    cx.sql()
        .get(
            format!(
                "SELECT state, reply FROM {table}
                 WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND subject = ?"
            ),
            &keyed(cx, [SqlValue::Integer(message), SqlValue::from(subject)]),
        )?
        .map(|row| {
            Ok(Delivery {
                state: row.text("state")?.to_owned(),
                reply: row.i64("reply")? != 0,
            })
        })
        .transpose()
}

/// What an edit that gives `mentions` writes, after its own writes: a
/// delivery and a follow for each subject it adds but the author, and the
/// pending or failed delivery of each it removes loses its mention
/// ("Batch 1 and 2 operations, amended").
pub(super) fn edited_mentions(cx: Cx<'_, '_>, before: &Message, mentions: &Value) -> Result<()> {
    let was = subjects(Some(&before.mentions));
    let now = subjects(Some(mentions));
    let table = cx.sql().table(DELIVERIES)?;
    let key_of = |subject: &str| keyed(cx, [SqlValue::Integer(before.id), SqlValue::from(subject)]);
    for subject in now.iter().filter(|subject| !was.contains(subject)) {
        if *subject == before.author {
            continue;
        }
        match delivery(cx, before.id, subject)? {
            None => insert_delivery(cx, before.id, subject, true, false)?,
            Some(row) if row.state == "sent" || row.state == "dropped" => {
                let mut params = vec![SqlValue::Integer(cx.now()), SqlValue::Integer(cx.now())];
                params.extend(key_of(subject));
                cx.sql().run(
                    format!(
                        "UPDATE {table} SET mention = 1, state = 'pending', due_at = ?, created_at = ?, attempts = 0,
                                token = NULL, claimed_by = NULL, error = NULL, settled_at = NULL
                         WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND subject = ?"
                    ),
                    &params,
                )?;
            }
            Some(_) => {
                cx.sql().run(
                    format!(
                        "UPDATE {table} SET mention = 1
                         WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND subject = ?"
                    ),
                    &key_of(subject),
                )?;
            }
        }
        if let Some(stand) = before.stand {
            if !subject.starts_with(ROLE) {
                follow_on_mention(
                    cx,
                    thread_of(before.id, before.thread),
                    subject,
                    mention_watermark(before.thread, stand),
                )?;
            }
        }
    }
    for subject in was.iter().filter(|subject| !now.contains(subject)) {
        let Some(row) = delivery(cx, before.id, subject)? else {
            continue;
        };
        if row.state != "pending" && row.state != "failed" {
            continue;
        }
        let statement = if row.reply {
            format!(
                "UPDATE {table} SET mention = 0
                 WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND subject = ?"
            )
        } else {
            format!(
                "DELETE FROM {table}
                 WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND subject = ?"
            )
        };
        cx.sql().run(statement, &key_of(subject))?;
    }
    Ok(())
}

/// Deletes the messages' deliveries but the claimed ones, which stay for
/// their claimer's settle; and with `threads`, every follow of those
/// threads (a purge of a top-level message).
pub(super) fn removed(cx: Cx<'_, '_>, ids: &[i64], threads: &[i64]) -> Result<()> {
    if !ids.is_empty() {
        let table = cx.sql().table(DELIVERIES)?;
        cx.sql().run(
            format!(
                "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND message IN ({}) AND state <> 'claimed'",
                placeholders(ids.len())
            ),
            &keyed(cx, ids.iter().map(|&id| SqlValue::Integer(id))),
        )?;
    }
    if !threads.is_empty() {
        let table = cx.sql().table(FOLLOWS)?;
        cx.sql().run(
            format!(
                "DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND thread IN ({})",
                placeholders(threads.len())
            ),
            &keyed(cx, threads.iter().map(|&id| SqlValue::Integer(id))),
        )?;
    }
    Ok(())
}

/// Deletes the instance's markers, follows and deliveries, claimed ones
/// included.
pub(super) fn delete_reads(cx: &InstanceCx<'_, CommentsConfig>) -> Result<()> {
    for table in [READS, FOLLOWS, DELIVERIES] {
        let table = cx.sql().table(table)?;
        cx.sql().run(
            format!("DELETE FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"),
            &key(cx),
        )?;
    }
    Ok(())
}

// thread_param is the top-level message markRead's or follow's `thread`
// names: present, and not a reply.
fn thread_param(cx: Cx<'_, '_>, operation: &str, thread: i64) -> Result<()> {
    let top = present(cx, operation, thread)?;
    if !top.top_level() {
        return Err(not_a_thread(cx, operation, &top));
    }
    Ok(())
}

pub(super) fn mark_read(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "markRead";
    let up_to = integer(params.get("upTo")).unwrap_or_default();
    let thread = integer(params.get("thread"));
    if let Some(thread) = thread {
        thread_param(cx, OP, thread)?;
    }
    let subject = cx.principal().subject.as_str();
    let messages = cx.sql().table(MESSAGES)?;
    let (listed, mut values) = match thread {
        None => ("thread IS NULL", keyed(cx, [])),
        Some(thread) => ("thread = ?", keyed(cx, [SqlValue::Integer(thread)])),
    };
    values.push(SqlValue::Integer(up_to));
    let greatest = cx
        .sql()
        .get(
            format!(
                "SELECT MAX(stand) AS s FROM {messages}
                 WHERE namespace = ? AND schema = ? AND id = ? AND {listed}
                   AND message <= ? AND stand IS NOT NULL"
            ),
            &values,
        )?
        .map(|row| row.opt_i64("s"))
        .transpose()?
        .flatten()
        .unwrap_or(0);
    let (marker, unread) = match thread {
        None => {
            let marker = move_conversation_marker(cx, subject, greatest)?;
            (marker, unread(cx)?)
        }
        Some(thread) => {
            let marker = match follow_row(cx, thread, subject)? {
                None => {
                    write_follow(cx, false, thread, subject, false, greatest)?;
                    greatest
                }
                Some(row) => {
                    let marker = row.read_up_to.max(greatest);
                    write_follow(cx, true, thread, subject, row.followed, marker)?;
                    marker
                }
            };
            (marker, thread_unread(cx, thread, marker, subject)?)
        }
    };
    Ok(object(vec![
        ("readUpTo", Value::from(marker)),
        ("unread", Value::from(unread)),
    ]))
}

pub(super) fn follow(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "follow";
    let thread = integer(params.get("thread")).unwrap_or_default();
    let on = params.get("on").and_then(Value::as_bool).unwrap_or(false);
    thread_param(cx, OP, thread)?;
    let subject = cx.principal().subject.as_str();
    let row = follow_row(cx, thread, subject)?;
    let following = match (on, row) {
        (true, None) => {
            let last = conversation(cx)?.map_or(0, |found| found.last_stand);
            write_follow(cx, false, thread, subject, true, last)?;
            true
        }
        (true, Some(row)) if !row.followed => {
            let last = conversation(cx)?.map_or(0, |found| found.last_stand);
            write_follow(cx, true, thread, subject, true, row.read_up_to.max(last))?;
            true
        }
        (false, Some(row)) if row.followed => {
            write_follow(cx, true, thread, subject, false, row.read_up_to)?;
            false
        }
        (_, _) => on,
    };
    Ok(object(vec![
        ("thread", Value::from(thread)),
        ("following", Value::from(following)),
    ]))
}
