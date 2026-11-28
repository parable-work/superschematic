//! What Comments' operations refuse (`comments-contract.md`, "Refusals"),
//! in the engine's words: `invalid_argument` at a JSON pointer, `forbidden`
//! for a permission the config names, and vetoes with the declaration's
//! codes and the contract's details; and the checks the operations share,
//! in the order their tables list them.

use serde_json::{json, Map, Value};
use superschematic_rules::{Hook, Refusal, SchemaIssue, Stop, Veto, View};

use super::messages::Message;
use super::{CommentsConfig, NAME};

/// The largest `data` an action block holds, in bytes of compact JSON.
pub(super) const MAX_ACTION_DATA: usize = 8192;
/// The most edits a message has.
pub(super) const MAX_EDITS: i64 = 500;
/// The most distinct emoji a message holds reactions with.
pub(super) const MAX_EMOJI: usize = 20;
/// The longest text an appendDraft adds, in bytes written as a JSON string,
/// its quotes included, so its draft signal fits a signal's 4096 bytes.
pub(super) const MAX_APPEND_BYTES: usize = 4000;

/// A veto of one of Comments' operations, with its code and details.
pub(super) fn veto(
    view: &View<'_, CommentsConfig>,
    code: &str,
    reason: String,
    details: Value,
) -> Stop {
    let mut veto = Veto::new(reason).with_code(code);
    if let Value::Object(details) = details {
        veto = veto.with_details(details);
    }
    view.veto(veto)
}

/// A refusal of an operation's parameters: one issue at `path`.
pub(super) fn invalid(
    view: &View<'_, CommentsConfig>,
    operation: &str,
    path: impl Into<String>,
    message: impl Into<String>,
) -> Stop {
    Refusal::operation_params(
        NAME,
        view.use_id(),
        operation,
        vec![SchemaIssue::new(path, message)],
    )
    .into()
}

/// The caller lacks a permission the config names.
pub(super) fn forbidden(
    view: &View<'_, CommentsConfig>,
    operation: &str,
    permission: &str,
) -> Stop {
    view.forbidden(format!(
        "{} may not {operation} on {}: it needs permission {permission}",
        view.caller().subject,
        row(view)
    ))
}

/// Whether the caller holds `permission`, when the config names one.
pub(super) fn holds(view: &View<'_, CommentsConfig>, permission: Option<&str>) -> Hook<bool> {
    match permission {
        Some(permission) => view.can(permission),
        None => Ok(false),
    }
}

/// Whether the caller moderates: the config names `moderatePermission` and
/// the caller holds it.
pub(super) fn moderates(view: &View<'_, CommentsConfig>) -> Hook<bool> {
    holds(view, view.config().moderate_permission.as_deref())
}

/// The row a message is about, as the engine named its instance: its type
/// and its key.
pub(super) fn row<C>(view: &View<'_, C>) -> String {
    match view.key() {
        Some(key) => format!("{} {key}", view.type_name()),
        None => view.type_name().to_owned(),
    }
}

/// The present message `id` names, or `no_message`.
pub(super) fn present(
    view: &View<'_, CommentsConfig>,
    messages: &[Message],
    id: i64,
) -> Hook<Message> {
    messages
        .iter()
        .find(|message| message.id == id)
        .cloned()
        .ok_or_else(|| no_message(view, id))
}

/// `no_message` for an id that names nothing present.
pub(super) fn no_message(view: &View<'_, CommentsConfig>, id: i64) -> Stop {
    veto(
        view,
        "no_message",
        format!("{} has no message {id}", row(view)),
        json!({ "message": id }),
    )
}

/// `deleted` for a message that is.
pub(super) fn deleted(view: &View<'_, CommentsConfig>, id: i64) -> Stop {
    veto(
        view,
        "deleted",
        format!("message {id} of {} is deleted", row(view)),
        json!({ "message": id }),
    )
}

/// `not_a_thread` for a reply named as a thread.
pub(super) fn not_a_thread(view: &View<'_, CommentsConfig>, reply: &Message) -> Stop {
    let thread = reply.thread.unwrap_or_default();
    veto(
        view,
        "not_a_thread",
        format!(
            "message {} of {} is a reply in thread {thread}, not a thread",
            reply.id,
            row(view)
        ),
        json!({ "message": reply.id, "thread": thread }),
    )
}

/// `composing` for a composing message an operation names, or a post's or
/// a begin's thread or quotes.
pub(super) fn composing(view: &View<'_, CommentsConfig>, id: i64) -> Stop {
    veto(
        view,
        "composing",
        format!(
            "message {id} of {} is composing: it is being written, and stands once finished",
            row(view)
        ),
        json!({ "message": id }),
    )
}

/// `not_composing` for an appendDraft, a finish or an interrupt of a
/// message that is not composing.
pub(super) fn not_composing(view: &View<'_, CommentsConfig>, message: &Message) -> Stop {
    veto(
        view,
        "not_composing",
        format!(
            "message {} of {} is {}, not composing",
            message.id,
            row(view),
            message.state
        ),
        json!({ "message": message.id, "state": message.state }),
    )
}

