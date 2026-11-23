//! Comments' read state and follows (D77's step 3, `comments-contract.md`,
//! "Batch 3"), each a sub-table of the row: each subject's conversation
//! marker (`reads`), its state on each thread, whether it follows it and its
//! marker on its replies (`follows`), and one delivery per message and
//! subject that should hear of it (`deliveries`). A message that comes to
//! stand makes its deliveries and its follows; `markRead` and `follow`
//! write the caller's own rows alone, beside the row; the `unread` field
//! and `listMessages`' `readUpTo` read them; and an edit, a delete and a
//! purge move them as the contract says.

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{Cx, Hook, SubRow, View};

use super::checks::{not_a_thread, object, present, whole};
use super::messages::{messages_of, subject_key, Conversation, Message};
use super::{CommentsConfig, DELIVERIES, FOLLOWS, READS};

/// A subject that names a group: it gets its deliveries as given, and
/// follows nothing.
const ROLE: &str = "role:";

pub(super) const MARK_READ: &str = "markRead";
pub(super) const FOLLOW: &str = "follow";

type C<'a, 'r> = &'a Cx<'r, CommentsConfig>;

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

/// A subject's row on a thread.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct Follow {
    pub thread: i64,
    pub subject: String,
    pub followed: bool,
    pub read_up_to: i64,
}

impl Follow {
    fn of(row: &SubRow) -> Follow {
        Follow {
            thread: row.value("thread").and_then(whole).unwrap_or_default(),
            subject: text(row, "subject"),
            followed: row.value("followed").and_then(Value::as_bool) == Some(true),
            read_up_to: row.value("readUpTo").and_then(whole).unwrap_or_default(),
        }
    }
}

fn text(row: &SubRow, name: &str) -> String {
    row.value(name)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

/// The row's follows, as the run has them.
pub(super) fn follows_of(view: &View<'_, CommentsConfig>) -> Hook<Vec<Follow>> {
    Ok(view.sub_rows(FOLLOWS)?.iter().map(Follow::of).collect())
}

/// A subject's conversation marker, by subject, of the row's `reads`.
pub(super) fn markers(rows: &[SubRow]) -> BTreeMap<String, i64> {
    rows.iter()
        .map(|row| {
            (
                row.key.clone(),
                row.value("upTo").and_then(whole).unwrap_or_default(),
            )
        })
        .collect()
}

/// The subject's conversation marker on the row, `None` when it has none.
pub(super) fn conversation_marker(
    view: &View<'_, CommentsConfig>,
    subject: &str,
) -> Hook<Option<i64>> {
    Ok(markers(&view.sub_rows(READS)?).get(subject).copied())
}

fn follow_row(view: &View<'_, CommentsConfig>, thread: i64, subject: &str) -> Hook<Option<Follow>> {
    Ok(follows_of(view)?
        .into_iter()
        .find(|row| row.thread == thread && row.subject == subject))
}

// write_follow inserts the subject's row on the thread, or updates the one
// it has (`existed`), with `updatedAt = now`.
fn write_follow(
    cx: C<'_, '_>,
    existed: bool,
    thread: i64,
    subject: &str,
    followed: bool,
    read_up_to: i64,
) -> Hook<()> {
    let mut values = Map::new();
    if !existed {
        values.insert("thread".into(), Value::from(thread));
        values.insert("subject".into(), Value::from(subject));
    }
    values.insert("followed".into(), Value::from(followed));
    values.insert("readUpTo".into(), Value::from(read_up_to));
    values.insert("updatedAt".into(), Value::from(cx.now()));
    let key = subject_key(thread, subject);
    if existed {
        cx.update_row(FOLLOWS, &key, values)
    } else {
        cx.insert_row(FOLLOWS, &key, values)
    }
}

// move_conversation_marker moves the subject's conversation marker up to
// `to`, never back, the row inserted at `to` when absent; the marker after.
fn move_conversation_marker(cx: C<'_, '_>, subject: &str, to: i64) -> Hook<i64> {
    let mut values = Map::new();
    values.insert("readAt".into(), Value::from(cx.now()));
    match conversation_marker(cx, subject)? {
        Some(was) => {
            let up_to = was.max(to);
            values.insert("upTo".into(), Value::from(up_to));
            cx.update_row(READS, subject, values)?;
            Ok(up_to)
        }
        None => {
            values.insert("upTo".into(), Value::from(to));
            cx.insert_row(READS, subject, values)?;
            Ok(to)
        }
    }
}

/// The `unread` field: the reader's count of others' standing top-level
/// messages whose stand is above its conversation marker; 0 with none.
pub(super) fn unread_of(messages: &[Message], marker: Option<i64>, subject: &str) -> i64 {
    let Some(marker) = marker else {
        return 0;
    };
    messages
        .iter()
        .filter(|message| {
            message.top_level()
                && message.stands()
                && message.author != subject
                && message.stand.is_some_and(|stand| stand > marker)
        })
        .count() as i64
}

// thread_unread is how many of others' standing replies in the thread
// stand above `marker`.
fn thread_unread(messages: &[Message], thread: i64, marker: i64, subject: &str) -> i64 {
    messages
        .iter()
        .filter(|message| {
            message.thread == Some(thread)
                && message.stands()
                && message.author != subject
                && message.stand.is_some_and(|stand| stand > marker)
        })
        .count() as i64
}

/// `listMessages`' `readUpTo`: the caller's conversation marker, or with
/// `thread` its thread marker there; `None` when it has none.
pub(super) fn read_up_to(
    view: &View<'_, CommentsConfig>,
    thread: Option<i64>,
) -> Hook<Option<i64>> {
    let subject = view.caller().subject.as_str();
    match thread {
        None => conversation_marker(view, subject),
        Some(thread) => Ok(follow_row(view, thread, subject)?.map(|row| row.read_up_to)),
    }
}

/// A delivery as the sub-table `deliveries` holds it.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct Delivery {
    pub key: String,
    pub message: i64,
    pub subject: String,
    pub mention: bool,
    pub reply: bool,
    pub state: String,
    pub due_at: Option<i64>,
    pub token: Option<String>,
    pub attempts: i64,
    pub created_at: i64,
}

