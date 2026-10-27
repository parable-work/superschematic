//! Comments' read-only operations: listMessages, getMessage, listEdits and
//! listReactions (`comments-contract.md`, "Operations"). None writes or
//! appends an event; the access policy is asked for read.

use serde_json::Value;

use superschematic_engine::behaviors::{page, page_request, Page};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{OperationCx, Result};

use super::checks::{integer, invalid, no_message, not_a_thread, object, present};
use super::record::{record_of, records};
use super::storage::{json_array, keyed, Message, EDITS, MESSAGES, MESSAGE_COLUMNS, REACTIONS};
use super::{CommentsConfig, NAME};

type Cx<'a, 'b> = &'a OperationCx<'b, CommentsConfig>;

/// A page's size when `limit` is absent.
const PAGE: i64 = 50;

pub(super) fn list_messages(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "listMessages";
    let anchors: Vec<(&str, i64)> = ["around", "before", "after"]
        .into_iter()
        .filter_map(|name| integer(params.get(name)).map(|value| (name, value)))
        .collect();
    if let [(first, _), (second, _), ..] = anchors.as_slice() {
        return Err(invalid(
            OP,
            format!("/{second}"),
            format!("must not be given with {first}"),
        ));
    }
    let thread = integer(params.get("thread"));
    let root = match thread {
        None => None,
        Some(thread) => {
            let top = present(cx, OP, thread)?;
            if !top.top_level() {
                return Err(not_a_thread(cx, OP, &top));
            }
            Some(top)
        }
    };
    let limit = integer(params.get("limit")).unwrap_or(PAGE);
    let listing = Listing { cx, thread };
    let items = match anchors.first() {
        None => listing.below(None, limit)?,
        Some(&("before", before)) => listing.below(Some(before), limit)?,
        Some(&("after", after)) => listing.above(after, limit)?,
        Some(&(_, around)) => {
            let Some(anchor) = listing.find(around)? else {
                return Err(no_message(cx, OP, around));
            };
            let lower = (limit - 1) / 2;
            let mut items = listing.below(Some(around), lower)?;
            items.push(anchor);
            items.extend(listing.above(around, limit - 1 - lower)?);
            items
        }
    };
    let (before, after) = match (items.first(), items.last()) {
        (Some(first), Some(last)) => (
            listing.has("message < ?", first.id)?.then_some(first.id),
            listing.has("message > ?", last.id)?.then_some(last.id),
        ),
        _ => (None, None),
    };
    let mut members = vec![
        ("items", Value::Array(records(cx, &items)?)),
        ("before", before.map_or(Value::Null, Value::from)),
        ("after", after.map_or(Value::Null, Value::from)),
    ];
    if let Some(marker) = super::follows::read_up_to(cx, thread)? {
        members.push(("readUpTo", Value::from(marker)));
    }
    if let Some(root) = root {
        members.push(("root", record_of(cx, &root)?));
    }
    Ok(object(members))
}

// The messages a listMessages call pages through: the top-level ones, or
// the replies of one thread.
struct Listing<'a, 'b> {
    cx: Cx<'a, 'b>,
    thread: Option<i64>,
}

impl Listing<'_, '_> {
    // select reads the listing's messages that `condition` keeps, in
    // `order`, at most `limit`.
    fn select(
        &self,
        condition: &str,
        bound: Option<i64>,
        order: &str,
        limit: i64,
    ) -> Result<Vec<Message>> {
        if limit <= 0 {
            return Ok(Vec::new());
        }
        let table = self.cx.sql().table(MESSAGES)?;
        let (listed, mut params) = self.filter();
        params.extend(bound.map(SqlValue::Integer));
        params.push(SqlValue::Integer(limit));
        let rows = self.cx.sql().all(
            format!(
                "SELECT {MESSAGE_COLUMNS} FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND {listed}{condition}
                 ORDER BY message {order} LIMIT ?"
            ),
            &params,
        )?;
        rows.iter().map(Message::of).collect()
    }

    // filter is the listing's condition and the instance's key with its
    // parameters.
    fn filter(&self) -> (&'static str, Vec<SqlValue>) {
        match self.thread {
            None => ("thread IS NULL", keyed(self.cx, [])),
            Some(thread) => ("thread = ?", keyed(self.cx, [SqlValue::Integer(thread)])),
        }
    }