/// The caller's for: the subject it acts for, when it acts for one other
/// than itself (D77's primitive 4).
pub(super) fn for_of<'a>(view: &View<'a, CommentsConfig>) -> Option<&'a str> {
    let caller = view.caller();
    caller
        .acting_for
        .as_deref()
        .filter(|subject| *subject != caller.subject)
}

/// The caller's via: the channel kind it came over (D157, #606), when its
/// host names one.
pub(super) fn via_of<'a>(view: &View<'a, CommentsConfig>) -> Option<&'a str> {
    view.caller()
        .channel
        .as_deref()
        .filter(|channel| !channel.is_empty())
}

/// `not_author` for a caller who is not the message's author.
pub(super) fn not_author(view: &View<'_, CommentsConfig>, message: &Message) -> Stop {
    veto(
        view,
        "not_author",
        format!(
            "{} is not the author of message {} of {}; {} is",
            view.caller().subject,
            message.id,
            row(view),
            message.author
        ),
        json!({ "message": message.id, "author": message.author }),
    )
}

/// The body a post or an edit gives is at most `maxBodyLength` code points.
pub(super) fn check_body(
    view: &View<'_, CommentsConfig>,
    operation: &str,
    body: Option<&str>,
) -> Hook<()> {
    let max = view.config().max_body_length;
    match body {
        Some(body) if body.chars().count() as i64 > max => Err(invalid(
            view,
            operation,
            "/body",
            format!("must NOT have more than {max} characters"),
        )),
        _ => Ok(()),
    }
}

/// A block's own checks, the first block by index that fails its kind's
/// (batch 5's order): an action's `data` at most 8192 bytes of compact
/// JSON; a code block's lines in order, then its path relative; and an
/// attachment's https uri with a host.
pub(super) fn check_block_shapes(
    view: &View<'_, CommentsConfig>,
    operation: &str,
    blocks: Option<&Value>,
) -> Hook<()> {
    for (index, block) in blocks_of(blocks).iter().enumerate() {
        match block.get("kind").and_then(Value::as_str) {
            Some("action") => {
                let bytes = block.get("data").map_or(0, |data| {
                    serde_json::to_vec(data).map_or(0, |text| text.len())
                });
                if bytes > MAX_ACTION_DATA {
                    return Err(invalid(
                        view,
                        operation,
                        format!("/blocks/{index}/data"),
                        format!("is more than {MAX_ACTION_DATA} bytes of JSON"),
                    ));
                }
            }
            Some("code") => {
                let lines: Vec<i64> = block
                    .get("lines")
                    .and_then(Value::as_array)
                    .map(|lines| lines.iter().filter_map(whole).collect())
                    .unwrap_or_default();
                if let [first, last] = lines.as_slice() {
                    if first > last {
                        return Err(invalid(
                            view,
                            operation,
                            format!("/blocks/{index}/lines"),
                            "must have its first line at most its last",
                        ));
                    }
                }
                let path = block
                    .get("path")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                if !is_relative_path(path) {
                    return Err(invalid(
                        view,
                        operation,
                        format!("/blocks/{index}/path"),
                        "must be a relative path: no leading or trailing /, no \\, and no empty, . or .. part",
                    ));
                }
            }
            Some("attachment") => {
                let uri = block.get("uri").and_then(Value::as_str).unwrap_or_default();
                if let Some(rest) = uri.strip_prefix("https:") {
                    let host = rest
                        .strip_prefix("//")
                        .and_then(|after| after.chars().next())
                        .is_some_and(|first| !matches!(first, '/' | '?' | '#'));
                    if !host {
                        return Err(invalid(
                            view,
                            operation,
                            format!("/blocks/{index}/uri"),
                            "must be an https URL with a host",
                        ));
                    }
                }
            }
            _ => {}
        }
    }
    Ok(())
}

// is_relative_path is whether a code block's path is relative: no leading
// or trailing /, no \, and no part, split at /, that is empty, . or ..
fn is_relative_path(path: &str) -> bool {
    !path.starts_with('/')
        && !path.ends_with('/')
        && !path.contains('\\')
        && path
            .split('/')
            .all(|part| !part.is_empty() && part != "." && part != "..")
}