impl Delivery {
    pub fn of(row: &SubRow) -> Delivery {
        let int = |name: &str| row.value(name).and_then(whole);
        Delivery {
            key: row.key.clone(),
            message: int("message").unwrap_or_default(),
            subject: text(row, "subject"),
            mention: row.value("mention").and_then(Value::as_bool) == Some(true),
            reply: row.value("reply").and_then(Value::as_bool) == Some(true),
            state: text(row, "state"),
            due_at: int("dueAt"),
            token: row
                .value("token")
                .and_then(Value::as_str)
                .map(str::to_owned),
            attempts: int("attempts").unwrap_or_default(),
            created_at: int("createdAt").unwrap_or_default(),
        }
    }
}

fn deliveries_of(view: &View<'_, CommentsConfig>) -> Hook<Vec<Delivery>> {
    Ok(view
        .sub_rows(DELIVERIES)?
        .iter()
        .map(Delivery::of)
        .collect())
}

// insert_delivery inserts a pending delivery of the message to the
// subject, due now.
fn insert_delivery(
    cx: C<'_, '_>,
    message: i64,
    subject: &str,
    mention: bool,
    reply: bool,
) -> Hook<()> {
    let now = cx.now();
    let mut values = Map::new();
    values.insert("message".into(), Value::from(message));
    values.insert("subject".into(), Value::from(subject));
    values.insert("mention".into(), Value::from(mention));
    values.insert("reply".into(), Value::from(reply));
    values.insert("state".into(), Value::from("pending"));
    values.insert("dueAt".into(), Value::from(now));
    values.insert("attempts".into(), Value::from(0));
    values.insert("createdAt".into(), Value::from(now));
    cx.insert_row(DELIVERIES, &subject_key(message, subject), values)
}

