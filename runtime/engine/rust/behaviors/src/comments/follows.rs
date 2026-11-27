//! Comments' read state and follows (D77 step 3, `comments-contract.md`,
//! "Batch 3"): each message's stand, which it takes when it comes to stand;
//! the deliveries a standing message makes for its mentions and its
//! thread's followers; who follows without asking (the author, the
//! mentioned); `markRead` and `follow`, which write only the caller's own
//! rows, append no event and ask read; the `unread` field and
//! `listMessages`' `readUpTo`; and what `edit`, `deleteMessage`, `purge`
//! and the instance's delete do to the deliveries and follows.

use std::collections::{BTreeMap, HashMap};

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

// How many rows one multi-row INSERT writes, and how many subjects one
// `IN` list names: far below SQLite's (32766) and Postgres' (65535) limits
// on a statement's parameters.
const BATCH: usize = 500;

// insert_deliveries inserts a pending delivery of the message to each
// subject with its reasons (mention, reply), due now, in the order given:
// one multi-row INSERT per BATCH of them.
fn insert_deliveries(cx: Cx<'_, '_>, message: i64, rows: &[(&str, bool, bool)]) -> Result<()> {
    let table = cx.sql().table(DELIVERIES)?;
    let now = cx.now();
    for chunk in rows.chunks(BATCH) {
        let tuples =
            vec!["(?, ?, ?, ?, ?, ?, ?, 'pending', ?, NULL, NULL, 0, NULL, ?, NULL)"; chunk.len()]
                .join(", ");
        let mut params = Vec::with_capacity(chunk.len() * 9);
        for &(subject, mention, reply) in chunk {
            params.extend(keyed(
                cx,
                [
                    SqlValue::Integer(message),
                    SqlValue::from(subject),
                    SqlValue::Integer(i64::from(mention)),
                    SqlValue::Integer(i64::from(reply)),
                    SqlValue::Integer(now),
                    SqlValue::Integer(now),
                ],
            ));
        }
        cx.sql().run(
            format!(
                "INSERT INTO {table} (namespace, schema, id, message, subject, mention, reply, state, due_at, token, claimed_by, attempts, error, created_at, settled_at)
                 VALUES {tuples}"
            ),
            &params,
        )?;
    }
    Ok(())
}

