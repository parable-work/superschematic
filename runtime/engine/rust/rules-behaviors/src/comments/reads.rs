//! Comments' read-only operations: listMessages, getMessage, listEdits,
//! listReactions and listPinned (`comments-contract.md`, "Operations",
//! "Batch 5: listPinned"). None writes.

use serde_json::Value;
use superschematic_rules::{Cx, Hook, Refusal};

use super::checks::{invalid, no_message, not_a_thread, object, present, whole};
use super::follows;
use super::messages::{edits_of, messages_of, reactions_of, Message, Talk};
use super::{CommentsConfig, NAME};
use crate::paging::{page, page_request, Page};

pub(super) const LIST_MESSAGES: &str = "listMessages";
pub(super) const GET_MESSAGE: &str = "getMessage";
pub(super) const LIST_EDITS: &str = "listEdits";
pub(super) const LIST_REACTIONS: &str = "listReactions";
pub(super) const LIST_PINNED: &str = "listPinned";

/// A page's size when `limit` is absent.
const PAGE: i64 = 50;

type C<'a, 'r> = &'a Cx<'r, CommentsConfig>;

pub(super) fn list_messages(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let anchors: Vec<(&str, i64)> = ["around", "before", "after"]
        .into_iter()
        .filter_map(|name| params.get(name).and_then(whole).map(|value| (name, value)))
        .collect();
    if let [(first, _), (second, _), ..] = anchors.as_slice() {
        return Err(invalid(
            cx,
            LIST_MESSAGES,
            format!("/{second}"),
            format!("must not be given with {first}"),
        ));
    }
    let thread = params.get("thread").and_then(whole);
    let talk = Talk::of(cx)?;
    let messages = &talk.messages;
    let root = match thread {
        None => None,
        Some(thread) => {
            let top = present(cx, messages, thread)?;
            if !top.top_level() {
                return Err(not_a_thread(cx, &top));
            }
            Some(top)
        }
    };
    let limit = params.get("limit").and_then(whole).unwrap_or(PAGE);
    // The listing: the top-level messages, or the replies of one thread,
    // in id order.
    let listing: Vec<&Message> = messages
        .iter()
        .filter(|message| message.thread == thread)
        .collect();
    let below = |bound: Option<i64>, count: i64| -> Vec<&Message> {
        let found: Vec<&Message> = listing
            .iter()
            .copied()
            .filter(|message| bound.is_none_or(|bound| message.id < bound))
            .collect();
        let count = usize::try_from(count.max(0)).unwrap_or(0);
        found[found.len().saturating_sub(count)..].to_vec()
    };
    let above = |bound: i64, count: i64| -> Vec<&Message> {
        listing
            .iter()
            .copied()
            .filter(|message| message.id > bound)
            .take(usize::try_from(count.max(0)).unwrap_or(0))
            .collect()
    };
    let items: Vec<&Message> = match anchors.first() {
        None => below(None, limit),
        Some(&("before", before)) => below(Some(before), limit),
        Some(&("after", after)) => above(after, limit),
        Some(&(_, around)) => {
            let Some(anchor) = listing.iter().copied().find(|message| message.id == around) else {
                return Err(no_message(cx, around));
            };
            let lower = (limit - 1) / 2;
            let mut items = below(Some(around), lower);
            items.push(anchor);
            items.extend(above(around, limit - 1 - lower));
            items
        }
    };
    let (before, after) = match (items.first(), items.last()) {
        (Some(first), Some(last)) => (
            listing
                .iter()
                .any(|message| message.id < first.id)
                .then_some(first.id),
            listing
                .iter()
                .any(|message| message.id > last.id)
                .then_some(last.id),
        ),
        _ => (None, None),
    };
    let subject = &cx.caller().subject;
    let mut members = vec![
        (
            "items",
            Value::Array(
                items
                    .iter()
                    .map(|message| talk.record(message, subject))
                    .collect(),
            ),
        ),
        ("before", before.map_or(Value::Null, Value::from)),
        ("after", after.map_or(Value::Null, Value::from)),
    ];
    // The caller's marker on the listing: the conversation's, or the
    // thread's with a thread; absent when it has none.
    if let Some(marker) = follows::read_up_to(cx, thread)? {
        members.push(("readUpTo", Value::from(marker)));
    }
    if let Some(root) = root {
        members.push(("root", talk.record(&root, subject)));
    }
    Ok(object(members))
}