// follow_on_mention is a mention's follow of the thread: absent, inserted
// following at `watermark`; not following, following again with its marker
// moved up to `watermark`; following, left.
fn follow_on_mention(cx: C<'_, '_>, thread: i64, subject: &str, watermark: i64) -> Hook<()> {
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
/// thread but its author and the subject it was written for, then the
/// author's follow and marker, the follow of the subject it was written
/// for, and the mentioned subjects' follows ("Deliveries", "Who follows",
/// batch 4).
pub(super) fn came_to_stand(cx: C<'_, '_>, message: &Message, stand: i64) -> Hook<()> {
    let (id, thread, author) = (message.id, message.thread, message.author.as_str());
    let for_subject = message.for_subject.as_deref();
    // 1: the thread's followers, read before the writes below.
    let followers: Vec<String> = match thread {
        None => Vec::new(),
        Some(thread) => {
            let mut found: Vec<String> = follows_of(cx)?
                .into_iter()
                .filter(|row| row.thread == thread && row.followed)
                .map(|row| row.subject)
                .collect();
            found.sort();
            found
        }
    };
    // 2: the mentioned, as written.
    let mentioned = subjects(Some(&message.mentions));
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
    // The subject it was written for follows as a mentioned one does, its
    // markers left: it did not write the message, so it has not read it.
    if let Some(subject) = for_subject {
        if subject != author && !subject.starts_with(ROLE) {
            follow_on_mention(cx, followed, subject, watermark)?;
        }
    }
    for subject in &mentioned {
        if subject != author && Some(subject.as_str()) != for_subject && !subject.starts_with(ROLE)
        {
            follow_on_mention(cx, followed, subject, watermark)?;
        }
    }
    Ok(())
}

/// What an edit that gives `mentions` writes, after its own writes: a
/// delivery and a follow for each subject it adds but the author, and the
/// pending or failed delivery of each it removes loses its mention
/// ("Batch 1 and 2 operations, amended").
pub(super) fn edited_mentions(cx: C<'_, '_>, before: &Message, mentions: &Value) -> Hook<()> {
    let was = subjects(Some(&before.mentions));
    let now = subjects(Some(mentions));
    let held = deliveries_of(cx)?;
    let find = |subject: &str| {
        held.iter()
            .find(|row| row.message == before.id && row.subject == subject)
    };
    for subject in now.iter().filter(|subject| !was.contains(subject)) {
        if *subject == before.author || before.for_subject.as_deref() == Some(subject.as_str()) {
            continue;
        }
        let key = subject_key(before.id, subject);
        match find(subject) {
            None => insert_delivery(cx, before.id, subject, true, false)?,
            Some(row) if row.state == "sent" || row.state == "dropped" => {
                let mut values = Map::new();
                values.insert("mention".into(), Value::from(true));
                values.insert("state".into(), Value::from("pending"));
                values.insert("dueAt".into(), Value::from(cx.now()));
                values.insert("createdAt".into(), Value::from(cx.now()));
                values.insert("attempts".into(), Value::from(0));
                for cleared in ["token", "claimedBy", "error", "settledAt"] {
                    values.insert(cleared.into(), Value::Null);
                }
                cx.update_row(DELIVERIES, &key, values)?;
            }
            Some(_) => {
                let mut values = Map::new();
                values.insert("mention".into(), Value::from(true));
                cx.update_row(DELIVERIES, &key, values)?;
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
        let Some(row) = find(subject) else {
            continue;
        };
        if row.state != "pending" && row.state != "failed" {
            continue;
        }
        if row.reply {
            let mut values = Map::new();
            values.insert("mention".into(), Value::from(false));
            cx.update_row(DELIVERIES, &row.key, values)?;
        } else {
            cx.delete_row(DELIVERIES, &row.key)?;
        }
    }
    Ok(())
}

/// Deletes the messages' deliveries but the claimed ones, which stay for
/// their claimer's settle; and with `threads`, every follow of those
/// threads (a purge of a top-level message).
pub(super) fn removed(cx: C<'_, '_>, ids: &[i64], threads: &[i64]) -> Hook<()> {
    for row in deliveries_of(cx)? {
        if ids.contains(&row.message) && row.state != "claimed" {
            cx.delete_row(DELIVERIES, &row.key)?;
        }
    }
    for row in cx.sub_rows(FOLLOWS)? {
        let thread = row.value("thread").and_then(whole).unwrap_or_default();
        if threads.contains(&thread) {
            cx.delete_row(FOLLOWS, &row.key)?;
        }
    }
    Ok(())
}

// thread_param is the top-level message markRead's or follow's `thread`
// names: present, and not a reply.
fn thread_param(cx: C<'_, '_>, messages: &[Message], thread: i64) -> Hook<()> {
    let top = present(cx, messages, thread)?;
    if !top.top_level() {
        return Err(not_a_thread(cx, &top));
    }
    Ok(())
}

pub(super) fn mark_read(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let up_to = params.get("upTo").and_then(whole).unwrap_or_default();
    let thread = params.get("thread").and_then(whole);
    let messages = messages_of(cx)?;
    if let Some(thread) = thread {
        thread_param(cx, &messages, thread)?;
    }
    let subject = cx.caller().subject.clone();
    // The greatest stand of the listing's messages whose id is at most
    // upTo; 0 when none stands.
    let greatest = messages
        .iter()
        .filter(|message| message.thread == thread && message.id <= up_to)
        .filter_map(|message| message.stand)
        .max()
        .unwrap_or(0);
    let (marker, unread) = match thread {
        None => {
            let marker = move_conversation_marker(cx, &subject, greatest)?;
            (marker, unread_of(&messages, Some(marker), &subject))
        }
        Some(thread) => {
            let marker = match follow_row(cx, thread, &subject)? {
                None => {
                    write_follow(cx, false, thread, &subject, false, greatest)?;
                    greatest
                }
                Some(row) => {
                    let marker = row.read_up_to.max(greatest);
                    write_follow(cx, true, thread, &subject, row.followed, marker)?;
                    marker
                }
            };
            (marker, thread_unread(&messages, thread, marker, &subject))
        }
    };
    Ok(object(vec![
        ("readUpTo", Value::from(marker)),
        ("unread", Value::from(unread)),
    ]))
}

pub(super) fn follow(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let thread = params.get("thread").and_then(whole).unwrap_or_default();
    let on = params.get("on").and_then(Value::as_bool).unwrap_or(false);
    let messages = messages_of(cx)?;
    thread_param(cx, &messages, thread)?;
    let subject = cx.caller().subject.clone();
    let row = follow_row(cx, thread, &subject)?;
    let following = match (on, row) {
        (true, None) => {
            let last = Conversation::of(cx)?.last_stand;
            write_follow(cx, false, thread, &subject, true, last)?;
            true
        }
        (true, Some(row)) if !row.followed => {
            let last = Conversation::of(cx)?.last_stand;
            write_follow(cx, true, thread, &subject, true, row.read_up_to.max(last))?;
            true
        }
        (false, Some(row)) if row.followed => {
            write_follow(cx, true, thread, &subject, false, row.read_up_to)?;
            false
        }
        (_, _) => on,
    };
    Ok(object(vec![
        ("thread", Value::from(thread)),
        ("following", Value::from(following)),
    ]))
}