// follow_on_mentions is each mentioned subject's follow of the thread, one
// upsert per BATCH of them: absent, inserted following at `watermark`; not
// following, following again with its marker moved up to `watermark`;
// following, left (the upsert's WHERE). The subjects are distinct, as
// Postgres' ON CONFLICT DO UPDATE asks of one statement's rows.
fn follow_on_mentions(
    cx: Cx<'_, '_>,
    thread: i64,
    subjects: &[&str],
    watermark: i64,
) -> Result<()> {
    let table = cx.sql().table(FOLLOWS)?;
    let now = cx.now();
    for chunk in subjects.chunks(BATCH) {
        let tuples = vec!["(?, ?, ?, ?, ?, 1, ?, ?)"; chunk.len()].join(", ");
        let mut params = Vec::with_capacity(chunk.len() * 7);
        for &subject in chunk {
            params.extend(keyed(
                cx,
                [
                    SqlValue::Integer(thread),
                    SqlValue::from(subject),
                    SqlValue::Integer(watermark),
                    SqlValue::Integer(now),
                ],
            ));
        }
        cx.sql().run(
            format!(
                "INSERT INTO {table} (namespace, schema, id, thread, subject, followed, read_up_to, updated_at)
                 VALUES {tuples}
                 ON CONFLICT (namespace, schema, id, thread, subject) DO UPDATE SET followed = 1,
                   read_up_to = CASE WHEN {table}.read_up_to > excluded.read_up_to
                                THEN {table}.read_up_to ELSE excluded.read_up_to END,
                   updated_at = excluded.updated_at
                 WHERE {table}.followed = 0"
            ),
            &params,
        )?;
    }
    Ok(())
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
/// thread but its author and the subject it was written for, then the
/// author's follow and marker, the follow of the subject it was written
/// for, and the mentioned subjects' follows ("Deliveries", "Who follows",
/// batch 4).
pub(super) fn came_to_stand(
    cx: Cx<'_, '_>,
    id: i64,
    thread: Option<i64>,
    author: &str,
    for_subject: Option<&str>,
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
    if let Some(subject) = for_subject {
        recipients.remove(subject);
    }
    let rows: Vec<(&str, bool, bool)> = recipients
        .iter()
        .map(|(subject, (mention, reply))| (*subject, *mention, *reply))
        .collect();
    insert_deliveries(cx, id, &rows)?;
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
    // The subject it was written for follows as a mentioned one does, its
    // markers left: it did not write the message, so it has not read it.
    let mut following: Vec<&str> = Vec::new();
    if let Some(subject) = for_subject {
        if subject != author && !subject.starts_with(ROLE) {
            following.push(subject);
        }
    }
    following.extend(mentioned.iter().map(String::as_str).filter(|&subject| {
        subject != author && Some(subject) != for_subject && !subject.starts_with(ROLE)
    }));
    follow_on_mentions(cx, followed, &following, mention_watermark(thread, stand))
}

// A delivery's state and reasons, as an edit reads them.
struct Delivery {
    state: String,
    reply: bool,
}

// deliveries_of reads the message's deliveries to the subjects, by subject,
// one `IN` list per BATCH of them.
fn deliveries_of(
    cx: Cx<'_, '_>,
    message: i64,
    subjects: &[&str],
) -> Result<HashMap<String, Delivery>> {
    let table = cx.sql().table(DELIVERIES)?;
    let mut out = HashMap::new();
    for chunk in subjects.chunks(BATCH) {
        let rows = cx.sql().all(
            format!(
                "SELECT subject, state, reply FROM {table}
                 WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND subject IN ({})",
                placeholders(chunk.len())
            ),
            &keyed(
                cx,
                std::iter::once(SqlValue::Integer(message))
                    .chain(chunk.iter().map(|&subject| SqlValue::from(subject))),
            ),
        )?;
        for row in rows {
            out.insert(
                row.text("subject")?.to_owned(),
                Delivery {
                    state: row.text("state")?.to_owned(),
                    reply: row.i64("reply")? != 0,
                },
            );
        }
    }
    Ok(out)
}

// on_deliveries runs `statement` (an UPDATE ... SET or a DELETE FROM the
// deliveries, its own parameters `set`) on the message's deliveries to the
// subjects, one `IN` list per BATCH of them.
fn on_deliveries(
    cx: Cx<'_, '_>,
    statement: &str,
    set: &[SqlValue],
    message: i64,
    subjects: &[&str],
) -> Result<()> {
    for chunk in subjects.chunks(BATCH) {
        let mut params = set.to_vec();
        params.extend(keyed(
            cx,
            std::iter::once(SqlValue::Integer(message))
                .chain(chunk.iter().map(|&subject| SqlValue::from(subject))),
        ));
        cx.sql().run(
            format!(
                "{statement} WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND subject IN ({})",
                placeholders(chunk.len())
            ),
            &params,
        )?;
    }
    Ok(())
}

/// What an edit that gives `mentions` writes, after its own writes: a
/// delivery and a follow for each subject it adds but the author, and the
/// pending or failed delivery of each it removes loses its mention
/// ("Batch 1 and 2 operations, amended"). The deliveries of both are read
/// in one statement and each kind of write is one statement over its
/// subjects; each subject is a row of its own, so the order of the writes
/// does not matter.
pub(super) fn edited_mentions(cx: Cx<'_, '_>, before: &Message, mentions: &Value) -> Result<()> {
    let was = subjects(Some(&before.mentions));
    let now = subjects(Some(mentions));
    let table = cx.sql().table(DELIVERIES)?;
    let added: Vec<&str> = now
        .iter()
        .map(String::as_str)
        .filter(|&subject| !was.iter().any(|seen| seen == subject))
        .filter(|&subject| {
            subject != before.author && before.for_subject.as_deref() != Some(subject)
        })
        .collect();
    let dropped: Vec<&str> = was
        .iter()
        .map(String::as_str)
        .filter(|&subject| !now.iter().any(|kept| kept == subject))
        .collect();
    let rows = deliveries_of(cx, before.id, &[added.as_slice(), &dropped].concat())?;

    // An added subject: no delivery, one inserted; a sent or dropped one,
    // pending again; any other, a mention too.
    let mut fresh: Vec<(&str, bool, bool)> = Vec::new();
    let mut renewed: Vec<&str> = Vec::new();
    let mut marked: Vec<&str> = Vec::new();
    for &subject in &added {
        match rows.get(subject) {
            None => fresh.push((subject, true, false)),
            Some(row) if row.state == "sent" || row.state == "dropped" => renewed.push(subject),
            Some(_) => marked.push(subject),
        }
    }
    insert_deliveries(cx, before.id, &fresh)?;
    on_deliveries(
        cx,
        &format!(
            "UPDATE {table} SET mention = 1, state = 'pending', due_at = ?, created_at = ?, attempts = 0,
                    token = NULL, claimed_by = NULL, error = NULL, settled_at = NULL"
        ),
        &[SqlValue::Integer(cx.now()), SqlValue::Integer(cx.now())],
        before.id,
        &renewed,
    )?;
    on_deliveries(
        cx,
        &format!("UPDATE {table} SET mention = 1"),
        &[],
        before.id,
        &marked,
    )?;
    if let Some(stand) = before.stand {
        let following: Vec<&str> = added
            .iter()
            .copied()
            .filter(|subject| !subject.starts_with(ROLE))
            .collect();
        follow_on_mentions(
            cx,
            thread_of(before.id, before.thread),
            &following,
            mention_watermark(before.thread, stand),
        )?;
    }

    // A removed subject's pending or failed delivery: kept without its
    // mention when it is a reply too, removed when not.
    let mut unmentioned: Vec<&str> = Vec::new();
    let mut removed: Vec<&str> = Vec::new();
    for &subject in &dropped {
        match rows.get(subject) {
            Some(row) if row.state == "pending" || row.state == "failed" => {
                if row.reply {
                    unmentioned.push(subject);
                } else {
                    removed.push(subject);
                }
            }
            _ => {}
        }
    }
    on_deliveries(
        cx,
        &format!("UPDATE {table} SET mention = 0"),
        &[],
        before.id,
        &unmentioned,
    )?;
    on_deliveries(
        cx,
        &format!("DELETE FROM {table}"),
        &[],
        before.id,
        &removed,
    )
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