/// The blocks a post or an edit gives: each kind one the config allows,
/// then each ref block's row one the database holds. A ref block whose
/// schema and id one of `before` holds is not checked again, so an edit
/// that moves an action's status does not fail on a ref whose row has
/// since gone.
pub(super) fn check_blocks(
    view: &View<'_, CommentsConfig>,
    blocks: Option<&Value>,
    before: Option<&Value>,
) -> Hook<()> {
    let blocks = blocks_of(blocks);
    if let Some(allowed) = &view.config().blocks {
        for (index, block) in blocks.iter().enumerate() {
            let kind = block
                .get("kind")
                .and_then(Value::as_str)
                .unwrap_or_default();
            if !allowed.iter().any(|one| one == kind) {
                let allows = if allowed.is_empty() {
                    "none".to_owned()
                } else {
                    allowed.join(", ")
                };
                return Err(veto(
                    view,
                    "block_not_allowed",
                    format!(
                        "block {index} is a {kind} block, which Comments on {} does not allow (it allows {allows})",
                        view.type_name()
                    ),
                    json!({ "block": index, "kind": kind, "allowed": allowed }),
                ));
            }
        }
    }
    let held = blocks_of(before);
    for (index, block) in blocks.iter().enumerate() {
        if block.get("kind").and_then(Value::as_str) != Some("attachment")
            || held.iter().any(|old| old == block)
        {
            continue;
        }
        check_attachment(view, index, block)?;
    }
    for (index, block) in blocks.iter().enumerate() {
        if block.get("kind").and_then(Value::as_str) != Some("ref") {
            continue;
        }
        let schema = block
            .get("schema")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let id = block.get("id").and_then(Value::as_str).unwrap_or_default();
        let known = held.iter().any(|old| {
            old.get("kind").and_then(Value::as_str) == Some("ref")
                && old.get("schema").and_then(Value::as_str) == Some(schema)
                && old.get("id").and_then(Value::as_str) == Some(id)
        });
        if known || held_row(view, schema, id)? {
            continue;
        }
        return Err(veto(
            view,
            "ref_not_found",
            format!(
                "block {index} refers to {schema} {id}, which is not an instance {} may read",
                view.caller().subject
            ),
            json!({ "block": index, "schema": schema, "id": id }),
        ));
    }
    Ok(())
}

// check_attachment holds an attachment block to the config's attachments,
// in the contract's order (batch 5): its uri's scheme, its media type, then
// its size, the first that fails the veto attachment_not_allowed.
fn check_attachment(view: &View<'_, CommentsConfig>, index: usize, block: &Value) -> Hook<()> {
    let allowed = &view.config().attachments;
    let refuse = |member: &str, value: Value, allows: Value, reason: String| {
        Err(veto(
            view,
            "attachment_not_allowed",
            reason,
            json!({ "block": index, "member": member, "value": value, "allowed": allows }),
        ))
    };
    let uri = block.get("uri").and_then(Value::as_str).unwrap_or_default();
    let scheme = uri.split(':').next().unwrap_or_default();
    if !allowed.schemes.iter().any(|one| one == scheme) {
        return refuse(
            "uri",
            json!(scheme),
            json!(allowed.schemes),
            format!(
                "block {index} is a file at a {scheme} URI, and Comments on {} allows {}",
                view.type_name(),
                allowed.schemes.join(", ")
            ),
        );
    }
    let media_type = block
        .get("mediaType")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if let Some(types) = &allowed.media_types {
        let family = media_type.split('/').next().unwrap_or_default();
        let held = types.iter().any(|one| {
            one == media_type || one.strip_suffix("/*").is_some_and(|range| range == family)
        });
        if !held {
            return refuse(
                "mediaType",
                json!(media_type),
                json!(types),
                format!(
                    "block {index} is a file of type {media_type}, and Comments on {} allows {}",
                    view.type_name(),
                    types.join(", ")
                ),
            );
        }
    }
    if let (Some(max), Some(bytes)) = (allowed.max_bytes, block.get("bytes").and_then(whole)) {
        if bytes > max {
            return refuse(
                "bytes",
                json!(bytes),
                json!(max),
                format!(
                    "block {index} is a file of {bytes} bytes, and Comments on {} allows at most {max}",
                    view.type_name()
                ),
            );
        }
    }
    Ok(())
}

// held_row is whether the database holds a row of the type by the key: the
// type is one of the database's, and the row is there. Whether the caller
// may read it is the route's, as a table's access policy is (D103).
fn held_row(view: &View<'_, CommentsConfig>, schema: &str, id: &str) -> Hook<bool> {
    if view.read_spec(schema)?.is_none() {
        return Ok(false);
    }
    Ok(view.row_of(schema, id)?.is_some())
}

/// A `blocks` parameter's blocks, none when it is absent.
pub(super) fn blocks_of(blocks: Option<&Value>) -> &[Value] {
    blocks
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default()
}

/// A whole number a schema let through: 2 or 2.0.
pub(super) fn whole(value: &Value) -> Option<i64> {
    let number = value.as_number()?;
    if let Some(whole) = number.as_i64() {
        return Some(whole);
    }
    number
        .as_f64()
        .filter(|n| n.is_finite() && n.fract() == 0.0)
        .map(|n| n as i64)
}

/// An object of the given members, in order, for a result.
pub(super) fn object(members: Vec<(&str, Value)>) -> Value {
    let mut out = Map::new();
    for (name, value) in members {
        out.insert(name.to_owned(), value);
    }
    Value::Object(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_code_blocks_path_is_relative() {
        for path in ["src/orders.rs", "a", "a/b.c/d"] {
            assert!(is_relative_path(path), "{path}");
        }
        for path in ["/src", "src/", "a//b", "a/./b", "../a", "a\\b", ""] {
            assert!(!is_relative_path(path), "{path}");
        }
    }
}