pub(super) fn get_message(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let talk = Talk::of(cx)?;
    let message = present(
        cx,
        &talk.messages,
        params.get("message").and_then(whole).unwrap_or_default(),
    )?;
    Ok(talk.record(&message, &cx.caller().subject))
}

pub(super) fn list_edits(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let request = page_request(params)
        .map_err(|issue| Refusal::operation_params(NAME, cx.use_id(), LIST_EDITS, vec![issue]))?;
    let id = params.get("message").and_then(whole).unwrap_or_default();
    present(cx, &messages_of(cx)?, id)?;
    let items: Vec<(i64, Value)> = edits_of(cx)?
        .iter()
        .filter(|edit| edit.value("message").and_then(whole) == Some(id))
        .filter_map(|edit| {
            let number = edit.value("number").and_then(whole)?;
            (number > request.after).then(|| {
                let text = |name: &str| edit.value(name).cloned().unwrap_or(Value::Null);
                let mut members = vec![
                    ("edit", Value::from(number)),
                    ("body", text("body")),
                    ("blocks", text("blocks")),
                    ("mentions", text("mentions")),
                    ("by", text("by")),
                ];
                if let Some(acted_for) = edit.value("forSubject") {
                    members.push(("for", acted_for.clone()));
                }
                members.push(("at", text("at")));
                (number, object(members))
            })
        })
        .collect();
    Ok(paged(items, request.limit))
}

pub(super) fn list_reactions(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let request = page_request(params).map_err(|issue| {
        Refusal::operation_params(NAME, cx.use_id(), LIST_REACTIONS, vec![issue])
    })?;
    let id = params.get("message").and_then(whole).unwrap_or_default();
    let emoji = params
        .get("emoji")
        .and_then(Value::as_str)
        .unwrap_or_default();
    present(cx, &messages_of(cx)?, id)?;
    let items: Vec<(i64, Value)> = reactions_of(cx)?
        .into_iter()
        .filter(|reaction| {
            reaction.message == id && reaction.emoji == emoji && reaction.number > request.after
        })
        .map(|reaction| {
            let mut members = vec![("subject", Value::from(reaction.subject))];
            if let Some(acted_for) = reaction.for_subject {
                members.push(("for", Value::from(acted_for)));
            }
            members.push(("at", Value::from(reaction.at)));
            (reaction.number, object(members))
        })
        .collect();
    Ok(paged(items, request.limit))
}

/// The conversation's pinned messages, top-level and replies, by id, paged
/// over the id; each item the record.
pub(super) fn list_pinned(cx: C<'_, '_>, params: &Value) -> Hook<Value> {
    let request = page_request(params)
        .map_err(|issue| Refusal::operation_params(NAME, cx.use_id(), LIST_PINNED, vec![issue]))?;
    let talk = Talk::of(cx)?;
    let pinned: Vec<&Message> = talk
        .messages
        .iter()
        .filter(|message| message.pinned && message.id > request.after)
        .take(usize::try_from(request.limit + 1).unwrap_or(usize::MAX))
        .collect();
    let found = page(pinned, request.limit, |message| message.id);
    let subject = &cx.caller().subject;
    Ok(Page {
        items: found
            .items
            .iter()
            .map(|message| talk.record(message, subject))
            .collect(),
        next: found.next,
    }
    .into_json())
}

// paged is `{ items, next }` of items each with its paging number, the
// first `limit` of them and the cursor after the last when more follow.
fn paged(items: Vec<(i64, Value)>, limit: i64) -> Value {
    let taken: Vec<(i64, Value)> = items
        .into_iter()
        .take(usize::try_from(limit + 1).unwrap_or(usize::MAX))
        .collect();
    let found = page(taken, limit, |(number, _)| *number);
    Page {
        items: found.items.into_iter().map(|(_, item)| item).collect(),
        next: found.next,
    }
    .into_json()
}
