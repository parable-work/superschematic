//! Comments' `moveAnchor` (D77 step 5, `comments-contract.md`, "Batch 5:
//! moveAnchor"): a code block moved to the same code at another revision,
//! by a caller that knows the repository. It is not an edit: it writes the
//! message's `blocks` alone, and the block keeps the anchor it was first
//! given with and counts its moves.

use serde_json::{json, Value};

use superschematic_engine::storage::SqlValue;
use superschematic_engine::Result;

use super::blocks::{kind_of, lines_in_order, moved};
use super::checks::{
    composing, deleted, holds, integer, invalid, moderates, not_author, present, veto,
};
use super::storage::{keyed, MESSAGES};
use super::writes::{after, compact, for_of, Cx};

pub(super) fn move_anchor(cx: Cx<'_, '_>, params: &Value) -> Result<Value> {
    const OP: &str = "moveAnchor";
    let id = integer(params.get("message")).unwrap_or_default();
    let block = integer(params.get("block")).unwrap_or_default();
    let revision = params
        .get("revision")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let lines = numbers(params.get("lines"));
    if !lines_in_order(Some(&lines)) {
        return Err(invalid(
            OP,
            "/lines",
            "must have its first line at most its last",
        ));
    }
    let message = present(cx, OP, id)?;
    if message.deleted() {
        return Err(deleted(cx, OP, id));
    }
    if message.composing() {
        return Err(composing(cx, OP, id));
    }
    let subject = cx.principal().subject.as_str();
    if message.author != subject
        && !holds(cx, cx.config().anchor_permission.as_deref())?
        && !moderates(cx)?
    {
        return Err(not_author(cx, OP, &message));
    }
    let blocks = message.blocks.as_array().cloned().unwrap_or_default();
    let count = blocks.len() as i64;
    let Some(found) = usize::try_from(block)
        .ok()
        .and_then(|index| blocks.get(index))
    else {
        return Err(veto(
            cx,
            OP,
            "no_block",
            format!("message {id} has {count} blocks, so no block {block}"),
            json!({ "message": id, "block": block, "blocks": count }),
        ));
    };
    let kind = kind_of(found);
    if kind != "code" {
        return Err(veto(
            cx,
            OP,
            "not_code",
            format!("block {block} of message {id} is a {kind} block, not a code block"),
            json!({ "message": id, "block": block, "kind": kind }),
        ));
    }
    if found.get("revision").and_then(Value::as_str) == Some(revision)
        && numbers(found.get("lines")) == lines
    {
        return Err(veto(
            cx,
            OP,
            "anchor_unchanged",
            format!(
                "block {block} of message {id} is at revision {revision}, those lines, already"
            ),
            json!({ "message": id, "block": block, "revision": revision, "lines": lines }),
        ));
    }
    let mut after_move = blocks.clone();
    after_move[block as usize] = moved(found, revision, &lines, cx.now(), subject, for_of(cx));
    let table = cx.sql().table(MESSAGES)?;
    cx.sql().run(
        format!(
            "UPDATE {table} SET blocks = ? WHERE namespace = ? AND schema = ? AND id = ? AND message = ?"
        ),
        &[SqlValue::from(compact(Some(&Value::Array(after_move))))]
            .into_iter()
            .chain(keyed(cx, [SqlValue::Integer(id)]))
            .collect::<Vec<_>>(),
    )?;
    after(cx, OP, id)
}

// numbers is a lines array with each line an integer, as stored.
fn numbers(lines: Option<&Value>) -> Value {
    Value::Array(
        lines
            .and_then(Value::as_array)
            .map(|lines| {
                lines
                    .iter()
                    .map(|line| integer(Some(line)).map_or(Value::Null, Value::from))
                    .collect()
            })
            .unwrap_or_default(),
    )
}