    // below is the greatest `limit` ids below `bound` (every id with none),
    // ascending.
    fn below(&self, bound: Option<i64>, limit: i64) -> Result<Vec<Message>> {
        let condition = if bound.is_some() {
            " AND message < ?"
        } else {
            ""
        };
        let mut found = self.select(condition, bound, "DESC", limit)?;
        found.reverse();
        Ok(found)
    }

    // above is the least `limit` ids above `bound`, ascending.
    fn above(&self, bound: i64, limit: i64) -> Result<Vec<Message>> {
        self.select(" AND message > ?", Some(bound), "ASC", limit)
    }

    // find is the listing's message `id`.
    fn find(&self, id: i64) -> Result<Option<Message>> {
        Ok(self.select(" AND message = ?", Some(id), "ASC", 1)?.pop())
    }

    // has is whether the listing has a message `condition` keeps.
    fn has(&self, condition: &str, bound: i64) -> Result<bool> {
        let table = self.cx.sql().table(MESSAGES)?;
        let (listed, mut params) = self.filter();
        params.push(SqlValue::Integer(bound));
        Ok(self
            .cx
            .sql()
            .get(
                format!(
                    "SELECT 1 AS found FROM {table} WHERE namespace = ? AND schema = ? AND id = ? AND {listed} AND {condition} LIMIT 1"
                ),
                &params,
            )?
            .is_some())
    }
}

pub(super) fn get_message(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    let message = present(
        cx,
        "getMessage",
        integer(params.get("message")).unwrap_or_default(),
    )?;
    record_of(cx, &message)
}

pub(super) fn list_edits(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "listEdits";
    let request = page_request(NAME, OP, params)?;
    let id = integer(params.get("message")).unwrap_or_default();
    present(cx, OP, id)?;
    let table = cx.sql().table(EDITS)?;
    let rows = cx.sql().all(
        format!(
            "SELECT edit, body, blocks, mentions, edited_by, edited_at FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND edit > ?
             ORDER BY edit LIMIT ?"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(id),
                SqlValue::Integer(request.after),
                SqlValue::Integer(request.limit + 1),
            ],
        ),
    )?;
    let items = rows
        .iter()
        .map(|row| {
            Ok((
                row.i64("edit")?,
                object(vec![
                    ("edit", Value::from(row.i64("edit")?)),
                    ("body", Value::from(row.text("body")?)),
                    ("blocks", json_array(row.text("blocks")?)),
                    ("mentions", json_array(row.text("mentions")?)),
                    ("by", Value::from(row.text("edited_by")?)),
                    ("at", Value::from(row.i64("edited_at")?)),
                ]),
            ))
        })
        .collect::<Result<Vec<_>>>()?;
    Ok(paged(items, request.limit))
}

pub(super) fn list_reactions(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "listReactions";
    let request = page_request(NAME, OP, params)?;
    let id = integer(params.get("message")).unwrap_or_default();
    let emoji = params
        .get("emoji")
        .and_then(Value::as_str)
        .unwrap_or_default();
    present(cx, OP, id)?;
    let table = cx.sql().table(REACTIONS)?;
    let rows = cx.sql().all(
        format!(
            "SELECT subject, reaction, reacted_at FROM {table}
             WHERE namespace = ? AND schema = ? AND id = ? AND message = ? AND emoji = ? AND reaction > ?
             ORDER BY reaction LIMIT ?"
        ),
        &keyed(
            cx,
            [
                SqlValue::Integer(id),
                SqlValue::from(emoji),
                SqlValue::Integer(request.after),
                SqlValue::Integer(request.limit + 1),
            ],
        ),
    )?;
    let items = rows
        .iter()
        .map(|row| {
            Ok((
                row.i64("reaction")?,
                object(vec![
                    ("subject", Value::from(row.text("subject")?)),
                    ("at", Value::from(row.i64("reacted_at")?)),
                ]),
            ))
        })
        .collect::<Result<Vec<_>>>()?;
    Ok(paged(items, request.limit))
}

// paged is `{ items, next }` of rows read with a limit of `limit + 1`,
// each with its paging number.
fn paged(items: Vec<(i64, Value)>, limit: i64) -> Value {
    let found = page(items, limit, |(number, _)| *number);
    Page {
        items: found.items.into_iter().map(|(_, item)| item).collect(),
        next: found.next,
    }
    .to_json()
}
